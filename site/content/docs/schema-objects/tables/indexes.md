---
title: "Indexes"
description: "All index methods, partial indexes, expression indexes, covering indexes, and CONCURRENTLY behaviour."
weight: 4
---

Indexes are declared in the `INDEXES { }` block inside a table's `{ }`. By default, DPG emits plain `CREATE INDEX` for additions on existing tables. Write `CONCURRENTLY` on an individual index to make it `CREATE INDEX CONCURRENTLY` instead — emitted as a non-transactional step after `COMMIT`.

`UNIQUE` and `CONCURRENTLY` are both prefix keywords, written before the index name — mirroring real PostgreSQL's own `CREATE UNIQUE INDEX CONCURRENTLY name ON table` order exactly. `CONCURRENTLY` is a bare presence keyword, same as in real PostgreSQL: there is no `CONCURRENTLY false` and no project-wide setting that changes the default — writing the keyword is the only way an index is ever created concurrently.

## Standard btree index

```sql
TABLE users ( email TEXT NOT NULL, ... )
{
    INDEXES { idx_users_email (email); }
}
```

```sql
-- emits (transactional)
CREATE INDEX IF NOT EXISTS "idx_users_email"
    ON "public"."users" ("email");
```

## Unique index

```sql
{ INDEXES { UNIQUE idx_unique_slug (slug); } }
```

```sql
CREATE UNIQUE INDEX IF NOT EXISTS "idx_unique_slug"
    ON "public"."users" ("slug");
```

## Composite index with sort order

```sql
{ INDEXES { idx_tenant_created (tenant_id ASC, created_at DESC); } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_tenant_created"
    ON "public"."events" ("tenant_id" ASC, "created_at" DESC);
```

## Partial index

```sql
{ INDEXES { idx_active_users (email) WHERE (status = 'active'); } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_active_users"
    ON "public"."users" ("email") WHERE (status = 'active');
```

## Expression index

```sql
{ INDEXES { idx_lower_email (lower(email)); } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_lower_email"
    ON "public"."users" (lower("email"));
```

## Covering index (INCLUDE)

```sql
{ INDEXES { idx_covering (user_id) INCLUDE (email, created_at); } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_covering"
    ON "public"."users" ("user_id") INCLUDE ("email", "created_at");
```

## GIN index

```sql
{ INDEXES {
    idx_tags USING gin (tags);
    idx_fts  USING gin (search_vec);
} }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_tags"
    ON "public"."posts" USING gin ("tags");

CREATE INDEX IF NOT EXISTS "idx_fts"
    ON "public"."posts" USING gin ("search_vec");
```

## GiST index

```sql
{ INDEXES { idx_location USING gist (location); } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_location"
    ON "public"."places" USING gist ("location");
```

## BRIN index with storage parameter

```sql
{ INDEXES { idx_brin USING brin (created_at) WITH (pages_per_range = 128); } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_brin"
    ON "public"."events" USING brin ("created_at") WITH (pages_per_range = 128);
```

## Index with tablespace

```sql
{ INDEXES { idx_archived (archived_at) TABLESPACE archive_space; } }
```

```sql
CREATE INDEX IF NOT EXISTS "idx_archived"
    ON "public"."records" ("archived_at") TABLESPACE "archive_space";
```

## Concurrent index creation

Write `CONCURRENTLY` to avoid locking the table during index creation on a large, live table:

```sql
{ INDEXES { CONCURRENTLY idx_email (email); } }
```

```sql
-- emits (non-transactional, after COMMIT)
CREATE INDEX CONCURRENTLY IF NOT EXISTS "idx_email" ON "public"."users" ("email");
```

This has no effect on an index declared alongside its own brand-new table — PostgreSQL rejects `CREATE INDEX CONCURRENTLY` inside a transaction block, and a new table's indexes are always emitted transactionally with it, so the compiler forces them non-concurrent regardless of this keyword, and the linter warns (`concurrently-new-table`).

## Index removal

Removing an index from the `INDEXES` block emits `DROP INDEX` — classified as `CAUTION` (acquires `ACCESS EXCLUSIVE` lock; no data loss, but blocks concurrent reads during the drop).

```sql
-- emits
DROP INDEX IF EXISTS "public"."idx_old_index";
```
