// Package precondition holds what must be true of a database before a
// migration runs (RFC Section 16.10): the SQL guards each migration
// carries, and the dependents query dpg apply's preflight runs too.
package precondition

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func quoteLit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// splitKey splits a "schema.name" snapshot key.
func splitKey(key string) (schema, name string, ok bool) {
	return strings.Cut(key, ".")
}

// existsExpr is a SQL boolean: the object of kind and key exists, or "" when
// the kind isn't checked.
func existsExpr(kind, key string) string {
	switch kind {
	case "schema":
		return fmt.Sprintf("EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = %s)", quoteLit(key))
	case "role":
		return fmt.Sprintf("EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s)", quoteLit(key))
	case "extension":
		return fmt.Sprintf("EXISTS (SELECT 1 FROM pg_extension WHERE extname = %s)", quoteLit(key))
	case "table", "view", "sequence":
		schema, name, ok := splitKey(key)
		if !ok {
			return ""
		}
		return fmt.Sprintf("EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = %s AND c.relname = %s)", quoteLit(schema), quoteLit(name))
	case "type":
		schema, name, ok := splitKey(key)
		if !ok {
			return ""
		}
		return fmt.Sprintf("EXISTS (SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname = %s AND t.typname = %s)", quoteLit(schema), quoteLit(name))
	}
	return ""
}

// oidExpr is a SQL expression for the object's catalog (classid) and OID,
// or "" when the kind isn't checked.
func oidExpr(kind, key string) (classid, oid string) {
	schema, name, qualified := splitKey(key)
	switch kind {
	case "table", "view", "sequence":
		if !qualified {
			return "", ""
		}
		return "'pg_class'::regclass", fmt.Sprintf("(SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = %s AND c.relname = %s)", quoteLit(schema), quoteLit(name))
	case "type":
		if !qualified {
			return "", ""
		}
		return "'pg_type'::regclass", fmt.Sprintf("(SELECT t.oid FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname = %s AND t.typname = %s)", quoteLit(schema), quoteLit(name))
	case "schema":
		return "'pg_namespace'::regclass", fmt.Sprintf("(SELECT oid FROM pg_namespace WHERE nspname = %s)", quoteLit(key))
	}
	return "", ""
}

// DependentsQuery lists, as DPG-style keys, the objects that depend on the
// object of kind and key in the ordinary way (deptype n): dropping it would
// fail, or drop them with CASCADE. A dependent part of another object (a
// view's rule, an index, a trigger, a constraint, a default) is listed as
// that object; the object's own parts aren't listed. "" when the kind isn't
// checked.
func DependentsQuery(kind, key string) string {
	classid, oid := oidExpr(kind, key)
	if oid == "" {
		return ""
	}
	rel := func(expr string) string {
		return fmt.Sprintf("(SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = %s)", expr)
	}
	return fmt.Sprintf(`SELECT DISTINCT owner FROM (
  SELECT CASE d.classid
    WHEN 'pg_rewrite'::regclass THEN %[3]s
    WHEN 'pg_class'::regclass THEN coalesce(%[4]s, %[5]s)
    WHEN 'pg_constraint'::regclass THEN coalesce(%[6]s, (SELECT n.nspname || '.' || t.typname FROM pg_constraint k JOIN pg_type t ON t.oid = k.contypid JOIN pg_namespace n ON n.oid = t.typnamespace WHERE k.oid = d.objid))
    WHEN 'pg_trigger'::regclass THEN %[7]s
    WHEN 'pg_attrdef'::regclass THEN %[8]s
    WHEN 'pg_proc'::regclass THEN (SELECT n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')' FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE p.oid = d.objid)
    WHEN 'pg_type'::regclass THEN (SELECT n.nspname || '.' || t.typname FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE t.oid = d.objid)
    ELSE pg_describe_object(d.classid, d.objid, d.objsubid)
  END AS owner
  FROM pg_depend d
  WHERE d.refclassid = %[1]s AND d.refobjid = %[2]s AND d.deptype = 'n'
) x WHERE owner IS NOT NULL AND owner <> %[9]s`,
		classid, oid,
		rel("(SELECT r.ev_class FROM pg_rewrite r WHERE r.oid = d.objid)"),
		rel("(SELECT i.indrelid FROM pg_index i WHERE i.indexrelid = d.objid)"),
		rel("d.objid"),
		rel("(SELECT nullif(k.conrelid, 0) FROM pg_constraint k WHERE k.oid = d.objid)"),
		rel("(SELECT t.tgrelid FROM pg_trigger t WHERE t.oid = d.objid)"),
		rel("(SELECT a.adrelid FROM pg_attrdef a WHERE a.oid = d.objid)"),
		quoteLit(key))
}

// Managed reports whether a dependent (as DependentsQuery names it) is one
// of the managed keys. A function is matched by its schema-qualified name
// (DPG writes argument types as the source does).
func Managed(dependent string, managed map[string]bool) bool {
	if managed[dependent] {
		return true
	}
	if i := strings.Index(dependent, "("); i > 0 {
		prefix := dependent[:i+1]
		for k := range managed {
			if strings.HasPrefix(k, prefix) {
				return true
			}
		}
	}
	return false
}

// Unmanaged runs DependentsQuery and returns the dependents not managed.
func Unmanaged(ctx context.Context, q pipeline.Querier, kind, key string, managed map[string]bool) ([]string, error) {
	query := DependentsQuery(kind, key)
	if query == "" {
		return nil, nil
	}
	rows, err := q.QueryRows(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		if !Managed(d, managed) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out, rows.Err()
}

// Guards returns the SQL guards of a migration whose ops are ops:
//   - P1: an object it creates doesn't exist yet (DPG-E051);
//   - P2: no object DPG doesn't manage (not in managed: the objects after
//     the migration and the ones it drops) depends on one it drops or
//     recreates (DPG-E052);
//   - P5: the running role is a member of every role it makes an owner
//     (DPG-E036).
func Guards(ops []pipeline.DiffOp, managed map[string]bool, ownerRoles []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, op := range ops {
		to, ok := op.(pipeline.TargetedOp)
		if !ok {
			continue
		}
		t := to.Target()
		id := t.Kind + "\x00" + t.Key + "\x00" + t.Effect.String()
		if seen[id] {
			continue
		}
		seen[id] = true
		switch t.Effect {
		case pipeline.EffectCreate:
			if e := existsExpr(t.Kind, t.Key); e != "" {
				out = append(out, fmt.Sprintf(`DO $dpg$
BEGIN
  IF %s THEN
    RAISE EXCEPTION 'DPG-E051: the migration creates %s %s, which already exists in this database (DPG doesn''t manage it: dpg resolve can adopt or ignore it)';
  END IF;
END
$dpg$;`, e, t.Kind, strings.ReplaceAll(t.Key, "'", "''")))
			}
		case pipeline.EffectDrop, pipeline.EffectRecreate:
			q := DependentsQuery(t.Kind, t.Key)
			if q == "" {
				continue
			}
			var keys []string
			for k := range managed {
				keys = append(keys, quoteLit(k))
			}
			sort.Strings(keys)
			list := "''"
			if len(keys) > 0 {
				list = strings.Join(keys, ", ")
			}
			// A function dependent is matched by name (Managed).
			out = append(out, fmt.Sprintf(`DO $dpg$
DECLARE
  d text;
BEGIN
  SELECT string_agg(owner, ', ' ORDER BY owner) INTO d FROM (%s) x
    WHERE owner NOT IN (%s)
      AND NOT (owner LIKE '%%(%%' AND split_part(owner, '(', 1) || '(' IN (SELECT split_part(k, '(', 1) || '(' FROM unnest(ARRAY[%s]::text[]) k));
  IF d IS NOT NULL THEN
    RAISE EXCEPTION 'DPG-E052: the migration %s %s %s, but %% depends on it and DPG doesn''t manage it', d;
  END IF;
END
$dpg$;`, q, list, list, effectVerb(t.Effect), t.Kind, strings.ReplaceAll(t.Key, "'", "''")))
		}
	}
	for _, role := range ownerRoles {
		out = append(out, fmt.Sprintf(`DO $dpg$
BEGIN
  IF NOT pg_has_role(current_user, %[1]s, 'MEMBER') THEN
    RAISE EXCEPTION 'DPG-E036: the running role is not a member of %%, which the migration makes an owner', %[1]s;
  END IF;
END
$dpg$;`, quoteLit(role)))
	}
	return out
}

func effectVerb(e pipeline.Effect) string {
	if e == pipeline.EffectRecreate {
		return "drops and recreates"
	}
	return "drops"
}
