//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// metadataScope names a log or tag table of a session.
func metadataScope(session *protocol.Session, kind protocol.MetadataScope_Kind, table string) *protocol.MetadataScope {
	return &protocol.MetadataScope{SessionId: session.GetId(), Table: table, Kind: kind}
}

// mutateMetadata sends one metadata write and returns its receipt.
func mutateMetadata(c *qt.C, p *backend.Provider, request *protocol.MutateMetadataRequest) *protocol.MetadataResult {
	c.Helper()
	c.Assert(provider.ValidateMutateMetadataRequest(request), qt.IsNil)
	result, err := p.MutateMetadata(c.Context(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateMutateMetadataResult(request, result), qt.IsNil)
	return result
}

var (
	started = &protocol.MigrationLogRecord{RunId: "run-1", Sequence: 1, Operation: "up", Version: 20260101, State: "started",
		ActorSource: "process-user", Actor: "ops", AtSeconds: 1767225600, AtNanos: 1000}
	finished = &protocol.MigrationLogRecord{RunId: "run-1", Sequence: 2, Operation: "up", Version: 20260101, State: "applied",
		ActorSource: "process-user", Actor: "ops", Checksum: "abc", AtSeconds: 1767225601}
)

func TestMetadata_Log(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	session := openSession(c, p, fileAddress(filepath.Join(c.TempDir(), "log.duckdb")))
	scope := metadataScope(session, protocol.MetadataScope_LOG, "schema_migrations_log")
	prepared, err := p.PrepareMetadata(c.Context(), &protocol.PrepareMetadataRequest{Scope: scope, RequestId: "prepare"})
	c.Assert(err, qt.IsNil)
	c.Assert(prepared.GetOutcome(), qt.Equals, protocol.MetadataResult_APPLIED)

	appends := []struct {
		name   string
		record *protocol.MigrationLogRecord
		want   protocol.MetadataResult_Outcome
	}{
		{"first", started, protocol.MetadataResult_APPLIED},
		{"second", finished, protocol.MetadataResult_APPLIED},
		{"a repeated identity", started, protocol.MetadataResult_REJECTED},
	}
	for _, step := range appends {
		result := mutateMetadata(c, p, &protocol.MutateMetadataRequest{Scope: scope, RequestId: step.name,
			Mutation: &protocol.MutateMetadataRequest_AppendLog{AppendLog: step.record}})
		c.Assert(result.GetOutcome(), qt.Equals, step.want, qt.Commentf("%s", step.name))
	}

	reader := &protocol.MetadataReadRequest{Scope: scope, ReaderId: "r1", RequestId: "open"}
	_, err = p.OpenMetadataRead(c.Context(), reader)
	c.Assert(err, qt.IsNil)
	pages := []struct {
		page     uint64
		logs     []*protocol.MigrationLogRecord
		complete bool
	}{
		{0, []*protocol.MigrationLogRecord{started}, false},
		{1, []*protocol.MigrationLogRecord{finished}, true},
	}
	for _, want := range pages {
		request := &protocol.ReadMetadataPageRequest{Reader: reader, Page: want.page, MaxRecords: 1}
		page, err := p.ReadMetadataPage(c.Context(), request)
		c.Assert(err, qt.IsNil)
		c.Assert(provider.ValidateMetadataPage(request, page), qt.IsNil)
		c.Assert(page.GetLogs(), qt.CmpEquals(protocmp.Transform()), want.logs)
		c.Assert(page.GetComplete(), qt.Equals, want.complete)
	}
	page, err := p.ReadMetadataPage(c.Context(), &protocol.ReadMetadataPageRequest{Reader: reader, Page: 5, MaxRecords: 1})
	c.Assert(status.Code(err), qt.Equals, codes.FailedPrecondition)
	c.Assert(page, qt.IsNil)
	_, err = p.CloseMetadataRead(c.Context(), reader)
	c.Assert(err, qt.IsNil)
	_, err = p.OpenMetadataRead(c.Context(), reader)
	c.Assert(status.Code(err), qt.Equals, codes.AlreadyExists)
}

func TestMetadata_Tags(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	session := openSession(c, p, fileAddress(filepath.Join(c.TempDir(), "tags.duckdb")))
	scope := metadataScope(session, protocol.MetadataScope_TAGS, "ptah_migration_tags")

	missing := mutateMetadata(c, p, &protocol.MutateMetadataRequest{Scope: scope, RequestId: "early",
		Mutation: &protocol.MutateMetadataRequest_DeleteTag{DeleteTag: "v1"}})
	c.Assert(missing.GetOutcome(), qt.Equals, protocol.MetadataResult_APPLIED)
	c.Assert(missing.GetDeleted(), qt.IsFalse)
	prepared, err := p.PrepareMetadata(c.Context(), &protocol.PrepareMetadataRequest{Scope: scope, RequestId: "prepare"})
	c.Assert(err, qt.IsNil)
	c.Assert(prepared.GetOutcome(), qt.Equals, protocol.MetadataResult_APPLIED)

	first := &protocol.MigrationTagRecord{Name: "v1", Version: 1, RecordedAtSeconds: 1767225600}
	moved := &protocol.MigrationTagRecord{Name: "v1", Version: 2, RecordedAtSeconds: 1767225700, RecordedAtNanos: 3000}
	steps := []struct {
		name     string
		mutation *protocol.MutateMetadataRequest
		deleted  bool
		found    *protocol.MigrationTagRecord
	}{
		{"record", &protocol.MutateMetadataRequest{Mutation: &protocol.MutateMetadataRequest_PutTag{PutTag: first}}, false, first},
		{"move", &protocol.MutateMetadataRequest{Mutation: &protocol.MutateMetadataRequest_PutTag{PutTag: moved}}, false, moved},
		{"remove", &protocol.MutateMetadataRequest{Mutation: &protocol.MutateMetadataRequest_DeleteTag{DeleteTag: "v1"}}, true, nil},
		{"remove again", &protocol.MutateMetadataRequest{Mutation: &protocol.MutateMetadataRequest_DeleteTag{DeleteTag: "v1"}}, false, nil},
	}
	for _, step := range steps {
		step.mutation.Scope, step.mutation.RequestId = scope, step.name
		result := mutateMetadata(c, p, step.mutation)
		c.Assert(result.GetOutcome(), qt.Equals, protocol.MetadataResult_APPLIED, qt.Commentf("%s", step.name))
		c.Assert(result.GetDeleted(), qt.Equals, step.deleted, qt.Commentf("%s", step.name))
		lookup := &protocol.LookupMetadataTagRequest{Scope: scope, RequestId: "lookup", Name: "v1"}
		found, err := p.LookupMetadataTag(c.Context(), lookup)
		c.Assert(err, qt.IsNil)
		c.Assert(provider.ValidateMetadataTagResult(lookup, found), qt.IsNil)
		c.Assert(found.GetRecord(), qt.CmpEquals(protocmp.Transform()), step.found, qt.Commentf("%s", step.name))
	}
}
