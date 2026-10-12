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
                ├── 20260101T120000Z-4f0c2b9e1d7a.sql   # migration log (committed)
                └── 20260101T120000Z-4f0c2b9e1d7a.json
```

**Discovery rules:**
- Every `dpg.toml` starts with `kind = "root"`, `kind = "cluster"`, or `kind = "database"`. The root is the directory whose `dpg.toml` says `kind = "root"`.
- Clusters and databases can sit at any depth below the root. A database belongs to its nearest enclosing cluster directory, or to the cluster named by `parent = "<relative path>"` in its `dpg.toml`, so it can also live outside the cluster's directory.
- A cluster can't be nested inside another cluster or a database, and a database can't be nested inside another database.
- All `.dpg` files beneath a database directory compile as one unit.
- Files under `schemas/<name>/` (at any depth) belong to schema `<name>`, as if wrapped in `SCHEMA <name> { }`. The schema's own settings (owner, comment, grants, default privileges) go in `schemas/<name>/schema.dpg`:

```sql
-- schemas/analytics/schema.dpg
SCHEMA analytics {
    OWNER analytics_role;
    COMMENT 'Derived tables and event aggregations';
}
```

- Inside a schema scope, a schema qualifier on a declared name may only repeat that schema; `TABLE other.t` inside `schemas/analytics/` is an error (DPG-E040).

## Root `dpg.toml`

```toml
kind = "root"

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
# Where the migration log lives. Default: ".dpg/migrations".
# It can't be empty.
directory = ".dpg/migrations"

[ledger]
# Each database records the migrations it has run in <schema>.migrations.
enabled = true
schema  = "dpg"

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
kind = "cluster"

[cluster]
name                = "production"
cluster_objects_dir = "cluster"      # reserved name; no database may share it

# Inline connection string (mutually exclusive with link):
url = "postgresql://pguser@primary.prod.internal:5432/postgres"

# Or a secrets-provider URI resolved at runtime:
# link = "env:PRIMARY_DB_URL"
```

`url` and `link` are mutually exclusive. Either may be omitted for offline use (`dpg plan`, `dpg diff`). Commands that connect (`dpg apply`, `dpg verify`, `dpg dump`) require one to be set.

## Database `dpg.toml`

```toml
# production/myapp/dpg.toml
kind = "database"

[database]
name           = "myapp"
default_schema = "public"
```

`default_schema` (optional, default `public`) is the schema for objects declared with no qualifier outside any `SCHEMA { }` block or `schemas/<name>/` directory.

A database can also set its own `url` or `link`, replacing the cluster's connection for that database only. It's used exactly as written, which suits a different role per database or a connection pooler whose pool name differs from the database name. Without one, the database connects through the cluster's `url`/`link` with the database name swapped in.

```toml
# databases/crm/dpg.toml — lives outside its cluster's directory
kind   = "database"
parent = "../../production"

[database]
name = "crm"
link = "env:CRM_DB_URL"
```

All `.dpg` files beneath this directory are scoped to the `myapp` database on the `production` cluster.

## Ignoring objects DPG doesn't manage

Databases often contain objects that aren't yours to declare: a hosting platform's roles (`rdsadmin`, `rds_*`, `azure_pg_admin`), schemas an extension creates (`cron`), or monitoring extensions. List them under `[ignore]` and DPG leaves them out of everything: `plan`, `apply` and `verify` don't compare them, and `dump` doesn't write them.

```toml
# production/dpg.toml — patterns for this environment only
[ignore]
roles      = ["rdsadmin", "rds_*"]
schemas    = ["cron"]                   # everything inside is ignored too
extensions = ["pg_stat_statements"]
functions  = ["public.pgaudit_*"]       # schema.name, or just name for any schema
```

`[ignore]` is allowed in the root, cluster and database `dpg.toml`; the lists of all three apply together. Patterns use `*` (any run of characters) and `?` (one character). There is one key per object kind: `roles`, `tablespaces`, `schemas`, `extensions`, `tables`, `views`, `functions`, `procedures`, `aggregates`, `types`, `sequences`, `collations`, `operators`, `operator_classes`, `operator_families`, `casts`, `statistics`, `foreign_data_wrappers`, `servers`, `user_mappings`, `publications`, `subscriptions`, `event_triggers`, `text_search_configurations`, `text_search_dictionaries`, `text_search_parsers` and `text_search_templates`.

Declaring an object that matches an `[ignore]` pattern is an error (DPG-E045). PostgreSQL's predefined `pg_*` roles and the bootstrap superuser are never read, so they need no pattern.

Roles are shared by everything on a cluster, so a live role that is neither declared nor ignored is reported as a warning by `dpg verify` and `dpg plan --live` — never as drift, and never dropped.

## Cluster-level objects

Roles, tablespaces, and custom foreign data wrappers are cluster-level. They are declared in `.dpg` files inside the cluster objects directory (`cluster/` by default). See [Roles](../../access-control/roles/), [Tablespaces](../../advanced/tablespaces/), and [Foreign Data](../../advanced/foreign-data/) for syntax.

## Snapshot location

`.dpg/snapshots/<cluster>/<database>.json` — one file per database. For example: `.dpg/snapshots/production/myapp.json`. Commit this file to version control. `dpg apply` updates it after a successful migration. `dpg plan` reads it to compute the diff without a database connection.

## Migration log

Every migration `dpg apply` runs is recorded in `.dpg/migrations/<cluster>/<database>/` as `<id>.sql` and `<id>.json`. Commit these files with the snapshot: together they are the history a database that's behind replays to catch up. See [Snapshots & Diffing](../snapshots/#migration-log).

See [Snapshots & Diffing](../snapshots/) for the snapshot file format and diffing workflow.
