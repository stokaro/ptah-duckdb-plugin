package backend

import (
	"context"
	"errors"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"
	providerruntime "ptah.run/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/commonobject"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
	"github.com/stokaro/ptah-duckdb-plugin/internal/typeloss"
)

// Render writes the SQL statement of each table operation the provider
// planned and of each change of a view, sequence, comment or schema, in the
// order the changes arrive, in either form the host asks for: every change is
// one statement, so the executed units and a migration file hold the same
// text. It connects to nothing. A change it cannot write refuses the whole
// batch, so the host never receives part of a plan.
//
// For a host that lists render.omissions, the result also names each column
// whose declared type states what DuckDB does not keep, such as the length of
// a VARCHAR(255) that DuckDB creates as VARCHAR: the statement writes DuckDB's
// type, and the omission says what the declaration lost (see typeloss).
func (*Provider) Render(ctx context.Context, request *protocol.RenderRequest) (*protocol.RenderResult, error) {
	result := &protocol.RenderResult{Complete: true}
	reportOmissions := slices.Contains(request.GetTargetFeatures(), provider.RenderOmissionsFeature)
	for _, change := range request.GetChanges() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		statement, err := renderChange(change)
		if err != nil {
			return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, err.Error())
		}
		// The host splits a migration file at semicolons, so every unit ends
		// with one.
		statement = strings.TrimRight(strings.TrimSpace(statement), ";") + ";"
		result.Units = append(result.Units, &protocol.Unit{ChangeId: change.GetId(), Body: &protocol.Unit_Sql{Sql: statement}})
		if reportOmissions {
			result.Omissions = appendOmissions(result.GetOmissions(), change)
		}
	}
	return result, nil
}

// appendOmissions adds the omissions of one change's table operation to
// omissions, each once: a column whose declared type states what DuckDB does
// not keep. Any other change omits nothing.
func appendOmissions(omissions []*protocol.RenderOmission, change *protocol.Change) []*protocol.RenderOmission {
	value := change.GetNative().GetValue().GetValue()
	if value == nil {
		return omissions
	}
	operation, err := tableop.Decode(value.GetPayload())
	if err != nil {
		return omissions
	}
	for _, declared := range operation.DeclaredTypes() {
		loss, lost := typeloss.Of(declared.Type)
		if !lost {
			continue
		}
		omission := &protocol.RenderOmission{Reason: "unsupported", Kind: "column", Name: value.GetOwner().GetName() + "." + declared.Column,
			Property: loss.Property, Detail: loss.Detail, Remedy: loss.Remedy}
		if !slices.ContainsFunc(omissions, func(earlier *protocol.RenderOmission) bool { return proto.Equal(earlier, omission) }) {
			omissions = append(omissions, omission)
		}
	}
	return omissions
}

// renderChange writes the statement of one change.
func renderChange(change *protocol.Change) (string, error) {
	if common := change.GetCommonObject(); common != nil {
		return commonobject.Render(common, defaultSchema)
	}
	native := change.GetNative()
	if native == nil {
		return "", errors.New("the provider renders the table operations it plans and changes of views, sequences, comments and schemas, and nothing else")
	}
	owner := native.GetValue().GetValue().GetOwner()
	switch native.GetRole() {
	case protocol.NativeOperation_STATEMENT:
		if native.GetParent() != nil {
			return "", errors.New("a standalone table operation has no parent")
		}
	case protocol.NativeOperation_ALTER_TABLE:
		// The host names a table in the default schema without its schema.
		parent := native.GetParent()
		schema := parent.GetSchema()
		if schema == "" {
			schema = defaultSchema
		}
		if parent.GetName() != owner.GetName() || schema != owner.GetSchema() || parent.GetKind() != "table" {
			return "", errors.New("a table operation names its own table as its parent")
		}
	default:
		return "", errors.New("a table operation is a statement or an ALTER TABLE")
	}
	return tableop.Statement(native.GetValue())
}
