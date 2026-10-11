//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestRoundtripGrantGrantedBy guards RFC audit item #90 (Section 7.10/11.3):
// GRANTED BY role-spec on a table-level GRANTS/REVOKES entry. Real
// PostgreSQL restricts the effective grantor to current_user regardless of
// who is named (confirmed live via a direct psql probe before writing this
// test: even a superuser gets "grantor must be current user" naming any
// role other than the one it's actually connected as) — so CURRENT_USER is
// the only role-spec value guaranteed to apply successfully in any
// environment, and is what this test exercises. Proves: (a) a declared
// GRANT ... GRANTED BY CURRENT_USER actually applies without error and the
// live ACL entry's grantor is the connecting role, (b) a second plan
// against the same declaration is a genuine no-op both offline (against the
// stored snapshot) and against live-introspected state, and (c) an explicit
// REVOKES entry with GRANTED BY CURRENT_USER actually revokes it live.
//
// The live-introspection no-op check in particular guards a real regression:
// introspectTableGrants now populates ir.Grant.GrantedBy from the real
// resolved grantor (pg_get_userbyid(a.grantor)), which is a concrete role
// name like "postgres", never the symbolic "CURRENT_USER" declared in
// source — diffGrantSet's grantedByMatches helper is what keeps that from
// looking like drift.
func TestRoundtripGrantGrantedBy(t *testing.T) {
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

	grantorOf := func(grantee string) (string, bool) {
		t.Helper()
		rows, err := conn.QueryRows(ctx,
			`SELECT acl.grantor::regrole::text FROM pg_class c, pg_namespace n,
			 LATERAL aclexplode(c.relacl) acl
			 WHERE c.relnamespace = n.oid AND n.nspname = 'public' AND c.relname = 'orders'
			 AND acl.grantee = $1::regrole AND acl.privilege_type = 'SELECT'`, grantee)
		if err != nil {
			t.Fatalf("query grantor: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			return "", false
		}
		var v string
		_ = rows.Scan(&v)
		return v, true
	}

	// v1: declare the grant with GRANTED BY CURRENT_USER.
	v1 := `ROLE reader NOLOGIN;

TABLE orders (id INT) {
    GRANTS { SELECT TO reader GRANTED BY CURRENT_USER; }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	connectingRole, err := conn.QueryRows(ctx, `SELECT current_user`)
	if err != nil {
		t.Fatalf("query current_user: %v", err)
	}
	var connRole string
	if !connectingRole.Next() {
		t.Fatal("current_user returned no row")
	}
	_ = connectingRole.Scan(&connRole)
	connectingRole.Close()

	grantor, ok := grantorOf("reader")
	if !ok {
		t.Fatal("reader has no live SELECT grant on orders after initial apply — GRANT never applied")
	}
	if grantor != connRole {
		t.Fatalf("grantor: got %q, want %q (the connecting role) — GRANTED BY CURRENT_USER did not resolve correctly", grantor, connRole)
	}

	// The offline dpg plan path (desired vs. the stored snapshot of last-
	// declared state, not live introspection) must already be a no-op for
	// the unchanged declaration.
	desired, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base, _ := store.Load("test", "dpgtest")
	replanOps, err := differ.Diff(desired, base)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(replanOps) != 0 {
		t.Errorf("expected no ops replanning the unchanged declaration, got: %v", replanOps)
	}

	// Also no-op against live-introspected state, not just the stored
	// snapshot — see this test's own doc comment for why this specifically
	// exercises grantedByMatches.
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	// v2: an explicit REVOKES entry with GRANTED BY CURRENT_USER must
	// actually revoke it live.
	v2 := `ROLE reader NOLOGIN;

TABLE orders (id INT) {
    REVOKES { SELECT FROM reader GRANTED BY CURRENT_USER; }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if _, ok := grantorOf("reader"); ok {
		t.Fatal("reader still has a live SELECT grant on orders after an explicit REVOKES entry with GRANTED BY — REVOKE never applied")
	}
}

// TestRoundtripTableGrantSelfGrantNoLiveDrift guards a bug found separately
// from GRANTED BY itself: introspectTableGrants had no grantor<>grantee
// filter, unlike introspectColumnGrants. PostgreSQL materializes the object
// owner's own full privilege set into a real aclitem row the moment ANY
// explicit GRANT touches a table (confirmed live via direct psql: an
// untouched table's relacl is NULL, but gains a real "owner=<privs>/owner"
// entry alongside the first explicit GRANT); without the filter,
// introspection read that synthesized self-grant back as an "extra" live
// grant and diffGrantSet's live-comparison path proposed REVOKEing it —
// contradicting RFC Section 11.2's own explicit "does NOT report extra
// grants present in the live catalog but absent from DPG source" text. This
// test uses a plain GRANT with no GRANTED BY, isolating the self-grant fix
// from the GrantedBy-introspection fix TestRoundtripGrantGrantedBy exercises.
func TestRoundtripTableGrantSelfGrantNoLiveDrift(t *testing.T) {
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

	src := `ROLE app_service NOLOGIN;

TABLE widgets (id INT) {
    GRANTS { SELECT TO app_service; }
}`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	selfGrantRows, err := conn.QueryRows(ctx,
		`SELECT count(*) FROM pg_class c, pg_namespace n, LATERAL aclexplode(c.relacl) acl
		 WHERE c.relnamespace = n.oid AND n.nspname = 'public' AND c.relname = 'widgets'
		 AND acl.grantor = acl.grantee`)
	if err != nil {
		t.Fatalf("query self-grant count: %v", err)
	}
	defer selfGrantRows.Close()
	if !selfGrantRows.Next() {
		t.Fatal("count query returned no row")
	}
	var selfGrantCount int
	_ = selfGrantRows.Scan(&selfGrantCount)
	selfGrantRows.Close()
	if selfGrantCount == 0 {
		t.Fatal("expected PostgreSQL to have materialized an owner self-grant on widgets after an explicit GRANT — test premise invalid")
	}

	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
