---
title: "Triggers"
description: "Row, statement, CONSTRAINT, and transition-table triggers declared in the TRIGGERS block."
weight: 6
---

Triggers are declared in the `TRIGGERS { }` block inside a table's `{ }`.

## BEFORE INSERT row trigger

```sql
TABLE users ( ... )
{
    TRIGGERS {
        before_insert BEFORE INSERT
            FOR EACH ROW
            EXECUTE FUNCTION set_defaults();
    }
}
```

```sql
-- emits
CREATE TRIGGER "before_insert"
    BEFORE INSERT ON "public"."users"
    FOR EACH ROW
    EXECUTE FUNCTION set_defaults();
```

## AFTER UPDATE OF specific columns

```sql
TRIGGERS {
    after_email_change AFTER UPDATE OF email
        FOR EACH ROW
        WHEN (OLD.email IS DISTINCT FROM NEW.email)
        EXECUTE FUNCTION notify_email_change();
}
```

```sql
CREATE TRIGGER "after_email_change"
    AFTER UPDATE OF "email" ON "public"."users"
    FOR EACH ROW
    WHEN (OLD.email IS DISTINCT FROM NEW.email)
    EXECUTE FUNCTION notify_email_change();
```

## Statement-level trigger with transition tables

```sql
TRIGGERS {
    audit_changes AFTER INSERT OR UPDATE OR DELETE
        REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
        FOR EACH STATEMENT
        EXECUTE FUNCTION audit_table_changes();
}
```

```sql
CREATE TRIGGER "audit_changes"
    AFTER INSERT OR UPDATE OR DELETE ON "public"."users"
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION audit_table_changes();
```

## CONSTRAINT trigger

```sql
TRIGGERS {
    check_ref CONSTRAINT AFTER INSERT OR UPDATE
        FROM orders
        DEFERRABLE INITIALLY DEFERRED
        FOR EACH ROW
        EXECUTE FUNCTION check_ref_integrity();
}
```

```sql
CREATE CONSTRAINT TRIGGER "check_ref"
    AFTER INSERT OR UPDATE ON "public"."users"
    FROM "public"."orders"
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION check_ref_integrity();
```

## Trigger change behaviour

- Adding a trigger: `CREATE TRIGGER` — `SAFE`.
- Changing any trigger property: `DROP TRIGGER` + `CREATE TRIGGER` — `CAUTION`.
- Removing a trigger: `DROP TRIGGER` — `CAUTION` (whatever the trigger enforced stops).
