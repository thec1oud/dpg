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
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestRoundtripCompositeTypeAttrAddedIsGranular is the regression guard for
// RFC audit item #13: any composite type attribute change — including a
// pure, RFC-promised-safe addition — used to force a bare
// "DROP TYPE; CREATE TYPE" instead of the RFC Section 5.2 granular
// "ALTER TYPE ... ADD ATTRIBUTE" (SAFE). Proves the granular op is actually
// what gets applied against a real database, and that the type's OID
// survives (DROP+CREATE would have assigned a new one).
func TestRoundtripCompositeTypeAttrAddedIsGranular(t *testing.T) {
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

	v1 := `TYPE addr AS (street text);`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	typeOID := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, "SELECT oid::text FROM pg_type WHERE typname = 'addr'")
		if err != nil {
			t.Fatalf("query pg_type: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("pg_type has no row for addr")
		}
		var oid string
		if err := rows.Scan(&oid); err != nil {
			t.Fatalf("scan oid: %v", err)
		}
		return oid
	}
	oidBefore := typeOID()

	v2 := `TYPE addr AS (street text, city text);`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	prevSnap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, prevSnap)
	if err != nil {
		t.Fatalf("diff (attr added): %v", err)
	}
	var sawDropType, sawAddAttr bool
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP TYPE") {
			sawDropType = true
		}
		if strings.Contains(op.SQL(), "ADD ATTRIBUTE") {
			sawAddAttr = true
			if op.Safety() != pipeline.Safe {
				t.Errorf("ADD ATTRIBUTE safety = %v, want Safe", op.Safety())
			}
		}
	}
	if sawDropType || !sawAddAttr {
		t.Fatalf("expected a granular ADD ATTRIBUTE with no DROP TYPE, got: %v", opsSQL(ops))
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := typeOID(); got != oidBefore {
		t.Errorf("addr: OID changed from %s to %s — the type was dropped and recreated instead of altered in place", oidBefore, got)
	}

	rows, err := conn.QueryRows(ctx, "SELECT attname FROM pg_attribute WHERE attrelid = (SELECT typrelid FROM pg_type WHERE typname = 'addr') AND attnum > 0 ORDER BY attnum")
	if err != nil {
		t.Fatalf("query pg_attribute: %v", err)
	}
	var attrs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan attname: %v", err)
		}
		attrs = append(attrs, name)
	}
	rows.Close()
	if len(attrs) != 2 || attrs[0] != "street" || attrs[1] != "city" {
		t.Fatalf("live attributes = %v, want [street city]", attrs)
	}

	// Live-verify blindness check: a fresh introspect pass must agree too.
	ci := introspect.New()
	liveObjects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	newSnap, _ := store.Load("test", "dpgtest")
	var managedLive []pipeline.IRObject
	for _, obj := range liveObjects {
		if _, ok := newSnap.Objects[obj.QualifiedName()]; ok {
			managedLive = append(managedLive, obj)
		}
	}
	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, managedLive); err != nil {
		t.Fatalf("populate live snapshot: %v", err)
	}
	liveDriftOps, err := differ.Diff(desired2, liveSnap)
	if err != nil {
		t.Fatalf("live drift diff: %v", err)
	}
	if len(liveDriftOps) != 0 {
		t.Errorf("expected zero drift against freshly introspected live state, got %d ops:", len(liveDriftOps))
		for _, op := range liveDriftOps {
			t.Errorf("  [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestRoundtripCompositeTypeAttrRenamed guards RFC Section 5.2's ATTRIBUTE
// name { RENAMED FROM old; } directive: the rename must be applied as ALTER
// TYPE ... RENAME ATTRIBUTE against a real database (same OID, new attribute
// name), not as a drop and add.
func TestRoundtripCompositeTypeAttrRenamed(t *testing.T) {
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

	v1 := `TYPE addr AS (old_name text, city text);`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	typeOID := func() string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, "SELECT oid::text FROM pg_type WHERE typname = 'addr'")
		if err != nil {
			t.Fatalf("query pg_type: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("pg_type has no row for addr")
		}
		var oid string
		if err := rows.Scan(&oid); err != nil {
			t.Fatalf("scan oid: %v", err)
		}
		return oid
	}
	oidBefore := typeOID()

	v2 := `TYPE addr AS (new_name text, city text) {
    ATTRIBUTE new_name { RENAMED FROM old_name; }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	desired2, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile v2: %v", err)
	}
	prevSnap, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired2, prevSnap)
	if err != nil {
		t.Fatalf("diff (attr renamed): %v", err)
	}
	var sawDropType, sawRename bool
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP TYPE") || strings.Contains(op.SQL(), "DROP ATTRIBUTE") {
			sawDropType = true
		}
		if strings.Contains(op.SQL(), "RENAME ATTRIBUTE") && strings.Contains(op.SQL(), "old_name") && strings.Contains(op.SQL(), "new_name") {
			sawRename = true
		}
	}
	if sawDropType || !sawRename {
		t.Fatalf("expected a granular RENAME ATTRIBUTE with no drop, got: %v", opsSQL(ops))
	}

	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if got := typeOID(); got != oidBefore {
		t.Errorf("addr: OID changed from %s to %s — the type was dropped and recreated instead of renamed in place", oidBefore, got)
	}

	rows, err := conn.QueryRows(ctx, "SELECT attname FROM pg_attribute WHERE attrelid = (SELECT typrelid FROM pg_type WHERE typname = 'addr') AND attnum > 0 ORDER BY attnum")
	if err != nil {
		t.Fatalf("query pg_attribute: %v", err)
	}
	var attrs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan attname: %v", err)
		}
		attrs = append(attrs, name)
	}
	rows.Close()
	if len(attrs) != 2 || attrs[0] != "new_name" || attrs[1] != "city" {
		t.Fatalf("live attributes = %v, want [new_name city]", attrs)
	}
}

// TestRoundtripCompositeTypeAttrComment guards RFC Section 5.2's ATTRIBUTE
// name { COMMENT '...'; } directive end to end: the comment is applied with
// COMMENT ON COLUMN, introspects back with no drift, and a change or removal
// is diffed as a comment-only op.
func TestRoundtripCompositeTypeAttrComment(t *testing.T) {
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

	attrComment := func() *string {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT col_description(t.typrelid, a.attnum)
FROM pg_type t JOIN pg_attribute a ON a.attrelid = t.typrelid
WHERE t.typname = 'addr' AND a.attname = 'street'`)
		if err != nil {
			t.Fatalf("query comment: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("no street attribute on addr")
		}
		var c *string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan comment: %v", err)
		}
		return c
	}

	steps := []struct {
		src  string
		want *string
	}{
		{`TYPE addr AS (street text, city text) {
    ATTRIBUTE street { COMMENT 'Street line'; }
}`, strPtr("Street line")},
		{`TYPE addr AS (street text, city text) {
    ATTRIBUTES {
        street { COMMENT 'Street and number'; }
    }
}`, strPtr("Street and number")},
		{`TYPE addr AS (street text, city text);`, nil},
	}
	for i, step := range steps {
		if err := os.WriteFile(f, []byte(step.src), 0o644); err != nil {
			t.Fatalf("write step %d: %v", i, err)
		}
		applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
		got := attrComment()
		if (got == nil) != (step.want == nil) || (got != nil && *got != *step.want) {
			t.Fatalf("step %d: street comment = %v, want %v", i, got, step.want)
		}
		assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
	}
}

func strPtr(s string) *string { return &s }
