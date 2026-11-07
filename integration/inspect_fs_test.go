//go:build integration

package integration_test

import (
	"context"
	"slices"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// readerFeatures are the two-way features a host that reads everything the
// provider reports lists.
func readerFeatures() []string {
	return []string{
		provider.ErrorReasonsFeature, provider.ObservedColumnsFeature, provider.ObservedPrimaryKeysFeature,
		provider.ObservedConstraintsFeature, provider.ObservedIndexesFeature, provider.CommonObjectsFeature, provider.CommonNamespacesFeature,
	}
}

// featuresWithout is readerFeatures without the ones dropped.
func featuresWithout(dropped ...string) []string {
	return slices.DeleteFunc(readerFeatures(), func(feature string) bool { return slices.Contains(dropped, feature) })
}

// inspectAs reads address in process as a host that lists features, and
// checks the snapshot with the validator the SDK's client runs.
func inspectAs(c *qt.C, features []string, address string, schemas ...string) (*protocol.Schema, error) {
	c.Helper()
	ctx := provider.ContextWithHostFeatures(c.Context(), features)
	p := backend.New()
	session, err := p.Open(ctx, openRequest(address))
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = p.Close(context.Background(), &protocol.CloseRequest{SessionId: session.GetId()}) })
	request := &protocol.InspectRequest{SessionId: session.GetId(), Schemas: schemas}
	snapshot, err := p.Inspect(ctx, request)
	if err != nil {
		return nil, err
	}
	_, err = provider.InspectionValues(provider.NegotiatedDescription(backend.Description(), features), request, snapshot)
	c.Assert(err, qt.IsNil)
	return snapshot, nil
}

func TestInspect_RichSchema(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "rich.duckdb", richSchema)
	snapshot, err := inspectAs(c, readerFeatures(), fileAddress(path))
	c.Assert(err, qt.IsNil)
	c.Assert(snapshot.GetComplete(), qt.IsTrue)
	c.Assert(snapshot.GetEngine(), qt.Equals, "duckdb")
	c.Assert(snapshot.GetTables(), qt.HasLen, 2)

	child, parent := snapshot.GetTables()[0], snapshot.GetTables()[1]
	c.Assert(parent.GetIdentity(), qt.CmpEquals(protocmp.Transform()), &protocol.Reference{Engine: "duckdb", Database: "rich", Schema: "main", Kind: "table", Name: "parent"})
	c.Assert(parent.GetColumns()[1], qt.CmpEquals(protocmp.Transform()), &protocol.Column{Name: "code", Observed: &protocol.ObservedColumn{
		Name: "code", DataType: "VARCHAR", FormattedType: "VARCHAR", ColumnType: "VARCHAR", IsNullable: "NO", OrdinalPosition: 2, Unique: true,
	}})
	c.Assert(parent.GetColumns()[2].GetObserved(), qt.CmpEquals(protocmp.Transform()), &protocol.ObservedColumn{
		Name: "amount", DataType: "DECIMAL(12,3)", FormattedType: "DECIMAL(12,3)", ColumnType: "DECIMAL(12,3)", IsNullable: "YES",
		DefaultExpression: new("1.5"), NumericPrecision: new(int64(12)), NumericScale: new(int64(3)), OrdinalPosition: 3,
	})
	c.Assert(child.GetColumns()[10].GetObserved(), qt.CmpEquals(protocmp.Transform()), &protocol.ObservedColumn{
		Name: "doubled", DataType: "INTEGER", FormattedType: "INTEGER", ColumnType: "INTEGER", IsNullable: "YES", OrdinalPosition: 11,
		GeneratedExpression: new("CAST((parent_id * 2) AS INTEGER)"), GeneratedKind: "VIRTUAL",
	})
	c.Assert(child.GetPrimaryKey(), qt.CmpEquals(protocmp.Transform()), &protocol.ObservedConstraint{
		Name: "child_id_pkey", TableName: "child", Schema: "main", Type: "PRIMARY KEY", ColumnNames: []string{"id"},
	})
	c.Assert(child.GetConstraints(), qt.CmpEquals(protocmp.Transform()), []*protocol.ObservedConstraint{
		{Name: "child_id_check", TableName: "child", Schema: "main", Type: "CHECK", ColumnNames: []string{"id"}, CheckClause: new("(id > 0)")},
		{Name: "child_parent_id_id_fkey", TableName: "child", Schema: "main", Type: "FOREIGN KEY", ColumnNames: []string{"parent_id"},
			ForeignTable: new("parent"), ForeignSchema: "main", ForeignColumns: []string{"id"}, DeleteRule: new("NO ACTION"), UpdateRule: new("NO ACTION")},
	})
	c.Assert(child.GetIndexes(), qt.CmpEquals(protocmp.Transform()), []*protocol.ObservedIndex{
		{Name: "child_expr", TableName: "child", Schema: "main", Unique: true, Parts: []*protocol.CapturedIndexPart{{Expression: "((id + 1))"}}},
		{Name: "child_parent_ts", TableName: "child", Schema: "main", Columns: []string{"parent_id", "ts"},
			Parts: []*protocol.CapturedIndexPart{{Column: "parent_id"}, {Column: "ts"}}},
	})

	common := snapshot.GetCommonObjects()
	c.Assert(common.GetViews(), qt.CmpEquals(protocmp.Transform()), []*protocol.ObservedView{{
		Identity: &protocol.Reference{Engine: "duckdb", Database: "rich", Schema: "main", Kind: "view", Name: "big_parents"},
		Body:     "SELECT id, code FROM parent WHERE (amount > 1)",
	}})
	c.Assert(common.GetSequences(), qt.CmpEquals(protocmp.Transform()), []*protocol.ObservedSequence{{
		Identity: &protocol.Reference{Engine: "duckdb", Database: "rich", Schema: "main", Kind: "sequence", Name: "seq_id"},
		DataType: "BIGINT", Start: new(int64(10)), Increment: new(int64(5)), MinValue: new(int64(1)), MaxValue: new(int64(1000)), Cycle: true,
	}})
	c.Assert(common.GetNamespaces(), qt.CmpEquals(protocmp.Transform()), []*protocol.ObservedNamespace{
		{Identity: &protocol.Reference{Engine: "duckdb", Database: "rich", Kind: "schema", Name: "main"}},
	})
	var comments []string
	for _, comment := range common.GetComments() {
		comments = append(comments, comment.GetObject().GetKind()+" "+comment.GetObject().GetName()+": "+comment.GetText())
	}
	c.Assert(comments, qt.DeepEquals, []string{
		"index child_parent_ts: lookup", "table parent: parents", "column code: the code", "view big_parents: big ones", "sequence seq_id: ids",
	})
}

// A table's normalized name folds ASCII letters, as DuckDB compares names,
// and keeps every other letter.
func TestInspect_NormalizesNamesAsDuckDBComparesThem(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "names.duckdb", []string{`CREATE TABLE "Mixed Ärger"(id INTEGER)`})
	snapshot, err := inspectAs(c, readerFeatures(), fileAddress(path))
	c.Assert(err, qt.IsNil)
	c.Assert(snapshot.GetTables()[0].GetIdentity().GetName(), qt.Equals, "Mixed Ärger")
	c.Assert(snapshot.GetTables()[0].GetNormalizedIdentity().GetName(), qt.Equals, "mixed Ärger")
}

// A read of the whole database reads every schema and lists each as a
// namespace; objects keep their schema.
func TestInspect_EverySchema(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "schemas.duckdb", []string{"CREATE TABLE kept(id INTEGER)", "CREATE SCHEMA Other", "CREATE TABLE other.elsewhere(id INTEGER)",
		"CREATE SCHEMA empty", "CREATE VIEW other.v AS SELECT 1 AS one"})
	snapshot, err := inspectAs(c, readerFeatures(), fileAddress(path))
	c.Assert(err, qt.IsNil)
	var tables []string
	for _, table := range snapshot.GetTables() {
		tables = append(tables, table.GetIdentity().GetSchema()+"."+table.GetIdentity().GetName()+" "+table.GetNormalizedIdentity().GetSchema())
	}
	c.Assert(tables, qt.DeepEquals, []string{"Other.elsewhere other", "main.kept main"})
	var namespaces []string
	for _, namespace := range snapshot.GetCommonObjects().GetNamespaces() {
		namespaces = append(namespaces, namespace.GetIdentity().GetName())
	}
	c.Assert(namespaces, qt.DeepEquals, []string{"Other", "empty", "main"})
	c.Assert(snapshot.GetCommonObjects().GetViews()[0].GetIdentity().GetSchema(), qt.Equals, "Other")
}

// A read that selects schemas reads them alone, binds each selector to the
// schema it names without regard to ASCII case, and binds a selector that
// names no schema to no namespace.
func TestInspect_SelectsSchemas(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "schemas.duckdb", []string{"CREATE TABLE kept(id INTEGER)", "CREATE SCHEMA Other", "CREATE TABLE other.elsewhere(id INTEGER)"})
	snapshot, err := inspectAs(c, readerFeatures(), fileAddress(path), "OTHER", "missing")
	c.Assert(err, qt.IsNil)
	c.Assert(snapshot.GetTables(), qt.HasLen, 1)
	c.Assert(snapshot.GetTables()[0].GetIdentity().GetName(), qt.Equals, "elsewhere")
	c.Assert(snapshot.GetCommonObjects().GetNamespaces(), qt.HasLen, 1)
	c.Assert(snapshot.GetScopeBindings(), qt.CmpEquals(protocmp.Transform()), []*protocol.InspectionScopeBinding{
		{Selector: "OTHER", Namespaces: []*protocol.ModelNamespace{{
			Database: &protocol.NamespaceComponent{Source: "schemas", Normalized: "schemas"},
			Catalog:  &protocol.NamespaceComponent{},
			Schema:   &protocol.NamespaceComponent{Source: "Other", Normalized: "other"},
		}}},
		{Selector: "missing", Namespaces: make([]*protocol.ModelNamespace, 0)},
	})
}

// A host that does not read namespaces gets main alone when it selects main.
func TestInspect_SelectsMainForAHostWithoutNamespaces(t *testing.T) {
	c := testcheck.New(t)
	path := seedFile(c, "schemas.duckdb", []string{"CREATE TABLE kept(id INTEGER)", "CREATE SCHEMA other", "CREATE TABLE other.elsewhere(id INTEGER)"})
	snapshot, err := inspectAs(c, featuresWithout(provider.CommonNamespacesFeature), fileAddress(path), "main")
	c.Assert(err, qt.IsNil)
	c.Assert(snapshot.GetTables(), qt.HasLen, 1)
	c.Assert(snapshot.GetCommonObjects().GetNamespaces(), qt.HasLen, 0)
}

func TestInspectEmptyDatabase_UnderEveryHost(t *testing.T) {
	for _, features := range [][]string{nil, readerFeatures()} {
		t.Run("", func(t *testing.T) {
			c := testcheck.New(t)
			snapshot, err := inspectAs(c, features, "duckdb://:memory:empty")
			c.Assert(err, qt.IsNil)
			c.Assert(snapshot.GetComplete(), qt.IsTrue)
			c.Assert(snapshot.GetTables(), qt.HasLen, 0)
		})
	}
}

func TestInspect_FailurePath(t *testing.T) {
	tests := []struct {
		name       string
		statements []string
		features   []string
		schemas    []string
		reason     string
	}{
		{"another schema for a host that reads no namespaces", []string{"CREATE SCHEMA other"}, featuresWithout(provider.CommonNamespacesFeature), nil,
			`the read covers schema "other", which needs a host that reads schema.common-namespaces; select main to read it alone`},
		{"a macro", []string{"CREATE MACRO add1(a) AS a + 1"}, readerFeatures(), nil,
			`schema "main" holds macro "add1", which the provider does not read`},
		{"a macro in another schema", []string{"CREATE SCHEMA tools", "CREATE MACRO tools.add1(a) AS a + 1"}, readerFeatures(), nil,
			`schema "tools" holds macro "add1", which the provider does not read`},
		{"a table for a host that reads no columns", []string{"CREATE TABLE t(a INTEGER)"}, []string{provider.ErrorReasonsFeature}, nil,
			`table "main"."t" needs a host that reads schema.observed-columns`},
		{"a check for a host that reads no constraints", []string{"CREATE TABLE t(a INTEGER CHECK (a > 0))"},
			[]string{provider.ErrorReasonsFeature, provider.ObservedColumnsFeature}, nil,
			`table "main"."t" needs a host that reads schema.observed-constraints`},
		{"a primary key for a host that reads no keys", []string{"CREATE TABLE t(a INTEGER PRIMARY KEY)"},
			[]string{provider.ErrorReasonsFeature, provider.ObservedColumnsFeature}, nil,
			`table "main"."t" needs a host that reads schema.observed-primary-keys`},
		{"an index for a host that reads no indexes", []string{"CREATE TABLE t(a INTEGER)", "CREATE INDEX ta ON t(a)"},
			[]string{provider.ErrorReasonsFeature, provider.ObservedColumnsFeature}, nil,
			`table "main"."t" needs a host that reads schema.observed-indexes`},
		{"a comment for a host that reads no common objects", []string{"CREATE TABLE t(a INTEGER)", "COMMENT ON TABLE t IS 'x'"},
			[]string{provider.ErrorReasonsFeature, provider.ObservedColumnsFeature}, nil,
			`table "main"."t" needs a host that reads schema.common-objects`},
		{"a view for a host that reads no common objects", []string{"CREATE VIEW v AS SELECT 1 AS one"},
			[]string{provider.ErrorReasonsFeature, provider.ObservedColumnsFeature}, nil,
			`view "main"."v" needs a host that reads schema.common-objects`},
		{"a sequence for a host that reads no common objects", []string{"CREATE SEQUENCE s"},
			[]string{provider.ErrorReasonsFeature, provider.ObservedColumnsFeature}, nil,
			`sequence "main"."s" needs a host that reads schema.common-objects`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			path := seedFile(c, "refused.duckdb", tc.statements)
			snapshot, err := inspectAs(c, tc.features, fileAddress(path), tc.schemas...)
			c.Assert(status.Code(err), qt.Equals, codes.FailedPrecondition)
			c.Assert(status.Convert(err).Message(), qt.Equals, tc.reason)
			c.Assert(snapshot, qt.IsNil)
		})
	}
}

func TestInspectUnknownSession_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	snapshot, err := backend.New().Inspect(c.Context(), &protocol.InspectRequest{SessionId: "never-opened"})
	c.Assert(status.Code(err), qt.Equals, codes.NotFound)
	c.Assert(snapshot, qt.IsNil)
}
