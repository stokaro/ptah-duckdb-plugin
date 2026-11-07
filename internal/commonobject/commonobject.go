// Package commonobject carries the DuckDB objects beyond tables that travel
// on Ptah's common provider wire: views, sequences, comments and schemas. The
// host compares and orders them for every engine; this package says what
// DuckDB holds of each, writes a declaration in DuckDB's spelling, and writes
// the statement each change is made with.
//
// DuckDB has no role, grant or materialized view, takes no comment on a
// schema, and alters no sequence in place, so each is refused by name.
package commonobject

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// SequenceType is the type of every DuckDB sequence: CREATE SEQUENCE takes
// no AS clause, and a sequence counts in 64-bit integers.
const SequenceType = "BIGINT"

// Refusal is a declared object DuckDB cannot hold: its kind, its name, and
// why.
type Refusal struct {
	Kind, Object, Reason string
}

// Refusals lists every declared object the provider refuses, in declaration
// order.
func Refusals(declared *protocol.DeclaredCommonObjects, features []string) []Refusal {
	var refusals []Refusal
	for _, view := range declared.GetViews() {
		if reason := tableplan.SchemaRefusal("view", view.GetSchema(), view.GetName(), features); reason != "" {
			refusals = append(refusals, Refusal{Kind: "view", Object: view.GetName(), Reason: reason})
		}
		if reason := viewRefusal(view); reason != "" {
			refusals = append(refusals, Refusal{Kind: "view", Object: view.GetName(), Reason: reason})
		}
	}
	for _, sequence := range declared.GetSequences() {
		if reason := tableplan.SchemaRefusal("sequence", sequence.GetSchema(), sequence.GetName(), features); reason != "" {
			refusals = append(refusals, Refusal{Kind: "sequence", Object: sequence.GetName(), Reason: reason})
		}
		if reason := sequenceRefusal(sequence); reason != "" {
			refusals = append(refusals, Refusal{Kind: "sequence", Object: sequence.GetName(), Reason: reason})
		}
	}
	for _, role := range declared.GetRoles() {
		refusals = append(refusals, Refusal{Kind: "role", Object: role.GetName(), Reason: fmt.Sprintf("role %q: DuckDB has no roles", role.GetName())})
	}
	if len(declared.GetGrants()) > 0 {
		refusals = append(refusals, Refusal{Kind: "grant", Object: declared.GetGrants()[0].GetRole(), Reason: "DuckDB has no grants"})
	}
	return refusals
}

func viewRefusal(view *protocol.SQLView) string {
	switch {
	case view.GetMaterialized():
		return fmt.Sprintf("view %q is materialized; DuckDB has no materialized views", view.GetName())
	case view.GetWithCheck():
		return fmt.Sprintf("view %q has WITH CHECK OPTION, which DuckDB does not have", view.GetName())
	case len(view.GetAttributes()) > 0:
		return fmt.Sprintf("view %q has attributes, which a DuckDB view has none of", view.GetName())
	}
	return ""
}

func sequenceRefusal(sequence *protocol.SQLSequence) string {
	switch {
	case !bigint(sequence.GetAsType()):
		return fmt.Sprintf("sequence %q is %s; a DuckDB sequence counts in BIGINT only", sequence.GetName(), sequence.GetAsType())
	case sequence.Cache != nil && sequence.GetCache() != 1:
		return fmt.Sprintf("sequence %q has a cache; a DuckDB sequence has none", sequence.GetName())
	case sequence.GetOwnedBy() != "":
		return fmt.Sprintf("sequence %q is owned by a column; a DuckDB sequence has no owner", sequence.GetName())
	case sequence.Increment != nil && sequence.GetIncrement() == 0:
		return fmt.Sprintf("sequence %q has an increment of zero", sequence.GetName())
	}
	return ""
}

// bigint reports a sequence type that is DuckDB's only sequence type.
func bigint(declared string) bool {
	switch strings.ToUpper(strings.TrimSpace(declared)) {
	case "", "BIGINT", "INT8", "LONG":
		return true
	}
	return false
}

// Normalizer writes a view's query in the spelling DuckDB stores it in; the
// backend answers it from a scratch database.
type Normalizer func(context.Context, string) (string, error)

// Prepare returns a declaration in DuckDB's spelling: each view's query as
// DuckDB stores it, and each sequence with DuckDB's default for every setting
// it leaves out. No entry changes its identity.
func Prepare(ctx context.Context, declared *protocol.DeclaredCommonObjects, normalize Normalizer) (*protocol.DeclaredCommonObjects, error) {
	prepared := proto.CloneOf(declared)
	if prepared == nil {
		return &protocol.DeclaredCommonObjects{}, nil
	}
	for _, view := range prepared.GetViews() {
		query, err := normalize(ctx, view.GetBody())
		if err != nil {
			return nil, fmt.Errorf("view %q: %w", view.GetName(), err)
		}
		view.Body = query
	}
	for _, sequence := range prepared.GetSequences() {
		Defaults(sequence)
	}
	return prepared, nil
}

// Defaults fills the settings a declared sequence leaves out with DuckDB's
// defaults, measured on the embedded DuckDB: an increment of 1; for a rising
// sequence a minimum of 1 and a maximum of 2^63-1, for a falling one a
// minimum of -2^63 and a maximum of -1; and a start at the minimum of a rising
// sequence or the maximum of a falling one.
func Defaults(sequence *protocol.SQLSequence) {
	if bigint(sequence.GetAsType()) {
		sequence.AsType = SequenceType
	}
	if sequence.Increment == nil {
		sequence.Increment = new(int64(1))
	}
	rising := sequence.GetIncrement() > 0
	if sequence.MinValue == nil {
		sequence.MinValue = new(int64(1))
		if !rising {
			sequence.MinValue = new(int64(math.MinInt64))
		}
	}
	if sequence.MaxValue == nil {
		sequence.MaxValue = new(int64(math.MaxInt64))
		if !rising {
			sequence.MaxValue = new(int64(-1))
		}
	}
	if sequence.Start == nil {
		sequence.Start = new(sequence.GetMinValue())
		if !rising {
			sequence.Start = new(sequence.GetMaxValue())
		}
	}
	sequence.Cache = nil
}

// Render writes the statement of one change; defaultSchema names an object
// that names no schema.
func Render(change *protocol.CommonObjectChange, defaultSchema string) (string, error) {
	schemaOf := func(schema string) string {
		if schema == "" {
			return defaultSchema
		}
		return schema
	}
	switch operation := change.GetOperation().(type) {
	case *protocol.CommonObjectChange_CreateView:
		view := operation.CreateView.GetView()
		if reason := viewRefusal(view); reason != "" {
			return "", errors.New(reason)
		}
		if operation.CreateView.GetReplace() {
			return ddl.ReplaceView(schemaOf(view.GetSchema()), view.GetName(), view.GetBody())
		}
		return ddl.CreateView(schemaOf(view.GetSchema()), view.GetName(), view.GetBody())
	case *protocol.CommonObjectChange_DropView:
		return ddl.DropView(schemaOf(operation.DropView.GetSchema()), operation.DropView.GetName()), nil
	case *protocol.CommonObjectChange_CreateSequence:
		sequence := operation.CreateSequence
		if reason := sequenceRefusal(sequence); reason != "" {
			return "", errors.New(reason)
		}
		return ddl.CreateSequence(schemaOf(sequence.GetSchema()), sequence.GetName(), ddl.Sequence{Start: sequence.Start, Increment: sequence.Increment,
			Min: sequence.MinValue, Max: sequence.MaxValue, Cycle: sequence.GetCycle()})
	case *protocol.CommonObjectChange_AlterSequence:
		return "", fmt.Errorf("sequence %q: DuckDB alters no sequence in place, and replacing it would restart its counter; drop it and create it explicitly",
			operation.AlterSequence.GetSequence().GetName())
	case *protocol.CommonObjectChange_DropSequence:
		return ddl.DropSequence(schemaOf(operation.DropSequence.GetSchema()), operation.DropSequence.GetName()), nil
	case *protocol.CommonObjectChange_SetComment:
		return renderComment(operation.SetComment, schemaOf)
	case *protocol.CommonObjectChange_CreateSchema:
		if operation.CreateSchema.GetComment() != "" {
			return "", fmt.Errorf("schema %q: DuckDB takes no comment on a schema", operation.CreateSchema.GetName())
		}
		return ddl.CreateSchema(operation.CreateSchema.GetName()), nil
	case *protocol.CommonObjectChange_DropSchema:
		return ddl.DropSchema(operation.DropSchema.GetName()), nil
	}
	return "", errors.New("DuckDB has no roles, grants or memberships")
}

// commentKinds are the objects DuckDB's COMMENT ON names by their kind.
var commentKinds = map[string]string{"table": "TABLE", "view": "VIEW", "sequence": "SEQUENCE", "index": "INDEX", "column": "COLUMN"}

func renderComment(comment *protocol.ObjectComment, schemaOf func(string) string) (string, error) {
	object := comment.GetObject()
	keyword, known := commentKinds[object.GetKind()]
	if !known {
		return "", fmt.Errorf("DuckDB takes no comment on a %s", object.GetKind())
	}
	schema := schemaOf(object.GetSchema())
	target := keyword + " " + ddl.Qualified(schema, object.GetName())
	if object.GetKind() == "column" {
		if len(object.GetPath()) != 1 {
			return "", errors.New("a column's comment names the column's table")
		}
		target = keyword + " " + ddl.Qualified(schema, object.GetPath()[0]) + "." + ddl.Quote(object.GetName())
	}
	return ddl.Comment(target, comment.GetText()), nil
}

// Kinds are the kinds the duckdb engine carries.
func Kinds() []protocol.CommonObjectSupport_Kind {
	return slices.Clone(kinds)
}

var kinds = []protocol.CommonObjectSupport_Kind{
	protocol.CommonObjectSupport_VIEW, protocol.CommonObjectSupport_SEQUENCE, protocol.CommonObjectSupport_COMMENT,
	protocol.CommonObjectSupport_SCHEMA,
}
