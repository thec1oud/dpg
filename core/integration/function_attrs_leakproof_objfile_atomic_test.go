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
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

// assertNoLiveDrift re-introspects the live database, restricts the result
// to objects the store already manages (matching every other roundtrip
// test's convention in this package), and fails if a fresh diff against
// that live state produces any ops.
func assertNoLiveDrift(t *testing.T, ctx context.Context, conn *executor.PgxConn, files []string, dir string, differ pipeline.Differ, store *memStore) {
	t.Helper()
	ci := introspect.New()
	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := snap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	desired, _, err := compiler.Compile(files, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripFunctionLeakproof guards RFC audit item #25: LEAKPROOF was
// entirely absent from FuncAttrs, so it was neither applied nor diffed.
// Proves a declared LEAKPROOF actually lands in pg_proc.proleakproof, that
// removing it runs a real CREATE OR REPLACE clearing the flag back, and
// that a fresh plan against live-introspected state is a genuine no-op at
// both stages.
func TestRoundtripFunctionLeakproof(t *testing.T) {
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

	leakproofFlag := func() bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT proleakproof FROM pg_proc WHERE proname = 'f_leak'`)
		if err != nil {
			t.Fatalf("query pg_proc: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("f_leak not found in pg_proc")
		}
		var v bool
		_ = rows.Scan(&v)
		return v
	}

	v1 := `FUNCTION f_leak(x integer) RETURNS integer LANGUAGE sql LEAKPROOF AS $$ SELECT x $$ {}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if !leakproofFlag() {
		t.Fatal("expected pg_proc.proleakproof = true after declaring LEAKPROOF")
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	v2 := `FUNCTION f_leak(x integer) RETURNS integer LANGUAGE sql AS $$ SELECT x $$ {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if leakproofFlag() {
		t.Fatal("expected pg_proc.proleakproof = false after removing LEAKPROOF")
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}

// TestRoundtripFunctionObjFileLinkSymbol guards RFC audit item #27. Before
// this fix, extractFuncAttrs only ever read the first item of the "as"
// DefElem's List, silently discarding link_symbol for the 2-item
// "AS 'obj_file', 'link_symbol'" form; and introspection built Body from
// prosrc alone (LANGUAGE C's link_symbol, not its obj_file — probin was
// never even queried), so re-emitting a diff-triggered recreate would have
// pointed the function at a bare symbol name with no shared-object path at
// all. Proves a function wired to pgcrypto's own real C symbol via this
// form is genuinely callable (proving both the shared-object path and the
// symbol survived intact), and that a fresh plan is a no-op.
func TestRoundtripFunctionObjFileLinkSymbol(t *testing.T) {
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

	if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		t.Fatalf("create pgcrypto: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")

	src := `FUNCTION my_digest(data text, typ text) RETURNS bytea LANGUAGE c AS '$libdir/pgcrypto', 'pg_digest' {}`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	rows, err := conn.QueryRows(ctx, `SELECT my_digest('hello', 'sha256') = digest('hello', 'sha256')`)
	if err != nil {
		t.Fatalf("call my_digest: %v", err)
	}
	if !rows.Next() {
		t.Fatal("no result from my_digest call")
	}
	var match bool
	if err := rows.Scan(&match); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rows.Close()
	if !match {
		t.Fatal("my_digest did not produce the same result as pgcrypto's own digest — obj_file/link_symbol did not survive intact")
	}

	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}

// TestRoundtripFunctionStrictSecurityDefChanged guards a diffFunction gap
// found while auditing items #25-#28's neighboring attributes: SnapFunction
// had no Strict/SecurityDef fields at all, so an in-place STRICT/SECURITY
// DEFINER-only change (no body/other-attribute change) produced zero diff
// ops — the property change was silently never applied. Proves declaring
// STRICT and SECURITY DEFINER on an already-applied function actually flips
// pg_proc.proisstrict/prosecdef live, that removing them flips both back,
// and that a fresh plan is a genuine no-op at every stage.
func TestRoundtripFunctionStrictSecurityDefChanged(t *testing.T) {
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

	flags := func() (strict, secDef bool) {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT proisstrict, prosecdef FROM pg_proc WHERE proname = 'f_strict_secdef'`)
		if err != nil {
			t.Fatalf("query pg_proc: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("f_strict_secdef not found in pg_proc")
		}
		if err := rows.Scan(&strict, &secDef); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return strict, secDef
	}

	v1 := `FUNCTION f_strict_secdef(x integer) RETURNS integer LANGUAGE sql AS $$ SELECT x $$ {}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if strict, secDef := flags(); strict || secDef {
		t.Fatalf("expected neither flag set initially, got strict=%v secDef=%v", strict, secDef)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	v2 := `FUNCTION f_strict_secdef(x integer) RETURNS integer LANGUAGE sql STRICT SECURITY DEFINER AS $$ SELECT x $$ {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if strict, secDef := flags(); !strict || !secDef {
		t.Fatalf("expected both flags set after declaring STRICT SECURITY DEFINER, got strict=%v secDef=%v", strict, secDef)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1 again: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if strict, secDef := flags(); strict || secDef {
		t.Fatalf("expected both flags cleared after removing STRICT SECURITY DEFINER, got strict=%v secDef=%v", strict, secDef)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}

// TestRoundtripFunctionAtomicBody guards RFC audit item #28. Before this
// fix, cfs.Options carried no "as" DefElem at all for a BEGIN ATOMIC
// function (confirmed live via pg_query.Parse), so extractFuncAttrs left
// Body permanently empty — a diff-triggered CREATE OR REPLACE would have
// rewritten the function with an empty body. Proves the function is
// genuinely callable after being declared via BEGIN ATOMIC, that a second
// declaration with a real behavioral change actually applies (not masked by
// an accidental hash collision against the empty-body bug), and that a
// fresh plan is a no-op at both stages.
func TestRoundtripFunctionAtomicBody(t *testing.T) {
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

	callF := func() int {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT f_atomic(5)`)
		if err != nil {
			t.Fatalf("call f_atomic: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("no result from f_atomic call")
		}
		var v int
		_ = rows.Scan(&v)
		return v
	}

	v1 := `FUNCTION f_atomic(x integer) RETURNS integer LANGUAGE sql BEGIN ATOMIC SELECT x + 1; END {}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := callF(); got != 6 {
		t.Fatalf("f_atomic(5): got %d, want 6", got)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	v2 := `FUNCTION f_atomic(x integer) RETURNS integer LANGUAGE sql BEGIN ATOMIC SELECT x + 100; END {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := callF(); got != 105 {
		t.Fatalf("f_atomic(5) after body change: got %d, want 105 — the recreate did not actually apply", got)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
