//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
)

func loadLog(t *testing.T, root string) *migrationlog.Log {
	t.Helper()
	l, err := migrationlog.Load(filepath.Join(root, ".dpg", "migrations", "c", "dpgtest"))
	if err != nil {
		t.Fatalf("load log: %v", err)
	}
	return l
}

func storedSnapshot(t *testing.T, root string) *pipeline.Snapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".dpg", "snapshots", "c", "dpgtest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s pipeline.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// TestMigrationLogLive: applies and adoptions chain in the log, the log
// replays to the snapshot, plan catches divergent heads and an edited
// snapshot, and a project that predates the log starts it with
// --baseline, whose SQL builds the database from empty.
func TestMigrationLogLive(t *testing.T) {
	ctx := context.Background()
	connStr := testpg.Start(t)
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(stmts ...string) {
		t.Helper()
		for _, s := range stmts {
			if _, err := conn.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}
	tFile := "c/dpgtest/schemas/public/tables/t.dpg"
	root := resolveProject(t, connStr, map[string]string{tFile: "TABLE t (\n    id   int PRIMARY KEY,\n    name text DEFAULT 'a'\n);\n"})

	// 1. The first apply starts the log.
	l := loadLog(t, root)
	snap := storedSnapshot(t, root)
	if len(l.Entries) != 1 || l.Entries[0].Parent != "" || l.Entries[0].Origin != migrationlog.OriginApply || snap.Migration != l.Head() {
		t.Fatalf("after the first apply: %d entries, snapshot migration %q", len(l.Entries), snap.Migration)
	}
	if snap.DPGVersion != "0.2.0" {
		t.Errorf("snapshot format %q", snap.DPGVersion)
	}

	// 2. A second apply chains.
	if err := os.WriteFile(filepath.Join(root, tFile), []byte("TABLE t (\n    id   int PRIMARY KEY,\n    name text DEFAULT 'a',\n    age  int\n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDPG(t, root, "apply", "--yes")
	l = loadLog(t, root)
	if len(l.Entries) != 2 || l.Entries[1].Parent != l.Entries[0].ID || storedSnapshot(t, root).Migration != l.Head() {
		t.Fatalf("after the second apply: %+v", l.Entries)
	}
	sql, _ := l.Paths(l.Head())
	if b, _ := os.ReadFile(sql); !strings.Contains(string(b), `ADD COLUMN "age"`) {
		t.Errorf("second migration's SQL:\n%s", b)
	}

	// 3. Adopting records a migration with what other databases need.
	exec(`ALTER TABLE public.t ALTER COLUMN name SET DEFAULT 'b'`)
	if err := runDPG(t, root, "resolve", "--yes", "--adopt", "table:public.t/column:name"); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	l = loadLog(t, root)
	head := l.Entries[len(l.Entries)-1]
	if len(l.Entries) != 3 || head.Origin != migrationlog.OriginAdopt || storedSnapshot(t, root).Migration != head.ID {
		t.Fatalf("after adopting: %d entries, head %+v", len(l.Entries), head)
	}
	sql, _ = l.Paths(head.ID)
	if b, _ := os.ReadFile(sql); !strings.Contains(string(b), `SET DEFAULT 'b'`) || strings.Contains(string(b), "age") {
		t.Errorf("the adoption's SQL must hold the adopted change only:\n%s", b)
	}
	// The database it was adopted from records it as run.
	rows, err := conn.QueryRows(ctx, `SELECT status FROM dpg.migrations WHERE log = 'dpgtest' AND id = $1`, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	for rows.Next() {
		rows.Scan(&status)
	}
	rows.Close()
	if status != "complete" {
		t.Errorf("the adoption in the ledger: %q", status)
	}
	if err := runDPG(t, root, "apply", "--yes"); err != nil {
		t.Errorf("apply after adopting must have nothing to do: %v", err)
	}
	if err := runDPG(t, root, "plan"); err != nil {
		t.Errorf("plan after adopting: %v", err)
	}
	if err := runDPG(t, root, "validate"); err != nil {
		t.Errorf("validate after adopting: %v", err)
	}

	// 4. Two migrations after the same parent: plan refuses.
	logDir := l.Dir
	sibling, err := migrationlog.New(l.Entries[1].ID, []migrationlog.Step{{SQL: "SELECT 1;", Safety: "SAFE", Stage: migrationlog.StageTransaction}}, migrationlog.OriginApply,
		map[string]json.RawMessage{}, map[string]json.RawMessage{"x": json.RawMessage(`1`)}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&migrationlog.Log{Dir: logDir}).Write(sibling, "-- other branch\n"); err != nil {
		t.Fatal(err)
	}
	if err := runDPG(t, root, "plan"); err == nil || !strings.Contains(err.Error(), "DPG-E047") {
		t.Errorf("divergent heads: %v", err)
	}
	sqlPath, entryPath := (&migrationlog.Log{Dir: logDir}).Paths(sibling.ID)
	os.Remove(sqlPath)
	os.Remove(entryPath)

	// 5. A hand-edited snapshot no longer matches its log.
	snapPath := filepath.Join(root, ".dpg", "snapshots", "c", "dpgtest.json")
	orig, _ := os.ReadFile(snapPath)
	edited := strings.Replace(string(orig), `"name": "age"`, `"name": "years"`, 1)
	if edited == string(orig) {
		t.Fatal("snapshot edit didn't apply")
	}
	os.WriteFile(snapPath, []byte(edited), 0o644)
	if err := runDPG(t, root, "plan"); err == nil || !strings.Contains(err.Error(), "DPG-E046") {
		t.Errorf("edited snapshot: %v", err)
	}
	os.WriteFile(snapPath, orig, 0o644)

	// 6. A project from before the log (and a database from before the
	// ledger): apply refuses until --baseline.
	if err := os.RemoveAll(filepath.Join(root, ".dpg", "migrations")); err != nil {
		t.Fatal(err)
	}
	exec(`DROP SCHEMA dpg CASCADE`)
	var s map[string]any
	json.Unmarshal(orig, &s)
	delete(s, "migration")
	b, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(snapPath, b, 0o644)
	if err := os.WriteFile(filepath.Join(root, tFile), []byte("TABLE t (\n    id   int PRIMARY KEY,\n    name text DEFAULT 'b',\n    age  int,\n    more int\n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCmd()
	cmd.SetArgs([]string{"-C", root, "apply", "--yes"})
	var applyErr error
	captureStdout(t, func() { applyErr = cmd.Execute() })
	if applyErr == nil || !strings.Contains(applyErr.Error(), "DPG-E048") {
		t.Fatalf("apply without a log: %v", applyErr)
	}
	// --baseline needs the source to match the snapshot.
	cmd = newRootCmd()
	cmd.SetArgs([]string{"-C", root, "apply", "--baseline"})
	captureStdout(t, func() { applyErr = cmd.Execute() })
	if applyErr == nil || !strings.Contains(applyErr.Error(), "pending change") {
		t.Fatalf("baseline with pending changes: %v", applyErr)
	}
	if err := os.WriteFile(filepath.Join(root, tFile), []byte("TABLE t (\n    id   int PRIMARY KEY,\n    name text DEFAULT 'b',\n    age  int\n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDPG(t, root, "apply", "--baseline")
	l = loadLog(t, root)
	if len(l.Entries) != 1 || l.Entries[0].Origin != migrationlog.OriginBaseline || storedSnapshot(t, root).Migration != l.Head() {
		t.Fatalf("baseline: %+v", l.Entries)
	}
	// The baseline's SQL builds the schema in an empty database.
	exec(`CREATE DATABASE fresh`)
	baselineSQL, _ := l.Paths(l.Head())
	sqlText, _ := os.ReadFile(baselineSQL)
	freshConn, err := executor.Connect(ctx, strings.Replace(connStr, "/dpgtest", "/fresh", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer freshConn.Close(ctx)
	if _, err := freshConn.Exec(ctx, string(sqlText)); err != nil {
		t.Fatalf("running the baseline in an empty database: %v\n%s", err, sqlText)
	}
	rows, err = freshConn.QueryRows(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 't'`)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for rows.Next() {
		rows.Scan(&n)
	}
	rows.Close()
	if n != 3 {
		t.Errorf("baseline built table t with %d columns, want 3", n)
	}
	// And the project goes on from it.
	if err := os.WriteFile(filepath.Join(root, tFile), []byte("TABLE t (\n    id   int PRIMARY KEY,\n    name text DEFAULT 'b',\n    age  int,\n    more int\n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDPG(t, root, "apply", "--yes")
	if l = loadLog(t, root); len(l.Entries) != 2 || l.Entries[1].Parent != l.Entries[0].ID {
		t.Errorf("after the baseline: %+v", l.Entries)
	}
}
