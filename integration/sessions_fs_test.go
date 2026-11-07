//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbversion"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// openSession opens address on p and closes the session when the test ends.
func openSession(c *qt.C, p *backend.Provider, address string) *protocol.Session {
	c.Helper()
	session, err := p.Open(c.Context(), openRequest(address))
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = p.Close(c.Context(), &protocol.CloseRequest{SessionId: session.GetId()}) })
	return session
}

func TestOpen_FileSession(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "app.duckdb")
	p := backend.New()
	first := openSession(c, p, fileAddress(path))
	c.Assert(first.GetEngine(), qt.Equals, "duckdb")
	c.Assert(first.GetDatabase(), qt.Equals, "app")
	c.Assert(first.GetDefaultSchema(), qt.Equals, "main")
	c.Assert(first.GetServerVersion(), qt.Equals, duckdbversion.Library)
	c.Assert(first.GetId(), qt.Not(qt.Equals), "")
	// No host feature is negotiated here, so every two-way feature is left
	// out of the session's features.
	c.Assert(first.GetFeatures(), qt.DeepEquals, []string{
		provider.InspectFeature, provider.RenderFeature, provider.ExecuteFeature, provider.AnalyzeSQLFeature, provider.ValidateSchemaFeature,
		provider.PrepareTablesFeature, provider.ProjectTableCreationsFeature, provider.PlanFeaturesFeature, provider.CheckEquivalenceFeature,
		provider.LocksFeature, provider.TransactionsFeature, provider.NormalizeValuesFeature,
	})

	second := openSession(c, p, fileAddress(path))
	c.Assert(second.GetId(), qt.Not(qt.Equals), first.GetId())
	c.Assert(p.Close(c.Context(), &protocol.CloseRequest{SessionId: first.GetId()}), qt.IsNil)
	c.Assert(p.Close(c.Context(), &protocol.CloseRequest{SessionId: first.GetId()}), qt.IsNil)
	_, err := os.Stat(path)
	c.Assert(err, qt.IsNil)
}

func TestOpen_InMemorySession(t *testing.T) {
	c := testcheck.New(t)
	session := openSession(c, backend.New(), "duckdb://:memory:")
	c.Assert(session.GetDatabase(), qt.Equals, "memory")
	c.Assert(session.GetServerVersion(), qt.Equals, duckdbversion.Library)
}

// A relative path resolves against the working directory of the provider
// process; e2e tests show it is the one the host started it in.
func TestOpen_RelativePathResolvesAgainstTheWorkingDirectory(t *testing.T) {
	c := testcheck.New(t)
	dir := c.TempDir()
	c.Assert(os.Mkdir(filepath.Join(dir, "data dir"), 0o750), qt.IsNil)
	t.Chdir(dir)
	session := openSession(c, backend.New(), "duckdb://data%20dir/rel.duckdb")
	c.Assert(session.GetDatabase(), qt.Equals, "rel")
	_, err := os.Stat(filepath.Join(dir, "data dir", "rel.duckdb"))
	c.Assert(err, qt.IsNil)
}

func TestOpen_FailurePath(t *testing.T) {
	tests := []struct {
		name    string
		address string
		code    codes.Code
		reason  string
	}{
		{"missing file read-only", "missing.duckdb?access_mode=read_only", codes.NotFound,
			"the database file does not exist, and a read-only open does not create it"},
		{"missing directory", "nowhere/app.duckdb", codes.NotFound,
			"the directory that would hold the database file does not exist"},
		{"number sign", "issue%231.duckdb", codes.InvalidArgument,
			"the DuckDB driver cannot open a file whose path contains a number sign"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			dir := c.TempDir()
			session, err := backend.New().Open(c.Context(), openRequest(fileAddress(dir)+"/"+tc.address))
			c.Assert(status.Code(err), qt.Equals, tc.code)
			c.Assert(status.Convert(err).Message(), qt.Equals, tc.reason)
			c.Assert(session, qt.IsNil)
		})
	}
}

// The capabilities a session states hold on the embedded DuckDB: schema
// statements run inside a transaction and roll back with it.
func TestSessionCapabilities_MatchTheEngine(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "caps.duckdb")
	session := openSession(c, backend.New(), fileAddress(path))
	c.Assert(session.GetCapabilities(), qt.DeepEquals, map[string]bool{"transactional_ddl": true, "ddl_inside_transaction": true})

	var registry instances.Registry
	handle := openFile(c, &registry, path, instances.ReadWrite)
	run(c, handle,
		"BEGIN TRANSACTION",
		"CREATE TABLE inside(id INTEGER)",
		"ALTER TABLE seeded ADD COLUMN extra VARCHAR",
		"CREATE INDEX inside_id ON inside(id)",
		"ROLLBACK",
	)
	c.Assert(count(c, handle, "SELECT count(*) FROM duckdb_tables() WHERE table_name = 'inside'"), qt.Equals, 0)
	c.Assert(count(c, handle, "SELECT count(*) FROM duckdb_columns() WHERE table_name = 'seeded'"), qt.Equals, 1)
	run(c, handle, "BEGIN TRANSACTION", "DROP TABLE seeded", "ROLLBACK")
	c.Assert(count(c, handle, "SELECT count(*) FROM seeded"), qt.Equals, 0)
}

// The identifier rules a session states hold on the embedded DuckDB.
func TestIdentifierRules_MatchTheEngine(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "names.duckdb")
	session := openSession(c, backend.New(), fileAddress(path))
	c.Assert(session.GetIdentifiers().GetTableNames(), qt.Equals, protocol.IdentifierRules_ASCII_CASE_INSENSITIVE)
	c.Assert(session.GetIdentifiers().GetColumnNames(), qt.Equals, protocol.IdentifierRules_ASCII_CASE_INSENSITIVE)
	c.Assert(session.GetIdentifiers().GetIndexNames(), qt.Equals, protocol.IdentifierRules_ASCII_CASE_INSENSITIVE)
	c.Assert(session.GetIdentifiers().GetIndexNamespace(), qt.Equals, protocol.IdentifierRules_SCHEMA)

	var registry instances.Registry
	handle := openFile(c, &registry, path, instances.ReadWrite)
	run(c, handle,
		`CREATE TABLE "Mixed"("Col" INTEGER)`,
		`CREATE TABLE "Ä"(x INTEGER)`,
		"CREATE TABLE other(x INTEGER)",
		"CREATE SCHEMA second",
		"CREATE TABLE second.t(x INTEGER)",
		"CREATE INDEX ix ON Mixed(col)",
		"CREATE INDEX ix ON second.t(x)",
	)
	// ASCII letters fold, quoted or not.
	c.Assert(count(c, handle, `SELECT count("COL") FROM "MIXED"`), qt.Equals, 0)
	// Other letters do not.
	_, err := handle.Conn().ExecContext(c.Context(), `SELECT * FROM "ä"`)
	c.Assert(err, qt.ErrorMatches, `(?s).*Table with name ä does not exist.*`)
	// An index name is taken in its schema, whatever the table.
	_, err = handle.Conn().ExecContext(c.Context(), "CREATE INDEX IX ON other(x)")
	c.Assert(err, qt.ErrorMatches, `(?s).*Index with name "IX" already exists.*`)
}
