//go:build integration

// Package integration runs end-to-end tests against a live PostgreSQL instance
// managed by testcontainers. The full DPG pipeline — compile → plan → apply →
// introspect → verify zero drift — is exercised for each scenario.
package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"

	_ "github.com/thec1oud/dpg/internal/blockparser"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	_ "github.com/thec1oud/dpg/internal/scanner"
)

// testdataFile returns the absolute path to a file under integration/testdata/.
func testdataFile(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

// memStore is a trivial in-memory SnapshotStore used in tests to avoid
// touching the filesystem.
type memStore struct {
	snaps map[string]*pipeline.Snapshot
}

func newMemStore() *memStore { return &memStore{snaps: map[string]*pipeline.Snapshot{}} }

func (m *memStore) Load(cluster, database string) (*pipeline.Snapshot, error) {
	if s, ok := m.snaps[cluster+"/"+database]; ok {
		// Deep-copy via JSON so callers can't mutate stored state.
		data, _ := json.Marshal(s)
		var out pipeline.Snapshot
		_ = json.Unmarshal(data, &out)
		return &out, nil
	}
	return &pipeline.Snapshot{}, nil
}

func (m *memStore) Save(cluster, database string, snap *pipeline.Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	var out pipeline.Snapshot
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	m.snaps[cluster+"/"+database] = &out
	return nil
}

var _ pipeline.SnapshotStore = (*memStore)(nil)

// TestRoundtrip compiles the testdata/schema.dpg fixture, applies it to a
// fresh Postgres database, introspects the live catalog, then verifies that
// diffing the desired IR against the live snapshot produces zero operations.
func TestRoundtrip(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	schemaFile := testdataFile("schema.dpg")
	schemaDir := filepath.Dir(schemaFile)

	// ── Step 1: Compile desired state ────────────────────────────────────────
	desired, _, err := compiler.Compile([]string{schemaFile}, schemaDir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// ── Step 2: Plan against empty snapshot ──────────────────────────────────
	emptySnap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired, emptySnap)
	if err != nil {
		t.Fatalf("diff (initial): %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("expected ops against empty snapshot, got none")
	}
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			t.Errorf("initial plan contains unexpected DESTRUCTIVE op: %s", op.SQL())
		}
	}

	// ── Step 3: Apply to the container DB ────────────────────────────────────
	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Persist snapshot representing the applied state.
	appliedSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(appliedSnap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", appliedSnap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// ── Step 4: Introspect live DB ────────────────────────────────────────────
	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	// Only include introspected objects that DPG applied (ignore pre-existing
	// infrastructure like the container superuser role).
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

	// ── Step 5: Diff desired vs live — must be zero ops ───────────────────────
	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift detected after apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripAddColumn verifies that adding a column to an already-applied
// schema produces only SAFE ops, applies cleanly, and leaves zero drift.
func TestRoundtripAddColumn(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	schemaFile := testdataFile("schema.dpg")
	schemaDir := filepath.Dir(schemaFile)

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Apply the base schema.
	applyFixture(t, ctx, conn, []string{schemaFile}, schemaDir, differ, emitter, applyExec, store)

	// Write an extended schema with an extra column added to users.
	extendedSchema := `TYPE status AS ENUM ('active', 'inactive', 'pending');

TABLE users (
    id          bigint GENERATED ALWAYS AS IDENTITY,
    name        text NOT NULL,
    email       text NOT NULL,
    status      status NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT users_email_key UNIQUE (email)
) {
    INDICES { idx_users_status (status); }
}

TABLE posts (
    id      bigint GENERATED ALWAYS AS IDENTITY,
    user_id bigint NOT NULL,
    title   text NOT NULL,
    body    text,
    CONSTRAINT posts_pkey PRIMARY KEY (id),
    CONSTRAINT posts_user_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT posts_title_nonempty CHECK (char_length(title) > 0)
) {}

VIEW active_users AS
    SELECT id, name, email FROM users WHERE status = 'active';
`
	dir := t.TempDir()
	extFile := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(extFile, []byte(extendedSchema), 0o644); err != nil {
		t.Fatalf("write extended schema: %v", err)
	}

	desired2, _, err := compiler.Compile([]string{extFile}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile extended: %v", err)
	}

	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (add column): %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("expected ops for added column, got none")
	}
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			t.Errorf("add-column plan contains DESTRUCTIVE op: %s", op.SQL())
		}
	}

	migration2, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration2, conn); err != nil {
		t.Fatalf("apply (add column): %v", err)
	}

	// Zero drift after add-column apply.
	liveObjects2, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	snap2, _ := store.Load("test", "dpgtest")
	var managedLive2 []pipeline.IRObject
	for _, obj := range liveObjects2 {
		if _, ok := snap2.Objects[obj.QualifiedName()]; ok {
			managedLive2 = append(managedLive2, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive2); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after add-column apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripEnumAddValue verifies that adding a new value to an ENUM type
// produces an ALTER TYPE ADD VALUE op, applies without error, and leaves zero
// drift when re-introspected.
func TestRoundtripEnumAddValue(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
	store := newMemStore()

	schemaFile := testdataFile("schema.dpg")
	schemaDir := filepath.Dir(schemaFile)

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Apply the base schema first.
	applyFixture(t, ctx, conn, []string{schemaFile}, schemaDir, differ, emitter, applyExec, store)

	// Schema with an extra enum value added.
	extendedSchema := `TYPE status AS ENUM ('active', 'inactive', 'pending', 'archived');

TABLE users (
    id      bigint GENERATED ALWAYS AS IDENTITY,
    name    text NOT NULL,
    email   text NOT NULL,
    status  status NOT NULL DEFAULT 'active',
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT users_email_key UNIQUE (email)
) {
    INDICES { idx_users_status (status); }
}

TABLE posts (
    id      bigint GENERATED ALWAYS AS IDENTITY,
    user_id bigint NOT NULL,
    title   text NOT NULL,
    body    text,
    CONSTRAINT posts_pkey PRIMARY KEY (id),
    CONSTRAINT posts_user_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT posts_title_nonempty CHECK (char_length(title) > 0)
) {}

VIEW active_users AS
    SELECT id, name, email FROM users WHERE status = 'active';
`
	dir := t.TempDir()
	extFile := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(extFile, []byte(extendedSchema), 0o644); err != nil {
		t.Fatalf("write extended schema: %v", err)
	}

	desired2, _, err := compiler.Compile([]string{extFile}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile extended: %v", err)
	}

	snap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, snap)
	if err != nil {
		t.Fatalf("diff (add enum value): %v", err)
	}

	// Must produce at least one ALTER TYPE ADD VALUE op.
	var hasAddValue bool
	for _, op := range ops {
		if strings.Contains(op.SQL(), "ADD VALUE") {
			hasAddValue = true
		}
	}
	if !hasAddValue {
		t.Fatal("expected ALTER TYPE ADD VALUE op for new enum value, got none")
	}

	migration2, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration2, conn); err != nil {
		t.Fatalf("apply (add enum value): %v", err)
	}

	// Save updated snapshot.
	snap2 := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap2, desired2); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", snap2); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// Zero drift after enum add-value apply.
	liveObjects2, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	var managedLive2 []pipeline.IRObject
	for _, obj := range liveObjects2 {
		if _, ok := snap2.Objects[obj.QualifiedName()]; ok {
			managedLive2 = append(managedLive2, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive2); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	driftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after enum add-value apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripEnumBareShorthand is the regression guard for RFC Section
// 5.1's documented "ENUM name ('a', 'b', ...);" shorthand — the form used
// throughout this specification (including Appendix B's worked example) and
// by lang/grammar's tree-sitter grammar, distinct from the generic "TYPE
// name AS ENUM (...)" form every other roundtrip test in this file uses.
// readPart1's KindEnum case in internal/scanner/scanner.go must synthesize
// the "AS ENUM" real PostgreSQL's CREATE TYPE grammar requires but DPG
// source never spells out; before that case existed, this exact source text
// failed to even compile ("PG parse error: syntax error at or near
// 'active'"), since Part 1 was reconstructed as the literally invalid
// "CREATE TYPE user_status ('active', ...)".
func TestRoundtripEnumBareShorthand(t *testing.T) {
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
	src := `ENUM user_status ('active', 'suspended', 'deleted') {
    COMMENT 'Account lifecycle states';
}`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	if err := snapshot.Populate(snap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
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
	if len(driftOps) != 0 {
		t.Errorf("drift after applying bare ENUM shorthand (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripFunction verifies that a function applies without error, that
// introspection returns a matching body hash, and that a second plan produces
// zero drift ops.
func TestRoundtripFunction(t *testing.T) {
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

	schema := `FUNCTION add_one(n integer) RETURNS integer
LANGUAGE sql AS $$
    SELECT n + 1;
$$ {}`

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
		t.Errorf("drift after function apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripFunctionSecurityDefinerSetSearchPath is the regression guard
// for the Phase 3 launch-readiness audit's FUNCTION SET finding: RFC
// Section 9.2 documents `SET param = value`/`SET param FROM CURRENT` as
// normative — including a SHOULD-level security recommendation that a
// SECURITY DEFINER function use it to set search_path against search-path
// injection — but FuncAttrs had no Config field at all, so this exact
// shape (taken directly from Appendix B's own worked example) silently
// dropped its declared SET search_path on every compile before this fix.
func TestRoundtripFunctionSecurityDefinerSetSearchPath(t *testing.T) {
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

	schema := `FUNCTION get_one() RETURNS integer
LANGUAGE sql SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    SELECT 1;
$$ {}

PROCEDURE do_thing()
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    NULL;
END;
$$ {}`

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(schema), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Confirm the SET clause actually landed live, not just that apply
	// didn't error — proconfig/prosecdef are the real proof, matching this
	// project's "verify counts, don't eyeball" standard.
	proProps := func(name string) (config []string, secDef bool) {
		t.Helper()
		rows, err := conn.QueryRows(ctx, "SELECT proconfig, prosecdef FROM pg_proc WHERE proname = $1", name)
		if err != nil {
			t.Fatalf("query pg_proc for %s: %v", name, err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("pg_proc has no row for %s", name)
		}
		if err := rows.Scan(&config, &secDef); err != nil {
			t.Fatalf("scan pg_proc row for %s: %v", name, err)
		}
		return config, secDef
	}

	fnConfig, fnSecDef := proProps("get_one")
	if !fnSecDef {
		t.Error("expected get_one to be SECURITY DEFINER live")
	}
	if len(fnConfig) != 1 || fnConfig[0] != "search_path=public, pg_temp" {
		t.Errorf("expected get_one's live proconfig to be [search_path=public, pg_temp], got: %v", fnConfig)
	}
	procConfig, procSecDef := proProps("do_thing")
	if !procSecDef {
		t.Error("expected do_thing to be SECURITY DEFINER live")
	}
	if len(procConfig) != 1 || procConfig[0] != "search_path=public" {
		t.Errorf("expected do_thing's live proconfig to be [search_path=public], got: %v", procConfig)
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

	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	driftOps, err := differ.Diff(desired, liveSnap)
	if err != nil {
		t.Fatalf("drift diff: %v", err)
	}
	if len(driftOps) != 0 {
		t.Errorf("drift after SECURITY DEFINER/SET search_path apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripSequence verifies that a standalone sequence with explicit
// parameters applies cleanly and introspects back to the same params (zero
// drift after apply).
func TestRoundtripSequence(t *testing.T) {
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

	schema := `SEQUENCE order_seq INCREMENT BY 10 MINVALUE 10 MAXVALUE 10000 START WITH 10 CACHE 5;`

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
		t.Errorf("drift after sequence apply (%d ops):", len(driftOps))
		for _, op := range driftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// applyFixture compiles files, diffs against the current store snapshot, and
// applies the resulting migration. The store snapshot is updated on success.
func applyFixture(
	t *testing.T,
	ctx context.Context,
	conn *executor.PgxConn,
	files []string, dbDir string,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	applyExec pipeline.ApplyExecutor,
	store *memStore,
) {
	t.Helper()

	desired, _, err := compiler.Compile(files, dbDir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	base, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired, base)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := applyExec.Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply fixture: %v", err)
	}

	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, desired); err != nil {
		t.Fatalf("populate snapshot: %v", err)
	}
	if err := store.Save("test", "dpgtest", snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
}
