# Ptah DuckDB provider

This repository will hold the executable DuckDB provider for
[Ptah #4204](https://github.com/stokaro/ptah/issues/4204).

**It is a scaffold with no features yet.** The executable negotiates with a
Ptah host, declares the `duckdb` engine, its identifier rules and its release
lines, and stops there. It implements no service. The session service the
runtime requires refuses every `Open` with `Unimplemented`. A host that
negotiates with it finds no schema inspection, planning, rendering or
execution to call, and no database address is read or contacted.

## What the host does today

The host has no DuckDB support of its own: it has no DuckDB dialect descriptor, no SQL rendering and no schema reader for it. Its provider catalog recognizes the engine name `duckdb` and the `duckdb://` URL scheme, and points to this repository as the official provider. Recognition does not install or authorize an executable; an operator still selects a trusted binary by path and checksum. See the host's [DuckDB provider page](https://github.com/stokaro/ptah/blob/master/docs/site/src/content/docs/extend/providers/duckdb.md).

## What the description declares

- Provider ID: `github.com/stokaro/ptah-duckdb-plugin`.
- Engine: `duckdb`, with no alias.
- Identifier rules: table, column and index names compare without regard to ASCII case, and an index name is unique in its schema, not in its table.
- Release lines: 2.0, 1.5 and 1.4. The host declares no DuckDB line, so these follow the lines the DuckDB project publishes. DuckDB is an embedded library, so no line names a container image. Every line is best effort and states why it runs
  no job. The scaffold certifies none.

## Roadmap

First comes a connection through an embedded DuckDB driver and the session service, then catalog inspection and SQL execution. Rendering and planning follow, and each service is advertised in the description only when it exists.

## Build and test

The module pins the published experimental SDK, `github.com/stokaro/ptah/provider-sdk`,
by pseudo-version. It needs neither the Ptah application source nor a local SDK
checkout and has no `replace` directive.

```sh
GOWORK=off go test -race ./...
GOWORK=off CGO_ENABLED=0 go build -o bin/ptah-plugin-duckdb ./cmd/ptah-plugin-duckdb
```

The executable requires the SDK's authenticated local subprocess bootstrap. Run
it through a Ptah SDK client, not as a command-line tool. Print the release
lines it declares with:

```sh
GOWORK=off go run ./cmd/release-lines
```

## Releases

A tag that starts with `v` runs `.github/workflows/release.yml`, which
publishes a GitHub release through GoReleaser (`.goreleaser.yaml`). Each
release carries one archive per platform, a `checksums.txt` file, and
`release-lines.json`:

```text
ptah-plugin-duckdb_<version>_<os>_<arch>.tar.gz   (.zip on Windows)
checksums.txt
release-lines.json
```

`<version>` is the tag without the leading `v`. The platforms are linux,
darwin and windows on amd64 and arm64. Every archive holds the
`ptah-plugin-duckdb` binary (`ptah-plugin-duckdb.exe` on Windows),
`README.md` and `LICENSE`. The binaries are built with `CGO_ENABLED=0` and
`-trimpath`.

`checksums.txt` lists the SHA-256 digest of every archive and of
`release-lines.json` in `sha256sum` format (`<hex digest>  <file name>`). A
host pins a release by version and by the digest listed there for its
platform's archive.

Pull requests that touch the release inputs run `goreleaser check` and a
snapshot build, then verify the snapshot's `checksums.txt`. To try it locally:

```sh
GOWORK=off goreleaser release --snapshot --clean
(cd dist && cp ../.release/release-lines.json . && shasum -a 256 -c checksums.txt)
```

## License

MIT. See `LICENSE`.
