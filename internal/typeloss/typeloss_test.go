package typeloss_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
	"github.com/stokaro/ptah-duckdb-plugin/internal/typeloss"
)

// Each declared type DuckDB accepts and holds with a type of its own reports
// what is lost and what DuckDB holds, as measured on the embedded DuckDB.
func TestOf_HappyPath(t *testing.T) {
	tests := []struct {
		declared string
		want     typeloss.Loss
	}{
		{"VARCHAR(255)", typeloss.Loss{Held: "VARCHAR", Property: "type modifier", Detail: "VARCHAR(255): length 255",
			Remedy: "a CHECK on the column's length keeps the limit",
			Note:   "DuckDB keeps no length and holds it as VARCHAR, which takes text of any length."}},
		{"char(2)", typeloss.Loss{Held: "VARCHAR", Property: "type modifier", Detail: "char(2): length 2",
			Remedy: "a CHECK on the column's length keeps the limit",
			Note:   "DuckDB keeps no length and holds it as VARCHAR, which takes text of any length."}},
		{"CHARACTER VARYING(10)", typeloss.Loss{Held: "VARCHAR", Property: "type modifier", Detail: "CHARACTER VARYING(10): length 10",
			Remedy: "a CHECK on the column's length keeps the limit",
			Note:   "DuckDB keeps no length and holds it as VARCHAR, which takes text of any length."}},
		{"VARCHAR(5)[]", typeloss.Loss{Held: "VARCHAR[]", Property: "type modifier", Detail: "VARCHAR(5)[]: length 5",
			Note: "DuckDB keeps no length and holds it as VARCHAR[], which takes text of any length."}},
		{"BIT(8)", typeloss.Loss{Held: "BIT", Property: "type modifier", Detail: "BIT(8): length 8",
			Remedy: "a CHECK on the column's bit_length keeps the length",
			Note:   "DuckDB keeps no length and holds it as BIT, which takes bits of any length."}},
		{"BIT VARYING(8)", typeloss.Loss{Held: "BIT", Property: "type modifier", Detail: "BIT VARYING(8): length 8",
			Remedy: "a CHECK on the column's bit_length keeps the length",
			Note:   "DuckDB keeps no length and holds it as BIT, which takes bits of any length."}},
		{"TIMESTAMP(1)", typeloss.Loss{Held: "TIMESTAMP_MS", Property: "type modifier", Detail: "TIMESTAMP(1): precision 1 (DuckDB keeps milliseconds)",
			Note: "DuckDB does not keep precision 1 and holds it as TIMESTAMP_MS, which keeps milliseconds."}},
		{"timestamp(5)", typeloss.Loss{Held: "TIMESTAMP", Property: "type modifier", Detail: "timestamp(5): precision 5 (DuckDB keeps microseconds)",
			Note: "DuckDB does not keep precision 5 and holds it as TIMESTAMP, which keeps microseconds."}},
		{"TIMESTAMP(7)", typeloss.Loss{Held: "TIMESTAMP_NS", Property: "type modifier", Detail: "TIMESTAMP(7): precision 7 (DuckDB keeps nanoseconds)",
			Note: "DuckDB does not keep precision 7 and holds it as TIMESTAMP_NS, which keeps nanoseconds."}},
	}
	for _, tc := range tests {
		t.Run(tc.declared, func(t *testing.T) {
			c := testcheck.New(t)
			loss, lost := typeloss.Of(tc.declared)
			c.Assert(lost, qt.IsTrue)
			c.Assert(loss, qt.DeepEquals, tc.want)
		})
	}
}

// A type DuckDB keeps whole, or holds under another name with the same
// meaning, loses nothing; so does a precision DuckDB has a type for.
func TestOf_KeptWhole(t *testing.T) {
	for _, declared := range []string{
		"VARCHAR", "TEXT", "INTEGER", "DECIMAL(10,2)", "NUMERIC(5)", "FLOAT(10)", "FLOAT(53)", "TIMESTAMP", "TIMESTAMP(0)", "TIMESTAMP(3)",
		"TIMESTAMP(6)", "TIMESTAMP(9)", "TIMESTAMP WITH TIME ZONE", "BIT", "VARCHAR[]",
	} {
		t.Run(declared, func(t *testing.T) {
			c := testcheck.New(t)
			loss, lost := typeloss.Of(declared)
			c.Assert(lost, qt.IsFalse)
			c.Assert(loss, qt.DeepEquals, typeloss.Loss{})
		})
	}
}
