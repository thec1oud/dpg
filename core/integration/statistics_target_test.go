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

// TestRoundtripStatisticsTarget guards RFC Section 14.6: an extended
// statistics object's STATISTICS n directive sets its target, re-planning
// and the live comparison find nothing, and STATISTICS DEFAULT resets it.
func TestRoundtripStatisticsTarget(t *testing.T) {
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
	write := func(target string) {
		t.Helper()
		src := "TABLE orders (a int, b int);\nSTATISTICS orders_ab (ndistinct) ON a, b FROM orders { STATISTICS " + target + "; }\n"
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := func() *int64 {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT stxstattarget::bigint FROM pg_statistic_ext WHERE stxname = 'orders_ab'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var v *int64
		if rows.Next() {
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
		}
		return v
	}

	write("500")
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if v := target(); v == nil || *v != 500 {
		t.Errorf("stxstattarget = %v, want 500", v)
	}
	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	write("DEFAULT")
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if v := target(); v != nil && *v != -1 {
		t.Errorf("after DEFAULT, stxstattarget = %v, want unset", *v)
	}
	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
