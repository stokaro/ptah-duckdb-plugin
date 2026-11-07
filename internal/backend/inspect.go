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
	"github.com/stokaro/ptah-duckdb-plugin/internal/history"
	"github.com/stokaro/ptah-duckdb-plugin/internal/metadata"
)

// foreignKeyRule is the delete and update rule of every DuckDB foreign key.
// DuckDB refuses CASCADE, SET NULL and SET DEFAULT when a foreign key is
// created, accepts RESTRICT and NO ACTION, and keeps neither:
// information_schema.referential_constraints reports NO ACTION for both.
const foreignKeyRule = "NO ACTION"

// Inspect reads the schemas of the session's database the request selects,
// every schema when it selects none: their base tables with their columns,
// primary keys, single-column UNIQUE constraints, CHECKs, foreign keys and
// indexes, and their views, sequences and comments, and lists the schemas
// themselves as namespaces. The snapshot is complete or the call fails with
// a reason that names what the read does not describe, because the host
// plans to create what a read leaves out. A host that does not read
// namespaces gets a read of main alone, and a read whose scope holds another
// schema is refused for it.
func (p *Provider) Inspect(ctx context.Context, request *protocol.InspectRequest) (*protocol.Schema, error) {
	current, err := p.lookup(request.GetSessionId())
	if err != nil {
		return nil, err
	}
	if slices.Contains(request.GetSchemas(), "") {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "a schema selector is empty")
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	engine := provider.NegotiatedDescription(Description(), provider.HostFeatures(ctx)).GetEngines()[0]
	schemas, err := catalog.Schemas(ctx, current.handle.Conn(), current.database)
	if err != nil {
		return nil, callFailed(ctx, "the provider could not read the DuckDB catalog")
	}
	scope := selected(schemas, request.GetSchemas())
	if err := checkScope(scope, engine.GetFeatures()); err != nil {
		return nil, err
	}
	reads, err := readSchemas(ctx, current, scope)
	if err != nil {
		return nil, err
	}
	reserved := reserve(current.database, reads)
	if err := carried(reads, engine.GetFeatures()); err != nil {
		return nil, err
	}
	snapshot := &protocol.Schema{
		Complete:        true,
		Engine:          engineID,
		TargetFeatures:  engine.GetFeatures(),
		Coverage:        &protocol.ModelCoverage{Representation: protocol.ValueInput_OBSERVED},
		ReservedObjects: reserved,
	}
	for _, read := range reads {
		for _, table := range read.Tables {
			snapshot.Tables = append(snapshot.Tables, observedTable(current.database, read.Name, table))
		}
	}
	if kinds := engine.GetCommonObjects().GetKinds(); len(kinds) > 0 {
		snapshot.CommonObjects = observedCommonObjects(current.database, reads, kinds)
	}
	for _, selector := range request.GetSchemas() {
		binding := &protocol.InspectionScopeBinding{Selector: selector, Namespaces: make([]*protocol.ModelNamespace, 0, 1)}
		for _, schema := range selected(schemas, []string{selector}) {
			binding.Namespaces = append(binding.Namespaces, namespace(current.database, schema))
		}
		snapshot.ScopeBindings = append(snapshot.ScopeBindings, binding)
	}
	return snapshot, nil
}

// reserve takes the provider's own tables out of reads and returns them as
// reserved objects: the revision table, the migration log and the tag table
// of Ptah's migrator, each told from a user's table by the provider's mark
// and its exact layout (see ownedtable), whatever its name. A plan never
// manages them, so it never drops the record of the migrations a database
// ran.
func reserve(database string, reads []catalog.Schema) []*protocol.ReservedObject {
	var reserved []*protocol.ReservedObject
	for i := range reads {
		reads[i].Tables = slices.DeleteFunc(reads[i].Tables, func(table catalog.Table) bool {
			purpose := reservedPurpose(reads[i].Name, table)
			if purpose == protocol.ReservedObject_PURPOSE_UNSPECIFIED {
				return false
			}
			reserved = append(reserved, &protocol.ReservedObject{Object: reference(database, reads[i].Name, "table", table.Name),
				NormalizedObject: normalizedReference(database, reads[i].Name, "table", table.Name), Purpose: purpose,
				ConflictKinds: []string{"table"}})
			return true
		})
	}
	return reserved
}

// reservedPurpose is what the provider keeps in a table, or no purpose for a
// user's table.
func reservedPurpose(schema string, table catalog.Table) protocol.ReservedObject_Purpose {
	switch {
	case history.Native(schema, table.Name).Holds(table), history.Atlas(schema, table.Name).Holds(table):
		return protocol.ReservedObject_HISTORY
	case metadata.Log(schema, table.Name).Holds(table):
		return protocol.ReservedObject_LOG
	case metadata.Tags(schema, table.Name).Holds(table):
		return protocol.ReservedObject_TAG
	}
	return protocol.ReservedObject_PURPOSE_UNSPECIFIED
}

// selected is the schemas among schemas the selectors name, compared without
// regard to ASCII case as DuckDB compares names, in the order of schemas.
// No selector selects every schema.
func selected(schemas, selectors []string) []string {
	if len(selectors) == 0 {
		return schemas
	}
	return slices.DeleteFunc(slices.Clone(schemas), func(schema string) bool {
		return !slices.ContainsFunc(selectors, func(selector string) bool { return strings.EqualFold(selector, schema) })
	})
}

// checkScope refuses a scope that holds a schema besides main for a host
// that does not read namespaces: it could not tell that schema from one the
// database does not have.
func checkScope(scope, features []string) error {
	if slices.Contains(features, provider.CommonNamespacesFeature) {
		return nil
	}
	for _, schema := range scope {
		if schema != defaultSchema {
			return providerruntime.ErrorWithReason(codes.FailedPrecondition,
				fmt.Sprintf("the read covers schema %q, which needs a host that reads %s; select main to read it alone", schema, provider.CommonNamespacesFeature))
		}
	}
	return nil
}

// readSchemas reads each schema of scope.
func readSchemas(ctx context.Context, current *session, scope []string) ([]catalog.Schema, error) {
	reads := make([]catalog.Schema, 0, len(scope))
	for _, schema := range scope {
		read, err := catalog.Read(ctx, current.handle.Conn(), current.database, schema)
		if unsupported, found := errors.AsType[*catalog.Unsupported](err); found {
			return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, unsupported.Reason)
		}
		if err != nil {
			return nil, callFailed(ctx, "the provider could not read the DuckDB catalog")
		}
		reads = append(reads, read)
	}
	return reads, nil
}

// carried refuses a read that holds something the host did not negotiate a
// way to receive. Leaving it out would read as its absence.
func carried(reads []catalog.Schema, features []string) error {
	need := func(feature, what string) error {
		if slices.Contains(features, feature) {
			return nil
		}
		return providerruntime.ErrorWithReason(codes.FailedPrecondition, fmt.Sprintf("%s needs a host that reads %s", what, feature))
	}
	for _, read := range reads {
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
				if err := need(check.feature, fmt.Sprintf("table %q.%q", read.Name, table.Name)); err != nil {
					return err
				}
			}
		}
		if len(read.Views) > 0 {
			if err := need(provider.CommonObjectsFeature, fmt.Sprintf("view %q.%q", read.Name, read.Views[0].Name)); err != nil {
				return err
			}
		}
		if len(read.Sequences) > 0 {
			if err := need(provider.CommonObjectsFeature, fmt.Sprintf("sequence %q.%q", read.Name, read.Sequences[0].Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// namespace is the namespace of schema in database.
func namespace(database, schema string) *protocol.ModelNamespace {
	return &protocol.ModelNamespace{
		Database: &protocol.NamespaceComponent{Source: database, Normalized: database},
		Catalog:  &protocol.NamespaceComponent{},
		Schema:   &protocol.NamespaceComponent{Source: schema, Normalized: foldASCII(schema)},
	}
}

// reference names an object of a schema.
func reference(database, schema, kind, name string) *protocol.Reference {
	return &protocol.Reference{Engine: engineID, Database: database, Schema: schema, Kind: kind, Name: name}
}

// normalizedReference names an object of a schema with the ASCII letters of
// the schema's and the object's names folded, as DuckDB compares names.
func normalizedReference(database, schema, kind, name string) *protocol.Reference {
	return reference(database, foldASCII(schema), kind, foldASCII(name))
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
func observedTable(database, schema string, table catalog.Table) *protocol.Table {
	result := &protocol.Table{Identity: reference(database, schema, "table", table.Name), NormalizedIdentity: normalizedReference(database, schema, "table", table.Name)}
	var keys []string
	if table.PrimaryKey != nil {
		keys = table.PrimaryKey.Columns
		result.PrimaryKey = &protocol.ObservedConstraint{Name: table.PrimaryKey.Name, TableName: table.Name, Schema: schema, Type: "PRIMARY KEY",
			ColumnNames: slices.Clone(keys)}
	}
	for i, column := range table.Columns {
		result.Columns = append(result.Columns, &protocol.Column{Name: column.Name, Observed: observedColumn(column, i+1, slices.Contains(keys, column.Name))})
	}
	for _, check := range table.Checks {
		result.Constraints = append(result.Constraints, &protocol.ObservedConstraint{Name: check.Name, TableName: table.Name, Schema: schema,
			Type: "CHECK", ColumnNames: slices.Clone(check.Columns), CheckClause: new(check.Expression)})
	}
	for _, key := range table.ForeignKeys {
		result.Constraints = append(result.Constraints, &protocol.ObservedConstraint{Name: key.Name, TableName: table.Name, Schema: schema,
			Type: "FOREIGN KEY", ColumnNames: slices.Clone(key.Columns), ForeignTable: new(key.RefTable), ForeignSchema: schema,
			ForeignColumns: slices.Clone(key.RefColumns), DeleteRule: new(foreignKeyRule), UpdateRule: new(foreignKeyRule)})
	}
	for _, index := range table.Indexes {
		observed := &protocol.ObservedIndex{Name: index.Name, TableName: table.Name, Schema: schema, Unique: index.Unique}
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
		DefaultExpression: column.Default, OrdinalPosition: int64(ordinal), PrimaryKey: primaryKey, Unique: column.Unique, Collation: column.Collation,
	}
	if column.Parsed.Kind == coltype.Decimal {
		observed.NumericPrecision, observed.NumericScale = new(int64(column.Parsed.Precision)), new(int64(column.Parsed.Scale))
	}
	if column.Generated != nil {
		observed.GeneratedExpression, observed.GeneratedKind = new(*column.Generated), "VIRTUAL"
	}
	return observed
}
