//go:build integration

package integration_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// referenced is a table another table's foreign key references, and the
// referencing table, which has an index.
var referenced = []string{
	"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR, score INTEGER DEFAULT 0 CHECK (score >= 0))",
	"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR, body VARCHAR)",
	"CREATE INDEX posts_title ON posts (title)",
}

// The declarations of the tables referenced holds, as a schema file writes
// them.
const (
	usersTable = "CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR, score INTEGER DEFAULT 0 CHECK (score >= 0));\n"
	postsTable = "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR, body VARCHAR);\n" +
		"CREATE INDEX posts_title ON posts (title);\n"
)

// indexed is a table with an index on its last column but one.
var indexed = []string{"CREATE TABLE t (id INTEGER, a INTEGER, b INTEGER, c INTEGER)", "CREATE INDEX t_b ON t (b)"}

// linked is a table another table's foreign key references, with no CHECK and
// no index, so a change to either refuses for the reference alone; unlinked is
// the same pair without the foreign key.
var (
	linked = []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR)",
		"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR)",
	}
	unlinked = []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR)",
		"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title VARCHAR)",
	}
)

// linkedUsers is the users table of linked, as a schema file writes it.
const linkedUsers = "CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR);\n"

// keyed is a table with a primary key and a UNIQUE column.
var keyed = []string{"CREATE TABLE k (id INTEGER PRIMARY KEY, a INTEGER UNIQUE, b INTEGER)"}

// Every change DuckDB cannot make is refused by name before a statement is
// sent, never planned as a removal and a new creation, and the database is
// left as it was.
func TestHostRefusesWhatDuckDBCannotDo(t *testing.T) {
	tests := []struct {
		name   string
		setup  []string
		schema string
		reason string
	}{
		{"the type of a column in a CHECK", referenced,
			"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR, score BIGINT DEFAULT 0 CHECK (score >= 0));\n" + postsTable,
			`changing the type of column "score" of table "users": DuckDB changes the type of no column in a CHECK constraint`},
		{"the type of a key column", keyed, "CREATE TABLE k (id BIGINT PRIMARY KEY, a INTEGER UNIQUE, b INTEGER);",
			`changing the type of column "id" of table "k": DuckDB changes the type of no column in a primary key`},
		{"the type of a UNIQUE column", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, a BIGINT UNIQUE, b INTEGER);",
			`changing the type of column "a" of table "k": DuckDB changes the type of no column with a UNIQUE constraint`},
		{"nullability while an index exists", referenced,
			usersTable + "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR NOT NULL, body VARCHAR);\n" +
				"CREATE INDEX posts_title ON posts (title);\n",
			`table "posts": DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while index "posts_title" exists; ` +
				`drop the index in this change to make it`},
		{"nullability while a foreign key references the table", referenced,
			"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR NOT NULL, score INTEGER DEFAULT 0 CHECK (score >= 0));\n" +
				postsTable,
			`table "users": DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while the foreign key of table "posts" references the table`},
		{"a column an index covers, in the change that drops the index", indexed, "CREATE TABLE t (id INTEGER, a INTEGER, c INTEGER);",
			`dropping column "b" of table "t" in the change that drops index "t_b": DuckDB keeps a dropped index until the transaction commits`},
		{"a column before an indexed column, in the change that drops the index", indexed, "CREATE TABLE t (id INTEGER, b INTEGER, c INTEGER);",
			`dropping column "a" of table "t" in the change that drops index "t_b": DuckDB keeps a dropped index until the transaction commits`},
		{"the type of an indexed column, in the change that drops the index", indexed, "CREATE TABLE t (id INTEGER, a INTEGER, b BIGINT, c INTEGER);",
			`changing the type of column "b" of table "t" in the change that drops index "t_b": DuckDB keeps a dropped index until the transaction commits`},
		{"a key column", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, b INTEGER);",
			`dropping column "a" of table "k": DuckDB drops no column in a primary key or a UNIQUE constraint`},
		{"a dropped primary key", keyed, "CREATE TABLE k (a INTEGER UNIQUE, b INTEGER);",
			`changing the primary key of table "k" from (id) to (): DuckDB changes and drops no primary key`},
		{"a column a UNIQUE column follows", []string{"CREATE TABLE f (a INTEGER, b INTEGER UNIQUE)"}, "CREATE TABLE f (b INTEGER UNIQUE);",
			`dropping column "a" of table "f": DuckDB drops no column that the key or UNIQUE column "b" follows`},
		{"a new CHECK", referenced, usersTable + "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR, " +
			"body VARCHAR CHECK (length(body) > 0));\nCREATE INDEX posts_title ON posts (title);\n",
			`table "posts": the declaration changes the table's CHECK constraints, and DuckDB adds and drops no CHECK on a table that exists`},
		{"a dropped foreign key", referenced, usersTable +
			"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title VARCHAR, body VARCHAR);\nCREATE INDEX posts_title ON posts (title);\n",
			`table "posts": the declaration changes the table's foreign keys, and DuckDB adds and drops no foreign key on a table that exists`},
		{"a changed primary key", keyed, "CREATE TABLE k (id INTEGER, a INTEGER UNIQUE, b INTEGER PRIMARY KEY);",
			`changing the primary key of table "k" from (id) to (b): DuckDB changes and drops no primary key`},
		{"a dropped UNIQUE", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER);",
			`table "k": the declaration drops the UNIQUE constraint of column "a", and DuckDB drops no UNIQUE constraint`},
		{"a table a foreign key references", referenced, "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, title VARCHAR, body VARCHAR);\n" +
			"CREATE INDEX posts_title ON posts (title);\n",
			`dropping table "users": the foreign key of table "posts" references it, and DuckDB drops no table a foreign key references`},
		{"the type of a column of a table a foreign key references", linked,
			"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name BIGINT);\n" +
				"CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR);\n",
			`table "users": DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while the foreign key of table "posts" references the table`},
		{"a NOT NULL column added while an index stays", indexed,
			"CREATE TABLE t (id INTEGER, a INTEGER, b INTEGER, c INTEGER, d INTEGER NOT NULL DEFAULT 0);\nCREATE INDEX t_b ON t (b);\n",
			`table "t": DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while index "t_b" exists; drop the index in this change to make it`},
		{"a UNIQUE column added while an index stays", indexed,
			"CREATE TABLE t (id INTEGER, a INTEGER, b INTEGER, c INTEGER, d INTEGER UNIQUE);\nCREATE INDEX t_b ON t (b);\n",
			`table "t": DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while index "t_b" exists; drop the index in this change to make it`},
		{"the type of a foreign key column", linked,
			linkedUsers + "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id BIGINT REFERENCES users (id), title VARCHAR);\n",
			`changing the type of column "user_id" of table "posts": DuckDB changes the type of no column in a FOREIGN KEY constraint`},
		{"a dropped foreign key column", linked, linkedUsers + "CREATE TABLE posts (id INTEGER PRIMARY KEY, title VARCHAR);\n",
			`table "posts": the declaration changes the table's foreign keys, and DuckDB adds and drops no foreign key on a table that exists`},
		{"a foreign key added to a table that exists", unlinked,
			linkedUsers + "CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id), title VARCHAR);\n",
			`table "posts": the declaration changes the table's foreign keys, and DuckDB adds and drops no foreign key on a table that exists`},
		{"a changed generated expression", []string{"CREATE TABLE g (a INTEGER, b INTEGER GENERATED ALWAYS AS (a * 2) VIRTUAL)"},
			"CREATE TABLE g (a INTEGER, b INTEGER GENERATED ALWAYS AS (a * 3) VIRTUAL);\n",
			`table "g": the declaration changes generated column "b", and DuckDB alters no generated column`},
		{"a new generated column", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, a INTEGER UNIQUE, b INTEGER, g INTEGER GENERATED ALWAYS AS (b * 2) VIRTUAL);",
			`table "k": DuckDB adds no generated column to a table that exists; column "g"`},
		{"a changed collation", []string{"CREATE TABLE n (a VARCHAR)"}, "CREATE TABLE n (a VARCHAR COLLATE nocase);",
			`table "n": the declaration changes the collation of column "a", which the provider does not plan`},
		{"a changed sequence", []string{"CREATE SEQUENCE s START 1"}, "CREATE SEQUENCE s START 5;",
			`sequence "s": DuckDB alters no sequence in place, and replacing it would restart its counter`},
		{"a cascading foreign key", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, a INTEGER UNIQUE, b INTEGER);\n" +
			"CREATE TABLE r (id INTEGER, k_id INTEGER REFERENCES k (id) ON DELETE CASCADE);",
			`has ON DELETE CASCADE; DuckDB has NO ACTION and RESTRICT only`},
		{"a partial index", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, a INTEGER UNIQUE, b INTEGER);\nCREATE INDEX k_b ON k (b) WHERE b > 0;",
			`has index "k_b", which has a WHERE filter; DuckDB creates no partial index`},
		{"a descending index key", keyed, "CREATE TABLE k (id INTEGER PRIMARY KEY, a INTEGER UNIQUE, b INTEGER);\nCREATE INDEX k_b ON k (b DESC);",
			`has index "k_b", which states a key order or a NULLS placement; DuckDB's index keeps neither`},
		{"a dropped UNIQUE over two columns", []string{"CREATE TABLE m (a INTEGER, b INTEGER, UNIQUE (a, b))"}, "CREATE TABLE m (a INTEGER, b INTEGER);",
			`table "m": the declaration drops the UNIQUE constraint over (a, b), and DuckDB drops no UNIQUE constraint`},
		{"a column of a UNIQUE over two columns", []string{"CREATE TABLE m (a INTEGER, b INTEGER, c INTEGER, UNIQUE (b, c))"},
			"CREATE TABLE m (a INTEGER, b INTEGER);",
			`dropping column "c" of table "m": DuckDB drops no column in a primary key or a UNIQUE constraint`},
		{"a STORED generated column", nil, "CREATE TABLE g (a INTEGER, b INTEGER GENERATED ALWAYS AS (a * 2) STORED);",
			`column "b" is a STORED generated column; DuckDB computes generated columns when it reads a row (VIRTUAL) only`},
		{"two new tables that reference each other", nil,
			"CREATE TABLE x (id INTEGER PRIMARY KEY, y_id INTEGER REFERENCES y (id));\nCREATE TABLE y (id INTEGER PRIMARY KEY, x_id INTEGER REFERENCES x (id));",
			`reference each other, and DuckDB creates a foreign key only to a table that exists`},
		{"a change between a dropped and a created reference", referenced,
			"CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name VARCHAR NOT NULL, score INTEGER DEFAULT 0 CHECK (score >= 0));\n" +
				"CREATE TABLE notes (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users (id));",
			`table "users": DuckDB changes no column's type, nullability or UNIQUE, and drops no column, while a foreign key references the table, ` +
				`and the plan creates table "notes", which references it, before it drops table "posts"; make the change and the new table in two plans`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			path := seedFile(c, "refused.duckdb", tc.setup)
			before := rows(c, path, "SELECT sql FROM duckdb_tables() UNION ALL SELECT sql FROM duckdb_indexes() ORDER BY 1")

			output, err := planSchema(c, path, tc.schema)
			c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
			c.Assert(output, qt.Contains, tc.reason)
			output, err = applySchema(c, path, tc.schema)
			c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
			c.Assert(output, qt.Contains, tc.reason)
			c.Assert(rows(c, path, "SELECT sql FROM duckdb_tables() UNION ALL SELECT sql FROM duckdb_indexes() ORDER BY 1"), qt.DeepEquals, before)
		})
	}
}

// An index declared under another name is dropped and created under the new
// one: DuckDB renames no index, and the provider does not ask Ptah for rename
// steps, so a rename never reaches DuckDB. The index holds no rows, so nothing
// is lost, and the next plan is synced.
func TestHostRecreatesARenamedIndex(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "renamed.duckdb", indexed)
	schema := "CREATE TABLE t (id INTEGER, a INTEGER, b INTEGER, c INTEGER);\nCREATE INDEX t_b_renamed ON t (b);\n"

	plan, err := planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", plan))
	output, err := applySchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	again, err := planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", again))

	c.Assert(plan, qt.Equals, planned("DROP INDEX \"main\".\"t_b\";", "CREATE INDEX \"t_b_renamed\" ON \"main\".\"t\" (\"b\");"))
	c.Assert(again, qt.Equals, synced)
	c.Assert(rows(c, path, "SELECT index_name FROM duckdb_indexes() ORDER BY 1"), qt.DeepEquals, []string{"t_b_renamed"})
}
