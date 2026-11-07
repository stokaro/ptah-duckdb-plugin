---
title: Migrations on DuckDB
description: How the DuckDB provider runs a migration statement by statement, where it keeps the migrator's records, how it holds the migration lock, and how it provisions, cleans and guards dev databases.
type: concept
audience:
  - database-engineer
  - ci-operator
readerQuestion: "How does a migration run on DuckDB, and what do a failure, the lock and a dev database look like there?"
goal: "Predict how a DuckDB migration runs and what it leaves after a failure, and resume, lint or rehearse one."
sourceOfTruth:
  - internal/migrationunits
  - internal/history
  - internal/metadata
  - internal/ownedtable
  - internal/devclean
  - internal/backend/execute.go
  - internal/backend/locks.go
  - internal/backend/devdb.go
  - stokaro/ptah#5667
generated: false
searchAliases:
  - "DuckDB migration history"
  - "DuckDB dev database"
  - "schema_migrations DuckDB"
overlaps: []
disposition: keep
sidebar:
  order: 3
---

# Migrations on DuckDB

`ptah migrations up`, `down`, `status`, `tag` and `log` run through the
provider. Ptah keeps the migration policy: which migrations run, in which
order, with which checks. The provider runs each statement and keeps the
records Ptah reads and writes.

## How a migration runs

The provider splits a migration body into its statements with DuckDB's own
lexing. DuckDB runs every statement in a transaction, schema statements
included, and a rollback undoes them, so each statement can run together with
the checkpoint that records it. What a failed migration leaves depends on the
transaction mode the command selects:

| `--tx-mode` | What runs in one transaction | What a failed migration leaves |
| --- | --- | --- |
| `file`, the default | Each migration file | Nothing of the failed migration. The migrations before it stay applied. |
| `all` | Every pending migration of the run | Nothing of the run. |
| `none` | Each statement, with its checkpoint | The statements before the failed one, committed. A rerun with `--allow-dirty` resumes after the last one that committed. |

The revision table records the failure in each mode: the migration's state is
`failed`, with how many of its statements applied and the statement that
failed. `ptah migrations status` shows it.

A body that controls a transaction or changes a setting is refused before
anything runs: `BEGIN`, `START TRANSACTION`, `COMMIT`, `END`, `ROLLBACK`,
`ABORT`, `SET`, `RESET`, `PRAGMA` and `USE`. Ptah decides where each
transaction begins and ends, and a setting would not reach the statements
after it, which may run on another connection.

A failure names the unit and DuckDB's class of error without quoting DuckDB's
message, which can quote a value: `DuckDB refused unit 1 of 1 with a catalog
error: an object the unit names is missing, or exists already`.

## Where the records live

The provider keeps the migrator's records in tables of the database, under
the names and in the schema the command selects:

| Table | Default name | Columns |
| --- | --- | --- |
| Native revision table | `schema_migrations` | `version BIGINT` key, `description`, `applied_at TIMESTAMPTZ`, `state` (`applied`, `pending` or `failed`, with `:down` for a rollback), `applied`, `total`, `error`, `error_stmt`, `execution_time_ms`, `checksum` |
| Atlas revision table, with `--revision-format atlas` | `atlas_schema_revisions` | `version VARCHAR` key, `description`, `type`, `applied`, `total`, `executed_at TIMESTAMPTZ`, `execution_time` in nanoseconds, `error`, `error_stmt`, `hash`, `partial_hashes VARCHAR[]`, `operator_version` |
| Run log | `schema_migrations_log` | `run_id` and `seq` as the key, `operation`, `version`, `state`, `actor`, `actor_source`, `checksum`, `logged_at TIMESTAMPTZ`, `error` |
| Tags | `ptah_migration_tags` | `tag` key, `version`, `recorded_at TIMESTAMPTZ` |

A timestamp is kept to the microsecond and the native run time to the
millisecond.

The provider creates each table in one transaction, with a comment that marks
it as the provider's. It reads and writes a table of a store's name only when
the table carries that mark and exactly the store's columns and key, with no
other constraint, index or generated column that could change or refuse a
record. A familiar name proves nothing: a table of that name without the mark
belongs to the user, and the provider refuses it, naming how it differs, rather
than adopt it.

A read leaves the marked tables out of the schema it reports and names them as
reserved, whatever their names, so `ptah schema apply` and `ptah migrations
generate` never plan to drop the record of the migrations a database ran.

## The migration lock

Ptah takes a lock before it runs migrations or applies a plan. The provider
keeps it in its own lock table, keyed by the database and the lock's name,
which the sessions of one command share. Another process cannot take it at
all: DuckDB lets no second process open a file one process writes, and the
open fails with the holder's process ID. Closing a session releases its locks.

## Dev databases

[Using the DuckDB provider](usage.md#dev-databases) lists the `--dev-url`
forms: `docker://duckdb`, a file, or `duckdb://:memory:`.

Ptah checks that a dev database is empty before it uses it, and empties it
afterwards, through the provider's list of what a cleanup removes. The cleanup
removes every schema but `main` with what it holds, then the views, macros,
tables, sequences and types of `main`: the views first, since DuckDB records
no dependency of a view on a table and would leave one behind; each table
after the tables whose foreign keys reference it; and the sequences after the
tables whose defaults draw from them. It runs in one transaction, so it
removes everything or nothing, and a read-only session removes nothing and
names every object that stopped it.

Before a replay, Ptah asks the provider whether each statement stays inside
the dev database, where a cleanup removes what it created. The provider
accepts schema statements on schemas, tables, views, indexes, sequences,
macros and types, comments, and reading and writing rows. It refuses, naming
the operation:

| Statement | Why |
| --- | --- |
| `CREATE TEMP TABLE` and any temporary object, a name in the `temp` or `system` catalog | It belongs to the session or the system, which a cleanup of the database does not reach. |
| `SET`, `RESET`, `PRAGMA`, `USE` | It changes a setting the statements after it read. |
| `BEGIN`, `COMMIT`, `ROLLBACK` and the rest of transaction control, `PREPARE`, `EXECUTE` | It changes the state of the session the replay keeps using. |
| `ATTACH`, `DETACH` | It reaches another database. |
| `INSTALL`, `LOAD`, `UPDATE EXTENSIONS` | It installs or loads an extension. |
| `CREATE SECRET` | A secret outlives the database. |
| `COPY`, `EXPORT`, `IMPORT` | It reads or writes a file outside the database. |
| `CALL` and any other statement | The provider cannot say what it changes. |

## Lint

`ptah migrations lint` splits each file with the provider's SQL analysis and
runs Ptah's rules over the statements. The provider publishes no lint rules of
its own.

## Generating migrations

`ptah migrations generate` writes an up file and a down file. The up file
carries the provider's notes, such as the note for a declared `VARCHAR(255)`,
as SQL comments beside the statements.

The down file undoes the up file, and DuckDB's rules hold for it too. Adding a
column to a table that keeps an index generates a down file that drops the
column, which DuckDB refuses while the index exists, so the provider refuses
the generation by name. Drop the index in the same change, or write the
migration by hand with `ptah migrations create`: in the transaction that drops
an index, DuckDB drops a column when the index covers neither that column nor
one after it.
