//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestRoundtripViewOptionsAndColumns guards RFC Section 8.1: a view's
// WITH (...) options, its WITH [LOCAL|CASCADED] CHECK OPTION, and its
// column-name list all reach the database; re-planning and comparing with
// the live catalog find nothing; an option change is an ALTER VIEW, and a
// query change keeps the options. Views over `*` keep a column list as a
// separate clause and match the catalog's expanded form.
func TestRoundtripViewOptionsAndColumns(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()
	differ, emitter, applyExec, store := diff.New(), emit.New(), executor.New(), newMemStore()
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	query := func(q string) string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s *string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			if s != nil {
				out = append(out, *s)
			}
		}
		return strings.Join(out, ",")
	}
	reloptions := func(view string) string {
		return query(`SELECT array_to_string(reloptions, ',') FROM pg_class WHERE relname = '` + view + `'`)
	}
	columns := func(view string) string {
		return query(`SELECT attname::text FROM pg_attribute WHERE attrelid = 'public.` + view + `'::regclass AND attnum > 0 ORDER BY attnum`)
	}

	const base = `TABLE users (id int, email text, tenant int);
TABLE orders (id int, user_id int, total numeric);
VIEW plain_star AS SELECT * FROM users WHERE tenant > 0;
VIEW join_star AS SELECT u.*, o.total FROM users u JOIN orders o ON o.user_id = u.id;
`
	write(base + `VIEW user_summary (user_id, mail) AS SELECT id, email FROM users;
VIEW secure_view WITH (security_barrier, security_invoker = true) AS SELECT id FROM users WHERE tenant = 1;
VIEW active AS SELECT id, tenant FROM users WHERE tenant > 0 WITH LOCAL CHECK OPTION;
VIEW starred (a, b) AS SELECT * FROM users;
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := columns("user_summary"); got != "user_id,mail" {
		t.Errorf("user_summary columns = %q", got)
	}
	if got := columns("starred"); got != "a,b,tenant" {
		t.Errorf("starred columns = %q", got)
	}
	if got := reloptions("secure_view"); got != "security_barrier=true,security_invoker=true" {
		t.Errorf("secure_view reloptions = %q", got)
	}
	if got := reloptions("active"); got != "check_option=local" {
		t.Errorf("active reloptions = %q", got)
	}
	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	// Options change only: targeted ALTER VIEW SET/RESET.
	write(base + `VIEW user_summary (user_id, mail) AS SELECT id, email FROM users;
VIEW secure_view WITH (security_barrier = false) AS SELECT id FROM users WHERE tenant = 1;
VIEW active AS SELECT id, tenant FROM users WHERE tenant > 0 WITH CASCADED CHECK OPTION;
VIEW starred (a, b) AS SELECT * FROM users;
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if got := reloptions("secure_view"); got != "security_barrier=false" {
		t.Errorf("after option change, secure_view reloptions = %q", got)
	}
	if got := reloptions("active"); got != "check_option=cascaded" {
		t.Errorf("after option change, active reloptions = %q", got)
	}
	assertNoReplan(t, []string{f}, dir, differ, store)

	// Query change: CREATE OR REPLACE VIEW keeps the declared options.
	write(base + `VIEW user_summary (user_id, mail) AS SELECT id, email FROM users;
VIEW secure_view WITH (security_barrier = false) AS SELECT id FROM users WHERE tenant = 2;
VIEW active AS SELECT id, tenant FROM users WHERE tenant > 0 WITH CASCADED CHECK OPTION;
VIEW starred (a, b) AS SELECT * FROM users;
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if got := reloptions("secure_view"); got != "security_barrier=false" {
		t.Errorf("after query change, secure_view reloptions = %q", got)
	}
	assertNoReplan(t, []string{f}, dir, differ, store)
}
