package backend

import (
	"context"
	"errors"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/scratch"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
)

// CompareChecks answers whether each declared CHECK and the CHECK a table
// holds under its name are one constraint on DuckDB. The declared expression
// is created on a scratch copy of the held table's columns and read back, so
// DuckDB writes both in its own spelling: a declared id > 0 is held as
// (id > 0).
func (p *Provider) CompareChecks(ctx context.Context, request *protocol.CompareChecksRequest) (*protocol.ComparedChecks, error) {
	result := &protocol.ComparedChecks{Complete: true}
	for _, table := range request.GetTables() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		answer := &protocol.ComparedCheckTable{Subject: proto.CloneOf(table.GetSubject())}
		for _, pair := range table.GetPairs() {
			declared, err := p.heldCheck(ctx, table.GetCurrent(), pair.GetDeclared())
			if err != nil {
				return nil, err
			}
			answer.Checks = append(answer.Checks, &protocol.ComparedCheck{Constraint: pair.GetConstraint(),
				Equivalent: declared == heldClause(table.GetCurrent(), pair.GetConstraint())})
		}
		result.Tables = append(result.Tables, answer)
	}
	return result, nil
}

// heldCheck writes a declared CHECK expression as DuckDB holds it on the
// columns of current.
func (p *Provider) heldCheck(ctx context.Context, current *protocol.ObservedTable, expression string) (string, error) {
	operation := tableop.Operation{Action: tableop.CreateTable, Checks: []string{expression}}
	for _, column := range current.GetColumns() {
		operation.Columns = append(operation.Columns, tableop.Column{Name: column.GetName(), Type: heldColumnType(column), Nullable: true})
	}
	held, err := p.scratch.Table(ctx, nil, func(schema string) (string, error) { return operation.SQL(schema, current.GetName()) })
	switch {
	case errors.Is(err, scratch.ErrRefused):
		return "", providerruntime.ErrorWithReason(codes.InvalidArgument, "DuckDB refuses a declared CHECK on the columns of table "+quoted(current.GetName()))
	case err != nil:
		if ctx.Err() != nil {
			return "", status.FromContextError(ctx.Err()).Err()
		}
		return "", providerruntime.ErrorWithReason(codes.Internal, "the provider could not read a declared CHECK back")
	case len(held.Checks) != 1:
		return "", providerruntime.ErrorWithReason(codes.Internal, "the scratch table holds another number of CHECKs than declared")
	}
	return held.Checks[0].Expression, nil
}

// heldClause is the clause of the CHECK a held table keeps under name.
func heldClause(table *protocol.ObservedTable, name string) string {
	for _, constraint := range table.GetConstraints() {
		if constraint.GetName() == name {
			return constraint.GetCheckClause()
		}
	}
	return ""
}
