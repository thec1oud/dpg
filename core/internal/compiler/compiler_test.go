package compiler_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/thec1oud/dpg/internal/blockparser"
	"github.com/thec1oud/dpg/internal/compiler"
	_ "github.com/thec1oud/dpg/internal/graph"
	"github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	"github.com/thec1oud/dpg/internal/pipeline"
	_ "github.com/thec1oud/dpg/internal/scanner"
)

// ── inferSchemaFromPath ───────────────────────────────────────────────────────

// inferSchemaFromPath is unexported, so we test it indirectly via Compile:
// a file placed under <dbDir>/schemas/<name>/... should produce an object with
// Schema == <name>.

func writeDPG(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func compile(t *testing.T, dbDir string, files []string) []pipeline.IRObject {
	t.Helper()
	out, _, err := compiler.Compile(files, dbDir, pipeline.Default)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return out
}

// ── Schema inference from directory ──────────────────────────────────────────

func TestCompile_InferSchemaFromPath(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "iam", "tables.dpg")
	writeDPG(t, f, `TABLE users (id BIGINT NOT NULL);`)

	objects := compile(t, dbDir, []string{f})

	var tbl *ir.Table
	for _, o := range objects {
		if t2, ok := o.(*ir.Table); ok && t2.Name == "users" {
			tbl = t2
			break
		}
	}
	if tbl == nil {
		t.Fatal("table 'users' not found in output")
	}
	if tbl.Schema != "iam" {
		t.Errorf("Schema inferred from directory: got %q", tbl.Schema)
	}
}

// TestCompile_BOMAndCRLFNormalized covers RFC Section 4.1: a leading UTF-8
// BOM MUST be silently stripped and CRLF line endings MUST be normalized to
// LF before any scanning occurs.
func TestCompile_BOMAndCRLFNormalized(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "tables.dpg")
	src := "\xEF\xBB\xBFTABLE users (\r\n    id BIGINT NOT NULL\r\n);\r\n"
	writeDPG(t, f, src)

	objects := compile(t, dbDir, []string{f})

	var tbl *ir.Table
	for _, o := range objects {
		if t2, ok := o.(*ir.Table); ok && t2.Name == "users" {
			tbl = t2
			break
		}
	}
	if tbl == nil {
		t.Fatal("table 'users' not found in output; BOM/CRLF handling likely broke scanning")
	}
}

func TestCompile_InferSchemaInjectsSyntheticSchema(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "billing", "tables.dpg")
	writeDPG(t, f, `TABLE invoices (id BIGINT NOT NULL);`)

	objects := compile(t, dbDir, []string{f})

	var found bool
	for _, o := range objects {
		if s, ok := o.(*ir.Schema); ok && s.Name == "billing" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected synthetic 'billing' schema in output")
	}
}

// TestCompile_InferSchemaFromReservedWordDirectory guards a real bug: a
// schema directory literally named after a PostgreSQL reserved word (e.g.
// schemas/order/) crashed compilation entirely — the synthetic "CREATE
// SCHEMA <name>" statement Compile injects fed pg_query an unquoted
// reserved word, a hard syntax error (confirmed live via pg_query.Parse:
// "CREATE SCHEMA order" errors, "CREATE SCHEMA \"order\"" doesn't). Same bug
// class as the explicit `SCHEMA name { }` reserved-word fix (scanner.go's
// readSchemaDecl), never extended to this directory-inferred path. Also
// covers a table declared inside that schema, proving the fix doesn't just
// avoid the crash but produces a usable, correctly-scoped schema.
func TestCompile_InferSchemaFromReservedWordDirectory(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "order", "tables.dpg")
	writeDPG(t, f, `TABLE items (id BIGINT NOT NULL);`)

	objects := compile(t, dbDir, []string{f})

	var schemaFound, tableFound bool
	for _, o := range objects {
		if s, ok := o.(*ir.Schema); ok && s.Name == "order" {
			schemaFound = true
		}
		if t2, ok := o.(*ir.Table); ok && t2.Name == "items" {
			tableFound = true
			if t2.Schema != "order" {
				t.Errorf("table Schema: got %q, want %q", t2.Schema, "order")
			}
		}
	}
	if !schemaFound {
		t.Error("expected synthetic 'order' schema in output")
	}
	if !tableFound {
		t.Error("expected 'items' table in output")
	}
}

func TestCompile_PublicSchemaNotInjected(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "public", "tables.dpg")
	writeDPG(t, f, `TABLE accounts (id BIGINT NOT NULL);`)

	objects := compile(t, dbDir, []string{f})

	for _, o := range objects {
		if s, ok := o.(*ir.Schema); ok && s.Name == "public" {
			t.Error("'public' schema should not be injected")
			break
		}
	}
}

// ── DEFAULT PRIVILEGES ────────────────────────────────────────────────────────
// DEFAULT PRIVILEGES is the one DPG object kind that never goes through
// PGSQLParser at all: real PostgreSQL's ALTER DEFAULT PRIVILEGES statement
// requires its GRANT/REVOKE action inline, so DPG's own "[FOR ROLE x]
// [IN SCHEMA y]" header text is never valid standalone PG SQL on its own —
// confirmed live against postgres:17 ("syntax error at end of input").
// Found live-testing a demo project: both of the RFC's own documented
// declaration forms (top-level, and nested inside a SCHEMA block) failed
// with exactly that PG parse error, because Compile unconditionally routed
// every object's Part 1 through PGSQLParser with no exception for this kind.

// TestCompile_DefaultPrivilegesTopLevel guards the top-level declaration
// form and the real multi-object-type PG grant grammar ("GRANT priv ON
// TABLES/FUNCTIONS/SEQUENCES/... TO role" — the object type is part of the
// GRANT clause itself in real PG, not a DPG-invented whole-declaration
// wrapper) — the RFC's own worked example declares TABLES, FUNCTIONS, and
// SEQUENCES together in one block, which must produce three independently-
// diffable IR objects (pg_default_acl has one row per (role, schema, object
// type) tuple), not one object that silently drops all but the last type.
func TestCompile_DefaultPrivilegesTopLevel(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "objects.dpg")
	writeDPG(t, f, `
DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public {
    GRANTS {
        SELECT ON TABLES TO app_readonly;
        EXECUTE ON FUNCTIONS TO app_service;
        USAGE ON SEQUENCES TO app_service;
    }
}`)

	objects := compile(t, dbDir, []string{f})

	byType := map[string]*ir.DefaultPrivileges{}
	for _, o := range objects {
		if dp, ok := o.(*ir.DefaultPrivileges); ok {
			byType[dp.ObjectType] = dp
		}
	}
	if len(byType) != 3 {
		t.Fatalf("expected 3 DefaultPrivileges objects (TABLES/FUNCTIONS/SEQUENCES), got %d: %+v", len(byType), byType)
	}
	for _, objType := range []string{"TABLES", "FUNCTIONS", "SEQUENCES"} {
		dp, ok := byType[objType]
		if !ok {
			t.Fatalf("missing DefaultPrivileges for %s", objType)
		}
		if dp.ForRole == nil || *dp.ForRole != "app_admin" {
			t.Errorf("%s: ForRole: got %v, want app_admin", objType, dp.ForRole)
		}
		if dp.InSchema == nil || *dp.InSchema != "public" {
			t.Errorf("%s: InSchema: got %v, want public", objType, dp.InSchema)
		}
		if len(dp.Grants) != 1 {
			t.Fatalf("%s: expected 1 grant, got %d", objType, len(dp.Grants))
		}
	}
	if byType["TABLES"].Grants[0].Privileges[0] != "SELECT" || byType["TABLES"].Grants[0].Roles[0] != "app_readonly" {
		t.Errorf("TABLES grant: got %+v", byType["TABLES"].Grants[0])
	}
	if byType["FUNCTIONS"].Grants[0].Privileges[0] != "EXECUTE" {
		t.Errorf("FUNCTIONS grant: got %+v", byType["FUNCTIONS"].Grants[0])
	}
	if byType["SEQUENCES"].Grants[0].Privileges[0] != "USAGE" {
		t.Errorf("SEQUENCES grant: got %+v", byType["SEQUENCES"].Grants[0])
	}
}

// TestCompile_DefaultPrivilegesNestedInSchema guards the RFC's other
// documented form: nested inside a SCHEMA block, with IN SCHEMA omitted
// (implied by the enclosing SCHEMA). raw.Schema (from the scanner's nested-
// object handling) must be used as the fallback InSchema when the
// declaration itself has no explicit "IN SCHEMA" clause.
func TestCompile_DefaultPrivilegesNestedInSchema(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "objects.dpg")
	writeDPG(t, f, `
SCHEMA public {
    DEFAULT PRIVILEGES FOR ROLE app_admin {
        GRANTS {
            SELECT ON TABLES TO app_readonly;
        }
    }
}`)

	objects := compile(t, dbDir, []string{f})

	var dp *ir.DefaultPrivileges
	for _, o := range objects {
		if d, ok := o.(*ir.DefaultPrivileges); ok {
			dp = d
		}
	}
	if dp == nil {
		t.Fatalf("no DefaultPrivileges object produced from nested declaration; objects: %+v", objects)
	}
	if dp.ForRole == nil || *dp.ForRole != "app_admin" {
		t.Errorf("ForRole: got %v, want app_admin", dp.ForRole)
	}
	if dp.InSchema == nil || *dp.InSchema != "public" {
		t.Errorf("InSchema (inferred from enclosing SCHEMA block): got %v, want public", dp.InSchema)
	}
	if dp.ObjectType != "TABLES" {
		t.Errorf("ObjectType: got %q, want TABLES", dp.ObjectType)
	}
}

// TestCompile_DefaultPrivilegesRevocationAndFallbackChainRole guards
// REVOKES parsing (ON type FROM role [CASCADE]) and that a declaration
// with no FOR ROLE / IN SCHEMA compiles with both left nil (a database-wide
// default for the current role).
func TestCompile_DefaultPrivilegesRevocationAndFallbackChainRole(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "objects.dpg")
	writeDPG(t, f, `
DEFAULT PRIVILEGES {
    REVOKES {
        SELECT ON TABLES FROM app_readonly CASCADE;
    }
}`)

	objects := compile(t, dbDir, []string{f})

	var dp *ir.DefaultPrivileges
	for _, o := range objects {
		if d, ok := o.(*ir.DefaultPrivileges); ok {
			dp = d
		}
	}
	if dp == nil {
		t.Fatalf("no DefaultPrivileges object produced; objects: %+v", objects)
	}
	if dp.ForRole != nil {
		t.Errorf("ForRole: got %v, want nil", dp.ForRole)
	}
	if dp.InSchema != nil {
		t.Errorf("InSchema: got %v, want nil", dp.InSchema)
	}
	if len(dp.Revocations) != 1 {
		t.Fatalf("expected 1 revocation, got %d", len(dp.Revocations))
	}
	r := dp.Revocations[0]
	if len(r.Privileges) != 1 || r.Privileges[0] != "SELECT" {
		t.Errorf("Revocation privileges: got %v", r.Privileges)
	}
	if len(r.Roles) != 1 || r.Roles[0] != "app_readonly" {
		t.Errorf("Revocation roles: got %v", r.Roles)
	}
	if !r.Cascade {
		t.Error("Revocation Cascade did not round-trip")
	}
}

// ── Schema block forbidden inside schemas/ hierarchy ─────────────────────────

func TestCompile_SchemaBlockInSchemasDirErrors(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "iam", "schema.dpg")
	writeDPG(t, f, `SCHEMA iam {}`)

	_, _, err := compiler.Compile([]string{f}, dbDir, pipeline.Default)
	if err == nil {
		t.Fatal("expected error for SCHEMA block inside schemas/ directory")
	}
}

// ── Multi-file merge ──────────────────────────────────────────────────────────

func TestCompile_MultiFileTableMerge(t *testing.T) {
	dbDir := t.TempDir()
	f1 := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	f2 := filepath.Join(dbDir, "schemas", "app", "extra.dpg")
	writeDPG(t, f1, `TABLE users (id BIGINT NOT NULL);`)
	writeDPG(t, f2, `TABLE users (email TEXT NOT NULL);`)

	objects := compile(t, dbDir, []string{f1, f2})

	var tbl *ir.Table
	for _, o := range objects {
		if t2, ok := o.(*ir.Table); ok && t2.Name == "users" {
			tbl = t2
			break
		}
	}
	if tbl == nil {
		t.Fatal("merged 'users' table not found")
	}
	if len(tbl.Columns) != 2 {
		t.Errorf("merged Columns: expected 2, got %d", len(tbl.Columns))
	}
}

// TestCompile_MultiFileScalarMergeConflict proves Compile's second return
// value surfaces a real scalar-merge-conflict diagnostic (RFC Section 19.1) when
// two files declare the same table with a conflicting OWNER — the same
// last-file-wins-plus-diagnostic behavior internal/merger's own unit tests
// exercise directly, verified here end-to-end through the real compiler
// pipeline (scan/parse/build), not a hand-built IR fixture.
func TestCompile_MultiFileScalarMergeConflict(t *testing.T) {
	dbDir := t.TempDir()
	f1 := filepath.Join(dbDir, "schemas", "app", "a_tables.dpg")
	f2 := filepath.Join(dbDir, "schemas", "app", "b_tables.dpg")
	writeDPG(t, f1, `TABLE users (id BIGINT NOT NULL) { OWNER "alice"; }`)
	writeDPG(t, f2, `TABLE users (email TEXT NOT NULL) { OWNER "bob"; }`)

	objects, mergeDiags, err := compiler.Compile([]string{f1, f2}, dbDir, pipeline.Default)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var conflicts []pipeline.LintDiagnostic
	for _, d := range mergeDiags {
		if d.Rule == "scalar-merge-conflict" {
			conflicts = append(conflicts, d)
		}
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 scalar-merge-conflict diagnostic, got %d: %v", len(conflicts), mergeDiags)
	}

	var tbl *ir.Table
	for _, o := range objects {
		if t2, ok := o.(*ir.Table); ok && t2.Name == "users" {
			tbl = t2
			break
		}
	}
	if tbl == nil {
		t.Fatal("merged 'users' table not found")
	}
	// Last-file-wins is unaffected by the diagnostic (RFC Section 3.7: "the winning
	// value is used regardless").
	if tbl.Owner == nil || *tbl.Owner != "bob" {
		t.Errorf("Owner (last-wins, regardless of conflict): got %v", tbl.Owner)
	}
}

func TestCompile_DuplicateNameMapToolWarns(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE users (id BIGINT NOT NULL) {
		NAME MAP prisma TO "First";
		NAME MAP prisma TO "Second";
	}`)

	objects, mergeDiags, err := compiler.Compile([]string{f}, dbDir, pipeline.Default)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var dups []pipeline.LintDiagnostic
	for _, d := range mergeDiags {
		if d.Rule == "duplicate-namemap-tool" {
			dups = append(dups, d)
		}
	}
	if len(dups) != 1 {
		t.Fatalf("expected 1 duplicate-namemap-tool diagnostic, got %d: %v", len(dups), mergeDiags)
	}
	if !strings.Contains(dups[0].Message, "DPG-E031") {
		t.Errorf("expected DPG-E031 in message, got %q", dups[0].Message)
	}

	var tbl *ir.Table
	for _, o := range objects {
		if t2, ok := o.(*ir.Table); ok && t2.Name == "users" {
			tbl = t2
			break
		}
	}
	if tbl == nil {
		t.Fatal("'users' table not found")
	}
	if len(tbl.NameMaps) != 1 || tbl.NameMaps[0].Value != "Second" {
		t.Errorf("expected last-entry-wins collapsed to 1 NameMap entry with Value=Second, got %+v", tbl.NameMaps)
	}
}

// ── FK dependency ordering in output ─────────────────────────────────────────

func TestCompile_FKDependencyOrdering(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `
TABLE orders (
    id      BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES app.users (id)
);
TABLE users (
    id BIGINT NOT NULL
);
`)
	objects := compile(t, dbDir, []string{f})

	usersIdx, ordersIdx := -1, -1
	for i, o := range objects {
		if t2, ok := o.(*ir.Table); ok {
			switch t2.Name {
			case "users":
				usersIdx = i
			case "orders":
				ordersIdx = i
			}
		}
	}
	if usersIdx < 0 || ordersIdx < 0 {
		t.Fatalf("could not find both tables (users=%d, orders=%d)", usersIdx, ordersIdx)
	}
	if usersIdx >= ordersIdx {
		t.Errorf("users (pos %d) must come before orders (pos %d)", usersIdx, ordersIdx)
	}
}

// ── File not found ────────────────────────────────────────────────────────────

func TestCompile_MissingFileErrors(t *testing.T) {
	_, _, err := compiler.Compile([]string{"/nonexistent/file.dpg"}, "/nonexistent", pipeline.Default)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// ── No files ──────────────────────────────────────────────────────────────────

func TestCompile_EmptyFileList(t *testing.T) {
	objects, _, err := compiler.Compile(nil, t.TempDir(), pipeline.Default)
	if err != nil {
		t.Fatalf("Compile with nil files: %v", err)
	}
	if len(objects) != 0 {
		t.Errorf("expected empty output, got %d objects", len(objects))
	}
}

// ── LIKE source_table (Section 7.1) ───────────────────────────────────────────

func findTable(t *testing.T, objects []pipeline.IRObject, name string) *ir.Table {
	t.Helper()
	for _, o := range objects {
		if tbl, ok := o.(*ir.Table); ok && tbl.Name == name {
			return tbl
		}
	}
	t.Fatalf("table %q not found", name)
	return nil
}

// TestCompile_LikeBareCopiesNamesTypesNotNullOnly is the regression guard
// for the audit's headline finding: buildTable's TableElts switch had no
// case for a LIKE clause at all, so it was silently discarded — "CREATE
// TABLE foo (LIKE bar)" built a table with zero columns and no error.
func TestCompile_LikeBareCopiesNamesTypesNotNullOnly(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE orders (id BIGINT NOT NULL, total NUMERIC DEFAULT 0);
TABLE orders_copy (LIKE orders);`)

	objects := compile(t, dbDir, []string{f})
	tbl := findTable(t, objects, "orders_copy")

	if len(tbl.Columns) != 2 {
		t.Fatalf("expected 2 columns copied from LIKE, got %d: %v", len(tbl.Columns), tbl.Columns)
	}
	if tbl.Columns[0].Name != "id" || !tbl.Columns[0].NotNull {
		t.Errorf("expected id NOT NULL copied, got %+v", tbl.Columns[0])
	}
	if tbl.Columns[1].Name != "total" {
		t.Errorf("expected total copied, got %+v", tbl.Columns[1])
	}
	// Bare LIKE (no INCLUDING) must NOT copy the default — PostgreSQL's own
	// documented default behavior.
	if tbl.Columns[1].Default != nil {
		t.Errorf("bare LIKE should not copy DEFAULT, got %v", *tbl.Columns[1].Default)
	}
	if len(tbl.LikeClauses) != 0 {
		t.Errorf("LikeClauses should be cleared after resolution, got %v", tbl.LikeClauses)
	}
}

// TestCompile_LikeIncludingDefaultsAndConstraints matches the RFC's own
// Section 7.1 example exactly.
func TestCompile_LikeIncludingDefaultsAndConstraints(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE order_items (
    id BIGINT NOT NULL,
    qty INTEGER NOT NULL DEFAULT 1,
    CHECK (qty > 0)
);
TABLE order_items_archive (
    LIKE order_items INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    archived_at TIMESTAMPTZ NOT NULL
);`)

	objects := compile(t, dbDir, []string{f})
	tbl := findTable(t, objects, "order_items_archive")

	if len(tbl.Columns) != 3 {
		t.Fatalf("expected 3 columns (2 copied + 1 explicit), got %d: %v", len(tbl.Columns), tbl.Columns)
	}
	// Position: LIKE-copied columns land where the LIKE clause appeared,
	// before the explicit trailing column.
	if tbl.Columns[0].Name != "id" || tbl.Columns[1].Name != "qty" || tbl.Columns[2].Name != "archived_at" {
		t.Fatalf("unexpected column order: %v", []string{tbl.Columns[0].Name, tbl.Columns[1].Name, tbl.Columns[2].Name})
	}
	if tbl.Columns[1].Default == nil || *tbl.Columns[1].Default != "1" {
		t.Errorf("expected qty's DEFAULT 1 copied (INCLUDING DEFAULTS), got %v", tbl.Columns[1].Default)
	}
	var checkCount int
	for _, c := range tbl.Constraints {
		if c.Type == "CHECK" {
			checkCount++
			if c.Name != "" {
				t.Errorf("copied CHECK constraint should have Name cleared for PG auto-naming, got %q", c.Name)
			}
		}
	}
	if checkCount != 1 {
		t.Errorf("expected the CHECK constraint copied (INCLUDING CONSTRAINTS), got %d CHECK constraints", checkCount)
	}
}

// TestCompile_LikeIncludingIndexesCopiesUniqueConstraint proves the
// constraint-shaped half of INCLUDING INDEXES (PRIMARY KEY/UNIQUE/EXCLUDE)
// works, with the copied constraint's Name cleared for PG's own auto-naming.
func TestCompile_LikeIncludingIndexesCopiesUniqueConstraint(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE accounts (id BIGINT NOT NULL, email TEXT UNIQUE);
TABLE accounts_copy (LIKE accounts INCLUDING INDEXES);`)

	objects := compile(t, dbDir, []string{f})
	tbl := findTable(t, objects, "accounts_copy")

	var uniqueCount int
	for _, c := range tbl.Constraints {
		if c.Type == "UNIQUE" {
			uniqueCount++
			if c.Name != "" {
				t.Errorf("copied UNIQUE constraint should have Name cleared, got %q", c.Name)
			}
		}
	}
	if uniqueCount != 1 {
		t.Errorf("expected the UNIQUE constraint copied (INCLUDING INDEXES), got %d", uniqueCount)
	}
}

// TestCompile_LikeChainResolvesTransitively proves a LIKE-of-a-LIKE ("C
// likes B likes A") resolves correctly regardless of declaration order,
// since a source table may itself have unresolved LikeClauses.
func TestCompile_LikeChainResolvesTransitively(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE c (LIKE b);
TABLE b (LIKE a);
TABLE a (id BIGINT NOT NULL);`)

	objects := compile(t, dbDir, []string{f})
	tbl := findTable(t, objects, "c")
	if len(tbl.Columns) != 1 || tbl.Columns[0].Name != "id" {
		t.Fatalf("expected LIKE chain to transitively resolve to [id], got %v", tbl.Columns)
	}
}

// TestCompile_LikeCircularReferenceErrors proves a genuine LIKE cycle is
// reported as a compile error rather than infinite-looping or silently
// producing an empty table.
func TestCompile_LikeCircularReferenceErrors(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE a (LIKE b);
TABLE b (LIKE a);`)

	_, _, err := compiler.Compile([]string{f}, dbDir, pipeline.Default)
	if err == nil {
		t.Fatal("expected an error for a circular LIKE reference")
	}
}

// TestCompile_LikeUnresolvedSourceErrors proves a LIKE naming a table
// nowhere in the compile unit is a compile error, not a silent empty table
// — DPG is offline-first, so a LIKE target must be declared in source.
func TestCompile_LikeUnresolvedSourceErrors(t *testing.T) {
	dbDir := t.TempDir()
	f := filepath.Join(dbDir, "schemas", "app", "tables.dpg")
	writeDPG(t, f, `TABLE orders_copy (LIKE nonexistent_table);`)

	_, _, err := compiler.Compile([]string{f}, dbDir, pipeline.Default)
	if err == nil {
		t.Fatal("expected an error for a LIKE source table absent from the compile unit")
	}
}
