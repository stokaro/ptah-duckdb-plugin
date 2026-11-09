//go:build integration

package integration_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// devSchema holds one object of each kind a cleanup removes, in main and in
// another schema: a view DuckDB records no dependency of, tables a foreign
// key orders against the order of their names, and a sequence a default
// draws from.
var devSchema = []string{
	"CREATE SEQUENCE ids",
	"CREATE TABLE customers (id BIGINT PRIMARY KEY DEFAULT nextval('ids'))",
	"CREATE TABLE orders (customer_id BIGINT REFERENCES customers (id))",
	"CREATE INDEX orders_customer ON orders (customer_id)",
	"CREATE VIEW recent AS SELECT * FROM orders",
	"CREATE MACRO twice(x) AS x * 2",
	"CREATE MACRO numbers() AS TABLE SELECT 1 AS n",
	"CREATE TYPE mood AS ENUM ('ok')",
	"CREATE SCHEMA app",
	"CREATE TABLE app.t (id INTEGER)",
	"CREATE VIEW app.v AS SELECT * FROM app.t",
}

// devObjects lists a session's dev objects as kind, schema and name.
func devObjects(c *qt.C, p *backend.Provider, session *protocol.Session, schemas ...string) []string {
	c.Helper()
	request := &protocol.ListDevObjectsRequest{SessionId: session.GetId(), Schemas: schemas}
	listed, err := p.ListDevObjects(c.Context(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateDevObjects(request, listed), qt.IsNil)
	var objects []string
	for _, object := range listed.GetObjects() {
		objects = append(objects, object.GetObject().GetKind()+" "+object.GetObject().GetSchema()+"."+object.GetObject().GetName())
	}
	return objects
}

func TestDevCleanup(t *testing.T) {
	tests := []struct {
		name    string
		schemas []string
		listed  []string
		left    []string
	}{
		{"the whole database", nil, []string{
			"view app.v", "table app.t", "schema .app",
			"view main.recent", "macro main.twice", "table macro main.numbers", "index main.orders_customer",
			"table main.orders", "table main.customers", "sequence main.ids", "type main.mood",
		}, nil},
		{"one schema", []string{"APP"}, []string{"view app.v", "table app.t", "schema .app"}, []string{
			"view main.recent", "macro main.twice", "table macro main.numbers", "index main.orders_customer",
			"table main.orders", "table main.customers", "sequence main.ids", "type main.mood",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			p := backend.New()
			session := openSession(c, p, fileAddress(seedFile(c, "dev.duckdb", devSchema)))
			c.Assert(devObjects(c, p, session, tc.schemas...), qt.DeepEquals, tc.listed)

			request := &protocol.CleanDevRequest{SessionId: session.GetId(), RequestId: "clean", Schemas: tc.schemas}
			cleaned, err := p.CleanDevDatabase(c.Context(), request)
			c.Assert(err, qt.IsNil)
			c.Assert(provider.CheckDevCleanup(request, cleaned), qt.IsNil)
			c.Assert(devObjects(c, p, session, tc.schemas...), qt.IsNil)
			c.Assert(devObjects(c, p, session), qt.DeepEquals, tc.left)
		})
	}
}

// A read-only session removes nothing and names every object that stopped
// it.
func TestDevCleanup_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	path := seedFile(c, "dev.duckdb", []string{"CREATE TABLE t (id INTEGER)"})
	session := openSession(c, p, fileAddress(path)+"?access_mode=read_only")
	request := &protocol.CleanDevRequest{SessionId: session.GetId(), RequestId: "clean"}
	cleaned, err := p.CleanDevDatabase(c.Context(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateDevCleanup(request, cleaned), qt.IsNil)
	c.Assert(cleaned.GetOutcome(), qt.Equals, protocol.DevCleanup_REFUSED)
	c.Assert(cleaned.GetBlocking(), qt.HasLen, 1)
	c.Assert(cleaned.GetBlocking()[0].GetRefusal(), qt.Equals, "the session opened the database read-only, so nothing in it can be removed")
	c.Assert(devObjects(c, p, session), qt.DeepEquals, []string{"table main.t"})
}

// The provider's own tables are reserved: a read leaves them out of the
// schema and names their purpose, so no plan manages them. A user's table of
// the same name without the provider's mark is read as the user's.
func TestInspect_ReservesTheProvidersTables(t *testing.T) {
	c := testcheck.New(t)
	p := backend.New()
	path := seedFile(c, "reserved.duckdb", []string{"CREATE TABLE ptah_migration_tags (id INTEGER)"})
	session := openSession(c, p, fileAddress(path))
	for _, scope := range []*protocol.HistoryScope{
		historyScope(session, protocol.HistoryScope_NATIVE, "schema_migrations"),
		historyScope(session, protocol.HistoryScope_ATLAS, "custom_revisions"),
	} {
		c.Assert(prepareHistory(c, p, scope).GetOutcome(), qt.Equals, protocol.HistoryResult_APPLIED)
	}
	prepared, err := p.PrepareMetadata(c.Context(), &protocol.PrepareMetadataRequest{
		Scope: metadataScope(session, protocol.MetadataScope_LOG, "schema_migrations_log"), RequestId: "log"})
	c.Assert(err, qt.IsNil)
	c.Assert(prepared.GetOutcome(), qt.Equals, protocol.MetadataResult_APPLIED)

	c.Assert(p.Close(c.Context(), &protocol.CloseRequest{SessionId: session.GetId()}), qt.IsNil)

	snapshot, err := inspectAs(c, readerFeatures(), fileAddress(path))
	c.Assert(err, qt.IsNil)
	var tables, reserved []string
	for _, table := range snapshot.GetTables() {
		tables = append(tables, table.GetIdentity().GetName())
	}
	for _, object := range snapshot.GetReservedObjects() {
		reserved = append(reserved, object.GetObject().GetName()+" "+object.GetPurpose().String())
	}
	c.Assert(tables, qt.DeepEquals, []string{"ptah_migration_tags"})
	c.Assert(reserved, qt.DeepEquals, []string{"custom_revisions HISTORY", "schema_migrations HISTORY", "schema_migrations_log LOG"})
}
