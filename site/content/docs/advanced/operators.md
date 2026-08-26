---
title: "Operators"
description: "OPERATOR, OPERATOR CLASS, and OPERATOR FAMILY declarations."
weight: 5
---

## OPERATOR

```sql
SCHEMA public {
    OPERATOR === (
        LEFTARG    = complex,
        RIGHTARG   = complex,
        PROCEDURE  = complex_eq,
        COMMUTATOR = ===,
        NEGATOR    = !==,
        RESTRICT   = eqsel,
        JOIN       = eqjoinsel,
        HASHES,
        MERGES
    );
}
```

```sql
-- emits
CREATE OPERATOR "public".=== (
    LEFTARG    = complex,
    RIGHTARG   = complex,
    PROCEDURE  = complex_eq,
    COMMUTATOR = ===,
    NEGATOR    = !==,
    RESTRICT   = eqsel,
    JOIN       = eqjoinsel,
    HASHES,
    MERGES
);
```

## Diffing behaviour — OPERATOR

Identity is `(schema, symbol, leftarg_type, rightarg_type)`.

| Change | SQL | Safety |
|--------|-----|--------|
| `PROCEDURE` change | `DROP OPERATOR` + `CREATE OPERATOR` | `DESTRUCTIVE` |
| `RESTRICT`/`JOIN` set, cleared, or changed | `ALTER OPERATOR SET (...)` | `SAFE` |
| `COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` newly set (previously unset) | `ALTER OPERATOR SET (...)` | `SAFE` |
| `COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` changed once already set | `DROP OPERATOR` + `CREATE OPERATOR` | `DESTRUCTIVE` |

`COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` are one-directional in real PostgreSQL: `ALTER OPERATOR ... SET (COMMUTATOR = ...)` errors once a value is already recorded, so only the unset-to-set transition can go through `ALTER OPERATOR`.

---

## OPERATOR FAMILY

```sql
SCHEMA public {
    OPERATOR FAMILY my_family USING btree;
}
```

```sql
-- emits
CREATE OPERATOR FAMILY "public"."my_family" USING btree;
```

---

## OPERATOR CLASS

```sql
SCHEMA public {
    OPERATOR CLASS my_ops USING btree FOR TYPE mytype (
        OPERATOR 1 <  ,
        OPERATOR 2 <= ,
        OPERATOR 3 =  ,
        OPERATOR 4 >= ,
        OPERATOR 5 >  ,
        FUNCTION 1 mytype_cmp(mytype, mytype)
    );
}
```

```sql
-- emits
CREATE OPERATOR CLASS "public"."my_ops"
    FOR TYPE mytype USING btree AS
    OPERATOR 1 < ,
    OPERATOR 2 <= ,
    OPERATOR 3 = ,
    OPERATOR 4 >= ,
    OPERATOR 5 > ,
    FUNCTION 1 mytype_cmp(mytype, mytype);
```

Any change to an operator class requires `DROP OPERATOR CLASS` + `CREATE OPERATOR CLASS` — `DESTRUCTIVE`.
