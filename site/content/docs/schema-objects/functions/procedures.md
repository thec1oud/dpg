---
title: "Procedures"
description: "PROCEDURE declarations; procedures may COMMIT mid-execution."
weight: 3
---

Procedures follow the same [two-part syntax](../../../fundamentals/two-part-syntax/) as functions. The key difference: procedures may call `COMMIT` mid-body.

## Basic procedure

```sql
SCHEMA public {
    PROCEDURE process_settlements()
    LANGUAGE plpgsql SECURITY DEFINER
    AS $$
    DECLARE
        v_id settlements.id%TYPE;
    BEGIN
        FOR v_id IN SELECT id FROM settlements WHERE processed = false LOOP
            PERFORM settle_order(v_id);
            COMMIT;
        END LOOP;
    END;
    $$ {
        GRANTS { EXECUTE TO scheduler_role; }
    }
}
```

```sql
-- emits
CREATE OR REPLACE PROCEDURE "public"."process_settlements"()
LANGUAGE plpgsql SECURITY DEFINER
AS $$
DECLARE
    v_id settlements.id%TYPE;
BEGIN
    FOR v_id IN SELECT id FROM settlements WHERE processed = false LOOP
        PERFORM settle_order(v_id);
        COMMIT;
    END LOOP;
END;
$$;

GRANT EXECUTE ON PROCEDURE "public"."process_settlements"() TO "scheduler_role";
```

## Procedure with parameters

```sql
PROCEDURE archive_orders(p_before DATE, p_target_schema TEXT DEFAULT 'archive')
LANGUAGE plpgsql
AS $$
BEGIN
    -- archiving logic
    COMMIT;
END;
$$;
```

```sql
CREATE OR REPLACE PROCEDURE "public"."archive_orders"(
    p_before        date,
    p_target_schema text DEFAULT 'archive'
)
LANGUAGE plpgsql
AS $$
BEGIN
    -- archiving logic
    COMMIT;
END;
$$;
```

## Diffing behaviour

Procedure identity is `(schema, name, argument_types)`. Any body change causes `CREATE OR REPLACE PROCEDURE`. No semantic diff is performed.
