package backend

import (
	"context"
	"database/sql"
	"errors"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/metadata"
	"github.com/stokaro/ptah-duckdb-plugin/internal/ownedtable"
)

// maxReaderBytes bounds the records one metadata snapshot holds in memory. A
// larger store is refused rather than read in part.
const maxReaderBytes = 64 << 20

// readerLimit bounds the readers one session keeps. A closed reader stays to
// fence a late open of its ID.
const readerLimit = 1024

// reader is one snapshot of a metadata store, read at open and handed out a
// page at a time.
type reader struct {
	scope *protocol.MetadataScope
	// exists is false for a store that did not exist at open.
	exists  bool
	records []proto.Message
	// next is the page the next request must ask for, and cursor the first
	// record it returns.
	next   uint64
	cursor int
	// closed is set by a close, by a page that failed, and when the
	// transaction the reader reads in finishes.
	closed bool
}

// metadataOutcomes maps a preparation's outcome to a metadata receipt's.
var metadataOutcomes = map[protocol.HistoryResult_Outcome]protocol.MetadataResult_Outcome{
	protocol.HistoryResult_APPLIED: protocol.MetadataResult_APPLIED,
	protocol.HistoryResult_REFUSED: protocol.MetadataResult_REJECTED,
	protocol.HistoryResult_UNKNOWN: protocol.MetadataResult_UNKNOWN,
}

// PrepareMetadata creates a missing log or tag table with the provider's
// mark and checks an existing one is the provider's and holds exactly its
// layout. Inside a transaction it only checks an existing table: preparation
// that writes runs outside transactions.
func (p *Provider) PrepareMetadata(ctx context.Context, request *protocol.PrepareMetadataRequest) (*protocol.MetadataResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	answer := func(outcome protocol.MetadataResult_Outcome) *protocol.MetadataResult {
		return &protocol.MetadataResult{Scope: proto.CloneOf(request.GetScope()), RequestId: request.GetRequestId(), Outcome: outcome}
	}
	store := metadata.StoreOf(request.GetScope())
	if request.GetScope().GetTransactionId() != "" {
		exists, err := ownedtable.Locate(ctx, current.handle.Conn(), store.Definition())
		if err != nil || !exists {
			return answer(protocol.MetadataResult_REJECTED), nil
		}
		return answer(protocol.MetadataResult_APPLIED), nil
	}
	outcome, _, _ := current.prepareStore(ctx, store.Definition(), store.Statements())
	return answer(metadataOutcomes[outcome]), nil
}

// MutateMetadata appends a log record, or records or removes a tag. A log
// record is one insert that commits as it runs; a tag changes in the
// request's transaction, or in one of the provider's own that it commits. A
// failure before the commit wrote nothing. A request is never sent twice.
func (p *Provider) MutateMetadata(ctx context.Context, request *protocol.MutateMetadataRequest) (*protocol.MetadataResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	answer := func(outcome protocol.MetadataResult_Outcome, deleted bool) *protocol.MetadataResult {
		return &protocol.MetadataResult{Scope: proto.CloneOf(request.GetScope()), RequestId: request.GetRequestId(), Outcome: outcome,
			Deleted: deleted && outcome == protocol.MetadataResult_APPLIED}
	}
	store := metadata.StoreOf(request.GetScope())
	exists, err := ownedtable.Locate(ctx, current.handle.Conn(), store.Definition())
	switch {
	case err != nil:
		return answer(protocol.MetadataResult_REJECTED, false), nil
	case !exists && request.GetDeleteTag() != "":
		// A tag a missing table never held is absent already.
		return answer(protocol.MetadataResult_APPLIED, false), nil
	case !exists:
		return answer(protocol.MetadataResult_REJECTED, false), nil
	}
	if record := request.GetAppendLog(); record != nil {
		err := store.Append(ctx, current.handle.Conn(), record)
		switch {
		case errors.Is(err, metadata.ErrDuplicate):
			return answer(protocol.MetadataResult_REJECTED, false), nil
		case err != nil && ctx.Err() != nil:
			return answer(protocol.MetadataResult_UNKNOWN, false), nil
		case err != nil:
			// DuckDB refused the insert, which wrote nothing.
			return answer(protocol.MetadataResult_REJECTED, false), nil
		}
		return answer(protocol.MetadataResult_APPLIED, false), nil
	}
	outcome, deleted := current.changeTag(ctx, store, request)
	return answer(outcome, deleted), nil
}

// changeTag records or removes the tag a request names, in the request's
// transaction or in one of the provider's own that it commits, and reports
// the outcome and whether a removed tag was held. The caller holds mu.
func (s *session) changeTag(ctx context.Context, store metadata.Store, request *protocol.MutateMetadataRequest) (protocol.MetadataResult_Outcome, bool) {
	change := func(q ownedtable.Querier) (bool, error) {
		if name := request.GetDeleteTag(); name != "" {
			return store.DeleteTag(ctx, q, name)
		}
		return false, store.PutTag(ctx, q, request.GetPutTag())
	}
	if id := request.GetScope().GetTransactionId(); id != "" {
		record, err := s.openTransaction(id)
		if err != nil {
			return protocol.MetadataResult_REJECTED, false
		}
		deleted, err := change(record.tx)
		if err != nil {
			// DuckDB aborted the transaction, which commits nothing.
			record.failed = true
			return protocol.MetadataResult_REJECTED, false
		}
		return protocol.MetadataResult_APPLIED, deleted
	}
	var deleted bool
	err := runInTransaction(ctx, s.handle.Conn(), func(tx *sql.Tx) error {
		var err error
		deleted, err = change(tx)
		return err
	})
	switch {
	case errors.Is(err, errNotCommitted):
		return protocol.MetadataResult_REJECTED, false
	case err != nil:
		return protocol.MetadataResult_UNKNOWN, false
	}
	return protocol.MetadataResult_APPLIED, deleted
}

// LookupMetadataTag reads one tag, in the request's transaction when it
// names one. A missing table holds no tag, and the lookup creates nothing.
func (p *Provider) LookupMetadataTag(ctx context.Context, request *protocol.LookupMetadataTagRequest) (*protocol.MetadataTagResult, error) {
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
	store := metadata.StoreOf(request.GetScope())
	result := &protocol.MetadataTagResult{Scope: proto.CloneOf(request.GetScope()), RequestId: request.GetRequestId()}
	exists, err := current.locateStore(ctx, q, store.Definition())
	if err != nil || !exists {
		return result, err
	}
	result.Record, err = store.Lookup(ctx, q, request.GetName())
	if err != nil {
		if record != nil {
			record.failed = true
		}
		return nil, callFailed(ctx, "the provider could not read the tag table")
	}
	result.Found = result.Record != nil
	return result, nil
}

// OpenMetadataRead reads a snapshot of a log or tag table under a reader ID
// the caller chose, in the request's transaction when it names one. A
// missing table is a snapshot that says so, and opening creates nothing.
func (p *Provider) OpenMetadataRead(ctx context.Context, request *protocol.MetadataReadRequest) (*protocol.MetadataReadResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if found := current.readers[request.GetReaderId()]; found != nil {
		return nil, providerruntime.ErrorWithReason(codes.AlreadyExists, "the reader ID was used before")
	}
	if len(current.readers) >= readerLimit {
		return nil, providerruntime.ErrorWithReason(codes.ResourceExhausted, "the session used every reader it keeps; open another session")
	}
	q, record, err := current.storeQuerier(request.GetScope().GetTransactionId())
	if err != nil {
		return nil, err
	}
	store := metadata.StoreOf(request.GetScope())
	exists, err := current.locateStore(ctx, q, store.Definition())
	if err != nil {
		return nil, err
	}
	opened := &reader{scope: proto.CloneOf(request.GetScope()), exists: exists}
	if exists {
		opened.records, err = store.Snapshot(ctx, q, maxReaderBytes)
		switch {
		case errors.Is(err, metadata.ErrTooLarge):
			return nil, providerruntime.ErrorWithReason(codes.ResourceExhausted, "the table holds more records than a snapshot reads")
		case err != nil:
			if record != nil {
				record.failed = true
			}
			return nil, callFailed(ctx, "the provider could not read the table")
		}
	}
	if current.readers == nil {
		current.readers = make(map[string]*reader)
	}
	current.readers[request.GetReaderId()] = opened
	return &protocol.MetadataReadResult{Scope: proto.CloneOf(request.GetScope()), ReaderId: request.GetReaderId(), RequestId: request.GetRequestId()}, nil
}

// ReadMetadataPage hands out the next page of a snapshot: at most the
// request's count of records, within the message bound. Pages advance once
// each, from zero; a page that cannot be answered closes the reader.
func (p *Provider) ReadMetadataPage(_ context.Context, request *protocol.ReadMetadataPageRequest) (*protocol.MetadataPage, error) {
	current, err := p.lookup(request.GetReader().GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	opened := current.readers[request.GetReader().GetReaderId()]
	switch {
	case opened == nil || opened.closed:
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the reader is not open")
	case !proto.Equal(opened.scope, request.GetReader().GetScope()):
		opened.closed = true
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the reader was opened for another store")
	case request.GetPage() != opened.next:
		opened.closed = true
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the page is not the reader's next page")
	}
	page := &protocol.MetadataPage{
		Reader: &protocol.MetadataReadResult{Scope: proto.CloneOf(opened.scope), ReaderId: request.GetReader().GetReaderId(), RequestId: request.GetReader().GetRequestId()},
		Page:   request.GetPage(), Exists: opened.exists,
	}
	for opened.cursor < len(opened.records) && len(page.GetLogs())+len(page.GetTags()) < int(request.GetMaxRecords()) {
		switch record := opened.records[opened.cursor].(type) {
		case *protocol.MigrationLogRecord:
			page.Logs = append(page.Logs, record)
		case *protocol.MigrationTagRecord:
			page.Tags = append(page.Tags, record)
		}
		if proto.Size(page) > provider.MaxMetadataBytes {
			opened.closed = true
			return nil, providerruntime.ErrorWithReason(codes.ResourceExhausted, "one record is larger than a page carries")
		}
		opened.cursor++
	}
	page.Complete = opened.cursor == len(opened.records)
	opened.next++
	return page, nil
}

// CloseMetadataRead releases a reader. Closing an ID that was never opened
// fences it, so a late open with that ID reads nothing; closing twice
// succeeds.
func (p *Provider) CloseMetadataRead(_ context.Context, request *protocol.MetadataReadRequest) (*protocol.MetadataReadResult, error) {
	current, err := p.lookup(request.GetScope().GetSessionId())
	if err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.readers == nil {
		current.readers = make(map[string]*reader)
	}
	if found := current.readers[request.GetReaderId()]; found != nil {
		found.closed, found.records = true, nil
	} else {
		current.readers[request.GetReaderId()] = &reader{scope: proto.CloneOf(request.GetScope()), closed: true}
	}
	return &protocol.MetadataReadResult{Scope: proto.CloneOf(request.GetScope()), ReaderId: request.GetReaderId(), RequestId: request.GetRequestId()}, nil
}

// closeReaders closes the readers that read in a transaction that finished.
// The caller holds mu.
func (s *session) closeReaders(transactionID string) {
	for _, opened := range s.readers {
		if opened.scope.GetTransactionId() == transactionID {
			opened.closed, opened.records = true, nil
		}
	}
}
