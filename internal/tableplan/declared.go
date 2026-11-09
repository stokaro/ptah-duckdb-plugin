package tableplan

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	protocol "ptah.run/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
)

// PrimaryKey lists a declared table's key columns in key order: the table's
// key list, then any column marked primary that the list does not name.
func PrimaryKey(table *protocol.DeclaredTable) []string {
	keys := slices.Clone(table.GetPrimaryKey())
	for _, column := range table.GetColumns() {
		if column.GetPrimary() && !slices.Contains(keys, column.GetName()) {
			keys = append(keys, column.GetName())
		}
	}
	return keys
}

// EffectiveNullable is a declared column's nullability as DuckDB stores it:
// a primary key column is NOT NULL whatever its declaration says.
func EffectiveNullable(table *protocol.DeclaredTable, column *protocol.DeclaredColumn) bool {
	return column.GetNullable() && !slices.Contains(PrimaryKey(table), column.GetName())
}

// EnumValues are the values of a declared enum column: the values it carries,
// or the values of the enum its type names. A column that names no enum has
// none.
func EnumValues(table *protocol.DeclaredTable, column *protocol.DeclaredColumn) []string {
	if values := column.GetEnumValues(); len(values) > 0 {
		return values
	}
	for _, enum := range table.GetEnums() {
		if enum.GetName() == strings.TrimSpace(column.GetType()) {
			return enum.GetValues()
		}
	}
	return nil
}

// ColumnType is the type a declared column is created with. An enum column
// is DuckDB's ENUM of its values, written in the column's definition: the
// provider creates no named type.
func ColumnType(table *protocol.DeclaredTable, column *protocol.DeclaredColumn) string {
	if values := EnumValues(table, column); len(values) > 0 {
		quoted := make([]string, len(values))
		for i, value := range values {
			quoted[i] = ddl.Literal(value)
		}
		return "ENUM(" + strings.Join(quoted, ", ") + ")"
	}
	return strings.TrimSpace(column.GetType())
}

// quotedLiteral is a value written as a SQL string literal.
var quotedLiteral = regexp.MustCompile(`^'((?:[^']|'')*)'$`)

// number is a decimal number as SQL writes one bare.
var number = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// numericTypes and booleanTypes are the type names, and their aliases,
// whose defaults DuckDB takes as bare numbers and booleans.
var (
	numericTypes = []string{"TINYINT", "INT1", "SMALLINT", "INT2", "SHORT", "INTEGER", "INT", "INT4", "SIGNED", "BIGINT", "INT8", "LONG",
		"HUGEINT", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "UHUGEINT", "FLOAT", "FLOAT4", "REAL", "DOUBLE", "FLOAT8",
		"DOUBLE PRECISION", "DECIMAL", "NUMERIC", "BIGNUM", "VARINT"}
	booleanTypes = []string{"BOOLEAN", "BOOL", "LOGICAL"}
)

// typeName is a declared type's name without its parameters, in upper case.
func typeName(declared string) string {
	name, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(declared)), "(")
	return strings.Join(strings.Fields(name), " ")
}

// DefaultSQL is the SQL that follows DEFAULT for a declared column, or nil
// when it declares no default. A literal on a numeric or boolean column is
// written bare and any other literal as a quoted string; an expression is
// written as declared.
func DefaultSQL(table *protocol.DeclaredTable, column *protocol.DeclaredColumn) *string {
	switch {
	case column.GetDefaultSet():
		value := strings.TrimSpace(column.GetDefaultValue())
		switch name := typeName(ColumnType(table, column)); {
		case quotedLiteral.MatchString(value):
			return new(value)
		case slices.Contains(numericTypes, name) && number.MatchString(value):
			return new(value)
		case slices.Contains(booleanTypes, name) && (strings.EqualFold(value, "true") || strings.EqualFold(value, "false")):
			return new(strings.ToLower(value))
		case strings.EqualFold(value, "null"):
			return new("NULL")
		}
		return new(ddl.Literal(column.GetDefaultValue()))
	case strings.TrimSpace(column.GetDefaultExpression()) != "":
		return new(strings.TrimSpace(column.GetDefaultExpression()))
	default:
		return nil
	}
}

// HeldColumn is the column a create-table or an add-column writes for a
// column as DuckDB holds the declaration: DuckDB's own type, default and
// generated expression, so the statement says what a read reports.
func HeldColumn(column catalog.Column) tableop.Column {
	return tableop.Column{Name: column.Name, Type: column.Type, Nullable: column.Nullable, Collation: column.Collation, Default: column.Default,
		Generated: column.Generated, Unique: column.Unique}
}

// CreateTable is the operation that creates a declared table as DuckDB holds
// it: the columns, key and CHECKs of held, which the backend read back from a
// scratch creation of the declaration, and the declaration's foreign keys.
// A column whose declared type states something DuckDB does not keep carries
// that type too, so the render reports what it leaves out.
func CreateTable(desired *protocol.DeclaredTable, held catalog.Table) (tableop.Operation, error) {
	operation := tableop.Operation{Action: tableop.CreateTable}
	for _, column := range held.Columns {
		created := HeldColumn(column)
		created.Declared = DeclaredLoss(desired, declaredColumn(desired, column.Name))
		operation.Columns = append(operation.Columns, created)
	}
	if held.PrimaryKey != nil {
		operation.PrimaryKey = slices.Clone(held.PrimaryKey.Columns)
	}
	for _, unique := range held.Uniques {
		if len(unique.Columns) > 1 {
			operation.Uniques = append(operation.Uniques, slices.Clone(unique.Columns))
		}
	}
	for _, check := range held.Checks {
		operation.Checks = append(operation.Checks, check.Expression)
	}
	keys, err := ForeignKeys(desired)
	if err != nil {
		return tableop.Operation{}, err
	}
	operation.ForeignKeys = keys
	return operation, nil
}

// ScratchTable is the operation the backend creates in a scratch database
// to learn how DuckDB holds a declared table: the declaration's columns as
// written, with their CHECKs and UNIQUEs, its key, its UNIQUEs over several
// columns and its table CHECKs, and no foreign key, since the tables a key
// references need not exist there.
func ScratchTable(table *protocol.DeclaredTable) tableop.Operation {
	operation := tableop.Operation{Action: tableop.CreateTable, PrimaryKey: PrimaryKey(table), Uniques: UniqueConstraints(table), Checks: TableChecks(table)}
	unique := UniqueColumns(table)
	for _, declared := range table.GetColumns() {
		column := tableop.Column{Name: declared.GetName(), Type: ColumnType(table, declared), Nullable: EffectiveNullable(table, declared),
			Collation: strings.TrimSpace(declared.GetCollation()), Default: DefaultSQL(table, declared), Unique: slices.Contains(unique, declared.GetName()),
			Check: strings.TrimSpace(declared.GetCheck())}
		if expression := strings.TrimSpace(declared.GetGeneratedExpression()); expression != "" {
			column.Generated, column.Default = new(expression), nil
		}
		operation.Columns = append(operation.Columns, column)
	}
	return operation
}

// TableChecks are the CHECK conditions a declared table states as table
// constraints, in declaration order.
func TableChecks(table *protocol.DeclaredTable) []string {
	var checks []string
	for _, constraint := range table.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "CHECK") && strings.TrimSpace(constraint.GetCheckExpression()) != "" {
			checks = append(checks, strings.TrimSpace(constraint.GetCheckExpression()))
		}
	}
	for _, expression := range table.GetChecks() {
		if strings.TrimSpace(expression) != "" {
			checks = append(checks, strings.TrimSpace(expression))
		}
	}
	return checks
}

// uniqueColumns are the columns a declared table makes UNIQUE with a table
// constraint of one column; a constraint of several is refused by Validate.
func uniqueColumns(table *protocol.DeclaredTable) []string {
	var columns []string
	for _, constraint := range table.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "UNIQUE") && len(constraint.GetColumns()) == 1 {
			columns = append(columns, constraint.GetColumns()[0])
		}
	}
	return columns
}

// UniqueConstraints are the UNIQUE constraints over several columns a
// declared table states, each with its columns in order, in declaration
// order.
func UniqueConstraints(table *protocol.DeclaredTable) [][]string {
	var uniques [][]string
	for _, constraint := range table.GetConstraints() {
		if strings.EqualFold(constraint.GetType(), "UNIQUE") && len(constraint.GetColumns()) > 1 {
			uniques = append(uniques, slices.Clone(constraint.GetColumns()))
		}
	}
	return uniques
}

// UniqueColumns are the columns a declared table makes UNIQUE, on the column
// or with a table constraint of one column.
func UniqueColumns(table *protocol.DeclaredTable) []string {
	columns := uniqueColumns(table)
	for _, column := range table.GetColumns() {
		if column.GetUnique() && !slices.Contains(columns, column.GetName()) {
			columns = append(columns, column.GetName())
		}
	}
	return columns
}

// ForeignKeys are the foreign keys a declared table states, on its columns
// and as table constraints, in declaration order. A key into another schema
// is refused: DuckDB creates none.
func ForeignKeys(table *protocol.DeclaredTable) ([]tableop.ForeignKey, error) {
	var keys []tableop.ForeignKey
	add := func(columns []string, reference string, refColumns []string) error {
		schema, refTable := splitQualified(reference)
		if schema != "" && !strings.EqualFold(schema, schemaOf(table)) {
			return fmt.Errorf("references a table in another schema, which DuckDB refuses")
		}
		key := tableop.ForeignKey{Columns: slices.Clone(columns), RefTable: refTable, RefColumns: slices.Clone(refColumns)}
		// A column's foreign key may reach the declaration twice, on the
		// column and as the table constraint it lowers to.
		if !slices.ContainsFunc(keys, func(held tableop.ForeignKey) bool { return sameKey(held, key) }) {
			keys = append(keys, key)
		}
		return nil
	}
	for _, column := range table.GetColumns() {
		if strings.TrimSpace(column.GetForeignReference()) == "" {
			continue
		}
		refTable, refColumns, ok := parseReference(column.GetForeignReference())
		if !ok {
			return nil, fmt.Errorf("column %q has a foreign reference that names no table and column", column.GetName())
		}
		if err := add([]string{column.GetName()}, refTable, refColumns); err != nil {
			return nil, fmt.Errorf("the foreign key of column %q %w", column.GetName(), err)
		}
	}
	for _, constraint := range table.GetConstraints() {
		if !strings.EqualFold(constraint.GetType(), "FOREIGN KEY") {
			continue
		}
		refColumns := slices.Clone(constraint.GetForeignColumns())
		if len(refColumns) == 0 && constraint.GetForeignColumn() != "" {
			refColumns = splitNames(constraint.GetForeignColumn())
		}
		if err := add(constraint.GetColumns(), constraint.GetForeignTable(), refColumns); err != nil {
			return nil, fmt.Errorf("foreign key %q %w", constraint.GetName(), err)
		}
	}
	return keys, nil
}

func sameKey(a, b tableop.ForeignKey) bool {
	return slices.Equal(a.Columns, b.Columns) && a.RefTable == b.RefTable && slices.Equal(a.RefColumns, b.RefColumns)
}

// schemaOf is a declared table's schema, main when it names none.
func schemaOf(table *protocol.DeclaredTable) string {
	if table.GetSchema() == "" {
		return DefaultSchema
	}
	return table.GetSchema()
}

// DefaultSchema is the schema DuckDB resolves an unqualified name in.
const DefaultSchema = "main"

// parseReference reads `table(column)`, `schema.table(column, ...)` or the
// shorthand `table`, whose referenced column is id, as Ptah's annotations
// write a foreign reference.
func parseReference(reference string) (string, []string, bool) {
	reference = strings.TrimSpace(reference)
	open, closing := strings.IndexByte(reference, '('), strings.LastIndexByte(reference, ')')
	if open < 0 && closing < 0 {
		return reference, []string{"id"}, reference != ""
	}
	if open <= 0 || closing != len(reference)-1 || strings.Count(reference, "(") != 1 || strings.Count(reference, ")") != 1 {
		return "", nil, false
	}
	table, columns := strings.TrimSpace(reference[:open]), splitNames(reference[open+1:closing])
	return table, columns, table != "" && len(columns) > 0
}

func splitNames(list string) []string {
	var names []string
	for name := range strings.SplitSeq(list, ",") {
		if name = strings.Trim(strings.TrimSpace(name), `"`); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// splitQualified splits `schema.table`; a bare table has no schema.
func splitQualified(reference string) (schema, table string) {
	if schema, table, found := strings.Cut(reference, "."); found {
		return strings.Trim(schema, `"`), strings.Trim(table, `"`)
	}
	return "", strings.Trim(reference, `"`)
}

// IndexOperation is the operation that creates a declared index. Validate
// has refused every index DuckDB cannot create as declared.
func IndexOperation(declared *protocol.DeclaredIndex) tableop.Operation {
	index := &tableop.Index{Name: declared.GetName(), Unique: declared.GetUnique()}
	if len(declared.GetParts()) > 0 {
		for _, part := range declared.GetParts() {
			index.Parts = append(index.Parts, tableop.IndexPart{Column: part.GetColumn(), Expression: strings.TrimSpace(part.GetExpression())})
		}
	} else {
		for _, field := range declared.GetFields() {
			index.Parts = append(index.Parts, tableop.IndexPart{Column: field})
		}
	}
	return tableop.Operation{Action: tableop.CreateIndex, Index: index}
}
