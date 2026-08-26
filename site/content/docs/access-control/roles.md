---
title: "Roles"
description: "Cluster-level ROLE declarations with login, password, connection limits, and membership."
weight: 1
---

Roles are cluster-level objects. Declare them in `.dpg` files inside the cluster objects directory (e.g. `production/cluster/roles.dpg`). See [Project Structure](../../fundamentals/project-structure/).

## Read-only role (no login)

```sql
ROLE app_readonly {
    NOLOGIN;
    COMMENT 'Read-only access for reporting tools';
}
```

```sql
-- emits
CREATE ROLE "app_readonly" NOLOGIN;
COMMENT ON ROLE "app_readonly" IS 'Read-only access for reporting tools';
```

## Login role with password

```sql
ROLE app_service {
    LOGIN;
    PASSWORD 'env:APP_SERVICE_PW';
    CONNECTION LIMIT 20;
    VALID UNTIL '2030-01-01';
}
```

```sql
-- emits
CREATE ROLE "app_service"
    LOGIN
    PASSWORD 'env:APP_SERVICE_PW'
    CONNECTION LIMIT 20
    VALID UNTIL '2030-01-01';
```

Hardcoded passwords are rejected by the linter. Passwords must use `env:VAR_NAME` — the value is resolved from the environment at connection time. See [Linting](../../migrations/linting/).

## Role with privilege flags and membership

```sql
ROLE app_admin {
    LOGIN;
    SUPERUSER  false;
    CREATEDB   false;
    CREATEROLE false;
    INHERIT;
    IN ROLE pg_read_all_stats;
}
```

```sql
-- emits
CREATE ROLE "app_admin"
    LOGIN
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    INHERIT
    IN ROLE "pg_read_all_stats";
```

## Role attribute reference

| Attribute | PostgreSQL option |
|-----------|------------------|
| `LOGIN` / `NOLOGIN` | `LOGIN` / `NOLOGIN` |
| `SUPERUSER true/false` | `SUPERUSER` / `NOSUPERUSER` |
| `CREATEDB true/false` | `CREATEDB` / `NOCREATEDB` |
| `CREATEROLE true/false` | `CREATEROLE` / `NOCREATEROLE` |
| `INHERIT` / `NOINHERIT` | `INHERIT` / `NOINHERIT` |
| `CONNECTION LIMIT n` | `CONNECTION LIMIT n` |
| `VALID UNTIL 'date'` | `VALID UNTIL 'date'` |
| `PASSWORD 'env:VAR'` | `PASSWORD 'value'` (resolved at runtime) |
| `IN ROLE role` | `IN ROLE role` |
| `COMMENT 'text'` | `COMMENT ON ROLE name IS '...'` |

## Diffing behaviour

- New role: `CREATE ROLE`.
- Changed attribute: `ALTER ROLE`.
- Removed role: `DROP ROLE` — `DESTRUCTIVE`.
