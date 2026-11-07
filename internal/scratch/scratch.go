// Package scratch asks the embedded DuckDB how it holds what a declaration
// writes. DuckDB rewrites what it stores: TEXT and VARCHAR(10) become
// VARCHAR, a default [1, 2] becomes list_value(1, 2), a CHECK x > 0 becomes
// (x > 0), and a view's query is stored in a normalized spelling. Rather than
// restate those rules, the offline services create the declared object in a
// private in-memory database and read it back as a read of a real database
// would.
//
// The scratch database belongs to the provider process, opens with the same
// extension and file access policy as every other database, and holds
// nothing of the operator's: each call works in a schema of its own, which it
// drops before it returns.
package scratch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/duckdb/duckdb-go/v2"

	"github.com/stokaro/ptah-duckdb-plugin/internal/catalog"
	"github.com/stokaro/ptah-duckdb-plugin/internal/ddl"
	"github.com/stokaro/ptah-duckdb-plugin/internal/instances"
)

// ErrRefused wraps DuckDB's refusal of what a declaration writes. Its text is
// the provider's, never the driver's.
var ErrRefused = errors.New("DuckDB refuses the declaration")

// Engine runs scratch work. The zero value is ready to use; it opens its
// database on first use.
type Engine struct {
	mu   sync.Mutex
	db   *sql.DB
	next int
}

// database is the catalog name of an in-memory database.
const database = "memory"

// open returns the scratch database. The caller holds mu.
func (e *Engine) open() (*sql.DB, error) {
	if e.db != nil {
		return e.db, nil
	}
	dsn, err := instances.DSN(":memory:", instances.ReadWrite)
	if err != nil {
		return nil, err
	}
	connector, err := duckdb.NewConnector(dsn, nil)
	if err != nil {
		return nil, err
	}
	e.db = sql.OpenDB(connector)
	e.db.SetMaxOpenConns(1)
	return e.db, nil
}

// Table creates the table create writes in a fresh schema and returns it as
// a read reports it. create receives the schema's name and writes one
// CREATE TABLE statement. sequences are the sequences the table's defaults
// name, as Sequences lists them: DuckDB refuses a default that names a
// sequence that does not exist, so the call creates each one first, a name of
// one part in the table's schema. DuckDB's refusal is ErrRefused.
func (e *Engine) Table(ctx context.Context, sequences [][]string, create func(schema string) (string, error)) (catalog.Table, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	db, err := e.open()
	if err != nil {
		return catalog.Table{}, err
	}
	e.next++
	schema := "scratch_" + strconv.Itoa(e.next)
	if _, err := db.ExecContext(ctx, ddl.CreateSchema(schema)); err != nil {
		return catalog.Table{}, err
	}
	// cleanup removes what the call created beside its schema, after the
	// schema and the table that depends on it are gone.
	var cleanup []string
	defer func() {
		background := context.WithoutCancel(ctx)
		_, _ = db.ExecContext(background, "DROP SCHEMA "+ddl.Quote(schema)+" CASCADE")
		for _, statement := range slices.Backward(cleanup) {
			_, _ = db.ExecContext(background, statement)
		}
	}()
	for _, name := range sequences {
		qualified := ""
		switch len(name) {
		case 1:
			qualified = ddl.Qualified(schema, name[0])
		case 2:
			if _, err := db.ExecContext(ctx, ddl.CreateSchema(name[0])); err == nil {
				cleanup = append(cleanup, "DROP SCHEMA "+ddl.Quote(name[0])+" CASCADE")
			}
			qualified = ddl.Qualified(name[0], name[1])
		default:
			// A name that states its database names one the scratch
			// database is not; the creation below refuses it.
			continue
		}
		if _, err := db.ExecContext(ctx, "CREATE SEQUENCE IF NOT EXISTS "+qualified); err != nil {
			return catalog.Table{}, ErrRefused
		}
		cleanup = append(cleanup, "DROP SEQUENCE IF EXISTS "+qualified)
	}
	statement, err := create(schema)
	if err != nil {
		return catalog.Table{}, err
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		if ctx.Err() != nil {
			return catalog.Table{}, ctx.Err()
		}
		return catalog.Table{}, ErrRefused
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return catalog.Table{}, err
	}
	defer func() { _ = conn.Close() }()
	read, err := catalog.Read(ctx, conn, database, schema)
	if err != nil {
		return catalog.Table{}, err
	}
	if len(read.Tables) != 1 {
		return catalog.Table{}, fmt.Errorf("the scratch schema holds %d tables", len(read.Tables))
	}
	return read.Tables[0], nil
}

// Query returns query in the spelling DuckDB stores a view's query in: the
// statement parsed and written back. A text that is not one SELECT DuckDB
// can parse is ErrRefused.
func (e *Engine) Query(ctx context.Context, query string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	db, err := e.open()
	if err != nil {
		return "", err
	}
	var serialized string
	if err := db.QueryRowContext(ctx, "SELECT json_serialize_sql($1)::VARCHAR", query).Scan(&serialized); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrRefused
	}
	var parsed struct {
		Error      bool              `json:"error"`
		Statements []json.RawMessage `json:"statements"`
	}
	if err := json.Unmarshal([]byte(serialized), &parsed); err != nil || parsed.Error || len(parsed.Statements) != 1 {
		return "", ErrRefused
	}
	var written string
	if err := db.QueryRowContext(ctx, "SELECT json_deserialize_sql(json($1))", serialized).Scan(&written); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrRefused
	}
	return written, nil
}

// Sequences lists the sequences expression names as the constant argument of
// nextval or currval, the calls by which DuckDB makes a table depend on a
// sequence, each split into the parts DuckDB reads the name as. A text DuckDB
// cannot parse lists none; the creation that uses it refuses it.
func (e *Engine) Sequences(ctx context.Context, expression string) ([][]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	db, err := e.open()
	if err != nil {
		return nil, err
	}
	var serialized string
	if err := db.QueryRowContext(ctx, "SELECT json_serialize_sql($1)::VARCHAR", "SELECT "+expression).Scan(&serialized); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	var tree any
	if err := json.Unmarshal([]byte(serialized), &tree); err != nil {
		return nil, nil
	}
	var names [][]string
	visit(tree, func(node map[string]any) {
		if node["class"] != "FUNCTION" {
			return
		}
		function, _ := node["function_name"].(string)
		arguments, _ := node["arguments"].([]any)
		if (!strings.EqualFold(function, "nextval") && !strings.EqualFold(function, "currval")) || len(arguments) == 0 {
			return
		}
		if name, found := constantText(arguments[0]); found {
			names = append(names, SplitName(name))
		}
	})
	return names, nil
}

// visit calls fn on every object of a decoded JSON tree.
func visit(tree any, fn func(map[string]any)) {
	switch node := tree.(type) {
	case map[string]any:
		fn(node)
		for _, value := range node {
			visit(value, fn)
		}
	case []any:
		for _, value := range node {
			visit(value, fn)
		}
	}
}

// constantText is the text of a string constant argument of a serialized
// function call, which DuckDB writes on its own or under "expression".
func constantText(argument any) (string, bool) {
	node, _ := argument.(map[string]any)
	if inner, found := node["expression"].(map[string]any); found {
		node = inner
	}
	literal, _ := node["literal"].(map[string]any)
	text, isText := literal["text"].(string)
	if node["class"] != "CONSTANT" || literal["kind"] != "STRING" || !isText {
		return "", false
	}
	return text, true
}

// SplitName splits a qualified name as DuckDB reads one inside a string:
// parts separated by dots, a part in double quotes taking dots and doubled
// quotes literally.
func SplitName(name string) []string {
	var parts []string
	var part strings.Builder
	quoted := false
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '"' && quoted && i+1 < len(name) && name[i+1] == '"':
			part.WriteByte('"')
			i++
		case c == '"':
			quoted = !quoted
		case c == '.' && !quoted:
			parts = append(parts, part.String())
			part.Reset()
		default:
			part.WriteByte(c)
		}
	}
	return append(parts, part.String())
}
