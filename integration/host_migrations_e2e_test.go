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

// migrationDir writes a migration directory of two migrations: users with an
// index, then posts, which references users.
func migrationDir(c *qt.C) string {
	c.Helper()
	return writeFiles(c, map[string]string{
		"1_users.up.sql":   "CREATE TABLE users (id INTEGER PRIMARY KEY, name VARCHAR);\nCREATE INDEX users_name ON users (name);\n",
		"1_users.down.sql": "DROP INDEX users_name;\nDROP TABLE users;\n",
		"2_posts.up.sql":   "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id));\n",
		"2_posts.down.sql": "DROP TABLE posts;\n",
	})
}

// The schema migrationDir leads to, as a declaration.
const migratedSchema = "CREATE TABLE users (id INTEGER PRIMARY KEY, name VARCHAR);\nCREATE INDEX users_name ON users (name);\n" +
	"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id));\n"

// The migrator keeps its revisions, its run log and its tags in tables the
// provider marks as its own, which a schema apply then leaves alone.
func TestHostRunsMigrationsUpAndDown(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "migrated.duckdb")
	dir := migrationDir(c)
	address := []string{"--db-url", fileAddress(path)}
	steps := []struct {
		name string
		args []string
		want string
	}{
		{"status before", []string{"migrations", "status", "--migrations-dir", dir}, "Pending Migrations: 2"},
		{"up", []string{"migrations", "up", "--migrations-dir", dir}, "Database is now at version: 2"},
		{"status after", []string{"migrations", "status", "--migrations-dir", dir}, "Status: ✅ Database is up to date"},
		{"tag", []string{"migrations", "tag", "release-1"}, "Tagged version 2 as release-1."},
		{"tags", []string{"migrations", "tag"}, "release-1  2"},
		{"log", []string{"migrations", "log"}, "up         applied"},
		{"down", []string{"migrations", "down", "--migrations-dir", dir, "--confirm", "--to-tag", "release-1"}, "Database is already at or below target version 2!"},
		{"down one", []string{"migrations", "down", "--migrations-dir", dir, "--confirm", "--target", "1"}, "Database is now at version: 1"},
	}
	for _, step := range steps {
		output, err := hostCommand(c, append(step.args, address...)...)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Contains, step.want, qt.Commentf("%s", step.name))
	}
	c.Assert(tablesOf(c, path), qt.DeepEquals, []string{"ptah_migration_tags", "schema_migrations", "schema_migrations_log", "users"})
	c.Assert(rows(c, path, "SELECT version, state, applied, total FROM schema_migrations ORDER BY version"), qt.DeepEquals, []string{"1|applied|2|2"})

	output, err := hostCommand(c, append([]string{"migrations", "up", "--migrations-dir", dir}, address...)...)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, migratedSchema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
}

// A failed migration leaves what its transaction mode promises: a file
// rolled back whole, a body without a transaction stopped after the last
// statement that committed, and every pending migration rolled back
// together. The revision table records the failure in each.
func TestHostRunsMigrationsInEachTransactionMode(t *testing.T) {
	dir := func(c *qt.C) string {
		c.Helper()
		return writeFiles(c, map[string]string{
			"1_ab.up.sql":    "CREATE TABLE a (id INTEGER);\nCREATE TABLE b (id INTEGER);\n",
			"1_ab.down.sql":  "DROP TABLE b;\nDROP TABLE a;\n",
			"2_bad.up.sql":   "CREATE TABLE c (id INTEGER);\nCREATE TABLE c (id INTEGER);\n",
			"2_bad.down.sql": "DROP TABLE c;\n",
		})
	}
	tests := []struct {
		mode     string
		tables   []string
		versions []string
	}{
		{"file", []string{"a", "b", "schema_migrations", "schema_migrations_log"}, []string{"1|applied|2|2", "2|failed|0|2"}},
		{"none", []string{"a", "b", "c", "schema_migrations", "schema_migrations_log"}, []string{"1|applied|2|2", "2|failed|1|2"}},
		{"all", []string{"schema_migrations", "schema_migrations_log"}, []string{"2|failed|0|2"}},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			c := testcheck.New(t)
			path := filepath.Join(c.TempDir(), tc.mode+".duckdb")
			output, err := hostCommand(c, "migrations", "up", "--db-url", fileAddress(path), "--migrations-dir", dir(c), "--tx-mode", tc.mode)
			c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
			c.Assert(output, qt.Contains, "DuckDB refused unit 1 of 1 with a catalog error: an object the unit names is missing, or exists already")
			c.Assert(tablesOf(c, path), qt.DeepEquals, tc.tables)
			c.Assert(rows(c, path, "SELECT version, state, applied, total FROM schema_migrations ORDER BY version"), qt.DeepEquals, tc.versions)
		})
	}
}

// A dev database is a temporary file for docker://duckdb, which the
// provider removes, or a file the operator names, which must be empty.
func TestHostUsesADevDatabase(t *testing.T) {
	c := testcheck.New(t)
	dir := migrationDir(c)
	steps := []struct {
		name string
		args []string
		want string
	}{
		{"lint on a provisioned database", []string{"migrations", "lint", "--dir", dir, "--dev-url", "docker://duckdb"}, "No lint findings."},
		{"lint on the linked line", []string{"migrations", "lint", "--dir", dir, "--dev-url", "docker://duckdb/2.0"}, "No lint findings."},
		{"lint on an empty file", []string{"migrations", "lint", "--dir", dir, "--dev-url", fileAddress(filepath.Join(c.TempDir(), "dev.duckdb"))},
			"No lint findings."},
		{"lint on the process's in-memory database", []string{"migrations", "lint", "--dir", dir, "--dev-url", "duckdb://:memory:"}, "No lint findings."},
		{"generate by replay", []string{"migrations", "generate", "--replay", "--dev-url", "docker://duckdb", "--migrations-dir", dir,
			"--schema-file", schemaFile(c, migratedSchema+"CREATE TABLE notes (id INTEGER PRIMARY KEY);\n"), "--name", "notes"}, "Generated migration files"},
	}
	for _, step := range steps {
		output, err := hostCommand(c, step.args...)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Contains, step.want, qt.Commentf("%s", step.name))
	}
	ups, err := filepath.Glob(filepath.Join(dir, "*_notes.up.sql"))
	c.Assert(err, qt.IsNil)
	c.Assert(ups, qt.HasLen, 1)
	up, err := os.ReadFile(ups[0]) // #nosec G304 -- a file the host wrote into the test's temporary directory
	c.Assert(err, qt.IsNil)
	c.Assert(string(up), qt.Contains, `CREATE TABLE "main"."notes" (`)
}

// The host checks a dev database the operator names is empty before it
// uses it, through the provider's list of what a cleanup removes.
func TestHostRefusesADevDatabaseThatHoldsATable(t *testing.T) {
	c := testcheck.New(t)
	dev := fileAddress(seedFile(c, "dirty.duckdb", []string{"CREATE TABLE junk (id INTEGER)"}))
	output, err := hostCommand(c, "migrations", "lint", "--dir", migrationDir(c), "--dev-url", dev)
	c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
	c.Assert(output, qt.Contains, `connected database is not clean: found table "junk" in schema "main"`)
}

// The provider links one DuckDB and provisions no other version.
func TestHostRefusesADevDatabaseOfAnotherVersion(t *testing.T) {
	c := testcheck.New(t)
	output, err := hostCommand(c, "migrations", "lint", "--dir", migrationDir(c), "--dev-url", "docker://duckdb/1.4")
	c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
	c.Assert(output, qt.Contains, "provider engine duckdb does not provision this dev database: the provider links DuckDB 2.0.0 and provisions that version only")
}

// A migration body holds what DuckDB reads as PostgreSQL does, each with a
// semicolon or an apostrophe inside: a nested block comment, a dollar-quoted
// string and a string continued on the next line. It also holds DuckDB's own
// reading of an escape string continued by a segment that ends in a
// backslash, which DuckDB scans as an ordinary string. The provider splits
// the body where DuckDB does, so each statement runs once and the rows hold
// what DuckDB made of the text.
func TestHostRunsAMigrationDuckDBReadsAsPostgreSQLDoes(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "text.duckdb")
	dir := writeFiles(c, map[string]string{
		"1_text.up.sql": "CREATE TABLE notes (id INTEGER, body VARCHAR); /* a /* nested */ it's; a comment */\n" +
			"INSERT INTO notes VALUES (1, $$it's; one$$);\n" +
			"INSERT INTO notes VALUES (2, E'a'\n'b\\'); INSERT INTO notes VALUES (3, 'x'\n'y');\n",
		"1_text.down.sql": "DROP TABLE notes;\n",
	})
	output, err := hostCommand(c, "migrations", "up", "--db-url", fileAddress(path), "--migrations-dir", dir)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(rows(c, path, "SELECT id, body FROM notes ORDER BY id"), qt.DeepEquals, []string{"1|it's; one", `2|ab\`, "3|xy"})
	c.Assert(rows(c, path, "SELECT version, state, applied, total FROM schema_migrations"), qt.DeepEquals, []string{"1|applied|4|4"})
}
