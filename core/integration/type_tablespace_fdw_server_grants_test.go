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

// TestRoundtripTypeGrants is the regression guard for RFC audit item #3:
// ir.Type had no Grants/Revocations field at all, for any of the 5 variants.
// Live-verification during the audit found ENUM's has_type_privilege()
// returning true after a live apply and initially read that as ENUM-
// specific evidence the bug didn't reproduce there; re-traced live (see
// ir.Type.Grants' doc comment) that's PostgreSQL's own default USAGE-to-
// PUBLIC grant every type variant gets on creation, confirmed identically
// true for ENUM/COMPOSITE/RANGE/DOMAIN with zero DPG involvement — not a
// real difference. This test proves the actual bug (an explicit GRANT to a
// specific, non-default role) against two structurally distinct variants:
// ENUM (buildEnum/simple opaque-ish diff) and DOMAIN (buildDomain/property-
// level diffing) — both must show the explicit grant landing, and PUBLIC's
// default access must not mask a missing REVOKE.
func TestRoundtripTypeGrants(t *testing.T) {
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

	v1 := `ROLE app_service NOLOGIN;

TYPE mood AS ENUM ('sad', 'happy') {
    REVOKES { USAGE FROM PUBLIC; }
    GRANTS { USAGE TO app_service; }
}

DOMAIN positive_integer AS integer {
    REVOKES { USAGE FROM PUBLIC; }
    GRANTS { USAGE TO app_service; }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	checkUsage := func(typeName, role string) bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT has_type_privilege($1, $2, 'USAGE')`, role, typeName)
		if err != nil {
			t.Fatalf("query has_type_privilege: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("has_type_privilege returned no row")
		}
		var v bool
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return v
	}

	for _, tp := range []string{"mood", "positive_integer"} {
		if !checkUsage(tp, "app_service") {
			t.Fatalf("%s: app_service should have USAGE after initial apply — bug #3 regressed (GRANT never applied)", tp)
		}
		if checkUsage(tp, "public") {
			t.Fatalf("%s: PUBLIC should have had USAGE revoked — bug #3 regressed (REVOKE never applied, or the default masked it)", tp)
		}
	}

	// Now remove the grant from source and confirm the resulting REVOKE
	// actually lands live.
	v2 := `ROLE app_service NOLOGIN;

TYPE mood AS ENUM ('sad', 'happy') {
    REVOKES { USAGE FROM PUBLIC; }
}

DOMAIN positive_integer AS integer {
    REVOKES { USAGE FROM PUBLIC; }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	for _, tp := range []string{"mood", "positive_integer"} {
		if checkUsage(tp, "app_service") {
			t.Fatalf("%s: app_service still has USAGE after the GRANT was removed from source — bug #3 regressed", tp)
		}
	}
}

// TestRoundtripTablespaceFDWServerGrants is the regression guard for RFC
// audit items #4/#5/#6: ir.Tablespace/ForeignDataWrapper/ForeignServer had
// no Grants/Revocations field at all — the same missing-field shape as the
// already-fixed Schema-grants pattern, never generalized to these three
// kinds.
func TestRoundtripTablespaceFDWServerGrants(t *testing.T) {
	connStr, container := testpg.StartWithContainer(t)
	ctx := context.Background()

	if code, _, err := container.Exec(ctx, []string{"mkdir", "-p", "/var/lib/postgresql/ts_grants"}); err != nil || code != 0 {
		t.Fatalf("mkdir in container: code=%d err=%v", code, err)
	}
	if code, _, err := container.Exec(ctx, []string{"chown", "postgres:postgres", "/var/lib/postgresql/ts_grants"}); err != nil || code != 0 {
		t.Fatalf("chown in container: code=%d err=%v", code, err)
	}

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

	v1 := `ROLE app_service NOLOGIN;

TABLESPACE gl_ts_grants LOCATION '/var/lib/postgresql/ts_grants' {
    GRANTS { CREATE TO app_service; }
}

FOREIGN DATA WRAPPER gl_fdw_grants {
    GRANTS { USAGE TO app_service; }
}

SERVER gl_srv_grants FOREIGN DATA WRAPPER gl_fdw_grants {
    GRANTS { USAGE TO app_service; }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	checkPriv := func(fn, name, role, priv string) bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, "SELECT "+fn+"($1, $2, $3)", role, name, priv)
		if err != nil {
			t.Fatalf("query %s: %v", fn, err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("query returned no row")
		}
		var v bool
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return v
	}

	if !checkPriv("has_tablespace_privilege", "gl_ts_grants", "app_service", "CREATE") {
		t.Fatal("app_service should have CREATE on gl_ts_grants after initial apply — bug #4 regressed")
	}
	if !checkPriv("has_foreign_data_wrapper_privilege", "gl_fdw_grants", "app_service", "USAGE") {
		t.Fatal("app_service should have USAGE on gl_fdw_grants after initial apply — bug #5 regressed")
	}
	if !checkPriv("has_server_privilege", "gl_srv_grants", "app_service", "USAGE") {
		t.Fatal("app_service should have USAGE on gl_srv_grants after initial apply — bug #6 regressed")
	}

	// Remove the grants from source and confirm the resulting REVOKEs land.
	v2 := `ROLE app_service NOLOGIN;

TABLESPACE gl_ts_grants LOCATION '/var/lib/postgresql/ts_grants';

FOREIGN DATA WRAPPER gl_fdw_grants;

SERVER gl_srv_grants FOREIGN DATA WRAPPER gl_fdw_grants;`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if checkPriv("has_tablespace_privilege", "gl_ts_grants", "app_service", "CREATE") {
		t.Fatal("app_service still has CREATE on gl_ts_grants after the GRANT was removed from source — bug #4 regressed")
	}
	if checkPriv("has_foreign_data_wrapper_privilege", "gl_fdw_grants", "app_service", "USAGE") {
		t.Fatal("app_service still has USAGE on gl_fdw_grants after the GRANT was removed from source — bug #5 regressed")
	}
	if checkPriv("has_server_privilege", "gl_srv_grants", "app_service", "USAGE") {
		t.Fatal("app_service still has USAGE on gl_srv_grants after the GRANT was removed from source — bug #6 regressed")
	}
}
