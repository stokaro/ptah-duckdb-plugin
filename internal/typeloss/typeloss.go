// Package typeloss says what a declared column type states and DuckDB does
// not keep. DuckDB accepts such a type and creates the column with a type of
// its own, so the declaration and the database part company without an
// error. Measured on the embedded DuckDB 2.0.0:
//
//	VARCHAR(255), CHAR(3), BPCHAR(4)  VARCHAR, any length (300 characters fit VARCHAR(255))
//	VARCHAR(5)[]                      VARCHAR[]
//	BIT(8), BIT VARYING(8)            BIT, any length ('1010' fits BIT(1))
//	TIMESTAMP(1), TIMESTAMP(2)        TIMESTAMP_MS
//	TIMESTAMP(4), TIMESTAMP(5)        TIMESTAMP
//	TIMESTAMP(7), TIMESTAMP(8)        TIMESTAMP_NS
//
// TIMESTAMP(0), (3), (6) and (9) are kept as TIMESTAMP_S, TIMESTAMP_MS,
// TIMESTAMP and TIMESTAMP_NS. FLOAT(p) is FLOAT up to 24 and DOUBLE above, as
// the SQL standard maps it, and a DECIMAL keeps its precision and scale, so
// neither loses anything. DuckDB refuses TIME(p), TIMESTAMPTZ(p) and a
// DECIMAL wider than 38, so those never reach a database.
package typeloss

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Loss is what one declared type states and DuckDB does not keep.
type Loss struct {
	// Held is the type DuckDB creates the column with, such as VARCHAR.
	Held string
	// Property names what is lost, as a render omission names it.
	Property string
	// Detail repeats the declared type and the lost value, such as
	// `VARCHAR(255): length 255`.
	Detail string
	// Remedy says how to keep the declaration on DuckDB, empty when nothing
	// does.
	Remedy string
	// Note is the sentence a plan carries for the column, after the column
	// and table names: what DuckDB holds instead.
	Note string
}

var (
	// textLength is a text type with a length, alone or as the element of
	// a list or an array.
	textLength = regexp.MustCompile(`(?i)^\s*(?:VARCHAR|CHAR|CHARACTER VARYING|CHARACTER|BPCHAR|STRING|TEXT|NVARCHAR|NCHAR)\s*\(\s*(\d+)\s*\)\s*((?:\[\s*\d*\s*\]\s*)*)$`)
	// bitLength is a bit string type with a length.
	bitLength = regexp.MustCompile(`(?i)^\s*(BIT(?:\s+VARYING)?)\s*\(\s*(\d+)\s*\)\s*$`)
	// timestampPrecision is a timestamp without a time zone with a precision.
	timestampPrecision = regexp.MustCompile(`(?i)^\s*TIMESTAMP\s*\(\s*(\d+)\s*\)\s*$`)
)

// heldTimestamps names the type DuckDB holds a TIMESTAMP(p) with, for each
// precision it does not keep, and the unit that type keeps.
var heldTimestamps = map[int][2]string{
	1: {"TIMESTAMP_MS", "milliseconds"}, 2: {"TIMESTAMP_MS", "milliseconds"},
	4: {"TIMESTAMP", "microseconds"}, 5: {"TIMESTAMP", "microseconds"},
	7: {"TIMESTAMP_NS", "nanoseconds"}, 8: {"TIMESTAMP_NS", "nanoseconds"},
}

// Of returns what the declared type states and DuckDB does not keep, and
// false for a type DuckDB keeps whole or refuses.
func Of(declared string) (Loss, bool) {
	declared = strings.TrimSpace(declared)
	if match := textLength.FindStringSubmatch(declared); match != nil {
		held := "VARCHAR" + strings.Join(strings.Fields(match[2]), "")
		loss := Loss{Held: held, Property: "type modifier", Detail: fmt.Sprintf("%s: length %s", declared, match[1]),
			Note: fmt.Sprintf("DuckDB keeps no length and holds it as %s, which takes text of any length.", held)}
		if match[2] == "" {
			loss.Remedy = "a CHECK on the column's length keeps the limit"
		}
		return loss, true
	}
	if match := bitLength.FindStringSubmatch(declared); match != nil {
		return Loss{Held: "BIT", Property: "type modifier", Detail: fmt.Sprintf("%s: length %s", declared, match[2]),
			Remedy: "a CHECK on the column's bit_length keeps the length",
			Note:   "DuckDB keeps no length and holds it as BIT, which takes bits of any length."}, true
	}
	if match := timestampPrecision.FindStringSubmatch(declared); match != nil {
		precision, err := strconv.Atoi(match[1])
		held, lost := heldTimestamps[precision]
		if err != nil || !lost {
			return Loss{}, false
		}
		return Loss{Held: held[0], Property: "type modifier", Detail: fmt.Sprintf("%s: precision %d (DuckDB keeps %s)", declared, precision, held[1]),
			Note: fmt.Sprintf("DuckDB does not keep precision %d and holds it as %s, which keeps %s.", precision, held[0], held[1])}, true
	}
	return Loss{}, false
}
