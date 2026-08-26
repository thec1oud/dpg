---
title: "Overview"
description: "The table syntax model: Part 1 column list, Part 2 block, inline vs. table-level constraints, and table modifiers."
weight: 1
---

Tables follow the [two-part syntax](../../../fundamentals/two-part-syntax/) exactly: the `( )` column list is Part 1 (pure PostgreSQL `CREATE TABLE` syntax); the `{ }` block is Part 2 (indexes, policies, triggers, grants, column attributes, lifecycle directives).

## Minimal table

```sql
TABLE users (
    id    BIGINT GENERATED ALWAYS AS IDENTITY,
    email TEXT NOT NULL,
    CONSTRAINT pk_users       PRIMARY KEY (id),
    CONSTRAINT uq_users_email UNIQUE (email)
);
```

```sql
-- emits
CREATE TABLE "public"."users" (
    "id"    bigint GENERATED ALWAYS AS IDENTITY,
    "email" text NOT NULL,
    CONSTRAINT "pk_users"       PRIMARY KEY ("id"),
    CONSTRAINT "uq_users_email" UNIQUE ("email")
);
```

## Full table with all sub-objects

```sql
TABLE users (
    id         BIGINT GENERATED ALWAYS AS IDENTITY,
    email      TEXT NOT NULL,
    status     user_status NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pk_users       PRIMARY KEY (id),
    CONSTRAINT uq_users_email UNIQUE (email)
)
{
    COMMENT 'Primary identity store';
    OWNER   "app_role";

    COLUMN email {
        COMMENT    "Verified email address";
        STATISTICS 300;
        GRANTS     { SELECT TO reporting_role; }
    }

    INDICES {
        idx_email  (email);
        idx_status (status) WHERE (status != 'deleted');
    }

    ENABLE ROW LEVEL SECURITY;

    POLICIES {
        view_self FOR SELECT USING (id = auth.uid());
    }

    TRIGGERS {
        after_email_change AFTER UPDATE OF email
            FOR EACH ROW
            WHEN (OLD.email IS DISTINCT FROM NEW.email)
            EXECUTE FUNCTION notify_email_change();
    }

    GRANTS {
        SELECT, INSERT, UPDATE TO app_service;
        SELECT                 TO app_readonly;
    }

    REVOCATIONS {
        ALL PRIVILEGES FROM PUBLIC;
    }
}
```

```sql
-- emits
CREATE TABLE "public"."users" (
    "id"         bigint GENERATED ALWAYS AS IDENTITY,
    "email"      text NOT NULL,
    "status"     user_status NOT NULL DEFAULT 'active',
    "created_at" timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT "pk_users"       PRIMARY KEY ("id"),
    CONSTRAINT "uq_users_email" UNIQUE ("email")
);

COMMENT ON TABLE  "public"."users"       IS 'Primary identity store';
COMMENT ON COLUMN "public"."users"."email" IS 'Verified email address';
ALTER TABLE "public"."users" OWNER TO "app_role";
ALTER TABLE "public"."users" ALTER COLUMN "email" SET STATISTICS 300;
ALTER TABLE "public"."users" ENABLE ROW LEVEL SECURITY;

CREATE POLICY "view_self" ON "public"."users"
    FOR SELECT USING (id = auth.uid());

CREATE TRIGGER "after_email_change"
    AFTER UPDATE OF "email" ON "public"."users"
    FOR EACH ROW
    WHEN (OLD.email IS DISTINCT FROM NEW.email)
    EXECUTE FUNCTION notify_email_change();

GRANT SELECT (email) ON TABLE "public"."users" TO "reporting_role";
GRANT SELECT, INSERT, UPDATE ON TABLE "public"."users" TO "app_service";
GRANT SELECT ON TABLE "public"."users" TO "app_readonly";
REVOKE ALL PRIVILEGES ON TABLE "public"."users" FROM PUBLIC;

CREATE INDEX "idx_email"  ON "public"."users" ("email");
CREATE INDEX "idx_status" ON "public"."users" ("status") WHERE (status != 'deleted');
```

Both indexes are declared alongside the table's own creation, so they're emitted inside the same transaction, non-concurrently — PostgreSQL rejects `CREATE INDEX CONCURRENTLY` inside a transaction block.

## Inline vs. table-level constraints

Both forms are accepted. The compiler always emits single-column `PRIMARY KEY`, `UNIQUE`, `REFERENCES`, and `CHECK` constraints inline in the generated DDL.

```sql
-- inline (preferred for single-column)
TABLE accounts (
    id   UUID    NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    slug TEXT    NOT NULL UNIQUE,
    org  UUID    NOT NULL REFERENCES organisations (id) ON DELETE CASCADE
);

-- named inline
TABLE accounts (
    id UUID NOT NULL DEFAULT gen_random_uuid() CONSTRAINT pk_accounts PRIMARY KEY
);

-- table-level (required for multi-column)
TABLE order_items (
    order_id   BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    CONSTRAINT pk_order_items PRIMARY KEY (order_id, product_id)
);
```

## Table modifiers

PG SQL clause modifiers appear after `)` in PG SQL's own ordering — they stay in Part 1.

```sql
TABLE large_events (
    id      BIGINT GENERATED ALWAYS AS IDENTITY,
    payload JSONB,
    CONSTRAINT pk_large_events PRIMARY KEY (id)
) WITH (fillfactor = 70);
```

```sql
-- emits
CREATE TABLE "public"."large_events" (
    "id"      bigint GENERATED ALWAYS AS IDENTITY,
    "payload" jsonb,
    CONSTRAINT "pk_large_events" PRIMARY KEY ("id")
) WITH (fillfactor = 70);
```

## UNLOGGED table

```sql
UNLOGGED TABLE session_cache (
    key   TEXT,
    value JSONB,
    CONSTRAINT pk_session_cache PRIMARY KEY (key)
);
```

```sql
-- emits
CREATE UNLOGGED TABLE "public"."session_cache" (
    "key"   text,
    "value" jsonb,
    CONSTRAINT "pk_session_cache" PRIMARY KEY ("key")
);
```

## INHERITS

```sql
TABLE user_audit_log (
    user_id BIGINT NOT NULL,
    action  TEXT   NOT NULL
) INHERITS (audit_log);
```

```sql
-- emits
CREATE TABLE "public"."user_audit_log" (
    "user_id" bigint NOT NULL,
    "action"  text NOT NULL
) INHERITS ("audit_log");
```

## PRIMARY KEY implies NOT NULL

PostgreSQL enforces `NOT NULL` on all `PRIMARY KEY` columns. DPG applies the same inference — writing `NOT NULL` on a PK column is accepted but redundant. The compiler does not emit a redundant `NOT NULL` clause and does not emit a spurious `ALTER COLUMN SET NOT NULL` when diffing.

> Sub-object details: [Columns](../columns/) · [Constraints](../constraints/) · [Indexes](../indexes/) · [Row Level Security](../row-level-security/) · [Triggers](../triggers/) · [Partitioning](../partitioning/)
