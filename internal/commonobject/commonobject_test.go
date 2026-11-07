package commonobject_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"

	"github.com/stokaro/ptah-duckdb-plugin/internal/commonobject"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestRefusals(t *testing.T) {
	c := testcheck.New(t)
	declared := &protocol.DeclaredCommonObjects{
		Views: []*protocol.SQLView{
			{Name: "fine", Body: "SELECT 1"},
			{Name: "m", Body: "SELECT 1", Materialized: true},
			{Name: "w", Body: "SELECT 1", WithCheck: true},
			{Schema: "app", Name: "elsewhere", Body: "SELECT 1"},
		},
		Sequences: []*protocol.SQLSequence{
			{Name: "s"},
			{Name: "small", AsType: "INTEGER"},
			{Name: "cached", Cache: new(int64(20))},
			{Name: "owned", OwnedBy: "t.id"},
			{Name: "still", Increment: new(int64(0))},
		},
		Roles:  []*protocol.SQLRole{{Name: "reader"}},
		Grants: []*protocol.SQLGrant{{Role: "reader", Privileges: []string{"SELECT"}}},
	}
	c.Assert(commonobject.Refusals(declared, nil), qt.DeepEquals, []commonobject.Refusal{
		{Kind: "view", Object: "m", Reason: `view "m" is materialized; DuckDB has no materialized views`},
		{Kind: "view", Object: "w", Reason: `view "w" has WITH CHECK OPTION, which DuckDB does not have`},
		{Kind: "view", Object: "elsewhere",
			Reason: `view "elsewhere" is in schema "app"; the host does not read schema.common-namespaces, so the provider reads and creates objects in main only`},
		{Kind: "sequence", Object: "small", Reason: `sequence "small" is INTEGER; a DuckDB sequence counts in BIGINT only`},
		{Kind: "sequence", Object: "cached", Reason: `sequence "cached" has a cache; a DuckDB sequence has none`},
		{Kind: "sequence", Object: "owned", Reason: `sequence "owned" is owned by a column; a DuckDB sequence has no owner`},
		{Kind: "sequence", Object: "still", Reason: `sequence "still" has an increment of zero`},
		{Kind: "role", Object: "reader", Reason: `role "reader": DuckDB has no roles`},
		{Kind: "grant", Object: "reader", Reason: "DuckDB has no grants"},
	})
}

// A host that reads namespaces takes objects in any schema.
func TestRefusals_NamespacesTakeEverySchema(t *testing.T) {
	c := testcheck.New(t)
	declared := &protocol.DeclaredCommonObjects{Views: []*protocol.SQLView{{Schema: "app", Name: "v", Body: "SELECT 1"}},
		Sequences: []*protocol.SQLSequence{{Schema: "app", Name: "s"}}}
	c.Assert(commonobject.Refusals(declared, []string{provider.CommonNamespacesFeature}), qt.IsNil)
}

func TestPrepare_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	declared := &protocol.DeclaredCommonObjects{
		Views:     []*protocol.SQLView{{Name: "v", Body: "select 1"}},
		Sequences: []*protocol.SQLSequence{{Name: "up"}, {Name: "down", Increment: new(int64(-2))}},
	}
	prepared, err := commonobject.Prepare(c.Context(), declared, func(_ context.Context, query string) (string, error) { return strings.ToUpper(query), nil })
	c.Assert(err, qt.IsNil)
	c.Assert(prepared.GetViews()[0].GetBody(), qt.Equals, "SELECT 1")
	c.Assert(declared.GetViews()[0].GetBody(), qt.Equals, "select 1")
	c.Assert(prepared.GetSequences(), qt.HasLen, 2)
	up, down := prepared.GetSequences()[0], prepared.GetSequences()[1]
	c.Assert([]int64{up.GetIncrement(), up.GetMinValue(), up.GetMaxValue(), up.GetStart()}, qt.DeepEquals, []int64{1, 1, math.MaxInt64, 1})
	c.Assert([]int64{down.GetIncrement(), down.GetMinValue(), down.GetMaxValue(), down.GetStart()}, qt.DeepEquals, []int64{-2, math.MinInt64, -1, -1})
	c.Assert(up.GetAsType(), qt.Equals, "BIGINT")
}

func TestPrepare_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	declared := &protocol.DeclaredCommonObjects{Views: []*protocol.SQLView{{Name: "v", Body: "not sql"}}}
	refused := errors.New("DuckDB refuses the declaration")
	prepared, err := commonobject.Prepare(c.Context(), declared, func(context.Context, string) (string, error) { return "", refused })
	c.Assert(err, qt.ErrorIs, refused)
	c.Assert(err, qt.ErrorMatches, `view "v": DuckDB refuses the declaration`)
	c.Assert(prepared, qt.IsNil)
}

func TestRender_HappyPath(t *testing.T) {
	tests := []struct {
		name   string
		change *protocol.CommonObjectChange
		want   string
	}{
		{"create a view", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateView{CreateView: &protocol.ViewCreation{
			View: &protocol.SQLView{Name: "v", Body: "SELECT 1"}}}}, `CREATE VIEW "main"."v" AS SELECT 1`},
		{"replace a view", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateView{CreateView: &protocol.ViewCreation{
			View: &protocol.SQLView{Schema: "app", Name: "v", Body: "SELECT 1"}, Replace: true}}}, `CREATE OR REPLACE VIEW "app"."v" AS SELECT 1`},
		{"drop a view", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_DropView{DropView: &protocol.Reference{Name: "v"}}},
			`DROP VIEW "main"."v"`},
		{"create a sequence", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateSequence{CreateSequence: &protocol.SQLSequence{
			Name: "s", Start: new(int64(100))}}}, `CREATE SEQUENCE "main"."s" START WITH 100`},
		{"drop a sequence", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_DropSequence{DropSequence: &protocol.Reference{Name: "s"}}},
			`DROP SEQUENCE "main"."s"`},
		{"comment on a table", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_SetComment{SetComment: &protocol.ObjectComment{
			Object: &protocol.Reference{Kind: "table", Name: "t"}, Text: "it's"}}}, `COMMENT ON TABLE "main"."t" IS 'it''s'`},
		{"comment on a column", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_SetComment{SetComment: &protocol.ObjectComment{
			Object: &protocol.Reference{Kind: "column", Schema: "app", Path: []string{"t"}, Name: "c"}, Text: "x"}}}, `COMMENT ON COLUMN "app"."t"."c" IS 'x'`},
		{"remove a comment", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_SetComment{SetComment: &protocol.ObjectComment{
			Object: &protocol.Reference{Kind: "index", Name: "i"}}}}, `COMMENT ON INDEX "main"."i" IS NULL`},
		{"create a schema", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateSchema{CreateSchema: &protocol.NamespaceCreation{Name: "app"}}},
			`CREATE SCHEMA "app"`},
		{"drop a schema", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_DropSchema{DropSchema: &protocol.Reference{Name: "app"}}},
			`DROP SCHEMA "app"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := commonobject.Render(tc.change, "main")
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.Equals, tc.want)
		})
	}
}

func TestRender_FailurePath(t *testing.T) {
	tests := []struct {
		name   string
		change *protocol.CommonObjectChange
		want   string
	}{
		{"a materialized view", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateView{CreateView: &protocol.ViewCreation{
			View: &protocol.SQLView{Name: "v", Body: "SELECT 1", Materialized: true}}}}, `view "v" is materialized; DuckDB has no materialized views`},
		{"a changed sequence", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_AlterSequence{AlterSequence: &protocol.SequenceAlteration{
			Sequence: &protocol.Reference{Name: "s"}}}},
			`sequence "s": DuckDB alters no sequence in place, and replacing it would restart its counter; drop it and create it explicitly`},
		{"a sequence of another type", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateSequence{CreateSequence: &protocol.SQLSequence{
			Name: "s", AsType: "SMALLINT"}}}, `sequence "s" is SMALLINT; a DuckDB sequence counts in BIGINT only`},
		{"a comment on a schema", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_CreateSchema{CreateSchema: &protocol.NamespaceCreation{
			Name: "app", Comment: "x"}}}, `schema "app": DuckDB takes no comment on a schema`},
		{"a comment on a role", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_SetComment{SetComment: &protocol.ObjectComment{
			Object: &protocol.Reference{Kind: "role", Name: "r"}, Text: "x"}}}, "DuckDB takes no comment on a role"},
		{"a column comment without its table", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_SetComment{SetComment: &protocol.ObjectComment{
			Object: &protocol.Reference{Kind: "column", Name: "c"}, Text: "x"}}}, "a column's comment names the column's table"},
		{"a grant", &protocol.CommonObjectChange{Operation: &protocol.CommonObjectChange_Grant{Grant: &protocol.SQLGrant{Role: "r"}}},
			"DuckDB has no roles, grants or memberships"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := commonobject.Render(tc.change, "main")
			c.Assert(err, qt.ErrorMatches, tc.want)
			c.Assert(got, qt.Equals, "")
		})
	}
}
