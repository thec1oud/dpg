package main

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// TestConstraintTriggerEmitted guards RFC Section 7.9: a CONSTRAINT
// trigger entry emits CREATE CONSTRAINT TRIGGER with its FROM table and
// deferral clause, and re-plans against its own snapshot as a no-op.
func TestConstraintTriggerEmitted(t *testing.T) {
	file, dir := dpgTempFile(t, `TABLE accounts (id int PRIMARY KEY);
TABLE entries (id int PRIMARY KEY, account_id int) {
    TRIGGERS {
        check_balance CONSTRAINT AFTER INSERT OR UPDATE
            FROM accounts
            DEFERRABLE INITIALLY DEFERRED
            FOR EACH ROW
            EXECUTE FUNCTION reject_negative();
    }
}`)
	desired, _, err := compiler.CompileWith([]string{file}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := diff.New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, op := range ops {
		all = append(all, op.SQL())
	}
	want := `CREATE CONSTRAINT TRIGGER "check_balance" AFTER INSERT OR UPDATE ON "public"."entries" FROM "accounts" DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_negative();`
	if !strings.Contains(strings.Join(all, "\n"), want) {
		t.Errorf("missing %q in:\n%s", want, strings.Join(all, "\n"))
	}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, desired); err != nil {
		t.Fatal(err)
	}
	if ops, _ := diff.New().Diff(desired, snap); len(ops) != 0 {
		t.Errorf("re-plan not a no-op: %d ops", len(ops))
	}
}
