// Package introspect implements pipeline.Introspector. It reads the live
// PostgreSQL catalog (PG 14+) and returns IRObjects equivalent to what the
// compiler would produce from .dpg source files.
package introspect

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyIntrospector, New())
}

// CatalogIntrospector implements pipeline.Introspector.
type CatalogIntrospector struct{}

// New returns a CatalogIntrospector.
func New() *CatalogIntrospector { return &CatalogIntrospector{} }

// Introspect reads the live PG catalog and returns schema objects as IRObjects.
func (ci *CatalogIntrospector) Introspect(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	var all []pipeline.IRObject

	schemas, err := introspectSchemas(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := introspectSchemaGrants(ctx, conn, schemas); err != nil {
		return nil, err
	}
	if err := introspectSchemaSecurityLabels(ctx, conn, schemas); err != nil {
		return nil, err
	}
	all = append(all, schemas...)

	extensions, err := introspectExtensions(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, extensions...)

	tables, err := introspectTables(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, tables...)

	views, err := introspectViews(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, views...)

	if err := introspectViewIndexes(ctx, conn, views); err != nil {
		return nil, err
	}

	functions, err := introspectFunctions(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, functions...)

	types, err := introspectTypes(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, types...)

	sequences, err := introspectSequences(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, sequences...)

	roles, err := introspectRoles(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, roles...)

	aggregates, err := introspectAggregates(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, aggregates...)

	defaultPrivs, err := introspectDefaultPrivileges(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, defaultPrivs...)

	paramPrivs, err := introspectParameterPrivileges(ctx, conn)
	if err != nil {
		return nil, err
	}
	all = append(all, paramPrivs...)

	// Reliable-tier opaque objects: reconstructed CREATE DDL, canonicalised
	// through pg_query so the body hash matches the compiler's. See opaque.go.
	for _, step := range []func(context.Context, pipeline.Querier) ([]pipeline.IRObject, error){
		introspectCollations,
		introspectCasts,
		introspectStatistics,
		introspectEventTriggers,
		introspectForeignDataWrappers,
		introspectForeignServers,
		introspectUserMappings,
		introspectPublications,
		introspectSubscriptions,
		introspectTablespaces,
		introspectOperators,
		introspectTSParsers,
		introspectTSTemplates,
		introspectTSDicts,
		introspectTSConfigs,
		introspectOperatorFamilies,
		introspectOperatorClasses,
	} {
		objs, err := step(ctx, conn)
		if err != nil {
			return nil, err
		}
		all = append(all, objs...)
	}

	return all, nil
}

// ── aggregates ────────────────────────────────────────────────────────────────

// aggModifyModeKeyword maps pg_aggregate's aggfinalmodify/aggmfinalmodify
// "char" catalog encoding to the RFC's modify-mode keyword (Section 9.4) —
// confirmed live: 'r' (the RFC's own default, READ_ONLY) is what a plain
// aggregate with no FINALFUNC_MODIFY declared reports.
func aggModifyModeKeyword(c string) string {
	switch c {
	case "s":
		return "SHAREABLE"
	case "w":
		return "READ_WRITE"
	default:
		return "READ_ONLY"
	}
}

// introspectAggregates reads every real CREATE AGGREGATE option (RFC audit
// item #29) from pg_aggregate, not just the six originally handled here
// (SFUNC/STYPE/INITCOND/FINALFUNC/COMBINEFUNC/SERIALFUNC) — every other
// field is only surfaced when it differs from PostgreSQL's own default (all
// confirmed live, not assumed), matching the "don't render noise"
// convention already used for function COST/ROWS. HYPOTHETICAL aggregates
// (aggkind = 'h', confirmed live via the built-in `rank`/`dense_rank`
// hypothetical-set aggregates) are a distinct, separate PostgreSQL grammar
// form (direct-args ORDER BY agg-args) that ir.Aggregate.Args doesn't model
// at all — a pre-existing gap, out of this fix's scope — so HYPOTHETICAL's
// bare flag is still surfaced here for round-trip completeness on the
// option itself, but reconstructing such an aggregate's full declaration
// may remain incomplete for that separate reason.
func introspectAggregates(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT n.nspname, p.proname,
       pg_get_function_identity_arguments(p.oid) AS args,
       pg_catalog.oidvectortypes(p.proargtypes)   AS arg_types,
       obj_description(p.oid, 'pg_proc') AS comment,
       a.aggtransfn::text                AS sfunc,
       a.aggtranstype::regtype::text     AS stype,
       a.agginitval                      AS initcond,
       NULLIF(a.aggfinalfn::text, '-')   AS finalfunc,
       NULLIF(a.aggcombinefn::text, '-') AS combinefunc,
       NULLIF(a.aggserialfn::text, '-')  AS serialfunc,
       NULLIF(a.aggdeserialfn::text, '-')  AS deserialfunc,
       NULLIF(a.aggmtransfn::text, '-')    AS msfunc,
       NULLIF(a.aggminvtransfn::text, '-') AS minvfunc,
       NULLIF(a.aggmtranstype::regtype::text, '-') AS mstype,
       NULLIF(a.aggmfinalfn::text, '-')    AS mfinalfunc,
       a.aggminitval                       AS minitcond,
       a.aggkind::text                     AS aggkind,
       a.aggfinalextra,
       a.aggmfinalextra,
       a.aggfinalmodify::text  AS finalmodify,
       a.aggmfinalmodify::text AS mfinalmodify,
       a.aggtransspace,
       a.aggmtransspace,
       o.oprname AS sortop,
       CASE p.proparallel WHEN 'r' THEN 'RESTRICTED' WHEN 's' THEN 'SAFE' ELSE 'UNSAFE' END AS parallel,
       r.rolname                         AS owner
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace
JOIN   pg_aggregate a ON a.aggfnoid = p.oid
JOIN   pg_roles r ON r.oid = p.proowner
LEFT JOIN pg_operator o ON o.oid = a.aggsortop
WHERE  p.prokind = 'a'
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
ORDER  BY n.nspname, p.proname, args`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect aggregates: %w", err)
	}
	defer rs.Close()

	aggIdx := make(map[string]*ir.Aggregate)
	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, args, argTypes, sfunc, stype, aggkind, finalmodify, mfinalmodify, parallel, owner string
		var comment, initcond, finalfunc, combinefunc, serialfunc, deserialfunc, msfunc, minvfunc, mstype, mfinalfunc, minitcond, sortop *string
		var finalextra, mfinalextra bool
		var transspace, mtransspace int
		if err := rs.Scan(&schema, &name, &args, &argTypes, &comment,
			&sfunc, &stype, &initcond, &finalfunc, &combinefunc, &serialfunc,
			&deserialfunc, &msfunc, &minvfunc, &mstype, &mfinalfunc, &minitcond,
			&aggkind, &finalextra, &mfinalextra, &finalmodify, &mfinalmodify,
			&transspace, &mtransspace, &sortop, &parallel, &owner); err != nil {
			return nil, err
		}
		agg := &ir.Aggregate{
			Schema:  schema,
			Name:    name,
			Comment: comment,
			Owner:   &owner,
		}
		// Use argTypes (type-only, from oidvectortypes) so QualifiedName matches
		// ir.ArgsKey(). Keep args (with parameter names) for the grants index key.
		if argTypes != "" {
			for a := range strings.SplitSeq(argTypes, ", ") {
				agg.Args = append(agg.Args, ir.FuncArg{Type: ir.TypeRef{Name: strings.TrimSpace(a)}})
			}
		}
		agg.Options = append(agg.Options, pipeline.StorageParam{Key: "sfunc", Value: sfunc})
		agg.Options = append(agg.Options, pipeline.StorageParam{Key: "stype", Value: stype})
		if initcond != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "initcond", Value: quoteLit(*initcond)})
		}
		if finalfunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "finalfunc", Value: *finalfunc})
		}
		if finalextra {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "finalfunc_extra"})
		}
		if finalmodify != "r" {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "finalfunc_modify", Value: aggModifyModeKeyword(finalmodify)})
		}
		if combinefunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "combinefunc", Value: *combinefunc})
		}
		if serialfunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "serialfunc", Value: *serialfunc})
		}
		if deserialfunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "deserialfunc", Value: *deserialfunc})
		}
		if transspace != 0 {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "sspace", Value: fmt.Sprintf("%d", transspace)})
		}
		if msfunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "msfunc", Value: *msfunc})
		}
		if minvfunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "minvfunc", Value: *minvfunc})
		}
		if mstype != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "mstype", Value: *mstype})
		}
		if mtransspace != 0 {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "msspace", Value: fmt.Sprintf("%d", mtransspace)})
		}
		if mfinalfunc != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "mfinalfunc", Value: *mfinalfunc})
		}
		if mfinalextra {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "mfinalfunc_extra"})
		}
		if mfinalmodify != "r" {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "mfinalfunc_modify", Value: aggModifyModeKeyword(mfinalmodify)})
		}
		if minitcond != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "minitcond", Value: quoteLit(*minitcond)})
		}
		if sortop != nil {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "sortop", Value: *sortop})
		}
		if parallel != "UNSAFE" {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "parallel", Value: parallel})
		}
		if aggkind == "h" {
			agg.Options = append(agg.Options, pipeline.StorageParam{Key: "hypothetical"})
		}
		optParts := make([]string, len(agg.Options))
		for i, p := range agg.Options {
			if p.Value == "" {
				optParts[i] = strings.ToUpper(p.Key)
			} else {
				optParts[i] = p.Key + " = " + p.Value
			}
		}
		argParts := make([]string, len(agg.Args))
		for i, a := range agg.Args {
			argParts[i] = a.Type.String()
		}
		agg.Body = fmt.Sprintf("CREATE AGGREGATE %s (%s) (%s)",
			qualIdentQ(schema, name), strings.Join(argParts, ", "), strings.Join(optParts, ", "))

		aggIdx[schema+"."+name+"("+args+")"] = agg
		out = append(out, agg)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	if err := introspectAggregateGrants(ctx, conn, aggIdx); err != nil {
		return nil, err
	}
	if err := introspectAggregateSecurityLabels(ctx, conn, aggIdx); err != nil {
		return nil, err
	}
	return out, nil
}

// functionLikeACLMaterializedKeys returns the set of "schema.name(args)" keys
// (args = pg_get_function_identity_arguments, matching every prokind-scoped
// grants query's own idx key format) for functions/procedures/aggregates
// (selected by prokind: 'f'/'p'/'a') whose pg_proc.proacl has been
// materialized (is NOT NULL) — i.e. at least one explicit GRANT/REVOKE has
// ever touched the object, as opposed to one still carrying Postgres's pure
// implicit-default ACL (proacl IS NULL, PUBLIC's default EXECUTE applies,
// nothing to declare).
//
// This distinction is invisible to aclexplode(proacl) alone: an object whose
// PUBLIC EXECUTE was never granted (proacl IS NULL) and one whose PUBLIC
// EXECUTE was explicitly revoked (proacl IS NOT NULL, no PUBLIC row) both
// produce zero PUBLIC rows from aclexplode. Each *Grants function below uses
// this to synthesize an explicit "REVOKE EXECUTE FROM PUBLIC" Revocation for
// the latter case only — without it, dump→reapply silently restores PUBLIC's
// implicit default on every explicitly-locked-down function-like object,
// undoing a real security decision (see RFC audit item C.4).
func functionLikeACLMaterializedKeys(ctx context.Context, conn pipeline.Querier, prokind string) (map[string]bool, error) {
	const q = `
SELECT n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) AS args
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace
WHERE  p.prokind = $1
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    p.proacl IS NOT NULL`

	rs, err := conn.QueryRows(ctx, q, prokind)
	if err != nil {
		return nil, fmt.Errorf("introspect function-like ACL materialization: %w", err)
	}
	defer rs.Close()

	out := make(map[string]bool)
	for rs.Next() {
		var schema, name, args string
		if err := rs.Scan(&schema, &name, &args); err != nil {
			return nil, err
		}
		out[schema+"."+name+"("+args+")"] = true
	}
	return out, rs.Err()
}

func introspectAggregateGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Aggregate) error {
	const q = `
SELECT n.nspname, p.proname,
       pg_get_function_identity_arguments(p.oid) AS args,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace,
       LATERAL aclexplode(p.proacl) a
WHERE  p.prokind = 'a'
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, p.proname, args, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect aggregate grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, name, args, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, args, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &args, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, args, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		agg, ok := idx[k.schema+"."+k.name+"("+k.args+")"]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		agg.Grants = append(agg.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}

	materialized, err := functionLikeACLMaterializedKeys(ctx, conn, "a")
	if err != nil {
		return err
	}
	for key, agg := range idx {
		if !materialized[key] || hasPublicGrant(agg.Grants) {
			continue
		}
		agg.Revocations = append(agg.Revocations, ir.Revocation{
			Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"},
		})
	}
	return nil
}

// hasPublicGrant reports whether any grant in grants names the PUBLIC
// pseudo-role — used by *Grants functions to decide whether a materialized
// ACL's missing PUBLIC row means "explicitly revoked" (see
// functionLikeACLMaterializedKeys's doc comment).
func hasPublicGrant(grants []ir.Grant) bool {
	for _, g := range grants {
		if slices.Contains(g.Roles, "PUBLIC") {
			return true
		}
	}
	return false
}

// ── default privileges ──────────────────────────────────────────────────────

// defaclObjTypeNames maps pg_default_acl.defaclobjtype's single-char code to
// the ON <type> keyword real PostgreSQL's own ALTER DEFAULT PRIVILEGES
// grammar uses (confirmed live via \h ALTER DEFAULT PRIVILEGES: TABLES,
// SEQUENCES, FUNCTIONS, TYPES, SCHEMAS).
var defaclObjTypeNames = map[string]string{
	"r": "TABLES",
	"S": "SEQUENCES",
	"f": "FUNCTIONS",
	"T": "TYPES",
	"n": "SCHEMAS",
}

// introspectDefaultPrivileges reads pg_default_acl, one *ir.DefaultPrivileges
// per (role, schema, object type) tuple — matching the catalog's own model,
// and Builder.BuildDefaultPrivileges's identical split for the compiled-
// source side (see ir.DefaultPrivileges.QualifiedName). defaclnamespace == 0
// means "no schema restriction" (a database-wide default), represented as a
// nil InSchema, matching how the compiler leaves it unset when no "IN
// SCHEMA" clause was declared.
func introspectDefaultPrivileges(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// grantor <> grantee excludes the self-grant PostgreSQL materializes for
	// the default-privilege owner the moment an explicit ALTER DEFAULT
	// PRIVILEGES ... GRANT touches the entry (same mechanism as relacl/
	// proacl/typacl self-grants elsewhere in this file) — without this, that
	// synthesized entry reads back as an "extra" live grant and corrupts
	// dpg dump with a phantom GRANTS {} block.
	const q = `
SELECT r.rolname AS for_role, n.nspname AS in_schema, d.defaclobjtype::text,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       a.privilege_type, a.is_grantable
FROM   pg_default_acl d
JOIN   pg_roles r ON r.oid = d.defaclrole
LEFT   JOIN pg_namespace n ON n.oid = NULLIF(d.defaclnamespace, 0),
       LATERAL aclexplode(d.defaclacl) a
WHERE  (n.nspname IS NULL OR n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast'))
AND    a.grantor <> a.grantee
ORDER  BY r.rolname, n.nspname, d.defaclobjtype, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect default privileges: %w", err)
	}
	defer rs.Close()

	type dpKey struct{ forRole, inSchema, objType string }
	type grantKey struct {
		dpKey
		grantee string
	}
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var grantOrder []grantKey
	dpSeen := make(map[dpKey]bool)
	var dpOrder []dpKey

	for rs.Next() {
		var forRole, objType, grantee, priv string
		var inSchema *string
		var grantable bool
		if err := rs.Scan(&forRole, &inSchema, &objType, &grantee, &priv, &grantable); err != nil {
			return nil, err
		}
		schemaVal := ""
		if inSchema != nil {
			schemaVal = *inSchema
		}
		dk := dpKey{forRole: forRole, inSchema: schemaVal, objType: objType}
		if !dpSeen[dk] {
			dpSeen[dk] = true
			dpOrder = append(dpOrder, dk)
		}
		gk := grantKey{dpKey: dk, grantee: grantee}
		e, ok := grants[gk]
		if !ok {
			e = &grantEntry{}
			grants[gk] = e
			grantOrder = append(grantOrder, gk)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}

	dpIdx := make(map[dpKey]*ir.DefaultPrivileges, len(dpOrder))
	out := make([]pipeline.IRObject, 0, len(dpOrder))
	for _, dk := range dpOrder {
		objType := defaclObjTypeNames[dk.objType]
		if objType == "" {
			objType = dk.objType // unrecognized code: pass through raw rather than silently drop
		}
		role := dk.forRole
		dp := &ir.DefaultPrivileges{ForRole: &role, ObjectType: objType}
		if dk.inSchema != "" {
			schema := dk.inSchema
			dp.InSchema = &schema
		}
		dpIdx[dk] = dp
		out = append(out, dp)
	}
	for _, gk := range grantOrder {
		dp := dpIdx[gk.dpKey]
		e := grants[gk]
		dp.Grants = append(dp.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{gk.grantee},
			WithGrant:  e.grantable,
		})
	}
	return out, nil
}

// introspectParameterPrivileges reads pg_parameter_acl (RFC Section 11.6,
// PG15+ — pg_parameter_acl doesn't exist on older servers, hence the
// version guard), returning a single *ir.ParameterPrivileges — PostgreSQL's
// pg_parameter_acl has one row per parameter, with no role/schema/type
// dimension to split multiple IRObjects on, matching
// Builder.BuildParameterPrivileges's identical singleton model.
// introspectSimpleGrants' addGrant callback fires once per (parameter,
// grantee) pair, so it can't recover DPG source's own "SET ON PARAMETER a, b
// TO role" multi-parameter grouping — dump necessarily re-expands each into
// its own single-parameter GRANT statement, semantically equivalent but less
// compact than hand-authored source (the same "dump doesn't reconstruct the
// exact original syntactic shape" limitation every other simple-grant kind
// already has).
func introspectParameterPrivileges(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	if serverVersionNum(ctx, conn) < 150000 {
		return nil, nil
	}
	pp := &ir.ParameterPrivileges{}
	if err := introspectSimpleGrants(ctx, conn, "pg_parameter_acl", "parname", "paracl", func(name string, g ir.Grant) {
		pp.Grants = append(pp.Grants, ir.ParameterGrant{
			Privileges: g.Privileges,
			Parameters: []string{name},
			Roles:      g.Roles,
			WithGrant:  g.WithGrant,
			GrantedBy:  g.GrantedBy,
		})
	}); err != nil {
		return nil, err
	}
	if len(pp.Grants) == 0 {
		return nil, nil
	}
	return []pipeline.IRObject{pp}, nil
}

// ── schemas ───────────────────────────────────────────────────────────────────

// introspectSchemas reads every managed schema, including "public". Unlike
// "information_schema"/"pg_%" (genuinely PostgreSQL-internal, never user-
// managed), "public" is a real, commonly-granted-on schema that used to be
// hard-excluded here — making its actual Owner/Comment/Grants state
// completely invisible to `dpg dump` and `plan --live`/`verify` drift
// detection (confirmed live: a `GRANT USAGE ON SCHEMA public` was silently
// dropped by dump and never flagged as drift — RFC audit item C.2). The
// differ's own diffing/dump-render logic for ir.Schema was already correct
// once given a snapshot to compare against; only introspection's read side
// was excluding it. The corresponding "never propose DROP SCHEMA public"
// lifecycle guard lives in differ.go's Pass 2 (schemas/tables/etc. absent
// from desired but present in snap) — "public" always exists in PostgreSQL
// and is never dropped, the same reasoning compiler.go already documents
// for why it's skipped from directory-based synthetic schema declarations.
func introspectSchemas(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT n.nspname,
       r.rolname AS owner,
       obj_description(n.oid, 'pg_namespace') AS comment
FROM   pg_namespace n
JOIN   pg_roles r ON r.oid = n.nspowner
WHERE  n.nspname NOT LIKE 'pg_%'
AND    n.nspname != 'information_schema'
ORDER  BY n.nspname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect schemas: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, owner string
		var comment *string
		if err := rs.Scan(&name, &owner, &comment); err != nil {
			return nil, err
		}
		out = append(out, &ir.Schema{Name: name, Owner: &owner, Comment: comment})
	}
	return out, rs.Err()
}

// introspectSchemaGrants populates Schema.Grants for every schema in objs
// using aclexplode on pg_namespace.nspacl, mirroring introspectTableGrants.
// Includes "public" — see introspectSchemas' doc comment (RFC audit item
// C.2); the grantee CASE's 'PUBLIC' pseudo-role string is unrelated to and
// not to be confused with the schema literally named "public".
func introspectSchemaGrants(ctx context.Context, conn pipeline.Querier, objs []pipeline.IRObject) error {
	idx := make(map[string]*ir.Schema, len(objs))
	for _, o := range objs {
		if s, ok := o.(*ir.Schema); ok {
			idx[s.Name] = s
		}
	}
	if len(idx) == 0 {
		return nil
	}

	// aclexplode(NULL) returns 0 rows on PG14+, so no COALESCE needed.
	const q = `
SELECT n.nspname,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_namespace n,
       LATERAL aclexplode(n.nspacl) a
WHERE  n.nspname NOT LIKE 'pg_%'
AND    n.nspname != 'information_schema'
AND    a.grantor <> a.grantee
ORDER  BY n.nspname, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect schema grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		s, ok := idx[k.schema]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		s.Grants = append(s.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}
	return nil
}

// ── extensions ────────────────────────────────────────────────────────────────

// introspectExtensions populates ir.Extension.Comment only when it differs
// from the extension's own control-file default: CREATE EXTENSION
// auto-populates obj_description from the .control file's `comment` field
// with zero explicit COMMENT ON EXTENSION ever run (confirmed live —
// postgres_fdw's real obj_description right after a bare CREATE EXTENSION
// is already "foreign-data wrapper for remote PostgreSQL servers"). Without
// this, an undeclared (nil) source Comment permanently mismatched that
// live default in diffExtension, producing a spurious `COMMENT ON
// EXTENSION ... IS NULL` on every plan for any extension whose control
// file sets one — the same "only report a non-default value" convention
// already used for Table.AccessMethod ("heap") and Index's opclass
// ("btree"). pg_available_extension_versions (not pg_available_extensions,
// which has only one row per name) is joined on both name AND the
// installed version, since the control-file comment is version-scoped.
func introspectExtensions(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT e.extname,
       n.nspname AS schema,
       e.extversion,
       obj_description(e.oid, 'pg_extension') AS comment,
       av.comment AS default_comment
FROM   pg_extension e
JOIN   pg_namespace n ON n.oid = e.extnamespace
LEFT   JOIN pg_available_extension_versions av
       ON av.name = e.extname AND av.version = e.extversion
WHERE  n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER  BY e.extname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect extensions: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, schema, version string
		var comment, defaultComment *string
		if err := rs.Scan(&name, &schema, &version, &comment, &defaultComment); err != nil {
			return nil, err
		}
		if comment != nil && defaultComment != nil && *comment == *defaultComment {
			comment = nil
		}
		out = append(out, &ir.Extension{Name: name, Schema: &schema, Version: &version, Comment: comment})
	}
	return out, rs.Err()
}

// ── tables ────────────────────────────────────────────────────────────────────

// replicaIdentityFromCatalog maps pg_class.relreplident ('d'=default,
// 'f'=full, 'n'=nothing, 'i'=index) plus the index name (only populated
// server-side when relreplident == 'i') to ir.ReplicaIdentity. "DEFAULT" is
// PostgreSQL's own default and left as the zero-value Mode, matching how a
// source declaration that omits the directive builds the same zero value —
// so an unmodified table never shows spurious drift after introspection.
func replicaIdentityFromCatalog(replident string, indexName *string) ir.ReplicaIdentity {
	switch replident {
	case "f":
		return ir.ReplicaIdentity{Mode: "FULL"}
	case "n":
		return ir.ReplicaIdentity{Mode: "NOTHING"}
	case "i":
		name := ""
		if indexName != nil {
			name = *indexName
		}
		return ir.ReplicaIdentity{Mode: "INDEX", IndexName: name}
	default:
		return ir.ReplicaIdentity{}
	}
}

func introspectTables(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT c.relname, n.nspname, c.relpersistence::text, c.relkind::text,
       r.rolname AS owner,
       obj_description(c.oid, 'pg_class') AS comment,
       c.relrowsecurity, c.relforcerowsecurity,
       fs.srvname, ft.ftoptions,
       ts.spcname AS tablespace,
       c.relreplident::text,
       (SELECT ic.relname FROM pg_index pi JOIN pg_class ic ON ic.oid = pi.indexrelid
          WHERE pi.indrelid = c.oid AND pi.indisreplident) AS replident_index,
       (SELECT ic2.relname FROM pg_index pi2 JOIN pg_class ic2 ON ic2.oid = pi2.indexrelid
          WHERE pi2.indrelid = c.oid AND pi2.indisclustered) AS cluster_index,
       CASE WHEN c.reloftype <> 0 THEN pg_catalog.format_type(c.reloftype, NULL) END AS of_type,
       am.amname AS access_method,
       c.reloptions
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_roles r     ON r.oid = c.relowner
LEFT   JOIN pg_foreign_table ft  ON ft.ftrelid = c.oid
LEFT   JOIN pg_foreign_server fs ON fs.oid = ft.ftserver
LEFT   JOIN pg_tablespace ts     ON ts.oid = c.reltablespace
LEFT   JOIN pg_am am          ON am.oid = c.relam
WHERE  c.relkind IN ('r', 'p', 'f')
AND    NOT c.relispartition
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER  BY n.nspname, c.relname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect tables: %w", err)
	}
	defer rs.Close()

	var tables []*ir.Table
	tableIdx := map[string]*ir.Table{}

	for rs.Next() {
		var name, schema, persistence, relkind, owner, replident string
		var comment, server, tablespace, replidentIndex, clusterIndex *string
		var ofType, accessMethod *string
		var rlsEnabled, rlsForced bool
		var ftoptions, reloptions []string
		if err := rs.Scan(&name, &schema, &persistence, &relkind, &owner, &comment, &rlsEnabled, &rlsForced, &server, &ftoptions, &tablespace, &replident, &replidentIndex, &clusterIndex, &ofType, &accessMethod, &reloptions); err != nil {
			return nil, err
		}
		t := &ir.Table{
			Schema:          schema,
			Name:            name,
			Unlogged:        persistence == "u",
			Foreign:         relkind == "f",
			Owner:           &owner,
			Comment:         comment,
			RLSEnabled:      rlsEnabled,
			RLSForced:       rlsForced,
			Tablespace:      tablespace,
			ReplicaIdentity: replicaIdentityFromCatalog(replident, replidentIndex),
			ClusterOn:       clusterIndex,
		}
		if ofType != nil {
			// format_type() already omits the schema when the type is
			// visible unqualified via search_path (the common case for a
			// same-schema reference) — matching typeNameToRef's identical
			// treatment of an unqualified "OF type_name" in source, the same
			// convention introspectDomainBodies' base-type text already
			// uses. Stored directly in Name (Schema left "") rather than
			// split, so String() reproduces exactly this text.
			t.OfType = ir.TypeRef{Name: *ofType}
		}
		// "heap" is PostgreSQL's own built-in default table access method —
		// same "only report a non-default value" convention as Index's
		// identical "btree" suppression (see dump.go/differ.go's `!=
		// "btree"` guards), since every table always has a concrete relam
		// regardless of whether USING was ever explicitly declared.
		if accessMethod != nil && *accessMethod != "heap" {
			t.AccessMethod = *accessMethod
		}
		for _, kv := range reloptions {
			if k, v, ok := strings.Cut(kv, "="); ok {
				t.StorageParams = append(t.StorageParams, pipeline.StorageParam{Key: k, Value: v})
			}
		}
		if t.Foreign {
			t.ForeignServer = server
			for _, kv := range ftoptions {
				if k, v, ok := strings.Cut(kv, "="); ok {
					t.ForeignOptions = append(t.ForeignOptions, pipeline.StorageParam{Key: k, Value: v})
				}
			}
		}
		tables = append(tables, t)
		tableIdx[schema+"."+name] = t
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	if err := introspectColumns(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectConstraints(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectIndexes(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectPolicies(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectTriggers(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectPartitions(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectTableInherits(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectTableSecurityLabels(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectColumnSecurityLabels(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectTableGrants(ctx, conn, tableIdx); err != nil {
		return nil, err
	}
	if err := introspectColumnGrants(ctx, conn, tableIdx); err != nil {
		return nil, err
	}

	out := make([]pipeline.IRObject, len(tables))
	for i, t := range tables {
		out[i] = t
	}
	return out, nil
}

// introspectColumns populates Table.Columns. relkind matches both 'r'
// (ordinary table) and 'p' (partitioned parent) — a partitioned table's own
// columns live in pg_attribute the same as an ordinary table's, but omitting
// 'p' here silently left every partitioned table's Columns empty.
// serialMarkerFromWidth maps a format_type() result to the canonical
// ir.Column.Serial marker spelling for a SERIAL-owned column, mirroring
// internal/ir/typeutil.go's serialUnderlyingType in reverse (that function
// maps a source-declared SERIAL name to its underlying type; this one maps
// the underlying type read back from a live catalog to the marker).
func serialMarkerFromWidth(dataType string) (string, bool) {
	switch dataType {
	case "smallint":
		return "SMALLSERIAL", true
	case "integer":
		return "SERIAL", true
	case "bigint":
		return "BIGSERIAL", true
	default:
		return "", false
	}
}

func introspectColumns(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	const q = `
SELECT n.nspname, c.relname,
       a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
       a.attnotnull,
       NULLIF(a.attidentity::text, '') AS identity_kind,
       NULLIF(a.attgenerated::text, '') AS generated_kind,
       pg_get_expr(d.adbin, d.adrelid) AS col_default,
       col_description(a.attrelid, a.attnum) AS comment,
       a.attstattarget,
       CASE a.attcompression WHEN '' THEN NULL ELSE a.attcompression::text END AS compression,
       CASE a.attstorage
           WHEN 'p' THEN 'PLAIN' WHEN 'e' THEN 'EXTERNAL'
           WHEN 'm' THEN 'MAIN'  WHEN 'x' THEN 'EXTENDED'
           ELSE NULL
       END AS storage,
       a.attstorage = t.typstorage AS storage_is_type_default,
       EXISTS (
           SELECT 1 FROM pg_depend dep
           JOIN pg_class sc ON sc.oid = dep.objid AND sc.relkind = 'S'
           WHERE dep.deptype = 'a'
             AND dep.classid = 'pg_class'::regclass
             AND dep.refclassid = 'pg_class'::regclass
             AND dep.refobjid = a.attrelid
             AND dep.refobjsubid = a.attnum
       ) AS serial_owned,
       ids.seqincrement, ids.seqmin, ids.seqmax, ids.seqstart, ids.seqcache, ids.seqcycle
FROM   pg_attribute a
JOIN   pg_class c     ON c.oid = a.attrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_type t      ON t.oid = a.atttypid
LEFT   JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
-- ids resolves an identity column's backing sequence (deptype 'i', the
-- catalog's own marker for GENERATED ... AS IDENTITY, distinct from
-- SERIAL's 'a') to its live options — previously never selected at all, so
-- a declared START WITH/INCREMENT BY/MINVALUE/MAXVALUE/CACHE/CYCLE was
-- silently discarded on every parse and could never round-trip through
-- dpg dump either (RFC audit finding: real data loss on identity columns).
LEFT   JOIN LATERAL (
           SELECT sq.seqincrement, sq.seqmin, sq.seqmax, sq.seqstart, sq.seqcache, sq.seqcycle
           FROM   pg_depend dep
           JOIN   pg_sequence sq ON sq.seqrelid = dep.objid
           WHERE  dep.deptype = 'i'
             AND  dep.classid = 'pg_class'::regclass
             AND  dep.refclassid = 'pg_class'::regclass
             AND  dep.refobjid = a.attrelid
             AND  dep.refobjsubid = a.attnum
       ) ids ON a.attidentity <> ''
WHERE  a.attnum > 0
AND    NOT a.attisdropped
-- attislocal excludes columns present only via classic table INHERITS
-- (not partitioning, already excluded above) — Table.Columns mirrors what
-- a DPG declaration itself lists, not what a parent contributes implicitly.
AND    a.attislocal
AND    c.relkind IN ('r', 'p', 'f')
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, a.attnum`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect columns: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, table, name, dataType string
		var notNull, storageIsDefault, serialOwned bool
		var identityKind, generatedKind, def, comment, compression, storage *string
		var stats *int
		var idIncrement, idMin, idMax, idStart, idCache *int64
		var idCycle *bool
		if err := rs.Scan(&schema, &table, &name, &dataType, &notNull, &identityKind, &generatedKind, &def, &comment, &stats, &compression, &storage, &storageIsDefault, &serialOwned, &idIncrement, &idMin, &idMax, &idStart, &idCache, &idCycle); err != nil {
			return err
		}
		t, ok := idx[schema+"."+table]
		if !ok {
			continue
		}
		col := &ir.Column{
			Name:                 name,
			Type:                 ir.TypeRef{Name: dataType},
			NotNull:              notNull,
			Comment:              comment,
			Compression:          compression,
			Storage:              storage,
			StorageIsTypeDefault: storageIsDefault,
		}
		switch {
		case identityKind != nil && (*identityKind == "a" || *identityKind == "d"):
			// idMin/idMax always come back as concrete bounds from pg_sequence
			// (PostgreSQL doesn't retain "an explicit NO MINVALUE/NO MAXVALUE
			// was written" as a distinct catalog bit, same as Sequence's own
			// introspection), so NoMinValue/NoMaxValue are never set here —
			// only ever true for a source-declared identity, mirroring
			// introspectSequences' identical asymmetry.
			col.Identity = &ir.Identity{
				Always:      *identityKind == "a",
				IncrementBy: idIncrement,
				MinValue:    idMin,
				MaxValue:    idMax,
				StartValue:  idStart,
				Cache:       idCache,
				Cycle:       idCycle,
			}
		case serialOwned && def != nil:
			// dataType is already the real underlying type (format_type()
			// on atttypid never returns "serial" — that's a source-syntax
			// pseudo-type, PostgreSQL's own catalog never stores it), so no
			// Type translation is needed, only picking the Serial marker
			// off the underlying width. Default stays nil, mirroring the
			// Identity branches above — a column whose default is a
			// deptype-'a'-owned-sequence nextval() is DPG's SERIAL shape,
			// never rendered/diffed as an ordinary Default expression.
			if marker, ok := serialMarkerFromWidth(dataType); ok {
				col.Serial = &marker
			} else if def != nil {
				// A hand-rolled owned-sequence-plus-nextval-default on a
				// non-integer column is legal PG but not SERIAL sugar —
				// falls through to plain Default handling, same as before
				// this change.
				stripped := stripStringLiteralCasts(*def)
				col.Default = &stripped
			}
		case generatedKind != nil && (*generatedKind == "s" || *generatedKind == "v") && def != nil:
			// "s" = STORED, "v" = VIRTUAL (PG18+, RFC Section 7.2) — both
			// populate pg_attrdef identically (confirmed live: a VIRTUAL
			// column's generation expression is retrievable via
			// pg_get_expr(d.adbin, d.adrelid) the same way a STORED one's
			// is, despite VIRTUAL never occupying physical storage).
			col.Generated = &ir.Generated{Expr: *def, Stored: *generatedKind == "s"}
		default:
			if def != nil {
				stripped := stripStringLiteralCasts(*def)
				col.Default = &stripped
			}
		}
		if stats != nil && *stats > 0 {
			col.Statistics = stats
		}
		t.Columns = append(t.Columns, col)
	}
	return rs.Err()
}

// introspectConstraints populates Table.Constraints. See introspectColumns
// for why relkind must include 'p' (partitioned parent) alongside 'r'.
func introspectConstraints(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	// conenforced/condeferred: conenforced is a genuinely new PostgreSQL 18
	// catalog column (ENFORCED/NOT ENFORCED didn't exist in any form
	// before) — querying it directly against an older server errors
	// outright, unlike connoinherit above (an existing pre-18 column, only
	// newly meaningful for contype='n' rows). Select-guarded the same way
	// introspectSubscriptions guards its own version-gated columns: a
	// literal "true" substituted on <18 (every constraint is enforced —
	// PostgreSQL's only possible state before this existed).
	enforcedCol := "true"
	if serverVersionNum(ctx, conn) >= 180000 {
		enforcedCol = "con.conenforced"
	}
	q := fmt.Sprintf(`
SELECT n.nspname, c.relname,
       con.conname,
       CASE con.contype
           WHEN 'p' THEN 'PRIMARY KEY' WHEN 'u' THEN 'UNIQUE'
           WHEN 'c' THEN 'CHECK'       WHEN 'f' THEN 'FOREIGN KEY'
           WHEN 'x' THEN 'EXCLUDE'     WHEN 'n' THEN 'NOT NULL'
           ELSE con.contype::text
       END AS con_type,
       pg_get_constraintdef(con.oid) AS def,
       NOT con.convalidated AS not_valid,
       con.condeferrable AS deferrable,
       con.condeferred AS initially_deferred,
       con.connoinherit AS no_inherit,
       NOT (%s) AS not_enforced,
       CASE WHEN con.contype IN ('p','u','f','n') AND array_length(con.conkey, 1) = 1
            THEN (SELECT a.attname FROM pg_attribute a
                  WHERE  a.attrelid = con.conrelid AND a.attnum = con.conkey[1])
            ELSE NULL
       END AS single_col,
       obj_description(con.oid, 'pg_constraint') AS comment
FROM   pg_constraint con
JOIN   pg_class c     ON c.oid = con.conrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  c.relkind IN ('r', 'p', 'f')
-- conislocal excludes a CHECK constraint present on a child only because it's
-- inherited from a parent under classic INHERITS (PRIMARY KEY/UNIQUE/FOREIGN
-- KEY/EXCLUDE never propagate to children this way, so this only ever
-- filters CHECK rows) — mirrors introspectColumns' attislocal filter above,
-- same reasoning.
AND    con.conislocal
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, con.conname`, enforcedCol)

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect constraints: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, table, name, typ, expr string
		var notValid, deferrable, initiallyDeferred, noInherit, notEnforced bool
		var singleCol, comment *string
		if err := rs.Scan(&schema, &table, &name, &typ, &expr, &notValid, &deferrable, &initiallyDeferred, &noInherit, &notEnforced, &singleCol, &comment); err != nil {
			return err
		}
		t, ok := idx[schema+"."+table]
		if !ok {
			continue
		}
		// pg_get_constraintdef bakes a trailing "NOT VALID" into def itself
		// for an unvalidated constraint — strip it so Expr always represents
		// the bare definition regardless of source, matching parseConstraint
		// (blockparser), which already strips the same suffix from
		// hand-written source into this same NotValid field. Without this,
		// a comment-bearing NOT VALID constraint sourced from live
		// introspection rendered "NOT VALID NOT VALID" wherever a caller
		// (e.g. dump) appends NOT VALID itself based on NotValid.
		if notValid {
			expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), "NOT VALID"))
		}
		// pg_get_constraintdef also bakes in a trailing "NOT ENFORCED" for
		// an unenforced CHECK/FOREIGN KEY (confirmed live: it suppresses
		// the otherwise-redundant "NOT VALID" in this case — an unenforced
		// constraint is unvalidated purely as a side effect, so notValid
		// above is true but the "NOT VALID" text never actually appears)
		// — same reasoning and treatment as NOT VALID just above.
		if notEnforced {
			expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), "NOT ENFORCED"))
		}
		if typ == "NOT NULL" {
			// PostgreSQL 18+ catalogues a real pg_constraint row for EVERY
			// NOT NULL column, even a bare "col TEXT NOT NULL" with no
			// CONSTRAINT keyword at all in source (confirmed live) —
			// introspectColumns already sets Column.NotNull from
			// attnotnull directly, independent of this row existing at
			// all. Collapse the ordinary case (unvalidated-never, not
			// NO INHERIT, catalog name matches what PostgreSQL's own
			// auto-naming algorithm would produce for an unnamed
			// declaration) back into that plain bool instead of promoting
			// it to a table-level ir.Constraint — otherwise every single
			// NOT NULL column in a dumped/verified project would sprout a
			// separate, noisy CONSTRAINT clause never actually written
			// in source. Only promote when there's something the bool
			// can't represent: NO INHERIT, an unvalidated (NOT VALID)
			// state, or a name that doesn't match the auto-generated
			// pattern (a real user-chosen name). A false negative here
			// (an unmodeled name collision making the auto-name
			// prediction wrong) only costs an unnecessary promotion, never
			// lost information — see predictNotNullConstraintName's own
			// doc comment for why full collision-tracking isn't done here.
			if singleCol != nil && !noInherit && !notValid && name == predictNotNullConstraintName(table, *singleCol) {
				continue
			}
		}
		cst := &ir.Constraint{
			Name:              name,
			Type:              typ,
			Expr:              expr,
			NotValid:          notValid,
			Deferrable:        deferrable,
			InitiallyDeferred: initiallyDeferred,
			NoInherit:         noInherit,
			NotEnforced:       notEnforced,
			Comment:           comment,
		}
		if singleCol != nil {
			cst.Columns = []string{*singleCol}
		}
		t.Constraints = append(t.Constraints, cst)
	}
	return rs.Err()
}

// predictNotNullConstraintName reconstructs PostgreSQL's own auto-generated
// name for an unnamed table-level NOT NULL constraint (ChooseConstraintName
// with label "not_null", src/backend/catalog/heap.c) — confirmed live:
// "<table>_<col>_not_null", e.g. "widgets_sku_not_null". Deliberately a
// narrower, single-purpose port of internal/diff's pgAutoConstraintName (not
// reused directly: introspect is a leaf package with no dependency on diff,
// matching the two packages' existing architecture) — no collision-retry
// suffix and no NAMEDATALEN truncation, since this is used only to decide
// whether introspectConstraints can collapse an ordinary NOT NULL row back
// into Column.NotNull's plain bool; getting it wrong for a pathologically
// long or colliding name just means the row is promoted to a real
// ir.Constraint instead (an unnecessary but harmless explicit declaration in
// dump output), never a silently dropped NO INHERIT/NOT VALID.
func predictNotNullConstraintName(table, col string) string {
	return table + "_" + col + "_not_null"
}

// introspectIndexes populates Table.Indexes. See introspectColumns for why
// relkind must include 'p' (partitioned parent) alongside 'r' — an index
// created directly on a partitioned table is itself a real pg_index entry
// with indrelid pointing at the relkind='p' parent, not just at its children.
func introspectIndexes(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	const q = `
SELECT n.nspname, c.relname,
       i.relname AS idx_name,
       ix.indisunique,
       am.amname AS method,
       pg_get_indexdef(ix.indexrelid) AS idx_def,
       its.spcname AS tablespace,
       obj_description(i.oid, 'pg_class') AS comment
FROM   pg_index ix
JOIN   pg_class c  ON c.oid = ix.indrelid
JOIN   pg_class i  ON i.oid = ix.indexrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_am am    ON am.oid = i.relam
LEFT   JOIN pg_tablespace its ON its.oid = i.reltablespace
WHERE  c.relkind IN ('r', 'p')
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    NOT EXISTS (
           SELECT 1 FROM pg_constraint con
           WHERE  con.conindid = ix.indexrelid
       )
ORDER  BY n.nspname, c.relname, i.relname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect indexes: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, table, name, method, def string
		var unique bool
		var tablespace, comment *string
		if err := rs.Scan(&schema, &table, &name, &unique, &method, &def, &tablespace, &comment); err != nil {
			return err
		}
		t, ok := idx[schema+"."+table]
		if !ok {
			continue
		}
		var where *string
		if i := strings.Index(strings.ToUpper(def), " WHERE "); i >= 0 {
			// Strip PG-added ::typename casts (e.g. from a WHERE clause
			// comparing against a typed column) so this matches hand-written
			// source and doesn't show as spurious drift — same treatment as
			// column defaults below.
			w := stripStringLiteralCasts(strings.TrimSpace(def[i+7:]))
			where = &w
		}
		t.Indexes = append(t.Indexes, &ir.Index{
			Name:             name,
			Unique:           unique,
			Method:           method,
			Columns:          parseIndexDef(def),
			Include:          parseIndexInclude(def),
			NullsNotDistinct: strings.Contains(strings.ToUpper(def), "NULLS NOT DISTINCT"),
			With:             parseIndexWith(def),
			Where:            where,
			Tablespace:       tablespace,
			Comment:          comment,
		})
	}
	return rs.Err()
}

// introspectViewIndexes is introspectIndexes' sibling for a materialized
// view's indexes (RFC Section 8.2) — real PostgreSQL only supports indexes on a
// materialized view or a table, never a plain view, so this only ever
// matches relkind = 'm'.
func introspectViewIndexes(ctx context.Context, conn pipeline.Querier, views []pipeline.IRObject) error {
	idx := make(map[string]*ir.View, len(views))
	for _, obj := range views {
		if v, ok := obj.(*ir.View); ok && v.Materialized {
			idx[v.Schema+"."+v.Name] = v
		}
	}
	if len(idx) == 0 {
		return nil
	}

	const q = `
SELECT n.nspname, c.relname,
       i.relname AS idx_name,
       ix.indisunique,
       am.amname AS method,
       pg_get_indexdef(ix.indexrelid) AS idx_def,
       its.spcname AS tablespace,
       obj_description(i.oid, 'pg_class') AS comment
FROM   pg_index ix
JOIN   pg_class c  ON c.oid = ix.indrelid
JOIN   pg_class i  ON i.oid = ix.indexrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_am am    ON am.oid = i.relam
LEFT   JOIN pg_tablespace its ON its.oid = i.reltablespace
WHERE  c.relkind = 'm'
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, i.relname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect view indexes: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, view, name, method, def string
		var unique bool
		var tablespace, comment *string
		if err := rs.Scan(&schema, &view, &name, &unique, &method, &def, &tablespace, &comment); err != nil {
			return err
		}
		v, ok := idx[schema+"."+view]
		if !ok {
			continue
		}
		var where *string
		if i := strings.Index(strings.ToUpper(def), " WHERE "); i >= 0 {
			w := stripStringLiteralCasts(strings.TrimSpace(def[i+7:]))
			where = &w
		}
		v.Indexes = append(v.Indexes, &ir.Index{
			Name:             name,
			Unique:           unique,
			Method:           method,
			Columns:          parseIndexDef(def),
			Include:          parseIndexInclude(def),
			NullsNotDistinct: strings.Contains(strings.ToUpper(def), "NULLS NOT DISTINCT"),
			With:             parseIndexWith(def),
			Where:            where,
			Tablespace:       tablespace,
			Comment:          comment,
		})
	}
	return rs.Err()
}

// parseIndexDef extracts the column list from a pg_get_indexdef result.
// Format: CREATE [UNIQUE] INDEX name ON schema.table USING method (col_exprs) [WHERE pred]
func parseIndexDef(def string) []pipeline.IndexColumn {
	upper := strings.ToUpper(def)
	usingIdx := strings.Index(upper, " USING ")
	if usingIdx < 0 {
		return nil
	}
	// Skip method name to find the opening '('
	rest := def[usingIdx+7:]
	parenIdx := strings.IndexByte(rest, '(')
	if parenIdx < 0 {
		return nil
	}
	rest = rest[parenIdx+1:]

	// Find the matching closing ')'
	depth := 1
	end := -1
	for i, ch := range rest {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
				goto found
			}
		}
	}
	return nil
found:
	return splitIndexColumns(rest[:end])
}

// parseIndexInclude extracts the covering (INCLUDE) column names from a
// pg_get_indexdef string, e.g. "… (a) INCLUDE (b, c) WHERE …" → ["b", "c"].
func parseIndexInclude(def string) []string {
	upper := strings.ToUpper(def)
	i := strings.Index(upper, " INCLUDE (")
	if i < 0 {
		return nil
	}
	rest := def[i+len(" INCLUDE ("):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return nil
	}
	var cols []string
	for _, c := range strings.Split(rest[:end], ",") {
		c = strings.Trim(strings.TrimSpace(c), `"`)
		if c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// parseIndexWith extracts WITH (...) storage parameters from a
// pg_get_indexdef string, e.g. "… (a) WITH (fillfactor='70') WHERE …".
func parseIndexWith(def string) []pipeline.StorageParam {
	upper := strings.ToUpper(def)
	i := strings.Index(upper, " WITH (")
	if i < 0 {
		return nil
	}
	rest := def[i+len(" WITH ("):]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return nil
	}
	return parseIndexWithParams(rest[:end])
}

func splitIndexColumns(s string) []pipeline.IndexColumn {
	var cols []pipeline.IndexColumn
	var cur strings.Builder
	depth := 0
	flush := func() {
		if p := strings.TrimSpace(cur.String()); p != "" {
			cols = append(cols, parseIndexColumn(p))
		}
		cur.Reset()
	}
	for _, ch := range s {
		switch ch {
		case '(':
			depth++
			cur.WriteRune(ch)
		case ')':
			depth--
			cur.WriteRune(ch)
		case ',':
			if depth == 0 {
				flush()
			} else {
				cur.WriteRune(ch)
			}
		default:
			cur.WriteRune(ch)
		}
	}
	flush()
	return cols
}

// splitIndexIdentToken reads one identifier token from the front of s: a
// double-quoted identifier (with "" escaping) or a bare word up to the next
// whitespace or '(' — mirrors blockparser's identically-named helper.
// Returns the raw token (quotes included when quoted; callers Trim them)
// and the remaining text, both with surrounding whitespace stripped.
func splitIndexIdentToken(s string) (token, rest string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		i := 1
		for i < len(s) {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					i += 2
					continue
				}
				i++
				break
			}
			i++
		}
		return s[:i], strings.TrimSpace(s[i:])
	}
	i := strings.IndexAny(s, " \t\n\r(")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// splitIndexParenGroup finds s's leading "(...)" group (s must start with
// '(') and returns its inner text and everything after the closing paren.
func splitIndexParenGroup(s string) (inner, rest string, ok bool) {
	depth := 0
	for i, ch := range s {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

func isIndexColTrailingKeyword(word string) bool {
	switch strings.ToUpper(word) {
	case "ASC", "DESC", "NULLS":
		return true
	default:
		return false
	}
}

// parseIndexColumn parses one index-col entry from a pg_get_indexdef()
// reconstruction, in real PostgreSQL's own actual clause order (confirmed
// live via pg_query.Parse): column name or expression, then optional
// COLLATE identifier, then optional opclass [(params)], then optional
// ASC/DESC, then optional NULLS FIRST/LAST — mirrors blockparser's
// identically-rewritten parseIndexColumnEntry (see its doc comment for the
// bug this replaced: any entry containing '(' anywhere, including a bare
// opclass with parameters, e.g. "doc tsvector_ops(siglen = 32)", used to be
// swallowed whole into one bogus expression column).
//
// Unlike blockparser's version, the captured Expr.Text here is left exactly
// as pg_get_indexdef wrote it (parens included, un-stripped) rather than
// normalized — confirmed live that PostgreSQL's own deparser is
// inconsistent about wrapping: a raw "(a+b)" reconstructs with an extra
// defensive layer ("((a + b))" as the whole list item), while a function
// call like "lower(email)" does not gain one. This matches this codebase's
// existing "never change what's stored, only what's used to decide drift"
// precedent (see stripOuterParens/normalizeExprForCompare) — diffIndexes
// does not currently apply that normalization to index-column expressions
// specifically (only to WHERE), a separate, pre-existing gap out of this
// item's scope, not introduced or fixed here.
func parseIndexColumn(s string) pipeline.IndexColumn {
	col := pipeline.IndexColumn{}
	s = strings.TrimSpace(s)

	switch {
	case strings.HasPrefix(s, "("):
		// A parenthesized expression — find ITS matching close paren
		// (not necessarily the end of s: a trailing DESC/NULLS can still
		// follow, e.g. pg_get_indexdef's own "((a + b)) DESC" reconstruction)
		// and keep everything up to and including it, verbatim.
		if end, ok := matchingParenEnd(s, 0); ok {
			col.Expr = &pipeline.RawExpr{Text: stripStringLiteralCasts(s[:end+1])}
			s = strings.TrimSpace(s[end+1:])
		} else {
			col.Expr = &pipeline.RawExpr{Text: stripStringLiteralCasts(s)}
			return col
		}
	default:
		i := strings.IndexAny(s, " \t\n\r(")
		if i >= 0 && s[i] == '(' {
			// An identifier immediately followed by '(' with no
			// intervening whitespace: real PostgreSQL's func_expr_
			// windowless index_elem alternative (confirmed live) — the
			// identifier and its call are one inseparable expression, not
			// a column name followed by an opclass (which pg_get_indexdef
			// always reconstructs with an intervening space before its own
			// optional "(params)", e.g. "tsvector_ops (siglen='32')").
			if end, ok := matchingParenEnd(s, i); ok {
				col.Expr = &pipeline.RawExpr{Text: stripStringLiteralCasts(s[:end+1])}
				s = strings.TrimSpace(s[end+1:])
			} else {
				col.Expr = &pipeline.RawExpr{Text: stripStringLiteralCasts(s)}
				return col
			}
		} else {
			var name string
			name, s = splitIndexIdentToken(s)
			col.Name = strings.Trim(name, `"`)
		}
	}

	// COLLATE/opclass never follow an expression column in this parser —
	// same deliberate scoping as blockparser's mirrored function.
	if col.Expr == nil {
		if word, rest := splitIndexIdentToken(s); strings.ToUpper(word) == "COLLATE" {
			var collName string
			collName, s = splitIndexIdentToken(rest)
			col.Collation = &pipeline.Identifier{Name: strings.Trim(collName, `"`)}
		}

		if s != "" {
			if word, rest := splitIndexIdentToken(s); !isIndexColTrailingKeyword(word) {
				col.OpClass = &pipeline.Identifier{Name: strings.Trim(word, `"`)}
				s = rest
				if strings.HasPrefix(s, "(") {
					if inner, rest2, ok := splitIndexParenGroup(s); ok {
						col.OpClassParams = parseIndexWithParams(inner)
						s = rest2
					}
				}
			}
		}
	}

	if word, rest := splitIndexIdentToken(s); strings.ToUpper(word) == "DESC" {
		col.SortOrder = "DESC"
		s = rest
	} else if strings.ToUpper(word) == "ASC" {
		col.SortOrder = "ASC"
		s = rest
	}

	if word, rest := splitIndexIdentToken(s); strings.ToUpper(word) == "NULLS" {
		if word2, _ := splitIndexIdentToken(rest); strings.ToUpper(word2) == "FIRST" {
			col.Nulls = "FIRST"
		} else if strings.ToUpper(word2) == "LAST" {
			col.Nulls = "LAST"
		}
	}

	return col
}

// matchingParenEnd returns the index (within s) of the ')' that closes the
// '(' at s[open], or ok=false if unbalanced.
func matchingParenEnd(s string, open int) (end int, ok bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// parseIndexWithParams parses a plain "key = value, key2 = value2" list —
// an opclass parameter list's shape, same as WITH (...)'s but without any
// quoting stripped (opclass parameter values are typically bare numbers or
// identifiers, e.g. siglen = 32).
func parseIndexWithParams(raw string) []pipeline.StorageParam {
	var params []pipeline.StorageParam
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			params = append(params, pipeline.StorageParam{
				Key:   strings.TrimSpace(kv[0]),
				Value: strings.TrimSpace(kv[1]),
			})
		}
	}
	return params
}

func introspectPolicies(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	const q = `
SELECT n.nspname, c.relname,
       p.polname,
       CASE p.polcmd WHEN 'r' THEN 'SELECT' WHEN 'a' THEN 'INSERT'
                     WHEN 'w' THEN 'UPDATE'  WHEN 'd' THEN 'DELETE'
                     ELSE 'ALL' END AS cmd,
       p.polpermissive,
       pg_get_expr(p.polqual, p.polrelid) AS using_expr,
       pg_get_expr(p.polwithcheck, p.polrelid) AS check_expr,
       obj_description(p.oid, 'pg_policy') AS comment,
       (SELECT array_agg(COALESCE(pr.rolname, 'PUBLIC') ORDER BY COALESCE(pr.rolname, 'PUBLIC'))
          FROM unnest(p.polroles) AS role_oid
          LEFT JOIN pg_roles pr ON pr.oid = role_oid) AS roles
FROM   pg_policy p
JOIN   pg_class c     ON c.oid = p.polrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, p.polname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect policies: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, table, name, cmd string
		var permissive bool
		var using, check, comment *string
		var roles []string
		if err := rs.Scan(&schema, &table, &name, &cmd, &permissive, &using, &check, &comment, &roles); err != nil {
			return err
		}
		t, ok := idx[schema+"."+table]
		if !ok {
			continue
		}
		t.Policies = append(t.Policies, &ir.Policy{
			Name:       name,
			Command:    cmd,
			Permissive: permissive,
			Using:      using,
			WithCheck:  check,
			Roles:      roles,
			Comment:    comment,
		})
	}
	return rs.Err()
}

// introspectTriggers populates Table.Triggers. The events column uses
// array_to_string (which skips NULL elements) rather than string
// concatenation with a hardcoded " OR " prefix on every event after the
// first bit position — that unconditional prefix left a dangling "OR " on
// any trigger whose ONLY event isn't INSERT (e.g. an UPDATE-only trigger
// introspected as "OR UPDATE" instead of "UPDATE"), which never round-
// tripped back to the same event list.
func introspectTriggers(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	const q = `
SELECT n.nspname, c.relname,
       t.tgname,
       CASE WHEN (t.tgtype & 2) != 0 THEN 'BEFORE' ELSE 'AFTER' END AS when,
       CASE WHEN (t.tgtype & 1) != 0 THEN 'ROW' ELSE 'STATEMENT' END AS for_each,
       array_to_string(ARRAY[
           CASE WHEN (t.tgtype & 4)  != 0 THEN 'INSERT' END,
           CASE WHEN (t.tgtype & 8)  != 0 THEN 'DELETE' END,
           CASE WHEN (t.tgtype & 16) != 0 THEN 'UPDATE' END,
           CASE WHEN (t.tgtype & 32) != 0 THEN 'TRUNCATE' END
       ], ' OR ') AS events,
       p.proname AS func_name,
       pn.nspname AS func_schema,
       pg_get_triggerdef(t.oid, true) AS triggerdef,
       obj_description(t.oid, 'pg_trigger') AS comment,
       -- RFC Section 7.9 (audit item #2): pg_trigger.tgoldtable/tgnewtable hold
       -- the REFERENCING OLD/NEW TABLE AS transition-table names directly
       -- (NULL when REFERENCING wasn't used for that side) — no deparse
       -- needed, unlike the WHEN condition below.
       t.tgoldtable AS old_transition_name,
       t.tgnewtable AS new_transition_name,
       -- RFC audit item #1: pg_trigger.tgattr is the "UPDATE OF col, ..."
       -- column list as an int2vector of attnums (empty when the trigger
       -- has no OF clause at all) — unnest WITH ORDINALITY preserves the
       -- declared column order.
       (SELECT array_agg(a.attname ORDER BY u.ord)
          FROM unnest(t.tgattr) WITH ORDINALITY AS u(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = t.tgrelid AND a.attnum = u.attnum
       ) AS update_of_columns,
       -- Section 9.1's [NO] DEPENDS ON EXTENSION, reused for triggers
       -- (Section 7.9, audit item #75) — deptype='x' is PostgreSQL's own
       -- auto-drop-dependency-on-an-extension marker, confirmed live via
       -- the identical mechanism introspectFunctionDependsOnExtension
       -- already uses for Function/Procedure.
       (SELECT array_agg(e.extname ORDER BY e.extname)
          FROM pg_depend d
          JOIN pg_extension e ON e.oid = d.refobjid
          WHERE d.classid = 'pg_trigger'::regclass AND d.objid = t.oid AND d.deptype = 'x'
       ) AS depends_on_extensions,
       -- trigger-enable-state (Section 7.9, audit item #56): tgenabled
       -- 'O' (origin, the default) means ENABLED; 'D' DISABLED; 'R'
       -- ENABLE REPLICA; 'A' ENABLE ALWAYS.
       t.tgenabled::text
FROM   pg_trigger t
JOIN   pg_class c     ON c.oid = t.tgrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_proc p      ON p.oid = t.tgfoid
JOIN   pg_namespace pn ON pn.oid = p.pronamespace
WHERE  NOT t.tgisinternal
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, t.tgname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect triggers: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, table, name, when, forEach, events, funcName, funcSchema, triggerDef, tgenabled string
		var comment *string
		var updateOfColumns, dependsOnExtensions []string
		var oldTransitionName, newTransitionName *string
		if err := rs.Scan(&schema, &table, &name, &when, &forEach, &events, &funcName, &funcSchema, &triggerDef, &comment, &oldTransitionName, &newTransitionName, &updateOfColumns, &dependsOnExtensions, &tgenabled); err != nil {
			return err
		}
		var enableState string
		switch tgenabled {
		case "D":
			enableState = "DISABLED"
		case "R":
			enableState = "ENABLE REPLICA"
		case "A":
			enableState = "ENABLE ALWAYS"
		}
		condition := extractTriggerWhenCondition(triggerDef)
		t, ok := idx[schema+"."+table]
		if !ok {
			continue
		}
		fn := funcSchema + "." + funcName
		// Parse events string: remove leading " OR " and split
		rawEvents := strings.TrimSpace(events)
		var cleanEvents []string
		for part := range strings.SplitSeq(rawEvents, " OR ") {
			part = strings.TrimSpace(part)
			if part != "" {
				cleanEvents = append(cleanEvents, part)
			}
		}
		t.Triggers = append(t.Triggers, &ir.Trigger{
			Name:                name,
			When:                when,
			Events:              cleanEvents,
			ForEach:             forEach,
			UpdateOfColumns:     updateOfColumns,
			OldTransitionName:   oldTransitionName,
			NewTransitionName:   newTransitionName,
			Function:            fn,
			Condition:           condition,
			Comment:             comment,
			EnableState:         enableState,
			DependsOnExtensions: dependsOnExtensions,
		})
	}
	return rs.Err()
}

// extractTriggerWhenCondition pulls a trigger's WHEN (...) condition out of
// pg_get_triggerdef's full deparsed CREATE TRIGGER text. There is no direct
// SQL-callable equivalent of pg_get_expr for a trigger qual: tgqual's Var
// nodes resolve against the trigger's implicit OLD/NEW pseudo-relations, and
// plain pg_get_expr(tgqual, tgrelid) errors with "expression contains
// variables of more than one relation" for any condition that references
// NEW or OLD (confirmed live — the common case for an UPDATE trigger's WHEN
// clause). pg_get_triggerdef deparses the whole statement correctly because
// it builds the OLD/NEW range-table context internally; this walks its
// output the same way introspectConstraints already relies on
// pg_get_constraintdef's text for a value the catalog doesn't expose more
// directly.
func extractTriggerWhenCondition(def string) *string {
	const marker = " WHEN ("
	idx := strings.Index(def, marker)
	if idx == -1 {
		return nil
	}
	start := idx + len(marker) - 1 // index of the opening '('
	depth := 0
	for i := start; i < len(def); i++ {
		switch def[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				cond := strings.TrimSpace(def[start+1 : i])
				return &cond
			}
		}
	}
	return nil
}

// ── views ─────────────────────────────────────────────────────────────────────

func introspectViews(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT n.nspname, c.relname,
       r.rolname AS owner,
       pg_get_viewdef(c.oid, true) AS query,
       obj_description(c.oid, 'pg_class') AS comment,
       c.relkind = 'm' AS materialized,
       NOT c.relispopulated AS with_no_data,
       ts.spcname AS tablespace,
       c.reloptions
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
JOIN   pg_roles r     ON r.oid = c.relowner
LEFT   JOIN pg_tablespace ts ON ts.oid = c.reltablespace
WHERE  c.relkind IN ('v', 'm')
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER  BY n.nspname, c.relname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect views: %w", err)
	}
	defer rs.Close()

	viewIdx := make(map[string]*ir.View)
	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, owner, query string
		var comment, tablespace *string
		var materialized, withNoData bool
		var reloptions []string
		if err := rs.Scan(&schema, &name, &owner, &query, &comment, &materialized, &withNoData, &tablespace, &reloptions); err != nil {
			return nil, err
		}
		q := normalizeViewQuery(query)
		v := &ir.View{
			Schema:       schema,
			Name:         name,
			Owner:        &owner,
			Query:        q,
			Comment:      comment,
			Materialized: materialized,
			Recursive:    strings.HasPrefix(q, "WITH RECURSIVE"),
			WithNoData:   materialized && withNoData,
		}
		// reloptions holds a materialized view's storage parameters
		// (Section 8.2) or a plain view's options, check_option included
		// (Section 8.1). Only a materialized view has a tablespace.
		if materialized {
			v.Tablespace = tablespace
		}
		for _, kv := range reloptions {
			if k, val, ok := strings.Cut(kv, "="); ok {
				v.StorageParams = append(v.StorageParams, pipeline.StorageParam{Key: k, Value: val})
			}
		}
		viewIdx[schema+"."+name] = v
		out = append(out, v)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	if err := introspectViewSecurityLabels(ctx, conn, viewIdx); err != nil {
		return nil, err
	}
	if err := introspectViewGrants(ctx, conn, viewIdx); err != nil {
		return nil, err
	}
	return out, nil
}

// ── functions ─────────────────────────────────────────────────────────────────

// parseFuncConfig parses pg_proc.proconfig (Section 9.2's SET-clause list)
// into FuncConfig entries. Each array element is a "param=value" pair,
// split at the first '=' (a GUC name can never itself contain one) — same
// format and parsing approach as introspectRoleConfigs' pg_db_role_setting
// handling. There is no way to distinguish a FROM CURRENT-declared entry
// from an ordinary SET here (PostgreSQL only ever stores the resolved
// value), so every entry comes back as an ordinary Value — the same
// accepted narrow limitation already documented for RoleConfig.
func parseFuncConfig(proconfig []string) []ir.FuncConfig {
	var out []ir.FuncConfig
	for _, kv := range proconfig {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		param, value := kv[:i], kv[i+1:]
		out = append(out, ir.FuncConfig{Param: param, Value: &value})
	}
	return out
}

func introspectFunctions(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT n.nspname, p.proname,
       p.oid::bigint AS oid,
       pg_get_function_identity_arguments(p.oid) AS args,
       pg_catalog.oidvectortypes(p.proargtypes)   AS arg_types,
       pg_catalog.format_type(p.prorettype, NULL) AS return_type,
       l.lanname AS language,
       CASE p.provolatile
           WHEN 'i' THEN 'IMMUTABLE'
           WHEN 's' THEN 'STABLE'
           ELSE 'VOLATILE'
       END,
       p.prosecdef,
       p.proisstrict,
       CASE p.proparallel
           WHEN 'r' THEN 'RESTRICTED'
           WHEN 's' THEN 'SAFE'
           ELSE 'UNSAFE'
       END,
       p.procost,
       p.prorows,
       p.proretset,
       l.lanname IN ('c', 'internal') AS is_c_or_internal,
       obj_description(p.oid, 'pg_proc') AS comment,
       p.prokind::text,
       p.prosrc,
       r.rolname AS owner,
       -- RFC audit item #25: [NOT] LEAKPROOF.
       p.proleakproof,
       -- RFC audit item #27: AS 'obj_file', 'link_symbol' (LANGUAGE C) —
       -- probin is the shared-object path; prosrc holds the link_symbol in
       -- this case (confirmed live: NULL/empty for every other language).
       p.probin,
       -- RFC audit item #26: TRANSFORM FOR TYPE t [, ...] — protrftypes is
       -- an oid[], empty/NULL when no transform is declared.
       (SELECT array_agg(pg_catalog.format_type(t, NULL) ORDER BY ord)
          FROM unnest(p.protrftypes) WITH ORDINALITY AS u(t, ord)) AS transform_types,
       -- RFC audit item #28: PG14+ BEGIN ATOMIC ... END. prosrc is empty
       -- for this form (confirmed live) — the body lives in prosqlbody
       -- instead, which pg_get_functiondef alone knows how to reconstruct
       -- back into BEGIN ATOMIC ... END text; only called for rows that
       -- actually need it.
       p.prosqlbody IS NOT NULL AS has_sql_body,
       CASE WHEN p.prosqlbody IS NOT NULL THEN pg_get_functiondef(p.oid) END AS funcdef,
       -- Section 9.2's SET clause list. Each array element is a
       -- "param=value" pair (confirmed live), identical format to
       -- pg_db_role_setting.setconfig — see introspectRoleConfigs' doc
       -- comment for the same parsing/comparison approach.
       p.proconfig
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace
JOIN   pg_language  l ON l.oid = p.prolang
JOIN   pg_roles r ON r.oid = p.proowner
WHERE  p.prokind IN ('f', 'p')
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
ORDER  BY n.nspname, p.proname, args`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect functions: %w", err)
	}
	defer rs.Close()

	funcIdx := make(map[string]*ir.Function)
	procIdx := make(map[string]*ir.Procedure)
	fnByOID := make(map[int64]*ir.Function)
	procByOID := make(map[int64]*ir.Procedure)
	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, args, argTypes, retType, lang, volatility string
		var oid int64
		var secDef, strict bool
		var parallel string
		var cost, rows float64
		var retset, isCOrInternal bool
		var comment *string
		var prokind, prosrc, owner string
		var leakproof bool
		var probin *string
		var transformTypes []string
		var hasSQLBody bool
		var funcdef *string
		var proconfig []string
		if err := rs.Scan(&schema, &name, &oid, &args, &argTypes, &retType, &lang, &volatility, &secDef, &strict,
			&parallel, &cost, &rows, &retset, &isCOrInternal, &comment, &prokind, &prosrc, &owner,
			&leakproof, &probin, &transformTypes, &hasSQLBody, &funcdef, &proconfig); err != nil {
			return nil, err
		}
		config := parseFuncConfig(proconfig)
		// RFC audit items #26/#27/#28: shared between Procedure and Function
		// below. probin non-empty (LANGUAGE C) means prosrc actually holds
		// the link_symbol, not a procedural body — confirmed live via
		// pg_proc/pg_get_functiondef. hasSQLBody (BEGIN ATOMIC) means prosrc
		// is empty and the real body only exists in prosqlbody, reconstructed
		// via pg_get_functiondef and re-extracted the same way the builder
		// side extracts it from source text.
		body := prosrc
		var objFile, linkSymbol *string
		if probin != nil && *probin != "" {
			ofile, lsym := *probin, prosrc
			objFile, linkSymbol = &ofile, &lsym
			body = ""
		}
		atomicBody := false
		if hasSQLBody {
			atomicBody = true
			if funcdef != nil {
				body = ir.ExtractAtomicBody(*funcdef)
			}
		}
		var transforms []ir.TypeRef
		for _, t := range transformTypes {
			transforms = append(transforms, ir.TypeRef{Name: t})
		}
		if prokind == "p" {
			proc := &ir.Procedure{
				Schema:  schema,
				Name:    name,
				Comment: comment,
				Owner:   &owner,
				Attrs: ir.FuncAttrs{
					Language:    lang,
					Body:        body,
					Transforms:  transforms,
					ObjFile:     objFile,
					LinkSymbol:  linkSymbol,
					AtomicBody:  atomicBody,
					SecurityDef: secDef,
					Config:      config,
				},
			}
			// Use argTypes (type-only, from oidvectortypes) to build Args so that
			// the QualifiedName matches argsKey() in the IR builder (which also uses
			// type-only). Keep args (full identity args with parameter names) for the
			// grants index key, which mirrors pg_get_function_identity_arguments.
			if argTypes != "" {
				for a := range strings.SplitSeq(argTypes, ", ") {
					proc.Args = append(proc.Args, ir.FuncArg{Type: ir.TypeRef{Name: strings.TrimSpace(a)}})
				}
			}
			procIdx[schema+"."+name+"("+args+")"] = proc
			procByOID[oid] = proc
			out = append(out, proc)
		} else {
			// procost/prorows are NOT NULL in pg_proc — every function has a
			// concrete value, PG's own default among them (1 for C/internal
			// language, 100 otherwise for cost; 0 for a scalar function, 1000
			// for a set-returning one, for rows). Only surface a value when
			// it genuinely differs from that computed default — otherwise
			// every ordinary function would render a noisy "COST 100" on
			// dump, the same suppress-when-default problem already solved
			// for column STORAGE (ir.Column.StorageIsTypeDefault).
			defaultCost := 100.0
			if isCOrInternal {
				defaultCost = 1
			}
			defaultRows := 0.0
			if retset {
				defaultRows = 1000
			}
			attrs := ir.FuncAttrs{
				Language:    lang,
				Volatility:  volatility,
				SecurityDef: secDef,
				Strict:      strict,
				Leakproof:   leakproof,
				Parallel:    parallel,
				Body:        body,
				Transforms:  transforms,
				ObjFile:     objFile,
				LinkSymbol:  linkSymbol,
				AtomicBody:  atomicBody,
				Config:      config,
			}
			if cost != defaultCost {
				c := cost
				attrs.Cost = &c
			}
			if rows != defaultRows {
				r := rows
				attrs.Rows = &r
			}
			fn := &ir.Function{
				Schema:     schema,
				Name:       name,
				ReturnType: ir.TypeRef{Name: retType, SetOf: retset},
				Comment:    comment,
				Owner:      &owner,
				Attrs:      attrs,
			}
			// Use argTypes (type-only) so QualifiedName matches argsKey() in IR builder.
			// Keep args (with parameter names) for the grants index key only.
			if argTypes != "" {
				for a := range strings.SplitSeq(argTypes, ", ") {
					fn.Args = append(fn.Args, ir.FuncArg{Type: ir.TypeRef{Name: strings.TrimSpace(a)}})
				}
			}
			funcIdx[schema+"."+name+"("+args+")"] = fn
			fnByOID[oid] = fn
			out = append(out, fn)
		}
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	if err := introspectFunctionArgs(ctx, conn, fnByOID, procByOID); err != nil {
		return nil, err
	}

	// Body hashes are computed here, after argument names/modes are final,
	// not inline in the row-scan loop above: ir.HashFunctionBody's plpgsql
	// canonicalisation compiles a reconstructed CREATE FUNCTION/PROCEDURE
	// shim through the real PL/pgSQL compiler, which resolves the
	// function's own parameter names against its declared argument list
	// (e.g. "a := a + 1"). A shim built before introspectFunctionArgs has
	// run would have empty/wrong names for the plain-IN-only common case,
	// so the plpgsql compile would frequently fail and silently fall back
	// to raw hashing — defeating the fix for exactly the case it exists
	// for.
	for _, fn := range funcIdx {
		fn.BodyHash = ir.HashFunctionBody(fn.Attrs.Language, fn.Attrs.Body, ir.RenderCreateFunctionSQL(fn))
	}
	for _, proc := range procIdx {
		proc.BodyHash = ir.HashFunctionBody(proc.Attrs.Language, proc.Attrs.Body, ir.RenderCreateProcedureSQL(proc))
	}

	if err := introspectFunctionSecurityLabels(ctx, conn, funcIdx); err != nil {
		return nil, err
	}
	if err := introspectProcedureSecurityLabels(ctx, conn, procIdx); err != nil {
		return nil, err
	}
	if err := introspectFunctionGrants(ctx, conn, funcIdx); err != nil {
		return nil, err
	}
	if err := introspectProcedureGrants(ctx, conn, procIdx); err != nil {
		return nil, err
	}
	if err := introspectFunctionDependsOnExtension(ctx, conn, fnByOID, procByOID); err != nil {
		return nil, err
	}
	return out, nil
}

// introspectFunctionDependsOnExtension populates DependsOnExtensions
// (Section 9.1) for every function/procedure from pg_depend's deptype='x'
// rows — PostgreSQL's own auto-drop-dependency-on-an-extension marker,
// confirmed live (distinct from deptype='e', true extension membership,
// which introspectFunctions' own WHERE clause already excludes entirely).
func introspectFunctionDependsOnExtension(ctx context.Context, conn pipeline.Querier, fnByOID map[int64]*ir.Function, procByOID map[int64]*ir.Procedure) error {
	const q = `
SELECT d.objid::bigint, e.extname
FROM   pg_depend d
JOIN   pg_extension e ON e.oid = d.refobjid
WHERE  d.classid = 'pg_proc'::regclass
AND    d.deptype = 'x'
ORDER  BY d.objid, e.extname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect function DEPENDS ON EXTENSION: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var oid int64
		var ext string
		if err := rs.Scan(&oid, &ext); err != nil {
			return err
		}
		if fn, ok := fnByOID[oid]; ok {
			fn.DependsOnExtensions = append(fn.DependsOnExtensions, ext)
		}
		if proc, ok := procByOID[oid]; ok {
			proc.DependsOnExtensions = append(proc.DependsOnExtensions, ext)
		}
	}
	return rs.Err()
}

// introspectFunctionArgs fixes up Args (name, mode, type) for every function
// AND procedure, superseding the type-only Args the main introspectFunctions
// query builds from oidvectortypes(proargtypes)/oidvectortypes(proargtypes)
// (function/procedure branches respectively) — which, like
// pg_get_function_identity_arguments, only ever reports IN/INOUT/VARIADIC
// argument TYPES, with no name or mode information at all, and never
// touches procedures' Args in any capacity.
//
// Before this fix, only functions with at least one non-plain-IN parameter
// (proargmodes IS NOT NULL) got real names/modes, via a narrower predecessor
// of this function; the overwhelming common case — plain IN-only functions,
// and every procedure regardless of its argument modes — had empty Name on
// every arg. That meant: a plain function's dumped/rendered signature always
// omitted parameter names (e.g. "add_ints(integer, integer)" instead of
// "add_ints(a integer, b integer)"), and — the reason this function was
// generalized rather than left as-is — HashFunctionBody's plpgsql
// canonicalisation needs an argument-accurate CREATE FUNCTION/PROCEDURE shim
// to feed the real PL/pgSQL compiler, which resolves the body's own
// parameter references (e.g. "a := a + 1") against the declared argument
// list at compile time; a shim with empty names would fail to compile for
// exactly the common case this whole feature targets.
//
// Uses generate_subscripts over COALESCE(proallargtypes, proargtypes::oid[])
// so it naturally covers both the "has a non-default mode" case
// (proallargtypes/proargmodes populated) and the plain-IN-only case
// (proallargtypes NULL, proargmodes NULL — COALESCE(proargmodes[i], 'i')
// supplies the default mode per PostgreSQL's own documented invariant that
// proargmodes, when non-NULL, has exactly the same length as
// proallargtypes). A function/procedure with zero arguments naturally
// produces zero subscript rows, leaving Args nil, same as before.
func introspectFunctionArgs(ctx context.Context, conn pipeline.Querier, fnByOID map[int64]*ir.Function, procByOID map[int64]*ir.Procedure) error {
	// alltypes is computed via a LATERAL subquery rather than inline
	// COALESCE(proallargtypes, proargtypes::oid[]) repeated at every use
	// site: proargtypes is an oidvector, PostgreSQL's special 0-based-lower-
	// bound array type (unlike every ordinary 1-based array), and a bare
	// ::oid[] cast preserves that 0-based numbering rather than
	// renormalizing it — confirmed live, this silently misaligned every
	// plain-IN-only function's argument names by one position before this
	// fix (name[i] read against a 0-based type array's index i landed on
	// the wrong argument). ARRAY(SELECT unnest(...)) is the standard
	// PostgreSQL idiom to renumber a 0-based array to an ordinary 1-based
	// one, matching proargnames/proargmodes's native 1-based indexing.
	const q = `
SELECT p.oid::bigint, p.prokind::text,
       COALESCE(p.proargmodes[i], 'i')::text AS mode,
       p.proargnames[i] AS name,
       pg_catalog.format_type(t.alltypes[i], NULL) AS type
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace,
       LATERAL (SELECT COALESCE(p.proallargtypes, ARRAY(SELECT unnest(p.proargtypes))) AS alltypes) t,
       LATERAL generate_subscripts(t.alltypes, 1) AS i
WHERE  p.prokind IN ('f','p')
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY p.oid, i`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect function/procedure args: %w", err)
	}
	defer rs.Close()

	argsByOID := make(map[int64][]ir.FuncArg)
	kindByOID := make(map[int64]string)
	var order []int64
	for rs.Next() {
		var oid int64
		var prokind, mode, typ string
		var name *string
		if err := rs.Scan(&oid, &prokind, &mode, &name, &typ); err != nil {
			return err
		}
		if _, ok := argsByOID[oid]; !ok {
			order = append(order, oid)
			kindByOID[oid] = prokind
		}
		argName := ""
		if name != nil {
			argName = *name
		}
		argMode := "IN"
		switch mode {
		case "o":
			argMode = "OUT"
		case "b":
			argMode = "INOUT"
		case "v":
			argMode = "VARIADIC"
		case "t":
			argMode = "TABLE"
		}
		argsByOID[oid] = append(argsByOID[oid], ir.FuncArg{Name: argName, Mode: argMode, Type: ir.TypeRef{Name: typ}})
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, oid := range order {
		if kindByOID[oid] == "p" {
			if proc, ok := procByOID[oid]; ok {
				proc.Args = argsByOID[oid]
			}
			continue
		}
		if fn, ok := fnByOID[oid]; ok {
			fn.Args = argsByOID[oid]
		}
	}
	return nil
}

// ── types ─────────────────────────────────────────────────────────────────────

func introspectTypes(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT n.nspname, t.typname,
       CASE t.typtype
           WHEN 'e' THEN 'ENUM'      WHEN 'c' THEN 'COMPOSITE'
           WHEN 'r' THEN 'RANGE'     WHEN 'd' THEN 'DOMAIN'
           WHEN 'b' THEN 'BASE'      ELSE 'UNKNOWN'
       END AS variant,
       obj_description(t.oid, 'pg_type') AS comment,
       pg_get_userbyid(t.typowner) AS owner
FROM   pg_type t
JOIN   pg_namespace n ON n.oid = t.typnamespace
WHERE  t.typtype IN ('e','c','r','d','b')
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    (t.typtype != 'c' OR NOT EXISTS (
    SELECT 1 FROM pg_class c WHERE c.oid = t.typrelid AND c.relkind != 'c'
))
-- PostgreSQL auto-creates an array shadow type (typcategory 'A', e.g.
-- "_mytype") for every base type — pg_type.typarray on the ELEMENT type
-- points forward to it, the same canonical test psql/format_type use, so
-- exclude any type that's the target of some other type's typarray.
AND    NOT EXISTS (SELECT 1 FROM pg_type base WHERE base.typarray = t.oid)
AND    NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
ORDER  BY n.nspname, t.typname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect types: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, variant, owner string
		var comment *string
		if err := rs.Scan(&schema, &name, &variant, &comment, &owner); err != nil {
			return nil, err
		}
		out = append(out, &ir.Type{Schema: schema, Name: name, Variant: variant, Comment: comment, Owner: &owner})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	if err := introspectEnumValues(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectDomainBodies(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectCompositeAttrs(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectRangeBodies(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectBaseBody(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectTypeSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectTypeGrants(ctx, conn, out); err != nil {
		return nil, err
	}
	return out, nil
}

// introspectTypeGrants populates Type.Grants for every type in types using
// aclexplode on pg_type.typacl (RFC audit item #3 — uniform across all 5
// variants, including DOMAIN: real PostgreSQL grants a domain exactly like
// any other type, via pg_type.typacl, confirmed live).
func introspectTypeGrants(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	idx := make(map[string]*ir.Type, len(types))
	for _, obj := range types {
		t := obj.(*ir.Type)
		idx[t.Schema+"."+t.Name] = t
	}

	const q = `
SELECT n.nspname, t.typname,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_type t
JOIN   pg_namespace n ON n.oid = t.typnamespace,
       LATERAL aclexplode(t.typacl) a
WHERE  t.typtype IN ('e','c','r','d','b')
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, t.typname, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect type grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, name, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		t, ok := idx[k.schema+"."+k.name]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		t.Grants = append(t.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}
	return nil
}

func introspectCompositeAttrs(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	const q = `
SELECT n.nspname, t.typname,
       a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod) AS attr_type,
       CASE WHEN a.attcollation <> 0 AND a.attcollation <> bt.typcollation
            THEN co.collname END AS collation,
       pg_catalog.col_description(c.oid, a.attnum) AS comment
FROM   pg_type t
JOIN   pg_namespace n   ON n.oid = t.typnamespace
JOIN   pg_class c       ON c.oid = t.typrelid
JOIN   pg_attribute a   ON a.attrelid = c.oid
JOIN   pg_type bt       ON bt.oid = a.atttypid
LEFT JOIN pg_collation co ON co.oid = a.attcollation
WHERE  t.typtype = 'c'
AND    c.relkind = 'c'
AND    a.attnum > 0
AND    NOT a.attisdropped
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, t.typname, a.attnum`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect composite attrs: %w", err)
	}
	defer rs.Close()

	typeIdx := map[string]*ir.Type{}
	for _, obj := range types {
		if t, ok := obj.(*ir.Type); ok && t.Variant == "COMPOSITE" {
			typeIdx[t.Schema+"."+t.Name] = t
		}
	}
	for rs.Next() {
		var schema, name, attrName, attrType string
		var collation, comment *string
		if err := rs.Scan(&schema, &name, &attrName, &attrType, &collation, &comment); err != nil {
			return err
		}
		if t, ok := typeIdx[schema+"."+name]; ok {
			col := &ir.Column{
				Name: attrName,
				Type: ir.TypeRef{Name: attrType},
			}
			if collation != nil {
				col.Collation = *collation
			}
			col.Comment = comment
			t.CompositeAttrs = append(t.CompositeAttrs, col)
		}
	}
	return rs.Err()
}

// introspectDomainBodies populates each DOMAIN's structured RFC Section 5.4
// diffing inputs (base type, DEFAULT, NOT NULL, CHECK constraints) —
// previously all four were concatenated into a single opaque Body string
// ("basetype NOT NULL DEFAULT expr CONSTRAINT c CHECK (...) ...", the same
// shape as RANGE/BASE's genuinely-opaque bodies), even though a domain is
// NOT opaque like those two: diffType now diffs each property individually
// (see differ.go), so it needs them as separate fields, not one blob to
// hash-compare. pg_type.typdefault is already plain SQL text (unlike a
// column default, which needs pg_get_expr on a stored node tree), so no
// deparsing is needed for it.
func introspectDomainBodies(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	const q = `
SELECT n.nspname, t.typname,
       pg_catalog.format_type(t.typbasetype, t.typtypmod) AS base_type,
       t.typnotnull,
       t.typdefault,
       CASE WHEN t.typcollation <> 0 AND t.typcollation <> bt.typcollation
            THEN co.collname END AS collation
FROM   pg_type t
JOIN   pg_namespace n ON n.oid = t.typnamespace
JOIN   pg_type bt     ON bt.oid = t.typbasetype
LEFT JOIN pg_collation co ON co.oid = t.typcollation
WHERE  t.typtype = 'd'
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, t.typname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect domain bodies: %w", err)
	}
	defer rs.Close()

	domainIdx := map[string]*ir.Type{}
	for _, obj := range types {
		if t, ok := obj.(*ir.Type); ok && t.Variant == "DOMAIN" {
			domainIdx[t.Schema+"."+t.Name] = t
		}
	}

	for rs.Next() {
		var schema, name, baseType string
		var notNull bool
		var defaultVal *string
		var collation *string
		if err := rs.Scan(&schema, &name, &baseType, &notNull, &defaultVal, &collation); err != nil {
			return err
		}
		t, ok := domainIdx[schema+"."+name]
		if !ok {
			continue
		}
		t.DomainBaseType = ir.TypeRef{Name: baseType}
		t.DomainNotNull = notNull
		t.DomainDefault = defaultVal
		if collation != nil {
			t.DomainCollation = *collation
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}
	return introspectDomainConstraints(ctx, conn, domainIdx)
}

// introspectDomainConstraints populates DomainConstraints for every domain
// in idx from pg_constraint — a separate query (rather than introspectDomain
// Bodies' single-row string_agg this replaced) because each domain can have
// any number of named CHECK constraints, needing the same one-row-per-entry
// shape introspectEnumValues already uses for ENUM values.
func introspectDomainConstraints(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Type) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, t.typname, con.conname, pg_get_constraintdef(con.oid)
FROM   pg_constraint con
JOIN   pg_type t      ON t.oid = con.contypid
JOIN   pg_namespace n ON n.oid = t.typnamespace
WHERE  con.contype = 'c'
AND    t.typtype = 'd'
ORDER  BY n.nspname, t.typname, con.conname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect domain constraints: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, name, conname, condef string
		if err := rs.Scan(&schema, &name, &conname, &condef); err != nil {
			return err
		}
		t, ok := idx[schema+"."+name]
		if !ok {
			continue
		}
		t.DomainConstraints = append(t.DomainConstraints, &ir.Constraint{
			Name: conname,
			Type: "CHECK",
			Expr: condef,
		})
	}
	return rs.Err()
}

// introspectRangeBodies reads each RANGE type's pg_range row and reconstructs
// its DPG-source options text into Body — the same "trailing-clause-only"
// shape introspectDomainBodies already establishes (no "CREATE TYPE name AS
// RANGE" prefix; dump.go's renderer supplies that). Found live-testing a
// demo project: RANGE types had NO introspection at all before this — not
// even a partial/wrong reconstruction, nothing whatsoever selected SUBTYPE
// or any other pg_range column, so `dpg dump` could only emit
// "-- type X (RANGE) omitted" for every range type in the database, despite
// the RFC listing Range types as "Declared, Diffed" with no caveat that
// introspection was incomplete.
//
// SUBTYPE_OPCLASS is only rendered when it differs from the subtype's own
// default btree opclass — same "suppress when it matches the type's default"
// discipline already used for column STORAGE (item 6m) — otherwise nearly every
// range type would render a redundant SUBTYPE_OPCLASS clause. COLLATION,
// CANONICAL, and SUBTYPE_DIFF are only rendered when actually set (PG
// reports 0/no-collation for the common case of a non-collatable subtype
// like numeric or a range with no custom CANONICAL/SUBTYPE_DIFF function).
func introspectRangeBodies(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	const q = `
WITH btree_am AS (SELECT oid FROM pg_am WHERE amname = 'btree')
SELECT n.nspname, t.typname,
       pg_catalog.format_type(r.rngsubtype, NULL) AS subtype,
       CASE WHEN dflt.oid IS NULL OR dflt.oid <> r.rngsubopc
            THEN quote_ident(opc.opcname) END AS subtype_opclass,
       CASE WHEN r.rngcollation <> 0 THEN quote_ident(coll.collname) END AS collation,
       CASE WHEN r.rngcanonical <> 0 THEN quote_ident(canfn.proname) END AS canonical,
       CASE WHEN r.rngsubdiff <> 0 THEN quote_ident(difffn.proname) END AS subtype_diff,
       mr.typname AS multirange_name
FROM   pg_range r
JOIN   pg_type mr         ON mr.oid = r.rngmultitypid
JOIN   pg_type t          ON t.oid = r.rngtypid
JOIN   pg_namespace n     ON n.oid = t.typnamespace
LEFT JOIN pg_opclass opc  ON opc.oid = r.rngsubopc
LEFT JOIN pg_opclass dflt ON dflt.opcintype = r.rngsubtype
                          AND dflt.opcmethod = (SELECT oid FROM btree_am)
                          AND dflt.opcdefault
LEFT JOIN pg_collation coll ON coll.oid = r.rngcollation
LEFT JOIN pg_proc canfn     ON canfn.oid = r.rngcanonical
LEFT JOIN pg_proc difffn    ON difffn.oid = r.rngsubdiff
WHERE  n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, t.typname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect range bodies: %w", err)
	}
	defer rs.Close()

	rangeIdx := map[string]*ir.Type{}
	for _, obj := range types {
		if t, ok := obj.(*ir.Type); ok && t.Variant == "RANGE" {
			rangeIdx[t.Schema+"."+t.Name] = t
		}
	}

	for rs.Next() {
		var schema, name, subtype string
		var opclass, collation, canonical, subtypeDiff *string
		var multirangeName string
		if err := rs.Scan(&schema, &name, &subtype, &opclass, &collation, &canonical, &subtypeDiff, &multirangeName); err != nil {
			return err
		}
		t, ok := rangeIdx[schema+"."+name]
		if !ok {
			continue
		}
		parts := []string{"SUBTYPE = " + subtype}
		if opclass != nil {
			parts = append(parts, "SUBTYPE_OPCLASS = "+*opclass)
		}
		if collation != nil {
			parts = append(parts, "COLLATION = "+*collation)
		}
		if canonical != nil {
			parts = append(parts, "CANONICAL = "+*canonical)
		}
		if subtypeDiff != nil {
			parts = append(parts, "SUBTYPE_DIFF = "+*subtypeDiff)
		}
		// MULTIRANGE_TYPE_NAME only when it isn't PostgreSQL's default,
		// same suppress-the-default discipline as SUBTYPE_OPCLASS above.
		t.MultirangeName = multirangeName
		if multirangeName != ir.DefaultMultirangeName(name) {
			parts = append(parts, "MULTIRANGE_TYPE_NAME = "+quoteIdent(multirangeName))
		}
		t.Body = strings.Join(parts, ", ")
		t.Reconstructed = true
	}
	return rs.Err()
}

// introspectBaseBody reconstructs a BASE type's CREATE TYPE options list
// (RFC Section 5.5: "INPUT = func, OUTPUT = func, ...") from pg_type — the same
// "reconstruct from catalog" pattern as introspectRangeBodies above. Diffing
// stays hash-only (RFC Section 5.5: any change is DESTRUCTIVE), so completeness
// here only affects how faithfully a dumped base type round-trips, not
// diffing behavior.
func introspectBaseBody(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	const q = `
SELECT n.nspname, t.typname,
       infn.proname, outfn.proname,
       recvfn.proname, sendfn.proname,
       modinfn.proname, modoutfn.proname,
       anlfn.proname, subfn.proname,
       t.typlen, t.typbyval, t.typalign::text, t.typstorage::text,
       t.typcategory::text, t.typispreferred, t.typdelim::text,
       elem.typname AS element, t.typcollation <> 0 AS collatable
FROM   pg_type t
JOIN   pg_namespace n ON n.oid = t.typnamespace
JOIN   pg_proc infn   ON infn.oid = t.typinput
JOIN   pg_proc outfn  ON outfn.oid = t.typoutput
LEFT   JOIN pg_proc recvfn   ON recvfn.oid = t.typreceive
LEFT   JOIN pg_proc sendfn   ON sendfn.oid = t.typsend
LEFT   JOIN pg_proc modinfn  ON modinfn.oid = t.typmodin
LEFT   JOIN pg_proc modoutfn ON modoutfn.oid = t.typmodout
LEFT   JOIN pg_proc anlfn    ON anlfn.oid = t.typanalyze
LEFT   JOIN pg_proc subfn    ON subfn.oid = t.typsubscript
LEFT   JOIN pg_type elem     ON elem.oid = NULLIF(t.typelem, 0)
WHERE  t.typtype = 'b'
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, t.typname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect base type bodies: %w", err)
	}
	defer rs.Close()

	baseIdx := map[string]*ir.Type{}
	for _, obj := range types {
		if t, ok := obj.(*ir.Type); ok && t.Variant == "BASE" {
			baseIdx[t.Schema+"."+t.Name] = t
		}
	}

	alignName := map[string]string{"c": "char", "s": "int2", "i": "int4", "d": "double"}
	storageName := map[string]string{"p": "plain", "e": "external", "m": "main", "x": "extended"}

	for rs.Next() {
		var schema, name, inputFn, outputFn, align, storage, category, delim string
		var recvFn, sendFn, modinFn, modoutFn, anlFn, subFn, element *string
		var typLen int32
		var byVal, preferred, collatable bool
		if err := rs.Scan(&schema, &name, &inputFn, &outputFn,
			&recvFn, &sendFn, &modinFn, &modoutFn, &anlFn, &subFn,
			&typLen, &byVal, &align, &storage,
			&category, &preferred, &delim, &element, &collatable); err != nil {
			return err
		}
		t, ok := baseIdx[schema+"."+name]
		if !ok {
			continue
		}
		parts := []string{
			"INPUT = " + quoteIdent(inputFn),
			"OUTPUT = " + quoteIdent(outputFn),
		}
		if recvFn != nil {
			parts = append(parts, "RECEIVE = "+quoteIdent(*recvFn))
		}
		if sendFn != nil {
			parts = append(parts, "SEND = "+quoteIdent(*sendFn))
		}
		if modinFn != nil {
			parts = append(parts, "TYPMOD_IN = "+quoteIdent(*modinFn))
		}
		if modoutFn != nil {
			parts = append(parts, "TYPMOD_OUT = "+quoteIdent(*modoutFn))
		}
		if anlFn != nil {
			parts = append(parts, "ANALYZE = "+quoteIdent(*anlFn))
		}
		if subFn != nil {
			parts = append(parts, "SUBSCRIPT = "+quoteIdent(*subFn))
		}
		if typLen == -1 {
			parts = append(parts, "INTERNALLENGTH = VARIABLE")
		} else if typLen > 0 {
			parts = append(parts, fmt.Sprintf("INTERNALLENGTH = %d", typLen))
		}
		if byVal {
			parts = append(parts, "PASSEDBYVALUE")
		}
		if a, ok := alignName[align]; ok {
			parts = append(parts, "ALIGNMENT = "+a)
		}
		if s, ok := storageName[storage]; ok {
			parts = append(parts, "STORAGE = "+s)
		}
		if element != nil {
			parts = append(parts, "ELEMENT = "+*element)
		}
		parts = append(parts, "DELIMITER = "+quoteLit(delim))
		parts = append(parts, "CATEGORY = "+quoteLit(category))
		if preferred {
			parts = append(parts, "PREFERRED = true")
		}
		if collatable {
			parts = append(parts, "COLLATABLE = true")
		}
		t.Body = strings.Join(parts, ", ")
		t.Reconstructed = true

		// Populate the same 7 in-place-alterable properties (Section 5.5)
		// the builder extracts from source, using identical value
		// formatting (bare lowercase function name; STORAGE's PG-code-to-
		// keyword mapping already matches typeNameToRef's rendering of a
		// bare unqualified name) so the two sides compare equal for an
		// unmodified object.
		t.BaseReceive = recvFn
		t.BaseSend = sendFn
		t.BaseTypmodIn = modinFn
		t.BaseTypmodOut = modoutFn
		t.BaseAnalyze = anlFn
		t.BaseSubscript = subFn
		if s, ok := storageName[storage]; ok {
			t.BaseStorage = &s
		}
	}
	return rs.Err()
}

func introspectEnumValues(ctx context.Context, conn pipeline.Querier, types []pipeline.IRObject) error {
	const q = `
SELECT n.nspname, t.typname, e.enumlabel
FROM   pg_enum e
JOIN   pg_type t      ON t.oid = e.enumtypid
JOIN   pg_namespace n ON n.oid = t.typnamespace
ORDER  BY n.nspname, t.typname, e.enumsortorder`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect enum values: %w", err)
	}
	defer rs.Close()

	typeIdx := map[string]*ir.Type{}
	for _, obj := range types {
		if t, ok := obj.(*ir.Type); ok && t.Variant == "ENUM" {
			typeIdx[t.Schema+"."+t.Name] = t
		}
	}
	for rs.Next() {
		var schema, name, label string
		if err := rs.Scan(&schema, &name, &label); err != nil {
			return err
		}
		if t, ok := typeIdx[schema+"."+name]; ok {
			t.EnumValues = append(t.EnumValues, label)
		}
	}
	return rs.Err()
}

// ── sequences ─────────────────────────────────────────────────────────────────

func introspectSequences(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT n.nspname, c.relname,
       r.rolname AS owner,
       obj_description(c.oid, 'pg_class') AS comment,
       s.seqincrement, s.seqmin, s.seqmax, s.seqstart, s.seqcache, s.seqcycle,
       format_type(s.seqtypid, NULL) AS as_type,
       c.relpersistence::text
FROM   pg_class c
JOIN   pg_namespace n  ON n.oid = c.relnamespace
JOIN   pg_roles r      ON r.oid = c.relowner
JOIN   pg_sequence s   ON s.seqrelid = c.oid
WHERE  c.relkind = 'S'
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
AND    NOT EXISTS (
           SELECT 1 FROM pg_depend d
           WHERE  d.classid = 'pg_class'::regclass
           AND    d.objid = c.oid
           AND    d.deptype IN ('i', 'e')
       )
-- A hand-declared "OWNED BY" sequence (RFC audit item #14) produces the
-- identical pg_depend deptype='a' row a SERIAL/bigserial column's
-- auto-generated sequence does — confirmed live, PostgreSQL's catalog
-- cannot tell the two apart by dependency shape alone. The prior blanket
-- "any deptype='a' dependency means SERIAL sugar, exclude it" filter
-- therefore also made every hand-declared OWNED BY sequence invisible to
-- introspection entirely. introspectColumns' own SERIAL detection already
-- requires the owning column to ALSO carry a nextval() default
-- (serialOwned && def != nil) — mirrored here: only exclude when the
-- owning column has a default at all, the same distinguishing signal.
AND    NOT EXISTS (
           SELECT 1 FROM pg_depend d
           JOIN   pg_attrdef ad ON ad.adrelid = d.refobjid AND ad.adnum = d.refobjsubid
           WHERE  d.classid = 'pg_class'::regclass
           AND    d.objid = c.oid
           AND    d.deptype = 'a'
           AND    d.refclassid = 'pg_class'::regclass
       )
ORDER  BY n.nspname, c.relname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect sequences: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, owner string
		var comment *string
		var increment, min, max, start, cache int64
		var cycle bool
		var asType string
		var persistence string
		if err := rs.Scan(&schema, &name, &owner, &comment, &increment, &min, &max, &start, &cache, &cycle, &asType, &persistence); err != nil {
			return nil, err
		}
		seq := &ir.Sequence{
			Schema:      schema,
			Name:        name,
			Unlogged:    persistence == "u",
			Owner:       &owner,
			Comment:     comment,
			IncrementBy: &increment,
			MinValue:    &min,
			MaxValue:    &max,
			StartValue:  &start,
			Cache:       &cache,
			Cycle:       &cycle,
		}
		if asType != "" {
			seq.AsType = &ir.TypeRef{Name: asType}
		}
		out = append(out, seq)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectSequenceSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	seqIdx := make(map[string]*ir.Sequence, len(out))
	for _, o := range out {
		seq := o.(*ir.Sequence)
		seqIdx[seq.Schema+"."+seq.Name] = seq
	}
	if err := introspectSequenceGrants(ctx, conn, seqIdx); err != nil {
		return nil, err
	}
	if err := introspectSequenceOwnedBy(ctx, conn, seqIdx); err != nil {
		return nil, err
	}
	return out, nil
}

// introspectSequenceOwnedBy populates Sequence.OwnedBy for every sequence in
// idx by reading the auto-dependency (pg_depend deptype 'a') PostgreSQL
// itself records for a real OWNED BY relationship (RFC audit item #14) — a
// sequence with no such dependency has no owner ("NONE"'s live-catalog
// equivalent; left nil here, matching the general nil-means-unspecified
// convention, since a bare sequence and one explicitly declared
// "OWNED BY NONE" are catalog-indistinguishable).
func introspectSequenceOwnedBy(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Sequence) error {
	const q = `
SELECT sn.nspname, s.relname, tn.nspname, t.relname, a.attname
FROM   pg_depend d
JOIN   pg_class s      ON s.oid = d.objid AND s.relkind = 'S'
JOIN   pg_namespace sn ON sn.oid = s.relnamespace
JOIN   pg_class t      ON t.oid = d.refobjid
JOIN   pg_namespace tn ON tn.oid = t.relnamespace
JOIN   pg_attribute a  ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
WHERE  d.classid = 'pg_class'::regclass
AND    d.refclassid = 'pg_class'::regclass
AND    d.deptype = 'a'
AND    d.refobjsubid > 0`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect sequence owned-by: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var seqSchema, seqName, tblSchema, tblName, colName string
		if err := rs.Scan(&seqSchema, &seqName, &tblSchema, &tblName, &colName); err != nil {
			return err
		}
		seq, ok := idx[seqSchema+"."+seqName]
		if !ok {
			continue
		}
		owned := fmt.Sprintf("%s.%s.%s", tblSchema, tblName, colName)
		seq.OwnedBy = &owned
	}
	return rs.Err()
}

// introspectSequenceGrants populates Sequence.Grants for every sequence in
// idx using aclexplode on pg_class.relacl, mirroring introspectTableGrants
// (RFC audit item #24: Sequence.Grants was correctly populated by the
// builder but never referenced by createSequence/diffSequence, and there was
// no introspection path for it at all).
func introspectSequenceGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Sequence) error {
	const q = `
SELECT n.nspname, c.relname,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(c.relacl) a
WHERE  c.relkind = 'S'
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect sequence grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, name, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		s, ok := idx[k.schema+"."+k.name]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		s.Grants = append(s.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}
	return nil
}

// introspectRoles reads every Role attribute (RFC Section 11.1) except PASSWORD:
// PostgreSQL restricts pg_authid (where a role's password, hashed, actually
// lives) to superuser, and pg_roles.rolpassword itself returns the fixed
// placeholder string '********' for any non-superuser caller regardless of
// whether a password is even set — confirmed live — so there's no reliable
// non-superuser proxy for "has a password" at all, let alone its value.
// PASSWORD stays source-side-only (never introspected, never live-diffed),
// same boundary as Subscription CONNECTION (item 6z/Section 13.2).
//
// rolvaliduntil is cast to text as introspected; PostgreSQL's own timestamp
// formatting may not byte-match a hand-written VALID UNTIL literal in every
// case (e.g. timezone/precision representation) — a known, not yet solved,
// residual spurious-drift risk for that one field specifically, same class
// of gap as several other live-vs-declared text-format mismatches already
// flagged elsewhere in this codebase.
func introspectRoles(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT r.rolname,
       shobj_description(r.oid, 'pg_authid') AS comment,
       r.rolcanlogin,
       r.rolsuper,
       r.rolcreatedb,
       r.rolcreaterole,
       r.rolinherit,
       r.rolreplication,
       r.rolbypassrls,
       r.rolconnlimit,
       r.rolvaliduntil::text
FROM   pg_roles r
WHERE  r.rolname NOT LIKE 'pg_%'
AND    r.rolname <> 'postgres'
ORDER  BY r.rolname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect roles: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		r := &ir.Role{}
		if err := rs.Scan(
			&r.Name, &r.Comment, &r.CanLogin, &r.Superuser, &r.CreateDB, &r.CreateRole,
			&r.Inherit, &r.IsReplication, &r.BypassRLS, &r.ConnectionLimit, &r.ValidUntil,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectRoleSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectRoleConfigs(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectRoleMemberships(ctx, conn, out); err != nil {
		return nil, err
	}
	return out, nil
}

// introspectRoleConfigs populates Role.Configs (RFC audit item #74) from
// pg_db_role_setting. setdatabase = 0 means cluster-wide (InDatabase stays
// nil); each setconfig array element is a "param=value" pair, split at the
// first '=' (a GUC name can never itself contain one). Values come back
// resolved/bare — e.g. a role declared with "SET x FROM CURRENT" is
// indistinguishable here from "SET x = '<resolved value>'" (confirmed live:
// pg_db_role_setting stores only the final resolved value either way, no
// record of which form was used) — so FromCurrent-declared entries never
// match on a live comparison; a known, narrow, SAFE-severity limitation
// (re-applies the same idempotent SET FROM CURRENT every time), the same
// class of CREATE/declare-time-only gap already accepted for Collation's
// CopyFrom.
func introspectRoleConfigs(ctx context.Context, conn pipeline.Querier, roles []pipeline.IRObject) error {
	idx := make(map[string]*ir.Role, len(roles))
	for _, o := range roles {
		if r, ok := o.(*ir.Role); ok {
			idx[r.Name] = r
		}
	}
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT r.rolname, d.datname, s.setconfig
FROM   pg_db_role_setting s
JOIN   pg_roles r ON r.oid = s.setrole
LEFT   JOIN pg_database d ON d.oid = s.setdatabase AND s.setdatabase <> 0
ORDER  BY r.rolname, d.datname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect role configs: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var name string
		var database *string
		var setconfig []string
		if err := rs.Scan(&name, &database, &setconfig); err != nil {
			return err
		}
		r, ok := idx[name]
		if !ok {
			continue
		}
		for _, kv := range setconfig {
			i := strings.IndexByte(kv, '=')
			if i < 0 {
				continue
			}
			param, value := kv[:i], kv[i+1:]
			r.Configs = append(r.Configs, ir.RoleConfig{Param: param, Value: &value, InDatabase: database})
		}
	}
	return rs.Err()
}

// introspectRoleMemberships populates Role.Memberships (RFC audit item #32)
// from pg_auth_members — one row per (parent, member) pair, real
// PostgreSQL's own catalog model, which is exactly why ir.RoleMembership
// unified what used to be three separately-diffed lists (see its own doc
// comment). Each row is expanded into two entries, one per side of the
// relationship: the member role gets an "IN_ROLE" entry pointing at the
// parent, and the parent role gets a "ROLE" entry pointing at the member —
// both carrying the identical Admin/Inherit/Set values, since it's the same
// underlying GRANT fact viewed from either role's own declaration.
//
// inherit_option/set_option (PG16+ only, confirmed live — pre-16
// pg_auth_members has no such columns) are ALWAYS recorded with their real
// live value here, never suppressed to nil even when they happen to match
// PostgreSQL's own default. A first version of this function suppressed a
// default-matching value to nil to keep dump's output compact — live-tested
// before shipping, that broke diffRoleMembership's "declared, so managed"
// convention outright: an explicit WITH INHERIT TRUE that happens to equal
// the member role's own default rolinherit would introspect back as
// Inherit == nil, and boolPtrDiffers(desired=&true, snap=nil) unconditionally
// reports "changed" (nil-on-the-snapshot-side means "never recorded, so
// different" — correct for a genuinely new declaration, wrong here), causing
// a permanent re-GRANT loop that never actually converges. Any dump-
// rendering compactness this would have bought belongs in cmd/dpg/dump.go's
// own rendering logic, not here, precisely because introspection output
// must double as diffRoleMembership's live-comparison input.
func introspectRoleMemberships(ctx context.Context, conn pipeline.Querier, roles []pipeline.IRObject) error {
	idx := make(map[string]*ir.Role, len(roles))
	for _, o := range roles {
		if r, ok := o.(*ir.Role); ok {
			idx[r.Name] = r
		}
	}
	if len(idx) == 0 {
		return nil
	}

	type row struct {
		parent, member string
		admin          bool
		inherit        bool
		setOpt         bool
		hasOptionCols  bool
	}
	var rows []row

	if serverVersionNum(ctx, conn) >= 160000 {
		const q = `
SELECT parent.rolname, member.rolname, am.admin_option,
       am.inherit_option, am.set_option
FROM   pg_auth_members am
JOIN   pg_roles parent ON parent.oid = am.roleid
JOIN   pg_roles member ON member.oid = am.member
ORDER  BY parent.rolname, member.rolname`
		rs, err := conn.QueryRows(ctx, q)
		if err != nil {
			return fmt.Errorf("introspect role memberships: %w", err)
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			r.hasOptionCols = true
			if err := rs.Scan(&r.parent, &r.member, &r.admin, &r.inherit, &r.setOpt); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	} else {
		const q = `
SELECT parent.rolname, member.rolname, am.admin_option
FROM   pg_auth_members am
JOIN   pg_roles parent ON parent.oid = am.roleid
JOIN   pg_roles member ON member.oid = am.member
ORDER  BY parent.rolname, member.rolname`
		rs, err := conn.QueryRows(ctx, q)
		if err != nil {
			return fmt.Errorf("introspect role memberships: %w", err)
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.parent, &r.member, &r.admin); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	}

	for _, r := range rows {
		var inherit, setOpt *bool
		if r.hasOptionCols {
			inherit = &r.inherit
			setOpt = &r.setOpt
		}
		if member, ok := idx[r.member]; ok {
			member.Memberships = append(member.Memberships, ir.RoleMembership{
				Role: r.parent, Direction: "IN_ROLE", Admin: r.admin, Inherit: inherit, Set: setOpt,
			})
		}
		if parent, ok := idx[r.parent]; ok {
			parent.Memberships = append(parent.Memberships, ir.RoleMembership{
				Role: r.member, Direction: "ROLE", Admin: r.admin, Inherit: inherit, Set: setOpt,
			})
		}
	}
	return nil
}

var _ pipeline.Introspector = (*CatalogIntrospector)(nil)

// ── query normalisation ───────────────────────────────────────────────────────

// stringLiteralCastRE matches a single-quoted SQL string literal (including
// escaped ” sequences) followed by a ::typename cast.
var stringLiteralCastRE = regexp.MustCompile(`('(?:[^']|'')*')::[A-Za-z_][A-Za-z0-9_]*`)

// stripStringLiteralCasts removes PG-added ::typename casts from single-quoted
// string literals in pg_get_expr / pg_get_viewdef output.
// e.g. 'active'::status → 'active', 'foo'::character → 'foo'.
// This makes the introspected form match what users write in .dpg source files.
func stripStringLiteralCasts(s string) string {
	return stringLiteralCastRE.ReplaceAllString(s, "$1")
}

// normalizeViewQuery strips PG-added type casts from string literals and then
// canonicalises the SQL through pg_query parse→deparse so that cosmetic
// differences (extra parentheses added by pg_get_viewdef, whitespace) do not
// produce spurious drift ops.
func normalizeViewQuery(q string) string {
	q = strings.TrimSpace(q)
	q = stripStringLiteralCasts(q)
	res, err := pg_query.Parse(q)
	if err != nil || len(res.Stmts) == 0 {
		return q
	}
	out, err := pg_query.Deparse(res)
	if err != nil {
		return q
	}
	return out
}

// introspectTableInherits populates Table.Inherits for every child table in idx.
func introspectTableInherits(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	const q = `
SELECT cn.nspname, cc.relname, pn.nspname AS parent_schema, pc.relname AS parent_name
FROM   pg_inherits i
JOIN   pg_class cc      ON cc.oid = i.inhrelid
JOIN   pg_namespace cn  ON cn.oid = cc.relnamespace
JOIN   pg_class pc      ON pc.oid = i.inhparent
JOIN   pg_namespace pn  ON pn.oid = pc.relnamespace
WHERE  NOT cc.relispartition
AND    cn.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY cn.nspname, cc.relname, pn.nspname, pc.relname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect table inherits: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, name, parentSchema, parentName string
		if err := rs.Scan(&schema, &name, &parentSchema, &parentName); err != nil {
			return err
		}
		t, ok := idx[schema+"."+name]
		if !ok {
			continue
		}
		parent := parentSchema + "." + parentName
		t.Inherits = append(t.Inherits, parent)
	}
	return rs.Err()
}

// introspectTableGrants populates Table.Grants for every table in idx using
// aclexplode on pg_class.relacl.
func introspectTableGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	// aclexplode(NULL) returns 0 rows on PG14+, so no COALESCE needed.
	// An empty-literal COALESCE ('{}'::aclitem[]) produces ARR_NDIM=0, which
	// PG17 rejects with "ACL arrays must be one-dimensional".
	//
	// grantor <> grantee excludes the self-grant PostgreSQL materializes for
	// the object owner the moment ANY explicit GRANT touches the object
	// (confirmed live: an untouched table's relacl is NULL, but after a
	// single unrelated GRANT it gains a real "owner=<full-privs>/owner"
	// aclitem alongside the explicit one) — without this, that synthesized
	// entry reads back as an "extra" live grant and diffGrantSet proposes
	// REVOKEing it, contradicting RFC Section 11.2's "does NOT report extra
	// grants present live but absent from DPG source". introspectColumnGrants
	// already carried this guard; every other object kind's *Grants query
	// needs the identical filter (same PostgreSQL ACL mechanism, not
	// table-specific — confirmed live across sequence/function/schema/type/
	// FDW too).
	const q = `
SELECT n.nspname, c.relname,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(c.relacl) a
WHERE  c.relkind IN ('r', 'p', 'f')
AND    NOT c.relispartition
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect table grants: %w", err)
	}
	defer rs.Close()

	// grantor is part of the key (not just carried alongside privs) so that
	// the rare case of the same grantee receiving different privileges from
	// different grantors on the same object produces one ir.Grant per
	// distinct grantor, each with its own accurate GrantedBy — rather than
	// silently collapsing them under a single arbitrary grantor.
	type grantKey struct{ schema, name, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		t, ok := idx[k.schema+"."+k.name]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		t.Grants = append(t.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}
	return nil
}

// introspectViewGrants populates View.Grants for every view in idx using
// aclexplode on pg_class.relacl.
func introspectViewGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.View) error {
	const q = `
SELECT n.nspname, c.relname,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(c.relacl) a
WHERE  c.relkind IN ('v', 'm')
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect view grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, name, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		v, ok := idx[k.schema+"."+k.name]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		v.Grants = append(v.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}
	return nil
}

// introspectFunctionGrants populates Function.Grants for every function in idx
// using aclexplode on pg_proc.proacl. The idx key is "schema.name(args)".
func introspectFunctionGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Function) error {
	const q = `
SELECT n.nspname, p.proname,
       pg_get_function_identity_arguments(p.oid) AS args,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace,
       LATERAL aclexplode(p.proacl) a
WHERE  p.prokind = 'f'
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, p.proname, args, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect function grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, name, args, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, args, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &args, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, args, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		fn, ok := idx[k.schema+"."+k.name+"("+k.args+")"]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		fn.Grants = append(fn.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}

	materialized, err := functionLikeACLMaterializedKeys(ctx, conn, "f")
	if err != nil {
		return err
	}
	for key, fn := range idx {
		if !materialized[key] || hasPublicGrant(fn.Grants) {
			continue
		}
		fn.Revocations = append(fn.Revocations, ir.Revocation{
			Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"},
		})
	}
	return nil
}

func introspectProcedureGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Procedure) error {
	const q = `
SELECT n.nspname, p.proname,
       pg_get_function_identity_arguments(p.oid) AS args,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   pg_proc p
JOIN   pg_namespace n ON n.oid = p.pronamespace,
       LATERAL aclexplode(p.proacl) a
WHERE  p.prokind = 'p'
AND    a.grantor <> a.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, p.proname, args, grantee, a.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect procedure grants: %w", err)
	}
	defer rs.Close()

	type grantKey struct{ schema, name, args, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var schema, name, args, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&schema, &name, &args, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{schema, name, args, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &grantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if grantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		proc, ok := idx[k.schema+"."+k.name+"("+k.args+")"]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		proc.Grants = append(proc.Grants, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}

	materialized, err := functionLikeACLMaterializedKeys(ctx, conn, "p")
	if err != nil {
		return err
	}
	for key, proc := range idx {
		if !materialized[key] || hasPublicGrant(proc.Grants) {
			continue
		}
		proc.Revocations = append(proc.Revocations, ir.Revocation{
			Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"},
		})
	}
	return nil
}

// introspectColumnGrants reads explicit column-level privileges from
// pg_attribute.attacl and populates each ir.Column's Grants slice.
func introspectColumnGrants(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	// Deliberately queries pg_attribute.attacl (via aclexplode) rather than
	// information_schema.column_privileges — found live-testing a demo
	// project: column_privileges reports every EFFECTIVE privilege a role
	// has on a column, which PostgreSQL defines to include privileges the
	// role only has because of a table-level GRANT, not just genuine
	// explicit column-level ACL entries. A table with nothing but a
	// table-level "GRANT SELECT TO app_service" (no column-level grant
	// anywhere) had that same SELECT duplicated onto attacl.Grants for
	// EVERY column via this query, even though pg_attribute.attacl itself
	// was confirmed live to be NULL for all of them — causing dump to
	// round-trip in a phantom per-column GRANT, and a subsequent plan to
	// propose spuriously revoking it. attacl only ever holds genuine
	// explicit column-level grants (confirmed live: a real "GRANT SELECT
	// (email) ON TABLE users TO app_service" shows up here; the table-level
	// grant on an unrelated table's columns does not).
	// aclexplode(NULL) returns 0 rows on PG14+ (see introspectTableGrants'
	// identical note), so a column with no explicit ACL at all contributes
	// nothing here without needing a separate NULL guard.
	const q = `
SELECT n.nspname, c.relname, a.attname,
       CASE WHEN acl.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(acl.grantee) END AS grantee,
       pg_get_userbyid(acl.grantor) AS grantor,
       acl.privilege_type, acl.is_grantable
FROM   pg_attribute a
JOIN   pg_class c ON c.oid = a.attrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace,
       LATERAL aclexplode(a.attacl) acl
WHERE  a.attnum > 0 AND NOT a.attisdropped
AND    acl.grantor <> acl.grantee
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, a.attname, grantee, acl.privilege_type`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect column grants: %w", err)
	}
	defer rs.Close()

	// Accumulate per-(table, column, grantee, grantor) privilege lists, then
	// convert.
	type colGrantKey struct{ schema, table, col, grantee, grantor string }
	type colGrantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[colGrantKey]*colGrantEntry)
	var order []colGrantKey // insertion order for determinism

	for rs.Next() {
		var schema, table, col, grantee, grantor, priv string
		var isGrantable bool
		if err := rs.Scan(&schema, &table, &col, &grantee, &grantor, &priv, &isGrantable); err != nil {
			return err
		}
		k := colGrantKey{schema, table, col, grantee, grantor}
		e, ok := grants[k]
		if !ok {
			e = &colGrantEntry{}
			grants[k] = e
			order = append(order, k)
		}
		e.privs = append(e.privs, priv)
		if isGrantable {
			e.grantable = true
		}
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		t, ok := idx[k.schema+"."+k.table]
		if !ok {
			continue
		}
		e := grants[k]
		grantor := k.grantor
		g := ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		}
		for i := range t.Columns {
			if t.Columns[i].Name == k.col {
				t.Columns[i].Grants = append(t.Columns[i].Grants, g)
				break
			}
		}
	}
	return nil
}

// introspectPartitions populates PartitionBy and Partitions on partitioned
// tables, recursing into sub-partitioned children (RFC Section 7.13) — a partition
// has relkind 'p' too when it's itself further partitioned, so both queries
// below deliberately cover ALL partitioned relations, not just top-level
// tables in idx. Two queries are used: one for the partition key
// (pg_get_partkeydef), one for the child partition bounds (pg_get_expr on
// relpartbound); the parent-child edges are then assembled into a tree.
func introspectPartitions(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.Table) error {
	const keyQ = `
SELECT n.nspname, c.relname, pg_get_partkeydef(c.oid) AS partkeydef
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  c.relkind = 'p'
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname`

	rs, err := conn.QueryRows(ctx, keyQ)
	if err != nil {
		return fmt.Errorf("introspect partition keys: %w", err)
	}
	partKeys := make(map[string]string)
	for rs.Next() {
		var schema, name, keyDef string
		if err := rs.Scan(&schema, &name, &keyDef); err != nil {
			rs.Close()
			return err
		}
		partKeys[schema+"."+name] = keyDef
	}
	if err := rs.Err(); err != nil {
		return err
	}
	rs.Close()

	const childQ = `
SELECT pn.nspname, pc.relname, cn.nspname, cc.relname,
       pg_get_expr(cc.relpartbound, cc.oid) AS bound,
       cc.relkind::text, fs.srvname, ft.ftoptions
FROM   pg_class cc
JOIN   pg_namespace cn  ON cn.oid = cc.relnamespace
JOIN   pg_inherits i    ON i.inhrelid = cc.oid
JOIN   pg_class pc      ON pc.oid = i.inhparent
JOIN   pg_namespace pn  ON pn.oid = pc.relnamespace
LEFT   JOIN pg_foreign_table ft  ON ft.ftrelid = cc.oid
LEFT   JOIN pg_foreign_server fs ON fs.oid = ft.ftserver
WHERE  cc.relispartition
-- relispartition is also true for a child INDEX partition auto-created when
-- an index exists directly on the partitioned parent (relkind 'i'), which
-- has no partition bound (pg_get_expr returns NULL there, crashing the scan
-- into a non-nullable string below) — restrict to actual table partitions,
-- 'f' included: a foreign table can be a direct partition child too (RFC
-- Section 7.13), and was previously invisible here entirely (relkind 'f'
-- excluded), the same class of bug already fixed for introspectTables'
-- top-level foreign-table handling.
AND    cc.relkind IN ('r', 'p', 'f')
AND    pn.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY pn.nspname, pc.relname, cc.relname`

	rs2, err := conn.QueryRows(ctx, childQ)
	if err != nil {
		return fmt.Errorf("introspect partition children: %w", err)
	}
	childrenOf := make(map[string][]*ir.Partition)
	byKey := make(map[string]*ir.Partition) // child qualified name -> its own Partition node
	for rs2.Next() {
		var parentSchema, parentName, childSchema, childName, bound, relkind string
		var server *string
		var ftoptions []string
		if err := rs2.Scan(&parentSchema, &parentName, &childSchema, &childName, &bound, &relkind, &server, &ftoptions); err != nil {
			rs2.Close()
			return err
		}
		parentKey := parentSchema + "." + parentName
		childKey := childSchema + "." + childName
		part := &ir.Partition{Name: childName, Bounds: bound}
		if keyDef, ok := partKeys[childKey]; ok {
			part.PartitionBy = parsePartitionKey(keyDef)
		}
		if relkind == "f" {
			part.Foreign = true
			part.ForeignServer = server
			for _, kv := range ftoptions {
				if k, v, ok := strings.Cut(kv, "="); ok {
					part.ForeignOptions = append(part.ForeignOptions, pipeline.StorageParam{Key: k, Value: v})
				}
			}
		}
		childrenOf[parentKey] = append(childrenOf[parentKey], part)
		byKey[childKey] = part
	}
	if err := rs2.Err(); err != nil {
		return err
	}
	rs2.Close()

	// Attach nested sub-partitions to their own Partition node before wiring
	// the top-level tables, since each level's .Partitions must already be
	// populated by the time its parent reads childrenOf[key].
	for childKey, part := range byKey {
		if part.PartitionBy != nil {
			part.Partitions = childrenOf[childKey]
		}
	}

	if err := introspectPartitionConstraints(ctx, conn, byKey); err != nil {
		return err
	}

	for key, t := range idx {
		if keyDef, ok := partKeys[key]; ok {
			t.PartitionBy = parsePartitionKey(keyDef)
			t.Partitions = childrenOf[key]
		}
	}
	return nil
}

// introspectPartitionConstraints populates Constraints on every partition
// node in byKey (keyed "schema.partition_name") with constraints genuinely
// LOCAL to that partition — never inherited from its parent. This is the
// read-back half of RFC Section 7.3's "DROP CONSTRAINT ... ONLY" gap
// (PostgreSQL 18+): after "ALTER TABLE ONLY parent DROP CONSTRAINT x", real
// PostgreSQL flips the constraint's existing row on every current partition
// from inherited (conislocal=false, coninhcount=1) to local (conislocal=true,
// coninhcount=0) instead of dropping it there too — confirmed live (PG18)
// this is the EXACT SAME catalog signature an ordinary "ALTER TABLE
// partition_name ADD CONSTRAINT" (declared directly on the partition, never
// on the parent at all) produces from the start; PostgreSQL itself draws no
// distinction between the two histories, so ir.Partition.Constraints
// doesn't either. Deliberately narrower than introspectConstraints above:
// no NOT NULL collapse-to-bool special case (a partition never has an
// independently-declared Column of its own to collapse onto — see
// ir.Partition's doc comment) and no single-column extraction (Columns
// exists on ir.Constraint solely to reconstruct an UNNAMED constraint's
// PostgreSQL-generated name, which a partition's constraint, always named
// per its { }-block grammar — RFC Section 7.3 — never needs).
func introspectPartitionConstraints(ctx context.Context, conn pipeline.Querier, byKey map[string]*ir.Partition) error {
	if len(byKey) == 0 {
		return nil
	}
	enforcedCol := "true"
	if serverVersionNum(ctx, conn) >= 180000 {
		enforcedCol = "con.conenforced"
	}
	q := fmt.Sprintf(`
SELECT n.nspname, c.relname,
       con.conname,
       CASE con.contype
           WHEN 'p' THEN 'PRIMARY KEY' WHEN 'u' THEN 'UNIQUE'
           WHEN 'c' THEN 'CHECK'       WHEN 'f' THEN 'FOREIGN KEY'
           WHEN 'x' THEN 'EXCLUDE'     WHEN 'n' THEN 'NOT NULL'
           ELSE con.contype::text
       END AS con_type,
       pg_get_constraintdef(con.oid) AS def,
       NOT con.convalidated AS not_valid,
       NOT (%s) AS not_enforced,
       obj_description(con.oid, 'pg_constraint') AS comment
FROM   pg_constraint con
JOIN   pg_class c     ON c.oid = con.conrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  c.relispartition
AND    c.relkind IN ('r', 'p', 'f')
-- Both conditions together (not conislocal alone, as introspectConstraints
-- uses above for a top-level table) are what isolate a genuinely
-- non-inherited row here — see this function's own doc comment for why a
-- partition additionally needs the coninhcount check: unlike a plain
-- table under classic INHERITS, PRIMARY KEY/UNIQUE/FOREIGN KEY/EXCLUDE (and,
-- PostgreSQL 18+, NOT NULL) all DO propagate onto a declarative partition
-- with their own conislocal=false row, so conislocal alone isn't a reliable
-- signal here the way it is for classic multi-parent inheritance's
-- CHECK-only propagation.
AND    con.conislocal
AND    con.coninhcount = 0
AND    n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
ORDER  BY n.nspname, c.relname, con.conname`, enforcedCol)

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect partition constraints: %w", err)
	}
	defer rs.Close()

	for rs.Next() {
		var schema, table, name, typ, expr string
		var notValid, notEnforced bool
		var comment *string
		if err := rs.Scan(&schema, &table, &name, &typ, &expr, &notValid, &notEnforced, &comment); err != nil {
			return err
		}
		part, ok := byKey[schema+"."+table]
		if !ok {
			continue
		}
		// Same stripping as introspectConstraints above — pg_get_constraintdef
		// bakes NOT VALID/NOT ENFORCED into def itself.
		if notValid {
			expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), "NOT VALID"))
		}
		if notEnforced {
			expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), "NOT ENFORCED"))
		}
		part.Constraints = append(part.Constraints, &ir.Constraint{
			Name:        name,
			Type:        typ,
			Expr:        expr,
			NotValid:    notValid,
			NotEnforced: notEnforced,
			Comment:     comment,
		})
	}
	return rs.Err()
}

// parsePartitionKey converts a pg_get_partkeydef result (e.g. "RANGE (logdate)")
// into an ir.PartitionSpec.
func parsePartitionKey(keyDef string) *ir.PartitionSpec {
	spec := &ir.PartitionSpec{}
	upper := strings.ToUpper(keyDef)
	for _, strategy := range []string{"RANGE", "LIST", "HASH"} {
		if strings.HasPrefix(upper, strategy) {
			spec.Strategy = strategy
			rest := strings.TrimSpace(keyDef[len(strategy):])
			if len(rest) >= 2 && rest[0] == '(' && rest[len(rest)-1] == ')' {
				rest = rest[1 : len(rest)-1]
			}
			for col := range strings.SplitSeq(rest, ",") {
				if col = strings.TrimSpace(col); col != "" {
					spec.Columns = append(spec.Columns, col)
				}
			}
			break
		}
	}
	return spec
}
