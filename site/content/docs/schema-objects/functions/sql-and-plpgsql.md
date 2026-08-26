---
title: "SQL & PL/pgSQL Functions"
description: "Function declarations in all languages: simple SQL, PL/pgSQL, set-returning, variadic, trigger, and named dollar-quoting."
weight: 1
---

Functions are written in complete, unmodified PostgreSQL syntax — `CREATE OR REPLACE FUNCTION` with only the `CREATE OR REPLACE` verb removed. The dollar-quoted body is Part 1. The `{ }` block holds grants, comments, and deprecation notices.

## Simple SQL function

```sql
SCHEMA public {
    FUNCTION active_user_count() RETURNS BIGINT
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$
        SELECT COUNT(*) FROM users WHERE status = 'active';
    $$;
}
```

```sql
-- emits
CREATE OR REPLACE FUNCTION "public"."active_user_count"()
RETURNS bigint
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    SELECT COUNT(*) FROM users WHERE status = 'active';
$$;
```

## PL/pgSQL function with grants and comment

```sql
FUNCTION get_user(p_email TEXT) RETURNS users
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public
AS $$
DECLARE
    v_user users;
BEGIN
    SELECT * INTO STRICT v_user FROM users WHERE email = p_email;
    RETURN v_user;
EXCEPTION
    WHEN NO_DATA_FOUND THEN
        RAISE EXCEPTION 'User not found: %', p_email;
END;
$$ {
    COMMENT 'Fetch a user record by verified email address';
    GRANTS { EXECUTE TO app_service; }
}
```

```sql
-- emits
CREATE OR REPLACE FUNCTION "public"."get_user"(p_email text)
RETURNS users
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = public
AS $$
DECLARE
    v_user users;
BEGIN
    SELECT * INTO STRICT v_user FROM users WHERE email = p_email;
    RETURN v_user;
EXCEPTION
    WHEN NO_DATA_FOUND THEN
        RAISE EXCEPTION 'User not found: %', p_email;
END;
$$;

COMMENT ON FUNCTION "public"."get_user"(text) IS 'Fetch a user record by verified email address';
GRANT EXECUTE ON FUNCTION "public"."get_user"(text) TO "app_service";
```

## Set-returning function (SETOF)

```sql
FUNCTION users_in_org(p_org_id UUID) RETURNS SETOF users
LANGUAGE sql STABLE
AS $$
    SELECT * FROM users WHERE org_id = p_org_id ORDER BY email;
$$;
```

```sql
CREATE OR REPLACE FUNCTION "public"."users_in_org"(p_org_id uuid)
RETURNS SETOF users
LANGUAGE sql STABLE
AS $$
    SELECT * FROM users WHERE org_id = p_org_id ORDER BY email;
$$;
```

## Returns TABLE inline

```sql
FUNCTION monthly_summary(p_year INT)
RETURNS TABLE (month INT, revenue NUMERIC, count BIGINT)
LANGUAGE sql STABLE
AS $$
    SELECT
        EXTRACT(MONTH FROM created_at)::INT,
        SUM(total),
        COUNT(*)
    FROM orders
    WHERE EXTRACT(YEAR FROM created_at) = p_year
    GROUP BY 1 ORDER BY 1;
$$;
```

```sql
CREATE OR REPLACE FUNCTION "public"."monthly_summary"(p_year integer)
RETURNS TABLE ("month" integer, "revenue" numeric, "count" bigint)
LANGUAGE sql STABLE
AS $$
    SELECT
        EXTRACT(MONTH FROM created_at)::INT,
        SUM(total),
        COUNT(*)
    FROM orders
    WHERE EXTRACT(YEAR FROM created_at) = p_year
    GROUP BY 1 ORDER BY 1;
$$;
```

## Variadic arguments

```sql
FUNCTION log_event(p_type TEXT, VARIADIC p_tags TEXT[]) RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO event_log (event_type, tags) VALUES (p_type, p_tags);
END;
$$;
```

```sql
CREATE OR REPLACE FUNCTION "public"."log_event"(p_type text, VARIADIC p_tags text[])
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO event_log (event_type, tags) VALUES (p_type, p_tags);
END;
$$;
```

## Default parameters

```sql
FUNCTION paginate_users(p_limit INT DEFAULT 20, p_offset INT DEFAULT 0)
RETURNS SETOF users
LANGUAGE sql STABLE
AS $$
    SELECT * FROM users LIMIT p_limit OFFSET p_offset;
$$;
```

```sql
CREATE OR REPLACE FUNCTION "public"."paginate_users"(
    p_limit  integer DEFAULT 20,
    p_offset integer DEFAULT 0
)
RETURNS SETOF users
LANGUAGE sql STABLE
AS $$
    SELECT * FROM users LIMIT p_limit OFFSET p_offset;
$$;
```

## Trigger function

```sql
FUNCTION notify_email_change() RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public
AS $$
BEGIN
    PERFORM pg_notify('email_changed', NEW.id::TEXT);
    RETURN NEW;
END;
$$;
```

```sql
CREATE OR REPLACE FUNCTION "public"."notify_email_change"()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public
AS $$
BEGIN
    PERFORM pg_notify('email_changed', NEW.id::TEXT);
    RETURN NEW;
END;
$$;
```

## Named dollar-quoting

Use a named tag when the function body itself contains `$$` literals.

```sql
FUNCTION format_price(p_amount NUMERIC) RETURNS TEXT
LANGUAGE plpgsql IMMUTABLE STRICT
AS $func$
BEGIN
    RETURN '$' || TO_CHAR(p_amount, 'FM999,999,990.00');
END;
$func$ {
    COMMENT 'Format a numeric amount as a dollar price string';
    GRANTS { EXECUTE TO app_readonly, app_service; }
}
```

```sql
CREATE OR REPLACE FUNCTION "public"."format_price"(p_amount numeric)
RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT
AS $func$
BEGIN
    RETURN '$' || TO_CHAR(p_amount, 'FM999,999,990.00');
END;
$func$;

COMMENT ON FUNCTION "public"."format_price"(numeric) IS 'Format a numeric amount as a dollar price string';
GRANT EXECUTE ON FUNCTION "public"."format_price"(numeric) TO "app_readonly", "app_service";
```

## Diffing behaviour

Function identity is `(schema, name, argument_types)`. Any change to the body text (including whitespace) changes the stored hash and causes `CREATE OR REPLACE FUNCTION` to be emitted. No semantic diff is performed.

> Function attributes reference: [Attributes](../attributes/)
