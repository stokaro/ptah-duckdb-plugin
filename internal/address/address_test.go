package address_test

import (
	"errors"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/address"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestParse_HappyPath(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    address.Target
	}{
		{"relative file", "duckdb://app.duckdb", address.Target{Kind: address.File, Path: "app.duckdb"}},
		{"relative file in a directory", "duckdb://data/app.duckdb", address.Target{Kind: address.File, Path: "data/app.duckdb"}},
		{"relative file in the parent directory", "duckdb://../app.duckdb", address.Target{Kind: address.File, Path: "../app.duckdb"}},
		{"dot-relative file", "duckdb://./app.duckdb", address.Target{Kind: address.File, Path: "./app.duckdb"}},
		{"absolute file", "duckdb:///var/lib/app.duckdb", address.Target{Kind: address.File, Path: "/var/lib/app.duckdb"}},
		{"upper-case scheme", "DuckDB:///var/lib/app.duckdb", address.Target{Kind: address.File, Path: "/var/lib/app.duckdb"}},
		{"escaped space", "duckdb:///srv/my%20data/app.duckdb", address.Target{Kind: address.File, Path: "/srv/my data/app.duckdb"}},
		{"literal space", "duckdb:///srv/my data/app.duckdb", address.Target{Kind: address.File, Path: "/srv/my data/app.duckdb"}},
		{"escaped question mark", "duckdb://what%3F.duckdb", address.Target{Kind: address.File, Path: "what?.duckdb"}},
		{"escaped number sign", "duckdb://issue%2342.duckdb", address.Target{Kind: address.File, Path: "issue#42.duckdb"}},
		{"plus is literal", "duckdb://a+b.duckdb", address.Target{Kind: address.File, Path: "a+b.duckdb"}},
		{"at sign is part of the path", "duckdb://user@host.duckdb", address.Target{Kind: address.File, Path: "user@host.duckdb"}},
		{"non-ASCII path", "duckdb://données/äpp.duckdb", address.Target{Kind: address.File, Path: "données/äpp.duckdb"}},
		{"windows drive with slashes", "duckdb://C:/data/app.duckdb", address.Target{Kind: address.File, Path: "C:/data/app.duckdb"}},
		{"windows drive after a slash", "duckdb:///C:/data/app.duckdb", address.Target{Kind: address.File, Path: "C:/data/app.duckdb"}},
		{"windows drive with backslashes", `duckdb://d:\data\app.duckdb`, address.Target{Kind: address.File, Path: `d:\data\app.duckdb`}},
		{"windows drive with escaped backslashes", "duckdb://C:%5Cdata%5Capp.duckdb", address.Target{Kind: address.File, Path: `C:\data\app.duckdb`}},
		{"windows UNC path", `duckdb://\\server\share\app.duckdb`, address.Target{Kind: address.File, Path: `\\server\share\app.duckdb`}},
		{"read-only file", "duckdb://app.duckdb?access_mode=read_only", address.Target{Kind: address.File, Path: "app.duckdb", ReadOnly: true}},
		{"read-write file", "duckdb://app.duckdb?access_mode=read_write", address.Target{Kind: address.File, Path: "app.duckdb"}},
		{"empty query", "duckdb://app.duckdb?", address.Target{Kind: address.File, Path: "app.duckdb"}},
		{"in-memory database", "duckdb://:memory:", address.Target{Kind: address.Memory}},
		{"named in-memory database", "duckdb://:memory:scratch_1", address.Target{Kind: address.Memory, MemoryName: "scratch_1"}},
		{"in-memory read-write", "duckdb://:memory:?access_mode=read_write", address.Target{Kind: address.Memory}},
		{"file called memory in a directory", "duckdb://./:memory:", address.Target{Kind: address.File, Path: "./:memory:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := address.Parse(tc.address)
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}

func TestParse_FailurePath(t *testing.T) {
	tests := []struct {
		name    string
		address string
		reason  string
	}{
		{"other scheme", "sqlite://app.db", "the address does not start with duckdb://"},
		{"no scheme", "app.duckdb", "the address does not start with duckdb://"},
		{"opaque form", "duckdb:app.duckdb", "the address does not start with duckdb://"},
		{"empty", "", "the address does not start with duckdb://"},
		{"no database", "duckdb://", "the address names no database; write duckdb://<path> or duckdb://:memory:"},
		{"only options", "duckdb://?access_mode=read_only", "the address names no database; write duckdb://<path> or duckdb://:memory:"},
		{"fragment", "duckdb://app.duckdb#x", "the address carries a fragment; write %23 for a # in a file name"},
		{"malformed escape", "duckdb://100%.duckdb", "the path of the address has a malformed percent escape"},
		{"control character", "duckdb://app%00.duckdb", "the path of the address holds a control character"},
		{"newline", "duckdb://app%0A.duckdb", "the path of the address holds a control character"},
		{"memory in the root", "duckdb:///:memory:", "the address names a file called :memory: in the root directory; write duckdb://:memory: for an in-memory database"},
		{"drive-relative windows path", "duckdb://C:app.duckdb", "the address names a path relative to the current directory of a Windows drive; give an absolute path such as C:/data/app.duckdb"},
		{"bad memory name", "duckdb://:memory:a.b", "an in-memory database name is 1 to 64 letters, digits, underscores and dashes"},
		{"read-only memory", "duckdb://:memory:?access_mode=read_only", "an in-memory database cannot be opened read-only; DuckDB refuses it"},
		{"unknown option", "duckdb://app.duckdb?threads=4", "the address carries an option the provider does not read; it reads access_mode only"},
		{"unknown access mode", "duckdb://app.duckdb?access_mode=automatic", "access_mode is read_only or read_write"},
		{"empty access mode", "duckdb://app.duckdb?access_mode=", "access_mode is read_only or read_write"},
		{"conflicting access modes", "duckdb://app.duckdb?access_mode=read_only&access_mode=read_write", "the address gives access_mode more than once"},
		{"repeated access mode", "duckdb://app.duckdb?access_mode=read_only&access_mode=read_only", "the address gives access_mode more than once"},
		{"malformed query", "duckdb://app.duckdb?access_mode=read_only;x=1", "the options of the address are malformed"},
		{"autoload", "duckdb://app.duckdb?autoload_known_extensions=true", "the address sets autoload_known_extensions, which the provider fixes for every database"},
		{"autoinstall", "duckdb://app.duckdb?autoinstall_known_extensions=true", "the address sets autoinstall_known_extensions, which the provider fixes for every database"},
		{"external access", "duckdb://app.duckdb?enable_external_access=true", "the address sets enable_external_access, which the provider fixes for every database"},
		{"extension directory", "duckdb://app.duckdb?extension_directory=x", "the address sets extension_directory, which the provider fixes for every database"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := address.Parse(tc.address)
			c.Assert(err, qt.ErrorIs, address.ErrInvalid)
			c.Assert(address.Reason(err), qt.Equals, tc.reason)
			c.Assert(got, qt.Equals, address.Target{})
		})
	}
}

// No refusal quotes the address: an address can carry a credential, and a
// reason reaches the operator's terminal.
func TestParse_RefusalsQuoteNothing(t *testing.T) {
	addresses := []string{
		"duckdb://secret-ish.duckdb?threads=hunter2",
		"duckdb://secret-ish.duckdb?access_mode=hunter2",
		"duckdb://secret-ish%zz.duckdb",
		"duckdb://secret-ish.duckdb#hunter2",
	}
	for _, given := range addresses {
		t.Run(given, func(t *testing.T) {
			c := testcheck.New(t)
			_, err := address.Parse(given)
			c.Assert(err, qt.ErrorIs, address.ErrInvalid)
			c.Assert(err.Error(), qt.Not(qt.Contains), "hunter2")
			c.Assert(err.Error(), qt.Not(qt.Contains), "secret-ish")
		})
	}
}

func TestReason_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	c.Assert(address.Reason(nil), qt.Equals, "")
	c.Assert(address.Reason(errors.New("other")), qt.Equals, "")
}
