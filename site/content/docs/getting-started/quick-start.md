---
title: "Quick Start"
description: "Create your first DPG project, write schemas, plan and apply migrations."
weight: 2
---

## Core Concepts in 90 Seconds

**Offline-first.** `dpg plan` and `dpg diff` never touch a database. They compare `.dpg` source files against a committed snapshot. CI pipelines can review migrations before any database is reached.

**Idempotent.** Applying the same plan twice produces zero SQL on the second run.

**Safety-classified.** Every generated statement is tagged `SAFE`, `CAUTION`, or `DESTRUCTIVE`. Destructive operations are blocked by default; they require `--allow-destructive` to proceed.

**Additive grants.** DPG only emits `GRANT`. It never auto-revokes. Removing a `GRANTS` entry emits nothing — write an explicit `REVOKES { }` block to revoke.

**No new DSL.** Part 1 of every declaration is valid PostgreSQL DDL syntax with the leading `CREATE` verb stripped. `FUNCTION` bodies, `VIEW` queries, `TABLE` column lists — all are written exactly as PostgreSQL requires.

---

## From Zero to First Migration

### 1. Install

```bash
git clone https://github.com/thec1oud/dpg
cd dpg
make install
```

Verify:

{{< dpg-version-output >}}

### 2. Scaffold a New Project

```bash
mkdir myproject && cd myproject
dpg init
```

This creates:

```
myproject/
├── dpg.toml                          # root config
└── production/
    ├── dpg.toml                      # cluster config (add url here)
    └── myapp/
        ├── dpg.toml                  # database config
        └── schemas/
            └── public/
                └── tables/
```

Open `production/dpg.toml` and add your PostgreSQL connection URL:

```toml
[cluster]
url = "postgres://user:password@localhost:5432"
```

### 3. Write a Schema

Create `production/myapp/schemas/public/tables/users.dpg`:

```sql
TABLE users (
    id    BIGINT GENERATED ALWAYS AS IDENTITY,
    email TEXT NOT NULL,
    name  TEXT NOT NULL,
    CONSTRAINT pk_users       PRIMARY KEY (id),
    CONSTRAINT uq_users_email UNIQUE (email)
) {
    INDEXES { idx_users_email (email); }
    GRANTS {
        SELECT TO app_readonly;
        SELECT, INSERT, UPDATE TO app_service;
    }
    ENABLE ROW LEVEL SECURITY;
}
```

### 4. Validate Offline

```bash
dpg validate
# ✓ 1 object compiled, 0 errors, 0 warnings
```

No database connection required. The compiler parses and lints all `.dpg` files.

### 5. Plan the Migration

```bash
dpg plan
```

Output:

```sql
-- DPG Migration
-- Cluster:         production
-- Database:        myapp

-- transactional
BEGIN;

-- source: schemas/public/tables/users.dpg:1
CREATE TABLE "public"."users" (
    "id"    bigint GENERATED ALWAYS AS IDENTITY,
    "email" text NOT NULL,
    "name"  text NOT NULL,
    CONSTRAINT "pk_users"       PRIMARY KEY ("id"),
    CONSTRAINT "uq_users_email" UNIQUE ("email")
);

-- source: schemas/public/tables/users.dpg:4
CREATE INDEX IF NOT EXISTS "idx_users_email" ON "public"."users" ("email");

GRANT SELECT ON TABLE "public"."users" TO "app_readonly";
GRANT SELECT, INSERT, UPDATE ON TABLE "public"."users" TO "app_service";
ALTER TABLE "public"."users" ENABLE ROW LEVEL SECURITY;

COMMIT;
```

No database connection required — the diff runs against the committed snapshot (empty on first run).

### 6. Apply the Migration

```bash
dpg apply
```

DPG runs the plan, prompts for approval, executes the SQL against the primary node, and updates `.dpg/snapshots/production/myapp.json`.

Use `-y` to skip the approval prompt in CI:

```bash
dpg apply -y
```

### 7. Verify No Drift

```bash
dpg verify
# ✓ No drift detected
```

`dpg verify` introspects the live database catalog and compares it against the snapshot. Exit code 0 means the live database matches the declared state.

### 8. Iterate

Edit a `.dpg` file and run `dpg plan` again. DPG emits only the delta:

```sql
-- Add name column to existing table
ALTER TABLE "public"."users" ADD COLUMN "name" text NOT NULL;
```

---

## Bootstrap from an Existing Database

If you have an existing PostgreSQL database, use `dpg dump` to generate initial `.dpg` source files:

```bash
dpg dump --cluster production --database myapp
```

This connects to the database, reads the live catalog, and writes `.dpg` files and an initial snapshot. Objects that cannot be cleanly reconstructed from catalog data are emitted as commented-out stubs with a `-- dpg:manual` marker — review and replace those manually. From there, treat the generated files as your source of truth and iterate with `dpg plan` and `dpg apply`.

---

## Next Steps

- [Two-Part Syntax](../fundamentals/two-part-syntax/) — the `{ }` block model, structural scoping, merge rules
- [Schema Objects](../schema-objects/) — every PostgreSQL object type with DPG source and generated SQL
- [Lifecycle Directives](../migrations/lifecycle/) — renames, deprecation, protected tables
- [CLI Reference](../cli/) — all commands with flags
