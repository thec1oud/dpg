---
title: "Statistics Objects"
description: "Extended statistics objects for multi-column correlation tracking."
weight: 7
---

Extended statistics objects give the PostgreSQL planner information about correlations between columns that single-column statistics cannot capture.

## Basic statistics object

```sql
SCHEMA public {
    STATISTICS orders_stats (dependencies, ndistinct, mcv)
        ON customer_id, created_at
        FROM orders;
}
```

```sql
-- emits
CREATE STATISTICS "public"."orders_stats" (dependencies, ndistinct, mcv)
    ON "customer_id", "created_at"
    FROM "public"."orders";
```

## Statistics kinds reference

| Kind | Description |
|------|-------------|
| `dependencies` | Functional dependency between columns |
| `ndistinct` | Multi-column distinct value count |
| `mcv` | Most common value combinations |

## Altering statistics target

```sql
SCHEMA public {
    STATISTICS orders_stats (dependencies, ndistinct, mcv)
        ON customer_id, created_at
        FROM orders {
        STATISTICS_TARGET 200;
    }
}
```

```sql
-- emits
ALTER STATISTICS "public"."orders_stats" SET STATISTICS 200;
```

## Diffing behaviour

| Change | SQL | Safety |
|--------|-----|--------|
| Add statistics object | `CREATE STATISTICS` | `SAFE` |
| Change column list or kinds | `DROP STATISTICS` + `CREATE STATISTICS` | `DESTRUCTIVE` |
| Change `statistics_target` | `ALTER STATISTICS SET STATISTICS` | `SAFE` |
| Remove statistics object | `DROP STATISTICS` | `SAFE` |
