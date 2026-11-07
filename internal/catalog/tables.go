package catalog

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/stokaro/ptah-duckdb-plugin/internal/coltype"
	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbsql"
)

// readTables reads the base tables of a schema, sorted by name, with their
// columns, constraints and indexes.
func readTables(ctx context.Context, conn Querier, database, schema string) ([]Table, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name, comment, sql FROM duckdb_tables() WHERE database_name = $1 AND schema_name = $2 AND NOT internal AND NOT temporary "+
			"ORDER BY table_name", database, schema)
	if err != nil {
		return nil, err
	}
	var tables []Table
	var statements []sql.NullString
	for rows.Next() {
		var table Table
		var comment, statement sql.NullString
		if err := rows.Scan(&table.Name, &comment, &statement); err != nil {
			_ = rows.Close()
			return nil, err
		}
		table.Comment = text(comment)
		tables = append(tables, table)
		statements = append(statements, statement)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, read := range []func(context.Context, Querier, string, string, []Table) error{readColumns, readConstraints, readIndexes} {
		if err := read(ctx, conn, database, schema, tables); err != nil {
			return nil, err
		}
	}
	for i := range tables {
		if err := readClauses(&tables[i], statements[i]); err != nil {
			return nil, err
		}
	}
	return tables, nil
}

// readClauses reads from a table's CREATE statement what duckdb_columns does
// not report: a column's collation, which it carries, and a column's
// compression, which no observed column has a field for and is refused.
func readClauses(table *Table, statement sql.NullString) error {
	if !statement.Valid {
		return unsupported("table %q reports no statement, which the provider reads its columns' collations from", table.Name)
	}
	clauses, err := duckdbsql.ParseTableColumns(statement.String)
	if err != nil {
		return unsupported("the provider cannot read the statement of table %q", table.Name)
	}
	for i := range table.Columns {
		found, ok := clauses[table.Columns[i].Name]
		if !ok {
			return unsupported("the statement of table %q does not name its column %q", table.Name, table.Columns[i].Name)
		}
		if found.Compression {
			return unsupported("column %q of table %q names a compression, which the provider does not read", table.Columns[i].Name, table.Name)
		}
		table.Columns[i].Collation = found.Collation
	}
	return nil
}

// readColumns fills the columns of tables in table order. duckdb_columns also
// lists the columns of views, which are skipped.
func readColumns(ctx context.Context, conn Querier, database, schema string, tables []Table) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name, column_name, data_type, column_default, is_nullable, is_generated, generation_expression, comment "+
			"FROM duckdb_columns() WHERE database_name = $1 AND schema_name = $2 AND NOT internal ORDER BY table_name, column_index", database, schema)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tableName string
		var column Column
		var columnDefault, generation, comment sql.NullString
		var generated bool
		if err := rows.Scan(&tableName, &column.Name, &column.Type, &columnDefault, &column.Nullable, &generated, &generation, &comment); err != nil {
			return err
		}
		at := indexOfTable(tables, tableName)
		if at < 0 {
			continue
		}
		if column.Parsed, err = coltype.Parse(column.Type); err != nil {
			return unsupported("column %q of table %q has a type the provider does not read", column.Name, tableName)
		}
		column.Comment = text(comment)
		// DuckDB reports a generated column's expression as its default too.
		switch {
		case generated && generation.Valid:
			column.Generated = new(generation.String)
		case generated:
			return unsupported("generated column %q of table %q reports no expression", column.Name, tableName)
		case columnDefault.Valid:
			column.Default = new(columnDefault.String)
		}
		tables[at].Columns = append(tables[at].Columns, column)
	}
	return rows.Err()
}

// readConstraints fills the primary key, the single-column UNIQUE flags, the
// CHECKs and the foreign keys of tables. NOT NULL is the columns'
// nullability, which readColumns read.
func readConstraints(ctx context.Context, conn Querier, database, schema string, tables []Table) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name, constraint_type, constraint_name, expression, constraint_column_names, referenced_table, referenced_column_names "+
			"FROM duckdb_constraints() WHERE database_name = $1 AND schema_name = $2 AND constraint_type <> 'NOT NULL' "+
			"ORDER BY table_name, constraint_index", database, schema)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	names := make(map[[2]string]bool)
	for rows.Next() {
		var tableName, kind, name string
		var expression, refTable sql.NullString
		var columnsValue, refColumnsValue any
		if err := rows.Scan(&tableName, &kind, &name, &expression, &columnsValue, &refTable, &refColumnsValue); err != nil {
			return err
		}
		at := indexOfTable(tables, tableName)
		if at < 0 {
			continue
		}
		if names[[2]string{tableName, name}] {
			return unsupported("table %q holds two constraints DuckDB names %q; DuckDB keeps no constraint names, so a read cannot tell them apart",
				tableName, name)
		}
		names[[2]string{tableName, name}] = true
		columns, err := stringList(columnsValue)
		if err != nil {
			return err
		}
		refColumns, err := stringList(refColumnsValue)
		if err != nil {
			return err
		}
		if err := placeConstraint(&tables[at], kind, name, columns, expression, refTable, refColumns); err != nil {
			return err
		}
	}
	return rows.Err()
}

// placeConstraint records one constraint on table.
func placeConstraint(table *Table, kind, name string, columns []string, expression, refTable sql.NullString, refColumns []string) error {
	switch kind {
	case "PRIMARY KEY":
		table.PrimaryKey = &Key{Name: name, Columns: columns}
	case "UNIQUE":
		if len(columns) == 0 {
			return unsupported("UNIQUE constraint %q of table %q reports no columns", name, table.Name)
		}
		table.Uniques = append(table.Uniques, Key{Name: name, Columns: columns})
		if len(columns) > 1 {
			break
		}
		at := slices.IndexFunc(table.Columns, func(column Column) bool { return column.Name == columns[0] })
		if at < 0 || table.Columns[at].Unique {
			return unsupported("table %q has a UNIQUE constraint the provider cannot place on one of its columns", table.Name)
		}
		table.Columns[at].Unique = true
	case "CHECK":
		if !expression.Valid || strings.TrimSpace(expression.String) == "" {
			return unsupported("CHECK constraint %q of table %q reports no expression", name, table.Name)
		}
		table.Checks = append(table.Checks, Check{Name: name, Columns: columns, Expression: expression.String})
	case "FOREIGN KEY":
		if !refTable.Valid || len(refColumns) != len(columns) {
			return unsupported("foreign key %q of table %q reports no referenced columns", name, table.Name)
		}
		table.ForeignKeys = append(table.ForeignKeys, ForeignKey{Name: name, Columns: columns, RefTable: refTable.String, RefColumns: refColumns})
	default:
		return unsupported("table %q has a %s constraint, which the provider does not read", table.Name, kind)
	}
	return nil
}

// readIndexes fills the secondary indexes of tables, sorted by name, read
// from the statement DuckDB keeps for each.
func readIndexes(ctx context.Context, conn Querier, database, schema string, tables []Table) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name, index_name, is_unique, is_primary, comment, sql FROM duckdb_indexes() "+
			"WHERE database_name = $1 AND schema_name = $2 ORDER BY table_name, index_name", database, schema)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tableName string
		var index Index
		var primary bool
		var comment, statement sql.NullString
		if err := rows.Scan(&tableName, &index.Name, &index.Unique, &primary, &comment, &statement); err != nil {
			return err
		}
		at := indexOfTable(tables, tableName)
		if at < 0 {
			return unsupported("index %q is on %q, which is not a table the provider reads", index.Name, tableName)
		}
		if primary || !statement.Valid {
			return unsupported("index %q of table %q is not one the provider reads", index.Name, tableName)
		}
		parsed, err := duckdbsql.ParseIndex(statement.String)
		if err != nil {
			return unsupported("the provider cannot read the key of index %q of table %q", index.Name, tableName)
		}
		if parsed.Method != "" {
			return unsupported("index %q of table %q uses method %s, which the provider does not read", index.Name, tableName, parsed.Method)
		}
		if parsed.Unique != index.Unique {
			return unsupported("index %q of table %q reports a uniqueness its statement does not", index.Name, tableName)
		}
		index.Parts, index.Comment = parsed.Parts, text(comment)
		tables[at].Indexes = append(tables[at].Indexes, index)
	}
	return rows.Err()
}

// readViews reads the views of a schema, sorted by name, with their queries.
func readViews(ctx context.Context, conn Querier, database, schema string) ([]View, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT view_name, comment, sql FROM duckdb_views() WHERE database_name = $1 AND schema_name = $2 AND NOT internal AND NOT temporary "+
			"ORDER BY view_name", database, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var views []View
	for rows.Next() {
		var view View
		var comment, statement sql.NullString
		if err := rows.Scan(&view.Name, &comment, &statement); err != nil {
			return nil, err
		}
		if !statement.Valid {
			return nil, unsupported("view %q reports no query", view.Name)
		}
		view.Query, err = duckdbsql.ParseView(statement.String)
		switch {
		case errors.Is(err, duckdbsql.ErrViewColumns):
			return nil, unsupported("view %q names its columns, which the provider does not read yet", view.Name)
		case err != nil:
			return nil, unsupported("the provider cannot read the query of view %q", view.Name)
		}
		view.Comment = text(comment)
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return views, refuseViewColumnComments(ctx, conn, database, schema, views)
}

// refuseViewColumnComments refuses a comment on a column of a view, which no
// comment the provider reports can name.
func refuseViewColumnComments(ctx context.Context, conn Querier, database, schema string, views []View) error {
	if len(views) == 0 {
		return nil
	}
	rows, err := conn.QueryContext(ctx,
		"SELECT table_name, column_name FROM duckdb_columns() WHERE database_name = $1 AND schema_name = $2 AND NOT internal "+
			"AND comment IS NOT NULL AND comment <> '' ORDER BY table_name, column_index", database, schema)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var relation, column string
		if err := rows.Scan(&relation, &column); err != nil {
			return err
		}
		if slices.ContainsFunc(views, func(view View) bool { return view.Name == relation }) {
			return unsupported("column %q of view %q has a comment, which the provider does not read", column, relation)
		}
	}
	return rows.Err()
}

// readSequences reads the sequences of a schema, sorted by name. The start
// is the value the sequence was created with, not its position.
func readSequences(ctx context.Context, conn Querier, database, schema string) ([]Sequence, error) {
	rows, err := conn.QueryContext(ctx,
		"SELECT sequence_name, comment, start_value, increment_by, min_value, max_value, cycle FROM duckdb_sequences() "+
			"WHERE database_name = $1 AND schema_name = $2 AND NOT temporary ORDER BY sequence_name", database, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var sequences []Sequence
	for rows.Next() {
		var sequence Sequence
		var comment sql.NullString
		if err := rows.Scan(&sequence.Name, &comment, &sequence.Start, &sequence.Increment, &sequence.Min, &sequence.Max, &sequence.Cycle); err != nil {
			return nil, err
		}
		sequence.Comment = text(comment)
		sequences = append(sequences, sequence)
	}
	return sequences, rows.Err()
}
