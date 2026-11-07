package backend

import (
	"context"
	"errors"
	"slices"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/commonobject"
	"github.com/stokaro/ptah-duckdb-plugin/internal/scratch"
)

// commonObjectKinds are the kinds beyond tables the duckdb engine carries:
// views, sequences, schemas, and the comments of tables, columns, indexes,
// views and sequences. DuckDB has no role, grant or materialized view, and
// refuses a comment on a schema.
func commonObjectKinds() []protocol.CommonObjectSupport_Kind { return commonobject.Kinds() }

// observedCommonObjects is what a read found beyond tables in the schemas it
// covers: every view and sequence, one comment for each object that has one,
// and, when the host reads namespaces, every schema. Each kind the host
// negotiated is read completely.
func observedCommonObjects(database string, reads []catalog.Schema, kinds []protocol.CommonObjectSupport_Kind) *protocol.ObservedCommonObjects {
	result := &protocol.ObservedCommonObjects{}
	for _, kind := range kinds {
		result.Coverage = append(result.Coverage, &protocol.CommonObjectCoverage{Kind: kind, Knowledge: &protocol.ModelKnowledge{State: protocol.ModelKnowledge_COMPLETE}})
	}
	comment := func(object *protocol.Reference, text string) {
		if text != "" {
			result.Comments = append(result.Comments, &protocol.ObjectComment{Object: object, Text: text})
		}
	}
	for _, read := range reads {
		if slices.Contains(kinds, protocol.CommonObjectSupport_SCHEMA) {
			result.Namespaces = append(result.Namespaces, &protocol.ObservedNamespace{
				Identity: &protocol.Reference{Engine: engineID, Database: database, Kind: provider.NamespaceKind, Name: read.Name},
			})
		}
		for _, table := range read.Tables {
			comment(reference(database, read.Name, "table", table.Name), table.Comment)
			for _, column := range table.Columns {
				comment(member(database, read.Name, "column", table.Name, column.Name), column.Comment)
			}
			for _, index := range table.Indexes {
				comment(member(database, read.Name, "index", table.Name, index.Name), index.Comment)
			}
		}
		for _, view := range read.Views {
			result.Views = append(result.Views, &protocol.ObservedView{Identity: reference(database, read.Name, "view", view.Name), Body: view.Query})
			comment(reference(database, read.Name, "view", view.Name), view.Comment)
		}
		for _, sequence := range read.Sequences {
			result.Sequences = append(result.Sequences, &protocol.ObservedSequence{
				Identity: reference(database, read.Name, "sequence", sequence.Name), DataType: commonobject.SequenceType,
				Start: new(sequence.Start), Increment: new(sequence.Increment), MinValue: new(sequence.Min), MaxValue: new(sequence.Max), Cycle: sequence.Cycle,
			})
			comment(reference(database, read.Name, "sequence", sequence.Name), sequence.Comment)
		}
	}
	return result
}

// member names a column or an index, with its table as the only path
// component.
func member(database, schema, kind, table, name string) *protocol.Reference {
	member := reference(database, schema, kind, name)
	member.Path = []string{table}
	return member
}

// PrepareCommonObjects writes a declaration in DuckDB's spelling: each view's
// query as DuckDB stores it, which the scratch database answers by parsing
// the query and writing it back, and each sequence with DuckDB's defaults
// for what it leaves out. A query DuckDB cannot parse is refused by name.
func (p *Provider) PrepareCommonObjects(ctx context.Context, request *protocol.PrepareCommonObjectsRequest) (*protocol.PreparedCommonObjects, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	prepared, err := commonobject.Prepare(ctx, request.GetDeclared(), func(ctx context.Context, query string) (string, error) {
		written, err := p.scratch.Query(ctx, query)
		if errors.Is(err, scratch.ErrRefused) {
			return "", errors.New("DuckDB cannot parse its query as one SELECT")
		}
		return written, err
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, err.Error())
	}
	return &protocol.PreparedCommonObjects{Complete: true, Prepared: prepared}, nil
}
