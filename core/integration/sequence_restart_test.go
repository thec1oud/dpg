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

// TestRoundtripSequenceRestart is the live regression guard for RFC audit
// item #68: ALTER SEQUENCE ... RESTART [WITH n] had no IR field at all —
// completely unimplemented despite being fully specified in the RFC
// (Section 10). This proves: (a) a declared RESTART WITH n genuinely resets
// the live sequence's next value, (b) RESTART's mere presence keeps
// re-emitting the ALTER on every subsequent plan (there is no persisted
// "current RESTART value" to diff against, so this is not drift-detection
// like every other option — it's an unconditional, deliberate re-emission),
// and (c) removing the directive from source stops emitting it, matching
// the RFC's documented one-shot-then-remove usage pattern.
func TestRoundtripSequenceRestart(t *testing.T) {
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

	v1 := `SEQUENCE order_seq START WITH 1;`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Move the sequence's current value away from its start.
	for range 3 {
		if _, err := conn.Exec(ctx, `SELECT nextval('order_seq');`); err != nil {
			t.Fatalf("nextval: %v", err)
		}
	}
	rows, err := conn.QueryRows(ctx, `SELECT last_value FROM order_seq;`)
	if err != nil {
		t.Fatalf("query last_value: %v", err)
	}
	var lastValue int64
	rows.Next()
	if err := rows.Scan(&lastValue); err != nil {
		t.Fatalf("scan last_value: %v", err)
	}
	rows.Close()
	if lastValue != 3 {
		t.Fatalf("order_seq: last_value = %d after 3 nextval() calls, want 3 — test setup is broken", lastValue)
	}

	// Declare RESTART WITH 1000 and apply.
	v2 := `SEQUENCE order_seq START WITH 1 RESTART WITH 1000;`
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
		t.Fatalf("diff (restart): %v", err)
	}
	op := findOpContaining(ops, `ALTER SEQUENCE "public"."order_seq" RESTART WITH 1000;`)
	if op == nil {
		t.Fatalf("expected ALTER SEQUENCE ... RESTART WITH 1000, got: %v", opsSQL(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected RESTART to be Caution safety, got %s", op.Safety())
	}
	if op.Mode() != pipeline.ModeTransactional {
		t.Error("expected RESTART to run inside the migration's transaction (bug #68 regressed)")
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := nextvalOnce(t, ctx, conn, "order_seq"); got != 1000 {
		t.Fatalf("order_seq: first nextval() after RESTART WITH 1000 = %d, want 1000 — bug #68 regressed", got)
	}

	// RESTART's mere presence must keep re-emitting the ALTER on the very
	// next plan too, even though the snapshot now reflects a "successful"
	// prior apply — there is no persisted value to compare against.
	postApplySnap, _ := store.Load("test", "dpgtest")
	ops2, err := differ.Diff(desired2, postApplySnap)
	if err != nil {
		t.Fatalf("diff (still declared): %v", err)
	}
	if findOpContaining(ops2, `ALTER SEQUENCE "public"."order_seq" RESTART WITH 1000;`) == nil {
		t.Fatalf("expected RESTART to still be re-emitted while declared, got: %v", opsSQL(ops2))
	}

	// Removing RESTART from source stops emitting it (the RFC's documented
	// one-shot-then-remove workflow).
	v3 := `SEQUENCE order_seq START WITH 1;`
	if err := os.WriteFile(f, []byte(v3), 0o644); err != nil {
		t.Fatalf("write v3: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	finalSnap, _ := store.Load("test", "dpgtest")
	desired3, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v3: %v", err)
	}
	ops3, err := differ.Diff(desired3, finalSnap)
	if err != nil {
		t.Fatalf("diff (removed): %v", err)
	}
	if len(ops3) != 0 {
		t.Errorf("expected zero drift once RESTART is removed from source, got %d ops:", len(ops3))
		for _, op := range ops3 {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

func nextvalOnce(t *testing.T, ctx context.Context, conn *executor.PgxConn, seq string) int64 {
	t.Helper()
	rows, err := conn.QueryRows(ctx, "SELECT nextval('"+seq+"');")
	if err != nil {
		t.Fatalf("nextval: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("nextval returned no row")
	}
	var v int64
	if err := rows.Scan(&v); err != nil {
		t.Fatalf("scan nextval: %v", err)
	}
	return v
}
