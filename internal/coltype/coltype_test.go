package coltype_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/coltype"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// Every spelling here is one DuckDB v2.0.0's catalog wrote for a column; the
// integration tests read the same set from a live catalog.
func TestParse_RoundTrip(t *testing.T) {
	spellings := []string{
		"BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT",
		"UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT",
		"FLOAT", "DOUBLE", "BIGNUM", "DECIMAL(18,4)", "DECIMAL(18,3)", "DECIMAL(38,10)", "DECIMAL(1,0)",
		"VARCHAR", "JSON", "BLOB", "BIT", "UUID",
		"DATE", "TIME", "TIME_NS", "TIME WITH TIME ZONE",
		"TIMESTAMP", "TIMESTAMP_S", "TIMESTAMP_MS", "TIMESTAMP_NS", "TIMESTAMP WITH TIME ZONE",
		"INTERVAL", "GEOMETRY", "VARIANT",
		"INTEGER[]", "INTEGER[][]", "INTEGER[2]", "VARCHAR[3][]", "INTEGER[3][2]",
		`STRUCT("a b" INTEGER, c VARCHAR[])`, "STRUCT(x DOUBLE, y DOUBLE)",
		"MAP(VARCHAR, STRUCT(x INTEGER))", "MAP(VARCHAR, INTEGER)",
		"UNION(i INTEGER, s VARCHAR)", "UNION(a INTEGER)",
		"ENUM('x y', 'it''s')", "ENUM('sad', 'ok', 'happy')",
		`STRUCT("q""t" MAP(INTEGER, ENUM('a'))[], "time" TIMESTAMP WITH TIME ZONE[2])`,
	}
	for _, spelling := range spellings {
		t.Run(spelling, func(t *testing.T) {
			c := testcheck.New(t)
			parsed, err := coltype.Parse(spelling)
			c.Assert(err, qt.IsNil)
			c.Assert(parsed.String(), qt.Equals, spelling)
		})
	}
}

func TestParse_Structure(t *testing.T) {
	c := testcheck.New(t)
	parsed, err := coltype.Parse(`STRUCT("a b" DECIMAL(9,2)[], m MAP(VARCHAR, ENUM('x')))`)
	c.Assert(err, qt.IsNil)
	elem := coltype.Type{Kind: coltype.Decimal, Precision: 9, Scale: 2}
	key, value := coltype.Type{Kind: coltype.Base, Name: "VARCHAR"}, coltype.Type{Kind: coltype.Enum, Labels: []string{"x"}}
	c.Assert(parsed, qt.DeepEquals, coltype.Type{Kind: coltype.Struct, Fields: []coltype.Field{
		{Name: "a b", Quoted: true, Type: coltype.Type{Kind: coltype.List, Elem: &elem}},
		{Name: "m", Type: coltype.Type{Kind: coltype.Map, Key: &key, Value: &value}},
	}})
}

func TestParse_FailurePath(t *testing.T) {
	spellings := []string{
		"", "UNKNOWN", "integer", "VARCHAR(10)", "DECIMAL", "DECIMAL(39,0)", "DECIMAL(5,6)", "DECIMAL(0,0)",
		"INTEGER[0]", "INTEGER[", "INTEGER[]]", "STRUCT()", "STRUCT(INTEGER)", "ENUM()", "ENUM(x)", "ENUM('a'",
		"MAP(INTEGER)", "UNION()", "GEOMETRY('EPSG:4326')", "TIME WITH", "TIMESTAMP WITH TIME", "INTEGER INTEGER",
		"STRUCT(\"a INTEGER)", "INTEGER;", "user_type",
	}
	for _, spelling := range spellings {
		t.Run(spelling, func(t *testing.T) {
			c := testcheck.New(t)
			parsed, err := coltype.Parse(spelling)
			c.Assert(err, qt.ErrorIs, coltype.ErrUnknown)
			c.Assert(parsed, qt.DeepEquals, coltype.Type{})
		})
	}
}
