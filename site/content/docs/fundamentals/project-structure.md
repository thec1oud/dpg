---
title: "Project Structure"
description: "Directory layout, cluster and database configuration, and dpg.toml reference."
weight: 2
---

A DPG project maps one-to-one onto the topology of a PostgreSQL deployment. The directory structure encodes clusters and databases; `.dpg` files are scoped to their containing database automatically.

## Directory layout

```
myproject/
├── dpg.toml                          # root config
│
├── production/                       # cluster directory
│   ├── dpg.toml                      # cluster config (connection URL, options)
│   ├── cluster/                      # cluster-level objects (roles, tablespaces)
│   │   ├── roles.dpg
│   │   └── tablespaces.dpg
│   │
│   └── myapp/                        # database directory
│       ├── dpg.toml                  # database config (name, default schema)
│       ├── extensions.dpg
│       └── schemas/
│           └── public/
│               ├── types.dpg
│               ├── tables/
│               │   ├── users.dpg
│               │   └── orders.dpg
│               ├── views.dpg
│               └── functions.dpg
│
└── .dpg/
    ├── snapshots/
    │   └── production/
    │       └── myapp.json            # committed snapshot — source of truth for diffing
    └── migrations/
        └── production/
            └── myapp/
                └── 20260101T120000Z_a3f7c91.sql  # archived migration SQL (committed)
```

**Discovery rules:**
- Clusters = immediate subdirectories of the root with a `dpg.toml`.
- Databases = immediate subdirectories of a cluster directory with a `dpg.toml`, excluding the cluster objects directory.
- All `.dpg` files beneath a database directory compile as one unit.

## Root `dpg.toml`

```toml
[compiler]
default_drop_behavior = "restrict"   # restrict | cascade

[linter]
warn_on_deprecated            = true
require_column_comments       = false
forbid_hardcoded_passwords    = true
max_columns_per_table         = 50   # 0 = disabled

[snapshots]
directory = ".dpg/snapshots"

[migrations]
# Where to archive applied migration SQL files.
# Set to "" to disable archiving. Default: ".dpg/migrations".
directory = ".dpg/migrations"

[fmt]
indent        = 4        # spaces per indentation level
keyword_case  = "upper"  # "upper" (default) or "lower"
```

### `.env` file loading

When a command resolves a `link = "env:VAR_NAME"` connection URI, DPG loads environment variables from a `.env` file before resolving:

1. The path passed to `--env <path>` (if given), **or**
2. `<project-root>/.env`, if it exists.

Variables already present in the process environment are never overwritten. A missing `.env` file is not an error. The `.env` format supports `KEY=value` lines; lines starting with `#` are comments.

## Cluster `dpg.toml`

```toml
# production/dpg.toml
[cluster]
name                = "production"
cluster_objects_dir = "cluster"      # reserved name; no database may share it

# Inline connection string (mutually exclusive with link):
url = "postgresql://pguser@primary.prod.internal:5432/postgres"

# Or a secrets-provider URI resolved at runtime:
# link = "env:PRIMARY_DB_URL"

[cluster.options]
snapshot_on_apply = true
```

`url` and `link` are mutually exclusive. Either may be omitted for offline use (`dpg plan`, `dpg diff`). Commands that connect (`dpg apply`, `dpg verify`, `dpg dump`) require one to be set.

## Database `dpg.toml`

```toml
# production/myapp/dpg.toml
[database]
name           = "myapp"
default_schema = "public"
```

All `.dpg` files beneath this directory are scoped to the `myapp` database on the `production` cluster.

## Cluster-level objects

Roles, tablespaces, and custom foreign data wrappers are cluster-level. They are declared in `.dpg` files inside the cluster objects directory (`cluster/` by default). See [Roles](../../access-control/roles/), [Tablespaces](../../advanced/tablespaces/), and [Foreign Data](../../advanced/foreign-data/) for syntax.

## Snapshot location

`.dpg/snapshots/<cluster>/<database>.json` — one file per database. For example: `.dpg/snapshots/production/myapp.json`. Commit this file to version control. `dpg apply` updates it after a successful migration. `dpg plan` reads it to compute the diff without a database connection.

## Migration archive

After each successful `dpg apply`, the emitted SQL is archived to `.dpg/migrations/<cluster>/<database>/<timestamp>_<short-hash>.sql`. Commit these files. Set `directory = ""` in `[migrations]` to disable archiving.

See [Snapshots & Diffing](../snapshots/) for the snapshot file format and diffing workflow.
