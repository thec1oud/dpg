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

// TestRoundtripIndexOpclassAndCollation is the live regression guard for
// RFC audit item #10: createIndex/diffIndexes never rendered or compared
// Collation/OpClass/OpClassParams at all before this — a declared opclass
// (with or without parameters) was silently dropped from the generated SQL,
// and a change to one was invisible to diffing.
//
// Confirms live: a COLLATE + opclass(params) index column applies
// correctly, a second plan against freshly introspected live state is a
// genuine no-op (proving introspection's parseIndexColumn correctly reads
// pg_get_indexdef's own reconstruction back, including its confirmed-live
// "opclass (params)" spacing), and changing the opclass runs a real
// DROP INDEX + CREATE INDEX against the server.
func TestRoundtripIndexOpclassAndCollation(t *testing.T) {
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

	opclassName := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `
			SELECT opc.opcname
			FROM pg_index ix
			JOIN pg_class i ON i.oid = ix.indexrelid
			JOIN pg_opclass opc ON opc.oid = ix.indclass[0]
			WHERE i.relname = 'idx_email'`)
		if err != nil {
			t.Fatalf("query opclass: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("idx_email not found")
		}
		var name string
		_ = rows.Scan(&name)
		return name
	}

	write(`TABLE t (email TEXT) {
    INDEXES {
        idx_email (email COLLATE "C" text_pattern_ops);
    }
}`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if got := opclassName(); got != "text_pattern_ops" {
		t.Fatalf("opclass after create: got %q, want %q", got, "text_pattern_ops")
	}
	noDriftAgainstLive(t, ctx, conn, []string{f}, dir, differ, store)

	write(`TABLE t (email TEXT) {
    INDEXES {
        idx_email (email COLLATE "C" varchar_pattern_ops);
    }
}`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	if got := opclassName(); got != "varchar_pattern_ops" {
		t.Fatalf("opclass after change: got %q, want %q", got, "varchar_pattern_ops")
	}
	noDriftAgainstLive(t, ctx, conn, []string{f}, dir, differ, store)
}

// TestRoundtripIndexOpclassWithParams proves an opclass declared WITH
// parameters (RFC Section 7.7's own worked example, "doc tsvector_ops(siglen
// = 32)") actually applies live — GiST's tsvector_ops is the one built-in
// opclass with a real parameter, so no extension is needed.
func TestRoundtripIndexOpclassWithParams(t *testing.T) {
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
	src := `TABLE t (doc TSVECTOR) {
    INDEXES {
        idx_doc USING gist (doc tsvector_ops(siglen = 32));
    }
}`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	rows, err := conn.QueryRows(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_doc'`)
	if err != nil {
		t.Fatalf("query indexdef: %v", err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatalf("idx_doc not found")
	}
	var indexdef string
	_ = rows.Scan(&indexdef)
	rows.Close()
	if !strings.Contains(indexdef, "siglen") {
		t.Fatalf("expected indexdef to declare the siglen opclass parameter, got: %s", indexdef)
	}

	noDriftAgainstLive(t, ctx, conn, []string{f}, dir, differ, store)
}

// TestRoundtripIndexOnOnly is the live regression guard for RFC Section
// 7.7's ON ONLY prefix: previously ir.Index had no Only field at all.
//
// Confirms live (verified live beforehand via direct psql, independent of
// this codebase, that ON ONLY on a partitioned table marks the parent
// index indisvalid=false and creates nothing on existing partitions): the
// same holds when applied through the full DPG pipeline, and — since Only
// deliberately has no SnapIndex counterpart (ir.Index.Only's doc comment) —
// a second plan is still a genuine no-op even though the live index's
// indisvalid state has no way to be reconstructed back into desired state.
func TestRoundtripIndexOnOnly(t *testing.T) {
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
	src := `TABLE t (id INTEGER NOT NULL, region TEXT NOT NULL) PARTITION BY LIST (region) {
    PARTITIONS {
        t_us FOR VALUES IN ('us');
    }
    INDEXES {
        ONLY idx_only (id);
    }
}`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	rows, err := conn.QueryRows(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = 'idx_only'::regclass`)
	if err != nil {
		t.Fatalf("query indisvalid: %v", err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatalf("idx_only not found")
	}
	var valid bool
	_ = rows.Scan(&valid)
	rows.Close()
	if valid {
		t.Fatalf("expected idx_only to be indisvalid=false (ONLY skips partitions), got true")
	}

	partRows, err := conn.QueryRows(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename = 't_us'`)
	if err != nil {
		t.Fatalf("query partition indexes: %v", err)
	}
	partRows.Next()
	var count int
	_ = partRows.Scan(&count)
	partRows.Close()
	if count != 0 {
		t.Fatalf("expected zero indexes on partition t_us, got %d", count)
	}

	noDriftAgainstLive(t, ctx, conn, []string{f}, dir, differ, store)
}
