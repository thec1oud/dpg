---
title: "Name Maps"
description: "Attach tool-specific naming conventions to DPG objects and columns for ORM generators, type-safe query builders, and other downstream consumers."
weight: 5
---

Name Maps let you annotate DPG schema objects with the naming convention each downstream tool should apply to them. ORM generators (Prisma, Drizzle), type-safe query builders (sqlc), and API code generators can read these annotations from the snapshot and produce identifiers in the right casing for their target language — without touching the DPG source.

Name maps are **metadata only**: they do not affect generated PostgreSQL DDL and produce no SQL migration steps. They are serialised into the snapshot JSON for downstream consumers.

## Rule keywords

DPG defines ten canonical naming-convention rules:

| Rule keyword | Example (`user_profile_id`) |
|---|---|
| `LOWER_SNAKE_CASE` | `user_profile_id` |
| `UPPER_SNAKE_CASE` | `USER_PROFILE_ID` |
| `LOWER_CAMEL_CASE` | `userProfileId` |
| `UPPER_CAMEL_CASE` | `UserProfileId` |
| `LOWER_KEBAB_CASE` | `user-profile-id` |
| `UPPER_KEBAB_CASE` | `USER-PROFILE-ID` |
| `TRAIN_CASE` | `User-Profile-Id` |
| `LOWER_CASE` | `userprofileid` |
| `UPPER_CASE` | `USERPROFILEID` |
| `PASCAL_SNAKE_CASE` | `User_Profile_Id` |

Rules are unquoted keywords in `.dpg` files and case-insensitive in `dpg.toml`.

You can also supply a **literal target name** by writing it in double quotes (`"MySpecialName"`). Literals are passed through verbatim — no transformation is applied.

The reserved key `default` acts as a catch-all that applies when a tool has no more specific rule.

## Configuration layer (`dpg.toml`)

All three config files (root, cluster, database) accept a `[namemaps]` section. Scalar string values are **global rules** keyed by tool name. Subtables (`[namemaps.<type>]`) are **per-object-type rules**.

```toml
# root dpg.toml — project-wide defaults
[namemaps]
default = "LOWER_SNAKE_CASE"   # catch-all for any tool
prisma  = "LOWER_CAMEL_CASE"

[namemaps.table]
prisma  = "UPPER_CAMEL_CASE"   # Prisma model names → PascalCase
drizzle = "LOWER_CAMEL_CASE"

[namemaps.column]
prisma  = "LOWER_CAMEL_CASE"
drizzle = "LOWER_CAMEL_CASE"
sqlc    = "LOWER_SNAKE_CASE"
```

A deeper config scope overrides a shallower one for the same (tool, object-type) pair:

```toml
# production/myapp/dpg.toml — override just one rule for this database
[namemaps]
sqlc = "LOWER_SNAKE_CASE"
```

Only rule keywords are accepted in config files. Literal names are not supported there.

## Block layer (inline directives)

Name map directives appear inside any `{ }` block — object-level or column-level. Two forms are available.

### Singular form

```sql
NAME MAP TO <rule> ;              -- applies to the "default" tool key
NAME MAP <tool> TO <rule> ;       -- applies to a named tool
NAME MAP <tool> TO "LiteralName"; -- literal target name for a tool
```

### Grouped form

```sql
NAME MAPS {
  <tool> TO <rule> ;
  <tool> TO "LiteralName" ;
}
```

The grouped form requires explicit tool names; the implicit `default` shorthand is not available inside `NAME MAPS { }`.

You can mix singular directives and `NAME MAPS` blocks freely in the same `{ }` block.

### Examples

```sql
TABLE users (
  id       BIGINT GENERATED ALWAYS AS IDENTITY,
  email    TEXT NOT NULL,
  username TEXT NOT NULL
) {
  NAME MAPS {
    default TO LOWER_SNAKE_CASE;
    prisma  TO LOWER_CAMEL_CASE;
    drizzle TO LOWER_CAMEL_CASE;
  }

  COLUMNS {
    username {
      -- sqlc gets a literal name override
      NAME MAP sqlc TO "UserName";

      -- Prisma also gets a specific rule
      NAME MAP prisma TO LOWER_CAMEL_CASE;
    }
  }
}
```

```sql
TYPE user_status AS ENUM ('active', 'inactive', 'banned') {
  NAME MAP TO LOWER_SNAKE_CASE;
  NAME MAP prisma TO UPPER_CAMEL_CASE;
}
```

## Resolution order

For a given (tool, object-type) pair, the most specific source wins:

```
block directive > database dpg.toml > cluster dpg.toml > root dpg.toml
```

For example, if root config sets `prisma = "LOWER_CAMEL_CASE"` globally, a database config `[namemaps.table] prisma = "UPPER_CAMEL_CASE"` overrides it for tables in that database. A `NAME MAP prisma TO "CustomModel"` on a specific table overrides that further.

## Snapshot representation

Each `name_maps` entry in the snapshot JSON has exactly one of `rule` (for rule keywords) or `name` (for literal names):

```json
{
  "public.users": {
    "kind": "table",
    "table": {
      "schema": "public",
      "name": "users",
      "name_maps": [
        { "tool": "default", "rule": "LOWER_SNAKE_CASE" },
        { "tool": "prisma",  "rule": "LOWER_CAMEL_CASE" }
      ],
      "columns": [
        {
          "name": "username",
          "type": "text",
          "name_maps": [
            { "tool": "sqlc",   "name": "UserName" },
            { "tool": "prisma", "rule": "LOWER_CAMEL_CASE" }
          ]
        }
      ]
    }
  }
}
```

The `name_maps` field is omitted when empty. All object types carry this field: tables, columns, views, functions, types, schemas, extensions, sequences, roles, virtual types, and opaque objects.
