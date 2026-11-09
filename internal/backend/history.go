package backend

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	providerruntime "ptah.run/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/history"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/ownedtable"
)

// maxSnapshotBytes bounds a history snapshot below the transport's message
// limit, with room for the reply's framing.
const maxSnapshotBytes = 3 << 20

// Diagnostic codes of a history or metadata result that did not apply.
const (
	storeLayout       = "duckdb.store-layout"
	storeMissing      = "duckdb.store-missing"
	storeRecord       = "duckdb.store-record"
	storeUnavailable  = "duckdb.store-unavailable"
	storeReadOnly     = "duckdb.store-read-only"
	storeUnconfirmed  = "duckdb.store-unconfirmed"
	storeWriteRefused = "duckdb.store-write-refused"
	storeTransaction  = "duckdb.store-transaction"
)

// PrepareHistory creates a missing revision table in the layout of the
// request's format, with the provider's mark, and checks an existing one is
// the provider's and holds exactly that layout. It never changes an existing
// table or its rows. DuckDB runs the statements that create the table in one
// transaction, so the table exists with its mark or not at all.
func (p *Provider) PrepareHistory(ctx context.Context, request *protocol.PrepareHistoryRequest) (*protocol.HistoryResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	answer := func(outcome protocol.HistoryResult_Outcome, code, message string) *protocol.HistoryResult {
		return &protocol.HistoryResult{Scope: proto.CloneOf(request.GetScope()), RequestId: request.GetRequestId(), Outcome: outcome,
			DiagnosticCode: code, Message: message}
	}
	outcome, code, message := current.prepareStore(ctx, history.StoreOf(request.GetScope()).Definition(), history.StoreOf(request.GetScope()).Statements())
	return answer(outcome, code, message), nil
}

// prepareStore creates the store definition names with statements when it
// is missing, and checks it when it exists. The caller holds mu.
func (s *session) prepareStore(ctx context.Context, definition ownedtable.Definition, statements []string) (
	outcome protocol.HistoryResult_Outcome, code, message string,
) {
	exists, err := ownedtable.Locate(ctx, s.handle.Conn(), definition)
	switch {
	case errors.Is(err, ownedtable.ErrLayout):
		return protocol.HistoryResult_REFUSED, storeLayout, "a table of the store's name exists and " + layoutReason(err)
	case err != nil:
		return protocol.HistoryResult_REFUSED, storeUnavailable, "the provider could not read the catalog"
	case exists:
		return protocol.HistoryResult_APPLIED, "", ""
	case s.handle.Access() == instances.ReadOnly:
		return protocol.HistoryResult_REFUSED, storeReadOnly, "the session opened the database read-only, so the store cannot be created"
	}
	switch err := runInTransaction(ctx, s.handle.Conn(), func(tx *sql.Tx) error {
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	}); {
	case errors.Is(err, errNotCommitted):
		return protocol.HistoryResult_REFUSED, storeWriteRefused, "DuckDB refused a statement that creates the store, and created nothing"
	case err != nil:
		return protocol.HistoryResult_UNKNOWN, storeUnconfirmed, "DuckDB did not confirm the creation of the store"
	}
	if exists, err := ownedtable.Locate(ctx, s.handle.Conn(), definition); err != nil || !exists {
		return protocol.HistoryResult_UNKNOWN, storeUnconfirmed, "the store the provider created could not be read back"
	}
	return protocol.HistoryResult_APPLIED, "", ""
}

// errNotCommitted reports a transaction that applied nothing: a statement in
// it failed, or DuckDB refused its commit.
var errNotCommitted = errors.New("the transaction applied nothing")

// runInTransaction runs fn in a transaction of its own on conn and commits
// it. It returns errNotCommitted when DuckDB refused a statement or the
// commit, both of which apply nothing, and the context's error when the call
// ended first.
func runInTransaction(ctx context.Context, conn *sql.Conn, fn func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(errNotCommitted, err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.Join(errNotCommitted, err)
	}
	if err := tx.Commit(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// DuckDB rolls back a transaction whose commit it refuses.
		return errors.Join(errNotCommitted, err)
	}
	return nil
}

// ReadHistory reads every record of a revision table, in the request's
// transaction when it names one. A missing table is a complete snapshot that
// says so; a table that is not the provider's store, a row no record stands
// for, or more records than the request takes fail the read.
func (p *Provider) ReadHistory(ctx context.Context, request *protocol.ReadHistoryRequest) (*protocol.HistorySnapshot, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	q, record, err := current.storeQuerier(request.GetScope().GetTransactionId())
	if err != nil {
		return nil, err
	}
	store := history.StoreOf(request.GetScope())
	exists, err := current.locateStore(ctx, q, store.Definition())
	if err != nil {
		return nil, err
	}
	snapshot := &protocol.HistorySnapshot{Scope: proto.CloneOf(request.GetScope()), Exists: exists, Complete: true}
	if !exists {
		return snapshot, nil
	}
	snapshot.Revisions, err = store.Read(ctx, q, request.GetMaxRevisions())
	switch {
	case errors.Is(err, history.ErrTooMany):
		return nil, providerruntime.ErrorWithReason(codes.ResourceExhausted, "the revision table holds more records than the read takes")
	case errors.Is(err, history.ErrRecord):
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the revision table holds a row no revision record stands for")
	case err != nil:
		if record != nil {
			record.failed = true
		}
		return nil, callFailed(ctx, "the provider could not read the revision table")
	}
	if proto.Size(snapshot) > maxSnapshotBytes {
		return nil, providerruntime.ErrorWithReason(codes.ResourceExhausted, "the revision table is larger than a snapshot carries")
	}
	return snapshot, nil
}

// MutateHistory compares the stored record of a version with the expected
// one and, when they are equal, writes the replacement, in one transaction:
// the request's, or one of its own that it commits. A conflict writes
// nothing. A failure before the commit wrote nothing, because DuckDB applies
// a transaction only at its commit, and a commit DuckDB refused applied
// nothing either. A request is never sent twice.
func (p *Provider) MutateHistory(ctx context.Context, request *protocol.MutateHistoryRequest) (*protocol.HistoryResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	answer := func(outcome protocol.HistoryResult_Outcome, code, message string) *protocol.HistoryResult {
		return &protocol.HistoryResult{Scope: proto.CloneOf(request.GetScope()), RequestId: request.GetRequestId(), Outcome: outcome,
			DiagnosticCode: code, Message: message}
	}
	store := history.StoreOf(request.GetScope())
	exists, err := ownedtable.Locate(ctx, current.handle.Conn(), store.Definition())
	switch {
	case errors.Is(err, ownedtable.ErrLayout):
		return answer(protocol.HistoryResult_REFUSED, storeLayout, "a table of the revision table's name exists and "+layoutReason(err)), nil
	case err != nil:
		return answer(protocol.HistoryResult_REFUSED, storeUnavailable, "the provider could not read the catalog"), nil
	case !exists:
		return answer(protocol.HistoryResult_REFUSED, storeMissing, "the revision table does not exist"), nil
	}
	if id := request.GetScope().GetTransactionId(); id != "" {
		record, err := current.openTransaction(id)
		if err != nil {
			return answer(protocol.HistoryResult_REFUSED, storeTransaction, "the transaction is not open or a statement in it failed"), nil
		}
		outcome, err := store.Mutate(ctx, record.tx, request)
		switch {
		case errors.Is(err, history.ErrRecord):
			return answer(protocol.HistoryResult_REFUSED, storeRecord, "the revision record does not fit the revision table"), nil
		case err != nil:
			// DuckDB aborted the transaction, which commits nothing.
			record.failed = true
			return answer(protocol.HistoryResult_REFUSED, storeWriteRefused, "the revision table refused the write, and the transaction can only be rolled back"), nil
		}
		return answer(outcome, "", ""), nil
	}
	outcome := protocol.HistoryResult_UNKNOWN
	err = runInTransaction(ctx, current.handle.Conn(), func(tx *sql.Tx) error {
		var err error
		if outcome, err = store.Mutate(ctx, tx, request); err == nil && outcome != protocol.HistoryResult_APPLIED {
			// A conflict writes nothing; the rollback below keeps it so.
			return errConflict
		}
		return err
	})
	switch {
	case errors.Is(err, errConflict):
		return answer(outcome, "", ""), nil
	case errors.Is(err, history.ErrRecord):
		return answer(protocol.HistoryResult_REFUSED, storeRecord, "the revision record does not fit the revision table"), nil
	case errors.Is(err, errNotCommitted):
		return answer(protocol.HistoryResult_REFUSED, storeWriteRefused, "DuckDB refused the write and applied nothing"), nil
	case err != nil:
		return answer(protocol.HistoryResult_UNKNOWN, storeUnconfirmed, "the call ended before DuckDB confirmed the commit, and it was not sent again"), nil
	}
	return answer(protocol.HistoryResult_APPLIED, "", ""), nil
}

// errConflict ends a provider transaction whose comparison found another
// record: it writes nothing.
var errConflict = errors.New("the stored record differs from the expected one")

// storeQuerier is where a store scope's reads and writes run: in the
// transaction id names when it names one, on the session's connection
// otherwise. The caller holds mu.
func (s *session) storeQuerier(id string) (ownedtable.Querier, *transaction, error) {
	if id == "" {
		return s.handle.Conn(), nil, nil
	}
	record, err := s.openTransaction(id)
	if err != nil {
		return nil, nil, err
	}
	return record.tx, record, nil
}

// locateStore reports whether a store's table exists and is the provider's,
// as an error the caller returns. The caller holds mu.
func (s *session) locateStore(ctx context.Context, q ownedtable.Querier, definition ownedtable.Definition) (bool, error) {
	exists, err := ownedtable.Locate(ctx, q, definition)
	switch {
	case errors.Is(err, ownedtable.ErrLayout):
		return false, providerruntime.ErrorWithReason(codes.FailedPrecondition, "a table of the store's name exists and "+layoutReason(err))
	case err != nil:
		return false, callFailed(ctx, "the provider could not read the catalog")
	}
	return exists, nil
}

// layoutReason says how a table differs from a store, from an
// ownedtable.LayoutError.
func layoutReason(err error) string {
	detail := "it is not the provider's"
	if layout, found := errors.AsType[*ownedtable.LayoutError](err); found {
		detail = layout.Detail
	}
	return "is not the store the provider keeps its records in: " + detail
}
