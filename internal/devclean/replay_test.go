package devclean_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/devclean"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestReplay(t *testing.T) {
	tests := []struct {
		statement string
		want      string
	}{
		{"CREATE TABLE a (id INTEGER)", ""},
		{"CREATE OR REPLACE VIEW v AS SELECT 1", ""},
		{"CREATE UNIQUE INDEX i ON a (id)", ""},
		{"CREATE MACRO m(x) AS x + 1", ""},
		{"CREATE TYPE mood AS ENUM ('a')", ""},
		{"CREATE SCHEMA s", ""},
		{"ALTER TABLE a ADD COLUMN b INTEGER", ""},
		{"DROP SEQUENCE s", ""},
		{"COMMENT ON TABLE a IS 'x'", ""},
		{"INSERT INTO a VALUES (1)", ""},
		{"FROM a SELECT *", ""},
		{"CHECKPOINT", ""},
		{"CREATE TEMP TABLE t (a INTEGER)", "CREATE TEMP creates an object of the session"},
		{"CREATE TABLE temp.t (a INTEGER)", "a name in the temp catalog, which a cleanup of the database does not reach"},
		{"CREATE TABLE temp (a INTEGER)", ""},
		{"CREATE SECRET s (TYPE s3)", "CREATE SECRET changes a secret, which outlives the database"},
		{"SET threads = 1", "SET changes a setting of the session or the database"},
		{"PRAGMA enable_profiling", "PRAGMA changes a setting of the session or the database"},
		{"BEGIN", "BEGIN changes the state of the session's transaction"},
		{"PREPARE p AS SELECT 1", "PREPARE changes the session's prepared statements"},
		{"ATTACH 'other.duckdb'", "ATTACH reaches another database"},
		{"INSTALL httpfs", "INSTALL installs or loads an extension"},
		{"UPDATE EXTENSIONS", "UPDATE EXTENSIONS installs or loads an extension"},
		{"COPY a TO 'a.csv'", "COPY reads or writes a file outside the database"},
		{"CALL pragma_version()", "CALL statement"},
		{"CREATE ROLE r", "CREATE ROLE"},
		{"SELECT 1; SELECT 2", "more than one statement in one replay statement"},
		{"SELECT 'x", "a statement the provider cannot read"},
	}
	for _, tc := range tests {
		t.Run(tc.statement, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(devclean.Replay(tc.statement), qt.Equals, tc.want)
		})
	}
}
