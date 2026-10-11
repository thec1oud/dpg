package blockparser_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/blockparser"
	"github.com/thec1oud/dpg/internal/pipeline"
)

var zeroPos = pipeline.SourcePos{File: "test.dpg", Line: 1, Col: 1}

func parse(t *testing.T, src string) pipeline.BlockAST {
	t.Helper()
	p := blockparser.New()
	ast, err := p.Parse(pipeline.KindTable, src, zeroPos)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return ast
}

func parseErr(t *testing.T, src string) error {
	t.Helper()
	p := blockparser.New()
	_, err := p.Parse(pipeline.KindTable, src, zeroPos)
	return err
}

// ── empty / blank ─────────────────────────────────────────────────────────────

func TestEmptyBlock(t *testing.T) {
	ast := parse(t, "")
	if ast.Comment != nil || ast.Owner != nil {
		t.Error("expected zero-value BlockAST for empty input")
	}
}

func TestBlankBlock(t *testing.T) {
	ast := parse(t, "   \n\t  ")
	if ast.Protected || ast.DropCascade {
		t.Error("expected zero-value BlockAST for whitespace input")
	}
}

// ── simple directives ─────────────────────────────────────────────────────────

func TestComment(t *testing.T) {
	ast := parse(t, `COMMENT 'hello world';`)
	if ast.Comment == nil || ast.Comment.Value != "hello world" {
		t.Errorf("Comment: got %v", ast.Comment)
	}
}

func TestOwner(t *testing.T) {
	ast := parse(t, `OWNER "app_role";`)
	if ast.Owner == nil || ast.Owner.Name != "app_role" {
		t.Errorf("Owner: got %v", ast.Owner)
	}
}

func TestRenamedFrom(t *testing.T) {
	ast := parse(t, `RENAMED FROM old_table;`)
	if ast.RenamedFrom == nil || ast.RenamedFrom.Name != "old_table" {
		t.Errorf("RenamedFrom: got %v", ast.RenamedFrom)
	}
}

func TestProtected(t *testing.T) {
	ast := parse(t, `PROTECTED;`)
	if !ast.Protected {
		t.Error("expected Protected = true")
	}
}

func TestDeprecated(t *testing.T) {
	ast := parse(t, `DEPRECATED 'Use new_table instead';`)
	if ast.Deprecated == nil || ast.Deprecated.Value != "Use new_table instead" {
		t.Errorf("Deprecated: got %v", ast.Deprecated)
	}
}

func TestDropCascade(t *testing.T) {
	ast := parse(t, `DROP CASCADE;`)
	if !ast.DropCascade {
		t.Error("expected DropCascade = true")
	}
}

func TestEnableRLS(t *testing.T) {
	ast := parse(t, `ENABLE ROW LEVEL SECURITY;`)
	if !ast.EnableRLS {
		t.Error("expected EnableRLS = true")
	}
}

// TestEventTriggerBlockEnableState guards RFC Section 14.1's DISABLED/
// ENABLE REPLICA/ENABLE ALWAYS block directive — parsed here as a plain
// top-level block directive (BlockAST.TriggerEnableState) rather than an
// inline Part-1 clause, since real CREATE EVENT TRIGGER has no such clause
// at all (confirmed live).
func TestEventTriggerBlockEnableState(t *testing.T) {
	cases := map[string]string{
		"DISABLED;":       "DISABLED",
		"ENABLE REPLICA;": "ENABLE REPLICA",
		"ENABLE ALWAYS;":  "ENABLE ALWAYS",
	}
	for src, want := range cases {
		ast := parse(t, src)
		if ast.TriggerEnableState != want {
			t.Errorf("parse(%q).TriggerEnableState = %q, want %q", src, ast.TriggerEnableState, want)
		}
	}
}

// TestEventTriggerBlockBareEnableErrors proves a bare "ENABLE;" (no ROW,
// REPLICA, or ALWAYS) is rejected — same as TriggerDef's identical parser —
// rather than silently accepted as a redundant no-op directive.
func TestEventTriggerBlockBareEnableErrors(t *testing.T) {
	if err := parseErr(t, `ENABLE;`); err == nil {
		t.Fatal("expected an error for bare ENABLE with no argument")
	}
}

// TestEnableRLSStillWorksAfterTriggerStateDispatch guards the ENABLE
// dispatch refactor that added trigger-enable-state routing: the original
// "ENABLE ROW LEVEL SECURITY" path must still work unchanged.
func TestEnableRLSStillWorksAfterTriggerStateDispatch(t *testing.T) {
	ast := parse(t, `ENABLE ROW LEVEL SECURITY;`)
	if !ast.EnableRLS {
		t.Error("expected EnableRLS = true")
	}
	if ast.TriggerEnableState != "" {
		t.Errorf("expected TriggerEnableState unset, got %q", ast.TriggerEnableState)
	}
}

func TestForceRLS(t *testing.T) {
	ast := parse(t, `FORCE ROW LEVEL SECURITY;`)
	if !ast.ForceRLS {
		t.Error("expected ForceRLS = true")
	}
}

func TestReplicaIdentityDefault(t *testing.T) {
	ast := parse(t, `REPLICA IDENTITY DEFAULT;`)
	if ast.ReplicaIdentity == nil || ast.ReplicaIdentity.Mode != "DEFAULT" {
		t.Fatalf("expected ReplicaIdentity Mode DEFAULT, got %+v", ast.ReplicaIdentity)
	}
}

func TestReplicaIdentityFull(t *testing.T) {
	ast := parse(t, `REPLICA IDENTITY FULL;`)
	if ast.ReplicaIdentity == nil || ast.ReplicaIdentity.Mode != "FULL" {
		t.Fatalf("expected ReplicaIdentity Mode FULL, got %+v", ast.ReplicaIdentity)
	}
}

func TestReplicaIdentityNothing(t *testing.T) {
	ast := parse(t, `REPLICA IDENTITY NOTHING;`)
	if ast.ReplicaIdentity == nil || ast.ReplicaIdentity.Mode != "NOTHING" {
		t.Fatalf("expected ReplicaIdentity Mode NOTHING, got %+v", ast.ReplicaIdentity)
	}
}

func TestReplicaIdentityUsingIndex(t *testing.T) {
	ast := parse(t, `REPLICA IDENTITY USING INDEX idx_orders_id;`)
	if ast.ReplicaIdentity == nil || ast.ReplicaIdentity.Mode != "INDEX" || ast.ReplicaIdentity.IndexName != "idx_orders_id" {
		t.Fatalf("expected ReplicaIdentity Mode INDEX/idx_orders_id, got %+v", ast.ReplicaIdentity)
	}
}

func TestReplicaIdentityInvalidMode(t *testing.T) {
	if err := parseErr(t, `REPLICA IDENTITY BOGUS;`); err == nil {
		t.Fatal("expected an error for an invalid REPLICA IDENTITY mode")
	}
}

func TestClusterOn(t *testing.T) {
	ast := parse(t, `CLUSTER ON idx_orders_created_at;`)
	if ast.ClusterOn == nil || ast.ClusterOn.Name != "idx_orders_created_at" {
		t.Fatalf("expected ClusterOn idx_orders_created_at, got %+v", ast.ClusterOn)
	}
}

// TestRefreshVersion guards RFC audit item #84: Collation's REFRESH VERSION
// block directive, a bare presence keyword with no argument.
func TestRefreshVersion(t *testing.T) {
	ast := parse(t, `REFRESH VERSION;`)
	if !ast.RefreshVersion {
		t.Fatal("expected RefreshVersion true")
	}
}

func TestRefreshVersionMissingVersionErrors(t *testing.T) {
	if err := parseErr(t, `REFRESH;`); err == nil {
		t.Fatal("expected an error for REFRESH without VERSION")
	}
}

func TestRefreshVersionUnspecifiedIsFalse(t *testing.T) {
	ast := parse(t, `COMMENT 'x';`)
	if ast.RefreshVersion {
		t.Fatal("expected RefreshVersion false when never declared")
	}
}

// ── INDEXES ───────────────────────────────────────────────────────────────────

func TestSimpleIndex(t *testing.T) {
	ast := parse(t, `INDEXES { idx_email (email); }`)
	if len(ast.Indices) != 1 {
		t.Fatalf("expected 1 index, got %d", len(ast.Indices))
	}
	idx := ast.Indices[0]
	if idx.Name.Name != "idx_email" {
		t.Errorf("index name: got %q", idx.Name.Name)
	}
	if len(idx.Columns) != 1 || idx.Columns[0].Name != "email" {
		t.Errorf("index columns: got %v", idx.Columns)
	}
	if idx.Unique {
		t.Error("expected non-unique")
	}
}

func TestUniqueIndex(t *testing.T) {
	ast := parse(t, `INDEXES { UNIQUE idx_uq (email, name); }`)
	if len(ast.Indices) != 1 {
		t.Fatalf("expected 1 index, got %d", len(ast.Indices))
	}
	idx := ast.Indices[0]
	if !idx.Unique {
		t.Error("expected Unique = true")
	}
	if len(idx.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(idx.Columns))
	}
}

// TestIndexConcurrentlyPrefix guards a real bug found live-testing a demo
// project: the RFC's ABNF/parser used to accept a trailing
// "CONCURRENTLY <bool>;" clause that has no real PostgreSQL equivalent —
// CONCURRENTLY is a bare presence keyword in real PG (CREATE [UNIQUE] INDEX
// [CONCURRENTLY] name), never a boolean toggle. This guards the corrected
// grammar: CONCURRENTLY as a bare prefix keyword, positioned exactly where
// real PG puts it, stacking with UNIQUE in the same PG order.
func TestIndexConcurrentlyPrefix(t *testing.T) {
	ast := parse(t, `INDEXES { CONCURRENTLY idx_c (email); }`)
	idx := ast.Indices[0]
	if !idx.Concurrently {
		t.Error("expected Concurrently = true")
	}
}

func TestIndexUniqueConcurrentlyPrefixOrder(t *testing.T) {
	ast := parse(t, `INDEXES { UNIQUE CONCURRENTLY idx_uc (email); }`)
	idx := ast.Indices[0]
	if !idx.Unique {
		t.Error("expected Unique = true")
	}
	if !idx.Concurrently {
		t.Error("expected Concurrently = true")
	}
}

// TestIndexModeBUniqueIndexPrefix guards Mode B's "UNIQUE INDEX name (...)"
// form, mirroring real PostgreSQL's CREATE UNIQUE INDEX order exactly —
// added alongside the CONCURRENTLY fix since Mode B's whole purpose is to
// mirror the literal CREATE INDEX statement shape.
func TestIndexModeBUniqueIndexPrefix(t *testing.T) {
	ast := parse(t, `UNIQUE INDEX idx_uq (email);`)
	if len(ast.Indices) != 1 {
		t.Fatalf("expected 1 index, got %d", len(ast.Indices))
	}
	idx := ast.Indices[0]
	if !idx.Unique {
		t.Error("expected Unique = true")
	}
}

func TestIndexModeBUniqueIndexConcurrentlyPrefix(t *testing.T) {
	ast := parse(t, `UNIQUE INDEX CONCURRENTLY idx_uqc (email);`)
	idx := ast.Indices[0]
	if !idx.Unique {
		t.Error("expected Unique = true")
	}
	if !idx.Concurrently {
		t.Error("expected Concurrently = true")
	}
}

func TestIndexModeBUniqueWithoutIndexErrors(t *testing.T) {
	if err := parseErr(t, `UNIQUE idx_uq (email);`); err == nil {
		t.Error("expected parse error for UNIQUE not followed by INDEX")
	}
}

func TestIndexWithWhere(t *testing.T) {
	ast := parse(t, `INDEXES { idx_active (status) WHERE (status != 'deleted'); }`)
	if len(ast.Indices) == 0 {
		t.Fatal("expected index")
	}
	idx := ast.Indices[0]
	if idx.Where == nil {
		t.Fatal("expected WHERE clause")
	}
	if idx.Where.Text == "" {
		t.Error("WHERE text should not be empty")
	}
}

func TestIndexWithUsing(t *testing.T) {
	// USING precedes the column list, mirroring real PostgreSQL's own
	// CREATE INDEX ... USING method (columns) order and the RFC's own
	// worked examples (idx_location USING gist (location);) — this used
	// to be accepted only after the columns, rejecting the RFC's syntax.
	ast := parse(t, `INDEXES { idx_text USING gin (content); }`)
	idx := ast.Indices[0]
	if idx.Method == nil || idx.Method.Name != "gin" {
		t.Errorf("Method: got %v", idx.Method)
	}
}

func TestMultipleIndices(t *testing.T) {
	src := `INDEXES {
		idx_email  (email);
		idx_status (status) WHERE (status != 'deleted');
	}`
	ast := parse(t, src)
	if len(ast.Indices) != 2 {
		t.Fatalf("expected 2 indices, got %d", len(ast.Indices))
	}
}

// Mode B (Section 4.8 Dual Definition Modes): the singular INDEX keyword precedes a
// single entry outside a plural block and must parse without a wrapping '{ }'
// (previously this went through the same brace-requiring parser as INDEXES
// and was a hard parse error, not just "unsupported").
func TestIndexModeBSingularKeyword(t *testing.T) {
	ast := parse(t, `INDEX idx_email (email);`)
	if len(ast.Indices) != 1 {
		t.Fatalf("expected 1 index, got %d", len(ast.Indices))
	}
	idx := ast.Indices[0]
	if idx.Name.Name != "idx_email" {
		t.Errorf("index name: got %q", idx.Name.Name)
	}
	if len(idx.Columns) != 1 || idx.Columns[0].Name != "email" {
		t.Errorf("index columns: got %v", idx.Columns)
	}
}

func TestIndexModeBWithWhereAndUsing(t *testing.T) {
	ast := parse(t, `INDEX idx_status USING gin (status) WHERE (status != 'deleted');`)
	if len(ast.Indices) != 1 {
		t.Fatalf("expected 1 index, got %d", len(ast.Indices))
	}
	idx := ast.Indices[0]
	if idx.Method == nil || idx.Method.Name != "gin" {
		t.Errorf("Method: got %v", idx.Method)
	}
	if idx.Where == nil {
		t.Fatal("expected WHERE clause")
	}
}

func TestIndexModeAAndBCanMix(t *testing.T) {
	src := `
		INDEXES { idx_a (a); }
		INDEX idx_b (b);
	`
	ast := parse(t, src)
	if len(ast.Indices) != 2 {
		t.Fatalf("expected 2 indices (one per mode), got %d", len(ast.Indices))
	}
}

// TestIndexRenamedFrom guards RFC Section 7.7's RENAMED FROM directive on an
// index entry.
func TestIndexRenamedFrom(t *testing.T) {
	ast := parse(t, `INDEXES { idx_email (email) RENAMED FROM idx_email_old; }`)
	idx := ast.Indices[0]
	if idx.RenamedFrom == nil || idx.RenamedFrom.Name != "idx_email_old" {
		t.Fatalf("RenamedFrom: got %v, want \"idx_email_old\"", idx.RenamedFrom)
	}
}

// TestIndexRenamedFromWithOtherClauses confirms RENAMED FROM composes with
// other trailing clauses regardless of the order they're written in — the
// clause loop matches whichever known keyword comes next, not a fixed
// sequence.
func TestIndexRenamedFromWithOtherClauses(t *testing.T) {
	ast := parse(t, `INDEXES { idx_active (status) WHERE (status != 'deleted') RENAMED FROM idx_active_old; }`)
	idx := ast.Indices[0]
	if idx.RenamedFrom == nil || idx.RenamedFrom.Name != "idx_active_old" {
		t.Fatalf("RenamedFrom: got %v, want \"idx_active_old\"", idx.RenamedFrom)
	}
	if idx.Where == nil {
		t.Fatal("expected WHERE clause to still parse")
	}
}

func TestIndexNullsNotDistinct(t *testing.T) {
	ast := parse(t, `INDEXES { UNIQUE idx_uq (email) NULLS NOT DISTINCT; }`)
	idx := ast.Indices[0]
	if !idx.NullsNotDistinct {
		t.Error("expected NullsNotDistinct = true")
	}
}

// The explicit-default spelling ("NULLS DISTINCT") must parse (not error) but
// records no special state — it's PG's default made explicit.
func TestIndexNullsDistinctExplicitDefault(t *testing.T) {
	ast := parse(t, `INDEXES { UNIQUE idx_uq (email) NULLS DISTINCT; }`)
	idx := ast.Indices[0]
	if idx.NullsNotDistinct {
		t.Error("expected NullsNotDistinct = false for explicit NULLS DISTINCT")
	}
}

func TestIndexNullsInvalidSuffix(t *testing.T) {
	if err := parseErr(t, `INDEXES { UNIQUE idx_uq (email) NULLS MAYBE; }`); err == nil {
		t.Error("expected parse error for NULLS MAYBE")
	}
}

func TestIndexWithStorageParams(t *testing.T) {
	ast := parse(t, `INDEXES { idx_a (a) WITH (fillfactor = 70, deduplicate_items = off); }`)
	idx := ast.Indices[0]
	if len(idx.With) != 2 {
		t.Fatalf("expected 2 storage params, got %d: %v", len(idx.With), idx.With)
	}
	if idx.With[0].Key != "fillfactor" || idx.With[0].Value != "70" {
		t.Errorf("param[0]: got %+v", idx.With[0])
	}
	if idx.With[1].Key != "deduplicate_items" || idx.With[1].Value != "off" {
		t.Errorf("param[1]: got %+v", idx.With[1])
	}
}

// ── COLUMN ────────────────────────────────────────────────────────────────────

func TestColumnComment(t *testing.T) {
	src := `COLUMN email { COMMENT 'Primary email address'; }`
	ast := parse(t, src)
	if len(ast.Columns) != 1 {
		t.Fatalf("expected 1 column block, got %d", len(ast.Columns))
	}
	col := ast.Columns[0]
	if col.Name.Name != "email" {
		t.Errorf("column name: got %q", col.Name.Name)
	}
	if col.Comment == nil || col.Comment.Value != "Primary email address" {
		t.Errorf("column comment: got %v", col.Comment)
	}
}

func TestColumnStatistics(t *testing.T) {
	src := `COLUMN status { STATISTICS 300; }`
	ast := parse(t, src)
	col := ast.Columns[0]
	if col.Statistics == nil || *col.Statistics != 300 {
		t.Errorf("Statistics: got %v", col.Statistics)
	}
}

// TestColumnStatisticsDefault is the regression guard for RFC audit item
// #112: parseStatisticsValue only accepted an integer, so
// "STATISTICS DEFAULT;" (real PostgreSQL's own ALTER ... SET STATISTICS
// DEFAULT spelling) was a hard parse error — the only way to reset a
// customized target back to default was deleting the directive entirely.
func TestColumnStatisticsDefault(t *testing.T) {
	src := `COLUMN status { STATISTICS DEFAULT; }`
	ast := parse(t, src)
	col := ast.Columns[0]
	if col.Statistics != nil {
		t.Errorf("Statistics: expected nil for DEFAULT, got %v", *col.Statistics)
	}
}

func TestColumnStatisticsInvalidErrors(t *testing.T) {
	if err := parseErr(t, `COLUMN status { STATISTICS bogus; }`); err == nil {
		t.Fatal("expected an error for a non-integer, non-DEFAULT STATISTICS value")
	}
}

// TestColumnStatisticsNegativeOne is the regression guard for DPG-E020's
// documented valid range, [-1, 10000]: -1 (restore PostgreSQL's system
// default) is itself a legal value, but parseStatisticsValue's digit-only
// scan couldn't even parse a leading '-', so the one negative value real
// PostgreSQL accepts was a hard parse error.
func TestColumnStatisticsNegativeOne(t *testing.T) {
	src := `COLUMN status { STATISTICS -1; }`
	ast := parse(t, src)
	col := ast.Columns[0]
	if col.Statistics == nil || *col.Statistics != -1 {
		t.Errorf("Statistics: got %v, want -1", col.Statistics)
	}
}

// TestColumnStatisticsOutOfRange is the regression guard for RFC Section
// 7.4's DPG-E020: a STATISTICS value outside [-1, 10000] was previously
// accepted with no range check at all.
func TestColumnStatisticsOutOfRange(t *testing.T) {
	for _, src := range []string{
		`COLUMN status { STATISTICS -2; }`,
		`COLUMN status { STATISTICS 10001; }`,
	} {
		err := parseErr(t, src)
		if err == nil {
			t.Fatalf("%s: expected DPG-E020 range error, got none", src)
		}
		if !strings.Contains(err.Error(), "DPG-E020") {
			t.Errorf("%s: expected error to mention DPG-E020, got: %v", src, err)
		}
	}
}

// TestDependsOnExtension is the regression guard for RFC audit item #71:
// Section 9.1's func-block-only "DEPENDS ON EXTENSION ext;" directive
// (Function/Procedure), repeatable.
func TestDependsOnExtension(t *testing.T) {
	src := `DEPENDS ON EXTENSION pgcrypto;
DEPENDS ON EXTENSION postgis;`
	ast := parse(t, src)
	if len(ast.DependsOnExtensions) != 2 || ast.DependsOnExtensions[0] != "pgcrypto" || ast.DependsOnExtensions[1] != "postgis" {
		t.Errorf("DependsOnExtensions: got %v, want [pgcrypto postgis]", ast.DependsOnExtensions)
	}
}

// TestNoDependsOnExtensionIsNoop proves the negative form parses without
// error but contributes nothing to the desired set — a purely declarative
// model already expresses "does not depend on ext" by omission.
func TestNoDependsOnExtensionIsNoop(t *testing.T) {
	src := `NO DEPENDS ON EXTENSION pgcrypto;`
	ast := parse(t, src)
	if len(ast.DependsOnExtensions) != 0 {
		t.Errorf("expected no DependsOnExtensions from the negative form, got %v", ast.DependsOnExtensions)
	}
}

func TestColumnRenamedFrom(t *testing.T) {
	src := `COLUMN email_address { RENAMED FROM email; }`
	ast := parse(t, src)
	col := ast.Columns[0]
	if col.RenamedFrom == nil || col.RenamedFrom.Name != "email" {
		t.Errorf("RenamedFrom: got %v", col.RenamedFrom)
	}
}

func TestColumnsBlock(t *testing.T) {
	src := `COLUMNS {
		email { COMMENT 'Email'; }
		status { STATISTICS 500; }
	}`
	ast := parse(t, src)
	if len(ast.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(ast.Columns))
	}
}

// ── GRANTS / REVOKES ──────────────────────────────────────────────────────

func TestGrants(t *testing.T) {
	src := `GRANTS { SELECT, INSERT TO app_service; SELECT TO app_readonly; }`
	ast := parse(t, src)
	if len(ast.Grants) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(ast.Grants))
	}
	g0 := ast.Grants[0]
	if len(g0.Privileges) != 2 {
		t.Errorf("grant 0 privs: got %v", g0.Privileges)
	}
	if len(g0.Roles) != 1 || g0.Roles[0].Name != "app_service" {
		t.Errorf("grant 0 roles: got %v", g0.Roles)
	}
}

func TestGrantAllPrivileges(t *testing.T) {
	src := `GRANTS { ALL PRIVILEGES TO admin; }`
	ast := parse(t, src)
	if len(ast.Grants) != 1 {
		t.Fatal("expected 1 grant")
	}
	if ast.Grants[0].Privileges != nil {
		t.Error("expected nil Privileges for ALL")
	}
}

// TestGrantGrantedBy guards RFC audit item #90: GRANTED BY role-spec on a
// grant-entry, both the WITH GRANT OPTION + GRANTED BY combination (grammar
// order matters — WITH GRANT OPTION first) and GRANTED BY alone.
// TestRoleConfigSet guards RFC audit item #74: "SET param TO value;" and
// "SET param = value;" forms, plain and IN DATABASE-qualified.
func TestRoleConfigSet(t *testing.T) {
	ast := parse(t, `SET statement_timeout TO '5s'; SET work_mem = '64MB' IN DATABASE mydb;`)
	if len(ast.RoleConfigs) != 2 {
		t.Fatalf("expected 2 role configs, got %d", len(ast.RoleConfigs))
	}
	c0 := ast.RoleConfigs[0]
	if c0.Param != "statement_timeout" || c0.Value == nil || *c0.Value != "'5s'" {
		t.Errorf("config 0: got %+v", c0)
	}
	if c0.InDatabase != nil {
		t.Errorf("config 0: expected nil InDatabase (cluster-wide), got %v", c0.InDatabase)
	}
	c1 := ast.RoleConfigs[1]
	if c1.Param != "work_mem" || c1.Value == nil || *c1.Value != "'64MB'" {
		t.Errorf("config 1: got %+v", c1)
	}
	if c1.InDatabase == nil || *c1.InDatabase != "mydb" {
		t.Errorf("config 1 InDatabase: got %v", c1.InDatabase)
	}
}

// TestRoleConfigSetFromCurrent guards the "SET param FROM CURRENT;" form.
func TestRoleConfigSetFromCurrent(t *testing.T) {
	ast := parse(t, `SET search_path FROM CURRENT;`)
	if len(ast.RoleConfigs) != 1 {
		t.Fatalf("expected 1 role config, got %d", len(ast.RoleConfigs))
	}
	c := ast.RoleConfigs[0]
	if c.Param != "search_path" || !c.FromCurrent || c.Value != nil {
		t.Errorf("got %+v", c)
	}
}

// TestRoleConfigReset guards "RESET param;" and "RESET ALL;", including the
// IN DATABASE qualifier.
func TestRoleConfigReset(t *testing.T) {
	ast := parse(t, `RESET statement_timeout; RESET ALL IN DATABASE mydb;`)
	if len(ast.RoleConfigs) != 2 {
		t.Fatalf("expected 2 role configs, got %d", len(ast.RoleConfigs))
	}
	c0 := ast.RoleConfigs[0]
	if !c0.Reset || c0.ResetAll || c0.Param != "statement_timeout" {
		t.Errorf("config 0: got %+v", c0)
	}
	c1 := ast.RoleConfigs[1]
	if !c1.Reset || !c1.ResetAll || c1.Param != "" {
		t.Errorf("config 1: got %+v", c1)
	}
	if c1.InDatabase == nil || *c1.InDatabase != "mydb" {
		t.Errorf("config 1 InDatabase: got %v", c1.InDatabase)
	}
}

// TestRoleConfigNamespacedParam guards a dotted, extension-namespaced GUC
// name (e.g. pg_stat_statements.track), real PostgreSQL var_name grammar.
func TestRoleConfigNamespacedParam(t *testing.T) {
	ast := parse(t, `SET pg_stat_statements.track = 'all';`)
	if len(ast.RoleConfigs) != 1 || ast.RoleConfigs[0].Param != "pg_stat_statements.track" {
		t.Fatalf("got %+v", ast.RoleConfigs)
	}
}

// TestMembershipInRoleBare guards RFC audit item #32's block-level "IN
// ROLE identifier;" form with no WITH clause at all.
func TestMembershipInRoleBare(t *testing.T) {
	ast := parse(t, `IN ROLE parent1;`)
	if len(ast.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d: %+v", len(ast.Memberships), ast.Memberships)
	}
	m := ast.Memberships[0]
	if m.Role.Name != "parent1" || m.Direction != "IN_ROLE" || m.AdminDefault {
		t.Errorf("got %+v", m)
	}
	if m.Admin != nil || m.Inherit != nil || m.Set != nil {
		t.Errorf("expected all modifiers nil for a bare entry, got %+v", m)
	}
}

// TestMembershipInRoleWithAllModifiers guards the full WITH ADMIN OPTION +
// WITH INHERIT + WITH SET combination, in RFC grammar order.
func TestMembershipInRoleWithAllModifiers(t *testing.T) {
	ast := parse(t, `IN ROLE parent1 WITH ADMIN OPTION WITH INHERIT FALSE WITH SET TRUE;`)
	if len(ast.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(ast.Memberships))
	}
	m := ast.Memberships[0]
	if m.Role.Name != "parent1" || m.Direction != "IN_ROLE" {
		t.Errorf("got %+v", m)
	}
	if m.Admin == nil || !*m.Admin {
		t.Errorf("Admin: got %v, want true", m.Admin)
	}
	if m.Inherit == nil || *m.Inherit {
		t.Errorf("Inherit: got %v, want false", m.Inherit)
	}
	if m.Set == nil || !*m.Set {
		t.Errorf("Set: got %v, want true", m.Set)
	}
}

// TestMembershipAdminBooleanSpelling guards WITH ADMIN's second accepted
// spelling — a bare boolean instead of the OPTION keyword (RFC's own
// membership-opt grammar: "ADMIN" WSP ( "OPTION" / boolean )).
func TestMembershipAdminBooleanSpelling(t *testing.T) {
	ast := parse(t, `ROLE child1 WITH ADMIN TRUE;`)
	if len(ast.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(ast.Memberships))
	}
	m := ast.Memberships[0]
	if m.Direction != "ROLE" || m.Admin == nil || !*m.Admin {
		t.Errorf("got %+v", m)
	}
}

// TestMembershipRoleBucket guards the bare "ROLE identifier;" block form
// (Direction "ROLE", no admin default).
func TestMembershipRoleBucket(t *testing.T) {
	ast := parse(t, `ROLE child1;`)
	if len(ast.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(ast.Memberships))
	}
	m := ast.Memberships[0]
	if m.Direction != "ROLE" || m.AdminDefault {
		t.Errorf("got %+v", m)
	}
}

// TestMembershipAdminBucket guards the bare "ADMIN identifier;" block
// form — AdminDefault true, Direction "ROLE" (RFC audit item #32's
// unification: ADMIN collapses into the same Direction as ROLE, admin
// baseline forced on).
func TestMembershipAdminBucket(t *testing.T) {
	ast := parse(t, `ADMIN child1;`)
	if len(ast.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(ast.Memberships))
	}
	m := ast.Memberships[0]
	if m.Direction != "ROLE" || !m.AdminDefault {
		t.Errorf("got %+v", m)
	}
}

// TestMembershipMultipleEntriesRepeatable guards that the block-level
// directive is repeatable (Mode B, like TRIGGER/POLICY), not limited to
// one entry per role declaration.
func TestMembershipMultipleEntriesRepeatable(t *testing.T) {
	ast := parse(t, `IN ROLE parent1; IN ROLE parent2 WITH INHERIT FALSE; ROLE child1; ADMIN child2;`)
	if len(ast.Memberships) != 4 {
		t.Fatalf("expected 4 memberships, got %d: %+v", len(ast.Memberships), ast.Memberships)
	}
}

func TestGrantGrantedBy(t *testing.T) {
	ast := parse(t, `GRANTS { SELECT TO reader WITH GRANT OPTION GRANTED BY admin; INSERT TO writer GRANTED BY admin2; }`)
	if len(ast.Grants) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(ast.Grants))
	}
	g0 := ast.Grants[0]
	if !g0.WithGrant {
		t.Error("grant 0: expected WithGrant=true")
	}
	if g0.GrantedBy == nil || *g0.GrantedBy != "admin" {
		t.Errorf("grant 0 GrantedBy: got %v", g0.GrantedBy)
	}
	g1 := ast.Grants[1]
	if g1.WithGrant {
		t.Error("grant 1: expected WithGrant=false")
	}
	if g1.GrantedBy == nil || *g1.GrantedBy != "admin2" {
		t.Errorf("grant 1 GrantedBy: got %v", g1.GrantedBy)
	}
}

// TestGrantGrantedByRoleSpecKeywords guards role-spec's three fixed
// keyword forms (CURRENT_ROLE/CURRENT_USER/SESSION_USER), not just a plain
// identifier.
func TestGrantGrantedByRoleSpecKeywords(t *testing.T) {
	ast := parse(t, `GRANTS { SELECT TO reader GRANTED BY CURRENT_ROLE; INSERT TO writer GRANTED BY session_user; }`)
	if len(ast.Grants) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(ast.Grants))
	}
	if ast.Grants[0].GrantedBy == nil || *ast.Grants[0].GrantedBy != "CURRENT_ROLE" {
		t.Errorf("grant 0 GrantedBy: got %v", ast.Grants[0].GrantedBy)
	}
	if ast.Grants[1].GrantedBy == nil || *ast.Grants[1].GrantedBy != "SESSION_USER" {
		t.Errorf("grant 1 GrantedBy: got %v", ast.Grants[1].GrantedBy)
	}
}

// TestGrantNoGrantedByLeavesNil guards the common (unspecified) case: no
// GRANTED BY clause must leave the field nil, not a zero-value empty string.
func TestGrantNoGrantedByLeavesNil(t *testing.T) {
	ast := parse(t, `GRANTS { SELECT TO reader; }`)
	if ast.Grants[0].GrantedBy != nil {
		t.Errorf("expected nil GrantedBy, got %v", ast.Grants[0].GrantedBy)
	}
}

// TestRevocationGrantedByAndCascade guards RFC audit item #90's
// revoke-entry: GRANTED BY role-spec, ordered before CASCADE.
func TestRevocationGrantedByAndCascade(t *testing.T) {
	ast := parse(t, `REVOKES { SELECT FROM reader GRANTED BY admin CASCADE; }`)
	if len(ast.Revocations) != 1 {
		t.Fatalf("expected 1 revocation, got %d", len(ast.Revocations))
	}
	r := ast.Revocations[0]
	if r.GrantedBy == nil || *r.GrantedBy != "admin" {
		t.Errorf("GrantedBy: got %v", r.GrantedBy)
	}
	if !r.Cascade {
		t.Error("expected Cascade=true")
	}
}

func TestRevocations(t *testing.T) {
	src := `REVOKES { ALL PRIVILEGES FROM PUBLIC; }`
	ast := parse(t, src)
	if len(ast.Revocations) != 1 {
		t.Fatalf("expected 1 revocation, got %d", len(ast.Revocations))
	}
	r := ast.Revocations[0]
	if len(r.Roles) != 1 || r.Roles[0].Name != "PUBLIC" {
		t.Errorf("revocation roles: got %v", r.Roles)
	}
}

// Mode B (Section 4.8 Dual Definition Modes): the singular GRANT/REVOKE keyword
// precedes a single entry outside a plural block and must parse without a
// wrapping '{ }' — previously this went through the same brace-requiring
// parser as GRANTS/REVOKES and was a hard parse error, exactly the same
// conflation bug fixed for INDEX/INDEXES.
func TestGrantModeBSingularKeyword(t *testing.T) {
	ast := parse(t, `GRANT SELECT, INSERT TO app_service;`)
	if len(ast.Grants) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(ast.Grants))
	}
	g := ast.Grants[0]
	if len(g.Privileges) != 2 || len(g.Roles) != 1 || g.Roles[0].Name != "app_service" {
		t.Errorf("grant: got %+v", g)
	}
}

func TestRevocationModeBSingularKeyword(t *testing.T) {
	ast := parse(t, `REVOKE ALL PRIVILEGES FROM PUBLIC;`)
	if len(ast.Revocations) != 1 {
		t.Fatalf("expected 1 revocation, got %d", len(ast.Revocations))
	}
	r := ast.Revocations[0]
	if r.Privileges != nil || len(r.Roles) != 1 || r.Roles[0].Name != "PUBLIC" {
		t.Errorf("revocation: got %+v", r)
	}
}

func TestGrantRevocationModeAAndBCanMix(t *testing.T) {
	src := `
		GRANTS { SELECT TO reader; }
		GRANT INSERT TO writer;
		REVOKES { ALL PRIVILEGES FROM PUBLIC; }
		REVOKE UPDATE FROM guest;
	`
	ast := parse(t, src)
	if len(ast.Grants) != 2 {
		t.Fatalf("expected 2 grants (one per mode), got %d", len(ast.Grants))
	}
	if len(ast.Revocations) != 2 {
		t.Fatalf("expected 2 revocations (one per mode), got %d", len(ast.Revocations))
	}
}

// GRANT/REVOKE Mode B inside a COLUMN block exercises the second of the
// three dispatch sites that had this conflation (table-level, column-level,
// DEFAULT PRIVILEGES-level).
func TestGrantModeBInColumnBlock(t *testing.T) {
	ast := parse(t, `COLUMN email { GRANT SELECT TO reader; REVOKE UPDATE FROM guest; }`)
	if len(ast.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(ast.Columns))
	}
	col := ast.Columns[0]
	if len(col.Grants) != 1 || len(col.Revocations) != 1 {
		t.Errorf("column grants/revocations: got %d/%d", len(col.Grants), len(col.Revocations))
	}
}

// GRANT/REVOKE Mode B inside a DEFAULT PRIVILEGES block exercises the
// third of the three dispatch sites that had this conflation.
func TestGrantModeBInDefaultPrivilegesBlock(t *testing.T) {
	ast := parse(t, `DEFAULT PRIVILEGES { GRANT SELECT ON TABLES TO reader; REVOKE UPDATE ON TABLES FROM guest; }`)
	if len(ast.DefaultPrivileges) != 1 {
		t.Fatalf("expected 1 default privileges block, got %d", len(ast.DefaultPrivileges))
	}
	dp := ast.DefaultPrivileges[0]
	if len(dp.Grants) != 1 || len(dp.Revocations) != 1 {
		t.Errorf("dp grants/revocations: got %d/%d", len(dp.Grants), len(dp.Revocations))
	}
	if dp.Grants[0].ObjectType != "TABLES" || dp.Revocations[0].ObjectType != "TABLES" {
		t.Errorf("dp grant/revocation ObjectType: got %q/%q", dp.Grants[0].ObjectType, dp.Revocations[0].ObjectType)
	}
}

// TestDefaultPrivilegesMultipleObjectTypes guards the RFC's own worked
// example: TABLES, FUNCTIONS, and SEQUENCES declared together in one
// GRANTS { } block, each carrying its own ON <type> clause — real
// PostgreSQL's actual ALTER DEFAULT PRIVILEGES grammar (confirmed via
// \h ALTER DEFAULT PRIVILEGES), not a DPG-invented whole-declaration
// "FOR object_type" wrapper the parser used to require instead.
func TestDefaultPrivilegesMultipleObjectTypes(t *testing.T) {
	ast := parse(t, `DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public {
		GRANTS {
			SELECT ON TABLES TO app_readonly;
			EXECUTE ON FUNCTIONS TO app_service;
			USAGE ON SEQUENCES TO app_service;
		}
	}`)
	if len(ast.DefaultPrivileges) != 1 {
		t.Fatalf("expected 1 default privileges block, got %d", len(ast.DefaultPrivileges))
	}
	dp := ast.DefaultPrivileges[0]
	if dp.ForRole == nil || dp.ForRole.Name != "app_admin" {
		t.Errorf("ForRole: got %v", dp.ForRole)
	}
	if dp.InSchema == nil || dp.InSchema.Name != "public" {
		t.Errorf("InSchema: got %v", dp.InSchema)
	}
	if len(dp.Grants) != 3 {
		t.Fatalf("expected 3 grants, got %d", len(dp.Grants))
	}
	wantTypes := []string{"TABLES", "FUNCTIONS", "SEQUENCES"}
	for i, want := range wantTypes {
		if dp.Grants[i].ObjectType != want {
			t.Errorf("Grants[%d].ObjectType: got %q, want %q", i, dp.Grants[i].ObjectType, want)
		}
	}
}

// TestDefaultPrivilegesHeaderOrderIndependent guards that IN SCHEMA and FOR
// ROLE may appear in either order — matching how the header is parsed as a
// keyword loop, not a fixed sequence.
func TestDefaultPrivilegesHeaderOrderIndependent(t *testing.T) {
	ast := parse(t, `DEFAULT PRIVILEGES IN SCHEMA public FOR ROLE app_admin {
		GRANTS { SELECT ON TABLES TO app_readonly; }
	}`)
	if len(ast.DefaultPrivileges) != 1 {
		t.Fatalf("expected 1 default privileges block, got %d", len(ast.DefaultPrivileges))
	}
	dp := ast.DefaultPrivileges[0]
	if dp.ForRole == nil || dp.ForRole.Name != "app_admin" {
		t.Errorf("ForRole: got %v", dp.ForRole)
	}
	if dp.InSchema == nil || dp.InSchema.Name != "public" {
		t.Errorf("InSchema: got %v", dp.InSchema)
	}
}

// TestDefaultPrivilegesWithGrantOption and ALL PRIVILEGES / CASCADE guard
// the remaining grant-clause options real PG supports.
func TestDefaultPrivilegesWithGrantOptionAndAllPrivileges(t *testing.T) {
	ast := parse(t, `DEFAULT PRIVILEGES {
		GRANTS { ALL PRIVILEGES ON TABLES TO app_admin WITH GRANT OPTION; }
		REVOKES { ALL ON TABLES FROM app_readonly CASCADE; }
	}`)
	dp := ast.DefaultPrivileges[0]
	if dp.Grants[0].Privileges != nil {
		t.Errorf("ALL PRIVILEGES should leave Privileges nil, got %v", dp.Grants[0].Privileges)
	}
	if !dp.Grants[0].WithGrant {
		t.Error("WITH GRANT OPTION did not parse")
	}
	if dp.Revocations[0].Privileges != nil {
		t.Errorf("ALL should leave Privileges nil, got %v", dp.Revocations[0].Privileges)
	}
	if !dp.Revocations[0].Cascade {
		t.Error("CASCADE did not parse")
	}
}

// TestParseDefaultPrivilegesTopLevel guards the exported top-level entry
// point used by the compiler's DEFAULT PRIVILEGES bypass (see
// compiler.Compile): header text ("FOR ROLE x IN SCHEMA y", DPG's Part 1
// equivalent for this kind) and body text (the '{ }' content, braces
// excluded — matching Parse's own part2 convention) are parsed from two
// separate strings, mirroring exactly what raw.Part1/raw.Part2 already are.
func TestParseDefaultPrivilegesTopLevel(t *testing.T) {
	dp, err := blockparser.ParseDefaultPrivileges(
		"FOR ROLE app_admin IN SCHEMA public",
		`GRANTS { SELECT ON TABLES TO app_readonly; }`,
		zeroPos,
	)
	if err != nil {
		t.Fatalf("ParseDefaultPrivileges: %v", err)
	}
	if dp.ForRole == nil || dp.ForRole.Name != "app_admin" {
		t.Errorf("ForRole: got %v", dp.ForRole)
	}
	if dp.InSchema == nil || dp.InSchema.Name != "public" {
		t.Errorf("InSchema: got %v", dp.InSchema)
	}
	if len(dp.Grants) != 1 || dp.Grants[0].ObjectType != "TABLES" {
		t.Fatalf("Grants: got %+v", dp.Grants)
	}
}

// TestParseDefaultPrivilegesTopLevelNoHeader guards an empty header (no FOR
// ROLE / IN SCHEMA at all — a database-wide default for the current role).
func TestParseDefaultPrivilegesTopLevelNoHeader(t *testing.T) {
	dp, err := blockparser.ParseDefaultPrivileges(
		"",
		`GRANTS { SELECT ON TABLES TO app_readonly; }`,
		zeroPos,
	)
	if err != nil {
		t.Fatalf("ParseDefaultPrivileges: %v", err)
	}
	if dp.ForRole != nil || dp.InSchema != nil {
		t.Errorf("expected nil ForRole/InSchema, got %v/%v", dp.ForRole, dp.InSchema)
	}
}

// ── PARAMETER PRIVILEGES (RFC Section 11.6, PG15+) ──────────────────────────────

// TestParseParameterPrivilegesTopLevel guards the top-level entry point used
// by the compiler's PARAMETER PRIVILEGES bypass, mirroring
// TestParseDefaultPrivilegesTopLevel: header text (always empty for this
// kind — no FOR ROLE/IN SCHEMA clause) and body text are parsed from two
// separate strings, matching raw.Part1/raw.Part2.
func TestParseParameterPrivilegesTopLevel(t *testing.T) {
	pp, err := blockparser.ParseParameterPrivileges(
		"",
		`GRANTS { SET ON PARAMETER work_mem, statement_timeout TO app_admin; }`,
		zeroPos,
	)
	if err != nil {
		t.Fatalf("ParseParameterPrivileges: %v", err)
	}
	if len(pp.Grants) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(pp.Grants))
	}
	g := pp.Grants[0]
	if len(g.Privileges) != 1 || g.Privileges[0] != "SET" {
		t.Errorf("Privileges: got %v", g.Privileges)
	}
	if len(g.Parameters) != 2 || g.Parameters[0].Name != "work_mem" || g.Parameters[1].Name != "statement_timeout" {
		t.Errorf("Parameters: got %v", g.Parameters)
	}
	if len(g.Roles) != 1 || g.Roles[0].Name != "app_admin" {
		t.Errorf("Roles: got %v", g.Roles)
	}
}

// TestParseParameterPrivilegesNonEmptyHeaderErrors guards that a non-blank
// header (text between "PARAMETER PRIVILEGES" and '{') is rejected —
// unlike DEFAULT PRIVILEGES, this kind has no FOR ROLE/IN SCHEMA clause at
// all (RFC Section 11.6: cluster-scoped, parameters have no schema).
func TestParseParameterPrivilegesNonEmptyHeaderErrors(t *testing.T) {
	_, err := blockparser.ParseParameterPrivileges("FOR ROLE app_admin", `GRANTS { SET ON PARAMETER work_mem TO app_admin; }`, zeroPos)
	if err == nil {
		t.Fatal("expected an error for a non-empty PARAMETER PRIVILEGES header")
	}
}

// TestParameterPrivilegesAlterSystemPrivilege guards the ALTER SYSTEM
// two-word privilege token, the one privilege of pp-privilege's four
// alternatives that spans two words.
func TestParameterPrivilegesAlterSystemPrivilege(t *testing.T) {
	pp, err := blockparser.ParseParameterPrivileges(
		"",
		`GRANTS { ALTER SYSTEM ON PARAMETER shared_preload_libraries TO app_admin; }`,
		zeroPos,
	)
	if err != nil {
		t.Fatalf("ParseParameterPrivileges: %v", err)
	}
	g := pp.Grants[0]
	if len(g.Privileges) != 1 || g.Privileges[0] != "ALTER SYSTEM" {
		t.Errorf("Privileges: got %v", g.Privileges)
	}
}

// TestParameterPrivilegesMultiplePrivileges guards a comma-separated
// privilege list mixing the single-word and two-word forms.
func TestParameterPrivilegesMultiplePrivileges(t *testing.T) {
	pp, err := blockparser.ParseParameterPrivileges(
		"",
		`GRANTS { SET, ALTER SYSTEM ON PARAMETER work_mem TO app_admin; }`,
		zeroPos,
	)
	if err != nil {
		t.Fatalf("ParseParameterPrivileges: %v", err)
	}
	g := pp.Grants[0]
	if len(g.Privileges) != 2 || g.Privileges[0] != "SET" || g.Privileges[1] != "ALTER SYSTEM" {
		t.Errorf("Privileges: got %v", g.Privileges)
	}
}

// TestParameterPrivilegesWithGrantOptionAndAllPrivileges guards WITH GRANT
// OPTION on a grant and ALL/CASCADE on a revocation, mirroring
// TestDefaultPrivilegesWithGrantOptionAndAllPrivileges.
func TestParameterPrivilegesWithGrantOptionAndAllPrivileges(t *testing.T) {
	pp, err := blockparser.ParseParameterPrivileges("", `
		GRANTS { ALL PRIVILEGES ON PARAMETER work_mem TO app_admin WITH GRANT OPTION; }
		REVOKES { ALL ON PARAMETER work_mem FROM app_readonly CASCADE; }
	`, zeroPos)
	if err != nil {
		t.Fatalf("ParseParameterPrivileges: %v", err)
	}
	if pp.Grants[0].Privileges != nil {
		t.Errorf("ALL PRIVILEGES should leave Privileges nil, got %v", pp.Grants[0].Privileges)
	}
	if !pp.Grants[0].WithGrant {
		t.Error("WITH GRANT OPTION did not parse")
	}
	if pp.Revocations[0].Privileges != nil {
		t.Errorf("ALL should leave Privileges nil, got %v", pp.Revocations[0].Privileges)
	}
	if !pp.Revocations[0].Cascade {
		t.Error("CASCADE did not parse")
	}
}

// TestParameterPrivilegesRevocation guards a plain REVOKES entry.
func TestParameterPrivilegesRevocation(t *testing.T) {
	pp, err := blockparser.ParseParameterPrivileges(
		"",
		`REVOKES { SET ON PARAMETER statement_timeout FROM app_readonly; }`,
		zeroPos,
	)
	if err != nil {
		t.Fatalf("ParseParameterPrivileges: %v", err)
	}
	if len(pp.Revocations) != 1 {
		t.Fatalf("expected 1 revocation, got %d", len(pp.Revocations))
	}
	r := pp.Revocations[0]
	if len(r.Parameters) != 1 || r.Parameters[0].Name != "statement_timeout" {
		t.Errorf("Parameters: got %v", r.Parameters)
	}
	if len(r.Roles) != 1 || r.Roles[0].Name != "app_readonly" {
		t.Errorf("Roles: got %v", r.Roles)
	}
}

func TestParameterPrivilegesGrantedBy(t *testing.T) {
	pp, err := blockparser.ParseParameterPrivileges("", `
		GRANTS { SET ON PARAMETER work_mem TO app_admin WITH GRANT OPTION GRANTED BY admin_role; }
		REVOKES { SET ON PARAMETER work_mem FROM app_readonly GRANTED BY admin_role CASCADE; }
	`, zeroPos)
	if err != nil {
		t.Fatalf("ParseParameterPrivileges: %v", err)
	}
	if pp.Grants[0].GrantedBy == nil || *pp.Grants[0].GrantedBy != "admin_role" {
		t.Errorf("GRANTED BY did not parse on grant, got %v", pp.Grants[0].GrantedBy)
	}
	if !pp.Grants[0].WithGrant {
		t.Error("WITH GRANT OPTION should still parse alongside GRANTED BY")
	}
	if pp.Revocations[0].GrantedBy == nil || *pp.Revocations[0].GrantedBy != "admin_role" {
		t.Errorf("GRANTED BY did not parse on revocation, got %v", pp.Revocations[0].GrantedBy)
	}
	if !pp.Revocations[0].Cascade {
		t.Error("CASCADE should still parse after GRANTED BY on revocation")
	}
}

// ── POLICIES ─────────────────────────────────────────────────────────────────

func TestSimplePolicy(t *testing.T) {
	src := `POLICIES {
		view_self FOR SELECT USING (id = auth.uid());
	}`
	ast := parse(t, src)
	if len(ast.Policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(ast.Policies))
	}
	pol := ast.Policies[0]
	if pol.Name.Name != "view_self" {
		t.Errorf("policy name: got %q", pol.Name.Name)
	}
	if pol.Command != "SELECT" {
		t.Errorf("policy command: got %q", pol.Command)
	}
	if pol.Using == nil {
		t.Error("expected USING clause")
	}
}

// TestPolicyRenamedFrom guards RFC Section 7.8's policy-decl RENAMED FROM
// clause (the last optional clause before the terminating ';').
func TestPolicyRenamedFrom(t *testing.T) {
	src := `POLICIES {
		view_self FOR SELECT USING (id = auth.uid()) RENAMED FROM view_own;
	}`
	ast := parse(t, src)
	if len(ast.Policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(ast.Policies))
	}
	pol := ast.Policies[0]
	if pol.RenamedFrom == nil || pol.RenamedFrom.Name != "view_own" {
		t.Errorf("RenamedFrom: got %v, want view_own", pol.RenamedFrom)
	}
}

// TestPolicyRenamedFromWithTrailingComment guards RENAMED FROM followed by
// a trailing { COMMENT '...'; } block on the same policy entry.
func TestPolicyRenamedFromWithTrailingComment(t *testing.T) {
	src := `POLICIES {
		view_self FOR SELECT RENAMED FROM view_own { COMMENT 'self-visibility'; }
	}`
	ast := parse(t, src)
	if len(ast.Policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(ast.Policies))
	}
	pol := ast.Policies[0]
	if pol.RenamedFrom == nil || pol.RenamedFrom.Name != "view_own" {
		t.Errorf("RenamedFrom: got %v, want view_own", pol.RenamedFrom)
	}
	if pol.Comment == nil || pol.Comment.Value != "self-visibility" {
		t.Errorf("Comment: got %v", pol.Comment)
	}
}

// Mode B (Section 4.8 Dual Definition Modes): the singular POLICY keyword precedes a
// single entry outside a plural block — previously this would have hit the
// same conflation bug already fixed for INDEX/INDEXES and GRANT/REVOKE
// (POLICY wasn't even in the dispatch switch at all, so it was "unknown block
// directive", not just brace-mismatched).
func TestPolicyModeBSingularKeyword(t *testing.T) {
	ast := parse(t, `POLICY view_self FOR SELECT USING (id = auth.uid());`)
	if len(ast.Policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(ast.Policies))
	}
	pol := ast.Policies[0]
	if pol.Name.Name != "view_self" {
		t.Errorf("policy name: got %q", pol.Name.Name)
	}
	if pol.Command != "SELECT" {
		t.Errorf("policy command: got %q", pol.Command)
	}
}

func TestPolicyModeAAndBCanMix(t *testing.T) {
	src := `
		POLICIES { p_a FOR SELECT USING (true); }
		POLICY p_b FOR INSERT USING (true);
	`
	ast := parse(t, src)
	if len(ast.Policies) != 2 {
		t.Fatalf("expected 2 policies (one per mode), got %d", len(ast.Policies))
	}
}

// ── TRIGGERS ─────────────────────────────────────────────────────────────────

func TestSimpleTrigger(t *testing.T) {
	src := `TRIGGERS {
		after_insert AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION on_insert();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if tr.Name.Name != "after_insert" {
		t.Errorf("trigger name: got %q", tr.Name.Name)
	}
	if tr.When != "AFTER" {
		t.Errorf("trigger when: got %q", tr.When)
	}
	if len(tr.Events) != 1 || tr.Events[0] != "INSERT" {
		t.Errorf("trigger events: got %v", tr.Events)
	}
	if tr.ForEach != "ROW" {
		t.Errorf("trigger forEach: got %q", tr.ForEach)
	}
	if tr.Function.Name != "on_insert" {
		t.Errorf("trigger function: got %q", tr.Function.Name)
	}
}

// TestTriggerUpdateOfColumns guards RFC audit item #1: the "UPDATE OF
// col1, col2, ..." column list was tokenized and explicitly discarded — a
// trigger declared to fire only on specific columns actually fired on
// every column update instead, a real semantics divergence.
func TestTriggerUpdateOfColumns(t *testing.T) {
	src := `TRIGGERS {
		after_email_change AFTER UPDATE OF email, status
			FOR EACH ROW
			EXECUTE FUNCTION notify_email_change();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if len(tr.Events) != 1 || tr.Events[0] != "UPDATE" {
		t.Errorf("trigger events: got %v", tr.Events)
	}
	if want := []string{"email", "status"}; !slices.Equal(tr.UpdateOfColumns, want) {
		t.Errorf("UpdateOfColumns: got %v, want %v", tr.UpdateOfColumns, want)
	}
}

// TestTriggerUpdateOfColumnsWithOrEvent proves the OF column list doesn't
// swallow a following "OR <event>" clause.
func TestTriggerUpdateOfColumnsWithOrEvent(t *testing.T) {
	src := `TRIGGERS {
		trg AFTER INSERT OR UPDATE OF email
			FOR EACH ROW
			EXECUTE FUNCTION f();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if want := []string{"INSERT", "UPDATE"}; !slices.Equal(tr.Events, want) {
		t.Errorf("Events: got %v, want %v", tr.Events, want)
	}
	if want := []string{"email"}; !slices.Equal(tr.UpdateOfColumns, want) {
		t.Errorf("UpdateOfColumns: got %v, want %v", tr.UpdateOfColumns, want)
	}
}

// TestTriggerUpdateWithoutOfColumns proves UpdateOfColumns stays nil when
// the source doesn't write an OF clause at all — fires on every column
// update, PostgreSQL's own default.
func TestTriggerUpdateWithoutOfColumns(t *testing.T) {
	src := `TRIGGERS {
		trg AFTER UPDATE
			FOR EACH ROW
			EXECUTE FUNCTION f();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	if ast.Triggers[0].UpdateOfColumns != nil {
		t.Errorf("UpdateOfColumns: got %v, want nil", ast.Triggers[0].UpdateOfColumns)
	}
}

// TestTriggerReferencingBothTables guards RFC Section 7.9 (audit item #2):
// REFERENCING OLD TABLE AS ... NEW TABLE AS ... was a hard parse error
// before this fix — internal/blockparser had no handling for it at all.
func TestTriggerReferencingBothTables(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT OR UPDATE OR DELETE
			REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
			FOR EACH STATEMENT
			EXECUTE FUNCTION audit_table_changes();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if tr.OldTransitionName == nil || *tr.OldTransitionName != "old_rows" {
		t.Errorf("OldTransitionName: got %v, want old_rows", tr.OldTransitionName)
	}
	if tr.NewTransitionName == nil || *tr.NewTransitionName != "new_rows" {
		t.Errorf("NewTransitionName: got %v, want new_rows", tr.NewTransitionName)
	}
}

// TestTriggerReferencingNewTableOnly proves a single-sided REFERENCING (only
// NEW TABLE, no OLD TABLE) leaves OldTransitionName nil — real PostgreSQL
// allows either side independently (e.g. an INSERT-only audit trigger has no
// OLD rows to reference at all).
func TestTriggerReferencingNewTableOnly(t *testing.T) {
	src := `TRIGGERS {
		audit_inserts AFTER INSERT
			REFERENCING NEW TABLE AS new_rows
			FOR EACH STATEMENT
			EXECUTE FUNCTION audit_table_changes();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if tr.OldTransitionName != nil {
		t.Errorf("OldTransitionName: got %v, want nil", tr.OldTransitionName)
	}
	if tr.NewTransitionName == nil || *tr.NewTransitionName != "new_rows" {
		t.Errorf("NewTransitionName: got %v, want new_rows", tr.NewTransitionName)
	}
}

// TestTriggerDependsOnExtension is the regression guard for RFC audit item
// #75: Section 9.1's DEPENDS ON EXTENSION directive, reused verbatim for
// triggers (Section 7.9) but previously not parsed anywhere.
// TestTriggerEnableStateDisabled is the regression guard for RFC audit
// item #56: Section 7.9's trigger-enable-state, previously not parsed
// anywhere.
func TestTriggerEnableStateDisabled(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			DISABLED;
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	if got := ast.Triggers[0].EnableState; got != "DISABLED" {
		t.Errorf("EnableState: got %q, want DISABLED", got)
	}
}

func TestTriggerEnableStateReplica(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			ENABLE REPLICA;
	}`
	ast := parse(t, src)
	if got := ast.Triggers[0].EnableState; got != "ENABLE REPLICA" {
		t.Errorf("EnableState: got %q, want ENABLE REPLICA", got)
	}
}

func TestTriggerEnableStateAlways(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			ENABLE ALWAYS;
	}`
	ast := parse(t, src)
	if got := ast.Triggers[0].EnableState; got != "ENABLE ALWAYS" {
		t.Errorf("EnableState: got %q, want ENABLE ALWAYS", got)
	}
}

// TestTriggerEnableStateOmittedIsEnabled proves omitting the clause
// entirely leaves EnableState "" (ENABLED, PostgreSQL's own default) —
// the overwhelmingly common case.
func TestTriggerEnableStateOmittedIsEnabled(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes();
	}`
	ast := parse(t, src)
	if got := ast.Triggers[0].EnableState; got != "" {
		t.Errorf("EnableState: got %q, want empty (ENABLED)", got)
	}
}

func TestTriggerEnableStateThenDependsOnExtension(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			ENABLE REPLICA
			DEPENDS ON EXTENSION my_audit_ext;
	}`
	ast := parse(t, src)
	tr := ast.Triggers[0]
	if tr.EnableState != "ENABLE REPLICA" {
		t.Errorf("EnableState: got %q, want ENABLE REPLICA", tr.EnableState)
	}
	if len(tr.DependsOnExtensions) != 1 || tr.DependsOnExtensions[0] != "my_audit_ext" {
		t.Errorf("DependsOnExtensions: got %v, want [my_audit_ext]", tr.DependsOnExtensions)
	}
}

func TestTriggerEnableStateInvalidErrors(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			ENABLE BOGUS;
	}`
	if err := parseErr(t, src); err == nil {
		t.Fatal("expected an error for ENABLE not followed by REPLICA or ALWAYS")
	}
}

func TestTriggerDependsOnExtension(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			DEPENDS ON EXTENSION my_audit_ext;
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if len(tr.DependsOnExtensions) != 1 || tr.DependsOnExtensions[0] != "my_audit_ext" {
		t.Errorf("DependsOnExtensions: got %v, want [my_audit_ext]", tr.DependsOnExtensions)
	}
}

// TestTriggerNoDependsOnExtensionIsNoop proves the negative form parses
// without error but contributes nothing — see
// pipeline.BlockAST.DependsOnExtensions' doc comment for why.
func TestTriggerNoDependsOnExtensionIsNoop(t *testing.T) {
	src := `TRIGGERS {
		audit_changes AFTER INSERT
			FOR EACH ROW
			EXECUTE FUNCTION audit_table_changes()
			NO DEPENDS ON EXTENSION my_audit_ext;
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	if len(ast.Triggers[0].DependsOnExtensions) != 0 {
		t.Errorf("expected no DependsOnExtensions from the negative form, got %v", ast.Triggers[0].DependsOnExtensions)
	}
}

// TestTriggerWithoutReferencing proves the transition-name fields stay nil
// when REFERENCING isn't written at all — the overwhelmingly common case,
// and the regression a naive fix could break (e.g. by requiring the clause).
func TestTriggerWithoutReferencing(t *testing.T) {
	src := `TRIGGERS {
		trg AFTER UPDATE
			FOR EACH ROW
			EXECUTE FUNCTION f();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if tr.OldTransitionName != nil || tr.NewTransitionName != nil {
		t.Errorf("expected nil transition names, got old=%v new=%v", tr.OldTransitionName, tr.NewTransitionName)
	}
}

func TestTriggerWithWhen(t *testing.T) {
	src := `TRIGGERS {
		after_email_change AFTER UPDATE
			FOR EACH ROW
			WHEN (OLD.email IS DISTINCT FROM NEW.email)
			EXECUTE FUNCTION notify_email_change();
	}`
	ast := parse(t, src)
	if len(ast.Triggers) == 0 {
		t.Fatal("expected trigger")
	}
	if ast.Triggers[0].Condition == nil {
		t.Error("expected WHEN condition")
	}
}

// Mode B: the singular TRIGGER keyword precedes a single entry outside a
// plural block — same conflation-bug family as POLICY/INDEX/GRANT/REVOKE.
func TestTriggerModeBSingularKeyword(t *testing.T) {
	ast := parse(t, `TRIGGER after_insert AFTER INSERT FOR EACH ROW EXECUTE FUNCTION on_insert();`)
	if len(ast.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(ast.Triggers))
	}
	tr := ast.Triggers[0]
	if tr.Name.Name != "after_insert" {
		t.Errorf("trigger name: got %q", tr.Name.Name)
	}
	if tr.Function.Name != "on_insert" {
		t.Errorf("trigger function: got %q", tr.Function.Name)
	}
}

func TestTriggerModeAAndBCanMix(t *testing.T) {
	src := `
		TRIGGERS { t_a AFTER INSERT FOR EACH ROW EXECUTE FUNCTION f_a(); }
		TRIGGER t_b AFTER UPDATE FOR EACH ROW EXECUTE FUNCTION f_b();
	`
	ast := parse(t, src)
	if len(ast.Triggers) != 2 {
		t.Fatalf("expected 2 triggers (one per mode), got %d", len(ast.Triggers))
	}
}

// ── CONSTRAINT ────────────────────────────────────────────────────────────────

func TestConstraintNotValid(t *testing.T) {
	src := `CONSTRAINT ck_positive CHECK (amount > 0) NOT VALID;`
	ast := parse(t, src)
	if len(ast.Constraints) != 1 {
		t.Fatalf("expected 1 constraint, got %d", len(ast.Constraints))
	}
	cst := ast.Constraints[0]
	if cst.Name.Name != "ck_positive" {
		t.Errorf("constraint name: got %q", cst.Name.Name)
	}
	if !cst.NotValid {
		t.Error("expected NotValid = true")
	}
}

// TestConstraintRenamedFrom guards RFC Section 7.3's RENAMED FROM directive
// on a table constraint.
func TestConstraintRenamedFrom(t *testing.T) {
	ast := parse(t, `CONSTRAINT ck_positive CHECK (amount > 0) RENAMED FROM ck_positive_old;`)
	cst := ast.Constraints[0]
	if cst.RenamedFrom == nil || *cst.RenamedFrom != "ck_positive_old" {
		t.Fatalf("RenamedFrom: got %v, want \"ck_positive_old\"", cst.RenamedFrom)
	}
	wantExpr := "CHECK (amount > 0)"
	if cst.Expr.Text != wantExpr {
		t.Errorf("Expr.Text: got %q, want %q (RENAMED FROM should be stripped out)", cst.Expr.Text, wantExpr)
	}
}

// TestConstraintRenamedFromWithNotValid confirms RENAMED FROM and NOT VALID
// compose correctly regardless of which comes textually first in a raw,
// suffix-stripped body (RFC Section 7.3: RENAMED FROM is the LAST optional
// clause, appearing after NOT VALID).
func TestConstraintRenamedFromWithNotValid(t *testing.T) {
	ast := parse(t, `CONSTRAINT ck_positive CHECK (amount > 0) NOT VALID RENAMED FROM ck_positive_old;`)
	cst := ast.Constraints[0]
	if !cst.NotValid {
		t.Error("expected NotValid = true")
	}
	if cst.RenamedFrom == nil || *cst.RenamedFrom != "ck_positive_old" {
		t.Fatalf("RenamedFrom: got %v, want \"ck_positive_old\"", cst.RenamedFrom)
	}
	wantExpr := "CHECK (amount > 0)"
	if cst.Expr.Text != wantExpr {
		t.Errorf("Expr.Text: got %q, want %q", cst.Expr.Text, wantExpr)
	}
}

// Mode A (Section 4.8 Dual Definition Modes): CONSTRAINTS { } is the plural-block
// wrapper completing the pattern already offered for the other 7 collection
// types (INDEXES, POLICIES, TRIGGERS, GRANTS, REVOKES, PARTITIONS,
// COLUMNS) — previously CONSTRAINTS had no parser at all, only the singular
// CONSTRAINT form worked.
func TestConstraintsBlock(t *testing.T) {
	src := `CONSTRAINTS {
		ck_positive CHECK (amount > 0);
		ck_reasonable CHECK (amount < 1000000) NOT VALID;
	}`
	ast := parse(t, src)
	if len(ast.Constraints) != 2 {
		t.Fatalf("expected 2 constraints, got %d", len(ast.Constraints))
	}
	if ast.Constraints[0].Name.Name != "ck_positive" {
		t.Errorf("constraint[0] name: got %q", ast.Constraints[0].Name.Name)
	}
	if ast.Constraints[1].Name.Name != "ck_reasonable" || !ast.Constraints[1].NotValid {
		t.Errorf("constraint[1]: got %+v", ast.Constraints[1])
	}
}

func TestConstraintModeAAndBCanMix(t *testing.T) {
	src := `
		CONSTRAINTS { ck_a CHECK (a > 0); }
		CONSTRAINT ck_b CHECK (b > 0);
	`
	ast := parse(t, src)
	if len(ast.Constraints) != 2 {
		t.Fatalf("expected 2 constraints (one per mode), got %d", len(ast.Constraints))
	}
}

// ── PARTITIONS ────────────────────────────────────────────────────────────────

func TestPartitions(t *testing.T) {
	src := `PARTITIONS {
		events_2024_q1 FOR VALUES FROM ('2024-01-01') TO ('2024-04-01');
		events_2024_q2 FOR VALUES FROM ('2024-04-01') TO ('2024-07-01');
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 2 {
		t.Fatalf("expected 2 partitions, got %v", ast.Partitions)
	}
	if ast.Partitions.Partitions[0].Name.Name != "events_2024_q1" {
		t.Errorf("partition name: got %q", ast.Partitions.Partitions[0].Name.Name)
	}
}

// TestPartitionRenamedFrom guards RFC Section 7.13's new RENAMED FROM
// directive on a partition entry: the old name must be captured into
// PartitionBound.RenamedFrom without corrupting the bounds text it's
// stripped from.
func TestPartitionRenamedFrom(t *testing.T) {
	src := `PARTITIONS {
		events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01') RENAMED FROM events_2024_old;
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if p.RenamedFrom == nil || *p.RenamedFrom != "events_2024_old" {
		t.Fatalf("RenamedFrom: got %v, want \"events_2024_old\"", p.RenamedFrom)
	}
	wantBounds := "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"
	if p.Bounds.Text != wantBounds {
		t.Errorf("Bounds.Text: got %q, want %q (RENAMED FROM should be stripped out)", p.Bounds.Text, wantBounds)
	}
}

// TestPartitionAttachedFrom guards RFC Section 7.13's "ATTACHED FROM
// existing_table" form: attaches an already-existing standalone table
// instead of creating a new one — Name is set to the existing table's own
// bare name so every existing name-keyed lookup continues to work.
func TestPartitionAttachedFrom(t *testing.T) {
	src := `PARTITIONS {
		ATTACHED FROM legacy_events FOR VALUES FROM ('2023-01-01') TO ('2024-01-01');
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if p.AttachedFrom == nil || p.AttachedFrom.Name != "legacy_events" {
		t.Fatalf("AttachedFrom: got %v", p.AttachedFrom)
	}
	if p.Name.Name != "legacy_events" {
		t.Errorf("Name: got %q, want %q", p.Name.Name, "legacy_events")
	}
	wantBounds := "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"
	if p.Bounds.Text != wantBounds {
		t.Errorf("Bounds.Text: got %q, want %q", p.Bounds.Text, wantBounds)
	}
}

// TestPartitionAttachedFromDefault guards the DEFAULT-bound variant of
// ATTACHED FROM.
func TestPartitionAttachedFromDefault(t *testing.T) {
	src := `PARTITIONS {
		ATTACHED FROM legacy_default DEFAULT;
	}`
	ast := parse(t, src)
	p := ast.Partitions.Partitions[0]
	if p.AttachedFrom == nil || p.AttachedFrom.Name != "legacy_default" {
		t.Fatalf("AttachedFrom: got %v", p.AttachedFrom)
	}
	if p.Bounds.Text != "DEFAULT" {
		t.Errorf("Bounds.Text: got %q, want %q", p.Bounds.Text, "DEFAULT")
	}
}

// TestPartitionAttachedFromSchemaQualified guards a schema-qualified
// existing-table reference.
func TestPartitionAttachedFromSchemaQualified(t *testing.T) {
	src := `PARTITIONS {
		ATTACHED FROM archive.legacy_events FOR VALUES FROM ('2023-01-01') TO ('2024-01-01');
	}`
	ast := parse(t, src)
	p := ast.Partitions.Partitions[0]
	if p.AttachedFrom == nil || p.AttachedFrom.Schema != "archive" || p.AttachedFrom.Name != "legacy_events" {
		t.Fatalf("AttachedFrom: got %v", p.AttachedFrom)
	}
}

// TestTableDetachedFrom guards RFC Section 7.13's DETACHED FROM block
// directive — the symmetric counterpart to ATTACHED FROM, written on a
// standalone TABLE declaration.
func TestTableDetachedFrom(t *testing.T) {
	ast := parse(t, `DETACHED FROM events;`)
	if ast.DetachedFrom == nil {
		t.Fatal("expected DetachedFrom to be set")
	}
	if ast.DetachedFrom.Table.Name != "events" {
		t.Errorf("Table.Name: got %q, want %q", ast.DetachedFrom.Table.Name, "events")
	}
	if ast.DetachedFrom.Concurrently {
		t.Error("expected Concurrently = false")
	}
}

// TestTableDetachedFromConcurrently guards the CONCURRENTLY modifier.
func TestTableDetachedFromConcurrently(t *testing.T) {
	ast := parse(t, `DETACHED FROM events CONCURRENTLY;`)
	if ast.DetachedFrom == nil || !ast.DetachedFrom.Concurrently {
		t.Fatalf("DetachedFrom: got %v", ast.DetachedFrom)
	}
}

// TestPartitionRenamedFromQuotedIdent confirms a double-quoted old name
// (e.g. containing a reserved word or mixed case) is unquoted correctly.
func TestPartitionRenamedFromQuotedIdent(t *testing.T) {
	src := `PARTITION events_2024 DEFAULT RENAMED FROM "Events 2024 Old";`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if p.RenamedFrom == nil || *p.RenamedFrom != "Events 2024 Old" {
		t.Fatalf("RenamedFrom: got %v, want \"Events 2024 Old\"", p.RenamedFrom)
	}
	if p.Bounds.Text != "DEFAULT" {
		t.Errorf("Bounds.Text: got %q, want \"DEFAULT\"", p.Bounds.Text)
	}
}

// TestPartitionRenamedFromWithSubPartitioning confirms RENAMED FROM and the
// nested sub-partitioning suffix compose correctly (RENAMED FROM precedes
// PARTITION BY in the grammar) without either clobbering the other.
func TestPartitionRenamedFromWithSubPartitioning(t *testing.T) {
	src := `PARTITIONS {
		events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')
			RENAMED FROM events_2024_old
			PARTITION BY LIST (region) {
				PARTITIONS {
					events_2024_us FOR VALUES IN ('us-east');
				}
			};
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if p.RenamedFrom == nil || *p.RenamedFrom != "events_2024_old" {
		t.Fatalf("RenamedFrom: got %v, want \"events_2024_old\"", p.RenamedFrom)
	}
	wantBounds := "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"
	if p.Bounds.Text != wantBounds {
		t.Errorf("Bounds.Text: got %q, want %q", p.Bounds.Text, wantBounds)
	}
	if p.SubStrategy != "LIST" || len(p.SubPartitions) != 1 {
		t.Fatalf("expected sub-partitioning to still parse correctly, got SubStrategy=%q SubPartitions=%v", p.SubStrategy, p.SubPartitions)
	}
	if p.SubPartitions[0].Name.Name != "events_2024_us" {
		t.Errorf("sub-partition name: got %q", p.SubPartitions[0].Name.Name)
	}
}

// Mode B: the singular PARTITION keyword precedes a single entry outside a
// plural block — same conflation-bug family as POLICY/TRIGGER/INDEX/GRANT/
// REVOKE. PartitionDef wraps a slice (unlike the others, nothing else
// merges multiple PARTITIONS{} declarations), so Mode B must lazily create it
// on first use rather than assume it already exists.
func TestPartitionModeBSingularKeyword(t *testing.T) {
	ast := parse(t, `PARTITION events_2024_q1 FOR VALUES FROM ('2024-01-01') TO ('2024-04-01');`)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	if ast.Partitions.Partitions[0].Name.Name != "events_2024_q1" {
		t.Errorf("partition name: got %q", ast.Partitions.Partitions[0].Name.Name)
	}
}

// Both orderings are exercised deliberately: PartitionDef wraps a slice, and
// the PARTITIONS (plural) dispatch case originally did a bare assignment
// (ast.Partitions = ...) rather than merging — which silently discarded
// whatever a PRECEDING Mode-B PARTITION entry had already added. PARTITIONS-
// then-PARTITION happens not to trigger it (PARTITIONS runs first and
// PARTITION's lazy-init sees a non-nil ast.Partitions to append to), so only
// testing that order would have missed the bug — confirmed by a live
// integration test (TestRoundtripPolicyTriggerPartitionModeB) using the other
// order, which failed until this was fixed.
func TestPartitionModeAAndBCanMix(t *testing.T) {
	src := `
		PARTITIONS { events_2024_q1 FOR VALUES FROM ('2024-01-01') TO ('2024-04-01'); }
		PARTITION events_2024_q2 FOR VALUES FROM ('2024-04-01') TO ('2024-07-01');
	`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 2 {
		t.Fatalf("expected 2 partitions (one per mode), got %v", ast.Partitions)
	}
}

// TestPartitionLocalConstraint guards RFC Section 7.3's "DROP CONSTRAINT ...
// ONLY" gap (PostgreSQL 18+): a leaf partition (no PARTITION BY at all) may
// now carry its own trailing { } body declaring constraints independently
// of the parent, using the same singular CONSTRAINT directive a table's own
// block accepts.
func TestPartitionLocalConstraint(t *testing.T) {
	src := `PARTITIONS {
		events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01') {
			CONSTRAINT ck_amount CHECK (amount > 0);
		};
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if len(p.Constraints) != 1 {
		t.Fatalf("expected 1 local constraint, got %v", p.Constraints)
	}
	cst := p.Constraints[0]
	if cst.Name.Name != "ck_amount" {
		t.Errorf("Name: got %q, want %q", cst.Name.Name, "ck_amount")
	}
	if cst.Expr.Text != "CHECK (amount > 0)" {
		t.Errorf("Expr.Text: got %q", cst.Expr.Text)
	}
}

// TestPartitionLocalConstraintsPluralBlock guards the Mode A (CONSTRAINTS
// { }) wrapped form inside a partition's own body, mirroring the table-level
// Dual Definition Mode.
func TestPartitionLocalConstraintsPluralBlock(t *testing.T) {
	src := `PARTITIONS {
		events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01') {
			CONSTRAINTS {
				ck_amount CHECK (amount > 0);
				ck_qty CHECK (qty >= 0) NOT VALID;
			}
		};
	}`
	ast := parse(t, src)
	p := ast.Partitions.Partitions[0]
	if len(p.Constraints) != 2 {
		t.Fatalf("expected 2 local constraints, got %v", p.Constraints)
	}
	if p.Constraints[1].Name.Name != "ck_qty" || !p.Constraints[1].NotValid {
		t.Errorf("second constraint: got %+v", p.Constraints[1])
	}
}

// TestPartitionLocalConstraintWithSubPartitioning confirms a partition's own
// local constraints and its PARTITION BY sub-partitioning suffix can coexist
// in the same trailing { } body, in either order.
func TestPartitionLocalConstraintWithSubPartitioning(t *testing.T) {
	src := `PARTITIONS {
		events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')
			PARTITION BY LIST (region) {
				PARTITIONS {
					events_2024_us FOR VALUES IN ('us-east');
				}
				CONSTRAINT ck_amount CHECK (amount > 0);
			};
	}`
	ast := parse(t, src)
	p := ast.Partitions.Partitions[0]
	if p.SubStrategy != "LIST" || len(p.SubPartitions) != 1 {
		t.Fatalf("expected sub-partitioning to still parse correctly, got %+v", p)
	}
	if len(p.Constraints) != 1 || p.Constraints[0].Name.Name != "ck_amount" {
		t.Fatalf("expected 1 local constraint, got %v", p.Constraints)
	}
}

// TestPartitionPartitionsWithoutPartitionByRejected guards against a
// PARTITIONS { } sub-block appearing without a preceding PARTITION BY clause
// on the same entry — meaningless (there is no sub-strategy to attach
// children to) and previously would have silently misparsed rather than
// erroring clearly.
func TestPartitionPartitionsWithoutPartitionByRejected(t *testing.T) {
	src := `PARTITIONS {
		events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01') {
			PARTITIONS {
				events_2024_us FOR VALUES IN ('us-east');
			}
		};
	}`
	err := parseErr(t, src)
	if err == nil {
		t.Fatal("expected an error for PARTITIONS without a preceding PARTITION BY clause")
	}
}

func TestPartitionModeBThenModeACanMix(t *testing.T) {
	src := `
		PARTITION events_2024_q1 FOR VALUES FROM ('2024-01-01') TO ('2024-04-01');
		PARTITIONS { events_2024_q2 FOR VALUES FROM ('2024-04-01') TO ('2024-07-01'); }
	`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 2 {
		t.Fatalf("expected 2 partitions (one per mode), got %v — PARTITIONS must merge into, not overwrite, a preceding Mode-B PARTITION", ast.Partitions)
	}
}

// Sub-partitioning (RFC Section 7.13): a partition entry may itself carry a nested
// PARTITION BY clause and PARTITIONS { } block.
func TestPartitionSubPartitioned(t *testing.T) {
	src := `PARTITIONS {
		metrics_2025 FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
		metrics_2026 FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')
			PARTITION BY LIST (channel) {
				PARTITIONS {
					metrics_2026_web   FOR VALUES IN ('web');
					metrics_2026_other FOR VALUES IN ('mobile', 'api');
				}
			};
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 2 {
		t.Fatalf("expected 2 top-level partitions, got %v", ast.Partitions)
	}

	leaf := ast.Partitions.Partitions[0]
	if leaf.Name.Name != "metrics_2025" {
		t.Errorf("partition[0] name: got %q", leaf.Name.Name)
	}
	if leaf.SubStrategy != "" || leaf.SubPartitions != nil {
		t.Errorf("partition[0] should have no sub-partitioning, got %+v", leaf)
	}
	wantBounds := "FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')"
	if leaf.Bounds.Text != wantBounds {
		t.Errorf("partition[0] bounds: got %q, want %q", leaf.Bounds.Text, wantBounds)
	}

	sub := ast.Partitions.Partitions[1]
	if sub.Name.Name != "metrics_2026" {
		t.Errorf("partition[1] name: got %q", sub.Name.Name)
	}
	wantBounds = "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')"
	if sub.Bounds.Text != wantBounds {
		t.Errorf("partition[1] bounds: got %q, want %q", sub.Bounds.Text, wantBounds)
	}
	if sub.SubStrategy != "LIST" {
		t.Fatalf("partition[1] SubStrategy: got %q, want LIST", sub.SubStrategy)
	}
	if len(sub.SubColumns) != 1 || sub.SubColumns[0] != "channel" {
		t.Fatalf("partition[1] SubColumns: got %v", sub.SubColumns)
	}
	if len(sub.SubPartitions) != 2 {
		t.Fatalf("partition[1] SubPartitions: got %d, want 2", len(sub.SubPartitions))
	}
	if sub.SubPartitions[0].Name.Name != "metrics_2026_web" {
		t.Errorf("sub-partition[0] name: got %q", sub.SubPartitions[0].Name.Name)
	}
	if sub.SubPartitions[1].Bounds.Text != "FOR VALUES IN ('mobile', 'api')" {
		t.Errorf("sub-partition[1] bounds: got %q", sub.SubPartitions[1].Bounds.Text)
	}
}

// A partition's PARTITION BY may itself be further sub-partitioned (depth > 2).
func TestPartitionSubPartitionedRecursive(t *testing.T) {
	src := `PARTITIONS {
		p1 FOR VALUES FROM (0) TO (100)
			PARTITION BY RANGE (b) {
				PARTITIONS {
					p1_1 FOR VALUES FROM (0) TO (50)
						PARTITION BY RANGE (c) {
							PARTITIONS {
								p1_1_1 FOR VALUES FROM (0) TO (25);
							}
						};
				}
			};
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 top-level partition, got %v", ast.Partitions)
	}
	p1 := ast.Partitions.Partitions[0]
	if p1.SubStrategy != "RANGE" || len(p1.SubPartitions) != 1 {
		t.Fatalf("p1: got %+v", p1)
	}
	p11 := p1.SubPartitions[0]
	if p11.Name.Name != "p1_1" || p11.SubStrategy != "RANGE" || len(p11.SubPartitions) != 1 {
		t.Fatalf("p1_1: got %+v", p11)
	}
	p111 := p11.SubPartitions[0]
	if p111.Name.Name != "p1_1_1" || p111.SubStrategy != "" {
		t.Fatalf("p1_1_1: got %+v", p111)
	}
}

// Mode B (singular PARTITION keyword) must also support a trailing
// sub-partition clause, not just Mode A inside a PARTITIONS { } block.
func TestPartitionModeBSubPartitioned(t *testing.T) {
	src := `PARTITION metrics_2026 FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')
		PARTITION BY HASH (id) {
			PARTITIONS {
				metrics_2026_0 FOR VALUES WITH (modulus 2, remainder 0);
				metrics_2026_1 FOR VALUES WITH (modulus 2, remainder 1);
			}
		};`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if p.SubStrategy != "HASH" || len(p.SubColumns) != 1 || p.SubColumns[0] != "id" {
		t.Fatalf("got %+v", p)
	}
	if len(p.SubPartitions) != 2 {
		t.Fatalf("expected 2 sub-partitions, got %d", len(p.SubPartitions))
	}
}

// TestPartitionForeign guards RFC Section 7.13's "FOREIGN partition-name ...
// SERVER server_name [OPTIONS (...)]" form — makes a partition a foreign
// table instead of a regular one.
func TestPartitionForeign(t *testing.T) {
	src := `PARTITIONS {
		FOREIGN events_archive DEFAULT SERVER archive_server OPTIONS (table_name 'events_archive');
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if !p.Foreign {
		t.Fatal("expected Foreign = true")
	}
	if p.Server == nil || p.Server.Name != "archive_server" {
		t.Fatalf("Server: got %v", p.Server)
	}
	if len(p.Options) != 1 || p.Options[0].Key != "table_name" || p.Options[0].Value != "events_archive" {
		t.Fatalf("Options: got %+v", p.Options)
	}
	if p.Bounds.Text != "DEFAULT" {
		t.Errorf("Bounds.Text: got %q, want %q (SERVER/OPTIONS should be stripped out)", p.Bounds.Text, "DEFAULT")
	}
}

// TestPartitionForeignNoOptions guards the OPTIONS-omitted case (OPTIONS is
// optional; SERVER alone is enough).
func TestPartitionForeignNoOptions(t *testing.T) {
	src := `PARTITIONS {
		FOREIGN events_us FOR VALUES IN ('us-east', 'us-west') SERVER srv;
	}`
	ast := parse(t, src)
	if ast.Partitions == nil || len(ast.Partitions.Partitions) != 1 {
		t.Fatalf("expected 1 partition, got %v", ast.Partitions)
	}
	p := ast.Partitions.Partitions[0]
	if !p.Foreign || p.Server == nil || p.Server.Name != "srv" {
		t.Fatalf("got %+v", p)
	}
	if len(p.Options) != 0 {
		t.Fatalf("expected no options, got %+v", p.Options)
	}
	wantBounds := "FOR VALUES IN ('us-east', 'us-west')"
	if p.Bounds.Text != wantBounds {
		t.Errorf("Bounds.Text: got %q, want %q", p.Bounds.Text, wantBounds)
	}
}

// TestPartitionForeignMultipleOptions guards a multi-entry OPTIONS list,
// including a value containing a comma and parens — only a real tokenizer
// (not a naive split on ',') can parse this correctly.
func TestPartitionForeignMultipleOptions(t *testing.T) {
	src := `PARTITIONS {
		FOREIGN remote_events DEFAULT SERVER srv OPTIONS (table_name 'events', schema_name 'public', query 'SELECT a, b FROM (t)');
	}`
	ast := parse(t, src)
	p := ast.Partitions.Partitions[0]
	want := []pipeline.StorageParam{
		{Key: "table_name", Value: "events"},
		{Key: "schema_name", Value: "public"},
		{Key: "query", Value: "SELECT a, b FROM (t)"},
	}
	if len(p.Options) != len(want) {
		t.Fatalf("Options: got %+v, want %+v", p.Options, want)
	}
	for i, o := range want {
		if p.Options[i] != o {
			t.Errorf("Options[%d]: got %+v, want %+v", i, p.Options[i], o)
		}
	}
}

// TestPartitionForeignMissingServer guards the mandatory-SERVER validation:
// real PostgreSQL rejects CREATE FOREIGN TABLE with no SERVER clause at all
// ("syntax error at end of input", confirmed live) — DPG must reject the
// same declaration at parse time rather than emitting invalid SQL later.
func TestPartitionForeignMissingServer(t *testing.T) {
	src := `PARTITIONS {
		FOREIGN events_archive DEFAULT;
	}`
	err := parseErr(t, src)
	if err == nil {
		t.Fatal("expected an error for FOREIGN partition with no SERVER clause")
	}
}

// TestPartitionForeignRejectsSubPartitioning guards a real PostgreSQL
// restriction confirmed live: a foreign table can never itself be
// PARTITION BY'd ("syntax error at or near PARTITION"). DPG must reject this
// combination at parse time.
func TestPartitionForeignRejectsSubPartitioning(t *testing.T) {
	src := `PARTITIONS {
		FOREIGN events_archive DEFAULT SERVER srv
			PARTITION BY LIST (region) {
				PARTITIONS {
					x FOR VALUES IN ('a');
				}
			};
	}`
	err := parseErr(t, src)
	if err == nil {
		t.Fatal("expected an error for a FOREIGN partition declaring its own PARTITION BY")
	}
}

// TestPartitionForeignRejectsAttachedFrom guards the FOREIGN+ATTACHED FROM
// combination: ATTACHED FROM references an already-existing table whose own
// declaration already determines whether it's foreign, so combining it with
// FOREIGN here is rejected rather than silently ignored.
func TestPartitionForeignRejectsAttachedFrom(t *testing.T) {
	src := `PARTITIONS {
		FOREIGN ATTACHED FROM legacy_events FOR VALUES FROM ('2023-01-01') TO ('2024-01-01');
	}`
	err := parseErr(t, src)
	if err == nil {
		t.Fatal("expected an error for FOREIGN combined with ATTACHED FROM")
	}
}

// ── TEXT SEARCH MAPPING ───────────────────────────────────────────────────────

// TestTSMappingSingleDictionary guards the common case: one dictionary per
// mapping entry.
func TestTSMappingSingleDictionary(t *testing.T) {
	ast := parse(t, `MAPPING FOR word, hword, hword_part WITH english_stem;`)
	if len(ast.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(ast.Mappings))
	}
	m := ast.Mappings[0]
	if len(m.TokenTypes) != 3 || m.TokenTypes[0] != "word" || m.TokenTypes[2] != "hword_part" {
		t.Errorf("TokenTypes: got %v", m.TokenTypes)
	}
	if len(m.Dictionaries) != 1 || m.Dictionaries[0].Name != "english_stem" {
		t.Errorf("Dictionaries: got %v", m.Dictionaries)
	}
}

// TestTSMappingDictionaryFallbackChain guards a real PG feature (confirmed
// real, not a DPG invention): "WITH dict1, dict2, ..." is a fallback chain —
// dictionaries are tried in order until one recognizes the token. The RFC's
// own worked example (Section 12.1) uses "WITH unaccent, english_stem". Before this,
// the parser only ever read a single identifier after WITH, then immediately
// expected ';' — so a genuine, RFC-documented multi-dictionary mapping
// failed to parse at all ("expected ';' after directive, got ','"), found
// live-testing a demo project.
func TestTSMappingDictionaryFallbackChain(t *testing.T) {
	ast := parse(t, `MAPPING FOR hword, hword_part, word WITH unaccent, english_stem;`)
	if len(ast.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(ast.Mappings))
	}
	m := ast.Mappings[0]
	if len(m.Dictionaries) != 2 || m.Dictionaries[0].Name != "unaccent" || m.Dictionaries[1].Name != "english_stem" {
		t.Fatalf("Dictionaries: got %v, want [unaccent english_stem]", m.Dictionaries)
	}
}

// TestTSMappingMultipleEntries guards MULTIPLE MAPPING FOR directives
// accumulating into ast.Mappings (Mode A: one directive per token-type
// grouping, matching how a real config typically declares distinct mappings
// for different token classes).
func TestTSMappingMultipleEntries(t *testing.T) {
	ast := parse(t, `
		MAPPING FOR word WITH english_stem;
		MAPPING FOR asciiword WITH simple;
	`)
	if len(ast.Mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(ast.Mappings))
	}
	if ast.Mappings[0].TokenTypes[0] != "word" || ast.Mappings[1].TokenTypes[0] != "asciiword" {
		t.Errorf("mappings out of order or wrong: %+v", ast.Mappings)
	}
}

// ── MIGRATE REMOVE ────────────────────────────────────────────────────────────

func TestMigrateRemove(t *testing.T) {
	src := `MIGRATE REMOVE ('cancelled') {
		UPDATE orders SET status = 'closed' WHERE status = 'cancelled';
	}`
	ast := parse(t, src)
	if ast.MigrateRemove == nil {
		t.Fatal("expected MigrateRemove")
	}
	if ast.MigrateRemove.SQL.Text == "" {
		t.Error("expected non-empty SQL in MigrateRemove")
	}
}

// ── VALUE ... RENAMED FROM ──────────────────────────────────────────────────

// TestEnumRenameValue guards RFC Section 5.1.1's "VALUE 'new' RENAMED FROM
// 'old'" enum-block directive.
func TestEnumRenameValue(t *testing.T) {
	ast := parse(t, `VALUE 'awaiting' RENAMED FROM 'pending';`)
	if len(ast.EnumValueRenames) != 1 {
		t.Fatalf("expected 1 rename, got %d", len(ast.EnumValueRenames))
	}
	r := ast.EnumValueRenames[0]
	if r.From != "pending" || r.To != "awaiting" {
		t.Errorf("rename: got From=%q To=%q, want From=%q To=%q", r.From, r.To, "pending", "awaiting")
	}
}

// TestEnumRenameValueMultiple guards that more than one VALUE ... RENAMED
// FROM directive in the same block accumulates rather than overwriting.
func TestEnumRenameValueMultiple(t *testing.T) {
	src := `VALUE 'awaiting' RENAMED FROM 'pending';
	VALUE 'dispatched' RENAMED FROM 'shipped';`
	ast := parse(t, src)
	if len(ast.EnumValueRenames) != 2 {
		t.Fatalf("expected 2 renames, got %d", len(ast.EnumValueRenames))
	}
	if ast.EnumValueRenames[0].From != "pending" || ast.EnumValueRenames[1].From != "shipped" {
		t.Errorf("unexpected rename order/content: %+v", ast.EnumValueRenames)
	}
}

// TestEnumRenameValueOldSyntaxRejected guards that the former RENAME VALUE
// spelling errors with a pointer at the current one.
func TestEnumRenameValueOldSyntaxRejected(t *testing.T) {
	err := parseErr(t, `RENAME VALUE 'pending' TO 'awaiting';`)
	if err == nil || !strings.Contains(err.Error(), "VALUE 'new' RENAMED FROM 'old'") {
		t.Fatalf("expected error naming the new syntax, got %v", err)
	}
}

// ── combined ──────────────────────────────────────────────────────────────────

func TestFullTableBlock(t *testing.T) {
	src := `
		COMMENT 'Primary identity store';
		OWNER   "app_role";

		COLUMN email {
			COMMENT    'Verified email address';
			STATISTICS 300;
		}

		INDEXES {
			idx_email  (email);
			idx_status (status) WHERE (status != 'deleted');
		}

		ENABLE ROW LEVEL SECURITY;

		POLICIES {
			view_self FOR SELECT USING (id = auth.uid());
		}

		TRIGGERS {
			after_insert AFTER INSERT
				FOR EACH ROW
				EXECUTE FUNCTION on_insert();
		}

		GRANTS {
			SELECT, INSERT, UPDATE TO app_service;
			SELECT                 TO app_readonly;
		}

		REVOKES {
			ALL PRIVILEGES FROM PUBLIC;
		}
	`
	ast := parse(t, src)

	if ast.Comment == nil || ast.Comment.Value != "Primary identity store" {
		t.Errorf("Comment: got %v", ast.Comment)
	}
	if ast.Owner == nil || ast.Owner.Name != "app_role" {
		t.Errorf("Owner: got %v", ast.Owner)
	}
	if len(ast.Columns) != 1 {
		t.Errorf("Columns: got %d", len(ast.Columns))
	}
	if len(ast.Indices) != 2 {
		t.Errorf("Indices: got %d", len(ast.Indices))
	}
	if !ast.EnableRLS {
		t.Error("expected EnableRLS")
	}
	if len(ast.Policies) != 1 {
		t.Errorf("Policies: got %d", len(ast.Policies))
	}
	if len(ast.Triggers) != 1 {
		t.Errorf("Triggers: got %d", len(ast.Triggers))
	}
	if len(ast.Grants) != 2 {
		t.Errorf("Grants: got %d", len(ast.Grants))
	}
	if len(ast.Revocations) != 1 {
		t.Errorf("Revocations: got %d", len(ast.Revocations))
	}
}

// ── PREFERRED JSON FORMAT ─────────────────────────────────────────────────────

func TestPreferredJsonFormatJsonb(t *testing.T) {
	p := blockparser.New()
	ast, err := p.Parse(pipeline.KindVirtualType, `PREFERRED JSON FORMAT jsonb;`, zeroPos)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if ast.PreferredJsonFormat != "jsonb" {
		t.Errorf("PreferredJsonFormat: got %q, want %q", ast.PreferredJsonFormat, "jsonb")
	}
}

func TestPreferredJsonFormatJson(t *testing.T) {
	p := blockparser.New()
	ast, err := p.Parse(pipeline.KindVirtualType, `PREFERRED JSON FORMAT json;`, zeroPos)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if ast.PreferredJsonFormat != "json" {
		t.Errorf("PreferredJsonFormat: got %q, want %q", ast.PreferredJsonFormat, "json")
	}
}

func TestPreferredJsonFormatWithComment(t *testing.T) {
	p := blockparser.New()
	ast, err := p.Parse(pipeline.KindVirtualType,
		`COMMENT 'some type'; PREFERRED JSON FORMAT json;`, zeroPos)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if ast.PreferredJsonFormat != "json" {
		t.Errorf("PreferredJsonFormat: got %q, want json", ast.PreferredJsonFormat)
	}
	if ast.Comment == nil || ast.Comment.Value != "some type" {
		t.Errorf("Comment: got %v", ast.Comment)
	}
}

func TestPreferredJsonFormatInvalidValue(t *testing.T) {
	p := blockparser.New()
	_, err := p.Parse(pipeline.KindVirtualType, `PREFERRED JSON FORMAT text;`, zeroPos)
	if err == nil {
		t.Error("expected error for invalid format value, got nil")
	}
}

func TestPreferredJsonFormatMissingKeywords(t *testing.T) {
	p := blockparser.New()
	_, err := p.Parse(pipeline.KindVirtualType, `PREFERRED jsonb;`, zeroPos)
	if err == nil {
		t.Error("expected error for missing JSON keyword, got nil")
	}
}

// ── NAME MAP / NAME MAPS ──────────────────────────────────────────────────────

func TestNameMapDefaultImplicit(t *testing.T) {
	ast := parse(t, `NAME MAP TO LOWER_SNAKE_CASE;`)
	if len(ast.NameMaps) != 1 {
		t.Fatalf("expected 1 NameMap entry, got %d", len(ast.NameMaps))
	}
	e := ast.NameMaps[0]
	if e.Tool != "default" {
		t.Errorf("Tool: got %q, want %q", e.Tool, "default")
	}
	if e.Value != "LOWER_SNAKE_CASE" {
		t.Errorf("Value: got %q, want %q", e.Value, "LOWER_SNAKE_CASE")
	}
	if e.IsLiteral {
		t.Error("expected IsLiteral=false for a rule")
	}
}

func TestNameMapDefaultExplicit(t *testing.T) {
	ast := parse(t, `NAME MAP default TO UPPER_CAMEL_CASE;`)
	if len(ast.NameMaps) != 1 {
		t.Fatalf("expected 1 NameMap entry, got %d", len(ast.NameMaps))
	}
	e := ast.NameMaps[0]
	if e.Tool != "default" {
		t.Errorf("Tool: got %q, want %q", e.Tool, "default")
	}
	if e.Value != "UPPER_CAMEL_CASE" {
		t.Errorf("Value: got %q, want %q", e.Value, "UPPER_CAMEL_CASE")
	}
}

func TestNameMapToolWithRule(t *testing.T) {
	ast := parse(t, `NAME MAP prisma TO LOWER_CAMEL_CASE;`)
	if len(ast.NameMaps) != 1 {
		t.Fatalf("expected 1 NameMap entry, got %d", len(ast.NameMaps))
	}
	e := ast.NameMaps[0]
	if e.Tool != "prisma" {
		t.Errorf("Tool: got %q, want %q", e.Tool, "prisma")
	}
	if e.Value != "LOWER_CAMEL_CASE" {
		t.Errorf("Value: got %q, want %q", e.Value, "LOWER_CAMEL_CASE")
	}
	if e.IsLiteral {
		t.Error("expected IsLiteral=false for a rule")
	}
}

func TestNameMapToolWithLiteralName(t *testing.T) {
	ast := parse(t, `NAME MAP prisma TO "ProductVariant";`)
	if len(ast.NameMaps) != 1 {
		t.Fatalf("expected 1 NameMap entry, got %d", len(ast.NameMaps))
	}
	e := ast.NameMaps[0]
	if e.Tool != "prisma" {
		t.Errorf("Tool: got %q, want %q", e.Tool, "prisma")
	}
	if e.Value != "ProductVariant" {
		t.Errorf("Value: got %q, want %q", e.Value, "ProductVariant")
	}
	if !e.IsLiteral {
		t.Error("expected IsLiteral=true for a double-quoted name")
	}
}

func TestNameMapsBlock(t *testing.T) {
	src := `NAME MAPS {
		default TO LOWER_SNAKE_CASE;
		prisma  TO "Order";
		drizzle TO LOWER_CAMEL_CASE;
	}`
	ast := parse(t, src)
	if len(ast.NameMaps) != 3 {
		t.Fatalf("expected 3 NameMap entries, got %d", len(ast.NameMaps))
	}
	if ast.NameMaps[0].Tool != "default" || ast.NameMaps[0].Value != "LOWER_SNAKE_CASE" || ast.NameMaps[0].IsLiteral {
		t.Errorf("entry[0]: %+v", ast.NameMaps[0])
	}
	if ast.NameMaps[1].Tool != "prisma" || ast.NameMaps[1].Value != "Order" || !ast.NameMaps[1].IsLiteral {
		t.Errorf("entry[1]: %+v", ast.NameMaps[1])
	}
	if ast.NameMaps[2].Tool != "drizzle" || ast.NameMaps[2].Value != "LOWER_CAMEL_CASE" || ast.NameMaps[2].IsLiteral {
		t.Errorf("entry[2]: %+v", ast.NameMaps[2])
	}
}

func TestNameMapMultipleSingular(t *testing.T) {
	src := `NAME MAP default TO LOWER_SNAKE_CASE;
		NAME MAP prisma TO "User";`
	ast := parse(t, src)
	if len(ast.NameMaps) != 2 {
		t.Fatalf("expected 2 NameMap entries, got %d", len(ast.NameMaps))
	}
}

func TestNameMapDuplicateToolLastWins(t *testing.T) {
	src := `NAME MAP prisma TO "First";
		NAME MAP prisma TO "Second";`
	ast := parse(t, src)
	if len(ast.NameMaps) != 1 {
		t.Fatalf("expected duplicate tool entries collapsed to 1, got %d: %+v", len(ast.NameMaps), ast.NameMaps)
	}
	if ast.NameMaps[0].Value != "Second" {
		t.Errorf("expected last entry to win (Value=Second), got %+v", ast.NameMaps[0])
	}
	if len(ast.NameMapWarnings) != 1 {
		t.Fatalf("expected 1 DPG-E031 warning, got %d: %+v", len(ast.NameMapWarnings), ast.NameMapWarnings)
	}
	w := ast.NameMapWarnings[0]
	if w.Rule != "duplicate-namemap-tool" || !strings.Contains(w.Message, "DPG-E031") || !strings.Contains(w.Message, "prisma") {
		t.Errorf("unexpected warning: %+v", w)
	}
}

func TestNameMapNoDuplicateNoWarning(t *testing.T) {
	src := `NAME MAP prisma TO "First";
		NAME MAP drizzle TO "Second";`
	ast := parse(t, src)
	if len(ast.NameMaps) != 2 {
		t.Fatalf("expected 2 distinct entries, got %d", len(ast.NameMaps))
	}
	if len(ast.NameMapWarnings) != 0 {
		t.Errorf("expected no warnings for distinct tools, got %+v", ast.NameMapWarnings)
	}
}

func TestNameMapColumnBlockDuplicateToolLastWins(t *testing.T) {
	src := `COLUMN created_at {
		NAME MAP prisma TO "First";
		NAME MAP prisma TO "Second";
	}`
	ast := parse(t, src)
	if len(ast.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(ast.Columns))
	}
	col := ast.Columns[0]
	if len(col.NameMaps) != 1 || col.NameMaps[0].Value != "Second" {
		t.Fatalf("expected last entry to win, got %+v", col.NameMaps)
	}
	if len(col.NameMapWarnings) != 1 || !strings.Contains(col.NameMapWarnings[0].Message, "DPG-E031") {
		t.Fatalf("expected 1 DPG-E031 warning on column block, got %+v", col.NameMapWarnings)
	}
}

func TestNameMapUnknownRuleErrors(t *testing.T) {
	err := parseErr(t, `NAME MAP default TO SNAKE_LOWER;`)
	if err == nil {
		t.Error("expected error for unknown rule, got nil")
	}
}

func TestNameMapColumnBlock(t *testing.T) {
	src := `COLUMN created_at {
		NAME MAP default TO LOWER_SNAKE_CASE;
		NAME MAP prisma TO "createdAt";
	}`
	ast := parse(t, src)
	if len(ast.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(ast.Columns))
	}
	col := ast.Columns[0]
	if len(col.NameMaps) != 2 {
		t.Fatalf("expected 2 column NameMap entries, got %d", len(col.NameMaps))
	}
	if col.NameMaps[0].Tool != "default" || col.NameMaps[0].Value != "LOWER_SNAKE_CASE" {
		t.Errorf("column entry[0]: %+v", col.NameMaps[0])
	}
	if col.NameMaps[1].Tool != "prisma" || col.NameMaps[1].Value != "createdAt" || !col.NameMaps[1].IsLiteral {
		t.Errorf("column entry[1]: %+v", col.NameMaps[1])
	}
}

func TestNameMapsColumnBlock(t *testing.T) {
	src := `COLUMN user_id {
		NAME MAPS {
			default TO LOWER_SNAKE_CASE;
			drizzle TO "userId";
		}
	}`
	ast := parse(t, src)
	if len(ast.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(ast.Columns))
	}
	if len(ast.Columns[0].NameMaps) != 2 {
		t.Fatalf("expected 2 column NameMap entries, got %d", len(ast.Columns[0].NameMaps))
	}
}

func TestAllRules(t *testing.T) {
	rules := []string{
		"LOWER_SNAKE_CASE", "UPPER_SNAKE_CASE", "LOWER_CAMEL_CASE", "UPPER_CAMEL_CASE",
		"LOWER_KEBAB_CASE", "UPPER_KEBAB_CASE", "TRAIN_CASE", "LOWER_CASE",
		"UPPER_CASE", "PASCAL_SNAKE_CASE",
	}
	for _, rule := range rules {
		src := "NAME MAP TO " + rule + ";"
		a := parse(t, src)
		if len(a.NameMaps) != 1 || a.NameMaps[0].Value != rule {
			t.Errorf("rule %q: got %+v", rule, a.NameMaps)
		}
	}
}

// ── registry ──────────────────────────────────────────────────────────────────

func TestRegistration(t *testing.T) {
	impl, ok := pipeline.Resolve[pipeline.BlockParser](pipeline.Default, pipeline.KeyBlockParser)
	if !ok {
		t.Fatal("BlockParser not registered; check that blockparser init() ran")
	}
	if impl == nil {
		t.Fatal("registered BlockParser is nil")
	}
}

// Index column sort order/NULLS must parse into structured fields (not be stored
// as part of the column name), and INCLUDE columns must be unquoted — matching
// key columns — so the differ doesn't double-quote them.
func TestIndexSortOrderAndInclude(t *testing.T) {
	ast := parse(t, `INDEXES { i (a DESC NULLS LAST, b) INCLUDE ("c", d); }`)
	idx := ast.Indices[0]
	if len(idx.Columns) != 2 {
		t.Fatalf("expected 2 key columns, got %d: %+v", len(idx.Columns), idx.Columns)
	}
	if idx.Columns[0].Name != "a" || idx.Columns[0].SortOrder != "DESC" || idx.Columns[0].Nulls != "LAST" {
		t.Errorf("column 0: got %+v, want a/DESC/LAST", idx.Columns[0])
	}
	if idx.Columns[1].Name != "b" || idx.Columns[1].SortOrder != "" {
		t.Errorf("column 1: got %+v, want b with no sort", idx.Columns[1])
	}
	if len(idx.Include) != 2 || idx.Include[0].Name != "c" || idx.Include[1].Name != "d" {
		t.Errorf("INCLUDE: got %+v, want unquoted [c d]", idx.Include)
	}
}

// TestIndexOnlyPrefix guards RFC Section 7.7's ON ONLY prefix keyword —
// positioned exactly where real PostgreSQL's CREATE INDEX ... ON ONLY table
// puts it, after CONCURRENTLY and before the index name.
func TestIndexOnlyPrefix(t *testing.T) {
	ast := parse(t, `INDEXES { ONLY idx_p (a); }`)
	idx := ast.Indices[0]
	if !idx.Only {
		t.Error("expected Only = true")
	}
}

func TestIndexUniqueConcurrentlyOnlyPrefixOrder(t *testing.T) {
	ast := parse(t, `INDEXES { UNIQUE CONCURRENTLY ONLY idx_p (a); }`)
	idx := ast.Indices[0]
	if !idx.Unique || !idx.Concurrently || !idx.Only {
		t.Errorf("expected Unique/Concurrently/Only all true, got %+v", idx)
	}
}

// TestIndexOpclassWithParams is the regression guard for RFC audit item
// #10: real PostgreSQL's own index_elem grammar order is "column [COLLATE
// collation] [opclass[(params)]] [ASC|DESC] [NULLS FIRST|LAST]" (confirmed
// live via pg_query.Parse — RFC Section 7.7's own ABNF lists them in the
// reverse order, which real PostgreSQL's parser rejects). The previous
// parser never recognized COLLATE or opclass at all: any entry containing
// '(' anywhere, including "doc tsvector_ops(siglen = 32)" (a column name
// plus an opclass with parameters, RFC Section 7.7's own worked example),
// was silently swallowed whole into one bogus expression column.
func TestIndexOpclassWithParams(t *testing.T) {
	ast := parse(t, `INDEXES { idx_doc USING gist (doc tsvector_ops(siglen = 32)); }`)
	idx := ast.Indices[0]
	if len(idx.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d: %+v", len(idx.Columns), idx.Columns)
	}
	col := idx.Columns[0]
	if col.Name != "doc" {
		t.Errorf("Name: got %q, want %q", col.Name, "doc")
	}
	if col.OpClass == nil || col.OpClass.Name != "tsvector_ops" {
		t.Fatalf("OpClass: got %+v", col.OpClass)
	}
	if len(col.OpClassParams) != 1 || col.OpClassParams[0].Key != "siglen" || col.OpClassParams[0].Value != "32" {
		t.Errorf("OpClassParams: got %+v", col.OpClassParams)
	}
}

// TestIndexOpclassBareNoParams guards the simpler, far more common bare-
// opclass form (no parameters) still works after the rewrite.
func TestIndexOpclassBareNoParams(t *testing.T) {
	ast := parse(t, `INDEXES { idx_a (a varchar_pattern_ops); }`)
	col := ast.Indices[0].Columns[0]
	if col.Name != "a" {
		t.Errorf("Name: got %q", col.Name)
	}
	if col.OpClass == nil || col.OpClass.Name != "varchar_pattern_ops" {
		t.Fatalf("OpClass: got %+v", col.OpClass)
	}
	if len(col.OpClassParams) != 0 {
		t.Errorf("OpClassParams: expected none, got %+v", col.OpClassParams)
	}
}

// TestIndexCollateOpclassSortOrder guards the full clause combination in
// real PostgreSQL's own fixed order: name, COLLATE, opclass, DESC, NULLS
// LAST all on one column.
func TestIndexCollateOpclassSortOrder(t *testing.T) {
	ast := parse(t, `INDEXES { idx_a (a COLLATE "en_US" text_pattern_ops DESC NULLS LAST); }`)
	col := ast.Indices[0].Columns[0]
	if col.Name != "a" {
		t.Errorf("Name: got %q", col.Name)
	}
	if col.Collation == nil || col.Collation.Name != "en_US" {
		t.Fatalf("Collation: got %+v", col.Collation)
	}
	if col.OpClass == nil || col.OpClass.Name != "text_pattern_ops" {
		t.Fatalf("OpClass: got %+v", col.OpClass)
	}
	if col.SortOrder != "DESC" || col.Nulls != "LAST" {
		t.Errorf("SortOrder/Nulls: got %q/%q", col.SortOrder, col.Nulls)
	}
}

// TestIndexExpressionColumnStillParses guards the pre-existing expression-
// column form (RFC Section 7.7's "(" expr ")") still works after the
// rewrite from suffix-stripping to left-to-right parsing — including a
// trailing DESC after the expression's own closing paren.
func TestIndexExpressionColumnStillParses(t *testing.T) {
	ast := parse(t, `INDEXES { idx_e ((lower(email)) DESC); }`)
	col := ast.Indices[0].Columns[0]
	if col.Expr == nil || col.Expr.Text != "lower(email)" {
		t.Fatalf("Expr: got %+v", col.Expr)
	}
	if col.SortOrder != "DESC" {
		t.Errorf("SortOrder: got %q, want DESC", col.SortOrder)
	}
}

// TestIndexExpressionColumnBareFunctionCall guards a bare function-call
// expression written WITHOUT the RFC-recommended extra wrapping parens
// (e.g. "lower(email)" instead of "(lower(email))") still parses as one
// expression rather than being silently misread as a column named "lower"
// followed by a bogus empty opclass — real PostgreSQL's own
// func_expr_windowless index_elem alternative allows this form directly.
func TestIndexExpressionColumnBareFunctionCall(t *testing.T) {
	ast := parse(t, `INDEXES { idx_e (lower(email) DESC); }`)
	col := ast.Indices[0].Columns[0]
	if col.Name != "" {
		t.Errorf("Name: got %q, want empty (this must parse as an expression)", col.Name)
	}
	if col.Expr == nil || col.Expr.Text != "lower(email)" {
		t.Fatalf("Expr: got %+v", col.Expr)
	}
	if col.SortOrder != "DESC" {
		t.Errorf("SortOrder: got %q, want DESC", col.SortOrder)
	}
}

// ── OPERATOR FAMILY loose members (RFC Section 14.4) ─────────────────────────────────

// TestOpFamilyMemberCommaSeparatedList guards the approved source style: a
// whole block is one comma-separated list of OPERATOR/FUNCTION items, each
// restating its own keyword (unlike real PG's ALTER ... ADD, which states
// ADD once) — no trailing ';' on the last item.
func TestOpFamilyMemberCommaSeparatedList(t *testing.T) {
	ast := parse(t, `
		OPERATOR 1 <(int4, int8),
		OPERATOR 3 =(int4, int8),
		FUNCTION 1 (int4, int8) btint48cmp(int4, int8)
	`)
	if len(ast.OpFamilyMembers) != 3 {
		t.Fatalf("expected 3 members, got %d: %+v", len(ast.OpFamilyMembers), ast.OpFamilyMembers)
	}
	m0 := ast.OpFamilyMembers[0]
	if m0.IsFunction || m0.Number != 1 || m0.Name.Name != "<" || m0.LeftType != "int4" || m0.RightType != "int8" {
		t.Errorf("member 0: got %+v", m0)
	}
	m2 := ast.OpFamilyMembers[2]
	if !m2.IsFunction || m2.Number != 1 || m2.Name.Name != "btint48cmp" ||
		len(m2.FuncArgs) != 2 || m2.FuncArgs[0] != "int4" || m2.FuncArgs[1] != "int8" {
		t.Errorf("member 2: got %+v", m2)
	}
}

// TestOpFamilyMemberSemicolonPerMember guards the alternative, also-accepted
// terminator style (';' per member, DPG's usual block-directive convention)
// parsing identically to the comma-separated form.
func TestOpFamilyMemberSemicolonPerMember(t *testing.T) {
	ast := parse(t, `
		OPERATOR 1 <(int4, int8);
		FUNCTION 1 (int4, int8) btint48cmp(int4, int8);
	`)
	if len(ast.OpFamilyMembers) != 2 {
		t.Fatalf("expected 2 members, got %d: %+v", len(ast.OpFamilyMembers), ast.OpFamilyMembers)
	}
}

func TestOpFamilyMemberForSearchDefault(t *testing.T) {
	ast := parse(t, `OPERATOR 1 <(int4, int8) FOR SEARCH`)
	if ast.OpFamilyMembers[0].OrderBy {
		t.Errorf("FOR SEARCH must not set OrderBy, got %+v", ast.OpFamilyMembers[0])
	}
}

func TestOpFamilyMemberForOrderBy(t *testing.T) {
	ast := parse(t, `OPERATOR 3 =(int4, int8) FOR ORDER BY public.my_family`)
	m := ast.OpFamilyMembers[0]
	if !m.OrderBy || m.SortFamily.Schema != "public" || m.SortFamily.Name != "my_family" {
		t.Errorf("FOR ORDER BY: got %+v", m)
	}
}

// TestOpFamilyMemberSchemaQualifiedOperator and TestOpFamilyMemberMultiCharSymbol
// guard readOperatorSymbol's two halves: an optional "schema." prefix (word
// characters, disjoint from operator characters so there's no lexing
// ambiguity) and a run of real PG operator characters of any length.
func TestOpFamilyMemberSchemaQualifiedOperator(t *testing.T) {
	ast := parse(t, `OPERATOR 1 pg_catalog.<(int4, int8)`)
	m := ast.OpFamilyMembers[0]
	if m.Name.Schema != "pg_catalog" || m.Name.Name != "<" {
		t.Errorf("got %+v, want schema=pg_catalog name=<", m.Name)
	}
}

func TestOpFamilyMemberMultiCharSymbol(t *testing.T) {
	ast := parse(t, `OPERATOR 1 @>(box, box)`)
	if ast.OpFamilyMembers[0].Name.Name != "@>" {
		t.Errorf("got %q, want @>", ast.OpFamilyMembers[0].Name.Name)
	}
}

func TestOpFamilyMemberFunctionWithoutOpTypes(t *testing.T) {
	ast := parse(t, `FUNCTION 1 btint48cmp(int4, int8)`)
	m := ast.OpFamilyMembers[0]
	if m.LeftType != "" || m.RightType != "" {
		t.Errorf("omitted op_types should leave LeftType/RightType empty at the parser layer (the IR builder resolves defaults), got %+v", m)
	}
	if len(m.FuncArgs) != 2 || m.FuncArgs[0] != "int4" || m.FuncArgs[1] != "int8" {
		t.Errorf("FuncArgs: got %v", m.FuncArgs)
	}
}

func TestOpFamilyMemberFunctionWithOpTypes(t *testing.T) {
	ast := parse(t, `FUNCTION 1 (int4, int8) btint48cmp(int4, int8)`)
	m := ast.OpFamilyMembers[0]
	if m.LeftType != "int4" || m.RightType != "int8" {
		t.Errorf("got left=%q right=%q, want int4/int8", m.LeftType, m.RightType)
	}
}

// TestOpFamilyMemberTypeModifiersSurviveParen guards readTypeListParen's
// reuse of readRawUntil's paren-depth tracking: a modifier-bearing type like
// numeric(10,2) must not have its internal comma mistaken for the type-list
// separator.
func TestOpFamilyMemberTypeModifiersSurviveParen(t *testing.T) {
	ast := parse(t, `OPERATOR 1 <(numeric(10,2), character varying(20))`)
	m := ast.OpFamilyMembers[0]
	if m.LeftType != "numeric(10,2)" || m.RightType != "character varying(20)" {
		t.Errorf("got left=%q right=%q", m.LeftType, m.RightType)
	}
}

func TestOpFamilyMemberMissingOpTypesErrors(t *testing.T) {
	if err := parseErr(t, `OPERATOR 1 <`); err == nil {
		t.Fatal("expected an error for a missing (op_type, op_type) list")
	}
}

func TestOpFamilyMemberForGarbageErrors(t *testing.T) {
	if err := parseErr(t, `OPERATOR 1 <(int4, int8) FOR GARBAGE`); err == nil {
		t.Fatal("expected an error for FOR not followed by SEARCH or ORDER BY")
	}
}

func TestOpFamilyMemberNonNumericNumberErrors(t *testing.T) {
	if err := parseErr(t, `OPERATOR x <(int4, int8)`); err == nil {
		t.Fatal("expected an error for a non-numeric strategy number")
	}
}

func TestRenamedKeywordsErrorWithReplacement(t *testing.T) {
	cases := []struct{ src, want string }{
		{`INDICES { idx_a (a); }`, "INDICES was renamed to INDEXES"},
		{`REVOCATIONS { SELECT FROM PUBLIC; }`, "REVOCATIONS was renamed to REVOKES"},
		{`REVOCATION SELECT FROM PUBLIC;`, "REVOCATION was renamed to REVOKE"},
		{`COLUMN a { REVOCATIONS { SELECT FROM PUBLIC; } }`, "REVOCATIONS was renamed to REVOKES"},
	}
	for _, c := range cases {
		err := parseErr(t, c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: expected error containing %q, got %v", c.src, c.want, err)
		}
	}
}

func TestRenamedKeywordsParse(t *testing.T) {
	ast := parse(t, `INDEXES { idx_a (a); }
REVOKES { SELECT FROM PUBLIC; }
REVOKE INSERT FROM PUBLIC;`)
	if len(ast.Indices) != 1 || len(ast.Revocations) != 2 {
		t.Fatalf("expected 1 index and 2 revocations, got %d and %d", len(ast.Indices), len(ast.Revocations))
	}
}

// TestUnquotedIdentifiersFold guards RFC Section 4.10: unquoted identifiers
// in a { } block are case-folded exactly as PostgreSQL folds them in Part 1,
// quoted ones are kept verbatim, and role keywords stay uppercase.
func TestUnquotedIdentifiersFold(t *testing.T) {
	ast := parse(t, `OWNER App_Admin;
COLUMN Email { COMMENT 'x'; }
COLUMN "MixedCase" { COMMENT 'y'; }
INDEX Idx_A (ColA, "ColB") INCLUDE (ColC, "ColD");
REVOKE SELECT FROM public, Some_Role;`)
	if ast.Owner == nil || ast.Owner.Name != "app_admin" {
		t.Errorf("owner: got %v, want app_admin", ast.Owner)
	}
	if ast.Columns[0].Name.Name != "email" || ast.Columns[1].Name.Name != "MixedCase" {
		t.Errorf("columns: got %q, %q", ast.Columns[0].Name.Name, ast.Columns[1].Name.Name)
	}
	idx := ast.Indices[0]
	if idx.Name.Name != "idx_a" || idx.Columns[0].Name != "cola" || idx.Columns[1].Name != "ColB" {
		t.Errorf("index: got name %q cols %+v", idx.Name.Name, idx.Columns)
	}
	if len(idx.Include) != 2 || idx.Include[0].Name != "colc" || idx.Include[1].Name != "ColD" {
		t.Errorf("include: got %+v", idx.Include)
	}
	roles := ast.Revocations[0].Roles
	if roles[0].Name != "PUBLIC" || roles[1].Name != "some_role" {
		t.Errorf("roles: got %+v", roles)
	}
}
