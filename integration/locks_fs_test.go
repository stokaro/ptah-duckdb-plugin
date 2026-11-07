//go:build integration

package integration_test

import (
	"context"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// lockRequest asks for the lock name on behalf of a session's lock ID.
func lockRequest(session *protocol.Session, lockID, name string) *protocol.LockRequest {
	return &protocol.LockRequest{Scope: &protocol.LockScope{SessionId: session.GetId(), LockId: lockID, Name: name}, RequestId: session.GetId() + "-" + lockID}
}

// The sessions of one provider process share its lock table, which every
// writer of a file goes through: DuckDB lets no other process write it.
func TestLocks(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	path := filepath.Join(c.TempDir(), "locks.duckdb")
	first, second := openSession(c, p, fileAddress(path)), openSession(c, p, fileAddress(path))
	other := openSession(c, p, fileAddress(filepath.Join(c.TempDir(), "other.duckdb")))
	steps := []struct {
		name    string
		call    func(context.Context, *protocol.LockRequest) (*protocol.LockResult, error)
		session *protocol.Session
		lockID  string
		lock    string
		want    protocol.LockResult_Outcome
	}{
		{"the first session takes the lock", p.TryLock, first, "a1", "migrate", protocol.LockResult_ACQUIRED},
		{"it checks the lock it holds", p.CheckLock, first, "a1", "migrate", protocol.LockResult_ACQUIRED},
		{"a session of the same file finds it busy", p.TryLock, second, "b1", "migrate", protocol.LockResult_BUSY},
		{"another lock name is free", p.TryLock, second, "b2", "seed", protocol.LockResult_ACQUIRED},
		{"the same name on another file is free", p.TryLock, other, "c1", "migrate", protocol.LockResult_ACQUIRED},
		{"the first session releases the lock", p.ReleaseLock, first, "a1", "migrate", protocol.LockResult_RELEASED},
		{"a lock ID that found the lock busy is spent", p.TryLock, second, "b1", "migrate", protocol.LockResult_BUSY},
		{"a new lock ID takes the free lock", p.TryLock, second, "b3", "migrate", protocol.LockResult_ACQUIRED},
	}
	for _, step := range steps {
		result, err := step.call(c.Context(), lockRequest(step.session, step.lockID, step.lock))
		c.Assert(err, qt.IsNil, qt.Commentf("%s", step.name))
		c.Assert(result.GetOutcome(), qt.Equals, step.want, qt.Commentf("%s", step.name))
	}

	c.Assert(p.Close(c.Context(), &protocol.CloseRequest{SessionId: second.GetId()}), qt.IsNil)
	result, err := p.TryLock(c.Context(), lockRequest(first, "a2", "migrate"))
	c.Assert(err, qt.IsNil)
	c.Assert(result.GetOutcome(), qt.Equals, protocol.LockResult_ACQUIRED)
}
