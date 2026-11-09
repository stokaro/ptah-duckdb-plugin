package backend

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"
	providerruntime "ptah.run/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/migrationunits"
)

// SplitMigration splits a migration body offline into the statements DuckDB
// runs it as, each in a transaction with its checkpoint, or refuses the body
// whole (see internal/migrationunits), and says of each check assertion
// whether it only reads rows.
func (*Provider) SplitMigration(ctx context.Context, request *protocol.SplitMigrationRequest) (*protocol.MigrationSplit, error) {
	description := provider.NegotiatedDescription(Description(), provider.HostFeatures(ctx))
	if err := provider.ValidateSplitMigrationRequest(description, request); err != nil {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the migration split request is not one the provider can answer")
	}
	result := &protocol.MigrationSplit{}
	result.Units, result.Refusal = migrationunits.Split(request.GetBody())
	for _, assertion := range request.GetAssertions() {
		verdict := &protocol.AssertionVerdict{ReadOnly: true}
		if readOnly, reason := migrationunits.ReadOnly(assertion); !readOnly {
			verdict = &protocol.AssertionVerdict{Reason: reason}
		}
		result.Assertions = append(result.Assertions, verdict)
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err := provider.ValidateMigrationSplitReply(description, request, result); err != nil {
		return nil, providerruntime.ErrorWithReason(codes.Internal, "the provider split the migration into units the contract does not accept")
	}
	return result, nil
}
