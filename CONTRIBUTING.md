# Contributing to DPG

Thank you for your interest in contributing. This document covers everything you need to get started.

## Prerequisites

| Requirement | Notes |
|---|---|
| Go 1.25 or later | `go version` to verify |
| GCC or Clang | Required for CGo — `pg_query_go` links against the PostgreSQL C parser |
| `staticcheck` | Optional, for `make lint` — `go install honnef.co/go/tools/cmd/staticcheck@latest` |

No live PostgreSQL instance is required for most development. The test suite is fully offline.

## Getting Started

```bash
git clone https://github.com/thec1oud/dpg
cd dpg
make build     # builds core/build/dpg
make test      # runs all tests
```

## Repository Layout

```
core/                  Go module (github.com/thec1oud/dpg)
  cmd/dpg/             CLI commands (plan, apply, verify, dump, diff, portability)
  internal/
    apply/             The apply flow the CLI and pkg/dpg/engine share
    ast/               Abstract syntax tree types
    blockparser/       DPG { } block parser
    compiler/          Orchestrates scan → parse → IR build pipeline
    config/            dpg.toml loading
    diff/              Differ: desired IR vs snapshot → DiffOps
    docssite/          Embedded documentation site (Hugo output)
    emit/              Renders DiffOps to a Migration with SQL text
    executor/          Applies migrations against a live PG connection
    graph/             Dependency resolver with topological sort
    introspect/        Live PG catalog introspection
    ir/                IR types and Builder (parse tree → typed IR objects)
    linter/            Static analysis diagnostics
    merger/            Source file merging
    pipeline/          Interfaces and registry (internal)
    pgparser/          PostgreSQL DDL parser wrapper
    portability/       Portability analysis
    project/           Project discovery and structure
    scanner/           Source file scanner
    secrets/           Secret resolution (env:, link:)
    snapshot/          Snapshot serialization
    ui/                Terminal output helpers
    version/           Build-time version metadata
  examples/            Runnable pipeline examples (also serve as integration tests)
  pkg/dpg/engine/      Provisional library API: status, plan, apply, verify
lang/                  Language tooling
  grammar/             Tree-sitter grammar (tree-sitter-dpg)
  lsp/                 Language server (dpg-lsp)
  syntaxes/            TextMate / VS Code syntax definitions
plugins/               Editor integrations
  helix/               Helix plugin
  idea/                JetBrains plugin
  nvim/                Neovim plugin
  vscode/              VS Code extension
rfc/                   Language specification
site/                  Hugo documentation site
```

## Making Changes

1. **Fork** the repository and create a branch from `main`:
   ```bash
   git checkout -b feat/your-feature-name
   ```

2. **Write tests** for any new behaviour. Most packages have `_test.go` files alongside the source. The `examples/` directory contains end-to-end pipeline tests — add a fixture if you are changing compilation or diffing behaviour.

3. **Run the full suite** before pushing:
   ```bash
   make test
   make vet
   ```
   Integration tests (`//go:build integration`) need a live server: `go test -tags integration ./...` starts Postgres containers, and a test that asks `testpg.Database` for a database of its own creates it on the server `DPG_TEST_POSTGRES` names instead, when it is set (a superuser's connection string).

4. **Commit messages** should be concise and written in the imperative mood (`add support for X`, `fix Y when Z`). Reference issues where applicable (`fixes #42`).

5. **Open a pull request** against `main` with a clear description of what changed and why. Include the relevant section of the [RFC](rfc/dpg-1.md) if your change affects language semantics.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Avoid comments that restate what the code does. Prefer comments that explain *why* — hidden constraints, non-obvious invariants, workarounds.
- Do not add error handling for scenarios that cannot happen. Trust framework guarantees. Only validate at system boundaries (user input, external APIs).
- Keep new abstractions close to the call site until a clear need for reuse emerges.

## Running the Linter

```bash
make lint      # requires staticcheck on PATH
```

## Reporting Bugs

Open a GitHub issue with:
- DPG version (`dpg --version`)
- A minimal `.dpg` source file or snapshot that reproduces the issue
- The command you ran, the output you got, and the output you expected

## Proposing Features

Open an issue before writing significant code. Features that change language semantics should reference or propose an RFC amendment. The current specification lives in [rfc/dpg-1.md](rfc/dpg-1.md).

## License

By contributing you agree that your contributions will be licensed under the [MIT License](LICENSE).
