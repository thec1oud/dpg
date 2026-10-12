//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestRoundtripImplicitRevokeIsCaution is the regression guard for RFC audit
// item #25: an implicit revoke (a GRANT simply removed from source) was
// classified safeOp for Table/View/Function/Procedure/Schema/column-level
// grants, but cautionOp for the identical real-world event under DEFAULT
// PRIVILEGES — an under-classification, since both produce the exact same
// REVOKE statement for a role about to lose a privilege it currently holds.
// This proves the fix against a real database: the GRANT applies, removing
// it from source produces a REVOKE tagged Caution, and applying that REVOKE
// genuinely removes the live privilege (the classification fix didn't
// accidentally change the SQL itself).
func TestRoundtripImplicitRevokeIsCaution(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")

	v1 := `ROLE app_readonly NOLOGIN;

TABLE orders (id INTEGER) {
    GRANTS { SELECT TO app_readonly; }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	hasSelect := func() bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT has_table_privilege('app_readonly', 'orders', 'SELECT')`)
		if err != nil {
			t.Fatalf("query has_table_privilege: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("has_table_privilege returned no row")
		}
		var v bool
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return v
	}
	if !hasSelect() {
		t.Fatal("app_readonly should have SELECT after initial apply — test setup is broken")
	}

	v2 := `ROLE app_readonly NOLOGIN;

TABLE orders (id INTEGER);`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	prevSnap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, prevSnap)
	if err != nil {
		t.Fatalf("diff (grant removed): %v", err)
	}
	var revoke pipeline.DiffOp
	for _, op := range ops {
		if op.SQL() == `REVOKE SELECT ON TABLE "public"."orders" FROM "app_readonly";` {
			revoke = op
		}
	}
	if revoke == nil {
		t.Fatalf("expected implicit REVOKE SELECT, got: %v", opsSQL(ops))
	}
	if revoke.Safety() != pipeline.Caution {
		t.Fatalf("expected implicit revoke to be Caution, got %s — bug #25 regressed", revoke.Safety())
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if hasSelect() {
		t.Fatal("app_readonly still has SELECT after the implicit revoke was applied")
	}
}

// TestRoundtripDropPolicyAndTriggerAreCaution guards RFC Section 17.2
// against a real database: removing a restrictive policy (which widens
// access) and a trigger (whose enforcement stops) from source drops them
// live, tagged Caution.
func TestRoundtripDropPolicyAndTriggerAreCaution(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")

	v1 := `FUNCTION trg_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN NEW;
END;
$$ {}

TABLE t (id INTEGER, owner_id INTEGER) {
    POLICY p_owner FOR SELECT AS RESTRICTIVE USING (true);
    TRIGGER trg_a AFTER INSERT FOR EACH ROW EXECUTE FUNCTION trg_touch();
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	livePolicyExists := func() bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT count(*) FROM pg_policy p JOIN pg_class c ON c.oid = p.polrelid WHERE c.relname = 't' AND p.polname = 'p_owner'`)
		if err != nil {
			t.Fatalf("query pg_policy: %v", err)
		}
		defer rows.Close()
		rows.Next()
		var n int
		_ = rows.Scan(&n)
		return n > 0
	}
	liveTriggerExists := func() bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname = 'trg_a' AND NOT tgisinternal`)
		if err != nil {
			t.Fatalf("query pg_trigger: %v", err)
		}
		defer rows.Close()
		rows.Next()
		var n int
		_ = rows.Scan(&n)
		return n > 0
	}
	if !livePolicyExists() || !liveTriggerExists() {
		t.Fatal("policy/trigger missing after initial apply — test setup is broken")
	}

	v2 := `FUNCTION trg_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN NEW;
END;
$$ {}

TABLE t (id INTEGER, owner_id INTEGER);`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	prevSnap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, prevSnap)
	if err != nil {
		t.Fatalf("diff (policy+trigger removed): %v", err)
	}
	var dropPolicy, dropTrigger pipeline.DiffOp
	for _, op := range ops {
		switch op.SQL() {
		case `DROP POLICY IF EXISTS "p_owner" ON "public"."t";`:
			dropPolicy = op
		case `DROP TRIGGER IF EXISTS "trg_a" ON "public"."t";`:
			dropTrigger = op
		}
	}
	if dropPolicy == nil || dropTrigger == nil {
		t.Fatalf("expected DROP POLICY and DROP TRIGGER, got: %v", opsSQL(ops))
	}
	if dropPolicy.Safety() != pipeline.Caution {
		t.Fatalf("expected DROP of a restrictive policy to be Caution, got %s", dropPolicy.Safety())
	}
	if dropTrigger.Safety() != pipeline.Caution {
		t.Fatalf("expected DROP TRIGGER to be Caution, got %s — bug #26 regressed", dropTrigger.Safety())
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if livePolicyExists() {
		t.Fatal("p_owner still exists live after the DROP POLICY op was applied")
	}
	if liveTriggerExists() {
		t.Fatal("trg_a still exists live after the DROP TRIGGER op was applied")
	}
}
