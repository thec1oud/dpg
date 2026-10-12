---
title: "Roles"
description: "Cluster-level ROLE declarations with login, password, connection limits, and membership."
weight: 1
---

Roles are cluster-level objects. Declare them in `.dpg` files inside the cluster objects directory (e.g. `production/cluster/roles.dpg`). See [Project Structure](../../fundamentals/project-structure/).

A role declaration is PostgreSQL's `CREATE ROLE` without the `CREATE`: attributes such as `LOGIN`, `PASSWORD` and `CONNECTION LIMIT` go before the `{ }` block, exactly where PostgreSQL puts them. The block holds only what `CREATE ROLE` can't say: a comment, session settings, membership options, and renames.

## Read-only role (no login)

```sql
ROLE app_readonly NOLOGIN {
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
ROLE app_service
    LOGIN
    PASSWORD '{{env:APP_SERVICE_PW}}'
    CONNECTION LIMIT 20
    VALID UNTIL '2030-01-01' {
    SET statement_timeout = '30s';
}
```

```sql
-- emits
CREATE ROLE "app_service" LOGIN CONNECTION LIMIT 20 PASSWORD '<resolved>' VALID UNTIL '2030-01-01';
ALTER ROLE "app_service" SET statement_timeout = '30s';
```

`{{env:APP_SERVICE_PW}}` is a secret placeholder. It is resolved immediately before the statement runs, never during `plan`, so plans and snapshots never contain the password itself. Other backends use the same form, e.g. `'{{vault:secret/roles/app_service#pw}}'`. A password with no placeholder is rejected by the linter (rule `hardcoded-role-password`, see [Linting](../../migrations/linting/)).

## Role with privilege flags and membership

```sql
ROLE app_admin
    LOGIN
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    INHERIT
    IN ROLE pg_read_all_stats {
    ROLE app_readonly;
}
```

```sql
-- emits
CREATE ROLE "app_admin" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT IN ROLE "pg_read_all_stats" ROLE "app_readonly";
```

`IN ROLE r` makes this role a member of `r`; `ROLE r` makes `r` a member of this role; `ADMIN r` does the same with the admin option. They can be written before the block, as in `CREATE ROLE`, or inside it, where they also take PostgreSQL 16's `WITH ADMIN`, `WITH INHERIT` and `WITH SET` options:

```sql
ROLE app_admin LOGIN {
    ROLE app_readonly WITH INHERIT FALSE;
}
```

## Reference

Before the block (PostgreSQL's `CREATE ROLE` options):

| Option | Notes |
|--------|-------|
| `LOGIN` / `NOLOGIN` | |
| `SUPERUSER` / `NOSUPERUSER` | |
| `CREATEDB` / `NOCREATEDB` | |
| `CREATEROLE` / `NOCREATEROLE` | |
| `INHERIT` / `NOINHERIT` | |
| `REPLICATION` / `NOREPLICATION` | |
| `BYPASSRLS` / `NOBYPASSRLS` | |
| `CONNECTION LIMIT n` | |
| `PASSWORD '...'` | Use a `{{secret-uri}}` placeholder. |
| `VALID UNTIL 'timestamp'` | |
| `IN ROLE r`, `ROLE r`, `ADMIN r` | Membership. |

Inside the block:

| Directive | Emits |
|-----------|-------|
| `COMMENT 'text';` | `COMMENT ON ROLE name IS 'text'` |
| `IN ROLE r` / `ROLE r` / `ADMIN r`, optionally followed by `WITH ADMIN OPTION`, `WITH INHERIT bool`, `WITH SET bool` | `GRANT ... TO ... [WITH ...]` |
| `SET param = value [IN DATABASE db];` | `ALTER ROLE name [IN DATABASE db] SET param = value` |
| `SET param FROM CURRENT;` / `RESET param;` / `RESET ALL;` | `ALTER ROLE name SET ... FROM CURRENT` / `RESET ...` |
| `RENAMED FROM old_name;` | `ALTER ROLE old_name RENAME TO name` |
| `SECURITY LABEL [FOR provider] 'label';` | `SECURITY LABEL ON ROLE ...` |

Options you don't write are left as they are; DPG never resets them to PostgreSQL's defaults.

## Diffing behaviour

- New role: `CREATE ROLE`.
- Changed option: one `ALTER ROLE` with every changed option.
- Changed password (the declared text, including the placeholder): `ALTER ROLE ... PASSWORD` — `CAUTION`.
- Membership removed: `REVOKE` — `CAUTION`.
- Renamed with `RENAMED FROM`: `ALTER ROLE ... RENAME TO` — `CAUTION`.
- Role removed from the source: `DROP ROLE` — `DESTRUCTIVE`.

`dpg plan --live` compares against the live catalog instead of the snapshot. Two things differ there:

- Passwords are not compared, because PostgreSQL shows them only to superusers.
- Roles you don't declare are left alone. Roles are shared by every database on the cluster, so a role created by another application, or the role DPG connects as, is never dropped. System roles (`pg_*`) and the bootstrap superuser are not read at all.
