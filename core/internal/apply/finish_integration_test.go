//go:build integration

package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
	"github.com/thec1oud/dpg/internal/ui"
)

// TestPrepareFinishLive: what prepareFinish makes of a CREATE INDEX
// CONCURRENTLY to run again, by the index of its name it finds.
func TestPrepareFinishLive(t *testing.T) {
	ctx := context.Background()
	conn, err := executor.Connect(ctx, testpg.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE t (x int); CREATE TABLE o (x int)`)
	build := finishOp{sql: `CREATE INDEX CONCURRENTLY "t x" ON "public"."t" ("x");`, migration: "m1"}
	asBuilt := func(same bool) builtFunc {
		return func(_ context.Context, table, name string) (bool, error) {
			if table != "public.t" || name != "t x" {
				t.Errorf("built(%q, %q)", table, name)
			}
			return same, nil
		}
	}
	prepare := func(built builtFunc) ([]pipeline.DiffOp, error) {
		t.Helper()
		return prepareFinish(ctx, conn, []pipeline.DiffOp{build}, built)
	}
	sqls := func(ops []pipeline.DiffOp) string {
		var out []string
		for _, op := range ops {
			out = append(out, op.SQL())
		}
		return strings.Join(out, " | ")
	}

	// None: built as the step has it.
	if ops, err := prepare(asBuilt(true)); err != nil || sqls(ops) != build.sql {
		t.Errorf("no index: %s, %v", sqls(ops), err)
	}

	// Valid, and as the migration builds it: done.
	exec(`CREATE INDEX "t x" ON t (x)`)
	if ops, err := prepare(asBuilt(true)); err != nil || len(ops) != 0 {
		t.Errorf("built: %s, %v", sqls(ops), err)
	}

	// Valid, but defined otherwise: refused, not dropped.
	ops, err := prepare(asBuilt(false))
	if err == nil || !strings.HasPrefix(err.Error(), "DPG-E058: ") {
		t.Errorf("defined otherwise: %s, %v", sqls(ops), err)
	}
	if db := (*ui.DBError)(nil); errors.As(err, &db) {
		t.Errorf("a refusal of DPG's is a database error: %v", err)
	}

	// Invalid: dropped, then built.
	exec(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'public."t x"'::regclass`)
	ops, err = prepare(func(context.Context, string, string) (bool, error) {
		t.Error("built asked of an invalid index")
		return true, nil
	})
	if want := `DROP INDEX CONCURRENTLY IF EXISTS "public"."t x"; | ` + build.sql; err != nil || sqls(ops) != want {
		t.Errorf("invalid: %s, %v", sqls(ops), err)
	} else if id := ops[0].(pipeline.IdentifiedOp); id.MigrationID() != "m1" || id.StepID() != "" || id.Guard() {
		t.Errorf("the drop is of migration %q, step %q, guard %v", id.MigrationID(), id.StepID(), id.Guard())
	}

	// On another table: refused.
	exec(`DROP INDEX "t x"; CREATE INDEX "t x" ON o (x)`)
	ops, err = prepare(asBuilt(true))
	if err == nil || !strings.HasPrefix(err.Error(), "DPG-E057: ") || !strings.Contains(err.Error(), "is on o") {
		t.Errorf("another table's: %s, %v", sqls(ops), err)
	}
	if db := (*ui.DBError)(nil); errors.As(err, &db) {
		t.Errorf("a refusal of DPG's is a database error: %v", err)
	}
}
