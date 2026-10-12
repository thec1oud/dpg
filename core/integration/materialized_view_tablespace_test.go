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

// TestRoundtripMaterializedViewTablespaceAndStorageParams is the live
// regression guard for RFC Section 8.2 — the RFC's own canonical worked
// example (MATERIALIZED VIEW product_stats WITH (fillfactor = 90)
// TABLESPACE analytics_space AS ...) did not round-trip through DPG at all
// before this: buildMaterializedView never read cta.Into.TableSpaceName or
// cta.Into.Options, ir.View had no field for either, and introspectViews
// never selected reltablespace/reloptions — a declared TABLESPACE/WITH
// clause was silently dropped on parse, and invisible to dpg dump.
//
// Confirms live: both clauses land correctly on CREATE, a second plan
// against freshly introspected live state is a genuine no-op, and changing
// either afterward runs a real, targeted ALTER MATERIALIZED VIEW ... SET
// TABLESPACE / SET (...) with the matview's OID staying stable (a genuine
// in-place ALTER, not a hidden drop-and-recreate). The same stage moves the
// underlying table with ALTER TABLE ... SET TABLESPACE; both moves copy the
// relation's files under an ACCESS EXCLUSIVE lock and are planned as CAUTION.
func TestRoundtripMaterializedViewTablespaceAndStorageParams(t *testing.T) {
	connStr, container := testpg.StartWithContainer(t)
	ctx := context.Background()

	for _, dir := range []string{"/var/lib/postgresql/analytics_space", "/var/lib/postgresql/analytics_space2"} {
		if code, _, err := container.Exec(ctx, []string{"mkdir", "-p", dir}); err != nil || code != 0 {
			t.Fatalf("mkdir %s in container: code=%d err=%v", dir, code, err)
		}
		if code, _, err := container.Exec(ctx, []string{"chown", "postgres:postgres", dir}); err != nil || code != 0 {
			t.Fatalf("chown %s in container: code=%d err=%v", dir, code, err)
		}
	}

	differ := diff.New()
	emitter := emit.New()
	applyExec := executor.New()
	ci := introspect.New()
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

	type mvInfo struct {
		oid        uint32
		tablespace string
		fillfactor string
	}
	relInfo := func(relname string) mvInfo {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `
			SELECT c.oid, coalesce(ts.spcname, ''), coalesce(
			  (SELECT split_part(o, '=', 2) FROM unnest(c.reloptions) o WHERE o LIKE 'fillfactor=%'), '')
			FROM pg_class c
			LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
			WHERE c.relname = $1`, relname)
		if err != nil {
			t.Fatalf("query %s info: %v", relname, err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("%s not found", relname)
		}
		var i mvInfo
		if err := rows.Scan(&i.oid, &i.tablespace, &i.fillfactor); err != nil {
			t.Fatalf("scan mv info: %v", err)
		}
		return i
	}
	info := func() mvInfo { return relInfo("product_stats") }

	noDrift := func() {
		t.Helper()
		desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		base, _ := store.Load("test", "dpgtest")
		ops, err := differ.Diff(desired, base)
		if err != nil {
			t.Fatalf("diff: %v", err)
		}
		if len(ops) != 0 {
			t.Errorf("expected no-op re-diff, got %d ops:", len(ops))
			for _, op := range ops {
				t.Errorf("  [%s] %s", op.Safety(), op.SQL())
			}
		}
		liveObjects, err := ci.Introspect(ctx, conn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		var managedLive []pipeline.IRObject
		for _, obj := range liveObjects {
			if _, ok := base.Objects[obj.QualifiedName()]; ok {
				managedLive = append(managedLive, obj)
			}
		}
		liveSnap := &pipeline.Snapshot{}
		if err := snapshot.Populate(liveSnap, managedLive); err != nil {
			t.Fatalf("populate live snapshot: %v", err)
		}
		driftOps, err := differ.Diff(desired, liveSnap)
		if err != nil {
			t.Fatalf("live drift diff: %v", err)
		}
		if len(driftOps) != 0 {
			t.Errorf("expected zero drift against freshly introspected live state, got %d ops:", len(driftOps))
			for _, op := range driftOps {
				t.Errorf("  [%s] %s", op.Safety(), op.SQL())
			}
		}
	}

	// Stage 0: create both tablespaces first, in their own apply — the
	// dependency resolver has no Tablespace edge for Table/View references
	// (a separate, pre-existing gap, not part of this fix), so a same-plan
	// CREATE TABLESPACE + CREATE MATERIALIZED VIEW ... TABLESPACE can race;
	// sidestepped here the same way tablespace_storage_params_test.go's
	// setup avoids it.
	write(`TABLESPACE analytics_space LOCATION '/var/lib/postgresql/analytics_space';
TABLESPACE analytics_space2 LOCATION '/var/lib/postgresql/analytics_space2';
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	// Stage 1: the RFC's own worked example (Section 8.2), minus the query
	// body's dependency on a real order_items table (kept minimal here).
	write(`TABLESPACE analytics_space LOCATION '/var/lib/postgresql/analytics_space';
TABLESPACE analytics_space2 LOCATION '/var/lib/postgresql/analytics_space2';
TABLE order_items (product_id BIGINT NOT NULL);
MATERIALIZED VIEW product_stats
WITH (fillfactor = 90)
TABLESPACE analytics_space AS
    SELECT product_id, COUNT(*) AS purchases
    FROM order_items
    GROUP BY product_id
WITH NO DATA;
`)
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	i1 := info()
	t1 := relInfo("order_items")
	if t1.tablespace != "" {
		t.Fatalf("expected order_items in the default tablespace after create, got %q", t1.tablespace)
	}
	if i1.tablespace != "analytics_space" {
		t.Fatalf("expected tablespace analytics_space after create, got %q", i1.tablespace)
	}
	if i1.fillfactor != "90" {
		t.Fatalf("expected fillfactor=90 after create, got %q", i1.fillfactor)
	}
	noDrift()

	// Stage 2: change both TABLESPACE and the fillfactor value, and move the
	// table — must be targeted ALTERs (OID stable), not a drop+recreate.
	write(`TABLESPACE analytics_space LOCATION '/var/lib/postgresql/analytics_space';
TABLESPACE analytics_space2 LOCATION '/var/lib/postgresql/analytics_space2';
TABLE order_items (product_id BIGINT NOT NULL) TABLESPACE analytics_space2;
MATERIALIZED VIEW product_stats
WITH (fillfactor = 70)
TABLESPACE analytics_space2 AS
    SELECT product_id, COUNT(*) AS purchases
    FROM order_items
    GROUP BY product_id
WITH NO DATA;
`)
	{
		desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		base, _ := store.Load("test", "dpgtest")
		ops, err := differ.Diff(desired, base)
		if err != nil {
			t.Fatalf("diff: %v", err)
		}
		moves := 0
		for _, op := range ops {
			if strings.Contains(op.SQL(), " SET TABLESPACE ") {
				moves++
				if op.Safety() != pipeline.Caution {
					t.Errorf("expected CAUTION for %s, got %s", op.SQL(), op.Safety())
				}
			}
		}
		if moves != 2 {
			t.Fatalf("expected 2 SET TABLESPACE ops (table + matview), got %d: %v", moves, ops)
		}
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	t2 := relInfo("order_items")
	if t2.oid != t1.oid {
		t.Fatalf("expected stable order_items OID across SET TABLESPACE, got %d before and %d after", t1.oid, t2.oid)
	}
	if t2.tablespace != "analytics_space2" {
		t.Fatalf("expected order_items in analytics_space2 after ALTER, got %q", t2.tablespace)
	}
	i2 := info()
	if i2.oid != i1.oid {
		t.Fatalf("expected stable OID across TABLESPACE/WITH changes (targeted ALTER, not drop+recreate), got %d before and %d after", i1.oid, i2.oid)
	}
	if i2.tablespace != "analytics_space2" {
		t.Fatalf("expected tablespace analytics_space2 after ALTER, got %q", i2.tablespace)
	}
	if i2.fillfactor != "70" {
		t.Fatalf("expected fillfactor=70 after ALTER, got %q", i2.fillfactor)
	}
	noDrift()
}
