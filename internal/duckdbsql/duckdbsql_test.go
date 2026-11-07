package duckdbsql_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbsql"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The statements are the ones DuckDB v2.0.0's catalog kept for each index.
func TestParseIndex_HappyPath(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		want      duckdbsql.Index
	}{
		{"columns", "CREATE INDEX i1 ON t(a, b);", duckdbsql.Index{Parts: []duckdbsql.IndexPart{{Column: "a"}, {Column: "b"}}}},
		{"unique", "CREATE UNIQUE INDEX i7 ON t(b, a);", duckdbsql.Index{Unique: true, Parts: []duckdbsql.IndexPart{{Column: "b"}, {Column: "a"}}}},
		{"quoted names", `CREATE INDEX i4 ON t("x y", "a,b", "q""t");`,
			duckdbsql.Index{Parts: []duckdbsql.IndexPart{{Column: "x y"}, {Column: "a,b"}, {Column: `q"t`}}}},
		{"expressions", "CREATE INDEX i5 ON t(((a + b)), (lower(CAST(j AS VARCHAR))));",
			duckdbsql.Index{Parts: []duckdbsql.IndexPart{{Expression: "((a + b))"}, {Expression: "(lower(CAST(j AS VARCHAR)))"}}}},
		{"qualified table", `CREATE INDEX ix ON s."My Table"(a);`, duckdbsql.Index{Parts: []duckdbsql.IndexPart{{Column: "a"}}}},
		{"method", "CREATE INDEX h ON t USING HNSW (v);", duckdbsql.Index{Method: "HNSW", Parts: []duckdbsql.IndexPart{{Column: "v"}}}},
		{"no semicolon", "CREATE INDEX i ON t(a)", duckdbsql.Index{Parts: []duckdbsql.IndexPart{{Column: "a"}}}},
		{"string with a comma", "CREATE INDEX i ON t((coalesce(a, 'x,y')));",
			duckdbsql.Index{Parts: []duckdbsql.IndexPart{{Expression: "(coalesce(a, 'x,y'))"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := duckdbsql.ParseIndex(tc.statement)
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.DeepEquals, tc.want)
		})
	}
}

func TestParseIndex_FailurePath(t *testing.T) {
	statements := []string{
		"", "CREATE TABLE t(a INTEGER);", "CREATE INDEX ON t(a);", "CREATE INDEX i t(a);", "CREATE INDEX i ON t;",
		"CREATE INDEX i ON t(a) WHERE a > 0;", "CREATE INDEX i ON t(a, );", "CREATE INDEX i ON t(a", "CREATE INDEX i ON t('a;",
		"CREATE INDEX i ON t USING (a);",
	}
	for _, statement := range statements {
		t.Run(statement, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := duckdbsql.ParseIndex(statement)
			c.Assert(err, qt.ErrorIs, duckdbsql.ErrShape)
			c.Assert(got, qt.DeepEquals, duckdbsql.Index{})
		})
	}
}

// The statements are the ones DuckDB v2.0.0's catalog kept for each view.
func TestParseView_HappyPath(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		want      string
	}{
		{"plain", "CREATE VIEW v1 AS SELECT a, b FROM t WHERE (a > 1);", "SELECT a, b FROM t WHERE (a > 1)"},
		{"quoted alias", `CREATE VIEW v3 AS SELECT a AS "Weird name" FROM t;`, `SELECT a AS "Weird name" FROM t`},
		{"qualified", "CREATE VIEW s.v4 AS SELECT * FROM main.t;", "SELECT * FROM main.t"},
		{"with", "CREATE VIEW v5 AS WITH c AS (SELECT 1 AS one) SELECT * FROM c;", "WITH c AS (SELECT 1 AS one) SELECT * FROM c"},
		{"string with a semicolon", "CREATE VIEW v6 AS SELECT 'it''s; ok' AS s;", "SELECT 'it''s; ok' AS s"},
		{"quoted view name", `CREATE VIEW "My View" AS SELECT 1;`, "SELECT 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := duckdbsql.ParseView(tc.statement)
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}

func TestParseView_FailurePath(t *testing.T) {
	statements := []string{"", "CREATE TABLE t(a INTEGER);", "CREATE VIEW AS SELECT 1;", "CREATE VIEW v SELECT 1;", "CREATE VIEW v AS ;", "CREATE VIEW v AS"}
	for _, statement := range statements {
		t.Run(statement, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := duckdbsql.ParseView(statement)
			c.Assert(err, qt.ErrorIs, duckdbsql.ErrShape)
			c.Assert(got, qt.Equals, "")
		})
	}
}

func TestParseViewColumns_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	got, err := duckdbsql.ParseView("CREATE VIEW v2 (x, y) AS SELECT a, b FROM t;")
	c.Assert(err, qt.ErrorIs, duckdbsql.ErrViewColumns)
	c.Assert(got, qt.Equals, "")
}
