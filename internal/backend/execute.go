package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/duckdb/duckdb-go/v2"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
)

// Execute runs the units of one request in order and reports how many of them
// DuckDB applied. It never runs a request again because of its ID: when the
// reply is lost, the host reads the database to learn what happened.
//
// Without a transaction each unit commits as it runs. A statement DuckDB
// refuses applied nothing, and the units before it stay applied. In a
// transaction the units run in it and commit with it; a unit that fails there
// aborts the transaction, which then commits nothing.
func (p *Provider) Execute(ctx context.Context, request *protocol.ExecuteRequest) (*protocol.Execution, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	units := request.GetUnits()
	statements := make([]string, len(units))
	for i, unit := range units {
		statement, ok := unit.GetBody().(*protocol.Unit_Sql)
		if !ok {
			return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the provider runs SQL units only")
		}
		statements[i] = statement.Sql
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	var run interface {
		ExecContext(context.Context, string, ...any) (interface{ RowsAffected() (int64, error) }, error)
	}
	var record *transaction
	if id := request.GetTransactionId(); id != "" {
		if record, err = current.openTransaction(id); err != nil {
			return nil, err
		}
		run = txExec{record}
	} else {
		run = connExec{current}
	}
	var applied uint32
	for _, statement := range statements {
		if err := ctx.Err(); err != nil {
			return stoppedAt(applied, fmt.Sprintf("the call ended before unit %d of %d ran", applied+1, len(units))), nil
		}
		if _, err := run.ExecContext(ctx, statement); err != nil {
			if record != nil {
				record.failed = true
			}
			if ctx.Err() != nil {
				return &protocol.Execution{Outcome: protocol.Execution_UNKNOWN, AppliedCount: applied,
					Diagnostic: &protocol.ProviderDiagnostic{Reason: fmt.Sprintf("the call ended while unit %d of %d was running", applied+1, len(units))}}, nil
			}
			return stoppedAt(applied, fmt.Sprintf("DuckDB refused unit %d of %d with %s", applied+1, len(units), refusalKind(err))), nil
		}
		applied++
	}
	return &protocol.Execution{Outcome: protocol.Execution_APPLIED, AppliedCount: applied}, nil
}

// connExec runs a statement on a session's own connection.
type connExec struct{ current *session }

func (c connExec) ExecContext(ctx context.Context, statement string, args ...any) (interface{ RowsAffected() (int64, error) }, error) {
	return c.current.handle.Conn().ExecContext(ctx, statement, args...)
}

// txExec runs a statement in a handle's transaction.
type txExec struct{ record *transaction }

func (t txExec) ExecContext(ctx context.Context, statement string, args ...any) (interface{ RowsAffected() (int64, error) }, error) {
	return t.record.tx.ExecContext(ctx, statement, args...)
}

// stoppedAt reports a batch that ran applied units and then stopped before
// the next one took effect: NOT_APPLIED when it applied none, PARTIAL when it
// applied some. The reason names the unit, never its text.
func stoppedAt(applied uint32, reason string) *protocol.Execution {
	outcome := protocol.Execution_PARTIAL
	if applied == 0 {
		outcome = protocol.Execution_NOT_APPLIED
	}
	return &protocol.Execution{Outcome: outcome, AppliedCount: applied, Diagnostic: &protocol.ProviderDiagnostic{Reason: reason}}
}

// refusalKind names the class of error DuckDB refused a statement with, in
// the provider's own words: a driver's message can quote the statement, a
// value or a path, and the host prints the reason.
func refusalKind(err error) string {
	refused, found := errors.AsType[*duckdb.Error](err)
	if !found {
		return "an error the driver reported"
	}
	switch refused.Type {
	case duckdb.ErrorTypeDependency:
		return "a dependency error: another object depends on what the unit changes"
	case duckdb.ErrorTypeConstraint:
		return "a constraint error: a row breaks a constraint"
	case duckdb.ErrorTypeCatalog:
		return "a catalog error: an object the unit names is missing, or exists already"
	case duckdb.ErrorTypeParser, duckdb.ErrorTypeSyntax:
		return "a parser error: DuckDB cannot parse the unit"
	case duckdb.ErrorTypeBinder:
		return "a binder error: the unit names a column, a type or a function DuckDB cannot resolve"
	case duckdb.ErrorTypeConversion, duckdb.ErrorTypeInvalidInput, duckdb.ErrorTypeOutOfRange, duckdb.ErrorTypeMismatchType:
		return "a conversion error: a value does not fit its type"
	case duckdb.ErrorTypeTransaction:
		return "a transaction conflict"
	case duckdb.ErrorTypeNotImplemented:
		return "a not-implemented error: DuckDB does not implement what the unit asks"
	case duckdb.ErrorTypeMissingExtension, duckdb.ErrorTypeAutoLoad:
		return "a missing-extension error: the provider loads no extension"
	case duckdb.ErrorTypeIO, duckdb.ErrorTypePermission:
		return "an I/O error"
	default:
		return "an error"
	}
}
