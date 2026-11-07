package backend

import (
	"context"
	"database/sql"
	"slices"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
)

// transactionLimit bounds the handles one session keeps. A finished handle
// keeps its outcome until the session closes.
const transactionLimit = 1024

// transaction is one handle of a session. Its transaction runs on a
// connection of its own, taken at begin and returned at finish, so the
// session's connection keeps reading the catalog outside it.
type transaction struct {
	conn *sql.Conn
	tx   *sql.Tx
	// outcome is the handle's terminal outcome once it finished, or the
	// outcome a finish reports when the handle never opened a transaction.
	outcome protocol.TransactionResult_Outcome
	// failed is set when a statement in the transaction failed: DuckDB
	// aborts the transaction then, and it can only be rolled back.
	failed bool
	// reason says why a commit ended in another outcome.
	reason string
}

// BeginTransaction opens the transaction a caller-chosen handle names.
// DuckDB runs schema statements inside a transaction and rolls them back
// with it, so the host can run a whole plan in one. A session has at most one
// open transaction.
func (p *Provider) BeginTransaction(ctx context.Context, request *protocol.BeginTransactionRequest) (*protocol.Transaction, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	record, err := current.reserveTransaction(request.GetTransactionId())
	if err != nil {
		return nil, err
	}
	if current.active != "" {
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the session has an open transaction; finish it before beginning another")
	}
	conn, err := current.handle.NewConn(ctx)
	if err != nil {
		return nil, callFailed(ctx, "the provider could not take a connection for the transaction")
	}
	// DuckDB's transactions are snapshot isolated, which serializes writes:
	// a write that conflicts with a concurrent one fails. The transaction
	// outlives the call that begins it, and database/sql rolls a transaction
	// back when the context it began under ends, so it begins under one that
	// does not.
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		_ = conn.Close()
		return nil, callFailed(ctx, "the provider could not begin the transaction")
	}
	record.conn, record.tx = conn, tx
	current.active = request.GetTransactionId()
	return &protocol.Transaction{SessionId: request.GetSessionId(), Id: request.GetTransactionId(), Isolation: request.GetIsolation()}, nil
}

// reserveTransaction records a new handle that has not opened a transaction.
// A handle is used once. The caller holds mu.
func (s *session) reserveTransaction(id string) (*transaction, error) {
	if s.transactions[id] != nil {
		return nil, providerruntime.ErrorWithReason(codes.AlreadyExists, "the transaction ID was used before")
	}
	if len(s.transactions) >= transactionLimit {
		return nil, providerruntime.ErrorWithReason(codes.ResourceExhausted, "the session used every transaction handle it keeps; open another session")
	}
	if s.transactions == nil {
		s.transactions = make(map[string]*transaction)
	}
	record := &transaction{outcome: protocol.TransactionResult_NOT_STARTED}
	s.transactions[id] = record
	return record, nil
}

// FinishTransaction commits or rolls back a handle's transaction and reports
// the outcome; a finished handle reports its outcome again without a second
// commit or rollback. Finishing a handle that never began fences it.
func (p *Provider) FinishTransaction(ctx context.Context, request *protocol.FinishTransactionRequest) (*protocol.TransactionResult, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	record := current.transactions[request.GetTransactionId()]
	if record == nil {
		if record, err = current.reserveTransaction(request.GetTransactionId()); err != nil {
			return nil, err
		}
	}
	if record.tx != nil {
		current.finish(record, request.GetAction())
		current.closeReaders(request.GetTransactionId())
	}
	result := &protocol.TransactionResult{Outcome: record.outcome}
	if request.GetAction() == protocol.FinishTransactionRequest_COMMIT && record.outcome != protocol.TransactionResult_COMMITTED ||
		request.GetAction() == protocol.FinishTransactionRequest_ROLLBACK && record.outcome == protocol.TransactionResult_UNKNOWN {
		reason := record.reason
		if reason == "" {
			reason = "the transaction never began"
		}
		result.Message = reason
		if slices.Contains(provider.HostFeatures(ctx), provider.ErrorReasonsFeature) {
			result.Diagnostic = &protocol.ProviderDiagnostic{Reason: reason}
		}
	}
	return result, nil
}

// finish ends a handle's open transaction with action and records the
// outcome. A transaction a statement failed in is rolled back whatever the
// action. The caller holds mu.
func (s *session) finish(record *transaction, action protocol.FinishTransactionRequest_Action) {
	switch {
	case action == protocol.FinishTransactionRequest_ROLLBACK:
		record.outcome = protocol.TransactionResult_ROLLED_BACK
	case record.failed:
		record.outcome, record.reason = protocol.TransactionResult_ROLLED_BACK, "a statement in the transaction failed, so it was rolled back"
	default:
		if err := record.tx.Commit(); err != nil {
			// DuckDB refuses a commit that conflicts with another
			// transaction's write and rolls the transaction back; nothing of
			// it committed.
			record.outcome, record.reason = protocol.TransactionResult_ROLLED_BACK, "DuckDB refused the commit, and nothing of the transaction committed"
		} else {
			record.outcome = protocol.TransactionResult_COMMITTED
		}
	}
	if record.outcome != protocol.TransactionResult_COMMITTED {
		_ = record.tx.Rollback()
	}
	_ = record.conn.Close()
	record.conn, record.tx = nil, nil
	s.active = ""
}

// rollbackTransactions rolls back every open transaction of a closing
// session. The caller holds mu.
func (s *session) rollbackTransactions() {
	for _, record := range s.transactions {
		if record.tx != nil {
			s.finish(record, protocol.FinishTransactionRequest_ROLLBACK)
		}
	}
}

// openTransaction returns the open transaction id names, or an error that
// says why the handle cannot run a statement. The caller holds mu.
func (s *session) openTransaction(id string) (*transaction, error) {
	record := s.transactions[id]
	switch {
	case record == nil || record.tx == nil:
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the transaction is not open")
	case record.failed:
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "a statement in the transaction failed; roll it back")
	}
	return record, nil
}
