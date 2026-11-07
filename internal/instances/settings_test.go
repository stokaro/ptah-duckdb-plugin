package instances_test

import (
	"runtime/debug"
	"testing"

	qt "github.com/frankban/quicktest"
	"google.golang.org/grpc/codes"

	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbversion"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

const policy = "autoinstall_known_extensions=false&autoload_known_extensions=false&allow_community_extensions=false" +
	"&allow_unsigned_extensions=false&allow_persistent_secrets=false&enable_external_access=false"

func TestDSN_HappyPath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		access instances.Access
		want   string
	}{
		{"read-write file", "/srv/app.duckdb", instances.ReadWrite, "/srv/app.duckdb?access_mode=read_write&" + policy},
		{"read-only file", "/srv/app.duckdb", instances.ReadOnly, "/srv/app.duckdb?access_mode=read_only&" + policy},
		{"space in the path", "/srv/my data/app.duckdb", instances.ReadWrite, "/srv/my data/app.duckdb?access_mode=read_write&" + policy},
		{"escape-shaped name", "/srv/a%41b.duckdb", instances.ReadWrite, "/srv/a%41b.duckdb?access_mode=read_write&" + policy},
		{"windows path", `C:\data\app.duckdb`, instances.ReadWrite, `C:\data\app.duckdb?access_mode=read_write&` + policy},
		{"in-memory database", ":memory:", instances.ReadWrite, ":memory:?access_mode=read_write&" + policy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := instances.DSN(tc.path, tc.access)
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}

func TestDSN_FailurePath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		reason string
	}{
		{"question mark", "/srv/what?.duckdb", "the DuckDB driver cannot open a file whose path contains a question mark"},
		{"number sign", "/srv/issue#42.duckdb", "the DuckDB driver cannot open a file whose path contains a number sign"},
		{"bare percent", "/srv/100%.duckdb", "the DuckDB driver cannot open a file whose path has a percent sign not followed by two hexadecimal digits"},
		{"percent and one digit", "/srv/a%4", "the DuckDB driver cannot open a file whose path has a percent sign not followed by two hexadecimal digits"},
		{"percent and a non-digit", "/srv/a%zz.duckdb", "the DuckDB driver cannot open a file whose path has a percent sign not followed by two hexadecimal digits"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := instances.DSN(tc.path, instances.ReadWrite)
			var refused *instances.Refusal
			c.Assert(err, qt.ErrorAs, &refused)
			c.Assert(refused.Code, qt.Equals, codes.InvalidArgument)
			c.Assert(refused.Reason, qt.Equals, tc.reason)
			c.Assert(got, qt.Equals, "")
		})
	}
}

func TestSettings_StateEveryPolicySetting(t *testing.T) {
	c := testcheck.New(t)
	c.Assert(instances.Settings(instances.ReadOnly), qt.DeepEquals, []instances.Setting{
		{Name: "access_mode", Value: "read_only"},
		{Name: "autoinstall_known_extensions", Value: "false"},
		{Name: "autoload_known_extensions", Value: "false"},
		{Name: "allow_community_extensions", Value: "false"},
		{Name: "allow_unsigned_extensions", Value: "false"},
		{Name: "allow_persistent_secrets", Value: "false"},
		{Name: "enable_external_access", Value: "false"},
	})
}

// The test binary records the module versions it was built from, and this
// package links the driver, so the driver version duckdbversion states is
// checked against the one go.mod selected. The library version is checked
// against the linked library by the integration tests, which open a database.
func TestDriverVersion_MatchesTheBuild(t *testing.T) {
	c := testcheck.New(t)
	info, found := debug.ReadBuildInfo()
	c.Assert(found, qt.IsTrue)
	versions := make(map[string]string, len(info.Deps))
	for _, dep := range info.Deps {
		versions[dep.Path] = dep.Version
	}
	c.Assert(versions[duckdbversion.DriverModule], qt.Equals, duckdbversion.Driver)
}
