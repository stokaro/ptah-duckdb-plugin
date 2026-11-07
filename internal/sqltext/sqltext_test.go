package sqltext_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/sqltext"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// statement is the part of a split statement a row states: its text, line,
// upper-cased words and kind.
type statement struct {
	Text  string
	Line  int
	Words []string
	Kind  sqltext.Kind
}

func TestSplit_HappyPath(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []statement
	}{
		{"two statements", "CREATE TABLE t (a INTEGER);\nINSERT INTO t VALUES (1);\n", []statement{
			{"CREATE TABLE t (a INTEGER);", 1, []string{"CREATE", "TABLE", "T", "(", "A", "INTEGER", ")"}, sqltext.Schema},
			{"INSERT INTO t VALUES (1);", 2, []string{"INSERT", "INTO", "T", "VALUES", "(", "1", ")"}, sqltext.Data},
		}},
		{"a last statement without a semicolon", "SELECT 1", []statement{{"SELECT 1", 1, []string{"SELECT", "1"}, sqltext.Data}}},
		{"semicolons inside quotes", `SELECT 'a;b', "c;d", E'e\';f', $$g;h$$, $tag$i;$$j$tag$;`, []statement{
			{`SELECT 'a;b', "c;d", E'e\';f', $$g;h$$, $tag$i;$$j$tag$;`, 1,
				[]string{"SELECT", "'a;b'", ",", `"c;d"`, ",", `E'e\';f'`, ",", "$$g;h$$", ",", "$tag$i;$$j$tag$"}, sqltext.Data},
		}},
		{"doubled quotes", `SELECT 'it''s', "q""t";`, []statement{
			{`SELECT 'it''s', "q""t";`, 1, []string{"SELECT", "'it''s'", ",", `"q""t"`}, sqltext.Data},
		}},
		{"comments start and end statements", "-- first\nDROP TABLE t; /* x; */\nDROP VIEW v; -- tail\n", []statement{
			{"-- first\nDROP TABLE t;", 1, []string{"DROP", "TABLE", "T"}, sqltext.Schema},
			{"/* x; */\nDROP VIEW v; -- tail\n", 2, []string{"DROP", "VIEW", "V"}, sqltext.Schema},
		}},
		{"schema statements", "CREATE OR REPLACE VIEW v AS SELECT 1; CREATE UNIQUE INDEX i ON t (a); ALTER TABLE t ADD COLUMN b INTEGER; " +
			"COMMENT ON TABLE t IS 'x'; CREATE SEQUENCE s; DROP SCHEMA s;", []statement{
			{"CREATE OR REPLACE VIEW v AS SELECT 1;", 1, []string{"CREATE", "OR", "REPLACE", "VIEW", "V", "AS", "SELECT", "1"}, sqltext.Schema},
			{"CREATE UNIQUE INDEX i ON t (a);", 1, []string{"CREATE", "UNIQUE", "INDEX", "I", "ON", "T", "(", "A", ")"}, sqltext.Schema},
			{"ALTER TABLE t ADD COLUMN b INTEGER;", 1, []string{"ALTER", "TABLE", "T", "ADD", "COLUMN", "B", "INTEGER"}, sqltext.Schema},
			{"COMMENT ON TABLE t IS 'x';", 1, []string{"COMMENT", "ON", "TABLE", "T", "IS", "'x'"}, sqltext.Schema},
			{"CREATE SEQUENCE s;", 1, []string{"CREATE", "SEQUENCE", "S"}, sqltext.Schema},
			{"DROP SCHEMA s;", 1, []string{"DROP", "SCHEMA", "S"}, sqltext.Schema},
		}},
		{"other statements", "SET threads = 2; CREATE MACRO m(a) AS a + 1; PRAGMA version;", []statement{
			{"SET threads = 2;", 1, []string{"SET", "THREADS", "=", "2"}, sqltext.Other},
			{"CREATE MACRO m(a) AS a + 1;", 1, []string{"CREATE", "MACRO", "M", "(", "A", ")", "AS", "A", "+", "1"}, sqltext.Other},
			{"PRAGMA version;", 1, []string{"PRAGMA", "VERSION"}, sqltext.Other},
		}},
		{"operators, casts and parameters", "SELECT a::INTEGER, b->>'k', $1 FROM t WHERE c <> 1;", []statement{
			{"SELECT a::INTEGER, b->>'k', $1 FROM t WHERE c <> 1;", 1,
				[]string{"SELECT", "A", "::", "INTEGER", ",", "B", "->>", "'k'", ",", "$1", "FROM", "T", "WHERE", "C", "<>", "1"}, sqltext.Data},
		}},
		// Measured with duckdb-go v2.20000.0 (DuckDB v2.0.0): a nested comment
		// is one comment, a string continues on the next line, and a segment
		// that continues an escape string is an ordinary string, so the
		// backslash before its quote does not escape it and both CREATE TABLE
		// statements ran.
		{"what DuckDB reads as PostgreSQL does, and its continued escape string",
			"SELECT /* a /* b; */ c */ 'x;'\n'y';\nCREATE TABLE r AS SELECT E'a'\n'b\\' AS v; CREATE TABLE s AS SELECT 1 AS w;", []statement{
				{"SELECT /* a /* b; */ c */ 'x;'\n'y';", 1, []string{"SELECT", "'x;'", "'y'"}, sqltext.Data},
				{"CREATE TABLE r AS SELECT E'a'\n'b\\' AS v;", 3, []string{"CREATE", "TABLE", "R", "AS", "SELECT", "E'a'", "'b\\'", "AS", "V"}, sqltext.Schema},
				{"CREATE TABLE s AS SELECT 1 AS w;", 4, []string{"CREATE", "TABLE", "S", "AS", "SELECT", "1", "AS", "W"}, sqltext.Schema},
			}},
		{"a dollar sign and a backtick are left to the server", "SELECT $ 1, `a`", []statement{
			{"SELECT $ 1, `a`", 1, []string{"SELECT", "$", "1", ",", "`", "A", "`"}, sqltext.Data},
		}},
		{"an empty text", " \n\t", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := sqltext.Split(tc.text)
			c.Assert(err, qt.IsNil)
			var described []statement
			for _, split := range got {
				described = append(described, statement{tc.text[split.Start:split.End], split.Line, split.Words, split.Kind})
			}
			c.Assert(described, qt.DeepEquals, tc.want)
		})
	}
}

// Every byte belongs to a statement or to the white space between them, so
// the source words keep the text as written.
func TestSplit_KeepsTheSourceWords(t *testing.T) {
	c := testcheck.New(t)
	got, err := sqltext.Split(`drop table "My Table";`)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 1)
	c.Assert(got[0].SourceWords, qt.DeepEquals, []string{"drop", "table", `"My Table"`})
	c.Assert(got[0].Words, qt.DeepEquals, []string{"DROP", "TABLE", `"My Table"`})
}

func TestSplit_FailurePath(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"an unclosed string", "SELECT 'a;", "a string does not close on line 1"},
		{"an unclosed escaped string", `SELECT E'a\'`, "a string does not close on line 1"},
		{"an unclosed name", `SELECT "a`, "a quoted identifier does not close on line 1"},
		{"an unclosed nested comment", "SELECT 1;\n/* x /* y */", "a block comment does not close on line 2"},
		{"an unclosed dollar quote", "SELECT $$a", `a dollar-quoted string \$\$ does not close on line 1`},
		{"an empty statement", "SELECT 1;\n;", "an empty statement ends on line 2"},
		{"comments alone", "-- nothing\n/* here */", "the SQL text holds comments and no statement"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := sqltext.Split(tc.text)
			c.Assert(err, qt.ErrorMatches, tc.want)
			c.Assert(got, qt.IsNil)
		})
	}
}
