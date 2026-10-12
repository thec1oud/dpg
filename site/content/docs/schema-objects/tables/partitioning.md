---
title: "Partitioning"
description: "RANGE, LIST, and HASH partitioned tables with sub-partitions and partition management."
weight: 7
---

## RANGE partitioned table

```sql
TABLE events (
    id         BIGINT GENERATED ALWAYS AS IDENTITY,
    tenant_id  UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at)
{
    PARTITIONS {
        events_2024_q1 FOR VALUES FROM ('2024-01-01') TO ('2024-04-01');
        events_2024_q2 FOR VALUES FROM ('2024-04-01') TO ('2024-07-01');
        events_2024_q3 FOR VALUES FROM ('2024-07-01') TO ('2024-10-01');
        events_2024_q4 FOR VALUES FROM ('2024-10-01') TO ('2025-01-01');
        events_default DEFAULT;
    }
}
```

```sql
-- emits
CREATE TABLE "public"."events" (
    "id"         bigint GENERATED ALWAYS AS IDENTITY,
    "tenant_id"  uuid NOT NULL,
    "event_type" text NOT NULL,
    "payload"    jsonb,
    "created_at" timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE ("created_at");

CREATE TABLE "public"."events_2024_q1"
    PARTITION OF "public"."events"
    FOR VALUES FROM ('2024-01-01') TO ('2024-04-01');

CREATE TABLE "public"."events_2024_q2"
    PARTITION OF "public"."events"
    FOR VALUES FROM ('2024-04-01') TO ('2024-07-01');

CREATE TABLE "public"."events_2024_q3"
    PARTITION OF "public"."events"
    FOR VALUES FROM ('2024-07-01') TO ('2024-10-01');

CREATE TABLE "public"."events_2024_q4"
    PARTITION OF "public"."events"
    FOR VALUES FROM ('2024-10-01') TO ('2025-01-01');

CREATE TABLE "public"."events_default"
    PARTITION OF "public"."events" DEFAULT;
```

## LIST partitioned table

```sql
TABLE orders_by_region (
    id     BIGINT GENERATED ALWAYS AS IDENTITY,
    region TEXT NOT NULL
) PARTITION BY LIST (region)
{
    PARTITIONS {
        orders_north FOR VALUES IN ('NYC', 'BOS', 'PHI');
        orders_south FOR VALUES IN ('MIA', 'ATL', 'DAL');
    }
}
```

```sql
-- emits
CREATE TABLE "public"."orders_by_region" (
    "id"     bigint GENERATED ALWAYS AS IDENTITY,
    "region" text NOT NULL
) PARTITION BY LIST ("region");

CREATE TABLE "public"."orders_north"
    PARTITION OF "public"."orders_by_region"
    FOR VALUES IN ('NYC', 'BOS', 'PHI');

CREATE TABLE "public"."orders_south"
    PARTITION OF "public"."orders_by_region"
    FOR VALUES IN ('MIA', 'ATL', 'DAL');
```

## HASH partitioned table

```sql
TABLE sessions (
    id      UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id BIGINT NOT NULL
) PARTITION BY HASH (user_id)
{
    PARTITIONS {
        sessions_0 FOR VALUES WITH (MODULUS 4, REMAINDER 0);
        sessions_1 FOR VALUES WITH (MODULUS 4, REMAINDER 1);
        sessions_2 FOR VALUES WITH (MODULUS 4, REMAINDER 2);
        sessions_3 FOR VALUES WITH (MODULUS 4, REMAINDER 3);
    }
}
```

```sql
-- emits
CREATE TABLE "public"."sessions" (
    "id"      uuid NOT NULL DEFAULT gen_random_uuid(),
    "user_id" bigint NOT NULL
) PARTITION BY HASH ("user_id");

CREATE TABLE "public"."sessions_0"
    PARTITION OF "public"."sessions"
    FOR VALUES WITH (MODULUS 4, REMAINDER 0);

-- ... (sessions_1, sessions_2, sessions_3 follow same pattern)
```

## Sub-partitioning

```sql
TABLE events ( ... ) PARTITION BY RANGE (created_at)
{
    PARTITIONS {
        events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')
            PARTITION BY LIST (region) {
                PARTITIONS {
                    events_2024_us FOR VALUES IN ('us-east', 'us-west');
                    events_2024_eu FOR VALUES IN ('eu-west', 'eu-central');
                }
            };
    }
}
```

```sql
-- emits
CREATE TABLE "public"."events" ( ... ) PARTITION BY RANGE ("created_at");

CREATE TABLE "public"."events_2024"
    PARTITION OF "public"."events"
    FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')
    PARTITION BY LIST ("region");

CREATE TABLE "public"."events_2024_us"
    PARTITION OF "public"."events_2024"
    FOR VALUES IN ('us-east', 'us-west');

CREATE TABLE "public"."events_2024_eu"
    PARTITION OF "public"."events_2024"
    FOR VALUES IN ('eu-west', 'eu-central');
```

## Partition-local constraints (PostgreSQL 18+)

A partition can declare its own constraints, independent of the parent, by
adding a trailing `{ }` body to its `PARTITIONS { }` entry:

```sql
TABLE orders (
    id     BIGINT GENERATED ALWAYS AS IDENTITY,
    amount NUMERIC(10,2) NOT NULL
) PARTITION BY RANGE (id)
{
    PARTITIONS {
        orders_1 FOR VALUES FROM (1) TO (1000) {
            CONSTRAINT ck_amount CHECK (amount > 0);
        };
    }
}
```

```sql
-- emits (orders_1 already exists)
ALTER TABLE "public"."orders_1"
    ADD CONSTRAINT "ck_amount" CHECK (amount > 0);
```

This is the same mechanism that represents PostgreSQL 18's
`ALTER TABLE ONLY parent DROP CONSTRAINT`: removing a constraint from the
parent's own declaration while a partition keeps declaring it locally emits
that statement instead of an ordinary drop, detaching the constraint from the
parent while leaving it enforced (now local, no longer inherited) on the
partition:

```sql
-- before: ck_amount declared on the parent, inherited by orders_1
TABLE orders (
    id     BIGINT GENERATED ALWAYS AS IDENTITY,
    amount NUMERIC(10,2) NOT NULL,
    CONSTRAINT ck_amount CHECK (amount > 0)
) PARTITION BY RANGE (id) { PARTITIONS { orders_1 FOR VALUES FROM (1) TO (1000); } }

-- after: dropped from the parent, kept locally on orders_1
TABLE orders (
    id     BIGINT GENERATED ALWAYS AS IDENTITY,
    amount NUMERIC(10,2) NOT NULL
) PARTITION BY RANGE (id)
{
    PARTITIONS {
        orders_1 FOR VALUES FROM (1) TO (1000) {
            CONSTRAINT ck_amount CHECK (amount > 0);
        };
    }
}
```

```sql
-- emits
ALTER TABLE ONLY "public"."orders" DROP CONSTRAINT "ck_amount";
```

`ALTER TABLE ONLY ... DROP CONSTRAINT` on a partitioned table requires
PostgreSQL 18 or newer — PostgreSQL 17 rejects it while any partition exists.
A constraint added directly to a partition with no such history has no
version requirement; it's an ordinary `ADD CONSTRAINT` on any PostgreSQL
version.

## Partition management

| Operation | Safety | Notes |
|-----------|--------|-------|
| Add a partition | `SAFE` | `CREATE TABLE ... PARTITION OF` |
| Remove a partition | `DESTRUCTIVE` | `DROP TABLE partition_name` |
| Change partition strategy | `DESTRUCTIVE` (instruction-only) | Requires `--approve-partition-rebuild`; full table rebuild |
| Drop a constraint from the parent, keep it on existing partitions (PG18+) | `SAFE` | `ALTER TABLE ONLY parent DROP CONSTRAINT name` |
| Add/remove a partition's own local constraint | `CAUTION`/`DESTRUCTIVE` | `ALTER TABLE partition ADD/DROP CONSTRAINT name` |

Indexes and grants declared in the parent table's `{ }` block apply to all partitions.
