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

// TestRoundtripSequenceAndTSConfigRenamedFrom guards RFC Sections 10 and
// 12.1: RENAMED FROM on a sequence (including a move to another schema)
// and on a text search configuration renames the existing object, which
// keeps its OID and the sequence its current value, rather than dropping
// and recreating it.
func TestRoundtripSequenceAndTSConfigRenamedFrom(t *testing.T) {
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
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`SCHEMA archive {}
SEQUENCE order_no START WITH 100;
TEXT SEARCH CONFIGURATION search_cfg (PARSER = default);
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if _, err := conn.Exec(ctx, `SELECT nextval('order_no'), nextval('order_no')`); err != nil {
		t.Fatal(err)
	}
	seqOID := catalogOID(t, ctx, conn, "pg_class", "relname", "order_no")
	cfgOID := catalogOID(t, ctx, conn, "pg_ts_config", "cfgname", "search_cfg")

	write(`SCHEMA archive {}
SEQUENCE archive.invoice_no START WITH 100 { RENAMED FROM public.order_no; }
TEXT SEARCH CONFIGURATION site_search (PARSER = default) { RENAMED FROM search_cfg; COMMENT 'site search'; }
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := catalogOID(t, ctx, conn, "pg_class", "relname", "invoice_no"); got == nil || seqOID == nil || *got != *seqOID {
		t.Errorf("sequence OID changed or missing: before %v, after %v", seqOID, got)
	}
	if got := catalogOID(t, ctx, conn, "pg_ts_config", "cfgname", "site_search"); got == nil || cfgOID == nil || *got != *cfgOID {
		t.Errorf("configuration OID changed or missing: before %v, after %v", cfgOID, got)
	}
	rows, err := conn.QueryRows(ctx, `SELECT last_value FROM archive.invoice_no`)
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	if rows.Next() {
		if err := rows.Scan(&last); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if last != 101 {
		t.Errorf("sequence value after rename = %d, want 101", last)
	}
	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
