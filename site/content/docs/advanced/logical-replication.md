---
title: "Logical Replication"
description: "PUBLICATION and SUBSCRIPTION declarations."
weight: 2
---

## PUBLICATION — specific tables

```sql
PUBLICATION user_data
    FOR TABLE users, profiles
    WITH (publish = 'insert, update, delete') {
    COMMENT 'Primary replication stream for user data';
}
```

```sql
-- emits
CREATE PUBLICATION "user_data"
    FOR TABLE "public"."users", "public"."profiles"
    WITH (publish = 'insert, update, delete');

COMMENT ON PUBLICATION "user_data" IS 'Primary replication stream for user data';
```

## PUBLICATION — all tables

```sql
PUBLICATION all_tables FOR ALL TABLES;
```

```sql
-- emits
CREATE PUBLICATION "all_tables" FOR ALL TABLES;
```

## PUBLICATION — filtered (column list + row filter)

```sql
PUBLICATION filtered_orders
    FOR TABLE orders (id, customer_id, status, total)
    WHERE (status != 'draft');
```

```sql
-- emits
CREATE PUBLICATION "filtered_orders"
    FOR TABLE "public"."orders" ("id", "customer_id", "status", "total")
    WHERE (status != 'draft');
```

## SUBSCRIPTION

```sql
SUBSCRIPTION replica_users
    CONNECTION 'host=primary.db.internal dbname=myapp user=replicator'
    PUBLICATION user_data
    WITH (enabled = true, copy_data = true);
```

```sql
-- emits
CREATE SUBSCRIPTION "replica_users"
    CONNECTION 'host=primary.db.internal dbname=myapp user=replicator'
    PUBLICATION "user_data"
    WITH (enabled = true, copy_data = true);
```

## Diffing behaviour

| Change | SQL | Safety |
|--------|-----|--------|
| Add table to publication | `ALTER PUBLICATION ADD TABLE` | `SAFE` |
| Remove table from publication | `ALTER PUBLICATION DROP TABLE` | `SAFE` |
| Change `WITH` options | `ALTER PUBLICATION SET` | `SAFE` |
| Publication owner changed | `ALTER PUBLICATION OWNER TO` | `SAFE` |
| Publication renamed (`RENAMED FROM`) | `ALTER PUBLICATION RENAME TO` | `CAUTION` |
| Remove publication | `DROP PUBLICATION` | `DESTRUCTIVE` |
| Change subscription connection/publication | `DROP SUBSCRIPTION` + `CREATE SUBSCRIPTION` | `DESTRUCTIVE` |
| Subscription owner changed | `ALTER SUBSCRIPTION OWNER TO` | `SAFE` |
| Subscription renamed (`RENAMED FROM`) | `ALTER SUBSCRIPTION RENAME TO` | `CAUTION` |
| Remove subscription | `DROP SUBSCRIPTION` | `DESTRUCTIVE` |
