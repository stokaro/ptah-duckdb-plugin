// Package catalog reads one schema of a DuckDB database from DuckDB's own
// catalog functions (duckdb_tables, duckdb_columns, duckdb_constraints,
// duckdb_indexes, duckdb_views, duckdb_sequences, duckdb_types and
// duckdb_functions) into the objects the provider describes: tables with
// their columns, primary key, single-column UNIQUE constraints, CHECKs,
// foreign keys and indexes, views, sequences and comments.
//
// A read describes the schema completely or fails with an *Unsupported that
// names what it does not describe, because the host plans to create what a
// read leaves out. Each refusal is a property of DuckDB measured on the
// embedded version, or a shape the protocol cannot carry yet:
//
//   - a macro or a type created with CREATE TYPE, which the provider does
//     not model; a column of such a type holds the type it stands for, and
//     reads as that;
//   - a column type the provider does not know (see coltype);
//   - a UNIQUE constraint over more than one column, which no field of an
//     observed table carries;
//   - two constraints of one table under one name: DuckDB keeps no
//     constraint name and names each after its table and columns, so two
//     CHECKs over the same columns share a name and cannot be told apart;
//   - a view that names its columns, and an index the provider cannot read
//     from its statement, such as one with a method other than ART.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/stokaro/ptah-duckdb-plugin/internal/coltype"
	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbsql"
)

// Unsupported is an object a read does not describe, with the reason the
// provider gives. The reason names the object and quotes no value.
type Unsupported struct {
	Reason string
}

func (u *Unsupported) Error() string { return u.Reason }

func unsupported(format string, args ...any) error {
	return &Unsupported{Reason: fmt.Sprintf(format, args...)}
}

// Schema is what a read found in one schema.
type Schema struct {
	Name      string
	Tables    []Table
	Views     []View
	Sequences []Sequence
}

// Table is one base table.
type Table struct {
	Name    string
	Comment string
	Columns []Column
	// PrimaryKey is nil for a table without one; its columns are in key order.
	PrimaryKey  *Key
	Checks      []Check
	ForeignKeys []ForeignKey
	Indexes     []Index
}

// Column is one column of a table, in table order.
type Column struct {
	Name string
	// Type is the type as the catalog writes it, and Parsed its structure.
	Type   string
	Parsed coltype.Type
	// Nullable is false for a NOT NULL column.
	Nullable bool
	// Default is the default expression as the catalog writes it, nil for a
	// column without one. A generated column has none.
	Default *string
	// Generated is the expression of a generated column, which DuckDB
	// computes when it reads the row (VIRTUAL; it refuses STORED).
	Generated *string
	// Unique is set for a column a single-column UNIQUE constraint covers.
	Unique bool
	// Collation is the collation the column's definition names, which only
	// the table's CREATE statement reports.
	Collation string
	Comment   string
}

// Key is a named list of columns in order.
type Key struct {
	Name    string
	Columns []string
}

// Check is a CHECK constraint: DuckDB's name for it, the columns it reads
// and its expression as DuckDB keeps it.
type Check struct {
	Name       string
	Columns    []string
	Expression string
}

// ForeignKey is a FOREIGN KEY constraint. DuckDB refuses a foreign key into
// another schema, so the referenced table is in the table's own schema.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   string
	RefColumns []string
}

// Index is a secondary index DuckDB's ART builds. DuckDB keeps no order and
// no NULLS placement in an index key: DESC and NULLS LAST are accepted and
// dropped.
type Index struct {
	Name    string
	Unique  bool
	Parts   []duckdbsql.IndexPart
	Comment string
}

// View is a view and its query as DuckDB stores it.
type View struct {
	Name    string
	Query   string
	Comment string
}

// Sequence is a sequence. DuckDB's sequences are 64-bit integers with no
// cache and no owner.
type Sequence struct {
	Name                       string
	Start, Increment, Min, Max int64
	Cycle                      bool
	Comment                    string
}

// Querier runs a query; a *sql.Conn is one.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Schemas lists the schemas of database by name: main, which DuckDB marks
// internal although it holds the user's objects, and every schema a
// statement created.
func Schemas(ctx context.Context, conn Querier, database string) ([]string, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT schema_name FROM duckdb_schemas() WHERE database_name = $1 AND (schema_name = 'main' OR NOT internal) ORDER BY schema_name", database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var schemas []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		schemas = append(schemas, name)
	}
	return schemas, rows.Err()
}

// Read reads schema of database. It fails with an *Unsupported for an object
// it does not describe, and with the driver's error for a failed query.
func Read(ctx context.Context, conn Querier, database, schema string) (Schema, error) {
	if err := refuseUnmodeled(ctx, conn, database, schema); err != nil {
		return Schema{}, err
	}
	tables, err := readTables(ctx, conn, database, schema)
	if err != nil {
		return Schema{}, err
	}
	views, err := readViews(ctx, conn, database, schema)
	if err != nil {
		return Schema{}, err
	}
	sequences, err := readSequences(ctx, conn, database, schema)
	if err != nil {
		return Schema{}, err
	}
	return Schema{Name: schema, Tables: tables, Views: views, Sequences: sequences}, nil
}

// refuseUnmodeled refuses a macro or a named type in the schema.
func refuseUnmodeled(ctx context.Context, conn Querier, database, schema string) error {
	var name, kind string
	err := queryRow(ctx, conn, &[]any{&name, &kind},
		"SELECT function_name, function_type FROM duckdb_functions() WHERE database_name = $1 AND schema_name = $2 AND NOT internal "+
			"ORDER BY function_name LIMIT 1", database, schema)
	switch {
	case err == nil:
		return unsupported("schema %q holds %s %q, which the provider does not read", schema, macroKind(kind), name)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	err = queryRow(ctx, conn, &[]any{&name},
		"SELECT type_name FROM duckdb_types() WHERE database_name = $1 AND schema_name = $2 AND NOT internal ORDER BY type_name LIMIT 1",
		database, schema)
	switch {
	case err == nil:
		return unsupported("schema %q holds type %q, which the provider does not read", schema, name)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	return nil
}

// macroKind names a user function the way CREATE MACRO does.
func macroKind(kind string) string {
	switch kind {
	case "macro":
		return "macro"
	case "table_macro":
		return "table macro"
	}
	return "function"
}

// queryRow scans the single row of a query into targets, or returns
// sql.ErrNoRows.
func queryRow(ctx context.Context, conn Querier, targets *[]any, query string, args ...any) error {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(*targets...); err != nil {
		return err
	}
	return rows.Err()
}

// strings converts a VARCHAR[] the driver scanned into Go strings.
func stringList(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("a list column scanned as %T", value)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("a list element scanned as %T", item)
		}
		result = append(result, text)
	}
	return result, nil
}

// text is the value of a nullable VARCHAR, with NULL read as the empty
// string. DuckDB keeps an empty comment apart from none, and both mean that
// the object has no comment to show.
func text(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// indexOfTable finds the table name among tables.
func indexOfTable(tables []Table, name string) int {
	return slices.IndexFunc(tables, func(table Table) bool { return table.Name == name })
}
