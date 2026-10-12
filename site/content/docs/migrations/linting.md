---
title: "Linting"
description: "Built-in static analysis rules, severity levels, and dpg.toml configuration."
weight: 2
---

The linter runs automatically as part of `dpg plan`, `dpg apply`, and `dpg validate`. It runs over the merged IR before any SQL is generated. Lint **errors** abort the command. Lint **warnings** are printed to stderr but do not block.

## Configuration

```toml
# dpg.toml
[linter]
warn_on_deprecated            = true   # default
require_column_comments       = false  # default
forbid_hardcoded_passwords    = true   # default
max_columns_per_table         = 50     # default: 50 (0 = disabled)
```

---

## `deprecated`

**Severity:** Warning — **Config:** `warn_on_deprecated` — **Default:** enabled

Warns when a table, column, view, or function is marked [`DEPRECATED`](lifecycle/).

```sql
TABLE legacy_sessions ( ... )
{ DEPRECATED "Use the jwt_tokens table instead"; }
```

```
warn  [deprecated] table public.legacy_sessions is deprecated: Use the jwt_tokens table instead
```

---

## `hardcoded-password`

**Severity:** Error — **Config:** `forbid_hardcoded_passwords` — **Default:** enabled

Errors when a column whose name contains `password`, `passwd`, `pwd`, `secret`, or `passphrase` has a string-literal `DEFAULT`.

```sql
TABLE service_accounts (
    password TEXT NOT NULL DEFAULT 'changeme',   -- error
    ...
);
```

```
error [hardcoded-password] column public.service_accounts.password default may contain a hardcoded password
```

Use a parameter or runtime value instead. For role passwords, use `env:VAR_NAME`. See [Roles](../../access-control/roles/).

---

## `hardcoded-role-password`

**Severity:** Error — **Config:** `forbid_hardcoded_passwords` — **Default:** enabled

Errors when a `ROLE`'s `PASSWORD` is a literal value with no `{{secret-uri}}` placeholder — a separate check from `hardcoded-password` above (a table column default), covering the same failure mode for `ROLE PASSWORD`.

```sql
ROLE app_user PASSWORD 'changeme';   -- error
```

```
error [hardcoded-role-password] role app_user: PASSWORD is a literal value; use a {{secret-uri}} reference instead
```

See [Roles](../../access-control/roles/).

---

## `security-definer-search-path`

**Severity:** Warning — **Config:** always enabled

Warns when a `SECURITY DEFINER` function does not set `search_path`.

```sql
-- triggers warning
FUNCTION unsafe_auth(p_user TEXT) RETURNS BOOLEAN
LANGUAGE plpgsql SECURITY DEFINER
AS $$ ... $$;
```

```
warn  [security-definer-search-path] SECURITY DEFINER function public.unsafe_auth should set search_path
```

Fix: add `SET search_path = public` to the signature line:

```sql
-- clean
FUNCTION safe_auth(p_user TEXT) RETURNS BOOLEAN
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public
AS $$ ... $$;
```

---

## `column-count-exceeded`

**Severity:** Error — **Config:** `max_columns_per_table` — **Default:** `50` (set to `0` to disable)

```toml
[linter]
max_columns_per_table = 50  # default; 0 = disabled
```

```
error [column-count-exceeded] table public.wide_table has 67 columns (max 50)
```

---

## `missing-column-comment`

**Severity:** Warning — **Config:** `require_column_comments` — **Default:** disabled

```toml
[linter]
require_column_comments = true
```

```
warn  [missing-column-comment] column public.users.created_at has no comment
```

Fix by adding a `COLUMN` block with a comment:

```sql
TABLE users ( created_at TIMESTAMPTZ NOT NULL DEFAULT now(), ... )
{
    COLUMN created_at { COMMENT 'UTC timestamp when the record was created'; }
}
```

---

## `serial-sequence-declared`

**Severity:** Warning — **Config:** always enabled

Warns when a hand-declared `SEQUENCE` object's name collides with the name PostgreSQL auto-manages for a `GENERATED ... AS IDENTITY` column's sequence (`<table>_<column>_seq`) elsewhere in the same project.

```sql
TABLE t ( id integer GENERATED ALWAYS AS IDENTITY, ... );

SEQUENCE t_id_seq;   -- triggers warning: collides with t's own identity sequence
```

```
warn  [serial-sequence-declared] sequence public.t_id_seq has the same name PostgreSQL auto-manages for an identity column's sequence in this schema
```

Covers both `GENERATED ... AS IDENTITY` columns and `SERIAL`/`BIGSERIAL`/`SMALLSERIAL` columns — both have an auto-managed owned sequence named `<table>_<column>_seq` to check against.

---

## `unnecessary-revocation`

**Severity:** Warning — **Config:** always enabled

Warns when a `REVOKES` entry names a (role, privilege) pair with no matching `GRANTS` entry for that same pair in the *same object's* declaration — usually a copy-paste or typo, since revoking a privilege that was never declared as granted is a no-op.

```sql
TABLE orders ( ... )
{
    GRANTS      { SELECT TO app_readonly; }
    REVOKES { DELETE FROM app_readonly; }   -- triggers warning: no GRANT of DELETE above
}
```

```
warn  [unnecessary-revocation] table public.orders: REVOKE of DELETE from app_readonly has no matching GRANT in this declaration
```

This is scoped to a single object's own declaration, not full grant history — it won't catch a revocation targeting a privilege that was granted by a previous `apply` but isn't declared in the current source.

---

## `deprecated-reference`

**Severity:** Warning — **Config:** `warn_on_deprecated` — **Default:** enabled

Warns when a non-deprecated object references a deprecated one:

- A `FOREIGN KEY` referencing a deprecated table, or a deprecated column of the target table.
- A column, `FUNCTION` parameter/return type, or `PROCEDURE` parameter, typed as a deprecated custom `TYPE`.

```sql
TABLE users ( id integer PRIMARY KEY )
{
    DEPRECATED 'use accounts instead';
}

TABLE orders (
    id      integer PRIMARY KEY,
    user_id integer REFERENCES users (id)   -- triggers warning: users is deprecated
);
```

```
warn  [deprecated-reference] foreign key (unnamed) on public.orders references deprecated table public.users: use accounts instead
```

Scoped to `FOREIGN KEY` and `TYPE` references only — a `VIEW` query, a function/procedure body, or a `DEFAULT` expression referencing a deprecated object is not covered, since none of those has a real SQL parser in DPG today and a text-scan heuristic risks a false-positive warning. An already-deprecated referencing object never double-fires this rule — its own `deprecated` diagnostic already covers it.

---

## `min-pg-version`

**Severity:** Warning — **Config:** `min_pg_version` in `[compiler]` (root, cluster or database `dpg.toml`) — **Default:** off

Warns when the source declares something the configured oldest PostgreSQL version doesn't support, such as a `VIRTUAL` generated column (18), `UNIQUE NULLS NOT DISTINCT` (15), a `MAINTAIN` grant (17), an unlogged sequence (15), or a subscription's `failover` option (17). Appendix F of the DPG specification lists every construct and its version.

```toml
[compiler]
min_pg_version = 15
```

```
warn  [min-pg-version] VIRTUAL generated column requires PostgreSQL 18+ (RFC Section 7.2), but this project's min_pg_version is 15
```

Statements DPG emits only for a change to something that already exists (such as `ALTER COLUMN ... SET EXPRESSION`, PostgreSQL 17) don't appear in the source, so the rule can't check them. SQL inside view queries, function bodies and `DEFAULT`/`CHECK` expressions isn't checked either.

---

## `concurrently-new-table`

**Severity:** Warning — **Config:** always enabled

Warns when an index declares `CONCURRENTLY` on a table or materialized view that isn't in the snapshot yet. A new relation's indexes are created in the same transaction as the relation, and PostgreSQL refuses `CREATE INDEX CONCURRENTLY` inside a transaction, so the keyword has no effect there.

```sql
TABLE events ( id bigint, at timestamptz )   -- not applied yet
{
    INDEXES { CONCURRENTLY idx_events_at (at); }
}
```

```
warn  [concurrently-new-table] index idx_events_at: CONCURRENTLY has no effect, public.events is new and its indexes are created in the same transaction
```

Checked by `dpg plan` and `dpg apply`, which load the snapshot. `dpg validate` doesn't load one, so it skips this rule.

## `[linter.rules]` — per-rule severity overrides

Individual rules can be set to `"error"`, `"warning"`, or `"off"`, overriding their own default level:

```toml
[linter.rules]
security-definer-search-path = "error"
deprecated                   = "off"
```

An `"off"` rule is suppressed entirely (no diagnostic emitted at all). An `"error"` override behaves like `--strict`, but scoped to just that rule.

---

## Linter output format

```
warn  [deprecated] table public.legacy_sessions is deprecated: Use the jwt_tokens table instead
error [hardcoded-password] column public.service_accounts.password default may contain a hardcoded password
warn  [security-definer-search-path] SECURITY DEFINER function public.unsafe_auth should set search_path
```

Errors cause all commands to exit non-zero before any SQL is generated.
