//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
)

// findOpContaining returns the first op whose SQL contains sql, or nil.
func findOpContaining(ops []pipeline.DiffOp, sql string) pipeline.DiffOp {
	for _, op := range ops {
		if strings.Contains(op.SQL(), sql) {
			return op
		}
	}
	return nil
}

// TestRoundtripCollationRefreshVersion is the live regression guard for RFC
// audit item #84: Collation's REFRESH VERSION block directive had no IR
// field, no parser wiring, and no differ handling at all — completely
// unimplemented despite being fully specified (Section 14.2). ICU is used
// (rather than a libc locale) because ICU collations always track a real,
// queryable version string (pg_collation.collversion), unlike "C"/"POSIX"
// which never do — giving something concrete to assert against. This
// proves: (a) the declared directive actually executes against a real
// database (ALTER COLLATION ... REFRESH VERSION succeeds and collversion
// stays populated with the current ICU version, not cleared or corrupted),
// (b) its mere presence keeps re-emitting the ALTER on every subsequent
// plan, and (c) removing it from source stops emitting it.
func TestRoundtripCollationRefreshVersion(t *testing.T) {
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

	v1 := `COLLATION c (PROVIDER = icu, LOCALE = 'und');`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	collVersion := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT collversion FROM pg_collation WHERE collname = 'c'`)
		if err != nil {
			t.Fatalf("query pg_collation: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("pg_collation has no row for c")
		}
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan collversion: %v", err)
		}
		return v
	}

	initialVersion := collVersion()
	if initialVersion == "" {
		t.Fatal("collation c: collversion is empty after CREATE with ICU provider — test setup is broken (ICU should always track a version)")
	}

	// Declare REFRESH VERSION and apply.
	v2 := `COLLATION c (PROVIDER = icu, LOCALE = 'und') { REFRESH VERSION; }`
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
		t.Fatalf("diff (refresh version): %v", err)
	}
	op := findOpContaining(ops, `ALTER COLLATION "public"."c" REFRESH VERSION;`)
	if op == nil {
		t.Fatalf("expected ALTER COLLATION ... REFRESH VERSION, got: %v", opsSQL(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected REFRESH VERSION to be Caution safety, got %s", op.Safety())
	}
	if op.Mode() != pipeline.ModeTransactional {
		t.Error("expected REFRESH VERSION to run inside the migration's transaction (bug #84 regressed)")
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := collVersion(); got != initialVersion {
		t.Fatalf("collation c: collversion changed from %q to %q after REFRESH VERSION against an unchanged ICU library — unexpected", initialVersion, got)
	}

	// REFRESH VERSION's mere presence must keep re-emitting the ALTER on
	// the very next plan too, even though the snapshot now reflects a
	// "successful" prior apply — there is no persisted value to compare.
	postApplySnap, _ := store.Load("test", "dpgtest")
	ops2, err := differ.Diff(desired2, postApplySnap)
	if err != nil {
		t.Fatalf("diff (still declared): %v", err)
	}
	if findOpContaining(ops2, `ALTER COLLATION "public"."c" REFRESH VERSION;`) == nil {
		t.Fatalf("expected REFRESH VERSION to still be re-emitted while declared, got: %v", opsSQL(ops2))
	}

	// Removing REFRESH VERSION from source stops emitting it.
	v3 := `COLLATION c (PROVIDER = icu, LOCALE = 'und');`
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
		t.Errorf("expected zero drift once REFRESH VERSION is removed from source, got %d ops:", len(ops3))
		for _, op := range ops3 {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}
