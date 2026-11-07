//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// uniqueConstraints lists the UNIQUE constraints of the tables in main: the
// table and DuckDB's text of the constraint.
const uniqueConstraints = "SELECT table_name, constraint_text FROM duckdb_constraints() " +
	"WHERE schema_name = 'main' AND constraint_type = 'UNIQUE' ORDER BY table_name, constraint_index"

// The host lowers an enum column, whether a named type declares it or the
// column states its values, to DuckDB's ENUM type, which lists the values in
// the column's type. The table holds the values and converges.
func TestHostCreatesEnumColumns(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "enum.duckdb")
	const schema = "CREATE TYPE mood AS ENUM ('happy', 'it''s');\n" +
		"CREATE TABLE t (id INTEGER PRIMARY KEY, m mood DEFAULT 'happy', n ENUM('x', 'y'));\n"

	output, err := planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(
		`CREATE TABLE "main"."t" (`,
		`  "id" INTEGER NOT NULL,`,
		`  "m" ENUM('happy', 'it''s') DEFAULT 'happy',`,
		`  "n" ENUM('x', 'y'),`,
		`  PRIMARY KEY ("id")`,
		`);`,
	))
	output, err = applySchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
	c.Assert(columnsOf(c, path, "t"), qt.DeepEquals, []string{
		"id|INTEGER|false|NULL",
		"m|ENUM('happy', 'it''s')|true|'happy'",
		"n|ENUM('x', 'y')|true|NULL",
	})
}

// A value added to an enum changes the column's type in place, and the rows
// keep their values.
func TestHostAddsAValueToAnEnumColumn(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "enum.duckdb", []string{
		"CREATE TABLE t (id INTEGER PRIMARY KEY, m ENUM('happy', 'it''s') DEFAULT 'happy')",
		"INSERT INTO t VALUES (1, 'it''s'), (2, DEFAULT)",
	})
	const schema = "CREATE TYPE mood AS ENUM ('happy', 'it''s', 'sad');\nCREATE TABLE t (id INTEGER PRIMARY KEY, m mood DEFAULT 'happy');\n"

	output, err := planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(`ALTER TABLE "main"."t" ALTER COLUMN "m" TYPE ENUM('happy', 'it''s', 'sad');`))
	output, err = applySchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
	c.Assert(columnsOf(c, path, "t"), qt.DeepEquals, []string{"id|INTEGER|false|NULL", "m|ENUM('happy', 'it''s', 'sad')|true|'happy'"})
	c.Assert(rows(c, path, "SELECT id, m FROM t ORDER BY id"), qt.DeepEquals, []string{"1|it's", "2|happy"})
}

// A UNIQUE constraint over several columns is created with its table and
// added to a table that exists. DuckDB names the constraint itself, so the
// plan says the declared name is not kept, and a declaration that names it
// differently plans nothing: the provider compares the columns.
func TestHostKeepsUniqueConstraintsOverSeveralColumns(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "unique.duckdb")
	steps := []struct {
		name    string
		schema  string
		plan    string
		uniques []string
	}{{
		name:   "create",
		schema: "CREATE TABLE m (id INTEGER PRIMARY KEY, a INTEGER, b VARCHAR, CONSTRAINT m_ab UNIQUE (a, b));",
		plan: planned(
			`-- DUCKDB: DuckDB keeps no constraint names; table "m" holds "m_ab" under the names DuckDB gives them.`,
			`CREATE TABLE "main"."m" (`,
			`  "id" INTEGER NOT NULL,`,
			`  "a" INTEGER,`,
			`  "b" VARCHAR,`,
			`  PRIMARY KEY ("id"),`,
			`  UNIQUE ("a", "b")`,
			`);`,
		),
		uniques: []string{"m|UNIQUE(a, b)"},
	}, {
		name:    "another name for the same columns",
		schema:  "CREATE TABLE m (id INTEGER PRIMARY KEY, a INTEGER, b VARCHAR, CONSTRAINT other_name UNIQUE (a, b));",
		plan:    synced,
		uniques: []string{"m|UNIQUE(a, b)"},
	}, {
		name:   "add a column and a UNIQUE over it",
		schema: "CREATE TABLE m (id INTEGER PRIMARY KEY, a INTEGER, b VARCHAR, c INTEGER, UNIQUE (a, b), UNIQUE (b, c));",
		plan: planned(
			`ALTER TABLE "main"."m" ADD COLUMN "c" INTEGER;`,
			`ALTER TABLE "main"."m" ADD UNIQUE ("b", "c");`,
		),
		uniques: []string{"m|UNIQUE(a, b)", "m|UNIQUE(b, c)"},
	}}
	for _, step := range steps {
		output, err := planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, step.plan, qt.Commentf("%s", step.name))
		output, err = applySchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		output, err = planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, synced, qt.Commentf("%s", step.name))
		c.Assert(rows(c, path, uniqueConstraints), qt.DeepEquals, step.uniques, qt.Commentf("%s", step.name))
	}
}

// DuckDB refuses a UNIQUE constraint the rows break, so the plan that adds
// one rolls back and the table keeps no constraint.
func TestHostRollsBackAUniqueConstraintTheRowsBreak(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "unique.duckdb", []string{"CREATE TABLE m (a INTEGER, b INTEGER)", "INSERT INTO m VALUES (1, 2), (1, 2)"})

	output, err := applySchema(c, path, "CREATE TABLE m (a INTEGER, b INTEGER, UNIQUE (a, b));")
	c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
	c.Assert(output, qt.Contains, "with a constraint error: a row breaks a constraint")
	c.Assert(rows(c, path, uniqueConstraints), qt.HasLen, 0)
}
