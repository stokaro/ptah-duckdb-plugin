package backend

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	providerruntime "github.com/stokaro/ptah/provider-sdk/runtime"
	"google.golang.org/grpc/codes"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/coltype"
)

// foreignKeyRule is the delete and update rule of every DuckDB foreign key.
// DuckDB refuses CASCADE, SET NULL and SET DEFAULT when a foreign key is
// created, accepts RESTRICT and NO ACTION, and keeps neither:
// information_schema.referential_constraints reports NO ACTION for both.
const foreignKeyRule = "NO ACTION"

// Inspect reads the main schema of the session's database: its base tables
// with their columns, primary keys, single-column UNIQUE constraints, CHECKs,
// foreign keys and indexes, and its views, sequences and comments. The
// snapshot is complete or the call fails with a reason that names what the
// read does not describe, because the host plans to create what a read
// leaves out. A read of the whole database also fails when the database has
// a schema besides main, which the provider does not read yet; a read that
// selects main reads it alone.
func (p *Provider) Inspect(ctx context.Context, request *protocol.InspectRequest) (*protocol.Schema, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	if err := checkSelectors(request.GetSchemas()); err != nil {
		return nil, err
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if len(request.GetSchemas()) == 0 {
		if err := refuseOtherSchemas(ctx, current); err != nil {
			return nil, err
		}
	}
	read, err := readMain(ctx, current)
	if err != nil {
		return nil, err
	}
	features := negotiatedFeatures(ctx)
	if err := carried(read, features); err != nil {
		return nil, err
	}
	snapshot := &protocol.Schema{
		Complete:       true,
		Engine:         engineID,
		TargetFeatures: features,
		Coverage:       &protocol.ModelCoverage{Representation: protocol.ValueInput_OBSERVED},
	}
	for _, table := range read.Tables {
		snapshot.Tables = append(snapshot.Tables, observedTable(current.database, table))
	}
	if slices.Contains(features, provider.CommonObjectsFeature) {
		snapshot.CommonObjects = observedCommonObjects(current.database, read)
	}
	for _, selector := range request.GetSchemas() {
		snapshot.ScopeBindings = append(snapshot.ScopeBindings, &protocol.InspectionScopeBinding{
			Selector:   selector,
			Namespaces: []*protocol.ModelNamespace{mainNamespace(current.database)},
		})
	}
	return snapshot, nil
}

// checkSelectors refuses a schema selector other than main, in any ASCII
// case. Namespaces reach the protocol in a later contract; until then the
// provider reads the main schema only.
func checkSelectors(selectors []string) error {
	for _, selector := range selectors {
		if !strings.EqualFold(selector, defaultSchema) {
			return providerruntime.ErrorWithReason(codes.FailedPrecondition,
				fmt.Sprintf("the provider reads the main schema only, and the read selects schema %q", selector))
		}
	}
	return nil
}

// refuseOtherSchemas refuses a read of the whole database when it holds a
// schema besides main, which the read would leave out.
func refuseOtherSchemas(ctx context.Context, current *session) error {
	others, err := catalog.UserSchemas(ctx, current.handle.Conn(), current.database)
	if err != nil {
		return callFailed(ctx, "the provider could not read the DuckDB catalog")
	}
	if len(others) > 0 {
		return providerruntime.ErrorWithReason(codes.FailedPrecondition, fmt.Sprintf(
			"the database has schema %q, and the provider reads the main schema only; select main to read it alone", others[0]))
	}
	return nil
}

// readMain reads the main schema.
func readMain(ctx context.Context, current *session) (catalog.Schema, error) {
	read, err := catalog.Read(ctx, current.handle.Conn(), current.database, defaultSchema)
	if unsupported, found := errors.AsType[*catalog.Unsupported](err); found {
		return catalog.Schema{}, providerruntime.ErrorWithReason(codes.FailedPrecondition, unsupported.Reason)
	}
	if err != nil {
		return catalog.Schema{}, callFailed(ctx, "the provider could not read the DuckDB catalog")
	}
	return read, nil
}

// carried refuses a read that holds something the host did not negotiate a
// way to receive. Leaving it out would read as its absence.
func carried(read catalog.Schema, features []string) error {
	need := func(feature, what string) error {
		if slices.Contains(features, feature) {
			return nil
		}
		return providerruntime.ErrorWithReason(codes.FailedPrecondition, fmt.Sprintf("%s needs a host that reads %s", what, feature))
	}
	for _, table := range read.Tables {
		checks := []struct {
			present bool
			feature string
		}{
			{true, provider.ObservedColumnsFeature},
			{table.PrimaryKey != nil, provider.ObservedPrimaryKeysFeature},
			{len(table.Indexes) > 0, provider.ObservedIndexesFeature},
			{len(table.Checks)+len(table.ForeignKeys) > 0, provider.ObservedConstraintsFeature},
			{table.Comment != "" || slices.ContainsFunc(table.Columns, func(column catalog.Column) bool { return column.Comment != "" }) ||
				slices.ContainsFunc(table.Indexes, func(index catalog.Index) bool { return index.Comment != "" }), provider.CommonObjectsFeature},
		}
		for _, check := range checks {
			if !check.present {
				continue
			}
			if err := need(check.feature, fmt.Sprintf("table %q", table.Name)); err != nil {
				return err
			}
		}
	}
	if len(read.Views) > 0 {
		return need(provider.CommonObjectsFeature, fmt.Sprintf("view %q", read.Views[0].Name))
	}
	if len(read.Sequences) > 0 {
		return need(provider.CommonObjectsFeature, fmt.Sprintf("sequence %q", read.Sequences[0].Name))
	}
	return nil
}

// mainNamespace is the main schema of database, the only namespace a read
// covers.
func mainNamespace(database string) *protocol.ModelNamespace {
	return &protocol.ModelNamespace{
		Database: &protocol.NamespaceComponent{Source: database, Normalized: database},
		Catalog:  &protocol.NamespaceComponent{},
		Schema:   &protocol.NamespaceComponent{Source: defaultSchema, Normalized: defaultSchema},
	}
}

// reference names an object of the main schema.
func reference(database, kind, name string) *protocol.Reference {
	return &protocol.Reference{Engine: engineID, Database: database, Schema: defaultSchema, Kind: kind, Name: name}
}

// normalizedReference names an object of the main schema with its name's
// ASCII letters folded, as DuckDB compares names.
func normalizedReference(database, kind, name string) *protocol.Reference {
	return reference(database, kind, foldASCII(name))
}

// foldASCII lowers the ASCII letters of name and keeps every other
// character.
func foldASCII(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, name)
}

// observedTable is a read table as the protocol carries it: each column as
// one complete observation, the primary key in key order, the CHECKs and
// foreign keys, and the indexes.
func observedTable(database string, table catalog.Table) *protocol.Table {
	result := &protocol.Table{Identity: reference(database, "table", table.Name), NormalizedIdentity: normalizedReference(database, "table", table.Name)}
	var keys []string
	if table.PrimaryKey != nil {
		keys = table.PrimaryKey.Columns
		result.PrimaryKey = &protocol.ObservedConstraint{Name: table.PrimaryKey.Name, TableName: table.Name, Schema: defaultSchema, Type: "PRIMARY KEY",
			ColumnNames: slices.Clone(keys)}
	}
	for i, column := range table.Columns {
		result.Columns = append(result.Columns, &protocol.Column{Name: column.Name, Observed: observedColumn(column, i+1, slices.Contains(keys, column.Name))})
	}
	for _, check := range table.Checks {
		result.Constraints = append(result.Constraints, &protocol.ObservedConstraint{Name: check.Name, TableName: table.Name, Schema: defaultSchema,
			Type: "CHECK", ColumnNames: slices.Clone(check.Columns), CheckClause: new(check.Expression)})
	}
	for _, key := range table.ForeignKeys {
		result.Constraints = append(result.Constraints, &protocol.ObservedConstraint{Name: key.Name, TableName: table.Name, Schema: defaultSchema,
			Type: "FOREIGN KEY", ColumnNames: slices.Clone(key.Columns), ForeignTable: new(key.RefTable), ForeignSchema: defaultSchema,
			ForeignColumns: slices.Clone(key.RefColumns), DeleteRule: new(foreignKeyRule), UpdateRule: new(foreignKeyRule)})
	}
	for _, index := range table.Indexes {
		observed := &protocol.ObservedIndex{Name: index.Name, TableName: table.Name, Schema: defaultSchema, Unique: index.Unique}
		for _, part := range index.Parts {
			if part.Column != "" {
				observed.Columns = append(observed.Columns, part.Column)
			}
			observed.Parts = append(observed.Parts, &protocol.CapturedIndexPart{Column: part.Column, Expression: part.Expression})
		}
		result.Indexes = append(result.Indexes, observed)
	}
	return result
}

// observedColumn is one column's complete observation. The type is DuckDB's
// spelling; a DECIMAL also states its precision and scale.
func observedColumn(column catalog.Column, ordinal int, primaryKey bool) *protocol.ObservedColumn {
	nullable := "YES"
	if !column.Nullable {
		nullable = "NO"
	}
	observed := &protocol.ObservedColumn{
		Name: column.Name, DataType: column.Type, FormattedType: column.Type, ColumnType: column.Type, IsNullable: nullable,
		DefaultExpression: column.Default, OrdinalPosition: int64(ordinal), PrimaryKey: primaryKey, Unique: column.Unique,
	}
	if column.Parsed.Kind == coltype.Decimal {
		observed.NumericPrecision, observed.NumericScale = new(int64(column.Parsed.Precision)), new(int64(column.Parsed.Scale))
	}
	if column.Generated != nil {
		observed.GeneratedExpression, observed.GeneratedKind = new(*column.Generated), "VIRTUAL"
	}
	return observed
}
