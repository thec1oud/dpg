//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestRoundtripTableReplicaIdentityAndClusterOn is the live regression
// guard for Section 7.11's REPLICA IDENTITY/CLUSTER ON directives:
// previously zero code anywhere, so a declared value had no effect at all.
// Proves against a real database that REPLICA IDENTITY FULL, USING INDEX,
// and CLUSTER ON actually take effect, and that a second apply of the same
// source is a true no-op (no drift from introspection round-tripping
// differently than the source declared).
func TestRoundtripTableReplicaIdentityAndClusterOn(t *testing.T) {
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

	src := `TABLE orders (
    id BIGINT NOT NULL,
    external_ref TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
) {
    INDEXES {
        idx_orders_created_at (created_at);
    }
    UNIQUE INDEX idx_orders_external_ref (external_ref);
    REPLICA IDENTITY USING INDEX idx_orders_external_ref;
    CLUSTER ON idx_orders_created_at;
}`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	replIdentMode := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT relreplident::text FROM pg_class WHERE relname = 'orders'`)
		if err != nil {
			t.Fatalf("query relreplident: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("orders table not found")
		}
		var mode string
		_ = rows.Scan(&mode)
		return mode
	}
	replIdentIndexName := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT ic.relname FROM pg_index pi JOIN pg_class ic ON ic.oid = pi.indexrelid JOIN pg_class t ON t.oid = pi.indrelid WHERE t.relname = 'orders' AND pi.indisreplident`)
		if err != nil {
			t.Fatalf("query replident index: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("no replica identity index found")
		}
		var name string
		_ = rows.Scan(&name)
		return name
	}
	clusteredIndexName := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT ic.relname FROM pg_index pi JOIN pg_class ic ON ic.oid = pi.indexrelid JOIN pg_class t ON t.oid = pi.indrelid WHERE t.relname = 'orders' AND pi.indisclustered`)
		if err != nil {
			t.Fatalf("query clustered index: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("no clustered index found")
		}
		var name string
		_ = rows.Scan(&name)
		return name
	}

	if got := replIdentMode(); got != "i" {
		t.Fatalf("expected relreplident = 'i' (index), got %q", got)
	}
	if got := replIdentIndexName(); got != "idx_orders_external_ref" {
		t.Fatalf("expected replica identity index idx_orders_external_ref, got %q", got)
	}
	if got := clusteredIndexName(); got != "idx_orders_created_at" {
		t.Fatalf("expected clustered index idx_orders_created_at, got %q", got)
	}

	// Re-applying the identical source must be a no-op — proves
	// introspection round-trips REPLICA IDENTITY/CLUSTER ON into the exact
	// same IR shape the source declared, not spurious drift.
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired, base)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "REPLICA IDENTITY") || strings.Contains(op.SQL(), "CLUSTER") {
			t.Errorf("expected no-op on second plan, got: %s", op.SQL())
		}
	}

	// Now switch to REPLICA IDENTITY FULL and remove CLUSTER ON.
	src2 := `TABLE orders (
    id BIGINT NOT NULL,
    external_ref TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
) {
    INDEXES {
        idx_orders_created_at (created_at);
    }
    UNIQUE INDEX idx_orders_external_ref (external_ref);
    REPLICA IDENTITY FULL;
}`
	if err := os.WriteFile(f, []byte(src2), 0o644); err != nil {
		t.Fatalf("write schema v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := replIdentMode(); got != "f" {
		t.Fatalf("expected relreplident = 'f' (full) after change, got %q", got)
	}
	rows, err := conn.QueryRows(ctx, `SELECT count(*) FROM pg_index pi JOIN pg_class t ON t.oid = pi.indrelid WHERE t.relname = 'orders' AND pi.indisclustered`)
	if err != nil {
		t.Fatalf("query clustered count: %v", err)
	}
	defer rows.Close()
	var n int
	rows.Next()
	_ = rows.Scan(&n)
	if n != 0 {
		t.Errorf("expected no clustered index after removing CLUSTER ON, got %d", n)
	}
}

