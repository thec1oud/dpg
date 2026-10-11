---
title: "Grants"
description: "GRANTS and REVOKES blocks — the additive model: DPG only emits GRANT, never auto-revokes."
weight: 2
---

DPG follows an additive grants model identical to raw PostgreSQL: declaring a grant emits `GRANT`. Removing a `GRANTS` entry from source emits **nothing**. To revoke, add an explicit `REVOKES { }` entry.

## Table grants

```sql
TABLE orders ( ... )
{
    GRANTS {
        SELECT                 TO app_readonly;
        SELECT, INSERT, UPDATE TO app_service;
        ALL PRIVILEGES         TO app_admin;
    }
}
```

```sql
-- emits
GRANT SELECT ON TABLE "public"."orders" TO "app_readonly";
GRANT SELECT, INSERT, UPDATE ON TABLE "public"."orders" TO "app_service";
GRANT ALL PRIVILEGES ON TABLE "public"."orders" TO "app_admin";
```

## Revocations

```sql
TABLE orders ( ... )
{
    REVOKES {
        ALL PRIVILEGES FROM PUBLIC;
    }
}
```

```sql
-- emits
REVOKE ALL PRIVILEGES ON TABLE "public"."orders" FROM PUBLIC;
```

## Column-level grants

Declared inside a `COLUMN name { }` block. The column scope is inferred.

```sql
TABLE users ( email TEXT, ssn TEXT, ... )
{
    COLUMN email {
        GRANTS {
            SELECT TO reporting_role;
            SELECT TO analytics_role;
        }
    }

    COLUMN ssn {
        GRANTS     { SELECT TO compliance_role; }
        REVOKES { ALL PRIVILEGES FROM PUBLIC; }
    }
}
```

```sql
-- emits
GRANT SELECT ("email") ON TABLE "public"."users" TO "reporting_role";
GRANT SELECT ("email") ON TABLE "public"."users" TO "analytics_role";
GRANT SELECT ("ssn")   ON TABLE "public"."users" TO "compliance_role";
REVOKE ALL PRIVILEGES ("ssn") ON TABLE "public"."users" FROM PUBLIC;
```

## Schema grants

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
GRANT USAGE ON SCHEMA "analytics" TO "app_readonly";
GRANT ALL ON SCHEMA "analytics" TO "analytics_admin";
```

## Function grants

```sql
FUNCTION calculate_total(p_id UUID) RETURNS NUMERIC
LANGUAGE plpgsql STABLE
AS $$ ... $$ {
    GRANTS { EXECUTE TO app_service; }
}
```

```sql
-- emits
GRANT EXECUTE ON FUNCTION "public"."calculate_total"(uuid) TO "app_service";
```

## Sequence grants

```sql
SEQUENCE order_number_seq ... {
    GRANTS { USAGE TO app_service; }
}
```

```sql
-- emits
GRANT USAGE ON SEQUENCE "public"."order_number_seq" TO "app_service";
```

## Multiple grantees in one entry

```sql
GRANTS {
    EXECUTE TO app_readonly, app_service;
}
```

```sql
GRANT EXECUTE ON FUNCTION "public"."..." TO "app_readonly", "app_service";
```

## Drift detection behaviour

`dpg verify` reports as drift any DPG-declared grant absent from the live catalog. It does not report extra grants present in the live catalog but absent from source.
