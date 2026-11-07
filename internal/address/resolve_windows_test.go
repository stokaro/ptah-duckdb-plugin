//go:build windows

package address_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/address"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// Windows reads a drive path and a UNC path as absolute, so neither is joined
// to the directory.
func TestResolveWindowsAbsolute_HappyPath(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{"drive with slashes", "duckdb://C:/data/app.duckdb", `C:\data\app.duckdb`},
		{"drive after a slash", "duckdb:///D:/data/app.duckdb", `D:\data\app.duckdb`},
		{"drive with backslashes", `duckdb://C:\data\app.duckdb`, `C:\data\app.duckdb`},
		{"drive with a space", "duckdb://C:/Program%20Files/app.duckdb", `C:\Program Files\app.duckdb`},
		{"UNC path", `duckdb://\\server\share\app.duckdb`, `\\server\share\app.duckdb`},
		{"UNC path with slashes", "duckdb:////server/share/app.duckdb", `\\server\share\app.duckdb`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			target, err := address.Parse(tc.address)
			c.Assert(err, qt.IsNil)
			got, err := address.Resolve(target, `E:\work`, "windows")
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}
