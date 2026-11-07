package ddl_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestCreateTable_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	got, err := ddl.CreateTable("main", `my "t"`, ddl.Table{
		Columns: []ddl.Column{
			{Name: "id", Type: "INTEGER"},
			{Name: "name", Type: "VARCHAR", Nullable: true, Collation: "nocase", Default: new("'x'"), Unique: true, Check: "length(name) > 0"},
			{Name: "twice", Type: "INTEGER", Nullable: true, Generated: new("id * 2")},
			{Name: "parent", Type: "INTEGER", Nullable: true},
		},
		PrimaryKey:  []string{"id"},
		Checks:      []string{" id > 0 "},
		ForeignKeys: []ddl.ForeignKey{{Columns: []string{"parent"}, RefTable: "parents", RefColumns: []string{"id"}}},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.Equals, `CREATE TABLE "main"."my ""t""" (
  "id" INTEGER NOT NULL,
  "name" VARCHAR COLLATE nocase DEFAULT 'x' UNIQUE CHECK (length(name) > 0),
  "twice" INTEGER GENERATED ALWAYS AS (id * 2) VIRTUAL,
  "parent" INTEGER,
  PRIMARY KEY ("id"),
  CHECK (id > 0),
  FOREIGN KEY ("parent") REFERENCES "main"."parents" ("id")
)`)
}

func TestCreateTable_FailurePath(t *testing.T) {
	tests := []struct {
		name  string
		table string
		def   ddl.Table
	}{
		{"no name", "", ddl.Table{Columns: []ddl.Column{{Name: "a", Type: "INTEGER"}}}},
		{"no column", "t", ddl.Table{}},
		{"a column without a type", "t", ddl.Table{Columns: []ddl.Column{{Name: "a"}}}},
		{"an empty CHECK", "t", ddl.Table{Columns: []ddl.Column{{Name: "a", Type: "INTEGER"}}, Checks: []string{" "}}},
		{"a foreign key without columns", "t", ddl.Table{Columns: []ddl.Column{{Name: "a", Type: "INTEGER"}},
			ForeignKeys: []ddl.ForeignKey{{RefTable: "p"}}}},
		{"a foreign key with fewer referenced columns", "t", ddl.Table{Columns: []ddl.Column{{Name: "a", Type: "INTEGER"}},
			ForeignKeys: []ddl.ForeignKey{{Columns: []string{"a"}, RefTable: "p"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := ddl.CreateTable("main", tc.table, tc.def)
			c.Assert(err, qt.ErrorIs, ddl.ErrOperand)
			c.Assert(got, qt.Equals, "")
		})
	}
}

func TestAlterStatements(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"drop table", ddl.DropTable("s", "t"), `DROP TABLE "s"."t"`},
		{"drop column", ddl.DropColumn("s", "t", "c"), `ALTER TABLE "s"."t" DROP COLUMN "c"`},
		{"drop default", ddl.DropDefault("s", "t", "c"), `ALTER TABLE "s"."t" ALTER COLUMN "c" DROP DEFAULT`},
		{"set not null", ddl.SetNotNull("s", "t", "c"), `ALTER TABLE "s"."t" ALTER COLUMN "c" SET NOT NULL`},
		{"drop not null", ddl.DropNotNull("s", "t", "c"), `ALTER TABLE "s"."t" ALTER COLUMN "c" DROP NOT NULL`},
		{"add unique", ddl.AddUnique("s", "t", "c"), `ALTER TABLE "s"."t" ADD UNIQUE ("c")`},
		{"drop index", ddl.DropIndex("s", "i"), `DROP INDEX "s"."i"`},
		{"create schema", ddl.CreateSchema(`a"b`), `CREATE SCHEMA "a""b"`},
		{"drop schema", ddl.DropSchema("a"), `DROP SCHEMA "a"`},
		{"drop view", ddl.DropView("s", "v"), `DROP VIEW "s"."v"`},
		{"drop sequence", ddl.DropSequence("s", "q"), `DROP SEQUENCE "s"."q"`},
		{"comment", ddl.Comment(`TABLE "s"."t"`, "it's"), `COMMENT ON TABLE "s"."t" IS 'it''s'`},
		{"removed comment", ddl.Comment(`TABLE "s"."t"`, ""), `COMMENT ON TABLE "s"."t" IS NULL`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(tc.got, qt.Equals, tc.want)
		})
	}
}

func TestOperandStatements_HappyPath(t *testing.T) {
	tests := []struct {
		name  string
		write func() (string, error)
		want  string
	}{
		{"add column", func() (string, error) {
			return ddl.AddColumn("s", "t", ddl.Column{Name: "c", Type: "BOOLEAN", Default: new("false")})
		}, `ALTER TABLE "s"."t" ADD COLUMN "c" BOOLEAN DEFAULT false NOT NULL`},
		{"type", func() (string, error) { return ddl.AlterColumnType("s", "t", "c", " BIGINT ") }, `ALTER TABLE "s"."t" ALTER COLUMN "c" TYPE BIGINT`},
		{"set default", func() (string, error) { return ddl.SetDefault("s", "t", "c", "now()") }, `ALTER TABLE "s"."t" ALTER COLUMN "c" SET DEFAULT now()`},
		{"add primary key", func() (string, error) { return ddl.AddPrimaryKey("s", "t", []string{"a", "b"}) }, `ALTER TABLE "s"."t" ADD PRIMARY KEY ("a", "b")`},
		{"create index", func() (string, error) {
			return ddl.CreateIndex("s", "t", ddl.Index{Name: "i", Unique: true, Parts: []ddl.IndexPart{{Column: "a"}, {Expression: "lower(b)"}}})
		}, `CREATE UNIQUE INDEX "i" ON "s"."t" ("a", (lower(b)))`},
		{"create view", func() (string, error) { return ddl.CreateView("s", "v", "SELECT 1") }, `CREATE VIEW "s"."v" AS SELECT 1`},
		{"replace view", func() (string, error) { return ddl.ReplaceView("s", "v", "SELECT 1") }, `CREATE OR REPLACE VIEW "s"."v" AS SELECT 1`},
		{"create sequence", func() (string, error) {
			return ddl.CreateSequence("s", "q", ddl.Sequence{Start: new(int64(5)), Increment: new(int64(-1)), Min: new(int64(-10)), Max: new(int64(5)), Cycle: true})
		}, `CREATE SEQUENCE "s"."q" INCREMENT BY -1 MINVALUE -10 MAXVALUE 5 START WITH 5 CYCLE`},
		{"create a default sequence", func() (string, error) { return ddl.CreateSequence("s", "q", ddl.Sequence{}) }, `CREATE SEQUENCE "s"."q"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := tc.write()
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}

func TestOperandStatements_FailurePath(t *testing.T) {
	tests := []struct {
		name  string
		write func() (string, error)
	}{
		{"a UNIQUE column added", func() (string, error) {
			return ddl.AddColumn("s", "t", ddl.Column{Name: "c", Type: "INTEGER", Unique: true})
		}},
		{"a CHECK column added", func() (string, error) {
			return ddl.AddColumn("s", "t", ddl.Column{Name: "c", Type: "INTEGER", Check: "c > 0"})
		}},
		{"a generated column added", func() (string, error) {
			return ddl.AddColumn("s", "t", ddl.Column{Name: "c", Type: "INTEGER", Generated: new("1")})
		}},
		{"a column without a name added", func() (string, error) { return ddl.AddColumn("s", "t", ddl.Column{Type: "INTEGER"}) }},
		{"no type", func() (string, error) { return ddl.AlterColumnType("s", "t", "c", " ") }},
		{"no default", func() (string, error) { return ddl.SetDefault("s", "t", "c", "") }},
		{"no key", func() (string, error) { return ddl.AddPrimaryKey("s", "t", nil) }},
		{"an index without a name", func() (string, error) {
			return ddl.CreateIndex("s", "t", ddl.Index{Parts: []ddl.IndexPart{{Column: "a"}}})
		}},
		{"an index part with both", func() (string, error) {
			return ddl.CreateIndex("s", "t", ddl.Index{Name: "i", Parts: []ddl.IndexPart{{Column: "a", Expression: "b"}}})
		}},
		{"a view without a query", func() (string, error) { return ddl.CreateView("s", "v", " ") }},
		{"a sequence without a name", func() (string, error) { return ddl.CreateSequence("s", "", ddl.Sequence{}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := tc.write()
			c.Assert(err, qt.ErrorIs, ddl.ErrOperand)
			c.Assert(got, qt.Equals, "")
		})
	}
}
