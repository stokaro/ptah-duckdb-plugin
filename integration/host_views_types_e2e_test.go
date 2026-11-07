//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// rewrittenViews declares views whose queries DuckDB stores in a spelling of
// its own: parentheses around each condition, count_star() for count(*), ~~
// for LIKE, = ANY for IN over a subquery, a quoted name for a keyword, and a
// CASE with an extra space. A star stays a star.
const rewrittenViews = `
CREATE TABLE t (id BIGINT PRIMARY KEY, name VARCHAR, price DOUBLE, score DECIMAL(10,2), d DATE, ts TIMESTAMP, flag BOOLEAN);
CREATE TABLE u (id BIGINT PRIMARY KEY, t_id BIGINT, label VARCHAR);
CREATE VIEW v01 AS SELECT id, name FROM t;
CREATE VIEW v02 AS SELECT id FROM t WHERE id > 0;
CREATE VIEW v03 AS SELECT id FROM t WHERE name IN ('a', 'b');
CREATE VIEW v04 AS SELECT id FROM t WHERE id BETWEEN 1 AND 10;
CREATE VIEW v05 AS SELECT id FROM t WHERE name LIKE 'a%';
CREATE VIEW v06 AS SELECT t.id, u.label FROM t LEFT JOIN u ON u.t_id = t.id WHERE u.label IS NOT NULL;
CREATE VIEW v07 AS SELECT name, count(*) AS n FROM t GROUP BY name HAVING count(*) > 1;
CREATE VIEW v08 AS SELECT id, CASE WHEN id > 5 THEN 'big' ELSE 'small' END AS size FROM t;
CREATE VIEW v09 AS SELECT id, CAST(id AS VARCHAR) AS sid FROM t;
CREATE VIEW v10 AS SELECT id FROM t WHERE d > '2020-01-01';
CREATE VIEW v11 AS SELECT id FROM t WHERE flag = true AND id <> 3 OR name IS NULL;
CREATE VIEW v12 AS SELECT id FROM t ORDER BY id LIMIT 10;
CREATE VIEW v13 AS SELECT id FROM t WHERE id IN (SELECT t_id FROM u);
CREATE VIEW v14 AS SELECT id FROM t UNION ALL SELECT id FROM u;
CREATE VIEW v15 AS SELECT id, coalesce(name, 'none') AS n FROM t;
CREATE VIEW v16 AS SELECT * FROM t;
CREATE VIEW v17 AS SELECT x.id FROM t AS x;
CREATE VIEW v18 AS SELECT id FROM t WHERE name NOT IN ('x');
CREATE VIEW v19 AS SELECT id, -id AS neg FROM t;
CREATE VIEW v20 AS select ID from T where Name = 'A';
CREATE VIEW v21 AS SELECT * EXCLUDE (name) FROM t WHERE price > 1.5;
`

// Every view converges although DuckDB rewrites its query: the provider
// prepares a declared query in the spelling DuckDB stores, and a host that
// asks the provider whether two queries are one gets the same answer as one
// that compares their texts.
func TestHostKeepsTheViewsDuckDBRewrites(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "views.duckdb")
	output, err := applySchema(c, path, rewrittenViews)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, rewrittenViews)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
}

// aliasedView is a view whose column alias is alias.
func aliasedView(alias string) string {
	return "CREATE TABLE t (id BIGINT PRIMARY KEY);\nCREATE VIEW v AS SELECT id AS " + alias + " FROM t;\n"
}

// aliasCasePlan is what each host plans when a view's alias changes only its
// case, by whether the host asks the provider whether two view queries are
// one: a host that compares their texts folds the case and plans nothing.
var aliasCasePlan = map[bool]string{
	false: synced,
	true:  planned(`DROP VIEW "main"."v";`, `CREATE VIEW "main"."v" AS SELECT id AS "ID" FROM t;`),
}

// aliasCaseColumn is the column the view holds after the change is applied,
// by the same question.
var aliasCaseColumn = map[bool][]string{false: {"Id"}, true: {"ID"}}

// DuckDB names a view's column by its alias, keeping its case, so an alias
// whose case changes changes the view. A host that asks the provider whether
// two view queries are one (contract 25.33) replaces the view, and the view
// then holds the declared column. A host that compares the texts folds the
// case, plans nothing, and the view keeps its old column.
func TestHostReplacesAViewWhoseAliasChangesCase(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "alias.duckdb")
	asks := hostComparesViewQueries(c)
	output, err := applySchema(c, path, aliasedView(`"Id"`))
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))

	output, err = planSchema(c, path, aliasedView(`"ID"`))
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, aliasCasePlan[asks])
	output, err = applySchema(c, path, aliasedView(`"ID"`))
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, aliasedView(`"ID"`))
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
	c.Assert(rows(c, path, "SELECT column_name FROM duckdb_columns() WHERE table_name = 'v'"), qt.DeepEquals, aliasCaseColumn[asks])
}

// keptInPart declares columns whose types DuckDB accepts and holds with a
// type of its own.
const keptInPart = `CREATE TABLE readings (
  id BIGINT PRIMARY KEY,
  label VARCHAR(20),
  flags BIT(8),
  taken TIMESTAMP(1),
  exact TIMESTAMP(3)
);
`

// A plan says what DuckDB does not keep of each declared type, the text and
// bit lengths and a timestamp precision DuckDB has no type for, then
// converges on the types DuckDB holds. A precision DuckDB has a type for
// needs no note.
func TestHostNotesTheTypesDuckDBKeepsInPart(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "types.duckdb")
	output, err := planSchema(c, path, keptInPart)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(
		`-- DUCKDB: column "label" of table "readings" is declared VARCHAR(20); DuckDB keeps no length and holds it as VARCHAR, which takes text of any length.`,
		`-- DUCKDB: column "flags" of table "readings" is declared BIT(8); DuckDB keeps no length and holds it as BIT, which takes bits of any length.`,
		`-- DUCKDB: column "taken" of table "readings" is declared TIMESTAMP(1); DuckDB does not keep precision 1 and holds it as TIMESTAMP_MS, `+
			`which keeps milliseconds.`,
		`CREATE TABLE "main"."readings" (`,
		`  "id" BIGINT NOT NULL,`,
		`  "label" VARCHAR,`,
		`  "flags" BIT,`,
		`  "taken" TIMESTAMP_MS,`,
		`  "exact" TIMESTAMP_MS,`,
		`  PRIMARY KEY ("id")`,
		`);`,
	))
	output, err = applySchema(c, path, keptInPart)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, keptInPart)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
	c.Assert(columnsOf(c, path, "readings"), qt.DeepEquals, []string{
		"id|BIGINT|false|NULL",
		"label|VARCHAR|true|NULL",
		"flags|BIT|true|NULL",
		"taken|TIMESTAMP_MS|true|NULL",
		"exact|TIMESTAMP_MS|true|NULL",
	})
}
