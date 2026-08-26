---
title: "Tablespaces"
description: "TABLESPACE declarations — cluster-level objects that map a name to a filesystem location."
weight: 9
---

Tablespaces are cluster-level objects. Declare them in the cluster objects directory (e.g. `production/cluster/tablespaces.dpg`). See [Project Structure](../../fundamentals/project-structure/).

## Basic tablespace

```sql
TABLESPACE fast_ssd LOCATION '/mnt/nvme/pg_data';
TABLESPACE archive  LOCATION '/mnt/hdd/pg_archive';
```

```sql
-- emits
CREATE TABLESPACE "fast_ssd" LOCATION '/mnt/nvme/pg_data';
CREATE TABLESPACE "archive"  LOCATION '/mnt/hdd/pg_archive';
```

## Tablespace with owner

`OWNER` sits inline in the native `CREATE TABLESPACE` clause, matching real PostgreSQL's own grammar exactly — it is not a `{ }` block directive here (unlike Table/View/etc., where `OWNER` has no place in the native `CREATE` statement).

```sql
TABLESPACE fast_ssd OWNER "pg_admin" LOCATION '/mnt/nvme/pg_data';
```

```sql
-- emits
CREATE TABLESPACE "fast_ssd" OWNER "pg_admin" LOCATION '/mnt/nvme/pg_data';
```

A later change to the declared `OWNER` emits `ALTER TABLESPACE name OWNER TO role`.

## Using a tablespace

Reference the tablespace name in a table, index, or materialized view declaration:

```sql
TABLE events ( ... ) TABLESPACE fast_ssd { ... }
```

```sql
{ INDICES { idx_events_ts (created_at) TABLESPACE archive; } }
```

See [Tables Overview](../../schema-objects/tables/overview/) and [Indexes](../../schema-objects/tables/indexes/).

## Diffing behaviour

| Change | SQL | Safety |
|--------|-----|--------|
| New tablespace | `CREATE TABLESPACE` | `SAFE` |
| Owner changed | `ALTER TABLESPACE OWNER TO` | `SAFE` |
| Renamed (`RENAMED FROM`) | `ALTER TABLESPACE RENAME TO` | `CAUTION` |
| `WITH (...)` option changed | `ALTER TABLESPACE SET (...)`/`RESET (...)` | `CAUTION` |
| `LOCATION` changed | `DROP TABLESPACE` + `CREATE TABLESPACE` | `DESTRUCTIVE` |
| Removed tablespace | `DROP TABLESPACE` | `DESTRUCTIVE` |

`LOCATION` cannot be changed after creation — any location change requires the full drop-and-recreate path. Dropping a non-empty tablespace fails at the PostgreSQL level; the compiler additionally emits a warning comment noting this.
