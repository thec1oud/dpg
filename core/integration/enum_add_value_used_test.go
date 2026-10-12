//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestRoundtripEnumAddValueUsedInSameMigration guards RFC Section 5.1.1
// against a real database: adding an enum value and, in the same
// migration, a column default that uses it must apply. PostgreSQL rejects
// using a value added in the same uncommitted transaction, so the ADD VALUE
// has to run before the transaction.
func TestRoundtripEnumAddValueUsedInSameMigration(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	store := newMemStore()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")

	v1 := `TYPE status AS ENUM ('draft', 'sent');
TABLE invoices (id int PRIMARY KEY, status status NOT NULL DEFAULT 'draft');`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	v2 := `TYPE status AS ENUM ('draft', 'pending', 'sent');
TABLE invoices (id int PRIMARY KEY, status status NOT NULL DEFAULT 'pending');`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if _, err := conn.Exec(ctx, `INSERT INTO invoices (id) VALUES (1)`); err != nil {
		t.Fatalf("insert using the new default: %v", err)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
