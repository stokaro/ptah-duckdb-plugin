// Package metadata keeps the migration log and the migration tags of Ptah's
// migrator in DuckDB tables, with the columns Ptah's log and tag tables have
// on every engine, in DuckDB's own types. The provider marks each table it
// creates (see ownedtable) and refuses a table of a store's name it did not
// create.
package metadata

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"google.golang.org/protobuf/proto"
	protocol "ptah.run/provider-sdk/protocol/experimental"

	"github.com/stokaro/ptah-duckdb-plugin/internal/ownedtable"
)

// The marks of the two tables.
const (
	LogMark  = "Ptah migration log; written by github.com/stokaro/ptah-duckdb-plugin"
	TagsMark = "Ptah migration tags; written by github.com/stokaro/ptah-duckdb-plugin"
)

// logColumns are the log's columns as duckdb_columns reports them after
// createLog.
var logColumns = []ownedtable.Column{
	{Name: "run_id", Type: "VARCHAR"},
	{Name: "seq", Type: "BIGINT"},
	{Name: "operation", Type: "VARCHAR"},
	{Name: "version", Type: "BIGINT"},
	{Name: "state", Type: "VARCHAR"},
	{Name: "actor", Type: "VARCHAR", Nullable: true},
	{Name: "actor_source", Type: "VARCHAR"},
	{Name: "checksum", Type: "VARCHAR", Nullable: true},
	{Name: "logged_at", Type: "TIMESTAMP WITH TIME ZONE"},
	{Name: "error", Type: "VARCHAR", Nullable: true},
}

// tagColumns are the tag table's columns, measured the same way.
var tagColumns = []ownedtable.Column{
	{Name: "tag", Type: "VARCHAR"},
	{Name: "version", Type: "BIGINT"},
	{Name: "recorded_at", Type: "TIMESTAMP WITH TIME ZONE"},
}

// The CREATE TABLE statements, with the table's quoted name in place of %s.
const (
	createLog = `CREATE TABLE %s (
    run_id VARCHAR NOT NULL,
    seq BIGINT NOT NULL,
    operation VARCHAR NOT NULL,
    version BIGINT NOT NULL,
    state VARCHAR NOT NULL,
    actor VARCHAR,
    actor_source VARCHAR NOT NULL,
    checksum VARCHAR,
    logged_at TIMESTAMP WITH TIME ZONE NOT NULL,
    error VARCHAR,
    PRIMARY KEY (run_id, seq)
)`
	createTags = `CREATE TABLE %s (
    tag VARCHAR PRIMARY KEY,
    version BIGINT NOT NULL,
    recorded_at TIMESTAMP WITH TIME ZONE NOT NULL
)`
)

// Store is the log or the tag table a scope names.
type Store struct {
	Schema, Table string
	Kind          protocol.MetadataScope_Kind
}

// StoreOf is the store a scope names. The SDK has already refused a scope
// without a table or with an unknown kind.
func StoreOf(scope *protocol.MetadataScope) Store {
	return Store{Schema: scope.GetSchema(), Table: scope.GetTable(), Kind: scope.GetKind()}
}

// Definition is the store's table and the layout it keeps.
func (s Store) Definition() ownedtable.Definition {
	if s.Kind == protocol.MetadataScope_LOG {
		return Log(s.Schema, s.Table)
	}
	return Tags(s.Schema, s.Table)
}

// Log is the definition of a log table.
func Log(schema, table string) ownedtable.Definition {
	return ownedtable.Definition{Schema: schema, Name: table, Columns: logColumns, PrimaryKey: []string{"run_id", "seq"}, Mark: LogMark}
}

// Tags is [Log] for a tag table.
func Tags(schema, table string) ownedtable.Definition {
	return ownedtable.Definition{Schema: schema, Name: table, Columns: tagColumns, PrimaryKey: []string{"tag"}, Mark: TagsMark}
}

// Statements create the store; see ownedtable.Definition.Statements.
func (s Store) Statements() []string {
	if s.Kind == protocol.MetadataScope_LOG {
		return s.Definition().Statements(createLog)
	}
	return s.Definition().Statements(createTags)
}

// ErrDuplicate reports a log record whose identity the log holds already. The
// insert that found it wrote nothing.
var ErrDuplicate = errors.New("the log holds a record with this run and sequence")

// Append writes one log record. A record whose run and sequence the log holds
// already fails with [ErrDuplicate] and writes nothing.
func (s Store) Append(ctx context.Context, q ownedtable.Querier, record *protocol.MigrationLogRecord) error {
	_, err := q.ExecContext(ctx, "INSERT INTO "+s.Definition().Quoted()+
		" (run_id, seq, operation, version, state, actor, actor_source, checksum, logged_at, error)"+
		" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)",
		record.GetRunId(), record.GetSequence(), record.GetOperation(), record.GetVersion(), record.GetState(), nullable(record.GetActor()),
		record.GetActorSource(), nullable(record.GetChecksum()), Time(record.GetAtSeconds(), record.GetAtNanos()), nullable(record.GetError()))
	if refused, found := errors.AsType[*duckdb.Error](err); found && refused.Type == duckdb.ErrorTypeConstraint {
		return ErrDuplicate
	}
	return err
}

// PutTag records a tag, replacing the record of a tag of the same name. The
// caller runs it in a transaction, which makes the read and the write one
// step.
func (s Store) PutTag(ctx context.Context, q ownedtable.Querier, record *protocol.MigrationTagRecord) error {
	found, err := s.Lookup(ctx, q, record.GetName())
	if err != nil {
		return err
	}
	statement := "INSERT INTO " + s.Definition().Quoted() + " (tag, version, recorded_at) VALUES ($1, $2, $3)"
	if found != nil {
		statement = "UPDATE " + s.Definition().Quoted() + " SET version = $2, recorded_at = $3 WHERE tag = $1"
	}
	_, err = q.ExecContext(ctx, statement, record.GetName(), record.GetVersion(), Time(record.GetRecordedAtSeconds(), record.GetRecordedAtNanos()))
	return err
}

// DeleteTag removes a tag and reports whether the table held it.
func (s Store) DeleteTag(ctx context.Context, q ownedtable.Querier, name string) (bool, error) {
	result, err := q.ExecContext(ctx, "DELETE FROM "+s.Definition().Quoted()+" WHERE tag = $1", name)
	if err != nil {
		return false, err
	}
	removed, err := result.RowsAffected()
	return removed == 1, err
}

// Lookup returns the record of a tag, or nil when the table holds none.
func (s Store) Lookup(ctx context.Context, q ownedtable.Querier, name string) (*protocol.MigrationTagRecord, error) {
	var record protocol.MigrationTagRecord
	var at time.Time
	err := q.QueryRowContext(ctx, "SELECT tag, version, recorded_at FROM "+s.Definition().Quoted()+" WHERE tag = $1", name).
		Scan(&record.Name, &record.Version, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record.RecordedAtSeconds, record.RecordedAtNanos = unix(at)
	return &record, nil
}

// ErrTooLarge reports a store larger than a snapshot holds.
var ErrTooLarge = errors.New("the store holds more than a snapshot reads")

// Snapshot reads every record of the store in key order: log records by run
// and sequence, tags by name. It fails with [ErrTooLarge] when the records
// exceed limit bytes, rather than returning part of the store.
func (s Store) Snapshot(ctx context.Context, q ownedtable.Querier, limit int) ([]proto.Message, error) {
	query := "SELECT tag, version, recorded_at FROM " + s.Definition().Quoted() + " ORDER BY tag"
	scan := scanTag
	if s.Kind == protocol.MetadataScope_LOG {
		query = "SELECT run_id, seq, operation, version, state, actor, actor_source, checksum, logged_at, error FROM " +
			s.Definition().Quoted() + " ORDER BY run_id, seq"
		scan = scanLog
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var records []proto.Message
	size := 0
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return nil, err
		}
		size += proto.Size(record)
		if size > limit {
			return nil, ErrTooLarge
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scanLog(rows *sql.Rows) (proto.Message, error) {
	var record protocol.MigrationLogRecord
	var actor, checksum, failure sql.NullString
	var at time.Time
	if err := rows.Scan(&record.RunId, &record.Sequence, &record.Operation, &record.Version, &record.State, &actor, &record.ActorSource,
		&checksum, &at, &failure); err != nil {
		return nil, err
	}
	record.Actor, record.Checksum, record.Error = actor.String, checksum.String, failure.String
	record.AtSeconds, record.AtNanos = unix(at)
	return &record, nil
}

func scanTag(rows *sql.Rows) (proto.Message, error) {
	var record protocol.MigrationTagRecord
	var at time.Time
	if err := rows.Scan(&record.Name, &record.Version, &at); err != nil {
		return nil, err
	}
	record.RecordedAtSeconds, record.RecordedAtNanos = unix(at)
	return &record, nil
}

// Time is a record's time as the table keeps it: in UTC, to the microsecond.
func Time(seconds int64, nanos int32) time.Time {
	return time.Unix(seconds, int64(nanos)).UTC().Truncate(time.Microsecond)
}

// unix splits a stored time as a record carries it.
func unix(at time.Time) (int64, int32) {
	at = at.UTC()
	return at.Unix(), int32(at.Nanosecond()) // #nosec G115 -- Nanosecond is below 1e9
}

// nullable is text as a nullable column takes it: NULL for none.
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
