---
title: "Composite"
description: "Row-shaped composite type declared with `TYPE name AS (...)`."
weight: 2
---

## Basic composite type

```sql
SCHEMA public {
    TYPE address AS (
        street      TEXT,
        city        TEXT,
        state       CHAR(2),
        postal_code TEXT,
        country     CHAR(2)
    );
}
```

```sql
-- emits
CREATE TYPE "public"."address" AS (
    "street"      text,
    "city"        text,
    "state"       char(2),
    "postal_code" text,
    "country"     char(2)
);
```

## Adding an attribute

Adding an attribute emits `ALTER TYPE ... ADD ATTRIBUTE` — `SAFE`.

```sql
-- before
TYPE contact AS (name TEXT, email TEXT);

-- after (add phone)
TYPE contact AS (name TEXT, email TEXT, phone TEXT);
```

```sql
-- emits (SAFE)
ALTER TYPE "public"."contact" ADD ATTRIBUTE "phone" text;
```

## Attribute comments and renames

Each attribute can carry its own `{ }` block, written with PostgreSQL's own `ATTRIBUTE` keyword. It accepts `COMMENT` and `RENAMED FROM`.

```sql
TYPE address AS (
    street_line TEXT,
    city        TEXT
) {
    ATTRIBUTE street_line {
        RENAMED FROM street;
        COMMENT 'First line of the street address';
    }
}
```

```sql
-- emits
ALTER TYPE "public"."address" RENAME ATTRIBUTE "street" TO "street_line";
COMMENT ON COLUMN "public"."address"."street_line" IS 'First line of the street address';
```

Several attributes can be grouped in a plural block:

```sql
TYPE address AS (street TEXT, city TEXT) {
    ATTRIBUTES {
        street { COMMENT 'Street and number'; }
        city   { COMMENT 'City name'; }
    }
}
```

## Changes and safety

| Change | DDL emitted | Safety |
|--------|-------------|--------|
| Attribute added | `ALTER TYPE name ADD ATTRIBUTE attr type` | `SAFE` |
| Attribute removed | `ALTER TYPE name DROP ATTRIBUTE attr` | `DESTRUCTIVE` |
| Attribute type or `COLLATE` changed | `ALTER TYPE name ALTER ATTRIBUTE attr TYPE type` | `DESTRUCTIVE` |
| Attribute renamed (`RENAMED FROM`) | `ALTER TYPE name RENAME ATTRIBUTE old TO new` | `SAFE` |
| Attribute comment changed | `COMMENT ON COLUMN name.attr IS '...'` | `SAFE` |
