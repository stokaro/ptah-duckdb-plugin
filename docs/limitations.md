---
title: What the DuckDB provider refuses
description: The objects a DuckDB read refuses, the declarations and table changes the provider refuses before a statement is sent, and what it does not support yet.
type: reference
audience:
  - database-engineer
  - schema-author
readerQuestion: "Which DuckDB objects and changes does the provider refuse or not describe yet, and why?"
goal: "Tell before planning whether a DuckDB schema or change is one the provider refuses, and what to do instead."
sourceOfTruth:
  - internal/catalog
  - internal/tableplan
  - internal/coltype
  - internal/commonobject
  - internal/migrationunits
  - internal/devclean
  - stokaro/ptah#5667
generated: false
searchAliases:
  - "DuckDB limitations"
  - "DuckDB ALTER TABLE refused"
overlaps: []
disposition: keep
sidebar:
  order: 4
---

# What the DuckDB provider refuses

Before you plan a DuckDB change, check here whether the provider refuses the
object or the change, and whether it describes the object yet. A refusal names
the object and the reason, and comes before any statement is sent. Ptah plans
to create what a read leaves out, so an object the provider cannot describe is
refused rather than skipped.

## Objects a read refuses

A read fails with a reason, rather than leave out an object it does not
describe:

| Object in the database | Reason |
| --- | --- |
| A macro, scalar or table | The provider does not model macros yet. |
| A type made with `CREATE TYPE` | The provider does not model named types yet. A column of such a type holds the type the name stands for and reads as that type. |
| A `UNIQUE` constraint over more than one column | Ptah's observed table has no field for it. |
| Two constraints of one table under one name, such as two `CHECK`s on the same column | DuckDB keeps no constraint names, so a read cannot tell them apart. |
| A view that names its columns, and a comment on a view's column | The provider does not describe them yet. |
| An index with a method other than DuckDB's ART index | The provider creates and reads ART indexes only. |
| A column with a compression | The observed column has no field for it. |
| A schema other than `main`, for a Ptah that does not read `schema.common-namespaces` | Such a Ptah could not tell it from a schema the database does not have. Select `main` to read it alone. |

## Declarations the provider refuses

| Declaration | Where it is described |
| --- | --- |
| An auto-increment or identity column; declare a sequence and `DEFAULT nextval('...')` | [Defaults and generated columns](declarations.md#defaults-and-generated-columns) |
| A `STORED` generated column | [Defaults and generated columns](declarations.md#defaults-and-generated-columns) |
| A `UNIQUE` over several columns or over an expression, and a constraint that is not enforced | [Keys and constraints](declarations.md#keys-and-constraints) |
| A foreign key rule other than `NO ACTION` or `RESTRICT`, a deferrable key, `MATCH`, a key into another schema | [Keys and constraints](declarations.md#keys-and-constraints) |
| A partial index, another method, an operator class, `INCLUDE`, storage parameters, a key order or `NULLS` placement, `NULLS NOT DISTINCT`, an invisible index | [Indexes](declarations.md#indexes) |
| A materialized view, `WITH CHECK OPTION`, a sequence with a cache, an owner or another type, a comment on a schema, a role, a grant | [Views, sequences, schemas and comments](declarations.md#views-sequences-schemas-and-comments) |
| A table collation, a storage engine, a trigger, custom table SQL | DuckDB has none of them. |

## Changes the provider refuses

DuckDB refuses many changes of a table that exists. The provider refuses each
one by name before a statement is sent, and never plans it as a removal and a
new creation, which would lose the table's rows. Measured on the embedded
DuckDB:

| Change | Refused when |
| --- | --- |
| A column's type, nullability or `UNIQUE`, a dropped column, an added `NOT NULL` or `UNIQUE` column | The table keeps an index, or a foreign key of another table references it. A plan may drop the index in the same change, except as in the next row. A default and a plain added column change either way. |
| A column's type or a dropped column, in the change that drops an index | The index covers the column, or the dropped column comes before a column it covers: DuckDB keeps a dropped index until the transaction commits. Drop the index in a change of its own first. |
| A column's type | The column is in the primary key, a `UNIQUE`, a `CHECK` or a foreign key. |
| A dropped column | The column is in the primary key, a `UNIQUE` or a foreign key, or a key or `UNIQUE` column comes after it. |
| A `CHECK` or foreign key added or dropped, a `UNIQUE` dropped, the primary key changed or dropped | Always: DuckDB has no statement for it. |
| A generated column added, a generated expression changed, a collation changed | Always. |
| A renamed index | Always: DuckDB renames no index. |
| A dropped table | A table the plan keeps references it. |
| A structural change of a table, with a dropped table and a created table that both reference it, in one plan | Always: Ptah creates every table before it drops any. Make the change and the new table in two plans. |
| Two created tables that reference each other | Always: DuckDB creates a foreign key only to a table that exists. |
| A changed sequence | Always: DuckDB alters no sequence, and replacing one would restart its counter. |

## Migrations and dev databases

A migration body that controls a transaction or changes a setting is refused
before anything runs, and a replay into a dev database refuses a statement
that reaches beyond it or into the session; [Migrations on DuckDB](migrations.md)
lists both. A dev database the operator names must be empty, and a
`docker://duckdb` dev database of a version other than the linked `2.0` is
refused.

## Not supported yet

- Declared rows and seed files (`ptah seed`, `ptah migrations data`): the
  provider serves no row services.
- Provider lint rules: `ptah migrations lint` runs Ptah's rules only.
- An opt-in to external access or to an extension DuckDB does not link.
- Network, MotherDuck, DuckLake and Quack: the provider serves the embedded
  file and in-memory modes only.
- `duckdb://:memory:<name>` as a `--db-url` or `--dev-url`: Ptah parses the
  address as a URL first and refuses it. `duckdb://:memory:` works.
