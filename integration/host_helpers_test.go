//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
)

// hostVariable names the Ptah binary the host-driven tests run. The host job
// of the CI workflow builds Ptah at the commit it resolves and sets it; the
// tests skip without it, and that job fails on a skip.
const hostVariable = "PTAH_HOST_TEST_BIN"

// hostManifest returns the host binary and a provider manifest that selects
// this provider, by its digest, for the duckdb engine.
func hostManifest(c *qt.C) (host, manifest string) {
	c.Helper()
	host = os.Getenv(hostVariable)
	if host == "" {
		c.Skip(hostVariable + " names no host binary")
	}
	binary, digest := providerBinary(c)
	configuration := map[string]any{"format_version": 2, "providers": []any{map[string]any{
		"executable": binary, "sha256": digest, "provider_id": backend.ProviderID,
		"engines": []any{map[string]any{"id": "duckdb", "aliases": make([]string, 0)}},
	}}}
	encoded, err := json.Marshal(configuration)
	c.Assert(err, qt.IsNil)
	manifest = filepath.Join(c.TempDir(), "providers.json")
	c.Assert(os.WriteFile(manifest, encoded, 0o600), qt.IsNil)
	return host, manifest
}

// hostCommand runs one command of the host with this provider selected for
// duckdb, and returns what it wrote to both streams and its error.
func hostCommand(c *qt.C, args ...string) (string, error) {
	c.Helper()
	host, manifest := hostManifest(c)
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
	defer cancel()
	output, err := testCommand(ctx, host, append(args, "--provider-config", manifest, "--provider-engine", "duckdb")...).CombinedOutput()
	return string(output), err
}

// schemaFile writes a desired schema into a new SQL file and returns its
// path.
func schemaFile(c *qt.C, schema string) string {
	c.Helper()
	path := filepath.Join(c.TempDir(), "schema.sql")
	c.Assert(os.WriteFile(path, []byte(schema), 0o600), qt.IsNil)
	return path
}

// planSchema runs ptah schema apply --dry-run of schema against the database
// file at path.
func planSchema(c *qt.C, path, schema string) (string, error) {
	c.Helper()
	return hostCommand(c, "schema", "apply", "--db-url", fileAddress(path), "--schema-file", schemaFile(c, schema), "--dry-run")
}

// applySchema runs ptah schema apply of schema against the database file at
// path, approved in advance.
func applySchema(c *qt.C, path, schema string) (string, error) {
	c.Helper()
	return hostCommand(c, "schema", "apply", "--db-url", fileAddress(path), "--schema-file", schemaFile(c, schema), "--auto-approve")
}

// synced is what the host prints for a database that holds the declared
// schema.
const synced = "Schema is synced, no changes to be made.\n"

// planned is what the host prints for a dry run that plans statements.
func planned(statements ...string) string {
	return "Planned schema changes:\n" + strings.Join(statements, "\n") + "\n"
}

// rows reads a database file the host has closed, opening it read-only in
// the test process, and returns each row of query as its values joined by
// "|", NULL for a missing one.
func rows(c *qt.C, path, query string) []string {
	c.Helper()
	connector, err := duckdb.NewConnector(path+"?access_mode=read_only", nil)
	c.Assert(err, qt.IsNil)
	db := sql.OpenDB(connector)
	defer func() { c.Assert(db.Close(), qt.IsNil) }()
	result, err := db.QueryContext(c.Context(), query)
	c.Assert(err, qt.IsNil)
	defer func() { c.Assert(result.Close(), qt.IsNil) }()
	columns, err := result.Columns()
	c.Assert(err, qt.IsNil)
	var lines []string
	for result.Next() {
		values := make([]sql.NullString, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		c.Assert(result.Scan(targets...), qt.IsNil)
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = nullText(value)
		}
		lines = append(lines, strings.Join(parts, "|"))
	}
	c.Assert(result.Err(), qt.IsNil)
	return lines
}

// nullText writes a scanned value, NULL when it is missing.
func nullText(value sql.NullString) string {
	if !value.Valid {
		return "NULL"
	}
	return value.String
}

// columnsOf lists the columns of the table name in main: name, type,
// whether it takes NULL, and default.
func columnsOf(c *qt.C, path, name string) []string {
	c.Helper()
	return rows(c, path, fmt.Sprintf(
		"SELECT column_name, data_type, is_nullable, column_default FROM duckdb_columns() WHERE schema_name = 'main' AND table_name = '%s' ORDER BY column_index",
		name))
}

// indexesOf lists the explicit indexes of the database: name and statement.
func indexesOf(c *qt.C, path string) []string {
	c.Helper()
	return rows(c, path, "SELECT index_name, sql FROM duckdb_indexes() ORDER BY index_name")
}

// tablesOf lists the tables of main.
func tablesOf(c *qt.C, path string) []string {
	c.Helper()
	return rows(c, path, "SELECT table_name FROM duckdb_tables() WHERE schema_name = 'main' ORDER BY table_name")
}
