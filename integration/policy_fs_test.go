//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// A statement cannot reach a file, the network, another database or an
// extension the binary does not link, and cannot lift the setting that
// forbids it. Each statement is refused, and none leaves a file behind.
func TestPolicy_RefusesExternalAccess(t *testing.T) {
	setup := testcheck.New(t)
	dir := setup.TempDir()
	outside := filepath.Join(dir, "outside.csv")
	setup.Assert(os.WriteFile(outside, []byte("a\n1\n"), 0o600), qt.IsNil)
	tests := []struct {
		name      string
		statement string
	}{
		{"copy to a file", "COPY (SELECT 1) TO '" + filepath.ToSlash(filepath.Join(dir, "copied.csv")) + "'"},
		{"read a local file", "SELECT * FROM read_csv('" + filepath.ToSlash(outside) + "')"},
		{"attach another database", "ATTACH '" + filepath.ToSlash(filepath.Join(dir, "other.duckdb")) + "' AS other"},
		{"read over the network", "SELECT * FROM read_csv('https://example.invalid/data.csv')"},
		{"load an extension", "LOAD spatial"},
		{"install an extension", "INSTALL spatial"},
		{"call an extension function", "SELECT st_point(1, 2)"},
		{"lift the setting", "SET enable_external_access = true"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			var registry instances.Registry
			handle := openMemory(c, &registry, "policy")
			_, err := handle.Conn().ExecContext(c.Context(), tc.statement)
			c.Assert(err, qt.IsNotNil)
			entries, err := os.ReadDir(dir)
			c.Assert(err, qt.IsNil)
			c.Assert(entries, qt.HasLen, 1)
			c.Assert(entries[0].Name(), qt.Equals, "outside.csv")
		})
	}
}

// The extensions linked into the binary keep working: JSON, ICU's time
// zones and the core functions. duckdb_extensions() lists the extension
// directory as well as the linked extensions, so the policy refuses it.
func TestPolicy_KeepsTheLinkedExtensions(t *testing.T) {
	c := testcheck.New(t)
	var registry instances.Registry
	handle := openMemory(c, &registry, "linked")
	var extracted, zoned string
	var summed int
	c.Assert(handle.Conn().QueryRowContext(c.Context(), `SELECT json_extract_string('{"a":"b"}', '$.a')`).Scan(&extracted), qt.IsNil)
	c.Assert(extracted, qt.Equals, "b")
	c.Assert(handle.Conn().QueryRowContext(c.Context(),
		`SELECT strftime(timezone('Asia/Tokyo', TIMESTAMPTZ '2026-01-01 00:00:00+00'), '%H')`).Scan(&zoned), qt.IsNil)
	c.Assert(zoned, qt.Equals, "09")
	c.Assert(handle.Conn().QueryRowContext(c.Context(), `SELECT list_sum([1, 2, 3])`).Scan(&summed), qt.IsNil)
	c.Assert(summed, qt.Equals, 6)
	_, err := handle.Conn().ExecContext(c.Context(), "SELECT * FROM duckdb_extensions()")
	c.Assert(err, qt.ErrorMatches, `(?s).*file system operations are disabled by configuration.*`)
}

// An in-memory database lives as long as the process: every session of one
// name reaches it, after the others closed too, and another name is another
// database.
func TestOpenMemory_LivesForTheProcess(t *testing.T) {
	c := testcheck.New(t)
	var registry instances.Registry
	first, err := registry.OpenMemory(c.Context(), "")
	c.Assert(err, qt.IsNil)
	run(c, first, "CREATE TABLE kept(id INTEGER)", "INSERT INTO kept VALUES (1)")
	second := openMemory(c, &registry, "")
	c.Assert(count(c, second, "SELECT count(*) FROM kept"), qt.Equals, 1)
	c.Assert(first.Close(), qt.IsNil)
	c.Assert(second.Close(), qt.IsNil)

	again := openMemory(c, &registry, "")
	c.Assert(count(c, again, "SELECT count(*) FROM kept"), qt.Equals, 1)
	other := openMemory(c, &registry, "other")
	c.Assert(count(c, other, "SELECT count(*) FROM duckdb_tables() WHERE table_name = 'kept'"), qt.Equals, 0)
	fresh := openMemory(c, &instances.Registry{}, "")
	c.Assert(count(c, fresh, "SELECT count(*) FROM duckdb_tables() WHERE table_name = 'kept'"), qt.Equals, 0)
}
