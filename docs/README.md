# DuckDB provider documentation

These pages and the repository [README](../README.md) document what DuckDB
does in Ptah: how an address reaches a database file, which statements a plan
writes, which changes the provider plans and which it refuses, how a migration
runs, and what is not supported yet. Ptah's own documentation, at
[docs.ptah.run](https://docs.ptah.run/edge/), describes the commands, the
source formats and the pipeline every engine shares; its
[Configure the DuckDB provider](https://docs.ptah.run/edge/extend/providers/duckdb/)
page installs and selects this provider.

| Topic | Where |
| --- | --- |
| Installing the provider, the engine name, addresses, concurrency, the extension policy and dev databases | [Using the DuckDB provider with Ptah](usage.md) |
| Tables, column types, keys, constraints, indexes, views, sequences, schemas and comments | [Declaring DuckDB objects](declarations.md) |
| How a migration runs, the tables the migrator's records live in, the lock, dev database cleanup and lint | [Migrations on DuckDB](migrations.md) |
| What the provider refuses and what it does not describe yet | [What the DuckDB provider refuses](limitations.md) |
| Releases, platforms and the minimum contract | [Releases](../README.md#releases) |
| Building the provider and running its tests | [Build and test](../README.md#build-and-test) |

The provider embeds one DuckDB, the library its driver links, and every test
runs against it. It declares that library's release line, `2.0`, as certified
by the linked library; each release publishes it in `release-lines.json`.
Ptah's support matrix lists only the engines Ptah builds in.

## How these pages reach docs.ptah.run

Every page in this folder except this README is published on docs.ptah.run
under `extend/providers/duckdb/<page>/`, beside Ptah's own pages, and
[Configure the DuckDB provider](https://docs.ptah.run/edge/extend/providers/duckdb/)
introduces them. The pages live here so that a change to the provider and a
change to its documentation are one pull request.

Ptah's documentation build imports this folder at the release of this provider
that `docs/site/scripts/data/provider-docs.json` in stokaro/ptah names. A merged
change here therefore reaches the site when Ptah declares a release that
carries it: the pull request that moves the declaration to the new release
imports its pages, records any new page's address, and adds a redirect for a
page this folder removed or renamed. Each released version of Ptah's
documentation keeps the pages of the release it declared.

A page here is written for both places:

- It starts with the frontmatter every page of the site carries: `title`,
  `description`, `type`, `audience`, `readerQuestion`, `goal`,
  `sourceOfTruth`, `generated: false`, `overlaps` and `disposition`.
  `sidebar.order` places it in the provider's part of the navigation.
- Its `# Title` heading repeats `title`, so the page reads on GitHub; the site
  renders the title from the frontmatter and drops the heading.
- It links another page of this folder by its file name, any other file of this
  repository by its relative path, and a Ptah page by its address on
  `https://docs.ptah.run/edge/`. The import turns each into an address that
  works on the site: a route of the site, or the file on GitHub at the imported
  commit. `sourceOfTruth` names paths relative to the repository root.

Ptah's
[documentation site README](https://github.com/stokaro/ptah/blob/master/docs/site/README.md#provider-documentation)
lists what the import accepts and refuses.

`.github/workflows/docs.yml` runs Ptah's documentation checks on this folder in
every pull request that changes it, from Ptah's own `provider-docs.yml`
workflow: the style, terminology and link checks, the command lines a page
publishes, and a build of the whole site with these pages in it. The checkers
stay in Ptah. To read a page on the site before a release declares it, import
this checkout into a Ptah checkout:

```console
cd docs/site
npm ci
node scripts/import-provider-docs.mjs --source duckdb=/path/to/ptah-duckdb-plugin
npm run dev
```

The import refuses an engine that `provider-docs.json` does not declare, so
until Ptah declares this provider, add a local entry for `duckdb` to that file
in the Ptah checkout first and leave it uncommitted. The workflow fails for the
same reason until then.
