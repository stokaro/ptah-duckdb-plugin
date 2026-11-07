// Package ddl writes the schema statements of DuckDB the provider plans: one
// statement per call, every identifier double-quoted and every table named
// with its schema, so a statement means the same thing in a migration file
// as in the plan that wrote it.
//
// The package writes what it is given. Whether DuckDB can run a statement on
// a given table is the planner's question (see tableplan); this package only
// refuses an operand it cannot write, such as an empty name.
package ddl

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrOperand is the error every refusal wraps.
var ErrOperand = errors.New("an operand the statement cannot carry")

func operand(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOperand, fmt.Sprintf(format, args...))
}

// Quote writes name as a double-quoted identifier.
func Quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Qualified writes schema.name with both parts quoted.
func Qualified(schema, name string) string { return Quote(schema) + "." + Quote(name) }

// Literal writes value as a single-quoted string.
func Literal(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// Column is one column of a CREATE TABLE or an ADD COLUMN.
type Column struct {
	Name string
	// Type is the type as DuckDB reads it, such as INTEGER or
	// ENUM('a', 'b').
	Type     string
	Nullable bool
	// Collation follows COLLATE, or is empty.
	Collation string
	// Default is the SQL that follows DEFAULT, or nil.
	Default *string
	// Generated is the expression of a VIRTUAL generated column, or nil.
	Generated *string
	// Unique writes the column's own UNIQUE constraint.
	Unique bool
	// Check is the column's own CHECK condition, or empty.
	Check string
}

// ForeignKey is a FOREIGN KEY constraint of a CREATE TABLE. DuckDB refuses
// one into another schema, so it names the table in the table's own schema.
type ForeignKey struct {
	Columns    []string
	RefTable   string
	RefColumns []string
}

// Table is what a CREATE TABLE makes.
type Table struct {
	Columns []Column
	// PrimaryKey is the key in key order, or empty.
	PrimaryKey []string
	// Checks are the table's CHECK conditions.
	Checks      []string
	ForeignKeys []ForeignKey
}

// CreateTable writes CREATE TABLE schema.name with its columns, key, CHECKs
// and foreign keys. DuckDB adds no CHECK and no foreign key to a table that
// exists, so they are written here.
func CreateTable(schema, name string, table Table) (string, error) {
	if name == "" || len(table.Columns) == 0 {
		return "", operand("a table needs a name and a column")
	}
	parts := make([]string, 0, len(table.Columns)+len(table.Checks)+len(table.ForeignKeys)+1)
	for _, column := range table.Columns {
		definition, err := columnDefinition(column)
		if err != nil {
			return "", err
		}
		parts = append(parts, definition)
	}
	if len(table.PrimaryKey) > 0 {
		parts = append(parts, "PRIMARY KEY ("+quoteList(table.PrimaryKey)+")")
	}
	for _, check := range table.Checks {
		if strings.TrimSpace(check) == "" {
			return "", operand("a CHECK needs a condition")
		}
		parts = append(parts, "CHECK ("+strings.TrimSpace(check)+")")
	}
	for _, key := range table.ForeignKeys {
		if len(key.Columns) == 0 || len(key.Columns) != len(key.RefColumns) || key.RefTable == "" {
			return "", operand("a foreign key needs its columns, a table and as many referenced columns")
		}
		parts = append(parts, fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", quoteList(key.Columns), Qualified(schema, key.RefTable), quoteList(key.RefColumns)))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", Qualified(schema, name), strings.Join(parts, ",\n  ")), nil
}

// columnDefinition writes one column of a CREATE TABLE or an ADD COLUMN.
func columnDefinition(column Column) (string, error) {
	if column.Name == "" || strings.TrimSpace(column.Type) == "" {
		return "", operand("a column needs a name and a type")
	}
	definition := Quote(column.Name) + " " + strings.TrimSpace(column.Type)
	if column.Collation != "" {
		definition += " COLLATE " + column.Collation
	}
	if column.Generated != nil {
		definition += " GENERATED ALWAYS AS (" + strings.TrimSpace(*column.Generated) + ") VIRTUAL"
	}
	if column.Default != nil {
		definition += " DEFAULT " + strings.TrimSpace(*column.Default)
	}
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.Unique {
		definition += " UNIQUE"
	}
	if column.Check != "" {
		definition += " CHECK (" + strings.TrimSpace(column.Check) + ")"
	}
	return definition, nil
}

// DropTable writes DROP TABLE schema.name.
func DropTable(schema, name string) string { return "DROP TABLE " + Qualified(schema, name) }

// AddColumn writes ALTER TABLE ... ADD COLUMN. DuckDB adds no column with a
// constraint other than NOT NULL, so a column that carries one is refused.
func AddColumn(schema, table string, column Column) (string, error) {
	if column.Unique || column.Check != "" || column.Generated != nil {
		return "", operand("DuckDB adds no column with a UNIQUE, a CHECK or a generated expression")
	}
	definition, err := columnDefinition(column)
	if err != nil {
		return "", err
	}
	return alter(schema, table, "ADD COLUMN "+definition), nil
}

// DropColumn writes ALTER TABLE ... DROP COLUMN.
func DropColumn(schema, table, column string) string {
	return alter(schema, table, "DROP COLUMN "+Quote(column))
}

// AlterColumnType writes ALTER TABLE ... ALTER COLUMN ... TYPE, which DuckDB
// runs as a cast of every value.
func AlterColumnType(schema, table, column, columnType string) (string, error) {
	if strings.TrimSpace(columnType) == "" {
		return "", operand("a type change needs a type")
	}
	return alter(schema, table, "ALTER COLUMN "+Quote(column)+" TYPE "+strings.TrimSpace(columnType)), nil
}

// SetDefault writes ALTER TABLE ... ALTER COLUMN ... SET DEFAULT.
func SetDefault(schema, table, column, expression string) (string, error) {
	if strings.TrimSpace(expression) == "" {
		return "", operand("a default needs an expression")
	}
	return alter(schema, table, "ALTER COLUMN "+Quote(column)+" SET DEFAULT "+strings.TrimSpace(expression)), nil
}

// DropDefault writes ALTER TABLE ... ALTER COLUMN ... DROP DEFAULT.
func DropDefault(schema, table, column string) string {
	return alter(schema, table, "ALTER COLUMN "+Quote(column)+" DROP DEFAULT")
}

// SetNotNull writes ALTER TABLE ... ALTER COLUMN ... SET NOT NULL.
func SetNotNull(schema, table, column string) string {
	return alter(schema, table, "ALTER COLUMN "+Quote(column)+" SET NOT NULL")
}

// DropNotNull writes ALTER TABLE ... ALTER COLUMN ... DROP NOT NULL.
func DropNotNull(schema, table, column string) string {
	return alter(schema, table, "ALTER COLUMN "+Quote(column)+" DROP NOT NULL")
}

// AddPrimaryKey writes ALTER TABLE ... ADD PRIMARY KEY, which DuckDB runs on
// a table without one.
func AddPrimaryKey(schema, table string, columns []string) (string, error) {
	if len(columns) == 0 {
		return "", operand("a primary key needs a column")
	}
	return alter(schema, table, "ADD PRIMARY KEY ("+quoteList(columns)+")"), nil
}

// AddUnique writes ALTER TABLE ... ADD UNIQUE for one column.
func AddUnique(schema, table, column string) string {
	return alter(schema, table, "ADD UNIQUE ("+Quote(column)+")")
}

// IndexPart is one key part of an index: a column, or an expression as
// written.
type IndexPart struct {
	Column     string
	Expression string
}

// Index is what a CREATE INDEX makes.
type Index struct {
	Name   string
	Unique bool
	Parts  []IndexPart
}

// CreateIndex writes CREATE [UNIQUE] INDEX on schema.table. An index is in
// its table's schema.
func CreateIndex(schema, table string, index Index) (string, error) {
	if index.Name == "" || len(index.Parts) == 0 {
		return "", operand("an index needs a name and a key")
	}
	parts := make([]string, len(index.Parts))
	for i, part := range index.Parts {
		switch {
		case part.Column != "" && part.Expression == "":
			parts[i] = Quote(part.Column)
		case part.Column == "" && strings.TrimSpace(part.Expression) != "":
			parts[i] = "(" + strings.TrimSpace(part.Expression) + ")"
		default:
			return "", operand("an index key part is a column or an expression")
		}
	}
	unique := ""
	if index.Unique {
		unique = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", unique, Quote(index.Name), Qualified(schema, table), strings.Join(parts, ", ")), nil
}

// DropIndex writes DROP INDEX schema.name.
func DropIndex(schema, name string) string { return "DROP INDEX " + Qualified(schema, name) }

// CreateSchema writes CREATE SCHEMA name.
func CreateSchema(name string) string { return "CREATE SCHEMA " + Quote(name) }

// DropSchema writes DROP SCHEMA name. It never cascades: what the schema
// holds by then is something the plan does not know.
func DropSchema(name string) string { return "DROP SCHEMA " + Quote(name) }

// CreateView writes CREATE VIEW schema.name AS query.
func CreateView(schema, name, query string) (string, error) {
	return view("CREATE VIEW ", schema, name, query)
}

// ReplaceView writes CREATE OR REPLACE VIEW schema.name AS query.
func ReplaceView(schema, name, query string) (string, error) {
	return view("CREATE OR REPLACE VIEW ", schema, name, query)
}

func view(verb, schema, name, query string) (string, error) {
	if name == "" || strings.TrimSpace(query) == "" {
		return "", operand("a view needs a name and a query")
	}
	return verb + Qualified(schema, name) + " AS " + strings.TrimSpace(query), nil
}

// DropView writes DROP VIEW schema.name.
func DropView(schema, name string) string { return "DROP VIEW " + Qualified(schema, name) }

// Sequence is what a CREATE SEQUENCE makes. A nil field takes DuckDB's
// default.
type Sequence struct {
	Start, Increment, Min, Max *int64
	Cycle                      bool
}

// CreateSequence writes CREATE SEQUENCE schema.name with the options that
// are set.
func CreateSequence(schema, name string, sequence Sequence) (string, error) {
	if name == "" {
		return "", operand("a sequence needs a name")
	}
	var options []string
	for _, option := range []struct {
		keyword string
		value   *int64
	}{{"INCREMENT BY", sequence.Increment}, {"MINVALUE", sequence.Min}, {"MAXVALUE", sequence.Max}, {"START WITH", sequence.Start}} {
		if option.value != nil {
			options = append(options, option.keyword+" "+strconv.FormatInt(*option.value, 10))
		}
	}
	if sequence.Cycle {
		options = append(options, "CYCLE")
	}
	statement := "CREATE SEQUENCE " + Qualified(schema, name)
	if len(options) > 0 {
		statement += " " + strings.Join(options, " ")
	}
	return statement, nil
}

// DropSequence writes DROP SEQUENCE schema.name.
func DropSequence(schema, name string) string { return "DROP SEQUENCE " + Qualified(schema, name) }

// Comment writes COMMENT ON target IS text, or IS NULL for an empty text,
// which removes the comment. target is written by the caller, such as
// TABLE "main"."t".
func Comment(target, text string) string {
	if text == "" {
		return "COMMENT ON " + target + " IS NULL"
	}
	return "COMMENT ON " + target + " IS " + Literal(text)
}

func alter(schema, table, action string) string {
	return "ALTER TABLE " + Qualified(schema, table) + " " + action
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = Quote(name)
	}
	return strings.Join(quoted, ", ")
}
