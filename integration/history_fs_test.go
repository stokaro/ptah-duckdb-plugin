//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// historyScope names a revision table of a session.
func historyScope(session *protocol.Session, format protocol.HistoryScope_Format, table string) *protocol.HistoryScope {
	return &protocol.HistoryScope{SessionId: session.GetId(), Table: table, Format: format}
}

// readHistory reads a revision table and checks the snapshot as the host
// does.
func readHistory(c *qt.C, p *backend.Provider, scope *protocol.HistoryScope) *protocol.HistorySnapshot {
	c.Helper()
	request := &protocol.ReadHistoryRequest{Scope: scope, MaxRevisions: provider.MaxHistoryRevisions}
	snapshot, err := p.ReadHistory(c.Context(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateHistorySnapshot(request, snapshot), qt.IsNil)
	return snapshot
}

// mutateHistory sends one compare-and-swap and returns its outcome.
func mutateHistory(c *qt.C, p *backend.Provider, scope *protocol.HistoryScope, expected, replacement *protocol.HistoryRevision) protocol.HistoryResult_Outcome {
	c.Helper()
	request := &protocol.MutateHistoryRequest{Scope: scope, RequestId: "mutate", Expected: expected, Replacement: replacement}
	c.Assert(provider.ValidateMutateHistoryRequest(request), qt.IsNil)
	result, err := p.MutateHistory(c.Context(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateHistoryResult(scope, "mutate", result), qt.IsNil)
	return result.GetOutcome()
}

// prepareHistory prepares a revision table and returns the result.
func prepareHistory(c *qt.C, p *backend.Provider, scope *protocol.HistoryScope) *protocol.HistoryResult {
	c.Helper()
	result, err := p.PrepareHistory(c.Context(), &protocol.PrepareHistoryRequest{Scope: scope, RequestId: "prepare"})
	c.Assert(err, qt.IsNil)
	return result
}

// The records are in the precision the native table keeps: a time to the
// microsecond and a run time to the millisecond.
var (
	pending = &protocol.HistoryRevision{Version: "20260101", Description: "Init", State: protocol.HistoryRevision_PENDING,
		Direction: protocol.HistoryRevision_UP, Total: 2, Checksum: "abc", AppliedAtSeconds: 1767225600, AppliedAtNanos: 123456000}
	applied = &protocol.HistoryRevision{Version: "20260101", Description: "Init", State: protocol.HistoryRevision_APPLIED,
		Direction: protocol.HistoryRevision_UP, Applied: 2, Total: 2, ExecutionNanoseconds: 42000000, Checksum: "abc",
		AppliedAtSeconds: 1767225601, AppliedAtNanos: 0}
	failedDown = &protocol.HistoryRevision{Version: "20260102", Description: "Second", State: protocol.HistoryRevision_FAILED,
		Direction: protocol.HistoryRevision_DOWN, Applied: 1, Total: 3, Error: "refused", ErrorStatement: "DROP TABLE t", Checksum: "def",
		AppliedAtSeconds: 1767225700, AppliedAtNanos: 5000}
)

func TestHistory_Native(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	session := openSession(c, p, fileAddress(filepath.Join(c.TempDir(), "history.duckdb")))
	scope := historyScope(session, protocol.HistoryScope_NATIVE, "schema_migrations")

	c.Assert(readHistory(c, p, scope).GetExists(), qt.IsFalse)
	c.Assert(mutateHistory(c, p, scope, nil, pending), qt.Equals, protocol.HistoryResult_REFUSED)
	c.Assert(prepareHistory(c, p, scope).GetOutcome(), qt.Equals, protocol.HistoryResult_APPLIED)
	c.Assert(prepareHistory(c, p, scope).GetOutcome(), qt.Equals, protocol.HistoryResult_APPLIED)

	steps := []struct {
		name                  string
		expected, replacement *protocol.HistoryRevision
		want                  protocol.HistoryResult_Outcome
	}{
		{"insert", nil, pending, protocol.HistoryResult_APPLIED},
		{"insert what exists", nil, pending, protocol.HistoryResult_CONFLICT},
		{"replace what was read", pending, applied, protocol.HistoryResult_APPLIED},
		{"replace a record that changed", pending, applied, protocol.HistoryResult_CONFLICT},
		{"insert a rollback's failure", nil, failedDown, protocol.HistoryResult_APPLIED},
	}
	for _, step := range steps {
		c.Assert(mutateHistory(c, p, scope, step.expected, step.replacement), qt.Equals, step.want, qt.Commentf("%s", step.name))
	}
	snapshot := readHistory(c, p, scope)
	c.Assert(snapshot.GetExists(), qt.IsTrue)
	c.Assert(snapshot.GetRevisions(), qt.CmpEquals(protocmp.Transform()), []*protocol.HistoryRevision{applied, failedDown})

	c.Assert(mutateHistory(c, p, scope, applied, nil), qt.Equals, protocol.HistoryResult_APPLIED)
	c.Assert(readHistory(c, p, scope).GetRevisions(), qt.CmpEquals(protocmp.Transform()), []*protocol.HistoryRevision{failedDown})
}

// The Atlas layout keeps its own facts: a text version, a type, partial
// hashes and the operator's version, and neither state nor direction.
func TestHistory_Atlas(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	session := openSession(c, p, fileAddress(filepath.Join(c.TempDir(), "atlas.duckdb")))
	scope := historyScope(session, protocol.HistoryScope_ATLAS, "atlas_schema_revisions")
	c.Assert(prepareHistory(c, p, scope).GetOutcome(), qt.Equals, protocol.HistoryResult_APPLIED)

	first := &protocol.HistoryRevision{Version: "20260101", Description: "init", AtlasType: 2, Applied: 1, Total: 2, Checksum: "h",
		PartialHashes: []string{"p1"}, OperatorVersion: "ptah", ExecutionNanoseconds: 1234, AppliedAtSeconds: 1767225600, AppliedAtNanos: 7000}
	second := &protocol.HistoryRevision{Version: "", Description: "baseline", AtlasType: 1, Checksum: "", OperatorVersion: "ptah",
		AppliedAtSeconds: 1767225600}
	c.Assert(mutateHistory(c, p, scope, nil, first), qt.Equals, protocol.HistoryResult_APPLIED)
	c.Assert(mutateHistory(c, p, scope, nil, second), qt.Equals, protocol.HistoryResult_APPLIED)
	c.Assert(readHistory(c, p, scope).GetRevisions(), qt.CmpEquals(protocmp.Transform()), []*protocol.HistoryRevision{second, first})
}

// A write in a caller transaction is visible there alone, and goes with the
// transaction's rollback.
func TestHistory_InATransaction(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	session := openSession(c, p, fileAddress(filepath.Join(c.TempDir(), "tx.duckdb")))
	scope := historyScope(session, protocol.HistoryScope_NATIVE, "schema_migrations")
	c.Assert(prepareHistory(c, p, scope).GetOutcome(), qt.Equals, protocol.HistoryResult_APPLIED)
	_, err := p.BeginTransaction(c.Context(), &protocol.BeginTransactionRequest{SessionId: session.GetId(), TransactionId: "t1"})
	c.Assert(err, qt.IsNil)

	inside := historyScope(session, protocol.HistoryScope_NATIVE, "schema_migrations")
	inside.TransactionId = "t1"
	c.Assert(mutateHistory(c, p, inside, nil, pending), qt.Equals, protocol.HistoryResult_APPLIED)
	c.Assert(readHistory(c, p, inside).GetRevisions(), qt.HasLen, 1)
	c.Assert(readHistory(c, p, scope).GetRevisions(), qt.HasLen, 0)

	finished, err := p.FinishTransaction(c.Context(), &protocol.FinishTransactionRequest{SessionId: session.GetId(), TransactionId: "t1",
		Action: protocol.FinishTransactionRequest_ROLLBACK})
	c.Assert(err, qt.IsNil)
	c.Assert(finished.GetOutcome(), qt.Equals, protocol.TransactionResult_ROLLED_BACK)
	c.Assert(readHistory(c, p, scope).GetRevisions(), qt.HasLen, 0)
}

func TestHistory_FailurePath(t *testing.T) {
	tests := []struct {
		name    string
		seed    []string
		address string
		code    string
	}{
		{"a user's table of the name", []string{"CREATE TABLE schema_migrations (id INTEGER)"}, "", "duckdb.store-layout"},
		{"the layout without the mark", []string{`CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, description VARCHAR NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL, state VARCHAR NOT NULL, applied BIGINT NOT NULL, total BIGINT NOT NULL, error VARCHAR, error_stmt VARCHAR,
			execution_time_ms BIGINT NOT NULL, checksum VARCHAR NOT NULL)`}, "", "duckdb.store-layout"},
		{"a view of the name", []string{"CREATE VIEW schema_migrations AS SELECT 1 AS version"}, "", "duckdb.store-layout"},
		{"a read-only session", []string{"CREATE TABLE other (id INTEGER)"}, "?access_mode=read_only", "duckdb.store-read-only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			p := backend.New()
			session := openSession(c, p, fileAddress(seedFile(c, "refused.duckdb", tc.seed))+tc.address)
			scope := historyScope(session, protocol.HistoryScope_NATIVE, "schema_migrations")
			result := prepareHistory(c, p, scope)
			c.Assert(result.GetOutcome(), qt.Equals, protocol.HistoryResult_REFUSED)
			c.Assert(result.GetDiagnosticCode(), qt.Equals, tc.code)
		})
	}
}

// A read of a table of the store's name that is not the provider's fails,
// rather than read a user's rows as revisions.
func TestReadHistory_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	session := openSession(c, p, fileAddress(seedFile(c, "user.duckdb", []string{"CREATE TABLE schema_migrations (id INTEGER)"})))
	snapshot, err := p.ReadHistory(c.Context(), &protocol.ReadHistoryRequest{
		Scope: historyScope(session, protocol.HistoryScope_NATIVE, "schema_migrations"), MaxRevisions: 10})
	c.Assert(status.Code(err), qt.Equals, codes.FailedPrecondition)
	c.Assert(status.Convert(err).Message(), qt.Equals,
		"a table of the store's name exists and is not the store the provider keeps its records in: it does not carry the provider's mark as its comment")
	c.Assert(snapshot, qt.IsNil)
}
