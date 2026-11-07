// Package history keeps the revision records of Ptah's migrator in a DuckDB
// table, in either layout the migrator asks for: the native one, whose
// columns are the ones Ptah's native revision table has on every engine, and
// the Atlas-compatible one.
//
// No earlier engine wrote DuckDB revision tables, so the types are DuckDB's
// own: BIGINT, VARCHAR, TIMESTAMP WITH TIME ZONE, and a VARCHAR list for the
// Atlas partial hashes, where other engines keep JSON. A layout holds some
// facts more coarsely than a record carries them, and a record is written in
// that precision: DuckDB keeps a timestamp to the microsecond, and the native
// table keeps a run time to the millisecond. The native table has no
// direction column; a rollback's direction is written into the state column
// as `pending:down` or `failed:down`, as Ptah's native table does elsewhere.
//
// The provider marks each table it creates (see ownedtable) and refuses a
// table of the store's name it did not create.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"github.com/stokaro/ptah/provider-sdk/provider"
	"google.golang.org/protobuf/proto"

	"github.com/stokaro/ptah-duckdb-plugin/internal/ownedtable"
)

// The marks of the two layouts' tables.
const (
	NativeMark = "Ptah migration history, native layout; written by github.com/stokaro/ptah-duckdb-plugin"
	AtlasMark  = "Ptah migration history, Atlas layout; written by github.com/stokaro/ptah-duckdb-plugin"
)

// nativeColumns are the native table's columns as duckdb_columns reports
// them after createNative.
var nativeColumns = []ownedtable.Column{
	{Name: "version", Type: "BIGINT"},
	{Name: "description", Type: "VARCHAR"},
	{Name: "applied_at", Type: "TIMESTAMP WITH TIME ZONE"},
	{Name: "state", Type: "VARCHAR"},
	{Name: "applied", Type: "BIGINT"},
	{Name: "total", Type: "BIGINT"},
	{Name: "error", Type: "VARCHAR", Nullable: true},
	{Name: "error_stmt", Type: "VARCHAR", Nullable: true},
	{Name: "execution_time_ms", Type: "BIGINT"},
	{Name: "checksum", Type: "VARCHAR"},
}

// atlasColumns are the Atlas-compatible table's columns, measured the same
// way.
var atlasColumns = []ownedtable.Column{
	{Name: "version", Type: "VARCHAR"},
	{Name: "description", Type: "VARCHAR"},
	{Name: "type", Type: "BIGINT"},
	{Name: "applied", Type: "BIGINT"},
	{Name: "total", Type: "BIGINT"},
	{Name: "executed_at", Type: "TIMESTAMP WITH TIME ZONE"},
	{Name: "execution_time", Type: "BIGINT"},
	{Name: "error", Type: "VARCHAR", Nullable: true},
	{Name: "error_stmt", Type: "VARCHAR", Nullable: true},
	{Name: "hash", Type: "VARCHAR"},
	{Name: "partial_hashes", Type: "VARCHAR[]", Nullable: true},
	{Name: "operator_version", Type: "VARCHAR"},
}

// The CREATE TABLE statements, with the table's quoted name in place of %s.
const (
	createNative = `CREATE TABLE %s (
    version BIGINT PRIMARY KEY,
    description VARCHAR NOT NULL,
    applied_at TIMESTAMP WITH TIME ZONE NOT NULL,
    state VARCHAR NOT NULL,
    applied BIGINT NOT NULL,
    total BIGINT NOT NULL,
    error VARCHAR,
    error_stmt VARCHAR,
    execution_time_ms BIGINT NOT NULL,
    checksum VARCHAR NOT NULL
)`
	createAtlas = `CREATE TABLE %s (
    version VARCHAR PRIMARY KEY,
    description VARCHAR NOT NULL,
    type BIGINT NOT NULL,
    applied BIGINT NOT NULL,
    total BIGINT NOT NULL,
    executed_at TIMESTAMP WITH TIME ZONE NOT NULL,
    execution_time BIGINT NOT NULL,
    error VARCHAR,
    error_stmt VARCHAR,
    hash VARCHAR NOT NULL,
    partial_hashes VARCHAR[],
    operator_version VARCHAR NOT NULL
)`
)

// Store is one revision table: where it is and which layout it keeps.
type Store struct {
	Schema, Table string
	Format        protocol.HistoryScope_Format
}

// StoreOf is the store a scope names. The SDK has already refused a scope
// without a table or with an unknown format.
func StoreOf(scope *protocol.HistoryScope) Store {
	return Store{Schema: scope.GetSchema(), Table: scope.GetTable(), Format: scope.GetFormat()}
}

// Definition is the store's table and the layout it keeps.
func (s Store) Definition() ownedtable.Definition {
	if s.Format == protocol.HistoryScope_ATLAS {
		return Atlas(s.Schema, s.Table)
	}
	return Native(s.Schema, s.Table)
}

// Native is the definition of a native revision table.
func Native(schema, table string) ownedtable.Definition {
	return ownedtable.Definition{Schema: schema, Name: table, Columns: nativeColumns, PrimaryKey: []string{"version"}, Mark: NativeMark}
}

// Atlas is [Native] for the Atlas-compatible layout.
func Atlas(schema, table string) ownedtable.Definition {
	return ownedtable.Definition{Schema: schema, Name: table, Columns: atlasColumns, PrimaryKey: []string{"version"}, Mark: AtlasMark}
}

// Statements create the store; see ownedtable.Definition.Statements.
func (s Store) Statements() []string {
	if s.Format == protocol.HistoryScope_ATLAS {
		return s.Definition().Statements(createAtlas)
	}
	return s.Definition().Statements(createNative)
}

// selectColumns is the column list a read selects, in the order scan reads it.
func (s Store) selectColumns() string {
	if s.Format == protocol.HistoryScope_ATLAS {
		return "version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version"
	}
	return "version, description, applied_at, state, applied, total, error, error_stmt, execution_time_ms, checksum"
}

// ErrTooMany reports a table that holds more records than a read may return.
var ErrTooMany = errors.New("the revision table holds more records than a read returns")

// ErrRecord reports a row the layout holds that no record stands for, or a
// record the layout cannot hold.
var ErrRecord = errors.New("the revision record does not fit the revision table")

// Read returns at most limit records in version order, or an error when the
// table holds more. The caller has located the table.
func (s Store) Read(ctx context.Context, q ownedtable.Querier, limit uint32) ([]*protocol.HistoryRevision, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+s.selectColumns()+" FROM "+s.Definition().Quoted()+
		" ORDER BY version LIMIT "+strconv.FormatUint(uint64(limit)+1, 10))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var records []*protocol.HistoryRevision
	for rows.Next() {
		record, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(records) > int(limit) {
		return nil, ErrTooMany
	}
	return records, nil
}

// Mutate compares the stored record of the request's version with the
// expected one and, when they are equal, replaces or deletes it, or inserts
// the replacement when neither exists. It reports CONFLICT and changes nothing
// when they differ. The caller runs it in a transaction, which makes the
// comparison and the write one atomic step.
func (s Store) Mutate(ctx context.Context, q ownedtable.Querier, request *protocol.MutateHistoryRequest) (protocol.HistoryResult_Outcome, error) {
	version := request.GetExpected().GetVersion()
	if request.GetExpected() == nil {
		version = request.GetReplacement().GetVersion()
	}
	key, err := s.key(version)
	if err != nil {
		return protocol.HistoryResult_REFUSED, err
	}
	var write string
	var args []any
	if request.GetReplacement() != nil {
		values, err := s.values(request.GetReplacement())
		if err != nil {
			return protocol.HistoryResult_REFUSED, err
		}
		write, args = s.update(), values
		if request.GetExpected() == nil {
			write = s.insert()
		}
	} else {
		write, args = "DELETE FROM "+s.Definition().Quoted()+" WHERE version = $1", []any{key}
	}
	rows, err := q.QueryContext(ctx, "SELECT "+s.selectColumns()+" FROM "+s.Definition().Quoted()+" WHERE version = $1", key)
	if err != nil {
		return protocol.HistoryResult_UNKNOWN, err
	}
	var stored []*protocol.HistoryRevision
	for rows.Next() {
		record, err := s.scan(rows)
		if err != nil {
			_ = rows.Close()
			return protocol.HistoryResult_UNKNOWN, err
		}
		stored = append(stored, record)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return protocol.HistoryResult_UNKNOWN, err
	}
	var current *protocol.HistoryRevision
	if len(stored) == 1 {
		current = stored[0]
	}
	if len(stored) > 1 || !proto.Equal(current, request.GetExpected()) {
		return protocol.HistoryResult_CONFLICT, nil
	}
	if _, err := q.ExecContext(ctx, write, args...); err != nil {
		return protocol.HistoryResult_UNKNOWN, err
	}
	return protocol.HistoryResult_APPLIED, nil
}

// key is the version as the table's key column holds it: a signed integer in
// the native layout, the text itself in the Atlas one.
func (s Store) key(version string) (any, error) {
	if s.Format == protocol.HistoryScope_ATLAS {
		return version, nil
	}
	value, err := strconv.ParseInt(version, 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != version {
		return nil, fmt.Errorf("%w: a native revision version is a signed decimal integer", ErrRecord)
	}
	return value, nil
}

// insert and update write a record's values, in the order values returns
// them, as one row.
func (s Store) insert() string {
	if s.Format == protocol.HistoryScope_ATLAS {
		return "INSERT INTO " + s.Definition().Quoted() +
			" (version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version)" +
			" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)"
	}
	return "INSERT INTO " + s.Definition().Quoted() +
		" (version, description, applied_at, state, applied, total, error, error_stmt, execution_time_ms, checksum)" +
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)"
}

func (s Store) update() string {
	if s.Format == protocol.HistoryScope_ATLAS {
		return "UPDATE " + s.Definition().Quoted() + " SET description = $2, type = $3, applied = $4, total = $5, executed_at = $6," +
			" execution_time = $7, error = $8, error_stmt = $9, hash = $10, partial_hashes = $11, operator_version = $12 WHERE version = $1"
	}
	return "UPDATE " + s.Definition().Quoted() + " SET description = $2, applied_at = $3, state = $4, applied = $5, total = $6," +
		" error = $7, error_stmt = $8, execution_time_ms = $9, checksum = $10 WHERE version = $1"
}

// values are a record's column values in the order insert and update write
// them: in the native layout an empty error is NULL; in the Atlas layout it
// is an empty string, and no partial hashes are NULL.
func (s Store) values(record *protocol.HistoryRevision) ([]any, error) {
	if err := provider.ValidateHistoryRevision(s.Format, record); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRecord, err)
	}
	key, err := s.key(record.GetVersion())
	if err != nil {
		return nil, err
	}
	if record.GetApplied() > math.MaxInt64 || record.GetTotal() > math.MaxInt64 {
		return nil, fmt.Errorf("%w: its counts exceed the table's BIGINT columns", ErrRecord)
	}
	at := Time(record.GetAppliedAtSeconds(), record.GetAppliedAtNanos())
	if s.Format == protocol.HistoryScope_ATLAS {
		var hashes any
		if len(record.GetPartialHashes()) != 0 {
			hashes = record.GetPartialHashes()
		}
		// #nosec G115 -- the counts are at most math.MaxInt64, checked above, and an Atlas type is a uint32
		return []any{key, record.GetDescription(), int64(record.GetAtlasType()), int64(record.GetApplied()), int64(record.GetTotal()), at,
			record.GetExecutionNanoseconds(), record.GetError(), record.GetErrorStatement(), record.GetChecksum(), hashes, record.GetOperatorVersion()}, nil
	}
	if record.GetOperatorVersion() != "" || len(record.GetPartialHashes()) != 0 || record.GetAtlasType() != 0 {
		return nil, fmt.Errorf("%w: the native table has no column for an operator version, partial hashes or an Atlas type", ErrRecord)
	}
	state, err := nativeState(record)
	if err != nil {
		return nil, err
	}
	// #nosec G115 -- the counts are at most math.MaxInt64, checked above
	return []any{key, record.GetDescription(), at, state, int64(record.GetApplied()), int64(record.GetTotal()), nullable(record.GetError()),
		nullable(record.GetErrorStatement()), record.GetExecutionNanoseconds() / int64(time.Millisecond), record.GetChecksum()}, nil
}

// nativeStates are the native table's states and the record's.
var nativeStates = map[string]protocol.HistoryRevision_State{
	"applied": protocol.HistoryRevision_APPLIED, "pending": protocol.HistoryRevision_PENDING, "failed": protocol.HistoryRevision_FAILED,
}

// nativeState is the state column's value for a record: its state, followed
// by `:down` when a rollback wrote it.
func nativeState(record *protocol.HistoryRevision) (string, error) {
	var state string
	for name, value := range nativeStates {
		if value == record.GetState() {
			state = name
		}
	}
	switch {
	case state == "":
		return "", fmt.Errorf("%w: a native record states its state", ErrRecord)
	case record.GetDirection() == protocol.HistoryRevision_DOWN:
		return state + ":down", nil
	case record.GetDirection() != protocol.HistoryRevision_UP:
		return "", fmt.Errorf("%w: a native record states its direction", ErrRecord)
	}
	return state, nil
}

// scan reads one row of the store's layout as a record.
func (s Store) scan(rows *sql.Rows) (*protocol.HistoryRevision, error) {
	if s.Format == protocol.HistoryScope_ATLAS {
		return scanAtlas(rows)
	}
	var version, applied, total, execution int64
	var description, state, checksum string
	var at time.Time
	var failure, statement sql.NullString
	if err := rows.Scan(&version, &description, &at, &state, &applied, &total, &failure, &statement, &execution, &checksum); err != nil {
		return nil, err
	}
	stored, suffix, directed := strings.Cut(state, ":")
	recordState, known := nativeStates[stored]
	direction := protocol.HistoryRevision_UP
	switch {
	case !known:
		return nil, fmt.Errorf("%w: the state column holds an unknown state", ErrRecord)
	case directed && suffix == "down":
		direction = protocol.HistoryRevision_DOWN
	case directed && suffix != "up":
		return nil, fmt.Errorf("%w: the state column holds an unknown direction", ErrRecord)
	}
	if applied < 0 || total < 0 || execution < 0 || execution > math.MaxInt64/int64(time.Millisecond) {
		return nil, fmt.Errorf("%w: a stored count is negative or out of range", ErrRecord)
	}
	at = at.UTC()
	return &protocol.HistoryRevision{
		Version: strconv.FormatInt(version, 10), Description: description, State: recordState, Direction: direction,
		Applied: uint64(applied), Total: uint64(total), Error: failure.String, ErrorStatement: statement.String,
		ExecutionNanoseconds: execution * int64(time.Millisecond), Checksum: checksum,
		AppliedAtSeconds: at.Unix(), AppliedAtNanos: int32(at.Nanosecond()), // #nosec G115 -- Nanosecond is below 1e9
	}, nil
}

// scanAtlas reads one row of the Atlas layout as a record.
func scanAtlas(rows *sql.Rows) (*protocol.HistoryRevision, error) {
	var version, description, hash, operator string
	var kind, applied, total, execution int64
	var at time.Time
	var failure, statement sql.NullString
	var hashes any
	if err := rows.Scan(&version, &description, &kind, &applied, &total, &at, &execution, &failure, &statement, &hash, &hashes, &operator); err != nil {
		return nil, err
	}
	if kind < 0 || kind > math.MaxUint32 || applied < 0 || total < 0 {
		return nil, fmt.Errorf("%w: a stored count is negative or out of range", ErrRecord)
	}
	var partial []string
	if hashes != nil {
		list, isList := hashes.([]any)
		if !isList {
			return nil, fmt.Errorf("%w: its partial hashes are not a list", ErrRecord)
		}
		for _, value := range list {
			text, isText := value.(string)
			if !isText {
				return nil, fmt.Errorf("%w: its partial hashes hold a NULL", ErrRecord)
			}
			partial = append(partial, text)
		}
	}
	at = at.UTC()
	return &protocol.HistoryRevision{
		Version: version, Description: description, AtlasType: uint32(kind), Applied: uint64(applied), Total: uint64(total),
		ExecutionNanoseconds: execution, Error: failure.String, ErrorStatement: statement.String, Checksum: hash, PartialHashes: partial,
		OperatorVersion: operator, AppliedAtSeconds: at.Unix(), AppliedAtNanos: int32(at.Nanosecond()), // #nosec G115 -- Nanosecond is below 1e9
	}, nil
}

// Time is a record's time as the table keeps it: in UTC, to the microsecond.
func Time(seconds int64, nanos int32) time.Time {
	return time.Unix(seconds, int64(nanos)).UTC().Truncate(time.Microsecond)
}

// nullable is text as a nullable column takes it: NULL for none.
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
