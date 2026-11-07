//go:build integration

package integration_test

import (
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/scratch"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// DuckDB makes a table depend on the sequence a constant nextval or currval
// names, and on no other.
func TestScratchSequences(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		want       [][]string
	}{
		{"nextval", "nextval('app.s')", [][]string{{"app", "s"}}},
		{"currval in an expression", "coalesce(currval('\"My\".\"S\"'), 0) + nextval('t')", [][]string{{"My", "S"}, {"t"}}},
		{"a computed name", "nextval(concat('a', 'b'))", nil},
		{"no sequence", "now()", nil},
		{"text DuckDB cannot parse", "nextval(", nil},
	}
	var engine scratch.Engine
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			got, err := engine.Sequences(c.Context(), tc.expression)
			c.Assert(err, qt.IsNil)
			c.Assert(got, qt.DeepEquals, tc.want)
		})
	}
}

// A default that draws from a sequence is held as written, and the scratch
// database keeps neither the sequence nor its schema afterward.
func TestScratchTable_CreatesTheSequencesADefaultNames(t *testing.T) {
	c := testcheck.New(t)
	var engine scratch.Engine
	create := func(schema string) (string, error) {
		return `CREATE TABLE "` + schema + `"."t" (a BIGINT DEFAULT nextval('app.s'), b BIGINT DEFAULT nextval('own'))`, nil
	}
	for range 2 {
		held, err := engine.Table(c.Context(), [][]string{{"app", "s"}, {"own"}}, create)
		c.Assert(err, qt.IsNil)
		c.Assert(held.Columns, qt.HasLen, 2)
		c.Assert(*held.Columns[0].Default, qt.Equals, "nextval('app.s')")
		c.Assert(*held.Columns[1].Default, qt.Equals, "nextval('own')")
	}
}

func TestScratchTable_FailurePath(t *testing.T) {
	tests := []struct {
		name      string
		sequences [][]string
		statement string
	}{
		{"a missing sequence", nil, `(a BIGINT DEFAULT nextval('missing'))`},
		{"a sequence in another database", [][]string{{"db", "app", "s"}}, `(a BIGINT DEFAULT nextval('db.app.s'))`},
		{"an unknown type", nil, `(a NOPE)`},
	}
	var engine scratch.Engine
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			held, err := engine.Table(c.Context(), tc.sequences, func(schema string) (string, error) {
				return `CREATE TABLE "` + schema + `"."t" ` + tc.statement, nil
			})
			c.Assert(err, qt.ErrorIs, scratch.ErrRefused)
			c.Assert(held, qt.DeepEquals, catalog.Table{})
		})
	}
}

// A view's query is prepared in the spelling DuckDB stores it in, and one
// DuckDB cannot parse is refused with the view's name.
func TestPrepareCommonObjects_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	prepared, err := backend.New().PrepareCommonObjects(c.Context(), &protocol.PrepareCommonObjectsRequest{Engine: "duckdb",
		Declared: &protocol.DeclaredCommonObjects{Views: []*protocol.SQLView{{Name: "v", Body: "select a::varchar from t where a>1"}}}})
	c.Assert(err, qt.IsNil)
	c.Assert(prepared.GetPrepared().GetViews()[0].GetBody(), qt.Equals, "SELECT CAST(a AS VARCHAR) FROM t WHERE (a > 1)")
}

func TestPrepareCommonObjects_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	prepared, err := backend.New().PrepareCommonObjects(c.Context(), &protocol.PrepareCommonObjectsRequest{Engine: "duckdb",
		Declared: &protocol.DeclaredCommonObjects{Views: []*protocol.SQLView{{Name: "v", Body: "DELETE FROM t"}}}})
	c.Assert(status.Code(err), qt.Equals, codes.InvalidArgument)
	c.Assert(status.Convert(err).Message(), qt.Equals, `view "v": DuckDB cannot parse its query as one SELECT`)
	c.Assert(prepared, qt.IsNil)
}

// A declared view's query and the one DuckDB holds are one query when DuckDB
// writes them back as one text. DuckDB writes keywords, spacing and
// parentheses its own way and keeps a star as written, and it keeps the case
// of a name as written, which for a column alias is the case of the view's
// column. A query DuckDB cannot parse is no query the view holds.
func TestCompareViewQueries(t *testing.T) {
	tests := []struct {
		name, declared, held string
		want                 bool
	}{
		{"one query in two spellings", "select id from t where id>0", "SELECT id FROM t WHERE (id > 0)", true},
		{"a star", "SELECT * FROM t", "SELECT * FROM t", true},
		{"a rewritten count and LIKE", "select count(*) as n from t where name like 'a%'", `SELECT count_star() AS n FROM t WHERE ("name" ~~ 'a%')`, true},
		{"another condition", "SELECT id FROM t WHERE id > 1", "SELECT id FROM t WHERE (id > 0)", false},
		{"an alias whose case changed", `SELECT id AS "ID" FROM t`, "SELECT id AS Id FROM t", false},
		{"a declaration DuckDB cannot parse", "DELETE FROM t", "SELECT id FROM t", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			identity := &protocol.Reference{Engine: "duckdb", Kind: "view", Schema: "main", Name: "v"}
			compared, err := backend.New().CompareViewQueries(c.Context(), &protocol.CompareViewQueriesRequest{Engine: "duckdb",
				Pairs: []*protocol.ViewQueryPair{{Current: &protocol.ObservedView{Identity: identity, Body: tc.held}, Declared: tc.declared}}})
			c.Assert(err, qt.IsNil)
			c.Assert(compared.GetComplete(), qt.IsTrue)
			c.Assert(compared.GetQueries(), qt.HasLen, 1)
			c.Assert(compared.GetQueries()[0].GetIdentity().GetName(), qt.Equals, "v")
			c.Assert(compared.GetQueries()[0].GetEquivalent(), qt.Equals, tc.want)
		})
	}
}
