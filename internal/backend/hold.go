package backend

import (
	"context"
	"errors"
	"fmt"

	protocol "ptah.run/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/scratch"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
)

// hold returns a declared table as DuckDB holds it: the table created in the
// scratch database, without foreign keys, and read back. DuckDB's refusal
// names the column that caused it where one column alone does.
func (p *Provider) hold(ctx context.Context, table *protocol.DeclaredTable) (catalog.Table, error) {
	operation := tableplan.ScratchTable(table)
	var sequences [][]string
	for _, column := range operation.Columns {
		if column.Default == nil {
			continue
		}
		names, err := p.scratch.Sequences(ctx, *column.Default)
		if err != nil {
			return catalog.Table{}, err
		}
		sequences = append(sequences, names...)
	}
	held, err := p.scratch.Table(ctx, sequences, func(schema string) (string, error) { return operation.SQL(schema, table.GetName()) })
	if errors.Is(err, scratch.ErrRefused) {
		return catalog.Table{}, p.refusedColumn(ctx, table, operation, sequences)
	}
	if unsupported, found := errors.AsType[*catalog.Unsupported](err); found {
		return catalog.Table{}, errors.New(unsupported.Reason)
	}
	if err != nil {
		return catalog.Table{}, err
	}
	return held, nil
}

// refusedColumn names the first column DuckDB refuses on its own, or says
// that DuckDB refuses the table's key or CHECKs.
func (p *Provider) refusedColumn(ctx context.Context, table *protocol.DeclaredTable, whole tableop.Operation, sequences [][]string) error {
	for _, column := range whole.Columns {
		alone := tableop.Operation{Action: tableop.CreateTable, Columns: []tableop.Column{{Name: column.Name, Type: column.Type, Nullable: true,
			Collation: column.Collation, Default: column.Default}}}
		if _, err := p.scratch.Table(ctx, sequences, func(schema string) (string, error) { return alone.SQL(schema, table.GetName()) }); errors.Is(err, scratch.ErrRefused) {
			return fmt.Errorf("DuckDB refuses column %q with the type %s, its collation or its default", column.Name, column.Type)
		}
	}
	return fmt.Errorf("DuckDB refuses the table's key, a generated expression or a CHECK")
}
