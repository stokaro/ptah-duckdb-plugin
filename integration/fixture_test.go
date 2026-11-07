//go:build integration

package integration_test

import (
	"database/sql"
	"path/filepath"

	"github.com/duckdb/duckdb-go/v2"
	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
)

// richSchema holds one of each object a read describes: native and nested
// column types, defaults, a generated column, a primary key, a single-column
// UNIQUE, CHECKs, a foreign key, a column index, an expression index, a
// view, a sequence, and a comment on each kind that takes one.
var richSchema = []string{
	"CREATE SEQUENCE seq_id START 10 INCREMENT 5 MINVALUE 1 MAXVALUE 1000 CYCLE",
	"CREATE TABLE parent(id INTEGER PRIMARY KEY, code VARCHAR UNIQUE NOT NULL, amount DECIMAL(12,3) DEFAULT 1.5, CHECK (amount > 0))",
	`CREATE TABLE child(
		id BIGINT DEFAULT nextval('seq_id'),
		parent_id INTEGER REFERENCES parent(id),
		tags VARCHAR[],
		pt STRUCT(x DOUBLE, "y y" DOUBLE),
		attrs MAP(VARCHAR, INTEGER),
		u UNION(num INTEGER, str VARCHAR),
		vec FLOAT[3],
		e ENUM('a', 'it''s'),
		ts TIMESTAMPTZ DEFAULT current_timestamp,
		j JSON,
		doubled INTEGER GENERATED ALWAYS AS (parent_id * 2) VIRTUAL,
		PRIMARY KEY (id),
		CHECK (id > 0))`,
	"CREATE INDEX child_parent_ts ON child(parent_id, ts)",
	"CREATE UNIQUE INDEX child_expr ON child((id + 1))",
	"CREATE VIEW big_parents AS SELECT id, code FROM parent WHERE amount > 1",
	"COMMENT ON TABLE parent IS 'parents'",
	"COMMENT ON COLUMN parent.code IS 'the code'",
	"COMMENT ON INDEX child_parent_ts IS 'lookup'",
	"COMMENT ON VIEW big_parents IS 'big ones'",
	"COMMENT ON SEQUENCE seq_id IS 'ids'",
}

// seedFile creates the database file name in a temporary directory, runs
// statements on it outside the provider and closes it. It returns the path.
func seedFile(c *qt.C, name string, statements []string) string {
	c.Helper()
	path := filepath.Join(c.TempDir(), name)
	connector, err := duckdb.NewConnector(path, nil)
	c.Assert(err, qt.IsNil)
	db := sql.OpenDB(connector)
	for _, statement := range statements {
		_, err := db.ExecContext(c.Context(), statement)
		c.Assert(err, qt.IsNil, qt.Commentf("statement: %s", statement))
	}
	c.Assert(db.Close(), qt.IsNil)
	return path
}

// seedMemory opens a new in-memory database in a registry of its own, runs
// statements on it and returns the handle.
func seedMemory(c *qt.C, statements []string) *instances.Handle {
	c.Helper()
	var registry instances.Registry
	handle := openMemory(c, &registry, "")
	run(c, handle, statements...)
	return handle
}
