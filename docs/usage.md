---
title: Using the DuckDB provider with Ptah
description: How a duckdb:// address reaches a database file or an in-memory database through this provider, what DuckDB's one-writer rule means for Ptah, and how a dev database is chosen.
type: how-to
sourceMode: source-neutral
audience:
  - database-engineer
readerQuestion: "How does Ptah connect to a DuckDB database through this provider?"
goal: "Point Ptah at a DuckDB file or in-memory database, run a schema change on it, and choose the database a run rehearses on."
sourceOfTruth:
  - internal/address
  - internal/instances
  - internal/backend/sessions.go
  - internal/backend/identity.go
  - internal/backend/devdb.go
  - cmd/ptah-plugin-duckdb
  - stokaro/ptah#5667
generated: false
searchAliases:
  - "duckdb:// URL"
  - "DuckDB file"
  - "DuckDB in-memory database"
overlaps: []
disposition: keep
sidebar:
  order: 1
---

# Using the DuckDB provider with Ptah

Ptah reaches DuckDB through this provider, provider ID
`github.com/stokaro/ptah-duckdb-plugin`. DuckDB is an embedded database: the
provider links the DuckDB library and opens the database file itself, so there
is no server to start and no container to run. The provider reads a database,
plans every table change, renders and runs the statements, keeps the
migrator's records, and empties dev databases. Ptah keeps the shared
pipeline: the source formats, the comparison and ordering of a plan, the
migration policy and the command surfaces.

The engine name is `duckdb`, with no alias. A `duckdb://` address names it.

## Support level

The provider embeds one DuckDB, through the official Go driver
`github.com/duckdb/duckdb-go/v2`, and every test of the provider runs that
library. It declares one release line, `2.0`, certified by the library the
executable links, version `2.0.0`. A Ptah that does not read linked release
lines (`engine.linked-release-lines`) receives the line as best effort, with a
reason that names the linked version.

The provider needs a Ptah of provider contract 25.32 or later that lists
`planning.table-ownership`, the provider's only claim on DuckDB's tables. A
Ptah that does not list it is refused when it starts the provider.

## Install and select the provider

Install the newest release this Ptah can run:

```console
ptah providers install duckdb
```

A command that names a `duckdb://` address then selects the installed copy
without a flag or a manifest. Ptah builds in no DuckDB engine of its own, so
the provider is the only way to reach DuckDB.

Apply a schema to a database file, which the first run creates:

```console
ptah schema apply --db-url duckdb://app.duckdb --schema-file schema.sql
```

Run the migrations of a directory against it:

```console
ptah migrations up --db-url duckdb://app.duckdb --migrations-dir migrations
```

## Addresses

A DuckDB address names a database file or an in-memory database:

```text
duckdb://<path>[?access_mode=read_only|read_write]
duckdb://:memory:
```

- The path is everything between `duckdb://` and the first `?`, with percent
  escapes decoded. A space may be written as `%20` or as itself; a `?` or `#`
  in a file name is written `%3F` or `%23`.
- `duckdb:///var/lib/app.duckdb` is an absolute path. On Windows,
  `duckdb://C:/data/app.duckdb` and `duckdb:///C:/data/app.duckdb` name the
  same absolute file.
- Any other path is relative, such as `duckdb://app.duckdb` or
  `duckdb://data/app.duckdb`. It resolves against the directory Ptah was
  started in, never the directory of the provider's executable: the host
  starts the provider in its own working directory.
- `access_mode` is the only option. `read_write` is the default, and opening a
  file that does not exist creates it when its directory exists. `read_only`
  opens an existing file and creates nothing.
- `duckdb://:memory:` is an in-memory database of the provider process. Every
  session of one command reaches the same database, and it ends with the
  command. The provider reads `duckdb://:memory:<name>` as another in-memory
  database, but Ptah parses an address as a URL before the provider sees it,
  and a URL parser reads the name as a port, so Ptah refuses a named one.

The provider refuses, by name and before touching any file: another scheme, an
address that names no database, an unknown or repeated option, a read-only
in-memory database, a file that is not a DuckDB database (an empty file, a
SQLite database, a directory), and a file it may not read or, read-write, may
not write.

## One writer at a time

DuckDB lets one process write a database file, or several read it, never both.
The provider keeps that rule and says so when it applies:

- Another process that holds the file, a second Ptah command or any other
  program, keeps this provider out. The refusal names the holder's process ID:
  `another process (PID 4242) has the database file open for writing; DuckDB
  lets no other process open it until that one closes it`. When the holder
  only reads, the reason says that more processes may read the file with
  `access_mode=read_only`.
- Inside one command, every session of a file shares one DuckDB instance, and
  a session that asks for the other access mode is refused by name.

The lock Ptah takes before it applies a plan or runs migrations is kept by the
provider for the sessions of one process, which share the instance; DuckDB
itself keeps every other process out of a file one process writes.

## Extensions and files

DuckDB can download and load extensions and read or write files a statement
names. The provider allows neither: every database opens with
`enable_external_access`, `autoinstall_known_extensions` and
`autoload_known_extensions` off, so a statement never downloads an extension,
attaches another database, or reads or writes a file but the database itself.
The extensions linked into the library keep working, `json`, `parquet`, `icu`
and `core_functions` among them. The provider offers no opt-in to more yet.

## Dev databases

A command that takes `--dev-url`, such as `migrations lint` or `migrations
generate --replay`, rehearses on a dev database. Three forms work:

| `--dev-url` | What the provider uses |
| --- | --- |
| `docker://duckdb`, or `docker://duckdb/2.0` | A database file in a new directory under the system's temporary directory, which the provider removes when the command ends. No container starts: DuckDB runs inside the provider. Another version is refused, because the provider links one DuckDB. |
| A file address, such as `duckdb:///tmp/dev.duckdb` | That file. It must hold no object: Ptah checks it is empty before it uses it, and empties it afterwards. |
| `duckdb://:memory:` | The provider process's in-memory database, which ends with the command. |

For example:

```console
ptah migrations lint --dir migrations --dev-url docker://duckdb
```

[Migrations on DuckDB](migrations.md#dev-databases) says what a cleanup
removes and which statements a replay refuses.
