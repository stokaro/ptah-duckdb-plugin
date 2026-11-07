package tableop_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// The description test validates the model inside the engine; this one
// pins what it declares.
func TestModel_DeclaresTheOperationKind(t *testing.T) {
	c := testcheck.New(t)
	model := tableop.Model()
	c.Assert(model.GetKind(), qt.Equals, "github.com/stokaro/ptah-duckdb-plugin/table-operation")
	c.Assert(model.GetRepresentation(), qt.Equals, protocol.ValueInput_OPERATION)
}

// Every action captures, writes its statement, and says what it does to the
// rows; Statement writes the same SQL from the captured value.
func TestCapture_HappyPath(t *testing.T) {
	tests := []struct {
		name      string
		operation tableop.Operation
		statement string
		impact    protocol.ChangeSemantics_Impact
	}{
		{"create table", tableop.Operation{Action: tableop.CreateTable, Columns: []tableop.Column{{Name: "id", Type: "INTEGER"}, {Name: "p", Type: "INTEGER", Nullable: true}},
			PrimaryKey: []string{"id"}, Uniques: [][]string{{"id", "p"}}, Checks: []string{"id > 0"},
			ForeignKeys: []tableop.ForeignKey{{Columns: []string{"p"}, RefTable: "q", RefColumns: []string{"id"}}}},
			"CREATE TABLE \"main\".\"t\" (\n  \"id\" INTEGER NOT NULL,\n  \"p\" INTEGER,\n  PRIMARY KEY (\"id\"),\n  UNIQUE (\"id\", \"p\"),\n  CHECK (id > 0),\n" +
				"  FOREIGN KEY (\"p\") REFERENCES \"main\".\"q\" (\"id\")\n)", protocol.ChangeSemantics_ADDITIVE},
		{"drop table", tableop.Operation{Action: tableop.DropTable}, `DROP TABLE "main"."t"`, protocol.ChangeSemantics_DESTRUCTIVE},
		{"add column", tableop.Operation{Action: tableop.AddColumn, Column: &tableop.Column{Name: "c", Type: "VARCHAR", Nullable: true}},
			`ALTER TABLE "main"."t" ADD COLUMN "c" VARCHAR`, protocol.ChangeSemantics_ADDITIVE},
		{"add a NOT NULL column without a default", tableop.Operation{Action: tableop.AddColumn, Column: &tableop.Column{Name: "c", Type: "VARCHAR"}},
			`ALTER TABLE "main"."t" ADD COLUMN "c" VARCHAR NOT NULL`, protocol.ChangeSemantics_BEHAVIORAL},
		{"drop column", tableop.Operation{Action: tableop.DropColumn, Name: "c"}, `ALTER TABLE "main"."t" DROP COLUMN "c"`, protocol.ChangeSemantics_DESTRUCTIVE},
		{"change a type", tableop.Operation{Action: tableop.AlterColumnType, Name: "c", Type: "BIGINT"}, `ALTER TABLE "main"."t" ALTER COLUMN "c" TYPE BIGINT`,
			protocol.ChangeSemantics_BEHAVIORAL},
		{"set a default", tableop.Operation{Action: tableop.SetDefault, Name: "c", Default: "1"}, `ALTER TABLE "main"."t" ALTER COLUMN "c" SET DEFAULT 1`,
			protocol.ChangeSemantics_ADDITIVE},
		{"drop a default", tableop.Operation{Action: tableop.DropDefault, Name: "c"}, `ALTER TABLE "main"."t" ALTER COLUMN "c" DROP DEFAULT`,
			protocol.ChangeSemantics_ADDITIVE},
		{"set not null", tableop.Operation{Action: tableop.SetNotNull, Name: "c"}, `ALTER TABLE "main"."t" ALTER COLUMN "c" SET NOT NULL`,
			protocol.ChangeSemantics_BEHAVIORAL},
		{"drop not null", tableop.Operation{Action: tableop.DropNotNull, Name: "c"}, `ALTER TABLE "main"."t" ALTER COLUMN "c" DROP NOT NULL`,
			protocol.ChangeSemantics_ADDITIVE},
		{"add a primary key", tableop.Operation{Action: tableop.AddPrimaryKey, Key: []string{"a", "b"}}, `ALTER TABLE "main"."t" ADD PRIMARY KEY ("a", "b")`,
			protocol.ChangeSemantics_BEHAVIORAL},
		{"add unique", tableop.Operation{Action: tableop.AddUnique, Key: []string{"c"}}, `ALTER TABLE "main"."t" ADD UNIQUE ("c")`, protocol.ChangeSemantics_BEHAVIORAL},
		{"add a unique over two columns", tableop.Operation{Action: tableop.AddUnique, Key: []string{"a", "b"}}, `ALTER TABLE "main"."t" ADD UNIQUE ("a", "b")`,
			protocol.ChangeSemantics_BEHAVIORAL},
		{"create an index", tableop.Operation{Action: tableop.CreateIndex, Index: &tableop.Index{Name: "i", Parts: []tableop.IndexPart{{Column: "c"}}}},
			`CREATE INDEX "i" ON "main"."t" ("c")`, protocol.ChangeSemantics_ADDITIVE},
		{"create a unique index", tableop.Operation{Action: tableop.CreateIndex, Index: &tableop.Index{Name: "i", Unique: true,
			Parts: []tableop.IndexPart{{Expression: "lower(c)"}}}}, `CREATE UNIQUE INDEX "i" ON "main"."t" ((lower(c)))`, protocol.ChangeSemantics_BEHAVIORAL},
		{"drop an index", tableop.Operation{Action: tableop.DropIndex, Name: "i"}, `DROP INDEX "main"."i"`, protocol.ChangeSemantics_ADDITIVE},
		{"drop tables", tableop.Operation{Action: tableop.DropTables, Tables: []string{"t", "u"}}, "DROP TABLE \"main\".\"t\";\nDROP TABLE \"main\".\"u\"",
			protocol.ChangeSemantics_DESTRUCTIVE},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			captured, err := tableop.Capture("step", tableop.Owner("main", "t"), tc.operation)
			c.Assert(err, qt.IsNil)
			c.Assert(string(captured.GetCanonicalProjection()), qt.Equals, tc.statement)
			c.Assert(captured.GetOperationSemantics().GetImpact(), qt.Equals, tc.impact)
			c.Assert(captured.GetOperationSemantics().GetAccess(), qt.Equals, protocol.ChangeSemantics_UNCHANGED)
			statement, err := tableop.Statement(captured)
			c.Assert(err, qt.IsNil)
			c.Assert(statement, qt.Equals, tc.statement)
		})
	}
}

func TestCapture_FailurePath(t *testing.T) {
	tests := []struct {
		name      string
		owner     *protocol.Reference
		operation tableop.Operation
		want      string
	}{
		{"an unknown action", tableop.Owner("main", "t"), tableop.Operation{Action: "rename-table"}, `a DuckDB table operation has the unknown action "rename-table"`},
		{"an operand the action does not take", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.DropTable, Name: "x"},
			"a drop-table operation does not take name"},
		{"a missing operand", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.DropColumn}, "a drop-column operation names nothing"},
		{"a unique over no column", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.AddUnique}, "an operand the statement cannot carry: a UNIQUE needs a column"},
		{"a column the statement cannot add", tableop.Owner("main", "t"),
			tableop.Operation{Action: tableop.AddColumn, Column: &tableop.Column{Name: "c", Type: "INTEGER", Unique: true}},
			"an operand the statement cannot carry: DuckDB adds no column with a UNIQUE, a CHECK or a generated expression"},
		{"one table to drop as several", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.DropTables, Tables: []string{"t"}},
			"a drop-tables operation drops two or more tables, its owner first"},
		{"tables to drop without their owner first", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.DropTables, Tables: []string{"u", "t"}},
			"a drop-tables operation drops two or more tables, its owner first"},
		{"a table to drop twice", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.DropTables, Tables: []string{"t", "u", "u"}},
			"a drop-tables operation names each table once"},
		{"an owner with a database", &protocol.Reference{Engine: "duckdb", Kind: "table", Database: "app", Schema: "main", Name: "t"},
			tableop.Operation{Action: tableop.DropTable}, "a DuckDB table operation names a table by its schema and name and nothing else"},
		{"an owner of another engine", &protocol.Reference{Engine: "sqlite", Kind: "table", Schema: "main", Name: "t"},
			tableop.Operation{Action: tableop.DropTable}, "a DuckDB table operation names a table by its schema and name and nothing else"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			captured, err := tableop.Capture("step", tc.owner, tc.operation)
			c.Assert(err, qt.ErrorMatches, tc.want)
			c.Assert(captured, qt.IsNil)
		})
	}
}

// A value that changed after it was planned is refused, whether its payload
// or its model no longer matches.
func TestStatement_FailurePath(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		model   string
		want    string
	}{
		{"an edited payload", `{"action":"drop-table"}`, "", "a DuckDB table operation changed after it was planned"},
		{"an unknown field", `{"action":"drop-table","cascade":true}`, "", `a DuckDB table operation is not one the provider writes: json: unknown field "cascade"`},
		{"trailing data", `{"action":"drop-table"} {}`, "", "a DuckDB table operation carries data after its value"},
		{"another model", `{"action":"drop-column","name":"c"}`, "0", "a DuckDB table operation does not match its model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			captured, err := tableop.Capture("step", tableop.Owner("main", "t"), tableop.Operation{Action: tableop.DropColumn, Name: "c"})
			c.Assert(err, qt.IsNil)
			c.Assert(string(captured.GetValue().GetPayload()), qt.Equals, `{"action":"drop-column","name":"c"}`)
			edited := proto.CloneOf(captured)
			edited.Value.Payload = []byte(tc.payload)
			edited.Value.DefinitionSha256 += tc.model
			statement, err := tableop.Statement(edited)
			c.Assert(err, qt.ErrorMatches, tc.want)
			c.Assert(statement, qt.Equals, "")
		})
	}
}
