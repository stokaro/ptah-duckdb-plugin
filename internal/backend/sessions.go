package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"runtime"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/address"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
)

// sessionCapabilities are the host capabilities a session states, each
// measured on the embedded DuckDB. A present false is an established answer;
// a capability the provider does not name stays unknown to the host.
//
// transactional_ddl and ddl_inside_transaction are true: DuckDB accepts
// CREATE, ALTER and DROP inside an explicit transaction, and a ROLLBACK
// undoes them together.
func sessionCapabilities() map[string]bool {
	return map[string]bool{
		"transactional_ddl":      true,
		"ddl_inside_transaction": true,
	}
}

// Open opens the database the address names and returns a session bound to
// it. A relative path resolves against the provider's working directory, which
// is the directory the host was started in: the host starts the provider
// without changing it. The address travels in the request, never on a command
// line, and no reason the provider writes quotes it.
func (p *Provider) Open(ctx context.Context, request *protocol.OpenRequest) (*protocol.Session, error) {
	if request.GetEngine() != engineID {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the provider serves the duckdb engine only")
	}
	target, err := address.Parse(request.GetAddress())
	if err != nil {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, address.Reason(err))
	}
	handle, err := p.openTarget(ctx, target)
	if err != nil {
		return nil, openFailed(ctx, err)
	}
	var database, version string
	err = handle.Conn().QueryRowContext(ctx, "SELECT current_database(), library_version FROM pragma_version()").Scan(&database, &version)
	if err != nil {
		_ = handle.Close()
		return nil, callFailed(ctx, "DuckDB did not report the database and its version")
	}
	id := rand.Text()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessions == nil {
		p.sessions = make(map[string]*session)
	}
	p.sessions[id] = &session{handle: handle}
	return &protocol.Session{
		Id:            id,
		Engine:        engineID,
		Database:      database,
		DefaultSchema: defaultSchema,
		ServerVersion: version,
		Features:      negotiatedFeatures(ctx),
		Identifiers:   identifierRules(),
		Capabilities:  sessionCapabilities(),
	}, nil
}

// openTarget opens what target names in the registry.
func (p *Provider) openTarget(ctx context.Context, target address.Target) (*instances.Handle, error) {
	if target.Kind == address.Memory {
		return p.instances.OpenMemory(ctx, target.MemoryName)
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, &instances.Refusal{Code: codes.Unavailable, Reason: "the provider could not read the directory it was started in"}
	}
	path, err := address.Resolve(target, dir, runtime.GOOS)
	if err != nil {
		return nil, &instances.Refusal{Code: codes.InvalidArgument, Reason: address.Reason(err)}
	}
	access := instances.ReadWrite
	if target.ReadOnly {
		access = instances.ReadOnly
	}
	return p.instances.OpenFile(ctx, path, access)
}

// Close releases one session. Closing a session twice succeeds.
func (p *Provider) Close(_ context.Context, request *protocol.CloseRequest) error {
	p.mu.Lock()
	found := p.sessions[request.GetSessionId()]
	delete(p.sessions, request.GetSessionId())
	p.mu.Unlock()
	if found == nil {
		return nil
	}
	found.mu.Lock()
	defer found.mu.Unlock()
	if found.handle.Close() != nil {
		return providerruntime.ErrorWithReason(codes.Internal, "the session did not close cleanly")
	}
	return nil
}

// openFailed reports a failed open. A canceled open keeps the context's
// error, and a refusal of the registry its own code and reason.
func openFailed(ctx context.Context, err error) error {
	if refused, ok := errors.AsType[*instances.Refusal](err); ok {
		return providerruntime.ErrorWithReason(refused.Code, refused.Reason)
	}
	return callFailed(ctx, "DuckDB could not open the database")
}

// callFailed reports a failed database call. A canceled call keeps the
// context's error; any other failure says what failed and leaves the driver's
// text out, because it can quote a path or a value.
func callFailed(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return providerruntime.ErrorWithReason(codes.Unavailable, reason)
}
