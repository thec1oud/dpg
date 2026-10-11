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

// TestRoundtripRangeMultirangeNameRename guards RFC Section 5.3 against a
// real database: setting MULTIRANGE_TYPE_NAME on an existing range type
// renames its multirange in place (the range keeps its OID) and leaves no
// drift.
func TestRoundtripRangeMultirangeNameRename(t *testing.T) {
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
	query := func(sql string) string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("%s: no rows", sql)
		}
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	const rangeOID = `SELECT oid::text FROM pg_type WHERE typname = 'floatrange'`
	const multiName = `SELECT mr.typname::text FROM pg_range r JOIN pg_type t ON t.oid = r.rngtypid JOIN pg_type mr ON mr.oid = r.rngmultitypid WHERE t.typname = 'floatrange'`

	if err := os.WriteFile(f, []byte(`TYPE floatrange AS RANGE (SUBTYPE = float8);`), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	before := query(rangeOID)
	if got := query(multiName); got != "floatmultirange" {
		t.Fatalf("default multirange name = %q", got)
	}

	if err := os.WriteFile(f, []byte(`TYPE floatrange AS RANGE (SUBTYPE = float8, MULTIRANGE_TYPE_NAME = float_ranges);`), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if got := query(multiName); got != "float_ranges" {
		t.Fatalf("multirange name after rename = %q, want float_ranges", got)
	}
	if after := query(rangeOID); after != before {
		t.Fatalf("range type OID changed %s -> %s: it was rebuilt, not renamed", before, after)
	}
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
