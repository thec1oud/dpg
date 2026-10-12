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

// TestRoundtripVirtualTypeColumn guards RFC Section 5.6: a virtual type
// column is created as jsonb/jsonb[], and neither re-planning against the
// snapshot nor comparing with the live catalog finds a change.
func TestRoundtripVirtualTypeColumn(t *testing.T) {
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
	src := `VIRTUAL TYPE line_item AS (sku text, qty integer);
VIRTUAL TYPE line_items AS line_item[];
TABLE orders (
    id      int PRIMARY KEY,
    items   line_item[],
    history line_items
);
TYPE shipment AS (
    carrier text,
    item    line_item
);
`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	rows, err := conn.QueryRows(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod)
FROM pg_attribute a
WHERE a.attrelid IN ('public.orders'::regclass, 'public.shipment'::regclass)
  AND a.attname IN ('items', 'history', 'item')
ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatal(err)
		}
		got = append(got, name+" "+typ)
	}
	rows.Close()
	if want := "history jsonb,item jsonb,items jsonb[]"; strings.Join(got, ",") != want {
		t.Errorf("column types = %q, want %q", strings.Join(got, ","), want)
	}

	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
