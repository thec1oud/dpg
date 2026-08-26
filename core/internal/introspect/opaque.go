package introspect

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// firstNormalObjectID is PostgreSQL's FirstNormalObjectId. Catalog rows with a
// lower OID are built-in system objects (e.g. the hundreds of pg_catalog casts
// and collations) that DPG must never dump or diff.
const firstNormalObjectID = 16384

// skipWarningsKey is the context key a caller uses to opt into collecting
// unreconstructable-object warnings (RFC Section 18.4) — e.g. a COLLATION
// with no LOCALE PostgreSQL will surface, or an OPERATOR CLASS with no
// reconstructable member. Without a sink installed via WithSkipWarnings,
// recordSkip is a no-op: only `dpg dump` (the command that actually loses
// information by omitting these objects) needs to know about them; verify/
// plan --live diff against the live catalog directly and are unaffected.
type skipWarningsKey struct{}

// WithSkipWarnings returns a context that causes introspection to append a
// human-readable message to *sink for every object it silently omits
// because nothing about it was reconstructable as valid DDL.
func WithSkipWarnings(ctx context.Context, sink *[]string) context.Context {
	return context.WithValue(ctx, skipWarningsKey{}, sink)
}

func recordSkip(ctx context.Context, format string, args ...any) {
	if sink, ok := ctx.Value(skipWarningsKey{}).(*[]string); ok {
		*sink = append(*sink, fmt.Sprintf(format, args...))
	}
}

// notExtensionOwned is a SQL predicate that excludes catalog rows owned by an
// extension (pg_depend deptype 'e'). Such objects are managed by CREATE
// EXTENSION — introspecting them as standalone objects would make `dump` emit
// them and `plan --live` propose dropping them. catalog is the row's system
// catalog regclass (e.g. "pg_cast") and aliasOID references that row's oid.
func notExtensionOwned(catalog, aliasOID string) string {
	return fmt.Sprintf(
		"NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = '%s'::regclass AND d.objid = %s AND d.deptype = 'e')",
		catalog, aliasOID)
}

// notInternallyDependent excludes catalog rows PostgreSQL creates itself as
// an automatic ('i' = "internal") consequence of some other object's own
// creation — e.g. the CAST from a RANGE type to its auto-generated
// multirange companion, created the moment the range type is declared and
// tied to it for as long as it exists. Such a row is not an independently
// manageable DPG object: it cannot be explicitly re-created from scratch
// (CREATE CAST on that exact pairing fails "already exists" even against a
// database that never had one explicitly declared) and PostgreSQL refuses
// to drop it on its own (it can only go away via CASCADE-dropping the
// parent). Found live-testing a demo project: introspectCasts had no such
// filter, so dpg dump proposed `DROP CAST` on one of these — a migration
// step that would fail live and abort the whole transaction.
func notInternallyDependent(catalog, aliasOID string) string {
	return fmt.Sprintf(
		"NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = '%s'::regclass AND d.objid = %s AND d.deptype = 'i')",
		catalog, aliasOID)
}

// ── DDL reconstruction helpers ────────────────────────────────────────────────

// canonicalDDL runs a reconstructed CREATE statement through pg_query
// parse→deparse so its text matches, byte-for-byte, the compiler's rawSQL()
// output for the same statement. The compiler stores every opaque object's Body
// as pg_query.Deparse of the parsed source, and the snapshot persists only
// sha256(TrimSpace(Body)); canonicalisation is what keeps an introspected Body's
// hash equal to the compiled one, so `dump`/`verify`/`plan --live` stay drift-free.
// On any parse/deparse error it returns the trimmed input unchanged.
func canonicalDDL(sql string) string {
	sql = strings.TrimSpace(sql)
	res, err := pg_query.Parse(sql)
	if err != nil || len(res.Stmts) == 0 {
		return sql
	}
	out, err := pg_query.Deparse(res)
	if err != nil {
		return sql
	}
	return out
}

// quoteIdent double-quotes an identifier for safe interpolation. canonicalDDL
// removes quoting that PostgreSQL considers unnecessary, so over-quoting here is
// harmless.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// qualIdentQ renders an optionally schema-qualified identifier.
func qualIdentQ(schema, name string) string {
	if schema == "" {
		return quoteIdent(name)
	}
	return quoteIdent(schema) + "." + quoteIdent(name)
}

// quoteLit single-quotes a string literal.
func quoteLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// formatOptions renders a catalog options array (text[] of "key=value" entries,
// as stored in pg_foreign_data_wrapper.fdwoptions, pg_foreign_server.srvoptions,
// and pg_user_mappings.umoptions) into an ` OPTIONS (key 'value', …)` clause.
// Returns "" when there are no options.
func formatOptions(opts []string) string {
	var parts []string
	for _, o := range opts {
		k, v, found := strings.Cut(o, "=")
		if !found {
			continue
		}
		parts = append(parts, k+" "+quoteLit(v))
	}
	if len(parts) == 0 {
		return ""
	}
	return " OPTIONS (" + strings.Join(parts, ", ") + ")"
}

// qualifiedFuncRef builds a "schema.name" reference, or bare "name" when
// schema is empty — same format ir's builder-side Function fields use, so
// diff.qualifyFuncForCompare's normalization applies identically regardless
// of which side (introspected or source-parsed) a comparison value came
// from.
func qualifiedFuncRef(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// optionsToStorageParams parses the same catalog options array formatOptions
// renders (text[] of "key=value" entries) into ir.ForeignDataWrapper/
// ForeignServer/UserMapping's structured Options field.
func optionsToStorageParams(opts []string) []pipeline.StorageParam {
	var out []pipeline.StorageParam
	for _, o := range opts {
		k, v, found := strings.Cut(o, "=")
		if !found {
			continue
		}
		out = append(out, pipeline.StorageParam{Key: k, Value: v})
	}
	return out
}

// nonSensitiveOptionsToStorageParams is optionsToStorageParams for
// UserMapping specifically: password-like keys are skipped entirely (not
// just redacted) so they never enter the structural diff comparison at
// all — see ir.UserMapping.Options' doc comment for why comparing a fixed
// redaction placeholder against the desired side's real declared value
// would otherwise show permanent, spurious drift on every plan.
func nonSensitiveOptionsToStorageParams(opts []string) []pipeline.StorageParam {
	var out []pipeline.StorageParam
	for _, o := range opts {
		k, v, found := strings.Cut(o, "=")
		if !found || isUserMappingPasswordKey(k) {
			continue
		}
		out = append(out, pipeline.StorageParam{Key: k, Value: v})
	}
	return out
}

// userMappingPasswordKeys mirrors internal/linter's passwordColNames — kept
// as a local duplicate (5 strings) rather than a cross-package import, since
// internal/introspect must not depend on internal/linter (introspect sits
// below linter in the dependency graph).
var userMappingPasswordKeys = []string{"password", "passwd", "pwd", "secret", "passphrase"}

func isUserMappingPasswordKey(key string) bool {
	lower := strings.ToLower(key)
	for _, kw := range userMappingPasswordKeys {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// UserMappingRedactedPlaceholder replaces a password-like OPTIONS value on
// introspection. pg_user_mappings.umoptions only ever exposes the resolved,
// live credential to a privileged caller — PostgreSQL has no memory of the
// {{secret-uri}} reference (if any) that originally produced it via
// pipeline.ResolveTemplate at apply time, so a dump has no way to recover
// the original reference, only to stop leaking the live plaintext into a
// file that's normally git-committed (see RFC Section 14.10/Section 24). Mirrors
// subscriptionConnInfoPlaceholder's approach. Deliberately does NOT contain
// "{{" so internal/linter's hardcoded-fdw-password rule still hard-errors if
// a user tries to plan/apply the dumped file unmodified (checkUserMapping's
// regex matches the literal "password" key specifically; the broader key
// list here still stops raw exposure for passwd/pwd/secret/passphrase too,
// just without that same automatic lint catch for those four).
const UserMappingRedactedPlaceholder = "live value redacted -- replace with a secret-uri reference before applying"

// formatUserMappingOptions is formatOptions' UserMapping-specific variant:
// redacts password-like OPTIONS values, leaving every other key untouched.
// FDW/Server OPTIONS (formatOptions, still called directly for those) are
// never redacted — RFC Section 24 treats them as non-sensitive by convention.
func formatUserMappingOptions(opts []string) string {
	var parts []string
	for _, o := range opts {
		k, v, found := strings.Cut(o, "=")
		if !found {
			continue
		}
		if isUserMappingPasswordKey(k) {
			v = UserMappingRedactedPlaceholder
		}
		parts = append(parts, k+" "+quoteLit(v))
	}
	if len(parts) == 0 {
		return ""
	}
	return " OPTIONS (" + strings.Join(parts, ", ") + ")"
}

// ── tablespaces ───────────────────────────────────────────────────────────────

func introspectTablespaces(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	const q = `
SELECT spcname,
       pg_tablespace_location(t.oid)               AS location,
       shobj_description(t.oid, 'pg_tablespace')    AS comment,
       r.rolname                                    AS owner,
       t.spcoptions
FROM   pg_tablespace t
JOIN   pg_roles r ON r.oid = t.spcowner
WHERE  spcname NOT IN ('pg_default', 'pg_global')
ORDER  BY spcname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect tablespaces: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, location, owner string
		var comment *string
		var spcoptions []string
		if err := rs.Scan(&name, &location, &comment, &owner, &spcoptions); err != nil {
			return nil, err
		}
		var params []pipeline.StorageParam
		for _, kv := range spcoptions {
			if k, v, ok := strings.Cut(kv, "="); ok {
				params = append(params, pipeline.StorageParam{Key: k, Value: v})
			}
		}
		body := fmt.Sprintf("CREATE TABLESPACE %s OWNER %s LOCATION %s", quoteIdent(name), quoteIdent(owner), quoteLit(location))
		if len(params) > 0 {
			parts := make([]string, len(params))
			for i, p := range params {
				parts[i] = fmt.Sprintf("%s=%s", p.Key, p.Value)
			}
			body += fmt.Sprintf(" WITH (%s)", strings.Join(parts, ", "))
		}
		out = append(out, &ir.Tablespace{Name: name, Location: location, Body: canonicalDDL(body), Comment: comment, Reconstructed: true, Owner: &owner, StorageParams: params})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectTablespaceSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	if err := introspectSimpleGrants(ctx, conn, "pg_tablespace", "spcname", "spcacl", func(name string, g ir.Grant) {
		for _, obj := range out {
			if ts := obj.(*ir.Tablespace); ts.Name == name {
				ts.Grants = append(ts.Grants, g)
				return
			}
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// introspectSimpleGrants populates Grants (via addGrant) for a bare-named
// (no schema) object kind using aclexplode on the given table/name/acl
// columns — shared by Tablespace/ForeignDataWrapper/ForeignServer (RFC audit
// items #4/#5/#6), mirroring introspectTableGrants' established shape.
func introspectSimpleGrants(ctx context.Context, conn pipeline.Querier, table, nameCol, aclCol string, addGrant func(name string, g ir.Grant)) error {
	// grantor <> grantee excludes the owner self-grant PostgreSQL
	// materializes the moment any explicit GRANT touches the object — same
	// fix as introspectTableGrants' identical guard, confirmed live to
	// apply identically to FDW/server/tablespace ACLs.
	q := fmt.Sprintf(`
SELECT t.%s,
       CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END AS grantee,
       pg_get_userbyid(a.grantor) AS grantor,
       a.privilege_type, a.is_grantable
FROM   %s t,
       LATERAL aclexplode(t.%s) a
WHERE  a.grantor <> a.grantee
ORDER  BY t.%s, grantee, a.privilege_type`, nameCol, table, aclCol, nameCol)

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect %s grants: %w", table, err)
	}
	defer rs.Close()

	type grantKey struct{ name, grantee, grantor string }
	type grantEntry struct {
		privs     []string
		grantable bool
	}
	grants := make(map[grantKey]*grantEntry)
	var order []grantKey

	for rs.Next() {
		var name, grantee, grantor, priv string
		var grantable bool
		if err := rs.Scan(&name, &grantee, &grantor, &priv, &grantable); err != nil {
			return err
		}
		k := grantKey{name, grantee, grantor}
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
		e := grants[k]
		grantor := k.grantor
		addGrant(k.name, ir.Grant{
			Privileges: e.privs,
			Roles:      []string{k.grantee},
			WithGrant:  e.grantable,
			GrantedBy:  &grantor,
		})
	}
	return nil
}

// ── foreign data wrappers ─────────────────────────────────────────────────────

func introspectForeignDataWrappers(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT f.fdwname,
       hn.nspname, h.proname,
       vn.nspname, v.proname,
       f.fdwoptions,
       obj_description(f.oid, 'pg_foreign_data_wrapper') AS comment, r.rolname AS owner
FROM   pg_foreign_data_wrapper f
JOIN   pg_roles r ON r.oid = f.fdwowner
LEFT   JOIN pg_proc h      ON h.oid = f.fdwhandler
LEFT   JOIN pg_namespace hn ON hn.oid = h.pronamespace
LEFT   JOIN pg_proc v      ON v.oid = f.fdwvalidator
LEFT   JOIN pg_namespace vn ON vn.oid = v.pronamespace
WHERE  f.oid >= $1
AND    ` + notExtensionOwned("pg_foreign_data_wrapper", "f.oid") + `
ORDER  BY f.fdwname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect foreign data wrappers: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, owner string
		var hSchema, hName, vSchema, vName *string
		var options []string
		var comment *string
		if err := rs.Scan(&name, &hSchema, &hName, &vSchema, &vName, &options, &comment, &owner); err != nil {
			return nil, err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE FOREIGN DATA WRAPPER %s", quoteIdent(name))
		// Emit HANDLER/VALIDATOR only when present. Their absence is the default,
		// so omitting them (rather than writing NO HANDLER/NO VALIDATOR) keeps the
		// reconstruction's canonical deparse identical to a minimal source form.
		handler, validator := "", ""
		if hName != nil {
			fmt.Fprintf(&sb, " HANDLER %s", qualIdentQ(deref(hSchema), *hName))
			handler = qualifiedFuncRef(deref(hSchema), *hName)
		}
		if vName != nil {
			fmt.Fprintf(&sb, " VALIDATOR %s", qualIdentQ(deref(vSchema), *vName))
			validator = qualifiedFuncRef(deref(vSchema), *vName)
		}
		sb.WriteString(formatOptions(options))
		out = append(out, &ir.ForeignDataWrapper{
			Name: name, Handler: handler, Validator: validator, Options: optionsToStorageParams(options),
			Owner: &owner,
			Body:  canonicalDDL(sb.String()), Comment: comment, Reconstructed: true,
		})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectSimpleGrants(ctx, conn, "pg_foreign_data_wrapper", "fdwname", "fdwacl", func(name string, g ir.Grant) {
		for _, obj := range out {
			if f := obj.(*ir.ForeignDataWrapper); f.Name == name {
				f.Grants = append(f.Grants, g)
				return
			}
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ── foreign servers ───────────────────────────────────────────────────────────

func introspectForeignServers(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT s.srvname, f.fdwname, s.srvtype, s.srvversion, s.srvoptions,
       obj_description(s.oid, 'pg_foreign_server') AS comment, r.rolname AS owner
FROM   pg_foreign_server s
JOIN   pg_foreign_data_wrapper f ON f.oid = s.srvfdw
JOIN   pg_roles r ON r.oid = s.srvowner
WHERE  ` + notExtensionOwned("pg_foreign_server", "s.oid") + `
ORDER  BY s.srvname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect foreign servers: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, fdw, owner string
		var srvType, srvVersion *string
		var options []string
		var comment *string
		if err := rs.Scan(&name, &fdw, &srvType, &srvVersion, &options, &comment, &owner); err != nil {
			return nil, err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE SERVER %s", quoteIdent(name))
		if srvType != nil && *srvType != "" {
			fmt.Fprintf(&sb, " TYPE %s", quoteLit(*srvType))
		}
		if srvVersion != nil && *srvVersion != "" {
			fmt.Fprintf(&sb, " VERSION %s", quoteLit(*srvVersion))
		}
		fmt.Fprintf(&sb, " FOREIGN DATA WRAPPER %s", quoteIdent(fdw))
		sb.WriteString(formatOptions(options))
		srv := &ir.ForeignServer{
			Name: name, FDWName: fdw, Options: optionsToStorageParams(options),
			Body: canonicalDDL(sb.String()), Comment: comment, Reconstructed: true, Owner: &owner,
		}
		if srvType != nil && *srvType != "" {
			t := *srvType
			srv.Type = &t
		}
		if srvVersion != nil && *srvVersion != "" {
			v := *srvVersion
			srv.Version = &v
		}
		out = append(out, srv)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectSimpleGrants(ctx, conn, "pg_foreign_server", "srvname", "srvacl", func(name string, g ir.Grant) {
		for _, obj := range out {
			if s := obj.(*ir.ForeignServer); s.Name == name {
				s.Grants = append(s.Grants, g)
				return
			}
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ── user mappings ─────────────────────────────────────────────────────────────

func introspectUserMappings(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// pg_user_mappings redacts umoptions to NULL unless the caller owns the
	// mapping or is a superuser; a redacted mapping is emitted without OPTIONS.
	const q = `
SELECT um.usename, um.srvname, um.umoptions
FROM   pg_user_mappings um
JOIN   pg_foreign_server s ON s.srvname = um.srvname
ORDER  BY um.srvname, um.usename`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect user mappings: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var user *string // NULL == mapping FOR PUBLIC
		var server string
		var options []string
		if err := rs.Scan(&user, &server, &options); err != nil {
			return nil, err
		}
		// pg_user_mappings.usename is the literal "public" for a FOR PUBLIC
		// mapping (never a real role — "public" is reserved). Treat it as the
		// PUBLIC case with an empty User, matching what the compiler records.
		forClause := "PUBLIC"
		irUser := ""
		if user != nil && *user != "public" {
			forClause = quoteIdent(*user)
			irUser = *user
		}
		body := fmt.Sprintf("CREATE USER MAPPING FOR %s SERVER %s%s",
			forClause, quoteIdent(server), formatUserMappingOptions(options))
		out = append(out, &ir.UserMapping{
			User: irUser, Server: server, Options: nonSensitiveOptionsToStorageParams(options),
			Body: canonicalDDL(body), Reconstructed: true,
		})
	}
	return out, rs.Err()
}

// ── event triggers ────────────────────────────────────────────────────────────

func introspectEventTriggers(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT e.evtname, e.evtevent, e.evttags,
       n.nspname, p.proname,
       obj_description(e.oid, 'pg_event_trigger') AS comment,
       r.rolname AS owner,
       e.evtenabled::text
FROM   pg_event_trigger e
JOIN   pg_proc p      ON p.oid = e.evtfoid
JOIN   pg_namespace n ON n.oid = p.pronamespace
JOIN   pg_roles r     ON r.oid = e.evtowner
WHERE  ` + notExtensionOwned("pg_event_trigger", "e.oid") + `
ORDER  BY e.evtname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect event triggers: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, event, fnSchema, fnName, owner, evtenabled string
		var tags []string
		var comment *string
		if err := rs.Scan(&name, &event, &tags, &fnSchema, &fnName, &comment, &owner, &evtenabled); err != nil {
			return nil, err
		}
		// trigger-enable-state (Section 14.1): evtenabled uses the same char
		// codes as pg_trigger.tgenabled — see introspectTriggers' identical
		// mapping.
		var enableState string
		switch evtenabled {
		case "D":
			enableState = "DISABLED"
		case "R":
			enableState = "ENABLE REPLICA"
		case "A":
			enableState = "ENABLE ALWAYS"
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE EVENT TRIGGER %s ON %s", quoteIdent(name), event)
		if len(tags) > 0 {
			quoted := make([]string, len(tags))
			for i, t := range tags {
				quoted[i] = quoteLit(t)
			}
			fmt.Fprintf(&sb, " WHEN TAG IN (%s)", strings.Join(quoted, ", "))
		}
		fmt.Fprintf(&sb, " EXECUTE FUNCTION %s()", qualIdentQ(fnSchema, fnName))
		out = append(out, &ir.EventTrigger{
			Name:          name,
			Event:         event,
			Tags:          tags,
			Function:      fnSchema + "." + fnName,
			Body:          canonicalDDL(sb.String()),
			Comment:       comment,
			Owner:         &owner,
			EnableState:   enableState,
			Reconstructed: true,
		})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectEventTriggerSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── casts ─────────────────────────────────────────────────────────────────────

func introspectCasts(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT format_type(c.castsource, NULL) AS src,
       format_type(c.casttarget, NULL) AS tgt,
       c.castcontext::text, c.castmethod::text,
       n.nspname, p.proname,
       pg_get_function_identity_arguments(p.oid) AS fn_args,
       obj_description(c.oid, 'pg_cast') AS comment
FROM   pg_cast c
LEFT   JOIN pg_proc p      ON p.oid = c.castfunc
LEFT   JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE  c.oid >= $1
AND    ` + notExtensionOwned("pg_cast", "c.oid") + `
AND    ` + notInternallyDependent("pg_cast", "c.oid") + `
ORDER  BY src, tgt`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect casts: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var src, tgt, context8, method string
		var fnSchema, fnName, fnArgs, comment *string
		if err := rs.Scan(&src, &tgt, &context8, &method, &fnSchema, &fnName, &fnArgs, &comment); err != nil {
			return nil, err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE CAST (%s AS %s)", src, tgt)
		switch method {
		case "f": // via function
			args := ""
			if fnArgs != nil {
				args = *fnArgs
			}
			fmt.Fprintf(&sb, " WITH FUNCTION %s(%s)", qualIdentQ(deref(fnSchema), deref(fnName)), args)
		case "i": // via I/O conversion
			sb.WriteString(" WITH INOUT")
		default: // 'b' — binary-coercible
			sb.WriteString(" WITHOUT FUNCTION")
		}
		switch context8 {
		case "a":
			sb.WriteString(" AS ASSIGNMENT")
		case "i":
			sb.WriteString(" AS IMPLICIT")
		}
		function := ""
		if fnName != nil {
			function = deref(fnName)
			if fnSchema != nil && *fnSchema != "" {
				function = *fnSchema + "." + function
			}
		}
		out = append(out, &ir.Cast{
			SourceType:    ir.TypeRef{Name: src},
			TargetType:    ir.TypeRef{Name: tgt},
			Method:        method,
			Context:       context8,
			Function:      function,
			Body:          canonicalDDL(sb.String()),
			Comment:       comment,
			Reconstructed: true,
		})
	}
	return out, rs.Err()
}

// ── publications ──────────────────────────────────────────────────────────────

func introspectPublications(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT p.oid, p.pubname, p.puballtables,
       p.pubinsert, p.pubupdate, p.pubdelete, p.pubtruncate,
       pg_get_userbyid(p.pubowner) AS owner,
       obj_description(p.oid, 'pg_publication') AS comment
FROM   pg_publication p
WHERE  ` + notExtensionOwned("pg_publication", "p.oid") + `
ORDER  BY p.pubname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect publications: %w", err)
	}
	defer rs.Close()

	type pubRow struct {
		oid                             uint32
		name                            string
		allTables, ins, upd, del, trunc bool
		owner                           string
		comment                         *string
	}
	var rows []pubRow
	for rs.Next() {
		var r pubRow
		if err := rs.Scan(&r.oid, &r.name, &r.allTables, &r.ins, &r.upd, &r.del, &r.trunc, &r.owner, &r.comment); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	var out []pipeline.IRObject
	for _, r := range rows {
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE PUBLICATION %s", quoteIdent(r.name))
		if r.allTables {
			sb.WriteString(" FOR ALL TABLES")
		} else {
			targets, err := publicationTargets(ctx, conn, r.oid)
			if err != nil {
				return nil, err
			}
			if targets != "" {
				fmt.Fprintf(&sb, " %s", targets)
			}
		}
		if opt, ok := publishOption(r.ins, r.upd, r.del, r.trunc); ok {
			fmt.Fprintf(&sb, " WITH (publish = %s)", quoteLit(opt))
		}
		owner := r.owner
		pub := &ir.Publication{
			Name: r.name, AllTables: r.allTables,
			Insert: r.ins, Update: r.upd, Delete: r.del, Truncate: r.trunc,
			Owner: &owner,
			Body:  canonicalDDL(sb.String()), Comment: r.comment, Reconstructed: true,
		}
		if !r.allTables {
			tables, err := publicationTableRefs(ctx, conn, r.oid)
			if err != nil {
				return nil, err
			}
			pub.Tables = tables
			filtered, err := publicationHasFilteredTables(ctx, conn, r.oid)
			if err != nil {
				return nil, err
			}
			pub.HasFilteredTables = filtered
		}
		out = append(out, pub)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectPublicationSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── subscriptions ────────────────────────────────────────────────────────────

// subscriptionConnInfoPlaceholder replaces the live CONNECTION string on
// introspection: pg_subscription.subconninfo has no default grant to PUBLIC
// (confirmed live — every other pg_subscription column keeps its default
// grant), and even a privileged caller who CAN read it would have no way to
// map the resolved value back to whatever {{secret-uri}} the original
// CONNECTION clause held, if any — same inherent limitation already
// documented for UserMapping OPTIONS (Section 14.10/item 6ff). subconninfo is never
// selected at all, by design, not merely omitted on error.
//
// Must be syntactically valid libpq keyword/value conninfo syntax, not just
// any string: PostgreSQL parses CONNECTION's value as conninfo at CREATE
// SUBSCRIPTION time even with WITH (connect = false) — confirmed live, a
// bare comment-text placeholder errors "invalid connection string syntax"
// outright, connect = false only skips the network dial, not parsing. The
// warning itself lives inside the (quoted, libpq-legal) password field's
// value so it still reads clearly in a dump.
const subscriptionConnInfoPlaceholder = "host=REDACTED port=0 dbname=REDACTED user=REDACTED " +
	"password='live value redacted -- re-declare CONNECTION manually before enabling'"

// introspectSubscriptions reads every Subscription attribute except
// subconninfo (see subscriptionConnInfoPlaceholder). Reconstructed: true
// excludes Body from the drift comparison entirely (sourceBodyHash returns ""
// for a reconstructed body), so the always-different placeholder CONNECTION
// never causes a spurious DROP+CREATE loop; introspecting at all is what
// closes the actual bug this fixes — without it, plan --live has no entry
// for an already-applied subscription and proposes a spurious re-CREATE that
// then errors on apply (item 6z).
//
// The reconstructed WITH clause always forces connect = false, create_slot =
// false, and enabled = false, regardless of the live subscription's actual
// state: subscriptionConnInfoPlaceholder is never a real conninfo, and
// PostgreSQL's own default (connect = true) would have CREATE SUBSCRIPTION
// try to dial that literal placeholder string and error outright — confirmed
// live, not assumed. Forcing all three keeps the reconstructed body valid,
// re-executable SQL (the same guarantee every other reliable-tier kind's
// Body already carries, proven by TestRenderOpaqueObjectsCompile), at the
// cost of the reconstruction never reflecting a currently-enabled live
// subscription's true state — acceptable, since the placeholder's own text
// already tells the reader this is an inert skeleton to hand-edit, not a
// live clone.
//
// pg_subscription is a shared, cluster-wide catalog (tablespace pg_global) —
// querying it from any database returns EVERY database's subscriptions, not
// just the current one (confirmed live), so subdbid must be filtered
// explicitly against the connected database's own oid; nothing else in this
// file needs that pattern, since every other reliable-tier catalog is
// already database-local.
//
// Column availability varies by PostgreSQL version (confirmed against the
// 14/15/16/17 pg_subscription catalog docs, not assumed from memory):
// subtwophasestate/subdisableonerr (15+), subpasswordrequired/subrunasowner
// (16+), subfailover (17+) don't exist on older servers. Each is
// select-guarded by serverVersionNum, substituting that option's own
// documented default (confirmed live against a fresh subscription, not the
// docs alone — the current docs page's prose default for `streaming` does
// not match live PG17 behavior) so it's simply omitted from the
// reconstructed WITH clause on an older server, identical to an unset
// option. subskiplsn is deliberately not read at all: it's one-shot admin
// state (ALTER SUBSCRIPTION ... SKIP), not part of a subscription's
// declarative definition, with no WITH-option equivalent.
func introspectSubscriptions(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	v := serverVersionNum(ctx, conn)
	twoPhaseCol, disableOnErrCol := `'d'::"char"`, "false"
	if v >= 150000 {
		twoPhaseCol, disableOnErrCol = "s.subtwophasestate", "s.subdisableonerr"
	}
	pwReqCol, runAsOwnerCol, originCol := "true", "false", "'any'::text"
	if v >= 160000 {
		pwReqCol, runAsOwnerCol, originCol = "s.subpasswordrequired", "s.subrunasowner", "COALESCE(s.suborigin, 'any')"
	}
	failoverCol := "false"
	if v >= 170000 {
		failoverCol = "s.subfailover"
	}

	// subenabled itself is never selected: the reconstructed body always
	// forces enabled = false (see the withOpts comment below), so the live
	// enabled/disabled state has nothing to feed into.
	q := fmt.Sprintf(`
SELECT s.subname, s.subbinary, s.substream, %s, %s,
       %s, %s, %s, s.subslotname, s.subsynccommit, s.subpublications, %s,
       obj_description(s.oid, 'pg_subscription') AS comment, r.rolname AS owner
FROM   pg_subscription s
JOIN   pg_roles r ON r.oid = s.subowner
WHERE  s.subdbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER  BY s.subname`, twoPhaseCol, disableOnErrCol, pwReqCol, runAsOwnerCol, failoverCol, originCol)

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect subscriptions: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var name, owner string
		var binary, disableOnErr, pwReq, runAsOwner, failover bool
		var stream, twoPhase byte
		var slotName *string
		var syncCommit, origin string
		var publications []string
		var comment *string
		if err := rs.Scan(&name, &binary, &stream, &twoPhase,
			&disableOnErr, &pwReq, &runAsOwner, &failover, &slotName, &syncCommit,
			&publications, &origin, &comment, &owner); err != nil {
			return nil, err
		}

		quotedPubs := make([]string, len(publications))
		for i, p := range publications {
			quotedPubs[i] = quoteIdent(p)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE SUBSCRIPTION %s CONNECTION %s PUBLICATION %s",
			quoteIdent(name), quoteLit(subscriptionConnInfoPlaceholder), strings.Join(quotedPubs, ", "))

		// connect/create_slot/enabled are always forced false, regardless of
		// the live subscription's actual state: subscriptionConnInfoPlaceholder
		// is never a real conninfo, so the PostgreSQL default (connect = true)
		// would have CREATE SUBSCRIPTION try to dial that literal placeholder
		// string and fail outright — confirmed live. This keeps the
		// reconstructed body valid, re-executable SQL (same guarantee every
		// other reliable-tier kind's Body already has), at the cost of never
		// reflecting the live enabled state; the placeholder's own text
		// ("re-declare manually") already tells the reader this is an inert
		// skeleton, not a live clone.
		withOpts := []string{"connect = false", "create_slot = false", "enabled = false"}
		if binary {
			withOpts = append(withOpts, "binary = true")
		}
		switch stream {
		case 't':
			withOpts = append(withOpts, "streaming = on")
		case 'p':
			withOpts = append(withOpts, "streaming = parallel")
		}
		if twoPhase != 'd' {
			withOpts = append(withOpts, "two_phase = true")
		}
		if disableOnErr {
			withOpts = append(withOpts, "disable_on_error = true")
		}
		if !pwReq {
			withOpts = append(withOpts, "password_required = false")
		}
		if runAsOwner {
			withOpts = append(withOpts, "run_as_owner = true")
		}
		if failover {
			withOpts = append(withOpts, "failover = true")
		}
		if origin != "any" {
			withOpts = append(withOpts, "origin = "+quoteLit(origin))
		}
		if slotName == nil {
			withOpts = append(withOpts, "slot_name = NONE")
		} else if *slotName != name {
			withOpts = append(withOpts, "slot_name = "+quoteLit(*slotName))
		}
		if syncCommit != "off" {
			withOpts = append(withOpts, "synchronous_commit = "+quoteLit(syncCommit))
		}
		if len(withOpts) > 0 {
			fmt.Fprintf(&sb, " WITH (%s)", strings.Join(withOpts, ", "))
		}

		out = append(out, &ir.Subscription{
			Name: name, ConnInfo: subscriptionConnInfoPlaceholder,
			Owner: &owner,
			Body:  canonicalDDL(sb.String()), Comment: comment, Reconstructed: true,
		})
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	if err := introspectSubscriptionSecurityLabels(ctx, conn, out); err != nil {
		return nil, err
	}
	return out, nil
}

// publicationTableRefs is the structured counterpart to publicationTargets'
// own table query (same underlying pg_publication_rel/pg_class join),
// returning ir.PublicationTableRef instead of pre-quoted display strings —
// used for RFC Section 13.1's structured Tables diffing input rather than Body
// reconstruction.
func publicationTableRefs(ctx context.Context, conn pipeline.Querier, pubid uint32) ([]ir.PublicationTableRef, error) {
	rs, err := conn.QueryRows(ctx, `
SELECT n.nspname, c.relname
FROM   pg_publication_rel pr
JOIN   pg_class c     ON c.oid = pr.prrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  pr.prpubid = $1
ORDER  BY n.nspname, c.relname`, pubid)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []ir.PublicationTableRef
	for rs.Next() {
		var schema, name string
		if err := rs.Scan(&schema, &name); err != nil {
			return nil, err
		}
		out = append(out, ir.PublicationTableRef{Schema: schema, Name: name})
	}
	return out, rs.Err()
}

// publicationHasFilteredTables reports whether any of a publication's
// FOR TABLE entries carries an explicit column list or WHERE row-filter —
// see ir.Publication.HasFilteredTables' doc comment for why this needs
// pg_publication_rel.prattrs/prqual directly (NULL exactly when no filter
// was written) rather than pg_publication_tables.attnames, which always
// resolves to concrete column names either way and so can't distinguish
// "no explicit list" from "explicit list happening to name every column."
func publicationHasFilteredTables(ctx context.Context, conn pipeline.Querier, pubid uint32) (bool, error) {
	rs, err := conn.QueryRows(ctx, `
SELECT EXISTS (
    SELECT 1 FROM pg_publication_rel pr
    WHERE pr.prpubid = $1 AND (pr.prattrs IS NOT NULL OR pr.prqual IS NOT NULL)
)`, pubid)
	if err != nil {
		return false, err
	}
	defer rs.Close()
	var filtered bool
	if rs.Next() {
		if err := rs.Scan(&filtered); err != nil {
			return false, err
		}
	}
	return filtered, rs.Err()
}

// publicationTargets builds the FOR TABLE / FOR TABLES IN SCHEMA clauses for a
// non-FOR-ALL-TABLES publication.
func publicationTargets(ctx context.Context, conn pipeline.Querier, pubid uint32) (string, error) {
	tables, err := scanQualNames(ctx, conn, `
SELECT n.nspname, c.relname
FROM   pg_publication_rel pr
JOIN   pg_class c     ON c.oid = pr.prrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  pr.prpubid = $1
ORDER  BY n.nspname, c.relname`, pubid)
	if err != nil {
		return "", err
	}
	schemas, err := scanNames(ctx, conn, `
SELECT n.nspname
FROM   pg_publication_namespace pn
JOIN   pg_namespace n ON n.oid = pn.pnnspid
WHERE  pn.pnpubid = $1
ORDER  BY n.nspname`, pubid)
	if err != nil {
		return "", err
	}
	// A publication takes ONE FOR clause with a comma-separated list of
	// publication objects: FOR TABLE a, b, TABLES IN SCHEMA s1, TABLES IN SCHEMA s2.
	// Repeating FOR (FOR TABLE … FOR TABLES IN SCHEMA …) is a syntax error.
	var objects []string
	if len(tables) > 0 {
		objects = append(objects, "TABLE "+strings.Join(tables, ", "))
	}
	for _, s := range schemas {
		objects = append(objects, "TABLES IN SCHEMA "+quoteIdent(s))
	}
	if len(objects) == 0 {
		return "", nil
	}
	return "FOR " + strings.Join(objects, ", "), nil
}

// publishOption returns the publish= value (e.g. "insert, update") and whether a
// WITH (publish = …) clause is needed. It reports false for the default
// (all four operations enabled), so no clause is emitted; for any other set —
// including the degenerate all-disabled case (value "") — it reports true.
func publishOption(ins, upd, del, trunc bool) (string, bool) {
	if ins && upd && del && trunc {
		return "", false
	}
	var ops []string
	if ins {
		ops = append(ops, "insert")
	}
	if upd {
		ops = append(ops, "update")
	}
	if del {
		ops = append(ops, "delete")
	}
	if trunc {
		ops = append(ops, "truncate")
	}
	return strings.Join(ops, ", "), true
}

// ── collations ────────────────────────────────────────────────────────────────

func introspectCollations(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// initdb imports the system locale collations into pg_catalog with normal
	// OIDs, so the namespace filter (not just oid >= FirstNormalObjectId) is
	// what keeps introspection to user-declared collations.
	//
	// The ICU locale column moved across releases: colllocale (PG17+),
	// colliculocale (PG15–16); on older releases the locale lives in
	// collcollate. Select the right column for the live server version.
	icuLocaleCol := "NULL::text"
	// collicurules (RFC audit item #111's RULES) was added in PG16 —
	// confirmed against PostgreSQL's own catalog docs, same version-gating
	// pattern as the ICU locale column above.
	rulesCol := "NULL::text"
	switch v := serverVersionNum(ctx, conn); {
	case v >= 170000:
		icuLocaleCol = "c.colllocale"
		rulesCol = "c.collicurules"
	case v >= 160000:
		icuLocaleCol = "c.colliculocale"
		rulesCol = "c.collicurules"
	case v >= 150000:
		icuLocaleCol = "c.colliculocale"
	}
	q := `
SELECT n.nspname, c.collname, c.collprovider,
       c.collcollate, c.collctype, c.collisdeterministic, ` + icuLocaleCol + `, ` + rulesCol + `,
       obj_description(c.oid, 'pg_collation') AS comment, r.rolname AS owner
FROM   pg_collation c
JOIN   pg_namespace n ON n.oid = c.collnamespace
JOIN   pg_roles r ON r.oid = c.collowner
WHERE  c.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_collation", "c.oid") + `
ORDER  BY n.nspname, c.collname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect collations: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, owner string
		var provider byte
		var collate, ctype, icuLocale, rules, comment *string
		var deterministic bool
		if err := rs.Scan(&schema, &name, &provider, &collate, &ctype, &deterministic, &icuLocale, &rules, &comment, &owner); err != nil {
			return nil, err
		}
		var attrs []string
		switch provider {
		case 'i': // icu — the locale is in icuLocale (or collcollate on old servers)
			loc := firstNonEmpty(icuLocale, collate)
			if loc == "" {
				// Cannot reconstruct a valid ICU collation; skip rather than
				// emit DDL that would fail to apply (LOCALE is required).
				recordSkip(ctx, "collation %s: ICU provider with no reported LOCALE — omitted, re-declare manually",
					qualIdentQ(schema, name))
				continue
			}
			attrs = append(attrs, "PROVIDER = icu", "LOCALE = "+quoteLit(loc))
			if rules != nil && *rules != "" {
				attrs = append(attrs, "RULES = "+quoteLit(*rules))
			}
		default: // 'c' (libc) / 'd' (database default) / 'b' (builtin)
			if collate != nil && ctype != nil && *collate == *ctype {
				attrs = append(attrs, "LOCALE = "+quoteLit(*collate))
			} else {
				if collate != nil {
					attrs = append(attrs, "LC_COLLATE = "+quoteLit(*collate))
				}
				if ctype != nil {
					attrs = append(attrs, "LC_CTYPE = "+quoteLit(*ctype))
				}
			}
		}
		if !deterministic {
			attrs = append(attrs, "DETERMINISTIC = false")
		}
		if len(attrs) == 0 {
			// nothing reconstructable; skip rather than emit invalid DDL
			recordSkip(ctx, "collation %s: no reconstructable attributes — omitted, re-declare manually",
				qualIdentQ(schema, name))
			continue
		}
		body := fmt.Sprintf("CREATE COLLATION %s (%s)", qualIdentQ(schema, name), strings.Join(attrs, ", "))
		out = append(out, &ir.Collation{
			Schema: schema, Name: name,
			Provider: string(provider), Collate: collate, Ctype: ctype, ICULocale: icuLocale, Deterministic: deterministic,
			Rules: rules,
			Body:  canonicalDDL(body), Comment: comment, Reconstructed: true, Owner: &owner,
		})
	}
	return out, rs.Err()
}

// serverVersionNum returns the live server's server_version_num (e.g. 170004),
// or 0 if it cannot be read.
func serverVersionNum(ctx context.Context, conn pipeline.Querier) int {
	rs, err := conn.QueryRows(ctx, "SHOW server_version_num")
	if err != nil {
		return 0
	}
	defer rs.Close()
	if rs.Next() {
		var v string
		if err := rs.Scan(&v); err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// firstNonEmpty returns the first non-nil, non-empty pointed-to string, or "".
func firstNonEmpty(ptrs ...*string) string {
	for _, p := range ptrs {
		if p != nil && *p != "" {
			return *p
		}
	}
	return ""
}

// ── extended statistics ───────────────────────────────────────────────────────

// statsKindNames maps pg_statistic_ext.stxkind's single-letter catalog
// codes to DPG's own source spelling — confirmed live against a real
// PostgreSQL 17 server (three throwaway single-kind statistics objects,
// one per keyword, each checked individually against its resulting
// stxkind). 'e' (has-expressions) is deliberately excluded: it's an
// internal marker PostgreSQL adds automatically whenever any element is an
// expression, never a kind the user can request — confirmed live: `CREATE
// STATISTICS s (ndistinct) ON a, (b+1) FROM t` yields stxkind = {d,e}, not
// {d} alone.
var statsKindNames = map[string]string{"d": "ndistinct", "f": "dependencies", "m": "mcv"}

func introspectStatistics(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT s.oid, n.nspname, s.stxname, s.stxkind::text[], s.stxstattarget,
       tn.nspname, c.relname,
       pg_get_statisticsobjdef(s.oid),
       obj_description(s.oid, 'pg_statistic_ext') AS comment
FROM   pg_statistic_ext s
JOIN   pg_namespace n  ON n.oid = s.stxnamespace
JOIN   pg_class c      ON c.oid = s.stxrelid
JOIN   pg_namespace tn ON tn.oid = c.relnamespace
WHERE  ` + notExtensionOwned("pg_statistic_ext", "s.oid") + `
ORDER  BY n.nspname, s.stxname`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("introspect statistics: %w", err)
	}
	type statRow struct {
		oid                uint32
		schema, name       string
		kind               []string
		target             *int
		tblSchema, tblName string
		def                string
		comment            *string
	}
	var rows []statRow
	for rs.Next() {
		var r statRow
		if err := rs.Scan(&r.oid, &r.schema, &r.name, &r.kind, &r.target, &r.tblSchema, &r.tblName, &r.def, &r.comment); err != nil {
			rs.Close()
			return nil, err
		}
		rows = append(rows, r)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, err
	}
	rs.Close()

	var out []pipeline.IRObject
	for _, r := range rows {
		var kinds []string
		for _, k := range r.kind {
			if name, ok := statsKindNames[k]; ok {
				kinds = append(kinds, name)
			}
		}
		columns, err := statisticsObjectColumns(ctx, conn, r.oid)
		if err != nil {
			return nil, err
		}
		out = append(out, &ir.StatisticsObject{
			Schema: r.schema, Name: r.name,
			Table: qualifiedFuncRef(r.tblSchema, r.tblName), Kinds: kinds, Columns: columns, StatisticsTarget: r.target,
			Body: canonicalDDL(r.def), Comment: r.comment, Reconstructed: true,
		})
	}
	return out, nil
}

// statisticsObjectColumns resolves a statistics object's plain column names
// (via stxkeys → pg_attribute, since stxkeys is only an attnum list) and
// expressions (via pg_get_statisticsobjdef_expressions, a dedicated
// PostgreSQL convenience function returning already-canonicalized
// expression text — confirmed live to match the same deparse form the
// builder produces via nodeToText, so both sides compare equal regardless
// of source formatting). Order is not preserved across the two sources
// (plain columns and expressions are fetched independently); Columns is
// compared as a set in the differ, so this doesn't affect correctness.
func statisticsObjectColumns(ctx context.Context, conn pipeline.Querier, statOID uint32) ([]string, error) {
	names, err := scanNames(ctx, conn, `
SELECT a.attname
FROM   pg_statistic_ext s
JOIN   pg_attribute a ON a.attrelid = s.stxrelid AND a.attnum = ANY(s.stxkeys)
WHERE  s.oid = $1
ORDER  BY array_position(s.stxkeys, a.attnum)`, statOID)
	if err != nil {
		return nil, err
	}
	exprRows, err := conn.QueryRows(ctx, `SELECT pg_get_statisticsobjdef_expressions($1)`, statOID)
	if err != nil {
		return nil, err
	}
	defer exprRows.Close()
	var exprs []string
	if exprRows.Next() {
		if err := exprRows.Scan(&exprs); err != nil {
			return nil, err
		}
	}
	if err := exprRows.Err(); err != nil {
		return nil, err
	}
	return append(names, exprs...), nil
}

// ── operators ─────────────────────────────────────────────────────────────────

func introspectOperators(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// oprcom/oprnegate reference other pg_operator rows by OID; a forward
	// reference (COMMUTATOR pointing at an operator created later) is legal —
	// PostgreSQL creates a shell and links it — so ordering is not a concern.
	q := `
SELECT n.nspname, o.oprname,
       NULLIF(o.oprleft, 0)::regtype::text  AS leftarg,
       NULLIF(o.oprright, 0)::regtype::text AS rightarg,
       o.oprcode::regproc::text             AS fn,
       cn.nspname AS com_schema, co.oprname AS com_name,
       nn.nspname AS neg_schema, no.oprname AS neg_name,
       NULLIF(o.oprrest, 0)::regproc::text   AS restrict_fn,
       NULLIF(o.oprjoin, 0)::regproc::text   AS join_fn,
       o.oprcanmerge, o.oprcanhash,
       obj_description(o.oid, 'pg_operator') AS comment
FROM   pg_operator o
JOIN   pg_namespace n  ON n.oid = o.oprnamespace
LEFT   JOIN pg_operator co ON co.oid = o.oprcom
LEFT   JOIN pg_namespace cn ON cn.oid = co.oprnamespace
LEFT   JOIN pg_operator no ON no.oid = o.oprnegate
LEFT   JOIN pg_namespace nn ON nn.oid = no.oprnamespace
WHERE  o.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_operator", "o.oid") + `
ORDER  BY n.nspname, o.oprname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect operators: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, fn string
		var leftArg, rightArg, comSchema, comName, negSchema, negName, restrictFn, joinFn, comment *string
		var canMerge, canHash bool
		if err := rs.Scan(&schema, &name, &leftArg, &rightArg, &fn,
			&comSchema, &comName, &negSchema, &negName, &restrictFn, &joinFn,
			&canMerge, &canHash, &comment); err != nil {
			return nil, err
		}
		var parts []string
		parts = append(parts, "FUNCTION = "+fn)
		if leftArg != nil {
			parts = append(parts, "LEFTARG = "+*leftArg)
		}
		if rightArg != nil {
			parts = append(parts, "RIGHTARG = "+*rightArg)
		}
		if comName != nil {
			parts = append(parts, "COMMUTATOR = OPERATOR("+operatorRef(deref(comSchema), *comName)+")")
		}
		if negName != nil {
			parts = append(parts, "NEGATOR = OPERATOR("+operatorRef(deref(negSchema), *negName)+")")
		}
		if restrictFn != nil {
			parts = append(parts, "RESTRICT = "+*restrictFn)
		}
		if joinFn != nil {
			parts = append(parts, "JOIN = "+*joinFn)
		}
		if canHash {
			parts = append(parts, "HASHES")
		}
		if canMerge {
			parts = append(parts, "MERGES")
		}
		body := fmt.Sprintf("CREATE OPERATOR %s (%s)", operatorRef(schema, name), strings.Join(parts, ", "))
		op := &ir.Operator{Schema: schema, Name: name, Body: canonicalDDL(body), Comment: comment, Reconstructed: true}
		// Restrict/Join/Commutator/Negator/Hashes/Merges (RFC Section 14.3)
		// structured fields, populated from the exact same catalog data
		// already fetched to reconstruct Body above — previously discarded
		// after building the Body string, forcing diffOperator's
		// predecessor (the generic opaque hash compare) to treat every
		// hint-only live change as an unrecognized drift needing a full
		// DROP+CREATE.
		if restrictFn != nil {
			op.Restrict = restrictFn
		}
		if joinFn != nil {
			op.Join = joinFn
		}
		if comName != nil {
			com := qualifiedFuncRef(deref(comSchema), *comName)
			op.Commutator = &com
		}
		if negName != nil {
			neg := qualifiedFuncRef(deref(negSchema), *negName)
			op.Negator = &neg
		}
		op.Hashes = canHash
		op.Merges = canMerge
		if leftArg != nil {
			t := ir.TypeRef{Name: *leftArg}
			op.LeftType = &t
		}
		if rightArg != nil {
			t := ir.TypeRef{Name: *rightArg}
			op.RightType = &t
		}
		out = append(out, op)
	}
	return out, rs.Err()
}

// operatorRef renders an optionally schema-qualified operator name. The operator
// symbol itself (e.g. "===", "@>") is never quoted — quoting is only applied to
// the schema identifier.
func operatorRef(schema, name string) string {
	if schema == "" {
		return name
	}
	return quoteIdent(schema) + "." + name
}

// ── text search: parsers ──────────────────────────────────────────────────────

func introspectTSParsers(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT n.nspname, p.prsname,
       p.prsstart::regproc::text    AS start_fn,
       p.prstoken::regproc::text    AS token_fn,
       p.prsend::regproc::text      AS end_fn,
       p.prslextype::regproc::text  AS lextype_fn,
       NULLIF(p.prsheadline, 0)::regproc::text AS headline_fn,
       obj_description(p.oid, 'pg_ts_parser') AS comment
FROM   pg_ts_parser p
JOIN   pg_namespace n ON n.oid = p.prsnamespace
WHERE  p.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_ts_parser", "p.oid") + `
ORDER  BY n.nspname, p.prsname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect text search parsers: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, startFn, tokenFn, endFn, lextypeFn string
		var headlineFn, comment *string
		if err := rs.Scan(&schema, &name, &startFn, &tokenFn, &endFn, &lextypeFn, &headlineFn, &comment); err != nil {
			return nil, err
		}
		parts := []string{
			"START = " + startFn,
			"GETTOKEN = " + tokenFn,
			"END = " + endFn,
			"LEXTYPES = " + lextypeFn,
		}
		if headlineFn != nil {
			parts = append(parts, "HEADLINE = "+*headlineFn)
		}
		body := fmt.Sprintf("CREATE TEXT SEARCH PARSER %s (%s)", qualIdentQ(schema, name), strings.Join(parts, ", "))
		out = append(out, &ir.TSParser{Schema: schema, Name: name, Body: canonicalDDL(body), Comment: comment, Reconstructed: true})
	}
	return out, rs.Err()
}

// ── text search: templates ────────────────────────────────────────────────────

func introspectTSTemplates(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT n.nspname, t.tmplname,
       NULLIF(t.tmplinit, 0)::regproc::text AS init_fn,
       t.tmpllexize::regproc::text          AS lexize_fn,
       obj_description(t.oid, 'pg_ts_template') AS comment
FROM   pg_ts_template t
JOIN   pg_namespace n ON n.oid = t.tmplnamespace
WHERE  t.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_ts_template", "t.oid") + `
ORDER  BY n.nspname, t.tmplname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect text search templates: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, lexizeFn string
		var initFn, comment *string
		if err := rs.Scan(&schema, &name, &initFn, &lexizeFn, &comment); err != nil {
			return nil, err
		}
		var parts []string
		if initFn != nil {
			parts = append(parts, "INIT = "+*initFn)
		}
		parts = append(parts, "LEXIZE = "+lexizeFn)
		body := fmt.Sprintf("CREATE TEXT SEARCH TEMPLATE %s (%s)", qualIdentQ(schema, name), strings.Join(parts, ", "))
		out = append(out, &ir.TSTemplate{Schema: schema, Name: name, Body: canonicalDDL(body), Comment: comment, Reconstructed: true})
	}
	return out, rs.Err()
}

// ── text search: dictionaries ─────────────────────────────────────────────────

// parseDictInitOption parses pg_ts_dict.dictinitoption's packed
// "key = 'value', key2 = 'value2'" format into ordered key/value pairs
// (audit item #52) — confirmed via a live psql probe against a real
// snowball dictionary. Splits on top-level ", " (a comma immediately
// followed by a space, which real PostgreSQL's own formatting always uses
// here) and unquotes each value the same way a SQL string literal is
// quoted (doubled ” as an escaped single quote) — values are stored raw/
// unquoted, matching every other StorageParam producer in this codebase
// (buildOrderedOptions' nodeToText included), so diffTSDictOptions can
// re-quote uniformly via quoteLit at SQL-emission time regardless of which
// side (source-declared or introspected) a value came from.
func parseDictInitOption(s string) []pipeline.StorageParam {
	var out []pipeline.StorageParam
	for _, part := range strings.Split(s, ", ") {
		key, val, ok := strings.Cut(part, " = ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
			val = strings.ReplaceAll(val[1:len(val)-1], "''", "'")
		}
		out = append(out, pipeline.StorageParam{Key: strings.TrimSpace(key), Value: val})
	}
	return out
}

func introspectTSDicts(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// dictinitoption is stored as the already-rendered option list, e.g.
	// "stopwords = 'english', language = 'english'"; it is appended verbatim.
	// The outer alias must not be "d": notExtensionOwned's subquery aliases
	// pg_depend as "d", and a matching outer alias would be shadowed.
	q := `
SELECT n.nspname, dict.dictname,
       tn.nspname AS tmpl_schema, t.tmplname AS tmpl_name,
       dict.dictinitoption,
       obj_description(dict.oid, 'pg_ts_dict') AS comment,
       r.rolname AS owner
FROM   pg_ts_dict dict
JOIN   pg_namespace n  ON n.oid = dict.dictnamespace
JOIN   pg_ts_template t ON t.oid = dict.dicttemplate
JOIN   pg_namespace tn ON tn.oid = t.tmplnamespace
JOIN   pg_roles r      ON r.oid = dict.dictowner
WHERE  dict.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_ts_dict", "dict.oid") + `
ORDER  BY n.nspname, dict.dictname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect text search dictionaries: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	for rs.Next() {
		var schema, name, tmplSchema, tmplName, owner string
		var initOption, comment *string
		if err := rs.Scan(&schema, &name, &tmplSchema, &tmplName, &initOption, &comment, &owner); err != nil {
			return nil, err
		}
		parts := []string{"TEMPLATE = " + qualIdentQ(tmplSchema, tmplName)}
		var options []pipeline.StorageParam
		if initOption != nil && strings.TrimSpace(*initOption) != "" {
			trimmed := strings.TrimSpace(*initOption)
			parts = append(parts, trimmed)
			options = parseDictInitOption(trimmed)
		}
		body := fmt.Sprintf("CREATE TEXT SEARCH DICTIONARY %s (%s)", qualIdentQ(schema, name), strings.Join(parts, ", "))
		out = append(out, &ir.TSDict{
			Schema: schema, Name: name,
			TemplateSchema: tmplSchema, TemplateName: tmplName,
			Options: options,
			Body:    canonicalDDL(body), Comment: comment, Owner: &owner, Reconstructed: true,
		})
	}
	return out, rs.Err()
}

// ── text search: configurations ───────────────────────────────────────────────

func introspectTSConfigs(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// The reconstructed body is the bare CREATE with its PARSER; token→dictionary
	// mappings (pg_ts_config_map) are a separate IR concern (TSConfig.Mappings)
	// and are not folded into the body.
	q := `
SELECT n.nspname, c.cfgname,
       pn.nspname AS parser_schema, p.prsname AS parser_name,
       obj_description(c.oid, 'pg_ts_config') AS comment,
       r.rolname AS owner
FROM   pg_ts_config c
JOIN   pg_namespace n  ON n.oid = c.cfgnamespace
JOIN   pg_ts_parser p  ON p.oid = c.cfgparser
JOIN   pg_namespace pn ON pn.oid = p.prsnamespace
JOIN   pg_roles r      ON r.oid = c.cfgowner
WHERE  c.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_ts_config", "c.oid") + `
ORDER  BY n.nspname, c.cfgname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect text search configurations: %w", err)
	}
	defer rs.Close()

	var out []pipeline.IRObject
	idx := make(map[string]*ir.TSConfig)
	for rs.Next() {
		var schema, name, parserSchema, parserName, owner string
		var comment *string
		if err := rs.Scan(&schema, &name, &parserSchema, &parserName, &comment, &owner); err != nil {
			return nil, err
		}
		body := fmt.Sprintf("CREATE TEXT SEARCH CONFIGURATION %s (PARSER = %s)",
			qualIdentQ(schema, name), qualIdentQ(parserSchema, parserName))
		tc := &ir.TSConfig{
			Schema: schema, Name: name,
			ParserSchema: parserSchema, ParserName: parserName,
			Body: canonicalDDL(body), Comment: comment, Owner: &owner, Reconstructed: true,
		}
		idx[schema+"."+name] = tc
		out = append(out, tc)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close()

	if err := introspectTSConfigMappings(ctx, conn, idx); err != nil {
		return nil, err
	}
	return out, nil
}

// introspectTSConfigMappings populates Mappings (RFC Section 12.1) for every
// TSConfig in idx from pg_ts_config_map — previously never queried at all
// (a comment here even claimed mappings were "a separate IR concern",
// implying somewhere else handled it, but nothing did: TSConfig.Mappings
// was always empty from introspection). maptokentype is an integer that
// only resolves to a name (e.g. "word", "hword") via ts_token_type(parser
// oid) — there's no direct catalog column with the readable alias. Token
// types are grouped into one MAPPING FOR entry per identical, ordered
// dictionary chain (matching how a human naturally writes it and how a
// live config's own \dF+ output groups them), not emitted one row per
// token type.
func introspectTSConfigMappings(ctx context.Context, conn pipeline.Querier, idx map[string]*ir.TSConfig) error {
	if len(idx) == 0 {
		return nil
	}
	const q = `
SELECT n.nspname, c.cfgname, tt.alias, dn.nspname AS dict_schema, d.dictname, m.mapseqno
FROM   pg_ts_config_map m
JOIN   pg_ts_config c        ON c.oid = m.mapcfg
JOIN   pg_namespace n        ON n.oid = c.cfgnamespace
JOIN   pg_ts_dict d          ON d.oid = m.mapdict
JOIN   pg_namespace dn       ON dn.oid = d.dictnamespace
JOIN   ts_token_type(c.cfgparser) tt ON tt.tokid = m.maptokentype
WHERE  n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
ORDER  BY n.nspname, c.cfgname, tt.alias, m.mapseqno`

	rs, err := conn.QueryRows(ctx, q)
	if err != nil {
		return fmt.Errorf("introspect text search config mappings: %w", err)
	}
	defer rs.Close()

	// Per-config, per-token-type ordered dictionary chain, built up across
	// rows (mapseqno order) before being grouped into MAPPING FOR entries.
	type key struct{ schema, name string }
	chains := make(map[key]map[string][]pipeline.Identifier)
	var order []key
	tokenOrder := make(map[key][]string)
	for rs.Next() {
		var schema, name, tokenAlias, dictSchema, dictName string
		var mapseqno int
		if err := rs.Scan(&schema, &name, &tokenAlias, &dictSchema, &dictName, &mapseqno); err != nil {
			return err
		}
		k := key{schema, name}
		if _, ok := idx[schema+"."+name]; !ok {
			continue
		}
		if chains[k] == nil {
			chains[k] = make(map[string][]pipeline.Identifier)
			order = append(order, k)
		}
		if _, ok := chains[k][tokenAlias]; !ok {
			tokenOrder[k] = append(tokenOrder[k], tokenAlias)
		}
		dictID := pipeline.Identifier{Name: dictName}
		if dictSchema != "pg_catalog" {
			dictID.Schema = dictSchema
		}
		chains[k][tokenAlias] = append(chains[k][tokenAlias], dictID)
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for _, k := range order {
		tc := idx[k.schema+"."+k.name]
		byChain := make(map[string][]string) // chain signature -> token types, in first-seen order
		var chainOrder []string
		chainDicts := make(map[string][]pipeline.Identifier)
		for _, tok := range tokenOrder[k] {
			dicts := chains[k][tok]
			sig := ""
			for _, d := range dicts {
				sig += d.String() + ","
			}
			if _, ok := byChain[sig]; !ok {
				chainOrder = append(chainOrder, sig)
				chainDicts[sig] = dicts
			}
			byChain[sig] = append(byChain[sig], tok)
		}
		for _, sig := range chainOrder {
			tc.Mappings = append(tc.Mappings, pipeline.TSMappingDef{
				TokenTypes:   byChain[sig],
				Dictionaries: chainDicts[sig],
			})
		}
	}
	return nil
}

// ── operator families ─────────────────────────────────────────────────────────

func introspectOperatorFamilies(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	// Every operator family is introspected as a standalone object — including
	// one PostgreSQL auto-created for an unqualified CREATE OPERATOR CLASS —
	// mirroring pg_dump's own model exactly (confirmed live against postgres:17:
	// `pg_dump -s` always emits a separate CREATE OPERATOR FAMILY for every
	// family, auto-created or not, and always gives every class an explicit
	// FAMILY clause; see introspectOperatorClasses).
	//
	// A prior version of this function tried to skip families it inferred were
	// auto-created (matching a class's own name+schema), to avoid a spurious
	// CREATE OPERATOR FAMILY that would conflict with PG's auto-creation on
	// apply. That heuristic was unreliable BY DESIGN, not just in a rare edge
	// case: confirmed live that the opclass→opfamily pg_depend row is deptype
	// 'a' (DEPENDENCY_AUTO) whether the family was truly auto-created OR
	// explicitly created and then attached via an explicit FAMILY clause of the
	// same name — there is no pg_depend signal that distinguishes the two, so
	// name-matching necessarily misclassified the second, legal case (an
	// explicit family — possibly enriched via ALTER OPERATOR FAMILY ADD, or
	// shared by several classes — silently dropped from the dump). Once every
	// class is given an explicit FAMILY clause unconditionally (see
	// introspectOperatorClasses) and every family is introspected
	// unconditionally, the ambiguity is moot: PostgreSQL itself no longer needs
	// to guess, since the class always names its family explicitly, matching
	// exactly how pg_dump avoids the same ambiguity.
	q := `
SELECT f.oid, n.nspname, f.opfname, am.amname,
       obj_description(f.oid, 'pg_opfamily') AS comment
FROM   pg_opfamily f
JOIN   pg_namespace n ON n.oid = f.opfnamespace
JOIN   pg_am am       ON am.oid = f.opfmethod
WHERE  f.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_opfamily", "f.oid") + `
ORDER  BY n.nspname, f.opfname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect operator families: %w", err)
	}

	type famRow struct {
		oid              uint32
		schema, name, am string
		comment          *string
	}
	var rows []famRow
	for rs.Next() {
		var r famRow
		if err := rs.Scan(&r.oid, &r.schema, &r.name, &r.am, &r.comment); err != nil {
			rs.Close()
			return nil, err
		}
		rows = append(rows, r)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, err
	}
	rs.Close()

	// Members are fetched per family in a second query, mirroring
	// introspectOperatorClasses's own buffer-then-query-per-row pattern —
	// a second query can't run while rs above is still open.
	var out []pipeline.IRObject
	for _, r := range rows {
		members, err := opFamilyLooseMembers(ctx, conn, r.oid)
		if err != nil {
			return nil, err
		}
		body := fmt.Sprintf("CREATE OPERATOR FAMILY %s USING %s", qualIdentQ(r.schema, r.name), quoteIdent(r.am))
		out = append(out, &ir.OperatorFamily{
			Schema: r.schema, Name: r.name, AccessMethod: r.am,
			Body: canonicalDDL(body), Comment: r.comment, Reconstructed: true,
			Members: members,
		})
	}
	return out, nil
}

// opFamilyLooseMembers returns every "loose" member of the operator family
// identified by famOID — i.e. a member added via ALTER OPERATOR FAMILY ...
// ADD rather than inherited from one of the family's operator classes' own
// AS-lists (RFC Section 14.4). The distinguishing catalog signal (confirmed live
// against PG 17) is NOT any column on pg_amop/pg_amproc itself — amopfamily/
// amprocfamily point at the family either way — it's the row's OWNING
// pg_depend entry: a class-owned member's pg_amop/pg_amproc row depends on
// pg_opclass (deptype='i', exactly the join opClassMembers requires); a
// loose member's row instead depends directly on pg_opfamily (deptype='a').
// So "loose" here means simply: no such pg_opclass/'i' dependency row
// exists. Unlike opClassMembers, this returns structured
// pipeline.OpFamilyMember values (not rendered clause strings) — Body stays
// a bare "CREATE OPERATOR FAMILY ... USING ..." regardless (see
// ir.OperatorFamily.Members's doc comment for why members are never folded
// into it).
func opFamilyLooseMembers(ctx context.Context, conn pipeline.Querier, famOID uint32) ([]pipeline.OpFamilyMember, error) {
	var members []pipeline.OpFamilyMember

	const opQ = `
SELECT ao.amopstrategy,
       opn.nspname AS opr_schema, op.oprname AS opr_name,
       ao.amoplefttype::regtype::text  AS lt,
       ao.amoprighttype::regtype::text AS rt,
       ao.amoppurpose::text,
       sfn.nspname AS sort_family_schema, sf.opfname AS sort_family_name
FROM   pg_amop ao
JOIN   pg_operator op    ON op.oid = ao.amopopr
JOIN   pg_namespace opn  ON opn.oid = op.oprnamespace
LEFT JOIN pg_opfamily sf   ON sf.oid = ao.amopsortfamily
LEFT JOIN pg_namespace sfn ON sfn.oid = sf.opfnamespace
WHERE  ao.amopfamily = $1
AND    NOT EXISTS (
         SELECT 1 FROM pg_depend d
         WHERE  d.classid    = 'pg_amop'::regclass
         AND    d.objid      = ao.oid
         AND    d.refclassid = 'pg_opclass'::regclass
         AND    d.deptype    = 'i')
ORDER  BY ao.amopstrategy, ao.amoplefttype, ao.amoprighttype`

	rs, err := conn.QueryRows(ctx, opQ, famOID)
	if err != nil {
		return nil, fmt.Errorf("introspect operator family loose operators: %w", err)
	}
	for rs.Next() {
		var strategy int
		var oprSchema, oprName, lt, rt, purpose string
		var sortSchema, sortName *string
		if err := rs.Scan(&strategy, &oprSchema, &oprName, &lt, &rt, &purpose, &sortSchema, &sortName); err != nil {
			rs.Close()
			return nil, err
		}
		m := pipeline.OpFamilyMember{
			Number:    strategy,
			Name:      pipeline.Identifier{Schema: oprSchema, Name: oprName},
			LeftType:  lt,
			RightType: rt,
		}
		if purpose == "o" && sortName != nil {
			m.OrderBy = true
			m.SortFamily = pipeline.Identifier{Schema: deref(sortSchema), Name: *sortName}
		}
		members = append(members, m)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, err
	}
	rs.Close()

	const fnQ = `
SELECT ap.amprocnum,
       pn.nspname AS fn_schema, p.proname AS fn_name,
       (SELECT array_agg(format_type(t, NULL) ORDER BY ord)
        FROM   unnest(p.proargtypes) WITH ORDINALITY AS u(t, ord)) AS fn_args,
       ap.amproclefttype::regtype::text  AS lt,
       ap.amprocrighttype::regtype::text AS rt
FROM   pg_amproc ap
JOIN   pg_proc p       ON p.oid = ap.amproc
JOIN   pg_namespace pn ON pn.oid = p.pronamespace
WHERE  ap.amprocfamily = $1
AND    NOT EXISTS (
         SELECT 1 FROM pg_depend d
         WHERE  d.classid    = 'pg_amproc'::regclass
         AND    d.objid      = ap.oid
         AND    d.refclassid = 'pg_opclass'::regclass
         AND    d.deptype    = 'i')
ORDER  BY ap.amprocnum, ap.amproclefttype, ap.amprocrighttype`

	rs2, err := conn.QueryRows(ctx, fnQ, famOID)
	if err != nil {
		return nil, fmt.Errorf("introspect operator family loose functions: %w", err)
	}
	for rs2.Next() {
		var support int
		var fnSchema, fnName, lt, rt string
		var fnArgs []string
		if err := rs2.Scan(&support, &fnSchema, &fnName, &fnArgs, &lt, &rt); err != nil {
			rs2.Close()
			return nil, err
		}
		members = append(members, pipeline.OpFamilyMember{
			IsFunction: true,
			Number:     support,
			Name:       pipeline.Identifier{Schema: fnSchema, Name: fnName},
			LeftType:   lt, RightType: rt,
			FuncArgs: fnArgs,
		})
	}
	if err := rs2.Err(); err != nil {
		rs2.Close()
		return nil, err
	}
	rs2.Close()

	return members, nil
}

// ── operator classes ──────────────────────────────────────────────────────────

func introspectOperatorClasses(ctx context.Context, conn pipeline.Querier) ([]pipeline.IRObject, error) {
	q := `
SELECT c.oid, n.nspname, c.opcname, am.amname,
       c.opcintype::regtype::text AS intype,
       c.opcdefault,
       NULLIF(c.opckeytype, 0)::regtype::text AS storagetype,
       fn.nspname AS fam_schema, f.opfname AS fam_name,
       obj_description(c.oid, 'pg_opclass') AS comment
FROM   pg_opclass c
JOIN   pg_namespace n ON n.oid = c.opcnamespace
JOIN   pg_am am       ON am.oid = c.opcmethod
JOIN   pg_opfamily f  ON f.oid = c.opcfamily
JOIN   pg_namespace fn ON fn.oid = f.opfnamespace
WHERE  c.oid >= $1
AND    n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
AND    ` + notExtensionOwned("pg_opclass", "c.oid") + `
ORDER  BY n.nspname, c.opcname`

	rs, err := conn.QueryRows(ctx, q, firstNormalObjectID)
	if err != nil {
		return nil, fmt.Errorf("introspect operator classes: %w", err)
	}

	type opcRow struct {
		oid                                      uint32
		schema, name, am, intype                 string
		isDefault                                bool
		storageType, famSchema, famName, comment *string
	}
	var rows []opcRow
	for rs.Next() {
		var r opcRow
		if err := rs.Scan(&r.oid, &r.schema, &r.name, &r.am, &r.intype, &r.isDefault,
			&r.storageType, &r.famSchema, &r.famName, &r.comment); err != nil {
			rs.Close()
			return nil, err
		}
		rows = append(rows, r)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, err
	}
	rs.Close()

	var out []pipeline.IRObject
	for _, r := range rows {
		members, err := opClassMembers(ctx, conn, r.oid)
		if err != nil {
			return nil, err
		}
		storageType := deref(r.storageType)
		// clauses is Body's AS-list text, rendered from the same structured
		// members the IR object carries — Body and Members can never drift
		// apart from each other the way the old string-only opClassMembers
		// and the builder's FUNCTION-only Functions capture could (the
		// asymmetry this structured rewrite closes): both now come from a
		// single AddClause() call per member, the exact renderer diffOperator
		// Family/dump already use for OperatorFamily's loose members.
		clauses := make([]string, len(members))
		for i, m := range members {
			clauses[i] = m.AddClause()
		}
		if storageType != "" {
			clauses = append(clauses, "STORAGE "+storageType)
		}
		if len(clauses) == 0 {
			// An operator class with no reconstructable members cannot be emitted
			// as valid DDL; skip rather than produce an empty AS clause.
			recordSkip(ctx, "operator class %s: no reconstructable members — omitted, re-declare manually",
				qualIdentQ(r.schema, r.name))
			continue
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "CREATE OPERATOR CLASS %s", qualIdentQ(r.schema, r.name))
		if r.isDefault {
			sb.WriteString(" DEFAULT")
		}
		fmt.Fprintf(&sb, " FOR TYPE %s USING %s", r.intype, quoteIdent(r.am))
		// Always name the family explicitly, matching pg_dump's own model exactly
		// (confirmed live: pg_dump -s does this even for a family PostgreSQL
		// auto-created via an unqualified CREATE OPERATOR CLASS). r.famName/
		// r.famSchema come from an INNER JOIN on pg_opclass.opcfamily, a NOT NULL
		// column — every class belongs to exactly one family, so these are always
		// populated; the pointer type only mirrors storageType's genuinely-nullable
		// shape. This removes the prior same-name-as-class "implicit" special case
		// entirely: see introspectOperatorFamilies for why that heuristic was
		// unreliable by construction (deptype 'a' cannot distinguish an
		// auto-created family from an explicit one sharing the class's name), not
		// merely wrong in a rare case.
		famSchema, famName := deref(r.famSchema), deref(r.famName)
		fmt.Fprintf(&sb, " FAMILY %s", qualIdentQ(famSchema, famName))
		fmt.Fprintf(&sb, " AS %s", strings.Join(clauses, ", "))
		out = append(out, &ir.OperatorClass{
			Schema: r.schema, Name: r.name, AccessMethod: r.am,
			FamilySchema: famSchema, FamilyName: famName,
			Members: members, StorageType: storageType,
			Body: canonicalDDL(sb.String()), Comment: r.comment, Reconstructed: true,
		})
	}
	return out, nil
}

// opClassMembers returns the structured OPERATOR and FUNCTION members that
// belong to the operator class identified by opcOID, in the same
// pipeline.OpFamilyMember shape opFamilyLooseMembers produces for
// OperatorFamily (the two are catalog-identical rows, distinguished only by
// which pg_depend link identifies them — deptype 'i' here vs "no such link"
// there). Membership is determined by an internal pg_depend link from the
// pg_amop/pg_amproc row to the opclass, which is exactly the set a CREATE
// OPERATOR CLASS ... AS clause established (members later added at family
// scope via ALTER depend on the family, not the class).
func opClassMembers(ctx context.Context, conn pipeline.Querier, opcOID uint32) ([]pipeline.OpFamilyMember, error) {
	var members []pipeline.OpFamilyMember

	const opQ = `
SELECT ao.amopstrategy,
       opn.nspname AS opr_schema, op.oprname AS opr_name,
       ao.amoplefttype::regtype::text  AS lt,
       ao.amoprighttype::regtype::text AS rt,
       ao.amoppurpose::text,
       sfn.nspname AS sort_family_schema, sf.opfname AS sort_family_name
FROM   pg_amop ao
JOIN   pg_depend d  ON d.classid = 'pg_amop'::regclass AND d.objid = ao.oid
                   AND d.refclassid = 'pg_opclass'::regclass AND d.refobjid = $1
                   AND d.deptype = 'i'
JOIN   pg_operator op    ON op.oid = ao.amopopr
JOIN   pg_namespace opn  ON opn.oid = op.oprnamespace
LEFT JOIN pg_opfamily sf   ON sf.oid = ao.amopsortfamily
LEFT JOIN pg_namespace sfn ON sfn.oid = sf.opfnamespace
ORDER  BY ao.amopstrategy, ao.amoplefttype, ao.amoprighttype`

	rs, err := conn.QueryRows(ctx, opQ, opcOID)
	if err != nil {
		return nil, fmt.Errorf("introspect operator class operators: %w", err)
	}
	for rs.Next() {
		var strategy int
		var oprSchema, oprName, lt, rt, purpose string
		var sortSchema, sortName *string
		if err := rs.Scan(&strategy, &oprSchema, &oprName, &lt, &rt, &purpose, &sortSchema, &sortName); err != nil {
			rs.Close()
			return nil, err
		}
		m := pipeline.OpFamilyMember{
			Number:    strategy,
			Name:      pipeline.Identifier{Schema: oprSchema, Name: oprName},
			LeftType:  lt,
			RightType: rt,
		}
		if purpose == "o" && sortName != nil {
			m.OrderBy = true
			m.SortFamily = pipeline.Identifier{Schema: deref(sortSchema), Name: *sortName}
		}
		members = append(members, m)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, err
	}
	rs.Close()

	const fnQ = `
SELECT ap.amprocnum,
       pn.nspname AS fn_schema, p.proname AS fn_name,
       (SELECT array_agg(format_type(t, NULL) ORDER BY ord)
        FROM   unnest(p.proargtypes) WITH ORDINALITY AS u(t, ord)) AS fn_args,
       ap.amproclefttype::regtype::text  AS lt,
       ap.amprocrighttype::regtype::text AS rt
FROM   pg_amproc ap
JOIN   pg_depend d ON d.classid = 'pg_amproc'::regclass AND d.objid = ap.oid
                  AND d.refclassid = 'pg_opclass'::regclass AND d.refobjid = $1
                  AND d.deptype = 'i'
JOIN   pg_proc p       ON p.oid = ap.amproc
JOIN   pg_namespace pn ON pn.oid = p.pronamespace
ORDER  BY ap.amprocnum, ap.amproclefttype, ap.amprocrighttype`

	rs2, err := conn.QueryRows(ctx, fnQ, opcOID)
	if err != nil {
		return nil, fmt.Errorf("introspect operator class functions: %w", err)
	}
	for rs2.Next() {
		var support int
		var fnSchema, fnName, lt, rt string
		var fnArgs []string
		if err := rs2.Scan(&support, &fnSchema, &fnName, &fnArgs, &lt, &rt); err != nil {
			rs2.Close()
			return nil, err
		}
		members = append(members, pipeline.OpFamilyMember{
			IsFunction: true,
			Number:     support,
			Name:       pipeline.Identifier{Schema: fnSchema, Name: fnName},
			LeftType:   lt, RightType: rt,
			FuncArgs: fnArgs,
		})
	}
	if err := rs2.Err(); err != nil {
		rs2.Close()
		return nil, err
	}
	rs2.Close()

	return members, nil
}

// ── shared row helpers ────────────────────────────────────────────────────────

// scanNames runs a single-column query and returns the values.
func scanNames(ctx context.Context, conn pipeline.Querier, q string, args ...any) ([]string, error) {
	rs, err := conn.QueryRows(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rs.Err()
}

// scanQualNames runs a (schema, name) query and returns quoted, qualified names.
func scanQualNames(ctx context.Context, conn pipeline.Querier, q string, args ...any) ([]string, error) {
	rs, err := conn.QueryRows(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var schema, name string
		if err := rs.Scan(&schema, &name); err != nil {
			return nil, err
		}
		out = append(out, qualIdentQ(schema, name))
	}
	return out, rs.Err()
}

// deref returns the pointed-to string or "" for a nil pointer.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
