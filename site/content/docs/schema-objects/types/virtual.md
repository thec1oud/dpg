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
VIRTUAL TYPE named_point AS point;   -- references virtual type "point"
VIRTUAL TYPE tags AS text[];         -- array form
```

```sql
-- emits nothing
```

### Composite

An inline record with named, typed fields. Field types may themselves be virtual type references:

```sql
VIRTUAL TYPE point AS (x float8, y float8);

VIRTUAL TYPE line_item AS (
    sku      text,
    quantity integer,
    price    numeric
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

Declare the column type as the virtual type name. DPG emits `jsonb` in the generated SQL. Add `[]` to get `jsonb[]`:

```sql
TABLE orders (
    id    bigint,
    meta  line_item,    -- single JSON object  → jsonb
    items line_item[]   -- JSON array          → jsonb[]
) { }
```

```sql
-- emits
CREATE TABLE "public"."orders" (
    "id"    bigint,
    "meta"  jsonb,
    "items" jsonb[]
);
```

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
- `VIRTUAL TYPE` may be schema-qualified; if unqualified it defaults to `default_schema`.
- When used as a column or attribute type, `[]` suffix causes DPG to emit `<format>[]` where `<format>` is `jsonb` (default) or `json` (if `PREFERRED JSON FORMAT json` is declared).
- Virtual types appear in the snapshot under `"kind": "virtual_type"` so code-generation tools can read them. The `json_format` field is stored when explicitly set.
- `dpg plan` produces no SQL for additions, modifications, or removals of virtual types.
- The `{}` block accepts `COMMENT` and `PREFERRED JSON FORMAT`. Any other directive is a compiler error.
- Downstream tooling consumes virtual types via the snapshot JSON or the `pkg/dpg` Go API. See [Plugin API](../../../extending/plugin-api/).
