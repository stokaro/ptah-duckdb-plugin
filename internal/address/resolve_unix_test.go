//go:build !windows

package address_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/address"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// A path that starts with a slash is absolute on Unix and is never joined to
// the directory. Two leading slashes name the same file as one.
func TestResolveUnixAbsolute_HappyPath(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{"absolute file", "duckdb:///var/lib/app.duckdb", "/var/lib/app.duckdb"},
		{"double slash", "duckdb:////var/lib/app.duckdb", "/var/lib/app.duckdb"},
		{"dot segments", "duckdb:///var/lib/../app.duckdb", "/var/app.duckdb"},
		{"backslash is part of the name", `duckdb://dir\app.duckdb`, `/work/dir\app.duckdb`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			target, err := address.Parse(tc.address)
			c.Assert(err, qt.IsNil)
			got, err := address.Resolve(target, "/work", "linux")
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}
