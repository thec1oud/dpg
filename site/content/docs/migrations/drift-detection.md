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

Exit codes: `0` no drift (unmanaged objects and warnings are allowed), `1` drift found, `2` error.

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

`change` is `changed`, `missing` (in the snapshot only), `extra` (in the database only) or `regranted`.

## Resolving drift

- **The change was a mistake:** run the SQL the report shows under the item.
- **The change was intended:** update your `.dpg` source to match, then apply, so the source and snapshot record it.
- **The object isn't DPG's to manage:** add it to `[ignore]`.

`dpg apply` alone does not undo drift: it plans from the snapshot, not from the database, so it doesn't see changes made outside DPG.

## Relation to `dpg plan --live`

`dpg plan --live` compares your source with the database and prints the migration that would make the database match the source. It mixes pending source changes with reverting drift, and it never proposes dropping an object your source doesn't declare (it lists those instead). Use `dpg verify` to detect and classify drift, and `dpg plan --live` when you want one migration for everything.
