# Ptah DuckDB provider

This repository holds the executable DuckDB provider for
[Ptah #5667](https://github.com/stokaro/ptah/issues/5667). Ptah starts it as a
subprocess and talks to it over the provider protocol of
`github.com/stokaro/ptah/provider-sdk`.

The provider opens DuckDB database files and in-memory databases, reads their
schemas completely or refuses the read by name, plans, renders and runs
schema changes, and keeps the records of Ptah's migrator: `ptah schema
apply`, `ptah migrations generate`, `up`, `down`, `status`, `tag`, `log` and
`lint` work through it, with a dev database where a command takes one. Each
service is advertised in the description only when it exists.

## The embedded DuckDB

DuckDB is embedded through the official Go driver,
[`github.com/duckdb/duckdb-go/v2`](https://github.com/duckdb/duckdb-go). The
driver links a prebuilt DuckDB library statically through cgo.

| | Version |
| --- | --- |
| Driver (`github.com/duckdb/duckdb-go/v2`) | `v2.20000.0` |
| DuckDB library it links | `v2.0.0` |

`internal/duckdbversion` records both. A unit test compares the driver version
with the build, and an integration test compares the library version with
what the linked DuckDB reports, so a dependency update that forgets the record
fails. Each session reports the library version as its server version.

Only this module and its release job need cgo and a C toolchain. Ptah itself
stays free of cgo and of any DuckDB dependency.

DuckDB `v2.0.0` writes files in its own storage format, which older DuckDB
releases cannot read. A file written by an older DuckDB keeps its format when
this provider writes to it: a file created with
`storage_compatibility_version=v1.0.0` still reports `v1.0.0+` after an insert
and a checkpoint.

## Addresses

A DuckDB address names a database file or an in-memory database:

```text
duckdb://<path>[?access_mode=read_only|read_write]
duckdb://:memory:[<name>]
```

- The path is everything between `duckdb://` and the first `?`, with percent
  escapes decoded. A space may be written as `%20` or as itself; a `?` or `#`
  in a file name is written `%3F` or `%23`.
- `duckdb:///var/lib/app.duckdb` is an absolute path. On Windows,
  `duckdb://C:/data/app.duckdb`, `duckdb:///C:/data/app.duckdb` and
  `duckdb://C:\data\app.duckdb` name the same absolute file, and a UNC path is
  written `duckdb://\\server\share\app.duckdb`.
- Any other path is relative: `duckdb://app.duckdb`, `duckdb://data/app.duckdb`.
  It resolves against the provider's working directory, which is the
  directory Ptah was started in, never the directory of the provider's
  executable: contract 25.30 states that the host starts the provider in its
  own working directory.
- `access_mode` is the only option. `read_write` is the default, and opening a
  file that does not exist creates it when its directory exists.
  `read_only` opens an existing file and creates nothing.

The provider refuses, by name and before touching any file: another scheme, an
address that names no database, a fragment, a malformed percent escape, a
control character, an unknown or repeated option, a read-only in-memory
database, a Windows path relative to a drive's current directory (`C:app`), a
drive path on a system other than Windows, a path without a drive on Windows,
`duckdb:///:memory:` (which would name a file called `:memory:` in the root
directory), and a setting the provider fixes for every database, such as
`autoload_known_extensions`. The DuckDB driver cannot open a file whose
resolved path contains `?`, `#` or a `%` that does not start a two-digit
escape: the driver reads the first two as URL syntax, and a `#` would silently
drop the provider's settings, so such a path is refused as well.

No refusal quotes the address or any part of it, because an address may carry
a credential. Each reason is a sentence the provider wrote; it never passes on
the driver's text, which quotes the path.

## Sessions

The host opens several sessions on one address during one command. The
provider keeps one DuckDB instance per database file and gives every session a
connection of its own to it, so the sessions see each other's changes. A file
reached through a symbolic link, a hard link or a non-canonical path is the
same file. When the last session of a file closes, the instance closes and
DuckDB releases the file.

An in-memory database lives as long as the provider process: every session on
`duckdb://:memory:` reaches the same database, also after the earlier sessions
closed. `duckdb://:memory:<name>` is another in-memory database. The provider
process ends with the Ptah command that started it, and its in-memory
databases end with it; a new command starts with empty ones. A session handle
of an ended process is refused by the next one.

### Concurrency

DuckDB allows one process to write a database file, or several to read it,
never both ([concurrency](https://duckdb.org/docs/current/connect/concurrency)).
The provider does not lift that rule, and reports a conflict clearly:

- A second provider process, or any other program, that holds the file keeps
  this provider out. The refusal is `Unavailable` and names the holder's
  process ID: `another process (PID 4242) has the database file open for
  writing; DuckDB lets no other process open it until that one closes it`, or,
  when the holder only reads, that more processes may read the file with
  `access_mode=read_only`. On Windows DuckDB names the holder but not its
  access mode, and so does the reason.
- Inside one provider process, every session of a file shares one instance,
  and a session that asks for the other access mode is refused by name
  (`FailedPrecondition`): DuckDB opens one file in one access mode per process.

The lock Ptah takes before it applies a plan is an entry of the provider's own
lock table, keyed by the database and the lock's name. That covers every
writer there is: the sessions of one process share the table, and DuckDB lets
no other process open a file one process writes. Closing a session releases
its locks.

### Files that are not DuckDB databases

Before DuckDB opens a file, the provider reads its header and refuses an empty
file, a SQLite database and any other file that is not a DuckDB database. It
also refuses a directory, a file inside something that is not a directory, a
file it may not read, and, read-write, a file it may not write. DuckDB itself
answers a SQLite file by loading its `sqlite` extension, which the provider
never lets it do. A corrupt file is refused with its checksum mismatch named.

## Inspection

`Inspect` reads the schemas of the session's database that a read selects,
every schema when it selects none, from DuckDB's own catalog functions (`duckdb_tables()`, `duckdb_columns()`,
`duckdb_constraints()`, `duckdb_indexes()`, `duckdb_views()`,
`duckdb_sequences()`, `duckdb_types()` and `duckdb_functions()`). A read is
complete or it fails with `FailedPrecondition` and a reason that names what
it does not describe: a read that left an object out would make Ptah plan to
create it.

What a read carries:

| Object | Read as |
| --- | --- |
| Schema | A namespace on the common-object wire (`schema.common-namespaces`), `main` included. A selector names a schema without regard to ASCII case; one that names no schema selects nothing. |
| Table | Name and comment; temporary tables belong to a connection and are left out. |
| Column | DuckDB's own type spelling, nullability, default expression as DuckDB stores it (`nextval('seq_id')`, `list_value(1, 2)`), the expression of a generated column (DuckDB's are all `VIRTUAL`), comment. A `DECIMAL` also states its precision and scale. |
| Primary key | DuckDB's name for it and its columns in key order. |
| `UNIQUE` on one column | The column's `unique` flag. |
| `CHECK` | DuckDB's name for it, the columns it reads and the expression as DuckDB keeps it, `(id > 0)`. |
| `FOREIGN KEY` | DuckDB's name, the columns, the referenced table and columns, and `NO ACTION` as both rules. |
| Index | Name, uniqueness, comment and key parts in order: a column, or an expression as DuckDB writes it. |
| View | Its query as DuckDB stores it, and its comment. |
| Sequence | Start, increment, minimum, maximum, cycle, comment; the type is always `BIGINT`. |

Every column type is parsed (`internal/coltype`), and a type the provider does
not know is refused rather than read as text. The types are the ones the
embedded DuckDB has, nested ones included: `BOOLEAN`, the signed and unsigned
integers up to `HUGEINT` and `UHUGEINT`, `FLOAT`, `DOUBLE`, `DECIMAL(p,s)`,
`BIGNUM`, `VARCHAR`, `JSON`, `BLOB`, `BIT`, `UUID`, `DATE`, `TIME`, `TIME_NS`,
`TIME WITH TIME ZONE`, the `TIMESTAMP` family, `INTERVAL`, `GEOMETRY`,
`VARIANT`, and `ENUM`, `STRUCT`, `MAP`, `UNION`, lists (`T[]`) and arrays
(`T[N]`) of any of them.

DuckDB itself decides some of what a read reports, and the read says what
DuckDB holds:

- DuckDB keeps no constraint name. It names each constraint after its table
  and columns (`child_id_check`), whatever name the statement gave.
- `VARCHAR(10)`, `TEXT` and `STRING` are all `VARCHAR`: DuckDB keeps no
  length. `NUMERIC` is `DECIMAL`, `INT8` is `BIGINT`, `VARINT` is `BIGNUM`.
- A column of a type made with `CREATE TYPE` holds the type the name stands
  for: dropping the named type leaves the column as it was.
- An index key keeps no order and no `NULLS` placement; DuckDB accepts `DESC`
  and `NULLS LAST` and drops them. DuckDB refuses partial indexes.
- DuckDB refuses `CASCADE`, `SET NULL` and `SET DEFAULT` on a foreign key and
  a foreign key into another schema, and keeps `RESTRICT` as `NO ACTION`.
- An empty comment and no comment are the same.

What a read refuses, by name:

- a schema besides `main` for a host that does not read namespaces
  (`schema.common-namespaces`): such a host could not tell it from a schema
  the database does not have. A read that selects `main` reads it alone;
- a macro or a type made with `CREATE TYPE`, which the provider does not model;
- a `UNIQUE` constraint over more than one column (see the contract gaps);
- two constraints of one table under one name, such as two `CHECK`s over the
  same column: without names a read cannot tell them apart;
- a view that names its columns (`CREATE VIEW v (a, b) AS ...`) and a comment
  on a view's column;
- an index with a method other than DuckDB's own ART index;
- anything the host did not negotiate a way to receive: a table needs
  `schema.observed-columns`, a primary key `schema.observed-primary-keys`, a
  `CHECK` or foreign key `schema.observed-constraints`, an index
  `schema.observed-indexes`, and a view, sequence or comment
  `schema.common-objects`.

`PrepareCommonObjects` writes a declared view's query in the spelling DuckDB
stores it in, by parsing it and writing it back in a private in-memory
database, and fills each declared sequence's settings with DuckDB's defaults,
so Ptah compares a declaration with a read in the same terms.

## Planning and applying

`ptah schema apply` and `ptah migrations generate` work through this
provider: Ptah reads the database, compares it with the declared schema, and
asks the provider to plan, render and run the changes. `ptah migrations lint`
splits migration files with the provider's SQL analysis. Versioned migrations
are described under [Migrations](#migrations).

The provider needs a host of contract 25.30 or later, which states that the
host starts the provider in its own working directory, so a relative address
names the same file for both.

### What the provider plans

The provider owns the lifecycle of DuckDB's tables. Ptah hands it every table
a plan creates, changes or drops, and the provider plans each statement:

- `CREATE TABLE` with the columns, primary key, single-column `UNIQUE`,
  `CHECK`s and foreign keys, since DuckDB adds no `CHECK` and no foreign key to
  a table that exists; then `CREATE INDEX` for each index.
- On a table that exists: `ADD COLUMN`, `DROP COLUMN`, a column's type
  (`ALTER COLUMN ... TYPE`), default (`SET DEFAULT`, `DROP DEFAULT`) and
  nullability (`SET NOT NULL`, `DROP NOT NULL`), `ADD UNIQUE` for one column,
  `ADD PRIMARY KEY` on a table without one, `CREATE INDEX` and `DROP INDEX`.
- `DROP TABLE`, which drops the table's indexes. Tables of one schema that
  reference one another are dropped in one unit, each after the tables that
  reference it.
- Schemas, views, sequences and comments on tables, columns, indexes, views
  and sequences, through Ptah's common objects. A view is replaced by
  `DROP VIEW` and `CREATE VIEW`.

Every identifier is double-quoted and every table is named with its schema.
Ptah runs a plan in one transaction: a statement DuckDB refuses aborts it,
nothing the plan changed before it stays, and the failure names the unit and
DuckDB's class of error, such as `a constraint error: a row breaks a
constraint`, without quoting DuckDB's message, which can quote a value.

Objects in a schema other than `main` need a host that reads
`schema.common-namespaces`; for any other host a declaration that names
another schema is refused, since the read could not see it afterwards.

### What DuckDB keeps differently

DuckDB keeps neither a declared length nor a declared constraint name. The
plan says so, instead of failing or planning the same change on every run:

- **`VARCHAR(n)`.** DuckDB accepts a length and keeps none: `VARCHAR(255)`,
  `CHAR(2)` and `TEXT` are all `VARCHAR`, and `VARCHAR(5)[]` is `VARCHAR[]`.
  The plan compares the declaration as DuckDB holds it and carries a note:
  `DUCKDB: column "email" of table "users" is declared VARCHAR(255); DuckDB
  keeps no length and holds it as VARCHAR, which takes text of any length.`
  The note is printed with the plan and written into a generated migration.
- **Constraint names.** DuckDB names every constraint itself. The plan
  compares `CHECK`s and foreign keys by what they say, never by name, and the
  creation carries a note that lists the declared names DuckDB will not keep.

A collation is part of a column's type in DuckDB, and Ptah compares no
collation of its own, so the provider compares the two together: a changed
collation reaches the planner, which refuses it by name.

A default that draws from a sequence (`DEFAULT nextval('app.ids')`) is
checked in a private in-memory database that creates the sequence it names
first, so DuckDB accepts it there as it will in the database.

### What DuckDB refuses, refused by name

DuckDB refuses many changes of a table that exists. The provider refuses each
one before a statement is sent, with a reason that names the table and the
column, and never plans it as a removal and a new creation, which would lose
the table's rows. Measured on the embedded DuckDB:

| Change | Refused when |
| --- | --- |
| A column's type, nullability or `UNIQUE`, a dropped column, an added `NOT NULL` or `UNIQUE` column | The table keeps an index, or a foreign key of another table references it. A plan may drop the index in the same change, except as below. A default and a plain added column change either way. |
| A column's type or a dropped column, in the change that drops an index | The index covers the column, or the dropped column comes before a column it covers: DuckDB keeps a dropped index until the transaction commits. Drop the index in a change of its own first. |
| A column's type | The column is in the primary key, a `UNIQUE`, a `CHECK` or a foreign key. |
| A dropped column | The column is in the primary key, a `UNIQUE` or a foreign key, or a key or `UNIQUE` column comes after it. |
| A `CHECK` or foreign key added or dropped, a `UNIQUE` dropped, the primary key changed or dropped | Always: DuckDB has no statement for it. |
| A generated column added, a generated expression or a collation changed | Always. |
| A dropped table | A table the plan keeps references it. |
| A structural change of a table, a dropped table that references it and a created table that references it, in one plan | Always: Ptah creates every table before it drops any. Make the change and the new table in two plans. |
| Two created tables that reference each other | Always: DuckDB creates a foreign key only to a table that exists. |
| A sequence changed | Always: DuckDB alters no sequence, and replacing it would restart its counter. |

A declaration DuckDB cannot hold is refused when Ptah validates the schema,
before any planning: auto-increment and identity columns (declare a sequence
and `DEFAULT nextval('...')`), `STORED` generated columns, constraints that are
not enforced, a `UNIQUE` over an expression or over several columns (declare a
unique index instead), foreign key rules other than `NO ACTION` and
`RESTRICT`, deferrable keys, `MATCH`, keys into another schema, partial
indexes, index methods other than ART, operator classes, `INCLUDE`, key order
and `NULLS` placement, invisible indexes, triggers, table collations, storage
engines, materialized views, `WITH CHECK OPTION`, roles and grants.

## Migrations

`ptah migrations up`, `down`, `status`, `tag` and `log` run through the
provider, which keeps the migrator's records in three tables of the database:
the revision table (`schema_migrations`, or `atlas_schema_revisions` with
`--revision-format atlas`), the run log (`schema_migrations_log`) and the tags
(`ptah_migration_tags`), under the names and in the schema the command
selects.

| Table | Columns |
| --- | --- |
| Native revision table | `version BIGINT` key, `description`, `applied_at TIMESTAMPTZ`, `state` (`applied`, `pending` or `failed`, with `:down` for a rollback), `applied`, `total`, `error`, `error_stmt`, `execution_time_ms`, `checksum` |
| Atlas revision table | `version VARCHAR` key, `description`, `type`, `applied`, `total`, `executed_at TIMESTAMPTZ`, `execution_time` in nanoseconds, `error`, `error_stmt`, `hash`, `partial_hashes VARCHAR[]`, `operator_version` |
| Run log | `run_id` and `seq` as the key, `operation`, `version`, `state`, `actor`, `actor_source`, `checksum`, `logged_at TIMESTAMPTZ`, `error` |
| Tags | `tag` key, `version`, `recorded_at TIMESTAMPTZ` |

A timestamp is kept to the microsecond and the native run time to the
millisecond. The provider creates each table with a comment that marks it as
its own, and reads and writes a table of a store's name only when it carries
that mark and exactly its columns, key and nothing else. A table of that name
without the mark is the user's, and the provider refuses it rather than adopt
it. A read leaves the marked tables out of the schema it reports and names
them as reserved, so `schema apply` and `migrations generate` never plan to
drop them.

Each migration runs in the transaction mode the command selects. The provider
splits a body into its statements (`units.migration-split`), and DuckDB runs
every statement in a transaction, schema statements included: under `--tx-mode
file` a failed migration leaves nothing of itself, under `all` nothing of the
run, and under `none` each statement commits with its checkpoint, so a failed
migration stops after the last statement that committed and a rerun resumes
there. A body that controls a transaction or changes a setting (`BEGIN`,
`COMMIT`, `SET`, `PRAGMA`, `USE`) is refused before anything runs.

### Dev databases

A command that takes `--dev-url` accepts three forms:

- `docker://duckdb`, or `docker://duckdb/2.0` for the linked line: the
  provider creates a database file in a directory of its own under the
  system's temporary directory and removes the directory when the command
  releases it. DuckDB runs inside the provider, so nothing starts a container.
  Another version is refused: the provider links one DuckDB.
- the address of a database file, which must be empty: Ptah checks that it
  holds no object before it uses it, and empties it afterwards.
- `duckdb://:memory:`, the provider process's in-memory database, which ends
  with the command.

The cleanup removes every schema but `main` with what it holds, and the views,
macros, tables, sequences and types of `main`, in one transaction, so it
removes everything or nothing. Before a replay, Ptah asks the provider whether
each statement stays inside the dev database. The provider refuses a statement
that reaches beyond it or into the session: a temporary object, a setting, a
transaction or prepared statement, `ATTACH`, an extension, a secret, a file
(`COPY`, `EXPORT`), and a name in the `temp` or `system` catalog.

## Extensions and file access

A Ptah provider plugin and a DuckDB extension are different things. Building
this provider is not consent to download DuckDB extensions later, so every
database opens with these settings, which no address and no statement can
loosen (`SET enable_external_access = true` answers that external access
cannot be enabled while the database is running):

| Setting | Value | Effect |
| --- | --- | --- |
| `autoinstall_known_extensions` | `false` | A statement never downloads an extension. |
| `autoload_known_extensions` | `false` | A statement never loads an extension it needs. |
| `allow_community_extensions` | `false` | No community extension. |
| `allow_unsigned_extensions` | `false` | No unsigned extension. |
| `allow_persistent_secrets` | `false` | No secret is read from disk. |
| `enable_external_access` | `false` | No `LOAD`, `INSTALL`, `ATTACH`, `COPY`, or read or write of any file but the database itself; no network access. |

The database file, its write-ahead log and the temporary directory DuckDB
spills to stay usable: a sort of four million rows under a 64 MB memory limit
spills and completes with external access off, as it does with it on.

The extensions linked into the binary keep working: `autocomplete`,
`core_functions`, `icu`, `json`, `parquet`, `tpcds` and `tpch`, as DuckDB
lists them with external access on. Under the policy, `duckdb_extensions()`
itself is refused, because it reads the extension directory.

Allowing more, an explicit opt-in to external access or to a named extension
with its trust policy, is not implemented yet.

## Capability inventory

Each entry is supported, unsupported by the engine, not implemented by the
provider yet, or unavailable on the embedded DuckDB, with its evidence.

| Capability | Status | Evidence |
| --- | --- | --- |
| Sessions on database files, read-write and read-only | Supported | `integration/sessions_fs_test.go`, `integration/instances_fs_test.go` |
| In-memory databases for the process's lifetime | Supported | `TestOpenMemory_LivesForTheProcess`, `TestOpen_InMemorySession` |
| Relative paths against the host's working directory | Supported | `TestProviderResolvesARelativePathAgainstTheHostsDirectory` |
| Lock conflicts with another provider process or program | Reported by name | `TestProvidersCompeteForAFile`, `TestProvidersShareAReadOnlyFile`, `TestProviderReportsAnExternalWriter` |
| Extension and file access policy | Enforced | `TestSettings_HoldOnEveryFile`, `TestPolicy_RefusesExternalAccess` |
| `transactional_ddl`, `ddl_inside_transaction` | True | `TestSessionCapabilities_MatchTheEngine` |
| Identifier rules: ASCII case folding, schema-wide index names | Measured | `TestIdentifierRules_MatchTheEngine` |
| Inspection of every schema: schemas as namespaces, tables, columns with native and nested types, defaults, generated columns, primary keys, single-column `UNIQUE`, `CHECK`, `FOREIGN KEY`, indexes, views, sequences, comments | Supported | `TestRead_RichSchema`, `TestRead_TypeInventory`, `TestInspect_RichSchema`, `TestProviderInspectsADatabaseFile` |
| Macros, types made with `CREATE TYPE` | Refused by name; not modeled yet | `TestRead_FailurePath` |
| `UNIQUE` over several columns | Refused by name; the contract has no field for it | `TestRead_FailurePath` |
| Schemas besides `main` | Supported for a host that reads namespaces | `TestInspect_EverySchema`, `TestInspect_SelectsSchemas`, `TestRead_AnotherSchema` |
| Constraint names | Unsupported by the engine: DuckDB names constraints itself | `TestRead_RichSchema`, `TestRead_FailurePath` |
| `VARCHAR(n)` length, index key order, foreign key rules other than `NO ACTION` | Unsupported by the engine | measured; see Inspection |
| Partial indexes, `STORED` generated columns, schema comments, foreign keys into another schema | Unsupported by the engine | measured |
| Conformance suite (offline, sessions, inspection, transactions) | Passes, except `live.locks`; see the contract gaps | `TestConformance` |
| Planning, rendering and running table changes, with each refusal by name | Supported | `integration/host_apply_e2e_test.go`, `integration/host_refusals_e2e_test.go` |
| Views, sequences, comments and schemas in a plan | Supported | `TestHostManagesCommonObjects`, `TestHostManagesAnotherSchema` |
| `ptah migrations generate` and `ptah migrations lint` | Supported | `integration/host_migrations_e2e_test.go` |
| Transactions: a refused statement leaves nothing applied | Supported | `TestHostRollsBackAPlanDuckDBRefuses`, `TestConformance` (`live.transactions`) |
| Locks | Supported for the sessions of one process; another process cannot open the file | `TestLocks` |
| Revision history, run log and tags (`ptah migrations up`, `down`, `status`, `tag`, `log`), native and Atlas layouts | Supported | `TestHostRunsMigrationsUpAndDown`, `integration/history_fs_test.go`, `integration/metadata_fs_test.go` |
| Transaction modes `file`, `none` and `all` | Supported | `TestHostRunsMigrationsInEachTransactionMode` |
| The provider's tables reserved in a read | Supported | `TestInspect_ReservesTheProvidersTables` |
| Dev databases: provisioned, named, cleaned, replay guard | Supported | `TestHostUsesADevDatabase`, `integration/devclean_fs_test.go` |
| Release line `2.0` | Certified by the linked library, `2.0.0` (`engine.linked-release-lines`) | `TestDescription_DeclaresTheLinkedReleaseLine` |
| Network, MotherDuck, DuckLake, Quack | Out of scope | embedded file mode only |

## Contract gaps

These are generic omissions of the provider contract or of Ptah, recorded here
and not worked around:

- **UNIQUE over several columns.** An observed table carries a primary key,
  FOREIGN KEY and CHECK constraints and indexes, and a single-column UNIQUE as
  a column flag. A UNIQUE constraint over several columns has no field: it is
  not an index, and `Table.constraints` takes FOREIGN KEY and CHECK only. The
  read refuses such a table rather than report it as an index or leave the
  constraint out, and a declaration of one is refused.
- **Table ownership needs a table setting.** Ptah assigns a table to the
  planning service that owns one of the table's settings. DuckDB's tables have
  none of their own, so the provider declares a desired and an observed model,
  `table-lifecycle`, no value of which exists, only to own their lifecycle.
- **No collation aspect.** A column comparison asks for type, default,
  nullability and sequence only. The provider compares a collation as part of
  the type, which is how DuckDB writes one.
- **Removal order of related tables.** Ptah orders the removals of tables by
  the declared schema's dependencies, which never name a removed table, and
  keeps them in a sequence of its own. An order between separate removal steps
  can contradict that sequence, so the provider drops related tables of one
  schema in one unit.
- **Named in-memory databases as a URL.** Ptah parses `--db-url` and
  `--dev-url` as URLs before the provider sees them, and
  `duckdb://:memory:<name>` is not one: Go's parser reads the name as a port.
  `duckdb://:memory:` passes. The provider provisions a dev database as a file
  for the same reason.
- **The conformance suite's lock check.** `live.locks` opens the database from
  a second process to take the lock against the first. DuckDB lets no second
  process open a file one process writes, which is the exclusion the lock
  exists for, so the check fails at the open. `TestConformance` holds that
  exact failure; `TestLocks` covers the sessions of one process.

## Platforms

The release ships exactly the platforms the driver links a prebuilt DuckDB
library for (`go.duckdb.org/libduckdb`):

| OS | Architecture | Built and tested on |
| --- | --- | --- |
| Linux (glibc) | amd64 | `ubuntu-22.04` |
| Linux (glibc) | arm64 | `ubuntu-22.04-arm` |
| macOS | arm64 | `macos-15` |
| macOS | amd64 | `macos-15-intel` |
| Windows | amd64 | `windows-2025` |

There is no Windows arm64, musl (Alpine) or FreeBSD build: the driver ships no
library for them, and a custom DuckDB build is not part of this release. On
Alpine the link fails, because the driver's Linux library needs glibc's
`malloc_trim`, `backtrace` and `__res_init`. Every other Ptah platform keeps
working without DuckDB.

The executable links DuckDB statically, but not the C and C++ runtimes. The
release job prints what each archive's executable needs
(`scripts/report-runtime-deps.sh`). Measured on the release builds:

- Linux, both architectures: `libc.so.6`, `libm.so.6`, `libstdc++.so.6` and
  `libgcc_s.so.1`, with glibc 2.34 or newer and the libstdc++ of GCC 12 or
  newer (`GLIBCXX_3.4.30`, `CXXABI_1.3.13`), as Ubuntu 22.04 and Debian 12
  ship them.
- macOS: the system's `libc++`, `libresolv`, CoreFoundation and Security, on
  macOS 13 or newer; the build sets that minimum, the oldest the Go toolchain
  supports.

## Build and test

The module pins the experimental SDK, `github.com/stokaro/ptah/provider-sdk`,
by version and has no `replace` directive. It needs neither the Ptah source
nor a local SDK checkout. Building needs cgo and a C toolchain: the system
compiler on Linux and macOS, MinGW-w64 GCC on Windows (see the driver's
README). A build with `CGO_ENABLED=0` fails to compile rather than produce a
provider without DuckDB.

```sh
GOWORK=off go test -race ./...
GOWORK=off go test -race -tags integration ./integration
GOWORK=off go build -o bin/ptah-plugin-duckdb ./cmd/ptah-plugin-duckdb
make lint
```

The integration tests open DuckDB in the test process and start the provider
as a subprocess, so they need no database server or container. They include
the SDK's conformance suite, which checks the executable as a strict host.
`PTAH_DUCKDB_PROVIDER_BIN` names a built executable for the end-to-end tests to
start instead of building one with the race detector.

The host-driven tests (`TestHost...`) run Ptah's own binary with this provider
selected, and skip without one. `PTAH_HOST_TEST_BIN` names it:

```sh
(cd ../ptah && GOWORK=off go build -o /tmp/ptah ./cmd/ptah)
PTAH_HOST_TEST_BIN=/tmp/ptah GOWORK=off go test -tags integration ./integration -run '^TestHost'
```

CI builds Ptah's master, and the newest release once one speaks the
provider's minimum contract, and fails a leg in which a host-driven test
skipped.

`make lint` holds the provider to Ptah's lint contract: qtlint, nolintguard and
golangci-lint over both build contours, and the declarative-test baseline. CI
compares `.golangci.yml` with the host's (`internal/cmd/lintcontract`).

The executable requires the SDK's authenticated local subprocess bootstrap. Run
it through a Ptah SDK client, not as a command-line tool. Print the release
lines it declares with:

```sh
GOWORK=off go run ./cmd/release-lines
```

## Releases

A tag that starts with `v` runs `.github/workflows/release.yml`. Because the
provider links DuckDB through cgo, no single runner builds every platform: a
job on each platform's own runner builds the executable, runs the end-to-end
tests and the conformance suite against that exact binary, prints its runtime
dependencies, and packs its archive with `internal/cmd/releasearchive`. A last
job writes `release-lines.json` and `ptah-provider.json`, checks that the
manifest names exactly the archives the builds wrote, writes `checksums.txt`
and publishes the release:

```text
ptah-plugin-duckdb_<version>_<os>_<arch>.tar.gz   (.zip on Windows)
checksums.txt
release-lines.json
ptah-provider.json
```

`<version>` is the tag without the leading `v`. Every archive holds the
`ptah-plugin-duckdb` executable (`ptah-plugin-duckdb.exe` on Windows),
`README.md` and `LICENSE`. Its entries are sorted and carry the commit's time
and no owner, so packing one binary twice writes the same bytes. The binaries
are built with `-trimpath`. `checksums.txt` lists the SHA-256 digest of every
archive and document in `sha256sum` format. A host pins a release by version
and by the digest listed there for its platform's archive.

A pull request that touches the release inputs runs the same build jobs and
the checks of the last job as a snapshot, and publishes nothing.

To build a release archive locally, on the platform it is for:

```sh
GOWORK=off go build -trimpath -ldflags "-s -w -X github.com/stokaro/ptah-duckdb-plugin/internal/buildversion.linked=0.1.0" \
  -o bin/ptah-plugin-duckdb ./cmd/ptah-plugin-duckdb
GOWORK=off go run ./internal/cmd/releasearchive -version 0.1.0 -os "$(go env GOOS)" -arch "$(go env GOARCH)" \
  -executable bin/ptah-plugin-duckdb -out dist -modified "$(git log -1 --format=%ct)"
```

## License

MIT. See `LICENSE`.
