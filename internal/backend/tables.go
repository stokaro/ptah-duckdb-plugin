package backend

import (
	"context"
	"slices"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// PrepareTables states which declared columns are key columns: the table's
// key list, then each column marked primary, in key order. It reads no
// database and changes nothing else of a declaration.
func (*Provider) PrepareTables(ctx context.Context, request *protocol.PrepareTablesRequest) (*protocol.PreparedTables, error) {
	result := &protocol.PreparedTables{Complete: true}
	for _, table := range request.GetTables() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		result.Tables = append(result.Tables, &protocol.PreparedTable{Subject: proto.CloneOf(table.GetSubject()), ColumnPrimaryKeysPrepared: true,
			ColumnPrimaryKeys: tableplan.PrimaryKey(table.GetDesired())})
	}
	return result, nil
}

// ProjectTableCreations predicts what a read reports for each declared table
// once CREATE TABLE has run: every column in order as DuckDB holds it, which
// the scratch database answers. A declaration DuckDB refuses fails the batch
// with the reason.
func (p *Provider) ProjectTableCreations(ctx context.Context, request *protocol.ProjectTableCreationsRequest) (*protocol.ProjectedTableCreations, error) {
	result := &protocol.ProjectedTableCreations{Complete: true}
	for _, input := range request.GetTables() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		table := input.GetDeclaration()
		held, err := p.hold(ctx, table)
		if err != nil {
			if ctx.Err() != nil {
				return nil, status.FromContextError(ctx.Err()).Err()
			}
			return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "table "+quoted(table.GetName())+": "+err.Error())
		}
		keys := tableplan.PrimaryKey(table)
		output := &protocol.ProjectedTableCreation{Subject: proto.CloneOf(input.GetSubject()), ColumnPrimaryKeysPrepared: true, ColumnPrimaryKeys: keys}
		for i, column := range held.Columns {
			output.Columns = append(output.Columns, observedColumn(column, i+1, slices.Contains(keys, column.Name)))
		}
		result.Tables = append(result.Tables, output)
	}
	return result, nil
}
