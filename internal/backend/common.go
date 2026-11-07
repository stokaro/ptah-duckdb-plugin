package backend

import (
	"context"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
)

// sequenceType is the type of every DuckDB sequence: CREATE SEQUENCE takes
// no AS clause, and a sequence counts in 64-bit integers.
const sequenceType = "BIGINT"

// commonObjectKinds are the kinds beyond tables the duckdb engine carries:
// views, sequences, and the comments of tables, columns, indexes, views and
// sequences. DuckDB has no role, grant or materialized view, and refuses a
// comment on a schema.
func commonObjectKinds() []protocol.CommonObjectSupport_Kind {
	return []protocol.CommonObjectSupport_Kind{
		protocol.CommonObjectSupport_VIEW, protocol.CommonObjectSupport_SEQUENCE, protocol.CommonObjectSupport_COMMENT,
	}
}

// observedCommonObjects is what a read found beyond tables: every view and
// sequence of the schema, and one comment for each object that has one. Each
// kind is read completely.
func observedCommonObjects(database string, read catalog.Schema) *protocol.ObservedCommonObjects {
	result := &protocol.ObservedCommonObjects{}
	for _, kind := range commonObjectKinds() {
		result.Coverage = append(result.Coverage, &protocol.CommonObjectCoverage{Kind: kind, Knowledge: &protocol.ModelKnowledge{State: protocol.ModelKnowledge_COMPLETE}})
	}
	comment := func(object *protocol.Reference, text string) {
		if text != "" {
			result.Comments = append(result.Comments, &protocol.ObjectComment{Object: object, Text: text})
		}
	}
	for _, table := range read.Tables {
		comment(reference(database, "table", table.Name), table.Comment)
		for _, column := range table.Columns {
			comment(member(database, "column", table.Name, column.Name), column.Comment)
		}
		for _, index := range table.Indexes {
			comment(member(database, "index", table.Name, index.Name), index.Comment)
		}
	}
	for _, view := range read.Views {
		result.Views = append(result.Views, &protocol.ObservedView{Identity: reference(database, "view", view.Name), Body: view.Query})
		comment(reference(database, "view", view.Name), view.Comment)
	}
	for _, sequence := range read.Sequences {
		result.Sequences = append(result.Sequences, &protocol.ObservedSequence{
			Identity: reference(database, "sequence", sequence.Name), DataType: sequenceType,
			Start: new(sequence.Start), Increment: new(sequence.Increment), MinValue: new(sequence.Min), MaxValue: new(sequence.Max), Cycle: sequence.Cycle,
		})
		comment(reference(database, "sequence", sequence.Name), sequence.Comment)
	}
	return result
}

// member names a column or an index, with its table as the only path
// component.
func member(database, kind, table, name string) *protocol.Reference {
	member := reference(database, kind, name)
	member.Path = []string{table}
	return member
}

// PrepareCommonObjects refuses every declaration: the provider reads views,
// sequences and comments, and does not yet write a declared one in DuckDB's
// spelling, which the host compares with what a read reports. The SDK
// requires the service beside the common-object wire.
func (*Provider) PrepareCommonObjects(ctx context.Context, _ *protocol.PrepareCommonObjectsRequest) (*protocol.PreparedCommonObjects, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the provider does not prepare declared views, sequences or comments yet")
}
