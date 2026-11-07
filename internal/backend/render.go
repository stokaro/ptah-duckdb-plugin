package backend

import (
	"context"
	"errors"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/commonobject"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
)

// Render writes the SQL statement of each table operation the provider
// planned and of each change of a view, sequence, comment or schema, in the
// order the changes arrive, in either form the host asks for: every change is
// one statement, so the executed units and a migration file hold the same
// text. It connects to nothing. A change it cannot write refuses the whole
// batch, so the host never receives part of a plan.
func (*Provider) Render(ctx context.Context, request *protocol.RenderRequest) (*protocol.RenderResult, error) {
	result := &protocol.RenderResult{Complete: true}
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
	}
	return result, nil
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
