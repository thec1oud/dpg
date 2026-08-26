---
title: "Schemas & Extensions"
description: "`SCHEMA` and `EXTENSION` declarations."
weight: 1
---

## SCHEMA

Schemas have no `( )` list. Their `{ }` block holds attributes and nested objects directly.

### Empty schema

```sql
SCHEMA public { }
```

```sql
-- emits
CREATE SCHEMA IF NOT EXISTS "public";
```

### Schema with owner and comment

```sql
SCHEMA analytics {
    OWNER   "analytics_role";
    COMMENT 'Derived tables and event aggregations';
}
```

```sql
-- emits
CREATE SCHEMA IF NOT EXISTS "analytics";
ALTER SCHEMA "analytics" OWNER TO "analytics_role";
COMMENT ON SCHEMA "analytics" IS 'Derived tables and event aggregations';
```

### Schema with grants

```sql
SCHEMA analytics {
    GRANTS {
        USAGE TO app_readonly;
        ALL   TO analytics_admin;
    }
}
```

```sql
-- emits
CREATE SCHEMA IF NOT EXISTS "analytics";
GRANT USAGE ON SCHEMA "analytics" TO "app_readonly";
GRANT ALL ON SCHEMA "analytics" TO "analytics_admin";
```

### Schema rename

```sql
SCHEMA reporting {
    RENAMED FROM old_reporting;
}
```

```sql
-- emits
ALTER SCHEMA "old_reporting" RENAME TO "reporting";
```

> See [Lifecycle Directives](../../migrations/lifecycle/) for `RENAMED FROM` behaviour across schema, table, and column renames.

### Nested objects

Objects declared inside a schema `{ }` block inherit the schema as their namespace.

```sql
SCHEMA analytics {
    TABLE events (
        id         BIGINT GENERATED ALWAYS AS IDENTITY,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
        CONSTRAINT pk_events PRIMARY KEY (id)
    )
    { INDICES { idx_ts (created_at); } }
}
```

```sql
-- emits
CREATE SCHEMA IF NOT EXISTS "analytics";

CREATE TABLE "analytics"."events" (
    "id"         bigint GENERATED ALWAYS AS IDENTITY,
    "created_at" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "pk_events" PRIMARY KEY ("id")
);

CREATE INDEX "idx_ts" ON "analytics"."events" ("created_at");
```

---

## EXTENSION

Extensions follow the no-verb mandate: `EXTENSION name` with optional PG SQL clauses.

### Simple extension

```sql
EXTENSION pgcrypto;
```

```sql
-- emits
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
```

### Extension with schema and version

```sql
EXTENSION postgis SCHEMA public VERSION '3.3';
```

```sql
-- emits
CREATE EXTENSION IF NOT EXISTS "postgis" SCHEMA "public" VERSION '3.3';
```

### Extension with CASCADE

```sql
EXTENSION pg_trgm CASCADE;
```

```sql
-- emits
CREATE EXTENSION IF NOT EXISTS "pg_trgm" CASCADE;
```
