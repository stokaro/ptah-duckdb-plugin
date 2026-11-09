package backend

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"
	providerruntime "ptah.run/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/devclean"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
)

// ListDevObjects lists every object a cleanup of the request's scope would
// remove from the session's database (see devclean). The host decides that
// the database is a dev database.
func (p *Provider) ListDevObjects(ctx context.Context, request *protocol.ListDevObjectsRequest) (*protocol.DevObjects, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	inventory, err := devclean.Read(ctx, current.handle.Conn(), request.GetSchemas())
	if err != nil {
		return nil, callFailed(ctx, "the provider could not read the DuckDB catalog")
	}
	result := &protocol.DevObjects{Complete: true}
	for _, object := range inventory.Objects {
		result.Objects = append(result.Objects, &protocol.DevObject{Object: current.devReference(object)})
	}
	return result, nil
}

// CleanDevDatabase removes every object of the request's scope in one
// transaction: DuckDB rolls it back whole when a statement fails, so a
// cleanup removes everything or nothing. A refusal names the object whose
// removal DuckDB refused.
func (p *Provider) CleanDevDatabase(ctx context.Context, request *protocol.CleanDevRequest) (*protocol.DevCleanup, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	inventory, err := devclean.Read(ctx, current.handle.Conn(), request.GetSchemas())
	if err != nil {
		return nil, callFailed(ctx, "the provider could not read the DuckDB catalog")
	}
	result := &protocol.DevCleanup{RequestId: request.GetRequestId(), Outcome: protocol.DevCleanup_CLEANED}
	if len(inventory.Steps) == 0 {
		return result, nil
	}
	if current.handle.Access() == instances.ReadOnly {
		result.Outcome = protocol.DevCleanup_REFUSED
		for _, step := range inventory.Steps {
			result.Blocking = append(result.Blocking, &protocol.DevObject{Object: current.devReference(step.Object),
				Refusal: "the session opened the database read-only, so nothing in it can be removed"})
		}
		return result, nil
	}
	var refused *devclean.Step
	var refusal error
	err = runInTransaction(ctx, current.handle.Conn(), func(tx *sql.Tx) error {
		for i, step := range inventory.Steps {
			if _, err := tx.ExecContext(ctx, step.Statement); err != nil {
				refused, refusal = &inventory.Steps[i], err
				return err
			}
		}
		return nil
	})
	switch {
	case err == nil:
		return result, nil
	case refused != nil && errors.Is(err, errNotCommitted):
		result.Outcome = protocol.DevCleanup_REFUSED
		result.Blocking = []*protocol.DevObject{{Object: current.devReference(refused.Object), Refusal: "DuckDB refused to remove it with " + refusalKind(refusal)}}
		return result, nil
	case ctx.Err() != nil:
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	result.Outcome, result.Message = protocol.DevCleanup_UNKNOWN, "DuckDB refused the commit of the cleanup, so the database may hold what it held"
	return result, nil
}

// devReference names an object of the session's database.
func (s *session) devReference(object devclean.Object) *protocol.Reference {
	return &protocol.Reference{Engine: engineID, Database: s.database, Schema: object.Schema, Kind: object.Kind, Name: object.Name}
}

// GuardReplay judges each statement of a migration replay offline and
// refuses one whose effects a cleanup of the dev database would not remove
// (see devclean.Replay). The scope a host grants changes nothing: DuckDB's
// database is one file or one in-memory database, and every refused effect
// reaches beyond it, or into the session the replay keeps using.
func (*Provider) GuardReplay(ctx context.Context, request *protocol.GuardReplayRequest) (*protocol.GuardedReplay, error) {
	description := provider.NegotiatedDescription(Description(), provider.HostFeatures(ctx))
	if err := provider.ValidateGuardReplayRequest(description, request); err != nil {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the replay guard request is not one the provider can answer")
	}
	result := &protocol.GuardedReplay{Verdicts: make([]*protocol.ReplayVerdict, 0, len(request.GetStatements()))}
	for _, statement := range request.GetStatements() {
		operation := devclean.Replay(statement)
		result.Verdicts = append(result.Verdicts, &protocol.ReplayVerdict{Refused: operation != "", Operation: operation})
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return result, nil
}
