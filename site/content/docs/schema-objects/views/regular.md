---
title: "Regular Views"
description: "View declarations; query changes emit `CREATE OR REPLACE VIEW`; output column list changes emit `DROP VIEW CASCADE`."
weight: 1
---

The `AS query` clause is Part 1, terminated with `;`. An optional `{ }` block holds grants, comments, and the owner.

## Basic view

```sql
SCHEMA public {
    VIEW active_users AS
        SELECT id, email, created_at
        FROM users
        WHERE status = 'active' AND deleted_at IS NULL;
}
```

```sql
-- emits
CREATE OR REPLACE VIEW "public"."active_users" AS
    SELECT id, email, created_at
    FROM users
    WHERE status = 'active' AND deleted_at IS NULL;
```

## View with explicit column aliases

```sql
VIEW user_summary (user_id, email, order_count) AS
    SELECT u.id, u.email, COUNT(o.id)
    FROM users u
    LEFT JOIN orders o ON o.user_id = u.id
    GROUP BY u.id, u.email;
```

```sql
CREATE OR REPLACE VIEW "public"."user_summary" ("user_id", "email", "order_count") AS
    SELECT u.id, u.email, COUNT(o.id)
    FROM users u
    LEFT JOIN orders o ON o.user_id = u.id
    GROUP BY u.id, u.email;
```

## View with `security_barrier`

```sql
VIEW secure_user_view WITH (security_barrier = true) AS
    SELECT id, email FROM users WHERE tenant_id = current_tenant();
```

```sql
CREATE OR REPLACE VIEW "public"."secure_user_view"
    WITH (security_barrier = true) AS
    SELECT id, email FROM users WHERE tenant_id = current_tenant();
```

## View with `WITH CHECK OPTION`

```sql
VIEW active_orders AS
    SELECT * FROM orders WHERE status != 'cancelled'
    WITH LOCAL CHECK OPTION;
```

```sql
CREATE OR REPLACE VIEW "public"."active_orders" AS
    SELECT * FROM orders WHERE status != 'cancelled'
    WITH LOCAL CHECK OPTION;
```

## View with grants and comment

```sql
VIEW admin_summary AS
    SELECT id, email, created_at FROM users WHERE role = 'admin' {
    COMMENT 'Admin user summary';
    GRANTS  { SELECT TO app_readonly; }
}
```

```sql
CREATE OR REPLACE VIEW "public"."admin_summary" AS
    SELECT id, email, created_at FROM users WHERE role = 'admin';

COMMENT ON VIEW "public"."admin_summary" IS 'Admin user summary';
GRANT SELECT ON "public"."admin_summary" TO "app_readonly";
```

## Diffing behaviour

| Change | SQL emitted | Safety |
|--------|-------------|--------|
| Query body changes; output column list unchanged | `CREATE OR REPLACE VIEW` | `SAFE` |
| Output column list changes in any way | `DROP VIEW CASCADE` then `CREATE VIEW` | `DESTRUCTIVE` |
