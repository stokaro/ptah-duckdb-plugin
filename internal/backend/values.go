package backend

import (
	"context"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
)

// NormalizeValues normalizes the table operations the provider planned, the
// only values it declares; every other value is refused.
func (*Provider) NormalizeValues(ctx context.Context, request *protocol.NormalizeValuesRequest) (*protocol.NormalizedValues, error) {
	result := &protocol.NormalizedValues{Complete: true}
	for _, input := range request.GetValues() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		if input.GetValue().GetKind() != tableop.Kind {
			return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the provider normalizes its table operations only")
		}
		normalized, err := tableop.Normalize(input)
		if err != nil {
			return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, err.Error())
		}
		result.Values = append(result.Values, normalized)
	}
	return result, nil
}
