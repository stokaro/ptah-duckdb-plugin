package tableplan_test

import (
	"errors"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableplan"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestColumnType(t *testing.T) {
	tests := []struct {
		name   string
		table  *protocol.DeclaredTable
		column *protocol.DeclaredColumn
		want   string
	}{
		{"as declared", &protocol.DeclaredTable{}, &protocol.DeclaredColumn{Type: " VARCHAR(10) "}, "VARCHAR(10)"},
		{"enum values on the column", &protocol.DeclaredTable{}, &protocol.DeclaredColumn{Type: "mood", EnumValues: []string{"a", "it's"}}, "ENUM('a', 'it''s')"},
		{"a named enum", &protocol.DeclaredTable{Enums: []*protocol.DeclaredEnum{{Name: "mood", Values: []string{"x"}}}}, &protocol.DeclaredColumn{Type: "mood"},
			"ENUM('x')"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(tableplan.ColumnType(tc.table, tc.column), qt.Equals, tc.want)
		})
	}
}

func TestDefaultSQL(t *testing.T) {
	tests := []struct {
		name   string
		column *protocol.DeclaredColumn
		want   *string
	}{
		{"no default", &protocol.DeclaredColumn{Type: "INTEGER"}, nil},
		{"a number on a numeric column", &protocol.DeclaredColumn{Type: "DECIMAL(10,2)", DefaultSet: true, DefaultValue: "1.5"}, new("1.5")},
		{"a number on a text column", &protocol.DeclaredColumn{Type: "VARCHAR", DefaultSet: true, DefaultValue: "15"}, new("'15'")},
		{"a boolean", &protocol.DeclaredColumn{Type: "BOOLEAN", DefaultSet: true, DefaultValue: "TRUE"}, new("true")},
		{"a quoted literal", &protocol.DeclaredColumn{Type: "VARCHAR", DefaultSet: true, DefaultValue: "'it''s'"}, new("'it''s'")},
		{"a bare string", &protocol.DeclaredColumn{Type: "VARCHAR", DefaultSet: true, DefaultValue: "it's"}, new("'it''s'")},
		{"null", &protocol.DeclaredColumn{Type: "VARCHAR", DefaultSet: true, DefaultValue: "null"}, new("NULL")},
		{"an expression", &protocol.DeclaredColumn{Type: "TIMESTAMP", DefaultExpression: " now() "}, new("now()")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(tableplan.DefaultSQL(&protocol.DeclaredTable{}, tc.column), qt.DeepEquals, tc.want)
		})
	}
}

func TestForeignKeys_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	keys, err := tableplan.ForeignKeys(&protocol.DeclaredTable{
		Name: "posts",
		Columns: []*protocol.DeclaredColumn{
			{Name: "user_id", ForeignReference: "users(id)"},
			{Name: "owner", ForeignReference: "main.users"},
		},
		Constraints: []*protocol.DeclaredConstraint{
			{Name: "fk_user", Type: "FOREIGN KEY", Columns: []string{"user_id"}, ForeignTable: "users", ForeignColumns: []string{"id"}},
			{Name: "fk_pair", Type: "FOREIGN KEY", Columns: []string{"a", "b"}, ForeignTable: "pairs", ForeignColumn: "x, y"},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(keys, qt.DeepEquals, []tableop.ForeignKey{
		{Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}},
		{Columns: []string{"owner"}, RefTable: "users", RefColumns: []string{"id"}},
		{Columns: []string{"a", "b"}, RefTable: "pairs", RefColumns: []string{"x", "y"}},
	})
}

func TestForeignKeys_FailurePath(t *testing.T) {
	tests := []struct {
		name  string
		table *protocol.DeclaredTable
		want  string
	}{
		{"another schema", &protocol.DeclaredTable{Name: "p", Columns: []*protocol.DeclaredColumn{{Name: "u", ForeignReference: "other.users(id)"}}},
			`the foreign key of column "u" references a table in another schema, which DuckDB refuses`},
		{"an unreadable reference", &protocol.DeclaredTable{Name: "p", Columns: []*protocol.DeclaredColumn{{Name: "u", ForeignReference: "users(id"}}},
			`column "u" has a foreign reference that names no table and column`},
		{"a constraint into another schema", &protocol.DeclaredTable{Name: "p", Schema: "app", Constraints: []*protocol.DeclaredConstraint{
			{Name: "fk", Type: "FOREIGN KEY", Columns: []string{"u"}, ForeignTable: "main.users", ForeignColumns: []string{"id"}}}},
			`foreign key "fk" references a table in another schema, which DuckDB refuses`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			keys, err := tableplan.ForeignKeys(tc.table)
			c.Assert(err, qt.ErrorMatches, tc.want)
			c.Assert(keys, qt.IsNil)
		})
	}
}

// The notes say what DuckDB does not keep, so a plan that converges on its
// own terms does not drop what the author wrote in silence.
func TestCreationNotes(t *testing.T) {
	c := testcheck.New(t)
	subject := &protocol.ComparisonIdentity{Kind: "table", Schema: &protocol.ComparisonPart{Source: "main"}, Name: &protocol.ComparisonPart{Source: "users"}}
	table := &protocol.DeclaredTable{
		Name:           "users",
		PrimaryKeyName: "pk_users",
		Columns: []*protocol.DeclaredColumn{
			{Name: "email", Type: "VARCHAR(255)"},
			{Name: "code", Type: "char(2)", CheckName: "code_check"},
			{Name: "id", Type: "INTEGER"},
			{Name: "tags", Type: "VARCHAR(5)[]"},
		},
		Constraints: []*protocol.DeclaredConstraint{{Name: "code_check", Type: "CHECK"}},
	}
	c.Assert(tableplan.Declared{}.CreationNotes(subject, table), qt.DeepEquals, []string{
		`DUCKDB: column "email" of table "users" is declared VARCHAR(255); DuckDB keeps no length and holds it as VARCHAR, which takes text of any length.`,
		`DUCKDB: column "code" of table "users" is declared char(2); DuckDB keeps no length and holds it as VARCHAR, which takes text of any length.`,
		`DUCKDB: column "tags" of table "users" is declared VARCHAR(5)[]; DuckDB keeps no length and holds it as VARCHAR[], which takes text of any length.`,
		`DUCKDB: DuckDB keeps no constraint names; table "users" holds "pk_users", "code_check" under the names DuckDB gives them.`,
	})
}

func TestSchemaRefusal(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		features []string
		want     string
	}{
		{"main", "MAIN", nil, ""},
		{"no schema", "", nil, ""},
		{"another schema with namespaces", "app", []string{provider.CommonNamespacesFeature}, ""},
		{"another schema without namespaces", "app", []string{provider.ValidateSchemaFeature},
			`table "t" is in schema "app"; the host does not read schema.common-namespaces, so the provider reads and creates objects in main only`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(tableplan.SchemaRefusal("table", tc.schema, "t", tc.features), qt.Equals, tc.want)
		})
	}
}

func TestIndexRefusal(t *testing.T) {
	tests := []struct {
		name  string
		index *protocol.DeclaredIndex
		want  string
	}{
		{"columns", &protocol.DeclaredIndex{Name: "i", Fields: []string{"a", "b"}}, ""},
		{"an ART index", &protocol.DeclaredIndex{Name: "i", Type: "art", Fields: []string{"a"}}, ""},
		{"an expression", &protocol.DeclaredIndex{Name: "i", Parts: []*protocol.CapturedIndexPart{{Expression: "lower(a)"}}}, ""},
		{"a filter", &protocol.DeclaredIndex{Name: "i", Fields: []string{"a"}, Condition: "a > 0"}, "has a WHERE filter; DuckDB creates no partial index"},
		{"another method", &protocol.DeclaredIndex{Name: "i", Type: "HNSW", Fields: []string{"a"}},
			`uses the method "HNSW"; the provider creates DuckDB's own ART index only`},
		{"an operator class", &protocol.DeclaredIndex{Name: "i", Operator: "gin_trgm_ops", Fields: []string{"a"}}, "names an operator class; a DuckDB index has none"},
		{"storage parameters", &protocol.DeclaredIndex{Name: "i", StorageParameters: map[string]string{"fillfactor": "70"}, Fields: []string{"a"}},
			"sets storage parameters; a DuckDB index has none"},
		{"included columns", &protocol.DeclaredIndex{Name: "i", IncludeColumns: []string{"b"}, Fields: []string{"a"}}, "has INCLUDE columns; a DuckDB index has none"},
		{"nulls not distinct", &protocol.DeclaredIndex{Name: "i", NullsDistinct: new(false), Fields: []string{"a"}},
			"is NULLS NOT DISTINCT, which a DuckDB index cannot be"},
		{"invisible", &protocol.DeclaredIndex{Name: "i", Invisible: true, Fields: []string{"a"}}, "is invisible, which a DuckDB index cannot be"},
		{"no key", &protocol.DeclaredIndex{Name: "i"}, "has no key"},
		{"a descending key", &protocol.DeclaredIndex{Name: "i", Parts: []*protocol.CapturedIndexPart{{Column: "a", Descending: true}}},
			"states a key order or a NULLS placement; DuckDB's index keeps neither"},
		{"a NULLS placement", &protocol.DeclaredIndex{Name: "i", Parts: []*protocol.CapturedIndexPart{{Column: "a", Nulls: "FIRST"}}},
			"states a key order or a NULLS placement; DuckDB's index keeps neither"},
		{"a key prefix", &protocol.DeclaredIndex{Name: "i", Parts: []*protocol.CapturedIndexPart{{Column: "a", Prefix: "10"}}},
			"has a key with an operator class or a prefix; a DuckDB index has neither"},
		{"an empty key part", &protocol.DeclaredIndex{Name: "i", Parts: []*protocol.CapturedIndexPart{{}}}, "has a key part that is neither a column nor an expression"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(tableplan.IndexRefusal(tc.index), qt.Equals, tc.want)
		})
	}
}

// Validate reports every problem of a table in order: what DuckDB cannot
// hold first, then what the scratch creation refused.
func TestValidate(t *testing.T) {
	c := testcheck.New(t)
	request := &protocol.ValidateSchemaRequest{TargetFeatures: []string{provider.ValidateSchemaFeature}, Tables: []*protocol.ValidationTable{
		{Declaration: &protocol.DeclaredTable{Name: "fine", Columns: []*protocol.DeclaredColumn{{Name: "a", Type: "INTEGER"}}}},
		{Declaration: &protocol.DeclaredTable{Name: "bad", Schema: "app", Engine: "InnoDB", Columns: []*protocol.DeclaredColumn{
			{Name: "id", Type: "INTEGER", AutoIncrement: true},
			{Name: "g", Type: "INTEGER", GeneratedExpression: "id * 2", GeneratedKind: "stored"},
			{Name: "r", Type: "INTEGER", ForeignReference: "fine(a)", OnDelete: "CASCADE"},
		}, Constraints: []*protocol.DeclaredConstraint{
			{Name: "u2", Type: "UNIQUE", Columns: []string{"id", "g"}},
			{Name: "ex", Type: "EXCLUDE"},
		}, Indexes: []*protocol.DeclaredIndex{{Name: "p", Fields: []string{"id"}, Condition: "id > 0"}}}},
		{Declaration: &protocol.DeclaredTable{Name: "refused", Columns: []*protocol.DeclaredColumn{{Name: "a", Type: "NOPE"}}}},
	}}
	refusals := map[string]error{"refused": errors.New(`DuckDB refuses column "a" with the type NOPE, its collation or its default`)}
	hold := func(table *protocol.DeclaredTable) error { return refusals[table.GetName()] }
	c.Assert(tableplan.Validate(request, hold), qt.DeepEquals, []tableplan.Problem{
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "bad" is in schema "app"; the host does not read schema.common-namespaces, so the provider reads and creates objects in main only`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad", Message: `table "app.bad" names a storage engine; DuckDB has none`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "app.bad" column "id" is auto-increment or identity; DuckDB has neither, so declare a sequence and DEFAULT nextval('...')`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "app.bad" column "g" is a STORED generated column; DuckDB computes generated columns when it reads a row (VIRTUAL) only`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "app.bad" column "r" has a foreign key that has ON DELETE CASCADE; DuckDB has NO ACTION and RESTRICT only`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "app.bad" constraint "u2" is a UNIQUE over several columns, which a read reports only to a host that reads schema.observed-unique-constraints; declare a unique index instead`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "app.bad" constraint "ex" is an EXCLUDE constraint; DuckDB has none`},
		{Code: protocol.SchemaDiagnostic_UNSUPPORTED_FEATURE, Kind: "table", Object: "app.bad",
			Message: `table "app.bad" has index "p", which has a WHERE filter; DuckDB creates no partial index`},
		{Code: protocol.SchemaDiagnostic_INVALID_SCHEMA, Kind: "table", Object: "refused",
			Message: `DuckDB refuses column "a" with the type NOPE, its collation or its default`},
	})
}

// A host that reads UNIQUE constraints takes one over several columns back
// from a read, so the declaration is one the provider creates.
func TestValidate_AUniqueOverSeveralColumns(t *testing.T) {
	c := testcheck.New(t)
	request := &protocol.ValidateSchemaRequest{TargetFeatures: []string{provider.ObservedUniqueConstraintsFeature}, Tables: []*protocol.ValidationTable{
		{Declaration: &protocol.DeclaredTable{Name: "m", Columns: []*protocol.DeclaredColumn{{Name: "a", Type: "INTEGER"}, {Name: "b", Type: "INTEGER"}},
			Constraints: []*protocol.DeclaredConstraint{{Name: "m_ab", Type: "UNIQUE", Columns: []string{"a", "b"}}}}},
	}}
	c.Assert(tableplan.Validate(request, func(*protocol.DeclaredTable) error { return nil }), qt.IsNil)
	c.Assert(tableplan.UniqueConstraints(request.GetTables()[0].GetDeclaration()), qt.DeepEquals, [][]string{{"a", "b"}})
}
