//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/ui"
)

func wantBlocked(t *testing.T, err error, contains ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected the preflight to refuse")
	}
	if code := ui.ExitCode(err); code != 1 {
		t.Errorf("exit code %d, want 1 (%v)", code, err)
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("%q not in %v", c, err)
		}
	}
}

// stderrOf runs dpg apply and returns its error and what it wrote to
// stderr (the preflight's report).
func applyStderr(t *testing.T, root string, args ...string) (error, string) {
	t.Helper()
	var err error
	out := captureStderr(t, func() { err = applyErr(t, root, args...) })
	return err, out
}

// TestPreflightLive: dpg apply refuses to run migrations on a database
// that isn't as they expect, and the guards in the migration files refuse
// the same for any runner.
func TestPreflightLive(t *testing.T) {
	dev, prod := startPG(t), startPG(t)
	const views = "c/dpgtest/schemas/public/views.dpg"
	root := resolveProject(t, dev.url, map[string]string{
		tFile:                                    "TABLE t (id int PRIMARY KEY, x int DEFAULT 0);\n",
		"c/dpgtest/schemas/public/tables/t2.dpg": "TABLE t2 (id int PRIMARY KEY);\n",
		views:                                    "VIEW v AS SELECT id, x FROM t;\n",
	})

	// 1. Drift on an object the migration changes blocks it (and --dry-run).
	dev.exec(`ALTER TABLE public.t ALTER COLUMN x SET DEFAULT 5`)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int DEFAULT 0, y int);\n")
	err, report := applyStderr(t, root)
	wantBlocked(t, err, "DPG-E053")
	if !strings.Contains(report, "table:public.t/column:x") {
		t.Errorf("the report doesn't name the item:\n%s", report)
	}
	err, _ = applyStderr(t, root, "--dry-run")
	wantBlocked(t, err)
	if got := dev.query(`SELECT count(*)::text FROM information_schema.columns WHERE table_name = 't' AND column_name = 'y'`); got != "0" {
		t.Fatal("a refused apply changed the database")
	}

	// Acknowledged, on a property the migration doesn't change: it runs.
	if err := runDPG(t, root, "resolve", "--yes", "--ack", "table:public.t/column:x", "--reason", "hotfix"); err != nil {
		t.Fatal(err)
	}
	if err := applyErr(t, root); err != nil {
		t.Fatalf("acknowledged drift on another property: %v", err)
	}
	// Changing that property in the source changes the item (the
	// acknowledgement lapses): a conflict, refused.
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int DEFAULT 1, y int);\n")
	err, report = applyStderr(t, root)
	wantBlocked(t, err)
	if !strings.Contains(report, "table:public.t/column:x: conflict") {
		t.Errorf("report:\n%s", report)
	}
	// --allow-drift proceeds.
	if err := applyErr(t, root, "--allow-drift"); err != nil {
		t.Fatalf("--allow-drift: %v", err)
	}

	// 2. Drift on an object the migration doesn't touch is a note.
	dev.exec(`ALTER TABLE public.t2 ADD COLUMN extra int`)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int DEFAULT 1, y int, z int);\n")
	err, report = applyStderr(t, root)
	if err != nil {
		t.Fatalf("drift elsewhere must not block: %v", err)
	}
	if !strings.Contains(report, "table:public.t2/column:extra") {
		t.Errorf("no note about the other drift:\n%s", report)
	}
	dev.exec(`ALTER TABLE public.t2 DROP COLUMN extra`)

	// 3. Prod, behind dev, is checked against its own position.
	deployTo(t, root, prod)
	if err := applyErr(t, root); err != nil {
		t.Fatalf("prod catching up: %v", err)
	}
	deployTo(t, root, dev)
	writeSource(t, root, "c/dpgtest/schemas/public/tables/u.dpg", "TABLE u (id int PRIMARY KEY);\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	l := loadLog(t, root)
	createU, _ := l.Paths(l.Head())
	deployTo(t, root, prod)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"-C", root, "verify", "--format", "json"})
	var verr error
	out := captureStdout(t, func() { verr = cmd.Execute() })
	var doc struct {
		Targets []struct {
			Database string
			Pending  int
			Items    []struct{ ID string }
		}
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("verify JSON: %v\n%s", err, out)
	}
	for _, tg := range doc.Targets {
		if tg.Database == "dpgtest" && (tg.Pending != 1 || len(tg.Items) != 0) {
			t.Errorf("verify on prod: pending %d, items %v (err %v)", tg.Pending, tg.Items, verr)
		}
	}

	// 4. The migration creates u, which prod already has (unmanaged):
	// the preflight refuses, and so does the file's guard.
	prod.exec(`CREATE TABLE public.u (other text)`)
	err, report = applyStderr(t, root)
	wantBlocked(t, err)
	if !strings.Contains(report, "table:public.u: the migration creates it") {
		t.Errorf("report:\n%s", report)
	}
	file, _ := os.ReadFile(createU)
	if _, err := prod.conn.Exec(context.Background(), string(file)); err == nil || !strings.Contains(err.Error(), "DPG-E051") {
		t.Errorf("the file's guard: %v", err)
	}
	prod.exec(`DROP TABLE public.u`)
	if err := applyErr(t, root); err != nil {
		t.Fatalf("prod after the collision is gone: %v", err)
	}

	// 5. Dropping t2, which prod has a view of its own on: refused, by
	// the preflight and by the guard.
	deployTo(t, root, dev)
	os.Remove(filepath.Join(root, "c/dpgtest/schemas/public/tables/t2.dpg"))
	if err := applyErr(t, root, "--allow-destructive"); err != nil {
		t.Fatal(err)
	}
	l = loadLog(t, root)
	dropT2, _ := l.Paths(l.Head())
	deployTo(t, root, prod)
	prod.exec(`CREATE VIEW public.w AS SELECT id FROM public.t2`)
	err, report = applyStderr(t, root, "--allow-destructive")
	wantBlocked(t, err)
	if !strings.Contains(report, "public.w depends on it") {
		t.Errorf("report:\n%s", report)
	}
	if err := applyErr(t, root, "--allow-destructive", "--allow-drift"); err == nil {
		t.Error("the view would still make DROP TABLE fail; something ran")
	}
	file, _ = os.ReadFile(dropT2)
	if _, err := prod.conn.Exec(context.Background(), string(file)); err == nil || !strings.Contains(err.Error(), "DPG-E052") {
		t.Errorf("the file's guard: %v", err)
	}
	prod.exec(`DROP VIEW public.w`)
	if err := applyErr(t, root, "--allow-destructive"); err != nil {
		t.Fatalf("prod after the view is gone: %v", err)
	}

	// 6. Prod acknowledges its own drift on t.x; a migration from dev then
	// changes t.x: refused while settling (the acknowledgement still holds
	// but doesn't cover changing the property).
	deployTo(t, root, dev)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int DEFAULT 3, y int, z int);\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	deployTo(t, root, prod)
	prod.exec(`ALTER TABLE public.t ALTER COLUMN x SET DEFAULT 7`)
	if err := runDPG(t, root, "resolve", "--yes", "--ack", "table:public.t/column:x", "--reason", "prod only"); err != nil {
		t.Fatalf("ack on prod: %v", err)
	}
	err, report = applyStderr(t, root)
	wantBlocked(t, err)
	if !strings.Contains(report, "table:public.t/column:x: acknowledged, but the migration changes it") {
		t.Errorf("report:\n%s", report)
	}
	prod.exec(`ALTER TABLE public.t ALTER COLUMN x SET DEFAULT 1`)
	if err := applyErr(t, root); err != nil {
		t.Fatalf("prod after its drift is gone: %v", err)
	}

	// 7. Recreating v loses a grant made outside DPG: refused.
	deployTo(t, root, dev)
	dev.exec(`CREATE ROLE reader`, `GRANT SELECT ON public.v TO reader`)
	writeSource(t, root, views, "VIEW v AS SELECT id FROM t;\n")
	err, report = applyStderr(t, root, "--allow-destructive")
	wantBlocked(t, err)
	if !strings.Contains(report, "grant:SELECT:reader") || !strings.Contains(report, "recreates") {
		t.Errorf("report:\n%s", report)
	}
}
