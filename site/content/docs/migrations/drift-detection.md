---
title: "Drift Detection"
description: "How `dpg verify` compares the live database with the committed snapshot and reports changes made outside DPG."
weight: 3
---

`dpg verify` reads the live PostgreSQL catalog and compares it with the committed snapshot: the state DPG last applied. Only properties your source declares are compared. A property PostgreSQL reports but you never declared (an owner, a column's storage mode, a role attribute left at its default) is never drift.

```bash
dpg verify
dpg verify --cluster production --database myapp
dpg verify --format json
```

Exit codes: `0` no drift (unmanaged objects, warnings and acknowledged items are allowed), `1` drift found, `2` error.

## Reading the report

```text
production (cluster)
  drift
    role:app_service/connection_limit  snapshot 20, database 50
      ALTER ROLE "app_service" LOGIN CONNECTION LIMIT 20;
  warnings
    role:reporting  exists in the database but is not declared

production/myapp
  drift
    table:public.orders/column:status  default: "'new'" → (none)
      ALTER TABLE "public"."orders" ALTER COLUMN "status" SET DEFAULT 'new';
    table:public.orders/index:orders_id_status_idx  only in the database: {"columns":"id, status","method":"btree","name":"orders_id_status_idx"}
      DROP INDEX IF EXISTS "orders_id_status_idx";
  unmanaged
    table:public.audit_log  exists in the database but is not in the snapshot
  warnings
    table:public.orders/grant:INSERT:app_service  only in the database: INSERT TO app_service

3 drift, 1 unmanaged, 2 warnings
```

Each line is one item with a stable ID: `kind:object[/property]`. Under a drift item, the indented SQL restores the declared state. It's shown when it can be computed: not for an object whose source you've changed since the snapshot was written.

## Groups

| Group | Meaning | Fails `verify` |
|-------|---------|----------------|
| drift | A declared object or property differs from the snapshot, or is missing from the database | yes |
| conflict | Drift on a property you've also changed in the source since the snapshot, to a different value | yes |
| converged | Drift on a property you've also changed in the source, to the value the database already has: the snapshot is out of date | yes |
| unmanaged | An object in the database that DPG doesn't manage | no |
| warning | A role or tablespace DPG doesn't declare, or a grant DPG didn't make | no |

## What counts as drift

| Object | Missing from the database | Changed | Extra in the database |
|--------|---------------------------|---------|-----------------------|
| Role, tablespace | drift | drift | warning |
| Role membership of a declared role | drift | drift | drift |
| Grant | drift | — | warning |
| Privilege named in `REVOKES { }` | — | granted again: drift | — |
| Other objects (tables, views, functions, ...) | drift | drift | unmanaged |
| Parts of an object (columns, constraints, indexes, triggers, policies, partitions, enum values) | drift | drift | drift |

Values PostgreSQL can't show are never compared: role passwords, a subscription's `CONNECTION`, password options of a user mapping. Grants to roles under `[ignore]`, and the privileges PostgreSQL gives `PUBLIC` by default, are not reported.

Objects matching an [`[ignore]`](../../fundamentals/project-structure/#ignoring-objects-dpg-doesnt-manage) pattern are not compared at all, which is how you quiet a hosting platform's roles or an extension's schema.

## JSON

`--format json` prints one document for CI and scripts:

```json
{
  "drift": true,
  "targets": [
    {
      "cluster": "production",
      "database": "(cluster)",
      "items": [
        {
          "id": "role:app_service/connection_limit",
          "group": "drift",
          "kind": "role",
          "object": "app_service",
          "property": "connection_limit",
          "change": "changed",
          "snapshot": "20",
          "live": "50",
          "sql": ["ALTER ROLE \"app_service\" LOGIN CONNECTION LIMIT 20;"]
        }
      ]
    }
  ]
}
```

`change` is `changed`, `missing` (in the snapshot only), `extra` (in the database only) or `regranted`. An item also carries `source` (your source's value, when you declare the property), and `acknowledged` or `lapsed` once it has been acknowledged.

## Resolving drift

Run `dpg resolve` in a terminal to choose interactively: the items are shown as a tree (cluster or database, group, object, item). Move with the arrow keys, choose with `space` (or `a` adopt, `r` revert, `i` ignore, `n` acknowledge, `u` unacknowledge, `tab` to cycle) on an item or a whole object, group or database, filter with `/`, preview with `p` and apply with `y`. `?` lists every key.

In scripts, `dpg resolve` acts on items by their IDs:

```bash
dpg resolve --adopt table:public.orders/column:status     # keep the database's change
dpg resolve --revert role:app_service/connection_limit    # undo it in the database
dpg resolve --ignore table:public.audit_log               # not DPG's to manage
dpg resolve --ack table:public.orders/index:tmp_idx --reason "load test" --expires 2026-11-01
dpg resolve --unack table:public.orders/index:tmp_idx
```

| Action | What it does | For |
|--------|--------------|-----|
| `--adopt` | Edits your source to declare what the database has, and updates the snapshot | every group |
| `--revert` | Runs the SQL the report shows, like `dpg apply` would (archived; `--allow-destructive` for DESTRUCTIVE statements) | drift and conflicts with SQL shown |
| `--ignore` | Adds the object to `[ignore]` in the database's `dpg.toml` (the cluster's for roles and tablespaces) | unmanaged objects, undeclared roles and tablespaces |
| `--ack` | Records that you know about the item; `verify` lists it separately and doesn't fail on it | every group |
| `--unack` | Removes the acknowledgement | acknowledged items |

Unmanaged objects can't be reverted: DPG never drops what it doesn't manage.

Before changing anything, `resolve` shows a diff of every file it will edit, the snapshot entries, any SQL it will run and the acknowledgement changes, and asks for confirmation (`--yes` skips the question, `--dry-run` stops there). Afterwards it compiles your project and checks drift again; if a resolved item is still reported or a new one appears, it puts every file, snapshot and acknowledgement back. SQL a revert already ran stays run.

### Adopting

Adopting edits your source the way you'd have written the change, using the same form as `dpg dump`:

- a changed property is replaced where you declared it;
- something the source lacks is added where it belongs: a column at the end of the column list, an index in `INDEXES { }`, a column comment in `COLUMNS { name { } }`, creating the group or the `{ }` block if needed;
- something the database no longer has is removed, with its comma, and with its group if it was the last entry;
- a new object gets its own declaration (`schemas/<schema>/tables/<table>.dpg` for a table, `views.dpg`, `functions.dpg`, `types.dpg` or `sequences.dpg` for those, `schema.dpg` for the rest of a schema, `roles.dpg` for roles), and its undeclared schema, types and referenced tables come with it;
- grants are added to `GRANTS { }`, or taken out of the entries that declare them (a declared `ALL` becomes the privileges actually held).

A column that comes from a macro needs `--macro edit` (change the macro, and so every table that uses it) or `--macro inline` (replace `...macro` in this table with its columns, changed).

### Acknowledging

Acknowledgements live in `.dpg/acknowledged/<cluster>/<database>.json` and are meant to be committed. Each records who acknowledged the item (git's `user.name`), when, why, and a fingerprint of the values it saw. If the drift changes, or the `--expires` date passes, the acknowledgement lapses and the item is reported again.

## Before `dpg apply` runs anything

`dpg apply` plans from the snapshot, not from the database, so it checks the database first (also with `--dry-run`). It refuses, with exit code 1 and the list of problems, when:

- an object the migration changes has drift, a conflict or a converged item (an acknowledged one only blocks if the migration changes that very property);
- an object it creates already exists;
- something DPG doesn't manage (a view, a function) depends on an object it drops or recreates;
- an object it recreates has grants made outside DPG, which recreating would lose.

Drift on objects the migration doesn't touch is printed as a note and doesn't block. Fix the problems with `dpg resolve`, or pass `--allow-drift` to go ahead anyway.

Every migration file also carries the checks it can make in SQL as guards at its start, so a migration run with `psql` or another tool refuses in the same situations without changing anything.

For drift, each database keeps a fingerprint of the objects DPG has seen (in `dpg.objects`): a hash of their catalog definition, grants included. A migration file refuses to change an object whose fingerprint changed since (DPG-E055), or one DPG has no fingerprint of (DPG-E054; `dpg verify` records it when the object has no drift). Run `SET dpg.allow_drift = on` first to skip that check deliberately.

## Databases that are behind

A database that hasn't run all of the migration log's migrations yet (production between deploys, say) is compared with the log at its own position, not with the latest snapshot: the migrations it hasn't run aren't drift. `dpg verify` shows how many it hasn't run (`pending` in the JSON), and `dpg apply` runs them.

## Relation to `dpg plan --live`

`dpg plan --live` compares your source with the database and prints the migration that would make the database match the source. It mixes pending source changes with reverting drift, and it never proposes dropping an object your source doesn't declare (it lists those instead). Use `dpg verify` to detect and classify drift, and `dpg plan --live` when you want one migration for everything.
