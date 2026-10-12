package main

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func viewPlan(t *testing.T, src string, snap *pipeline.Snapshot) ([]pipeline.IRObject, []string) {
	t.Helper()
	file, dir := dpgTempFile(t, src)
	desired, _, err := compiler.CompileWith([]string{file}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := diff.New().Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var sql []string
	for _, op := range ops {
		sql = append(sql, op.SQL())
	}
	return desired, sql
}

// TestViewOptionsAndColumnsEmitted guards RFC Section 8.1: view options,
// the check option and the column-name list reach the generated SQL; an
// option-only change is ALTER VIEW SET/RESET; a query change restates the
// options in CREATE OR REPLACE VIEW, which would otherwise reset them.
func TestViewOptionsAndColumnsEmitted(t *testing.T) {
	const table = "TABLE users (id int, email text, tenant int);\n"
	desired, sql := viewPlan(t, table+`VIEW a (user_id, mail) AS SELECT id, email FROM users;
VIEW b WITH (security_barrier, security_invoker = true) AS SELECT id FROM users WHERE tenant = 1 WITH LOCAL CHECK OPTION;
VIEW c (x, y) AS SELECT * FROM users;
`, &pipeline.Snapshot{})
	all := strings.Join(sql, "\n")
	for _, want := range []string{
		`CREATE VIEW "public"."a" AS SELECT id AS user_id, email AS mail FROM users;`,
		`CREATE VIEW "public"."b" WITH (security_barrier=true, security_invoker=true, check_option=local) AS`,
		`CREATE VIEW "public"."c" ("x", "y") AS SELECT * FROM users;`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}

	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, desired); err != nil {
		t.Fatal(err)
	}
	_, sql = viewPlan(t, table+`VIEW a (user_id, mail) AS SELECT id, email FROM users;
VIEW b WITH (security_barrier = false) AS SELECT id FROM users WHERE tenant = 1;
VIEW c (x, y) AS SELECT * FROM users;
`, snap)
	all = strings.Join(sql, "\n")
	for _, want := range []string{
		`ALTER VIEW "public"."b" SET (security_barrier=false);`,
		`ALTER VIEW "public"."b" RESET (security_invoker, check_option);`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}

	_, sql = viewPlan(t, table+`VIEW a (user_id, mail) AS SELECT id, email FROM users;
VIEW b WITH (security_barrier, security_invoker = true) AS SELECT id FROM users WHERE tenant = 2 WITH LOCAL CHECK OPTION;
VIEW c (x, y) AS SELECT * FROM users;
`, snap)
	if want := `CREATE OR REPLACE VIEW "public"."b" WITH (security_barrier=true, security_invoker=true, check_option=local) AS`; !strings.Contains(strings.Join(sql, "\n"), want) || len(sql) != 1 {
		t.Errorf("want only %q, got:\n%s", want, strings.Join(sql, "\n"))
	}
}
