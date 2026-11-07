// Package duckdbversion records which DuckDB the provider embeds: the version
// of the official Go driver, github.com/duckdb/duckdb-go/v2, that go.mod pins,
// and the DuckDB library that driver links statically. The provider reports
// the library version as each session's server version, and the tests compare
// both records with what the build actually holds, so a dependency update
// that forgets this file fails.
package duckdbversion

// DriverModule is the module path of the official DuckDB driver.
const DriverModule = "github.com/duckdb/duckdb-go/v2"

// Driver is the version of DriverModule that go.mod pins.
const Driver = "v2.20000.0"

// Library is the version of the DuckDB library Driver links, as DuckDB's
// library_version reports it.
const Library = "v2.0.0"
