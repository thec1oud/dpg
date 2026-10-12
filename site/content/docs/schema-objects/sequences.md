---
title: "Sequences"
description: "SEQUENCE declarations with AS, START, INCREMENT, MINVALUE, MAXVALUE, CACHE, CYCLE, and OWNED BY."
weight: 6
---

Sequences are schema-level objects. Do not declare sequences that back `GENERATED AS IDENTITY` or `SERIAL` columns — PostgreSQL manages those automatically.

## Basic sequence

```sql
SCHEMA public {
    SEQUENCE order_number_seq;
}
```

```sql
-- emits
CREATE SEQUENCE "public"."order_number_seq";
```

## Sequence with all options

```sql
SCHEMA public {
    SEQUENCE order_number_seq
        AS BIGINT
        START WITH  10000
        INCREMENT BY 1
        MINVALUE     10000
        MAXVALUE     99999999
        CACHE        50
        NO CYCLE
        OWNED BY orders.order_number;
}
```

```sql
-- emits
CREATE SEQUENCE "public"."order_number_seq"
    AS bigint
    START WITH  10000
    INCREMENT BY 1
    MINVALUE     10000
    MAXVALUE     99999999
    CACHE        50
    NO CYCLE
    OWNED BY "public"."orders"."order_number";
```

## Altering sequence options

Changing any option emits `ALTER SEQUENCE`:

```sql
-- change CACHE from 50 to 100
SEQUENCE order_number_seq
    AS BIGINT
    START WITH 10000
    CACHE      100
    OWNED BY orders.order_number;
```

```sql
-- emits
ALTER SEQUENCE "public"."order_number_seq" CACHE 100;
```

## Renaming or moving a sequence

```sql
SEQUENCE archive.invoice_no { RENAMED FROM public.order_no; }
```

```sql
-- emits
ALTER SEQUENCE "public"."order_no" SET SCHEMA "archive";
ALTER SEQUENCE "archive"."order_no" RENAME TO "invoice_no";
```

The sequence keeps its current value.

## Removing a sequence

Removing a sequence from source emits `DROP SEQUENCE` — always `DESTRUCTIVE`.
