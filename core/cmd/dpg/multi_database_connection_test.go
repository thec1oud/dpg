//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestMultiDatabaseClusterConnectsToCorrectDatabase proves the fix for a bug
// where every per-database command connected using the cluster's raw
// connection string verbatim, ignoring which database was actually selected
// — so every database under one cluster silently operated against whatever
// database happened to be embedded in the cluster's url/link. Reproduced
// live before this fix: `dpg dump --database mdb_b` against a cluster whose
// URL pointed at mdb_a wrote mdb_a's schema into mdb_b's output directory,
// with no error.
//
// The cluster's URL here deliberately points at a THIRD database
// (testpg's default "dpgtest") that is neither mdb_a nor mdb_b and contains
// none of the fixture tables — so a regression surfaces as "wrong/empty
// database" for every assertion below, rather than accidentally passing by
// coincidentally matching one target database's content.
func TestMultiDatabaseClusterConnectsToCorrectDatabase(t *testing.T) {
	ctx := context.Background()
	baseConnStr := testpg.Start(t) // dbname=dpgtest — deliberately never a target below

	admin, err := executor.Connect(ctx, baseConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE mdb_a"); err != nil {
		t.Fatalf("create mdb_a: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE mdb_b"); err != nil {
		t.Fatalf("create mdb_b: %v", err)
	}

	connA, err := executor.ConnectToDatabase(ctx, baseConnStr, "mdb_a")
	if err != nil {
		t.Fatalf("connect to mdb_a: %v", err)
	}
	defer connA.Close(ctx)
	if _, err := connA.Exec(ctx, "CREATE TABLE table_a (id int)"); err != nil {
		t.Fatalf("create table_a: %v", err)
	}

	connB, err := executor.ConnectToDatabase(ctx, baseConnStr, "mdb_b")
	if err != nil {
		t.Fatalf("connect to mdb_b: %v", err)
	}
	defer connB.Close(ctx)
	if _, err := connB.Exec(ctx, "CREATE TABLE table_b (id int)"); err != nil {
		t.Fatalf("create table_b: %v", err)
	}

	cl := &project.Cluster{
		Config: config.ClusterConfig{Cluster: config.ClusterDef{Name: "cluster1", URL: baseConnStr}},
	}
	dbA := &project.Database{Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "mdb_a"}}}
	dbB := &project.Database{Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "mdb_b"}}}
	fmtOpts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	resolver := &mockSecretResolver{}

	scratchOut := t.TempDir()
	dbAOut := filepath.Join(scratchOut, "mdb_a")
	dbBOut := filepath.Join(scratchOut, "mdb_b")
	clusterOut := filepath.Join(scratchOut, "cluster1", "cluster")
	store := &snapshot.FileStore{Dir: filepath.Join(scratchOut, ".dpg", "snapshots")}
	migrationsDir := filepath.Join(scratchOut, ".dpg", "migrations")

	// --- dump: each database's dump must contain only its own table ---
	if err := runDump(cl, dbA, dbAOut, clusterOut, introspect.New(), store, resolver, fmtOpts, true, migrationsDir); err != nil {
		t.Fatalf("runDump mdb_a: %v", err)
	}
	schemaA, err := os.ReadFile(filepath.Join(dbAOut, "schemas", "public", "schema.dpg"))
	if err != nil {
		t.Fatalf("read mdb_a dump: %v", err)
	}
	if !strings.Contains(string(schemaA), "table_a") {
		t.Errorf("mdb_a dump missing table_a, got:\n%s", schemaA)
	}
	if strings.Contains(string(schemaA), "table_b") {
		t.Errorf("mdb_a dump must not contain table_b (wrong-database leak), got:\n%s", schemaA)
	}

	if err := runDump(cl, dbB, dbBOut, clusterOut, introspect.New(), store, resolver, fmtOpts, true, migrationsDir); err != nil {
		t.Fatalf("runDump mdb_b: %v", err)
	}
	schemaBPath := filepath.Join(dbBOut, "schemas", "public", "schema.dpg")
	schemaB, err := os.ReadFile(schemaBPath)
	if err != nil {
		t.Fatalf("read mdb_b dump: %v", err)
	}
	if !strings.Contains(string(schemaB), "table_b") {
		t.Errorf("mdb_b dump missing table_b, got:\n%s", schemaB)
	}
	if strings.Contains(string(schemaB), "table_a") {
		t.Errorf("mdb_b dump must not contain table_a (wrong-database leak), got:\n%s", schemaB)
	}

	// --- introspectSnapshot (plan --live's per-database path): must reflect
	// mdb_b's live state only ---
	liveSnap, err := introspectSnapshot(ctx, cl, dbB, resolver, introspect.New())
	if err != nil {
		t.Fatalf("introspectSnapshot mdb_b: %v", err)
	}
	foundB := false
	for key := range liveSnap.Objects {
		if strings.Contains(key, "table_b") {
			foundB = true
		}
		if strings.Contains(key, "table_a") {
			t.Errorf("introspectSnapshot(mdb_b) must not see table_a, found key %q", key)
		}
	}
	if !foundB {
		t.Errorf("introspectSnapshot(mdb_b) missing table_b; objects: %v", liveSnap.Objects)
	}

	// --- verify: dbB's just-dumped snapshot must be checked against mdb_b's
	// own live state. A same-schema storage/canonicalization nuance can
	// legitimately produce minor drift here regardless of which database was
	// connected to, so the real signal isn't "zero drift" — it's whether
	// verify thinks table_b needs to be created from scratch (which is
	// exactly what would happen if it had connected to an empty database
	// instead of mdb_b) or, worse, ever references table_a at all.
	dbB.SourceFiles = []string{schemaBPath}
	dbB.Dir = dbBOut
	report, err := runVerify(cl, dbB, store, introspect.New(), diff.New(), resolver, nil)
	if err != nil {
		t.Fatalf("runVerify mdb_b: %v", err)
	}
	for _, it := range report.Items {
		if strings.Contains(it.Object, "table_a") {
			t.Errorf("runVerify(mdb_b) reported table_a — connected to the wrong database: %s", it.ID)
		}
		if it.Object == "public.table_b" && it.Change == "missing" {
			t.Errorf("runVerify(mdb_b) found table_b missing — it must exist if connected to mdb_b: %s", it.ID)
		}
	}

	// --- apply: a new table declared in mdb_b's source must land in mdb_b
	// only, never in mdb_a or the cluster URL's own default database ---
	extraFile := filepath.Join(dbBOut, "extra.dpg")
	if err := os.WriteFile(extraFile, []byte("TABLE table_b_extra (id int);\n"), 0o644); err != nil {
		t.Fatalf("write extra source: %v", err)
	}
	dbB.SourceFiles = []string{schemaBPath, extraFile}
	if err := runApply(cl, dbB, store, diff.New(), emit.New(), executor.New(), resolver, applyOptions{yes: true, allowDestructive: true, migrationsDir: migrationsDir}); err != nil {
		t.Fatalf("runApply mdb_b: %v", err)
	}

	afterB, err := introspectSnapshot(ctx, cl, dbB, resolver, introspect.New())
	if err != nil {
		t.Fatalf("introspectSnapshot mdb_b after apply: %v", err)
	}
	foundExtraInB := false
	for key := range afterB.Objects {
		if strings.Contains(key, "table_b_extra") {
			foundExtraInB = true
		}
	}
	if !foundExtraInB {
		t.Errorf("runApply(mdb_b) did not create table_b_extra in mdb_b; objects: %v", afterB.Objects)
	}

	afterA, err := introspectSnapshot(ctx, cl, dbA, resolver, introspect.New())
	if err != nil {
		t.Fatalf("introspectSnapshot mdb_a after mdb_b apply: %v", err)
	}
	for key := range afterA.Objects {
		if strings.Contains(key, "table_b_extra") {
			t.Errorf("runApply(mdb_b) leaked table_b_extra into mdb_a — applied against the wrong database")
		}
	}
}
