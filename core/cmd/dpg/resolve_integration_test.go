//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/resolve/tui"
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
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Logf("dpg %s:\n%s", strings.Join(args, " "), out)
	}
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

	// Per object: every item of public.t at once.
	exec(`ALTER TABLE public.t ADD COLUMN extra int`)
	if err := runDPG(t, root, "resolve", "--yes", "--ack", "table:public.t", "--reason", "both"); err != nil {
		t.Fatalf("ack object: %v", err)
	}
	items = resolveItems(t, root)
	findItem(t, items, "acknowledged", changed)
	findItem(t, items, "acknowledged", "table:public.t/column:extra")
	if err := runDPG(t, root, "resolve", "--yes", "--unack", "table:public.t"); err != nil {
		t.Fatalf("unack object: %v", err)
	}
	items = resolveItems(t, root)
	findItem(t, items, "drift", changed)
	findItem(t, items, "drift", "table:public.t/column:extra")
	if err := runDPG(t, root, "resolve", "--yes", "--revert", "table:public.t/column:extra", "--allow-destructive"); err != nil {
		t.Fatalf("revert extra column: %v", err)
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

// TestResolveAdoptInsertLive adopts parts the source doesn't declare yet:
// columns into multi-line and one-line column lists (a trailing comment
// stays with its entry), block statements into an existing block and into
// a declaration without one, and a role option into Part 1.
func TestResolveAdoptInsertLive(t *testing.T) {
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

	root := resolveProject(t, connStr, map[string]string{
		"c/cluster/roles.dpg":                   "ROLE app LOGIN CONNECTION LIMIT 3;\n",
		"c/dpgtest/schemas/public/tables/t.dpg": "TABLE t (\n    id   int PRIMARY KEY,\n    name text -- display name\n) {\n    COMMENT 'people';\n};\n",
		"c/dpgtest/schemas/public/tables/s.dpg": "TABLE s (id int PRIMARY KEY);\n",
		"c/dpgtest/schemas/public/tables/r.dpg": "TABLE r (\n    id  int PRIMARY KEY,\n    old int,\n    v   int\n) {\n    INDEX r_v_idx (v);\n};\n",
	})
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected a clean project after apply, got %v", items)
	}
	exec(`ALTER TABLE public.t ADD COLUMN age int`,
		`CREATE INDEX t_name_idx ON public.t (name)`,
		`ALTER TABLE public.s ADD COLUMN note text`,
		`COMMENT ON TABLE public.s IS 'stuff'`,
		`ALTER ROLE app CONNECTION LIMIT 5`)
	items := resolveItems(t, root)
	t.Logf("items:\n  %s", strings.Join(items, "\n  "))
	var args []string
	for _, s := range items {
		_, id, _ := strings.Cut(s, " ")
		args = append(args, "--adopt", id)
	}
	if err := runDPG(t, root, append([]string{"resolve", "--yes"}, args...)...); err != nil {
		t.Errorf("resolve: %v", err)
	}
	for _, f := range []string{"c/cluster/roles.dpg", "c/dpgtest/schemas/public/tables/t.dpg", "c/dpgtest/schemas/public/tables/s.dpg"} {
		t.Logf("%s:\n%s", f, readFile(t, root, f))
	}
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected nothing left, got:\n  %s", strings.Join(items, "\n  "))
	}

	// Second round: into what the first round created, and changes to
	// existing parts.
	exec(`CREATE INDEX t_age_idx ON public.t (age)`,
		`COMMENT ON COLUMN public.t.name IS 'shown'`,
		`ALTER TABLE public.t ADD COLUMN a1 int, ADD COLUMN a2 int`,
		`ALTER TABLE public.r DROP COLUMN old`,
		`DROP INDEX public.r_v_idx`, `CREATE UNIQUE INDEX r_v_idx ON public.r (v)`,
		`ALTER ROLE app CONNECTION LIMIT 5`,
		`CREATE ROLE newcomer NOLOGIN`)
	items = resolveItems(t, root)
	t.Logf("items:\n  %s", strings.Join(items, "\n  "))
	args = nil
	for _, s := range items {
		_, id, _ := strings.Cut(s, " ")
		args = append(args, "--adopt", id)
	}
	if err := runDPG(t, root, append([]string{"resolve", "--yes"}, args...)...); err != nil {
		t.Errorf("resolve: %v", err)
	}
	for _, f := range []string{"c/cluster/roles.dpg", "c/dpgtest/schemas/public/tables/t.dpg", "c/dpgtest/schemas/public/tables/r.dpg"} {
		t.Logf("%s:\n%s", f, readFile(t, root, f))
	}
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected nothing left, got:\n  %s", strings.Join(items, "\n  "))
	}
}

// TestResolveAdoptGrantsLive adopts every kind of grant change: a declared
// privilege no longer held, a declared ALL that lost a privilege, a grant
// made outside DPG (on the table and on a column), and a revoked privilege
// granted again.
func TestResolveAdoptGrantsLive(t *testing.T) {
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
	const table = `TABLE g (
    id     int PRIMARY KEY,
    secret text
) {
    GRANTS {
        SELECT, INSERT TO app;
        ALL TO rpt;
    }
    REVOKES {
        DELETE FROM aud;
    }
}
`
	root := resolveProject(t, connStr, map[string]string{
		"c/cluster/roles.dpg":                   "ROLE app;\n\nROLE rpt;\n\nROLE aud;\n",
		"c/dpgtest/schemas/public/tables/g.dpg": table,
	})
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected a clean project after apply, got %v", items)
	}
	exec(`REVOKE INSERT ON public.g FROM app`,
		`REVOKE TRUNCATE ON public.g FROM rpt`,
		`GRANT UPDATE ON public.g TO aud`,
		`GRANT DELETE ON public.g TO aud`,
		`GRANT SELECT (secret) ON public.g TO aud`)
	items := resolveItems(t, root)
	t.Logf("items:\n  %s", strings.Join(items, "\n  "))
	var args []string
	for _, s := range items {
		_, id, _ := strings.Cut(s, " ")
		args = append(args, "--adopt", id)
	}
	if err := runDPG(t, root, append([]string{"resolve", "--yes"}, args...)...); err != nil {
		t.Errorf("resolve: %v", err)
	}
	got := readFile(t, root, "c/dpgtest/schemas/public/tables/g.dpg")
	t.Logf("g.dpg:\n%s", got)
	for _, want := range []string{"SELECT TO app;", "TO rpt;", "UPDATE TO aud;", "DELETE TO aud;", "SELECT TO aud;"} {
		if !strings.Contains(got, want) {
			t.Errorf("g.dpg lacks %q", want)
		}
	}
	if strings.Contains(got, "TRUNCATE") || strings.Contains(got, "DELETE FROM aud") || strings.Contains(got, "INSERT TO app") {
		t.Errorf("g.dpg still declares what the database no longer has")
	}
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected nothing left, got:\n  %s", strings.Join(items, "\n  "))
	}
}

// TestResolveAdoptRequiredAndMacroLive adopts a table whose schema, column
// type and foreign-key target are undeclared too (they are adopted with
// it), and a changed column a macro declares, inlined at its use.
func TestResolveAdoptRequiredAndMacroLive(t *testing.T) {
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
	const macro = "MACRO audit (\n    created_at timestamptz NOT NULL DEFAULT now(),\n    updated_at timestamptz\n)\n\n"
	root := resolveProject(t, connStr, map[string]string{
		"c/dpgtest/schemas/public/tables/m.dpg": macro + "TABLE m (\n    id int PRIMARY KEY,\n    ...audit\n);\n",
	})
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected a clean project after apply, got %v", items)
	}
	exec(`CREATE SCHEMA cron`,
		`CREATE TYPE cron.state AS ENUM ('a', 'b')`,
		`CREATE TABLE cron.job (id int PRIMARY KEY, s cron.state)`,
		`CREATE TABLE cron.run (id int PRIMARY KEY, job int REFERENCES cron.job (id))`,
		`ALTER TABLE public.m ALTER COLUMN updated_at SET DEFAULT now()`)
	items := resolveItems(t, root)
	t.Logf("items:\n  %s", strings.Join(items, "\n  "))

	// Without --macro the macro column is refused, and nothing changes.
	col := findItem(t, items, "drift", "table:public.m/column:updated_at")
	if err := runDPG(t, root, "resolve", "--yes", "--adopt", col); err == nil {
		t.Error("adopting a macro-declared column without --macro must be refused")
	}
	if err := runDPG(t, root, "resolve", "--yes", "--macro", "inline", "--adopt", col, "--adopt", "table:cron.run"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, f := range []string{"c/dpgtest/schemas/public/tables/m.dpg", "c/dpgtest/schemas/cron/schema.dpg",
		"c/dpgtest/schemas/cron/types.dpg", "c/dpgtest/schemas/cron/tables/job.dpg", "c/dpgtest/schemas/cron/tables/run.dpg"} {
		got := readFile(t, root, f)
		t.Logf("%s:\n%s", f, got)
		if got == "" {
			t.Errorf("%s not written", f)
		}
	}
	m := readFile(t, root, "c/dpgtest/schemas/public/tables/m.dpg")
	if strings.Contains(m, "...audit") || !strings.Contains(m, "updated_at timestamp with time zone DEFAULT now()") || !strings.HasPrefix(m, macro) {
		t.Errorf("the spread wasn't inlined with the change (and the macro left alone):\n%s", m)
	}
	if items := resolveItems(t, root); len(items) != 0 {
		t.Fatalf("expected nothing left, got:\n  %s", strings.Join(items, "\n  "))
	}
}

// tuiKey turns "down", "a", "enter", ... into a key message.
func tuiKey(s string) tea.KeyMsg {
	named := map[string]tea.KeyType{"down": tea.KeyDown, "up": tea.KeyUp, "end": tea.KeyEnd, "home": tea.KeyHome,
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "backspace": tea.KeyBackspace, " ": tea.KeySpace}
	if k, ok := named[s]; ok {
		return tea.KeyMsg{Type: k}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// tuiDrive feeds keys to the model, running its commands synchronously.
func tuiDrive(m tea.Model, keys ...string) tea.Model {
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = m.Update(tuiKey(k))
		for cmd != nil {
			msg := cmd()
			if msg == nil {
				break
			}
			if _, quit := msg.(tea.QuitMsg); quit {
				return m
			}
			m, cmd = m.Update(msg)
		}
	}
	return m
}

// TestResolveTUILive drives the interactive dpg resolve with the real
// engine: adopt an unmanaged table, acknowledge a drifted column with a
// reason, each through filter, choice, preview and apply.
func TestResolveTUILive(t *testing.T) {
	ctx := context.Background()
	connStr := testpg.Start(t)
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	root := resolveProject(t, connStr, map[string]string{
		"c/dpgtest/schemas/public/tables/t.dpg": "TABLE t (\n    id   int PRIMARY KEY,\n    name text DEFAULT 'a'\n);\n",
	})
	for _, s := range []string{`CREATE TABLE public.u (x int)`, `ALTER TABLE public.t ALTER COLUMN name SET DEFAULT 'b'`} {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	projectDir = root
	env, err := loadResolveEnv(resolveFlags{})
	if err != nil {
		t.Fatal(err)
	}
	e := &tuiEngine{env: env, by: "tester"}
	var m tea.Model = tui.New(e)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	keys := []string{"/"}
	for _, r := range "table:public.u" {
		keys = append(keys, string(r))
	}
	keys = append(keys, "enter", "end", "a", "p", "y")
	m = tuiDrive(m, keys...)
	if v := m.View(); !strings.Contains(v, "resolved 1 item(s)") {
		t.Fatalf("after adopting:\n%s", v)
	}
	if got := readFile(t, root, "c/dpgtest/schemas/public/tables/u.dpg"); !strings.Contains(got, "TABLE u") {
		t.Errorf("u.dpg:\n%s", got)
	}

	// The tree was reloaded: only the default's drift is left.
	keys = []string{"/"}
	for _, r := range "column:name" {
		keys = append(keys, string(r))
	}
	keys = append(keys, "enter", "end", "n", "p")
	for _, r := range "hotfix" {
		keys = append(keys, string(r))
	}
	keys = append(keys, "enter", "enter", "y")
	m = tuiDrive(m, keys...)
	if v := m.View(); !strings.Contains(v, "resolved 1 item(s)") {
		t.Fatalf("after acknowledging:\n%s", v)
	}
	acks, err := drift.LoadAcks(filepath.Join(root, ".dpg", "acknowledged", "c", "dpgtest.json"))
	if err != nil || len(acks) != 1 || acks[0].Reason != "hotfix" || acks[0].By != "tester" {
		t.Errorf("acks: %+v %v", acks, err)
	}
	if err := runDPG(t, root, "verify"); err != nil {
		t.Errorf("verify: %v", err)
	}
	// Quitting with nothing chosen.
	if m = tuiDrive(m, "esc", "q"); !m.(tui.Model).Quitting() {
		t.Error("q didn't quit")
	}
}
