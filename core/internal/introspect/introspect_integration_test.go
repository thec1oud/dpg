//go:build integration

package introspect_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestIntrospectAggregate is the live-catalog guard for CREATE AGGREGATE
// reconstruction: Args (previously would have been fine since introspection
// never shared the builder's broken ds.Args parsing) and, more importantly,
// Options/Body — previously always empty ("we cannot reconstruct DDL from
// catalog"), which meant dpg dump could never round-trip a live aggregate at
// all, and a --live apply of a brand-new aggregate had no SQL to emit.
func TestIntrospectAggregate(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE AGGREGATE public.amount_product (numeric) (
			SFUNC = numeric_mul, STYPE = numeric, INITCOND = '1'
		)`,
		`COMMENT ON AGGREGATE public.amount_product(numeric) IS 'multiplicative aggregate'`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Aggregate
	for _, obj := range objects {
		if a, ok := obj.(*ir.Aggregate); ok && a.Name == "amount_product" && a.Schema == "public" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: aggregate public.amount_product not found in results")
	}
	if len(found.Args) != 1 || found.Args[0].Type.Name != "numeric" {
		t.Fatalf("Args: got %+v, want 1 arg of type numeric", found.Args)
	}
	if found.Comment == nil || *found.Comment != "multiplicative aggregate" {
		t.Errorf("Comment: got %v", found.Comment)
	}

	wantOpts := map[string]string{"sfunc": "numeric_mul", "stype": "numeric", "initcond": "'1'"}
	if len(found.Options) != len(wantOpts) {
		t.Fatalf("Options: got %d, want %d: %+v", len(found.Options), len(wantOpts), found.Options)
	}
	for _, p := range found.Options {
		if want, ok := wantOpts[p.Key]; !ok || want != p.Value {
			t.Errorf("Options[%s]: got %q, want %q", p.Key, p.Value, wantOpts[p.Key])
		}
	}
	if !strings.Contains(found.Body, "CREATE AGGREGATE") || !strings.Contains(found.Body, "numeric_mul") {
		t.Errorf("Body should reconstruct a usable CREATE AGGREGATE statement, got %q", found.Body)
	}
}

// ── PUBLIC EXECUTE revocation synthesis (RFC audit item C.4) ───────────────
// Regression guard for the live-reproduced bug: introspection only ever read
// aclexplode(proacl)'s existing rows, so a function-like object whose PUBLIC
// EXECUTE default was explicitly revoked (a common security practice) looked
// identical to one that was never touched at all — dump→reapply silently
// restored PUBLIC's implicit default, undoing the revocation. Fixed by
// synthesizing a Revocation whenever proacl is materialized (NOT NULL) but
// carries no PUBLIC row.

func TestIntrospectAggregateSynthesizesPublicRevocationWhenExplicitlyRevoked(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE AGGREGATE public.locked_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)`,
		`REVOKE EXECUTE ON FUNCTION public.locked_sum(numeric) FROM PUBLIC`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Aggregate
	for _, obj := range objects {
		if a, ok := obj.(*ir.Aggregate); ok && a.Name == "locked_sum" && a.Schema == "public" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: aggregate public.locked_sum not found in results")
	}
	if len(found.Revocations) != 1 || len(found.Revocations[0].Roles) != 1 || found.Revocations[0].Roles[0] != "PUBLIC" {
		t.Fatalf("Revocations: got %+v, want one EXECUTE-FROM-PUBLIC revocation", found.Revocations)
	}
	if !slices.Contains(found.Revocations[0].Privileges, "EXECUTE") {
		t.Errorf("Revocations[0].Privileges: got %v, want EXECUTE", found.Revocations[0].Privileges)
	}
}

func TestIntrospectAggregateUntouchedACLHasNoSyntheticRevocation(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE AGGREGATE public.default_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)`); err != nil {
		t.Fatalf("exec: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Aggregate
	for _, obj := range objects {
		if a, ok := obj.(*ir.Aggregate); ok && a.Name == "default_sum" && a.Schema == "public" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: aggregate public.default_sum not found in results")
	}
	if len(found.Revocations) != 0 {
		t.Errorf("Revocations: got %+v, want none — PUBLIC EXECUTE was never touched (proacl IS NULL)", found.Revocations)
	}
}

func TestIntrospectAggregateExplicitGrantToOtherRoleKeepsPublicDefault(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE ROLE it_analyst`,
		`CREATE AGGREGATE public.shared_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)`,
		`GRANT EXECUTE ON FUNCTION public.shared_sum(numeric) TO it_analyst`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Aggregate
	for _, obj := range objects {
		if a, ok := obj.(*ir.Aggregate); ok && a.Name == "shared_sum" && a.Schema == "public" {
			found = a
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: aggregate public.shared_sum not found in results")
	}
	// proacl is materialized (the GRANT to it_analyst forced that), but
	// PUBLIC's default EXECUTE was never revoked, so aclexplode still
	// carries a PUBLIC row and no synthetic revocation should be added.
	if len(found.Revocations) != 0 {
		t.Errorf("Revocations: got %+v, want none — PUBLIC's default was never revoked", found.Revocations)
	}
}

func TestIntrospectFunctionSynthesizesPublicRevocationWhenExplicitlyRevoked(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE FUNCTION public.locked_fn() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`REVOKE EXECUTE ON FUNCTION public.locked_fn() FROM PUBLIC`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Function
	for _, obj := range objects {
		if f, ok := obj.(*ir.Function); ok && f.Name == "locked_fn" && f.Schema == "public" {
			found = f
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: function public.locked_fn not found in results")
	}
	if len(found.Revocations) != 1 || len(found.Revocations[0].Roles) != 1 || found.Revocations[0].Roles[0] != "PUBLIC" {
		t.Fatalf("Revocations: got %+v, want one EXECUTE-FROM-PUBLIC revocation", found.Revocations)
	}
}

func TestIntrospectProcedureSynthesizesPublicRevocationWhenExplicitlyRevoked(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE PROCEDURE public.locked_proc() LANGUAGE sql AS 'SELECT 1'`,
		`REVOKE EXECUTE ON PROCEDURE public.locked_proc() FROM PUBLIC`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Procedure
	for _, obj := range objects {
		if p, ok := obj.(*ir.Procedure); ok && p.Name == "locked_proc" && p.Schema == "public" {
			found = p
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: procedure public.locked_proc not found in results")
	}
	if len(found.Revocations) != 1 || len(found.Revocations[0].Roles) != 1 || found.Revocations[0].Roles[0] != "PUBLIC" {
		t.Fatalf("Revocations: got %+v, want one EXECUTE-FROM-PUBLIC revocation", found.Revocations)
	}
}

// TestIntrospectDefaultPrivileges is the live-catalog guard for
// pg_default_acl reconstruction — previously never queried at all, so
// introspection always returned zero *ir.DefaultPrivileges objects
// regardless of what was actually configured live. Declares default
// privileges for a role across two object types (TABLES, FUNCTIONS) to
// confirm pg_default_acl's real one-row-per-(role,schema,objtype) model is
// correctly reconstructed as two independent objects, matching
// Builder.BuildDefaultPrivileges's identical split on the compiled-source
// side.
func TestIntrospectDefaultPrivileges(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE ROLE dp_admin`,
		`CREATE ROLE dp_reader`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE dp_admin IN SCHEMA public GRANT SELECT ON TABLES TO dp_reader`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE dp_admin IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO dp_reader`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	byType := map[string]*ir.DefaultPrivileges{}
	for _, obj := range objects {
		if dp, ok := obj.(*ir.DefaultPrivileges); ok && dp.ForRole != nil && *dp.ForRole == "dp_admin" {
			byType[dp.ObjectType] = dp
		}
	}
	if len(byType) != 2 {
		t.Fatalf("expected 2 DefaultPrivileges objects (TABLES/FUNCTIONS) for dp_admin, got %d: %+v", len(byType), byType)
	}

	tables, ok := byType["TABLES"]
	if !ok {
		t.Fatal("missing DefaultPrivileges for TABLES")
	}
	if tables.InSchema == nil || *tables.InSchema != "public" {
		t.Errorf("TABLES InSchema: got %v, want public", tables.InSchema)
	}
	if len(tables.Grants) != 1 || tables.Grants[0].Roles[0] != "dp_reader" || tables.Grants[0].Privileges[0] != "SELECT" {
		t.Errorf("TABLES Grants: got %+v", tables.Grants)
	}

	funcs, ok := byType["FUNCTIONS"]
	if !ok {
		t.Fatal("missing DefaultPrivileges for FUNCTIONS")
	}
	if len(funcs.Grants) != 1 || funcs.Grants[0].Roles[0] != "dp_reader" || funcs.Grants[0].Privileges[0] != "EXECUTE" {
		t.Errorf("FUNCTIONS Grants: got %+v", funcs.Grants)
	}
}

// TestIntrospectDefaultPrivilegesExcludesSelfGrant guards a real bug: unlike
// every sibling *Grants query (table/column/sequence/function/schema/type/
// FDW), introspectDefaultPrivileges had no "grantor <> grantee" filter.
// PostgreSQL materializes a self-grant aclitem for the defaclrole the moment
// ANY explicit ALTER DEFAULT PRIVILEGES ... GRANT touches the entry (same
// mechanism as the relacl/proacl owner self-grant), and without the filter
// that phantom entry read back as a real grant, corrupting dpg dump with a
// bogus extra GRANTS {} block.
func TestIntrospectDefaultPrivilegesExcludesSelfGrant(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE ROLE dpsg_admin`,
		`CREATE ROLE dpsg_reader`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE dpsg_admin GRANT SELECT ON TABLES TO dpsg_reader`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var dp *ir.DefaultPrivileges
	for _, obj := range objects {
		if d, ok := obj.(*ir.DefaultPrivileges); ok && d.ForRole != nil && *d.ForRole == "dpsg_admin" && d.ObjectType == "TABLES" {
			dp = d
		}
	}
	if dp == nil {
		t.Fatal("missing DefaultPrivileges for dpsg_admin/TABLES")
	}
	for _, g := range dp.Grants {
		if slices.Contains(g.Roles, "dpsg_admin") {
			t.Fatalf("phantom self-grant to dpsg_admin leaked into Grants: %+v", dp.Grants)
		}
	}
	if len(dp.Grants) != 1 || dp.Grants[0].Roles[0] != "dpsg_reader" {
		t.Fatalf("expected exactly one grant to dpsg_reader, got %+v", dp.Grants)
	}
}

// TestIntrospectColumnGrantsExcludesTableInheritedPrivileges guards a real
// bug found live-testing a demo project: introspectColumnGrants used to
// query information_schema.column_privileges, which PostgreSQL defines to
// report every EFFECTIVE privilege a role has on a column — including
// privileges the role only has because of a table-level GRANT, not a real
// column-level ACL entry. A table with nothing but a table-level "GRANT
// SELECT TO app_reader" (no column-level grant anywhere) had that same
// SELECT spuriously duplicated onto every column's Grants, confirmed live
// against a real PostgreSQL 17 container: pg_attribute.attacl itself was
// NULL for all of them the whole time. Now queries attacl directly (via
// aclexplode), the same technique introspectTableGrants already used for
// pg_class.relacl. This table declares both shapes side by side: a
// table-level-only grant (id, name — must end up with zero column grants)
// and a genuine column-level grant (email — must end up with exactly one).
func TestIntrospectColumnGrantsExcludesTableInheritedPrivileges(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE ROLE app_reader`,
		`CREATE TABLE cg_users (id bigint, email text, name text)`,
		`GRANT SELECT ON TABLE cg_users TO app_reader`,
		`GRANT SELECT (email) ON TABLE cg_users TO app_reader`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var tbl *ir.Table
	for _, obj := range objects {
		if t2, ok := obj.(*ir.Table); ok && t2.Name == "cg_users" {
			tbl = t2
		}
	}
	if tbl == nil {
		t.Fatal("table cg_users missing from introspection")
	}
	for _, col := range tbl.Columns {
		switch col.Name {
		case "id", "name":
			if len(col.Grants) != 0 {
				t.Errorf("column %s: expected zero grants (table-level only), got %+v", col.Name, col.Grants)
			}
		case "email":
			if len(col.Grants) != 1 || col.Grants[0].Roles[0] != "app_reader" || col.Grants[0].Privileges[0] != "SELECT" {
				t.Errorf("column email: expected exactly one SELECT grant to app_reader, got %+v", col.Grants)
			}
		}
	}
}

func TestIntrospectTable(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx,
		`CREATE TABLE public.items (
			id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			label text NOT NULL,
			qty   integer NOT NULL DEFAULT 0
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Table
	for _, obj := range objects {
		if tbl, ok := obj.(*ir.Table); ok && tbl.Name == "items" && tbl.Schema == "public" {
			found = tbl
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: table public.items not found in results")
	}

	wantCols := map[string]string{"id": "bigint", "label": "text", "qty": "integer"}
	for _, col := range found.Columns {
		want, ok := wantCols[col.Name]
		if !ok {
			continue
		}
		if col.Type.String() != want {
			t.Errorf("column %s: type = %q, want %q", col.Name, col.Type.String(), want)
		}
		delete(wantCols, col.Name)
	}
	for name := range wantCols {
		t.Errorf("column %q not found in introspected table", name)
	}
}

// TestIntrospectColumnStorageIsTypeDefault is the live-catalog guard for a
// new field added during a dump false-negative fix (Owner/Storage were
// genuinely diffed but never rendered by dump): every real column has a
// concrete pg_attribute.attstorage value, so Storage itself can't tell
// "matches the type's own default" apart from "explicitly overridden" the
// way a nil pointer normally would — StorageIsTypeDefault (computed via a
// live join against pg_type.typstorage) is what dump uses to avoid adding a
// STORAGE line to every ordinary variable-length column.
func TestIntrospectColumnStorageIsTypeDefault(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// body: text, left at its type default (EXTENDED). id: integer, left at
	// its type default (PLAIN). overridden: text, explicitly set to EXTERNAL
	// (text's default is EXTENDED, so this is a genuine override).
	_, err = conn.Exec(ctx,
		`CREATE TABLE public.storage_t (
			id         integer,
			body       text,
			overridden text
		);
		ALTER TABLE public.storage_t ALTER COLUMN overridden SET STORAGE EXTERNAL;`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Table
	for _, obj := range objects {
		if tbl, ok := obj.(*ir.Table); ok && tbl.Name == "storage_t" && tbl.Schema == "public" {
			found = tbl
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: table public.storage_t not found in results")
	}

	byName := map[string]*ir.Column{}
	for _, col := range found.Columns {
		byName[col.Name] = col
	}

	if col := byName["id"]; col == nil || col.Storage == nil || *col.Storage != "PLAIN" || !col.StorageIsTypeDefault {
		t.Errorf("id: got Storage=%v StorageIsTypeDefault=%v, want PLAIN/true", col.Storage, col != nil && col.StorageIsTypeDefault)
	}
	if col := byName["body"]; col == nil || col.Storage == nil || *col.Storage != "EXTENDED" || !col.StorageIsTypeDefault {
		t.Errorf("body: got Storage=%v StorageIsTypeDefault=%v, want EXTENDED/true", col.Storage, col != nil && col.StorageIsTypeDefault)
	}
	if col := byName["overridden"]; col == nil || col.Storage == nil || *col.Storage != "EXTERNAL" || col.StorageIsTypeDefault {
		t.Errorf("overridden: got Storage=%v StorageIsTypeDefault=%v, want EXTERNAL/false", col.Storage, col != nil && col.StorageIsTypeDefault)
	}
}

// TestIntrospectFunctionAndProcedureBody is the live regression guard for
// the dump function/procedure fix: pg_proc.prosrc was previously selected
// only to compute BodyHash, then discarded — dump had no raw body text to
// work with, so it could only emit a placeholder comment for a function
// (and had no case at all for procedures). Confirms the raw body now lands
// on Attrs.Body against a real PostgreSQL 17 catalog for both.
func TestIntrospectFunctionAndProcedureBody(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `
CREATE FUNCTION public.add_ints(a integer, b integer) RETURNS integer
LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RETURN a + b;
END;
$$;

CREATE PROCEDURE public.recalc() LANGUAGE plpgsql AS $$
BEGIN
    NULL;
END;
$$;`)
	if err != nil {
		t.Fatalf("create function/procedure: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var fn *ir.Function
	var proc *ir.Procedure
	for _, obj := range objects {
		switch o := obj.(type) {
		case *ir.Function:
			if o.Name == "add_ints" {
				fn = o
			}
		case *ir.Procedure:
			if o.Name == "recalc" {
				proc = o
			}
		}
	}
	if fn == nil {
		t.Fatal("introspect: function public.add_ints not found")
	}
	if !strings.Contains(fn.Attrs.Body, "RETURN a + b") {
		t.Errorf("function Attrs.Body = %q, want it to contain the real function body", fn.Attrs.Body)
	}
	if proc == nil {
		t.Fatal("introspect: procedure public.recalc not found")
	}
	if !strings.Contains(proc.Attrs.Body, "NULL") {
		t.Errorf("procedure Attrs.Body = %q, want it to contain the real procedure body", proc.Attrs.Body)
	}

	// Regression guard for introspectFunctionArgs (generalized from
	// introspectFunctionArgModes): add_ints has proargmodes IS NULL (plain
	// IN-only, the common case) — before that generalization, Args only
	// ever got real names for functions with a non-default mode present, so
	// this assertion would fail (both names empty). Argument names must be
	// accurate here for a second reason beyond dump fidelity: they feed the
	// CREATE FUNCTION shim HashFunctionBody's plpgsql canonicalisation
	// compiles through the real PL/pgSQL compiler, which needs them to
	// resolve the body's own parameter references.
	if len(fn.Args) != 2 || fn.Args[0].Name != "a" || fn.Args[1].Name != "b" {
		t.Errorf("function Args = %+v, want [{Name:a ...} {Name:b ...}]", fn.Args)
	}
}

// TestIntrospectPlpgsqlBodyHashStableAcrossReformatting is the live
// end-to-end guard for the introspect-side half of the plpgsql body-hash
// canonicalization fix (the builder-side half is covered by
// core/integration/roundtrip_opaque_test.go's snapshot-level tests): a
// function is created, introspected, then ALTERed to a cosmetically
// reformatted (but semantically identical) body, and re-introspected —
// BodyHash must be unchanged. This exercises introspectFunctionArgs +
// ir.RenderCreateFunctionSQL + ir.HashFunctionBody against a real catalog,
// not just the direct pg_query-level unit tests in internal/ir.
func TestIntrospectPlpgsqlBodyHashStableAcrossReformatting(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	create := `CREATE FUNCTION public.bump(n integer) RETURNS integer LANGUAGE plpgsql AS $$
BEGIN
    n := n + 1;
    RETURN n;
END;
$$;`
	if _, err := conn.Exec(ctx, create); err != nil {
		t.Fatalf("create function: %v", err)
	}

	findBump := func() *ir.Function {
		ci := introspect.New()
		objects, err := ci.Introspect(ctx, conn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, obj := range objects {
			if fn, ok := obj.(*ir.Function); ok && fn.Name == "bump" {
				return fn
			}
		}
		t.Fatal("introspect: function public.bump not found")
		return nil
	}

	before := findBump()
	if before.BodyHash == "" {
		t.Fatal("expected a non-empty BodyHash")
	}

	reformatted := `CREATE OR REPLACE FUNCTION public.bump(n integer) RETURNS integer LANGUAGE plpgsql AS $$
BEGIN
  -- bump n by one
  n := n + 1;
  RETURN n;
END;
$$;`
	if _, err := conn.Exec(ctx, reformatted); err != nil {
		t.Fatalf("alter function: %v", err)
	}

	after := findBump()
	if after.BodyHash != before.BodyHash {
		t.Errorf("BodyHash changed after cosmetic-only reformatting: before=%q after=%q", before.BodyHash, after.BodyHash)
	}

	genuinelyChanged := `CREATE OR REPLACE FUNCTION public.bump(n integer) RETURNS integer LANGUAGE plpgsql AS $$
BEGIN
  n := n + 2;
  RETURN n;
END;
$$;`
	if _, err := conn.Exec(ctx, genuinelyChanged); err != nil {
		t.Fatalf("alter function (genuine change): %v", err)
	}

	changed := findBump()
	if changed.BodyHash == before.BodyHash {
		t.Error("BodyHash unchanged after a genuine logic change — canonicalization is masking real drift")
	}
}

// TestIntrospectFunctionRowsSuppressesDefault is the live-catalog guard for
// ROWS suppression specifically: ROWS is only syntactically valid on a
// set-returning function ("RETURNS SETOF ..."), which DPG's own compiler
// can't yet author (ir.TypeRef has no SETOF representation — a separate,
// pre-existing gap). So this exercises introspection directly against a
// SETOF function created via raw SQL: one with an explicit ROWS override,
// one left at PostgreSQL's own default (1000 for a set-returning function),
// confirming Attrs.Rows is populated only when it genuinely differs from
// that live default — mirroring TestIntrospectColumnStorageIsTypeDefault's
// suppress-when-default pattern for COST/ROWS/PARALLEL.
func TestIntrospectFunctionRowsSuppressesDefault(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `
CREATE FUNCTION public.gen_default(n integer) RETURNS SETOF integer
LANGUAGE sql AS $$
    SELECT generate_series(1, n);
$$;

CREATE FUNCTION public.gen_explicit(n integer) RETURNS SETOF integer
LANGUAGE sql ROWS 50 AS $$
    SELECT generate_series(1, n);
$$;`)
	if err != nil {
		t.Fatalf("create functions: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var byName = map[string]*ir.Function{}
	for _, obj := range objects {
		if fn, ok := obj.(*ir.Function); ok {
			byName[fn.Name] = fn
		}
	}

	def := byName["gen_default"]
	if def == nil {
		t.Fatal("introspect: function public.gen_default not found")
	}
	if def.Attrs.Rows != nil {
		t.Errorf("gen_default: Attrs.Rows = %v, want nil (matches PostgreSQL's own default of 1000 for a set-returning function)", *def.Attrs.Rows)
	}

	explicit := byName["gen_explicit"]
	if explicit == nil {
		t.Fatal("introspect: function public.gen_explicit not found")
	}
	if explicit.Attrs.Rows == nil || *explicit.Attrs.Rows != 50 {
		got := "nil"
		if explicit.Attrs.Rows != nil {
			got = fmt.Sprintf("%v", *explicit.Attrs.Rows)
		}
		t.Errorf("gen_explicit: Attrs.Rows = %s, want 50", got)
	}
}

// TestIntrospectFunctionArgModes is the live-catalog guard for the gap found
// while fixing RETURNS TABLE(...) introspection: oidvectortypes(proargtypes)
// — the source introspectFunctions used for Args — only ever reports
// IN/INOUT/VARIADIC argument TYPES with no mode or name at all, and OUT
// arguments are invisible to it entirely. Before this fix: a plain OUT-only
// function's OUT columns were completely missing from introspected Args
// (not just mis-rendered, the same severity as the RETURNS TABLE bug); an
// INOUT or VARIADIC function's mode keyword was silently lost even though
// its type was captured correctly. Covers all three plus a plain-IN control
// (confirmed to still take the original, unmodified introspection path).
func TestIntrospectFunctionArgModes(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `
CREATE FUNCTION public.f_out(n integer, OUT a integer, OUT b text) LANGUAGE sql AS $$
    SELECT n, 'x';
$$;

CREATE FUNCTION public.f_inout(INOUT n integer) LANGUAGE sql AS $$
    SELECT n;
$$;

CREATE FUNCTION public.f_variadic(VARIADIC n integer[]) RETURNS integer LANGUAGE sql AS $$
    SELECT 1;
$$;

CREATE FUNCTION public.f_plain(n integer, m text) RETURNS integer LANGUAGE sql AS $$
    SELECT n;
$$;

CREATE FUNCTION public.f_unnamed_out(integer, OUT integer) LANGUAGE sql AS $$
    SELECT $1;
$$;`)
	if err != nil {
		t.Fatalf("create functions: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	byName := map[string]*ir.Function{}
	for _, obj := range objects {
		if fn, ok := obj.(*ir.Function); ok {
			byName[fn.Name] = fn
		}
	}

	fOut := byName["f_out"]
	if fOut == nil {
		t.Fatal("introspect: function public.f_out not found")
	}
	if len(fOut.Args) != 3 {
		t.Fatalf("f_out: got %d args, want 3 (n, a, b) — OUT columns must not be missing", len(fOut.Args))
	}
	if fOut.Args[1].Mode != "OUT" || fOut.Args[1].Name != "a" || fOut.Args[1].Type.Name != "integer" {
		t.Errorf("f_out.Args[1]: got %+v, want Mode=OUT Name=a Type=integer", fOut.Args[1])
	}
	if fOut.Args[2].Mode != "OUT" || fOut.Args[2].Name != "b" || fOut.Args[2].Type.Name != "text" {
		t.Errorf("f_out.Args[2]: got %+v, want Mode=OUT Name=b Type=text", fOut.Args[2])
	}

	fInout := byName["f_inout"]
	if fInout == nil {
		t.Fatal("introspect: function public.f_inout not found")
	}
	if len(fInout.Args) != 1 || fInout.Args[0].Mode != "INOUT" || fInout.Args[0].Name != "n" {
		t.Errorf("f_inout.Args: got %+v, want a single Mode=INOUT Name=n arg", fInout.Args)
	}

	fVariadic := byName["f_variadic"]
	if fVariadic == nil {
		t.Fatal("introspect: function public.f_variadic not found")
	}
	if len(fVariadic.Args) != 1 || fVariadic.Args[0].Mode != "VARIADIC" || fVariadic.Args[0].Name != "n" {
		t.Errorf("f_variadic.Args: got %+v, want a single Mode=VARIADIC Name=n arg", fVariadic.Args)
	}

	fPlain := byName["f_plain"]
	if fPlain == nil {
		t.Fatal("introspect: function public.f_plain not found")
	}
	if len(fPlain.Args) != 2 {
		t.Errorf("f_plain.Args: got %d, want 2 (unaffected by this fix, still uses the original oidvectortypes path)", len(fPlain.Args))
	}

	// An unnamed OUT parameter is a real, valid PostgreSQL construct (verified
	// live: proargnames comes back as an entirely NULL array in this case, not
	// per-position empty strings) — guards against a naive non-nullable scan
	// crashing introspection for any user function shaped this way.
	fUnnamedOut := byName["f_unnamed_out"]
	if fUnnamedOut == nil {
		t.Fatal("introspect: function public.f_unnamed_out not found")
	}
	if len(fUnnamedOut.Args) != 2 || fUnnamedOut.Args[1].Mode != "OUT" || fUnnamedOut.Args[1].Name != "" {
		t.Errorf("f_unnamed_out.Args: got %+v, want [{Mode:IN} {Mode:OUT Name:\"\"}]", fUnnamedOut.Args)
	}
}

func TestIntrospectEnum(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `CREATE TYPE public.mood AS ENUM ('happy', 'sad', 'neutral')`)
	if err != nil {
		t.Fatalf("create enum: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Type
	for _, obj := range objects {
		if typ, ok := obj.(*ir.Type); ok && typ.Name == "mood" && typ.Schema == "public" {
			found = typ
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: type public.mood not found in results")
	}
	if found.Variant != "ENUM" {
		t.Errorf("type variant = %q, want ENUM", found.Variant)
	}
	wantVals := []string{"happy", "sad", "neutral"}
	if len(found.EnumValues) != len(wantVals) {
		t.Fatalf("enum values = %v, want %v", found.EnumValues, wantVals)
	}
	for i, v := range wantVals {
		if found.EnumValues[i] != v {
			t.Errorf("enum value[%d] = %q, want %q", i, found.EnumValues[i], v)
		}
	}
}

func TestIntrospectView(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, `CREATE TABLE public.products (id bigint PRIMARY KEY, name text)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = conn.Exec(ctx, `CREATE VIEW public.product_names AS SELECT id, name FROM public.products`)
	if err != nil {
		t.Fatalf("create view: %v", err)
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.View
	for _, obj := range objects {
		if v, ok := obj.(*ir.View); ok && v.Name == "product_names" && v.Schema == "public" {
			found = v
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: view public.product_names not found in results")
	}
}

// TestIntrospectMaterializedViewIndex is the live-catalog guard for RFC
// Section 8.2's matview-block INDEXES support: real PostgreSQL only supports
// indexes on a materialized view or a table, never a plain view — before
// this, introspection never populated ir.View.Indexes at all (the field
// didn't exist), so a materialized view's index was silently invisible to
// dump/diff even though it existed live.
func TestIntrospectMaterializedViewIndex(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE TABLE public.orders (id bigint PRIMARY KEY, status text)`,
		`CREATE MATERIALIZED VIEW public.order_status_totals AS
			SELECT status, count(*) AS c FROM public.orders GROUP BY status`,
		`CREATE UNIQUE INDEX order_status_totals_status_uq ON public.order_status_totals (status)`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.View
	for _, obj := range objects {
		if v, ok := obj.(*ir.View); ok && v.Name == "order_status_totals" && v.Schema == "public" {
			found = v
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: materialized view public.order_status_totals not found in results")
	}
	if !found.Materialized {
		t.Error("expected Materialized = true")
	}
	if len(found.Indexes) != 1 {
		t.Fatalf("expected 1 index, got %d: %+v", len(found.Indexes), found.Indexes)
	}
	idx := found.Indexes[0]
	if idx.Name != "order_status_totals_status_uq" || !idx.Unique {
		t.Errorf("index: got %+v", idx)
	}
}

// TestIntrospectSubPartitionedTable is the live-catalog guard for RFC Section 7.13
// sub-partitioning: a partition can itself carry a nested PARTITION BY and
// its own child partitions (relkind 'p' again). Before this,
// introspectPartitions only recorded ONE flat level — a partition that was
// itself further partitioned would get a partition key of its own but no way
// to attach ITS children, since the parent-lookup map only indexed top-level
// tables.
func TestIntrospectSubPartitionedTable(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE TABLE public.metrics (
			id         bigint GENERATED ALWAYS AS IDENTITY,
			created_at timestamptz NOT NULL,
			channel    text NOT NULL
		) PARTITION BY RANGE (created_at)`,
		`CREATE TABLE public.metrics_2025 PARTITION OF public.metrics
			FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')`,
		`CREATE TABLE public.metrics_2026 PARTITION OF public.metrics
			FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')
			PARTITION BY LIST (channel)`,
		`CREATE TABLE public.metrics_2026_web PARTITION OF public.metrics_2026
			FOR VALUES IN ('web')`,
		`CREATE TABLE public.metrics_2026_other PARTITION OF public.metrics_2026
			FOR VALUES IN ('mobile', 'api')`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Table
	for _, obj := range objects {
		if tbl, ok := obj.(*ir.Table); ok && tbl.Name == "metrics" && tbl.Schema == "public" {
			found = tbl
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: table public.metrics not found in results")
	}
	if found.PartitionBy == nil || found.PartitionBy.Strategy != "RANGE" {
		t.Fatalf("expected top-level PartitionBy RANGE, got %v", found.PartitionBy)
	}
	if len(found.Partitions) != 2 {
		t.Fatalf("expected 2 partitions, got %d: %+v", len(found.Partitions), found.Partitions)
	}

	var leaf, sub *ir.Partition
	for _, p := range found.Partitions {
		switch p.Name {
		case "metrics_2025":
			leaf = p
		case "metrics_2026":
			sub = p
		}
	}
	if leaf == nil {
		t.Fatal("metrics_2025 partition not found")
	}
	if leaf.PartitionBy != nil || len(leaf.Partitions) != 0 {
		t.Errorf("metrics_2025 should be a leaf, got %+v", leaf)
	}

	if sub == nil {
		t.Fatal("metrics_2026 partition not found")
	}
	if sub.PartitionBy == nil || sub.PartitionBy.Strategy != "LIST" {
		t.Fatalf("expected metrics_2026 PartitionBy LIST, got %v", sub.PartitionBy)
	}
	if len(sub.PartitionBy.Columns) != 1 || sub.PartitionBy.Columns[0] != "channel" {
		t.Errorf("metrics_2026 partition columns: got %v", sub.PartitionBy.Columns)
	}
	if len(sub.Partitions) != 2 {
		t.Fatalf("expected 2 nested sub-partitions under metrics_2026, got %d: %+v", len(sub.Partitions), sub.Partitions)
	}

	names := map[string]bool{}
	for _, p := range sub.Partitions {
		names[p.Name] = true
		if p.PartitionBy != nil {
			t.Errorf("sub-partition %s should be a leaf, got PartitionBy=%v", p.Name, p.PartitionBy)
		}
	}
	if !names["metrics_2026_web"] || !names["metrics_2026_other"] {
		t.Errorf("expected metrics_2026_web and metrics_2026_other, got %v", names)
	}
}

// TestIntrospectPublicSchemaGrantsVisible is the live-catalog guard for RFC
// audit item C.2: introspectSchemas/introspectSchemaGrants used to hard-
// exclude "public", so a real GRANT on it was completely invisible to
// dpg dump and plan --live/verify drift detection. Reproduces the audit's
// exact scenario: GRANT USAGE ON SCHEMA public TO a role, then confirm
// introspection now actually captures it.
func TestIntrospectPublicSchemaGrantsVisible(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE ROLE audit_role`,
		`GRANT USAGE ON SCHEMA public TO audit_role`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	ci := introspect.New()
	objects, err := ci.Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	var found *ir.Schema
	for _, obj := range objects {
		if s, ok := obj.(*ir.Schema); ok && s.Name == "public" {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatal("introspect: schema public not found in results — C.2 regression")
	}
	if found.Owner == nil {
		t.Error("Owner: got nil, want the public schema's real owner")
	}
	var grantedToAuditRole bool
	for _, g := range found.Grants {
		if slices.Contains(g.Roles, "audit_role") && slices.Contains(g.Privileges, "USAGE") {
			grantedToAuditRole = true
		}
	}
	if !grantedToAuditRole {
		t.Errorf("Grants: got %+v, want a USAGE grant to audit_role", found.Grants)
	}
}
