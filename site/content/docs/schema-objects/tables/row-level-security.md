---
title: "Row Level Security"
description: "ENABLE/FORCE RLS and policy declarations with FOR clause, USING, and WITH CHECK."
weight: 5
---

## Enable RLS

```sql
TABLE orders ( ... )
{
    ENABLE ROW LEVEL SECURITY;
}
```

```sql
-- emits
ALTER TABLE "public"."orders" ENABLE ROW LEVEL SECURITY;
```

## Force RLS (applies to table owner too)

```sql
TABLE orders ( ... )
{
    ENABLE ROW LEVEL SECURITY;
    FORCE  ROW LEVEL SECURITY;
}
```

```sql
-- emits
ALTER TABLE "public"."orders" ENABLE ROW LEVEL SECURITY;
ALTER TABLE "public"."orders" FORCE  ROW LEVEL SECURITY;
```

## SELECT policy

```sql
TABLE orders ( ... )
{
    ENABLE ROW LEVEL SECURITY;
    POLICIES {
        view_own FOR SELECT USING (user_id = auth.uid());
    }
}
```

```sql
-- emits
ALTER TABLE "public"."orders" ENABLE ROW LEVEL SECURITY;
CREATE POLICY "view_own" ON "public"."orders"
    FOR SELECT USING (user_id = auth.uid());
```

## INSERT policy

```sql
POLICIES {
    insert_own FOR INSERT WITH CHECK (user_id = auth.uid());
}
```

```sql
CREATE POLICY "insert_own" ON "public"."orders"
    FOR INSERT WITH CHECK (user_id = auth.uid());
```

## UPDATE policy (USING + WITH CHECK)

```sql
POLICIES {
    update_own FOR UPDATE
        USING     (user_id = auth.uid())
        WITH CHECK (user_id = auth.uid() AND status != 'locked');
}
```

```sql
CREATE POLICY "update_own" ON "public"."orders"
    FOR UPDATE
    USING     (user_id = auth.uid())
    WITH CHECK (user_id = auth.uid() AND status != 'locked');
```

## RESTRICTIVE policy

```sql
POLICIES {
    restrict_deleted AS RESTRICTIVE FOR ALL USING (deleted_at IS NULL);
}
```

```sql
CREATE POLICY "restrict_deleted" ON "public"."orders"
    AS RESTRICTIVE FOR ALL USING (deleted_at IS NULL);
```

## Policy scoped to specific roles

```sql
POLICIES {
    admin_all FOR ALL
        TO admin_role
        USING (true);

    service_read FOR SELECT
        TO service_role, readonly_role
        USING (true);
}
```

```sql
CREATE POLICY "admin_all" ON "public"."orders"
    FOR ALL TO "admin_role" USING (true);

CREATE POLICY "service_read" ON "public"."orders"
    FOR SELECT TO "service_role", "readonly_role" USING (true);
```

## Policy change behaviour

- Adding a policy: `CREATE POLICY` — `SAFE`.
- Changing a policy's `TO`, `USING`, or `WITH CHECK`: `ALTER POLICY` — `SAFE`.
- Changing a policy's `FOR` command or `AS PERMISSIVE`/`RESTRICTIVE`: `DROP POLICY` + `CREATE POLICY` — `CAUTION`.
- Removing a permissive policy: `DROP POLICY` — `SAFE` (it only narrows access).
- Removing a restrictive policy: `DROP POLICY` — `CAUTION` (it widens access).
- Enabling/disabling RLS: `ALTER TABLE ENABLE/DISABLE ROW LEVEL SECURITY` — `CAUTION`.
