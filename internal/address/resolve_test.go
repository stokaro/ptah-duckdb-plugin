package address_test

import (
	"path/filepath"
	"runtime"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/address"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The joins run on the platform the test runs on, so each expected path is
// built with the same platform's filepath: the assertion holds the same
// meaning on Windows and on Unix.
func TestResolve_HappyPath(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"relative file", "app.duckdb", filepath.Join(dir, "app.duckdb")},
		{"relative file in a directory", "data/app.duckdb", filepath.Join(dir, "data", "app.duckdb")},
		{"parent directory", "../app.duckdb", filepath.Join(filepath.Dir(dir), "app.duckdb")},
		{"dot segments", "./data/../app.duckdb", filepath.Join(dir, "app.duckdb")},
		{"space in the name", "my data/app.duckdb", filepath.Join(dir, "my data", "app.duckdb")},
		{"the directory itself is absolute", filepath.ToSlash(filepath.Join(dir, "abs.duckdb")), filepath.Join(dir, "abs.duckdb")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := address.Resolve(address.Target{Kind: address.File, Path: tc.path}, dir, runtime.GOOS)
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}

// The refusals depend on the platform a path is meant for, never on the
// machine reading it, so both platforms' rows run everywhere.
func TestResolve_FailurePath(t *testing.T) {
	tests := []struct {
		name   string
		target address.Target
		dir    string
		goos   string
		reason string
	}{
		{"drive path on linux", address.Target{Kind: address.File, Path: "C:/data/app.duckdb"}, "/work", "linux",
			"the address names a Windows drive path, and the provider runs on linux"},
		{"drive path with backslashes on darwin", address.Target{Kind: address.File, Path: `C:\data\app.duckdb`}, "/work", "darwin",
			"the address names a Windows drive path, and the provider runs on darwin"},
		{"rooted path on windows", address.Target{Kind: address.File, Path: "/var/lib/app.duckdb"}, `C:\work`, "windows",
			"the address names a path that starts at the root of no drive; give a drive, such as C:/data/app.duckdb"},
		{"rooted backslash path on windows", address.Target{Kind: address.File, Path: `\data\app.duckdb`}, `C:\work`, "windows",
			"the address names a path that starts at the root of no drive; give a drive, such as C:/data/app.duckdb"},
		{"memory target", address.Target{Kind: address.Memory}, "/work", "linux", "the address names no database file"},
		{"relative path with a relative directory", address.Target{Kind: address.File, Path: "app.duckdb"}, "work", runtime.GOOS,
			"the provider has no absolute directory to resolve a relative path against"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := address.Resolve(tc.target, tc.dir, tc.goos)
			c.Assert(err, qt.ErrorIs, address.ErrInvalid)
			c.Assert(address.Reason(err), qt.Equals, tc.reason)
			c.Assert(got, qt.Equals, "")
		})
	}
}
