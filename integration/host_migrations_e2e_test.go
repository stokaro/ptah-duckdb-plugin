//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// generateMigration runs ptah migrations generate of schema against the
// database file at path into a new directory and returns the up and the down
// file it wrote.
func generateMigration(c *qt.C, path, schema string) (up, down string) {
	c.Helper()
	dir := c.TempDir()
	output, err := hostCommand(c, "migrations", "generate", "--db-url", fileAddress(path), "--schema-file", schemaFile(c, schema),
		"--migrations-dir", dir, "--name", "change")
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	ups, err := filepath.Glob(filepath.Join(dir, "*_change.up.sql"))
	c.Assert(err, qt.IsNil)
	c.Assert(ups, qt.HasLen, 1)
	downs, err := filepath.Glob(filepath.Join(dir, "*_change.down.sql"))
	c.Assert(err, qt.IsNil)
	c.Assert(downs, qt.HasLen, 1)
	upBody, err := os.ReadFile(ups[0]) // #nosec G304 -- a file the host wrote into the test's temporary directory
	c.Assert(err, qt.IsNil)
	downBody, err := os.ReadFile(downs[0]) // #nosec G304 -- a file the host wrote into the test's temporary directory
	c.Assert(err, qt.IsNil)
	return string(upBody), string(downBody)
}

// The up file carries the provider's notes beside the statements, and the
// down file undoes them in the order DuckDB takes: the tables a foreign key
// references last.
func TestHostGeneratesTheFirstMigration(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "migrations.duckdb")

	up, down := generateMigration(c, path, usersAndPosts)
	c.Assert(up, qt.Contains, `-- DUCKDB: column "email" of table "users" is declared VARCHAR(255); DuckDB keeps no length`)
	c.Assert(up, qt.Contains, "CREATE TABLE \"main\".\"users\" (\n  \"id\" INTEGER NOT NULL,")
	c.Assert(up, qt.Contains, `CREATE INDEX "posts_title" ON "main"."posts" ("title");`)
	c.Assert(down, qt.Contains, "DROP TABLE \"main\".\"posts\";\nDROP TABLE \"main\".\"users\";")
}

// A migration that changes a table drops the index the change needs gone in
// its down file before the column it removes.
func TestHostGeneratesAMigrationThatChangesATable(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "change.duckdb")
	output, err := applySchema(c, path, "CREATE TABLE t (id INTEGER PRIMARY KEY, title VARCHAR NOT NULL);")
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))

	up, down := generateMigration(c, path, "CREATE TABLE t (id INTEGER PRIMARY KEY, title VARCHAR NOT NULL, body VARCHAR DEFAULT 'x');\n"+
		"CREATE INDEX t_body ON t (body);")
	c.Assert(up, qt.Contains, "ALTER TABLE \"main\".\"t\" ADD COLUMN \"body\" VARCHAR DEFAULT 'x';\nCREATE INDEX \"t_body\" ON \"main\".\"t\" (\"body\");")
	c.Assert(down, qt.Contains, "DROP INDEX \"main\".\"t_body\";\nALTER TABLE \"main\".\"t\" DROP COLUMN \"body\";")
	c.Assert(tablesOf(c, path), qt.DeepEquals, []string{"t"})
	c.Assert(columnsOf(c, path, "t"), qt.DeepEquals, []string{"id|INTEGER|false|NULL", "title|VARCHAR|false|NULL"})
}

// migrations lint splits each file through the provider's SQL analysis and
// reads what each statement does.
func TestHostLintsAMigrationDirectory(t *testing.T) {
	c := testcheck.New(t)
	dir := c.TempDir()
	c.Assert(os.WriteFile(filepath.Join(dir, "1_drop.up.sql"), []byte("ALTER TABLE posts DROP COLUMN body;\nDROP TABLE users;\n"), 0o600), qt.IsNil)
	c.Assert(os.WriteFile(filepath.Join(dir, "1_drop.down.sql"), []byte("SELECT 1;\n"), 0o600), qt.IsNil)

	output, err := hostCommand(c, "migrations", "lint", "--dir", dir)
	c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
	c.Assert(output, qt.Contains, "1_drop.up.sql:1 [error] DS102: DROP COLUMN permanently deletes the column's data")
	c.Assert(output, qt.Contains, "1_drop.up.sql:2 [error] DS101: DROP TABLE permanently deletes table users")
}
