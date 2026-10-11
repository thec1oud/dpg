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

// TestRoundtripEnumPositionalAddAndRenameValue is the live regression guard
// for RFC audit items #18/#19: Section 5.1.1's positional ADD VALUE
// (BEFORE/AFTER, inferred purely from where a new value sits in the
// desired list, not a separate directive) and RENAME VALUE (a new
// enum-block directive — real CREATE TYPE ... AS ENUM has no inline form
// for either, confirmed live, so RENAME VALUE is modeled as a block
// directive rather than the RFC's literal inline placement).
//
// Confirms live: a value inserted in the middle of the list lands at the
// correct position (via pg_enum.enumsortorder, not just appended), a
// renamed value keeps its OID and any row still referencing it (existing
// rows are keyed by OID, not label, so a rename never touches table data),
// and a second plan against freshly introspected live state is a genuine
// no-op.
func TestRoundtripEnumPositionalAddAndRenameValue(t *testing.T) {
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

	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatalf("write schema: %v", err)
		}
	}

	enumState := func() []struct {
		label string
		oid   int64
	} {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT enumlabel, oid::bigint FROM pg_enum WHERE enumtypid = 'status'::regtype ORDER BY enumsortorder`)
		if err != nil {
			t.Fatalf("query pg_enum: %v", err)
		}
		defer rows.Close()
		var out []struct {
			label string
			oid   int64
		}
		for rows.Next() {
			var label string
			var oid int64
			if err := rows.Scan(&label, &oid); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, struct {
				label string
				oid   int64
			}{label, oid})
		}
		return out
	}

	write(`TYPE status AS ENUM ('active', 'shipped');

TABLE orders (id INTEGER, s status NOT NULL DEFAULT 'active');`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if _, err := conn.Exec(ctx, `INSERT INTO orders (id, s) VALUES (1, 'shipped');`); err != nil {
		t.Fatalf("insert row: %v", err)
	}

	before := enumState()
	var shippedOID int64
	for _, e := range before {
		if e.label == "shipped" {
			shippedOID = e.oid
		}
	}
	if shippedOID == 0 {
		t.Fatal("shipped not found after initial apply")
	}

	// Insert 'confirmed' in the middle (between active and shipped) and
	// rename 'shipped' to 'delivered' in the same migration.
	write(`TYPE status AS ENUM ('active', 'confirmed', 'delivered') {
    VALUE 'delivered' RENAMED FROM 'shipped';
}

TABLE orders (id INTEGER, s status NOT NULL DEFAULT 'active');`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	after := enumState()
	if len(after) != 3 {
		t.Fatalf("expected 3 enum values, got %d: %+v", len(after), after)
	}
	if after[0].label != "active" || after[1].label != "confirmed" || after[2].label != "delivered" {
		t.Fatalf("expected order [active, confirmed, delivered], got %+v", after)
	}
	if after[2].oid != shippedOID {
		t.Fatalf("delivered has a different OID (%d) than shipped had (%d) — value was dropped and recreated instead of renamed", after[2].oid, shippedOID)
	}

	// The existing row must still reference the (renamed) value.
	rows, err := conn.QueryRows(ctx, `SELECT s::text FROM orders WHERE id = 1`)
	if err != nil {
		t.Fatalf("query orders: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row id=1 missing after rename")
	}
	var s string
	_ = rows.Scan(&s)
	rows.Close()
	if s != "delivered" {
		t.Fatalf("expected existing row to now show 'delivered', got %q", s)
	}

	noDriftAgainstLive(t, ctx, conn, []string{f}, dir, differ, store)
}
