---
title: "DPG — Declarative PG"
description: "A declarative, state-based superset of PostgreSQL SQL that compiles to idiomatic PG DDL."
---

## What is DPG?

DPG is a schema compiler and migration tool for PostgreSQL. You describe what your database **should be**; DPG figures out what needs to change and generates the minimal, safe SQL migration.

```sql
TABLE users (
    id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL
) {
    INDEXES { UNIQUE users_email_idx (email); }
    GRANTS { SELECT, INSERT TO app_role; }
}
```

Run `dpg plan` to see the SQL diff. Run `dpg apply` to execute it.

---

## Key Features

- **Offline-first** — `dpg plan` and `dpg diff` never require a live database
- **Safe by default** — destructive operations are blocked unless explicitly permitted
- **Snapshot-driven** — the committed snapshot is the source of truth, not the live DB
- **Zero new syntax** — DPG extends valid PostgreSQL DDL; no new language to learn
- **Plugin API** — extend the pipeline via `pkg/dpg` (linters, snapshot stores, apply executors)

---

## Quick Links

| | |
|---|---|
| [Installation](docs/getting-started/installation/) | Build from source, system requirements |
| [Quick Start](docs/getting-started/quick-start/) | Your first DPG project in 5 minutes |
| [CLI Reference](docs/cli/) | All commands with flags |
| [Language Reference](docs/fundamentals/two-part-syntax/) | Two-part syntax, merge rules, structural scoping |
| [Plugin API](docs/extending/) | Extend DPG via the public Go API |
| [RFC DPG-001](rfc/dpg-1/) | Full specification |
