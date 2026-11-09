package backend

import (
	"context"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"
	providerruntime "ptah.run/provider-sdk/runtime"
)

// lockKey names one lock: the database it guards and its name.
type lockKey struct{ database, name string }

// heldLock is the session and lock ID that hold a lock.
type heldLock struct{ session, lockID string }

// lockRecord is one lock ID of a session. A lock ID is used once: a second
// request with the same ID gets the answer of the first.
type lockRecord struct {
	name    string
	outcome protocol.LockResult_Outcome
}

// TryLock makes one attempt to take a lock and does not wait.
//
// DuckDB lets one process write a database file, and the provider holds that
// process's sessions, so a lock is an entry of the provider's own lock table,
// keyed by the database and the lock's name: the sessions of the process are
// every writer there is. A read-only file is shared by reading processes,
// which write nothing a lock would guard.
func (p *Provider) TryLock(ctx context.Context, request *protocol.LockRequest) (*protocol.LockResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	record, fresh, err := current.reserveLock(request.GetScope())
	if err != nil {
		return nil, err
	}
	if !fresh {
		return lockReceipt(ctx, request, record.outcome, "the lock ID was used before"), nil
	}
	key := lockKey{database: current.handle.Key(), name: record.name}
	if holder, held := p.locks[key]; held && holder.lockID != request.GetScope().GetLockId() {
		record.outcome = protocol.LockResult_BUSY
		return lockReceipt(ctx, request, record.outcome, "another session of the provider holds the lock"), nil
	}
	if p.locks == nil {
		p.locks = make(map[lockKey]heldLock)
	}
	p.locks[key] = heldLock{session: request.GetScope().GetSessionId(), lockID: request.GetScope().GetLockId()}
	record.outcome = protocol.LockResult_ACQUIRED
	return lockReceipt(ctx, request, record.outcome, ""), nil
}

// CheckLock proves that the session holds the lock now: the lock table names
// this lock ID.
func (p *Provider) CheckLock(ctx context.Context, request *protocol.LockRequest) (*protocol.LockResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	record, _, err := current.reserveLock(request.GetScope())
	if err != nil {
		return nil, err
	}
	switch record.outcome {
	case protocol.LockResult_ACQUIRED:
		if holder := p.locks[lockKey{database: current.handle.Key(), name: record.name}]; holder.lockID != request.GetScope().GetLockId() {
			record.outcome = protocol.LockResult_LOST
			return lockReceipt(ctx, request, record.outcome, "the lock is no longer held by this lock ID"), nil
		}
		return lockReceipt(ctx, request, record.outcome, ""), nil
	case protocol.LockResult_BUSY:
		return lockReceipt(ctx, request, protocol.LockResult_NOT_ACQUIRED, "the attempt with this lock ID found the lock busy"), nil
	default:
		return lockReceipt(ctx, request, record.outcome, "the lock ID holds no lock"), nil
	}
}

// ReleaseLock gives a lock back. Releasing an ID that never took a lock fences
// it, so a late TryLock with that ID takes nothing.
func (p *Provider) ReleaseLock(ctx context.Context, request *protocol.LockRequest) (*protocol.LockResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	record, _, err := current.reserveLock(request.GetScope())
	if err != nil {
		return nil, err
	}
	switch record.outcome {
	case protocol.LockResult_ACQUIRED:
		p.releaseHeld(current.handle.Key(), record.name, request.GetScope().GetLockId())
		record.outcome = protocol.LockResult_RELEASED
	case protocol.LockResult_BUSY:
		record.outcome = protocol.LockResult_NOT_ACQUIRED
	}
	return lockReceipt(ctx, request, record.outcome, ""), nil
}

// releaseHeld removes a lock from the table when lockID holds it. The caller
// holds lockMu.
func (p *Provider) releaseHeld(database, name, lockID string) {
	key := lockKey{database: database, name: name}
	if holder, held := p.locks[key]; held && holder.lockID == lockID {
		delete(p.locks, key)
	}
}

// releaseLocks gives back every lock a closing session holds. The caller
// holds lockMu.
func (p *Provider) releaseLocks(current *session) {
	for lockID, record := range current.lockIDs {
		if record.outcome == protocol.LockResult_ACQUIRED {
			p.releaseHeld(current.handle.Key(), record.name, lockID)
			record.outcome = protocol.LockResult_RELEASED
		}
	}
}

// reserveLock returns the record of a lock ID and whether this call created
// it. The caller holds Provider.lockMu.
func (s *session) reserveLock(scope *protocol.LockScope) (*lockRecord, bool, error) {
	if s.lockIDs == nil {
		s.lockIDs = make(map[string]*lockRecord)
	}
	if record := s.lockIDs[scope.GetLockId()]; record != nil {
		if record.name != scope.GetName() {
			return nil, false, providerruntime.ErrorWithReason(codes.InvalidArgument, "a lock ID names one lock")
		}
		return record, false, nil
	}
	record := &lockRecord{name: scope.GetName(), outcome: protocol.LockResult_NOT_ACQUIRED}
	s.lockIDs[scope.GetLockId()] = record
	return record, true, nil
}

// lockReceipt answers a lock request with its own scope and request ID, and
// the reason for an outcome that is not a success when the host takes
// reasons.
func lockReceipt(ctx context.Context, request *protocol.LockRequest, outcome protocol.LockResult_Outcome, reason string) *protocol.LockResult {
	result := &protocol.LockResult{Scope: proto.CloneOf(request.GetScope()), RequestId: request.GetRequestId(), Outcome: outcome}
	if reason != "" && slices.Contains(provider.HostFeatures(ctx), provider.ErrorReasonsFeature) && !lockSucceeded(outcome) {
		result.Diagnostic = &protocol.ProviderDiagnostic{Reason: reason}
	}
	return result
}

// lockSucceeded reports the outcomes a lock call succeeds with, which carry no
// reason.
func lockSucceeded(outcome protocol.LockResult_Outcome) bool {
	return outcome == protocol.LockResult_ACQUIRED || outcome == protocol.LockResult_RELEASED || outcome == protocol.LockResult_NOT_ACQUIRED
}
