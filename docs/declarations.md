---
title: Declaring DuckDB objects
description: The column types, keys, constraints, indexes, views, sequences, schemas and comments the DuckDB provider creates, what DuckDB keeps differently from a declaration, and the statements a plan writes.
type: reference
audience:
  - schema-author
  - database-engineer
readerQuestion: "What can a Go, YAML, HCL or SQL schema declare for DuckDB, and which statements does the provider write for it?"
goal: "Declare a DuckDB schema that the provider creates and converges on, and predict the statements a plan writes."
sourceOfTruth:
  - internal/coltype
  - internal/tableplan
  - internal/tableop
  - internal/ddl
  - internal/scratch
  - internal/commonobject
  - internal/catalog
  - stokaro/ptah#5667
generated: false
searchAliases:
  - "DuckDB column types"
  - "DuckDB VARCHAR length"
  - "DuckDB constraint names"
overlaps: []
disposition: keep
sidebar:
  order: 2
---

# Declaring DuckDB objects

A schema declared for DuckDB, in Go, YAML, HCL or SQL, becomes DuckDB's own
statements. Every identifier is double-quoted and every table is named with
its schema, so a statement means the same in a plan and in a migration file.

The provider learns how DuckDB holds a declaration by creating it in a private
in-memory database and reading it back, and compares the declaration with a
read in those terms. A declaration DuckDB refuses there is refused before any
planning, with the column it refused.

## Column types

Every type DuckDB has is declared and read in DuckDB's own spelling, nested
types included: `BOOLEAN`; the signed and unsigned integers from `TINYINT` to
`HUGEINT` and `UHUGEINT`; `FLOAT`, `DOUBLE` and `DECIMAL(p,s)`; `BIGNUM`;
`VARCHAR`, `JSON`, `BLOB`, `BIT` and `UUID`; `DATE`, `TIME`, `TIME_NS`, `TIME
WITH TIME ZONE`, the `TIMESTAMP` family and `INTERVAL`; `GEOMETRY` and
`VARIANT`; and `ENUM`, `STRUCT`, `MAP`, `UNION`, lists (`T[]`) and arrays
(`T[N]`) of any of them. A type the provider does not know is refused, never
read as text.

An enum column is DuckDB's `ENUM('a', 'b')`, which lists the values in the
column's type. Ptah lowers a declared enum to it, whether a named type
declares the values (`CREATE TYPE mood AS ENUM ('happy', 'sad')`, a Go enum
field) or the column writes them, so the provider creates no named type:

```sql
CREATE TABLE "main"."t" (
  "id" INTEGER NOT NULL,
  "m" ENUM('happy', 'sad') DEFAULT 'happy',
  PRIMARY KEY ("id")
);
```

A changed value list changes the column's type in place:

```sql
ALTER TABLE "main"."t" ALTER COLUMN "m" TYPE ENUM('happy', 'sad', 'calm');
```

Rows keep their values by text. A row that holds a value the new list leaves
out makes DuckDB refuse the statement, and the plan rolls back.

DuckDB normalizes some spellings, and a read reports what it holds: `TEXT` and
`STRING` are `VARCHAR`, `NUMERIC` is `DECIMAL`, `INT8` is `BIGINT`, and
`VARINT` is `BIGNUM`. The comparison uses the same spelling, so a declaration
of `TEXT` converges on a column that holds `VARCHAR`.

### VARCHAR(n)

DuckDB accepts a length and keeps none: `VARCHAR(255)` and `CHAR(2)` are
`VARCHAR`, and `VARCHAR(5)[]` is `VARCHAR[]`, which takes text of any length.
The plan compares the declaration as DuckDB holds it, so it converges, and the
statement that creates the column carries a note that says what DuckDB holds:

```sql
-- DUCKDB: column "email" of table "users" is declared VARCHAR(255); DuckDB keeps no length and holds it as VARCHAR, which takes text of any length.
CREATE TABLE "main"."users" (
  "id" INTEGER NOT NULL,
  "email" VARCHAR NOT NULL UNIQUE,
  PRIMARY KEY ("id")
);
```

The note is printed with the plan and written into a generated migration.

## Defaults and generated columns

A default is a literal or an expression. A literal on a numeric or boolean
column is written bare and any other literal as a quoted string; an expression
is written as declared and compared in the spelling DuckDB stores it in, so a
declared `DEFAULT [1, 2]` converges on a stored `list_value(1, 2)`.

A default may draw from a sequence, `DEFAULT nextval('item_ids')`; the plan
creates the sequence before the table.

DuckDB computes a generated column when it reads a row (`VIRTUAL`); a `STORED`
generated column is refused.

A column may name a collation, such as `COLLATE nocase`. Ptah compares a
collation as an aspect of the column, and the provider refuses a changed one
by name: DuckDB changes no column's collation. A Ptah that does not compare
collations (`schema.column-collations`) has the provider compare it as part of
the type, which is how DuckDB writes one, and the change is refused the same
way.

## Keys and constraints

| Declaration | What DuckDB holds |
| --- | --- |
| A primary key, on one column or several | The key, its columns `NOT NULL`. |
| `UNIQUE`, on one column or several | The UNIQUE constraint, written in `CREATE TABLE` or added with `ADD UNIQUE`. One over several columns needs a Ptah that reads UNIQUE constraints (`schema.observed-unique-constraints`); for any other Ptah it is refused, and a unique index declares the same rule. |
| `CHECK`, on a column or the table | The condition in DuckDB's spelling, `(score >= 0)`, written in `CREATE TABLE`. |
| A foreign key | The key, into a table of the same schema, with the rule `NO ACTION`. |

DuckDB adds no `CHECK` and no foreign key to a table that exists, so the
provider writes them in the table's `CREATE TABLE`. A foreign key with `ON
DELETE` or `ON UPDATE` other than `NO ACTION` or `RESTRICT`, a deferrable key,
`MATCH` and a key into another schema are refused: DuckDB has none of them.

### Constraint names

DuckDB names every constraint itself, after its table and columns
(`users_score_check`, `m_a_b_key` for a UNIQUE over `a` and `b`), whatever
name the declaration gives it. The plan compares `CHECK`s and foreign keys by
what they say and a `UNIQUE` by its columns in order, never by name, so a
declared name does not make the plan repeat itself, and a declaration that
renames a constraint plans nothing. The creation carries a note that lists the
declared names DuckDB will not keep:

```sql
-- DUCKDB: DuckDB keeps no constraint names; table "posts" holds "fk_posts_user" under the names DuckDB gives them.
```

## Indexes

An index is DuckDB's ART index on columns or expressions, unique or not:

```sql
CREATE INDEX "posts_title" ON "main"."posts" ("title");
CREATE UNIQUE INDEX "users_lower_email" ON "main"."users" ((lower(email)));
```

DuckDB keeps no key order and no `NULLS` placement, so a key that states
either is refused rather than dropped. A `WHERE` filter, another index method,
an operator class, `INCLUDE`, storage parameters, `NULLS NOT DISTINCT` and an
invisible index are refused too. An index name is unique in its schema. DuckDB
renames no index: a renamed index is refused, and declaring the new name with
the old one removed plans a drop and a creation.

## Views, sequences, schemas and comments

| Object | What the provider writes |
| --- | --- |
| View | `CREATE VIEW`, and a changed query as `DROP VIEW` then `CREATE VIEW`. The query is compared in the spelling DuckDB stores it in. A materialized view, `WITH CHECK OPTION` and a view that names its columns are refused. |
| Sequence | `CREATE SEQUENCE` with `INCREMENT BY`, `MINVALUE`, `MAXVALUE`, `START WITH` and `CYCLE`. A DuckDB sequence counts in `BIGINT`, has no cache and no owner. DuckDB alters no sequence, and replacing one would restart its counter, so a changed sequence is refused. |
| Schema | `CREATE SCHEMA`, for a Ptah that reads `schema.common-namespaces`. For any other Ptah a declaration in a schema other than `main` is refused, since the read could not see it afterwards. A comment on a schema is refused. |
| Comment | `COMMENT ON` a table, a column, an index, a view or a sequence; an empty comment removes it. |

A role and a grant are refused: DuckDB has neither.

## Changing a table

On a table that exists, the provider writes one statement per change:

```sql
ALTER TABLE "main"."t" ADD COLUMN "c" VARCHAR DEFAULT 'x';
ALTER TABLE "main"."t" ALTER COLUMN "b" SET DEFAULT 2;
ALTER TABLE "main"."t" ALTER COLUMN "b" TYPE BIGINT;
ALTER TABLE "main"."t" ALTER COLUMN "b" SET NOT NULL;
ALTER TABLE "main"."t" ADD UNIQUE ("c");
ALTER TABLE "main"."t" ADD UNIQUE ("b", "c");
ALTER TABLE "main"."t" ADD PRIMARY KEY ("id");
ALTER TABLE "main"."t" DROP COLUMN "a";
```

A key or a `UNIQUE` runs after the statements that add its columns or make
them `NOT NULL`.

DuckDB refuses many of these while the table has an index or another table's
foreign key references it. [What the DuckDB provider refuses](limitations.md#changes-the-provider-refuses)
lists each case; the provider refuses it by name before a statement is sent and
never plans it as a removal and a new creation.

Tables of one schema that reference one another and that a plan drops are
dropped in one unit, each after the tables that reference it:

```sql
DROP TABLE "main"."lines";
DROP TABLE "main"."orders";
DROP TABLE "main"."customers";
```
