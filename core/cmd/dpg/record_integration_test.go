//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/ui"
)

// dpgErr runs a dpg command and returns its error.
func dpgErr(t *testing.T, root string, args ...string) error {
	t.Helper()
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"-C", root}, args...))
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Logf("dpg %v:\n%s", args, out)
	}
	return err
}

// TestOfflineWorkflowLive: changes are recorded without a database, each
// database runs them when dpg apply settles, the CI check notices
// unrecorded changes, --amend replaces the last migration, and a merge of
// two branches' migrations is repaired by dpg log rebase.
func TestOfflineWorkflowLive(t *testing.T) {
	dev, prod := startPG(t), startPG(t)
	root := resolveProject(t, dev.url, map[string]string{tFile: "TABLE t (id int PRIMARY KEY, x int);\n"})
	offline := &pg{t: t, url: "postgres://nobody@127.0.0.1:1/dpgtest?sslmode=disable&connect_timeout=1"}

	// 1. Recorded with no database at all.
	deployTo(t, root, offline)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int);\n")
	if err := dpgErr(t, root, "plan", "--record"); err != nil {
		t.Fatalf("record: %v", err)
	}
	l := loadLog(t, root)
	if len(l.Entries) != 2 || l.Entries[1].Origin != migrationlog.OriginRecord || storedSnapshot(t, root).Migration != l.Head() {
		t.Fatalf("after recording: %+v", l.Entries)
	}
	if err := dpgErr(t, root, "plan", "--record"); err != nil {
		t.Errorf("nothing left to record: %v", err)
	}
	if len(loadLog(t, root).Entries) != 2 {
		t.Error("recording nothing added a migration")
	}

	// 2. The CI check.
	if err := dpgErr(t, root, "plan", "--check"); err != nil {
		t.Errorf("check with everything recorded: %v", err)
	}
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int);\n")
	if err := dpgErr(t, root, "plan", "--check"); err == nil || ui.ExitCode(err) != 1 || !strings.Contains(err.Error(), "DPG-E056") {
		t.Errorf("check with an unrecorded change: %v", err)
	}

	// 3. --amend: the last migration made again from the source.
	recorded := loadLog(t, root).Head()
	if err := dpgErr(t, root, "plan", "--record", "--amend"); err != nil {
		t.Fatalf("amend: %v", err)
	}
	l = loadLog(t, root)
	if len(l.Entries) != 2 || l.Head() == recorded || l.Entries[1].Parent != l.Entries[0].ID {
		t.Fatalf("after amending: %+v", l.Entries)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, recorded+".json")); !os.IsNotExist(err) {
		t.Error("the replaced migration's files remain")
	}
	sql, _ := l.Paths(l.Head())
	if b, _ := os.ReadFile(sql); !strings.Contains(string(b), `"y"`) || !strings.Contains(string(b), `"z"`) {
		t.Errorf("the amended migration must add y and z:\n%s", b)
	}
	if err := dpgErr(t, root, "plan", "--check"); err != nil {
		t.Errorf("check after amending: %v", err)
	}
	// Destructive changes need --allow-destructive to be recorded too.
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, y int, z int);\n")
	if err := dpgErr(t, root, "plan", "--record"); err == nil || !strings.Contains(err.Error(), "DPG-E025") {
		t.Errorf("recording a destructive change: %v", err)
	}
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int);\n")

	// 4. Dev runs what was recorded.
	deployTo(t, root, dev)
	if err := applyErr(t, root); err != nil {
		t.Fatalf("dev settling: %v", err)
	}
	if got := dev.query(`SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 't'`); got != "id,x,y,z" {
		t.Errorf("dev's t: %s", got)
	}

	// 5. With the ledger off nothing would run a recorded migration.
	os.WriteFile(filepath.Join(root, "dpg.toml"), []byte(rootTOML()+"\n[ledger]\nenabled = false\n"), 0o644)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int, w int);\n")
	if err := dpgErr(t, root, "plan", "--record"); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Errorf("record with the ledger off: %v", err)
	}
	os.WriteFile(filepath.Join(root, "dpg.toml"), []byte(rootTOML()), 0o644)

	// 6. dpg log repair rewrites a snapshot a merge left unreadable.
	snapPath := filepath.Join(root, ".dpg", "snapshots", "c", "dpgtest.json")
	good, _ := os.ReadFile(snapPath)
	os.WriteFile(snapPath, []byte("<<<<<<< ours\n"+string(good)+"=======\n>>>>>>> theirs\n"), 0o644)
	if err := dpgErr(t, root, "log", "repair"); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if storedSnapshot(t, root).Migration != loadLog(t, root).Head() {
		t.Error("repair didn't restore the snapshot")
	}
	if err := dpgErr(t, root, "plan", "--check"); err == nil {
		t.Error("w is still unrecorded after the repair")
	}

	// 7. Two branches each record a migration after the same one.
	base := loadLog(t, root)
	baseSnap, _ := os.ReadFile(snapPath)
	// Branch A: adds w.
	if err := dpgErr(t, root, "plan", "--record"); err != nil {
		t.Fatal(err)
	}
	a := loadLog(t, root).Head()
	aSQL, aJSON := loadLog(t, root).Paths(a)
	aSQLData, _ := os.ReadFile(aSQL)
	aJSONData, _ := os.ReadFile(aJSON)
	// Dev runs branch A's migration.
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	// Branch B, from the same point: adds v.
	migrationlog.Remove(base.Dir, a)
	os.WriteFile(snapPath, baseSnap, 0o644)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int, v int);\n")
	if err := dpgErr(t, root, "plan", "--record"); err != nil {
		t.Fatal(err)
	}
	b := loadLog(t, root).Head()
	// The merge: both migrations, both columns, a conflicted snapshot.
	os.WriteFile(aSQL, aSQLData, 0o644)
	os.WriteFile(aJSON, aJSONData, 0o644)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int, v int, w int);\n")
	os.WriteFile(snapPath, []byte("<<<<<<< conflict\n"), 0o644)
	if err := dpgErr(t, root, "plan"); err == nil || !strings.Contains(err.Error(), "dpg log rebase") {
		t.Fatalf("a conflicted snapshot: %v", err)
	}
	os.WriteFile(snapPath, baseSnap, 0o644)
	if err := dpgErr(t, root, "plan"); err == nil || !strings.Contains(err.Error(), "DPG-E047") {
		t.Fatalf("divergent heads: %v", err)
	}
	os.WriteFile(snapPath, []byte("<<<<<<< conflict\n"), 0o644)
	if err := dpgErr(t, root, "log", "rebase", "--keep", b); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	l = loadLog(t, root)
	if len(l.Entries) != len(base.Entries)+2 || l.Entries[len(l.Entries)-2].ID != b || l.Find(a) != nil {
		var ids []string
		for _, e := range l.Entries {
			ids = append(ids, e.ID+"<-"+e.Parent)
		}
		t.Fatalf("after rebasing (a %s, b %s): %v", a, b, ids)
	}
	if err := dpgErr(t, root, "plan", "--check"); err != nil {
		t.Errorf("check after rebasing: %v", err)
	}
	// A fresh database runs the whole log; dev, which ran A, has no place
	// in it any more.
	deployTo(t, root, prod)
	if err := applyErr(t, root); err != nil {
		t.Fatalf("prod: %v", err)
	}
	if got := prod.query(`SELECT string_agg(column_name, ',' ORDER BY column_name) FROM information_schema.columns WHERE table_name = 't'`); got != "id,v,w,x,y,z" {
		t.Errorf("prod's t: %s", got)
	}
	deployTo(t, root, dev)
	if err := applyErr(t, root); err == nil || !strings.Contains(err.Error(), "DPG-E050") {
		t.Errorf("dev after the rebase: %v", err)
	}
}
