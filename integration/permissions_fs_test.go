//go:build integration && !windows

package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	"google.golang.org/grpc/codes"

	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// A file the process may read and not write opens read-only and is refused
// read-write, by name. Unix file modes are what this test changes; Windows
// grants access through ACLs, which it does not set up.
func TestOpenFileReadOnlyPermissions(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "locked-down.duckdb")
	c.Assert(os.Chmod(path, 0o400), qt.IsNil)
	c.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	var registry instances.Registry
	handle, err := registry.OpenFile(c.Context(), path, instances.ReadWrite)
	assertRefused(c, err, codes.PermissionDenied,
		"the provider may not write the database file or its directory; access_mode=read_only opens it for reading")
	c.Assert(handle, qt.IsNil)
	reader := openFile(c, &registry, path, instances.ReadOnly)
	c.Assert(count(c, reader, "SELECT count(*) FROM seeded"), qt.Equals, 0)
}

func TestOpenFileUnreadable_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "unreadable.duckdb")
	c.Assert(os.Chmod(path, 0o200), qt.IsNil)
	c.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	var registry instances.Registry
	handle, err := registry.OpenFile(c.Context(), path, instances.ReadOnly)
	assertRefused(c, err, codes.PermissionDenied, "the provider may not read the database file")
	c.Assert(handle, qt.IsNil)
}

// A symbolic link reaches the same database as the file it points to.
func TestOpenFileThroughASymbolicLink(t *testing.T) {
	c := testcheck.New(t)
	path := createDatabase(c, "target.duckdb")
	link := filepath.Join(c.TempDir(), "link.duckdb")
	c.Assert(os.Symlink(path, link), qt.IsNil)
	var registry instances.Registry
	direct := openFile(c, &registry, path, instances.ReadWrite)
	run(c, direct, "INSERT INTO seeded VALUES (7)")
	linked := openFile(c, &registry, link, instances.ReadWrite)
	c.Assert(count(c, linked, "SELECT count(*) FROM seeded"), qt.Equals, 1)
	_, err := registry.OpenFile(c.Context(), link, instances.ReadOnly)
	assertRefused(c, err, codes.FailedPrecondition,
		"another session of this provider has the database file open read-write; DuckDB opens one file in one access mode per process")
}
