package scratch_test

import (
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/scratch"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

func TestSplitName(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"one part", "s", []string{"s"}},
		{"schema and name", "app.s", []string{"app", "s"}},
		{"three parts", "db.app.s", []string{"db", "app", "s"}},
		{"quoted parts", `"my app"."s.1"`, []string{"my app", "s.1"}},
		{"a doubled quote", `"a""b".s`, []string{`a"b`, "s"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			c.Assert(scratch.SplitName(tc.text), qt.DeepEquals, tc.want)
		})
	}
}
