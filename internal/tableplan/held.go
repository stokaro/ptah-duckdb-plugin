package tableplan

import (
	"fmt"
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/typeloss"
)

// Declared says how DuckDB holds a declared table once it is created: its
// columns with DuckDB's types, defaults and generated expressions, its key,
// its UNIQUE columns and its CHECKs under the names DuckDB gives them. The
// backend answers it by creating the table in a scratch database; foreign
// keys are left out, since their tables need not exist there.
type Declared struct {
	Hold func(*protocol.DeclaredTable) (catalog.Table, error)
}

// CreationNotes are the notes a table's creation carries for what the
// declaration writes and DuckDB does not keep: a text or bit length and a
// timestamp precision, which DuckDB accepts and holds with a type of its own
// (see typeloss), and a constraint name, since DuckDB names every constraint
// itself. The plan converges on both: the read reports DuckDB's types and
// names, and the planner compares the declaration in the same terms.
func (d Declared) CreationNotes(subject *protocol.ComparisonIdentity, table *protocol.DeclaredTable) []string {
	var notes []string
	for _, column := range table.GetColumns() {
		if note := TypeNote(subject, table, column); note != "" {
			notes = append(notes, note)
		}
	}
	if names := constraintNames(table); len(names) > 0 {
		notes = append(notes, fmt.Sprintf("DUCKDB: DuckDB keeps no constraint names; table %s holds %s under the names DuckDB gives them.",
			phrase(subject), strings.Join(names, ", ")))
	}
	return notes
}

// TypeNote is the note for a declared column whose type states something
// DuckDB does not keep, or the empty string.
func TypeNote(subject *protocol.ComparisonIdentity, table *protocol.DeclaredTable, column *protocol.DeclaredColumn) string {
	declared := ColumnType(table, column)
	loss, lost := typeloss.Of(declared)
	if !lost {
		return ""
	}
	return fmt.Sprintf("DUCKDB: column %q of table %s is declared %s; %s", column.GetName(), phrase(subject), declared, loss.Note)
}

// DeclaredLoss is the declared type of a column whose type states something
// DuckDB does not keep, which an operation carries so its render can report
// the omission, or the empty string.
func DeclaredLoss(table *protocol.DeclaredTable, column *protocol.DeclaredColumn) string {
	if column == nil {
		return ""
	}
	declared := strings.TrimSpace(ColumnType(table, column))
	if _, lost := typeloss.Of(declared); !lost {
		return ""
	}
	return declared
}

// constraintNames lists, quoted, the constraint names a declaration gives.
func constraintNames(table *protocol.DeclaredTable) []string {
	var names []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(names, fmt.Sprintf("%q", name)) {
			names = append(names, fmt.Sprintf("%q", name))
		}
	}
	add(table.GetPrimaryKeyName())
	for _, column := range table.GetColumns() {
		add(column.GetCheckName())
		add(column.GetForeignKeyName())
	}
	for _, constraint := range table.GetConstraints() {
		add(constraint.GetName())
	}
	return names
}

// heldPrimaryKey lists the key a table holds in key order: the PRIMARY KEY
// constraint the host reads into the observation, or the columns a read marks
// as key columns when it carries no constraint.
func heldPrimaryKey(table *protocol.ObservedTable) []string {
	for _, constraint := range table.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "PRIMARY KEY") {
			if len(constraint.GetColumnNames()) > 0 {
				return slices.Clone(constraint.GetColumnNames())
			}
			return []string{constraint.GetColumnName()}
		}
	}
	var keys []string
	for _, column := range table.GetColumns() {
		if column.GetPrimaryKey() {
			keys = append(keys, column.GetName())
		}
	}
	return keys
}

// heldKeys are the foreign keys a table holds.
func heldKeys(table *protocol.ObservedTable) []keyShape {
	var keys []keyShape
	for _, constraint := range table.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "FOREIGN KEY") {
			keys = append(keys, keyShape{columns: foldAll(constraint.GetColumnNames()), table: foldASCII(constraint.GetForeignTable()),
				refColumns: foldAll(constraint.GetForeignColumns())})
		}
	}
	return keys
}

// keyShape is what a foreign key says, folded as DuckDB compares names.
type keyShape struct {
	columns    []string
	table      string
	refColumns []string
}

func (k keyShape) equal(other keyShape) bool {
	return slices.Equal(k.columns, other.columns) && k.table == other.table && slices.Equal(k.refColumns, other.refColumns)
}

func (k keyShape) String() string {
	return fmt.Sprintf("(%s) REFERENCES %s (%s)", strings.Join(k.columns, ", "), k.table, strings.Join(k.refColumns, ", "))
}

func foldAll(names []string) []string {
	folded := make([]string, len(names))
	for i, name := range names {
		folded[i] = foldASCII(name)
	}
	return folded
}

func observedColumn(table *protocol.ObservedTable, name string) *protocol.ObservedColumn {
	for _, column := range table.GetColumns() {
		if foldASCII(column.GetName()) == foldASCII(name) {
			return column
		}
	}
	return nil
}

func declaredColumn(table *protocol.DeclaredTable, name string) *protocol.DeclaredColumn {
	for _, column := range table.GetColumns() {
		if foldASCII(column.GetName()) == foldASCII(name) {
			return column
		}
	}
	return nil
}

func declaredIndex(table *protocol.DeclaredTable, name string) *protocol.DeclaredIndex {
	for _, index := range table.GetIndexes() {
		if foldASCII(index.GetName()) == foldASCII(name) {
			return index
		}
	}
	return nil
}

func heldColumn(table catalog.Table, name string) *catalog.Column {
	for i := range table.Columns {
		if foldASCII(table.Columns[i].Name) == foldASCII(name) {
			return &table.Columns[i]
		}
	}
	return nil
}

// heldType is the full type a read reports for a column.
func heldType(column *protocol.ObservedColumn) string {
	if column.GetColumnType() != "" {
		return column.GetColumnType()
	}
	return column.GetDataType()
}
