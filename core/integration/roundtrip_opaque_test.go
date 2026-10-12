//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

// assertOpaqueRoundtrip compiles an inline .dpg fixture, applies it, introspects
// the live catalog, and asserts that re-diffing the desired IR against the
// introspected state yields zero drift. It targets the reliable-tier opaque
// objects, whose reconstructed bodies must deparse identically to the compiler's.
func assertOpaqueRoundtrip(t *testing.T, schema string) {
	t.Helper()
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(schema), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

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

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
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

func TestRoundtripPublication(t *testing.T) {
	assertOpaqueRoundtrip(t, `PUBLICATION my_pub FOR ALL TABLES;`)
}

func TestRoundtripForeignInfra(t *testing.T) {
	assertOpaqueRoundtrip(t, `FOREIGN DATA WRAPPER dummy_fdw;
SERVER dummy_srv FOREIGN DATA WRAPPER dummy_fdw;
USER MAPPING FOR PUBLIC SERVER dummy_srv;`)
}

// ── G-live closure: Tablespace/Cast/EventTrigger structured diffing ───────────
// These three tests are the actual proof the G-live gap is closed, not just
// that structured diffing round-trips cleanly (assertOpaqueRoundtrip only
// proves reconstruction correctness — it can't catch a live-path detection
// regression, since it re-diffs the SAME object it just introspected, never
// a live-catalog-only mutation DPG never applied). Each test: applies via
// DPG, then mutates the object DIRECTLY via raw SQL (bypassing DPG
// entirely — simulating a hand-run change or drift from another tool),
// introspects, and asserts the resulting drift diff (desired vs. the fresh
// live-introspected snapshot) now detects the change. Before the fix, all
// three showed zero drift here (Reconstructed forced BodyHash to "" on the
// live side, so diffOpaqueIR's comparison silently never ran).

func TestGLiveCastFunctionChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	// Two fresh ENUM types guarantee no pre-existing system cast conflicts
	// (unlike built-in type pairs such as integer/text, which PostgreSQL
	// already casts implicitly).
	fixture := `TYPE cast_src_enum AS ENUM ('a');
TYPE cast_tgt_enum AS ENUM ('b');

FUNCTION cast_fn_v1(x cast_src_enum) RETURNS cast_tgt_enum
LANGUAGE sql AS $$ SELECT 'b'::cast_tgt_enum; $$ {}

CAST (cast_src_enum AS cast_tgt_enum) WITH FUNCTION cast_fn_v1(cast_src_enum);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: PostgreSQL has no ALTER CAST, so this is
	// DROP + CREATE with a different function — exactly what a hand-run
	// migration outside DPG would look like.
	if _, err := conn.Exec(ctx, `CREATE FUNCTION cast_fn_v2(x cast_src_enum) RETURNS cast_tgt_enum LANGUAGE sql AS $$ SELECT 'b'::cast_tgt_enum; $$;`); err != nil {
		t.Fatalf("create cast_fn_v2: %v", err)
	}
	if _, err := conn.Exec(ctx, `DROP CAST (cast_src_enum AS cast_tgt_enum);`); err != nil {
		t.Fatalf("drop cast: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE CAST (cast_src_enum AS cast_tgt_enum) WITH FUNCTION cast_fn_v2(cast_src_enum);`); err != nil {
		t.Fatalf("recreate cast with cast_fn_v2: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP CAST") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only cast function change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "cast_fn_v1") {
		t.Errorf("expected recreate to restore the declared cast_fn_v1, got: %s", sql)
	}
}

func TestGLiveEventTriggerTagChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FUNCTION evt_fn() RETURNS event_trigger
LANGUAGE plpgsql AS $$ BEGIN END; $$ {}

EVENT TRIGGER evt_test ON sql_drop
    WHEN TAG IN ('DROP TABLE')
    EXECUTE FUNCTION evt_fn();`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: PostgreSQL has no ALTER EVENT TRIGGER
	// for the tag list — DROP + CREATE with an extra tag, matching a
	// hand-run migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP EVENT TRIGGER evt_test;`); err != nil {
		t.Fatalf("drop event trigger: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE EVENT TRIGGER evt_test ON sql_drop WHEN TAG IN ('DROP TABLE', 'DROP SCHEMA') EXECUTE FUNCTION evt_fn();`); err != nil {
		t.Fatalf("recreate event trigger with extra tag: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP EVENT TRIGGER") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only tag change, got %d ops: %q", len(driftOps), sql)
	}
	if strings.Contains(sql, "'DROP SCHEMA'") {
		t.Errorf("expected recreate to restore the declared tag list ('DROP TABLE' only), not the live-mutated one, got: %s", sql)
	}
	for _, op := range driftOps {
		if op.Safety() != pipeline.Safe {
			t.Errorf("expected event trigger DROP+CREATE to be Safe (RFC Section 14.1), got %v: %s", op.Safety(), op.SQL())
		}
	}
}

// TestGLiveTablespaceLocationChangeDetected needs real filesystem
// directories inside the container (PostgreSQL requires LOCATION to
// already exist on disk — no SQL statement can create it), so it uses
// testpg.StartWithContainer rather than plain testpg.Start.
func TestGLiveTablespaceLocationChangeDetected(t *testing.T) {
	connStr, container := testpg.StartWithContainer(t)
	ctx := context.Background()

	for _, dir := range []string{"/var/lib/postgresql/ts1", "/var/lib/postgresql/ts2"} {
		if code, _, err := container.Exec(ctx, []string{"mkdir", "-p", dir}); err != nil || code != 0 {
			t.Fatalf("mkdir %s in container: code=%d err=%v", dir, code, err)
		}
		if code, _, err := container.Exec(ctx, []string{"chown", "postgres:postgres", dir}); err != nil || code != 0 {
			t.Fatalf("chown %s in container: code=%d err=%v", dir, code, err)
		}
	}

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLESPACE gl_ts LOCATION '/var/lib/postgresql/ts1';`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: LOCATION cannot be changed after
	// creation (RFC Section 14.7) — DROP + CREATE at the second directory,
	// matching a hand-run migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP TABLESPACE gl_ts;`); err != nil {
		t.Fatalf("drop tablespace: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLESPACE gl_ts LOCATION '/var/lib/postgresql/ts2';`); err != nil {
		t.Fatalf("recreate tablespace at ts2: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP TABLESPACE") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only location change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "/var/lib/postgresql/ts1") {
		t.Errorf("expected recreate to restore the declared location ts1, got: %s", sql)
	}
}

func TestGLiveFDWHandlerChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// file_fdw is a standard PostgreSQL contrib extension bundled with the
	// official image, giving a real, always-available HANDLER function
	// (file_fdw_handler) without depending on postgres_fdw or any other
	// specific extension's semantics.
	if _, err := conn.Exec(ctx, `CREATE EXTENSION file_fdw;`); err != nil {
		t.Fatalf("create file_fdw extension: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FOREIGN DATA WRAPPER gl_fdw HANDLER file_fdw_handler;`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: real PostgreSQL does support
	// ALTER FOREIGN DATA WRAPPER, but RFC Section 14.8 deliberately treats any
	// change as DROP+CREATE — so the live-catalog-only mutation here still
	// uses DROP+CREATE, matching a hand-run migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP FOREIGN DATA WRAPPER gl_fdw;`); err != nil {
		t.Fatalf("drop fdw: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE FOREIGN DATA WRAPPER gl_fdw;`); err != nil {
		t.Fatalf("recreate fdw without handler: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP FOREIGN DATA WRAPPER") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only handler change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "file_fdw_handler") {
		t.Errorf("expected recreate to restore the declared HANDLER, got: %s", sql)
	}
}

// TestGLiveForeignServerOptionsChangeDetectedAsAlter is the live-catalog
// proof for the new capability, not just the G-live gap closure: a
// live-only OPTIONS change is not just detected, it's detected as a real,
// targeted ALTER SERVER (RFC Section 14.9), not a spurious DROP+CREATE — the
// generic diffOpaqueIR path this replaces could only ever have produced
// DROP+CREATE once un-blinded, never the correct minimal-diff ALTER.
func TestGLiveForeignServerOptionsChangeDetectedAsAlter(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FOREIGN DATA WRAPPER gl_fdw2;
SERVER gl_srv FOREIGN DATA WRAPPER gl_fdw2 OPTIONS (host 'a');`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: a real ALTER SERVER, exactly as a
	// hand-run migration outside DPG would do.
	if _, err := conn.Exec(ctx, `ALTER SERVER gl_srv OPTIONS (SET host 'b');`); err != nil {
		t.Fatalf("alter server options: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 1 {
		t.Fatalf("G-live gap not closed: expected exactly 1 op (targeted ALTER SERVER) detecting the live-only OPTIONS change, got %d: %v", len(driftOps), driftOps)
	}
	sql := driftOps[0].SQL()
	if !strings.HasPrefix(sql, "ALTER SERVER") {
		t.Errorf("expected a targeted ALTER SERVER, not DROP+CREATE, got: %s", sql)
	}
	if driftOps[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER SERVER OPTIONS to be Safe, got %v: %s", driftOps[0].Safety(), sql)
	}
	if !strings.Contains(sql, "SET host 'a'") {
		t.Errorf("expected recreate to restore the declared host 'a', got: %s", sql)
	}

	// Apply the correction and confirm zero drift remains — proves the
	// generated ALTER SERVER is valid SQL a real server accepts, not just
	// well-formed text.
	migration, err := emitter.Emit(driftOps, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply corrective ALTER SERVER: %v", err)
	}
	liveObjects2, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("re-introspect: %v", err)
	}
	var managedLive2 []pipeline.IRObject
	for _, obj := range liveObjects2 {
		if _, ok := snap.Objects[obj.QualifiedName()]; ok {
			managedLive2 = append(managedLive2, obj)
		}
	}
	liveSnap2 := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap2, managedLive2); err != nil {
		t.Fatalf("populate live snapshot 2: %v", err)
	}
	finalOps, err := differ.Diff(desired, liveSnap2)
	if err != nil {
		t.Fatalf("final drift diff: %v", err)
	}
	if len(finalOps) != 0 {
		t.Errorf("expected zero drift after applying the corrective ALTER SERVER, got: %v", finalOps)
	}
}

func TestGLiveUserMappingNonSensitiveOptionsChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FOREIGN DATA WRAPPER gl_fdw3;
SERVER gl_srv3 FOREIGN DATA WRAPPER gl_fdw3;
USER MAPPING FOR PUBLIC SERVER gl_srv3 OPTIONS (user 'app_v1');`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: RFC Section 14.10 gives UserMapping no ALTER
	// path (DROP+CREATE only), matching a hand-run migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP USER MAPPING FOR PUBLIC SERVER gl_srv3;`); err != nil {
		t.Fatalf("drop user mapping: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE USER MAPPING FOR PUBLIC SERVER gl_srv3 OPTIONS (user 'app_v2');`); err != nil {
		t.Fatalf("recreate user mapping with a different user option: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP USER MAPPING") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-only non-sensitive OPTIONS change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "app_v1") {
		t.Errorf("expected recreate to restore the declared user 'app_v1', got: %s", sql)
	}
}

// TestGLivePublicationChangesDetectedAsAlter is the live-catalog proof for
// Publication's new capability, not just the G-live gap closure: live-only
// Tables and WITH (publish = ...) changes are each detected as a real,
// targeted ALTER PUBLICATION (RFC Section 13.1), not a spurious DROP+CREATE.
func TestGLivePublicationChangesDetectedAsAlter(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE gl_t1 (id integer) {}
TABLE gl_t2 (id integer) {}

PUBLICATION gl_pub FOR TABLE gl_t1;`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: real ALTER PUBLICATION statements,
	// exactly as a hand-run migration outside DPG would issue.
	if _, err := conn.Exec(ctx, `ALTER PUBLICATION gl_pub SET TABLE gl_t1, gl_t2;`); err != nil {
		t.Fatalf("alter publication set table: %v", err)
	}
	if _, err := conn.Exec(ctx, `ALTER PUBLICATION gl_pub SET (publish = 'insert');`); err != nil {
		t.Fatalf("alter publication set publish: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 2 {
		t.Fatalf("G-live gap not closed: expected exactly 2 ops (targeted ALTER PUBLICATION x2) detecting the live-only changes, got %d: %v", len(driftOps), driftOps)
	}
	for _, op := range driftOps {
		if !strings.HasPrefix(op.SQL(), "ALTER PUBLICATION") {
			t.Errorf("expected a targeted ALTER PUBLICATION, not DROP+CREATE, got: %s", op.SQL())
		}
		if op.Safety() != pipeline.Safe {
			t.Errorf("expected ALTER PUBLICATION ops to be Safe, got %v: %s", op.Safety(), op.SQL())
		}
	}

	// Apply the correction and confirm zero drift remains — proves the
	// generated ALTER PUBLICATION statements are valid SQL a real server
	// accepts, not just well-formed text.
	migration, err := emitter.Emit(driftOps, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply corrective ALTER PUBLICATION statements: %v", err)
	}
	liveObjects2, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("re-introspect: %v", err)
	}
	var managedLive2 []pipeline.IRObject
	for _, obj := range liveObjects2 {
		if _, ok := snap.Objects[obj.QualifiedName()]; ok {
			managedLive2 = append(managedLive2, obj)
		}
	}
	liveSnap2 := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap2, managedLive2); err != nil {
		t.Fatalf("populate live snapshot 2: %v", err)
	}
	finalOps, err := differ.Diff(desired, liveSnap2)
	if err != nil {
		t.Fatalf("final drift diff: %v", err)
	}
	if len(finalOps) != 0 {
		t.Errorf("expected zero drift after applying the corrective ALTER PUBLICATION statements, got: %v", finalOps)
	}
}

func TestGLivePublicationAllTablesChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE gl_t3 (id integer) {}

PUBLICATION gl_pub2 FOR TABLE gl_t3;`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: real PostgreSQL has no way to convert
	// a table-scoped publication to FOR ALL TABLES via ALTER (confirmed
	// live) — DROP+CREATE, matching a hand-run migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP PUBLICATION gl_pub2;`); err != nil {
		t.Fatalf("drop publication: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE PUBLICATION gl_pub2 FOR ALL TABLES;`); err != nil {
		t.Fatalf("recreate publication for all tables: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP PUBLICATION") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-only AllTables change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "gl_t3") {
		t.Errorf("expected recreate to restore the declared FOR TABLE gl_t3, got: %s", sql)
	}
}

// TestGLivePublicationFilteredTableChangeFallsBackToDropCreate is the
// live-catalog correctness proof for HasFilteredTables: a table-list
// change on a publication using a column-list/WHERE filter (the exact
// shape RFC Section 13.1's own worked example uses) must fall back to DROP+CREATE
// rather than a targeted ALTER PUBLICATION ... SET TABLE — the latter
// would silently rebuild the table list without the filter, an
// unintentional widening of what's replicated.
func TestGLivePublicationFilteredTableChangeFallsBackToDropCreate(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE gl_orders (id integer, customer_id integer, status text, total integer) {}
TABLE gl_line_items (id integer) {}

PUBLICATION gl_filtered_pub
    FOR TABLE gl_orders (id, customer_id, status, total)
    WHERE (status != 'draft');`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Confirm PostgreSQL actually recorded the column-list/WHERE filter —
	// a guard against the assertion below passing for the wrong reason.
	rfRows, err := conn.QueryRows(ctx, `SELECT rowfilter FROM pg_publication_tables WHERE pubname = 'gl_filtered_pub'`)
	if err != nil {
		t.Fatalf("query pg_publication_tables: %v", err)
	}
	var rowfilter *string
	if rfRows.Next() {
		if err := rfRows.Scan(&rowfilter); err != nil {
			t.Fatalf("scan rowfilter: %v", err)
		}
	}
	rfRows.Close()
	if rowfilter == nil || !strings.Contains(*rowfilter, "status") {
		t.Fatalf("expected the applied publication to have a real row filter, got: %v", rowfilter)
	}

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: a real hand-run migration outside DPG
	// changing the table list on a filtered publication.
	if _, err := conn.Exec(ctx, `ALTER PUBLICATION gl_filtered_pub DROP TABLE gl_orders;`); err != nil {
		t.Fatalf("drop table from publication: %v", err)
	}
	if _, err := conn.Exec(ctx, `ALTER PUBLICATION gl_filtered_pub ADD TABLE gl_line_items;`); err != nil {
		t.Fatalf("add table to publication: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if strings.Contains(sql, "SET TABLE") {
		t.Fatalf("expected the lossy ALTER PUBLICATION SET TABLE path to be avoided for a filtered publication, got: %q", sql)
	}
	if !strings.Contains(sql, "DROP PUBLICATION") {
		t.Fatalf("expected a fallback DROP+CREATE detecting the live-only table change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "gl_orders") || !strings.Contains(sql, "status") {
		t.Errorf("expected the recreate to restore the original filtered table declaration, got: %s", sql)
	}
}

func TestGLiveCollationLocaleChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `COLLATION gl_coll (LOCALE = 'C');`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: real PostgreSQL's ALTER COLLATION has
	// no locale-changing form at all (only REFRESH VERSION/OWNER TO/
	// RENAME TO/SET SCHEMA) — DROP + CREATE, matching a hand-run
	// migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP COLLATION gl_coll;`); err != nil {
		t.Fatalf("drop collation: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE COLLATION gl_coll (LOCALE = 'POSIX');`); err != nil {
		t.Fatalf("recreate collation with a different locale: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP COLLATION") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only locale change, got %d ops: %q", len(driftOps), sql)
	}
	if !strings.Contains(sql, "'C'") {
		t.Errorf("expected recreate to restore the declared locale 'C', got: %s", sql)
	}
}

// TestGLiveStatisticsObjectTargetChangeDetectedAsAlter is the live-catalog
// proof for StatisticsObject's new capability, not just the G-live gap
// closure: a live-only STATISTICS target change is detected as a real,
// targeted ALTER STATISTICS (RFC Section 14.6), not a spurious DROP+CREATE.
func TestGLiveStatisticsObjectTargetChangeDetectedAsAlter(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE gl_stats_t (id integer, val integer) {}

STATISTICS gl_stats (ndistinct) ON id, val FROM gl_stats_t;`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: a real ALTER STATISTICS, exactly as a
	// hand-run migration outside DPG would issue.
	if _, err := conn.Exec(ctx, `ALTER STATISTICS gl_stats SET STATISTICS 300;`); err != nil {
		t.Fatalf("alter statistics: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 1 {
		t.Fatalf("G-live gap not closed: expected exactly 1 op (targeted ALTER STATISTICS) detecting the live-only target change, got %d: %v", len(driftOps), driftOps)
	}
	sql := driftOps[0].SQL()
	if !strings.HasPrefix(sql, "ALTER STATISTICS") {
		t.Errorf("expected a targeted ALTER STATISTICS, not DROP+CREATE, got: %s", sql)
	}
	if driftOps[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER STATISTICS SET STATISTICS to be Safe, got %v: %s", driftOps[0].Safety(), sql)
	}
	if !strings.Contains(sql, "SET STATISTICS -1") {
		t.Errorf("expected recreate to restore the declared (unset/default) target, got: %s", sql)
	}

	// Apply the correction and confirm zero drift remains — proves the
	// generated ALTER STATISTICS is valid SQL a real server accepts, not
	// just well-formed text.
	migration, err := emitter.Emit(driftOps, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply corrective ALTER STATISTICS: %v", err)
	}
	liveObjects2, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("re-introspect: %v", err)
	}
	var managedLive2 []pipeline.IRObject
	for _, obj := range liveObjects2 {
		if _, ok := snap.Objects[obj.QualifiedName()]; ok {
			managedLive2 = append(managedLive2, obj)
		}
	}
	liveSnap2 := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap2, managedLive2); err != nil {
		t.Fatalf("populate live snapshot 2: %v", err)
	}
	finalOps, err := differ.Diff(desired, liveSnap2)
	if err != nil {
		t.Fatalf("final drift diff: %v", err)
	}
	if len(finalOps) != 0 {
		t.Errorf("expected zero drift after applying the corrective ALTER STATISTICS, got: %v", finalOps)
	}
}

func TestGLiveStatisticsObjectColumnsChangeDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE gl_stats_t2 (id integer, val integer, extra integer) {}

STATISTICS gl_stats2 (ndistinct) ON id, val FROM gl_stats_t2;`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: real PostgreSQL's ALTER STATISTICS has
	// no column-list-changing form at all — DROP + CREATE with a
	// different column list, matching a hand-run migration outside DPG.
	if _, err := conn.Exec(ctx, `DROP STATISTICS gl_stats2;`); err != nil {
		t.Fatalf("drop statistics: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE STATISTICS gl_stats2 (ndistinct) ON id, val, extra FROM gl_stats_t2;`); err != nil {
		t.Fatalf("recreate statistics with an extra column: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP STATISTICS") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only column-list change, got %d ops: %q", len(driftOps), sql)
	}
}

// ── Operator family loose members (RFC Section 14.4) ─────────────────────────────────

// TestRoundtripOpFamilyLooseMembers proves reconstruction matches source
// exactly for a family with loose (ALTER OPERATOR FAMILY ... ADD-shaped)
// members: two OPERATOR members reusing real pg_catalog operators plus a
// FUNCTION member with a fresh, DPG-declared cross-type function.
// Adversarially declares the family BEFORE the function it references, to
// prove the graph.go dependency edges added for this feature (not just the
// declaration order happening to work) are load-bearing.
func TestRoundtripOpFamilyLooseMembers(t *testing.T) {
	fixture := `SCHEMA opfam_rt {
	OPERATOR FAMILY opfam_rt.cross_fam USING btree {
		OPERATOR 1 <(int4, int8),
		OPERATOR 3 =(int4, int8),
		FUNCTION 1 (int4, int8) opfam_rt.cross_cmp(int4, int8)
	};

	FUNCTION cross_cmp(a int4, b int8) RETURNS int4
	LANGUAGE sql IMMUTABLE AS $$ SELECT 0; $$ {}
}`
	assertOpaqueRoundtrip(t, fixture)
}

// TestGLiveOpFamilyLooseMemberAddDetected is the G-live guard for RFC
// Section 14.4's loose members: a member added directly against the live catalog
// (bypassing DPG) must show up as a DESTRUCTIVE DROP in plan --live's drift
// output (the declared state has no such member, so the differ proposes
// removing it) — before this feature, loose members were never introspected
// at all, so this drift was completely invisible.
func TestGLiveOpFamilyLooseMemberAddDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `SCHEMA opfam_glive1 {
	OPERATOR FAMILY opfam_glive1.fam USING btree {
		OPERATOR 1 <(int4, int8)
	};
}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Mutate directly via raw SQL: add a second loose member DPG never
	// declared — real PostgreSQL supports this incrementally, unlike a
	// Cast/Collation/etc. body edit, which needs DROP+CREATE.
	if _, err := conn.Exec(ctx, `ALTER OPERATOR FAMILY opfam_glive1.fam USING btree ADD OPERATOR 3 =(int4, int8);`); err != nil {
		t.Fatalf("live ADD: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP OPERATOR 3") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the live-catalog-only loose member, got %d ops: %q", len(driftOps), sql)
	}
}

// TestGLiveOpFamilyLooseMemberRemoveDetected is
// TestGLiveOpFamilyLooseMemberAddDetected's mirror: a declared member
// removed directly against the live catalog must show up as a SAFE ADD in
// plan --live's drift output.
func TestGLiveOpFamilyLooseMemberRemoveDetected(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `SCHEMA opfam_glive2 {
	OPERATOR FAMILY opfam_glive2.fam USING btree {
		OPERATOR 1 <(int4, int8),
		OPERATOR 3 =(int4, int8)
	};
}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, err := conn.Exec(ctx, `ALTER OPERATOR FAMILY opfam_glive2.fam USING btree DROP OPERATOR 3 (int4, int8);`); err != nil {
		t.Fatalf("live DROP: %v", err)
	}

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

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	var sql string
	for _, op := range driftOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "ADD OPERATOR 3") {
		t.Fatalf("G-live gap not closed: expected plan --live to detect the missing loose member, got %d ops: %q", len(driftOps), sql)
	}
}

// TestRoundtripOpFamilyClassOwnedMembersNotTreatedAsLoose is the single
// highest-value regression guard for this whole feature: a full operator
// class AS-list plus an empty-block family declared for the same family
// must show zero drift — proving the pg_depend filter in
// opFamilyLooseMembers correctly excludes class-owned members (deptype='i'
// against pg_opclass) rather than proposing to DROP them as if they were
// undeclared loose members.
func TestRoundtripOpFamilyClassOwnedMembersNotTreatedAsLoose(t *testing.T) {
	fixture := `SCHEMA opfam_classowned {
	OPERATOR FAMILY opfam_classowned.myfam USING btree;

	OPERATOR CLASS opfam_classowned.myops FOR TYPE int4 USING btree FAMILY opfam_classowned.myfam AS
		OPERATOR 1 <,
		OPERATOR 2 <=,
		OPERATOR 3 =,
		OPERATOR 4 >=,
		OPERATOR 5 >,
		FUNCTION 1 btint4cmp(int4, int4);
}`
	assertOpaqueRoundtrip(t, fixture)
}

// TestRoundtripOpaqueBodyEditAppliesLive is the live-catalog guard for #3
// (real update path for opaque objects, internal/diff/differ.go diffOpaqueIR):
// previously an offline body edit to an opaque object (here, a COLLATION's
// LOCALE) only ever produced a "-- WARNING: ... manual DROP + recreate
// required" SQL comment — applying it was a silent no-op against a real
// database. This proves the structured DROP + CREATE it emits now is valid
// SQL that a real PostgreSQL instance accepts, and that after applying it the
// live catalog actually reflects the new body (zero drift against the edited
// desired state, not the original one).
func TestRoundtripOpaqueBodyEditAppliesLive(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `COLLATION my_coll (LOCALE = 'C');`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Edit the body (same identity, different LOCALE) and diff against the
	// snapshot from the v1 apply — this must route through diffOpaqueIR's
	// offline body-hash-changed branch.
	v2 := `COLLATION my_coll (LOCALE = 'POSIX');`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (body edit): %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("expected structured DROP+CREATE ops for the body edit, got none")
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "manual DROP + recreate required") {
			t.Fatalf("must not fall back to the manual warning for collation: %s", op.SQL())
		}
	}

	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (body edit) against live PostgreSQL: %v", err)
	}

	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired2); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", appliedSnap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after body-edit apply (%d ops) — live catalog doesn't reflect the new body:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	rs, err := conn.QueryRows(ctx, `SELECT collcollate FROM pg_collation WHERE collname = 'my_coll'`)
	if err != nil {
		t.Fatalf("query pg_collation: %v", err)
	}
	defer rs.Close()
	if !rs.Next() {
		t.Fatal("my_coll not found in pg_collation after body-edit apply")
	}
	var collate string
	if err := rs.Scan(&collate); err != nil {
		t.Fatalf("scan collcollate: %v", err)
	}
	if !strings.Contains(strings.ToUpper(collate), "POSIX") {
		t.Errorf("live collcollate = %q, want it to reflect the POSIX edit (was 'C')", collate)
	}
}

// TestRoundtripIndexVariants applies a table carrying every index variant —
// unique, multi-column sort order (DESC/ASC + NULLS), partial (WHERE), covering
// (INCLUDE), expression, and a non-btree method — and asserts zero drift. This
// exercises the parse → createIndex → introspect round-trip for the index class
// that repeatedly hid apply-only defects (sort-order corrupting the column,
// INCLUDE silently dropped).
// TestRoundtripDeferredTierObjects applies the "deferred tier" opaque objects
// whose catalog reconstruction was added alongside the reliable tier — an
// operator, an operator family, a text-search dictionary and configuration, and
// an operator class with a full member list — and asserts zero drift. Before
// these introspectors existed, plan --live emitted a spurious CREATE for each
// (introspection returned nothing), so zero drift here proves they are now
// discovered and their reconstructed DDL round-trips.
//
// All five reference only built-in functions/templates so the fixture needs no C
// extension. Text-search parsers and templates are intentionally excluded: their
// START/LEXIZE functions must be C-language internal functions, which cannot be
// created from pure SQL; their introspection queries are still exercised (for
// column-name validity) by every assertOpaqueRoundtrip call, which runs a full
// Introspect over the live catalog.
func TestRoundtripDeferredTierObjects(t *testing.T) {
	assertOpaqueRoundtrip(t, `OPERATOR public.=== (FUNCTION = int4eq, LEFTARG = integer, RIGHTARG = integer);
OPERATOR FAMILY public.rt_fam USING btree;
TEXT SEARCH DICTIONARY public.rt_dict (TEMPLATE = pg_catalog.simple);
TEXT SEARCH CONFIGURATION public.rt_cfg (PARSER = pg_catalog."default");
OPERATOR CLASS public.rt_opc FOR TYPE integer USING btree AS
    OPERATOR 1 <, OPERATOR 2 <=, OPERATOR 3 =, OPERATOR 4 >=, OPERATOR 5 >,
    FUNCTION 1 btint4cmp(integer, integer);`)
}

// TestRoundtripOperatorClassDeclaredBeforeFamily is the full-pipeline guard
// for the operator-family general fix's dependency-edge half (graph.go):
// applies a fixture that deliberately declares the CLASS textually BEFORE its
// explicit FAMILY — the reverse of natural reading order, and NOT
// self-healing the way most PostgreSQL CREATE statements are (CREATE OPERATOR
// FAMILY has no IF NOT EXISTS, confirmed live, so the family must genuinely
// exist before the class's FAMILY clause can reference it). Without the
// class→family dependency edge added to the resolver, this fixture would
// apply the CREATE OPERATOR CLASS statement first and fail with a real
// PostgreSQL "operator family ... does not exist" error — this test proves
// the resolver reorders it correctly regardless of source declaration order,
// then confirms zero drift on re-introspection (the class's FamilySchema/
// FamilyName fields captured on both the source-parse and introspection sides
// must agree).
func TestRoundtripOperatorClassDeclaredBeforeFamily(t *testing.T) {
	assertOpaqueRoundtrip(t, `OPERATOR CLASS public.rt_ordered_opc FOR TYPE integer USING btree FAMILY rt_ordered_fam AS
    OPERATOR 3 =, FUNCTION 1 btint4cmp(integer, integer);
OPERATOR FAMILY public.rt_ordered_fam USING btree;`)
}

// TestIntrospectOperatorFamilySharingClassNameRoundtrips is the full-pipeline
// guard for the operator-family general fix's introspection half: an
// EXPLICIT family sharing its attached class's name (the original
// misclassification bug this fix closes — confirmed live that PostgreSQL's
// opclass→opfamily pg_depend row is deptype 'a' for this case too, identical
// to a genuinely auto-created family, so no catalog signal alone could ever
// distinguish them) now round-trips with zero drift, the same as any other
// operator class/family pair. A direct, narrower guard for the introspection
// fields themselves lives in internal/introspect
// (TestIntrospectOperatorFamilyExplicitSharingClassName); this proves the
// full apply → introspect → diff pipeline is drift-free for it too.
func TestIntrospectOperatorFamilySharingClassNameRoundtrips(t *testing.T) {
	assertOpaqueRoundtrip(t, `OPERATOR FAMILY public.rt_same_name USING btree;
OPERATOR CLASS public.rt_same_name FOR TYPE integer USING btree FAMILY rt_same_name AS
    OPERATOR 3 =, FUNCTION 1 btint4cmp(integer, integer);`)
}

// TestRoundtripTableInherits is the permanent regression guard flagged as
// missing by the 2026-08-17 audit-followup's Part D (process/test-coverage
// gaps): Table.Inherits' dump round trip (Part 2b item 6) had only ever been
// manually live-verified in-session, with nothing committed to CI to catch a
// future regression the way integration/trigger_condition_test.go does for
// its own fix. Exercises the full apply → introspect → diff pipeline for a
// classic single-inheritance parent/child pair.
func TestRoundtripTableInherits(t *testing.T) {
	assertOpaqueRoundtrip(t, `TABLE rt_parent (id INTEGER PRIMARY KEY, label TEXT);
TABLE rt_child (extra TEXT) INHERITS (rt_parent);`)
}

// TestRoundtripBaseType is the permanent regression guard flagged as missing
// by the 2026-08-17 audit-followup's Part D: BASE type's dump round trip
// (Part 2b item 7) had likewise only ever been manually live-verified in-
// session. Built via PostgreSQL's own documented "reuse an existing
// internal function" bootstrapping trick (a genuine C-extension base type
// isn't buildable without a compiler in this environment) — the same
// myint/int4in/int4out shape used for that session's manual verification,
// now committed as a permanent test. The resolver must order the two
// LANGUAGE internal I/O functions before the finalizing TYPE statement
// (graph.go's BASE-type forward-reference exemption, see
// isBaseTypeTarget's doc comment) for this to apply at all.
func TestRoundtripBaseType(t *testing.T) {
	assertOpaqueRoundtrip(t, `FUNCTION rt_base_in(cstring) RETURNS rt_base LANGUAGE internal AS $$int4in$$;
FUNCTION rt_base_out(rt_base) RETURNS cstring LANGUAGE internal AS $$int4out$$;
TYPE rt_base (INPUT = rt_base_in, OUTPUT = rt_base_out, INTERNALLENGTH = 4);`)
}

func TestRoundtripIndexVariants(t *testing.T) {
	assertOpaqueRoundtrip(t, `TABLE t (a INTEGER, b INTEGER, c TEXT, e TEXT) {
    INDEXES {
        UNIQUE i_uniq (a);
        i_sort (c DESC NULLS LAST, b);
        i_partial (b) WHERE (b > 0);
        i_cover (a) INCLUDE (c, b);
        i_expr (lower(e));
        i_gin USING gin (to_tsvector('english', e));
        UNIQUE i_nulls_nd (b) NULLS NOT DISTINCT;
        i_with (a) WITH (fillfactor = 70);
    }
}`)
}

// TestRoundtripIndexModeB is the live-catalog guard for the Mode-B (RFC Section 4.8
// singular keyword) fix: previously "INDEX name (...)" outside an INDEXES{}
// block was a hard parse error, so it could never reach a live database at
// all. This proves the whole pipeline — parse Mode-B, build IR, emit CREATE
// INDEX, introspect back — round-trips against real PostgreSQL with zero
// drift, and that it can be freely mixed with a Mode-A INDEXES{} block on the
// same table.
func TestRoundtripIndexModeB(t *testing.T) {
	assertOpaqueRoundtrip(t, `TABLE t (a INTEGER, b INTEGER) {
    INDEX i_modeb_a (a);
    UNIQUE INDEX i_modeb_uniq (b) NULLS NOT DISTINCT;
    INDEXES { i_modea_ab (a, b); }
}`)
}

// TestRoundtripPolicyTriggerPartitionModeB is the live-catalog guard for the
// Mode-B fix applied to POLICY, TRIGGER, and PARTITION (RFC Section 4.8 singular
// keyword): previously these three keywords weren't in the block dispatch
// switch at all (unlike INDEX/GRANT/REVOKE, which at least hit the wrong,
// brace-requiring parser), so "POLICY ...;"/"TRIGGER ...;"/"PARTITION ...;"
// outside their plural blocks were "unknown block directive" errors. This
// proves all three parse, apply, and round-trip against real PostgreSQL with
// zero drift, each mixed with its Mode-A block form on the same table.
// Uses direct pg_catalog queries rather than assertOpaqueRoundtrip's
// introspect-and-diff round trip: applying this fixture surfaced two
// separate, pre-existing bugs unrelated to the Mode-B dispatch fix (a
// PARTITION OF ... FOR VALUES SQL-generation bug, fixed alongside this — see
// differ.go — and partition/trigger introspection producing spurious drift on
// a partitioned table, NOT fixed, out of scope, see .dpg-notes). Direct
// catalog checks isolate what this test is actually verifying: that Mode-B
// POLICY/TRIGGER/PARTITION parse into valid SQL that a real PostgreSQL
// instance accepts.
// Originally used direct pg_catalog counts instead of assertOpaqueRoundtrip's
// introspect-and-diff round trip, because introspecting a partitioned parent
// table was badly broken at the time: introspectColumns/Constraints/Indexes
// all filtered on relkind = 'r' only (excluding relkind = 'p', a partitioned
// parent), so a partitioned table's own columns/constraints/indexes were
// silently dropped on introspection, and diffTriggers compared a trigger's
// EXECUTE FUNCTION reference as a raw string (introspection always returns it
// schema-qualified; hand-written source commonly doesn't). Both are now fixed
// (see introspect.go doc comments on introspectColumns et al., and
// qualifyFuncForCompare in differ.go) — this now uses the full round trip,
// which is a strictly stronger check.
func TestRoundtripPolicyTriggerPartitionModeB(t *testing.T) {
	assertOpaqueRoundtrip(t, `FUNCTION trg_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN NEW;
END;
$$ {}

TABLE t (a INTEGER, created_at DATE NOT NULL) PARTITION BY RANGE (created_at) {
    CONSTRAINT ck_a CHECK (a > 0);
    INDEXES { i_a (a); }
    POLICY p_modeb FOR SELECT USING (true);
    POLICIES { p_modea FOR INSERT WITH CHECK (true); }
    TRIGGER trg_modeb AFTER INSERT FOR EACH ROW EXECUTE FUNCTION trg_touch();
    TRIGGERS { trg_modea AFTER UPDATE FOR EACH ROW EXECUTE FUNCTION trg_touch(); }
    PARTITION t_modeb FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');
    PARTITIONS { t_modea FOR VALUES FROM ('2025-01-01') TO ('2026-01-01'); }
}`)
}

// TestRoundtripReservedWordSchema is the live-catalog guard for the
// reserved-word/quoted SCHEMA name fix: the scanner's readSchemaDecl
// previously read the name via a bare-word-only reader, so a schema named
// after a reserved word or containing characters like a hyphen or an
// embedded quote couldn't be declared at all. Proves such a schema
// compiles, applies against a real PostgreSQL instance, and round-trips
// with zero drift.
func TestRoundtripReservedWordSchema(t *testing.T) {
	assertOpaqueRoundtrip(t, `SCHEMA "select" {
    COMMENT 'a reserved-word schema name';
}

SCHEMA "weird""name" {
}

TABLE "select".t (a INTEGER);`)
}

// TestRoundtripConstraintsBlockModeA is the live-catalog guard for the new
// CONSTRAINTS { } plural block (Mode A, RFC Section 4.8): previously only the
// singular CONSTRAINT form parsed at all — CONSTRAINTS had no parser
// whatsoever, unlike the other 7 collection types, which at least had both
// forms even when one was buggy. This proves the new block form parses,
// applies, and round-trips against real PostgreSQL with zero drift, mixed
// with a standalone Mode-B CONSTRAINT entry on the same table.
func TestRoundtripConstraintsBlockModeA(t *testing.T) {
	assertOpaqueRoundtrip(t, `TABLE t (a INTEGER, b INTEGER) {
    CONSTRAINT ck_a CHECK (a > 0);
    CONSTRAINTS {
        ck_b CHECK (b > 0);
        ck_sum CHECK (a + b < 1000);
    }
}`)
}

// TestRoundtripGrantRevocationModeBAppliesLive is the live-catalog guard for
// the GRANT/GRANTS + REVOKE/REVOKES Mode-B fix (RFC Section 4.8 singular
// keyword): previously "GRANT ...;"/"REVOKE ...;" outside a
// GRANTS{}/REVOKES{} block was a hard parse error — the identical
// conflation bug fixed for INDEX/INDEXES, both keywords routed to the same
// brace-requiring block parser. This proves Mode-B grants/revocations parse,
// apply, and take effect against a real PostgreSQL instance, mixed freely
// with Mode A on the same table.
//
// The REVOKE assertion here also exercises the fix for the separate bug
// this test originally surfaced: internal/diff/differ.go never read
// ir.Table/Column/View.Revocations at all, so a table-level REVOKE was a
// silent no-op regardless of syntax mode. That's now fixed (diffRevocationSet
// / tableExplicitRevokeOp in internal/diff/differ.go) — see CHANGELOG and
// .dpg-notes for detail. UPDATE is granted directly first so the REVOKE
// has a real privilege to remove; otherwise "rt_writer lacks UPDATE" would
// hold whether or not REVOKE actually ran, since it never had it either way.
func TestRoundtripGrantRevocationModeBAppliesLive(t *testing.T) {
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

	if _, err := conn.Exec(ctx, `CREATE ROLE rt_reader`); err != nil {
		t.Fatalf("create role rt_reader: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE ROLE rt_writer`); err != nil {
		t.Fatalf("create role rt_writer: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `TABLE t (a INTEGER) {
    GRANT SELECT TO rt_reader;
    GRANTS { INSERT TO rt_writer; }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if _, err := conn.Exec(ctx, `GRANT UPDATE ON t TO rt_writer`); err != nil {
		t.Fatalf("grant update (setup): %v", err)
	}

	v2 := `TABLE t (a INTEGER) {
    GRANT SELECT TO rt_reader;
    GRANTS { INSERT TO rt_writer; }
    REVOKE UPDATE FROM rt_writer;
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (add revocation): %v", err)
	}
	var sawRevoke bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "REVOKE UPDATE ON TABLE") {
			sawRevoke = true
		}
	}
	if !sawRevoke {
		t.Fatalf("expected a structured REVOKE for the Mode B REVOKE, got %d ops", len(ops))
	}
	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (add revocation) against live PostgreSQL: %v", err)
	}

	rs, err := conn.QueryRows(ctx, `SELECT
		has_table_privilege('rt_reader', 'public.t', 'SELECT'),
		has_table_privilege('rt_writer', 'public.t', 'INSERT'),
		has_table_privilege('rt_writer', 'public.t', 'UPDATE')`)
	if err != nil {
		t.Fatalf("query privileges: %v", err)
	}
	defer rs.Close()
	if !rs.Next() {
		t.Fatal("no privilege row returned")
	}
	var readerSelect, writerInsert, writerUpdate bool
	if err := rs.Scan(&readerSelect, &writerInsert, &writerUpdate); err != nil {
		t.Fatalf("scan privileges: %v", err)
	}
	if !readerSelect {
		t.Error("rt_reader should have SELECT (Mode B GRANT, table-level)")
	}
	if !writerInsert {
		t.Error("rt_writer should have INSERT (Mode A GRANTS block)")
	}
	if writerUpdate {
		t.Error("rt_writer should have had UPDATE actually revoked by the Mode B REVOKE, but still has it")
	}
}

// TestRoundtripColumnRevocationAppliesLive is the live-catalog guard for the
// column-level half of the Table/Column/View REVOKE emission fix — its
// SQL shape (privilege (column), ...) differs from the table-level form
// already proven by TestRoundtripGrantRevocationModeBAppliesLive, so it's
// worth its own real-PostgreSQL check rather than trusting it by analogy.
func TestRoundtripColumnRevocationAppliesLive(t *testing.T) {
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

	if _, err := conn.Exec(ctx, `CREATE ROLE rt_col_reader`); err != nil {
		t.Fatalf("create role: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `TABLE docs (id INTEGER, body TEXT) {
    COLUMN body { GRANTS { SELECT TO rt_col_reader; } }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	v2 := `TABLE docs (id INTEGER, body TEXT) {
    COLUMN body {
        GRANTS { SELECT TO rt_col_reader; }
        REVOKES { SELECT FROM rt_col_reader; }
    }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (add column revocation): %v", err)
	}
	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (add column revocation) against live PostgreSQL: %v", err)
	}

	rs, err := conn.QueryRows(ctx, `SELECT has_column_privilege('rt_col_reader', 'public.docs', 'body', 'SELECT')`)
	if err != nil {
		t.Fatalf("query column privilege: %v", err)
	}
	defer rs.Close()
	if !rs.Next() {
		t.Fatal("no privilege row returned")
	}
	var stillHasSelect bool
	if err := rs.Scan(&stillHasSelect); err != nil {
		t.Fatalf("scan privilege: %v", err)
	}
	if stillHasSelect {
		t.Error("rt_col_reader should have had column-level SELECT actually revoked, but still has it")
	}
}

// TestRoundtripIndexDefinitionChangeAppliesLive is the live-catalog guard for
// the diffIndexes name-only-matching fix (internal/diff/differ.go): previously
// a same-named index was matched by name only, with zero comparison of its
// actual definition, so editing an existing index's properties (here: adding
// a WHERE predicate and switching btree -> gin) was a silent no-op against a
// real database — plan/apply never touched it. This proves the structured
// DROP + CREATE it now emits is valid SQL a real PostgreSQL instance accepts,
// and that the live catalog actually reflects the new definition afterward.
func TestRoundtripIndexDefinitionChangeAppliesLive(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `TABLE t (a INTEGER, e TEXT) {
    INDEXES { t_idx (a); }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Edit the index's definition (same name, WHERE added, method changed to
	// gin over an expression) and diff against the snapshot from the v1
	// apply — this must route through diffIndexes' new content-comparison
	// branch, not the name-only-existence check.
	v2 := `TABLE t (a INTEGER, e TEXT) {
    INDEXES { t_idx USING gin (to_tsvector('english', e)) WHERE (a > 0); }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (index definition change): %v", err)
	}
	var sawDrop, sawCreate bool
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "DROP INDEX") && strings.Contains(sql, `"t_idx"`) {
			sawDrop = true
		}
		if strings.Contains(sql, "CREATE INDEX") && strings.Contains(sql, `"t_idx"`) {
			sawCreate = true
		}
	}
	if !sawDrop || !sawCreate {
		t.Fatalf("expected structured DROP INDEX + CREATE INDEX for t_idx, got: %v", ops)
	}

	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (index definition change) against live PostgreSQL: %v", err)
	}

	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired2); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", appliedSnap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after index-definition-change apply (%d ops) — live catalog doesn't reflect the new definition:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	rs, err := conn.QueryRows(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 't_idx'`)
	if err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	defer rs.Close()
	if !rs.Next() {
		t.Fatal("t_idx not found in pg_indexes after definition-change apply")
	}
	var indexdef string
	if err := rs.Scan(&indexdef); err != nil {
		t.Fatalf("scan indexdef: %v", err)
	}
	if !strings.Contains(indexdef, "gin") || !strings.Contains(indexdef, "WHERE") {
		t.Errorf("live indexdef = %q, want it to reflect the gin+WHERE edit", indexdef)
	}
}

// TestRoundtripSequenceCycleChangeAndProcedure is the live-catalog guard for
// two gaps found during a diff-package coverage push: (1) PROCEDURE had zero
// diff-level test coverage anywhere, unit or live; (2) diffSequence's CYCLE
// comparison was gated on IncrementBy also being explicitly set, so adding
// CYCLE alone to an already-applied sequence was silently ignored by
// verify/plan --live. Mirrors TestRoundtripIndexDefinitionChangeAppliesLive's
// pattern: diff a real change against the v1 snapshot (the offline `plan`
// path), then apply and reconfirm zero drift against the live catalog.
func TestRoundtripSequenceCycleChangeAndProcedure(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `SEQUENCE seq_id INCREMENT BY 1;

PROCEDURE recalc_totals() LANGUAGE plpgsql AS $$
BEGIN
    NULL;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// v2 adds CYCLE to the sequence (no other sequence param touched — the
	// exact shape the bug missed) and changes the procedure body.
	v2 := `SEQUENCE seq_id INCREMENT BY 1 CYCLE;

PROCEDURE recalc_totals() LANGUAGE plpgsql AS $$
BEGIN
    UPDATE pg_temp.nonexistent_marker SET x = 1 WHERE false;
EXCEPTION WHEN OTHERS THEN NULL;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (cycle + procedure body change): %v", err)
	}
	var sawCycle, sawProcReplace bool
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "ALTER SEQUENCE") && strings.Contains(sql, "CYCLE") {
			sawCycle = true
		}
		if strings.Contains(sql, "CREATE OR REPLACE PROCEDURE") && strings.Contains(sql, "recalc_totals") {
			sawProcReplace = true
		}
	}
	if !sawCycle {
		t.Fatalf("expected ALTER SEQUENCE ... CYCLE for the cycle-only change, got: %v", ops)
	}
	if !sawProcReplace {
		t.Fatalf("expected CREATE OR REPLACE PROCEDURE for the body change, got: %v", ops)
	}

	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (cycle + procedure body change) against live PostgreSQL: %v", err)
	}

	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired2); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", appliedSnap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after cycle+procedure apply (%d ops) — live catalog doesn't reflect the change:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	rs, err := conn.QueryRows(ctx, `SELECT seqcycle FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid WHERE c.relname = 'seq_id'`)
	if err != nil {
		t.Fatalf("query pg_sequence: %v", err)
	}
	defer rs.Close()
	if !rs.Next() {
		t.Fatal("seq_id not found in pg_sequence after cycle-change apply")
	}
	var cycle bool
	if err := rs.Scan(&cycle); err != nil {
		t.Fatalf("scan seqcycle: %v", err)
	}
	if !cycle {
		t.Error("live seqcycle = false, want true after applying CYCLE")
	}
}

// TestRoundtripSequenceOwner is the live regression guard for a bug found
// reviewing the dump Owner-rendering fix: Sequence Owner was never actually
// wired into createSequence/diffSequence/SnapSequence, unlike Table/Schema
// (which both genuinely act on Owner) — a declared sequence Owner had zero
// effect anywhere, for both initial creation and subsequent drift, even
// though the IR builder correctly parsed it and dump had started rendering
// it. Confirms OWNER TO actually reaches a real PostgreSQL 17 catalog on
// both create and a follow-up change, and that verify sees zero drift after.
func TestRoundtripSequenceOwner(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE ROLE rt_seq_owner_a`); err != nil {
		t.Fatalf("create role rt_seq_owner_a: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE ROLE rt_seq_owner_b`); err != nil {
		t.Fatalf("create role rt_seq_owner_b: %v", err)
	}
	// RFC Section 11.5: a declared OWNER now creates the sequence directly as that
	// role via SET ROLE, not as the connecting superuser reassigned
	// afterward — so rt_seq_owner_a genuinely needs CREATE on schema public
	// itself (postgres:17 revokes it from PUBLIC by default, unlike pre-15).
	if _, err := conn.Exec(ctx, `GRANT CREATE ON SCHEMA public TO rt_seq_owner_a`); err != nil {
		t.Fatalf("grant create on schema public to rt_seq_owner_a: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `SEQUENCE seq_owned { OWNER rt_seq_owner_a; }`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	queryOwner := func() string {
		t.Helper()
		rs, err := conn.QueryRows(ctx,
			`SELECT r.rolname FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE c.relname = 'seq_owned'`)
		if err != nil {
			t.Fatalf("query owner: %v", err)
		}
		defer rs.Close()
		if !rs.Next() {
			t.Fatal("seq_owned not found in pg_class")
		}
		var owner string
		if err := rs.Scan(&owner); err != nil {
			t.Fatalf("scan owner: %v", err)
		}
		return owner
	}
	if got := queryOwner(); got != "rt_seq_owner_a" {
		t.Fatalf("owner after create = %q, want rt_seq_owner_a", got)
	}

	v2 := `SEQUENCE seq_owned { OWNER rt_seq_owner_b; }`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (owner change): %v", err)
	}
	var sawOwnerChange bool
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "ALTER SEQUENCE") && strings.Contains(sql, "OWNER TO") && strings.Contains(sql, "rt_seq_owner_b") {
			sawOwnerChange = true
		}
	}
	if !sawOwnerChange {
		t.Fatalf("expected ALTER SEQUENCE ... OWNER TO rt_seq_owner_b, got: %v", ops)
	}

	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (owner change) against live PostgreSQL: %v", err)
	}
	if got := queryOwner(); got != "rt_seq_owner_b" {
		t.Fatalf("owner after change = %q, want rt_seq_owner_b", got)
	}

	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired2); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", appliedSnap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after owner-change apply (%d ops) — live catalog doesn't reflect the change:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedConstraintsNoLiveDrift is the live regression guard
// for the unnamed-PK/UNIQUE/FK matching fix: PostgreSQL's
// pg_constraint.conname is NEVER empty (it auto-generates a name like
// "widgets_pkey" even when the source declares no CONSTRAINT name at all),
// so before this fix, verify/plan --live against a table with an inline
// unnamed PK/UNIQUE/FK produced a self-inconsistent DROP+ADD pair on every
// single run — never converging. Confirms all three constraint kinds
// (declared with no CONSTRAINT name anywhere in source) show zero drift
// against a real PostgreSQL 17 catalog after apply.
func TestRoundtripUnnamedConstraintsNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `TABLE widgets (
    id BIGINT PRIMARY KEY,
    external_id TEXT UNIQUE
);

TABLE orders (
    id BIGINT PRIMARY KEY,
    widget_id BIGINT REFERENCES widgets (id)
);`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for inline unnamed PK/UNIQUE/FK against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedCheckConstraintNoLiveDrift extends the unnamed-naming
// reconciliation fix (TestRoundtripUnnamedConstraintsNoLiveDrift, PK/UNIQUE/
// FK only) to CHECK: an unnamed single-column CHECK and an unnamed
// multi-column CHECK, applied against a real PostgreSQL 17 container, must
// each match their real auto-generated name (heap.c's pull_var_clause-based
// "tab_col_check" vs "tab_check" rule) on re-introspection, not produce a
// self-inconsistent DROP+ADD pair on every verify/plan --live.
func TestRoundtripUnnamedCheckConstraintNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `TABLE orders (
    amount INTEGER,
    a INTEGER,
    b INTEGER,
    CHECK (amount > 0),
    CHECK (a > 0 AND b > 0)
);`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm PG actually generated the names this test's rationale claims —
	// a guard against the test passing for the wrong reason.
	var tbl *snapshot.SnapTable
	for _, raw := range liveSnap.Objects {
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err == nil && so.Table != nil && so.Table.Name == "orders" {
			tbl = so.Table
		}
	}
	if tbl == nil {
		t.Fatal("orders table not found in live snapshot")
	}
	wantNames := map[string]bool{"orders_amount_check": false, "orders_check": false}
	for _, c := range tbl.Constraints {
		if _, ok := wantNames[c.Name]; ok {
			wantNames[c.Name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected live-generated constraint name %q, got constraints: %+v", name, tbl.Constraints)
		}
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for inline unnamed CHECK constraints against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripOperatorBodyEditAppliesLive mirrors
// TestRoundtripOpaqueBodyEditAppliesLive's pattern, but for "operator" —
// previously the one opaque kind excluded from the structured DROP+CREATE
// path (diffOpaqueIR fell back to a manual-warning comment) because
// dropObject couldn't safely build a DROP OPERATOR statement: PostgreSQL
// requires a mandatory (lefttype, righttype) clause that ir.Operator didn't
// capture. This proves the real DROP OPERATOR statement (with operand types)
// actually works against a live server, not just compiles.
func TestRoundtripOperatorBodyEditAppliesLive(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	v1 := `OPERATOR public.#+# (FUNCTION = int4pl, LEFTARG = integer, RIGHTARG = integer);`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Edit the body (same identity — same schema/name/operand types — but a
	// different FUNCTION) and diff against the snapshot from the v1 apply.
	// This must route through diffOpaqueIR's structured drop+recreate path,
	// not the old manual-warning fallback.
	v2 := `OPERATOR public.#+# (FUNCTION = int4mi, LEFTARG = integer, RIGHTARG = integer);`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (body edit): %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (structured DROP+CREATE) for the body edit, got %d: %v", len(ops), ops)
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "manual DROP + recreate required") {
			t.Fatalf("must not fall back to the manual warning for operator: %s", op.SQL())
		}
	}
	if !strings.Contains(ops[0].SQL(), `DROP OPERATOR IF EXISTS "public".#+#(integer, integer);`) {
		t.Fatalf("expected a valid DROP OPERATOR with operand types, got: %s", ops[0].SQL())
	}

	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (body edit) against live PostgreSQL: %v", err)
	}

	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired2); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", appliedSnap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after body-edit apply (%d ops) — live catalog doesn't reflect the new body:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	rs, err := conn.QueryRows(ctx, `SELECT oprcode::regproc::text FROM pg_operator WHERE oprname = '#+#'`)
	if err != nil {
		t.Fatalf("query pg_operator: %v", err)
	}
	defer rs.Close()
	if !rs.Next() {
		t.Fatal("#+# not found in pg_operator after body-edit apply")
	}
	var fn string
	if err := rs.Scan(&fn); err != nil {
		t.Fatalf("scan oprcode: %v", err)
	}
	if fn != "int4mi" {
		t.Errorf("live oprcode = %q, want int4mi (the edit)", fn)
	}
}

// TestRoundtripExcludeConstraint proves EXCLUDE constraints — previously a
// silent no-op source-side (buildConstraint discarded the entire body down
// to a placeholder that would fail to apply) — now genuinely round-trip:
// compile, apply, introspect, and re-diff at zero drift, against a real
// PostgreSQL 17 catalog. Covers the canonical "no overlapping bookings"
// shape (USING gist, two elements, a WHERE clause — btree_gist supplies the
// integer "=" operator class GiST needs) and the no-USING (defaults to
// btree) single-element form.
func TestRoundtripExcludeConstraint(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS btree_gist`); err != nil {
		t.Fatalf("create btree_gist: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE bookings (
    room integer,
    during tsrange,
    CONSTRAINT no_overlap EXCLUDE USING gist (room WITH =, during WITH &&) WHERE (room > 0)
);

TABLE singles (
    a integer,
    CONSTRAINT one_a EXCLUDE (a WITH =)
);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm both constraints actually applied and reference the right
	// access method — a guard against the zero-drift check below passing
	// for the wrong reason (e.g. both constraints silently missing).
	rs, err := conn.QueryRows(ctx, `
SELECT con.conname, am.amname
FROM pg_constraint con
JOIN pg_class idx ON idx.oid = con.conindid
JOIN pg_am am ON am.oid = idx.relam
WHERE con.contype = 'x'
ORDER BY con.conname`)
	if err != nil {
		t.Fatalf("query pg_constraint: %v", err)
	}
	defer rs.Close()
	gotAM := map[string]string{}
	for rs.Next() {
		var name, am string
		if err := rs.Scan(&name, &am); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotAM[name] = am
	}
	if gotAM["no_overlap"] != "gist" {
		t.Errorf("no_overlap access method = %q, want gist", gotAM["no_overlap"])
	}
	if gotAM["one_a"] != "btree" {
		t.Errorf("one_a access method = %q, want btree (PG's default)", gotAM["one_a"])
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for EXCLUDE constraints against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedExcludeNoLiveDrift is the naming-reconciliation
// counterpart to TestRoundtripExcludeConstraint (which used named
// constraints, so it never exercised the naming path at all): an inline
// UNNAMED EXCLUDE — no CONSTRAINT keyword anywhere in source — must match
// PostgreSQL's own auto-generated name on re-introspection, not produce a
// self-inconsistent DROP+ADD pair on every verify/plan --live, the same
// class of bug already fixed for unnamed PRIMARY KEY/UNIQUE/FOREIGN
// KEY/CHECK. Covers both a multi-element gist EXCLUDE and a single-element
// no-USING (defaults to btree) EXCLUDE, confirming each got its real
// predicted name via a direct pg_constraint query before checking drift — a
// guard against the zero-drift check passing for the wrong reason.
func TestRoundtripUnnamedExcludeNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS btree_gist`); err != nil {
		t.Fatalf("create btree_gist: %v", err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE bookings (
    room integer,
    during tsrange,
    EXCLUDE USING gist (room WITH =, during WITH &&)
);

TABLE singles (
    a integer,
    EXCLUDE (a WITH =)
);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm PG actually generated the names this test's rationale claims —
	// a guard against the zero-drift check below passing for the wrong
	// reason (e.g. the constraints silently missing).
	rs, err := conn.QueryRows(ctx, `SELECT conname FROM pg_constraint WHERE contype = 'x' ORDER BY conname`)
	if err != nil {
		t.Fatalf("query pg_constraint: %v", err)
	}
	defer rs.Close()
	wantNames := map[string]bool{"bookings_room_during_excl": false, "singles_a_excl": false}
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := wantNames[name]; ok {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected live-generated constraint name %q, not found in pg_constraint", name)
		}
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for inline unnamed EXCLUDE constraints against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedExcludeFuncCallElementNoLiveDrift covers the
// function-call-element naming case an independent verification pass
// found was incorrectly excluded from the original EXCLUDE-naming fix: a
// bare, top-level function-call element (lower(a)) predicts PostgreSQL's
// real generated name without needing OID resolution, since PG's own
// algorithm never descends into the call's arguments for naming. Also
// covers the two-elements-same-function-name case, which PostgreSQL
// disambiguates with its own per-element numeric suffix
// (confirmed live: "lower"/"lower1").
func TestRoundtripUnnamedExcludeFuncCallElementNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE t1 (
    a text,
    EXCLUDE ((lower(a)) WITH =)
);

TABLE t2 (
    a text,
    b text,
    EXCLUDE ((lower(a)) WITH =, (lower(b)) WITH =)
);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm PG actually generated the names this test's rationale claims —
	// a guard against the zero-drift check below passing for the wrong
	// reason (e.g. the constraints silently missing).
	rs, err := conn.QueryRows(ctx, `SELECT conname FROM pg_constraint WHERE contype = 'x' ORDER BY conname`)
	if err != nil {
		t.Fatalf("query pg_constraint: %v", err)
	}
	defer rs.Close()
	wantNames := map[string]bool{"t1_lower_excl": false, "t2_lower_lower1_excl": false}
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := wantNames[name]; ok {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected live-generated constraint name %q, not found in pg_constraint", name)
		}
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for unnamed function-call EXCLUDE elements against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedExcludeCastElementNoLiveDrift covers the type-cast
// naming case an independent verification pass found was missing from the
// FuncCall-only fix above: PostgreSQL's real algorithm for a TypeCast
// element (FigureColnameInternal, confirmed via source and live testing)
// prefers a "strong" name (a column or function call) from the cast's own
// argument over the cast's target type — and only falls back to the
// target type's name when the argument gives nothing better. Covers both
// branches: a cast over a plain column (predicts the column's name) and a
// cast over a bare operator expression (predicts the cast's own type name,
// since the operator alone gives nothing usable).
func TestRoundtripUnnamedExcludeCastElementNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE t1 (
    a integer,
    EXCLUDE ((a::text) WITH =)
);

TABLE t2 (
    a integer,
    b integer,
    EXCLUDE (((a + b)::text) WITH =)
);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm PG actually generated the names this test's rationale claims —
	// a guard against the zero-drift check below passing for the wrong
	// reason (e.g. the constraints silently missing).
	rs, err := conn.QueryRows(ctx, `SELECT conname FROM pg_constraint WHERE contype = 'x' ORDER BY conname`)
	if err != nil {
		t.Fatalf("query pg_constraint: %v", err)
	}
	defer rs.Close()
	wantNames := map[string]bool{"t1_a_excl": false, "t2_text_excl": false}
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := wantNames[name]; ok {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected live-generated constraint name %q, not found in pg_constraint", name)
		}
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for unnamed cast-based EXCLUDE elements against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedExcludeExtraNodeTypesNoLiveDrift covers the four
// remaining FigureColnameInternal node shapes an independent verification
// pass confirmed were also predictable and worth closing: COALESCE, CASE
// (with and without an ELSE clause), an array subscript, and COLLATE.
func TestRoundtripUnnamedExcludeExtraNodeTypesNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE t1 (
    a integer,
    EXCLUDE ((coalesce(a, 0)) WITH =)
);

TABLE t2 (
    a integer,
    EXCLUDE ((case when a > 0 then 1 else 2 end) WITH =)
);

TABLE t3 (
    a integer[],
    EXCLUDE ((a[1]) WITH =)
);

TABLE t4 (
    a text,
    EXCLUDE ((a COLLATE "C") WITH =)
);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm PG actually generated the names this test's rationale claims —
	// a guard against the zero-drift check below passing for the wrong
	// reason (e.g. the constraints silently missing).
	rs, err := conn.QueryRows(ctx, `SELECT conname FROM pg_constraint WHERE contype = 'x' ORDER BY conname`)
	if err != nil {
		t.Fatalf("query pg_constraint: %v", err)
	}
	defer rs.Close()
	wantNames := map[string]bool{
		"t1_coalesce_excl": false,
		"t2_case_excl":     false,
		"t3_a_excl":        false,
		"t4_a_excl":        false,
	}
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := wantNames[name]; ok {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected live-generated constraint name %q, not found in pg_constraint", name)
		}
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for these EXCLUDE elements against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripUnnamedExcludeBareExpressionElementNoLiveDrift closes the one
// EXCLUDE element shape none of the other roundtrip tests above cover: a
// bare, uncast operator expression (e.g. "a + b"), which FigureIndexColname
// can't derive a name from at all. This was previously believed to need a
// live catalog connection to resolve (PostgreSQL's ChooseIndexExpressionName
// needs a fully analyzed, OID-resolved tree for THAT algorithm) — but that's
// the wrong algorithm: EXCLUDE's own constraint-naming path goes through
// ChooseIndexColumnNames instead, which falls back to the literal string
// "expr" (deduplicated like any other repeated element name) purely
// syntactically, no catalog access needed. Confirmed live against PG 17:
// EXCLUDE USING btree ((a + b) WITH =) really does generate "t1_expr_excl",
// and two such elements dedup to "expr"/"expr1" exactly like two same-named
// function-call elements already do.
func TestRoundtripUnnamedExcludeBareExpressionElementNoLiveDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `TABLE t1 (
    a integer,
    b integer,
    EXCLUDE ((a + b) WITH =)
);

TABLE t2 (
    a integer,
    b integer,
    c integer,
    d integer,
    EXCLUDE ((a + b) WITH =, (c * d) WITH =)
);

TABLE t3 (
    room integer,
    a integer,
    EXCLUDE (room WITH =, (a + 1) WITH =)
);`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := appliedSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}

	// Confirm PG actually generated the names this test's rationale claims —
	// a guard against the zero-drift check below passing for the wrong
	// reason (e.g. the constraints silently missing).
	rs, err := conn.QueryRows(ctx, `SELECT conname FROM pg_constraint WHERE contype = 'x' ORDER BY conname`)
	if err != nil {
		t.Fatalf("query pg_constraint: %v", err)
	}
	defer rs.Close()
	wantNames := map[string]bool{
		"t1_expr_excl":       false,
		"t2_expr_expr1_excl": false,
		"t3_room_expr_excl":  false,
	}
	for rs.Next() {
		var name string
		if err := rs.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := wantNames[name]; ok {
			wantNames[name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("expected live-generated constraint name %q, not found in pg_constraint", name)
		}
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for bare-expression EXCLUDE elements against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripFunctionParallelCostRows covers PARALLEL/COST/ROWS fidelity
// end to end: a scalar function that explicitly declares PARALLEL/COST, and
// a plain function that declares neither (the common case, which must not
// show spurious drift against PostgreSQL's own live catalog defaults — the
// same suppress-when-default problem already solved for column STORAGE).
//
// ROWS is deliberately NOT exercised through the normal DPG-compiled path
// here: ROWS is only valid on a set-returning function ("RETURNS SETOF ...").
// At the time this test was written, DPG had no SETOF representation
// anywhere in ir.TypeRef/ir.Function — a genuine, separate, pre-existing gap
// found while building this fixture (confirmed live: PostgreSQL itself
// rejects ROWS on a scalar function with "ROWS is not applicable when
// function does not return a set"). That gap has since been closed (see
// TestRoundtripFunctionSetOf, which exercises ROWS together with SETOF
// through the full DPG-compiled path) — this test is left scoped to
// PARALLEL/COST only since that's what it already covers correctly and
// there's no need to touch a passing test to widen unrelated coverage.
func TestRoundtripFunctionParallelCostRows(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FUNCTION f_explicit(n integer) RETURNS integer
LANGUAGE sql STABLE PARALLEL SAFE COST 500 AS $$
    SELECT n + 1;
$$ {}

FUNCTION f_default(n integer) RETURNS integer
LANGUAGE sql AS $$
    SELECT n + 1;
$$ {}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

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

	// Confirm PostgreSQL actually recorded the explicit values — a guard
	// against the zero-drift check below passing for the wrong reason.
	rs, err := conn.QueryRows(ctx, `
SELECT proname, proparallel::text, procost
FROM pg_proc WHERE proname IN ('f_explicit', 'f_default') ORDER BY proname`)
	if err != nil {
		t.Fatalf("query pg_proc: %v", err)
	}
	defer rs.Close()
	type attrs struct {
		parallel string
		cost     float64
	}
	got := map[string]attrs{}
	for rs.Next() {
		var name, parallel string
		var cost float64
		if err := rs.Scan(&name, &parallel, &cost); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = attrs{parallel, cost}
	}
	if got["f_explicit"] != (attrs{"s", 500}) {
		t.Errorf("f_explicit pg_proc attrs: got %+v, want {s 500}", got["f_explicit"])
	}
	if got["f_default"] != (attrs{"u", 100}) {
		t.Errorf("f_default pg_proc attrs: got %+v, want {u 100} (PostgreSQL's own defaults)", got["f_default"])
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for PARALLEL/COST (explicit and default) against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// A zero-drift check alone doesn't exercise diffFunction's Parallel/Cost
	// comparison at all — f_explicit was already correctly created with the
	// right attributes on the FIRST apply (via buildFunctionSQL, unaffected
	// by this check), so there's genuinely nothing to detect either way.
	// Change f_explicit's PARALLEL/COST and confirm the diff against the
	// snapshot from the v1 apply is a real, non-empty CREATE OR REPLACE —
	// the actual code path diffFunction's new Parallel/Cost comparison adds.
	v2 := `FUNCTION f_explicit(n integer) RETURNS integer
LANGUAGE sql STABLE PARALLEL UNSAFE COST 600 AS $$
    SELECT n + 1;
$$ {}

FUNCTION f_default(n integer) RETURNS integer
LANGUAGE sql AS $$
    SELECT n + 1;
$$ {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap2, _ := store.Load("test", "dpgtest")
	changeOps, err := differ.Diff(desired2, snap2)
	if err != nil {
		t.Fatalf("diff (attribute change): %v", err)
	}
	if len(changeOps) == 0 {
		t.Fatal("expected a CREATE OR REPLACE FUNCTION op for the PARALLEL/COST change, got none")
	}
	var sql string
	for _, op := range changeOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "f_explicit") {
		t.Errorf("expected the recreate op to target f_explicit, got: %s", sql)
	}
	if !strings.Contains(sql, "COST 600") {
		t.Errorf("expected the recreate to reflect the new COST 600, got: %s", sql)
	}
	if strings.Contains(sql, "PARALLEL") {
		t.Errorf("expected no PARALLEL clause (UNSAFE is PostgreSQL's own default), got: %s", sql)
	}

	migration, err := emitter.Emit(changeOps, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (attribute change) against live PostgreSQL: %v", err)
	}
	rs2, err := conn.QueryRows(ctx, `SELECT procost FROM pg_proc WHERE proname = 'f_explicit'`)
	if err != nil {
		t.Fatalf("query updated procost: %v", err)
	}
	defer rs2.Close()
	var newCost float64
	if !rs2.Next() {
		t.Fatal("expected a row for f_explicit")
	}
	if err := rs2.Scan(&newCost); err != nil {
		t.Fatalf("scan updated procost: %v", err)
	}
	if newCost != 600 {
		t.Errorf("live procost after apply = %v, want 600", newCost)
	}
}

// TestRoundtripFunctionSQLBodyReformattingNoSpuriousDrift proves
// ir.HashFunctionBody's LANGUAGE SQL canonicalisation (RFC Section 9.5) actually
// closes the spurious-drift gap end to end: a LANGUAGE SQL function is
// applied, then its source is edited to a cosmetically-reformatted (case,
// whitespace) but semantically identical body — re-diffing against the
// snapshot from the first apply must show zero ops, not a spurious
// CREATE OR REPLACE FUNCTION. A genuinely different body must still be
// detected, proving the fix doesn't just suppress every function diff.
func TestRoundtripFunctionSQLBodyReformattingNoSpuriousDrift(t *testing.T) {
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
	original := `FUNCTION f_sql(n integer) RETURNS integer
LANGUAGE sql AS $$
    SELECT   n + 1;
$$ {}`
	if err := os.WriteFile(f, []byte(original), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Cosmetically reformatted (case, whitespace) but semantically identical.
	reformatted := `FUNCTION f_sql(n integer) RETURNS integer
LANGUAGE sql AS $$
select n+1;
$$ {}`
	if err := os.WriteFile(f, []byte(reformatted), 0o644); err != nil {
		t.Fatalf("write reformatted: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile reformatted: %v", err)
	}
	base, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired, base)
	if err != nil {
		t.Fatalf("diff (reformatted): %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a cosmetically-reformatted-but-equivalent LANGUAGE SQL body, got %d:", len(ops))
		for _, op := range ops {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// Genuinely different body (even though also reformatted) must still be
	// detected — the canonicalisation must not blind the differ entirely.
	changed := `FUNCTION f_sql(n integer) RETURNS integer
LANGUAGE sql AS $$
select n+2;
$$ {}`
	if err := os.WriteFile(f, []byte(changed), 0o644); err != nil {
		t.Fatalf("write changed: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile changed: %v", err)
	}
	base2, _ := store.Load("test", "dpgtest")
	changeOps, err := differ.Diff(desired2, base2)
	if err != nil {
		t.Fatalf("diff (changed): %v", err)
	}
	if len(changeOps) == 0 {
		t.Fatal("expected a CREATE OR REPLACE FUNCTION op for a genuine body change, got none")
	}
	if !strings.Contains(changeOps[0].SQL(), "CREATE OR REPLACE FUNCTION") {
		t.Errorf("expected CREATE OR REPLACE FUNCTION, got: %s", changeOps[0].SQL())
	}
}

// TestRoundtripFunctionPlpgsqlBodyReformattingNoSpuriousDrift is
// TestRoundtripFunctionSQLBodyReformattingNoSpuriousDrift's plpgsql sibling,
// exercising the builder-side half of ir.HashFunctionBody's plpgsql
// canonicalisation (both "desired" computations below go through
// buildFunction/HashFunctionBody with a real fullStatement — see RFC Section 9.5).
// The body deliberately assigns to its own declared parameter ("n := n +
// 1"), the case the whole shim design exists for: the PL/pgSQL compiler
// must resolve "n" against the function's own argument list at compile
// time, so a shim with an inaccurate argument list would fail to compile
// and silently fall back to raw hashing, defeating the fix for exactly this
// common case. The introspect-side half (live catalog, not source-vs-
// snapshot) is covered separately by
// TestRoundtripFunctionPlpgsqlBodyReformattingLiveNoSpuriousDrift below.
func TestRoundtripFunctionPlpgsqlBodyReformattingNoSpuriousDrift(t *testing.T) {
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
	original := `FUNCTION f_pl(n integer) RETURNS integer
LANGUAGE plpgsql AS $$
BEGIN
    n := n + 1;
    RETURN n;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(original), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Cosmetically reformatted (blank lines, a comment) but semantically identical.
	reformatted := `FUNCTION f_pl(n integer) RETURNS integer
LANGUAGE plpgsql AS $$
BEGIN

    -- bump n by one
    n := n + 1;

    RETURN n;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(reformatted), 0o644); err != nil {
		t.Fatalf("write reformatted: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile reformatted: %v", err)
	}
	base, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired, base)
	if err != nil {
		t.Fatalf("diff (reformatted): %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a cosmetically-reformatted-but-equivalent plpgsql body, got %d:", len(ops))
		for _, op := range ops {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// Genuinely different body (even though also reformatted) must still be
	// detected — the canonicalisation must not blind the differ entirely.
	changed := `FUNCTION f_pl(n integer) RETURNS integer
LANGUAGE plpgsql AS $$
BEGIN
    n := n + 2;
    RETURN n;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(changed), 0o644); err != nil {
		t.Fatalf("write changed: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile changed: %v", err)
	}
	base2, _ := store.Load("test", "dpgtest")
	changeOps, err := differ.Diff(desired2, base2)
	if err != nil {
		t.Fatalf("diff (changed): %v", err)
	}
	if len(changeOps) == 0 {
		t.Fatal("expected a CREATE OR REPLACE FUNCTION op for a genuine body change, got none")
	}
	if !strings.Contains(changeOps[0].SQL(), "CREATE OR REPLACE FUNCTION") {
		t.Errorf("expected CREATE OR REPLACE FUNCTION, got: %s", changeOps[0].SQL())
	}
}

// TestRoundtripFunctionPlpgsqlBodyReformattingLiveNoSpuriousDrift is the
// test that actually proves the introspect-side half of the plpgsql
// body-hash fix works: unlike the snapshot-only test above (which never
// touches internal/introspect), this applies a function, edits only the
// *source* file cosmetically (the live catalog body is never re-applied,
// so the live function keeps its original, unreformatted text), then
// introspects the live function and diffs desired (from the reformatted
// source) against a fresh live snapshot — proving
// introspectFunctionArgs + ir.RenderCreateFunctionSQL + HashFunctionBody
// together reconstruct an argument-accurate shim from the catalog that
// canonicalizes to the same hash as the source side.
func TestRoundtripFunctionPlpgsqlBodyReformattingLiveNoSpuriousDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	original := `FUNCTION f_pl_live(n integer) RETURNS integer
LANGUAGE plpgsql AS $$
BEGIN
    n := n + 1;
    RETURN n;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(original), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	liveSnapFor := func() *pipeline.Snapshot {
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
		return liveSnap
	}

	// Source-only cosmetic reformatting — the live catalog body is untouched.
	reformatted := `FUNCTION f_pl_live(n integer) RETURNS integer
LANGUAGE plpgsql AS $$
BEGIN

    -- bump n by one
    n := n + 1;

    RETURN n;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(reformatted), 0o644); err != nil {
		t.Fatalf("write reformatted: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile reformatted: %v", err)
	}
	driftOps, err := differ.Diff(desired, liveSnapFor())
	if err != nil {
		t.Fatalf("drift diff (reformatted): %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero live drift for a cosmetically-reformatted-but-equivalent plpgsql body, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// Genuine change must still be detected against the live catalog.
	changed := `FUNCTION f_pl_live(n integer) RETURNS integer
LANGUAGE plpgsql AS $$
BEGIN
    n := n + 2;
    RETURN n;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(changed), 0o644); err != nil {
		t.Fatalf("write changed: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile changed: %v", err)
	}
	changeOps, err := differ.Diff(desired2, liveSnapFor())
	if err != nil {
		t.Fatalf("drift diff (changed): %v", err)
	}
	if len(changeOps) == 0 {
		t.Fatal("expected a CREATE OR REPLACE FUNCTION op for a genuine body change against the live catalog, got none")
	}
	if !strings.Contains(changeOps[0].SQL(), "CREATE OR REPLACE FUNCTION") {
		t.Errorf("expected CREATE OR REPLACE FUNCTION, got: %s", changeOps[0].SQL())
	}
}

// TestRoundtripFunctionPlpgsqlEmbeddedExpressionReformattingLiveNoSpuriousDrift
// is TestRoundtripFunctionPlpgsqlBodyReformattingLiveNoSpuriousDrift's
// sibling for the embedded-expression canonicalisation gap: instead of
// reformatting the outer control-flow shape (blank lines/comments around
// statements), this reformats *inside* an expression fragment itself
// (removing whitespace around concatenation operators) — the exact
// scenario the original plpgsql body-hash fix didn't cover, closed by
// canonicalizePlpgsqlExprFragments re-parsing/re-deparsing each
// PLpgSQL_expr fragment via github.com/thec1oud/dpg_query_go's
// raw-parse-mode entry points.
func TestRoundtripFunctionPlpgsqlEmbeddedExpressionReformattingLiveNoSpuriousDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	original := `FUNCTION f_pl_expr_live(v_name text, v_version text) RETURNS text
LANGUAGE plpgsql AS $$
BEGIN
    RETURN v_name || '/' || v_version;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(original), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	liveSnapFor := func() *pipeline.Snapshot {
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
		return liveSnap
	}

	// Source-only reformatting, and *only* inside the RETURN expression —
	// the live catalog body is untouched.
	reformatted := `FUNCTION f_pl_expr_live(v_name text, v_version text) RETURNS text
LANGUAGE plpgsql AS $$
BEGIN
    RETURN v_name||'/'||v_version;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(reformatted), 0o644); err != nil {
		t.Fatalf("write reformatted: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile reformatted: %v", err)
	}
	driftOps, err := differ.Diff(desired, liveSnapFor())
	if err != nil {
		t.Fatalf("drift diff (reformatted): %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero live drift for whitespace reformatted only inside an embedded expression, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// A genuine change inside the same expression must still be detected.
	changed := `FUNCTION f_pl_expr_live(v_name text, v_version text) RETURNS text
LANGUAGE plpgsql AS $$
BEGIN
    RETURN v_name || '-' || v_version;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(changed), 0o644); err != nil {
		t.Fatalf("write changed: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile changed: %v", err)
	}
	changeOps, err := differ.Diff(desired2, liveSnapFor())
	if err != nil {
		t.Fatalf("drift diff (changed): %v", err)
	}
	if len(changeOps) == 0 {
		t.Fatal("expected a CREATE OR REPLACE FUNCTION op for a genuine change inside an embedded expression against the live catalog, got none")
	}
	if !strings.Contains(changeOps[0].SQL(), "CREATE OR REPLACE FUNCTION") {
		t.Errorf("expected CREATE OR REPLACE FUNCTION, got: %s", changeOps[0].SQL())
	}
}

// TestRoundtripProcedurePlpgsqlBodyReformattingLiveNoSpuriousDrift is
// TestRoundtripFunctionPlpgsqlBodyReformattingLiveNoSpuriousDrift's
// PROCEDURE sibling. Procedure shares HashFunctionBody's exact call path
// (buildProcedure/introspect's procedure branch both call it identically to
// Function) but has its own RenderCreateProcedureSQL shim renderer — this
// is the only test that would catch that renderer diverging from
// RenderCreateFunctionSQL's correctness (e.g. a wrong argument-list shape
// that silently fails plpgsql compilation and falls back to raw hashing).
func TestRoundtripProcedurePlpgsqlBodyReformattingLiveNoSpuriousDrift(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	original := `PROCEDURE p_pl_live(n integer) LANGUAGE plpgsql AS $$
BEGIN
    n := n + 1;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(original), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	liveSnapFor := func() *pipeline.Snapshot {
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
		return liveSnap
	}

	reformatted := `PROCEDURE p_pl_live(n integer) LANGUAGE plpgsql AS $$
BEGIN

    -- bump n by one
    n := n + 1;

END;
$$ {}`
	if err := os.WriteFile(f, []byte(reformatted), 0o644); err != nil {
		t.Fatalf("write reformatted: %v", err)
	}
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile reformatted: %v", err)
	}
	driftOps, err := differ.Diff(desired, liveSnapFor())
	if err != nil {
		t.Fatalf("drift diff (reformatted): %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero live drift for a cosmetically-reformatted-but-equivalent plpgsql procedure body, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	changed := `PROCEDURE p_pl_live(n integer) LANGUAGE plpgsql AS $$
BEGIN
    n := n + 2;
END;
$$ {}`
	if err := os.WriteFile(f, []byte(changed), 0o644); err != nil {
		t.Fatalf("write changed: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile changed: %v", err)
	}
	changeOps, err := differ.Diff(desired2, liveSnapFor())
	if err != nil {
		t.Fatalf("drift diff (changed): %v", err)
	}
	if len(changeOps) == 0 {
		t.Fatal("expected a CREATE OR REPLACE PROCEDURE op for a genuine body change against the live catalog, got none")
	}
	if !strings.Contains(changeOps[0].SQL(), "CREATE OR REPLACE PROCEDURE") {
		t.Errorf("expected CREATE OR REPLACE PROCEDURE, got: %s", changeOps[0].SQL())
	}
}

// TestRoundtripFunctionSetOf covers RETURNS SETOF fidelity end to end: a
// set-returning function (also declaring ROWS, only valid together with
// SETOF — closing the gap TestRoundtripFunctionParallelCostRows had to
// document and scope around) and a plain scalar function, confirming both
// round-trip with zero drift against a live catalog. ReturnType.SetOf
// (pg_query's TypeName.Setof field) was previously never read anywhere in
// the codebase, so DPG silently dropped SETOF from every function it
// compiled — meaning a function declared as SETOF in DPG source was actually
// being created live as a plain scalar function.
//
// Also exercises the return-type-change code path specifically: confirmed
// live against postgres:17 that PostgreSQL rejects CREATE OR REPLACE
// FUNCTION outright when the return type changes ("cannot change return type
// of existing function"), so toggling SETOF on an existing function must
// diff to a DROP FUNCTION + CREATE FUNCTION pair, not a plain
// CREATE OR REPLACE — this was never diffed at all before (ReturnType had no
// comparison in diffFunction whatsoever), a genuinely separate, pre-existing
// gap found while wiring SetOf through the differ.
func TestRoundtripFunctionSetOf(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FUNCTION f_setof(n integer) RETURNS SETOF integer
LANGUAGE sql ROWS 50 AS $$
    SELECT generate_series(1, n);
$$ {}

FUNCTION f_plain(n integer) RETURNS integer
LANGUAGE sql AS $$
    SELECT n;
$$ {}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

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

	// Confirm PostgreSQL actually recorded a genuine SETOF function — a
	// guard against the zero-drift check below passing for the wrong reason
	// (e.g. SETOF silently dropped again but nothing noticing).
	rs, err := conn.QueryRows(ctx, `
SELECT proname, proretset, prorows
FROM pg_proc WHERE proname IN ('f_setof', 'f_plain') ORDER BY proname`)
	if err != nil {
		t.Fatalf("query pg_proc: %v", err)
	}
	defer rs.Close()
	type attrs struct {
		retset bool
		rows   float64
	}
	got := map[string]attrs{}
	for rs.Next() {
		var name string
		var retset bool
		var rows float64
		if err := rs.Scan(&name, &retset, &rows); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = attrs{retset, rows}
	}
	if got["f_setof"] != (attrs{true, 50}) {
		t.Errorf("f_setof pg_proc attrs: got %+v, want {true 50}", got["f_setof"])
	}
	if got["f_plain"] != (attrs{false, 0}) {
		t.Errorf("f_plain pg_proc attrs: got %+v, want {false 0}", got["f_plain"])
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for SETOF/ROWS (explicit and default) against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// Toggle f_plain to SETOF and confirm this diffs to a real DROP + CREATE
	// pair (not a plain CREATE OR REPLACE, which PostgreSQL would reject).
	v2 := `FUNCTION f_setof(n integer) RETURNS SETOF integer
LANGUAGE sql ROWS 50 AS $$
    SELECT generate_series(1, n);
$$ {}

FUNCTION f_plain(n integer) RETURNS SETOF integer
LANGUAGE sql AS $$
    SELECT generate_series(1, n);
$$ {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap2, _ := store.Load("test", "dpgtest")
	changeOps, err := differ.Diff(desired2, snap2)
	if err != nil {
		t.Fatalf("diff (return-type change): %v", err)
	}
	var sql string
	for _, op := range changeOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP FUNCTION") {
		t.Errorf("expected a DROP FUNCTION op for the SETOF toggle, got: %s", sql)
	}
	if !strings.Contains(sql, "RETURNS SETOF integer") {
		t.Errorf("expected the recreate to declare RETURNS SETOF integer, got: %s", sql)
	}

	migration, err := emitter.Emit(changeOps, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (SETOF toggle) against live PostgreSQL: %v", err)
	}
	rs2, err := conn.QueryRows(ctx, `SELECT proretset FROM pg_proc WHERE proname = 'f_plain'`)
	if err != nil {
		t.Fatalf("query updated proretset: %v", err)
	}
	defer rs2.Close()
	var nowRetset bool
	if !rs2.Next() {
		t.Fatal("expected a row for f_plain")
	}
	if err := rs2.Scan(&nowRetset); err != nil {
		t.Fatalf("scan updated proretset: %v", err)
	}
	if !nowRetset {
		t.Error("live proretset for f_plain after apply = false, want true")
	}
}

// TestRoundtripFunctionReturnsTable covers RETURNS TABLE(...) fidelity end to
// end. Args entries with Mode "TABLE" were previously rendered inline in the
// main parameter list as an invalid "TABLE a integer" literal (a genuine,
// separate, pre-existing bug found while building SETOF support), and
// introspection built Args purely from oidvectortypes(proargtypes), which —
// like pg_get_function_identity_arguments — never reports OUT/TABLE-mode
// columns at all, so a RETURNS TABLE function's output columns were silently
// missing from introspected Args entirely, not just mis-rendered.
func TestRoundtripFunctionReturnsTable(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FUNCTION f_table(n integer) RETURNS TABLE(a integer)
LANGUAGE sql AS $$
    SELECT n;
$$ {}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

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

	// Confirm PostgreSQL actually recorded a genuine RETURNS TABLE function
	// (not, say, a plain scalar one from SETOF/TABLE silently being dropped
	// again) — a guard against the zero-drift check below passing for the
	// wrong reason.
	rs, err := conn.QueryRows(ctx, `SELECT pg_get_function_result(oid) FROM pg_proc WHERE proname = 'f_table'`)
	if err != nil {
		t.Fatalf("query pg_get_function_result: %v", err)
	}
	var resultText string
	if !rs.Next() {
		t.Fatal("expected a row for f_table")
	}
	if err := rs.Scan(&resultText); err != nil {
		t.Fatalf("scan: %v", err)
	}
	rs.Close()
	if resultText != "TABLE(a integer)" {
		t.Fatalf("pg_get_function_result: got %q, want TABLE(a integer)", resultText)
	}

	// Confirm introspection itself actually captured the TABLE-mode column
	// (not just that PostgreSQL created it correctly) — this is the specific
	// gap this fix closes.
	var introFn *ir.Function
	for _, obj := range liveObjects {
		if fn, ok := obj.(*ir.Function); ok && fn.Name == "f_table" {
			introFn = fn
		}
	}
	if introFn == nil {
		t.Fatal("introspect: function public.f_table not found")
	}
	if got := ir.FormatTableColumns(ir.FuncTableColumns(introFn.Args)); got != "a integer" {
		t.Errorf("introspected f_table TABLE columns: got %q, want %q", got, "a integer")
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for RETURNS TABLE against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}

	// Rename the TABLE column (same type, same body) and confirm this diffs
	// to a real DROP + CREATE pair. This isolates the ReturnTable comparison
	// specifically: a plain column-list edit that leaves the body's SQL text
	// completely unchanged means BodyHash stays identical, so if only
	// ReturnTable were compared incorrectly (or not at all), nothing else
	// would catch this change — confirmed live that PostgreSQL validates
	// column COUNT at CREATE time for a SQL-language function ("return type
	// mismatch ... too few columns"), which is why a rename (not an added
	// column) is used here to keep the body byte-identical.
	v2 := `FUNCTION f_table(n integer) RETURNS TABLE(a2 integer)
LANGUAGE sql AS $$
    SELECT n;
$$ {}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	snap2, _ := store.Load("test", "dpgtest")
	changeOps, err := differ.Diff(desired2, snap2)
	if err != nil {
		t.Fatalf("diff (column-list change): %v", err)
	}
	var sql string
	for _, op := range changeOps {
		sql += op.SQL()
	}
	if !strings.Contains(sql, "DROP FUNCTION") {
		t.Errorf("expected a DROP FUNCTION op for the TABLE column rename, got: %s", sql)
	}
	if !strings.Contains(sql, "RETURNS TABLE(a2 integer)") {
		t.Errorf("expected the recreate to declare RETURNS TABLE(a2 integer), got: %s", sql)
	}

	migration, err := emitter.Emit(changeOps, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply (column-list change) against live PostgreSQL: %v", err)
	}
	// Query via a proname filter, not a '...'::regproc cast: pgx's extended
	// query protocol caches prepared statements by exact SQL text, and this
	// exact text was already used (pre-apply) above — a regproc cast folds to
	// a constant OID at prepare time, so re-executing the identical statement
	// after DROP+CREATE (a new OID) would silently return NULL for the old,
	// now-nonexistent OID rather than re-resolving the name. Not a product
	// bug: confirmed by re-running the same regproc-cast query with oid-only
	// debug output and seeing the stale OID, while a fresh proname-based
	// query in the same session saw the correct new one.
	rs2, err := conn.QueryRows(ctx, `SELECT pg_get_function_result(oid) FROM pg_proc WHERE proname = 'f_table'`)
	if err != nil {
		t.Fatalf("query updated pg_get_function_result: %v", err)
	}
	defer rs2.Close()
	if !rs2.Next() {
		t.Fatal("expected a row for f_table after apply")
	}
	var newResultText string
	if err := rs2.Scan(&newResultText); err != nil {
		t.Fatalf("scan updated pg_get_function_result: %v", err)
	}
	if newResultText != "TABLE(a2 integer)" {
		t.Errorf("live pg_get_function_result after apply = %q, want TABLE(a2 integer)", newResultText)
	}
}

// TestRoundtripFunctionArgModes covers OUT/INOUT/VARIADIC argument fidelity
// end to end. Found while fixing RETURNS TABLE(...) introspection:
// oidvectortypes(proargtypes) — the source introspectFunctions used for
// Args — never reports OUT-mode arguments at all, and never carries mode
// information for any argument, so a plain OUT-only function's OUT columns
// were completely missing from introspected Args (the same severity as the
// RETURNS TABLE bug), and an INOUT/VARIADIC function's mode keyword was
// silently lost even though its type was captured correctly. This exercises
// the full pipeline (apply from DPG source, introspect back, zero drift),
// not just introspection in isolation.
func TestRoundtripFunctionArgModes(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	// RETURNS is explicit here (PostgreSQL allows omitting it for an
	// all-OUT/INOUT signature — the type is then implied; now fixed and
	// covered separately by TestRoundtripFunctionImplicitReturns) to keep
	// this test's scope isolated to Args-mode capture specifically.
	fixture := `FUNCTION f_out(n integer, OUT a integer, OUT b text) RETURNS record
LANGUAGE sql AS $$
    SELECT n, 'x';
$$ {}

FUNCTION f_inout(INOUT n integer) RETURNS integer
LANGUAGE sql AS $$
    SELECT n;
$$ {}

FUNCTION f_variadic(VARIADIC n integer[]) RETURNS integer
LANGUAGE sql AS $$
    SELECT 1;
$$ {}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

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

	// Confirm introspection itself actually captured the OUT columns and
	// INOUT/VARIADIC modes (not just that PostgreSQL created the functions
	// correctly) — the specific gap this fix closes.
	byName := map[string]*ir.Function{}
	for _, obj := range liveObjects {
		if fn, ok := obj.(*ir.Function); ok {
			byName[fn.Name] = fn
		}
	}
	fOut := byName["f_out"]
	if fOut == nil {
		t.Fatal("introspect: function public.f_out not found")
	}
	if len(fOut.Args) != 3 {
		t.Fatalf("introspected f_out: got %d args, want 3 (n, a, b)", len(fOut.Args))
	}
	fInout := byName["f_inout"]
	if fInout == nil || len(fInout.Args) != 1 || fInout.Args[0].Mode != "INOUT" {
		t.Errorf("introspected f_inout: got %+v, want a single Mode=INOUT arg", fInout)
	}
	fVariadic := byName["f_variadic"]
	if fVariadic == nil || len(fVariadic.Args) != 1 || fVariadic.Args[0].Mode != "VARIADIC" {
		t.Errorf("introspected f_variadic: got %+v, want a single Mode=VARIADIC arg", fVariadic)
	}

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for OUT/INOUT/VARIADIC functions against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripFunctionImplicitReturns exercises the omitted-RETURNS form
// directly (TestRoundtripFunctionArgModes above deliberately worked around
// it with an explicit RETURNS clause, flagging the gap instead of fixing
// it) — PostgreSQL allows omitting RETURNS entirely for a signature with at
// least one OUT/INOUT parameter, the return type then being implied
// (confirmed live: single OUT/INOUT param -> that param's own type; more
// than one -> "record"). Before ir.Builder's impliedReturnType fix, the
// desired-side ir.Function.ReturnType stayed the zero TypeRef for this
// input, so buildFunctionSQL's CREATE would itself have been a syntax error
// ("RETURNS  LANGUAGE ...") — this test proves apply succeeds AND that the
// implied type matches what live introspection reports (zero drift), not
// just that PostgreSQL's own implicit-RETURNS inference works.
func TestRoundtripFunctionImplicitReturns(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	fixture := `FUNCTION f_single_out(n integer, OUT a integer)
LANGUAGE sql AS $$
    SELECT n + 1;
$$ {}

FUNCTION f_multi_out(n integer, OUT a integer, OUT b text)
LANGUAGE sql AS $$
    SELECT n, 'x';
$$ {}

FUNCTION f_inout_implicit(INOUT n integer)
LANGUAGE sql AS $$
    SELECT n + 1;
$$ {}`
	if err := os.WriteFile(f, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

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

	// Confirm what real PostgreSQL actually implied, directly from the
	// catalog, so a bug in this test's own expectations can't mask a bug in
	// the fix (or vice versa).
	assertFuncResultType := func(funcName, want string) {
		t.Helper()
		rs, err := conn.QueryRows(ctx, `SELECT pg_get_function_result(oid) FROM pg_proc WHERE proname = $1`, funcName)
		if err != nil {
			t.Fatalf("query pg_get_function_result for %s: %v", funcName, err)
		}
		defer rs.Close()
		if !rs.Next() {
			t.Fatalf("expected a row for %s", funcName)
		}
		var got string
		if err := rs.Scan(&got); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if got != want {
			t.Errorf("%s live result type: got %q, want %q", funcName, got, want)
		}
	}
	assertFuncResultType("f_single_out", "integer")
	assertFuncResultType("f_multi_out", "record")
	assertFuncResultType("f_inout_implicit", "integer")

	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("expected zero drift for implicit-RETURNS functions against a live catalog, got %d ops:", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}
