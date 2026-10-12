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

// TestRoundtripConstraintTrigger guards RFC Section 7.9: a CONSTRAINT
// trigger with FROM and DEFERRABLE INITIALLY DEFERRED is created as one,
// fires at commit, and re-planning and the live comparison find nothing.
func TestRoundtripConstraintTrigger(t *testing.T) {
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
	src := `TABLE accounts (id int PRIMARY KEY);
TABLE entries (id int PRIMARY KEY, account_id int, amount numeric) {
    TRIGGERS {
        check_balance CONSTRAINT AFTER INSERT OR UPDATE
            FROM accounts
            DEFERRABLE INITIALLY DEFERRED
            FOR EACH ROW
            EXECUTE FUNCTION reject_negative();
    }
}
FUNCTION reject_negative() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT sum(amount) FROM entries WHERE account_id = NEW.account_id) < 0 THEN
        RAISE EXCEPTION 'negative balance';
    END IF;
    RETURN NULL;
END $$;
`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	rows, err := conn.QueryRows(ctx, `
SELECT t.tgconstraint <> 0, t.tgconstrrelid::regclass::text, t.tgdeferrable, t.tginitdeferred
FROM pg_trigger t WHERE t.tgname = 'check_balance'`)
	if err != nil {
		t.Fatal(err)
	}
	var isConstraint, deferrable, deferred bool
	var from string
	if !rows.Next() {
		t.Fatal("trigger missing")
	}
	if err := rows.Scan(&isConstraint, &from, &deferrable, &deferred); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !isConstraint || from != "accounts" || !deferrable || !deferred {
		t.Errorf("catalog: constraint=%v from=%q deferrable=%v deferred=%v", isConstraint, from, deferrable, deferred)
	}

	// Deferred: a temporarily negative balance is fine inside the
	// transaction and rejected only at commit.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO accounts VALUES (1)`,
		`INSERT INTO entries VALUES (1, 1, -5)`,
		`INSERT INTO entries VALUES (2, 1, 10)`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit with a final positive balance: %v", err)
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO entries VALUES (3, 1, -100)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(ctx); err == nil || !strings.Contains(err.Error(), "negative balance") {
		t.Errorf("commit with a negative balance: got %v, want the trigger's error", err)
	}

	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
