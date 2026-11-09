package backend_test

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"google.golang.org/protobuf/proto"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/tableop"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// renderRequest is a render of a table creation whose columns DuckDB keeps
// in part, and of an alteration that gives a column such a type, for a host
// that lists hostFeatures.
func renderRequest(c *qt.C, hostFeatures []string) (*protocol.RenderRequest, *protocol.Description) {
	c.Helper()
	description := provider.NegotiatedDescription(backend.Description(), hostFeatures)
	create, err := tableop.Capture("create", tableop.Owner("main", "users"), tableop.Operation{Action: tableop.CreateTable,
		Columns: []tableop.Column{
			{Name: "id", Type: "INTEGER"},
			{Name: "email", Type: "VARCHAR", Nullable: true, Declared: "VARCHAR(255)"},
			{Name: "flags", Type: "BIT", Nullable: true, Declared: "BIT(8)"},
		}, PrimaryKey: []string{"id"}})
	c.Assert(err, qt.IsNil)
	alter, err := tableop.Capture("alter", tableop.Owner("main", "users"),
		tableop.Operation{Action: tableop.AlterColumnType, Name: "seen", Type: "TIMESTAMP_MS", Declared: "TIMESTAMP(1)"})
	c.Assert(err, qt.IsNil)
	return &protocol.RenderRequest{Engine: "duckdb", TargetFeatures: description.GetEngines()[0].GetFeatures(), Changes: []*protocol.Change{
		{Id: "create", Object: proto.CloneOf(create.GetValue().GetOwner()),
			Operation: &protocol.Change_Native{Native: &protocol.NativeOperation{Value: create, Role: protocol.NativeOperation_STATEMENT}}},
		{Id: "alter", Object: proto.CloneOf(alter.GetValue().GetOwner()), Operation: &protocol.Change_Native{Native: &protocol.NativeOperation{
			Value: alter, Role: protocol.NativeOperation_ALTER_TABLE, Parent: &protocol.Reference{Engine: "duckdb", Kind: "table", Name: "users"}}}},
	}}, description
}

// A host that lists render.omissions learns each column whose declared type
// DuckDB keeps in part, once, and the SDK accepts the reply.
func TestRender_ReportsWhatDuckDBDoesNotKeep(t *testing.T) {
	c := testcheck.New(t)
	request, description := renderRequest(c, backend.Description().GetFeatures())
	result, err := backend.New().Render(context.Background(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateRenderedBatch(description, request, result), qt.IsNil)
	c.Assert(result.GetUnits(), qt.HasLen, 2)
	c.Assert(omissionLines(result), qt.DeepEquals, []string{
		"unsupported column users.email | type modifier | VARCHAR(255): length 255 | a CHECK on the column's length keeps the limit",
		"unsupported column users.flags | type modifier | BIT(8): length 8 | a CHECK on the column's bit_length keeps the length",
		"unsupported column users.seen | type modifier | TIMESTAMP(1): precision 1 (DuckDB keeps milliseconds) | ",
	})
}

// omissionLines writes each omission of a render as one line of text, so a
// failed comparison prints the lines rather than protobuf messages.
func omissionLines(result *protocol.RenderResult) []string {
	var lines []string
	for _, omission := range result.GetOmissions() {
		lines = append(lines, omission.GetReason()+" "+omission.GetKind()+" "+omission.GetName()+" | "+omission.GetProperty()+" | "+omission.GetDetail()+
			" | "+omission.GetRemedy())
	}
	return lines
}

// A host that does not list render.omissions receives the same statements
// and no omission, as a host of contract 25.32 does.
func TestRender_ReportsNothingToAHostWithoutOmissions(t *testing.T) {
	c := testcheck.New(t)
	request, description := renderRequest(c, []string{provider.TableOwnershipFeature})
	result, err := backend.New().Render(context.Background(), request)
	c.Assert(err, qt.IsNil)
	c.Assert(provider.ValidateRenderedBatch(description, request, result), qt.IsNil)
	c.Assert(result.GetUnits(), qt.HasLen, 2)
	c.Assert(omissionLines(result), qt.IsNil)
}
