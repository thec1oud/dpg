//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// pg is a test server: its URL and a connection to it.
type pg struct {
	t    *testing.T
	url  string
	conn *executor.PgxConn
}

func startPG(t *testing.T) *pg {
	t.Helper()
	url := testpg.Start(t)
	conn, err := executor.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return &pg{t, url, conn}
}

func (p *pg) exec(stmts ...string) {
	p.t.Helper()
	for _, s := range stmts {
		if _, err := p.conn.Exec(context.Background(), s); err != nil {
			p.t.Fatalf("%s: %v", s, err)
		}
	}
}

// query returns the first column of every row, joined with ",".
func (p *pg) query(sql string) string {
	p.t.Helper()
	rows, err := p.conn.QueryRows(context.Background(), sql)
	if err != nil {
		p.t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			p.t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// deployTo points the project's cluster at a server: the same source and
// snapshot, another environment.
func deployTo(t *testing.T, root string, p *pg) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "c", "dpg.toml"), []byte(clusterTOML("c", p.url)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSource(t *testing.T, root, file, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, file), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// apply runs dpg apply and returns its error.
func applyErr(t *testing.T, root string, args ...string) error {
	t.Helper()
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"-C", root, "apply", "--yes"}, args...))
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Logf("dpg apply %v:\n%s", args, out)
	}
	return err
}

const tFile = "c/dpgtest/schemas/public/tables/t.dpg"

// TestSettleLive: dev and prod are two servers with the same project (one
// snapshot, one log). Each database's ledger says which migrations it has
// run; apply catches a database up by running the rest, in order.
func TestSettleLive(t *testing.T) {
	dev, prod := startPG(t), startPG(t)
	root := resolveProject(t, dev.url, map[string]string{tFile: "TABLE t (id int PRIMARY KEY, x int);\n"})
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int);\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	l := loadLog(t, root)
	if len(l.Entries) != 2 {
		t.Fatalf("log: %d entries", len(l.Entries))
	}
	if got := dev.query(`SELECT id FROM dpg.migrations WHERE log = 'dpgtest' AND status = 'complete' ORDER BY started_at`); got != l.Entries[0].ID+","+l.Entries[1].ID {
		t.Errorf("dev's ledger: %s", got)
	}

	// 1. Prod is fresh: apply runs both migrations from the log.
	deployTo(t, root, prod)
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	if got := prod.query(`SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 't'`); got != "id,x,y" {
		t.Errorf("prod's t: %s", got)
	}
	if got := prod.query(`SELECT count(*)::text FROM dpg.migrations WHERE status = 'complete' AND log = 'dpgtest'`); got != "2" {
		t.Errorf("prod's ledger: %s rows", got)
	}
	if len(loadLog(t, root).Entries) != 2 {
		t.Error("settling must not add migrations to the log")
	}

	// 2. Again: nothing to do.
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	if got := prod.query(`SELECT count(*)::text FROM dpg.migrations`); got != "2" {
		t.Errorf("rerun: %s rows", got)
	}

	// 3. A migration made on dev, run on prod by another runner from its
	// file: prod's ledger records it, apply then has nothing to do, and the
	// file run again changes nothing.
	deployTo(t, root, dev)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int);\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	l = loadLog(t, root)
	sqlPath, _ := l.Paths(l.Head())
	file, _ := os.ReadFile(sqlPath)
	if _, err := prod.conn.Exec(context.Background(), string(file)); err != nil {
		t.Fatalf("running the migration file on prod: %v", err)
	}
	deployTo(t, root, prod)
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	if got := prod.query(`SELECT count(*)::text FROM dpg.migrations WHERE status = 'complete'`); got != "3" {
		t.Errorf("after the external run: %s complete rows", got)
	}
	if _, err := prod.conn.Exec(context.Background(), string(file)); err == nil || !strings.Contains(err.Error(), "already been applied") {
		t.Errorf("running the file again: %v", err)
	}
	// An older migration's file, out of order, is refused too.
	first, _ := l.Paths(l.Entries[0].ID)
	old, _ := os.ReadFile(first)
	if _, err := prod.conn.Exec(context.Background(), string(old)); err == nil || !strings.Contains(err.Error(), "already been applied") {
		t.Errorf("an old migration again: %v", err)
	}

	// 4. A migration that fails on prod (a row its new CHECK rejects: only
	// running it finds that) is retried once the cause is gone.
	deployTo(t, root, dev)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int, CONSTRAINT x_pos CHECK (x >= 0));\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	deployTo(t, root, prod)
	prod.exec(`INSERT INTO public.t (id, x) VALUES (100, -1)`)
	if err := applyErr(t, root); err == nil {
		t.Fatal("settling must fail while prod has a row the CHECK rejects")
	}
	if got := prod.query(`SELECT status FROM dpg.migrations ORDER BY started_at DESC LIMIT 1`); got != "started" {
		t.Errorf("the failed attempt's row: %s", got)
	}
	prod.exec(`DELETE FROM public.t WHERE id = 100`)
	if err := applyErr(t, root); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := prod.query(`SELECT count(*)::text FROM dpg.migrations WHERE status = 'complete'`); got != "4" {
		t.Errorf("after the retry: %s complete rows", got)
	}

	// 5. A step after the transaction fails on prod (duplicates for a
	// unique index built concurrently): the migration is committed, apply
	// refuses until --resume finishes it.
	deployTo(t, root, dev)
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int, y int, z int, CONSTRAINT x_pos CHECK (x >= 0)) {\n    INDEXES {\n        UNIQUE CONCURRENTLY t_x_u (x);\n    }\n}\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	deployTo(t, root, prod)
	prod.exec(`INSERT INTO public.t (id, x) VALUES (1, 7), (2, 7)`)
	if err := applyErr(t, root); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("after-commit failure: %v", err)
	}
	if got := prod.query(`SELECT status FROM dpg.migrations ORDER BY started_at DESC LIMIT 1`); got != "committed" {
		t.Errorf("the migration's row: %s", got)
	}
	if err := applyErr(t, root); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Errorf("apply without --resume: %v", err)
	}
	prod.exec(`DELETE FROM public.t WHERE id = 2`, `DROP INDEX IF EXISTS public.t_x_u`)
	if err := applyErr(t, root, "--resume"); err != nil {
		t.Fatalf("--resume: %v", err)
	}
	if got := prod.query(`SELECT count(*)::text FROM dpg.migrations WHERE status = 'complete'`); got != "5" {
		t.Errorf("after --resume: %s complete rows", got)
	}
	if got := prod.query(`SELECT indexname FROM pg_indexes WHERE indexname = 't_x_u'`); got != "t_x_u" {
		t.Errorf("the index after --resume: %q", got)
	}

	// 6. A database whose last migration isn't in the log is refused.
	prod.exec(`INSERT INTO dpg.migrations (log, id, parent, checksum, dpg_version, status) VALUES ('dpgtest', 'elsewhere', '` + loadLog(t, root).Head() + `', 'x', 'x', 'complete')`)
	if err := applyErr(t, root); err == nil || !strings.Contains(err.Error(), "DPG-E050") {
		t.Errorf("unknown last migration: %v", err)
	}
	prod.exec(`DELETE FROM dpg.migrations WHERE id = 'elsewhere'`)

	// 7. A database with the log's objects but no ledger: refused until
	// --baseline finds how far along it is.
	prod.exec(`DROP SCHEMA dpg CASCADE`)
	if err := applyErr(t, root); err == nil || !strings.Contains(err.Error(), "--baseline") {
		t.Fatalf("no ledger: %v", err)
	}
	if err := applyErr(t, root, "--baseline"); err != nil {
		t.Fatalf("--baseline: %v", err)
	}
	if got := prod.query(`SELECT count(*)::text FROM dpg.migrations WHERE status = 'complete'`); got != "5" {
		t.Errorf("--baseline recorded %s migrations, want 5", got)
	}
	if err := applyErr(t, root); err != nil {
		t.Errorf("apply after --baseline: %v", err)
	}
}

// TestLedgerOffLive: with [ledger] enabled = false nothing is written to
// the database besides the migration itself.
func TestLedgerOffLive(t *testing.T) {
	dev := startPG(t)
	root := resolveProject(t, dev.url, map[string]string{tFile: "TABLE t (id int PRIMARY KEY);\n"})
	dev.exec(`DROP SCHEMA dpg CASCADE`)
	if err := os.WriteFile(filepath.Join(root, "dpg.toml"), []byte(rootTOML()+"\n[ledger]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int);\n")
	if err := applyErr(t, root); err != nil {
		t.Fatal(err)
	}
	if got := dev.query(`SELECT count(*)::text FROM pg_namespace WHERE nspname = 'dpg'`); got != "0" {
		t.Errorf("the ledger schema exists with the ledger off")
	}
	if len(loadLog(t, root).Entries) != 2 {
		t.Error("the log is kept with the ledger off")
	}
}
