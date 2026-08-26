---
title: "Foreign Data"
description: "FOREIGN DATA WRAPPER, SERVER, USER MAPPING, and FOREIGN TABLE declarations."
weight: 8
---

## FOREIGN DATA WRAPPER

In the common case, FDWs are installed via extension (`EXTENSION postgres_fdw;`). The explicit declaration is reserved for custom C-implemented FDWs. Place in the cluster objects directory.

```sql
FOREIGN DATA WRAPPER myfdw
    HANDLER   myfdw_handler
    VALIDATOR myfdw_validator;
```

```sql
-- emits
CREATE FOREIGN DATA WRAPPER myfdw
    HANDLER   myfdw_handler
    VALIDATOR myfdw_validator;
```

---

## SERVER

Servers are database-level objects that reference a foreign data wrapper.

```sql
SERVER analytics_warehouse
    FOREIGN DATA WRAPPER postgres_fdw
    OPTIONS (host 'warehouse.internal', dbname 'analytics', port '5432');
```

```sql
-- emits
CREATE SERVER "analytics_warehouse"
    FOREIGN DATA WRAPPER postgres_fdw
    OPTIONS (host 'warehouse.internal', dbname 'analytics', port '5432');
```

---

## USER MAPPING

```sql
USER MAPPING FOR app_service
    SERVER analytics_warehouse
    OPTIONS (user 'fdw_user', password '{{vault:secret/fdw/analytics#password}}');
```

```sql
-- emits
CREATE USER MAPPING FOR "app_service"
    SERVER "analytics_warehouse"
    OPTIONS (user 'fdw_user', password '<resolved-at-apply-time>');
```

---

## FOREIGN TABLE

`SERVER` and `OPTIONS` are PG SQL clauses on the `CREATE FOREIGN TABLE` signature — they stay in Part 1 per the [two-part syntax](../../fundamentals/two-part-syntax/) rules.

```sql
FOREIGN TABLE remote_events (
    id         BIGINT,
    payload    JSONB,
    created_at TIMESTAMPTZ
) SERVER log_server OPTIONS (table_name 'events', schema_name 'public')
{
    COLUMN id { COMMENT 'Remote event primary key'; }
    GRANTS { SELECT TO app_readonly; }
}
```

```sql
-- emits
CREATE FOREIGN TABLE "public"."remote_events" (
    "id"         bigint,
    "payload"    jsonb,
    "created_at" timestamptz
) SERVER "log_server" OPTIONS (table_name 'events', schema_name 'public');

COMMENT ON COLUMN "public"."remote_events"."id" IS 'Remote event primary key';
GRANT SELECT ON "public"."remote_events" TO "app_readonly";
```

## Diffing behaviour

| Object | Change | Safety |
|--------|--------|--------|
| FDW | HANDLER/VALIDATOR/OPTIONS changed | `DESTRUCTIVE` (drop + recreate) |
| FDW | Owner changed | `SAFE` |
| FDW | Renamed | `CAUTION` |
| FDW | Grants/revocations changed | `SAFE` |
| FDW | Removed | `DESTRUCTIVE` |
| Server | OPTIONS changed | `SAFE` |
| Server | VERSION changed only | `SAFE` |
| Server | Owner changed | `SAFE` |
| Server | Renamed | `CAUTION` |
| Server | FDW changed | `DESTRUCTIVE` (drop + recreate) |
| Server | Removed | `DESTRUCTIVE` |
| User mapping | OPTIONS changed | `DESTRUCTIVE` then `SAFE` (drop + recreate) |
| User mapping | Removed | `DESTRUCTIVE` |
| Foreign table | Column changes | `ALTER FOREIGN TABLE` |
