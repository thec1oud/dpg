// Package linter implements pipeline.Linter. It runs built-in lint rules
// over the merged IR and returns diagnostics.
package linter

import (
	"fmt"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyLinter, New())
}

// BuiltinLinter implements pipeline.Linter.
type BuiltinLinter struct{}

// New returns a BuiltinLinter.
func New() *BuiltinLinter { return &BuiltinLinter{} }

// Lint checks objects against all enabled rules.
func (l *BuiltinLinter) Lint(objects []pipeline.IRObject, cfg pipeline.LinterConfig) ([]pipeline.LintDiagnostic, error) {
	var diags []pipeline.LintDiagnostic

	for _, obj := range objects {
		diags = append(diags, checkObject(obj, cfg)...)
	}
	diags = append(diags, checkCrossObjectRules(objects, cfg)...)

	return ApplyRuleSeverityOverrides(diags, cfg.Rules), nil
}

// FilterMergeDiagnostics applies [linter.rules] severity overrides to the
// warning diagnostics returned by compiler.Compile (DPG-E031 duplicate Name
// Map tool keys), the same way Lint's own diagnostics are overridden.
func FilterMergeDiagnostics(mergeDiags []pipeline.LintDiagnostic, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	return ApplyRuleSeverityOverrides(mergeDiags, cfg.Rules)
}

// ApplyRuleSeverityOverrides applies RFC Section 19.2's [linter.rules] per-rule
// severity overrides ("error", "warning", or "off") to diags, matched by
// d.Rule. A rule ID absent from rules is left at its own default severity.
// This runs once here, inside the one function every external Lint caller
// (plan/apply/validate/pkg/dpg) converges on, rather than being duplicated
// at each call site — the same reasoning --strict's existing per-command
// IsError-promotion loops don't apply to, since those only run at 2 of the
// 5 call sites today. Exported so callers combining Lint's diagnostics with
// compiler.Compile's (via FilterMergeDiagnostics above) can apply the identical
// override logic to both without duplicating it — also re-exported from
// pkg/dpg for external Go-API consumers, who can't import this internal
// package directly.
func ApplyRuleSeverityOverrides(diags []pipeline.LintDiagnostic, rules map[string]string) []pipeline.LintDiagnostic {
	if len(rules) == 0 {
		return diags
	}
	out := diags[:0]
	for _, d := range diags {
		switch strings.ToLower(rules[d.Rule]) {
		case "off":
			continue // drop entirely
		case "error":
			d.IsError = true
		case "warning":
			d.IsError = false
		}
		out = append(out, d)
	}
	return out
}

func checkObject(obj pipeline.IRObject, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	diags = append(diags, checkMinPGVersion(obj, cfg)...)

	diags = append(diags, checkConcurrentlyOnNewRelation(obj, cfg)...)

	switch o := obj.(type) {
	case *ir.Table:
		diags = append(diags, checkTable(o, cfg)...)
	case *ir.Function:
		diags = append(diags, checkFunction(o, cfg)...)
	case *ir.Procedure:
		diags = append(diags, checkProcedure(o, cfg)...)
	case *ir.View:
		diags = append(diags, checkView(o, cfg)...)
	case *ir.Role:
		diags = append(diags, checkRole(o, cfg)...)
	case *ir.UserMapping:
		diags = append(diags, checkUserMapping(o, cfg)...)
	default:
		_ = o
	}

	return diags
}

// ── min_pg_version gating ────────────────────────────────────────────────────

// checkMinPGVersion implements the min-pg-version rule (RFC Section 3.8):
// warns when a declared construct needs a PostgreSQL major version newer
// than cfg.MinPGVersion, the floor already resolved for this cluster or
// database. 0 means no floor is configured, and the rule does nothing.
// It covers every declared construct with a Min PG Version in RFC
// Appendix F; statements DPG emits only for a change to an existing
// object are not visible in the source and are not checked.
func checkMinPGVersion(obj pipeline.IRObject, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	if cfg.MinPGVersion == 0 {
		return nil
	}

	var diags []pipeline.LintDiagnostic
	need := func(pos pipeline.SourcePos, construct string, minVersion int, section string) {
		if minVersion <= cfg.MinPGVersion {
			return
		}
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:  pos,
			Rule: "min-pg-version",
			Message: fmt.Sprintf("%s requires PostgreSQL %d+ (RFC Section %s), but this project's min_pg_version is %d",
				construct, minVersion, section, cfg.MinPGVersion),
		})
	}
	maintain := func(pos pipeline.SourcePos, grants []ir.Grant, revocations []ir.Revocation) {
		if hasPrivilege(grants, revocations, "MAINTAIN") {
			need(pos, "MAINTAIN privilege", 17, "7.10")
		}
	}
	constraints := func(partitioned bool, csts []*ir.Constraint) {
		for _, c := range csts {
			if c.Type == "NOT NULL" {
				// A bare inline NOT NULL is a column flag; a NOT NULL
				// constraint is named, NO INHERIT, or NOT VALID.
				need(c.Pos, "NOT NULL constraint (named, NO INHERIT or NOT VALID)", 18, "7.3")
			}
			if c.NotEnforced {
				need(c.Pos, "NOT ENFORCED", 18, "7.2")
			}
			if c.WithoutOverlaps || c.FkWithPeriod || c.PkWithPeriod {
				need(c.Pos, "temporal key (WITHOUT OVERLAPS/PERIOD)", 18, "7.3")
			}
			if len(c.DeleteSetColumns) > 0 {
				need(c.Pos, "ON DELETE SET NULL/SET DEFAULT column list", 15, "7.2")
			}
			if c.NullsNotDistinct {
				need(c.Pos, "UNIQUE NULLS NOT DISTINCT", 15, "7.3")
			}
			if partitioned && c.Type == "EXCLUDE" {
				need(c.Pos, "EXCLUDE constraint on a partitioned table", 17, "7.3")
			}
		}
	}
	indexes := func(idxs []*ir.Index) {
		for _, idx := range idxs {
			if idx.NullsNotDistinct {
				need(idx.Pos, "UNIQUE index NULLS NOT DISTINCT", 15, "7.7")
			}
		}
	}
	var partitions func([]*ir.Partition)
	partitions = func(ps []*ir.Partition) {
		for _, p := range ps {
			constraints(p.PartitionBy != nil, p.Constraints)
			partitions(p.Partitions)
		}
	}

	switch o := obj.(type) {
	case *ir.Table:
		partitioned := o.PartitionBy != nil
		for _, col := range o.Columns {
			if col.Generated != nil && !col.Generated.Stored {
				need(col.SrcPos, "VIRTUAL generated column", 18, "7.2")
			}
			maintain(col.SrcPos, col.Grants, col.Revocations)
		}
		constraints(partitioned, o.Constraints)
		indexes(o.Indexes)
		partitions(o.Partitions)
		if partitioned && o.AccessMethod != "" {
			need(o.SrcPos, "USING access method on a partitioned table", 17, "7.1")
		}
		maintain(o.SrcPos, o.Grants, o.Revocations)
	case *ir.View:
		indexes(o.Indexes)
		maintain(o.SrcPos, o.Grants, o.Revocations)
	case *ir.DefaultPrivileges:
		maintain(o.SrcPos, o.Grants, o.Revocations)
	case *ir.Sequence:
		if o.Unlogged {
			need(o.SrcPos, "UNLOGGED SEQUENCE", 15, "10")
		}
	case *ir.Role:
		for _, m := range o.Memberships {
			if m.Inherit != nil || m.Set != nil {
				need(m.Pos, "role membership WITH INHERIT/WITH SET", 16, "11.1")
			}
		}
	case *ir.ParameterPrivileges:
		need(o.SrcPos, "PARAMETER PRIVILEGES", 15, "11.6")
	case *ir.Publication:
		for _, f := range publicationFeatures(o.Body) {
			need(o.SrcPos, f.construct, f.version, "13.1")
		}
	case *ir.Subscription:
		for _, f := range subscriptionFeatures(o.Body) {
			need(o.SrcPos, f.construct, f.version, "13.2")
		}
	case *ir.EventTrigger:
		if strings.EqualFold(o.Event, "login") {
			need(o.SrcPos, "EVENT TRIGGER ON login", 17, "14.1")
		}
	case *ir.Collation:
		if o.Provider == "b" {
			need(o.SrcPos, "COLLATION PROVIDER builtin", 17, "14.2")
		}
		if o.RefreshVersion {
			need(o.SrcPos, "COLLATION REFRESH VERSION", 15, "14.2")
		}
		if o.Rules != nil {
			need(o.SrcPos, "COLLATION RULES (ICU tailoring)", 16, "14.2")
		}
	}
	return diags
}

// versionedFeature is a clause inside an opaque statement body and the
// PostgreSQL major version that introduced it.
type versionedFeature struct {
	construct string
	version   int
}

// publicationFeatures lists the version-gated clauses in a CREATE
// PUBLICATION statement (RFC Section 13.1). A body that doesn't parse has
// none; the compiler has already rejected it.
func publicationFeatures(body string) []versionedFeature {
	res, err := pg_query.Parse(body)
	if err != nil || len(res.Stmts) != 1 {
		return nil
	}
	stmt := res.Stmts[0].Stmt.GetCreatePublicationStmt()
	if stmt == nil {
		return nil
	}
	var out []versionedFeature
	seen := map[string]bool{}
	add := func(construct string, version int) {
		if !seen[construct] {
			seen[construct] = true
			out = append(out, versionedFeature{construct, version})
		}
	}
	for _, obj := range stmt.Pubobjects {
		spec := obj.GetPublicationObjSpec()
		if spec == nil {
			continue
		}
		switch spec.Pubobjtype {
		case pg_query.PublicationObjSpecType_PUBLICATIONOBJ_TABLES_IN_SCHEMA,
			pg_query.PublicationObjSpecType_PUBLICATIONOBJ_TABLES_IN_CUR_SCHEMA:
			add("publication FOR TABLES IN SCHEMA", 15)
		}
		if t := spec.GetPubtable(); t != nil {
			if len(t.Columns) > 0 {
				add("publication column list", 15)
			}
			if t.WhereClause != nil {
				add("publication row filter (WHERE)", 15)
			}
		}
	}
	for _, opt := range stmt.Options {
		if d := opt.GetDefElem(); d != nil && strings.EqualFold(d.Defname, "publish_generated_columns") {
			add("publication option publish_generated_columns", 18)
		}
	}
	return out
}

// subscriptionOptionVersions maps CREATE SUBSCRIPTION options newer than
// PostgreSQL 14 to the version that introduced them.
var subscriptionOptionVersions = map[string]int{
	"two_phase":         15,
	"disable_on_error":  15,
	"origin":            16,
	"run_as_owner":      16,
	"password_required": 16,
	"failover":          17,
}

// defElemWord returns an option value written as a quoted string or a bare
// word (which PostgreSQL parses as a type name), or "".
func defElemWord(arg *pg_query.Node) string {
	if arg == nil {
		return ""
	}
	if sv := arg.GetString_(); sv != nil {
		return sv.Sval
	}
	if tn := arg.GetTypeName(); tn != nil && len(tn.Names) == 1 {
		if sv := tn.Names[0].GetString_(); sv != nil {
			return sv.Sval
		}
	}
	return ""
}

// subscriptionFeatures lists the version-gated options in a CREATE
// SUBSCRIPTION statement (RFC Section 13.2).
func subscriptionFeatures(body string) []versionedFeature {
	res, err := pg_query.Parse(body)
	if err != nil || len(res.Stmts) != 1 {
		return nil
	}
	stmt := res.Stmts[0].Stmt.GetCreateSubscriptionStmt()
	if stmt == nil {
		return nil
	}
	var out []versionedFeature
	for _, opt := range stmt.Options {
		d := opt.GetDefElem()
		if d == nil {
			continue
		}
		name := strings.ToLower(d.Defname)
		if v, ok := subscriptionOptionVersions[name]; ok {
			out = append(out, versionedFeature{"subscription option " + name, v})
		}
		if name == "streaming" && strings.EqualFold(defElemWord(d.Arg), "parallel") {
			out = append(out, versionedFeature{"subscription option streaming = parallel", 16})
		}
	}
	return out
}

// checkConcurrentlyOnNewRelation implements the concurrently-new-table
// rule (RFC Section 7.7): an index on a table or materialized view that is
// not yet in the snapshot is created in the same transaction as the
// relation, so CONCURRENTLY on it has no effect. Skipped without a
// snapshot.
func checkConcurrentlyOnNewRelation(obj pipeline.IRObject, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	if cfg.Snapshot == nil {
		return nil
	}
	var indexes []*ir.Index
	var oldName string
	switch o := obj.(type) {
	case *ir.Table:
		indexes = o.Indexes
		if o.RenamedFrom != nil {
			oldName = qualName(renamedSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	case *ir.View:
		if !o.Materialized {
			return nil
		}
		indexes = o.Indexes
		if o.RenamedFrom != nil {
			oldName = qualName(renamedSchema(o.Schema, o.RenamedFromSchema), *o.RenamedFrom)
		}
	default:
		return nil
	}
	if _, ok := cfg.Snapshot.Objects[obj.QualifiedName()]; ok {
		return nil
	}
	if _, ok := cfg.Snapshot.Objects[oldName]; ok && oldName != "" {
		return nil
	}
	var diags []pipeline.LintDiagnostic
	for _, idx := range indexes {
		if !idx.Concurrently {
			continue
		}
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:  idx.Pos,
			Rule: "concurrently-new-table",
			Message: fmt.Sprintf("index %s: CONCURRENTLY has no effect, %s is new and its indexes are created in the same transaction",
				idx.Name, obj.QualifiedName()),
		})
	}
	return diags
}

func renamedSchema(schema string, renamedFromSchema *string) string {
	if renamedFromSchema != nil {
		return *renamedFromSchema
	}
	return schema
}

func qualName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// hasPrivilege reports whether priv (case-insensitive) appears in any grant
// or revocation entry's privilege list.
func hasPrivilege(grants []ir.Grant, revocations []ir.Revocation, priv string) bool {
	for _, g := range grants {
		for _, p := range g.Privileges {
			if strings.EqualFold(p, priv) {
				return true
			}
		}
	}
	for _, r := range revocations {
		for _, p := range r.Privileges {
			if strings.EqualFold(p, priv) {
				return true
			}
		}
	}
	return false
}

// ── Table rules ───────────────────────────────────────────────────────────────

func checkTable(t *ir.Table, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic
	pos := t.SrcPos

	// DEPRECATED warning.
	if cfg.WarnOnDeprecated && t.Deprecated != nil {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     pos,
			Rule:    "deprecated",
			Message: fmt.Sprintf("table %s is deprecated: %s", t.QualifiedName(), *t.Deprecated),
		})
	}

	// Max columns.
	if cfg.MaxColumnsPerTable > 0 && len(t.Columns) > cfg.MaxColumnsPerTable {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     pos,
			Rule:    "column-count-exceeded",
			Message: fmt.Sprintf("table %s has %d columns (max %d)", t.QualifiedName(), len(t.Columns), cfg.MaxColumnsPerTable),
			IsError: true,
		})
	}

	for _, col := range t.Columns {
		// Require column comments.
		if cfg.RequireColumnComments && col.Comment == nil {
			diags = append(diags, pipeline.LintDiagnostic{
				Pos:     col.SrcPos,
				Rule:    "missing-column-comment",
				Message: fmt.Sprintf("column %s.%s has no comment", t.QualifiedName(), col.Name),
			})
		}
		// Deprecated column.
		if cfg.WarnOnDeprecated && col.Deprecated != nil {
			diags = append(diags, pipeline.LintDiagnostic{
				Pos:     col.SrcPos,
				Rule:    "deprecated",
				Message: fmt.Sprintf("column %s.%s is deprecated: %s", t.QualifiedName(), col.Name, *col.Deprecated),
			})
		}
		// Hardcoded passwords: look for default values that look like password strings.
		if cfg.ForbidHardcodedPasswords && col.Default != nil {
			if looksLikePassword(col.Name, *col.Default) {
				diags = append(diags, pipeline.LintDiagnostic{
					Pos:     col.SrcPos,
					Rule:    "hardcoded-password",
					Message: fmt.Sprintf("column %s.%s default may contain a hardcoded password", t.QualifiedName(), col.Name),
					IsError: true,
				})
			}
		}
		diags = append(diags, checkUnnecessaryRevocation(col.Grants, col.Revocations, col.SrcPos,
			fmt.Sprintf("column %s.%s", t.QualifiedName(), col.Name))...)
	}

	diags = append(diags, checkUnnecessaryRevocation(t.Grants, t.Revocations, pos, "table "+t.QualifiedName())...)

	return diags
}

// ── Function rules ────────────────────────────────────────────────────────────

func checkFunction(f *ir.Function, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	if cfg.WarnOnDeprecated && f.Deprecated != nil {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     f.SrcPos,
			Rule:    "deprecated",
			Message: fmt.Sprintf("function %s is deprecated: %s", f.QualifiedName(), *f.Deprecated),
		})
	}
	// Warn on SECURITY DEFINER without an explicit SET search_path clause
	// (Section 9.2). Previously checked strings.Contains(f.Attrs.Body,
	// "search_path") — a substring scan of the function BODY text, which
	// SET search_path (a structural attribute, not body text) never
	// appears in at all, so a function correctly declaring
	// "SET search_path = ..." was warned about anyway; this only ever
	// passed by coincidence (e.g. the literal word appearing in a
	// comment). Now checks the real Config list Section 9.2 documents.
	if f.Attrs.SecurityDef && !hasSearchPathConfig(f.Attrs.Config) {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     f.SrcPos,
			Rule:    "security-definer-search-path",
			Message: fmt.Sprintf("SECURITY DEFINER function %s should set search_path", f.QualifiedName()),
		})
	}
	diags = append(diags, checkUnnecessaryRevocation(f.Grants, f.Revocations, f.SrcPos, "function "+f.QualifiedName())...)

	return diags
}

// hasSearchPathConfig reports whether a Section 9.2 SET-clause list
// includes a search_path entry, case-insensitively (PostgreSQL GUC names
// are case-insensitive).
func hasSearchPathConfig(config []ir.FuncConfig) bool {
	for _, c := range config {
		if strings.EqualFold(c.Param, "search_path") {
			return true
		}
	}
	return false
}

// ── Procedure rules ──────────────────────────────────────────────────────────

func checkProcedure(p *ir.Procedure, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic
	// Same SECURITY DEFINER + search_path guard as checkFunction — real
	// PostgreSQL procedures carry the identical search-path-injection risk
	// (RFC Section 9.3: "Procedures follow the same model as functions"),
	// but this was never checked here at all, since Procedure's
	// SecurityDef/Config were previously silently dropped everywhere else
	// too (parser capture was fine; only introspection/rendering/diffing
	// never wired them through) — see ir.RenderCreateProcedureSQL's fix.
	if p.Attrs.SecurityDef && !hasSearchPathConfig(p.Attrs.Config) {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     p.SrcPos,
			Rule:    "security-definer-search-path",
			Message: fmt.Sprintf("SECURITY DEFINER procedure %s should set search_path", p.QualifiedName()),
		})
	}
	diags = append(diags, checkUnnecessaryRevocation(p.Grants, p.Revocations, p.SrcPos, "procedure "+p.QualifiedName())...)
	return diags
}

// ── View rules ────────────────────────────────────────────────────────────────

func checkView(v *ir.View, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	if cfg.WarnOnDeprecated && v.Deprecated != nil {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     v.SrcPos,
			Rule:    "deprecated",
			Message: fmt.Sprintf("view %s is deprecated: %s", v.QualifiedName(), *v.Deprecated),
		})
	}
	diags = append(diags, checkUnnecessaryRevocation(v.Grants, v.Revocations, v.SrcPos, "view "+v.QualifiedName())...)

	return diags
}

// ── Role rules ────────────────────────────────────────────────────────────────

// checkRole implements RFC Section 11.1's "Hardcoded passwords" rule: a ROLE
// PASSWORD with no {{secret-uri}} placeholder at all is a literal
// credential sitting in plaintext in the .dpg source file. IsError (not a
// warning) when cfg.ForbidHardcodedPasswords is enabled (default true),
// matching the RFC's "MUST emit an error" wording — the same severity the
// table-column "hardcoded-password" rule above uses for the analogous
// table-column-default case. Scheme-agnostic: checks for any {{...}}
// placeholder, not one specific scheme (an earlier RFC draft named
// env:VAR_NAME specifically, before four more backends existed).
// Rule ID is "hardcoded-role-password", not "hardcoded-password" — the
// latter is the pre-existing, semantically distinct table-column-default
// rule above (checkTable), which checks a completely different thing.
// RFC Section 19.1's rules table now lists both under their real, disambiguated
// code names (kebab-case, matching every rule ID actually in code); it
// used to conflate this rule alone under a single snake_case
// `hardcoded_password` entry with no table-column entry at all.
func checkRole(r *ir.Role, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	if cfg.ForbidHardcodedPasswords && r.Password != nil && !strings.Contains(*r.Password, "{{") {
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     r.SrcPos,
			Rule:    "hardcoded-role-password",
			Message: fmt.Sprintf("role %s: PASSWORD is a literal value; use a {{secret-uri}} reference instead (e.g. {{vault:secret/roles/%s#password}})", r.Name, r.Name),
			IsError: true,
		})
	}

	return diags
}

// ── UserMapping rules ────────────────────────────────────────────────────────

// fdwPasswordLit matches a password OPTIONS entry inside a USER MAPPING's
// opaque Body text: password 'value' (PostgreSQL's own OPTIONS syntax,
// case-insensitive keyword, ” escaping supported like any SQL string
// literal). UserMapping stays fully opaque (no structured OPTIONS field
// exists to check directly, unlike Role.Password) — see
// checkUserMapping implements RFC Section 19.1's hardcoded_fdw_password
// rule: a literal (non-{{secret-uri}}) value under any password-like
// OPTIONS key in a USER MAPPING, when forbid_hardcoded_passwords is
// enabled — same severity and intent as checkRole. Matches against the
// structured Options field (populated by buildUserMapping from the real
// parsed OPTIONS clause) using the same 5-key substring list
// (passwordColNames) as checkColumn's hardcoded-password rule and
// introspect.userMappingPasswordKeys' redaction — previously this only
// matched the literal key "password" via a regex against raw Body text, so
// a literal value under passwd/pwd/secret/passphrase went uncaught even
// though dump's redaction already treated all 5 as sensitive.
func checkUserMapping(u *ir.UserMapping, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	if !cfg.ForbidHardcodedPasswords {
		return diags
	}
	for _, opt := range u.Options {
		if !looksLikePasswordKey(opt.Key) || strings.Contains(opt.Value, "{{") {
			continue
		}
		diags = append(diags, pipeline.LintDiagnostic{
			Pos:     u.SrcPos,
			Rule:    "hardcoded-fdw-password",
			Message: fmt.Sprintf("user mapping %s: OPTIONS %s is a literal value; use a {{secret-uri}} reference instead (e.g. {{vault:secret/fdw/%s#%s}})", u.QualifiedName(), opt.Key, u.Server, opt.Key),
			IsError: true,
		})
	}

	return diags
}

// ── Cross-object rules ───────────────────────────────────────────────────────

// checkCrossObjectRules runs rules that need to see the whole desired
// object set at once, not just one object in isolation.
func checkCrossObjectRules(objects []pipeline.IRObject, cfg pipeline.LinterConfig) []pipeline.LintDiagnostic {
	diags := checkSerialSequenceDeclared(objects)
	// deprecated-reference has no dedicated RFC-documented config toggle —
	// kept under the same WarnOnDeprecated gate as the base "deprecated"
	// rule (checkTable/checkView/checkFunction), while [linter.rules] still
	// allows independent per-rule override via ApplyRuleSeverityOverrides.
	if cfg.WarnOnDeprecated {
		diags = append(diags, checkDeprecatedReference(objects)...)
	}
	return diags
}

// checkSerialSequenceDeclared implements RFC Section 19.1's serial_sequence_declared
// rule, covering both GENERATED ... AS IDENTITY columns and SERIAL/
// BIGSERIAL/SMALLSERIAL columns (SERIAL now has a distinct IR representation
// via ir.Column.Serial, so it's no longer out of scope the way an earlier
// version of this rule's Section 19.3 entry noted). Warns when a
// hand-declared SEQUENCE's name collides with the name PostgreSQL
// auto-generates for an identity or serial column in the same desired state
// ("<table>_<column>_seq", PostgreSQL's own naming convention for a
// column's implicit sequence) — such a sequence is either redundant (it's
// the column's own auto-managed sequence, which DPG and PostgreSQL already
// handle without a separate declaration) or a genuine naming collision
// PostgreSQL will reject at apply time; either way, it's worth flagging
// before that happens.
func checkSerialSequenceDeclared(objects []pipeline.IRObject) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	identityNames := make(map[string]bool) // "schema.name" of auto-managed sequences
	for _, obj := range objects {
		t, ok := obj.(*ir.Table)
		if !ok {
			continue
		}
		for _, col := range t.Columns {
			if col.Identity != nil || col.Serial != nil {
				identityNames[t.Schema+"."+t.Name+"_"+col.Name+"_seq"] = true
			}
		}
	}
	if len(identityNames) == 0 {
		return diags
	}

	for _, obj := range objects {
		seq, ok := obj.(*ir.Sequence)
		if !ok {
			continue
		}
		if identityNames[seq.Schema+"."+seq.Name] {
			diags = append(diags, pipeline.LintDiagnostic{
				Pos:  seq.Pos(),
				Rule: "serial-sequence-declared",
				Message: fmt.Sprintf("sequence %s.%s has the same name PostgreSQL auto-manages for an identity column's sequence in this schema",
					seq.Schema, seq.Name),
			})
		}
	}

	return diags
}

// ── helpers ───────────────────────────────────────────────────────────────────

// checkUnnecessaryRevocation implements RFC Section 19.1's unnecessary_revocation
// rule, scoped to a single object's own declaration (see rfc/dpg-1.md
// Section 19.3): warns when revocations names a (role, privilege) pair
// with no matching entry in grants for that same object — catching a
// revocation with no corresponding grant (a copy-paste or typo), not the
// RFC's literal broader wording ("never granted... by DPG" — that would
// need snapshot/grant-history access the linter doesn't have, see the RFC
// note for why). A grant with nil Privileges means ALL, and covers every
// revoked privilege for that role.
func checkUnnecessaryRevocation(grants []ir.Grant, revocations []ir.Revocation, pos pipeline.SourcePos, objDesc string) []pipeline.LintDiagnostic {
	if len(revocations) == 0 {
		return nil
	}

	granted := make(map[string]map[string]bool) // role -> privilege ("ALL" or specific) -> true
	for _, g := range grants {
		for _, role := range g.Roles {
			if granted[role] == nil {
				granted[role] = make(map[string]bool)
			}
			if len(g.Privileges) == 0 {
				granted[role]["ALL"] = true
				continue
			}
			for _, priv := range g.Privileges {
				granted[role][strings.ToUpper(priv)] = true
			}
		}
	}

	var diags []pipeline.LintDiagnostic
	for _, rev := range revocations {
		privs := rev.Privileges
		if len(privs) == 0 {
			privs = []string{"ALL"}
		}
		for _, role := range rev.Roles {
			for _, priv := range privs {
				p := strings.ToUpper(priv)
				if granted[role]["ALL"] || granted[role][p] {
					continue
				}
				diags = append(diags, pipeline.LintDiagnostic{
					Pos:  rev.Pos,
					Rule: "unnecessary-revocation",
					Message: fmt.Sprintf("%s: REVOKE of %s from %s has no matching GRANT in this declaration",
						objDesc, priv, role),
				})
			}
		}
	}
	return diags
}

var passwordColNames = []string{"password", "passwd", "pwd", "secret", "passphrase"}

func looksLikePassword(colName, defaultExpr string) bool {
	lower := strings.ToLower(colName)
	for _, kw := range passwordColNames {
		if strings.Contains(lower, kw) {
			// Check if the default is a string literal (starts with ').
			trimmed := strings.TrimSpace(defaultExpr)
			if strings.HasPrefix(trimmed, "'") {
				return true
			}
		}
	}
	return false
}

// looksLikePasswordKey is checkUserMapping's key-only counterpart to
// looksLikePassword — a USER MAPPING OPTIONS key has no separate "is this a
// string literal" question the way a column DEFAULT expression does, since
// FDW options are always plain string values.
func looksLikePasswordKey(key string) bool {
	lower := strings.ToLower(key)
	for _, kw := range passwordColNames {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

var _ pipeline.Linter = (*BuiltinLinter)(nil)
