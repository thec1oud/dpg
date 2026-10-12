//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// resolveProject writes a one-cluster, one-database project at a temporary
// root and applies it.
func resolveProject(t *testing.T, connStr string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{
		"dpg.toml":             rootTOML(),
		"c/dpg.toml":           clusterTOML("c", connStr),
		"c/dpgtest/dpg.toml":   databaseTOML("dpgtest", "public"),
		"c/cluster/.keep":      "",
		".dpg/snapshots/.keep": "",
	}
	for k, v := range files {
		all[k] = v
	}
	for name, content := range all {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runDPG(t, root, "apply", "--yes")
	return root
}

// runDPG runs a dpg command against the project at root.
func runDPG(t *testing.T, root string, args ...string) error {
	t.Helper()
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"-C", root}, args...))
	var err error
	captureStdout(t, func() { err = cmd.Execute() })
	if err != nil && args[0] == "apply" {
		t.Fatalf("dpg %s: %v", strings.Join(args, " "), err)
	}
	return err
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

// resolveItems is every item verify reports for the project, as
// "group id" (acknowledged items as "acknowledged id").
func resolveItems(t *testing.T, root string) []string {
	t.Helper()
	projectDir = root
	env, err := loadResolveEnv(resolveFlags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var out []string
	for _, tg := range env.targets {
		for _, it := range tg.Report.Items {
			g := it.Group.String()
			if it.Acknowledged != nil {
				g = "acknowledged"
			}
			out = append(out, g+" "+it.ID)
		}
	}
	sort.Strings(out)
	return out
}

func findItem(t *testing.T, items []string, group, prefix string) string {
	t.Helper()
	for _, s := range items {
		g, id, _ := strings.Cut(s, " ")
		if g == group && strings.HasPrefix(id, prefix) {
			return id
		}
	}
	t.Fatalf("no %s item %s* in:\n  %s", group, prefix, strings.Join(items, "\n  "))
	return ""
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// TestResolveLive drives dpg resolve end to end: each action against real
// changes made outside DPG, then verify must report the item gone (or
// acknowledged), with the source edited the way a person would have.
func TestResolveLive(t *testing.T) {
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

	const table = "TABLE t (\n    id   int NOT NULL,\n    name text DEFAULT 'a'\n);\n"
	root := resolveProject(t, connStr, map[string]string{
		"c/dpgtest/schemas/public/tables/t.dpg": table,
		"c/dpgtest/schemas/public/views.dpg":    "VIEW v AS SELECT 1 AS x;\n\nVIEW keep AS SELECT 2 AS y;\n",
	})
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected a clean project after apply, got %v", items)
	}

	exec(`ALTER TABLE public.t ALTER COLUMN name SET DEFAULT 'b'`,
		`CREATE TABLE public.u (x int)`,
		`CREATE TABLE public.w (x int)`,
		`DROP VIEW public.v`)
	items := resolveItems(t, root)
	t.Logf("items:\n  %s", strings.Join(items, "\n  "))
	changed := findItem(t, items, "drift", "table:public.t/")
	newTable := findItem(t, items, "unmanaged", "table:public.u")
	ignored := findItem(t, items, "unmanaged", "table:public.w")
	missing := findItem(t, items, "drift", "view:public.v")

	// --dry-run changes nothing.
	if err := runDPG(t, root, "resolve", "--adopt", changed, "--dry-run"); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := readFile(t, root, "c/dpgtest/schemas/public/tables/t.dpg"); got != table {
		t.Fatalf("dry run edited the source:\n%s", got)
	}

	// Adopt the changed default, the new table and the dropped view; ignore w.
	err = runDPG(t, root, "resolve", "--yes",
		"--adopt", changed, "--adopt", newTable, "--adopt", missing, "--ignore", ignored)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := readFile(t, root, "c/dpgtest/schemas/public/tables/t.dpg"); !strings.Contains(got, "name text DEFAULT 'b'") ||
		strings.Contains(got, "'a'") {
		t.Errorf("changed default not adopted:\n%s", got)
	}
	if got := readFile(t, root, "c/dpgtest/schemas/public/tables/u.dpg"); !strings.Contains(got, "TABLE u") {
		t.Errorf("new table not adopted into tables/u.dpg:\n%s", got)
	}
	if got := readFile(t, root, "c/dpgtest/schemas/public/views.dpg"); strings.Contains(got, "VIEW v ") ||
		!strings.Contains(got, "VIEW keep AS SELECT 2 AS y;") {
		t.Errorf("dropped view not removed (or the other one damaged):\n%s", got)
	}
	if got := readFile(t, root, "c/dpgtest/dpg.toml"); !strings.Contains(got, "[ignore]\ntables = [\"public.w\"]") {
		t.Errorf("ignore pattern not added to the database's config:\n%s", got)
	}
	if got := readFile(t, root, "c/dpg.toml"); strings.Contains(got, "ignore") {
		t.Errorf("a database object's pattern must not go to the cluster config:\n%s", got)
	}
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected nothing left, got:\n  %s", strings.Join(items, "\n  "))
	}
	// The adopted state is the declared state: plan has nothing to do.
	if err := runDPG(t, root, "plan", "--live"); err != nil {
		t.Errorf("plan --live: %v", err)
	}

	// Revert: the default changes again and is put back in the database.
	exec(`ALTER TABLE public.t ALTER COLUMN name SET DEFAULT 'c'`)
	items = resolveItems(t, root)
	changed = findItem(t, items, "drift", "table:public.t/")
	if err := runDPG(t, root, "resolve", "--yes", "--revert", changed); err != nil {
		t.Fatalf("revert: %v", err)
	}
	var def string
	rows, err := conn.QueryRows(ctx, `SELECT pg_get_expr(adbin, adrelid) FROM pg_attrdef d JOIN pg_attribute a
		ON a.attrelid = d.adrelid AND a.attnum = d.adnum WHERE a.attrelid = 'public.t'::regclass AND a.attname = 'name'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		if err := rows.Scan(&def); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if def != "'b'::text" {
		t.Errorf("revert: default is %s, want 'b'::text", def)
	}
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("after revert: %v", items)
	}

	// Acknowledge, then unacknowledge.
	exec(`ALTER TABLE public.t ALTER COLUMN name SET DEFAULT 'd'`)
	items = resolveItems(t, root)
	changed = findItem(t, items, "drift", "table:public.t/")
	if err := runDPG(t, root, "resolve", "--yes", "--ack", changed, "--reason", "hotfix", "--expires", "2099-01-01"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	items = resolveItems(t, root)
	findItem(t, items, "acknowledged", changed)
	if err := runDPG(t, root, "verify"); err != nil {
		t.Errorf("verify must pass with the drift acknowledged: %v", err)
	}
	acks, err := drift.LoadAcks(filepath.Join(root, ".dpg", "acknowledged", "c", "dpgtest.json"))
	if err != nil || len(acks) != 1 || acks[0].Reason != "hotfix" {
		t.Errorf("ack file: %+v %v", acks, err)
	}
	if err := runDPG(t, root, "resolve", "--yes", "--unack", changed); err != nil {
		t.Fatalf("unack: %v", err)
	}
	findItem(t, resolveItems(t, root), "drift", changed)
	if err := runDPG(t, root, "verify"); err == nil {
		t.Error("verify must fail once the acknowledgement is removed")
	}

	// A disallowed action is refused and changes nothing.
	exec(`CREATE TABLE public.z (x int)`)
	z := findItem(t, resolveItems(t, root), "unmanaged", "table:public.z")
	if err := runDPG(t, root, "resolve", "--yes", "--revert", z); err == nil {
		t.Error("revert of an unmanaged object must be refused")
	}
	if err := runDPG(t, root, "resolve", "--yes", "--adopt", "table:public.nope"); err == nil {
		t.Error("an unknown item must be refused")
	}
}
