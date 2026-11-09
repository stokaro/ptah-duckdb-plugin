package backend

import (
	"context"

	"google.golang.org/grpc/status"
	protocol "ptah.run/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/commonobject"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// ValidateSchema checks the declared tables and common objects offline and
// reports each one the provider cannot create on DuckDB, with the reason. A
// table's columns, CHECKs and key are created in the scratch database, so
// DuckDB itself answers whether it takes them.
func (p *Provider) ValidateSchema(ctx context.Context, request *protocol.ValidateSchemaRequest) (*protocol.SchemaValidation, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	result := &protocol.SchemaValidation{Complete: true}
	hold := func(table *protocol.DeclaredTable) error {
		_, err := p.hold(ctx, table)
		return err
	}
	for _, problem := range tableplan.Validate(request, hold) {
		result.Diagnostics = append(result.Diagnostics, &protocol.SchemaDiagnostic{Code: problem.Code, Kind: problem.Kind, Object: problem.Object, Message: problem.Message})
	}
	for _, refusal := range commonobject.Refusals(request.GetCommonObjects(), request.GetTargetFeatures()) {
		result.Diagnostics = append(result.Diagnostics, &protocol.SchemaDiagnostic{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: refusal.Kind,
			Object: refusal.Object, Message: refusal.Reason})
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return result, nil
}
