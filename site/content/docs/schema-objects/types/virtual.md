---
title: "Virtual"
description: "`VIRTUAL TYPE` — typed schema for JSON/JSONB columns, resolved to jsonb in SQL."
weight: 5
---

Virtual types are DPG DDL constructs that give a structural schema to `JSON`/`JSONB` columns and JSON array columns. They generate no PostgreSQL DDL — no `CREATE TYPE`, `ALTER TYPE`, or `DROP TYPE` is ever emitted. When a column or composite type attribute is typed as a virtual type, DPG resolves it to `jsonb` (or `jsonb[]`) in all generated SQL.

The structured body is stored in the snapshot so downstream consumers — ORMs, type-safe query builders — can read type information via the snapshot JSON or the `pkg/dpg` Go API.

---

## Body forms

### Type reference

A single native PostgreSQL type or another declared `VIRTUAL TYPE`:

```sql
VIRTUAL TYPE label AS text;
VIRTUAL TYPE metric AS numeric;
VIRTUAL TYPE location AS point;      -- PostgreSQL's point type
VIRTUAL TYPE tags AS label[];        -- a JSON array of label values
```

```sql
-- emits nothing
```

### Composite

An inline record with named, typed fields. Field types may themselves be virtual type references:

```sql
VIRTUAL TYPE coordinate AS (x float8, y float8);

VIRTUAL TYPE line_item AS (
    sku      text,
    quantity integer,
    price    numeric,
    tags     label[]
);
```

```sql
-- emits nothing
```

### Union

Two or more terms joined with `|`. Any mix of composites and type references is valid:

```sql
VIRTUAL TYPE payment AS
    (kind text, amount numeric, currency text)
    | (kind text, token text)
    | text {
    COMMENT 'Discriminated union for payment method';
}
```

```sql
-- emits nothing
-- (COMMENT stored in snapshot for tooling consumers)
```

---

## Using a virtual type as a column type

Declare the column type as the virtual type name. DPG emits `jsonb` in the generated SQL. On a column, `[]` means a PostgreSQL array, as it does for any type, so `line_item[]` emits `jsonb[]` with one `line_item` per element. For a single `jsonb` value holding a JSON array, declare a virtual type whose body is the array:

```sql
VIRTUAL TYPE line_items AS line_item[];

TABLE orders (
    id      bigint,
    meta    line_item,    -- one JSON object              → jsonb
    items   line_item[],  -- PostgreSQL array of objects  → jsonb[]
    history line_items    -- one JSON array of objects    → jsonb
) { }
```

```sql
-- emits
CREATE TABLE "public"."orders" (
    "id"      bigint,
    "meta"    jsonb,
    "items"   jsonb[],
    "history" jsonb
);
```

Inside a virtual type body, `[]` always means a JSON array.

---

## Using a virtual type as a composite attribute type

```sql
TYPE full_address AS (
    street text,
    detail address_detail   -- virtual type ref → jsonb
);
```

```sql
-- emits
CREATE TYPE "public"."full_address" AS (
    "street" text,
    "detail" jsonb
);
```

---

## Preferred JSON format

By default, virtual type references resolve to `jsonb`. Use `PREFERRED JSON FORMAT` in the `{}` block to choose `json` instead:

```sql
VIRTUAL TYPE raw_event AS (type text, data text) {
    PREFERRED JSON FORMAT json;
}
```

```sql
-- column using raw_event emits json, not jsonb
CREATE TABLE "public"."ingest" (
    "id"      bigint,
    "event"   json,
    "history" json[]
);
```

Both `json` and `jsonb` are valid values. The directive has no effect on snapshot content — it only controls what DPG writes in generated DDL.

---

## Rules

- The body is parsed and validated by the compiler. It is one of: a type reference, a composite definition, or a union of those.
- `VIRTUAL TYPE` may be schema-qualified; if unqualified it belongs to its schema scope, or else to `default_schema`.
- A virtual type can't be named after a PostgreSQL built-in type (`point`, `text`, `integer`, ...), or reuse the name of a type, table, view, materialized view or foreign table in the same schema (DPG-E044). So a type name always means exactly one thing.
- An unqualified reference names a virtual type in the referencing object's own schema, or else in `default_schema`. Otherwise it's an ordinary PostgreSQL type.
- When used as a column or attribute type, `[]` suffix causes DPG to emit `<format>[]` where `<format>` is `jsonb` (default) or `json` (if `PREFERRED JSON FORMAT json` is declared).
- Virtual types appear in the snapshot under `"kind": "virtual_type"` so code-generation tools can read them. The `json_format` field is stored when explicitly set. Inside a body, a reference to another virtual type carries `"virtual": true` and its schema.
- A column declared with a virtual type keeps its declared type in the snapshot: `type` is the stored `jsonb`/`json` type and `virtual_type` is the qualified reference, for example `"public.line_item[]"`.
- `dpg plan` produces no SQL for additions, modifications, or removals of virtual types.
- The `{}` block accepts `COMMENT` and `PREFERRED JSON FORMAT`. Any other directive is a compiler error.
- Downstream tooling consumes virtual types via the snapshot JSON or the `pkg/dpg` Go API. See [Plugin API](../../../extending/plugin-api/).
