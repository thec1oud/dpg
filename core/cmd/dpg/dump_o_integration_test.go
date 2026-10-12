//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestDumpDashOSandboxesClusterOutput proves the `-o` fix: with -o given,
// `dpg dump` must never write cluster-scoped roles.dpg or the snapshot to
// the real project — both used to bypass -o entirely and land in the real
// project's cluster/ObjectsDir and the registered snapshot store regardless
// of where -o pointed. A live ROLE (cluster-scoped, so it's the object kind
// that specifically exercises the previously-broken write path) is applied
// against a real Postgres container, then dumped with -o pointed at a
// scratch directory; asserts roles.dpg and the snapshot both land under the
// scratch directory and that the real project's cluster ObjectsDir is never
// even created.
func TestDumpDashOSandboxesClusterOutput(t *testing.T) {
	ctx := context.Background()
	connStr := testpg.Start(t)
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// Apply a real cluster-scoped ROLE so introspection has something to
	// route into the cluster-scoped bucket (isClusterScoped) and trigger
	// the roles.dpg write path.
	roleDir := t.TempDir()
	roleFile := filepath.Join(roleDir, "roles.dpg")
	if err := os.WriteFile(roleFile, []byte(`ROLE dump_o_probe NOLOGIN;`), 0o644); err != nil {
		t.Fatalf("write role fixture: %v", err)
	}
	desired, _, err := compiler.Compile([]string{roleFile}, roleDir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile role fixture: %v", err)
	}
	ops, err := diff.New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	migration, err := emit.New().Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := executor.New().Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Real "project" locations that -o must never touch.
	realProjectDir := t.TempDir()
	realClusterDir := filepath.Join(realProjectDir, "cluster1")
	realObjectsDir := filepath.Join(realClusterDir, "cluster")

	cl := &project.Cluster{
		Dir: realClusterDir,
		Config: config.ClusterConfig{
			Cluster: config.ClusterDef{Name: "cluster1", URL: connStr},
		},
		ObjectsDir: realObjectsDir,
	}
	db := &project.Database{
		Dir: filepath.Join(realClusterDir, "db1"),
		// Name must be the database testpg's container actually has
		// ("dpgtest") — runDump now connects to this exact database by name
		// rather than whatever database happens to be embedded in the
		// cluster URL, so an unrelated placeholder name here would fail to
		// connect at all.
		Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "dpgtest"}},
	}

	// Scratch -o target, structurally separate from realProjectDir.
	scratchOut := t.TempDir()
	dbOut := filepath.Join(scratchOut, "db1")
	clusterOut := filepath.Join(scratchOut, cl.Name(), "cluster")
	scratchStore := &snapshot.FileStore{Dir: filepath.Join(scratchOut, ".dpg", "snapshots")}

	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	if err := runDump(cl, db, dbOut, clusterOut, introspect.New(), scratchStore, &mockSecretResolver{}, fmtOpts, true, dumpLogOptions{}); err != nil {
		t.Fatalf("runDump: %v", err)
	}

	// The real project's cluster ObjectsDir must never even be created.
	if _, statErr := os.Stat(realObjectsDir); !os.IsNotExist(statErr) {
		t.Errorf("real project cluster ObjectsDir %s must not be created by a sandboxed -o dump (stat err: %v)", realObjectsDir, statErr)
	}

	// roles.dpg must land under the scratch clusterOut instead.
	scratchRoles := filepath.Join(clusterOut, "roles.dpg")
	rolesContent, err := os.ReadFile(scratchRoles)
	if err != nil {
		t.Fatalf("expected roles.dpg at %s: %v", scratchRoles, err)
	}
	if !strings.Contains(string(rolesContent), "dump_o_probe") {
		t.Errorf("scratch roles.dpg missing the applied role, got: %s", rolesContent)
	}

	// The snapshot must land under the scratch store's directory, both for
	// the database and the cluster snapshot key.
	dbSnap, err := scratchStore.Load(cl.Name(), db.Name())
	if err != nil {
		t.Fatalf("load scratch db snapshot: %v", err)
	}
	if len(dbSnap.Objects) == 0 && dbSnap.DPGVersion == "" {
		t.Error("expected a populated db snapshot written to the scratch store")
	}
	clusterSnap, err := scratchStore.Load(cl.Name(), cl.ClusterSnapshotKey())
	if err != nil {
		t.Fatalf("load scratch cluster snapshot: %v", err)
	}
	if clusterSnap.DPGVersion == "" {
		t.Error("expected a populated cluster snapshot written to the scratch store")
	}

	// The real project's own .dpg/ directory (where its snapshot would live)
	// must never be created by a sandboxed -o dump either.
	if _, statErr := os.Stat(filepath.Join(realProjectDir, ".dpg")); !os.IsNotExist(statErr) {
		t.Errorf("real project .dpg dir must not be created by a sandboxed -o dump (stat err: %v)", statErr)
	}
}

// TestDumpRefusesToOverwriteWithoutConfirmation proves runDump's actual
// wiring of the overwrite-protection fix end to end, not just
// confirmOverwrite in isolation (already covered by dump_test.go's fast
// unit tests): re-dumping into a directory that already has a
// previously-dumped roles.dpg must (a) succeed silently with skipConfirm
// (--yes) true, genuinely overwriting the file, and (b) refuse and leave
// the file untouched when skipConfirm is false and the (real, temporarily
// swapped) os.Stdin declines the prompt — proving runDump reads from the
// real global os.Stdin, not a test-only seam, the same as an actual
// interactive user would.
func TestDumpRefusesToOverwriteWithoutConfirmation(t *testing.T) {
	ctx := context.Background()
	connStr := testpg.Start(t)
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	roleDir := t.TempDir()
	roleFile := filepath.Join(roleDir, "roles.dpg")
	if err := os.WriteFile(roleFile, []byte(`ROLE redump_probe NOLOGIN;`), 0o644); err != nil {
		t.Fatalf("write role fixture: %v", err)
	}
	desired, _, err := compiler.Compile([]string{roleFile}, roleDir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile role fixture: %v", err)
	}
	ops, err := diff.New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	migration, err := emit.New().Emit(ops, pipeline.MigrationMeta{Cluster: "test", Database: "dpgtest"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := executor.New().Apply(ctx, migration, conn); err != nil {
		t.Fatalf("apply: %v", err)
	}

	scratchOut := t.TempDir()
	dbOut := filepath.Join(scratchOut, "db1")
	clusterOut := filepath.Join(scratchOut, "cluster1", "cluster")
	store := &snapshot.FileStore{Dir: filepath.Join(scratchOut, ".dpg", "snapshots")}
	cl := &project.Cluster{
		Config: config.ClusterConfig{Cluster: config.ClusterDef{Name: "cluster1", URL: connStr}},
	}
	// Name must be the database testpg's container actually has ("dpgtest") —
	// see the comment on the equivalent Database literal above.
	db := &project.Database{Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "dpgtest"}}}
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}

	// First dump: nothing exists yet, must succeed with zero confirmation
	// (skipConfirm=false here specifically, to prove the frictionless-first-
	// dump guarantee holds even when confirmation isn't being bypassed).
	if err := runDump(cl, db, dbOut, clusterOut, introspect.New(), store, &mockSecretResolver{}, fmtOpts, false, dumpLogOptions{}); err != nil {
		t.Fatalf("first runDump (nothing pre-existing): %v", err)
	}
	rolesPath := filepath.Join(clusterOut, "roles.dpg")
	before, err := os.ReadFile(rolesPath)
	if err != nil {
		t.Fatalf("read roles.dpg after first dump: %v", err)
	}

	// Second dump, skipConfirm=true: roles.dpg now exists, must still
	// succeed (and does genuinely overwrite — same content here since
	// nothing changed live, but the write path is exercised for real).
	if err := runDump(cl, db, dbOut, clusterOut, introspect.New(), store, &mockSecretResolver{}, fmtOpts, true, dumpLogOptions{}); err != nil {
		t.Fatalf("second runDump with skipConfirm=true (roles.dpg pre-existing): %v", err)
	}

	// Third dump, skipConfirm=false, with a fake os.Stdin that declines the
	// first prompt: must fail, and must NOT touch the existing roles.dpg.
	origStdin := os.Stdin
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	if _, err := w.WriteString("n\n"); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	w.Close()
	os.Stdin = r
	err = runDump(cl, db, dbOut, clusterOut, introspect.New(), store, &mockSecretResolver{}, fmtOpts, false, dumpLogOptions{})
	os.Stdin = origStdin
	r.Close()

	if err == nil {
		t.Fatal("expected runDump to refuse when the (fake, declining) interactive prompt says no")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected an 'aborted' error, got: %v", err)
	}
	after, readErr := os.ReadFile(rolesPath)
	if readErr != nil {
		t.Fatalf("read roles.dpg after declined third dump: %v", readErr)
	}
	if string(after) != string(before) {
		t.Error("roles.dpg must be byte-identical after a declined overwrite confirmation")
	}
}
