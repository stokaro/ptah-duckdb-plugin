package migrationunits_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "ptah.run/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/migrationunits"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// unit is the part of a unit a row states.
type unit struct {
	SQL, Source string
}

// Every statement is a unit that runs in a transaction with its checkpoint,
// schema statements included, since DuckDB runs those in a transaction too.
func TestSplit_HappyPath(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []unit
	}{
		{"statements", "CREATE TABLE a (id INTEGER);\nINSERT INTO a VALUES (1);\n", []unit{
			{"CREATE TABLE a (id INTEGER)", "CREATE TABLE a (id INTEGER);"},
			{"INSERT INTO a VALUES (1)", "INSERT INTO a VALUES (1);"},
		}},
		{"a comment starts a statement", "-- first\nCREATE TABLE a (id INTEGER);", []unit{
			{"-- first\nCREATE TABLE a (id INTEGER)", "-- first\nCREATE TABLE a (id INTEGER);"},
		}},
		{"a last statement without a semicolon", "DROP TABLE a", []unit{{"DROP TABLE a", "DROP TABLE a"}}},
		{"a semicolon in a string", "INSERT INTO a VALUES ('x;y');", []unit{{"INSERT INTO a VALUES ('x;y')", "INSERT INTO a VALUES ('x;y');"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			units, refusal := migrationunits.Split(tc.body)
			c.Assert(refusal, qt.IsNil)
			var got []unit
			for _, split := range units {
				c.Assert(split.GetPlacement(), qt.Equals, protocol.MigrationUnit_WITH_CHECKPOINT)
				got = append(got, unit{split.GetSql(), split.GetSource()})
			}
			c.Assert(got, qt.DeepEquals, tc.want)
		})
	}
}

func TestSplit_AnEmptyBody(t *testing.T) {
	c := testcheck.New(t)
	units, refusal := migrationunits.Split(" \n")
	c.Assert(units, qt.IsNil)
	c.Assert(refusal, qt.IsNil)
}

// A refusal covers the statement that caused it, or the whole body when the
// provider cannot read it, and names no part of it.
func TestSplit_FailurePath(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		start, end uint32
		reason     string
	}{
		{"a transaction", "CREATE TABLE a (id INTEGER);\nBEGIN;\nCOMMIT;", 29, 35, migrationunits.TransactionControlReason},
		{"a setting", "SET threads = 1;\nCREATE TABLE a (id INTEGER);", 0, 16, migrationunits.SettingReason},
		{"a pragma", "PRAGMA enable_progress_bar;", 0, 27, migrationunits.SettingReason},
		{"another default schema", "USE other;", 0, 10, migrationunits.SettingReason},
		{"an unclosed string", "INSERT INTO a VALUES ('x);", 0, 26, migrationunits.LexingReason},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			units, refusal := migrationunits.Split(tc.body)
			c.Assert(units, qt.IsNil)
			c.Assert([]uint32{refusal.GetStart(), refusal.GetEnd()}, qt.DeepEquals, []uint32{tc.start, tc.end})
			c.Assert(refusal.GetReason(), qt.Equals, tc.reason)
		})
	}
}

func TestReadOnly(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		readOnly bool
		reason   string
	}{
		{"a query", "SELECT count(*) = 0 FROM a", true, ""},
		{"a query in DuckDB's from-first form", "FROM a SELECT count(*)", true, ""},
		{"a common table expression", "WITH x AS (SELECT 1) SELECT * FROM x", true, ""},
		{"a write in a common table expression", "WITH x AS (DELETE FROM a RETURNING *) SELECT * FROM x", false, "the statement changes rows or advances a sequence"},
		{"a sequence advanced", "SELECT nextval('s')", false, "the statement changes rows or advances a sequence"},
		{"a change", "DELETE FROM a", false, "the statement is not a query that only reads rows"},
		{"two statements", "SELECT 1; SELECT 2", false, "the text is not exactly one statement"},
		{"unreadable text", "SELECT 'x", false, migrationunits.LexingReason},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			readOnly, reason := migrationunits.ReadOnly(tc.text)
			c.Assert(readOnly, qt.Equals, tc.readOnly)
			c.Assert(reason, qt.Equals, tc.reason)
		})
	}
}
