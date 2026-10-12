---
title: "Collations"
description: "COLLATION declarations with provider, locale, and determinism."
weight: 4
---

## ICU collation (non-deterministic)

Non-deterministic collations allow case-insensitive and accent-insensitive comparisons.

```sql
SCHEMA public {
    COLLATION case_insensitive (
        PROVIDER      = icu,
        LOCALE        = 'und-u-ks-level2',
        DETERMINISTIC = false
    );
}
```

```sql
-- emits
CREATE COLLATION "public"."case_insensitive" (
    PROVIDER      = icu,
    LOCALE        = 'und-u-ks-level2',
    DETERMINISTIC = false
);
```

## Standard libc collation

```sql
SCHEMA public {
    COLLATION custom_en (
        PROVIDER = libc,
        LOCALE   = 'en_US.UTF-8'
    );
}
```

```sql
-- emits
CREATE COLLATION "public"."custom_en" (
    PROVIDER = libc,
    LOCALE   = 'en_US.UTF-8'
);
```

## Collation copy

```sql
SCHEMA public {
    COLLATION my_collation (COPY = pg_catalog."default");
}
```

```sql
-- emits
CREATE COLLATION "public"."my_collation" (COPY = pg_catalog."default");
```

## Diffing behaviour

| Change | SQL | Safety |
|--------|-----|--------|
| Owner changed | `ALTER COLLATION OWNER TO` | `SAFE` |
| Renamed (`RENAMED FROM`) | `ALTER COLLATION RENAME TO` (+ `SET SCHEMA` if schema-qualified) | `CAUTION` |
| `REFRESH VERSION` present in source | `ALTER COLLATION REFRESH VERSION` (re-emitted every apply while present) | `CAUTION` |
| Any other property change (`LOCALE`/`PROVIDER`/`RULES`/`FROM` target/etc.) | `DROP COLLATION` + `CREATE COLLATION` | `DESTRUCTIVE` |

Columns using the collation must be re-indexed after a drop-and-recreate.
