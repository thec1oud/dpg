---
title: "Aggregates"
description: "AGGREGATE declarations with SFUNC, STYPE, INITCOND, FINALFUNC, and ordered-set variants."
weight: 4
---

Aggregates use two `( )` groups per PostgreSQL's `CREATE AGGREGATE` syntax — both are Part 1. The `{ }` block holds grants and comments.

## Basic aggregate

```sql
SCHEMA public {
    AGGREGATE product (DOUBLE PRECISION) (
        SFUNC    = float8mul,
        STYPE    = DOUBLE PRECISION,
        INITCOND = '1'
    )
    {
        COMMENT 'Multiplicative aggregate over DOUBLE PRECISION values';
        GRANTS  { EXECUTE TO app_service; }
    }
}
```

```sql
-- emits
CREATE OR REPLACE AGGREGATE "public"."product" (DOUBLE PRECISION) (
    SFUNC    = float8mul,
    STYPE    = DOUBLE PRECISION,
    INITCOND = '1'
);

COMMENT ON AGGREGATE "public"."product" (DOUBLE PRECISION) IS 'Multiplicative aggregate over DOUBLE PRECISION values';
GRANT EXECUTE ON AGGREGATE "public"."product" (DOUBLE PRECISION) TO "app_service";
```

## Ordered-set aggregate

```sql
AGGREGATE percentile_disc (DOUBLE PRECISION ORDER BY anyelement) (
    SFUNC           = ordered_set_transition,
    STYPE           = internal,
    FINALFUNC       = percentile_disc_final,
    FINALFUNC_EXTRA
)
{
    COMMENT 'Ordered-set percentile aggregate';
}
```

```sql
CREATE OR REPLACE AGGREGATE "public"."percentile_disc" (DOUBLE PRECISION ORDER BY anyelement) (
    SFUNC           = ordered_set_transition,
    STYPE           = internal,
    FINALFUNC       = percentile_disc_final,
    FINALFUNC_EXTRA
);

COMMENT ON AGGREGATE "public"."percentile_disc" (DOUBLE PRECISION ORDER BY anyelement) IS 'Ordered-set percentile aggregate';
```

## Aggregate with combine and serial functions (parallel-safe)

```sql
AGGREGATE sum_parallel (NUMERIC) (
    SFUNC       = numeric_add,
    STYPE       = NUMERIC,
    INITCOND    = '0',
    COMBINEFUNC = numeric_add,
    SERIALFUNC  = numeric_serialize,
    DESERIALFUNC = numeric_deserialize,
    PARALLEL    = SAFE
);
```

```sql
CREATE OR REPLACE AGGREGATE "public"."sum_parallel" (NUMERIC) (
    SFUNC        = numeric_add,
    STYPE        = NUMERIC,
    INITCOND     = '0',
    COMBINEFUNC  = numeric_add,
    SERIALFUNC   = numeric_serialize,
    DESERIALFUNC = numeric_deserialize,
    PARALLEL     = SAFE
);
```

## Diffing behaviour

Aggregate identity is `(schema, name, input_types)`. Changes to `SFUNC`, `STYPE`, `INITCOND`, `FINALFUNC`, `COMBINEFUNC`, or `SERIALFUNC` require `DROP AGGREGATE IF EXISTS` + `CREATE AGGREGATE` — `DESTRUCTIVE`.
