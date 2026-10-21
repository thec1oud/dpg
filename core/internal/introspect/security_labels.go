package introspect

import (
	"context"
	"fmt"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// introspectXSecurityLabels below populate SecurityLabels (RFC Section 14.11) for
// every kind PostgreSQL's real SECURITY LABEL statement supports. Two
// separate system catalogs back this, split by PostgreSQL itself along the
// per-database/shared-across-database boundary — confirmed live (a naive
// single-catalog implementation would have silently returned zero labels
// for roles and tablespaces):
//
//   - pg_seclabel: every per-database object kind (table, column, view,
//     function, procedure, aggregate, type/domain, schema, sequence,
//     publication, event trigger). Keyed by (classoid, objoid, objsubid) —
//     objsubid is the column's attnum for a table column, 0 for everything
//     else.
//   - pg_shseclabel: ROLE, TABLESPACE, and SUBSCRIPTION — three kinds
//     backed by a genuinely shared, cluster-wide catalog (relisshared,
//     confirmed live) rather than a per-database one, despite Subscription
//     having per-database CREATE SUBSCRIPTION syntax like every pg_seclabel
//     kind above (same boundary obj_description/shobj_description already
//     splits on for comments — see introspectSubscriptionSecurityLabels'
//     own doc comment for why this one is easy to get wrong). No objsubid
//     column at all — shared objects never have sub-objects.
//
// Every function below follows introspectXGrants' established shape
// exactly: one query joining the catalog's name back from pg_seclabel/
// pg_shseclabel, matched into the already-built idx map by qualified name,
// silently skipping any row whose object isn't in idx (mirrors
// introspectTableGrants et al. — never expected in practice since the
// query already excludes system schemas via the JOIN with pg_namespace
// where relevant, but keeps this file robust even if that ever drifts).

func introspectSchemaSecurityLabels(ctx context.Context, conn pipeline.Querier, objs []pipeline.IRObject) error {
	idx := make(map[string]*ir.Schema)
	for _, o := range objs {
		if s, ok := o.(*ir.Schema); ok {
			idx[s.Name] = s
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_namespace n ON n.oid = sl.objoid
WHERE  sl.classoid = 'pg_namespace'::regclass AND sl.objsubid = 0
ORDER  BY n.nspname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect schema security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name, provider, label string
		if err := rs.Scan(&name, &provider, &label); err != nil {
			return err
		}
		if s, ok := idx[name]; ok {
			s.SecurityLabels = append(s.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectTableSecurityLabels(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, c.relname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_class c ON c.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  sl.classoid = 'pg_class'::regclass AND sl.objsubid = 0
ORDER  BY n.nspname, c.relname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect table security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, provider, label string
		if err := rs.Scan(&schema, &name, &provider, &label); err != nil {
			return err
		}
		if t, ok := idx[schema+"."+name]; ok {
			t.SecurityLabels = append(t.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

// introspectColumnSecurityLabels is introspectTableSecurityLabels' COLUMN
// counterpart — objsubid is the column's attnum here (always > 0), unlike
// every other per-database kind's fixed 0.
func introspectColumnSecurityLabels(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, c.relname, a.attname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_class c ON c.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_attribute a ON a.attrelid = c.oid AND a.attnum = sl.objsubid
WHERE  sl.classoid = 'pg_class'::regclass AND sl.objsubid > 0
ORDER  BY n.nspname, c.relname, a.attnum, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect column security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, table, col, provider, label string
		if err := rs.Scan(&schema, &table, &col, &provider, &label); err != nil {
			return err
		}
		t, ok := idx[schema+"."+table]
		if !ok {
			continue
		}
		for _, c := range t.Columns {
			if c.Name == col {
				c.SecurityLabels = append(c.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
				break
			}
		}
	}
	return rs.Err()
}

func introspectViewSecurityLabels(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.View) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, c.relname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_class c ON c.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  sl.classoid = 'pg_class'::regclass AND sl.objsubid = 0
ORDER  BY n.nspname, c.relname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect view security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, provider, label string
		if err := rs.Scan(&schema, &name, &provider, &label); err != nil {
			return err
		}
		if v, ok := idx[schema+"."+name]; ok {
			v.SecurityLabels = append(v.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectFunctionSecurityLabels(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Function) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) AS args, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_proc p ON p.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = p.pronamespace
WHERE  sl.classoid = 'pg_proc'::regclass AND sl.objsubid = 0 AND p.prokind = 'f'
ORDER  BY n.nspname, p.proname, args, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect function security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, args, provider, label string
		if err := rs.Scan(&schema, &name, &args, &provider, &label); err != nil {
			return err
		}
		if fn, ok := idx[schema+"."+name+"("+args+")"]; ok {
			fn.SecurityLabels = append(fn.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectProcedureSecurityLabels(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Procedure) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) AS args, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_proc p ON p.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = p.pronamespace
WHERE  sl.classoid = 'pg_proc'::regclass AND sl.objsubid = 0 AND p.prokind = 'p'
ORDER  BY n.nspname, p.proname, args, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect procedure security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, args, provider, label string
		if err := rs.Scan(&schema, &name, &args, &provider, &label); err != nil {
			return err
		}
		if p, ok := idx[schema+"."+name+"("+args+")"]; ok {
			p.SecurityLabels = append(p.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectAggregateSecurityLabels(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Aggregate) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) AS args, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_proc p ON p.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = p.pronamespace
WHERE  sl.classoid = 'pg_proc'::regclass AND sl.objsubid = 0 AND p.prokind = 'a'
ORDER  BY n.nspname, p.proname, args, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect aggregate security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, args, provider, label string
		if err := rs.Scan(&schema, &name, &args, &provider, &label); err != nil {
			return err
		}
		if a, ok := idx[schema+"."+name+"("+args+")"]; ok {
			a.SecurityLabels = append(a.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

// introspectTypeSecurityLabels covers every ir.Type variant at once (ENUM/
// COMPOSITE/RANGE/BASE/DOMAIN) — all live in pg_type regardless of variant,
// so one query and one idx suffice, matching introspectEnumValues'/
// introspectDomainBodies' own established "[]pipeline.IRObject in, filter
// by type assertion" pattern for this kind.
func introspectTypeSecurityLabels(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	idx := make(map[string]*ir.Type, len(types))
	for _, o := range types {
		if t, ok := o.(*ir.Type); ok {
			idx[t.Schema+"."+t.Name] = t
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, t.typname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_type t ON t.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = t.typnamespace
WHERE  sl.classoid = 'pg_type'::regclass AND sl.objsubid = 0
ORDER  BY n.nspname, t.typname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect type security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, provider, label string
		if err := rs.Scan(&schema, &name, &provider, &label); err != nil {
			return err
		}
		if t, ok := idx[schema+"."+name]; ok {
			t.SecurityLabels = append(t.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectSequenceSecurityLabels(ctx context.Context, conn pipeline.Querier, sequences []pipeline.IRObject) error {
	idx := make(map[string]*ir.Sequence, len(sequences))
	for _, o := range sequences {
		if s, ok := o.(*ir.Sequence); ok {
			idx[s.Schema+"."+s.Name] = s
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, c.relname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_class c ON c.oid = sl.objoid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  sl.classoid = 'pg_class'::regclass AND sl.objsubid = 0 AND c.relkind = 'S'
ORDER  BY n.nspname, c.relname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect sequence security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var schema, name, provider, label string
		if err := rs.Scan(&schema, &name, &provider, &label); err != nil {
			return err
		}
		if s, ok := idx[schema+"."+name]; ok {
			s.SecurityLabels = append(s.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

// introspectRoleSecurityLabels reads pg_shseclabel, not pg_seclabel — ROLE
// is cluster-wide (pg_authid has no per-database existence), the same
// boundary shobj_description/obj_description already split on for
// Role.Comment. No objsubid at all: shared objects never have sub-objects.
func introspectRoleSecurityLabels(ctx context.Context, conn pipeline.Querier, roles []pipeline.IRObject) error {
	idx := make(map[string]*ir.Role, len(roles))
	for _, o := range roles {
		if r, ok := o.(*ir.Role); ok {
			idx[r.Name] = r
		}
	}
	if len(idx) == 0 {
		return nil
	}
	// pg_roles, not pg_authid: only a superuser may read pg_authid, and
	// a database's owner must be able to introspect it.
	const q = `
SELECT r.rolname, sl.provider, sl.label
FROM   pg_shseclabel sl
JOIN   pg_roles r ON r.oid = sl.objoid
WHERE  sl.classoid = 'pg_authid'::regclass
ORDER  BY r.rolname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect role security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name, provider, label string
		if err := rs.Scan(&name, &provider, &label); err != nil {
			return err
		}
		if r, ok := idx[name]; ok {
			r.SecurityLabels = append(r.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

// introspectTablespaceSecurityLabels is introspectRoleSecurityLabels'
// TABLESPACE counterpart — the other cluster-wide kind among these 14,
// same pg_shseclabel catalog.
func introspectTablespaceSecurityLabels(ctx context.Context, conn pipeline.Querier, objs []pipeline.IRObject) error {
	idx := make(map[string]*ir.Tablespace, len(objs))
	for _, o := range objs {
		if ts, ok := o.(*ir.Tablespace); ok {
			idx[ts.Name] = ts
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT t.spcname, sl.provider, sl.label
FROM   pg_shseclabel sl
JOIN   pg_tablespace t ON t.oid = sl.objoid
WHERE  sl.classoid = 'pg_tablespace'::regclass
ORDER  BY t.spcname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect tablespace security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name, provider, label string
		if err := rs.Scan(&name, &provider, &label); err != nil {
			return err
		}
		if ts, ok := idx[name]; ok {
			ts.SecurityLabels = append(ts.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectPublicationSecurityLabels(ctx context.Context, conn pipeline.Querier, objs []pipeline.IRObject) error {
	idx := make(map[string]*ir.Publication, len(objs))
	for _, o := range objs {
		if p, ok := o.(*ir.Publication); ok {
			idx[p.Name] = p
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT p.pubname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_publication p ON p.oid = sl.objoid
WHERE  sl.classoid = 'pg_publication'::regclass AND sl.objsubid = 0
ORDER  BY p.pubname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect publication security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name, provider, label string
		if err := rs.Scan(&name, &provider, &label); err != nil {
			return err
		}
		if p, ok := idx[name]; ok {
			p.SecurityLabels = append(p.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

// introspectSubscriptionSecurityLabels reads pg_shseclabel, not pg_seclabel:
// pg_subscription is itself a shared, cluster-wide catalog (relisshared —
// confirmed live, not assumed from its per-database CREATE SUBSCRIPTION
// syntax; easy to get wrong, since every other per-database-syntax kind in
// this file correctly uses pg_seclabel), so its SECURITY LABEL entries live
// alongside ROLE/TABLESPACE's, not with TABLE/FUNCTION/etc's.
func introspectSubscriptionSecurityLabels(ctx context.Context, conn pipeline.Querier, objs []pipeline.IRObject) error {
	idx := make(map[string]*ir.Subscription, len(objs))
	for _, o := range objs {
		if s, ok := o.(*ir.Subscription); ok {
			idx[s.Name] = s
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT s.subname, sl.provider, sl.label
FROM   pg_shseclabel sl
JOIN   pg_subscription s ON s.oid = sl.objoid
WHERE  sl.classoid = 'pg_subscription'::regclass
ORDER  BY s.subname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect subscription security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name, provider, label string
		if err := rs.Scan(&name, &provider, &label); err != nil {
			return err
		}
		if s, ok := idx[name]; ok {
			s.SecurityLabels = append(s.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}

func introspectEventTriggerSecurityLabels(ctx context.Context, conn pipeline.Querier, objs []pipeline.IRObject) error {
	idx := make(map[string]*ir.EventTrigger, len(objs))
	for _, o := range objs {
		if e, ok := o.(*ir.EventTrigger); ok {
			idx[e.Name] = e
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT e.evtname, sl.provider, sl.label
FROM   pg_seclabel sl
JOIN   pg_event_trigger e ON e.oid = sl.objoid
WHERE  sl.classoid = 'pg_event_trigger'::regclass AND sl.objsubid = 0
ORDER  BY e.evtname, sl.provider`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect event trigger security labels: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name, provider, label string
		if err := rs.Scan(&name, &provider, &label); err != nil {
			return err
		}
		if e, ok := idx[name]; ok {
			e.SecurityLabels = append(e.SecurityLabels, pipeline.SecurityLabel{Provider: provider, Label: label})
		}
	}
	return rs.Err()
}
