# DPG — Declarative PG

> Describe the database you want. DPG figures out how to get there.

DPG is a declarative, state-based schema compiler for PostgreSQL. Instead of writing migration files, you describe the desired state of your database in `.dpg` source files. DPG computes the minimal, safe SQL required to reach that state from wherever things currently are.

```sql
TABLE users (
    id    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email TEXT   NOT NULL UNIQUE
) {
    INDEXES { idx_users_email (email); }
    GRANTS { SELECT TO app_readonly; SELECT, INSERT, UPDATE TO app_service; }
}
```

Run `dpg plan`:

```sql
BEGIN;

CREATE TABLE "public"."users" (
    "id"    bigint GENERATED ALWAYS AS IDENTITY,
    "email" text NOT NULL,
    CONSTRAINT "pk_users" PRIMARY KEY ("id"),
    CONSTRAINT "uq_users_email" UNIQUE ("email")
);

GRANT SELECT ON TABLE "public"."users" TO "app_readonly";
GRANT SELECT, INSERT, UPDATE ON TABLE "public"."users" TO "app_service";

COMMIT;

-- Non-transactional (executed after COMMIT):
CREATE INDEX CONCURRENTLY "idx_users_email" ON "public"."users" ("email");
```

If the database already matches, the output is `(no changes)`. No-ops are a first-class result.

## Why DPG

PostgreSQL DDL is imperative. Migration files describe *actions taken at a point in time* — not the current intended state. The result is drift, no single source of truth, and a history that is non-idempotent by construction.

DPG flips the model:

- **Source files describe desired state.** The compiler derives the delta.
- **Offline-first.** `dpg plan` and `dpg diff` never touch a database. CI can review migrations before any server is reached.
- **Safety-classified output.** Every generated statement is tagged `SAFE`, `CAUTION`, `DESTRUCTIVE`, or `MANUAL`. Destructive operations are blocked by default.
- **No new language.** DPG syntax is valid PostgreSQL DDL with the leading verb removed and a `{ }` block appended for sub-objects (indexes, grants, RLS policies, comments). Existing tooling, syntax highlighting, and institutional knowledge apply directly.
- **Idempotent.** Applying the same plan twice produces zero SQL on the second run.

## Installation

**Requirements:** Go 1.25+, a C compiler (GCC or Clang) — `pg_query_go` links against the real PostgreSQL parser via CGo.

```bash
git clone https://github.com/thec1oud/dpg
cd dpg
make install       # installs dpg to $GOPATH/bin
```

See the [installation guide](https://thec1oud.github.io/dpg/docs/getting-started/installation/) for cross-compilation, pre-built binaries, and all `make` targets.

## Quick Start

```bash
# Bootstrap from an existing database
dpg dump --cluster prod --database myapp

# Declare schema changes in .dpg source files, then:
dpg plan           # preview the SQL (no database connection required)
dpg apply          # execute the migration and update the snapshot
dpg verify         # detect live drift against the committed snapshot
```

## Commands

| Command | Description |
|---|---|
| `dpg plan` | Diff source files vs committed snapshot; print SQL. No DB required. |
| `dpg apply` | Lint, diff, prompt for approval, execute SQL, update snapshot. |
| `dpg verify` | Connect to live DB and report any drift from the snapshot. |
| `dpg dump` | Introspect a live DB and generate initial `.dpg` source files. |
| `dpg diff` | Diff two `.dpg` source directories and print the SQL between them. |
| `dpg portability` | Report PostgreSQL-specific constructs that reduce portability. |

All commands accept `--cluster` and `--database` flags when a project has multiple targets. See the [CLI reference](https://thec1oud.github.io/dpg/docs/cli/) for the full flag reference.

## Project Layout

```
myproject/
├── dpg.toml                         # Root config
├── prod/                            # Cluster directory
│   ├── dpg.toml                     # Cluster config (connection url or secret link)
│   ├── cluster/                     # Cluster-level objects (roles, tablespaces)
│   └── myapp/                       # Database directory
│       ├── dpg.toml                 # Database config
│       └── schemas/
│           └── public/
│               ├── tables.dpg
│               ├── views.dpg
│               └── functions.dpg
└── .dpg/
    └── snapshots/                   # Committed snapshot (source of truth)
```

See the [project structure guide](https://thec1oud.github.io/dpg/docs/fundamentals/project-structure/) for the full directory layout.

## Documentation

| Document | Contents |
|---|---|
| [Installation](https://thec1oud.github.io/dpg/docs/getting-started/installation/) | Build requirements, make targets, cross-compilation |
| [Project Structure](https://thec1oud.github.io/dpg/docs/fundamentals/project-structure/) | Directory layout, dpg.toml, cluster and database config |
| [Two-Part Syntax](https://thec1oud.github.io/dpg/docs/fundamentals/two-part-syntax/) | The `{ }` block model, merge rules, structural scoping |
| [Schema Objects](https://thec1oud.github.io/dpg/docs/schema-objects/) | Tables, views, functions, types, sequences, roles, indexes, RLS, grants |
| [CLI Reference](https://thec1oud.github.io/dpg/docs/cli/) | All commands and flags |
| [Linting](https://thec1oud.github.io/dpg/docs/migrations/linting/) | Lint rules and configuration |
| [Lifecycle Directives](https://thec1oud.github.io/dpg/docs/migrations/lifecycle/) | RENAMED FROM, DEPRECATED, PROTECTED, DROP CASCADE |
| [Snapshots & Diffing](https://thec1oud.github.io/dpg/docs/fundamentals/snapshots/) | JSON snapshot format, dry-run, watch mode |
| [RFC DPG-1](rfc/dpg-1.md) | Full language specification |

## Development

### Setup

```bash
make setup
```

Installs Go, a C compiler (required by `pg_query_go`), Docker (integration tests), staticcheck, Hugo extended, and Node.js, then configures the git hooks. Safe to re-run.

```bash
make setup -- --check     # verify tools without installing anything
make setup -- --no-docs   # skip Hugo + Node if you don't need the docs site
```

### Versioning

The project uses three tag namespaces, each triggering its own CI workflow:

| Tag format | Component | Workflow |
|---|---|---|
| `v1.2.3` | `dpg` CLI | Build + release binaries |
| `lsp-v1.2.3` | `dpg-lsp` language server | Build + release LSP binaries |
| `docs-v1.2.3` | Documentation site | Deploy to GitHub Pages |

All three share a single `CHANGELOG.md`. LSP and docs entries keep their full prefix in the section header (`[lsp-v1.2.3]`, `[docs-v1.2.3]`) so you can tell at a glance which component each release covers. Main dpg entries use bare semver (`[1.2.3]`).

### Changelog

```bash
make changelog            # refresh [Unreleased] from git log (dpg baseline)
make changelog PREFIX=lsp-v   # use lsp-v* tags as the baseline instead
make changelog PREFIX=docs-v  # use docs-v* tags as the baseline
```

Populates the `[Unreleased]` section in `CHANGELOG.md` with commits since the last tag of the given type, sorted into `### Added` (`feat:`), `### Fixed` (`fix:`), and `### Changed` (everything else). Internal commits (`chore:`, `test:`, `ci:`, `style:`, `build:`) are omitted. The `## [Unreleased]` header is preserved so you can run this any time during development.

### Releasing

```bash
make tag TAG=v1.2.3
make tag TAG=lsp-v1.2.3
make tag TAG=docs-v1.2.3
```

1. Runs `make changelog` for the matching namespace to populate `[Unreleased]`.
2. Replaces the `## [Unreleased]` header with the versioned header (e.g. `## [1.2.3] — 2026-05-16`) and inserts a fresh empty `## [Unreleased]` above it.
3. Commits `CHANGELOG.md` as `chore: release <tag>` and creates the git tag.

Then push:

```bash
git push && git push origin <tag>
```

CI picks up the tag and publishes the release automatically.

**Grammar-only releases**: `tree-sitter-dpg`'s npm trusted-publisher entry only supports one workflow filename, and it's normally set to `release.yml` (the full-cascade path). Pushing a standalone `grammar-v*` tag runs `release-grammar.yml` directly instead, which won't match and will fail npm publish with a 404 — see the comment above the `publish-npm` job in [`release-grammar.yml`](.github/workflows/release-grammar.yml) for the toggle steps.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE) — Copyright (c) 2026 Daniel Tsegaw
