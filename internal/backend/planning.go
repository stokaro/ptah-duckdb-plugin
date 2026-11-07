package backend

import (
	"context"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// PlanFeatures plans the tables the host creates, changes and drops; see
// tableplan. It reads no database: how DuckDB holds a declaration comes from
// the scratch database. A change DuckDB cannot make is a completed refusal
// that names the table, and a request the service cannot read is an error.
func (p *Provider) PlanFeatures(ctx context.Context, request *protocol.PlanFeaturesRequest) (*protocol.PlannedFeatures, error) {
	declared := tableplan.Declared{Hold: func(table *protocol.DeclaredTable) (catalog.Table, error) { return p.hold(ctx, table) }}
	result, err := tableplan.Plan(ctx, request, declared)
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, err.Error())
	}
	return result, nil
}
