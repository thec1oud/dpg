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

// TestRoundtripParameterPrivileges is the live regression guard for #110
// (RFC Section 11.6, PG15+): PARAMETER PRIVILEGES was a wholly new object
// kind (a synthetic top-level block, same DEFAULT PRIVILEGES-style
// PGSQLParser bypass, but backed by a genuinely-parseable real PostgreSQL
// GRANT ... ON PARAMETER statement underneath). This proves, against
// pg_parameter_acl via has_parameter_privilege(): (a) a declared grant
// actually lands live at CREATE time, (b) removing the GRANTS declaration
// alone is a no-op — the additive model (RFC Section 11.6/11.2): the grant
// must still be present live, not revoked, and (c) an explicit
// REVOKES entry actually revokes it live.
func TestRoundtripParameterPrivileges(t *testing.T) {
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

	checkPriv := func(role, param, priv string) bool {
		t.Helper()
		rows, err := conn.QueryRows(ctx, `SELECT has_parameter_privilege($1, $2, $3)`, role, param, priv)
		if err != nil {
			t.Fatalf("query has_parameter_privilege: %v", err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatal("has_parameter_privilege returned no row")
		}
		var v bool
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return v
	}

	// v1: declare the grant.
	v1 := `ROLE app_admin NOLOGIN;

PARAMETER PRIVILEGES {
    GRANTS {
        SET ON PARAMETER work_mem, statement_timeout TO app_admin;
    }
}`
	if err := os.WriteFile(f, []byte(v1), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if !checkPriv("app_admin", "work_mem", "SET") {
		t.Fatal("app_admin should have SET on work_mem after initial apply — GRANT never applied")
	}
	if !checkPriv("app_admin", "statement_timeout", "SET") {
		t.Fatal("app_admin should have SET on statement_timeout after initial apply — GRANT never applied")
	}

	// v2: remove the PARAMETER PRIVILEGES declaration entirely. The
	// additive model (RFC Section 11.6) says this must emit nothing — the
	// grant must remain live.
	v2 := `ROLE app_admin NOLOGIN;`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if !checkPriv("app_admin", "work_mem", "SET") {
		t.Fatal("app_admin lost SET on work_mem after the declaration was merely removed from source — additive model violated (a spurious REVOKE was emitted)")
	}
	if !checkPriv("app_admin", "statement_timeout", "SET") {
		t.Fatal("app_admin lost SET on statement_timeout after the declaration was merely removed from source — additive model violated (a spurious REVOKE was emitted)")
	}

	// v3: an explicit REVOKES entry must actually revoke it live.
	v3 := `ROLE app_admin NOLOGIN;

PARAMETER PRIVILEGES {
    REVOKES {
        SET ON PARAMETER work_mem FROM app_admin;
    }
}`
	if err := os.WriteFile(f, []byte(v3), 0o644); err != nil {
		t.Fatalf("write v3: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if checkPriv("app_admin", "work_mem", "SET") {
		t.Fatal("app_admin still has SET on work_mem after an explicit REVOKES entry — REVOKE never applied")
	}
	// statement_timeout was never targeted by the REVOKES entry and was
	// dropped from GRANTS in v2 — it was already proven still-live above;
	// the REVOKES-driven apply must not touch it either.
	if !checkPriv("app_admin", "statement_timeout", "SET") {
		t.Fatal("app_admin lost SET on statement_timeout as a side effect of an unrelated REVOKES entry")
	}
}

// TestRoundtripParameterPrivilegesGrantedBy guards the lower-priority
// fresh-audit finding that Parameter Privileges (Section 11.6) had no
// GRANTED BY support at all, unlike every other GRANT kind in this
// codebase. Real PostgreSQL's GRANT/REVOKE ... ON PARAMETER accepts the
// same optional GRANTED BY role-spec as any other GRANT form (confirmed
// live via a direct pg_query.Parse probe before implementing this).
// Mirrors TestRoundtripGrantGrantedBy's structure/reasoning: CURRENT_USER
// is the only role-spec value guaranteed to apply in any environment (real
// PostgreSQL restricts the effective grantor to current_user regardless of
// who is named). Proves: (a) a declared GRANT ... GRANTED BY CURRENT_USER
// actually applies and pg_parameter_acl's grantor is the connecting role,
// (b) a second plan is a no-op against live-introspected state (guards
// introspectParameterPrivileges now populating ParameterGrant.GrantedBy
// from the real resolved grantor, which grantedByMatches must not treat as
// drift against the declared CURRENT_USER), and (c) an explicit
// REVOKES entry with GRANTED BY CURRENT_USER actually revokes it live.
func TestRoundtripParameterPrivilegesGrantedBy(t *testing.T) {
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

	grantorOf := func() (string, bool) {
		t.Helper()
		rows, err := conn.QueryRows(ctx,
			`SELECT acl.grantor::regrole::text FROM pg_parameter_acl pa,
			 LATERAL aclexplode(pa.paracl) acl
			 WHERE pa.parname = 'work_mem' AND acl.grantee = 'app_admin'::regrole
			 AND acl.privilege_type = 'SET'`)
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
	v1 := `ROLE app_admin NOLOGIN;

PARAMETER PRIVILEGES {
    GRANTS {
        SET ON PARAMETER work_mem TO app_admin GRANTED BY CURRENT_USER;
    }
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

	grantor, ok := grantorOf()
	if !ok {
		t.Fatal("app_admin has no live SET grant on work_mem after initial apply — GRANT never applied")
	}
	if grantor != connRole {
		t.Fatalf("grantor: got %q, want %q (the connecting role) — GRANTED BY CURRENT_USER did not resolve correctly", grantor, connRole)
	}

	// Live-introspected state must also be a no-op for the unchanged
	// declaration — see this test's own doc comment for why this
	// specifically exercises grantedByMatches for Parameter Privileges.
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)

	// v2: an explicit REVOKES entry with GRANTED BY CURRENT_USER must
	// actually revoke it live.
	v2 := `ROLE app_admin NOLOGIN;

PARAMETER PRIVILEGES {
    REVOKES {
        SET ON PARAMETER work_mem FROM app_admin GRANTED BY CURRENT_USER;
    }
}`
	if err := os.WriteFile(f, []byte(v2), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	if _, ok := grantorOf(); ok {
		t.Fatal("app_admin still has a live SET grant on work_mem after an explicit REVOKES entry with GRANTED BY — REVOKE never applied")
	}
}
