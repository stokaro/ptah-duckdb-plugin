//go:build integration

package integration_test

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
	qt "github.com/frankban/quicktest"
	"google.golang.org/grpc/codes"

	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbversion"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// openFile opens path in registry and closes the handle when the test ends.
func openFile(c *qt.C, registry *instances.Registry, path string, access instances.Access) *instances.Handle {
	c.Helper()
	handle, err := registry.OpenFile(c.Context(), path, access)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = handle.Close() })
	return handle
}

// run runs statements on a handle's connection.
func run(c *qt.C, handle *instances.Handle, statements ...string) {
	c.Helper()
	for _, statement := range statements {
		_, err := handle.Conn().ExecContext(c.Context(), statement)
		c.Assert(err, qt.IsNil, qt.Commentf("statement: %s", statement))
	}
}

// count reads one integer.
func count(c *qt.C, handle *instances.Handle, query string) int {
	c.Helper()
	var n int
	c.Assert(handle.Conn().QueryRowContext(c.Context(), query).Scan(&n), qt.IsNil, qt.Commentf("query: %s", query))
	return n
}

func TestOpenFile_CreatesAMissingFile(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "new.duckdb")
	var registry instances.Registry
	handle := openFile(c, &registry, path, instances.ReadWrite)
	c.Assert(handle.Access(), qt.Equals, instances.ReadWrite)
	run(c, handle, "CREATE TABLE t(id INTEGER)")
	_, err := os.Stat(path)
	c.Assert(err, qt.IsNil)
}

func TestOpenFile_ReadsTheLinkedLibrary(t *testing.T) {
	c := testcheck.New(t)
	var registry instances.Registry
	handle := openFile(c, &registry, filepath.Join(c.TempDir(), "v.duckdb"), instances.ReadWrite)
	var version string
	c.Assert(handle.Conn().QueryRowContext(c.Context(), "SELECT library_version FROM pragma_version()").Scan(&version), qt.IsNil)
	c.Assert(version, qt.Equals, duckdbversion.Library)
}

// The sessions of one file share one database: what one session writes the
// other reads, whichever name of the file each opened.
func TestOpenFile_SessionsShareOneDatabase(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "shared.duckdb")
	var registry instances.Registry
	first := openFile(c, &registry, path, instances.ReadWrite)
	run(c, first, "CREATE TABLE t(id INTEGER)", "INSERT INTO t VALUES (1), (2)")
	c.Assert(os.Link(path, filepath.Join(filepath.Dir(path), "hardlink.duckdb")), qt.IsNil)

	second := openFile(c, &registry, path, instances.ReadWrite)
	third := openFile(c, &registry, filepath.Join(filepath.Dir(path), "sub", "..", "shared.duckdb"), instances.ReadWrite)
	fourth := openFile(c, &registry, filepath.Join(filepath.Dir(path), "hardlink.duckdb"), instances.ReadWrite)
	c.Assert(count(c, second, "SELECT count(*) FROM t"), qt.Equals, 2)
	c.Assert(count(c, third, "SELECT count(*) FROM t"), qt.Equals, 2)
	c.Assert(count(c, fourth, "SELECT count(*) FROM t"), qt.Equals, 2)
}

// DuckDB releases a file only when its instance closes. Opening the file
// again in this process with another configuration succeeds only once no
// instance of it is left, so it tells an instance closed from one kept open.
func TestOpenFile_ReleasesTheFileWithTheLastSession(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "release.duckdb")
	var registry instances.Registry
	first, err := registry.OpenFile(c.Context(), path, instances.ReadWrite)
	c.Assert(err, qt.IsNil)
	second, err := registry.OpenFile(c.Context(), path, instances.ReadWrite)
	c.Assert(err, qt.IsNil)

	c.Assert(first.Close(), qt.IsNil)
	c.Assert(first.Close(), qt.IsNil)
	c.Assert(directPing(c, path), qt.ErrorMatches, `.*different configuration.*`)

	c.Assert(second.Close(), qt.IsNil)
	c.Assert(directPing(c, path), qt.IsNil)
}

// directPing opens path read-only in this process, outside the registry,
// and reports whether DuckDB let it.
func directPing(c *qt.C, path string) error {
	c.Helper()
	connector, err := duckdb.NewConnector(path+"?access_mode=read_only", nil)
	if err == nil {
		c.Assert(connector.Close(), qt.IsNil)
	}
	return err
}

func TestOpenFile_ModeConflict_FailurePath(t *testing.T) {
	tests := []struct {
		name   string
		held   instances.Access
		asked  instances.Access
		reason string
	}{
		{"read-only after read-write", instances.ReadWrite, instances.ReadOnly,
			"another session of this provider has the database file open read-write; DuckDB opens one file in one access mode per process"},
		{"read-write after read-only", instances.ReadOnly, instances.ReadWrite,
			"another session of this provider has the database file open read-only; DuckDB opens one file in one access mode per process"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			path := createDatabase(c, "mode.duckdb")
			var registry instances.Registry
			openFile(c, &registry, path, tc.held)
			handle, err := registry.OpenFile(c.Context(), path, tc.asked)
			var refused *instances.Refusal
			c.Assert(err, qt.ErrorAs, &refused)
			c.Assert(refused.Code, qt.Equals, codes.FailedPrecondition)
			c.Assert(refused.Reason, qt.Equals, tc.reason)
			c.Assert(handle, qt.IsNil)
		})
	}
}

func TestOpenFile_ReadOnly(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "ro.duckdb")
	var registry instances.Registry
	first := openFile(c, &registry, path, instances.ReadOnly)
	second := openFile(c, &registry, path, instances.ReadOnly)
	c.Assert(first.Access(), qt.Equals, instances.ReadOnly)
	c.Assert(count(c, second, "SELECT count(*) FROM seeded"), qt.Equals, 0)
	_, err := first.Conn().ExecContext(c.Context(), "CREATE TABLE refused(id INTEGER)")
	c.Assert(err, qt.ErrorMatches, `.*read-only mode.*`)
}

// assertRefused asserts that err is a refusal with code and reason.
func assertRefused(c *qt.C, err error, code codes.Code, reason string) {
	c.Helper()
	var refused *instances.Refusal
	c.Assert(err, qt.ErrorAs, &refused)
	c.Assert(refused.Code, qt.Equals, code)
	c.Assert(refused.Reason, qt.Equals, reason)
}

// Paths the registry refuses before or instead of creating anything.
func TestOpenFilePath_FailurePath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		access instances.Access
		code   codes.Code
		reason string
	}{
		{"missing file read-only", "missing.duckdb", instances.ReadOnly, codes.NotFound,
			"the database file does not exist, and a read-only open does not create it"},
		{"missing directory", filepath.Join("nowhere", "app.duckdb"), instances.ReadWrite, codes.NotFound,
			"the directory that would hold the database file does not exist"},
		{"the directory itself", ".", instances.ReadWrite, codes.InvalidArgument,
			"the address names a directory, not a database file"},
		{"number sign in the path", "issue#1.duckdb", instances.ReadWrite, codes.InvalidArgument,
			"the DuckDB driver cannot open a file whose path contains a number sign"},
		{"question mark in the path", "what?.duckdb", instances.ReadWrite, codes.InvalidArgument,
			"the DuckDB driver cannot open a file whose path contains a question mark"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			dir := c.TempDir()
			var registry instances.Registry
			handle, err := registry.OpenFile(c.Context(), filepath.Join(dir, tc.path), tc.access)
			assertRefused(c, err, tc.code, tc.reason)
			c.Assert(handle, qt.IsNil)
			entries, err := os.ReadDir(dir)
			c.Assert(err, qt.IsNil)
			c.Assert(entries, qt.HasLen, 0)
		})
	}
}

func TestOpenFileRelativePath_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	var registry instances.Registry
	handle, err := registry.OpenFile(c.Context(), "app.duckdb", instances.ReadWrite)
	assertRefused(c, err, codes.InvalidArgument, "the provider opens a database file by its absolute path only")
	c.Assert(handle, qt.IsNil)
}

func TestOpenFileInsideAFile_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	plain := filepath.Join(c.TempDir(), "plain.txt")
	c.Assert(os.WriteFile(plain, []byte("x"), 0o600), qt.IsNil)
	var registry instances.Registry
	handle, err := registry.OpenFile(c.Context(), filepath.Join(plain, "app.duckdb"), instances.ReadWrite)
	assertRefused(c, err, codes.InvalidArgument, "the database file would be created inside something that is not a directory")
	c.Assert(handle, qt.IsNil)
}

// A file that is not a DuckDB database is refused by what it holds, and is
// left as it was.
func TestOpenFileContents_FailurePath(t *testing.T) {
	tests := []struct {
		name     string
		contents []byte
		reason   string
	}{
		{"empty file", make([]byte, 0), "the file is empty, so it is not a DuckDB database"},
		{"sqlite file", append([]byte("SQLite format 3\x00"), make([]byte, 4096)...), "the file is a SQLite database, not a DuckDB database"},
		{"text file", []byte("these are notes, not a database\n"), "the file is not a DuckDB database"},
		{"short file", []byte("DUCK"), "the file is not a DuckDB database"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			path := filepath.Join(c.TempDir(), "file.duckdb")
			c.Assert(os.WriteFile(path, tc.contents, 0o600), qt.IsNil)
			var registry instances.Registry
			handle, err := registry.OpenFile(c.Context(), path, instances.ReadWrite)
			assertRefused(c, err, codes.FailedPrecondition, tc.reason)
			c.Assert(handle, qt.IsNil)
			after, err := os.ReadFile(path) // #nosec G304 -- a file this test wrote
			c.Assert(err, qt.IsNil)
			c.Assert(after, qt.DeepEquals, tc.contents)
		})
	}
}

// The DuckDB header carries a checksum over its block, so changing a byte of
// the block makes the file corrupt.
func TestOpenFileCorrupt_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "corrupt.duckdb")
	contents, err := os.ReadFile(path) // #nosec G304 -- a file this test created
	c.Assert(err, qt.IsNil)
	binary.LittleEndian.PutUint64(contents[12:20], 100000)
	c.Assert(os.WriteFile(path, contents, 0o600), qt.IsNil) // #nosec G703 -- a file this test created in its temporary directory
	var registry instances.Registry
	handle, err := registry.OpenFile(c.Context(), path, instances.ReadWrite)
	assertRefused(c, err, codes.FailedPrecondition, "the database file is corrupt: its checksum does not match its content")
	c.Assert(handle, qt.IsNil)
}

// openMemory opens the in-memory database name in registry.
func openMemory(c *qt.C, registry *instances.Registry, name string) *instances.Handle {
	c.Helper()
	handle, err := registry.OpenMemory(c.Context(), name)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = handle.Close() })
	return handle
}

// Every database file opens with the settings the provider fixes, read back
// from DuckDB itself.
func TestSettings_HoldOnEveryFile(t *testing.T) {
	for _, access := range []instances.Access{instances.ReadWrite, instances.ReadOnly} {
		t.Run(fmt.Sprintf("access %d", access), func(t *testing.T) {
			c := testcheck.New(t)
			var registry instances.Registry
			handle := openFile(c, &registry, createDatabase(c, "settings.duckdb"), access)
			c.Assert(currentSettings(c, handle, access), qt.DeepEquals, instances.Settings(access))
		})
	}
}

func TestSettings_HoldOnAnInMemoryDatabase(t *testing.T) {
	c := testcheck.New(t)
	var registry instances.Registry
	handle := openMemory(c, &registry, "settings")
	c.Assert(currentSettings(c, handle, instances.ReadWrite), qt.DeepEquals, instances.Settings(instances.ReadWrite))
}

// currentSettings reads the value DuckDB holds for each setting the provider
// fixes.
func currentSettings(c *qt.C, handle *instances.Handle, access instances.Access) []instances.Setting {
	c.Helper()
	var held []instances.Setting
	for _, setting := range instances.Settings(access) {
		var value any
		err := handle.Conn().QueryRowContext(c.Context(), "SELECT current_setting($1)", setting.Name).Scan(&value)
		c.Assert(err, qt.IsNil, qt.Commentf("setting %s", setting.Name))
		held = append(held, instances.Setting{Name: setting.Name, Value: fmt.Sprint(value)})
	}
	return held
}
