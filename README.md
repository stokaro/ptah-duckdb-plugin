# Ptah DuckDB provider

This repository holds the executable DuckDB provider for
[Ptah #4204](https://github.com/stokaro/ptah/issues/4204). Ptah starts it as a
subprocess and talks to it over the provider protocol of
`github.com/stokaro/ptah/provider-sdk`.

**The provider opens and reads databases; it plans, renders and runs nothing
yet.** It opens DuckDB database files and in-memory databases, reports the
embedded DuckDB's version, identifier rules and capabilities, and reads the
main schema of a database completely or refuses the read by name. Ptah
requires rendering, execution and SQL analysis of every provider it runs, so
a Ptah command does not use this provider yet; the SDK's client and its
conformance suite do. The other services follow, each advertised in the
description only when it exists.

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
  executable.
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

### Files that are not DuckDB databases

Before DuckDB opens a file, the provider reads its header and refuses an empty
file, a SQLite database and any other file that is not a DuckDB database. It
also refuses a directory, a file inside something that is not a directory, a
file it may not read, and, read-write, a file it may not write. DuckDB itself
answers a SQLite file by loading its `sqlite` extension, which the provider
never lets it do. A corrupt file is refused with its checksum mismatch named.

## Inspection

`Inspect` reads the main schema of the session's database from DuckDB's own
catalog functions (`duckdb_tables()`, `duckdb_columns()`,
`duckdb_constraints()`, `duckdb_indexes()`, `duckdb_views()`,
`duckdb_sequences()`, `duckdb_types()` and `duckdb_functions()`). A read is
complete or it fails with `FailedPrecondition` and a reason that names what
it does not describe: a read that left an object out would make Ptah plan to
create it.

What a read carries:

| Object | Read as |
| --- | --- |
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

- a schema besides `main`, in a read of the whole database (a read that
  selects `main` reads it alone), and a selector that names another schema:
  Ptah's contract does not carry schemas yet;
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

`PrepareCommonObjects`, which writes a declared view, sequence or comment in
DuckDB's spelling for Ptah to compare, refuses every request until the
planning services exist.

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
| Inspection of the main schema: tables, columns with native and nested types, defaults, generated columns, primary keys, single-column `UNIQUE`, `CHECK`, `FOREIGN KEY`, indexes, views, sequences, comments | Supported | `TestRead_RichSchema`, `TestRead_TypeInventory`, `TestInspect_RichSchema`, `TestProviderInspectsADatabaseFile` |
| Macros, types made with `CREATE TYPE` | Refused by name; not modeled yet | `TestRead_FailurePath` |
| `UNIQUE` over several columns | Refused by name; the contract has no field for it | `TestRead_FailurePath` |
| Schemas besides `main` | Refused by name until the contract carries schemas | `TestInspect_FailurePath` |
| Constraint names | Unsupported by the engine: DuckDB names constraints itself | `TestRead_RichSchema`, `TestRead_FailurePath` |
| `VARCHAR(n)` length, index key order, foreign key rules other than `NO ACTION` | Unsupported by the engine | measured; see Inspection |
| Partial indexes, `STORED` generated columns, schema comments | Unsupported by the engine | measured |
| Conformance suite (offline, sessions, inspection) | Passes | `TestConformance` |
| Preparing declared views and sequences, rendering, planning, execution, history, locks, dev databases | Not implemented yet | |
| Release lines | Not declared | the contract requires an image for a tested line; see below |
| Network, MotherDuck, DuckLake, Quack | Out of scope | embedded file mode only |

## Contract gaps

These are generic omissions of the provider contract, recorded here and not
worked around:

- **Working directory.** `OpenRequest` carries no invocation directory. The
  provider resolves a relative path against its own working directory, which
  is the host's because the SDK's bootstrap starts the process without setting
  one. Nothing in the contract states that, so a host that started providers
  elsewhere would move every relative DuckDB path without either side
  noticing.
- **UNIQUE over several columns.** An observed table carries a primary key,
  FOREIGN KEY and CHECK constraints and indexes, and a single-column UNIQUE as
  a column flag. A UNIQUE constraint over several columns has no field: it is
  not an index, and `Table.constraints` takes FOREIGN KEY and CHECK only. The
  read refuses such a table rather than report it as an index or leave the
  constraint out.
- **Release lines of an embedded engine.** A release line may claim
  `CERTIFIED` or `LEGACY_TESTED` only with an `image`, and an embedded engine
  has none: the provider embeds one DuckDB, which every test runs against, yet
  could declare it only as `BEST_EFFORT`. The provider declares no line until
  the contract can say "embedded, tested".

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
