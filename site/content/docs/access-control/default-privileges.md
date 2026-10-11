---
title: "Default Privileges"
description: "`DEFAULT PRIVILEGES FOR ROLE` — automatically grant privileges on future objects created by a role."
weight: 3
---

Default privileges apply to objects created in the future by a specific role. They are declared inside a `SCHEMA { }` block.

## Default privileges for tables and functions

```sql
SCHEMA public {
    DEFAULT PRIVILEGES FOR ROLE app_admin {
        GRANTS {
            SELECT   ON TABLES    TO app_readonly;
            EXECUTE  ON FUNCTIONS TO app_service;
            USAGE    ON SEQUENCES TO app_service;
        }
    }
}
```

```sql
-- emits
ALTER DEFAULT PRIVILEGES FOR ROLE "app_admin" IN SCHEMA "public"
    GRANT SELECT ON TABLES TO "app_readonly";

ALTER DEFAULT PRIVILEGES FOR ROLE "app_admin" IN SCHEMA "public"
    GRANT EXECUTE ON FUNCTIONS TO "app_service";

ALTER DEFAULT PRIVILEGES FOR ROLE "app_admin" IN SCHEMA "public"
    GRANT USAGE ON SEQUENCES TO "app_service";
```

## Supported object types in `ON` clause

| Keyword | Applies to |
|---------|-----------|
| `TABLES` | `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `REFERENCES`, `TRIGGER` |
| `SEQUENCES` | `USAGE`, `SELECT`, `UPDATE` |
| `FUNCTIONS` | `EXECUTE` |
| `TYPES` | `USAGE` |
| `SCHEMAS` | `USAGE`, `CREATE` |

## Revoking default privileges

```sql
SCHEMA public {
    DEFAULT PRIVILEGES FOR ROLE app_admin {
        REVOKES {
            EXECUTE ON FUNCTIONS FROM PUBLIC;
        }
    }
}
```

```sql
-- emits
ALTER DEFAULT PRIVILEGES FOR ROLE "app_admin" IN SCHEMA "public"
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
```
