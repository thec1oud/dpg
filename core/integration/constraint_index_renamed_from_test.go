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

// TestRoundtripConstraintRenamedFrom is the regression guard for table-level
// constraint rename detection: before this, ir.Constraint had no
// RenamedFrom field at all, so a renamed constraint was indistinguishable
// from "old constraint dropped, new one added" — a real
// ALTER TABLE ... DROP CONSTRAINT + ADD CONSTRAINT pair instead of a
// metadata-only rename. For a NOT VALID constraint, drop+add would also
// silently re-validate against existing rows (the constraint returns to
// its initial, unvalidated state on re-add) — not a concern this test
// exercises directly, but the underlying reason a real ALTER ... RENAME
// CONSTRAINT matters beyond just avoiding an extra statement.
func TestRoundtripConstraintRenamedFrom(t *testing.T) {
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

	v1 := `TABLE orders (amount NUMERIC) {
    CONSTRAINT ck_amount_old CHECK (amount > 0);
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	constraintExists := func(name string) bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = $1 AND conrelid = 'public.orders'::regclass`, name)
		if err != nil {
			t.Fatalf("query pg_constraint for %s: %v", name, err)
		}
		defer rows.Close()
		var n int
		rows.Next()
		_ = rows.Scan(&n)
		return n == 1
	}
	if !constraintExists("ck_amount_old") {
		t.Fatalf("ck_amount_old does not exist after initial apply")
	}

	v2 := `TABLE orders (amount NUMERIC) {
    CONSTRAINT ck_amount_positive CHECK (amount > 0) RENAMED FROM ck_amount_old;
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if constraintExists("ck_amount_old") {
		t.Fatalf("ck_amount_old still exists live after rename — the rename didn't land")
	}
	if !constraintExists("ck_amount_positive") {
		t.Fatalf("ck_amount_positive doesn't exist live after rename")
	}
}

// TestRoundtripIndexRenamedFrom is the analogous regression guard for index
// rename detection, live-verified with the same OID-stability check used for
// today's earlier Table/Partition rename fixes: a real DROP INDEX + CREATE
// INDEX would produce a different OID (and, on a large table, cost a real
// rebuild), whereas ALTER INDEX ... RENAME TO is metadata-only.
func TestRoundtripIndexRenamedFrom(t *testing.T) {
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

	v1 := `TABLE users (email TEXT) {
    INDEXES {
        users_email_idx_old (email);
    }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	regclassOID := func(qualified string) *int64 {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT to_regclass($1)::oid::bigint`, qualified)
		if err != nil {
			t.Fatalf("query oid for %s: %v", qualified, err)
		}
		defer rows.Close()
		if !rows.Next() {
			return nil
		}
		var oid *int64
		if err := rows.Scan(&oid); err != nil {
			t.Fatalf("scan oid for %s: %v", qualified, err)
		}
		return oid
	}

	oldOID := regclassOID("public.users_email_idx_old")
	if oldOID == nil {
		t.Fatalf("public.users_email_idx_old does not exist after initial apply")
	}

	v2 := `TABLE users (email TEXT) {
    INDEXES {
        users_email_idx (email) RENAMED FROM users_email_idx_old;
    }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if regclassOID("public.users_email_idx_old") != nil {
		t.Fatalf("public.users_email_idx_old still exists after rename")
	}
	renamedOID := regclassOID("public.users_email_idx")
	if renamedOID == nil {
		t.Fatalf("public.users_email_idx does not exist after rename")
	}
	if *renamedOID != *oldOID {
		t.Fatalf("public.users_email_idx has a different OID (%d) than users_email_idx_old had (%d) — dropped and recreated instead of renamed", *renamedOID, *oldOID)
	}
}
