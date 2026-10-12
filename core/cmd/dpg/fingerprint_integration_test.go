//go:build integration

package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// runFile runs a migration file on a server as another runner would, with
// optional session settings first.
func runFile(t *testing.T, p *pg, path string, settings ...string) error {
	t.Helper()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(file)
	if len(settings) > 0 {
		sql = strings.Join(settings, ";\n") + ";\n" + sql
	}
	_, err = p.conn.Exec(context.Background(), sql)
	return err
}

// TestFingerprintGuardLive: a migration file refuses to run on a database
// whose objects changed since DPG last saw them, for any runner.
func TestFingerprintGuardLive(t *testing.T) {
	dev, prod := startPG(t), startPG(t)
	const views = "c/dpgtest/schemas/public/views.dpg"
	root := resolveProject(t, dev.url, map[string]string{
		tFile: "TABLE t (id int PRIMARY KEY, x int DEFAULT 0);\n",
		views: "VIEW v AS SELECT id, x FROM t;\n",
	})
	deployTo(t, root, prod)
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	if got := prod.query(`SELECT string_agg(kind || ' ' || key, ',' ORDER BY key) FROM dpg.objects`); !strings.Contains(got, "table public.t") || !strings.Contains(got, "view public.v") {
		t.Errorf("prod's recorded objects after settling: %s", got)
	}

	// A migration made on dev, run on prod by another runner.
	newMigration := func(src string) string {
		t.Helper()
		deployTo(t, root, dev)
		writeSource(t, root, tFile, src)
		if err := applyErr(t, root, "--allow-destructive"); err != nil {
			t.Fatal(err)
		}
		l := loadLog(t, root)
		path, _ := l.Paths(l.Head())
		deployTo(t, root, prod)
		return path
	}

	// 1. Prod changed t by hand: the file refuses, changing nothing.
	addY := newMigration("TABLE t (id int PRIMARY KEY, x int DEFAULT 0, y int);\n")
	prod.exec(`ALTER TABLE public.t ALTER COLUMN x SET DEFAULT 9`)
	if err := runFile(t, prod, addY); err == nil || !strings.Contains(err.Error(), "DPG-E055") {
		t.Fatalf("drifted table: %v", err)
	}
	if got := prod.query(`SELECT count(*)::text FROM information_schema.columns WHERE table_name = 't' AND column_name = 'y'`); got != "0" {
		t.Error("the refused file changed the table")
	}
	// 2. The runner can opt out.
	if err := runFile(t, prod, addY, "SET dpg.allow_drift = on"); err != nil {
		t.Fatalf("with dpg.allow_drift: %v", err)
	}
	prod.exec(`RESET dpg.allow_drift`, `ALTER TABLE public.t ALTER COLUMN x SET DEFAULT 0`)

	// 3. An object DPG has no record of: refused until dpg verify records it.
	addZ := newMigration("TABLE t (id int PRIMARY KEY, x int DEFAULT 0, y int, z int);\n")
	prod.exec(`DELETE FROM dpg.objects WHERE key = 'public.t'`)
	if err := runFile(t, prod, addZ); err == nil || !strings.Contains(err.Error(), "DPG-E054") {
		t.Fatalf("unrecorded table: %v", err)
	}
	if err := runDPG(t, root, "verify"); err != nil {
		t.Fatalf("verify on prod: %v", err)
	}
	if got := prod.query(`SELECT migration FROM dpg.objects WHERE key = 'public.t'`); got != "verify" {
		t.Errorf("verify didn't record t: %q", got)
	}
	if err := runFile(t, prod, addZ); err != nil {
		t.Fatalf("after verify: %v", err)
	}

	// 4. A grant made by hand on v, which the next migration recreates:
	// the fingerprint holds v's ACL, so the file refuses.
	prod.exec(`CREATE ROLE reader`, `GRANT SELECT ON public.v TO reader`)
	deployTo(t, root, dev)
	writeSource(t, root, views, "VIEW v AS SELECT id FROM t;\n")
	if err := applyErr(t, root, "--allow-destructive"); err != nil {
		t.Fatal(err)
	}
	l := loadLog(t, root)
	recreateV, _ := l.Paths(l.Head())
	deployTo(t, root, prod)
	if err := runFile(t, prod, recreateV); err == nil || !strings.Contains(err.Error(), "DPG-E055") || !strings.Contains(err.Error(), "public.v") {
		t.Fatalf("lost grant: %v", err)
	}
}
