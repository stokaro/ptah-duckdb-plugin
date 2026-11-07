//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// usersAndPosts declares what the DuckDB catalog keeps in its own terms: a
// VARCHAR(255) it holds as VARCHAR, and constraint names it replaces with
// its own. The plan says so and still converges.
const usersAndPosts = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  email VARCHAR(255) NOT NULL UNIQUE,
  name TEXT,
  score INTEGER DEFAULT 0 CONSTRAINT score_positive CHECK (score >= 0),
  created_at TIMESTAMP DEFAULT now(),
  tags VARCHAR[],
  point STRUCT(x DOUBLE, y DOUBLE)
);
CREATE TABLE posts (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL,
  title VARCHAR NOT NULL,
  CONSTRAINT fk_posts_user FOREIGN KEY (user_id) REFERENCES users (id)
);
CREATE INDEX posts_title ON posts (title);
CREATE UNIQUE INDEX users_name_score ON users (name, score);
`

func TestHostCreatesTablesAndConverges(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "app.duckdb")

	output, err := planSchema(c, path, usersAndPosts)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(
		`-- DUCKDB: column "email" of table "users" is declared VARCHAR(255); DuckDB keeps no length and holds it as VARCHAR, which takes text of any length.`,
		`-- DUCKDB: DuckDB keeps no constraint names; table "users" holds "score_positive" under the names DuckDB gives them.`,
		`CREATE TABLE "main"."users" (`,
		`  "id" INTEGER NOT NULL,`,
		`  "email" VARCHAR NOT NULL UNIQUE,`,
		`  "name" VARCHAR,`,
		`  "score" INTEGER DEFAULT 0,`,
		`  "created_at" TIMESTAMP DEFAULT now(),`,
		`  "tags" VARCHAR[],`,
		`  "point" STRUCT(x DOUBLE, y DOUBLE),`,
		`  PRIMARY KEY ("id"),`,
		`  CHECK ((score >= 0))`,
		`);`,
		`-- DUCKDB: DuckDB keeps no constraint names; table "posts" holds "fk_posts_user" under the names DuckDB gives them.`,
		`CREATE TABLE "main"."posts" (`,
		`  "id" INTEGER NOT NULL,`,
		`  "user_id" INTEGER NOT NULL,`,
		`  "title" VARCHAR NOT NULL,`,
		`  PRIMARY KEY ("id"),`,
		`  FOREIGN KEY ("user_id") REFERENCES "main"."users" ("id")`,
		`);`,
		`CREATE UNIQUE INDEX "users_name_score" ON "main"."users" ("name", "score");`,
		`CREATE INDEX "posts_title" ON "main"."posts" ("title");`,
	))

	output, err = applySchema(c, path, usersAndPosts)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Contains, "Schema apply completed successfully.")
	output, err = planSchema(c, path, usersAndPosts)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)

	c.Assert(columnsOf(c, path, "users"), qt.DeepEquals, []string{
		"id|INTEGER|false|NULL",
		"email|VARCHAR|false|NULL",
		"name|VARCHAR|true|NULL",
		"score|INTEGER|true|0",
		"created_at|TIMESTAMP|true|now()",
		"tags|VARCHAR[]|true|NULL",
		"point|STRUCT(x DOUBLE, y DOUBLE)|true|NULL",
	})
	c.Assert(rows(c, path, "SELECT table_name, constraint_type, constraint_text FROM duckdb_constraints() "+
		"WHERE schema_name = 'main' AND constraint_type <> 'NOT NULL' ORDER BY table_name, constraint_index"), qt.DeepEquals, []string{
		"posts|PRIMARY KEY|PRIMARY KEY(id)",
		"posts|FOREIGN KEY|FOREIGN KEY (user_id) REFERENCES users(id)",
		"users|UNIQUE|UNIQUE(email)",
		"users|PRIMARY KEY|PRIMARY KEY(id)",
		"users|CHECK|CHECK((score >= 0))",
	})
	c.Assert(indexesOf(c, path), qt.DeepEquals, []string{
		`posts_title|CREATE INDEX posts_title ON posts(title);`,
		`users_name_score|CREATE UNIQUE INDEX users_name_score ON users("name", score);`,
	})
}

// Each step changes the table the step before left, and every change it
// plans is one DuckDB makes in place. The read-back checks DuckDB holds the
// declaration, and the second plan that the read agrees.
func TestHostAltersATableInPlace(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "alter.duckdb")
	steps := []struct {
		name    string
		schema  string
		plan    []string
		columns []string
		indexes []string
	}{{
		name:   "create",
		schema: "CREATE TABLE t (id INTEGER PRIMARY KEY, a VARCHAR, b INTEGER DEFAULT 1);\nCREATE INDEX t_a ON t (a);",
		plan: []string{`CREATE TABLE "main"."t" (`, `  "id" INTEGER NOT NULL,`, `  "a" VARCHAR,`, `  "b" INTEGER DEFAULT 1,`, `  PRIMARY KEY ("id")`, `);`,
			`CREATE INDEX "t_a" ON "main"."t" ("a");`},
		columns: []string{"id|INTEGER|false|NULL", "a|VARCHAR|true|NULL", "b|INTEGER|true|1"},
		indexes: []string{"t_a|CREATE INDEX t_a ON t(a);"},
	}, {
		name:    "add a column and change a default while an index exists",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, a VARCHAR, b INTEGER DEFAULT 2, c VARCHAR DEFAULT 'x');\nCREATE INDEX t_a ON t (a);",
		plan:    []string{`ALTER TABLE "main"."t" ALTER COLUMN "b" SET DEFAULT 2;`, `ALTER TABLE "main"."t" ADD COLUMN "c" VARCHAR DEFAULT 'x';`},
		columns: []string{"id|INTEGER|false|NULL", "a|VARCHAR|true|NULL", "b|INTEGER|true|2", "c|VARCHAR|true|'x'"},
		indexes: []string{"t_a|CREATE INDEX t_a ON t(a);"},
	}, {
		name:    "make a column NOT NULL in the change that drops the index",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, a VARCHAR, b INTEGER NOT NULL DEFAULT 2, c VARCHAR DEFAULT 'x');",
		plan:    []string{`DROP INDEX "main"."t_a";`, `ALTER TABLE "main"."t" ALTER COLUMN "b" SET NOT NULL;`},
		columns: []string{"id|INTEGER|false|NULL", "a|VARCHAR|true|NULL", "b|INTEGER|false|2", "c|VARCHAR|true|'x'"},
	}, {
		name:    "drop a column and a default",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, b INTEGER NOT NULL, c VARCHAR DEFAULT 'x');",
		plan:    []string{`ALTER TABLE "main"."t" ALTER COLUMN "b" DROP DEFAULT;`, `ALTER TABLE "main"."t" DROP COLUMN "a";`},
		columns: []string{"id|INTEGER|false|NULL", "b|INTEGER|false|NULL", "c|VARCHAR|true|'x'"},
	}, {
		name:    "change a type and make a column nullable",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, b BIGINT, c VARCHAR DEFAULT 'x');",
		plan:    []string{`ALTER TABLE "main"."t" ALTER COLUMN "b" TYPE BIGINT;`, `ALTER TABLE "main"."t" ALTER COLUMN "b" DROP NOT NULL;`},
		columns: []string{"id|INTEGER|false|NULL", "b|BIGINT|true|NULL", "c|VARCHAR|true|'x'"},
	}, {
		name:    "make a column UNIQUE, then index another",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, b BIGINT, c VARCHAR DEFAULT 'x' UNIQUE);\nCREATE INDEX t_b ON t (b);",
		plan:    []string{`ALTER TABLE "main"."t" ADD UNIQUE ("c");`, `CREATE INDEX "t_b" ON "main"."t" ("b");`},
		columns: []string{"id|INTEGER|false|NULL", "b|BIGINT|true|NULL", "c|VARCHAR|true|'x'"},
		indexes: []string{"t_b|CREATE INDEX t_b ON t(b);"},
	}, {
		name:    "add a NOT NULL column in the change that drops the index",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, b BIGINT, c VARCHAR DEFAULT 'x' UNIQUE, d BOOLEAN NOT NULL DEFAULT false);",
		plan:    []string{`DROP INDEX "main"."t_b";`, `ALTER TABLE "main"."t" ADD COLUMN "d" BOOLEAN DEFAULT false NOT NULL;`},
		columns: []string{"id|INTEGER|false|NULL", "b|BIGINT|true|NULL", "c|VARCHAR|true|'x'", "d|BOOLEAN|false|false"},
	}, {
		name:    "index a column again",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, b BIGINT, c VARCHAR DEFAULT 'x' UNIQUE, d BOOLEAN NOT NULL DEFAULT false);\nCREATE INDEX t_b ON t (b);",
		plan:    []string{`CREATE INDEX "t_b" ON "main"."t" ("b");`},
		columns: []string{"id|INTEGER|false|NULL", "b|BIGINT|true|NULL", "c|VARCHAR|true|'x'", "d|BOOLEAN|false|false"},
		indexes: []string{"t_b|CREATE INDEX t_b ON t(b);"},
	}, {
		name:    "drop a column after the index in the change that drops the index",
		schema:  "CREATE TABLE t (id INTEGER PRIMARY KEY, b BIGINT, c VARCHAR DEFAULT 'x' UNIQUE);",
		plan:    []string{`DROP INDEX "main"."t_b";`, `ALTER TABLE "main"."t" DROP COLUMN "d";`},
		columns: []string{"id|INTEGER|false|NULL", "b|BIGINT|true|NULL", "c|VARCHAR|true|'x'"},
	}}
	for _, step := range steps {
		output, err := planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, planned(step.plan...), qt.Commentf("%s", step.name))
		output, err = applySchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		output, err = planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, synced, qt.Commentf("%s", step.name))
		c.Assert(columnsOf(c, path, "t"), qt.DeepEquals, step.columns, qt.Commentf("%s", step.name))
		c.Assert(indexesOf(c, path), qt.DeepEquals, step.indexes, qt.Commentf("%s", step.name))
	}
}

// A primary key added to a table that exists runs after the change that
// makes its columns NOT NULL, and DuckDB holds it.
func TestHostAddsAPrimaryKeyToATable(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "key.duckdb", []string{"CREATE TABLE p (a INTEGER NOT NULL, b INTEGER)", "INSERT INTO p VALUES (1, 1), (1, 2)"})
	const schema = "CREATE TABLE p (a INTEGER, b INTEGER, PRIMARY KEY (a, b));"

	output, err := planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(`ALTER TABLE "main"."p" ALTER COLUMN "b" SET NOT NULL;`, `ALTER TABLE "main"."p" ADD PRIMARY KEY ("a", "b");`))
	output, err = applySchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, schema)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
	c.Assert(rows(c, path, "SELECT constraint_text FROM duckdb_constraints() WHERE table_name = 'p' AND constraint_type = 'PRIMARY KEY'"),
		qt.DeepEquals, []string{"PRIMARY KEY(a, b)"})
}

// DuckDB drops no table a foreign key references, so the plan drops the
// referencing table first.
func TestHostDropsATableAfterTheTableThatReferencesIt(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "drop.duckdb")
	output, err := applySchema(c, path, usersAndPosts)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))

	other := "CREATE TABLE other (id INTEGER);"
	output, err = planSchema(c, path, other)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(`CREATE TABLE "main"."other" (`, `  "id" INTEGER`, `);`, `DROP TABLE "main"."posts";`, `DROP TABLE "main"."users";`))
	output, err = applySchema(c, path, other)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(tablesOf(c, path), qt.DeepEquals, []string{"other"})
	c.Assert(indexesOf(c, path), qt.IsNil)
}

// The host keeps the removals of tables in a sequence of its own, by name, so
// it would drop customers before orders, whose foreign key references it.
// The provider drops the two in one step, orders first, and the migration
// that generates the same removal does too.
func TestHostDropsTablesThatReferenceEachOther(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "shop.duckdb")
	shop := "CREATE TABLE customers (id INTEGER PRIMARY KEY);\n" +
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers (id));\n" +
		"CREATE TABLE lines (id INTEGER, order_id INTEGER REFERENCES orders (id));\n" +
		"CREATE TABLE kept (id INTEGER);"
	output, err := applySchema(c, path, shop)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))

	_, down := generateMigration(c, filepath.Join(c.TempDir(), "empty.duckdb"), shop)
	c.Assert(down, qt.Contains, "DROP TABLE \"main\".\"kept\";\nDROP TABLE \"main\".\"lines\";\nDROP TABLE \"main\".\"orders\";\nDROP TABLE \"main\".\"customers\";")

	output, err = planSchema(c, path, "CREATE TABLE kept (id INTEGER);")
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(`DROP TABLE "main"."lines";`, `DROP TABLE "main"."orders";`, `DROP TABLE "main"."customers";`))
	output, err = applySchema(c, path, "CREATE TABLE kept (id INTEGER);")
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(tablesOf(c, path), qt.DeepEquals, []string{"kept"})
}

// A host that reads namespaces plans objects in any schema: the schema
// itself, a sequence a default draws from, a view, comments, and tables that
// reference each other, which it creates, changes and drops there.
func TestHostManagesAnotherSchema(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "schemas.duckdb")
	steps := []struct {
		name   string
		schema string
		plan   []string
		tables []string
	}{{
		name: "create",
		schema: `CREATE SCHEMA app;
CREATE SEQUENCE app.item_ids START 100;
CREATE TABLE app.items (id BIGINT PRIMARY KEY DEFAULT nextval('app.item_ids'), price DECIMAL(10,2) NOT NULL);
CREATE TABLE app.notes (id INTEGER, item_id BIGINT REFERENCES app.items (id));
COMMENT ON TABLE app.items IS 'catalog items';
CREATE VIEW app.cheap AS SELECT id FROM app.items WHERE price < 10;`,
		plan: []string{`CREATE SCHEMA "app";`, `CREATE SEQUENCE "app"."item_ids" START WITH 100;`, `CREATE TABLE "app"."items" (`,
			`  "id" BIGINT DEFAULT nextval('app.item_ids') NOT NULL,`, `  "price" DECIMAL(10,2) NOT NULL,`, `  PRIMARY KEY ("id")`, `);`,
			`COMMENT ON TABLE "app"."items" IS 'catalog items';`,
			`-- DUCKDB: DuckDB keeps no constraint names; table "app"."notes" holds "fk_notes_item_id" under the names DuckDB gives them.`,
			`CREATE TABLE "app"."notes" (`, `  "id" INTEGER,`, `  "item_id" BIGINT,`, `  FOREIGN KEY ("item_id") REFERENCES "app"."items" ("id")`, `);`,
			`CREATE VIEW "app"."cheap" AS SELECT id FROM app.items WHERE price < 10;`},
		tables: []string{"app.items", "app.notes"},
	}, {
		name: "change and drop",
		schema: `CREATE SCHEMA app;
CREATE TABLE app.other (id INTEGER, label VARCHAR DEFAULT 'x');`,
		plan: []string{`DROP VIEW "app"."cheap";`, `CREATE TABLE "app"."other" (`, `  "id" INTEGER,`, `  "label" VARCHAR DEFAULT 'x'`, `);`,
			`DROP TABLE "app"."notes";`, `DROP TABLE "app"."items";`, `DROP SEQUENCE "app"."item_ids";`},
		tables: []string{"app.other"},
	}}
	for _, step := range steps {
		output, err := planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, planned(step.plan...), qt.Commentf("%s", step.name))
		output, err = applySchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		output, err = planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, synced, qt.Commentf("%s", step.name))
		c.Assert(rows(c, path, "SELECT schema_name || '.' || table_name FROM duckdb_tables() ORDER BY 1"), qt.DeepEquals, step.tables, qt.Commentf("%s", step.name))
	}
}

// DuckDB changes no column's nullability while a foreign key references the
// table, so the plan drops the referencing table before the change.
func TestHostChangesATableAfterDroppingTheTableThatReferencesIt(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "reference.duckdb")
	output, err := applySchema(c, path, usersAndPosts)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))

	users := `CREATE TABLE users (id INTEGER PRIMARY KEY, email VARCHAR NOT NULL UNIQUE, name TEXT NOT NULL,
  score INTEGER DEFAULT 0 CHECK (score >= 0), created_at TIMESTAMP DEFAULT now(), tags VARCHAR[], point STRUCT(x DOUBLE, y DOUBLE));`
	output, err = planSchema(c, path, users)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, planned(`DROP INDEX "main"."users_name_score";`, `DROP TABLE "main"."posts";`,
		`ALTER TABLE "main"."users" ALTER COLUMN "name" SET NOT NULL;`))
	output, err = applySchema(c, path, users)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	output, err = planSchema(c, path, users)
	c.Assert(err, qt.IsNil, qt.Commentf("%s", output))
	c.Assert(output, qt.Equals, synced)
	c.Assert(tablesOf(c, path), qt.DeepEquals, []string{"users"})
}

// A statement DuckDB refuses aborts the transaction the plan runs in, so
// nothing the plan changed before it stays, and the reason names DuckDB's
// class of error without quoting it.
func TestHostRollsBackAPlanDuckDBRefuses(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "refused.duckdb", []string{"CREATE TABLE t (a INTEGER)", "INSERT INTO t VALUES (1)", "CREATE TABLE u (a INTEGER)"})

	output, err := applySchema(c, path, "CREATE TABLE t (a INTEGER, b INTEGER NOT NULL);\nCREATE TABLE v (a INTEGER);")
	c.Assert(err, qt.ErrorMatches, "exit status [0-9]+")
	c.Assert(output, qt.Contains, "DuckDB refused unit 2 of 3 with a constraint error: a row breaks a constraint")
	c.Assert(tablesOf(c, path), qt.DeepEquals, []string{"t", "u"})
	c.Assert(columnsOf(c, path, "t"), qt.DeepEquals, []string{"a|INTEGER|true|NULL"})
}

// commonObjects declares a sequence a default draws from, comments, and a
// view on the table.
const commonObjects = `
CREATE SEQUENCE item_ids START 100;
CREATE TABLE items (
  id BIGINT PRIMARY KEY DEFAULT nextval('item_ids'),
  price DECIMAL(10,2) NOT NULL
);
COMMENT ON TABLE items IS 'catalog items';
COMMENT ON COLUMN items.price IS 'in cents';
CREATE VIEW cheap AS SELECT id, price FROM items WHERE price < 10;
`

func TestHostManagesCommonObjects(t *testing.T) {
	c := testcheck.New(t)
	path := filepath.Join(c.TempDir(), "objects.duckdb")
	steps := []struct {
		name      string
		schema    string
		plan      []string
		sequences []string
		views     []string
		comments  []string
	}{{
		name:   "create",
		schema: commonObjects,
		plan: []string{`CREATE SEQUENCE "main"."item_ids" START WITH 100;`, `CREATE TABLE "main"."items" (`, `  "id" BIGINT DEFAULT nextval('item_ids') NOT NULL,`,
			`  "price" DECIMAL(10,2) NOT NULL,`, `  PRIMARY KEY ("id")`, `);`, `COMMENT ON TABLE "main"."items" IS 'catalog items';`,
			`COMMENT ON COLUMN "main"."items"."price" IS 'in cents';`, `CREATE VIEW "main"."cheap" AS SELECT id, price FROM items WHERE price < 10;`},
		sequences: []string{"item_ids|100"},
		views:     []string{"cheap|CREATE VIEW cheap AS SELECT id, price FROM items WHERE (price < 10);"},
		comments:  []string{"items|catalog items", "price|in cents"},
	}, {
		name: "change the view and the comments",
		schema: `CREATE SEQUENCE item_ids START 100;
CREATE TABLE items (id BIGINT PRIMARY KEY DEFAULT nextval('item_ids'), price DECIMAL(10,2) NOT NULL);
COMMENT ON TABLE items IS 'items for sale';
CREATE VIEW cheap AS SELECT id, price FROM items WHERE price < 20;`,
		plan: []string{`DROP VIEW "main"."cheap";`, `CREATE VIEW "main"."cheap" AS SELECT id, price FROM items WHERE price < 20;`,
			`COMMENT ON TABLE "main"."items" IS 'items for sale';`, `COMMENT ON COLUMN "main"."items"."price" IS NULL;`},
		sequences: []string{"item_ids|100"},
		views:     []string{"cheap|CREATE VIEW cheap AS SELECT id, price FROM items WHERE (price < 20);"},
		comments:  []string{"items|items for sale"},
	}, {
		name:   "drop them",
		schema: "CREATE TABLE other (id INTEGER);",
		plan: []string{`DROP VIEW "main"."cheap";`, `CREATE TABLE "main"."other" (`, `  "id" INTEGER`, `);`, `DROP TABLE "main"."items";`,
			`DROP SEQUENCE "main"."item_ids";`},
	}}
	for _, step := range steps {
		output, err := planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, planned(step.plan...), qt.Commentf("%s", step.name))
		output, err = applySchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		output, err = planSchema(c, path, step.schema)
		c.Assert(err, qt.IsNil, qt.Commentf("%s: %s", step.name, output))
		c.Assert(output, qt.Equals, synced, qt.Commentf("%s", step.name))
		c.Assert(rows(c, path, "SELECT sequence_name, start_value FROM duckdb_sequences() ORDER BY 1"), qt.DeepEquals, step.sequences,
			qt.Commentf("%s", step.name))
		c.Assert(rows(c, path, "SELECT view_name, sql FROM duckdb_views() WHERE NOT internal ORDER BY 1"), qt.DeepEquals, step.views,
			qt.Commentf("%s", step.name))
		c.Assert(rows(c, path, "SELECT table_name, comment FROM duckdb_tables() WHERE comment IS NOT NULL "+
			"UNION ALL SELECT column_name, comment FROM duckdb_columns() WHERE comment IS NOT NULL ORDER BY 1"), qt.DeepEquals, step.comments,
			qt.Commentf("%s", step.name))
	}
}
