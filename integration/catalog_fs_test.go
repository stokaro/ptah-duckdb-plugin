//go:build integration

package integration_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/coltype"
	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbsql"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// column builds an expected column of type typ, parsed as the read parses it.
func column(c *qt.C, name, typ string, nullable bool) catalog.Column {
	c.Helper()
	parsed, err := coltype.Parse(typ)
	c.Assert(err, qt.IsNil)
	return catalog.Column{Name: name, Type: typ, Parsed: parsed, Nullable: nullable}
}

func TestRead_RichSchema(t *testing.T) {
	c := testcheck.New(t)
	handle := seedMemory(c, richSchema)
	read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "main")
	c.Assert(err, qt.IsNil)

	id, ts, doubled := column(c, "id", "BIGINT", false), column(c, "ts", "TIMESTAMP WITH TIME ZONE", true), column(c, "doubled", "INTEGER", true)
	id.Default, ts.Default, doubled.Generated = new("nextval('seq_id')"), new("current_timestamp"), new("CAST((parent_id * 2) AS INTEGER)")
	code, amount := column(c, "code", "VARCHAR", false), column(c, "amount", "DECIMAL(12,3)", true)
	code.Unique, code.Comment, amount.Default = true, "the code", new("1.5")
	c.Assert(read, qt.DeepEquals, catalog.Schema{
		Name: "main",
		Tables: []catalog.Table{
			{
				Name: "child",
				Columns: []catalog.Column{
					id, column(c, "parent_id", "INTEGER", true), column(c, "tags", "VARCHAR[]", true),
					column(c, "pt", `STRUCT(x DOUBLE, "y y" DOUBLE)`, true), column(c, "attrs", "MAP(VARCHAR, INTEGER)", true),
					column(c, "u", "UNION(num INTEGER, str VARCHAR)", true), column(c, "vec", "FLOAT[3]", true),
					column(c, "e", "ENUM('a', 'it''s')", true), ts, column(c, "j", "JSON", true), doubled,
				},
				PrimaryKey:  &catalog.Key{Name: "child_id_pkey", Columns: []string{"id"}},
				Checks:      []catalog.Check{{Name: "child_id_check", Columns: []string{"id"}, Expression: "(id > 0)"}},
				ForeignKeys: []catalog.ForeignKey{{Name: "child_parent_id_id_fkey", Columns: []string{"parent_id"}, RefTable: "parent", RefColumns: []string{"id"}}},
				Indexes: []catalog.Index{
					{Name: "child_expr", Unique: true, Parts: []duckdbsql.IndexPart{{Expression: "((id + 1))"}}},
					{Name: "child_parent_ts", Parts: []duckdbsql.IndexPart{{Column: "parent_id"}, {Column: "ts"}}, Comment: "lookup"},
				},
			},
			{
				Name: "parent", Comment: "parents",
				Columns:    []catalog.Column{column(c, "id", "INTEGER", false), code, amount},
				PrimaryKey: &catalog.Key{Name: "parent_id_pkey", Columns: []string{"id"}},
				Checks:     []catalog.Check{{Name: "parent_amount_check", Columns: []string{"amount"}, Expression: "(amount > 0)"}},
			},
		},
		Views:     []catalog.View{{Name: "big_parents", Query: "SELECT id, code FROM parent WHERE (amount > 1)", Comment: "big ones"}},
		Sequences: []catalog.Sequence{{Name: "seq_id", Start: 10, Increment: 5, Min: 1, Max: 1000, Cycle: true, Comment: "ids"}},
	})
}

// Every type DuckDB has reads back in the catalog's own spelling, and its
// structure writes that spelling again: no type is approximated.
func TestRead_TypeInventory(t *testing.T) {
	c := testcheck.New(t)
	handle := seedMemory(c, []string{`CREATE TABLE all_types(
		a BOOLEAN, b TINYINT, c SMALLINT, d INTEGER, e BIGINT, f HUGEINT, g UTINYINT, h USMALLINT, i UINTEGER, j UBIGINT, k UHUGEINT,
		l FLOAT, m DOUBLE, n DECIMAL(18,4), n2 DECIMAL, n3 NUMERIC(38,10), o VARCHAR, o2 TEXT, o5 VARCHAR(10), p BLOB, q BIT, r BIGNUM, r2 VARINT,
		s DATE, t TIME, t2 TIMETZ, t3 TIME_NS, u TIMESTAMP, u2 TIMESTAMP_S, u3 TIMESTAMP_MS, u4 TIMESTAMP_NS, u5 TIMESTAMPTZ, v INTERVAL, w UUID,
		x JSON, y INTEGER[], y2 INTEGER[][], z INTEGER[2], z2 VARCHAR[3][], z3 INTEGER[3][2],
		st STRUCT("a b" INT, c VARCHAR[]), mp MAP(VARCHAR, STRUCT(x INT)), un UNION(i INT, s VARCHAR), en ENUM('x y', 'it''s'),
		geo GEOMETRY, var VARIANT)`})
	read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "main")
	c.Assert(err, qt.IsNil)
	var types []string
	for _, column := range read.Tables[0].Columns {
		c.Assert(column.Parsed.String(), qt.Equals, column.Type)
		types = append(types, column.Type)
	}
	c.Assert(types, qt.DeepEquals, []string{
		"BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT",
		"FLOAT", "DOUBLE", "DECIMAL(18,4)", "DECIMAL(18,3)", "DECIMAL(38,10)", "VARCHAR", "VARCHAR", "VARCHAR", "BLOB", "BIT", "BIGNUM", "BIGNUM",
		"DATE", "TIME", "TIME WITH TIME ZONE", "TIME_NS", "TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMP WITH TIME ZONE",
		"INTERVAL", "UUID", "JSON", "INTEGER[]", "INTEGER[][]", "INTEGER[2]", "VARCHAR[3][]", "INTEGER[3][2]",
		`STRUCT("a b" INTEGER, c VARCHAR[])`, "MAP(VARCHAR, STRUCT(x INTEGER))", "UNION(i INTEGER, s VARCHAR)", "ENUM('x y', 'it''s')",
		"GEOMETRY", "VARIANT",
	})
}

// A column of a type made with CREATE TYPE holds the type it stands for:
// dropping the named type leaves the column as it was, so reading the column
// as that type loses nothing.
func TestRead_NamedTypeColumnHoldsItsType(t *testing.T) {
	c := testcheck.New(t)
	handle := seedMemory(c, []string{"CREATE TYPE mood AS ENUM ('sad', 'ok')", "CREATE TABLE feelings(m mood)", "DROP TYPE mood"})
	read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "main")
	c.Assert(err, qt.IsNil)
	c.Assert(read.Tables[0].Columns[0].Type, qt.Equals, "ENUM('sad', 'ok')")
}

func TestRead_EmptySchema(t *testing.T) {
	c := testcheck.New(t)
	handle := seedMemory(c, nil)
	read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "main")
	c.Assert(err, qt.IsNil)
	c.Assert(read, qt.DeepEquals, catalog.Schema{Name: "main"})
}

// Temporary objects belong to one connection, not to the database, and a
// read leaves them out.
func TestRead_LeavesOutTemporaryObjects(t *testing.T) {
	c := testcheck.New(t)
	handle := seedMemory(c, []string{"CREATE TEMP TABLE scratch(a INTEGER)", "CREATE TEMP VIEW scratch_view AS SELECT 1", "CREATE TEMP SEQUENCE scratch_seq"})
	read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "main")
	c.Assert(err, qt.IsNil)
	c.Assert(read, qt.DeepEquals, catalog.Schema{Name: "main"})
}

func TestRead_FailurePath(t *testing.T) {
	tests := []struct {
		name       string
		statements []string
		reason     string
	}{
		{"macro", []string{"CREATE MACRO add1(a) AS a + 1"}, `schema "main" holds macro "add1", which the provider does not read`},
		{"table macro", []string{"CREATE MACRO numbers(n) AS TABLE SELECT * FROM range(n)"},
			`schema "main" holds table macro "numbers", which the provider does not read`},
		{"named type", []string{"CREATE TYPE mood AS ENUM ('sad', 'ok')"}, `schema "main" holds type "mood", which the provider does not read`},
		{"unique over two columns", []string{"CREATE TABLE t(a INTEGER, b INTEGER, UNIQUE (a, b))"},
			`table "t" has the UNIQUE constraint "t_a_b_key" over 2 columns, which the provider does not read yet`},
		{"two checks of one column", []string{"CREATE TABLE t(id INTEGER, CHECK (id > 0), CONSTRAINT named CHECK (id < 9))"},
			`table "t" holds two constraints DuckDB names "t_id_check"; DuckDB keeps no constraint names, so a read cannot tell them apart`},
		{"two foreign keys of one column", []string{
			"CREATE TABLE p(id INTEGER PRIMARY KEY)", "CREATE TABLE q(id INTEGER PRIMARY KEY)",
			"CREATE TABLE t(a INTEGER REFERENCES p(id), FOREIGN KEY (a) REFERENCES q(id))",
		}, `table "t" holds two constraints DuckDB names "t_a_id_fkey"; DuckDB keeps no constraint names, so a read cannot tell them apart`},
		{"view naming its columns", []string{"CREATE TABLE t(a INTEGER)", "CREATE VIEW v (x) AS SELECT a FROM t"},
			`view "v" names its columns, which the provider does not read yet`},
		{"comment on a view column", []string{"CREATE VIEW v AS SELECT 1 AS one", "COMMENT ON COLUMN v.one IS 'first'"},
			`column "one" of view "v" has a comment, which the provider does not read`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			handle := seedMemory(c, tc.statements)
			read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "main")
			var refused *catalog.Unsupported
			c.Assert(err, qt.ErrorAs, &refused)
			c.Assert(refused.Reason, qt.Equals, tc.reason)
			c.Assert(read, qt.DeepEquals, catalog.Schema{})
		})
	}
}

// A schema a statement created holds objects as main does, and its foreign
// keys stay inside it: DuckDB refuses one into another schema.
func TestRead_AnotherSchema(t *testing.T) {
	c := testcheck.New(t)
	handle := seedMemory(c, []string{
		"CREATE SCHEMA Sales", "CREATE TABLE sales.orders(id INTEGER PRIMARY KEY)",
		"CREATE TABLE sales.lines(order_id INTEGER REFERENCES sales.orders(id))", "CREATE VIEW sales.recent AS SELECT id FROM sales.orders",
		"CREATE TABLE elsewhere(id INTEGER)",
	})
	schemas, err := catalog.Schemas(c.Context(), handle.Conn(), "memory")
	c.Assert(err, qt.IsNil)
	c.Assert(schemas, qt.DeepEquals, []string{"Sales", "main"})
	read, err := catalog.Read(c.Context(), handle.Conn(), "memory", "Sales")
	c.Assert(err, qt.IsNil)
	c.Assert(read.Name, qt.Equals, "Sales")
	c.Assert(read.Tables, qt.HasLen, 2)
	c.Assert(read.Tables[0].ForeignKeys, qt.DeepEquals, []catalog.ForeignKey{
		{Name: "lines_order_id_id_fkey", Columns: []string{"order_id"}, RefTable: "orders", RefColumns: []string{"id"}},
	})
	c.Assert(read.Views, qt.DeepEquals, []catalog.View{{Name: "recent", Query: "SELECT id FROM sales.orders"}})
}
