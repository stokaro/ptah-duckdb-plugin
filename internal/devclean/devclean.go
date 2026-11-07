// Package devclean decides what a cleanup of a DuckDB dev database removes,
// in which order and with which statements, and judges the statements a
// migration replay runs on one. It reads the database's catalog functions
// and runs the cleanup's statements in the transaction it is given.
//
// A cleanup removes every object of the schemas in its scope: a schema other
// than main with DROP SCHEMA ... CASCADE, which removes what it holds, and
// the objects of main one by one, since DuckDB drops no main schema. In main
// the views and macros go first, because DuckDB records no dependency of a
// view on a table and would leave a view behind; then the tables, each after
// the tables whose foreign keys reference it; then the sequences their
// defaults draw from; then the types. Indexes go with their tables. Temporary
// objects belong to a connection, not to the database, and are left out.
package devclean

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
	"github.com/stokaro/ptah-duckdb-plugin/internal/ownedtable"
)

// The kinds of the objects a cleanup lists, as a dev object reference names
// them.
const (
	KindSchema     = "schema"
	KindTable      = "table"
	KindView       = "view"
	KindIndex      = "index"
	KindSequence   = "sequence"
	KindMacro      = "macro"
	KindTableMacro = "table macro"
	KindType       = "type"
)

// DefaultSchema is the schema DuckDB drops no part of but its objects.
const DefaultSchema = "main"

// Object is one object a cleanup finds.
type Object struct {
	Kind, Schema, Name string
}

// Inventory is every object of the database in a cleanup's scope, in the
// order the cleanup removes them, and the steps that remove them.
type Inventory struct {
	Objects []Object
	Steps   []Step
}

// Step is one statement of a cleanup and the object it removes, with what
// that object holds: an index goes with its table, and everything in a
// schema other than main with the schema.
type Step struct {
	Statement string
	Object    Object
}

// Read lists the objects of the schemas in scope, every schema of the
// database when scope is empty. A schema is named without regard to ASCII
// case, as DuckDB names one.
func Read(ctx context.Context, q ownedtable.Querier, scope []string) (Inventory, error) {
	schemas, err := names(ctx, q, "SELECT schema_name FROM duckdb_schemas() WHERE database_name = current_database() ORDER BY schema_name")
	if err != nil {
		return Inventory{}, err
	}
	var inventory Inventory
	for _, schema := range schemas {
		if len(scope) > 0 && !slices.ContainsFunc(scope, func(selector string) bool { return strings.EqualFold(selector, schema) }) {
			continue
		}
		objects, err := contents(ctx, q, schema)
		if err != nil {
			return Inventory{}, err
		}
		if !strings.EqualFold(schema, DefaultSchema) {
			object := Object{Kind: KindSchema, Name: schema}
			inventory.Objects = append(append(inventory.Objects, objects...), object)
			inventory.Steps = append(inventory.Steps, Step{Statement: "DROP SCHEMA " + ddl.Quote(schema) + " CASCADE", Object: object})
			continue
		}
		order, err := tableOrder(ctx, q, schema, objects)
		if err != nil {
			return Inventory{}, err
		}
		for _, object := range order {
			inventory.Objects = append(inventory.Objects, object)
			if statement := drop(object); statement != "" {
				inventory.Steps = append(inventory.Steps, Step{Statement: statement, Object: object})
			}
		}
	}
	return inventory, nil
}

// contents lists the objects of one schema by kind, in the order a cleanup of
// main removes them.
func contents(ctx context.Context, q ownedtable.Querier, schema string) ([]Object, error) {
	queries := []struct {
		kind, query string
	}{
		{KindView, "SELECT view_name FROM duckdb_views() WHERE database_name = current_database() AND schema_name = $1 AND NOT internal AND NOT temporary ORDER BY 1"},
		{KindMacro, "SELECT DISTINCT function_name FROM duckdb_functions() WHERE database_name = current_database() AND schema_name = $1 AND NOT internal " +
			"AND function_type = 'macro' ORDER BY 1"},
		{KindTableMacro, "SELECT DISTINCT function_name FROM duckdb_functions() WHERE database_name = current_database() AND schema_name = $1 AND NOT internal " +
			"AND function_type = 'table_macro' ORDER BY 1"},
		{KindIndex, "SELECT index_name FROM duckdb_indexes() WHERE database_name = current_database() AND schema_name = $1 ORDER BY 1"},
		{KindTable, "SELECT table_name FROM duckdb_tables() WHERE database_name = current_database() AND schema_name = $1 AND NOT temporary ORDER BY 1"},
		{KindSequence, "SELECT sequence_name FROM duckdb_sequences() WHERE database_name = current_database() AND schema_name = $1 AND NOT temporary ORDER BY 1"},
		{KindType, "SELECT type_name FROM duckdb_types() WHERE database_name = current_database() AND schema_name = $1 AND NOT internal ORDER BY 1"},
	}
	var objects []Object
	for _, query := range queries {
		found, err := names(ctx, q, query.query, schema)
		if err != nil {
			return nil, err
		}
		for _, name := range found {
			objects = append(objects, Object{Kind: query.kind, Schema: schema, Name: name})
		}
	}
	return objects, nil
}

// tableOrder orders the tables of objects so each comes after every table
// whose foreign key references it. DuckDB creates a foreign key only to a
// table that exists, so the keys form no cycle.
func tableOrder(ctx context.Context, q ownedtable.Querier, schema string, objects []Object) ([]Object, error) {
	rows, err := q.QueryContext(ctx, "SELECT table_name, referenced_table FROM duckdb_constraints() "+
		"WHERE database_name = current_database() AND schema_name = $1 AND constraint_type = 'FOREIGN KEY'", schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	referencedBy := make(map[string][]string)
	for rows.Next() {
		var table, referenced string
		if err := rows.Scan(&table, &referenced); err != nil {
			return nil, err
		}
		if !strings.EqualFold(table, referenced) {
			referencedBy[strings.ToLower(referenced)] = append(referencedBy[strings.ToLower(referenced)], strings.ToLower(table))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var tables, ordered []Object
	for _, object := range objects {
		if object.Kind == KindTable {
			tables = append(tables, object)
		}
	}
	for len(tables) > 0 {
		next := slices.IndexFunc(tables, func(table Object) bool {
			return !slices.ContainsFunc(tables, func(other Object) bool {
				return slices.Contains(referencedBy[strings.ToLower(table.Name)], strings.ToLower(other.Name))
			})
		})
		next = max(next, 0)
		ordered = append(ordered, tables[next])
		tables = slices.Delete(tables, next, next+1)
	}
	// The tables keep their place among the kinds, in the order found.
	result := make([]Object, 0, len(objects))
	for _, object := range objects {
		if object.Kind != KindTable {
			result = append(result, object)
			continue
		}
		result = append(result, ordered...)
		ordered = nil
	}
	return result, nil
}

// drop is the statement that removes one object of main, or the empty
// string for an index, which goes with its table.
func drop(object Object) string {
	name := ddl.Qualified(object.Schema, object.Name)
	switch object.Kind {
	case KindView:
		return "DROP VIEW " + name
	case KindMacro:
		return "DROP MACRO " + name
	case KindTableMacro:
		return "DROP MACRO TABLE " + name
	case KindTable:
		return "DROP TABLE " + name
	case KindSequence:
		return "DROP SEQUENCE " + name
	case KindType:
		return "DROP TYPE " + name
	}
	return ""
}

// names reads one column of names.
func names(ctx context.Context, q ownedtable.Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var found []string
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		found = append(found, name.String)
	}
	return found, rows.Err()
}
