---
title: "Materialized Views"
description: "Materialized views; any query change emits `DROP MATERIALIZED VIEW` then `CREATE MATERIALIZED VIEW`."
weight: 2
---

## Basic materialized view

```sql
SCHEMA analytics {
    MATERIALIZED VIEW daily_revenue AS
        SELECT
            date_trunc('day', created_at) AS day,
            SUM(total_amount)             AS revenue,
            COUNT(*)                      AS order_count
        FROM orders
        WHERE status = 'completed'
        GROUP BY 1;
}
```

```sql
-- emits
CREATE MATERIALIZED VIEW "analytics"."daily_revenue" AS
    SELECT
        date_trunc('day', created_at) AS day,
        SUM(total_amount)             AS revenue,
        COUNT(*)                      AS order_count
    FROM orders
    WHERE status = 'completed'
    GROUP BY 1;
```

## With storage options and `WITH NO DATA`

```sql
MATERIALIZED VIEW product_stats
WITH (fillfactor = 90)
TABLESPACE analytics_space AS
    SELECT product_id, COUNT(*) AS purchases, AVG(price) AS avg_price
    FROM order_items
    GROUP BY product_id
WITH NO DATA {
    INDEXES { idx_product_stats_id (product_id); }
    GRANTS  { SELECT TO app_readonly; }
}
```

```sql
-- emits
CREATE MATERIALIZED VIEW "public"."product_stats"
    WITH (fillfactor = 90)
    TABLESPACE "analytics_space" AS
    SELECT product_id, COUNT(*) AS purchases, AVG(price) AS avg_price
    FROM order_items
    GROUP BY product_id
WITH NO DATA;

GRANT SELECT ON "public"."product_stats" TO "app_readonly";

CREATE INDEX IF NOT EXISTS "idx_product_stats_id"
    ON "public"."product_stats" ("product_id");
```

## Diffing behaviour

Any change to a materialized view's query (beyond whitespace, which is normalized away before comparison) emits `DROP MATERIALIZED VIEW` then `CREATE MATERIALIZED VIEW` — always `DESTRUCTIVE`. Unlike regular views, there is no `CREATE OR REPLACE MATERIALIZED VIEW`.
