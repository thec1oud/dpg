---
title: "ENUM"
description: "Enumerated type with safe value addition via ALTER TYPE and safe value removal via MIGRATE REMOVE."
weight: 1
---

## Basic ENUM

```sql
ENUM user_status ('active', 'suspended', 'deleted');
```

```sql
-- emits (new type)
CREATE TYPE "public"."user_status" AS ENUM ('active', 'suspended', 'deleted');
```

## ENUM with comment

```sql
ENUM invoice_status ('draft', 'sent', 'paid', 'void', 'overdue');
{
    COMMENT "Billing lifecycle states for customer invoices";
}
```

```sql
-- emits
CREATE TYPE "public"."invoice_status" AS ENUM ('draft', 'sent', 'paid', 'void', 'overdue');
COMMENT ON TYPE "public"."invoice_status" IS 'Billing lifecycle states for customer invoices';
```

## Adding a value

Add a value to the end of the list (or use `BEFORE`/`AFTER` positioning in source — the compiler preserves order).

```sql
-- before
ENUM order_status ('pending', 'confirmed', 'shipped', 'delivered');

-- after (add 'refunded')
ENUM order_status ('pending', 'confirmed', 'shipped', 'delivered', 'refunded');
```

```sql
-- emits
ALTER TYPE "public"."order_status" ADD VALUE 'refunded';
```

## Renaming a value

```sql
ENUM order_status ('pending', 'confirmed', 'shipped', 'delivered' RENAMED FROM 'fulfilled');
```

```sql
-- emits
ALTER TYPE "public"."order_status" RENAME VALUE 'fulfilled' TO 'delivered';
```

## Removing a value — `MIGRATE REMOVE`

PostgreSQL has no `ALTER TYPE DROP VALUE`. DPG handles removal through a safe six-step migration.

```sql
ENUM order_status ('pending', 'confirmed', 'shipped', 'delivered');
{
    MIGRATE REMOVE ('cancelled') {
        UPDATE orders SET status = 'delivered' WHERE status = 'cancelled';
    }
}
```

```sql
-- emits (six-step migration)

-- 1. Create a new type with the reduced value set
CREATE TYPE "public"."order_status__dpg_new" AS ENUM ('pending', 'confirmed', 'shipped', 'delivered');

-- 2. Run the migration DML (inside transaction)
UPDATE orders SET status = 'delivered' WHERE status = 'cancelled';

-- 3. Verify no rows still carry removed values — aborts with error if any remain

-- 4. Retype every column that used the old type
ALTER TABLE "public"."orders"
    ALTER COLUMN "status" TYPE "public"."order_status__dpg_new"
    USING "status"::text::"public"."order_status__dpg_new";

-- 5. Drop the old type
DROP TYPE "public"."order_status";

-- 6. Rename the new type
ALTER TYPE "public"."order_status__dpg_new" RENAME TO "order_status";

-- On failure: DROP TYPE IF EXISTS "public"."order_status__dpg_new"
```

After the migration succeeds, remove the `MIGRATE REMOVE` block from source. If left in place, the next `dpg apply` will error because the values no longer exist.

Multiple values can be removed in one block:

```sql
MIGRATE REMOVE ('cancelled', 'refunded') {
    UPDATE orders SET status = 'delivered' WHERE status IN ('cancelled', 'refunded');
}
```
