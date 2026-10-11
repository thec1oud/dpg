---
title: "Lifecycle Directives"
description: "RENAMED FROM, DEPRECATED, PROTECTED, DROP CASCADE, and MIGRATE REMOVE — DPG-specific annotations that drive safe multi-step migrations."
weight: 1
---

Lifecycle directives live inside `{ }` blocks. They communicate intent to the compiler for operations that cannot be expressed as a simple `CREATE` or `ALTER`.

---

## RENAMED FROM — object renames

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

### Table rename

```sql
TABLE user_accounts ( ... )
{
    RENAMED FROM users;
}
```

```sql
-- emits
ALTER TABLE "public"."users" RENAME TO "user_accounts";
```

### Column rename

The new name goes in both the `( )` list and the `COLUMN` block. The old name appears only in `RENAMED FROM`.

```sql
TABLE users (
    email_address TEXT NOT NULL,
    CONSTRAINT uq_users_email UNIQUE (email_address)   -- use new name
)
{
    COLUMN email_address {
        RENAMED FROM email;
    }
}
```

```sql
-- emits
ALTER TABLE "public"."users" RENAME COLUMN "email" TO "email_address";
```

**Compiler algorithm:**
1. Sees `email_address` in `( )` and `RENAMED FROM email` in the `COLUMN` block.
2. Looks up `email` in the snapshot. If `email` exists and `email_address` does not → rename.
3. Emits `ALTER TABLE RENAME COLUMN`.
4. Validates all index and constraint references use the new name — errors on mismatch.
5. If `email` does not exist in the snapshot → compiler error. Remove `RENAMED FROM` for genuinely new columns.

**After the migration:** remove `RENAMED FROM` from source. It applies only to the single migration cycle where the rename occurs.

---

## DEPRECATED

Marks an object as deprecated. The linter warns at compile time. The compiler stores the message as a `COMMENT ON` prefixed with `[DEPRECATED]`.

```sql
TABLE legacy_sessions ( ... )
{
    DEPRECATED "Use the sessions table instead. Remove after 2026-01-01.";
}
```

```sql
-- emits
COMMENT ON TABLE "public"."legacy_sessions"
    IS '[DEPRECATED] Use the sessions table instead. Remove after 2026-01-01.';
```

```sql
TABLE users ( ... )
{
    COLUMN old_username {
        DEPRECATED "Use email field instead.";
    }
}
```

```sql
-- emits
COMMENT ON COLUMN "public"."users"."old_username"
    IS '[DEPRECATED] Use email field instead.';
```

**Applies to:** tables, columns, views, functions.

---

## PROTECTED

Prevents the compiler from emitting `DROP` for this object even if it is removed from source.

```sql
TABLE audit_trail ( ... )
{
    PROTECTED;
}
```

```sql
-- emits nothing on removal
-- (compiler errors if audit_trail is removed from source while PROTECTED is set)
```

**Applies to:** tables. To drop a protected table, remove `PROTECTED` first and apply, then remove the table declaration and apply again.

---

## DROP CASCADE

Overrides the global `default_drop_behavior` for a specific object. When the object is removed from source, `DROP ... CASCADE` is emitted instead of `DROP ... RESTRICT`.

```sql
TABLE scratch_data ( ... )
{
    DROP CASCADE;
}
```

```sql
-- emits when scratch_data is removed from source (DESTRUCTIVE)
DROP TABLE "public"."scratch_data" CASCADE;
```

---

## MIGRATE REMOVE — ENUM value removal

PostgreSQL has no `ALTER TYPE DROP VALUE`. DPG triggers a safe six-step migration. See [ENUM](../../schema-objects/types/enum/) for the complete procedure and output.

```sql
TYPE order_status AS ENUM ('pending', 'confirmed', 'shipped', 'delivered') {
    MIGRATE REMOVE ('cancelled') {
        UPDATE orders SET status = 'delivered' WHERE status = 'cancelled';
    }
}
```

**After the migration:** remove the `MIGRATE REMOVE` block from source.

---

## NOT VALID — deferred constraint validation

`NOT VALID` is a PostgreSQL option tracked across migrations. See [Constraints](../../schema-objects/tables/constraints/) for the full DPG→SQL pair.

```sql
-- Migration 1: add without scanning rows
TABLE orders ( ... )
{ CONSTRAINT ck_amount_positive CHECK (amount > 0) NOT VALID; }
```

```sql
-- emits
ALTER TABLE "public"."orders"
    ADD CONSTRAINT "ck_amount_positive" CHECK (amount > 0) NOT VALID;
```

```sql
-- Migration 2: validate (remove NOT VALID)
TABLE orders ( ... )
{ CONSTRAINT ck_amount_positive CHECK (amount > 0); }
```

```sql
-- emits
ALTER TABLE "public"."orders" VALIDATE CONSTRAINT "ck_amount_positive";
```
