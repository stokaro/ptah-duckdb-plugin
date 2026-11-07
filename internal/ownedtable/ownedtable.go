// Package ownedtable tells the tables the provider keeps its own records in,
// such as Ptah's revision table, from a user's, and checks that one holds
// exactly the layout the provider reads and writes before the provider reads
// or writes it.
//
// A table is the provider's when it carries the provider's mark as its
// comment and exactly the store's columns, types, nullability and primary
// key, and nothing that could change or refuse a row the provider writes: no
// other constraint, no index and no generated column. A familiar name proves
// nothing, so a table of a store's name without the mark, or with another
// layout, belongs to someone else, and the provider refuses it rather than
// adopt it. DuckDB keeps a table's comment in its catalog, so the mark
// travels with the file.
package ownedtable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
)

// DefaultSchema is the schema a store that names none lives in.
const DefaultSchema = "main"

// Querier runs statements on a session's connection or in a transaction.
// *sql.Conn and *sql.Tx implement it.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Column is one column of a layout: its name, its type as duckdb_columns
// writes it, and whether it takes NULL.
type Column struct {
	Name     string
	Type     string
	Nullable bool
}

// Definition is a table's identity and the layout the provider keeps in it.
type Definition struct {
	// Schema is the table's schema; empty selects the default schema.
	Schema string
	Name   string
	// Columns are the columns in the order the table declares them.
	Columns []Column
	// PrimaryKey are the key columns in key order.
	PrimaryKey []string
	// Mark is the comment the provider gives the table, which names what
	// it keeps there.
	Mark string
}

// ErrLayout reports a table that exists under the store's name and is not
// the provider's store. Every such error is a *LayoutError.
var ErrLayout = errors.New("the table is not the store the provider keeps its records in")

// LayoutError is an [ErrLayout] that says how the table differs.
type LayoutError struct {
	// Detail is a clause such as "its columns differ".
	Detail string
}

func (e *LayoutError) Error() string { return ErrLayout.Error() + ": " + e.Detail }

// Is reports that the error is an [ErrLayout].
func (e *LayoutError) Is(target error) bool { return target == ErrLayout }

// SchemaName is the schema the definition selects, with the default schema
// spelled out.
func (d Definition) SchemaName() string {
	if d.Schema == "" {
		return DefaultSchema
	}
	return d.Schema
}

// Quoted is the table's name as a statement writes it, qualified by its
// schema.
func (d Definition) Quoted() string { return ddl.Qualified(d.SchemaName(), d.Name) }

// Statements create the store: its schema when that is not the default one,
// the table that create writes with the table's quoted name in place of %s,
// and the mark. The caller runs them in one transaction, so a store exists
// with its mark or not at all.
func (d Definition) Statements(create string) []string {
	var statements []string
	if !strings.EqualFold(d.SchemaName(), DefaultSchema) {
		statements = append(statements, "CREATE SCHEMA IF NOT EXISTS "+ddl.Quote(d.SchemaName()))
	}
	return append(statements, fmt.Sprintf(create, d.Quoted()), "COMMENT ON TABLE "+d.Quoted()+" IS "+ddl.Literal(d.Mark))
}

// Holds reports whether a table a read describes is the definition's store,
// whatever its name: the mark, exactly the columns and the key, and nothing
// else. A read calls it to leave the provider's tables out of the schema it
// reports.
func (d Definition) Holds(table catalog.Table) bool {
	if table.Comment != d.Mark || len(table.Uniques) != 0 || len(table.Checks) != 0 || len(table.ForeignKeys) != 0 || len(table.Indexes) != 0 ||
		table.PrimaryKey == nil || !slices.Equal(table.PrimaryKey.Columns, d.PrimaryKey) || len(table.Columns) != len(d.Columns) {
		return false
	}
	for i, column := range table.Columns {
		if column.Name != d.Columns[i].Name || column.Type != d.Columns[i].Type || column.Nullable != d.Columns[i].Nullable ||
			column.Generated != nil || column.Unique || column.Collation != "" {
			return false
		}
	}
	return true
}

// Locate reports whether a table of the store's name exists, and when one
// does, checks that it is the store. One that is not fails with a
// *LayoutError; so does a view of that name.
func Locate(ctx context.Context, q Querier, d Definition) (bool, error) {
	var tables, views int
	var comment sql.NullString
	err := q.QueryRowContext(ctx, "SELECT count(*), max(comment) FROM duckdb_tables() "+
		"WHERE database_name = current_database() AND lower(schema_name) = lower($1) AND lower(table_name) = lower($2) AND NOT temporary",
		d.SchemaName(), d.Name).Scan(&tables, &comment)
	if err != nil {
		return false, err
	}
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_views() "+
		"WHERE database_name = current_database() AND lower(schema_name) = lower($1) AND lower(view_name) = lower($2) AND NOT internal",
		d.SchemaName(), d.Name).Scan(&views); err != nil {
		return false, err
	}
	switch {
	case views > 0:
		return true, &LayoutError{Detail: "it is a view"}
	case tables == 0:
		return false, nil
	case comment.String != d.Mark:
		return true, &LayoutError{Detail: "it does not carry the provider's mark as its comment"}
	}
	columns, err := readColumns(ctx, q, d)
	if err != nil {
		return true, err
	}
	if !slices.Equal(columns, d.Columns) {
		return true, &LayoutError{Detail: "its columns differ"}
	}
	key, others, err := readConstraints(ctx, q, d)
	if err != nil {
		return true, err
	}
	switch {
	case !slices.Equal(key, d.PrimaryKey):
		return true, &LayoutError{Detail: "its primary key differs"}
	case others:
		return true, &LayoutError{Detail: "it has a constraint besides its primary key, which could refuse a record"}
	}
	var indexes int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM duckdb_indexes() "+
		"WHERE database_name = current_database() AND lower(schema_name) = lower($1) AND lower(table_name) = lower($2)",
		d.SchemaName(), d.Name).Scan(&indexes); err != nil {
		return true, err
	}
	if indexes != 0 {
		return true, &LayoutError{Detail: "it has an index, which could refuse a record"}
	}
	return true, nil
}

// readColumns reads the table's columns in table order. A generated column
// is not one the provider writes, and reads as a type no layout holds.
func readColumns(ctx context.Context, q Querier, d Definition) ([]Column, error) {
	rows, err := q.QueryContext(ctx, "SELECT column_name, data_type, is_nullable, is_generated FROM duckdb_columns() "+
		"WHERE database_name = current_database() AND lower(schema_name) = lower($1) AND lower(table_name) = lower($2) ORDER BY column_index",
		d.SchemaName(), d.Name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var columns []Column
	for rows.Next() {
		var column Column
		var generated bool
		if err := rows.Scan(&column.Name, &column.Type, &column.Nullable, &generated); err != nil {
			return nil, err
		}
		if generated {
			column.Type = "generated " + column.Type
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

// readConstraints reads the table's primary key, and whether it has another
// constraint than its key and its NOT NULLs.
func readConstraints(ctx context.Context, q Querier, d Definition) (key []string, others bool, err error) {
	rows, err := q.QueryContext(ctx, "SELECT constraint_type, constraint_column_names FROM duckdb_constraints() "+
		"WHERE database_name = current_database() AND lower(schema_name) = lower($1) AND lower(table_name) = lower($2) AND constraint_type <> 'NOT NULL'",
		d.SchemaName(), d.Name)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var names any
		if err := rows.Scan(&kind, &names); err != nil {
			return nil, false, err
		}
		if kind != "PRIMARY KEY" || key != nil {
			others = true
			continue
		}
		list, _ := names.([]any)
		for _, name := range list {
			text, _ := name.(string)
			key = append(key, text)
		}
	}
	return key, others, rows.Err()
}
