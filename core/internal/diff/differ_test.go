package diff

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func sha256Sum(s string) [32]byte {
	return sha256.Sum256([]byte(strings.TrimSpace(s)))
}

func opsSQL(ops []pipeline.DiffOp) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.SQL()
	}
	return out
}

func TestDiffEmptyDesiredEmptySnap(t *testing.T) {
	d := New()
	ops, err := d.Diff(nil, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("want 0 ops, got %d", len(ops))
	}
}

func TestDiffCreateSchema(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Schema{Name: "myschema"},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected at least one op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "CREATE SCHEMA") {
		t.Errorf("expected CREATE SCHEMA, got: %s", sql)
	}
	if !strings.Contains(sql, `"myschema"`) {
		t.Errorf("expected quoted schema name, got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

func TestDiffDropSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myschema", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "myschema"},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected drop op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "DROP SCHEMA") {
		t.Errorf("expected DROP SCHEMA, got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Destructive {
		t.Errorf("expected Destructive, got %s", ops[0].Safety())
	}
}

func TestDiffSchemaCommentAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myschema", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "myschema"},
	})
	comment := "reporting tables"
	desired := []pipeline.IRObject{
		&ir.Schema{Name: "myschema", Comment: &comment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON SCHEMA") || !containsSQL(ops, "'reporting tables'") {
		t.Errorf("expected COMMENT ON SCHEMA, got: %v", sqlList(ops))
	}
}

// TestDiffCreateSchemaWithOwnerUsesSetRole is RFC Section 11.5's (audit item #28)
// regression guard: a brand-new object with a declared OWNER must be
// created directly as that role via SET ROLE/RESET ROLE — PostgreSQL
// attributes default-privilege eligibility (Section 11.4) to whoever executed
// CREATE, not to final ownership, so a role reassigned only afterward (via
// ALTER ... OWNER TO, or via CREATE SCHEMA's own AUTHORIZATION clause
// alone) never satisfies a matching DEFAULT PRIVILEGES FOR ROLE block.
func TestDiffCreateSchemaWithOwnerUsesSetRole(t *testing.T) {
	d := New()
	owner := "app_admin"
	desired := []pipeline.IRObject{
		&ir.Schema{Name: "myschema", Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "app_admin";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE SCHEMA, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "AUTHORIZATION") {
		t.Errorf("expected CREATE SCHEMA ... AUTHORIZATION to be kept, got: %v", sqlList(ops))
	}
}

func TestDiffSchemaOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "alice"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myschema", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "myschema", Owner: &oldOwner},
	})
	newOwner := "bob"
	desired := []pipeline.IRObject{
		&ir.Schema{Name: "myschema", Owner: &newOwner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER SCHEMA") || !containsSQL(ops, "OWNER TO") || !containsSQL(ops, `"bob"`) {
		t.Errorf("expected ALTER SCHEMA ... OWNER TO, got: %v", sqlList(ops))
	}
}

func TestDiffSchemaUnchangedIsNoop(t *testing.T) {
	d := New()
	comment := "reporting tables"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myschema", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "myschema", Comment: &comment},
	})
	desired := []pipeline.IRObject{
		&ir.Schema{Name: "myschema", Comment: &comment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged schema, got: %v", sqlList(ops))
	}
}

func TestDiffCreateTable(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "integer"}, NotNull: true},
				{Name: "email", Type: ir.TypeRef{Name: "text"}, NotNull: true},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected ops")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "CREATE TABLE") {
		t.Errorf("expected CREATE TABLE, got: %s", sql)
	}
	if !strings.Contains(sql, `"public"."users"`) {
		t.Errorf("expected qualified table name, got: %s", sql)
	}
	if !strings.Contains(sql, `"id"`) {
		t.Errorf("expected id column, got: %s", sql)
	}
}

// TestDiffCreateTypedTableBareForm proves a fresh typed table (RFC Section
// 7.1's "Form 2") with no declared constraints emits "CREATE TABLE ... OF
// type" with no parenthesized list at all — real PostgreSQL rejects empty
// parens on this form outright.
func TestDiffCreateTypedTableBareForm(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "shipping_address", OfType: ir.TypeRef{Name: "address"}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `CREATE TABLE "public"."shipping_address" OF address;`) {
		t.Errorf("expected bare OF-type CREATE TABLE with no parens, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTypedTableWithConstraint proves a typed table declaring a
// table-level constraint renders it inside the OF-type parenthesized list.
func TestDiffCreateTypedTableWithConstraint(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "shipping_address", OfType: ir.TypeRef{Name: "address"},
			Constraints: []*ir.Constraint{
				{Name: "zip_format", Type: "CHECK", Expr: "CHECK (zip ~ '^[0-9]{5}$')"},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `CREATE TABLE "public"."shipping_address" OF address (`) {
		t.Errorf("expected OF-type CREATE TABLE with a parenthesized constraint list, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `CONSTRAINT "zip_format" CHECK (zip ~ '^[0-9]{5}$')`) {
		t.Errorf("expected the constraint rendered inside it, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTableWithAccessMethod proves a declared USING method
// (Section 7.1) renders on a fresh CREATE TABLE.
func TestDiffCreateTableWithAccessMethod(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events", AccessMethod: "heap2",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "USING heap2") {
		t.Errorf("expected USING heap2, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTableAccessMethodAndPartitionByOrdering guards a real
// pre-existing bug: real PostgreSQL's CREATE TABLE grammar requires
// PARTITION BY to precede USING ("CREATE TABLE t (...) USING heap PARTITION
// BY RANGE (a)" is a confirmed live syntax error; the reverse order is not),
// but createTable previously rendered USING before calling finishCreateTable
// (whose own body renders PARTITION BY), producing invalid SQL whenever a
// table declared both together.
func TestDiffCreateTableAccessMethodAndPartitionByOrdering(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events", AccessMethod: "heap2",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}, {Name: "created_at", Type: ir.TypeRef{Name: "date"}}},
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `PARTITION BY RANGE (created_at) USING heap2`) {
		t.Errorf("expected PARTITION BY before USING, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTableWithStorageParams proves a declared WITH (...) clause
// (Section 7.11) renders on a fresh CREATE TABLE, in source order, after
// PARTITION BY/USING per real PostgreSQL's own fixed clause order.
func TestDiffCreateTableWithStorageParams(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events",
			Columns:       []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}},
			StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}, {Key: "autovacuum_enabled", Value: "false"}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `WITH (fillfactor=70, autovacuum_enabled=false)`) {
		t.Errorf("expected WITH (fillfactor=70, autovacuum_enabled=false), got: %v", sqlList(ops))
	}
}

// TestDiffTableStorageParamsSetAndReset proves a changed WITH (...) clause
// (Section 7.11) against an existing table emits ALTER TABLE ... SET (...)
// for added/changed keys and RESET (...) for removed ones, CAUTION (alters
// storage/maintenance behavior) rather than a recreate.
func TestDiffTableStorageParamsSetAndReset(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "t",
			StorageParams: "fillfactor=70, autovacuum_enabled=false",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "90"}, {Key: "toast_tuple_target", Value: "512"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" SET (fillfactor=90, toast_tuple_target=512);`) {
		t.Errorf("expected SET with changed+new keys, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" RESET (autovacuum_enabled);`) {
		t.Errorf("expected RESET with removed key, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if (strings.Contains(op.SQL(), " SET (") || strings.Contains(op.SQL(), " RESET (")) && op.Safety() != pipeline.Caution {
			t.Errorf("storage-params op safety = %v, want Caution: %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffTableStorageParamsUnchangedIsNoop proves an unchanged WITH (...)
// clause produces no SET/RESET op at all.
func TestDiffTableStorageParamsUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t", StorageParams: "fillfactor=70"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged storage params, got: %v", sqlList(ops))
	}
}

// TestDiffTypedTableSkipsColumnDiffing proves a typed table's Columns are
// never compared: a live-introspected typed table's snapshot genuinely
// carries the type's inherited attributes (introspectColumns has no
// reloftype awareness), while the desired side is always empty (buildTable
// rejects WITH OPTIONS narrowing). Without this guard, every plan against
// an already-applied typed table would propose dropping and re-adding every
// one of its columns.
func TestDiffTypedTableSkipsColumnDiffing(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.shipping_address", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "shipping_address", OfType: "address",
			Columns: []snapshot.SnapColumn{
				{Name: "street", Type: "text"},
				{Name: "city", Type: "text"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "shipping_address", OfType: ir.TypeRef{Name: "address"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged typed table (columns must never be diffed), got: %v", sqlList(ops))
	}
}

// TestDiffTableOfTypeChangedIsCaution proves gaining/switching a typed
// table's OF type_name association (Section 7.11) emits a targeted ALTER,
// not a DROP+CREATE — PostgreSQL validates columns against the new type at
// execution time, so it's CAUTION, not SAFE.
func TestDiffTableOfTypeChangedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", OfType: ir.TypeRef{Name: "address"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" OF address;`) {
		t.Errorf("expected ALTER TABLE ... OF address, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), " OF address") && op.Safety() != pipeline.Caution {
			t.Errorf("OF type_name safety = %v, want Caution", op.Safety())
		}
	}
}

// TestDiffTableOfTypeRemovedEmitsNotOf proves removing a previously-declared
// OF type_name association emits ALTER TABLE ... NOT OF.
func TestDiffTableOfTypeRemovedEmitsNotOf(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t", OfType: "address"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" NOT OF;`) {
		t.Errorf("expected ALTER TABLE ... NOT OF, got: %v", sqlList(ops))
	}
}

// TestDiffTableAccessMethodChangedIsCaution proves a changed USING method
// (Section 7.11's SET ACCESS METHOD) emits a targeted ALTER, CAUTION
// (rewrites the table's storage) rather than a recreate.
func TestDiffTableAccessMethodChangedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t", AccessMethod: "heap"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", AccessMethod: "columnar"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" SET ACCESS METHOD columnar;`) {
		t.Errorf("expected ALTER TABLE ... SET ACCESS METHOD columnar, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "SET ACCESS METHOD") && op.Safety() != pipeline.Caution {
			t.Errorf("SET ACCESS METHOD safety = %v, want Caution", op.Safety())
		}
	}
}

// TestDiffTableAccessMethodRemovedEmitsDefault proves clearing a previously-
// declared USING method emits SET ACCESS METHOD DEFAULT, not a no-op —
// there is no bare "unset" form in real PostgreSQL's grammar.
func TestDiffTableAccessMethodRemovedEmitsDefault(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t", AccessMethod: "columnar"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" SET ACCESS METHOD DEFAULT;`) {
		t.Errorf("expected ALTER TABLE ... SET ACCESS METHOD DEFAULT, got: %v", sqlList(ops))
	}
}

// TestDiffCreateIndexWithOpclassParams is the regression guard for RFC
// audit item #10: createIndex never rendered Collation/OpClass/
// OpClassParams at all before this — a declared opclass with parameters
// (RFC Section 7.7's own worked example) was silently dropped from the
// generated SQL.
func TestDiffCreateIndexWithOpclassParams(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "doc", Type: ir.TypeRef{Name: "tsvector"}}},
			Indexes: []*ir.Index{
				{
					Name: "idx_doc", Method: "gist",
					Columns: []pipeline.IndexColumn{{
						Name:          "doc",
						OpClass:       &pipeline.Identifier{Name: "tsvector_ops"},
						OpClassParams: []pipeline.StorageParam{{Key: "siglen", Value: "32"}},
					}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `"doc" tsvector_ops(siglen = 32)`) {
		t.Errorf("expected opclass with params rendered, got: %v", sqlList(ops))
	}
}

// TestDiffCreateIndexOnOnly proves RFC Section 7.7's ON ONLY prefix renders
// into the generated CREATE INDEX statement.
func TestDiffCreateIndexOnOnly(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "int4"}}},
			Indexes: []*ir.Index{
				{Name: "idx_a", Only: true, Columns: []pipeline.IndexColumn{{Name: "a"}}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `CREATE INDEX "idx_a" ON ONLY "public"."t"`) {
		t.Errorf("expected ON ONLY, got: %v", sqlList(ops))
	}
}

// TestDiffIndexOnlyNotComparedAsDrift proves Only is deliberately excluded
// from diffIndexes' definition comparison (see ir.Index.Only's doc
// comment): PostgreSQL has no catalog column recording it, so treating it
// as ongoing drift would propose a spurious recreate on every plan against
// an already-applied index.
func TestDiffIndexOnlyNotComparedAsDrift(t *testing.T) {
	d := New()
	idx := baseFullIndex()
	idx.Only = false
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{baseFullIndexTable(idx)}); err != nil {
		t.Fatal(err)
	}
	desiredIdx := baseFullIndex()
	desiredIdx.Only = true
	ops, err := d.Diff([]pipeline.IRObject{baseFullIndexTable(desiredIdx)}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops (Only is not diffable drift), got: %v", sqlList(ops))
	}
}

// TestDiffCreateTableWithExcludeConstraint proves a fresh CREATE TABLE
// carrying a named EXCLUDE constraint emits the constraint's real SQL body
// (via createTable's generic table-level rendering path — EXCLUDE isn't in
// isInlineable/the inline-classification switch, so it must fall through to
// "CONSTRAINT name <Expr>" unchanged), not the old "EXCLUDE" placeholder
// that would already fail to apply.
func TestDiffCreateTableWithExcludeConstraint(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "bookings",
			Columns: []*ir.Column{
				{Name: "room", Type: ir.TypeRef{Name: "integer"}},
				{Name: "during", Type: ir.TypeRef{Name: "tsrange"}},
			},
			Constraints: []*ir.Constraint{
				{Name: "no_overlap", Type: "EXCLUDE", Columns: []string{"room", "during"},
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "gist",
						Elements: []ir.ExcludeElement{
							{Column: "room", Operator: "="},
							{Column: "during", Operator: "&&"},
						},
					},
					Expr: `EXCLUDE USING gist ("room" WITH =, "during" WITH &&)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected ops")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, `CONSTRAINT "no_overlap" EXCLUDE USING gist ("room" WITH =, "during" WITH &&)`) {
		t.Errorf("expected the real EXCLUDE body inline in CREATE TABLE, got: %s", sql)
	}
	if strings.Contains(sql, `EXCLUDE;`) || strings.Contains(sql, `EXCLUDE,`) || strings.Contains(sql, `EXCLUDE\n`) {
		t.Errorf("must not regress to the old bare-EXCLUDE placeholder, got: %s", sql)
	}
}

func TestDiffDropTable(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "users",
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected drop op")
	}
	if !strings.Contains(ops[0].SQL(), "DROP TABLE") {
		t.Errorf("expected DROP TABLE, got: %s", ops[0].SQL())
	}
	if ops[0].Safety() != pipeline.Destructive {
		t.Errorf("expected Destructive")
	}
}

func TestDiffAddColumn(t *testing.T) {
	d := New()

	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "users",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "integer"}},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "integer"}},
				{Name: "email", Type: ir.TypeRef{Name: "text"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD COLUMN") {
			addOp = o
			break
		}
	}
	if addOp == nil {
		t.Fatal("expected ADD COLUMN op")
	}
	if !strings.Contains(addOp.SQL(), `"email"`) {
		t.Errorf("expected email column, got: %s", addOp.SQL())
	}
}

// TestCreateTableWithIdentityOptions guards createTable's identity-column
// rendering path (differ.go's inline column-rendering loop): a declared
// START WITH/INCREMENT BY/CACHE/CYCLE previously had no code path at all —
// col.Identity carried only the ALWAYS/BY DEFAULT flag — so CREATE TABLE
// always emitted a bare "GENERATED ALWAYS AS IDENTITY" regardless of what
// options were declared.
func TestCreateTableWithIdentityOptions(t *testing.T) {
	d := New()
	inc, start, cache := int64(10), int64(1000), int64(20)
	cyc := true
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{
					Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true,
					Identity: &ir.Identity{Always: true, IncrementBy: &inc, StartValue: &start, Cache: &cache, Cycle: &cyc},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var createOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE TABLE") {
			createOp = o
		}
	}
	if createOp == nil {
		t.Fatalf("expected a CREATE TABLE op, got ops: %v", opsSQL(ops))
	}
	want := "GENERATED ALWAYS AS IDENTITY (START WITH 1000 INCREMENT BY 10 CACHE 20 CYCLE)"
	if !strings.Contains(createOp.SQL(), want) {
		t.Errorf("expected identity options in CREATE TABLE, want substring %q, got: %s", want, createOp.SQL())
	}
}

// TestDiffAddColumnWithIdentityOptions is TestCreateTableWithIdentityOptions'
// ADD COLUMN counterpart — the differ's separate "new column on an existing
// table" emission path had the identical gap.
func TestDiffAddColumnWithIdentityOptions(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
		},
	})

	inc, max := int64(5), int64(999999)
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true,
					Identity: &ir.Identity{Always: true, IncrementBy: &inc, MaxValue: &max},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD COLUMN") && strings.Contains(o.SQL(), `"id"`) {
			addOp = o
		}
	}
	if addOp == nil {
		t.Fatalf("expected an ADD COLUMN op for id, got ops: %v", opsSQL(ops))
	}
	want := "GENERATED ALWAYS AS IDENTITY (INCREMENT BY 5 MAXVALUE 999999)"
	if !strings.Contains(addOp.SQL(), want) {
		t.Errorf("expected identity options in ADD COLUMN, want substring %q, got: %s", want, addOp.SQL())
	}
}

// TestDiffAddGeneratedColumn guards a real bug found live-testing a demo
// project: createTable's column-rendering loop has always handled
// col.Generated (GENERATED ALWAYS AS (expr) STORED), but the separate
// ALTER TABLE ADD COLUMN branch — used when a generated column is added to
// an already-existing table, not declared with the table from the start —
// never checked col.Generated at all, silently emitting a plain, ungenerated
// column instead. Confirmed live: `dpg plan` for a table that already had an
// `amount` column produced `ADD COLUMN "amount_with_tax" numeric(10,2);`
// with no GENERATED clause whatsoever, for a column declared as
// `GENERATED ALWAYS AS (amount * 1.08) STORED`.
func TestDiffAddGeneratedColumn(t *testing.T) {
	d := New()

	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount * 1.08", Stored: true},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD COLUMN") {
			addOp = o
			break
		}
	}
	if addOp == nil {
		t.Fatal("expected ADD COLUMN op")
	}
	if !strings.Contains(addOp.SQL(), "GENERATED ALWAYS AS (amount * 1.08) STORED") {
		t.Errorf("expected GENERATED ALWAYS AS (...) STORED clause, got: %s", addOp.SQL())
	}
}

// TestDiffAddVirtualGeneratedColumn is TestDiffAddGeneratedColumn's VIRTUAL
// counterpart (PostgreSQL 18+, RFC Section 7.2) — guards the ADD COLUMN
// branch emitting VIRTUAL rather than always hardcoding STORED.
func TestDiffAddVirtualGeneratedColumn(t *testing.T) {
	d := New()

	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount * 1.08", Stored: false},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD COLUMN") {
			addOp = o
			break
		}
	}
	if addOp == nil {
		t.Fatal("expected ADD COLUMN op")
	}
	if !strings.Contains(addOp.SQL(), "GENERATED ALWAYS AS (amount * 1.08) VIRTUAL") {
		t.Errorf("expected GENERATED ALWAYS AS (...) VIRTUAL clause, got: %s", addOp.SQL())
	}
}

// baseGeneratedSnap returns a snapshot with one table ("public.orders")
// containing a plain "amount" column and a generated "amount_with_tax"
// column, matching genCol/genVirtual. Shared fixture for the
// diffGeneratedColumn transition tests below.
func baseGeneratedSnap(expr string, virtual bool) *pipeline.Snapshot {
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "amount", Type: "numeric"},
				{Name: "amount_with_tax", Type: "numeric", Generated: &expr, GeneratedVirtual: virtual},
			},
		},
	})
	return snap
}

// TestDiffGeneratedColumnExpressionChanged guards RFC Section 7.4's "existing
// generated expression text changed, STORED/VIRTUAL kind unchanged" row —
// real PostgreSQL 18's SET EXPRESSION AS (...), confirmed live to leave
// attgenerated (the STORED/VIRTUAL kind) untouched, so this must be CAUTION,
// not a DROP+ADD.
func TestDiffGeneratedColumnExpressionChanged(t *testing.T) {
	d := New()
	snap := baseGeneratedSnap("amount * 1.08", false) // snapshot: STORED, matching desired's Stored:true below

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount * 1.10", Stored: true},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var setExprOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET EXPRESSION") {
			setExprOp = o
		}
		if strings.Contains(o.SQL(), "DROP COLUMN") || strings.Contains(o.SQL(), "ADD COLUMN") {
			t.Errorf("expression-only change should not drop/re-add the column, got: %s", o.SQL())
		}
	}
	if setExprOp == nil {
		t.Fatal("expected an ALTER COLUMN ... SET EXPRESSION op")
	}
	if !strings.Contains(setExprOp.SQL(), "SET EXPRESSION AS (amount * 1.10)") {
		t.Errorf("expected new expression in SET EXPRESSION clause, got: %s", setExprOp.SQL())
	}
	if setExprOp.Safety() != pipeline.Caution {
		t.Errorf("expected Caution, got %s", setExprOp.Safety())
	}
}

// TestDiffGeneratedColumnExpressionUnchangedIsNoop guards against
// diffGeneratedColumn firing spuriously when nothing actually changed —
// whitespace-only differences in the expression text must not trigger a
// SET EXPRESSION op (same normalizeWS treatment other expression-bearing
// diffs already get).
func TestDiffGeneratedColumnExpressionUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := baseGeneratedSnap("amount * 1.08", false) // snapshot: STORED, matching desired's Stored:true below

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount  *  1.08", Stored: true},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "amount_with_tax") {
			t.Errorf("expected no op for amount_with_tax, got: %s", o.SQL())
		}
	}
}

// TestDiffGeneratedColumnIntrospectedOuterParensIsNoop guards a real bug
// found live-testing against a PostgreSQL 18 container: introspection reads
// a generated column's expression via pg_get_expr, which wraps it in an
// extra outer paren pair — a source "amount * 1.08" reads back as
// "(amount * 1.08)" — so without stripping that pair before comparing (the
// same normalizeExprForCompare treatment policy USING/index WHERE/trigger
// WHEN already get), every `dpg plan` immediately after a `dpg dump` or
// live-drift check would spuriously re-run SET EXPRESSION for no real
// change.
func TestDiffGeneratedColumnIntrospectedOuterParensIsNoop(t *testing.T) {
	d := New()
	snap := baseGeneratedSnap("(amount * 1.08)", false) // as pg_get_expr would report it, STORED

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount * 1.08", Stored: true}, // as the source declares it
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "amount_with_tax") {
			t.Errorf("expected no op for amount_with_tax, got: %s", o.SQL())
		}
	}
}

// TestDiffGeneratedColumnKindChanged guards RFC Section 7.4's
// "STORED<->VIRTUAL changed, expression unchanged" row — confirmed live that
// SET EXPRESSION never changes a column's STORED/VIRTUAL kind, so switching
// kind requires DROP COLUMN + ADD COLUMN, hence DESTRUCTIVE.
func TestDiffGeneratedColumnKindChanged(t *testing.T) {
	d := New()
	snap := baseGeneratedSnap("amount * 1.08", true) // snapshot: VIRTUAL

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount * 1.08", Stored: true}, // desired: STORED
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp, addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP COLUMN") && strings.Contains(o.SQL(), "amount_with_tax") {
			dropOp = o
		}
		if strings.Contains(o.SQL(), "ADD COLUMN") && strings.Contains(o.SQL(), "amount_with_tax") {
			addOp = o
		}
		if strings.Contains(o.SQL(), "SET EXPRESSION") {
			t.Errorf("kind change must not use SET EXPRESSION, got: %s", o.SQL())
		}
	}
	if dropOp == nil || addOp == nil {
		t.Fatalf("expected DROP COLUMN + ADD COLUMN ops for amount_with_tax, got ops: %v", opsSQL(ops))
	}
	if dropOp.Safety() != pipeline.Destructive || addOp.Safety() != pipeline.Destructive {
		t.Errorf("expected both ops Destructive, got drop=%s add=%s", dropOp.Safety(), addOp.Safety())
	}
	if !strings.Contains(addOp.SQL(), "GENERATED ALWAYS AS (amount * 1.08) STORED") {
		t.Errorf("expected re-added column to carry the STORED clause, got: %s", addOp.SQL())
	}
}

// TestDiffGeneratedColumnAddedToExistingPlainColumn guards RFC Section 7.4's
// "GENERATED ... AS (expr) added where none existed" row for a column that
// already exists as plain (not the create-table-time or brand-new-ADD-COLUMN
// path already covered by TestDiffAddGeneratedColumn/
// TestDiffAddVirtualGeneratedColumn) — confirmed live that PostgreSQL has no
// ALTER COLUMN ... ADD GENERATED AS (expr) form at all, so this also
// requires DROP+ADD, hence DESTRUCTIVE.
func TestDiffGeneratedColumnAddedToExistingPlainColumn(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "amount", Type: "numeric"},
				{Name: "amount_with_tax", Type: "numeric"}, // plain, not generated
			},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{
					Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"},
					Generated: &ir.Generated{Expr: "amount * 1.08", Stored: true},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp, addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP COLUMN") && strings.Contains(o.SQL(), "amount_with_tax") {
			dropOp = o
		}
		if strings.Contains(o.SQL(), "ADD COLUMN") && strings.Contains(o.SQL(), "amount_with_tax") {
			addOp = o
		}
	}
	if dropOp == nil || addOp == nil {
		t.Fatalf("expected DROP COLUMN + ADD COLUMN ops for amount_with_tax, got ops: %v", opsSQL(ops))
	}
	if dropOp.Safety() != pipeline.Destructive || addOp.Safety() != pipeline.Destructive {
		t.Errorf("expected both ops Destructive, got drop=%s add=%s", dropOp.Safety(), addOp.Safety())
	}
}

// TestDiffGeneratedColumnRemovedStored guards RFC Section 7.4's "GENERATED
// ... AS (expr) removed, column kept" row for a STORED column — DROP
// EXPRESSION IF EXISTS freezes the column's current values with no rewrite,
// hence SAFE.
func TestDiffGeneratedColumnRemovedStored(t *testing.T) {
	d := New()
	snap := baseGeneratedSnap("amount * 1.08", false) // STORED

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"}}, // no longer generated
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropExprOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP EXPRESSION") {
			dropExprOp = o
		}
		if strings.Contains(o.SQL(), "DROP COLUMN") {
			t.Errorf("removing GENERATED from a STORED column should use DROP EXPRESSION, not DROP COLUMN, got: %s", o.SQL())
		}
	}
	if dropExprOp == nil {
		t.Fatal("expected an ALTER COLUMN ... DROP EXPRESSION IF EXISTS op")
	}
	if !strings.Contains(dropExprOp.SQL(), `ALTER COLUMN "amount_with_tax" DROP EXPRESSION IF EXISTS`) {
		t.Errorf("unexpected SQL: %s", dropExprOp.SQL())
	}
	if dropExprOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", dropExprOp.Safety())
	}
}

// TestDiffGeneratedColumnRemovedVirtual guards the VIRTUAL counterpart of
// the row above — confirmed live against a real PostgreSQL 18 server that
// DROP EXPRESSION is flatly rejected for a VIRTUAL generated column ("ALTER
// TABLE / DROP EXPRESSION is not supported for virtual generated columns"),
// so removing generation from a VIRTUAL column must drop and re-add it as
// plain, hence DESTRUCTIVE (unlike the STORED case above).
func TestDiffGeneratedColumnRemovedVirtual(t *testing.T) {
	d := New()
	snap := baseGeneratedSnap("amount * 1.08", true) // VIRTUAL

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
				{Name: "amount_with_tax", Type: ir.TypeRef{Name: "numeric"}}, // no longer generated
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp, addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP EXPRESSION") {
			t.Errorf("removing GENERATED from a VIRTUAL column must not use DROP EXPRESSION (real PostgreSQL rejects it), got: %s", o.SQL())
		}
		if strings.Contains(o.SQL(), "DROP COLUMN") && strings.Contains(o.SQL(), "amount_with_tax") {
			dropOp = o
		}
		if strings.Contains(o.SQL(), "ADD COLUMN") && strings.Contains(o.SQL(), "amount_with_tax") {
			addOp = o
		}
	}
	if dropOp == nil || addOp == nil {
		t.Fatalf("expected DROP COLUMN + ADD COLUMN ops for amount_with_tax, got ops: %v", opsSQL(ops))
	}
	if strings.Contains(addOp.SQL(), "GENERATED") {
		t.Errorf("re-added column should be plain, not generated, got: %s", addOp.SQL())
	}
	if dropOp.Safety() != pipeline.Destructive || addOp.Safety() != pipeline.Destructive {
		t.Errorf("expected both ops Destructive, got drop=%s add=%s", dropOp.Safety(), addOp.Safety())
	}
}

// ── Identity-column diffing (RFC Section 7.4) ──────────────────────────────
//
// diffColumns' "alter existing column" branch previously never read
// col.Identity at all, so every one of the RFC's 4 identity-change rows
// (clause added, ALWAYS<->BY DEFAULT toggle, identity-opts changed, clause
// removed) produced zero DiffOps — source and live database could silently
// and permanently diverge. Same bug class as the generated-column tests
// above (RFC E.21), which these mirror.

// TestDiffIdentityAddedToExistingPlainColumn guards the "clause added where
// none existed" row — real PostgreSQL has a genuine in-place ALTER path for
// this (unlike GENERATED ... AS (expr), which has none), confirmed live:
// ALTER TABLE t ALTER COLUMN c ADD GENERATED ... AS IDENTITY [(opts)],
// CAUTION.
func TestDiffIdentityAddedToExistingPlainColumn(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint", NotNull: true}},
		},
	})

	start := int64(1000)
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{
					Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true,
					Identity: &ir.Identity{Always: true, StartValue: &start},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD GENERATED") {
			addOp = o
		}
	}
	if addOp == nil {
		t.Fatalf("expected an ADD GENERATED ... AS IDENTITY op, got ops: %v", opsSQL(ops))
	}
	if !strings.Contains(addOp.SQL(), `ALTER COLUMN "id" ADD GENERATED ALWAYS AS IDENTITY (START WITH 1000)`) {
		t.Errorf("unexpected SQL: %s", addOp.SQL())
	}
	if addOp.Safety() != pipeline.Caution {
		t.Errorf("expected Caution, got %s", addOp.Safety())
	}
}

// TestDiffIdentityRemoved guards the "clause removed, column kept" row —
// DROP IDENTITY IF EXISTS, SAFE (no rewrite).
func TestDiffIdentityRemoved(t *testing.T) {
	d := New()
	identity := "ALWAYS"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint", NotNull: true, Identity: &identity}},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true}, // Identity dropped from source
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP IDENTITY") {
			dropOp = o
		}
	}
	if dropOp == nil {
		t.Fatalf("expected a DROP IDENTITY op, got ops: %v", opsSQL(ops))
	}
	if !strings.Contains(dropOp.SQL(), `ALTER COLUMN "id" DROP IDENTITY IF EXISTS`) {
		t.Errorf("unexpected SQL: %s", dropOp.SQL())
	}
	if dropOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", dropOp.Safety())
	}
}

// TestDiffIdentityDirectionToggle guards the "ALWAYS<->BY DEFAULT changed,
// options unchanged" row — SET GENERATED {ALWAYS|BY DEFAULT}, SAFE.
func TestDiffIdentityDirectionToggle(t *testing.T) {
	d := New()
	identity := "ALWAYS"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint", NotNull: true, Identity: &identity}},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true, Identity: &ir.Identity{Always: false}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var setOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET GENERATED") {
			setOp = o
		}
		if strings.Contains(o.SQL(), "DROP COLUMN") || strings.Contains(o.SQL(), "ADD COLUMN") {
			t.Errorf("direction toggle should not drop/re-add the column, got: %s", o.SQL())
		}
	}
	if setOp == nil {
		t.Fatalf("expected a SET GENERATED op, got ops: %v", opsSQL(ops))
	}
	if !strings.Contains(setOp.SQL(), `ALTER COLUMN "id" SET GENERATED BY DEFAULT`) {
		t.Errorf("unexpected SQL: %s", setOp.SQL())
	}
	if setOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", setOp.Safety())
	}
}

// TestDiffIdentityOptionsChanged guards the "identity-opts changed" row —
// one targeted SET <option> per changed option, SAFE, distinct from
// ALTER SEQUENCE (no sequence name involved). Changes only a subset
// (IncrementBy, Cache) to confirm unchanged options (StartValue) don't
// spuriously re-emit.
func TestDiffIdentityOptionsChanged(t *testing.T) {
	d := New()
	identity := "ALWAYS"
	snapInc, snapStart, snapCache := int64(1), int64(1000), int64(1)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{{
				Name: "id", Type: "bigint", NotNull: true, Identity: &identity,
				IdentityIncrementBy: &snapInc, IdentityStartValue: &snapStart, IdentityCache: &snapCache,
			}},
		},
	})

	newInc, newCache := int64(5), int64(50)
	start := int64(1000) // unchanged from snapshot
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{
					Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true,
					Identity: &ir.Identity{Always: true, IncrementBy: &newInc, StartValue: &start, Cache: &newCache},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var incOp, cacheOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET INCREMENT BY") {
			incOp = o
		}
		if strings.Contains(o.SQL(), "SET CACHE") {
			cacheOp = o
		}
		if strings.Contains(o.SQL(), "SET START WITH") {
			t.Errorf("unchanged StartValue should not emit a SET START WITH op, got: %s", o.SQL())
		}
	}
	if incOp == nil || !strings.Contains(incOp.SQL(), `ALTER COLUMN "id" SET INCREMENT BY 5`) {
		t.Errorf("expected SET INCREMENT BY 5, got ops: %v", opsSQL(ops))
	}
	if incOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", incOp.Safety())
	}
	if cacheOp == nil || !strings.Contains(cacheOp.SQL(), `ALTER COLUMN "id" SET CACHE 50`) {
		t.Errorf("expected SET CACHE 50, got ops: %v", opsSQL(ops))
	}
}

// TestDiffIdentityUnchangedIsNoop guards against diffIdentityColumn firing
// spuriously when the desired identity spec is byte-for-byte what the
// snapshot already recorded.
func TestDiffIdentityUnchangedIsNoop(t *testing.T) {
	d := New()
	identity := "ALWAYS"
	inc, start, cache := int64(1), int64(1), int64(1)
	cyc := true
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{{
				Name: "id", Type: "bigint", NotNull: true, Identity: &identity,
				IdentityIncrementBy: &inc, IdentityStartValue: &start, IdentityCache: &cache, IdentityCycle: &cyc,
			}},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{
					Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true,
					Identity: &ir.Identity{Always: true, IncrementBy: &inc, StartValue: &start, Cache: &cache, Cycle: &cyc},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "IDENTITY") || strings.Contains(o.SQL(), "SET GENERATED") || strings.Contains(o.SQL(), "SET INCREMENT") || strings.Contains(o.SQL(), "SET START") || strings.Contains(o.SQL(), "SET CACHE") || strings.Contains(o.SQL(), "SET CYCLE") {
			t.Errorf("unchanged identity spec should produce no ops, got: %s", o.SQL())
		}
	}
}

func TestDiffDropColumn(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "users",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "integer"},
				{Name: "old_col", Type: "text"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP COLUMN") {
			dropOp = o
			break
		}
	}
	if dropOp == nil {
		t.Fatal("expected DROP COLUMN op")
	}
	if !strings.Contains(dropOp.SQL(), `"old_col"`) {
		t.Errorf("expected old_col, got: %s", dropOp.SQL())
	}
	if dropOp.Safety() != pipeline.Destructive {
		t.Errorf("expected Destructive")
	}
}

func TestDiffRenameTable(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "users",
		},
	})

	old := "users"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "accounts",
			RenamedFrom: &old,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var renameOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			renameOp = o
			break
		}
	}
	if renameOp == nil {
		t.Fatalf("expected RENAME TO op, got ops: %v", sqlList(ops))
	}
	if !strings.Contains(renameOp.SQL(), `"accounts"`) {
		t.Errorf("expected new name in rename, got: %s", renameOp.SQL())
	}
}

// TestDiffRenameTableCrossSchema exercises a RENAMED FROM directive that also
// moves schemas (RENAMED FROM old_schema.old_name). Before RenamedFromSchema
// existed, renamedFromKey assumed the old name lived in the object's own
// (new) schema, so this case never matched the snapshot entry at all —
// producing a DROP of the orphaned old table plus a CREATE of a "new" one,
// instead of recognizing it as the same table. Per RFC Section 7.6, a
// cross-schema move now emits SET SCHEMA first (old_schema.old_name -> new
// schema), then RENAME TO (new_schema.old_name -> new_name).
func TestDiffRenameTableCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "old_schema",
			Name:   "users",
		},
	})

	old := "users"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:            "new_schema",
			Name:              "accounts",
			RenamedFrom:       &old,
			RenamedFromSchema: &oldSchema,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TABLE") || containsSQL(ops, "CREATE TABLE") {
		t.Fatalf("cross-schema rename should match the existing table, not DROP+CREATE, got: %v", sqlList(ops))
	}
	// SET SCHEMA moves the table using its actual current identity
	// (old_schema.users), then RENAME TO operates on it in its new schema.
	if !containsSQL(ops, `ALTER TABLE "old_schema"."users" SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the table's actual (old) schema, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "new_schema"."users" RENAME TO "accounts";`) {
		t.Errorf("expected RENAME TO after the schema move, got: %v", sqlList(ops))
	}
}

// TestDiffRenameViewCrossSchema is diffTable's TestDiffRenameTableCrossSchema
// counterpart for View, also confirming a materialized view emits
// ALTER MATERIALIZED VIEW (not ALTER VIEW, which real PostgreSQL rejects
// against a matview relkind) for both statements.
func TestDiffRenameViewCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.orders_summary", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "old_schema", Name: "orders_summary", Query: "SELECT 1",
		},
	})
	old := "orders_summary"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "new_schema", Name: "order_summary", Materialized: true, Query: "SELECT 1",
			RenamedFrom: &old, RenamedFromSchema: &oldSchema,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP MATERIALIZED VIEW") || containsSQL(ops, "CREATE MATERIALIZED VIEW") {
		t.Fatalf("cross-schema rename should match the existing matview, not DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER MATERIALIZED VIEW "old_schema"."orders_summary" SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the matview's actual (old) schema, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER MATERIALIZED VIEW "new_schema"."orders_summary" RENAME TO "order_summary";`) {
		t.Errorf("expected RENAME TO after the schema move, got: %v", sqlList(ops))
	}
}

func TestDiffNoChanges(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "users",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "integer", NotNull: true}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}, NotNull: true}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for identical state, got: %v", sqlList(ops))
	}
}

func TestDiffCreateIndex(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "btree", Columns: []pipeline.IndexColumn{{Name: "email"}}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var idxOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE") && strings.Contains(o.SQL(), "INDEX") {
			idxOp = o
			break
		}
	}
	if idxOp == nil {
		t.Fatalf("expected CREATE INDEX op, got: %v", sqlList(ops))
	}
	// RFC Section 17.2: an index created with its own new table is SAFE.
	if idxOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe for a new table's index, got %v", idxOp.Safety())
	}

	// Added to an existing table it blocks writes while it builds: CAUTION.
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{
		Schema: "public", Name: "users", Columns: []snapshot.SnapColumn{{Name: "email", Type: "text"}},
	}})
	ops, err = d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE INDEX") && o.Safety() != pipeline.Caution {
			t.Errorf("expected Caution for an index on an existing table, got %v: %s", o.Safety(), o.SQL())
		}
	}
}

// TestDiffConcurrentIndexOnNewTableIsSuppressed guards a real correctness
// rule, not just a preference: PostgreSQL rejects CREATE INDEX CONCURRENTLY
// inside a transaction block, and an index on a brand-new table is always
// emitted in the SAME transactional migration as its CREATE TABLE — so an
// explicit CONCURRENTLY on a new table's index must be silently suppressed,
// never honored. See createIndex's doc comment and createTable's index loop.
func TestDiffConcurrentIndexOnNewTableIsSuppressed(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "btree", Concurrently: true,
					Columns: []pipeline.IndexColumn{{Name: "email"}}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CONCURRENTLY") {
			t.Fatalf("new-table index must never be CONCURRENTLY (same-transaction PG restriction), got: %v", sqlList(ops))
		}
	}
}

// TestDiffConcurrentIndexOnExistingTableExplicit guards the case CONCURRENTLY
// actually applies: an EXISTING table (already in the snapshot) gaining a new
// index with an explicit CONCURRENTLY in source. There is no project-wide
// default — CONCURRENTLY only ever fires when the source writes it, exactly
// like real PostgreSQL's own bare CONCURRENTLY keyword.
func TestDiffConcurrentIndexOnExistingTableExplicit(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Columns: []snapshot.SnapColumn{{Name: "email", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "btree", Concurrently: true,
					Columns: []pipeline.IndexColumn{{Name: "email"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var idxOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CONCURRENTLY") {
			idxOp = o
			break
		}
	}
	if idxOp == nil {
		t.Fatalf("expected CONCURRENTLY index op, got: %v", sqlList(ops))
	}
	if idxOp.Safety() != pipeline.Caution {
		t.Errorf("expected Caution safety for concurrent index")
	}
	if idxOp.Mode() == pipeline.ModeTransactional {
		t.Errorf("concurrent index must not be transactional")
	}
}

// TestDiffIndexWithoutConcurrentlyIsNeverConcurrent guards the absence of any
// implicit default: a new index on an existing table with no explicit
// CONCURRENTLY in source must never emit CONCURRENTLY — there is nothing
// (no project config, no other signal) that can turn it on.
func TestDiffIndexWithoutConcurrentlyIsNeverConcurrent(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Columns: []snapshot.SnapColumn{{Name: "email", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "btree",
					Columns: []pipeline.IndexColumn{{Name: "email"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(sqlList(ops), "\n"), "CONCURRENTLY") {
		t.Errorf("expected no CONCURRENTLY without an explicit source keyword, got: %v", sqlList(ops))
	}
}

func TestDiffEnumAddValue(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema:  "public",
			Name:    "status",
			Variant: "ENUM",
			Values:  []string{"active", "inactive"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema:     "public",
			Name:       "status",
			Variant:    "ENUM",
			EnumValues: []string{"active", "inactive", "pending"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected ADD VALUE op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "ADD VALUE") {
		t.Errorf("expected ADD VALUE, got: %s", sql)
	}
	if !strings.Contains(sql, "'pending'") {
		t.Errorf("expected pending value, got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe safety for ADD VALUE (RFC Section 1.4's PG 14 floor is past the PG 12 transaction-block restriction)")
	}
	if ops[0].Mode() != pipeline.ModeTransactional {
		t.Errorf("expected ADD VALUE to run transactionally")
	}
}

// TestDiffEnumAddValueBefore guards RFC audit item #18: positional ADD
// VALUE control is inferred purely from where a new value sits in the
// desired list relative to already-existing ones, not a separate
// directive. Inserting at the front of the list (no earlier anchor) must
// use BEFORE the nearest later already-existing value.
func TestDiffEnumAddValueBefore(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "inactive"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"new", "active", "inactive"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TYPE "public"."status" ADD VALUE 'new' BEFORE 'active';`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffEnumAddValueMiddleAndMultiple guards inserting in the middle of
// the list and inserting more than one new value in the same migration —
// each subsequent new value must chain AFTER the one added just before it.
func TestDiffEnumAddValueMiddleAndMultiple(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "shipped"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"active", "confirmed", "packed", "shipped"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TYPE "public"."status" ADD VALUE 'confirmed' AFTER 'active';`) {
		t.Errorf("expected confirmed AFTER active, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "public"."status" ADD VALUE 'packed' AFTER 'confirmed';`) {
		t.Errorf("expected packed AFTER confirmed (chained onto the value just added), got: %v", sqlList(ops))
	}
}

// TestDiffEnumValueRenameEmitsAlterRenameValue guards RFC Section 5.1.1's
// RENAME VALUE directive (audit item #19): before this, ir.Type had no such
// field at all, so an enum value rename was unexpressable except as a
// destructive MIGRATE REMOVE-and-add pair.
func TestDiffEnumValueRenameEmitsAlterRenameValue(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "pending"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues:       []string{"active", "awaiting"},
			EnumValueRenames: []pipeline.EnumValueRenameDir{{From: "pending", To: "awaiting"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TYPE "public"."status" RENAME VALUE 'pending' TO 'awaiting';`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "ADD VALUE") || containsSQL(ops, "CREATE TYPE") {
		t.Errorf("a pure rename must not also be treated as an add or trigger MIGRATE REMOVE, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "RENAME VALUE") && op.Safety() != pipeline.Safe {
			t.Errorf("RENAME VALUE safety = %v, want Safe: %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffEnumValueRenamePostApplyNoop proves a RENAME VALUE directive still
// present in source after a successful rename is a no-op, mirroring the
// same three-state resolution Index/Constraint/Policy's identical
// RENAMED-FROM-style directives already use.
func TestDiffEnumValueRenamePostApplyNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "awaiting"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues:       []string{"active", "awaiting"},
			EnumValueRenames: []pipeline.EnumValueRenameDir{{From: "pending", To: "awaiting"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for a post-apply stale RENAME VALUE, got: %v", sqlList(ops))
	}
}

// TestDiffEnumValueRenameStaleErrors proves a RENAME VALUE directive whose
// old value matches neither the current nor a prior snapshot value errors
// clearly instead of silently doing nothing or misfiring.
func TestDiffEnumValueRenameStaleErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues:       []string{"active", "new"},
			EnumValueRenames: []pipeline.EnumValueRenameDir{{From: "nonexistent", To: "new"}},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected an error for a RENAME VALUE matching neither value in the snapshot")
	}
}

// TestDiffEnumValueRenameWithSeparateRemoval guards the combined case: a
// RENAME VALUE alongside a genuinely separate value removal (covered by
// MIGRATE REMOVE) in the same migration. The renamed value must not be
// treated as removed by diffEnumRemove's own independent computation, and
// the rename must run before the shadow-type dance so the removal
// verification's col::text check reflects the already-renamed label.
func TestDiffEnumValueRenameWithSeparateRemoval(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "pending", "cancelled"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues:       []string{"active", "awaiting"},
			EnumValueRenames: []pipeline.EnumValueRenameDir{{From: "pending", To: "awaiting"}},
			MigrateRemove:    &pipeline.MigrateRemoveBlock{SQL: pipeline.RawExpr{Text: "UPDATE t SET s = 'active' WHERE s = 'cancelled';"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	renameIdx, shadowIdx := -1, -1
	for i, op := range ops {
		sql := op.SQL()
		if strings.Contains(sql, "RENAME VALUE 'pending' TO 'awaiting'") {
			renameIdx = i
		}
		if strings.Contains(sql, "CREATE TYPE") && strings.Contains(sql, "__dpg_new") {
			shadowIdx = i
		}
		if strings.Contains(sql, "'pending'") && strings.Contains(sql, "still carry a removed") {
			t.Errorf("renamed value 'pending' must not be checked for as a removed value: %s", sql)
		}
	}
	if renameIdx == -1 {
		t.Fatalf("expected a RENAME VALUE op, got: %v", sqlList(ops))
	}
	if shadowIdx == -1 {
		t.Fatalf("expected the MIGRATE REMOVE shadow-type CREATE TYPE op, got: %v", sqlList(ops))
	}
	if renameIdx > shadowIdx {
		t.Errorf("RENAME VALUE must run before the shadow-type dance, got order: %v", sqlList(ops))
	}
}

func TestDiffViewChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.active_users", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "active_users",
			Query:  "SELECT * FROM users WHERE active = true",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "active_users",
			Query:  "SELECT id, email FROM users WHERE active = true",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected CREATE OR REPLACE VIEW op")
	}
	if !strings.Contains(ops[0].SQL(), "CREATE OR REPLACE VIEW") {
		t.Errorf("expected CREATE OR REPLACE VIEW, got: %s", ops[0].SQL())
	}
}

// TestCreateMaterializedViewTablespaceAndStorageParams guards createView's
// rendering of RFC Section 8.2's own worked example (WITH (fillfactor = 90)
// TABLESPACE analytics_space) — previously never read at all, so a declared
// TABLESPACE/WITH clause silently vanished from the emitted CREATE
// MATERIALIZED VIEW.
func TestCreateMaterializedViewTablespaceAndStorageParams(t *testing.T) {
	d := New()
	ts := "analytics_space"
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public", Name: "product_stats", Materialized: true,
			Query:         "SELECT product_id FROM order_items",
			Tablespace:    &ts,
			StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "90"}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var createOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE MATERIALIZED VIEW") {
			createOp = o
		}
	}
	if createOp == nil {
		t.Fatalf("expected a CREATE MATERIALIZED VIEW op, got: %v", sqlList(ops))
	}
	if !strings.Contains(createOp.SQL(), "WITH (fillfactor=90)") {
		t.Errorf("expected WITH (fillfactor=90), got: %s", createOp.SQL())
	}
	if !strings.Contains(createOp.SQL(), `TABLESPACE "analytics_space"`) {
		t.Errorf("expected TABLESPACE clause, got: %s", createOp.SQL())
	}
	// TABLESPACE must precede AS (real PostgreSQL grammar order).
	if strings.Index(createOp.SQL(), "TABLESPACE") > strings.Index(createOp.SQL(), " AS ") {
		t.Errorf("expected TABLESPACE before AS, got: %s", createOp.SQL())
	}
}

// TestDiffMaterializedViewTablespaceChanged guards the targeted, SAFE
// ALTER MATERIALIZED VIEW ... SET TABLESPACE path — confirmed live against
// PostgreSQL 17 — instead of a drop+recreate.
func TestDiffMaterializedViewTablespaceChanged(t *testing.T) {
	d := New()
	oldTS := "ts1"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public", Name: "mv", Query: "SELECT 1", Recursive: false,
			Tablespace: &oldTS,
		},
	})
	newTS := "ts2"
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "mv", Materialized: true, Query: "SELECT 1", Tablespace: &newTS},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP MATERIALIZED VIEW") {
			t.Errorf("tablespace-only change must not drop/recreate, got: %s", o.SQL())
		}
	}
	if len(ops) != 1 || !strings.Contains(ops[0].SQL(), `ALTER MATERIALIZED VIEW "public"."mv" SET TABLESPACE "ts2";`) {
		t.Fatalf("expected SET TABLESPACE op, got: %v", sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

// TestDiffMaterializedViewStorageParamsChanged guards the targeted, SAFE
// ALTER MATERIALIZED VIEW ... SET (...)/RESET (...) path.
func TestDiffMaterializedViewStorageParamsChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public", Name: "mv", Query: "SELECT 1",
			StorageParams: "fillfactor=90",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public", Name: "mv", Materialized: true, Query: "SELECT 1",
			StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || !strings.Contains(ops[0].SQL(), `ALTER MATERIALIZED VIEW "public"."mv" SET (fillfactor=70)`) {
		t.Fatalf("expected SET (fillfactor=70), got: %v", sqlList(ops))
	}
}

// TestDiffMaterializedViewTablespaceUnchangedIsNoop guards against spurious
// ops when Tablespace/StorageParams match the snapshot exactly.
func TestDiffMaterializedViewTablespaceUnchangedIsNoop(t *testing.T) {
	d := New()
	ts := "ts1"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public", Name: "mv", Query: "SELECT 1",
			Tablespace: &ts, StorageParams: "fillfactor=90",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public", Name: "mv", Materialized: true, Query: "SELECT 1",
			Tablespace: &ts, StorageParams: []pipeline.StorageParam{{Key: "fillfactor", Value: "90"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no-op, got: %v", sqlList(ops))
	}
}

// TestDiffViewUnaliasedColumnRenameDropsAndRecreates is the regression
// guard for RFC audit item #29: DPG emitted CREATE OR REPLACE VIEW
// unconditionally on any query-text change, but real PostgreSQL rejects a
// replacement that changes an existing output column's implicit name
// (SQLSTATE 42P16) — the exact shape of an unaliased "SELECT col FROM t"
// whose source column got renamed elsewhere in the same migration. RFC
// Section 8.1 requires detecting this and falling back to DROP VIEW CASCADE;
// CREATE VIEW (DESTRUCTIVE) instead of a migration-aborting CREATE OR
// REPLACE.
func TestDiffViewUnaliasedColumnRenameDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v",
			Query:  "SELECT old_name FROM t",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "v",
			Query:  "SELECT new_name FROM t",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP VIEW IF EXISTS "public"."v" CASCADE;`) {
		t.Errorf("expected DROP VIEW ... CASCADE, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "CREATE OR REPLACE VIEW") {
		t.Errorf("must not use CREATE OR REPLACE VIEW when the output column name changed, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `CREATE VIEW "public"."v" AS SELECT new_name FROM t;`) {
		t.Errorf("expected recreated CREATE VIEW, got: %v", sqlList(ops))
	}
	var dropOp pipeline.DiffOp
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP VIEW") {
			dropOp = op
		}
	}
	if dropOp == nil || dropOp.Safety() != pipeline.Destructive {
		t.Errorf("DROP VIEW safety = %v, want Destructive", dropOp)
	}
}

// TestDiffViewSameOutputColumnsStaysReplace proves the companion case: a
// query-text change that doesn't touch the output column list (aliased
// selection, added WHERE clause, ...) must still use the SAFE CREATE OR
// REPLACE VIEW path — the new detection must not over-trigger DESTRUCTIVE
// for ordinary, harmless view edits.
func TestDiffViewSameOutputColumnsStaysReplace(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v",
			Query:  "SELECT id, name FROM t",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "v",
			Query:  "SELECT id, name FROM t WHERE active = true",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP VIEW") {
		t.Errorf("must not DROP VIEW when the output column list is unchanged, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE OR REPLACE VIEW") {
		t.Errorf("expected CREATE OR REPLACE VIEW, got: %v", sqlList(ops))
	}
}

// TestDiffViewAliasedColumnRenameStaysReplace proves an aliased SELECT
// (RFC Section 7.6's mechanism doesn't even apply here — the alias itself, not the
// underlying source column, determines the output name) never triggers the
// new DESTRUCTIVE path merely because the source column changed underneath
// an unrelated, stable alias.
func TestDiffViewAliasedColumnRenameStaysReplace(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v",
			Query:  "SELECT old_name AS label FROM t",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "v",
			Query:  "SELECT new_name AS label FROM t",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP VIEW") {
		t.Errorf("must not DROP VIEW when the alias keeps the output column name stable, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE OR REPLACE VIEW") {
		t.Errorf("expected CREATE OR REPLACE VIEW, got: %v", sqlList(ops))
	}
}

func TestDiffFunctionChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_func()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema:     "public",
			Name:       "my_func",
			Args:       "",
			ReturnType: "void",
			Language:   "plpgsql",
			Volatility: "VOLATILE",
			BodyHash:   "oldhash",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:     "public",
			Name:       "my_func",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "newhash",
			Attrs: ir.FuncAttrs{
				Language:   "plpgsql",
				Volatility: "VOLATILE",
				Body:       "BEGIN END;",
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected CREATE OR REPLACE FUNCTION op")
	}
	if !strings.Contains(ops[0].SQL(), "CREATE OR REPLACE FUNCTION") {
		t.Errorf("expected CREATE OR REPLACE FUNCTION, got: %s", ops[0].SQL())
	}
}

// TestDiffCreateFunctionEmitsParallelCostRows proves a fresh CREATE FUNCTION
// emits explicit PARALLEL/COST/ROWS clauses when the desired side declares
// them, in PostgreSQL's own documented attribute ordering (LANGUAGE ->
// volatility -> STRICT -> SECURITY DEFINER -> PARALLEL -> COST -> ROWS -> AS).
func TestDiffCreateFunctionEmitsParallelCostRows(t *testing.T) {
	d := New()
	cost := 500.0
	rows := 50.0
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs: ir.FuncAttrs{
				Language: "sql", Volatility: "STABLE", Parallel: "SAFE",
				Cost: &cost, Rows: &rows, Body: "SELECT 1",
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected a CREATE FUNCTION op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "PARALLEL SAFE") {
		t.Errorf("expected PARALLEL SAFE, got: %s", sql)
	}
	if !strings.Contains(sql, "COST 500") {
		t.Errorf("expected COST 500, got: %s", sql)
	}
	if !strings.Contains(sql, "ROWS 50") {
		t.Errorf("expected ROWS 50, got: %s", sql)
	}
}

// TestDiffCreateFunctionOmitsDefaultParallelCostRows proves the common case
// (no explicit PARALLEL/COST/ROWS in source) renders none of those clauses
// — PARALLEL UNSAFE is PostgreSQL's own default and would be pure noise if
// always emitted, and unset Cost/Rows have nothing meaningful to render.
func TestDiffCreateFunctionOmitsDefaultParallelCostRows(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1"},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	sql := ops[0].SQL()
	if strings.Contains(sql, "PARALLEL") || strings.Contains(sql, "COST") || strings.Contains(sql, "ROWS") {
		t.Errorf("expected no PARALLEL/COST/ROWS clause for the default case, got: %s", sql)
	}
}

// TestDiffFunctionParallelChanged proves an explicit PARALLEL change alone
// (no body/language/volatility change) still triggers CREATE OR REPLACE, when
// the snapshot already carries a real (non-empty) Parallel value — the common
// case for any snapshot written by this feature's own introspection/apply
// path. See TestDiffFunctionLegacySnapshotParallelIsNoop for the other case
// (an empty snapshot value from a pre-upgrade snapshot.json), which must NOT
// be treated as a genuine difference.
func TestDiffFunctionParallelChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "SAFE", Body: "SELECT 1"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "CREATE OR REPLACE FUNCTION") {
		t.Errorf("expected CREATE OR REPLACE FUNCTION for the PARALLEL change, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionLeakproofChanged guards RFC audit item #25: LEAKPROOF was
// not part of FuncAttrs at all, so a LEAKPROOF-only source change against an
// otherwise-identical snapshot would have produced zero diff ops.
func TestDiffFunctionLeakproofChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Leakproof: true, Body: "SELECT 1"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "LEAKPROOF") {
		t.Errorf("expected a recreate with LEAKPROOF, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionConfigChanged guards the Phase 3 launch-readiness audit
// fix: a Section 9.2 SET-clause-only change (no other attribute touched)
// previously produced zero diff ops at all, since FuncAttrs had no Config
// field to compare in the first place.
func TestDiffFunctionConfigChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	val := "public"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			Attrs: ir.FuncAttrs{
				Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1",
				Config: []ir.FuncConfig{{Param: "search_path", Value: &val}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "SET search_path") {
		t.Errorf("expected a recreate with SET search_path, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionConfigUnchangedNoOp proves a matching Config on both
// sides (accounting for the quoted-vs-bare-value normalization a live
// introspected snapshot always uses) produces zero ops — the idempotency
// guarantee every other FuncAttrs field already has.
func TestDiffFunctionConfigUnchangedNoOp(t *testing.T) {
	d := New()
	snapVal := "public"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
			Config: []snapshot.SnapFuncConfig{{Param: "search_path", Value: &snapVal}},
		},
	})
	desiredVal := "'public'"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			Attrs: ir.FuncAttrs{
				Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1",
				Config: []ir.FuncConfig{{Param: "search_path", Value: &desiredVal}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a quoted-vs-bare-equivalent Config, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionTransformsChanged guards RFC audit item #26: Transforms
// was not compared at all, so a TRANSFORM-only source change would have
// produced zero diff ops.
func TestDiffFunctionTransformsChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f(hstore)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", Args: "hstore", ReturnType: "integer",
			Language: "plpython3u", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "hstore"}}},
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			Attrs: ir.FuncAttrs{
				Language: "plpython3u", Volatility: "VOLATILE", Parallel: "UNSAFE",
				Transforms: []ir.TypeRef{{Name: "hstore"}}, Body: "return 1",
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "TRANSFORM FOR TYPE hstore") {
		t.Errorf("expected a recreate with TRANSFORM FOR TYPE hstore, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionObjFileChanged guards RFC audit item #27: a LANGUAGE C
// function's shared-object path/symbol were not compared at all.
func TestDiffFunctionObjFileChanged(t *testing.T) {
	d := New()
	oldObjFile, oldSymbol := "$libdir/old", "old_impl"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", Args: "integer", ReturnType: "integer",
			Language: "c", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "",
			ObjFile: &oldObjFile, LinkSymbol: &oldSymbol,
		},
	})
	newObjFile, newSymbol := "$libdir/new", "new_impl"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs: ir.FuncAttrs{
				Language: "c", Volatility: "VOLATILE", Parallel: "UNSAFE",
				ObjFile: &newObjFile, LinkSymbol: &newSymbol,
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "'$libdir/new'") || !strings.Contains(ops[0].SQL(), "'new_impl'") {
		t.Errorf("expected a recreate with the new obj_file/link_symbol, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionObjFileUnchangedIsNoop is the no-op counterpart of
// TestDiffFunctionObjFileChanged.
func TestDiffFunctionObjFileUnchangedIsNoop(t *testing.T) {
	d := New()
	objFile, symbol := "$libdir/pgcrypto", "pg_digest"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", Args: "integer", ReturnType: "integer",
			Language: "c", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "",
			ObjFile: &objFile, LinkSymbol: &symbol,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs: ir.FuncAttrs{
				Language: "c", Volatility: "VOLATILE", Parallel: "UNSAFE",
				ObjFile: &objFile, LinkSymbol: &symbol,
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged obj_file/link_symbol, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionAtomicBodyChanged guards RFC audit item #28's diffing
// half: AtomicBody-only drift (e.g. self-healing a function that was
// created via a plain dollar-quoted body but is now declared BEGIN ATOMIC)
// must trigger a recreate even when BodyHash alone doesn't move — a
// dollar-quoted "SELECT x" and a BEGIN ATOMIC "SELECT x; END" body
// canonicalise to the identical hash (same underlying SQL), so without
// this the declared form would never actually be applied.
func TestDiffFunctionAtomicBodyChanged(t *testing.T) {
	d := New()
	hash := ir.HashFunctionBody("sql", "SELECT x", "")
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", Args: "integer", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: hash,
			AtomicBody: false,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   hash,
			Attrs: ir.FuncAttrs{
				Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE",
				AtomicBody: true, Body: "SELECT x",
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "BEGIN ATOMIC") {
		t.Errorf("expected a recreate rendering BEGIN ATOMIC, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionStrictSecurityDefChanged guards a gap found while auditing
// item #25-#28's neighboring attributes: diffFunction's recreate condition
// never compared Strict/SecurityDef at all, so adding/removing STRICT or
// SECURITY DEFINER alone (no body/other-attribute change) was silently
// invisible to plan forever, even though CREATE-time rendering
// (RenderCreateFunctionSQL) already writes both correctly for a brand-new
// function.
func TestDiffFunctionStrictSecurityDefChanged(t *testing.T) {
	hash := ir.HashFunctionBody("sql", "SELECT x", "")
	newDesired := func(strict, secDef bool) []pipeline.IRObject {
		return []pipeline.IRObject{
			&ir.Function{
				Schema: "public", Name: "f",
				Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
				ReturnType: ir.TypeRef{Name: "integer"},
				BodyHash:   hash,
				Attrs: ir.FuncAttrs{
					Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE",
					Body: "SELECT x", Strict: strict, SecurityDef: secDef,
				},
			},
		}
	}
	newSnap := func(strict, secDef bool) *pipeline.Snapshot {
		snap := &pipeline.Snapshot{}
		_ = snap.SetObject("public.f(integer)", &snapshot.SnapObject{
			Kind: "function",
			Function: &snapshot.SnapFunction{
				Schema: "public", Name: "f", Args: "integer", ReturnType: "integer",
				Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: hash,
				Strict: strict, SecurityDef: secDef,
			},
		})
		return snap
	}

	t.Run("strict added", func(t *testing.T) {
		d := New()
		ops, err := d.Diff(newDesired(true, false), newSnap(false, false))
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "STRICT") {
			t.Errorf("expected a recreate rendering STRICT, got: %v", sqlList(ops))
		}
	})

	t.Run("security definer added", func(t *testing.T) {
		d := New()
		ops, err := d.Diff(newDesired(false, true), newSnap(false, false))
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "SECURITY DEFINER") {
			t.Errorf("expected a recreate rendering SECURITY DEFINER, got: %v", sqlList(ops))
		}
	})

	t.Run("unchanged is noop", func(t *testing.T) {
		d := New()
		ops, err := d.Diff(newDesired(true, true), newSnap(true, true))
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) != 0 {
			t.Errorf("expected no ops, got: %v", sqlList(ops))
		}
	})
}

// TestDiffProcedureTransformsChanged guards RFC audit item #26 for
// Procedure specifically: diffProcedure previously compared BodyHash only,
// so Transforms drift (which doesn't feed BodyHash at all) went completely
// undiffed.
func TestDiffProcedureTransformsChanged(t *testing.T) {
	d := New()
	body := "pass"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.p(hstore)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "p", Args: "hstore", BodyHash: hash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "public", Name: "p",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "hstore"}}},
			Attrs:    ir.FuncAttrs{Language: "plpython3u", Transforms: []ir.TypeRef{{Name: "hstore"}}, Body: body},
			BodyHash: hash,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "TRANSFORM FOR TYPE hstore") {
		t.Errorf("expected a recreate with TRANSFORM FOR TYPE hstore, got: %v", sqlList(ops))
	}
}

// TestDiffProcedureSecurityDefinerAndConfigChanged guards the Phase 3
// launch-readiness audit finding: Procedure's SecurityDef and Config
// (Section 9.2/9.3 — RFC's own Section 9.3 worked example uses SECURITY
// DEFINER) were never compared here at all, so declaring either on an
// existing procedure produced zero diff ops — silent drift, not just a
// missing feature.
func TestDiffProcedureSecurityDefinerAndConfigChanged(t *testing.T) {
	d := New()
	body := "pass"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.p()", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "p", Args: "", BodyHash: hash,
		},
	})
	val := "public"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "public", Name: "p",
			Attrs: ir.FuncAttrs{
				Language: "plpgsql", SecurityDef: true, Body: body,
				Config: []ir.FuncConfig{{Param: "search_path", Value: &val}},
			},
			BodyHash: hash,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected a recreate op, got none")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "SECURITY DEFINER") {
		t.Errorf("expected SECURITY DEFINER in recreate SQL, got: %s", sql)
	}
	if !strings.Contains(sql, "SET search_path") {
		t.Errorf("expected SET search_path in recreate SQL, got: %s", sql)
	}
}

// TestDiffFunctionExplicitCostChanged proves an explicit COST change
// against a snapshot with a different value triggers CREATE OR REPLACE.
func TestDiffFunctionExplicitCostChanged(t *testing.T) {
	d := New()
	oldCost := 100.0
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Cost: &oldCost, BodyHash: "h",
		},
	})
	newCost := 500.0
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Cost: &newCost, Body: "SELECT 1"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "COST 500") {
		t.Errorf("expected a recreate with COST 500, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionUnspecifiedCostRowsIsNoop is the core regression guard
// for the suppress-when-unspecified rule: a snapshot carrying a concrete
// Cost/Rows (e.g. from a prior live introspection, which always records
// PostgreSQL's own real catalog value) must NOT be treated as drift when
// the desired side simply never mentions COST/ROWS at all — the same
// "only act when the desired side actually declares it" rule already
// established for column STORAGE.
func TestDiffFunctionUnspecifiedCostRowsIsNoop(t *testing.T) {
	d := New()
	introspectedCost := 250.0
	introspectedRows := 75.0
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE",
			Cost: &introspectedCost, Rows: &introspectedRows, BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			// Cost/Rows intentionally nil — source never mentions COST/ROWS.
			Attrs: ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unspecified COST/ROWS must not flag drift against a live-introspected default), got: %v", sqlList(ops))
	}
}

// TestDiffFunctionLegacySnapshotParallelIsNoop guards a real asymmetry an
// independent verification pass caught: unlike Cost/Rows (a *float64, nil on
// the DESIRED side means "unspecified"), Parallel is a plain string that the
// IR builder ALWAYS defaults to a concrete "UNSAFE" on the desired side even
// when source never mentions PARALLEL. A snapshot.json written before this
// field existed has no "parallel" JSON key at all, so snap.Parallel comes
// back as the Go zero value "" — comparing that bare-string against the
// desired side's always-concrete "UNSAFE" would spuriously flag every
// function in every pre-existing project as changed on the very first
// plan/apply after upgrading, which is exactly the primary offline workflow
// this project's CLAUDE.md calls out as must-work-without-a-DB. Confirmed
// this reproduces before the fix (parallelChanged treating snap=="" as
// "unknown, don't diff yet") and disappears after.
func TestDiffFunctionLegacySnapshotParallelIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			// Parallel deliberately omitted, simulating a pre-upgrade
			// snapshot.json with no "parallel" key at all.
			Schema: "public", Name: "f", ReturnType: "integer",
			Language: "sql", Volatility: "VOLATILE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer"},
			BodyHash:   "h",
			// source never mentions PARALLEL -> builder defaults to "UNSAFE"
			Attrs: ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops against a legacy (pre-feature) snapshot with no recorded Parallel value, got: %v", sqlList(ops))
	}
}

// TestDiffCreateFunctionEmitsSetOf proves a fresh CREATE for a SETOF function
// renders the SETOF keyword. ReturnType.SetOf was previously never read
// anywhere (typeNameToRef never looked at pg_query's TypeName.Setof field),
// so DPG silently dropped SETOF from every function it compiled.
func TestDiffCreateFunctionEmitsSetOf(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer", SetOf: true},
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT n"},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "RETURNS SETOF integer") {
		t.Errorf("expected RETURNS SETOF integer, got: %v", sqlList(ops))
	}
}

// TestDiffCreateFunctionEmitsReturnsTable proves a RETURNS TABLE(...) function
// renders correctly: the TABLE-mode columns must appear ONLY inside the
// RETURNS TABLE(...) clause, never inline in the main parameter list (that
// was the actual bug — buildFunctionSQL previously wrote "TABLE a integer"
// as a literal, invalid parameter-mode prefix into the main parens).
func TestDiffCreateFunctionEmitsReturnsTable(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "record", SetOf: true},
			Args: []ir.FuncArg{
				{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
				{Name: "a", Mode: "TABLE", Type: ir.TypeRef{Name: "integer"}},
				{Name: "b", Mode: "TABLE", Type: ir.TypeRef{Name: "text"}},
			},
			Attrs: ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1, 'x'"},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected a CREATE FUNCTION op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "(n integer) RETURNS TABLE(a integer, b text)") {
		t.Errorf("expected the main parens to hold only n, and RETURNS TABLE(a integer, b text), got: %s", sql)
	}
	if strings.Contains(sql, "TABLE a integer") || strings.Contains(sql, "TABLE b text") {
		t.Errorf("TABLE-mode columns must not be rendered inline in the parameter list, got: %s", sql)
	}
}

// TestDiffFunctionReturnTypeChangeRequiresDropCreate proves a return-type
// change (here: adding SETOF) is NOT rendered as CREATE OR REPLACE FUNCTION —
// confirmed live against postgres:17 that PostgreSQL rejects that outright
// ("cannot change return type of existing function", hinting to DROP
// FUNCTION first). It must instead be a DROP FUNCTION followed by a fresh
// CREATE FUNCTION, the same DROP-required pattern diffView already uses for
// a materialized view's query change.
func TestDiffFunctionReturnTypeChangeRequiresDropCreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer", ReturnsSet: false,
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer", SetOf: true},
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT n"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 {
		t.Fatalf("expected a DROP FUNCTION + CREATE FUNCTION pair, got: %v", sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "DROP FUNCTION") {
		t.Errorf("expected the first op to be DROP FUNCTION, got: %s", ops[0].SQL())
	}
	if ops[0].Safety() != pipeline.Destructive {
		t.Errorf("expected DROP FUNCTION to be classified DESTRUCTIVE, got: %v", ops[0].Safety())
	}
	if !strings.Contains(ops[1].SQL(), "CREATE OR REPLACE FUNCTION") || !strings.Contains(ops[1].SQL(), "RETURNS SETOF integer") {
		t.Errorf("expected the second op to (re)create with RETURNS SETOF integer, got: %s", ops[1].SQL())
	}
}

// TestDiffFunctionSetOfUnchangedIsNoop is the zero-drift control: matching
// SetOf on both sides (here: both false, the common case) must not itself
// trigger the new DROP+CREATE path.
func TestDiffFunctionSetOfUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "integer", ReturnsSet: false,
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "integer", SetOf: false},
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT n"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged return type, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionReturnTableColumnListChanged proves a RETURNS TABLE(...)
// column-list edit (here: adding a column) is detected — ReturnType/SetOf
// alone can't tell two different TABLE column lists apart, since PostgreSQL's
// own catalog reports "record"/true for a TABLE-mode function regardless of
// its actual columns; this exercises the ReturnTable comparison specifically.
func TestDiffFunctionReturnTableColumnListChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "record", ReturnsSet: true, ReturnTable: "a integer",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "record", SetOf: true},
			Args: []ir.FuncArg{
				{Name: "a", Mode: "TABLE", Type: ir.TypeRef{Name: "integer"}},
				{Name: "b", Mode: "TABLE", Type: ir.TypeRef{Name: "text"}},
			},
			BodyHash: "h",
			Attrs:    ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1, 'x'"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 {
		t.Fatalf("expected a DROP FUNCTION + CREATE FUNCTION pair for the column-list change, got: %v", sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "DROP FUNCTION") {
		t.Errorf("expected the first op to be DROP FUNCTION, got: %s", ops[0].SQL())
	}
	if !strings.Contains(ops[1].SQL(), "RETURNS TABLE(a integer, b text)") {
		t.Errorf("expected the recreate to declare RETURNS TABLE(a integer, b text), got: %s", ops[1].SQL())
	}
}

// TestDiffFunctionReturnTableUnchangedIsNoop is the zero-drift control for
// ReturnTable: an identical TABLE column list on both sides must not trigger
// the DROP+CREATE path.
func TestDiffFunctionReturnTableUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", ReturnType: "record", ReturnsSet: true, ReturnTable: "a integer, b text",
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			ReturnType: ir.TypeRef{Name: "record", SetOf: true},
			Args: []ir.FuncArg{
				{Name: "a", Mode: "TABLE", Type: ir.TypeRef{Name: "integer"}},
				{Name: "b", Mode: "TABLE", Type: ir.TypeRef{Name: "text"}},
			},
			BodyHash: "h",
			Attrs:    ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT 1, 'x'"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged TABLE column list, got: %v", sqlList(ops))
	}
}

// TestBuildFunctionSQLImplicitReturnsSingleOut guards the fix for a function
// whose source omitted RETURNS entirely (valid PostgreSQL for a signature
// with at least one OUT/INOUT parameter — confirmed live against postgres:17
// that PG itself computes and stores a concrete return type in this case).
// Before the ir.Builder fix (internal/ir/builder.go's impliedReturnType),
// ir.Function.ReturnType stayed the zero TypeRef for this input, and
// buildFunctionSQL rendered a bare "RETURNS  LANGUAGE ..." (double space,
// empty type) — a real syntax error on apply. This test operates purely at
// the differ layer (constructing the ir.Function as the builder now would,
// rather than re-parsing source) since buildFunctionSQL is what actually
// turns ReturnType into SQL text.
func TestBuildFunctionSQLImplicitReturnsSingleOut(t *testing.T) {
	fn := &ir.Function{
		Schema: "public", Name: "f_single_out",
		Args: []ir.FuncArg{
			{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
			{Name: "a", Mode: "OUT", Type: ir.TypeRef{Name: "integer"}},
		},
		ReturnType: ir.TypeRef{Name: "integer"},
		Attrs:      ir.FuncAttrs{Language: "sql", Body: "SELECT n + 1"},
	}
	sql := buildFunctionSQL(fn)
	if strings.Contains(sql, "RETURNS  LANGUAGE") || strings.Contains(sql, "RETURNS  ") {
		t.Fatalf("expected no bare/empty RETURNS clause, got: %s", sql)
	}
	if !strings.Contains(sql, "RETURNS integer LANGUAGE") {
		t.Errorf("expected RETURNS integer LANGUAGE, got: %s", sql)
	}
	if !strings.Contains(sql, "OUT a integer") {
		t.Errorf("expected the OUT parameter to render inline, got: %s", sql)
	}
}

// TestBuildFunctionSQLImplicitReturnsMultiOut is the multi-OUT-parameter
// sibling: PostgreSQL implies "record" (not the type of any one column) when
// more than one OUT/INOUT parameter is present and RETURNS is omitted.
func TestBuildFunctionSQLImplicitReturnsMultiOut(t *testing.T) {
	fn := &ir.Function{
		Schema: "public", Name: "f_multi_out",
		Args: []ir.FuncArg{
			{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
			{Name: "a", Mode: "OUT", Type: ir.TypeRef{Name: "integer"}},
			{Name: "b", Mode: "OUT", Type: ir.TypeRef{Name: "text"}},
		},
		ReturnType: ir.TypeRef{Name: "record"},
		Attrs:      ir.FuncAttrs{Language: "sql", Body: "SELECT n, 'x'"},
	}
	sql := buildFunctionSQL(fn)
	if !strings.Contains(sql, "RETURNS record LANGUAGE") {
		t.Errorf("expected RETURNS record LANGUAGE, got: %s", sql)
	}
}

// TestBuildFunctionSQLDollarQuoteCollision guards a real bug fixed alongside
// the plpgsql body-hash canonicalization work: buildFunctionSQL used to
// hardcode a literal "$$" delimiter with no collision handling, so a body
// that itself contains "$$" (e.g. dynamic SQL using a dollar-quoted string
// literal) would produce broken, unparseable migration SQL. It now delegates
// to ir.RenderCreateFunctionSQL, which picks a colliding-free tag.
func TestBuildFunctionSQLDollarQuoteCollision(t *testing.T) {
	fn := &ir.Function{
		Schema:     "public",
		Name:       "f",
		ReturnType: ir.TypeRef{Name: "text"},
		Attrs:      ir.FuncAttrs{Language: "sql", Body: "SELECT $$literal$$"},
	}
	sql := buildFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with a body containing a literal $$ failed to parse: %v\nSQL: %s", err, sql)
	}
}

// TestDiffFunctionImplicitReturnsNoLiveDrift proves that the implied return
// type computed for an omitted-RETURNS function (ir.Builder's
// impliedReturnType) matches what live introspection reports for the same
// function — i.e. this doesn't just fix the syntax-error half of the bug,
// it also avoids a permanent self-inconsistent DROP+CREATE loop on every
// verify/plan --live (which an offline-only fix, e.g. simply omitting the
// RETURNS clause from the rendered SQL, would NOT have avoided: the snapshot
// side's ReturnType is always concrete since pg_proc.prorettype is never
// null).
func TestDiffFunctionImplicitReturnsNoLiveDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f_single_out(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f_single_out", ReturnType: "integer", ReturnsSet: false,
			Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", BodyHash: "h",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f_single_out",
			Args: []ir.FuncArg{
				{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
				{Name: "a", Mode: "OUT", Type: ir.TypeRef{Name: "integer"}},
			},
			ReturnType: ir.TypeRef{Name: "integer"}, // as impliedReturnType now computes it
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Body: "SELECT n + 1"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops once the implied return type matches the live-introspected one, got: %v", sqlList(ops))
	}
}

func TestDiffRegistration(t *testing.T) {
	d, ok := pipeline.Resolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if !ok {
		t.Fatal("Differ not registered")
	}
	if d == nil {
		t.Fatal("registered Differ is nil")
	}
}

// TestDiffColumnRenameMissingSnapshotErrors verifies RFC Section 7.4 step 5: a
// RENAMED FROM that names a column the snapshot doesn't contain is a compiler
// error rather than a silent fall-through to ADD COLUMN.
func TestDiffColumnRenameMissingSnapshotErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "users",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	stale := "ghost_col" // not in snapshot
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "email", Type: ir.TypeRef{Name: "text"}, RenamedFrom: &stale},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected diff error for stale RENAMED FROM, got nil")
	}
	for _, want := range []string{"RENAMED FROM", `"ghost_col"`, `"email"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q: %s", want, err.Error())
		}
	}
}

// TestDiffTableRenameMissingSnapshotErrors verifies the same rule for table-
// level RENAMED FROM directives. Without the guard, a stale rename silently
// degrades to a CREATE TABLE that loses the link to the original.
func TestDiffTableRenameMissingSnapshotErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	stale := "ghost_table"
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "accounts", RenamedFrom: &stale},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected diff error for stale table RENAMED FROM, got nil")
	}
	if !strings.Contains(err.Error(), "RENAMED FROM") || !strings.Contains(err.Error(), "ghost_table") {
		t.Errorf("error missing expected substrings: %s", err.Error())
	}
}

// TestDiffTableRenamePostApplyIsNoop verifies the post-apply state: once the
// rename has run and the snapshot has been rewritten to the new name, leaving
// RENAMED FROM in the source must not error and must not regenerate the
// rename. Without this, every directive would become a one-shot that the user
// has to remove after applying — defeating the point of declarative state.
func TestDiffTableRenamePostApplyIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.accounts", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "accounts"},
	})
	stale := "users" // already-applied rename: not in snapshot
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "accounts", RenamedFrom: &stale},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatalf("expected no error in post-apply state, got: %v", err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a RENAME TO op in post-apply state, got: %s", o.SQL())
		}
	}
}

// TestDiffTableRenameStateDDropsOrphan verifies the symmetric case: snapshot
// has both the old and new names (a partial apply or hand-edited snapshot).
// Old behaviour erred. New behaviour treats it as cleanup — Pass 2 drops the
// orphaned old name, Pass 3 diffs the new one.
func TestDiffTableRenameStateDDropsOrphan(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "users"},
	})
	_ = snap.SetObject("public.accounts", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "accounts"},
	})
	stale := "users"
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "accounts", RenamedFrom: &stale},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatalf("expected no error for State D, got: %v", err)
	}
	var sawDropUsers bool
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "DROP TABLE") && strings.Contains(sql, `"users"`) {
			sawDropUsers = true
		}
		if strings.Contains(sql, "RENAME TO") {
			t.Errorf("did not expect RENAME TO in State D, got: %s", sql)
		}
	}
	if !sawDropUsers {
		t.Errorf("expected DROP TABLE for orphaned users, got: %v", sqlList(ops))
	}
}

// TestDiffColumnRenamePostApplyIsNoop is the column-level analogue: the rename
// has been applied, the snapshot has the new column name, and RENAMED FROM is
// still in the source. Must not error and must not emit a redundant
// ALTER TABLE RENAME COLUMN.
func TestDiffColumnRenamePostApplyIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "users",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "email_address", Type: "text"},
			},
		},
	})
	stale := "email"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "email_address", Type: ir.TypeRef{Name: "text"}, RenamedFrom: &stale},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatalf("expected no error in column post-apply state, got: %v", err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME COLUMN") {
			t.Errorf("did not expect RENAME COLUMN in post-apply state, got: %s", o.SQL())
		}
	}
}

// TestDiffColumnRenameKeepsConstraints verifies that a RENAMED FROM directive
// doesn't manufacture a spurious drop+recreate of constraints whose snapshot
// expression still references the pre-rename column name.
// TestDiffValidateConstraintOnNotValidRemoval guards a real gap found live-
// testing a demo project: the RFC's NOT VALID lifecycle (Section 7.3) documents
// that removing NOT VALID from source must emit
// ALTER TABLE ... VALIDATE CONSTRAINT ..., but diffConstraints' identity key
// (name/type/expr only) never compared NotValid at all — a constraint
// transitioning NotValid: true -> false was invisible to the differ and
// silently produced "no changes" instead.
func TestDiffValidateConstraintOnNotValidRemoval(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 0)", NotValid: true},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 0)"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "VALIDATE CONSTRAINT") {
			found = true
			if !strings.Contains(o.SQL(), `"ck_amount_positive"`) {
				t.Errorf("VALIDATE CONSTRAINT does not name the constraint: %s", o.SQL())
			}
			if o.Safety() != pipeline.Caution {
				t.Errorf("expected CAUTION safety for VALIDATE CONSTRAINT, got %v", o.Safety())
			}
		}
		if strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			t.Errorf("did not expect a re-ADD of an already-existing constraint, got: %s", o.SQL())
		}
	}
	if !found {
		t.Fatalf("expected ALTER TABLE ... VALIDATE CONSTRAINT ..., got: %v", sqlList(ops))
	}
}

// TestDiffNoValidateConstraintWhenAlreadyValid guards against a spurious
// VALIDATE CONSTRAINT when the snapshot's constraint was never NOT VALID to
// begin with — nothing to validate, so no-op.
func TestDiffNoValidateConstraintWhenAlreadyValid(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 0)", NotValid: false},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 0)"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an already-valid constraint, got: %v", sqlList(ops))
	}
}

// TestDiffAddNamedNotNullConstraint guards RFC Section 7.3's table-level
// named NOT NULL constraint (PostgreSQL 18+): a brand-new one emits ALTER
// TABLE ... ADD CONSTRAINT name NOT NULL col NO INHERIT, CAUTION — and,
// since the constraint's own ADD CONSTRAINT performs the "make not null"
// transition itself (confirmed live), diffColumns must NOT also emit a
// separate ALTER COLUMN SET NOT NULL for the same column.
func TestDiffAddNamedNotNullConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "sku", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "sku", Type: ir.TypeRef{Name: "text"}, NotNull: true}},
			Constraints: []*ir.Constraint{
				{Name: "named_nn", Type: "NOT NULL", Columns: []string{"sku"}, NoInherit: true, Expr: `NOT NULL "sku" NO INHERIT`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET NOT NULL") {
			t.Errorf("expected no separate SET NOT NULL alongside ADD CONSTRAINT, got: %v", sqlList(ops))
		}
		if strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			addOp = o
		}
	}
	if addOp == nil {
		t.Fatalf("expected an ADD CONSTRAINT op, got: %v", sqlList(ops))
	}
	if !strings.Contains(addOp.SQL(), `ADD CONSTRAINT "named_nn" NOT NULL "sku" NO INHERIT`) {
		t.Errorf("unexpected SQL: %s", addOp.SQL())
	}
	if addOp.Safety() != pipeline.Caution {
		t.Errorf("expected Caution, got %s", addOp.Safety())
	}
}

// TestDiffAlterNotNullInheritOnly guards RFC Section 7.3's "NOT NULL
// constraint inheritability (PostgreSQL 18+)" row: when only NO INHERIT
// differs, the compiler emits a targeted ALTER TABLE ... ALTER CONSTRAINT
// name [NO] INHERIT — SAFE — instead of drop-and-recreate.
func TestDiffAlterNotNullInheritOnly(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "sku", Type: "text", NotNull: true}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "named_nn", Type: "NOT NULL", Expr: `NOT NULL "sku"`, NoInherit: false},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "sku", Type: ir.TypeRef{Name: "text"}, NotNull: true}},
			Constraints: []*ir.Constraint{
				{Name: "named_nn", Type: "NOT NULL", Columns: []string{"sku"}, NoInherit: true, Expr: `NOT NULL "sku" NO INHERIT`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var alterOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") || strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			t.Errorf("NO INHERIT-only change should not drop/re-add the constraint, got: %v", sqlList(ops))
		}
		if strings.Contains(o.SQL(), "ALTER CONSTRAINT") {
			alterOp = o
		}
	}
	if alterOp == nil {
		t.Fatalf("expected an ALTER CONSTRAINT op, got: %v", sqlList(ops))
	}
	if !strings.Contains(alterOp.SQL(), `ALTER CONSTRAINT "named_nn" NO INHERIT`) {
		t.Errorf("unexpected SQL: %s", alterOp.SQL())
	}
	if alterOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", alterOp.Safety())
	}
}

// TestDiffValidateConstraintReusedForNotNull guards #114: the NOT VALID/
// VALIDATE CONSTRAINT lifecycle already implemented for CHECK/FK (RFC
// Section 7.3, see TestDiffValidateConstraintOnNotValidRemoval above) is
// type-agnostic and reused unmodified for NOT NULL, since Constraint.Type
// was added to pgConstraintNameLabel and validateConstraintOp never
// switches on Type at all.
func TestDiffValidateConstraintReusedForNotNull(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "sku", Type: "text", NotNull: true}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "named_nn", Type: "NOT NULL", Expr: `NOT NULL "sku"`, NotValid: true},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "sku", Type: ir.TypeRef{Name: "text"}, NotNull: true}},
			Constraints: []*ir.Constraint{
				{Name: "named_nn", Type: "NOT NULL", Columns: []string{"sku"}, Expr: `NOT NULL "sku"`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "VALIDATE CONSTRAINT") {
			found = true
			if !strings.Contains(o.SQL(), `"named_nn"`) {
				t.Errorf("VALIDATE CONSTRAINT does not name the constraint: %s", o.SQL())
			}
		}
	}
	if !found {
		t.Fatalf("expected ALTER TABLE ... VALIDATE CONSTRAINT ..., got: %v", sqlList(ops))
	}
}

// TestDiffDropNotNullConstraintNoDoubleOp guards against a real conflict
// found live-testing against a PostgreSQL 18 server: ALTER COLUMN ... DROP
// NOT NULL already removes the underlying named/NO INHERIT constraint
// entirely (confirmed live, regardless of its custom name) — a second,
// separate ALTER TABLE ... DROP CONSTRAINT for the same now-gone name would
// fail with "constraint does not exist". Removing NOT NULL from source
// (column kept) must emit only the DROP NOT NULL, never both.
func TestDiffDropNotNullConstraintNoDoubleOp(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "sku", Type: "text", NotNull: true}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "named_nn", Type: "NOT NULL", Expr: `NOT NULL "sku"`, NoInherit: true},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "sku", Type: ir.TypeRef{Name: "text"}}}, // no longer NOT NULL
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropNotNullOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") {
			t.Errorf("expected no separate DROP CONSTRAINT alongside DROP NOT NULL, got: %v", sqlList(ops))
		}
		if strings.Contains(o.SQL(), "DROP NOT NULL") {
			dropNotNullOp = o
		}
	}
	if dropNotNullOp == nil {
		t.Fatalf("expected a DROP NOT NULL op, got: %v", sqlList(ops))
	}
	if dropNotNullOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", dropNotNullOp.Safety())
	}
}

// TestDiffNotNullConstraintRenameKeptStillNeedsDropRecreate is the negative
// case for TestDiffDropNotNullConstraintNoDoubleOp above: when the column
// STAYS NOT NULL but the named constraint disappears from desired (e.g. the
// user removed the explicit name, reverting to a bare inline NOT NULL),
// this is a genuine identity change that still needs its own
// drop-and-recreate — the narrower guard in diffConstraints' removal loop
// must not suppress this case just because the column is still NOT NULL.
func TestDiffNotNullConstraintRenameKeptStillNeedsDropRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "sku", Type: "text", NotNull: true}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "named_nn", Type: "NOT NULL", Expr: `NOT NULL "sku"`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "sku", Type: ir.TypeRef{Name: "text"}, NotNull: true}}, // plain, no promoted constraint
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") && strings.Contains(o.SQL(), "named_nn") {
			dropOp = o
		}
	}
	if dropOp == nil {
		t.Fatalf("expected DROP CONSTRAINT \"named_nn\" (constraint identity genuinely changed), got: %v", sqlList(ops))
	}
}

// TestDiffAddCheckNotEnforced guards RFC Section 7.2's ENFORCED/NOT
// ENFORCED (PostgreSQL 18+, CHECK/FOREIGN KEY only): a brand-new NOT
// ENFORCED CHECK constraint emits ADD CONSTRAINT ... NOT ENFORCED.
func TestDiffAddCheckNotEnforced(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck", Type: "CHECK", Columns: []string{"amount"}, Expr: "CHECK (amount > 0)", NotEnforced: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			addOp = o
		}
	}
	if addOp == nil {
		t.Fatalf("expected an ADD CONSTRAINT op, got: %v", sqlList(ops))
	}
	if !strings.Contains(addOp.SQL(), `ADD CONSTRAINT "ck" CHECK (amount > 0) NOT ENFORCED`) {
		t.Errorf("unexpected SQL: %s", addOp.SQL())
	}
}

// TestDiffCheckEnforcedChangeRecreates guards the CHECK half of #38's
// diffing: real PostgreSQL has no ALTER path for a CHECK constraint's
// ENFORCED/NOT ENFORCED at all (confirmed live: "cannot alter
// enforceability of constraint ... is not a foreign key constraint"), so a
// change must drop and recreate it.
func TestDiffCheckEnforcedChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck", Type: "CHECK", Expr: "CHECK (amount > 0)", NotEnforced: false},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck", Type: "CHECK", Columns: []string{"amount"}, Expr: "CHECK (amount > 0)", NotEnforced: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var dropOp, addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER CONSTRAINT") {
			t.Errorf("CHECK ENFORCED change must not use ALTER CONSTRAINT (real PostgreSQL rejects it), got: %s", o.SQL())
		}
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") {
			dropOp = o
		}
		if strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			addOp = o
		}
	}
	if dropOp == nil || addOp == nil {
		t.Fatalf("expected DROP CONSTRAINT + ADD CONSTRAINT, got: %v", sqlList(ops))
	}
	if !strings.Contains(addOp.SQL(), "NOT ENFORCED") {
		t.Errorf("re-added constraint missing NOT ENFORCED: %s", addOp.SQL())
	}
}

// TestDiffFKEnforcedChangeUsesAlterConstraint guards the FOREIGN KEY half:
// real PostgreSQL DOES support ALTER CONSTRAINT ENFORCED/NOT ENFORCED for
// FOREIGN KEY (confirmed live, unlike CHECK), so this must be a targeted
// SAFE ALTER, not a drop-and-recreate.
func TestDiffFKEnforcedChangeUsesAlterConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "parent_id", Type: "bigint"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "fk_parent", Type: "FOREIGN KEY", Expr: `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`, NotEnforced: false},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "parent_id", Type: ir.TypeRef{Name: "bigint"}},
			},
			Constraints: []*ir.Constraint{
				{Name: "fk_parent", Type: "FOREIGN KEY", Columns: []string{"parent_id"}, Expr: `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`, NotEnforced: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var alterOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") || strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			t.Errorf("FK ENFORCED-only change should not drop/re-add the constraint, got: %v", sqlList(ops))
		}
		if strings.Contains(o.SQL(), "ALTER CONSTRAINT") {
			alterOp = o
		}
	}
	if alterOp == nil {
		t.Fatalf("expected an ALTER CONSTRAINT op, got: %v", sqlList(ops))
	}
	if !strings.Contains(alterOp.SQL(), `ALTER CONSTRAINT "fk_parent" NOT ENFORCED`) {
		t.Errorf("unexpected SQL: %s", alterOp.SQL())
	}
	if alterOp.Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", alterOp.Safety())
	}
}

// TestDiffFKDeferrableOnlyChangeUsesAlterConstraint guards RFC Section
// 7.3's "Deferrability-only changes" row for FOREIGN KEY — previously
// documented but never actually implemented anywhere in core/ (found while
// implementing #38, since ENFORCED and deferrability share the same ALTER
// CONSTRAINT statement in real PostgreSQL).
func TestDiffFKDeferrableOnlyChangeUsesAlterConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "parent_id", Type: "bigint"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "fk_parent", Type: "FOREIGN KEY", Expr: `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`, Deferrable: false},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "parent_id", Type: ir.TypeRef{Name: "bigint"}},
			},
			Constraints: []*ir.Constraint{
				{Name: "fk_parent", Type: "FOREIGN KEY", Columns: []string{"parent_id"}, Expr: `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`, Deferrable: true, InitiallyDeferred: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var alterOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") || strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			t.Errorf("deferrability-only change should not drop/re-add the constraint, got: %v", sqlList(ops))
		}
		if strings.Contains(o.SQL(), "ALTER CONSTRAINT") {
			alterOp = o
		}
	}
	if alterOp == nil {
		t.Fatalf("expected an ALTER CONSTRAINT op, got: %v", sqlList(ops))
	}
	if !strings.Contains(alterOp.SQL(), `ALTER CONSTRAINT "fk_parent" DEFERRABLE INITIALLY DEFERRED`) {
		t.Errorf("unexpected SQL: %s", alterOp.SQL())
	}
}

// TestDiffFKCombinedDeferrableAndEnforcedOneStatement guards that a
// simultaneous deferrability + ENFORCED change emits ONE combined ALTER
// CONSTRAINT statement, matching real PostgreSQL's own combined grammar
// (RFC Section 7.3), not two separate statements.
func TestDiffFKCombinedDeferrableAndEnforcedOneStatement(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "parent_id", Type: "bigint"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "fk_parent", Type: "FOREIGN KEY", Expr: `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`, Deferrable: false, NotEnforced: false},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "parent_id", Type: ir.TypeRef{Name: "bigint"}},
			},
			Constraints: []*ir.Constraint{
				{Name: "fk_parent", Type: "FOREIGN KEY", Columns: []string{"parent_id"}, Expr: `FOREIGN KEY ("parent_id") REFERENCES "orders" ("id")`, Deferrable: true, NotEnforced: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var alterOps []pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER CONSTRAINT") {
			alterOps = append(alterOps, o)
		}
	}
	if len(alterOps) != 1 {
		t.Fatalf("expected exactly one combined ALTER CONSTRAINT statement, got %d: %v", len(alterOps), sqlList(ops))
	}
	sql := alterOps[0].SQL()
	if !strings.Contains(sql, "DEFERRABLE") || !strings.Contains(sql, "NOT ENFORCED") {
		t.Errorf("expected both DEFERRABLE and NOT ENFORCED in one statement, got: %s", sql)
	}
}

// TestDiffTemporalForeignKeyColumnDropCascade guards a real gap found while
// implementing RFC Section 7.3's PERIOD temporal FOREIGN KEY (PostgreSQL
// 18+): localConstraintCols previously truncated a "PERIOD colname" entry
// at its own internal space, returning the literal word "PERIOD" instead
// of the actual column name — so the "every local column this constraint
// references is being dropped, skip the redundant DROP CONSTRAINT" cascade
// check (PostgreSQL itself already drops the constraint via DROP COLUMN)
// never matched for a temporal FK, which would have emitted a second,
// conflicting DROP CONSTRAINT against a name PostgreSQL already removed.
func TestDiffTemporalForeignKeyColumnDropCascade(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.room_bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "room_bookings",
			Columns: []snapshot.SnapColumn{{Name: "room_id", Type: "bigint"}},
		},
	})
	_ = snap.SetObject("public.room_events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "room_events",
			Columns: []snapshot.SnapColumn{
				{Name: "room_id", Type: "bigint"},
				{Name: "valid_at", Type: "daterange"},
			},
			Constraints: []snapshot.SnapConstraint{
				{
					Name: "fk_room", Type: "FOREIGN KEY",
					Expr: `FOREIGN KEY ("room_id", PERIOD "valid_at") REFERENCES "room_bookings" ("room_id", PERIOD "valid_at")`,
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "room_bookings",
			Columns: []*ir.Column{{Name: "room_id", Type: ir.TypeRef{Name: "bigint"}}},
		},
		&ir.Table{
			Schema: "public",
			Name:   "room_events",
			// valid_at (and its temporal FK) dropped entirely.
			Columns: []*ir.Column{{Name: "room_id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") && strings.Contains(o.SQL(), "fk_room") {
			t.Errorf("expected no separate DROP CONSTRAINT for fk_room (DROP COLUMN valid_at already cascades it), got: %v", sqlList(ops))
		}
	}
	var dropColOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP COLUMN") && strings.Contains(o.SQL(), "valid_at") {
			dropColOp = o
		}
	}
	if dropColOp == nil {
		t.Fatalf("expected a DROP COLUMN valid_at op, got: %v", sqlList(ops))
	}
}

// TestDiffMultiColumnUniqueColumnDropCascade guards anyDropped's general
// fix (found while implementing temporal FOREIGN KEY above, but not
// specific to it): confirmed live that PostgreSQL cascades away a
// multi-column UNIQUE constraint entirely when even ONE of its two
// columns is dropped, not only when both are — the pre-fix ALL-columns
// condition would have emitted a redundant, conflicting DROP CONSTRAINT
// for a name PostgreSQL had already removed via cascade.
func TestDiffMultiColumnUniqueColumnDropCascade(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "t",
			Columns: []snapshot.SnapColumn{
				{Name: "a", Type: "integer"},
				{Name: "b", Type: "integer"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "uq", Type: "UNIQUE", Expr: `UNIQUE ("a", "b")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "b", Type: ir.TypeRef{Name: "integer"}}}, // "a" dropped
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") {
			t.Errorf("expected no separate DROP CONSTRAINT for uq (DROP COLUMN a already cascades it), got: %v", sqlList(ops))
		}
	}
}

// TestCreateForeignTableEmitsServerOptions guards a real bug found live-
// testing a demo project: createTable wrote the "CREATE FOREIGN TABLE"
// keyword but never appended SERVER/OPTIONS — not just incomplete, an
// outright PostgreSQL syntax error, since SERVER is mandatory for a foreign
// table. Confirmed live: applying the old output failed immediately.
func TestCreateForeignTableEmitsServerOptions(t *testing.T) {
	d := New()
	server := "loopback_server"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:        "public",
			Name:          "remote_users",
			Foreign:       true,
			ForeignServer: &server,
			ForeignOptions: []pipeline.StorageParam{
				{Key: "table_name", Value: "users"},
				{Key: "schema_name", Value: "public"},
			},
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var createOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE FOREIGN TABLE") {
			createOp = o
			break
		}
	}
	if createOp == nil {
		t.Fatalf("expected a CREATE FOREIGN TABLE op, got: %v", sqlList(ops))
	}
	sql := createOp.SQL()
	if !strings.Contains(sql, `SERVER "loopback_server"`) {
		t.Errorf("CREATE FOREIGN TABLE missing SERVER clause: %s", sql)
	}
	if !strings.Contains(sql, "OPTIONS (table_name 'users', schema_name 'public')") {
		t.Errorf("CREATE FOREIGN TABLE missing OPTIONS clause: %s", sql)
	}
}

// TestDiffForeignTableServerChangeDropRecreate guards that a SERVER change
// on an existing foreign table is DROP + CREATE — real PostgreSQL has no
// ALTER FOREIGN TABLE clause to change the server.
func TestDiffForeignTableServerChangeDropRecreate(t *testing.T) {
	d := New()
	oldServer := "server_a"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.remote_users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "remote_users",
			Foreign: true, ForeignServer: &oldServer,
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	newServer := "server_b"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "remote_users",
			Foreign: true, ForeignServer: &newServer,
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var sawDrop, sawCreate bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP FOREIGN TABLE") {
			sawDrop = true
			if o.Safety() != pipeline.Destructive {
				t.Errorf("expected DESTRUCTIVE safety for DROP FOREIGN TABLE, got %v", o.Safety())
			}
		}
		if strings.Contains(o.SQL(), "CREATE FOREIGN TABLE") && strings.Contains(o.SQL(), `SERVER "server_b"`) {
			sawCreate = true
		}
	}
	if !sawDrop || !sawCreate {
		t.Fatalf("expected DROP FOREIGN TABLE + CREATE FOREIGN TABLE with new server, got: %v", sqlList(ops))
	}
}

// TestDiffForeignTableOptionsChange guards that an OPTIONS-only change uses
// real PostgreSQL's in-place ALTER FOREIGN TABLE ... OPTIONS (ADD/SET/DROP
// ...) rather than a needless drop+recreate.
func TestDiffForeignTableOptionsChange(t *testing.T) {
	d := New()
	server := "loopback_server"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.remote_users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "remote_users",
			Foreign: true, ForeignServer: &server,
			ForeignOptions: "table_name=users, stale_opt=x",
			Columns:        []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "remote_users",
			Foreign: true, ForeignServer: &server,
			ForeignOptions: []pipeline.StorageParam{
				{Key: "table_name", Value: "users_v2"}, // changed -> SET
				{Key: "schema_name", Value: "public"},  // new -> ADD
				// stale_opt absent -> DROP
			},
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var alterOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER FOREIGN TABLE") {
			alterOp = o
			break
		}
	}
	if alterOp == nil {
		t.Fatalf("expected ALTER FOREIGN TABLE ... OPTIONS, got: %v", sqlList(ops))
	}
	sql := alterOp.SQL()
	for _, want := range []string{"SET table_name 'users_v2'", "ADD schema_name 'public'", "DROP stale_opt"} {
		if !strings.Contains(sql, want) {
			t.Errorf("ALTER FOREIGN TABLE missing %q: %s", want, sql)
		}
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP FOREIGN TABLE") {
			t.Errorf("did not expect drop+recreate for an OPTIONS-only change, got: %v", sqlList(ops))
		}
	}
}

// TestDiffConstraintRenamedFromEmitsAlterRename is the regression guard for
// table-level constraint rename detection: before this, RENAMED FROM had no
// field to carry the old name at all, so a renamed constraint was
// indistinguishable from "old constraint dropped, new one added" — a
// DROP CONSTRAINT + ADD CONSTRAINT pair instead of a metadata-only rename.
func TestDiffConstraintRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck_amount_old", Type: "CHECK", Expr: "CHECK (amount > 0)"},
			},
		},
	})
	old := "ck_amount_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 0)", RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP CONSTRAINT") || containsSQL(ops, "ADD CONSTRAINT") {
		t.Fatalf("renamed constraint should match by RENAMED FROM, not drop+add, got: %v", sqlList(ops))
	}
	want := `ALTER TABLE "public"."orders" RENAME CONSTRAINT "ck_amount_old" TO "ck_amount_positive";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME CONSTRAINT") && o.Safety() != pipeline.Safe {
			t.Errorf("expected SAFE safety for RENAME CONSTRAINT (matches Index's sub-object rename precedent), got %v", o.Safety())
		}
	}
}

// TestDiffConstraintRenamedFromMatchIsIdentityOnly documents an existing,
// pre-existing (not introduced by RENAMED FROM support) property of
// diffConstraints: matching is identity-only, by name — a constraint's own
// Expr is never compared once its identity (direct name, or now RENAMED
// FROM) is found. So a rename accompanied by an expression change still
// only emits the rename; the expression change goes undetected, the exact
// same way it already would for a plain, non-renamed same-name constraint
// whose Expr changed (see this function's own top-of-function doc comment:
// "a named constraint whose definition changes while keeping the same name
// was already invisible to it before this fix"). This test exists to pin
// that RENAMED FROM support didn't change that pre-existing behavior either
// way, not to endorse it as correct — it's a known, separately-scoped gap.
func TestDiffConstraintRenamedFromMatchIsIdentityOnly(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck_amount_old", Type: "CHECK", Expr: "CHECK (amount > 0)"},
			},
		},
	})
	old := "ck_amount_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 10)", RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders" RENAME CONSTRAINT "ck_amount_old" TO "ck_amount_positive";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP CONSTRAINT") || containsSQL(ops, "ADD CONSTRAINT") {
		t.Errorf("did not expect drop+add — matching is identity-only, same as the pre-existing non-rename case; got: %v", sqlList(ops))
	}
}

// TestDiffConstraintRenamedFromStaleErrors mirrors diffCompositeAttrs/
// diffPartitionList's identical stale-RENAMED-FROM validation: neither the
// old nor new name exists in the snapshot, indistinguishable from a typo on
// a genuinely new constraint.
func TestDiffConstraintRenamedFromStaleErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "numeric"}},
		},
	})
	old := "nonexistent_old_name"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "numeric"}}},
			Constraints: []*ir.Constraint{
				{Name: "ck_amount_positive", Type: "CHECK", Expr: "CHECK (amount > 0)", RenamedFrom: &old},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot constraint")
	}
	// DPG-E021 (stale_renamed_from, RFC Appendix C).
	cerr, ok := err.(*pipeline.CompilerError)
	if !ok {
		t.Fatalf("expected *pipeline.CompilerError, got %T: %v", err, err)
	}
	if cerr.Code != "DPG-E021" {
		t.Errorf("Code: got %q, want DPG-E021", cerr.Code)
	}
}

// TestDiffDomainConstraintRenamedFromEmitsAlterRename is
// TestDiffConstraintRenamedFromEmitsAlterRename's Domain counterpart —
// ir.Constraint is reused verbatim for DomainConstraints (RFC Section 5.4),
// so the same RenamedFrom field and matching logic applies there too.
func TestDiffDomainConstraintRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.positive_amount", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema:         "public",
			Name:           "positive_amount",
			Variant:        "DOMAIN",
			DomainBaseType: "numeric",
			DomainConstraints: []snapshot.SnapConstraint{
				{Name: "ck_positive_old", Type: "CHECK", Expr: "CHECK (VALUE > 0)"},
			},
		},
	})
	old := "ck_positive_old"
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema:         "public",
			Name:           "positive_amount",
			Variant:        "DOMAIN",
			DomainBaseType: ir.TypeRef{Name: "numeric"},
			DomainConstraints: []*ir.Constraint{
				{Name: "ck_positive", Type: "CHECK", Expr: "CHECK (VALUE > 0)", RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP CONSTRAINT") || containsSQL(ops, "ADD CONSTRAINT") {
		t.Fatalf("renamed domain constraint should match by RENAMED FROM, not drop+add, got: %v", sqlList(ops))
	}
	want := `ALTER DOMAIN "public"."positive_amount" RENAME CONSTRAINT "ck_positive_old" TO "ck_positive";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffDomainConstraintRenamedFromAndExprChanged confirms a simultaneous
// rename + expression change on a DOMAIN constraint still requires drop+add
// (unlike table-level constraints, Domain's diffing DOES compare Expr — see
// TestDiffConstraintRenamedFromMatchIsIdentityOnly's doc comment for the
// contrast), but drops under the constraint's CURRENT (old, matched-
// snapshot) name, not the desired new name, which doesn't exist live yet.
func TestDiffDomainConstraintRenamedFromAndExprChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.positive_amount", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema:         "public",
			Name:           "positive_amount",
			Variant:        "DOMAIN",
			DomainBaseType: "numeric",
			DomainConstraints: []snapshot.SnapConstraint{
				{Name: "ck_positive_old", Type: "CHECK", Expr: "CHECK (VALUE > 0)"},
			},
		},
	})
	old := "ck_positive_old"
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema:         "public",
			Name:           "positive_amount",
			Variant:        "DOMAIN",
			DomainBaseType: ir.TypeRef{Name: "numeric"},
			DomainConstraints: []*ir.Constraint{
				{Name: "ck_positive", Type: "CHECK", Expr: "CHECK (VALUE > 10)", RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER DOMAIN "public"."positive_amount" DROP CONSTRAINT "ck_positive_old";`) {
		t.Errorf("expected DROP CONSTRAINT referencing the constraint's actual (old) name, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER DOMAIN "public"."positive_amount" ADD CONSTRAINT "ck_positive" CHECK (VALUE > 10);`) {
		t.Errorf("expected ADD CONSTRAINT for the new expression under the new name, got: %v", sqlList(ops))
	}
}

func TestDiffColumnRenameKeepsConstraints(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("iam.groups", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "iam",
			Name:   "groups",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "locality_id", Type: "bigint"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "", Type: "FOREIGN KEY",
					Expr: `FOREIGN KEY ("locality_id") REFERENCES "iam"."localities" ("id")`},
			},
		},
	})

	old := "locality_id"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "iam",
			Name:   "groups",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "locale_id", Type: ir.TypeRef{Name: "bigint"}, RenamedFrom: &old},
			},
			Constraints: []*ir.Constraint{
				{Type: "FOREIGN KEY",
					Expr: `FOREIGN KEY ("locale_id") REFERENCES "iam"."localities" ("id")`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "WARNING") {
			t.Errorf("did not expect a constraint WARNING after RENAMED FROM, got: %s", sql)
		}
		if strings.Contains(sql, "ADD") && strings.Contains(sql, "FOREIGN KEY") {
			t.Errorf("did not expect FK to be re-added after RENAMED FROM, got: %s", sql)
		}
		if strings.Contains(sql, "DROP CONSTRAINT") {
			t.Errorf("did not expect DROP CONSTRAINT after RENAMED FROM, got: %s", sql)
		}
	}
}

// TestDiffColumnRenameStaleConstraintRefIsError is the regression guard for
// RFC Section 7.6's DPG-E019: "After emitting the rename, all constraint
// and index declarations MUST use [the new name]." A constraint whose
// structured Columns list still names the pre-rename column is a genuine
// authoring mistake (the user renamed the column but forgot to update the
// constraint declaration to match) and must error, not silently pass
// through unchecked.
func TestDiffColumnRenameStaleConstraintRefIsError(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "app", Name: "users",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "email", Type: "text"},
			},
		},
	})

	old := "email"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "app", Name: "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "email_address", Type: ir.TypeRef{Name: "text"}, RenamedFrom: &old},
			},
			Constraints: []*ir.Constraint{
				// Stale: still lists "email", the pre-rename name.
				{Name: "uq_users_email", Type: "UNIQUE", Columns: []string{"email"},
					Expr: `UNIQUE ("email")`},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected error for constraint still referencing the pre-rename column name, got nil")
	}
	ce, ok := err.(*pipeline.CompilerError)
	if !ok {
		t.Fatalf("expected *pipeline.CompilerError, got %T: %v", err, err)
	}
	if ce.Code != "DPG-E019" {
		t.Errorf("expected DPG-E019, got code %q: %v", ce.Code, err)
	}
}

// TestDiffColumnRenameStaleIndexRefIsError is DPG-E019's index-side
// counterpart to TestDiffColumnRenameStaleConstraintRefIsError.
func TestDiffColumnRenameStaleIndexRefIsError(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "app", Name: "users",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "email", Type: "text"},
			},
		},
	})

	old := "email"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "app", Name: "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "email_address", Type: ir.TypeRef{Name: "text"}, RenamedFrom: &old},
			},
			Indexes: []*ir.Index{
				// Stale: still lists "email", the pre-rename name.
				{Name: "idx_users_email", Columns: []pipeline.IndexColumn{{Name: "email"}}},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected error for index still referencing the pre-rename column name, got nil")
	}
	ce, ok := err.(*pipeline.CompilerError)
	if !ok {
		t.Fatalf("expected *pipeline.CompilerError, got %T: %v", err, err)
	}
	if ce.Code != "DPG-E019" {
		t.Errorf("expected DPG-E019, got code %q: %v", ce.Code, err)
	}
}

// TestDiffColumnRenameKeepsIndex guards a real bug found live-testing a
// demo project: diffIndexes compared the desired index against the
// snapshot's SnapIndex verbatim, with no rename translation at all (unlike
// diffConstraints, which already translates constraint expressions via
// translateConstraintExpr) — so a same-named index whose column was renamed
// always compared unequal (old name in the snapshot vs. new name in
// desired) and got spuriously DROP + recreated, even though real
// PostgreSQL's ALTER TABLE RENAME COLUMN keeps every dependent index
// transparently, no rebuild needed at all.
// TestDiffColumnDeprecatedEmitsComment guards a real, functionally-total
// gap found live-testing a demo project: ir.Column.Deprecated was parsed,
// stored in the snapshot, and used by the linter (which is why `dpg plan`
// already printed a "deprecated" warning), but the differ never referenced
// it at all anywhere — a DEPRECATED directive had zero effect on generated
// SQL, despite the RFC documenting
// `COMMENT ON COLUMN t.c IS '[DEPRECATED] msg'` as the expected output.
func TestDiffColumnDeprecatedEmitsComment(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.room_bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "room_bookings",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "room", Type: "text"},
			},
		},
	})
	msg := "will be replaced by room_id"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "room_bookings",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "room", Type: ir.TypeRef{Name: "text"}, Deprecated: &msg},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "COMMENT ON COLUMN") {
			found = true
			want := `IS '[DEPRECATED] will be replaced by room_id';`
			if !strings.Contains(o.SQL(), want) {
				t.Errorf("COMMENT ON COLUMN text: got %q, want substring %q", o.SQL(), want)
			}
		}
	}
	if !found {
		t.Fatalf("expected a COMMENT ON COLUMN op for DEPRECATED, got: %v", sqlList(ops))
	}
}

// TestDiffColumnDeprecatedNoOpWhenUnchanged guards against re-emitting the
// COMMENT ON COLUMN every plan once DEPRECATED has already been applied —
// the snapshot's own Deprecated field (not just Comment) must be compared.
func TestDiffColumnDeprecatedNoOpWhenUnchanged(t *testing.T) {
	d := New()
	msg := "will be replaced by room_id"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.room_bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "room_bookings",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "room", Type: "text", Deprecated: &msg},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "room_bookings",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "room", Type: ir.TypeRef{Name: "text"}, Deprecated: &msg},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "COMMENT ON COLUMN") {
			t.Errorf("expected no COMMENT ON COLUMN op when DEPRECATED is unchanged, got: %s", o.SQL())
		}
	}
}

// TestDiffTableDeprecatedEmitsComment, TestDiffViewDeprecatedEmitsComment,
// and TestDiffFunctionDeprecatedEmitsComment guard the same gap as
// TestDiffColumnDeprecatedEmitsComment, for the RFC's other three
// DEPRECATED-bearing kinds (Section 19.1: "Applied to tables, columns, views,
// functions"). ir.Table/View/Function.Deprecated was parsed, snapshotted
// (for Table only — SnapView/SnapFunction didn't even have the field, a
// deeper gap than Column's), and used by the linter, but never referenced
// by the differ.
func TestDiffTableDeprecatedEmitsComment(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.legacy_orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "legacy_orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	msg := "use orders instead"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "legacy_orders",
			Columns:    []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Deprecated: &msg,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "COMMENT ON TABLE") {
			found = true
			want := `IS '[DEPRECATED] use orders instead';`
			if !strings.Contains(o.SQL(), want) {
				t.Errorf("COMMENT ON TABLE text: got %q, want substring %q", o.SQL(), want)
			}
		}
	}
	if !found {
		t.Fatalf("expected a COMMENT ON TABLE op for DEPRECATED, got: %v", sqlList(ops))
	}
}

func TestDiffViewDeprecatedEmitsComment(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.legacy_summary", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public", Name: "legacy_summary",
			Query: "SELECT 1",
		},
	})
	msg := "use order_summary instead"
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public", Name: "legacy_summary",
			Query:      "SELECT 1",
			Deprecated: &msg,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "COMMENT ON VIEW") {
			found = true
			want := `IS '[DEPRECATED] use order_summary instead';`
			if !strings.Contains(o.SQL(), want) {
				t.Errorf("COMMENT ON VIEW text: got %q, want substring %q", o.SQL(), want)
			}
		}
	}
	if !found {
		t.Fatalf("expected a COMMENT ON VIEW op for DEPRECATED, got: %v", sqlList(ops))
	}
}

func TestDiffFunctionDeprecatedEmitsComment(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.legacy_calc()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "legacy_calc",
			ReturnType: "void",
			Language:   "plpgsql",
			Volatility: "VOLATILE",
			BodyHash:   "hash1",
		},
	})
	msg := "use calc_v2 instead"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:     "public",
			Name:       "legacy_calc",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "hash1",
			Deprecated: &msg,
			Attrs: ir.FuncAttrs{
				Language:   "plpgsql",
				Volatility: "VOLATILE",
				Body:       "BEGIN END;",
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "COMMENT ON FUNCTION") {
			found = true
			want := `IS '[DEPRECATED] use calc_v2 instead';`
			if !strings.Contains(o.SQL(), want) {
				t.Errorf("COMMENT ON FUNCTION text: got %q, want substring %q", o.SQL(), want)
			}
		}
	}
	if !found {
		t.Fatalf("expected a COMMENT ON FUNCTION op for DEPRECATED, got: %v", sqlList(ops))
	}
}

func TestDiffColumnRenameKeepsIndex(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "metadata", Type: "jsonb"},
			},
			Indexes: []snapshot.SnapIndex{
				{Name: "orders_metadata_gin_idx", Method: "gin", Columns: "metadata"},
			},
		},
	})

	old := "metadata"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "extra_data", Type: ir.TypeRef{Name: "jsonb"}, RenamedFrom: &old},
			},
			Indexes: []*ir.Index{
				{Name: "orders_metadata_gin_idx", Method: "gin", Columns: []pipeline.IndexColumn{{Name: "extra_data"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "DROP INDEX") || strings.Contains(sql, "CREATE INDEX") {
			t.Errorf("did not expect the index to be dropped/recreated after a plain column rename, got: %s", sql)
		}
	}
}

// TestDiffColumnRenameKeepsCheckConstraintUnquotedReference is the
// regression guard for a PRE-EXISTING bug an independent verification pass
// found (while checking a related EXCLUDE fix, but reproducible here too,
// unrelated to EXCLUDE specifically): replaceQuotedIdents originally matched
// only the quoted "old" form, but nodeToText/pg_query.Deparse — which
// renders a CHECK expression — leaves a plain lowercase identifier unquoted
// (confirmed live: `SELECT room > 0` deparses to `room > 0`, not
// `"room" > 0`). A CHECK whose expression is a bare comparison like
// `room > 0` (the overwhelmingly common shape) never got its renamed column
// translated at all.
func TestDiffColumnRenameKeepsCheckConstraintUnquotedReference(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "bookings",
			Columns: []snapshot.SnapColumn{{Name: "room", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "", Type: "CHECK", Expr: `CHECK (room > 0)`},
			},
		},
	})

	old := "room"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "bookings",
			Columns: []*ir.Column{
				{Name: "chamber", Type: ir.TypeRef{Name: "integer"}, RenamedFrom: &old},
			},
			Constraints: []*ir.Constraint{
				{Type: "CHECK", Expr: `CHECK (chamber > 0)`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "WARNING") {
			t.Errorf("did not expect a constraint WARNING after RENAMED FROM, got: %s", sql)
		}
		if strings.Contains(sql, "DROP CONSTRAINT") || strings.Contains(sql, "ADD") {
			t.Errorf("did not expect the CHECK constraint to be dropped/re-added after RENAMED FROM, got: %s", sql)
		}
	}
}

// TestDiffColumnRenameKeepsExcludeConstraint guards translateConstraintExpr's
// EXCLUDE case: an unnamed EXCLUDE constraint's structural-signature match
// (the offline path for unnamed constraints) must translate a renamed
// column's quoted element-list reference the same way CHECK's whole-string
// translation already does. The WHERE clause here deliberately references a
// column NOT being renamed — the WHERE-clause-specific case (an unquoted
// reference, which is what the real pipeline actually renders) is covered
// separately by TestDiffColumnRenameKeepsExcludeConstraintReferencedOnlyInWhere,
// since the two exercise different text — quoted (element list, built via
// quoteIdent) vs unquoted (WHERE/expression elements, built via
// nodeToText/pg_query.Deparse, which only quotes an identifier that actually
// needs it).
func TestDiffColumnRenameKeepsExcludeConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "bookings",
			Columns: []snapshot.SnapColumn{
				{Name: "room", Type: "integer"},
				{Name: "span", Type: "tsrange"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "", Type: "EXCLUDE",
					Expr: `EXCLUDE USING gist ("room" WITH =, "span" WITH &&) WHERE (room > 0)`},
			},
		},
	})

	old := "span"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "bookings",
			Columns: []*ir.Column{
				{Name: "room", Type: ir.TypeRef{Name: "integer"}},
				{Name: "during", Type: ir.TypeRef{Name: "tsrange"}, RenamedFrom: &old},
			},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Expr: `EXCLUDE USING gist ("room" WITH =, "during" WITH &&) WHERE (room > 0)`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "WARNING") {
			t.Errorf("did not expect a constraint WARNING after RENAMED FROM, got: %s", sql)
		}
		if strings.Contains(sql, "DROP CONSTRAINT") || strings.Contains(sql, "ADD") {
			t.Errorf("did not expect the EXCLUDE constraint to be dropped/re-added after RENAMED FROM, got: %s", sql)
		}
	}
}

// TestDiffColumnRenameKeepsExcludeConstraintReferencedOnlyInWhere is the
// regression guard for a bug an independent verification pass found: a
// renamed column referenced ONLY in an EXCLUDE's WHERE clause wasn't
// translated, because replaceQuotedIdents originally matched only the
// quoted "old" form — but nodeToText/pg_query.Deparse (which renders the
// WHERE clause) leaves a plain lowercase identifier unquoted (confirmed
// live: `SELECT room > 0` deparses to `room > 0`, not `"room" > 0`). The
// element list's own reference to "room" (elsewhere in this same
// constraint) IS quoted (built via quoteIdent) and would already have been
// translated correctly — this test's fixture avoids that column entirely
// (uses "during" for the element) so it isolates and proves the WHERE-only
// case specifically.
func TestDiffColumnRenameKeepsExcludeConstraintReferencedOnlyInWhere(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "bookings",
			Columns: []snapshot.SnapColumn{
				{Name: "room", Type: "integer"},
				{Name: "during", Type: "tsrange"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "", Type: "EXCLUDE",
					Expr: `EXCLUDE USING gist ("during" WITH &&) WHERE (room > 0)`},
			},
		},
	})

	old := "room"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "bookings",
			Columns: []*ir.Column{
				{Name: "chamber", Type: ir.TypeRef{Name: "integer"}, RenamedFrom: &old},
				{Name: "during", Type: ir.TypeRef{Name: "tsrange"}},
			},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Expr: `EXCLUDE USING gist ("during" WITH &&) WHERE (chamber > 0)`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "WARNING") {
			t.Errorf("did not expect a constraint WARNING after RENAMED FROM, got: %s", sql)
		}
		if strings.Contains(sql, "DROP CONSTRAINT") || strings.Contains(sql, "ADD") {
			t.Errorf("did not expect the EXCLUDE constraint to be dropped/re-added after RENAMED FROM, got: %s", sql)
		}
	}
}

// TestDiffColumnRenameDoesNotMangleStringLiteral is the regression guard for
// a bug an independent verification pass found in the bare-word substitution
// added above: a string literal is delimited by a single quote, a
// non-identifier character, so an unqualified \bold\b regex treats 'room'
// (the STRING VALUE) exactly like the bare identifier room. Renaming an
// unrelated column also named "room" elsewhere on the table must not mangle
// this CHECK's literal 'room' into 'chamber' — that would make an
// UNCHANGED constraint's snapshot-side key stop matching its (correctly
// unchanged) desired-side key, producing a spurious destructive
// drop+recreate for a constraint that never actually changed.
func TestDiffColumnRenameDoesNotMangleStringLiteral(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "t",
			Columns: []snapshot.SnapColumn{
				{Name: "status", Type: "text"},
				{Name: "room", Type: "integer"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "", Type: "CHECK", Expr: `CHECK (status <> 'room')`},
			},
		},
	})

	old := "room"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "t",
			Columns: []*ir.Column{
				{Name: "status", Type: ir.TypeRef{Name: "text"}},
				{Name: "chamber", Type: ir.TypeRef{Name: "integer"}, RenamedFrom: &old},
			},
			Constraints: []*ir.Constraint{
				// Genuinely unchanged: the literal 'room' is a string value,
				// unrelated to the renamed "room" column.
				{Type: "CHECK", Expr: `CHECK (status <> 'room')`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "WARNING") {
			t.Errorf("did not expect a constraint WARNING — the string literal 'room' must not be mistaken for the renamed column, got: %s", sql)
		}
		if strings.Contains(sql, "DROP CONSTRAINT") || (strings.Contains(sql, "ADD") && strings.Contains(sql, "CHECK")) {
			t.Errorf("did not expect the unchanged CHECK constraint to be dropped/re-added, got: %s", sql)
		}
	}
}

// TestDiffColumnDropSuppressesCascadedConstraint verifies that when a column
// is dropped (no RENAMED FROM), an unnamed constraint on that column doesn't
// surface as a manual-drop warning — DROP COLUMN cascades to it in PG.
func TestDiffColumnDropSuppressesCascadedConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("iam.groups", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "iam",
			Name:   "groups",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "locality_id", Type: "bigint"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "", Type: "FOREIGN KEY",
					Expr: `FOREIGN KEY ("locality_id") REFERENCES "iam"."localities" ("id")`},
			},
			Indexes: []snapshot.SnapIndex{
				{Name: "groups_locality_id_idx", Method: "btree", Columns: "locality_id"},
			},
		},
	})

	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "iam",
			Name:   "groups",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var sawDropCol bool
	for _, o := range ops {
		sql := o.SQL()
		if strings.Contains(sql, "DROP COLUMN") && strings.Contains(sql, `"locality_id"`) {
			sawDropCol = true
		}
		if strings.Contains(sql, "WARNING") {
			t.Errorf("expected no WARNING for cascaded constraint, got: %s", sql)
		}
		if strings.Contains(sql, "DROP INDEX") && strings.Contains(sql, "groups_locality_id_idx") {
			t.Errorf("expected no DROP INDEX for index whose only column is dropped, got: %s", sql)
		}
	}
	if !sawDropCol {
		t.Fatalf("expected DROP COLUMN locality_id, got: %v", sqlList(ops))
	}
}

// ── SQL string-literal-aware rename translation ─────────────────────────────────

// TestSplitSQLStringLiteralsBasic proves a plain literal is correctly
// isolated from the surrounding code text.
func TestSplitSQLStringLiteralsBasic(t *testing.T) {
	segs := splitSQLStringLiterals(`status <> 'room'`)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(segs), segs)
	}
	if segs[0].isLiteral || segs[0].text != "status <> " {
		t.Errorf("segs[0] = %+v, want {\"status <> \", false}", segs[0])
	}
	if !segs[1].isLiteral || segs[1].text != "'room'" {
		t.Errorf("segs[1] = %+v, want {\"'room'\", true}", segs[1])
	}
}

// TestSplitSQLStringLiteralsEscapedQuote proves SQL's doubled-quote escape
// (” inside a literal means a literal single quote, not end-of-string) is
// handled — confirmed this is the real PostgreSQL rule, and that
// pg_query.Deparse's own output uses it (e.g. rendering "it's" as 'it”s').
// A naive split on every apostrophe would end the literal early here.
func TestSplitSQLStringLiteralsEscapedQuote(t *testing.T) {
	segs := splitSQLStringLiterals(`name = 'it''s room'`)
	if len(segs) != 2 {
		t.Fatalf("got %d segments, want 2: %+v", len(segs), segs)
	}
	if !segs[1].isLiteral || segs[1].text != `'it''s room'` {
		t.Errorf("segs[1] = %+v, want the whole escaped literal as one segment", segs[1])
	}
}

// TestSplitSQLStringLiteralsMultipleLiterals proves code text between two
// literals is correctly re-isolated as its own non-literal segment.
func TestSplitSQLStringLiteralsMultipleLiterals(t *testing.T) {
	segs := splitSQLStringLiterals(`a = 'x' AND b = 'y'`)
	var gotLiterals, gotCode []string
	for _, seg := range segs {
		if seg.isLiteral {
			gotLiterals = append(gotLiterals, seg.text)
		} else {
			gotCode = append(gotCode, seg.text)
		}
	}
	if len(gotLiterals) != 2 || gotLiterals[0] != "'x'" || gotLiterals[1] != "'y'" {
		t.Errorf("literals: got %v, want ['x'] ['y']", gotLiterals)
	}
	if len(gotCode) != 2 || gotCode[0] != "a = " || gotCode[1] != " AND b = " {
		t.Errorf("code segments: got %v", gotCode)
	}
}

// TestSplitSQLStringLiteralsNoLiteral proves text with no literal at all
// round-trips as a single non-literal segment.
func TestSplitSQLStringLiteralsNoLiteral(t *testing.T) {
	segs := splitSQLStringLiterals(`room > 0`)
	if len(segs) != 1 || segs[0].isLiteral || segs[0].text != "room > 0" {
		t.Errorf("got %+v, want a single non-literal segment", segs)
	}
}

// ── Unnamed-constraint / live-generated-name matching ─────────────────────────
//
// Regression guard for a bug found reviewing the diff-coverage-push session:
// PostgreSQL's pg_constraint.conname is NEVER empty (it auto-generates a name
// like "t_pkey" even when the user wrote none), so a live-introspected
// unnamed inline PK/UNIQUE/FK always keyed as "n:<real name>", while the
// desired IR (still unnamed in source) keyed as "s:<type>|<expr>" — the two
// never matched, so verify/plan --live produced a self-inconsistent
// DROP+ADD pair on every single run for any inline unnamed PK/UNIQUE/FK.
// pgAutoConstraintName reconstructs PostgreSQL's actual auto-naming
// algorithm (ChooseConstraintName/makeObjectName, ported from PG's own C
// source) so an unnamed desired constraint can be matched against the real
// generated name directly.

func TestMakeObjectNamePrimaryKey(t *testing.T) {
	if got := makeObjectName("orders", "", "pkey"); got != "orders_pkey" {
		t.Errorf("makeObjectName(orders, \"\", pkey) = %q, want orders_pkey", got)
	}
}

func TestMakeObjectNameUnique(t *testing.T) {
	if got := makeObjectName("orders", "user_id", "key"); got != "orders_user_id_key" {
		t.Errorf("makeObjectName(orders, user_id, key) = %q, want orders_user_id_key", got)
	}
}

// TestMakeObjectNameTruncation guards PG's actual truncation rule: when
// name1_name2_label would exceed NAMEDATALEN-1 (63) bytes, the longer of
// name1/name2 is shortened first, one byte at a time, alternating — the
// label is never touched. Verified against PostgreSQL's own C source
// (src/backend/commands/indexcmds.c makeObjectName), not from memory.
func TestMakeObjectNameTruncation(t *testing.T) {
	name1 := strings.Repeat("a", 40)
	name2 := strings.Repeat("b", 40)
	got := makeObjectName(name1, name2, "key")
	if len(got) > 63 {
		t.Fatalf("generated name exceeds NAMEDATALEN-1 (63 bytes): %d bytes: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "_key") {
		t.Errorf("label must never be truncated, got: %q", got)
	}
	// availchars = 63 - 1(sep) - 3(key) - 1(sep) = 58; split evenly since
	// both names start equal length: 29 + 29.
	wantName1 := strings.Repeat("a", 29)
	wantName2 := strings.Repeat("b", 29)
	want := wantName1 + "_" + wantName2 + "_key"
	if got != want {
		t.Errorf("makeObjectName truncation = %q, want %q", got, want)
	}
}

func TestMakeObjectNameMultiByteSafeTruncation(t *testing.T) {
	// A multi-byte (3-byte UTF-8) rune landing exactly on the truncation
	// boundary must not be split — mbClipLen must round the cut down to the
	// nearest complete rune, mirroring PG's pg_mbcliplen.
	name1 := strings.Repeat("a", 55) + "日本語" // 55 ASCII + 3 three-byte runes = 64 bytes
	got := makeObjectName(name1, "", "pkey")
	if !utf8.ValidString(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
}

func TestPgAutoConstraintNameCollisionSuffix(t *testing.T) {
	existing := map[string]bool{"orders_pkey": true}
	got := pgAutoConstraintName("orders", nil, "pkey", existing)
	if got != "orders_pkey1" {
		t.Errorf("expected collision suffix orders_pkey1, got: %q", got)
	}
	if !existing["orders_pkey1"] {
		t.Error("expected pgAutoConstraintName to record the chosen name in existingNames")
	}
}

// TestDiffUnnamedPrimaryKeyMatchesLiveGeneratedName is the core regression
// guard: an inline `id BIGINT PRIMARY KEY` (unnamed in source) must produce
// zero ops against a snapshot shaped like live introspection — real
// generated name "orders_pkey", never empty.
func TestDiffUnnamedPrimaryKeyMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint", NotNull: true}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_pkey", Type: "PRIMARY KEY", Expr: `PRIMARY KEY ("id")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true}},
			Constraints: []*ir.Constraint{
				{Type: "PRIMARY KEY", Columns: []string{"id"}, Expr: `PRIMARY KEY ("id")`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed PK must match live-generated orders_pkey), got: %v", sqlList(ops))
	}
}

func TestDiffUnnamedUniqueMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "external_id", Type: "text"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_external_id_key", Type: "UNIQUE", Expr: `UNIQUE ("external_id")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "external_id", Type: ir.TypeRef{Name: "text"}}},
			Constraints: []*ir.Constraint{
				{Type: "UNIQUE", Columns: []string{"external_id"}, Expr: `UNIQUE ("external_id")`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed UNIQUE must match live-generated orders_external_id_key), got: %v", sqlList(ops))
	}
}

func TestDiffUnnamedForeignKeyMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "user_id", Type: "bigint"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_user_id_fkey", Type: "FOREIGN KEY",
					Expr: `FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "user_id", Type: ir.TypeRef{Name: "bigint"}}},
			Constraints: []*ir.Constraint{
				{Type: "FOREIGN KEY", Columns: []string{"user_id"},
					Expr: `FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id")`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed FK must match live-generated orders_user_id_fkey), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedCheckMatchesLiveGeneratedName proves an unnamed CHECK whose
// expression references exactly one distinct column matches PG's real
// generated name ("orders_amount_check" — heap.c's single-Var name2 rule).
func TestDiffUnnamedCheckMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_amount_check", Type: "CHECK", Expr: `CHECK ((amount > 0))`},
			},
		},
	})
	col := "amount"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "CHECK", CheckColumn: &col, Expr: `CHECK (amount > 0)`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed CHECK must match live-generated orders_amount_check), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedCheckMultiColumnMatchesLiveGeneratedName covers the other
// branch of heap.c's rule: when more than one distinct column is
// referenced, name2 is omitted entirely ("orders_check", not
// "orders_a_b_check").
func TestDiffUnnamedCheckMultiColumnMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}, {Name: "b", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_check", Type: "CHECK", Expr: `CHECK (((a > 0) AND (b > 0)))`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}, {Name: "b", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "CHECK", Expr: `CHECK (a > 0 AND b > 0)`}, // CheckColumn nil: 2 distinct columns
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed multi-column CHECK must match live-generated orders_check), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedCheckRemovalStillDetected proves the fix doesn't paper over
// a genuinely removed CHECK constraint.
func TestDiffUnnamedCheckRemovalStillDetected(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_amount_check", Type: "CHECK", Expr: `CHECK ((amount > 0))`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "integer"}}},
			// No CHECK constraint at all — genuinely removed.
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP CONSTRAINT") || !containsSQL(ops, `orders_amount_check`) {
		t.Errorf("expected DROP CONSTRAINT orders_amount_check for the removed CHECK, got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedCheckDifferentColumnDetectedAsChange proves a single-column
// unnamed CHECK is NOT subject to the same identity-only blind spot as an
// unnamed PRIMARY KEY (TestDiffUnnamedPrimaryKeyRemovalStillDetected's doc
// comment): unlike a PK's name, which never encodes columns at all, a
// single-column CHECK's predicted name DOES encode its column — so moving
// the check to a different column produces a genuinely different predicted
// name, correctly surfacing as DROP old + ADD new rather than a silent
// no-op.
func TestDiffUnnamedCheckDifferentColumnDetectedAsChange(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}, {Name: "b", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_a_check", Type: "CHECK", Expr: `CHECK ((a > 0))`},
			},
		},
	})
	colB := "b"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}, {Name: "b", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "CHECK", CheckColumn: &colB, Expr: `CHECK (b > 0)`},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP CONSTRAINT") || !containsSQL(ops, "orders_a_check") {
		t.Errorf("expected DROP CONSTRAINT orders_a_check, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "ADD") || !containsSQL(ops, "b > 0") {
		t.Errorf("expected ADD for the new b-based CHECK, got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedPrimaryKeyRemovalStillDetected proves the fix doesn't paper
// over a genuinely removed PK: when desired drops the PRIMARY KEY
// constraint entirely, a live-style "orders_pkey" in the snapshot must still
// surface as a real DROP CONSTRAINT.
//
// Note a known, pre-existing limitation this fix inherits rather than
// introduces: matching here is identity-only (by name, or — for an unnamed
// constraint — by predicted name), never full-definition equality. This
// already applied to any hand-named constraint before this fix (redefining
// a CHECK's expression while keeping its explicit name goes undetected the
// same way); it now also applies to an unnamed PK specifically, since a
// PRIMARY KEY's PG-generated name never encodes its columns at all (a table
// has only one PK), so an unnamed PK moved to a different column on both
// sides is not something the tool can distinguish from unchanged. Genuine
// removal (this test) or a change of TYPE (a different test could add
// UNIQUE where a PK used to be) are still caught correctly, since those
// change which map entry — if any — the snap constraint matches.
func TestDiffUnnamedPrimaryKeyRemovalStillDetected(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_pkey", Type: "PRIMARY KEY", Expr: `PRIMARY KEY ("id")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			// No PRIMARY KEY constraint at all — genuinely removed.
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP CONSTRAINT") || !containsSQL(ops, `orders_pkey`) {
		t.Errorf("expected DROP CONSTRAINT orders_pkey for the removed PK, got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedConstraintSchemaWideCollision proves the collision universe
// is genuinely schema-wide, not just same-table: a second table in the same
// schema already using the name "widgets_pkey" would force PostgreSQL's own
// algorithm to fall back to "orders_pkey1" ONLY if the two tables' own
// generated names actually collided — this test instead checks the more
// realistic case (two DIFFERENT unnamed PKs on two different tables produce
// two DIFFERENT names, since name1 embeds the table name) to confirm
// schemaConstraintNames doesn't cause false cross-table collisions.
func TestDiffUnnamedConstraintNoFalseCrossTableCollision(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "widgets_pkey", Type: "PRIMARY KEY", Expr: `PRIMARY KEY ("id")`},
			},
		},
	})
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_pkey", Type: "PRIMARY KEY", Expr: `PRIMARY KEY ("id")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "widgets",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true}},
			Constraints: []*ir.Constraint{{Type: "PRIMARY KEY", Columns: []string{"id"}, Expr: `PRIMARY KEY ("id")`}},
		},
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true}},
			Constraints: []*ir.Constraint{{Type: "PRIMARY KEY", Columns: []string{"id"}, Expr: `PRIMARY KEY ("id")`}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for both tables' unnamed PKs matching their own distinct generated names, got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedPrimaryKeyRelationNameCollision is the regression guard for
// a gap an independent verification pass found live: PostgreSQL's
// ChooseRelationName (used for PRIMARY KEY/UNIQUE, since both are
// index-backed) checks for a naming collision against ANY relation in the
// schema via pg_class — not just other constraint names. A plain table
// literally named "bar_pkey" forces PG to fall back to "bar_pkey1" for an
// unnamed PK on table "bar", even though no OTHER CONSTRAINT is named
// "bar_pkey". schemaConstraintNames alone (constraint names only) would
// miss this and predict "bar_pkey", silently reintroducing the exact
// self-inconsistent DROP+ADD bug this feature exists to eliminate.
// schemaRelationNames closes this by also collecting table/view/sequence/
// index names in the schema.
func TestDiffUnnamedPrimaryKeyRelationNameCollision(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// A plain, unrelated table that happens to be named exactly what
	// table "bar"'s auto-generated PK name would otherwise be.
	_ = snap.SetObject("public.bar_pkey", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "bar_pkey",
			Columns: []snapshot.SnapColumn{{Name: "x", Type: "bigint"}},
		},
	})
	_ = snap.SetObject("public.bar", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "bar",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Constraints: []snapshot.SnapConstraint{
				// The name PG actually assigns once "bar_pkey" the relation
				// is taken: it falls back to the next available suffix.
				{Name: "bar_pkey1", Type: "PRIMARY KEY", Expr: `PRIMARY KEY ("id")`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "bar_pkey",
			Columns: []*ir.Column{{Name: "x", Type: ir.TypeRef{Name: "bigint"}}},
		},
		&ir.Table{
			Schema: "public", Name: "bar",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true}},
			Constraints: []*ir.Constraint{{Type: "PRIMARY KEY", Columns: []string{"id"}, Expr: `PRIMARY KEY ("id")`}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops: unnamed PK on \"bar\" must predict \"bar_pkey1\" (relation-name collision with table \"bar_pkey\"), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedCheckNoRelationNameCollision proves CHECK's collision
// universe stays constraint-names-only, unlike PRIMARY KEY/UNIQUE: CHECK
// isn't index-backed, so its real default-naming path (ChooseConstraintName,
// heap.c) never scans pg_class — a plain table named "orders_amount_check"
// must NOT force a predicted CHECK name to fall back to a "1" suffix.
func TestDiffUnnamedCheckNoRelationNameCollision(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders_amount_check", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders_amount_check",
			Columns: []snapshot.SnapColumn{{Name: "x", Type: "bigint"}},
		},
	})
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "amount", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_amount_check", Type: "CHECK", Expr: `CHECK ((amount > 0))`},
			},
		},
	})
	col := "amount"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders_amount_check",
			Columns: []*ir.Column{{Name: "x", Type: ir.TypeRef{Name: "bigint"}}},
		},
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns:     []*ir.Column{{Name: "amount", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{{Type: "CHECK", CheckColumn: &col, Expr: `CHECK (amount > 0)`}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops: CHECK naming must not collide with an unrelated relation name, got: %v", sqlList(ops))
	}
}

// ── Unnamed EXCLUDE / live-generated-name matching ──────────────────────────────

// TestDiffUnnamedExcludeMatchesLiveGeneratedName proves an unnamed EXCLUDE
// whose every element is a plain column matches PG's real generated name
// (confirmed live: "bookings_room_during_excl" for
// EXCLUDE USING gist (room WITH =, during WITH &&) on table "bookings" —
// heap.c/indexcmds.c's name2-is-every-colname-joined-by-underscore rule,
// same as UNIQUE).
func TestDiffUnnamedExcludeMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.bookings", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "bookings",
			Columns: []snapshot.SnapColumn{
				{Name: "room", Type: "integer"},
				{Name: "during", Type: "tsrange"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "bookings_room_during_excl", Type: "EXCLUDE",
					Expr: `EXCLUDE USING gist ("room" WITH =, "during" WITH &&)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "bookings",
			Columns: []*ir.Column{
				{Name: "room", Type: ir.TypeRef{Name: "integer"}},
				{Name: "during", Type: ir.TypeRef{Name: "tsrange"}},
			},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE", Columns: []string{"room", "during"},
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "gist",
						Elements: []ir.ExcludeElement{
							{Column: "room", Operator: "="},
							{Column: "during", Operator: "&&"},
						},
					},
					Expr: `EXCLUDE USING gist ("room" WITH =, "during" WITH &&)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed EXCLUDE must match live-generated bookings_room_during_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeSingleColumnMatchesLiveGeneratedName covers the
// single-element case (confirmed live: "singles_a_excl" for
// EXCLUDE (a WITH =) on table "singles").
func TestDiffUnnamedExcludeSingleColumnMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.singles", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "singles",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "singles_a_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ("a" WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "singles",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE", Columns: []string{"a"},
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements:     []ir.ExcludeElement{{Column: "a", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree ("a" WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed EXCLUDE must match live-generated singles_a_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeRemovalStillDetected proves the fix doesn't paper
// over a genuinely removed EXCLUDE constraint.
func TestDiffUnnamedExcludeRemovalStillDetected(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.singles", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "singles",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "singles_a_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ("a" WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "singles",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			// No EXCLUDE constraint at all — genuinely removed.
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP CONSTRAINT") || !containsSQL(ops, "singles_a_excl") {
		t.Errorf("expected DROP CONSTRAINT singles_a_excl for the removed EXCLUDE, got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeExpressionElementMatchesLiveGeneratedName proves an
// EXCLUDE with a bare, uncast operator-expression element (not a plain
// column, function call, or cast) is still correctly matched against
// PostgreSQL's real generated name. PredictedName is empty at the IR layer
// for this shape (a syntax-only pass can't derive anything from "a + b"),
// but PostgreSQL's own ChooseIndexColumnNames (indexcmds.c) falls back to
// the literal string "expr" whenever an element's indexcolname is unset —
// confirmed live against PG 17: EXCLUDE USING btree ((a + b) WITH =) on
// table "t" really does generate "t_expr_excl". predictName's "excl" case
// reconstructs that same literal fallback, so this is no longer an
// unpredictable shape.
func TestDiffUnnamedExcludeExpressionElementMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}, {Name: "b", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_expr_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ((a + b) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}, {Name: "b", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						// PredictedName intentionally left unset — "a + b" is
						// a bare, uncast operator expression, matching what
						// buildConstraint would actually produce for it.
						Elements: []ir.ExcludeElement{{Expr: "a + b", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree ((a + b) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed EXCLUDE must match live-generated t_expr_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeExpressionElementsDeduped proves two bare-expression
// elements on the same unnamed EXCLUDE dedup exactly like two same-named
// function-call elements do (TestDiffUnnamedExcludeSameFuncNameElementsDeduped)
// — confirmed live: EXCLUDE USING gist ((a + b) WITH =, (c * d) WITH =) on
// table "t2" generates "t2_expr_expr1_excl".
func TestDiffUnnamedExcludeExpressionElementsDeduped(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t2", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "t2",
			Columns: []snapshot.SnapColumn{
				{Name: "a", Type: "integer"}, {Name: "b", Type: "integer"},
				{Name: "c", Type: "integer"}, {Name: "d", Type: "integer"},
			},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t2_expr_expr1_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING gist ((a + b) WITH =, (c * d) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t2",
			Columns: []*ir.Column{
				{Name: "a", Type: ir.TypeRef{Name: "integer"}}, {Name: "b", Type: ir.TypeRef{Name: "integer"}},
				{Name: "c", Type: ir.TypeRef{Name: "integer"}}, {Name: "d", Type: ir.TypeRef{Name: "integer"}},
			},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "gist",
						Elements: []ir.ExcludeElement{
							{Expr: "a + b", Operator: "="},
							{Expr: "c * d", Operator: "="},
						},
					},
					Expr: `EXCLUDE USING gist ((a + b) WITH =, (c * d) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (must predict deduped t2_expr_expr1_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeFuncCallElementMatchesLiveGeneratedName proves an
// unnamed EXCLUDE with a bare, top-level function-call element predicts
// PostgreSQL's real generated name (confirmed live: "t_lower_excl" for
// EXCLUDE ((lower(a)) WITH =) on table "t") — this is the case an earlier
// pass of this feature incorrectly excluded as "requires OID resolution,
// infeasible offline"; the exclusion's premise didn't hold once verified
// live: PostgreSQL's real algorithm only ever uses the bare function name
// for such an element, never descending into its arguments, so no OID
// lookup is actually needed.
func TestDiffUnnamedExcludeFuncCallElementMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "text"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_lower_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ((lower(a)) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "text"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements:     []ir.ExcludeElement{{Expr: "lower(a)", PredictedName: "lower", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree ((lower(a)) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed function-call EXCLUDE must match live-generated t_lower_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeSameFuncNameElementsDeduped proves two elements
// that independently derive the SAME function name (different columns,
// same function) get PostgreSQL's own per-element disambiguating suffix —
// confirmed live: EXCLUDE ((lower(a)) WITH =, (lower(b)) WITH =) on table
// "t" generates "t_lower_lower1_excl", not "t_lower_lower_excl".
func TestDiffUnnamedExcludeSameFuncNameElementsDeduped(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "text"}, {Name: "b", Type: "text"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_lower_lower1_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ((lower(a)) WITH =, (lower(b)) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "text"}}, {Name: "b", Type: ir.TypeRef{Name: "text"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements: []ir.ExcludeElement{
							{Expr: "lower(a)", PredictedName: "lower", Operator: "="},
							{Expr: "lower(b)", PredictedName: "lower", Operator: "="},
						},
					},
					Expr: `EXCLUDE USING btree ((lower(a)) WITH =, (lower(b)) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (must predict deduped t_lower_lower1_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeMixedColumnAndFuncCallElements proves a mix of a
// plain column element and a function-call element predicts correctly
// (confirmed live: "t_a_lower_excl" for EXCLUDE (a WITH =, (lower(b)) WITH =)).
func TestDiffUnnamedExcludeMixedColumnAndFuncCallElements(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}, {Name: "b", Type: "text"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_a_lower_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ("a" WITH =, (lower(b)) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}, {Name: "b", Type: ir.TypeRef{Name: "text"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE", Columns: []string{"a"},
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements: []ir.ExcludeElement{
							{Column: "a", Operator: "="},
							{Expr: "lower(b)", PredictedName: "lower", Operator: "="},
						},
					},
					Expr: `EXCLUDE USING btree ("a" WITH =, (lower(b)) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (must predict mixed-element t_a_lower_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeCastElementMatchesLiveGeneratedName proves a cast
// wrapping a plain column predicts PostgreSQL's real generated name
// end-to-end through the diff layer — confirmed live: "t_a_excl" for
// EXCLUDE ((a::text) WITH =) on table "t" (the column's own name wins over
// the cast's target type, since a ColumnRef is a "strong" name).
func TestDiffUnnamedExcludeCastElementMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_a_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ((a::text) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements:     []ir.ExcludeElement{{Expr: "a::text", PredictedName: "a", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree ((a::text) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed cast-over-column EXCLUDE must match live-generated t_a_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeCastOverOperatorMatchesLiveGeneratedName proves a
// cast wrapping a bare operator expression predicts the cast's OWN target
// type name — confirmed live: "t_text_excl" for
// EXCLUDE (((a + b)::text) WITH =) on table "t".
func TestDiffUnnamedExcludeCastOverOperatorMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}, {Name: "b", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_text_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree (((a + b)::text) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}, {Name: "b", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements:     []ir.ExcludeElement{{Expr: "(a + b)::text", PredictedName: "text", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree (((a + b)::text) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed cast-over-operator EXCLUDE must match live-generated t_text_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeCoalesceElementMatchesLiveGeneratedName proves a
// COALESCE element predicts PostgreSQL's real generated name end-to-end
// through the diff layer — confirmed live: "t_coalesce_excl" for
// EXCLUDE ((coalesce(a,0)) WITH =) on table "t".
func TestDiffUnnamedExcludeCoalesceElementMatchesLiveGeneratedName(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "t_coalesce_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ((coalesce(a, 0)) WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE",
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements:     []ir.ExcludeElement{{Expr: "coalesce(a, 0)", PredictedName: "coalesce", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree ((coalesce(a, 0)) WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (unnamed coalesce EXCLUDE must match live-generated t_coalesce_excl), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeRelationNameCollision is the regression guard for
// EXCLUDE's schema-wide relation-name collision scope — confirmed live: a
// plain table literally named "t2_a_excl" forces PostgreSQL to fall back to
// "t2_a_excl1" for an unnamed EXCLUDE on table "t2", even though no OTHER
// constraint is named "t2_a_excl". Mirrors
// TestDiffUnnamedPrimaryKeyRelationNameCollision's PK case exactly.
func TestDiffUnnamedExcludeRelationNameCollision(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t2_a_excl", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t2_a_excl",
			Columns: []snapshot.SnapColumn{{Name: "x", Type: "bigint"}},
		},
	})
	_ = snap.SetObject("public.t2", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t2",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				// The name PG actually assigns once "t2_a_excl" the relation
				// is taken: it falls back to the next available suffix.
				{Name: "t2_a_excl1", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ("a" WITH =)`},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "t2_a_excl",
			Columns: []*ir.Column{{Name: "x", Type: ir.TypeRef{Name: "bigint"}}},
		},
		&ir.Table{
			Schema: "public", Name: "t2",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{
				{Type: "EXCLUDE", Columns: []string{"a"},
					Exclude: &ir.ExcludeSpec{
						AccessMethod: "btree",
						Elements:     []ir.ExcludeElement{{Column: "a", Operator: "="}},
					},
					Expr: `EXCLUDE USING btree ("a" WITH =)`,
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops: unnamed EXCLUDE on \"t2\" must predict \"t2_a_excl1\" (relation-name collision with table \"t2_a_excl\"), got: %v", sqlList(ops))
	}
}

// TestDiffUnnamedExcludeNoFalseCrossTableCollision proves two different
// tables' own unnamed EXCLUDE constraints predict two DIFFERENT names
// (name1 embeds the table name), so schemaConstraintNames/
// schemaRelationNames don't cause a false cross-table collision.
func TestDiffUnnamedExcludeNoFalseCrossTableCollision(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "widgets_a_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ("a" WITH =)`},
			},
		},
	})
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "integer"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "orders_a_excl", Type: "EXCLUDE", Expr: `EXCLUDE USING btree ("a" WITH =)`},
			},
		},
	})
	excl := func() *ir.ExcludeSpec {
		return &ir.ExcludeSpec{AccessMethod: "btree", Elements: []ir.ExcludeElement{{Column: "a", Operator: "="}}}
	}
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "widgets",
			Columns:     []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{{Type: "EXCLUDE", Columns: []string{"a"}, Exclude: excl(), Expr: `EXCLUDE USING btree ("a" WITH =)`}},
		},
		&ir.Table{
			Schema: "public", Name: "orders",
			Columns:     []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "integer"}}},
			Constraints: []*ir.Constraint{{Type: "EXCLUDE", Columns: []string{"a"}, Exclude: excl(), Expr: `EXCLUDE USING btree ("a" WITH =)`}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for both tables' unnamed EXCLUDEs matching their own distinct generated names, got: %v", sqlList(ops))
	}
}

// ── Virtual type JSONB resolution ─────────────────────────────────────────────

// makeVtype is a helper to build a VirtualType with a simple type-ref body.
func makeVtype(schema, name string, body ir.VtypeBody) *ir.VirtualType {
	return &ir.VirtualType{Schema: schema, Name: name, Body: body}
}

func TestVirtualTypeNoSQL(t *testing.T) {
	// VIRTUAL TYPE declarations generate no SQL whatsoever.
	d := New()
	desired := []pipeline.IRObject{
		makeVtype("public", "user_state", ir.VtypeTypeRef{Name: "text"}),
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops for VIRTUAL TYPE, got %d: %v", len(ops), sqlList(ops))
	}
}

func TestVirtualTypeNoSQLOnChange(t *testing.T) {
	// Changes to VIRTUAL TYPE also produce no SQL.
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.user_state", &snapshot.SnapObject{
		Kind: "virtual_type",
		VirtualType: &snapshot.SnapVirtualType{
			Schema: "public",
			Name:   "user_state",
			Body:   snapshot.SnapVtypeBody{Kind: "type_ref", Name: "text"},
		},
	})
	desired := []pipeline.IRObject{
		makeVtype("public", "user_state", ir.VtypeComposite{
			Fields: []ir.VtypeField{{Name: "val", Type: ir.VtypeTypeRef{Name: "integer"}}},
		}),
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected 0 ops for VIRTUAL TYPE change, got %d", len(ops))
	}
}

// ── SERIAL/BIGSERIAL/SMALLSERIAL ────────────────────────────────────────────

func serialPtr(s string) *string { return &s }

// TestCreateTableSerialEmitsLiteralKeyword proves createTable emits the
// literal SERIAL keyword (letting PostgreSQL's own macro-expansion create
// the sequence/default/ownership/NOT NULL) rather than the resolved
// underlying type with a hand-reconstructed NOT NULL/DEFAULT.
func TestCreateTableSerialEmitsLiteralKeyword(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "widgets",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "integer"}, Serial: serialPtr("SERIAL"), NotNull: true},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, `"id" SERIAL`) {
		t.Errorf("expected literal SERIAL keyword, got: %s", sql)
	}
	if strings.Contains(sql, "NOT NULL") {
		t.Errorf("expected NOT NULL to be suppressed for SERIAL (PG implies it), got: %s", sql)
	}
	if strings.Contains(sql, "nextval") || strings.Contains(sql, "DEFAULT") {
		t.Errorf("expected no DEFAULT clause for SERIAL, got: %s", sql)
	}
}

// TestDiffColumnsAddSerialColumnCaution proves a new SERIAL column added
// to an existing table is CAUTION (filling it from the sequence rewrites
// every row, RFC Section 17.2) and emits the literal SERIAL keyword rather
// than "integer ... NOT NULL".
func TestDiffColumnsAddSerialColumnCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "name", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "widgets",
			Columns: []*ir.Column{
				{Name: "name", Type: ir.TypeRef{Name: "text"}},
				{Name: "id", Type: ir.TypeRef{Name: "integer"}, Serial: serialPtr("SERIAL"), NotNull: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var addOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD COLUMN") {
			addOp = o
			break
		}
	}
	if addOp == nil {
		t.Fatal("expected ADD COLUMN op")
	}
	if !strings.Contains(addOp.SQL(), `"id" SERIAL`) {
		t.Errorf("expected literal SERIAL keyword, got: %s", addOp.SQL())
	}
	if strings.Contains(addOp.SQL(), "NOT NULL") {
		t.Errorf("expected NOT NULL suppressed, got: %s", addOp.SQL())
	}
	if addOp.Safety() != pipeline.Caution {
		t.Errorf("expected CAUTION, got %v", addOp.Safety())
	}
}

// TestDiffColumnsLegacySerialSnapshotNoDrift is a regression guard for the
// stale-snapshot self-healing path: a pre-upgrade snapshot stores the
// literal, un-normalized "serial" type name (before Column.Serial existed).
// Without the isLegacySerialTypeName guard in diffColumns, comparing the
// new normalized "integer" against the old literal "serial" would emit a
// spurious destructive ALTER COLUMN TYPE on the very first plan after
// upgrading, even though the live column hasn't changed shape at all.
func TestDiffColumnsLegacySerialSnapshotNoDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "widgets",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "serial", NotNull: true},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "widgets",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "integer"}, Serial: serialPtr("SERIAL"), NotNull: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER COLUMN") && strings.Contains(o.SQL(), "TYPE") {
			t.Errorf("expected no ALTER COLUMN TYPE against a legacy 'serial' snapshot, got: %s", o.SQL())
		}
		if o.Safety() == pipeline.Destructive {
			t.Errorf("expected no destructive ops against a legacy 'serial' snapshot, got: %s", o.SQL())
		}
	}
}

func TestCreateTableWithVirtualTypeColumn(t *testing.T) {
	// A column typed as a virtual type must emit jsonb in CREATE TABLE.
	d := New()
	desired := []pipeline.IRObject{
		makeVtype("public", "user_profile", ir.VtypeComposite{
			Fields: []ir.VtypeField{
				{Name: "name", Type: ir.VtypeTypeRef{Name: "text"}},
				{Name: "age", Type: ir.VtypeTypeRef{Name: "integer"}},
			},
		}),
		&ir.Table{
			Schema: "public",
			Name:   "users",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "profile", Type: ir.TypeRef{Name: "user_profile"}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, "jsonb") {
		t.Errorf("expected jsonb in CREATE TABLE SQL, got: %s", combined)
	}
	if strings.Contains(combined, "user_profile") {
		t.Errorf("unexpected virtual type name in SQL (should be jsonb): %s", combined)
	}
}

func TestCreateTableWithVirtualTypeArrayColumn(t *testing.T) {
	// A column typed as virtual_type[] must emit jsonb[] in CREATE TABLE.
	d := New()
	desired := []pipeline.IRObject{
		makeVtype("public", "line_item", ir.VtypeComposite{
			Fields: []ir.VtypeField{
				{Name: "sku", Type: ir.VtypeTypeRef{Name: "text"}},
				{Name: "qty", Type: ir.VtypeTypeRef{Name: "integer"}},
			},
		}),
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "items", Type: ir.TypeRef{Name: "line_item", ArrayDims: 1}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, "jsonb[]") {
		t.Errorf("expected jsonb[] in CREATE TABLE SQL, got: %s", combined)
	}
}

func TestCreateTableWithSchemaQualifiedVirtualTypeColumn(t *testing.T) {
	// Schema-qualified virtual type reference: billing.payment_method → jsonb.
	d := New()
	desired := []pipeline.IRObject{
		makeVtype("billing", "payment_method", ir.VtypeTypeRef{Name: "text"}),
		&ir.Table{
			Schema: "public",
			Name:   "invoices",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "payment", Type: ir.TypeRef{Schema: "billing", Name: "payment_method"}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, "jsonb") {
		t.Errorf("expected jsonb in CREATE TABLE SQL for schema-qualified virtual type, got: %s", combined)
	}
}

func TestAddColumnWithVirtualTypeResolvesToJsonb(t *testing.T) {
	// ADD COLUMN with a virtual type reference must use jsonb.
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
			},
		},
	})
	desired := []pipeline.IRObject{
		makeVtype("public", "line_item", ir.VtypeComposite{
			Fields: []ir.VtypeField{{Name: "sku", Type: ir.VtypeTypeRef{Name: "text"}}},
		}),
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "items", Type: ir.TypeRef{Name: "line_item", ArrayDims: 1}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, "ADD COLUMN") || !strings.Contains(combined, "jsonb[]") {
		t.Errorf("expected ADD COLUMN ... jsonb[], got: %s", combined)
	}
}

func TestCompositeTypeWithVirtualTypeAttrResolvesToJsonb(t *testing.T) {
	// A composite type attribute typed as a virtual type must emit jsonb.
	d := New()
	desired := []pipeline.IRObject{
		makeVtype("public", "address_detail", ir.VtypeComposite{
			Fields: []ir.VtypeField{{Name: "line", Type: ir.VtypeTypeRef{Name: "text"}}},
		}),
		&ir.Type{
			Schema:  "public",
			Name:    "full_address",
			Variant: "COMPOSITE",
			CompositeAttrs: []*ir.Column{
				{Name: "street", Type: ir.TypeRef{Name: "text"}},
				{Name: "detail", Type: ir.TypeRef{Name: "address_detail"}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, "jsonb") {
		t.Errorf("expected jsonb in CREATE TYPE SQL for virtual type attr, got: %s", combined)
	}
	if strings.Contains(combined, "address_detail") {
		t.Errorf("unexpected virtual type name in SQL: %s", combined)
	}
}

func TestCreateTableVirtualTypePreferredJson(t *testing.T) {
	// PREFERRED JSON FORMAT json → column emits json, not jsonb.
	d := New()
	desired := []pipeline.IRObject{
		&ir.VirtualType{
			Schema:     "public",
			Name:       "event_payload",
			Body:       ir.VtypeTypeRef{Name: "text"},
			JsonFormat: "json",
		},
		&ir.Table{
			Schema: "public",
			Name:   "events",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "payload", Type: ir.TypeRef{Name: "event_payload"}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, `"payload" json`) {
		t.Errorf("expected 'json' column type for PREFERRED JSON FORMAT json, got: %s", combined)
	}
	if strings.Contains(combined, `"payload" jsonb`) {
		t.Errorf("unexpected 'jsonb' in SQL for json-preferred virtual type: %s", combined)
	}
}

func TestCreateTableVirtualTypePreferredJsonArray(t *testing.T) {
	// PREFERRED JSON FORMAT json with [] → json[].
	d := New()
	desired := []pipeline.IRObject{
		&ir.VirtualType{
			Schema:     "public",
			Name:       "event_payload",
			Body:       ir.VtypeTypeRef{Name: "text"},
			JsonFormat: "json",
		},
		&ir.Table{
			Schema: "public",
			Name:   "events",
			Columns: []*ir.Column{
				{Name: "payloads", Type: ir.TypeRef{Name: "event_payload", ArrayDims: 1}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, "json[]") {
		t.Errorf("expected json[] for json-preferred virtual type array, got: %s", combined)
	}
	if strings.Contains(combined, "jsonb[]") {
		t.Errorf("unexpected jsonb[] for json-preferred virtual type: %s", combined)
	}
}

func TestCreateTableVirtualTypeDefaultIsJsonb(t *testing.T) {
	// No PREFERRED JSON FORMAT → defaults to jsonb.
	d := New()
	desired := []pipeline.IRObject{
		&ir.VirtualType{Schema: "public", Name: "tag", Body: ir.VtypeTypeRef{Name: "text"}},
		&ir.Table{
			Schema: "public", Name: "items",
			Columns: []*ir.Column{
				{Name: "meta", Type: ir.TypeRef{Name: "tag"}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.Join(sqlList(ops), " ")
	if !strings.Contains(combined, `"meta" jsonb`) {
		t.Errorf("expected jsonb default for virtual type, got: %s", combined)
	}
}

func TestNonVirtualTypeColumnNotAffected(t *testing.T) {
	// A real PG type column (e.g. text) must not be changed to jsonb.
	d := New()
	desired := []pipeline.IRObject{
		makeVtype("public", "tag", ir.VtypeTypeRef{Name: "text"}),
		&ir.Table{
			Schema: "public",
			Name:   "products",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "name", Type: ir.TypeRef{Name: "text"}},
			},
		},
	}
	if err := ir.ResolveVirtualTypes(desired, "public"); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "\"name\" jsonb") {
			t.Errorf("plain text column was wrongly resolved to jsonb: %s", op.SQL())
		}
	}
}

func sqlList(ops []pipeline.DiffOp) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = o.SQL()
	}
	return out
}

// containsSQL returns true if any op's SQL contains substr.
func containsSQL(ops []pipeline.DiffOp, substr string) bool {
	for _, o := range ops {
		if strings.Contains(o.SQL(), substr) {
			return true
		}
	}
	return false
}

// findOpSQL returns the DiffOp whose SQL contains substr, or nil.
func findOpSQL(ops []pipeline.DiffOp, substr string) pipeline.DiffOp {
	for _, o := range ops {
		if strings.Contains(o.SQL(), substr) {
			return o
		}
	}
	return nil
}

// TestDiffTableGrantRemovedIsCaution guards RFC audit item #25: an implicit
// revoke (a GRANT simply removed from source) can break another role's
// dependent access exactly like an explicit REVOKE does, so it must be
// classified cautionOp — matching DEFAULT PRIVILEGES's already-correct
// handling of the identical real-world event, not safeOp like it was before
// this fix.
func TestDiffTableGrantRemovedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Grants:  []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, "REVOKE SELECT ON TABLE")
	if op == nil {
		t.Fatalf("expected REVOKE SELECT ON TABLE, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected implicit revoke to be Caution, got %s", op.Safety())
	}
}

// TestDiffColumnGrantRemovedIsCaution is the column-level counterpart of
// TestDiffTableGrantRemovedIsCaution (RFC audit item #25).
func TestDiffColumnGrantRemovedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "orders",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "email", Type: "text", Grants: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}}},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "email", Type: ir.TypeRef{Name: "text"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, `REVOKE SELECT ("email")`)
	if op == nil {
		t.Fatalf("expected column-level REVOKE SELECT, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected implicit column-level revoke to be Caution, got %s", op.Safety())
	}
}

// TestDiffPolicyRemovedIsCaution guards RFC Section 17.2: dropping a
// RESTRICTIVE policy widens access, so DROP POLICY is Caution.
func TestDiffPolicyRemovedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "owner_only", Command: "ALL"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, `DROP POLICY IF EXISTS "owner_only"`)
	if op == nil {
		t.Fatalf("expected DROP POLICY for removed policy, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected DROP POLICY to be Caution, got %s", op.Safety())
	}
}

// TestDiffPermissivePolicyRemovedIsSafe guards RFC Section 17.2: dropping a
// PERMISSIVE policy only narrows access, so DROP POLICY is Safe.
func TestDiffPermissivePolicyRemovedIsSafe(t *testing.T) {
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:   "public",
			Name:     "t",
			Columns:  []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{{Name: "readers", Command: "SELECT", Permissive: true}},
		},
	})
	desired := []pipeline.IRObject{&ir.Table{Schema: "public", Name: "t",
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}}}}
	ops, err := New().Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, `DROP POLICY IF EXISTS "readers"`)
	if op == nil || op.Safety() != pipeline.Safe {
		t.Fatalf("expected a Safe DROP POLICY, got: %v", sqlList(ops))
	}
}

// TestDiffTriggerRemovedIsCaution guards RFC audit item #26: a dropped
// trigger silently stops enforcing whatever business logic it implemented —
// so DROP TRIGGER must be cautionOp, not safeOp.
func TestDiffTriggerRemovedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`)
	if op == nil {
		t.Fatalf("expected DROP TRIGGER for removed trigger, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected DROP TRIGGER to be Caution, got %s", op.Safety())
	}
}

func TestDiffTableGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Grants:  []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT SELECT ON TABLE") {
		t.Errorf("expected GRANT SELECT ON TABLE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `"readonly"`) {
		t.Errorf("expected quoted role name, got: %v", sqlList(ops))
	}
}

func TestDiffTableGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Grants:  []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE SELECT ON TABLE") {
		t.Errorf("expected REVOKE SELECT ON TABLE, got: %v", sqlList(ops))
	}
}

// TestRoleListPublicNotQuoted guards a real bug found live-testing a demo
// project: roleList quoted every role name unconditionally, including the
// PostgreSQL pseudo-role keyword PUBLIC. Quoting it makes PG look up an
// actual role literally named "PUBLIC" (which never exists), erroring with
// `role "PUBLIC" does not exist` — hit live via introspection's grant
// queries, which emit the literal string "PUBLIC" as the grantee for PG's
// own implicit default EXECUTE-to-PUBLIC grant on a new function/aggregate.
func TestRoleListPublicNotQuoted(t *testing.T) {
	got := roleList([]string{"PUBLIC", "app_service", "public"})
	want := `PUBLIC, "app_service", PUBLIC`
	if got != want {
		t.Errorf("roleList: got %q, want %q", got, want)
	}
}

// TestDiffTableGrantRemovedFromPublic is TestDiffTableGrantRemoved's
// PUBLIC-specific sibling: the emitted REVOKE must not quote PUBLIC.
func TestDiffTableGrantRemovedFromPublic(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Grants:  []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"PUBLIC"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `REVOKE SELECT ON TABLE "public"."orders" FROM PUBLIC;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q (PUBLIC unquoted), got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, `"PUBLIC"`) {
		t.Errorf("PUBLIC must not be quoted, got: %v", sqlList(ops))
	}
}

func TestDiffTableGrantUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Grants:  []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Grants:  []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "GRANT") || containsSQL(ops, "REVOKE") {
		t.Errorf("expected no GRANT/REVOKE for unchanged grant, got: %v", sqlList(ops))
	}
}

// ── Explicit REVOKES (RFC Section 11.3) ──────────────────────────────────────────
//
// Regression guard for the discovery that ir.Table/Column/View.Revocations was
// parsed into the IR but never read anywhere in diff/create/emit — an explicit
// REVOKE was a total silent no-op regardless of Mode A/B syntax. These
// tests cover: emission on a brand-new object, emission when a REVOKE is
// newly added to an already-applied object, restoration (re-GRANT) when a
// REVOKE is removed, and idempotency (unchanged REVOKE => zero ops,
// so a stored revocation isn't re-issued — PG errors on REVOKE of an
// already-absent privilege per the RFC).

func TestDiffCreateTableEmitsRevocation(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Revocations: []ir.Revocation{{Privileges: []string{"UPDATE"}, Roles: []string{"readonly"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE UPDATE ON TABLE") {
		t.Errorf("expected REVOKE UPDATE ON TABLE at create time, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `"readonly"`) {
		t.Errorf("expected quoted role name, got: %v", sqlList(ops))
	}
}

func TestDiffTableRevocationAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Revocations: []ir.Revocation{{Privileges: []string{"UPDATE"}, Roles: []string{"readonly"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "REVOKE UPDATE ON TABLE") {
			found = true
			if o.Safety() != pipeline.Caution {
				t.Errorf("explicit revocation safety = %v, want Caution: %s", o.Safety(), o.SQL())
			}
		}
	}
	if !found {
		t.Errorf("expected REVOKE UPDATE ON TABLE, got: %v", sqlList(ops))
	}
}

func TestDiffTableRevocationRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"UPDATE"}, Roles: []string{"readonly"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT UPDATE ON TABLE") {
		t.Errorf("expected GRANT UPDATE ON TABLE to restore the revoked privilege, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE") {
		t.Errorf("must not also emit REVOKE when the revocation itself was removed: %v", sqlList(ops))
	}
}

// The idempotency guard: PG errors on REVOKE of an already-absent privilege
// (RFC Section 11.3), so an unchanged REVOKE must produce zero ops once it's
// been applied and recorded in the snapshot — not re-issued on every apply.
func TestDiffTableRevocationUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"UPDATE"}, Roles: []string{"readonly"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Revocations: []ir.Revocation{{Privileges: []string{"UPDATE"}, Roles: []string{"readonly"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "GRANT") || containsSQL(ops, "REVOKE") {
		t.Errorf("expected no GRANT/REVOKE for unchanged revocation, got: %v", sqlList(ops))
	}
}

func TestDiffTableRevocationCascade(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Revocations: []ir.Revocation{
				{Privileges: []string{"UPDATE"}, Roles: []string{"readonly"}, Cascade: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE UPDATE ON TABLE") || !containsSQL(ops, "CASCADE") {
		t.Errorf("expected REVOKE ... CASCADE, got: %v", sqlList(ops))
	}
}

func TestDiffViewGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v_active", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users WHERE active",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users WHERE active",
			Grants: []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"api"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT SELECT ON TABLE") {
		t.Errorf("expected GRANT SELECT ON TABLE for view, got: %v", sqlList(ops))
	}
}

func TestDiffViewGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v_active", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users WHERE active",
			Grants: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"api"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users WHERE active",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE SELECT ON TABLE") {
		t.Errorf("expected REVOKE SELECT ON TABLE for view, got: %v", sqlList(ops))
	}
}

func TestDiffCreateViewEmitsRevocation(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:      "public",
			Name:        "v_active",
			Query:       "SELECT id FROM users WHERE active",
			Revocations: []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE SELECT ON TABLE") {
		t.Errorf("expected REVOKE SELECT ON TABLE for a new view, got: %v", sqlList(ops))
	}
}

// TestDiffCreateViewEmitsOwner guards a real bug found live-testing a demo
// project: ir.View.Owner (and snapshot.SnapView.Owner) both existed and were
// populated by the builder, but createView/diffView never referenced Owner
// at all — an OWNER directive on a VIEW, RECURSIVE VIEW, or MATERIALIZED
// VIEW silently vanished with no error, no diff, nothing.
//
// RFC Section 11.5 (audit item #28) changed how a new object's OWNER is applied: a
// brand-new object is now created directly as its declared owner via
// SET ROLE/RESET ROLE, not created as the connecting role and reassigned
// afterward via a trailing ALTER ... OWNER TO — the latter never satisfies a
// matching DEFAULT PRIVILEGES FOR ROLE block, since PostgreSQL attributes
// default-privilege eligibility to whoever executed CREATE, not to final
// ownership.
func TestDiffCreateViewEmitsOwner(t *testing.T) {
	d := New()
	owner := "app_role"
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "v_active", Query: "SELECT id FROM users", Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "app_role";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE VIEW, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER VIEW ... OWNER TO for a brand-new view, got: %v", sqlList(ops))
	}
}

// TestDiffCreateMaterializedViewEmitsOwner is the materialized-view sibling
// of TestDiffCreateViewEmitsOwner.
func TestDiffCreateMaterializedViewEmitsOwner(t *testing.T) {
	d := New()
	owner := "app_role"
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "mv_totals", Materialized: true, Query: "SELECT 1", Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "app_role";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE MATERIALIZED VIEW, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER MATERIALIZED VIEW ... OWNER TO for a brand-new matview, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTableWithOwnerUsesSetRole is RFC Section 11.5's (audit item #28)
// table-kind sibling of TestDiffCreateViewEmitsOwner.
func TestDiffCreateTableWithOwnerUsesSetRole(t *testing.T) {
	d := New()
	owner := "app_role"
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "widgets", Owner: &owner, Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
		}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "app_role";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE TABLE, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER TABLE ... OWNER TO for a brand-new table, got: %v", sqlList(ops))
	}
}

// TestDiffCreateEnumTypeWithOwnerUsesSetRole and
// TestDiffCreateDomainWithOwnerUsesSetRole are RFC Section 11.5's (audit item #28)
// TYPE-kind siblings — ENUM and DOMAIN take different createType code paths
// (see createType's switch), so both need their own coverage.
func TestDiffCreateEnumTypeWithOwnerUsesSetRole(t *testing.T) {
	d := New()
	owner := "app_role"
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "mood", Variant: "ENUM", EnumValues: []string{"sad", "ok", "happy"}, Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "app_role";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE TYPE, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER TYPE ... OWNER TO for a brand-new enum, got: %v", sqlList(ops))
	}
}

func TestDiffCreateDomainWithOwnerUsesSetRole(t *testing.T) {
	d := New()
	owner := "app_role"
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "posint", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}, Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "app_role";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE DOMAIN, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER DOMAIN ... OWNER TO for a brand-new domain, got: %v", sqlList(ops))
	}
}

// TestDiffEnumRenamedFromEmitsAlterRename is the regression guard for Type
// rename detection: before this, ir.Type had no RenamedFrom field at all
// (and renamedFromKey had no *ir.Type case), so a renamed type was
// indistinguishable from "old type dropped, new one added." Uses ENUM as
// the representative variant since diffType's shared OWNER TO/RENAME TO
// handling at the top of the function applies identically to all 5.
func TestDiffEnumRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mood_old", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "mood_old", Variant: "ENUM", Values: []string{"sad", "happy"}},
	})
	old := "mood_old"
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "mood", Variant: "ENUM", EnumValues: []string{"sad", "happy"}, RenamedFrom: &old},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") || containsSQL(ops, "CREATE TYPE") {
		t.Fatalf("renamed type should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER TYPE "public"."mood_old" RENAME TO "mood";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER TYPE RENAME TO (RFC's top-level-object rename classification), got %v", o.Safety())
		}
	}
}

// TestDiffDomainRenamedFromUsesAlterDomain confirms the DOMAIN-specific
// ALTER verb (matching OWNER TO's existing alterKW distinction) is used for
// a domain rename, not the generic ALTER TYPE.
func TestDiffDomainRenamedFromUsesAlterDomain(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.posint_old", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "posint_old", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	old := "posint_old"
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "posint", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}, RenamedFrom: &old},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER DOMAIN "public"."posint_old" RENAME TO "posint";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffEnumRenamedFromWithValueRemovalKeepsRename is the regression guard
// for a second bug found while implementing Type rename: diffType's ENUM
// branch used to `return diffEnumRemove(...)` directly on a value removal,
// discarding whatever ops the shared OWNER TO/RENAME TO handling at the top
// of diffType had already accumulated — a rename combined with a value
// removal would silently lose the rename.
func TestDiffEnumRenamedFromWithValueRemovalKeepsRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mood_old", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "mood_old", Variant: "ENUM", Values: []string{"sad", "ok", "happy"}},
	})
	old := "mood_old"
	migrate := &pipeline.MigrateRemoveBlock{Reason: "ok is no longer used", SQL: pipeline.RawExpr{Text: "-- no-op"}}
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "mood", Variant: "ENUM", EnumValues: []string{"sad", "happy"},
			RenamedFrom: &old, MigrateRemove: migrate,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TYPE "public"."mood_old" RENAME TO "mood";`) {
		t.Errorf("expected the rename to survive alongside the value removal, got: %v", sqlList(ops))
	}
}

// TestDiffTypeRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for Type: SET SCHEMA emitted
// first against the type's actual current identity, then RENAME TO in the
// new schema.
func TestDiffTypeRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.mood_old", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "old_schema", Name: "mood_old", Variant: "ENUM", Values: []string{"sad", "happy"}},
	})
	old := "mood_old"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "new_schema", Name: "mood", Variant: "ENUM", EnumValues: []string{"sad", "happy"},
			RenamedFrom: &old, RenamedFromSchema: &oldSchema,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") || containsSQL(ops, "CREATE TYPE") {
		t.Fatalf("cross-schema rename should match the existing type, not drop+create, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "old_schema"."mood_old" SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the type's actual (old) schema, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "new_schema"."mood_old" RENAME TO "mood";`) {
		t.Errorf("expected RENAME TO after the schema move, got: %v", sqlList(ops))
	}
}

// TestDiffTypeRenamedFromStaleErrors mirrors every other kind's identical
// stale-RENAMED-FROM validation (this one runs through Differ.Diff's
// top-level Pass 1, shared by every kind using the generic renamedFromKey
// mechanism, not a per-kind reimplementation).
func TestDiffTypeRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_type"
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "mood", Variant: "ENUM", EnumValues: []string{"sad", "happy"}, RenamedFrom: &old},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot type")
	}
}

// TestDiffCollationRenamedFromEmitsAlterRename is Type's Collation
// counterpart. Collation routes through the generic SnapOpaque path, not a
// dedicated snapshot struct, but the same renamedFromKey/RENAME TO shape
// applies identically.
func TestDiffCollationRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c_old", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c_old", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	old := "c_old"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			RenamedFrom: &old, Body: "CREATE COLLATION public.c (LOCALE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP COLLATION") {
		t.Fatalf("renamed collation should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER COLLATION "public"."c_old" RENAME TO "c";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER COLLATION RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffCollationRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for Collation.
func TestDiffCollationRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.c_old", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "old_schema", Name: "c_old", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	old := "c_old"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "new_schema", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			RenamedFrom: &old, RenamedFromSchema: &oldSchema, Body: "CREATE COLLATION new_schema.c (LOCALE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP COLLATION") {
		t.Fatalf("cross-schema rename should match the existing collation, not drop+create, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER COLLATION "old_schema"."c_old" SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the collation's actual (old) schema, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER COLLATION "new_schema"."c_old" RENAME TO "c";`) {
		t.Errorf("expected RENAME TO after the schema move, got: %v", sqlList(ops))
	}
}

// TestDiffCollationRenamedFromAndPropertyChanged confirms a simultaneous
// rename + property change (LOCALE/PROVIDER/etc.) still requires DROP+
// CREATE (real PostgreSQL's ALTER COLLATION has no path to change those),
// with no separate ALTER COLLATION RENAME TO — the CREATE already
// establishes the final (new) name directly. Drops under the collation's
// CURRENT (old, matched) name via dropObject's own embedded snap identity.
func TestDiffCollationRenamedFromAndPropertyChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c_old", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c_old", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	old := "c_old"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("POSIX"), Ctype: strPtr("POSIX"), Deterministic: true,
			RenamedFrom: &old, Body: "CREATE COLLATION public.c (LOCALE = 'POSIX')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP COLLATION IF EXISTS "public"."c_old"`) {
		t.Errorf("expected DROP COLLATION referencing the collation's actual (old) name, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `CREATE COLLATION public.c (LOCALE = 'POSIX');`) {
		t.Errorf("expected CREATE COLLATION for the new definition under the new name, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a separate ALTER COLLATION RENAME TO alongside drop+create, got: %v", sqlList(ops))
		}
	}
}

// TestDiffCreateMaterializedViewEmitsIndex guards RFC Section 8.2's matview-block
// INDEXES support: a new materialized view's indexes must be created
// alongside it, non-concurrently (it doesn't exist yet — same reasoning as
// createTable's brand-new-table indexes).
func TestDiffCreateMaterializedViewEmitsIndex(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public", Name: "mv_totals", Materialized: true, Query: "SELECT status FROM orders",
			Indexes: []*ir.Index{
				{Name: "mv_totals_status_uq", Unique: true, Columns: []pipeline.IndexColumn{{Name: "status"}}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE UNIQUE INDEX "mv_totals_status_uq" ON "public"."mv_totals" ("status");`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffMaterializedViewIndexAddedToExisting guards diffView's index diff
// path (not just create): adding an INDEXES entry to an already-applied
// materialized view must emit a CREATE INDEX without disturbing the view
// itself.
func TestDiffMaterializedViewIndexAddedToExisting(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv_totals", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public", Name: "mv_totals", Query: "SELECT status FROM orders",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public", Name: "mv_totals", Materialized: true, Query: "SELECT status FROM orders",
			Indexes: []*ir.Index{
				{Name: "mv_totals_status_uq", Unique: true, Columns: []pipeline.IndexColumn{{Name: "status"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE UNIQUE INDEX "mv_totals_status_uq" ON "public"."mv_totals" ("status");`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP MATERIALIZED VIEW") || containsSQL(ops, "CREATE MATERIALIZED VIEW") {
		t.Errorf("adding an index should not recreate the view itself, got: %v", sqlList(ops))
	}
}

// TestDiffMaterializedViewIndexRemoved is the removal-side sibling.
func TestDiffMaterializedViewIndexRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv_totals", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public", Name: "mv_totals", Query: "SELECT status FROM orders",
			Indexes: []snapshot.SnapIndex{
				{Name: "mv_totals_status_uq", Unique: true, Method: "btree", Columns: "status"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "mv_totals", Materialized: true, Query: "SELECT status FROM orders"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP INDEX IF EXISTS "mv_totals_status_uq";`) {
		t.Errorf("expected DROP INDEX for removed matview index, got: %v", sqlList(ops))
	}
}

// TestDiffViewOwnerChanged guards the diff-existing-view path (not just
// create): changing OWNER on an already-applied view must also be detected.
func TestDiffViewOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "old_role"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v_active", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users",
			Owner:  &oldOwner,
		},
	})
	newOwner := "new_role"
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "v_active", Query: "SELECT id FROM users", Owner: &newOwner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER VIEW "public"."v_active" OWNER TO "new_role";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffViewOwnerUnchangedIsNoop is the negative case: identical Owner
// must not produce a spurious ALTER on every plan.
func TestDiffViewOwnerUnchangedIsNoop(t *testing.T) {
	d := New()
	owner := "app_role"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v_active", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users",
			Owner:  &owner,
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "v_active", Query: "SELECT id FROM users", Owner: &owner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no OWNER TO op for unchanged owner, got: %v", sqlList(ops))
	}
}

func TestDiffViewRevocationAddedAndRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v_active", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "v_active",
			Query:  "SELECT id FROM users WHERE active",
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:      "public",
			Name:        "v_active",
			Query:       "SELECT id FROM users WHERE active",
			Revocations: []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE SELECT ON TABLE") {
		t.Errorf("expected REVOKE SELECT ON TABLE when adding a view revocation, got: %v", sqlList(ops))
	}

	// Now remove it: the snapshot side has the revocation, desired doesn't.
	snap2 := &pipeline.Snapshot{}
	_ = snap2.SetObject("public.v_active", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema:      "public",
			Name:        "v_active",
			Query:       "SELECT id FROM users WHERE active",
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
		},
	})
	desired2 := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "v_active", Query: "SELECT id FROM users WHERE active"},
	}
	ops2, err := d.Diff(desired2, snap2)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops2, "GRANT SELECT ON TABLE") {
		t.Errorf("expected GRANT SELECT ON TABLE to restore, got: %v", sqlList(ops2))
	}
}

func TestDiffFunctionGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema:     "public",
			Name:       "get_user",
			ReturnType: "void",
			Language:   "plpgsql",
			Volatility: "VOLATILE",
			BodyHash:   "abc",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:     "public",
			Name:       "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Grants:     []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT EXECUTE ON FUNCTION") {
		t.Errorf("expected GRANT EXECUTE ON FUNCTION, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionGrantGrantedByNewGrant guards RFC audit item #90: a
// brand-new grant declared with GRANTED BY renders the clause.
func TestDiffFunctionGrantGrantedByNewGrant(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "get_user", ReturnType: "void",
			Language: "plpgsql", Volatility: "VOLATILE", BodyHash: "abc",
		},
	})
	grantedBy := "admin"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Grants:     []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &grantedBy}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANTED BY \"admin\"") {
		t.Errorf("expected GRANTED BY \"admin\", got: %v", sqlList(ops))
	}
}

// TestDiffFunctionGrantGrantedByChanged guards the "matched grant, GrantedBy
// changed" branch: same privileges/roles/WithGrant identity, but the
// desired side declares a different GrantedBy than the snapshot — must
// re-GRANT to apply it (real PostgreSQL treats a repeated GRANT as
// updating in place), even though grantKey itself doesn't include
// GrantedBy.
func TestDiffFunctionGrantGrantedByChanged(t *testing.T) {
	d := New()
	oldGrantedBy := "old_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "get_user", ReturnType: "void",
			Language: "plpgsql", Volatility: "VOLATILE", BodyHash: "abc",
			Grants: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &oldGrantedBy}},
		},
	})
	newGrantedBy := "new_admin"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Grants:     []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &newGrantedBy}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANTED BY \"new_admin\"") {
		t.Errorf("expected a re-GRANT with GRANTED BY \"new_admin\", got: %v", sqlList(ops))
	}
}

// TestDiffFunctionGrantGrantedByUnspecifiedIsNoop guards the "declared, so
// managed" rule: when the desired side never mentions GRANTED BY at all
// (nil), a snapshot carrying some other value (e.g. from an earlier
// declaration since removed from source) must NOT be treated as drift —
// matching Cost/Rows' identical suppress-when-unspecified precedent.
func TestDiffFunctionGrantGrantedByUnspecifiedIsNoop(t *testing.T) {
	d := New()
	priorGrantedBy := "old_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "get_user", ReturnType: "void",
			Language: "plpgsql", Volatility: "VOLATILE", BodyHash: "abc",
			Grants: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &priorGrantedBy}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Grants:     []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops (GrantedBy not declared, don't compare), got: %v", sqlList(ops))
	}
}

// TestDiffFunctionGrantGrantedBySymbolicRoleSpecNeverDrifts guards
// grantedByMatches: a declared symbolic role-spec (CURRENT_USER etc.) must
// never be treated as drift against a live-introspected concrete grantor —
// PostgreSQL restricts a GRANT's effective grantor to whichever role
// executes it regardless of what's named, so any non-nil live grantor
// already proves the declaration was satisfied. Without this, fixing
// introspection to actually populate GrantedBy from the real grantor would
// have turned "GRANTED BY CURRENT_USER" into a permanent spurious re-GRANT
// on every live plan.
func TestDiffFunctionGrantGrantedBySymbolicRoleSpecNeverDrifts(t *testing.T) {
	d := New()
	liveGrantor := "postgres"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "get_user", ReturnType: "void",
			Language: "plpgsql", Volatility: "VOLATILE", BodyHash: "abc",
			Grants: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &liveGrantor}},
		},
	})
	grantedBy := "CURRENT_USER"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Grants:     []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &grantedBy}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops (symbolic GRANTED BY satisfied by any concrete live grantor), got: %v", sqlList(ops))
	}
}

// TestDiffFunctionRevocationGrantedBy guards RFC audit item #90's
// revoke-entry: GRANTED BY on an explicit REVOKES declaration.
func TestDiffFunctionRevocationGrantedBy(t *testing.T) {
	d := New()
	grantedBy := "admin"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Revocations: []ir.Revocation{
				{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}, GrantedBy: &grantedBy, Cascade: true},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE EXECUTE ON FUNCTION") || !containsSQL(ops, "GRANTED BY \"admin\" CASCADE") {
		t.Errorf("expected REVOKE ... GRANTED BY \"admin\" CASCADE, got: %v", sqlList(ops))
	}
}

func TestDiffFunctionGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema:     "public",
			Name:       "get_user",
			ReturnType: "void",
			Language:   "plpgsql",
			Volatility: "VOLATILE",
			BodyHash:   "abc",
			Grants:     []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:     "public",
			Name:       "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE EXECUTE ON FUNCTION") {
		t.Errorf("expected REVOKE EXECUTE ON FUNCTION, got: %v", sqlList(ops))
	}
}

// ── CREATE-time grant emission ────────────────────────────────────────────────

func TestDiffCreateViewEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.View{
			Schema: "public",
			Name:   "v_summary",
			Query:  "SELECT 1",
			Grants: []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"readonly"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE") {
		t.Fatal("expected CREATE VIEW")
	}
	if !containsSQL(ops, "GRANT SELECT ON TABLE") {
		t.Errorf("expected GRANT at creation time, got: %v", sqlList(ops))
	}
}

func TestDiffCreateFunctionEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:     "public",
			Name:       "do_work",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "h",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN END;"},
			Grants:     []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"worker"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE OR REPLACE FUNCTION") {
		t.Fatal("expected CREATE FUNCTION")
	}
	if !containsSQL(ops, "GRANT EXECUTE ON FUNCTION") {
		t.Errorf("expected GRANT at creation time, got: %v", sqlList(ops))
	}
}

// ── INHERITS diffing ──────────────────────────────────────────────────────────

func TestDiffTableInheritsAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.logs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "logs",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:   "public",
			Name:     "logs",
			Columns:  []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Inherits: []string{"base_logs"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER TABLE") || !containsSQL(ops, "INHERIT") {
		t.Errorf("expected ALTER TABLE ... INHERIT, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "NO INHERIT") {
		t.Errorf("unexpected NO INHERIT, got: %v", sqlList(ops))
	}
}

func TestDiffTableInheritsRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.logs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:   "public",
			Name:     "logs",
			Columns:  []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Inherits: []string{"base_logs"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "logs",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "NO INHERIT") {
		t.Errorf("expected NO INHERIT, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "NO INHERIT") && o.Safety() != pipeline.Safe {
			t.Errorf("expected NO INHERIT op to be SAFE (catalog-only, no rewrite), got %v: %v", o.Safety(), o.SQL())
		}
	}
}

// ── Column attribute diffing ──────────────────────────────────────────────────

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }

func TestDiffColumnStorageChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "docs",
			Columns: []snapshot.SnapColumn{{Name: "body", Type: "text", Storage: strPtr("EXTENDED")}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "docs",
			Columns: []*ir.Column{{Name: "body", Type: ir.TypeRef{Name: "text"}, Storage: strPtr("EXTERNAL")}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "SET STORAGE EXTERNAL") {
		t.Errorf("expected SET STORAGE EXTERNAL, got: %v", sqlList(ops))
	}
}

func TestDiffColumnCompressionChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "docs",
			Columns: []snapshot.SnapColumn{{Name: "body", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "docs",
			Columns: []*ir.Column{{Name: "body", Type: ir.TypeRef{Name: "text"}, Compression: strPtr("lz4")}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "SET COMPRESSION lz4") {
		t.Errorf("expected SET COMPRESSION lz4, got: %v", sqlList(ops))
	}
}

func TestDiffColumnStatisticsSet(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "events",
			Columns: []snapshot.SnapColumn{{Name: "ts", Type: "timestamptz"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "events",
			Columns: []*ir.Column{{Name: "ts", Type: ir.TypeRef{Name: "timestamptz"}, Statistics: intPtr(500)}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "SET STATISTICS 500") {
		t.Errorf("expected SET STATISTICS 500, got: %v", sqlList(ops))
	}
}

func TestDiffColumnStatisticsReset(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "events",
			Columns: []snapshot.SnapColumn{{Name: "ts", Type: "timestamptz", Statistics: intPtr(500)}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "events",
			Columns: []*ir.Column{{Name: "ts", Type: ir.TypeRef{Name: "timestamptz"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "SET STATISTICS -1") {
		t.Errorf("expected SET STATISTICS -1 (reset), got: %v", sqlList(ops))
	}
}

// ── View structural changes ───────────────────────────────────────────────────

// TestDiffViewRecursiveChangedDropsAndRecretes previously asserted the
// re-created view's CREATE statement carried a literal "RECURSIVE" keyword
// — that was the pre-#27-fix behavior, and turned out to be wrong once
// RFC audit item #27 wired Recursive through for real: pg_query's deparse
// of an actual RECURSIVE VIEW's query already returns a self-contained
// "WITH RECURSIVE ..." CTE (confirmed live against postgres:17), so
// "CREATE RECURSIVE VIEW ... AS WITH RECURSIVE ..." is a genuine PostgreSQL
// syntax error (SQLSTATE 42601) — this test's hand-built Query
// ("SELECT id FROM nodes") never had that shape, masking the bug. createView
// now deliberately omits the RECURSIVE keyword (same reasoning dump's View
// case already used), relying on Query alone — this test now asserts that
// corrected contract instead.
func TestDiffViewRecursiveChangedDropsAndRecretes(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.v_tree", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema:    "public",
			Name:      "v_tree",
			Query:     "SELECT id FROM nodes",
			Recursive: false,
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:    "public",
			Name:      "v_tree",
			Query:     "SELECT id FROM nodes",
			Recursive: true,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP VIEW IF EXISTS") {
		t.Errorf("expected DROP VIEW IF EXISTS, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "CREATE RECURSIVE VIEW") {
		t.Errorf("CREATE RECURSIVE VIEW is a real PostgreSQL syntax error against a self-contained WITH RECURSIVE query text, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE VIEW") {
		t.Errorf("expected a plain CREATE VIEW (Query text alone is self-sufficient), got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if o.Safety() == pipeline.Safe && strings.Contains(o.SQL(), "DROP") {
			t.Errorf("DROP should be Destructive, got Safe: %s", o.SQL())
		}
	}
}

func TestDiffCreateMaterViewWithNoData(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:       "public",
			Name:         "mv_summary",
			Query:        "SELECT count(*) FROM orders",
			Materialized: true,
			WithNoData:   true,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE MATERIALIZED VIEW") {
		t.Fatal("expected CREATE MATERIALIZED VIEW")
	}
	if !containsSQL(ops, "WITH NO DATA") {
		t.Errorf("expected WITH NO DATA clause, got: %v", sqlList(ops))
	}
}

func TestDiffMaterViewWithNoDataChangedIsNote(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv_summary", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema:     "public",
			Name:       "mv_summary",
			Query:      "SELECT count(*) FROM orders",
			WithNoData: false,
		},
	})
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:       "public",
			Name:         "mv_summary",
			Query:        "SELECT count(*) FROM orders",
			Materialized: true,
			WithNoData:   true,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REFRESH MATERIALIZED VIEW") {
		t.Errorf("expected REFRESH MATERIALIZED VIEW notice, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "REFRESH") && (o.Mode() != pipeline.ModeInstructionOnly || o.Safety() != pipeline.Safe) {
			t.Errorf("WITH NO DATA change notice should be a Safe instruction-only note, got %s %s", o.Safety(), o.Mode())
		}
	}
}

// ── Partitioning ─────────────────────────────────────────────────────────────

// ── TRIGGERS diffing ─────────────────────────────────────────────────────────
//
// No unit test exercised diffTriggers at all before now — the function-
// qualification mismatch below (introspection always returns "schema.func",
// hand-written source commonly writes an unqualified "func") was invisible at
// both the unit and (until a live partition test happened to exercise the
// full introspect+diff round trip) integration level.

func TestDiffTriggerUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "trg_touch"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "TRIGGER") {
		t.Errorf("expected no trigger ops: an unqualified source function name ('trg_touch') must compare equal to introspection's qualified form ('public.trg_touch'), got: %v", sqlList(ops))
	}
}

// TestDiffTriggerDependsOnExtension is the regression guard for RFC audit
// item #75: Section 9.1's [NO] DEPENDS ON EXTENSION directive, reused
// verbatim for triggers (Section 7.9), previously zero code anywhere.
// TestDiffTriggerEnableStateChangedIsTargetedAlter is the regression guard
// for RFC audit item #56: previously zero code anywhere, so an enable-state
// change either had no effect or (worse) would have needed to be excluded
// from the generic field-comparison recreate condition, which this test
// also proves — a change in isolation must not drop+recreate the trigger.
func TestDiffTriggerEnableStateChangedIsTargetedAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "trg_touch", EnableState: "ENABLE REPLICA"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" ENABLE REPLICA TRIGGER "trg_a";`) {
		t.Errorf("expected targeted ENABLE REPLICA TRIGGER, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP TRIGGER") {
		t.Errorf("an enable-state-only change must not drop+recreate the trigger, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerEnableStateDisabledEmitsDisable(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "trg_touch", EnableState: "DISABLED"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" DISABLE TRIGGER "trg_a";`) {
		t.Errorf("expected DISABLE TRIGGER, got: %v", sqlList(ops))
	}
}

// TestCreateTriggerWithEnableState proves a brand-new trigger applies its
// declared enable-state at creation time.
func TestCreateTriggerWithEnableState(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "trg_touch", EnableState: "DISABLED"},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE TRIGGER") {
		t.Fatalf("expected a CREATE TRIGGER op, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" DISABLE TRIGGER "trg_a";`) {
		t.Errorf("expected the enable-state to be applied at creation, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerDependsOnExtension(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.trg_touch", DependsOnExtensions: []string{"old_ext"}},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "trg_touch", DependsOnExtensions: []string{"new_ext"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TRIGGER "trg_a" ON "public"."t" DEPENDS ON EXTENSION "new_ext";`) {
		t.Errorf("expected ADD for new_ext, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TRIGGER "trg_a" ON "public"."t" NO DEPENDS ON EXTENSION "old_ext";`) {
		t.Errorf("expected NO DEPENDS for the removed old_ext, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP TRIGGER") {
		t.Errorf("a DEPENDS ON EXTENSION-only change should not drop+recreate the trigger, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerFunctionGenuinelyChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.old_func"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "new_func"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) || !containsSQL(ops, "new_func") {
		t.Errorf("expected DROP+CREATE for a genuinely changed function, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerConditionParenNormalizationIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				// Simulates pg_get_expr's mandatory outer-paren wrapping on introspection.
				{Name: "trg_a", When: "AFTER", Events: "UPDATE", ForEach: "ROW", Function: "public.trg_touch", Condition: "(status = 'active'::text)"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				// Simulates the blockparser storing only the interior of WHEN (...), no parens.
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "trg_touch", Condition: strPtr("status = 'active'::text")},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "TRIGGER") {
		t.Errorf("expected no trigger ops: a paren-wrapping-only difference between introspected and declared WHEN condition must not be treated as drift, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerConditionGenuinelyChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "UPDATE", ForEach: "ROW", Function: "public.trg_touch", Condition: "(status = 'active'::text)"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "trg_touch", Condition: strPtr("status = 'inactive'")},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) || !containsSQL(ops, "WHEN (status = 'inactive')") {
		t.Errorf("expected DROP+CREATE with the new WHEN condition for a genuinely changed condition, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerConditionAddedAndRemoved(t *testing.T) {
	// Added: no condition in snapshot, one declared now.
	snapAdded := &pipeline.Snapshot{}
	_ = snapAdded.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "UPDATE", ForEach: "ROW", Function: "public.trg_touch"},
			},
		},
	})
	desiredAdded := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "trg_touch", Condition: strPtr("status = 'active'")},
			},
		},
	}
	ops, err := New().Diff(desiredAdded, snapAdded)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) || !containsSQL(ops, "WHEN (status = 'active')") {
		t.Errorf("expected DROP+CREATE emitting the newly-added WHEN condition, got: %v", sqlList(ops))
	}

	// Removed: condition in snapshot, none declared now.
	snapRemoved := &pipeline.Snapshot{}
	_ = snapRemoved.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "UPDATE", ForEach: "ROW", Function: "public.trg_touch", Condition: "(status = 'active'::text)"},
			},
		},
	})
	desiredRemoved := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", Function: "trg_touch"},
			},
		},
	}
	ops, err = New().Diff(desiredRemoved, snapRemoved)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) || containsSQL(ops, "WHEN") {
		t.Errorf("expected DROP+CREATE with no WHEN clause for a removed condition, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "trg_touch"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `CREATE TRIGGER "trg_a"`) {
		t.Errorf("expected CREATE TRIGGER for new trigger, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTriggerEmitsUpdateOfColumns guards RFC audit item #1: the
// UPDATE OF column list was tokenized and explicitly discarded — a trigger
// declared to fire only on specific columns actually fired on every column
// update instead.
func TestDiffCreateTriggerEmitsUpdateOfColumns(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", UpdateOfColumns: []string{"email", "status"}, Function: "trg_touch"},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `UPDATE OF "email", "status"`) {
		t.Errorf("expected UPDATE OF \"email\", \"status\" in CREATE TRIGGER, got: %v", sqlList(ops))
	}
}

// TestDiffTriggerUpdateOfColumnsChangedDropsAndRecreates guards a changed
// UPDATE OF column list being detected as a real change (RFC audit item
// #1) — previously invisible to diffing since the field didn't exist at
// all.
func TestDiffTriggerUpdateOfColumnsChangedDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "UPDATE", ForEach: "ROW", UpdateOfColumns: "email", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", UpdateOfColumns: []string{"email", "status"}, Function: "trg_touch"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) {
		t.Errorf("expected DROP TRIGGER for a changed UPDATE OF list, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `UPDATE OF "email", "status"`) {
		t.Errorf("expected re-CREATE TRIGGER with the new UPDATE OF list, got: %v", sqlList(ops))
	}
}

// TestDiffTriggerUpdateOfColumnsUnchangedIsNoop proves an identical
// UPDATE OF column list doesn't trigger a spurious drop+recreate.
func TestDiffTriggerUpdateOfColumnsUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "UPDATE", ForEach: "ROW", UpdateOfColumns: "email, status", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"UPDATE"}, ForEach: "ROW", UpdateOfColumns: []string{"email", "status"}, Function: "trg_touch"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged UPDATE OF list, got: %v", sqlList(ops))
	}
}

// TestCreateTriggerEmitsReferencing and TestDiffTriggerReferencingChanged
// guard RFC Section 7.9 (audit item #2): REFERENCING OLD/NEW TABLE AS was a hard
// parse error before this fix; ir.Trigger now carries the two transition
// names through to CREATE TRIGGER emission and change diffing.
func TestCreateTriggerEmitsReferencing(t *testing.T) {
	d := New()
	oldName := "old_rows"
	newName := "new_rows"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{
					Name: "audit_changes", When: "AFTER", Events: []string{"INSERT", "UPDATE"}, ForEach: "STATEMENT",
					OldTransitionName: &oldName, NewTransitionName: &newName,
					Function: "audit_table_changes",
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	want := `REFERENCING OLD TABLE AS "old_rows" NEW TABLE AS "new_rows"`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q in CREATE TRIGGER, got: %v", want, sqlList(ops))
	}
}

// TestCreateTriggerReferencingNewTableOnly proves only the declared side is
// emitted — a single-sided REFERENCING must not spuriously emit "OLD TABLE
// AS" when only NEW was declared.
func TestCreateTriggerReferencingNewTableOnly(t *testing.T) {
	d := New()
	newName := "new_rows"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{
					Name: "audit_inserts", When: "AFTER", Events: []string{"INSERT"}, ForEach: "STATEMENT",
					NewTransitionName: &newName,
					Function:          "audit_table_changes",
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `NEW TABLE AS "new_rows"`) {
		t.Errorf("expected NEW TABLE AS, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OLD TABLE") {
		t.Errorf("expected no OLD TABLE clause, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerReferencingChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "STATEMENT", NewTransitionName: "new_rows", Function: "public.audit_table_changes"},
			},
		},
	})
	newName := "new_rows2"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "STATEMENT", NewTransitionName: &newName, Function: "audit_table_changes"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) {
		t.Errorf("expected DROP TRIGGER for a changed transition name, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `NEW TABLE AS "new_rows2"`) {
		t.Errorf("expected re-CREATE TRIGGER with the new transition name, got: %v", sqlList(ops))
	}
}

// TestDiffTriggerReferencingUnchangedIsNoop proves an identical transition
// name doesn't trigger a spurious drop+recreate.
func TestDiffTriggerReferencingUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "STATEMENT", NewTransitionName: "new_rows", Function: "public.audit_table_changes"},
			},
		},
	})
	newName := "new_rows"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Triggers: []*ir.Trigger{
				{Name: "trg_a", When: "AFTER", Events: []string{"INSERT"}, ForEach: "STATEMENT", NewTransitionName: &newName, Function: "audit_table_changes"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged transition name, got: %v", sqlList(ops))
	}
}

func TestDiffTriggerRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Triggers: []snapshot.SnapTrigger{
				{Name: "trg_a", When: "AFTER", Events: "INSERT", ForEach: "ROW", Function: "public.trg_touch"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TRIGGER IF EXISTS "trg_a"`) {
		t.Errorf("expected DROP TRIGGER for removed trigger, got: %v", sqlList(ops))
	}
}

func TestQualifyFuncForCompare(t *testing.T) {
	cases := []struct{ in, want string }{
		{"trg_touch", "public.trg_touch"},
		{"public.trg_touch", "public.trg_touch"},
		{"other_schema.trg_touch", "other_schema.trg_touch"},
	}
	for _, tc := range cases {
		if got := qualifyFuncForCompare(tc.in); got != tc.want {
			t.Errorf("qualifyFuncForCompare(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiffCreateTableWithPartitionBy(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "events",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true},
			},
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "PARTITION BY RANGE") {
		t.Errorf("expected PARTITION BY RANGE in CREATE TABLE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "created_at") {
		t.Errorf("expected partition column in CREATE TABLE, got: %v", sqlList(ops))
	}
}

func TestDiffCreateTableWithPartitions(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "events",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true},
			},
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	// Exact-text assertion, not a substring check: Bounds already carries the
	// full "FOR VALUES ..." clause (matching both the parser and
	// introspection), so a substring check for "FOR VALUES FROM" would pass
	// even with the once-real bug of prepending "FOR VALUES" a second time
	// ("... FOR VALUES FOR VALUES FROM (...)", a PG syntax error).
	want := `CREATE TABLE "public"."events_2024" PARTITION OF "public"."events" FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');`
	if !containsSQL(ops, want) {
		t.Errorf("expected exact partition CREATE statement %q, got: %v", want, sqlList(ops))
	}
}

func TestDiffPartitionAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	// Exact-text, not substring — see TestDiffCreateTableWithPartitions.
	want := `CREATE TABLE "public"."events_2024" PARTITION OF "public"."events" FOR VALUES FROM ('2024-01-01') TO ('2025-01-01');`
	if !containsSQL(ops, want) {
		t.Errorf("expected exact partition CREATE statement %q, got: %v", want, sqlList(ops))
	}
}

func TestDiffPartitionRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2024", Bound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP TABLE") {
		t.Errorf("expected DROP TABLE for removed partition, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionAttachedFrom is the regression guard for RFC Section
// 7.13's "ATTACHED FROM existing_table" form: a not-yet-tracked partition
// declaring AttachedFrom must emit ALTER TABLE ... ATTACH PARTITION, not
// the CREATE TABLE ... PARTITION OF a normal new partition would get (which
// would fail live — the table already exists).
func TestDiffPartitionAttachedFrom(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	from := "legacy_events"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "legacy_events", AttachedFrom: &from, Bounds: "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."events" ATTACH PARTITION "public"."legacy_events" FOR VALUES FROM ('2023-01-01') TO ('2024-01-01');`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "CREATE TABLE") {
		t.Errorf("must not CREATE TABLE for an attached-from partition, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "ATTACH PARTITION") && op.Safety() != pipeline.Caution {
			t.Errorf("ATTACH PARTITION safety = %v, want Caution", op.Safety())
		}
	}
}

// TestDiffPartitionAttachedFromDoesNotDropTarget is the regression guard
// for a real bug found live-testing this feature: the target table
// genuinely disappears from desired as an independent top-level object
// once rewritten as an ATTACHED FROM entry (it's now only reachable nested
// inside its new parent's Partitions) — without marking it as consumed,
// Pass 2's "absent from desired → drop" logic would drop it, racing the
// ALTER TABLE ... ATTACH PARTITION emitted for it in the same migration
// and making that statement fail live with "relation does not exist"
// (confirmed live before this fix).
func TestDiffPartitionAttachedFromDoesNotDropTarget(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)",
		},
	})
	_ = snap.SetObject("public.legacy_events", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "legacy_events"},
	})
	from := "legacy_events"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Partitions: []*ir.Partition{
				{Name: "legacy_events", AttachedFrom: &from, Bounds: "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, `DROP TABLE IF EXISTS "public"."legacy_events"`) {
		t.Errorf("must not drop the ATTACHED FROM target, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ATTACH PARTITION "public"."legacy_events"`) {
		t.Errorf("expected ATTACH PARTITION, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionAttachedFromAlreadyAttachedIsNoop proves that once an
// ATTACHED FROM partition has actually been attached (now a normal entry in
// the snapshot's partition list), it's compared exactly like any other
// partition on subsequent plans — no re-attach attempted just because the
// source declaration still says ATTACHED FROM.
func TestDiffPartitionAttachedFromAlreadyAttachedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "legacy_events", Bound: "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"},
			},
		},
	})
	from := "legacy_events"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "legacy_events", AttachedFrom: &from, Bounds: "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an already-attached partition, got: %v", sqlList(ops))
	}
}

// TestDiffTableDetachedFrom is the regression guard for RFC Section 7.13's
// DETACHED FROM directive: a standalone Table declaring it, matched against
// a nested entry in its parent's snapshot, must emit exactly one ALTER
// TABLE parent DETACH PARTITION child — never the DROP TABLE (parent's own
// removal detection) + CREATE TABLE (the child's own not-found handling)
// pair that would otherwise result from two independent, uncoordinated
// diffs.
func TestDiffTableDetachedFrom(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2023", Bound: "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
		&ir.Table{
			Schema: "public", Name: "events_2023",
			DetachedFrom: &ir.DetachedFrom{ParentTable: "events"},
			Columns:      []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."events" DETACH PARTITION "public"."events_2023";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP TABLE") || containsSQL(ops, "CREATE TABLE") {
		t.Errorf("must not DROP or CREATE the detached child, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DETACH PARTITION") && op.Safety() != pipeline.Caution {
			t.Errorf("DETACH PARTITION safety = %v, want Caution", op.Safety())
		}
	}
}

// TestDiffTableDetachedFromConcurrentlyIsNonTransactional guards the CONCURRENTLY
// modifier's Safety classification (RFC's own diffing table: "MANUAL if
// CONCURRENTLY written, else CAUTION").
func TestDiffTableDetachedFromConcurrentlyIsNonTransactional(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)",
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2023", Bound: "FOR VALUES FROM ('2023-01-01') TO ('2024-01-01')"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
		},
		&ir.Table{
			Schema: "public", Name: "events_2023",
			DetachedFrom: &ir.DetachedFrom{ParentTable: "events", Concurrently: true},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."events" DETACH PARTITION "public"."events_2023" CONCURRENTLY;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DETACH PARTITION") && (op.Safety() != pipeline.Caution || op.Mode() != pipeline.ModeNonTransactional) {
			t.Errorf("DETACH PARTITION CONCURRENTLY = %v %v, want Caution non-transactional", op.Safety(), op.Mode())
		}
	}
}

// TestDiffTableDetachedFromAlreadyDetachedIsNoop proves that once a
// DETACHED FROM has already landed (the child now exists as its own
// top-level table, no longer listed among the parent's partitions), a
// still-present DETACHED FROM directive in source is a no-op — not an
// error — the same "new state already present" three-state resolution
// every other RENAMED-FROM-style directive in this codebase uses.
func TestDiffTableDetachedFromAlreadyDetachedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)"},
	})
	_ = snap.SetObject("public.events_2023", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "events_2023"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "events", PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}}},
		&ir.Table{Schema: "public", Name: "events_2023", DetachedFrom: &ir.DetachedFrom{ParentTable: "events"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an already-detached table, got: %v", sqlList(ops))
	}
}

// TestDiffTableDetachedFromNotAPartitionErrors proves a DETACHED FROM
// pointing at a real parent that doesn't currently have this child as a
// partition is a compile error, not a silent no-op or an accidental
// CREATE TABLE.
func TestDiffTableDetachedFromNotAPartitionErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "events", PartitionBy: "RANGE (created_at)"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "events", PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}}},
		&ir.Table{Schema: "public", Name: "events_2023", DetachedFrom: &ir.DetachedFrom{ParentTable: "events"}},
	}
	if _, err := d.Diff(desired, snap); err == nil {
		t.Fatal("expected an error for DETACHED FROM a parent that doesn't have this child as a partition")
	}
}

// TestDiffTableDetachedFromUnknownParentErrors proves a DETACHED FROM
// pointing at a nonexistent parent table is a compile error.
func TestDiffTableDetachedFromUnknownParentErrors(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "events_2023", DetachedFrom: &ir.DetachedFrom{ParentTable: "nonexistent"}},
	}
	if _, err := d.Diff(desired, &pipeline.Snapshot{}); err == nil {
		t.Fatal("expected an error for DETACHED FROM an unknown parent")
	}
}

// TestDiffPartitionRenamedFromEmitsAlterRename is the regression guard for
// the partition rename-detection gap: before this, a partition rename was
// indistinguishable from "old partition removed, new partition added" —
// silently emitting a real DROP TABLE (data loss) for the old name plus a
// CREATE/ATTACH for the new one, instead of a metadata-only rename.
func TestDiffPartitionRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2024_old", Bound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	})
	old := "events_2024_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')", RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TABLE") || containsSQL(ops, "CREATE TABLE") {
		t.Fatalf("renamed partition should match by RENAMED FROM, not DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."events_2024_old" RENAME TO "events_2024";`) {
		t.Errorf("expected ALTER TABLE ... RENAME TO, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionRenamedFromAndBoundChanged confirms a simultaneous rename
// + bound change still requires DROP+CREATE (real PostgreSQL cannot alter a
// partition's bound in place), but crucially drops the partition under its
// CURRENT (old, matched-snapshot) name — not the desired new name, which
// doesn't exist live yet. This is the same class of bug fixed today for
// Table/View/Function/Procedure/Aggregate's RENAME TO emission.
func TestDiffPartitionRenamedFromAndBoundChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2024_old", Bound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	})
	old := "events_2024_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-06-01') TO ('2025-01-01')", RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TABLE "public"."events_2024_old";`) {
		t.Errorf("expected DROP TABLE referencing the partition's actual (old) name, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `CREATE TABLE "public"."events_2024" PARTITION OF`) {
		t.Errorf("expected CREATE TABLE for the new bound under the new name, got: %v", sqlList(ops))
	}
}

// TestDiffSubPartitionRenamedFrom confirms RENAMED FROM works identically on
// a nested sub-partition, since diffPartitionList recurses using the same
// production/matching logic at every depth.
func TestDiffSubPartitionRenamedFrom(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}, {Name: "region", Type: "text"}},
			Partitions: []snapshot.SnapPartition{
				{
					Schema: "public", Name: "events_2024", Bound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')",
					PartitionBy: "LIST (region)",
					Partitions: []snapshot.SnapPartition{
						{Schema: "public", Name: "events_2024_us_old", Bound: "FOR VALUES IN ('us-east')"},
					},
				},
			},
		},
	})
	old := "events_2024_us_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "region", Type: ir.TypeRef{Name: "text"}},
			},
			Partitions: []*ir.Partition{
				{
					Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')",
					PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"region"}},
					Partitions: []*ir.Partition{
						{Name: "events_2024_us", Bounds: "FOR VALUES IN ('us-east')", RenamedFrom: &old},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TABLE") || containsSQL(ops, "CREATE TABLE") {
		t.Fatalf("renamed sub-partition should match by RENAMED FROM, not DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."events_2024_us_old" RENAME TO "events_2024_us";`) {
		t.Errorf("expected ALTER TABLE ... RENAME TO for the sub-partition, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionRenamedFromStaleErrors mirrors diffCompositeAttrs'
// identical stale-RENAMED-FROM validation: neither the old nor new name
// exists in the snapshot at all, so this can't be distinguished from a typo
// on a genuinely new partition.
func TestDiffPartitionRenamedFromStaleErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	old := "nonexistent_old_name"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')", RenamedFrom: &old},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot partition")
	}
}

func TestDiffPartitionBoundChangedDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2024", Bound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2024-07-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TABLE "public"."events_2024";`) {
		t.Errorf("expected DROP TABLE for bound change, got: %v", sqlList(ops))
	}
	// Exact-text, not substring — see TestDiffCreateTableWithPartitions.
	want := `CREATE TABLE "public"."events_2024" PARTITION OF "public"."events" FOR VALUES FROM ('2024-01-01') TO ('2024-07-01');`
	if !containsSQL(ops, want) {
		t.Errorf("expected exact partition CREATE statement %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffCreateTableWithSubPartitions guards RFC Section 7.13 sub-partitioning:
// a partition entry may itself be PARTITION BY'd, in which case its CREATE
// TABLE ... PARTITION OF statement carries a trailing PARTITION BY clause,
// and each of ITS partitions is created as PARTITION OF the sub-partitioned
// child (not the top-level table).
func TestDiffCreateTableWithSubPartitions(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "metrics",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}, NotNull: true},
				{Name: "channel", Type: ir.TypeRef{Name: "text"}, NotNull: true},
			},
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Partitions: []*ir.Partition{
				{
					Name:        "metrics_2026",
					Bounds:      "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
					PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"channel"}},
					Partitions: []*ir.Partition{
						{Name: "metrics_2026_web", Bounds: "FOR VALUES IN ('web')"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	wantParent := `CREATE TABLE "public"."metrics_2026" PARTITION OF "public"."metrics" FOR VALUES FROM ('2026-01-01') TO ('2027-01-01') PARTITION BY LIST (channel);`
	if !containsSQL(ops, wantParent) {
		t.Errorf("expected sub-partitioned CREATE statement %q, got: %v", wantParent, sqlList(ops))
	}
	wantChild := `CREATE TABLE "public"."metrics_2026_web" PARTITION OF "public"."metrics_2026" FOR VALUES IN ('web');`
	if !containsSQL(ops, wantChild) {
		t.Errorf("expected nested partition CREATE statement %q, got: %v", wantChild, sqlList(ops))
	}
}

// TestDiffSubPartitionAdded guards adding a new leaf under an EXISTING
// sub-partitioned partition (the common ongoing-maintenance case, e.g.
// adding next quarter's channel partition).
func TestDiffSubPartitionAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.metrics", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "metrics",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{
					Schema:      "public",
					Name:        "metrics_2026",
					Bound:       "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
					PartitionBy: "LIST (channel)",
					Partitions: []snapshot.SnapPartition{
						{Schema: "public", Name: "metrics_2026_web", Bound: "FOR VALUES IN ('web')"},
					},
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "metrics",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{
					Name:        "metrics_2026",
					Bounds:      "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
					PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"channel"}},
					Partitions: []*ir.Partition{
						{Name: "metrics_2026_web", Bounds: "FOR VALUES IN ('web')"},
						{Name: "metrics_2026_other", Bounds: "FOR VALUES IN ('mobile', 'api')"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	// Only the new leaf should be created — the existing "metrics_2026" and
	// "metrics_2026_web" partitions must NOT be dropped/recreated.
	want := `CREATE TABLE "public"."metrics_2026_other" PARTITION OF "public"."metrics_2026" FOR VALUES IN ('mobile', 'api');`
	if !containsSQL(ops, want) {
		t.Errorf("expected exact new-leaf CREATE statement %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, `DROP TABLE "public"."metrics_2026"`) {
		t.Errorf("existing sub-partitioned parent should not be dropped, got: %v", sqlList(ops))
	}
	if containsSQL(ops, `DROP TABLE "public"."metrics_2026_web"`) {
		t.Errorf("existing leaf should not be dropped, got: %v", sqlList(ops))
	}
}

// TestDiffSubPartitionStrategyChangedIsInstruction mirrors
// TestDiffPartitionStrategyChangedIsInstruction one level down: a sub-partition's
// OWN PARTITION BY strategy also cannot be altered in place.
func TestDiffSubPartitionStrategyChangedIsInstruction(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.metrics", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "metrics",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{
					Schema:      "public",
					Name:        "metrics_2026",
					Bound:       "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
					PartitionBy: "LIST (channel)",
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "metrics",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{
					Name:        "metrics_2026",
					Bounds:      "FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')",
					PartitionBy: &ir.PartitionSpec{Strategy: "HASH", Columns: []string{"id"}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	hasInstruction := false
	for _, o := range ops {
		if o.Mode() == pipeline.ModeInstructionOnly && o.Safety() == pipeline.Destructive {
			hasInstruction = true
		}
	}
	if !hasInstruction {
		t.Errorf("expected a Destructive instruction-only op for sub-partition strategy change, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP TABLE") {
		t.Errorf("strategy change should not auto-drop, got: %v", sqlList(ops))
	}
}

func TestDiffPartitionStrategyChangedIsInstruction(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"region"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	hasInstruction := false
	for _, o := range ops {
		if o.Mode() == pipeline.ModeInstructionOnly && o.Safety() == pipeline.Destructive {
			hasInstruction = true
		}
	}
	if !hasInstruction {
		t.Errorf("expected a Destructive instruction-only op for partition strategy change, got: %v", sqlList(ops))
	}
}

// ── Column-level grant tracking ───────────────────────────────────────────────

func TestDiffCreateTableEmitsColumnGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{
					Name: "body",
					Type: ir.TypeRef{Name: "text"},
					Grants: []ir.Grant{
						{Privileges: []string{"SELECT"}, Roles: []string{"reader"}},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT SELECT ("body")`) {
		t.Errorf("expected column-level GRANT SELECT (body), got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "ON TABLE") {
		t.Errorf("expected ON TABLE in column grant, got: %v", sqlList(ops))
	}
}

func TestDiffColumnGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "docs",
			Columns: []snapshot.SnapColumn{{Name: "body", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{
					Name: "body",
					Type: ir.TypeRef{Name: "text"},
					Grants: []ir.Grant{
						{Privileges: []string{"SELECT"}, Roles: []string{"analyst"}},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT SELECT ("body")`) {
		t.Errorf("expected column GRANT SELECT, got: %v", sqlList(ops))
	}
}

func TestDiffColumnGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "docs",
			Columns: []snapshot.SnapColumn{
				{
					Name:   "body",
					Type:   "text",
					Grants: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"analyst"}}},
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "docs",
			Columns: []*ir.Column{{Name: "body", Type: ir.TypeRef{Name: "text"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE SELECT ("body")`) {
		t.Errorf("expected column REVOKE SELECT, got: %v", sqlList(ops))
	}
}

func TestDiffCreateTableEmitsColumnRevocation(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{
					Name:        "body",
					Type:        ir.TypeRef{Name: "text"},
					Revocations: []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE SELECT ("body")`) {
		t.Errorf("expected column-level REVOKE SELECT (body) at create time, got: %v", sqlList(ops))
	}
}

func TestDiffColumnRevocationAddedAndRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "docs",
			Columns: []snapshot.SnapColumn{{Name: "body", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{
					Name:        "body",
					Type:        ir.TypeRef{Name: "text"},
					Revocations: []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE SELECT ("body")`) {
		t.Errorf("expected column REVOKE SELECT when adding a revocation, got: %v", sqlList(ops))
	}

	// Now remove it: the snapshot side has the revocation, desired doesn't.
	snap2 := &pipeline.Snapshot{}
	_ = snap2.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "docs",
			Columns: []snapshot.SnapColumn{
				{
					Name:        "body",
					Type:        "text",
					Revocations: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
				},
			},
		},
	})
	desired2 := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "docs",
			Columns: []*ir.Column{{Name: "body", Type: ir.TypeRef{Name: "text"}}},
		},
	}
	ops2, err := d.Diff(desired2, snap2)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops2, `GRANT SELECT ("body")`) {
		t.Errorf("expected column GRANT SELECT (body) to restore, got: %v", sqlList(ops2))
	}
}

func TestDiffColumnRevocationUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "docs",
			Columns: []snapshot.SnapColumn{
				{
					Name:        "body",
					Type:        "text",
					Revocations: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{
					Name:        "body",
					Type:        ir.TypeRef{Name: "text"},
					Revocations: []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "GRANT") || containsSQL(ops, "REVOKE") {
		t.Errorf("expected no GRANT/REVOKE for unchanged column revocation, got: %v", sqlList(ops))
	}
}

// New column added via ALTER TABLE ADD COLUMN, carrying its own REVOKE —
// exercises the other diffColumns call site (new-column path vs
// existing-column path already covered above).
func TestDiffAddColumnEmitsRevocation(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "docs",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{
					Name:        "body",
					Type:        ir.TypeRef{Name: "text"},
					Revocations: []ir.Revocation{{Privileges: []string{"SELECT"}, Roles: []string{"guest"}}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE SELECT ("body")`) {
		t.Errorf("expected column REVOKE SELECT for a newly added column, got: %v", sqlList(ops))
	}
}

func TestDiffColumnGrantUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "docs",
			Columns: []snapshot.SnapColumn{
				{
					Name:   "body",
					Type:   "text",
					Grants: []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"analyst"}}},
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{
					Name: "body",
					Type: ir.TypeRef{Name: "text"},
					Grants: []ir.Grant{
						{Privileges: []string{"SELECT"}, Roles: []string{"analyst"}},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "GRANT") || containsSQL(ops, "REVOKE") {
		t.Errorf("expected no grant ops when column grant unchanged, got: %v", sqlList(ops))
	}
}

func TestDiffAddColumnEmitsGrant(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.docs", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "docs",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "docs",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{
					Name: "secret",
					Type: ir.TypeRef{Name: "text"},
					Grants: []ir.Grant{
						{Privileges: []string{"SELECT"}, Roles: []string{"admin"}},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ADD COLUMN") {
		t.Errorf("expected ADD COLUMN, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `GRANT SELECT ("secret")`) {
		t.Errorf("expected column grant after ADD COLUMN, got: %v", sqlList(ops))
	}
}

func TestDiffPartitionUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "RANGE (created_at)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_2024", Bound: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"created_at"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "events_2024", Bounds: "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "PARTITION") {
		t.Errorf("expected no partition ops when unchanged, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionConstraintDroppedViaOnly guards RFC Section 7.3's "DROP
// CONSTRAINT ... ONLY" gap (PostgreSQL 18+): a constraint disappearing from
// the parent's own declaration while a direct partition independently
// declares a local constraint of that same name must emit
// "ALTER TABLE ONLY parent DROP CONSTRAINT" (SAFE), not the ordinary
// cascading drop — and must NOT also emit a redundant ADD CONSTRAINT for the
// partition (already physically local there as a result of the ONLY drop).
func TestDiffPartitionConstraintDroppedViaOnly(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: "RANGE (id)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}, {Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck_amount", Type: "CHECK", Expr: "CHECK (amount > 0)"},
			},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "orders_1", Bound: "FOR VALUES FROM (1) TO (1000)"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "orders",
			PartitionBy: &ir.PartitionSpec{
				Strategy: "RANGE", Columns: []string{"id"},
			},
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
			},
			Partitions: []*ir.Partition{
				{
					Name:   "orders_1",
					Bounds: "FOR VALUES FROM (1) TO (1000)",
					Constraints: []*ir.Constraint{
						{Name: "ck_amount", Type: "CHECK", Expr: "CHECK (amount > 0)"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE ONLY "public"."orders" DROP CONSTRAINT "ck_amount";`
	op := findOpSQL(ops, want)
	if op == nil {
		t.Fatalf("expected %q, got: %v", want, sqlList(ops))
	}
	if op.Safety() != pipeline.Safe {
		t.Errorf("ONLY drop safety = %v, want Safe", op.Safety())
	}
	if containsSQL(ops, "ADD CONSTRAINT") {
		t.Errorf("expected no redundant ADD CONSTRAINT on the partition, got: %v", sqlList(ops))
	}
	if containsSQL(ops, `ALTER TABLE "public"."orders" DROP CONSTRAINT`) {
		t.Errorf("expected the ONLY variant, not a plain cascading drop, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionConstraintPlainDropWhenNotRetained confirms the ordinary,
// pre-existing behavior is unchanged when no partition retains the
// constraint locally: a plain cascading DESTRUCTIVE drop, no ONLY.
func TestDiffPartitionConstraintPlainDropWhenNotRetained(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: "RANGE (id)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}, {Name: "amount", Type: "numeric"}},
			Constraints: []snapshot.SnapConstraint{
				{Name: "ck_amount", Type: "CHECK", Expr: "CHECK (amount > 0)"},
			},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "orders_1", Bound: "FOR VALUES FROM (1) TO (1000)"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"id"}},
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "amount", Type: ir.TypeRef{Name: "numeric"}},
			},
			Partitions: []*ir.Partition{
				{Name: "orders_1", Bounds: "FOR VALUES FROM (1) TO (1000)"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders" DROP CONSTRAINT "ck_amount";`
	op := findOpSQL(ops, want)
	if op == nil {
		t.Fatalf("expected plain drop %q, got: %v", want, sqlList(ops))
	}
	if op.Safety() != pipeline.Destructive {
		t.Errorf("plain drop safety = %v, want Destructive", op.Safety())
	}
	if containsSQL(ops, "ONLY") {
		t.Errorf("expected no ONLY variant when no partition retains the constraint, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionLocalConstraintAdded guards the ordinary case: a
// constraint declared directly on an already-existing partition, unrelated
// to anything on the parent — a plain CAUTION ADD CONSTRAINT, no PostgreSQL
// version dependency.
func TestDiffPartitionLocalConstraintAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: "RANGE (id)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "orders_1", Bound: "FOR VALUES FROM (1) TO (1000)"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"id"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{
					Name:   "orders_1",
					Bounds: "FOR VALUES FROM (1) TO (1000)",
					Constraints: []*ir.Constraint{
						{Name: "ck_id_positive", Type: "CHECK", Expr: "CHECK (id > 0)"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders_1" ADD CONSTRAINT "ck_id_positive" CHECK (id > 0);`
	op := findOpSQL(ops, want)
	if op == nil {
		t.Fatalf("expected %q, got: %v", want, sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("ADD CONSTRAINT safety = %v, want Caution", op.Safety())
	}
}

// TestDiffPartitionLocalConstraintDropped guards the ordinary removal case:
// a partition's own local constraint disappearing from desired emits a
// plain DESTRUCTIVE drop targeting the partition itself.
func TestDiffPartitionLocalConstraintDropped(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: "RANGE (id)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Partitions: []snapshot.SnapPartition{
				{
					Schema: "public", Name: "orders_1", Bound: "FOR VALUES FROM (1) TO (1000)",
					Constraints: []snapshot.SnapConstraint{
						{Name: "ck_id_positive", Type: "CHECK", Expr: "CHECK (id > 0)"},
					},
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"id"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{Name: "orders_1", Bounds: "FOR VALUES FROM (1) TO (1000)"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders_1" DROP CONSTRAINT "ck_id_positive";`
	op := findOpSQL(ops, want)
	if op == nil {
		t.Fatalf("expected %q, got: %v", want, sqlList(ops))
	}
	if op.Safety() != pipeline.Destructive {
		t.Errorf("DROP CONSTRAINT safety = %v, want Destructive", op.Safety())
	}
}

// TestDiffCreateNewPartitionWithLocalConstraint guards a brand-new partition
// declaring its own local constraint from the start: the CREATE must be
// followed by a separate ADD CONSTRAINT (createPartitionOps never inlines
// constraints into the PARTITION OF statement itself).
func TestDiffCreateNewPartitionWithLocalConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: "RANGE (id)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "orders",
			PartitionBy: &ir.PartitionSpec{Strategy: "RANGE", Columns: []string{"id"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Partitions: []*ir.Partition{
				{
					Name:   "orders_1",
					Bounds: "FOR VALUES FROM (1) TO (1000)",
					Constraints: []*ir.Constraint{
						{Name: "ck_id_positive", Type: "CHECK", Expr: "CHECK (id > 0)"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `CREATE TABLE "public"."orders_1" PARTITION OF "public"."orders" FOR VALUES FROM (1) TO (1000);`) {
		t.Errorf("expected bare CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."orders_1" ADD CONSTRAINT "ck_id_positive" CHECK (id > 0);`) {
		t.Errorf("expected ADD CONSTRAINT after CREATE, got: %v", sqlList(ops))
	}
}

// TestDiffCreateTableWithForeignPartition guards RFC Section 7.13's
// "FOREIGN partition-name ... SERVER server_name [OPTIONS (...)]" form:
// createPartitionOps must emit CREATE FOREIGN TABLE ... PARTITION OF, not
// CREATE TABLE (a real PostgreSQL syntax error for a foreign table), with
// SERVER/OPTIONS appended in PostgreSQL's own required clause order.
func TestDiffCreateTableWithForeignPartition(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "events",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "bigint"}},
				{Name: "region", Type: ir.TypeRef{Name: "text"}},
			},
			PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"region"}},
			Partitions: []*ir.Partition{
				{
					Name:          "events_archive",
					Bounds:        "DEFAULT",
					Foreign:       true,
					ForeignServer: strPtr("archive_server"),
					ForeignOptions: []pipeline.StorageParam{
						{Key: "table_name", Value: "events_archive"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE FOREIGN TABLE "public"."events_archive" PARTITION OF "public"."events" DEFAULT SERVER "archive_server" OPTIONS (table_name 'events_archive');`
	if !containsSQL(ops, want) {
		t.Errorf("expected exact partition CREATE statement %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffPartitionForeignUnchangedIsNoop mirrors
// TestDiffPartitionUnchangedIsNoop for a FOREIGN partition: an already-
// applied foreign partition (same bound, same Foreign flag) must not
// generate any partition-recreate op on a second plan.
func TestDiffPartitionForeignUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "LIST (region)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}, {Name: "region", Type: "text"}},
			Partitions: []snapshot.SnapPartition{
				{
					Schema:         "public",
					Name:           "events_archive",
					Bound:          "DEFAULT",
					Foreign:        true,
					ForeignServer:  strPtr("archive_server"),
					ForeignOptions: "table_name=events_archive",
				},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"region"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}, {Name: "region", Type: ir.TypeRef{Name: "text"}}},
			Partitions: []*ir.Partition{
				{
					Name:          "events_archive",
					Bounds:        "DEFAULT",
					Foreign:       true,
					ForeignServer: strPtr("archive_server"),
					ForeignOptions: []pipeline.StorageParam{
						{Key: "table_name", Value: "events_archive"},
					},
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "PARTITION") {
		t.Errorf("expected no partition ops when unchanged, got: %v", sqlList(ops))
	}
}

// TestDiffPartitionForeignnessChangedDropsAndRecreates guards the case
// where an existing partition's Foreign flag itself changes (declared
// FOREIGN on a partition the snapshot has as an ordinary table, or vice
// versa) — real PostgreSQL cannot ALTER a table's relkind in place, so this
// must drop under the CURRENT (snapshot) kind and recreate under the new
// one, exactly like a bound change.
func TestDiffPartitionForeignnessChangedDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "LIST (region)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}, {Name: "region", Type: "text"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_archive", Bound: "DEFAULT"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"region"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}, {Name: "region", Type: ir.TypeRef{Name: "text"}}},
			Partitions: []*ir.Partition{
				{Name: "events_archive", Bounds: "DEFAULT", Foreign: true, ForeignServer: strPtr("srv")},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TABLE "public"."events_archive";`) {
		t.Errorf("expected DROP TABLE (the CURRENT, non-foreign kind) for the Foreign-ness change, got: %v", sqlList(ops))
	}
	want := `CREATE FOREIGN TABLE "public"."events_archive" PARTITION OF "public"."events" DEFAULT SERVER "srv";`
	if !containsSQL(ops, want) {
		t.Errorf("expected exact partition CREATE statement %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffPartitionRemovedForeignUsesDropForeignTable guards the removal
// path's DROP verb: real PostgreSQL rejects DROP TABLE on a foreign table
// ("... is not a table", confirmed live) — a removed foreign partition must
// be dropped via DROP FOREIGN TABLE, not the plain DROP TABLE a regular
// partition removal gets.
func TestDiffPartitionRemovedForeignUsesDropForeignTable(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.events", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "events",
			PartitionBy: "LIST (region)",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}, {Name: "region", Type: "text"}},
			Partitions: []snapshot.SnapPartition{
				{Schema: "public", Name: "events_archive", Bound: "DEFAULT", Foreign: true, ForeignServer: strPtr("srv")},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:      "public",
			Name:        "events",
			PartitionBy: &ir.PartitionSpec{Strategy: "LIST", Columns: []string{"region"}},
			Columns:     []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}, {Name: "region", Type: ir.TypeRef{Name: "text"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP FOREIGN TABLE "public"."events_archive";`) {
		t.Errorf("expected DROP FOREIGN TABLE for removed foreign partition, got: %v", sqlList(ops))
	}
}

// ── MIGRATE REMOVE ─────────────────────────────────────────────────────────

func TestDiffEnumRemoveRequiresMigrateRemoveBlock(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "inactive", "cancelled"}},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"active", "inactive"}},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected error when MIGRATE REMOVE block is absent")
	}
	if !strings.Contains(err.Error(), "MIGRATE REMOVE") {
		t.Errorf("expected MIGRATE REMOVE in error, got: %s", err)
	}
	// DPG-E014 (unguarded_enum_removal, RFC Appendix C).
	cerr, ok := err.(*pipeline.CompilerError)
	if !ok {
		t.Fatalf("expected *pipeline.CompilerError, got %T: %v", err, err)
	}
	if cerr.Code != "DPG-E014" {
		t.Errorf("Code: got %q, want DPG-E014", cerr.Code)
	}
}

func TestDiffEnumRemoveEmitsShadowTypeAndDrop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "inactive", "cancelled"}},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"active", "inactive"},
			MigrateRemove: &pipeline.MigrateRemoveBlock{
				SQL: pipeline.RawExpr{Text: "UPDATE orders SET status = 'active' WHERE status = 'cancelled';"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	sqls := sqlList(ops)
	if !containsSQL(ops, "CREATE TYPE") || !containsSQL(ops, "__dpg_new") {
		t.Errorf("expected shadow type creation, got: %v", sqls)
	}
	if !containsSQL(ops, "UPDATE orders") {
		t.Errorf("expected DML passthrough, got: %v", sqls)
	}
	if !containsSQL(ops, "DROP TYPE") {
		t.Errorf("expected DROP TYPE, got: %v", sqls)
	}
	if !containsSQL(ops, "RENAME TO") {
		t.Errorf("expected RENAME TO, got: %v", sqls)
	}
}

func TestDiffEnumRemoveAltersAffectedColumns(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"open", "closed", "archived"}},
	})
	_ = snap.SetObject("public.tickets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "tickets",
			Columns: []snapshot.SnapColumn{
				{Name: "id", Type: "bigint"},
				{Name: "state", Type: "public.status"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"open", "closed"},
			MigrateRemove: &pipeline.MigrateRemoveBlock{
				SQL: pipeline.RawExpr{Text: "UPDATE tickets SET state = 'closed' WHERE state = 'archived';"},
			},
		},
		&ir.Table{
			Schema:  "public",
			Name:    "tickets",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}, {Name: "state", Type: ir.TypeRef{Schema: "public", Name: "status"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	sqls := sqlList(ops)
	if !containsSQL(ops, "ALTER TABLE") || !containsSQL(ops, "ALTER COLUMN") || !containsSQL(ops, "TYPE") {
		t.Errorf("expected ALTER COLUMN TYPE for affected column, got: %v", sqls)
	}
	if !containsSQL(ops, "tickets") {
		t.Errorf("expected affected table tickets in ops, got: %v", sqls)
	}
	if !containsSQL(ops, "RAISE EXCEPTION") {
		t.Errorf("expected verification DO block, got: %v", sqls)
	}
	// DPG-E013 (enum_migration_data_remains, RFC Appendix C) — a runtime
	// RAISE EXCEPTION, not a compile-time CompilerError, so the code is
	// embedded directly in the raised message text (same precedent as
	// DPG-E036/DPG-E012 elsewhere in this codebase).
	if !containsSQL(ops, "DPG-E013") {
		t.Errorf("expected DPG-E013 in the verification DO block's RAISE EXCEPTION message, got: %v", sqls)
	}
}

func TestDiffEnumRemoveNoAffectedColumnsSkipsAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.color", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "color", Variant: "ENUM",
			Values: []string{"red", "green", "blue"}},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "color", Variant: "ENUM",
			EnumValues: []string{"red", "green"},
			MigrateRemove: &pipeline.MigrateRemoveBlock{
				SQL: pipeline.RawExpr{Text: "DELETE FROM palette WHERE color = 'blue';"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "ALTER TABLE") {
		t.Errorf("expected no ALTER TABLE when no columns reference the type, got: %v", sqlList(ops))
	}
	// Shadow type and rename must still be emitted.
	if !containsSQL(ops, "__dpg_new") {
		t.Errorf("expected shadow type, got: %v", sqlList(ops))
	}
}

func TestDiffEnumRemovePreservesComment(t *testing.T) {
	d := New()
	comment := "order lifecycle"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.order_status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "order_status", Variant: "ENUM",
			Values: []string{"pending", "shipped", "cancelled"}},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "order_status", Variant: "ENUM",
			EnumValues: []string{"pending", "shipped"},
			Comment:    &comment,
			MigrateRemove: &pipeline.MigrateRemoveBlock{
				SQL: pipeline.RawExpr{Text: "UPDATE orders SET status = 'shipped' WHERE status = 'cancelled';"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON TYPE") {
		t.Errorf("expected COMMENT ON TYPE after rename, got: %v", sqlList(ops))
	}
}

func TestDiffEnumAddValueUnchangedByMigrateRemove(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "status", Variant: "ENUM",
			Values: []string{"active", "inactive"}},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"active", "inactive", "pending"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ADD VALUE") {
		t.Errorf("expected ADD VALUE for added enum value, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "__dpg_new") {
		t.Errorf("ADD VALUE should not trigger MIGRATE REMOVE procedure, got: %v", sqlList(ops))
	}
}

// ── Materialized view comment uses correct SQL object type ─────────────────

// ── Aggregate semantic diffing ────────────────────────────────────────────────

func TestDiffCreateAggregateEmitsBody(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)",
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE AGGREGATE") {
		t.Errorf("expected CREATE AGGREGATE, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP AGGREGATE") {
		t.Errorf("unexpected DROP AGGREGATE on create, got: %v", sqlList(ops))
	}
}

func TestDiffCreateAggregateEmitsComment(t *testing.T) {
	d := New()
	comment := "sums numerics"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema:  "public",
			Name:    "my_agg",
			Args:    []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:    "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)",
			Comment: &comment,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON AGGREGATE") {
		t.Errorf("expected COMMENT ON AGGREGATE, got: %v", sqlList(ops))
	}
}

func TestDiffCreateAggregateEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)",
			Grants: []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"analyst"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT EXECUTE ON FUNCTION") {
		t.Errorf("expected GRANT EXECUTE ON FUNCTION, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateBodyChangedDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	oldHash := fmt.Sprintf("%x", sha256Sum("CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"))
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: oldHash,
		},
	})
	newBody := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = float8_accum, STYPE = float8[])"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   newBody,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP AGGREGATE IF EXISTS") {
		t.Errorf("expected DROP AGGREGATE IF EXISTS, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE AGGREGATE") {
		t.Errorf("expected CREATE AGGREGATE, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateBodyChangedDropIsDestructive(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	oldHash := fmt.Sprintf("%x", sha256Sum("CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"))
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: oldHash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = float8_accum, STYPE = float8[])",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP AGGREGATE") {
			if o.Safety() != pipeline.Destructive {
				t.Errorf("expected Destructive safety for DROP AGGREGATE, got %s", o.Safety())
			}
			return
		}
	}
	t.Error("DROP AGGREGATE op not found")
}

func TestDiffAggregateCommentOnlyChange(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	bodyHash := fmt.Sprintf("%x", sha256Sum(body))
	oldComment := "old comment"
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric",
			BodyHash: bodyHash, Comment: &oldComment,
		},
	})
	newComment := "updated comment"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema:  "public",
			Name:    "my_agg",
			Args:    []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:    body,
			Comment: &newComment,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP AGGREGATE") {
		t.Errorf("unexpected DROP AGGREGATE for comment-only change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "COMMENT ON AGGREGATE") {
		t.Errorf("expected COMMENT ON AGGREGATE, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	bodyHash := fmt.Sprintf("%x", sha256Sum(body))
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: bodyHash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   body,
			Grants: []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"analyst"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP AGGREGATE") {
		t.Errorf("unexpected DROP AGGREGATE for grant-only change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "GRANT EXECUTE ON FUNCTION") {
		t.Errorf("expected GRANT EXECUTE ON FUNCTION, got: %v", sqlList(ops))
	}
}

// ── AGGREGATE Revocations ──────────────────────────────────────────────────
// Regression guard mirroring TestDiffCreateProcedureEmitsRevocation and
// friends: ir.Aggregate had no Revocations field at all, so a declared
// REVOKES block was parsed but silently dropped everywhere downstream
// (build, snapshot, diff, dump) — only the GRANT half ever took effect.

func TestDiffCreateAggregateEmitsRevocation(t *testing.T) {
	d := New()
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema:      "public",
			Name:        "my_agg",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:        body,
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE EXECUTE ON FUNCTION") {
		t.Errorf("expected REVOKE EXECUTE ON FUNCTION at create time, got: %v", sqlList(ops))
	}
	if containsSQL(ops, `"PUBLIC"`) {
		t.Errorf("PUBLIC must not be quoted, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateRevocationAdded(t *testing.T) {
	d := New()
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	bodyHash := fmt.Sprintf("%x", sha256Sum(body))
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: bodyHash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema:      "public",
			Name:        "my_agg",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:        body,
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "REVOKE EXECUTE ON FUNCTION") {
			found = true
			if o.Safety() != pipeline.Caution {
				t.Errorf("explicit revocation safety = %v, want Caution: %s", o.Safety(), o.SQL())
			}
		}
	}
	if !found {
		t.Errorf("expected REVOKE EXECUTE ON FUNCTION, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateRevocationRemoved(t *testing.T) {
	d := New()
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	bodyHash := fmt.Sprintf("%x", sha256Sum(body))
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: bodyHash,
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   body,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT EXECUTE ON FUNCTION") {
		t.Errorf("expected GRANT EXECUTE ON FUNCTION to restore the revoked privilege, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE") {
		t.Errorf("must not also emit REVOKE when the revocation itself was removed: %v", sqlList(ops))
	}
}

func TestDiffAggregateRevocationUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	bodyHash := fmt.Sprintf("%x", sha256Sum(body))
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: bodyHash,
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema:      "public",
			Name:        "my_agg",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:        body,
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "GRANT") || containsSQL(ops, "REVOKE") {
		t.Errorf("expected no GRANT/REVOKE for unchanged revocation, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	body := "CREATE AGGREGATE public.my_agg(numeric) (SFUNC = numeric_add, STYPE = numeric)"
	bodyHash := fmt.Sprintf("%x", sha256Sum(body))
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric", BodyHash: bodyHash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   body,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged aggregate, got: %v", sqlList(ops))
	}
}

// ── AGGREGATE structural Options comparison (C.3 fix) ──────────────────────
// Regression guard for the audit's live-reproduced bug: `diffAggregate`
// compared raw BodyHash even when the snap side came from introspection's
// catalog reconstruction, so a cosmetically-different but catalog-identical
// Body (e.g. lowercase "sfunc = ..." vs. hand-written "SFUNC = ...") always
// registered as bodyChanged, producing a spurious DESTRUCTIVE DROP+CREATE
// on every single `plan --live`/`apply`, even with zero real changes.

func TestDiffAggregateStructuredOptionsCosmeticBodyDriftIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Snap's Body/BodyHash simulate an introspected reconstruction: same
	// options, different casing/spacing from hand-written source.
	liveBody := "CREATE AGGREGATE public.my_agg (numeric) (sfunc = numeric_add, stype = numeric, initcond = '0')"
	liveHash := fmt.Sprintf("%x", sha256Sum(liveBody))
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric",
			BodyHash:                   liveHash,
			AggregateOptionsStructured: true,
			AggregateOptions: []snapshot.SnapOptionKV{
				{Key: "sfunc", Value: "numeric_add"},
				{Key: "stype", Value: "numeric"},
				{Key: "initcond", Value: "'0'"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   "CREATE AGGREGATE audit_sum (numeric) (SFUNC = numeric_add, STYPE = numeric, INITCOND = '0')",
			Options: []pipeline.StorageParam{
				{Key: "sfunc", Value: "numeric_add"},
				{Key: "stype", Value: "numeric"},
				{Key: "initcond", Value: "'0'"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for a cosmetically-different but structurally identical aggregate, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateStructuredOptionsGenuineChangeStillDrops(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	liveBody := "CREATE AGGREGATE public.my_agg (numeric) (sfunc = numeric_add, stype = numeric)"
	liveHash := fmt.Sprintf("%x", sha256Sum(liveBody))
	_ = snap.SetObject(`public.my_agg(numeric)`, &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "my_agg", Args: "numeric",
			BodyHash:                   liveHash,
			AggregateOptionsStructured: true,
			AggregateOptions: []snapshot.SnapOptionKV{
				{Key: "sfunc", Value: "numeric_add"},
				{Key: "stype", Value: "numeric"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public",
			Name:   "my_agg",
			Args:   []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:   "CREATE AGGREGATE public.my_agg (numeric) (SFUNC = float8_accum, STYPE = float8)",
			Options: []pipeline.StorageParam{
				{Key: "sfunc", Value: "float8_accum"},
				{Key: "stype", Value: "float8"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP AGGREGATE IF EXISTS") {
		t.Errorf("expected DROP AGGREGATE IF EXISTS for a genuine option change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE AGGREGATE") {
		t.Errorf("expected CREATE AGGREGATE for a genuine option change, got: %v", sqlList(ops))
	}
}

func TestDiffMaterViewCommentUsesCorrectKind(t *testing.T) {
	d := New()
	comment := "a summary view"
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:       "public",
			Name:         "mv_summary",
			Query:        "SELECT 1",
			Materialized: true,
			Comment:      &comment,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "COMMENT ON VIEW") {
		t.Errorf("materialized view comment should use COMMENT ON MATERIALIZED VIEW, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "COMMENT ON MATERIALIZED VIEW") {
		t.Errorf("expected COMMENT ON MATERIALIZED VIEW, got: %v", sqlList(ops))
	}
}

// ── Extension diff ────────────────────────────────────────────────────────────

func TestDiffCreateExtension(t *testing.T) {
	d := New()
	schema := "public"
	ver := "1.3"
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto", Schema: &schema, Version: &ver},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE EXTENSION IF NOT EXISTS") || !containsSQL(ops, `"pgcrypto"`) ||
		!containsSQL(ops, `SCHEMA "public"`) || !containsSQL(ops, "VERSION '1.3'") {
		t.Errorf("expected CREATE EXTENSION with schema+version, got: %v", sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

// TestDiffCreateExtensionCascade guards RFC audit item #15: a declared
// CASCADE must appear in the emitted CREATE EXTENSION statement.
func TestDiffCreateExtensionCascade(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pg_trgm", Cascade: true},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CASCADE") {
		t.Errorf("expected CASCADE in CREATE EXTENSION, got: %v", sqlList(ops))
	}
}

// TestDiffExtensionSchemaChangeEmitsSetSchema guards RFC audit item #50:
// real PostgreSQL supports ALTER EXTENSION ... SET SCHEMA (confirmed via
// pg_query.Parse), so a SCHEMA change must be a targeted SAFE ALTER, not a
// destructive drop + recreate — previously SnapExtension.Schema existed but
// diffExtension never compared it at all, a spurious no-op.
func TestDiffExtensionSchemaChangeEmitsSetSchema(t *testing.T) {
	d := New()
	oldSchema, newSchema := "public", "extensions"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("postgis", &snapshot.SnapObject{
		Kind:      "extension",
		Extension: &snapshot.SnapExtension{Name: "postgis", Schema: &oldSchema},
	})
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "postgis", Schema: &newSchema},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP EXTENSION") {
		t.Errorf("expected no DROP EXTENSION for a schema change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER EXTENSION "postgis" SET SCHEMA "extensions"`) {
		t.Errorf("expected ALTER EXTENSION SET SCHEMA, got: %v", sqlList(ops))
	}
	setSchemaOp := ops[0]
	if setSchemaOp.Safety() != pipeline.Safe {
		t.Errorf("expected ALTER EXTENSION SET SCHEMA to be Safe, got %s", setSchemaOp.Safety())
	}
}

// TestDiffExtensionSchemaUnspecifiedIsNoop proves the nil-means-unspecified
// convention: a source that never mentions SCHEMA must not touch an
// existing extension's schema.
func TestDiffExtensionSchemaUnspecifiedIsNoop(t *testing.T) {
	d := New()
	oldSchema := "public"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("pgcrypto", &snapshot.SnapObject{
		Kind:      "extension",
		Extension: &snapshot.SnapExtension{Name: "pgcrypto", Schema: &oldSchema},
	})
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops when SCHEMA unspecified in source, got: %v", sqlList(ops))
	}
}

func TestDiffExtensionUnchangedIsNoop(t *testing.T) {
	d := New()
	ver := "1.0"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("pgcrypto", &snapshot.SnapObject{
		Kind:      "extension",
		Extension: &snapshot.SnapExtension{Name: "pgcrypto", Version: &ver},
	})
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto", Version: &ver},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged extension, got: %v", sqlList(ops))
	}
}

func TestDiffExtensionVersionUpdated(t *testing.T) {
	d := New()
	old, new_ := "1.0", "1.1"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("pgcrypto", &snapshot.SnapObject{
		Kind:      "extension",
		Extension: &snapshot.SnapExtension{Name: "pgcrypto", Version: &old},
	})
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto", Version: &new_},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER EXTENSION") || !containsSQL(ops, "UPDATE TO") || !containsSQL(ops, "'1.1'") {
		t.Errorf("expected ALTER EXTENSION ... UPDATE TO '1.1', got: %v", sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

// ── Extension Comment (ir.Extension previously had no Comment field at all;
// COMMENT ON EXTENSION is valid, real PostgreSQL syntax but was silently
// dropped by build/snapshot/diff/dump) ─────────────────────────────────────

func TestDiffCreateExtensionEmitsComment(t *testing.T) {
	d := New()
	comment := "crypto functions"
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto", Comment: &comment},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON EXTENSION") || !containsSQL(ops, "'crypto functions'") {
		t.Errorf("expected COMMENT ON EXTENSION at create time, got: %v", sqlList(ops))
	}
}

func TestDiffExtensionCommentAdded(t *testing.T) {
	d := New()
	ver := "1.0"
	comment := "crypto functions"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("pgcrypto", &snapshot.SnapObject{
		Kind:      "extension",
		Extension: &snapshot.SnapExtension{Name: "pgcrypto", Version: &ver},
	})
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto", Version: &ver, Comment: &comment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON EXTENSION") || !containsSQL(ops, "'crypto functions'") {
		t.Errorf("expected COMMENT ON EXTENSION, got: %v", sqlList(ops))
	}
}

func TestDiffExtensionCommentRemoved(t *testing.T) {
	d := New()
	ver := "1.0"
	comment := "crypto functions"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("pgcrypto", &snapshot.SnapObject{
		Kind:      "extension",
		Extension: &snapshot.SnapExtension{Name: "pgcrypto", Version: &ver, Comment: &comment},
	})
	desired := []pipeline.IRObject{
		&ir.Extension{Name: "pgcrypto", Version: &ver},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON EXTENSION") || !containsSQL(ops, "IS NULL") {
		t.Errorf("expected COMMENT ON EXTENSION ... IS NULL, got: %v", sqlList(ops))
	}
}

// ── Sequence diff ─────────────────────────────────────────────────────────────

func TestDiffCreateSequence(t *testing.T) {
	d := New()
	inc, start, cache := int64(2), int64(100), int64(10)
	cyc := true
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", IncrementBy: &inc, StartValue: &start, Cache: &cache, Cycle: &cyc},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE SEQUENCE IF NOT EXISTS") || !containsSQL(ops, "INCREMENT BY 2") ||
		!containsSQL(ops, "START WITH 100") || !containsSQL(ops, "CACHE 10") || !containsSQL(ops, "CYCLE") {
		t.Errorf("expected CREATE SEQUENCE with all params + CYCLE, got: %v", sqlList(ops))
	}
}

// TestDiffCreateUnloggedSequence guards RFC Section 10's UNLOGGED prefix —
// createSequence never rendered it at all before this.
func TestDiffCreateUnloggedSequence(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "session_counter", Unlogged: true},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE UNLOGGED SEQUENCE IF NOT EXISTS") {
		t.Errorf("expected CREATE UNLOGGED SEQUENCE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceUnloggedToggledIsCautionAlter proves toggling UNLOGGED on
// an existing sequence (Section 10's own diffing table) emits a targeted
// CAUTION ALTER SEQUENCE ... SET UNLOGGED/LOGGED, not a recreate — the same
// mechanism as an unlogged table's identical toggle (Section 7.12).
func TestDiffSequenceUnloggedToggledIsCautionAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Unlogged: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER SEQUENCE "public"."seq_id" SET UNLOGGED;`) {
		t.Errorf("expected ALTER SEQUENCE ... SET UNLOGGED, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "SET UNLOGGED") && op.Safety() != pipeline.Caution {
			t.Errorf("SET UNLOGGED safety = %v, want Caution", op.Safety())
		}
	}
}

func TestDiffSequenceLoggedToggledEmitsSetLogged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", Unlogged: true},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER SEQUENCE "public"."seq_id" SET LOGGED;`) {
		t.Errorf("expected ALTER SEQUENCE ... SET LOGGED, got: %v", sqlList(ops))
	}
}

// TestDiffTableUnloggedToggledIsCautionAlter is the regression guard for
// RFC Section 7.12's LOGGED/UNLOGGED toggle: diffTable had zero handling
// for Unlogged at all — createTable read it at CREATE time, but a change on
// an already-applied table was a silent no-op — despite the RFC's own E.13
// changelog entry claiming this was already "folded in" as a
// DESTRUCTIVE-to-safe-ALTER swap. Mirrors Sequence's identical toggle
// (Section 10, TestDiffSequenceUnloggedToggledIsCautionAlter).
func TestDiffTableUnloggedToggledIsCautionAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t"},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", Unlogged: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" SET UNLOGGED;`) {
		t.Errorf("expected ALTER TABLE ... SET UNLOGGED, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "SET UNLOGGED") && op.Safety() != pipeline.Caution {
			t.Errorf("SET UNLOGGED safety = %v, want Caution", op.Safety())
		}
	}
}

func TestDiffTableLoggedToggledEmitsSetLogged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t", Unlogged: true},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."t" SET LOGGED;`) {
		t.Errorf("expected ALTER TABLE ... SET LOGGED, got: %v", sqlList(ops))
	}
}

// TestDiffForeignTableUnloggedNeverCompared proves a foreign table (which
// has no local storage, so LOGGED/UNLOGGED is meaningless) never emits the
// toggle even if Unlogged somehow differs — matching createTable's own
// mutually-exclusive Unlogged/Foreign switch.
func TestDiffForeignTableUnloggedNeverCompared(t *testing.T) {
	d := New()
	server := "srv"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind:  "table",
		Table: &snapshot.SnapTable{Schema: "public", Name: "t", Foreign: true, ForeignServer: &server},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", Foreign: true, ForeignServer: &server, Unlogged: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "SET UNLOGGED") || containsSQL(ops, "SET LOGGED") {
		t.Errorf("expected no LOGGED/UNLOGGED toggle for a foreign table, got: %v", sqlList(ops))
	}
}

// TestDiffCreateSequenceAsTypeAndOwnedBy guards RFC audit item #14: AS type
// and OWNED BY were completely unimplemented — breaking the RFC's own
// canonical example verbatim.
func TestDiffCreateSequenceAsTypeAndOwnedBy(t *testing.T) {
	d := New()
	owned := "orders.order_number"
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "order_number_seq", AsType: &ir.TypeRef{Name: "bigint"}, OwnedBy: &owned},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE SEQUENCE IF NOT EXISTS") || !containsSQL(ops, " AS bigint") {
		t.Errorf("expected CREATE SEQUENCE ... AS bigint, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `OWNED BY "orders"."order_number"`) {
		t.Errorf("expected OWNED BY orders.order_number, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceAsTypeChangedDropsAndRecreates guards the AS-type half of
// RFC audit item #14: per the RFC's own diffing table, an AS type change is
// DROP + CREATE, DESTRUCTIVE (not an in-place ALTER, even though real
// PostgreSQL supports one).
func TestDiffSequenceAsTypeChangedDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", AsType: "integer"},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", AsType: &ir.TypeRef{Name: "bigint"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP SEQUENCE IF EXISTS") {
		t.Errorf("expected DROP SEQUENCE for an AS type change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, " AS bigint") {
		t.Errorf("expected re-CREATE SEQUENCE with the new AS type, got: %v", sqlList(ops))
	}
	dropOp := ops[0]
	if dropOp.Safety() != pipeline.Destructive {
		t.Errorf("expected DROP SEQUENCE to be Destructive, got %s", dropOp.Safety())
	}
}

// TestDiffSequenceOwnedByChangedIsSafeAlter guards the OWNED BY half of RFC
// audit item #14: per the RFC's own diffing table, an OWNED BY change is a
// SAFE ALTER SEQUENCE (not DROP+CREATE — a genuinely separate diff branch
// from AS type, despite both being new fields on the same object).
func TestDiffSequenceOwnedByChangedIsSafeAlter(t *testing.T) {
	d := New()
	oldOwned := "orders.legacy_number"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", OwnedBy: &oldOwned},
	})
	newOwned := "orders.order_number"
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", OwnedBy: &newOwned},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP SEQUENCE") {
		t.Errorf("OWNED BY change should not DROP+CREATE, got: %v", sqlList(ops))
	}
	op := findOpSQL(ops, `OWNED BY "orders"."order_number"`)
	if op == nil {
		t.Fatalf("expected ALTER SEQUENCE ... OWNED BY, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Safe {
		t.Errorf("expected OWNED BY change to be Safe, got %s", op.Safety())
	}
}

// TestDiffSequenceOwnedByNoneChangedIsSafeAlter proves the explicit "OWNED
// BY NONE" sentinel round-trips through diffing distinctly from nil
// (unspecified).
func TestDiffSequenceOwnedByNoneChangedIsSafeAlter(t *testing.T) {
	d := New()
	oldOwned := "orders.order_number"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", OwnedBy: &oldOwned},
	})
	none := "NONE"
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", OwnedBy: &none},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "OWNED BY NONE") {
		t.Errorf("expected ALTER SEQUENCE ... OWNED BY NONE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceRestartWithValueEmittedEveryPlan guards RFC audit item
// #68: RESTART [WITH n] is an imperative action with no persisted state to
// diff against, so it must be unconditionally re-emitted on every plan for
// as long as it remains declared, Safety Caution, transactional (real
// PostgreSQL's ALTER SEQUENCE RESTART has no transaction-block restriction).
func TestDiffSequenceRestartWithValueEmittedEveryPlan(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	restartWith := int64(1000)
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Restart: true, RestartWith: &restartWith},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, `ALTER SEQUENCE "public"."seq_id" RESTART WITH 1000;`)
	if op == nil {
		t.Fatalf("expected ALTER SEQUENCE ... RESTART WITH 1000, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected RESTART to be Caution safety, got %s", op.Safety())
	}
	if op.Mode() != pipeline.ModeTransactional {
		t.Error("expected RESTART to still run inside the migration's transaction, got Transactional() == false")
	}

	// Re-diffing against a snapshot that reflects the previous apply (no
	// persisted RESTART field to compare) must still re-emit it, not treat
	// it as already-applied drift.
	ops2, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if findOpSQL(ops2, `ALTER SEQUENCE "public"."seq_id" RESTART WITH 1000;`) == nil {
		t.Fatalf("expected RESTART to be re-emitted on a second plan while still declared, got: %v", sqlList(ops2))
	}
}

// TestDiffSequenceRestartEmitsRemovalReminder proves RESTART's emission is
// followed by an inline comment recommending the directive be removed from
// source once applied (RFC Section 10), matching real PostgreSQL's own
// documented one-shot-then-remove usage pattern for RESTART.
func TestDiffSequenceRestartEmitsRemovalReminder(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	restartWith := int64(1000)
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Restart: true, RestartWith: &restartWith},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range ops {
		if strings.Contains(op.SQL(), "remove the RESTART directive") {
			found = true
			if op.Mode() != pipeline.ModeInstructionOnly || op.Safety() != pipeline.Safe {
				t.Errorf("expected removal-reminder comment to be a Safe instruction-only note, got %s %s", op.Safety(), op.Mode())
			}
		}
	}
	if !found {
		t.Fatalf("expected a removal-reminder comment op, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceRestartBareOmitsWith proves a bare RESTART (no WITH n)
// omits the WITH clause, matching real PostgreSQL's "reset to START value"
// semantics for that form.
func TestDiffSequenceRestartBareOmitsWith(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Restart: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if findOpSQL(ops, `ALTER SEQUENCE "public"."seq_id" RESTART;`) == nil {
		t.Fatalf("expected bare ALTER SEQUENCE ... RESTART with no WITH clause, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceRestartUnspecifiedIsNoop proves a source that never
// mentions RESTART never emits it.
func TestDiffSequenceRestartUnspecifiedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "RESTART") {
		t.Errorf("expected no RESTART when never declared, got: %v", sqlList(ops))
	}
}

// TestCreateSequenceWithRestart proves a brand-new sequence declared with
// RESTART emits the follow-up ALTER right after CREATE (CREATE SEQUENCE has
// no meaningful use for RESTART — see Sequence.Restart's doc comment).
func TestCreateSequenceWithRestart(t *testing.T) {
	restartWith := int64(500)
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Restart: true, RestartWith: &restartWith},
	}
	ops, err := New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE SEQUENCE") {
		t.Fatalf("expected CREATE SEQUENCE, got: %v", sqlList(ops))
	}
	if findOpSQL(ops, `ALTER SEQUENCE "public"."seq_id" RESTART WITH 500;`) == nil {
		t.Fatalf("expected a follow-up ALTER SEQUENCE ... RESTART WITH 500 after CREATE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceAsTypeOwnedByUnspecifiedIsNoop proves the nil-means-
// unspecified convention: a source that never mentions AS/OWNED BY must not
// touch an existing sequence's type or ownership either way.
func TestDiffSequenceAsTypeOwnedByUnspecifiedIsNoop(t *testing.T) {
	d := New()
	owned := "orders.order_number"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", AsType: "bigint", OwnedBy: &owned},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops when AS/OWNED BY unspecified in source, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceCycleChangedWithoutOtherParams is the regression guard for a
// bug found while pushing diff-package unit test coverage: paramsChanged in
// diffSequence originally gated the Cycle comparison on `o.IncrementBy !=
// nil`, so an explicit CYCLE with no other sequence option set (the common
// case — CYCLE is usually the only thing anyone changes on an existing
// sequence) was silently ignored by verify/plan --live. Cycle must be
// compared whenever it was itself explicitly set, independent of any other
// option.
func TestDiffSequenceCycleChangedWithoutOtherParams(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", Cycle: false},
	})
	cyc := true
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Cycle: &cyc},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER SEQUENCE") || !containsSQL(ops, "CYCLE") {
		t.Errorf("expected ALTER SEQUENCE ... CYCLE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceCycleToggledOffEmitsNoCycle is the regression guard for
// RFC audit item #21: writeSeqParams only ever wrote "CYCLE" when Cycle was
// true, so toggling an existing CYCLE sequence to NO CYCLE was detected as a
// diff (paramsChanged correctly saw *o.Cycle != snap.Cycle) but emitted an
// ALTER SEQUENCE statement with no CYCLE clause at all. Unlike CREATE
// SEQUENCE, PostgreSQL's ALTER SEQUENCE does not reset an omitted option to
// its default — so the live sequence kept cycling forever, and the snapshot
// recorded the change as applied, permanently hiding the drift from future
// diffs. The emitted SQL must say "NO CYCLE" explicitly.
func TestDiffSequenceCycleToggledOffEmitsNoCycle(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", Cycle: true},
	})
	cyc := false
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Cycle: &cyc},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER SEQUENCE") || !containsSQL(ops, "NO CYCLE") {
		t.Errorf("expected ALTER SEQUENCE ... NO CYCLE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceCycleUnspecifiedIsNoop proves the nil-means-unspecified
// semantics: a sequence source that never mentions CYCLE/NO CYCLE must not
// touch an existing sequence's cycle setting either way.
func TestDiffSequenceCycleUnspecifiedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", Cycle: true},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops when CYCLE unspecified in source, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceNoMinValueToggledOnIsDetected is the regression guard for
// RFC audit item #20: switching an existing bounded sequence to explicit
// "NO MINVALUE" produced the same nil MinValue as "not mentioned at all",
// so diffSequence's paramsChanged (gated on o.MinValue != nil) silently
// ignored the change entirely.
func TestDiffSequenceNoMinValueToggledOnIsDetected(t *testing.T) {
	d := New()
	min := int64(5)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", MinValue: &min},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", NoMinValue: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER SEQUENCE") || !containsSQL(ops, "NO MINVALUE") {
		t.Errorf("expected ALTER SEQUENCE ... NO MINVALUE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceNoMaxValueToggledOnIsDetected is NoMinValue's MAXVALUE
// counterpart (RFC audit item #20).
func TestDiffSequenceNoMaxValueToggledOnIsDetected(t *testing.T) {
	d := New()
	max := int64(1000)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", MaxValue: &max},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", NoMaxValue: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER SEQUENCE") || !containsSQL(ops, "NO MAXVALUE") {
		t.Errorf("expected ALTER SEQUENCE ... NO MAXVALUE, got: %v", sqlList(ops))
	}
}

// TestDiffSequenceMinValueUnspecifiedIsNoop proves the nil-means-unspecified
// semantics extend to NoMinValue/NoMaxValue: a sequence source that never
// mentions MINVALUE/MAXVALUE/NO MINVALUE/NO MAXVALUE must not touch an
// existing sequence's bounds either way.
func TestDiffSequenceMinValueUnspecifiedIsNoop(t *testing.T) {
	d := New()
	min := int64(5)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", MinValue: &min},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops when MINVALUE unspecified in source, got: %v", sqlList(ops))
	}
}

// TestDiffCreateSequenceEmitsGrant and TestDiffSequenceGrantAdded/Removed are
// the regression guard for RFC audit item #24: ir.Sequence.Grants was
// correctly populated by the builder, but neither createSequence nor
// diffSequence ever referenced it — no GRANT SQL on create, no diff/ALTER on
// update — and SnapSequence had no Grants field at all, so it couldn't even
// round-trip through the snapshot.
func TestDiffCreateSequenceEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_readonly"}}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON SEQUENCE "public"."seq_id" TO "app_readonly"`) {
		t.Errorf("expected GRANT USAGE ON SEQUENCE, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_readonly"}}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON SEQUENCE "public"."seq_id" TO "app_readonly"`) {
		t.Errorf("expected GRANT USAGE ON SEQUENCE, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind: "sequence",
		Sequence: &snapshot.SnapSequence{
			Schema: "public", Name: "seq_id",
			Grants: []snapshot.SnapGrant{{Privileges: []string{"USAGE"}, Roles: []string{"app_readonly"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE USAGE ON SEQUENCE "public"."seq_id" FROM "app_readonly"`) {
		t.Errorf("expected REVOKE USAGE ON SEQUENCE, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged sequence, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceCommentAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id"},
	})
	comment := "order id sequence"
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Comment: &comment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON SEQUENCE") || !containsSQL(ops, "'order id sequence'") {
		t.Errorf("expected COMMENT ON SEQUENCE with new comment, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceCommentRemoved(t *testing.T) {
	d := New()
	old := "old comment"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", Comment: &old},
	})
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON SEQUENCE") || !containsSQL(ops, "IS NULL") {
		t.Errorf("expected COMMENT ON SEQUENCE ... IS NULL, got: %v", sqlList(ops))
	}
}

// TestDiffCreateSequenceWithOwner and TestDiffSequenceOwnerChanged are the
// regression guard for a bug found reviewing the dump Owner-rendering fix:
// createSequence never emitted ALTER SEQUENCE ... OWNER TO for a brand-new
// sequence, SnapSequence had no Owner field at all, and diffSequence never
// compared it — so a declared sequence Owner was completely inert, for both
// initial creation and subsequent drift, unlike Table/Schema (which both
// genuinely act on Owner). dump had already started rendering `OWNER role;`
// into sequence source (see the render-side fix), which without this fix
// would silently do nothing at either create or apply time.
func TestDiffCreateSequenceWithOwner(t *testing.T) {
	d := New()
	owner := "app_owner"
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE SEQUENCE IF NOT EXISTS") {
		t.Errorf("expected CREATE SEQUENCE, got: %v", sqlList(ops))
	}
	// RFC Section 11.5 (audit item #28): a brand-new object is created directly as
	// its declared owner via SET ROLE/RESET ROLE, not reassigned afterward
	// via ALTER ... OWNER TO — the latter never satisfies a matching
	// DEFAULT PRIVILEGES FOR ROLE block.
	if !containsSQL(ops, `SET ROLE "app_owner";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE SEQUENCE, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER SEQUENCE ... OWNER TO for a brand-new sequence, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "old_owner"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.seq_id", &snapshot.SnapObject{
		Kind:     "sequence",
		Sequence: &snapshot.SnapSequence{Schema: "public", Name: "seq_id", Owner: &oldOwner},
	})
	newOwner := "new_owner"
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq_id", Owner: &newOwner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER SEQUENCE "public"."seq_id" OWNER TO "new_owner"`) {
		t.Errorf("expected ALTER SEQUENCE ... OWNER TO new_owner, got: %v", sqlList(ops))
	}
}

// ── Role diff ─────────────────────────────────────────────────────────────────

func TestDiffCreateRole(t *testing.T) {
	d := New()
	comment := "application role"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Comment: &comment},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE ROLE") || !containsSQL(ops, `"app_role"`) {
		t.Errorf("expected CREATE ROLE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "COMMENT ON ROLE") || !containsSQL(ops, "'application role'") {
		t.Errorf("expected COMMENT ON ROLE, got: %v", sqlList(ops))
	}
}

func TestDiffRoleUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role"},
	})
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged role, got: %v", sqlList(ops))
	}
}

func TestDiffRoleCommentAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role"},
	})
	comment := "application role"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Comment: &comment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON ROLE") || !containsSQL(ops, "'application role'") {
		t.Errorf("expected COMMENT ON ROLE with new comment, got: %v", sqlList(ops))
	}
}

// TestCreateRoleConfigsEmitsAlterRole guards RFC audit item #74: a
// brand-new role with declared SET/RESET config entries needs its own
// follow-up ALTER ROLE for each — real CREATE ROLE has no inline SET/RESET
// clause at all.
func TestCreateRoleConfigsEmitsAlterRole(t *testing.T) {
	d := New()
	value := "'5s'"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{
			{Param: "statement_timeout", Value: &value},
		}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER ROLE "app_role" SET statement_timeout = '5s';`) {
		t.Errorf("expected ALTER ROLE SET statement_timeout, got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigNewEntry guards a new SET entry declared against a role
// that already exists (no prior configs in the snapshot).
func TestDiffRoleConfigNewEntry(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role"},
	})
	value := "'64MB'"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{{Param: "work_mem", Value: &value}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER ROLE "app_role" SET work_mem = '64MB';`) {
		t.Errorf("expected ALTER ROLE SET work_mem, got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigUnchangedIsNoop guards the core idempotency case: the
// same entry already recorded in the snapshot must not re-emit.
func TestDiffRoleConfigUnchangedIsNoop(t *testing.T) {
	d := New()
	value := "'64MB'"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role", Configs: []snapshot.SnapRoleConfig{{Param: "work_mem", Value: &value}}},
	})
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{{Param: "work_mem", Value: &value}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged config, got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigValueQuoteStyleNormalized guards a real, deliberate
// design point: a live-introspected value always comes back bare/resolved
// (e.g. "64MB", no quotes — see introspectRoleConfigs), while a hand-
// written declaration might quote it ('64MB') or not. Both spellings must
// compare equal, or every role with any declared config would show
// permanent spurious drift against live-introspected state.
func TestDiffRoleConfigValueQuoteStyleNormalized(t *testing.T) {
	d := New()
	declaredValue := "'64MB'"
	introspectedValue := "64MB"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role", Configs: []snapshot.SnapRoleConfig{{Param: "work_mem", Value: &introspectedValue}}},
	})
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{{Param: "work_mem", Value: &declaredValue}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops (quoted vs bare value must compare equal), got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigValueChanged guards a genuine value change.
func TestDiffRoleConfigValueChanged(t *testing.T) {
	d := New()
	oldValue := "'32MB'"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role", Configs: []snapshot.SnapRoleConfig{{Param: "work_mem", Value: &oldValue}}},
	})
	newValue := "'64MB'"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{{Param: "work_mem", Value: &newValue}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET work_mem = '64MB'`) {
		t.Errorf("expected re-SET to the new value, got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigRemovedFromSourceIsNoop guards the additive-model
// treatment: an entry simply disappearing from source must not emit a
// RESET — matching diffGrantSet's identical "removing a declaration emits
// nothing" convention (RESET is itself an explicit, available directive).
func TestDiffRoleConfigRemovedFromSourceIsNoop(t *testing.T) {
	d := New()
	value := "'64MB'"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role", Configs: []snapshot.SnapRoleConfig{{Param: "work_mem", Value: &value}}},
	})
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops (declaration removed, additive model), got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigInDatabaseIsDistinctKey guards roleConfigKey's own
// reasoning: the same param SET differently IN DATABASE a vs cluster-wide
// are two independent settings, not one — declaring both must emit both,
// and each must diff independently.
func TestDiffRoleConfigInDatabaseIsDistinctKey(t *testing.T) {
	d := New()
	clusterValue := "'30s'"
	dbValue := "'5s'"
	db := "mydb"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{
			{Param: "statement_timeout", Value: &clusterValue},
			{Param: "statement_timeout", Value: &dbValue, InDatabase: &db},
		}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER ROLE "app_role" SET statement_timeout = '30s';`) {
		t.Errorf("expected cluster-wide SET, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER ROLE "app_role" IN DATABASE "mydb" SET statement_timeout = '5s';`) {
		t.Errorf("expected IN DATABASE SET, got: %v", sqlList(ops))
	}
}

// TestDiffRoleConfigResetAll guards the RESET ALL form specifically.
func TestDiffRoleConfigResetAll(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", Configs: []ir.RoleConfig{{Reset: true, ResetAll: true}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER ROLE "app_role" RESET ALL;`) {
		t.Errorf("expected RESET ALL, got: %v", sqlList(ops))
	}
}

// TestDiffRoleRenamedFromEmitsAlterRename is the regression guard for Role
// rename detection: before this, ir.Role had no RenamedFrom field at all
// (and renamedFromKey had no *ir.Role case), so a renamed role was
// indistinguishable from "old role dropped, new one added" — a real
// DROP ROLE that PostgreSQL can outright refuse if the role still owns any
// object, per the RFC's own documented severity for this gap.
func TestDiffRoleRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app_role_old", &snapshot.SnapObject{
		Kind: "role",
		Role: &snapshot.SnapRole{Name: "app_role_old"},
	})
	old := "app_role_old"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", RenamedFrom: &old},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP ROLE") || containsSQL(ops, "CREATE ROLE") {
		t.Fatalf("renamed role should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER ROLE "app_role_old" RENAME TO "app_role";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER ROLE RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffRoleRenamedFromStaleErrors mirrors every other kind's identical
// stale-RENAMED-FROM validation (Differ.Diff's shared top-level Pass 1).
func TestDiffRoleRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_role"
	desired := []pipeline.IRObject{
		&ir.Role{Name: "app_role", RenamedFrom: &old},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot role")
	}
}

// ── DefaultPrivileges diffing ─────────────────────────────────────────────────

func TestDiffDefaultPrivilegesCreate(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.DefaultPrivileges{
			ObjectType: "TABLES",
			Grants:     []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "ALTER DEFAULT PRIVILEGES") || !containsSQL(ops, "GRANT SELECT") || !containsSQL(ops, "app_readonly") {
		t.Errorf("expected ALTER DEFAULT PRIVILEGES GRANT, got: %v", sqlList(ops))
	}
}

func TestDiffDefaultPrivilegesGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	dp := &snapshot.SnapDefaultPrivileges{
		ObjectType: "TABLES",
		Grants:     []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
	}
	_ = snap.SetObject("DEFAULT PRIVILEGES TABLES", &snapshot.SnapObject{
		Kind:              "default_privileges",
		DefaultPrivileges: dp,
	})
	desired := []pipeline.IRObject{
		&ir.DefaultPrivileges{
			ObjectType: "TABLES",
			Grants: []ir.Grant{
				{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}},
				{Privileges: []string{"INSERT"}, Roles: []string{"app_writer"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT INSERT") || !containsSQL(ops, "app_writer") {
		t.Errorf("expected GRANT for new privilege, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE") {
		t.Errorf("expected no REVOKE for unchanged grant, got: %v", sqlList(ops))
	}
}

func TestDiffDefaultPrivilegesGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	dp := &snapshot.SnapDefaultPrivileges{
		ObjectType: "TABLES",
		Grants: []snapshot.SnapGrant{
			{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}},
			{Privileges: []string{"INSERT"}, Roles: []string{"app_writer"}},
		},
	}
	_ = snap.SetObject("DEFAULT PRIVILEGES TABLES", &snapshot.SnapObject{
		Kind:              "default_privileges",
		DefaultPrivileges: dp,
	})
	desired := []pipeline.IRObject{
		&ir.DefaultPrivileges{
			ObjectType: "TABLES",
			Grants:     []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE INSERT") || !containsSQL(ops, "app_writer") {
		t.Errorf("expected REVOKE for removed grant, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE SELECT") {
		t.Errorf("unchanged SELECT grant should not be revoked, got: %v", sqlList(ops))
	}
}

func TestDiffDefaultPrivilegesUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	dp := &snapshot.SnapDefaultPrivileges{
		ObjectType: "TABLES",
		Grants:     []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
	}
	_ = snap.SetObject("DEFAULT PRIVILEGES TABLES", &snapshot.SnapObject{
		Kind:              "default_privileges",
		DefaultPrivileges: dp,
	})
	desired := []pipeline.IRObject{
		&ir.DefaultPrivileges{
			ObjectType: "TABLES",
			Grants:     []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged default privileges, got: %v", sqlList(ops))
	}
}

func TestDiffDefaultPrivilegesDropEmitsRevoke(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	dp := &snapshot.SnapDefaultPrivileges{
		ObjectType: "TABLES",
		Grants:     []snapshot.SnapGrant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
	}
	_ = snap.SetObject("DEFAULT PRIVILEGES", &snapshot.SnapObject{
		Kind:              "default_privileges",
		DefaultPrivileges: dp,
	})
	// desired has no DefaultPrivileges — it was removed
	ops, err := d.Diff([]pipeline.IRObject{}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE SELECT") || !containsSQL(ops, "app_readonly") {
		t.Errorf("expected REVOKE when default privileges removed, got: %v", sqlList(ops))
	}
}

func TestDiffDefaultPrivilegesForRole(t *testing.T) {
	d := New()
	forRole := "dba"
	desired := []pipeline.IRObject{
		&ir.DefaultPrivileges{
			ForRole:    &forRole,
			ObjectType: "TABLES",
			Grants:     []ir.Grant{{Privileges: []string{"ALL"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "FOR ROLE") || !containsSQL(ops, "dba") {
		t.Errorf("expected FOR ROLE in DEFAULT PRIVILEGES, got: %v", sqlList(ops))
	}
}

// ── ParameterPrivileges diffing (RFC Section 11.6, PG15+) ───────────────────────

func TestDiffParameterPrivilegesCreate(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{{
				Privileges: []string{"SET"},
				Parameters: []string{"work_mem", "statement_timeout"},
				Roles:      []string{"app_admin"},
			}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT SET ON PARAMETER") || !containsSQL(ops, "work_mem, statement_timeout") || !containsSQL(ops, "app_admin") {
		t.Errorf("expected GRANT SET ON PARAMETER, got: %v", sqlList(ops))
	}
}

func TestDiffParameterPrivilegesGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	pp := &snapshot.SnapParameterPrivileges{
		Grants: []snapshot.SnapParamGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
	}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind:                "parameter_privileges",
		ParameterPrivileges: pp,
	})
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{
				{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}},
				{Privileges: []string{"ALTER SYSTEM"}, Parameters: []string{"shared_preload_libraries"}, Roles: []string{"dba"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT ALTER SYSTEM") || !containsSQL(ops, "shared_preload_libraries") || !containsSQL(ops, "dba") {
		t.Errorf("expected GRANT for new privilege, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE") {
		t.Errorf("expected no REVOKE for unchanged grant, got: %v", sqlList(ops))
	}
}

// TestDiffParameterPrivilegesGrantedByDrift guards the "declared, so
// managed" GrantedBy re-check (RFC audit item #90, generalized here) — a
// matched grant whose desired side newly declares GRANTED BY that the
// snapshot doesn't already satisfy re-issues the GRANT with GRANTED BY.
func TestDiffParameterPrivilegesGrantedByDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	pp := &snapshot.SnapParameterPrivileges{
		Grants: []snapshot.SnapParamGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
	}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind:                "parameter_privileges",
		ParameterPrivileges: pp,
	})
	adminRole := "admin_role"
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{
				{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}, GrantedBy: &adminRole},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANTED BY \"admin_role\"") {
		t.Errorf("expected re-GRANT with GRANTED BY, got: %v", sqlList(ops))
	}
}

// TestDiffParameterPrivilegesGrantedByAlreadySatisfiedIsNoop guards the
// inverse: when the snapshot already records the matching GrantedBy, no
// re-GRANT is emitted.
func TestDiffParameterPrivilegesGrantedByAlreadySatisfiedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	adminRole := "admin_role"
	pp := &snapshot.SnapParameterPrivileges{
		Grants: []snapshot.SnapParamGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}, GrantedBy: &adminRole}},
	}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind:                "parameter_privileges",
		ParameterPrivileges: pp,
	})
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{
				{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}, GrantedBy: &adminRole},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected noop when GrantedBy already satisfied, got: %v", sqlList(ops))
	}
}

// TestDiffParameterPrivilegesGrantRemovedEmitsNothing guards RFC Section
// 11.6's literal additive-model text: removing a GRANTS { } entry from
// source (while the PARAMETER PRIVILEGES block itself remains) must not
// emit any REVOKE — a deliberate departure from diffDefaultPrivileges'
// current implicit-revoke behavior (see diffParameterPrivileges' doc
// comment). Only an explicit REVOKES entry actually revokes.
func TestDiffParameterPrivilegesGrantRemovedEmitsNothing(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	pp := &snapshot.SnapParameterPrivileges{
		Grants: []snapshot.SnapParamGrant{
			{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}},
			{Privileges: []string{"ALTER SYSTEM"}, Parameters: []string{"shared_preload_libraries"}, Roles: []string{"dba"}},
		},
	}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind:                "parameter_privileges",
		ParameterPrivileges: pp,
	})
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{
				{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("removing a grant declaration alone should emit nothing (additive model), got: %v", sqlList(ops))
	}
}

func TestDiffParameterPrivilegesUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	pp := &snapshot.SnapParameterPrivileges{
		Grants: []snapshot.SnapParamGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
	}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind:                "parameter_privileges",
		ParameterPrivileges: pp,
	})
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged parameter privileges, got: %v", sqlList(ops))
	}
}

// TestDiffParameterPrivilegesStaleSnapshotDoesNotRecreate guards
// diffObject's nil-ParameterPrivileges guard: a snapshot entry found under
// the "PARAMETER PRIVILEGES" key (so found == true, no CREATE path taken)
// but with a nil ParameterPrivileges sub-struct — e.g. a hand-edited or
// legacy snapshot where Kind is set but the payload isn't — must fall back
// to a safe no-op rather than either crashing or (worse) silently treating
// every desired grant as brand new and re-issuing it. Mirrors
// DefaultPrivileges' identical "if snap.DefaultPrivileges == nil" guard.
func TestDiffParameterPrivilegesStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind: "parameter_privileges",
		// ParameterPrivileges intentionally left nil.
	})
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for a stale/nil ParameterPrivileges snapshot entry, got: %v", sqlList(ops))
	}
}

// TestDiffParameterPrivilegesDeclarationRemovedEmitsNothing guards that
// deleting the entire PARAMETER PRIVILEGES block from source — not just one
// grant within it — is still governed by the additive model: it's the union
// of removing every one of its individual grant declarations, each of which
// emits nothing (see dropObject's "parameter_privileges" case and
// diffParameterPrivileges' doc comment).
func TestDiffParameterPrivilegesDeclarationRemovedEmitsNothing(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	pp := &snapshot.SnapParameterPrivileges{
		Grants: []snapshot.SnapParamGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
	}
	_ = snap.SetObject("PARAMETER PRIVILEGES", &snapshot.SnapObject{
		Kind:                "parameter_privileges",
		ParameterPrivileges: pp,
	})
	// desired has no ParameterPrivileges — the whole declaration was removed.
	ops, err := d.Diff([]pipeline.IRObject{}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("removing the entire declaration should emit nothing (additive model), got: %v", sqlList(ops))
	}
}

func TestDiffParameterPrivilegesExplicitRevocation(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Revocations: []ir.ParameterRevocation{
				{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_readonly"}, Cascade: true},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE SET ON PARAMETER work_mem FROM") || !containsSQL(ops, "app_readonly") || !containsSQL(ops, "CASCADE") {
		t.Errorf("expected explicit REVOKE with CASCADE, got: %v", sqlList(ops))
	}
}

// ── Operator class/family DROP access method (regression: hardcoded btree) ─────

func TestDiffDropOperatorClassUsesRecordedAccessMethod(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_ops", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "public", Name: "my_ops", Using: "gin",
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected drop op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "DROP OPERATOR CLASS") || !strings.Contains(sql, "USING gin") {
		t.Errorf("expected DROP … USING gin, got: %s", sql)
	}
	if strings.Contains(sql, "USING btree") {
		t.Errorf("must not hardcode btree, got: %s", sql)
	}
}

func TestDiffDropOperatorFamilyUsesRecordedAccessMethod(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_family", &snapshot.SnapObject{
		Kind: "operator_family",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_family", Schema: "public", Name: "my_family", Using: "gist",
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "USING gist") {
		t.Fatalf("expected DROP … USING gist, got: %v", ops)
	}
}

// ── Operator family loose members (RFC Section 14.4) ─────────────────────────────────

func opFamilyMemberObj(members ...pipeline.OpFamilyMember) *ir.OperatorFamily {
	return &ir.OperatorFamily{
		Schema: "public", Name: "member_fam", AccessMethod: "btree",
		Body:          `CREATE OPERATOR FAMILY "public"."member_fam" USING btree`,
		Reconstructed: true, Members: members,
	}
}

func opFamilyMemberSnap(structured bool, members ...snapshot.SnapOpFamilyMember) *snapshot.SnapObject {
	return &snapshot.SnapObject{Kind: "operator_family", Opaque: &snapshot.SnapOpaque{
		Kind: "operator_family", Schema: "public", Name: "member_fam", Using: "btree",
		OpFamilyMembersStructured: structured, OpFamilyMembers: members,
	}}
}

func TestDiffOpFamilyMemberAddOnly(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(true))
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"}
	ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj(m)}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Safety() != pipeline.Safe || !strings.Contains(ops[0].SQL(), "ADD OPERATOR 1") {
		t.Fatalf("expected 1 SAFE ADD op, got %v", sqlList(ops))
	}
}

func TestDiffOpFamilyMemberRemoveOnly(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	sm := snapshot.SnapOpFamilyMember{Number: 1, Name: "<", LeftType: "integer", RightType: "bigint"}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(true, sm))
	ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj()}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Safety() != pipeline.Destructive || !strings.Contains(ops[0].SQL(), "DROP OPERATOR 1") {
		t.Fatalf("expected 1 DESTRUCTIVE DROP op, got %v", sqlList(ops))
	}
}

// TestDiffOpFamilyMemberInPlaceChange guards the "same slot, different
// operator" case: Key() deliberately excludes operator identity (see its
// doc comment), so a slot whose operator symbol changes must diff as
// DROP-then-ADD, not silently match as unchanged.
func TestDiffOpFamilyMemberInPlaceChange(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	sm := snapshot.SnapOpFamilyMember{Number: 1, Name: "<", LeftType: "integer", RightType: "bigint"}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(true, sm))
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<="}, LeftType: "integer", RightType: "bigint"}
	ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj(m)}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Safety() != pipeline.Destructive || ops[1].Safety() != pipeline.Safe {
		t.Fatalf("expected DROP then ADD, got %v", sqlList(ops))
	}
}

// TestDiffOpFamilyMemberUnqualifiedOperatorMatchesPgCatalog is a live-bug
// regression guard: source commonly writes a bare "<" (no schema), while
// introspection always returns the operator's real, fully qualified schema
// (pg_catalog for a built-in) — a raw Identifier comparison flagged every
// unqualified built-in operator as changed on every apply, even with zero
// actual difference (caught by TestRoundtripOpFamilyLooseMembers, an
// integration test, before this unit test existed).
func TestDiffOpFamilyMemberUnqualifiedOperatorMatchesPgCatalog(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	sm := snapshot.SnapOpFamilyMember{Number: 1, NameSchema: "pg_catalog", Name: "<", LeftType: "integer", RightType: "bigint"}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(true, sm))
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"}
	ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj(m)}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("expected zero ops (unqualified '<' must match pg_catalog.<), got %v", sqlList(ops))
	}
}

func TestDiffOpFamilyMemberNoChange(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// NameSchema "pg_catalog", matching what real introspection always
	// returns for a built-in operator like "<" — an unqualified snap-side
	// name never actually occurs outside this kind of synthetic fixture.
	sm := snapshot.SnapOpFamilyMember{Number: 1, NameSchema: "pg_catalog", Name: "<", LeftType: "integer", RightType: "bigint"}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(true, sm))
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"}
	ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj(m)}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("expected zero ops, got %v", sqlList(ops))
	}
}

// TestDiffOperatorFamilyRenamedFromEmitsAlterRename is the regression guard
// for OperatorFamily rename detection: before this, ir.OperatorFamily had
// no RenamedFrom field at all, so a renamed family was indistinguishable
// from "old one dropped, new one added." Reconstructed: true keeps the
// rename isolated from diffOpaqueIR's body-hash comparison, matching
// opFamilyMemberObj's own established fixture shape.
func TestDiffOperatorFamilyRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	sm := snapshot.SnapOpFamilyMember{Number: 1, NameSchema: "pg_catalog", Name: "<", LeftType: "integer", RightType: "bigint"}
	// Body text differs by name on each side, matching real compiler
	// output (Reconstructed: false, a real BodyHash set) — proves
	// opaqueBodyForHash's name-normalization fix, not just that the
	// rename mechanism exists; a Reconstructed:true or empty-BodyHash
	// fixture would bypass the hash comparison entirely and not actually
	// exercise it.
	oldBody := `CREATE OPERATOR FAMILY "public"."member_fam_old" USING btree`
	newBody := `CREATE OPERATOR FAMILY "public"."member_fam" USING btree`
	_ = snap.SetObject("public.member_fam_old USING btree FAMILY", &snapshot.SnapObject{
		Kind: "operator_family",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_family", Schema: "public", Name: "member_fam_old", Using: "btree",
			BodyHash:                  hashText(oldBody),
			OpFamilyMembersStructured: true, OpFamilyMembers: []snapshot.SnapOpFamilyMember{sm},
		},
	})
	old := "member_fam_old"
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"}
	desired := &ir.OperatorFamily{
		Schema: "public", Name: "member_fam", AccessMethod: "btree",
		Body: newBody, Members: []pipeline.OpFamilyMember{m},
		RenamedFrom: &old,
	}
	ops, err := d.Diff([]pipeline.IRObject{desired}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP OPERATOR FAMILY") {
		t.Fatalf("renamed operator family should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER OPERATOR FAMILY "public"."member_fam_old" USING btree RENAME TO "member_fam";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER OPERATOR FAMILY RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffOperatorFamilyRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for OperatorFamily.
func TestDiffOperatorFamilyRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	sm := snapshot.SnapOpFamilyMember{Number: 1, NameSchema: "pg_catalog", Name: "<", LeftType: "integer", RightType: "bigint"}
	oldBody := `CREATE OPERATOR FAMILY "old_schema"."member_fam" USING btree`
	newBody := `CREATE OPERATOR FAMILY "new_schema"."member_fam" USING btree`
	_ = snap.SetObject("old_schema.member_fam USING btree FAMILY", &snapshot.SnapObject{
		Kind: "operator_family",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_family", Schema: "old_schema", Name: "member_fam", Using: "btree",
			BodyHash:                  hashText(oldBody),
			OpFamilyMembersStructured: true, OpFamilyMembers: []snapshot.SnapOpFamilyMember{sm},
		},
	})
	old := "member_fam"
	oldSchema := "old_schema"
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"}
	desired := &ir.OperatorFamily{
		Schema: "new_schema", Name: "member_fam", AccessMethod: "btree",
		Body: newBody, Members: []pipeline.OpFamilyMember{m},
		RenamedFrom: &old, RenamedFromSchema: &oldSchema,
	}
	ops, err := d.Diff([]pipeline.IRObject{desired}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP OPERATOR FAMILY") {
		t.Fatalf("cross-schema rename should match the existing operator family, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER OPERATOR FAMILY "old_schema"."member_fam" USING btree SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffOperatorFamilyRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation.
func TestDiffOperatorFamilyRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_fam"
	desired := &ir.OperatorFamily{
		Schema: "public", Name: "member_fam", AccessMethod: "btree",
		Body: `CREATE OPERATOR FAMILY "public"."member_fam" USING btree`, RenamedFrom: &old,
	}
	_, err := d.Diff([]pipeline.IRObject{desired}, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot operator family")
	}
}

// TestDiffOpFamilyMemberStaleSnapshotNoOp guards the mandatory
// OpFamilyMembersStructured sentinel: a snapshot written before this
// feature existed must never be treated as "genuinely zero members" — that
// would emit an ADD for a member PostgreSQL may already have, which
// actually errors live (no "ADD ... IF NOT EXISTS").
func TestDiffOpFamilyMemberStaleSnapshotNoOp(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(false))
	m := pipeline.OpFamilyMember{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"}
	ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj(m)}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("expected zero ops for stale (unstructured) snapshot, got %v", sqlList(ops))
	}
}

// TestDiffOpFamilyMemberDeterministicOrder guards diffOpFamilyMembers's
// sorted-key iteration — unlike diffTSConfigMappings's plain map iteration,
// this must produce identical plan text across repeated runs.
func TestDiffOpFamilyMemberDeterministicOrder(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.member_fam USING btree FAMILY", opFamilyMemberSnap(true))
	members := []pipeline.OpFamilyMember{
		{Number: 5, Name: pipeline.Identifier{Name: ">"}, LeftType: "integer", RightType: "bigint"},
		{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "bigint"},
		{IsFunction: true, Number: 1, Name: pipeline.Identifier{Name: "cmp"}, LeftType: "integer", RightType: "bigint", FuncArgs: []string{"integer", "bigint"}},
	}
	var first string
	for i := range 5 {
		ops, err := d.Diff([]pipeline.IRObject{opFamilyMemberObj(members...)}, snap)
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		for _, op := range ops {
			sb.WriteString(op.SQL())
		}
		if i == 0 {
			first = sb.String()
		} else if sb.String() != first {
			t.Fatalf("run %d produced different plan text:\n%s\nvs first run:\n%s", i, sb.String(), first)
		}
	}
}

// Legacy snapshots predate the access-method field; DROP falls back to btree.
func TestDiffDropOperatorClassLegacyFallsBackToBtree(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_ops", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "public", Name: "my_ops", // no Using
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 || !strings.Contains(ops[0].SQL(), "USING btree") {
		t.Fatalf("expected fallback USING btree, got: %v", ops)
	}
}

// ── OperatorClass structured-member comparison ─────────────────────────────
// Regression guard for the builder/introspect member-capture asymmetry (the
// builder previously captured only FUNCTION items via a bare-name Functions
// list, dropping OPERATOR/STORAGETYPE entirely, while introspection captured
// all three but only as flattened Body text) and the false-DESTRUCTIVE diff
// it enabled: since PostgreSQL has no incremental ALTER OPERATOR CLASS (RFC
// Section 14.4), any real AS-list change must fall back to DROP+CREATE — but a
// purely cosmetic Body-text difference (whitespace, item order, spelling)
// between hand-written source and the snapshot must NOT trigger one.

// toSnapOpFamilyMembersTest mirrors snapshot.toSnapOpFamilyMembers (unexported,
// so this package can't call it directly) purely for building test fixtures.
func toSnapOpFamilyMembersTest(members []pipeline.OpFamilyMember) []snapshot.SnapOpFamilyMember {
	out := make([]snapshot.SnapOpFamilyMember, len(members))
	for i, m := range members {
		out[i] = snapshot.SnapOpFamilyMember{
			IsFunction: m.IsFunction, Number: m.Number,
			NameSchema: m.Name.Schema, Name: m.Name.Name,
			LeftType: m.LeftType, RightType: m.RightType,
			FuncArgs: m.FuncArgs, OrderBy: m.OrderBy,
			SortFamilySchema: m.SortFamily.Schema, SortFamilyName: m.SortFamily.Name,
		}
	}
	return out
}

func opClassMemberFixture() []pipeline.OpFamilyMember {
	return []pipeline.OpFamilyMember{
		{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "integer"},
		{Number: 3, Name: pipeline.Identifier{Name: "="}, LeftType: "integer", RightType: "integer"},
		{IsFunction: true, Number: 1, Name: pipeline.Identifier{Name: "btint4cmp"}, LeftType: "integer", RightType: "integer",
			FuncArgs: []string{"integer", "integer"}},
	}
}

// opClassMemberFixtureIntrospected mirrors opClassMemberFixture but for a
// test's snap/introspected side specifically: real introspection always
// populates NameSchema (confirmed live: "pg_catalog" for every built-in
// operator/function here — see opMemberNameMatches's doc comment), unlike
// opClassMemberFixture's bare, schema-less identifiers, which model how a
// user writes an unqualified reference in hand-written source. Using
// opClassMemberFixture's own (unqualified) output for BOTH sides was
// coincidentally fine before opMemberNameMatches existed, since both sides
// got the exact same schema-less default; it stopped being realistic once
// unqualified-vs-explicit-schema comparison became meaningful.
func opClassMemberFixtureIntrospected() []snapshot.SnapOpFamilyMember {
	members := toSnapOpFamilyMembersTest(opClassMemberFixture())
	for i := range members {
		members[i].NameSchema = "pg_catalog"
	}
	return members
}

func TestDiffOperatorClassCosmeticBodyDriftIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_ops USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "public", Name: "my_ops", Using: "btree",
			BodyHash: fmt.Sprintf("%x", sha256Sum(
				"CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 1 <(integer, integer), OPERATOR 3 =(integer, integer), FUNCTION 1 (integer, integer) btint4cmp(integer, integer)")),
			OperatorClassMembersStructured: true,
			OperatorClassMembers:           opClassMemberFixtureIntrospected(),
			OperatorClassFamilySchema:      "public", OperatorClassFamilyName: "my_ops",
		},
	})
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "public", Name: "my_ops", AccessMethod: "btree",
			FamilySchema: "public", FamilyName: "my_ops",
			// Same members, deliberately different Body text (extra
			// whitespace, reordered STORAGE-free AS-list, no explicit
			// op_types) — the exact drift a hand edit or a differently
			// formatting introspection run would produce.
			Body:    "CREATE OPERATOR CLASS public.my_ops   FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 3 = , FUNCTION 1 btint4cmp(int4, int4), OPERATOR 1 <",
			Members: opClassMemberFixture(),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for structurally-identical members, got: %v", sqlList(ops))
	}
}

// TestDiffOperatorClassRenamedFromEmitsAlterRename is the regression guard
// for OperatorClass rename detection: before this, ir.OperatorClass had no
// RenamedFrom field at all, so a renamed class was indistinguishable from
// "old one dropped, new one added." Uses the same structurally-equal-
// members fixture as TestDiffOperatorClassCosmeticBodyDriftIsNoop so the
// rename is isolated from any member-change branch.
func TestDiffOperatorClassRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// FamilySchema/FamilyName deliberately reference a family that is NOT
	// the class's own auto-created same-name one, and stays identical on
	// both sides — a class rename doesn't rename its (independent) family
	// in real PostgreSQL, so this isolates the rename test from that
	// unrelated comparison rather than accidentally exercising it.
	_ = snap.SetObject("public.my_ops_old USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "public", Name: "my_ops_old", Using: "btree",
			BodyHash:                       fmt.Sprintf("%x", sha256Sum("CREATE OPERATOR CLASS public.my_ops_old FOR TYPE int4 USING btree FAMILY public.shared_family AS OPERATOR 1 <(integer, integer), OPERATOR 3 =(integer, integer), FUNCTION 1 (integer, integer) btint4cmp(integer, integer)")),
			OperatorClassMembersStructured: true,
			OperatorClassMembers:           opClassMemberFixtureIntrospected(),
			OperatorClassFamilySchema:      "public", OperatorClassFamilyName: "shared_family",
		},
	})
	old := "my_ops_old"
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "public", Name: "my_ops", AccessMethod: "btree",
			FamilySchema: "public", FamilyName: "shared_family",
			RenamedFrom: &old,
			Body:        "CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.shared_family AS OPERATOR 1 <, OPERATOR 3 =, FUNCTION 1 btint4cmp(int4, int4)",
			Members:     opClassMemberFixture(),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP OPERATOR CLASS") {
		t.Fatalf("renamed operator class should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER OPERATOR CLASS "public"."my_ops_old" USING btree RENAME TO "my_ops";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER OPERATOR CLASS RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffOperatorClassRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for OperatorClass.
func TestDiffOperatorClassRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.my_ops USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "old_schema", Name: "my_ops", Using: "btree",
			BodyHash:                       fmt.Sprintf("%x", sha256Sum("CREATE OPERATOR CLASS old_schema.my_ops FOR TYPE int4 USING btree FAMILY old_schema.shared_family AS OPERATOR 1 <(integer, integer), OPERATOR 3 =(integer, integer), FUNCTION 1 (integer, integer) btint4cmp(integer, integer)")),
			OperatorClassMembersStructured: true,
			OperatorClassMembers:           opClassMemberFixtureIntrospected(),
			OperatorClassFamilySchema:      "old_schema", OperatorClassFamilyName: "shared_family",
		},
	})
	old := "my_ops"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "new_schema", Name: "my_ops", AccessMethod: "btree",
			FamilySchema: "old_schema", FamilyName: "shared_family",
			RenamedFrom: &old, RenamedFromSchema: &oldSchema,
			Body:    "CREATE OPERATOR CLASS new_schema.my_ops FOR TYPE int4 USING btree FAMILY old_schema.shared_family AS OPERATOR 1 <, OPERATOR 3 =, FUNCTION 1 btint4cmp(int4, int4)",
			Members: opClassMemberFixture(),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP OPERATOR CLASS") {
		t.Fatalf("cross-schema rename should match the existing operator class, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER OPERATOR CLASS "old_schema"."my_ops" USING btree SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffOperatorClassImplicitAutoFamilySetSchemaIsSafeNotDestructive
// guards a real bug: a class relying on PostgreSQL's implicit same-name
// auto-family (no FAMILY clause declared at all — FamilySchema/FamilyName
// both "") previously had its desired-side family schema fall back to the
// class's own (post-move) schema, spuriously miscomparing against the
// snapshot's real, unmoved family schema and forcing a DESTRUCTIVE
// drop+recreate on every cross-schema SET SCHEMA — confirmed live via a
// real postgres:17 server that ALTER OPERATOR CLASS ... SET SCHEMA moves
// the class but never its auto-created family. A pure schema move must stay
// the plain SAFE ALTER ... SET SCHEMA it actually is.
func TestDiffOperatorClassImplicitAutoFamilySetSchemaIsSafeNotDestructive(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.my_ops USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "old_schema", Name: "my_ops", Using: "btree",
			OperatorClassMembersStructured: true,
			OperatorClassMembers:           opClassMemberFixtureIntrospected(),
			// The auto-created family lives where the class was ORIGINALLY
			// created — still "old_schema" even as the class itself moves.
			OperatorClassFamilySchema: "old_schema", OperatorClassFamilyName: "my_ops",
		},
	})
	oldName := "my_ops"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			// No FAMILY clause declared at all: implicit auto-family. Same
			// name, only the schema moves — still needs RENAMED FROM to
			// match against the snapshot at all (Section 7.6: a cross-
			// schema move has no unqualified-match path of its own).
			Schema: "new_schema", Name: "my_ops", AccessMethod: "btree",
			RenamedFrom: &oldName, RenamedFromSchema: &oldSchema,
			Body:    "CREATE OPERATOR CLASS new_schema.my_ops FOR TYPE int4 USING btree AS OPERATOR 1 <, OPERATOR 3 =, FUNCTION 1 btint4cmp(int4, int4)",
			Members: opClassMemberFixture(),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP OPERATOR CLASS") {
		t.Fatalf("a class-only SET SCHEMA move must not drop+recreate just because the (unmoved) implicit family now appears in a different schema than the class, got: %v", sqlList(ops))
	}
	want := `ALTER OPERATOR CLASS "old_schema"."my_ops" USING btree SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			t.Errorf("expected no DESTRUCTIVE op for a pure schema move, got: [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffOperatorClassRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation.
func TestDiffOperatorClassRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_ops"
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "public", Name: "my_ops", AccessMethod: "btree", RenamedFrom: &old,
			Body: "CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree AS OPERATOR 1 <",
		},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot operator class")
	}
}

func TestDiffOperatorClassRealMemberChangeStillDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_ops USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "public", Name: "my_ops", Using: "btree",
			BodyHash:                       fmt.Sprintf("%x", sha256Sum("CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 1 <(integer, integer)")),
			OperatorClassMembersStructured: true,
			OperatorClassMembers:           opClassMemberFixtureIntrospected(),
			OperatorClassFamilySchema:      "public", OperatorClassFamilyName: "my_ops",
		},
	})
	newMembers := opClassMemberFixture()
	newMembers[1].Name = pipeline.Identifier{Name: "<="} // strategy 3 now maps to a different operator
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "public", Name: "my_ops", AccessMethod: "btree",
			FamilySchema: "public", FamilyName: "my_ops",
			Body:    "CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 1 <(integer, integer), OPERATOR 3 <=(integer, integer), FUNCTION 1 (integer, integer) btint4cmp(integer, integer)",
			Members: newMembers,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("want 2 ops (structured DROP + CREATE) for a genuine member change, got %d: %v", len(ops), sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Destructive {
		t.Errorf("DROP op safety = %v, want Destructive", ops[0].Safety())
	}
	if !strings.Contains(ops[0].SQL(), `DROP OPERATOR CLASS IF EXISTS "public"."my_ops" USING btree;`) {
		t.Errorf("op[0] = %q, want the structured DROP", ops[0].SQL())
	}
}

// TestDiffOperatorClassLiveMemberChangeDetectedWithEmptyBodyHash is the
// regression guard for RFC audit item C.5: plan --live/verify build their
// comparison snapshot from live introspection, and introspectOperatorClasses
// sets Reconstructed unconditionally, which (via sourceBodyHash) makes the
// snapshot's BodyHash always "". Before this fix, diffOperatorClass fell
// through to diffOpaqueIR whenever the structural comparison found a real
// difference, and diffOpaqueIR's `snap.BodyHash != ""` guard silently
// produced zero ops the moment BodyHash was empty — a real, structurally-
// confirmed AS-list change was completely invisible to plan --live/verify.
// This snapshot fixture (empty BodyHash, OperatorClassMembersStructured
// true, members that genuinely differ from desired) reproduces exactly that
// shape; it must now still produce the DROP+CREATE.
func TestDiffOperatorClassLiveMemberChangeDetectedWithEmptyBodyHash(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_ops USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			Kind: "operator_class", Schema: "public", Name: "my_ops", Using: "btree",
			// BodyHash deliberately empty — simulates sourceBodyHash's
			// output for a live-introspected (Reconstructed == true)
			// OperatorClass, exactly as plan --live/verify build it.
			BodyHash:                       "",
			OperatorClassMembersStructured: true,
			OperatorClassMembers:           opClassMemberFixtureIntrospected(),
			OperatorClassFamilySchema:      "public", OperatorClassFamilyName: "my_ops",
		},
	})
	newMembers := opClassMemberFixture()
	newMembers[1].Name = pipeline.Identifier{Name: "<="} // strategy 3 now maps to a different operator
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "public", Name: "my_ops", AccessMethod: "btree",
			FamilySchema: "public", FamilyName: "my_ops",
			Body:    "CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 1 <(integer, integer), OPERATOR 3 <=(integer, integer), FUNCTION 1 (integer, integer) btint4cmp(integer, integer)",
			Members: newMembers,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("want 2 ops (structured DROP + CREATE) for a genuine member change even with empty BodyHash, got %d: %v", len(ops), sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Destructive {
		t.Errorf("DROP op safety = %v, want Destructive", ops[0].Safety())
	}
	if !strings.Contains(ops[0].SQL(), `DROP OPERATOR CLASS IF EXISTS "public"."my_ops" USING btree;`) {
		t.Errorf("op[0] = %q, want the structured DROP", ops[0].SQL())
	}
}

func TestDiffOperatorClassStaleSnapshotFallsBackToBodyHash(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_ops USING btree", &snapshot.SnapObject{
		Kind: "operator_class",
		Opaque: &snapshot.SnapOpaque{
			// Predates this feature: no OperatorClassMembersStructured, so
			// the differ must fall back to the pre-existing raw-BodyHash
			// comparison rather than trust an absent/empty Members list.
			Kind: "operator_class", Schema: "public", Name: "my_ops", Using: "btree",
			BodyHash: fmt.Sprintf("%x", sha256Sum("CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 1 <(integer, integer)")),
		},
	})
	desired := []pipeline.IRObject{
		&ir.OperatorClass{
			Schema: "public", Name: "my_ops", AccessMethod: "btree",
			FamilySchema: "public", FamilyName: "my_ops",
			Body: "CREATE OPERATOR CLASS public.my_ops FOR TYPE int4 USING btree FAMILY public.my_ops AS OPERATOR 1 <(integer, integer), OPERATOR 3 =(integer, integer)",
			Members: []pipeline.OpFamilyMember{
				{Number: 1, Name: pipeline.Identifier{Name: "<"}, LeftType: "integer", RightType: "integer"},
				{Number: 3, Name: pipeline.Identifier{Name: "="}, LeftType: "integer", RightType: "integer"},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("want 2 ops (structured DROP + CREATE) via BodyHash fallback, got %d: %v", len(ops), sqlList(ops))
	}
}

// An introspected opaque object (used as the baseline by `plan --live`) may have
// no body hash; comparing a real body against an empty snapshot hash must NOT
// report spurious drift.
// TestDiffOpaqueNoSpuriousDriftWhenSnapHashEmpty predates Collation's
// structured diffing (RFC Section 14.2): originally asserted exactly 0 ops for a
// snap.BodyHash == "" scenario. diffCollation's own stale-snapshot guard
// (CollationStructured == false here, since this snap predates the field)
// now deliberately emits one harmless refresh-metadata comment op instead
// of staying silently stale forever — same self-healing pattern as
// DOMAIN/Tablespace/Cast/etc. (see diffCollation's doc comment) — so the
// real invariant this test protects (no spurious DESTRUCTIVE drift) is
// checked directly rather than asserting a literal op count.
func TestDiffOpaqueNoSpuriousDriftWhenSnapHashEmpty(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_coll", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "my_coll", // BodyHash == ""
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "my_coll", Body: "CREATE COLLATION public.my_coll (LOCALE = 'C')"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Safety() == pipeline.Destructive {
			t.Fatalf("want no spurious destructive drift, got: %v", sqlList(ops))
		}
	}
}

// A FOR PUBLIC user mapping has an empty user; the DROP must emit FOR PUBLIC,
// not FOR "" (a zero-length identifier that aborts the migration).
func TestDiffDropUserMappingForPublic(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("@dummy_srv", &snapshot.SnapObject{
		Kind: "user_mapping",
		Opaque: &snapshot.SnapOpaque{
			Kind: "user_mapping", Name: "@dummy_srv",
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected drop op")
	}
	sql := ops[0].SQL()
	if !strings.Contains(sql, "FOR PUBLIC") {
		t.Errorf("expected FOR PUBLIC, got: %s", sql)
	}
	if strings.Contains(sql, `FOR ""`) {
		t.Errorf("must not emit zero-length identifier, got: %s", sql)
	}
}

// Collation/statistics bodies are catalog reconstructions on the live side and
// won't byte-match equivalent hand-written source; against a reconstructed
// baseline (plan --live) the differ must not report spurious "body changed"
// drift (regression: capturing Body activated this).
// TestDiffCollationNoSpuriousBodyDrift is the actual regression guard for
// the risk diffCollation's design was built to avoid (see its doc
// comment): LOCALE and LC_COLLATE/LC_CTYPE are two different DPG source
// spellings that PostgreSQL resolves to the identical collcollate/collctype
// pair (confirmed live) — Collate/Ctype are populated with those REAL
// resolved values on both sides (not left as Go zero values, which would
// make this test pass for the wrong reason — both sides merely
// "unpopulated," not "genuinely equivalent").
func TestDiffCollationNoSpuriousBodyDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Baseline as introspection produces it (qualified, LOCALE form; Reconstructed).
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", Reconstructed: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Desired from hand-written source (unqualified, LC_* form) — resolves
	// to the same Collate/Ctype PostgreSQL would actually store.
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION c (LC_COLLATE = 'C', LC_CTYPE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("spurious drift for equivalent collation spelling: %d ops: %v", len(ops), ops)
	}
}

// TestCommentOnOpaqueSQL guards a real bug found live-testing a demo
// project: PostgreSQL genuinely supports COMMENT ON for all 14 of these
// opaque kinds (confirmed via \h COMMENT against a real server), but the
// blockparser's generic { COMMENT '...'; } was silently discarded for
// every one of them — 9 had no Comment field at all, and the other 5
// (tablespace/fdw/server/ts_config/ts_dict) captured it but never emitted
// it anywhere. Table-driven across all 14 kinds, exercising every distinct
// identity shape dropObject already established: plain quoteIdent
// (tablespace/fdw/server/event_trigger), qualIdent (collation/statistics/
// the 4 TS kinds), and the 3 kinds with non-trivial COMMENT ON syntax
// (operator's operand-type suffix, operator class/family's USING method,
// cast's parenthesized source/target pair — none of which are simple
// identifiers).
func TestCommentOnOpaqueSQL(t *testing.T) {
	comment := "a comment"
	cases := []struct {
		name                               string
		kind, schema, objName, args, using string
		want                               string
	}{
		{"tablespace", "tablespace", "", "fast_ssd", "", "", `COMMENT ON TABLESPACE "fast_ssd" IS 'a comment';`},
		{"fdw", "fdw", "", "my_fdw", "", "", `COMMENT ON FOREIGN DATA WRAPPER "my_fdw" IS 'a comment';`},
		{"server", "server", "", "my_srv", "", "", `COMMENT ON SERVER "my_srv" IS 'a comment';`},
		{"publication", "publication", "", "order_changes", "", "", `COMMENT ON PUBLICATION "order_changes" IS 'a comment';`},
		{"event_trigger", "event_trigger", "", "log_ddl", "", "", `COMMENT ON EVENT TRIGGER "log_ddl" IS 'a comment';`},
		{"collation", "collation", "public", "case_insensitive", "", "", `COMMENT ON COLLATION "public"."case_insensitive" IS 'a comment';`},
		{"operator", "operator", "public", "<<<", "order_status, order_status", "", `COMMENT ON OPERATOR "public".<<<(order_status, order_status) IS 'a comment';`},
		{"operator_class", "operator_class", "public", "text_ci_ops", "", "btree", `COMMENT ON OPERATOR CLASS "public"."text_ci_ops" USING btree IS 'a comment';`},
		{"operator_family", "operator_family", "public", "text_ci_ops", "", "btree", `COMMENT ON OPERATOR FAMILY "public"."text_ci_ops" USING btree IS 'a comment';`},
		{"cast", "cast", "", "order_status->integer", "", "", `COMMENT ON CAST (order_status AS integer) IS 'a comment';`},
		{"statistics", "statistics", "public", "orders_stats", "", "", `COMMENT ON STATISTICS "public"."orders_stats" IS 'a comment';`},
		{"ts_config", "ts_config", "public", "my_cfg", "", "", `COMMENT ON TEXT SEARCH CONFIGURATION "public"."my_cfg" IS 'a comment';`},
		{"ts_dict", "ts_dict", "public", "my_dict", "", "", `COMMENT ON TEXT SEARCH DICTIONARY "public"."my_dict" IS 'a comment';`},
		{"ts_parser", "ts_parser", "public", "my_prs", "", "", `COMMENT ON TEXT SEARCH PARSER "public"."my_prs" IS 'a comment';`},
		{"ts_template", "ts_template", "public", "my_tmpl", "", "", `COMMENT ON TEXT SEARCH TEMPLATE "public"."my_tmpl" IS 'a comment';`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commentOnOpaqueSQL(tc.kind, tc.schema, tc.objName, tc.args, tc.using, &comment)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCommentOnOpaqueSQLNull guards the comment-removal case (comment set
// to nil emits IS NULL, not an empty/quoted string).
func TestCommentOnOpaqueSQLNull(t *testing.T) {
	got := commentOnOpaqueSQL("statistics", "public", "orders_stats", "", "", nil)
	want := `COMMENT ON STATISTICS "public"."orders_stats" IS NULL;`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestCommentOnOpaqueSQLUnknownKind guards user_mapping and subscription
// (both genuinely have no Comment field/COMMENT ON support at the DPG
// level) and any other unrecognized kind: must return "", not a malformed
// statement — appendCommentOp relies on this to silently skip kinds that
// don't support COMMENT ON at all. Publication used to be the canonical
// example here (it had no Comment field), until it was found live-testing
// a demo project that real PostgreSQL genuinely supports
// COMMENT ON PUBLICATION (confirmed via \h COMMENT) — see
// TestCommentOnOpaqueSQL's "publication" case for its now-real support.
func TestCommentOnOpaqueSQLUnknownKind(t *testing.T) {
	comment := "x"
	if got := commentOnOpaqueSQL("user_mapping", "", "alice@my_srv", "", "", &comment); got != "" {
		t.Errorf("got %q, want empty string for user_mapping (no Comment field)", got)
	}
	if got := commentOnOpaqueSQL("nonexistent_kind", "", "whatever", "", "", &comment); got != "" {
		t.Errorf("got %q, want empty string for a genuinely unrecognized kind", got)
	}
}

func TestDiffStatisticsNoSpuriousBodyDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "st", Body: "CREATE STATISTICS public.st ON a, b FROM st", Reconstructed: true},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "st", Body: "CREATE STATISTICS public.st ON a, b FROM public.st"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("spurious drift for equivalent statistics spelling: %d ops: %v", len(ops), ops)
	}
}

// TestCreateStatisticsEmitsCommentAfterCreate guards the CREATE-time half of
// the comment fix through the real Diff() pipeline (not just the SQL-string
// helper): a brand-new opaque object with Comment set must get both the
// CREATE and a following COMMENT ON, in that order (COMMENT ON a
// not-yet-existing object would itself error).
// TestCreatePublicationEmitsCommentAfterCreate and
// TestDiffPublicationCommentOnlyChangeNoDestroy guard the real gap found
// live-testing a demo project: ir.Publication had no Comment field at all,
// despite real PostgreSQL genuinely supporting COMMENT ON PUBLICATION
// (confirmed live via \h COMMENT) — Publication was excluded from the
// original 14-kind Comment/Grant fix on the mistaken assumption that it
// didn't apply.
func TestCreatePublicationEmitsCommentAfterCreate(t *testing.T) {
	d := New()
	comment := "large order inserts"
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name:    "order_changes",
			Body:    "CREATE PUBLICATION order_changes FOR ALL TABLES",
			Comment: &comment,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (CREATE + COMMENT), got %d: %v", len(ops), sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "CREATE PUBLICATION") {
		t.Errorf("expected CREATE PUBLICATION first, got: %s", ops[0].SQL())
	}
	want := `COMMENT ON PUBLICATION "order_changes" IS 'large order inserts';`
	if ops[1].SQL() != want {
		t.Errorf("got %q, want %q", ops[1].SQL(), want)
	}
}

// TestCreatePublicationEmitsOwner and TestDiffPublicationOwnerChanged guard
// RFC audit item #7: Publication had no Owner field at all.
func TestCreatePublicationEmitsOwner(t *testing.T) {
	d := New()
	owner := "app_admin"
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name:  "order_changes",
			Body:  "CREATE PUBLICATION order_changes FOR ALL TABLES",
			Owner: &owner,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	// RFC Section 11.5 (audit item #28): a brand-new object is created directly as
	// its declared owner via SET ROLE/RESET ROLE, not reassigned afterward
	// via ALTER ... OWNER TO — the latter never satisfies a matching
	// DEFAULT PRIVILEGES FOR ROLE block.
	if !containsSQL(ops, `SET ROLE "app_admin";`) || !containsSQL(ops, "RESET ROLE;") {
		t.Errorf("expected SET ROLE/RESET ROLE wrapping the CREATE PUBLICATION, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "OWNER TO") {
		t.Errorf("expected no ALTER PUBLICATION ... OWNER TO for a brand-new publication, got: %v", sqlList(ops))
	}
}

func TestDiffPublicationOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true, PublicationAllTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
			PublicationOwner: &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.Publication{Name: "p", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true, Body: "CREATE PUBLICATION p FOR ALL TABLES", Owner: &newOwner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER PUBLICATION "p" OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER PUBLICATION ... OWNER TO, got: %v", sqlList(ops))
	}
}

func TestDiffPublicationCommentOnlyChangeNoDestroy(t *testing.T) {
	d := New()
	oldComment := "old comment"
	newComment := "new comment"
	body := "CREATE PUBLICATION order_changes FOR ALL TABLES"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Publication{Name: "order_changes", Body: body, Comment: &oldComment},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Publication{Name: "order_changes", Body: body, Comment: &newComment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (COMMENT ON only), got %d: %v", len(ops), sqlList(ops))
	}
	want := `COMMENT ON PUBLICATION "order_changes" IS 'new comment';`
	if ops[0].SQL() != want {
		t.Errorf("got %q, want %q", ops[0].SQL(), want)
	}
}

func TestCreateStatisticsEmitsCommentAfterCreate(t *testing.T) {
	d := New()
	comment := "order status correlation"
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "orders_stats",
			Body:    "CREATE STATISTICS public.orders_stats ON a, b FROM orders",
			Comment: &comment,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (CREATE + COMMENT), got %d: %v", len(ops), sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "CREATE STATISTICS") {
		t.Errorf("expected CREATE STATISTICS first, got: %s", ops[0].SQL())
	}
	want := `COMMENT ON STATISTICS "public"."orders_stats" IS 'order status correlation';`
	if ops[1].SQL() != want {
		t.Errorf("got %q, want %q", ops[1].SQL(), want)
	}
}

// TestDiffOpaqueCommentOnlyChangeNoDestroy guards the UPDATE-time half: a
// comment-only edit (body unchanged) must emit a bare COMMENT ON, never the
// DROP+CREATE a real body change requires — needlessly dropping and
// recreating a STATISTICS object (or worse, a COLLATION/OPERATOR CLASS)
// just to change its comment would be actively destructive for no reason.
func TestDiffOpaqueCommentOnlyChangeNoDestroy(t *testing.T) {
	d := New()
	oldComment := "old comment"
	newComment := "new comment"
	body := "CREATE STATISTICS public.orders_stats ON a, b FROM orders"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "orders_stats", Body: body, Comment: &oldComment},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "orders_stats", Body: body, Comment: &newComment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (COMMENT ON only), got %d: %v", len(ops), sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP") || strings.Contains(o.SQL(), "CREATE STATISTICS") {
			t.Errorf("comment-only change must not DROP/CREATE, got: %s", o.SQL())
		}
	}
	want := `COMMENT ON STATISTICS "public"."orders_stats" IS 'new comment';`
	if ops[0].SQL() != want {
		t.Errorf("got %q, want %q", ops[0].SQL(), want)
	}
}

// TestDiffOpaqueCommentRemovalEmitsNull guards removing a comment entirely
// (desired.Comment nil, snapshot had one set) — must emit IS NULL, and must
// not be silently skipped just because the new value is nil.
func TestDiffOpaqueCommentRemovalEmitsNull(t *testing.T) {
	d := New()
	oldComment := "old comment"
	body := "CREATE STATISTICS public.orders_stats ON a, b FROM orders"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "orders_stats", Body: body, Comment: &oldComment},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "orders_stats", Body: body}, // Comment nil
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op, got %d: %v", len(ops), sqlList(ops))
	}
	want := `COMMENT ON STATISTICS "public"."orders_stats" IS NULL;`
	if ops[0].SQL() != want {
		t.Errorf("got %q, want %q", ops[0].SQL(), want)
	}
}

// TestCreateOpaqueSchemaScopedGetsSearchPath guards a real bug found
// live-testing a demo project: opaque schema-scoped kinds (COLLATION,
// OPERATOR, OPERATOR CLASS/FAMILY, STATISTICS, the 4 TEXT SEARCH kinds)
// render their Body as deparsed from source, which only carries a
// schema-qualified name if the user wrote one explicitly — DPG's own
// tracked Schema (from directory placement or an enclosing SCHEMA { }
// block) was never injected. Confirmed live: a STATISTICS object declared
// under a non-public schema was created in `public` instead, silently
// landing in the wrong place. Fixed via SET LOCAL search_path (the same
// technique pg_dump itself uses) rather than rewriting each of the 9
// differently-shaped CREATE statements to inject a qualified name.
func TestCreateOpaqueSchemaScopedGetsSearchPath(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "billing", Name: "billing_stats",
			Body: "CREATE STATISTICS billing_stats (dependencies) ON status, amount FROM invoices",
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE STATISTICS") {
			found = true
			if !strings.Contains(o.SQL(), `SET LOCAL search_path = "billing", public;`) {
				t.Errorf("expected SET LOCAL search_path before the CREATE, got: %s", o.SQL())
			}
		}
	}
	if !found {
		t.Fatal("expected a CREATE STATISTICS op")
	}
}

// TestCreateOpaquePublicSchemaNoSearchPath guards the common case: an
// object declared in the default "public" schema must not get a redundant
// SET LOCAL, keeping generated migrations for the overwhelmingly common
// case unchanged from before this fix.
func TestCreateOpaquePublicSchemaNoSearchPath(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st",
			Body: "CREATE STATISTICS st ON a, b FROM t",
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "search_path") {
			t.Errorf("did not expect a SET LOCAL search_path for the public schema, got: %s", o.SQL())
		}
	}
}

// TestCreateOpaqueNonSchemaScopedKindNoSearchPath guards CAST specifically:
// unlike the other 9 createOpaque-routed kinds, it has no schema concept at
// all in PostgreSQL (identified purely by its source/target type pair), so
// it must never get a SET LOCAL regardless of anything resembling a schema.
func TestCreateOpaqueNonSchemaScopedKindNoSearchPath(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Cast{
			SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "bool"},
			Body: "CREATE CAST (int4 AS bool) WITH INOUT AS IMPLICIT",
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "search_path") {
			t.Errorf("CAST has no schema concept, did not expect SET LOCAL search_path, got: %s", o.SQL())
		}
	}
}

// OFFLINE plan/apply: both sides source-derived (Reconstructed=false). A genuine
// edit to a collation/publication body MUST be detected (regression guard: the
// over-broad fix silently dropped this).
func TestDiffCollationOfflineDetectsEdit(t *testing.T) {
	d := New()
	cLocale, posixLocale := "C", "POSIX"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", Provider: "c", Collate: &cLocale, Ctype: &cLocale, Deterministic: true, Body: "CREATE COLLATION public.c (LOCALE = 'C')"},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", Provider: "c", Collate: &posixLocale, Ctype: &posixLocale, Deterministic: true, Body: "CREATE COLLATION public.c (LOCALE = 'POSIX')"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("offline: genuine collation body edit must be detected, got 0 ops")
	}
}

func TestDiffPublicationOfflineDetectsEdit(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Publication{Name: "p", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true, Body: "CREATE PUBLICATION p FOR ALL TABLES"},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p", Tables: []ir.PublicationTableRef{{Schema: "public", Name: "t"}},
			Insert: true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p FOR TABLE public.t",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("offline: genuine publication body edit must be detected, got 0 ops")
	}
}

func TestDiffSubscriptionUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "CREATE SUBSCRIPTION sub CONNECTION '{{vault:secret/db#pw}}' PUBLICATION pub"
	comment := "replication for orders"
	sub := &ir.Subscription{Name: "sub", ConnInfo: "{{vault:secret/db#pw}}", Body: body, Comment: &comment}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{sub}); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff([]pipeline.IRObject{sub}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged subscription, got %d: %v", len(ops), ops)
	}
}

// TestDiffSubscriptionOwnerChanged guards the lower-priority fresh-audit
// finding that ir.Subscription had no Owner support at all.
func TestDiffSubscriptionOwnerChanged(t *testing.T) {
	d := New()
	body := "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y' PUBLICATION pub"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Subscription{Name: "sub", ConnInfo: "host=x user=y", Body: body, Owner: strPtr("alice")},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Subscription{Name: "sub", ConnInfo: "host=x user=y", Body: body, Owner: strPtr("bob")},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER SUBSCRIPTION "sub" OWNER TO "bob"`) {
		t.Errorf("expected ALTER SUBSCRIPTION OWNER TO, got: %v", sqlList(ops))
	}
}

func TestDiffSubscriptionOwnerUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y' PUBLICATION pub"
	sub := &ir.Subscription{Name: "sub", ConnInfo: "host=x user=y", Body: body, Owner: strPtr("alice")}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{sub}); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff([]pipeline.IRObject{sub}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged Owner, got: %v", sqlList(ops))
	}
}

// TestDiffSubscriptionCommentOnlyChangeIsFieldLevel guards Comment being
// diffed separately from Body: a comment-only edit must emit a plain
// COMMENT ON SUBSCRIPTION, not a structured DROP+CREATE (which would
// needlessly interrupt a working, already-syncing subscription).
func TestDiffSubscriptionCommentOnlyChangeIsFieldLevel(t *testing.T) {
	d := New()
	body := "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y' PUBLICATION pub"
	oldComment := "old comment"
	newComment := "new comment"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Subscription{Name: "sub", ConnInfo: "host=x user=y", Body: body, Comment: &oldComment},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Subscription{Name: "sub", ConnInfo: "host=x user=y", Body: body, Comment: &newComment},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (COMMENT ON SUBSCRIPTION), got %d: %v", len(ops), ops)
	}
	if !strings.Contains(ops[0].SQL(), "COMMENT ON SUBSCRIPTION") || !strings.Contains(ops[0].SQL(), "new comment") {
		t.Errorf("expected a COMMENT ON SUBSCRIPTION op, got: %s", ops[0].SQL())
	}
	if strings.Contains(ops[0].SQL(), "DROP SUBSCRIPTION") {
		t.Error("comment-only change must not drop and recreate the subscription")
	}
}

// TestDiffSubscriptionRenamedFromEmitsAlterRename is the regression guard
// for Subscription rename detection: before this, ir.Subscription had no
// RenamedFrom field at all, so a renamed subscription was indistinguishable
// from "old one dropped, new one added."
func TestDiffSubscriptionRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	// Body text differs by name on each side — matching what the real
	// compiler actually produces for each declaration (Body is the literal
	// "CREATE SUBSCRIPTION <name> ..." text, always embedding that
	// declaration's own name) — not a shared/identical string, which would
	// mask the exact bug this test guards against (opaqueBodyForHash
	// normalizing the name back out before hashing).
	oldBody := "CREATE SUBSCRIPTION sub_old CONNECTION 'host=x user=y' PUBLICATION pub"
	newBody := "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y' PUBLICATION pub"
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Subscription{Name: "sub_old", ConnInfo: "host=x user=y", Body: oldBody},
	}); err != nil {
		t.Fatal(err)
	}
	old := "sub_old"
	desired := []pipeline.IRObject{
		&ir.Subscription{Name: "sub", ConnInfo: "host=x user=y", Body: newBody, RenamedFrom: &old},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP SUBSCRIPTION") {
		t.Fatalf("renamed subscription should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER SUBSCRIPTION "sub_old" RENAME TO "sub";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER SUBSCRIPTION RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffSubscriptionRenamedFromAndBodyChanged confirms a simultaneous
// rename + connection/publication change still requires DROP+CREATE, with
// no redundant separate rename — createSubscription already creates under
// the final (new) name.
func TestDiffSubscriptionRenamedFromAndBodyChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Subscription{Name: "sub_old", ConnInfo: "host=x user=y", Body: "CREATE SUBSCRIPTION sub_old CONNECTION 'host=x user=y' PUBLICATION pub"},
	}); err != nil {
		t.Fatal(err)
	}
	old := "sub_old"
	desired := []pipeline.IRObject{
		&ir.Subscription{
			Name: "sub", ConnInfo: "host=z user=y", RenamedFrom: &old,
			Body: "CREATE SUBSCRIPTION sub CONNECTION 'host=z user=y' PUBLICATION pub",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP SUBSCRIPTION IF EXISTS "sub_old"`) {
		t.Errorf("expected DROP referencing the subscription's actual (old) name, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a separate RENAME TO alongside drop+create, got: %v", sqlList(ops))
		}
	}
}

// TestDiffSubscriptionRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation.
func TestDiffSubscriptionRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_sub"
	desired := []pipeline.IRObject{
		&ir.Subscription{Name: "sub", RenamedFrom: &old, Body: "CREATE SUBSCRIPTION sub CONNECTION 'host=x' PUBLICATION pub"},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot subscription")
	}
}

// ── createSubscription / subscriptionCreateOp ───────────────────────────────────

type fakeDiffResolver struct{ values map[string]string }

func (f *fakeDiffResolver) Resolve(uri string) (string, error) {
	v, ok := f.values[uri]
	if !ok {
		return "", fmt.Errorf("no such secret: %s", uri)
	}
	return v, nil
}

func TestCreateSubscriptionSQLIsAlwaysThePlaceholderForm(t *testing.T) {
	o := &ir.Subscription{
		Name:     "sub",
		ConnInfo: "-",
		Body:     "CREATE SUBSCRIPTION sub CONNECTION '-' PUBLICATION pub",
	}
	ops, err := createSubscription(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if got := ops[0].SQL(); got != "CREATE SUBSCRIPTION sub CONNECTION '-' PUBLICATION pub;" {
		t.Errorf("SQL(): got %q, want the unresolved placeholder form", got)
	}
	if _, ok := ops[0].(pipeline.SecretBearingOp); !ok {
		t.Fatalf("expected %T to implement pipeline.SecretBearingOp", ops[0])
	}
}

func TestCreateSubscriptionExecSQLResolvesWholeValueTemplate(t *testing.T) {
	o := &ir.Subscription{
		Name:     "sub",
		ConnInfo: "{{vault:secret/db#conninfo}}",
		Body:     "CREATE SUBSCRIPTION sub CONNECTION '{{vault:secret/db#conninfo}}' PUBLICATION pub",
	}
	ops, _ := createSubscription(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	resolver := &fakeDiffResolver{values: map[string]string{
		"vault:secret/db#conninfo": "host=real dbname=real user=repl password=s3cr3t",
	}}
	got, err := sb.ExecSQL(resolver)
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE SUBSCRIPTION sub CONNECTION 'host=real dbname=real user=repl password=s3cr3t' PUBLICATION pub;"
	if got != want {
		t.Errorf("ExecSQL: got %q, want %q", got, want)
	}
	// SQL() must be completely unaffected by the ExecSQL call above.
	if got := ops[0].SQL(); got != o.Body+";" {
		t.Errorf("SQL() changed after ExecSQL was called: got %q", got)
	}
}

func TestCreateSubscriptionExecSQLResolvesPartialTemplateInNativeLiteral(t *testing.T) {
	o := &ir.Subscription{
		Name:     "sub",
		ConnInfo: "host=x user=y password={{env:DB_PASS}}",
		Body:     "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y password={{env:DB_PASS}}' PUBLICATION pub",
	}
	ops, _ := createSubscription(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	resolver := &fakeDiffResolver{values: map[string]string{"env:DB_PASS": "s3cr3t"}}
	got, err := sb.ExecSQL(resolver)
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y password=s3cr3t' PUBLICATION pub;"
	if got != want {
		t.Errorf("ExecSQL: got %q, want %q", got, want)
	}
}

func TestCreateSubscriptionExecSQLPlainLiteralNeverCallsResolver(t *testing.T) {
	o := &ir.Subscription{
		Name:     "sub",
		ConnInfo: "host=x user=y password=hunter2",
		Body:     "CREATE SUBSCRIPTION sub CONNECTION 'host=x user=y password=hunter2' PUBLICATION pub",
	}
	ops, _ := createSubscription(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	// A resolver with no entries: if it's called at all for a plain literal
	// with no {{...}}, this must fail loudly rather than silently succeed
	// with an empty value.
	got, err := sb.ExecSQL(&fakeDiffResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if got != o.Body+";" {
		t.Errorf("ExecSQL: got %q, want the literal returned unchanged", got)
	}
}

// TestCreateSubscriptionIsNonTransactional and
// TestDropSubscriptionIsNonTransactional guard two pre-existing bugs found
// live-testing Phase 3 (predating secret-reference support entirely — the
// generic createOpaque/destructiveOp paths every other opaque kind still
// correctly uses were never live-tested for Subscription specifically):
// both CREATE SUBSCRIPTION (WITH (create_slot = true), the default) and
// DROP SUBSCRIPTION error "cannot run inside a transaction block" in real
// PostgreSQL — confirmed live against a real postgres:17 pair. Destructive
// safety must survive the fix for DROP (still gated by --allow-destructive
// in cmd/dpg/apply.go), so DROP is a Destructive non-transactional op.
func TestCreateSubscriptionIsNonTransactional(t *testing.T) {
	ops, err := createSubscription(&ir.Subscription{
		Name: "sub", ConnInfo: "-", Body: "CREATE SUBSCRIPTION sub CONNECTION '-' PUBLICATION pub",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Mode() == pipeline.ModeTransactional {
		t.Error("CREATE SUBSCRIPTION must be non-transactional (PostgreSQL rejects create_slot=true inside a transaction block)")
	}
}

// TestCreateSubscriptionCommentIsAlsoNonTransactional guards a real ordering
// bug found live-testing: emit.Emit buckets ops purely by Transactional()
// into two separate lists, and the executor runs the whole transactional
// block before any non-transactional op. A transactional create-time COMMENT
// paired with the (necessarily non-transactional) CREATE above would run
// BEFORE it, erroring "subscription does not exist" against a real
// PostgreSQL server — confirmed live before this fix.
func TestCreateSubscriptionCommentIsAlsoNonTransactional(t *testing.T) {
	comment := "replication for orders"
	ops, err := createSubscription(&ir.Subscription{
		Name: "sub", ConnInfo: "-", Body: "CREATE SUBSCRIPTION sub CONNECTION '-' PUBLICATION pub", Comment: &comment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (CREATE + COMMENT), got %d", len(ops))
	}
	if ops[1].Mode() == pipeline.ModeTransactional {
		t.Error("create-time COMMENT ON SUBSCRIPTION must be non-transactional, matching CREATE's own bucket, or it runs before the subscription exists")
	}
	if !strings.Contains(ops[1].SQL(), "COMMENT ON SUBSCRIPTION") || !strings.Contains(ops[1].SQL(), comment) {
		t.Errorf("unexpected COMMENT op: %s", ops[1].SQL())
	}
}

func TestDropSubscriptionIsNonTransactional(t *testing.T) {
	ops := dropObject(&snapshot.SnapObject{
		Kind: "subscription", Opaque: &snapshot.SnapOpaque{Kind: "subscription", Name: "sub"},
	})
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Mode() == pipeline.ModeTransactional {
		t.Error("DROP SUBSCRIPTION must be non-transactional (PostgreSQL rejects it inside a transaction block)")
	}
	if ops[0].Safety() != pipeline.Destructive {
		t.Errorf("Safety() = %v, want Destructive (must still require --allow-destructive)", ops[0].Safety())
	}
}

func TestCreateSubscriptionExecSQLResolveErrorPropagates(t *testing.T) {
	o := &ir.Subscription{
		Name:     "sub",
		ConnInfo: "{{vault:secret/missing}}",
		Body:     "CREATE SUBSCRIPTION sub CONNECTION '{{vault:secret/missing}}' PUBLICATION pub",
	}
	ops, _ := createSubscription(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	if _, err := sb.ExecSQL(&fakeDiffResolver{}); err == nil {
		t.Fatal("expected an error when the referenced secret doesn't resolve")
	}
}

// ── Role attributes (RFC Section 11.1) ──────────────────────────────────────────────

func boolp(v bool) *bool { return &v }
func intp(v int) *int    { return &v }
func strp(v string) *string {
	return &v
}

func TestCreateRoleAllAttributes(t *testing.T) {
	o := &ir.Role{
		Name: "app_service", CanLogin: boolp(true), Superuser: boolp(false),
		CreateDB: boolp(false), CreateRole: boolp(false), Inherit: boolp(true),
		IsReplication: boolp(false), BypassRLS: boolp(false), ConnectionLimit: intp(20),
		Password: strp("hunter2"), ValidUntil: strp("2030-01-01"),
		Memberships: []ir.RoleMembership{
			{Role: "role_a", Direction: "IN_ROLE"},
			{Role: "role_b", Direction: "ROLE"},
			{Role: "role_c", Direction: "ROLE", Admin: true},
		},
	}
	ops := createRole(o)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op (no comment set), got %d: %v", len(ops), ops)
	}
	want := `CREATE ROLE "app_service" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 20 PASSWORD 'hunter2' VALID UNTIL '2030-01-01' IN ROLE "role_a" ROLE "role_b" ADMIN "role_c";`
	if got := ops[0].SQL(); got != want {
		t.Errorf("SQL():\n got  %q\n want %q", got, want)
	}
}

func TestCreateRoleWithPasswordIsSecretBearingOp(t *testing.T) {
	o := &ir.Role{Name: "svc", Password: strp("{{vault:secret/roles/svc#pw}}")}
	ops := createRole(o)
	sb, ok := ops[0].(pipeline.SecretBearingOp)
	if !ok {
		t.Fatalf("expected %T to implement pipeline.SecretBearingOp", ops[0])
	}
	if got := ops[0].SQL(); got != `CREATE ROLE "svc" PASSWORD '{{vault:secret/roles/svc#pw}}';` {
		t.Errorf("SQL(): got %q, want the unresolved placeholder form", got)
	}
	resolver := &fakeDiffResolver{values: map[string]string{"vault:secret/roles/svc#pw": "s3cr3t"}}
	got, err := sb.ExecSQL(resolver)
	if err != nil {
		t.Fatal(err)
	}
	if want := `CREATE ROLE "svc" PASSWORD 's3cr3t';`; got != want {
		t.Errorf("ExecSQL(): got %q, want %q", got, want)
	}
	// SQL() must be unaffected by the ExecSQL call above.
	if got := ops[0].SQL(); got != `CREATE ROLE "svc" PASSWORD '{{vault:secret/roles/svc#pw}}';` {
		t.Errorf("SQL() changed after ExecSQL was called: got %q", got)
	}
}

func TestCreateRoleNoPasswordIsPlainOp(t *testing.T) {
	o := &ir.Role{Name: "plain_role", CanLogin: boolp(false)}
	ops := createRole(o)
	if _, ok := ops[0].(pipeline.SecretBearingOp); ok {
		t.Error("a role with no PASSWORD must not implement SecretBearingOp")
	}
	if got := ops[0].SQL(); got != `CREATE ROLE "plain_role" NOLOGIN;` {
		t.Errorf("SQL(): got %q", got)
	}
}

func TestCreateRoleWithComment(t *testing.T) {
	o := &ir.Role{Name: "app_readonly", CanLogin: boolp(false), Comment: strp("Read-only access")}
	ops := createRole(o)
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (CREATE + COMMENT), got %d", len(ops))
	}
	if got := ops[1].SQL(); got != `COMMENT ON ROLE "app_readonly" IS 'Read-only access';` {
		t.Errorf("COMMENT op: got %q", got)
	}
}

func TestDiffRoleAttrChangesBatchedIntoOneAlter(t *testing.T) {
	o := &ir.Role{Name: "svc", CanLogin: boolp(true), ConnectionLimit: intp(5)}
	snap := &snapshot.SnapRole{Name: "svc", CanLogin: boolp(false), ConnectionLimit: intp(1)}
	ops := diffRole(o, snap, false)
	if len(ops) != 1 {
		t.Fatalf("expected 1 batched ALTER ROLE op, got %d: %v", len(ops), ops)
	}
	want := `ALTER ROLE "svc" LOGIN CONNECTION LIMIT 5;`
	if got := ops[0].SQL(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDiffRoleUndeclaredFieldsNeverCompared(t *testing.T) {
	// o declares nothing; snap has a totally different state. Since nothing
	// is declared, nothing should be diffed — undeclared means "not managed
	// by DPG for this role", not "reset to PostgreSQL's default".
	o := &ir.Role{Name: "svc"}
	snap := &snapshot.SnapRole{
		Name: "svc", CanLogin: boolp(true), Superuser: boolp(true),
		ConnectionLimit: intp(99), ValidUntil: strp("2020-01-01"),
	}
	ops := diffRole(o, snap, false)
	if len(ops) != 0 {
		t.Errorf("expected no ops for an undeclared-everything role, got %d: %v", len(ops), ops)
	}
}

func TestDiffRolePasswordChangeDetectedViaHash(t *testing.T) {
	o := &ir.Role{Name: "svc", Password: strp("{{vault:secret/svc#new}}")}
	snap := &snapshot.SnapRole{Name: "svc", PasswordHash: hashText("{{vault:secret/svc#old}}")}
	ops := diffRole(o, snap, false)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op (ALTER ROLE PASSWORD), got %d: %v", len(ops), ops)
	}
	sb, ok := ops[0].(pipeline.SecretBearingOp)
	if !ok {
		t.Fatalf("expected %T to implement pipeline.SecretBearingOp", ops[0])
	}
	if got := ops[0].SQL(); got != `ALTER ROLE "svc" PASSWORD '{{vault:secret/svc#new}}';` {
		t.Errorf("SQL(): got %q", got)
	}
	if ops[0].Safety() != pipeline.Caution {
		t.Errorf("Safety() = %v, want Caution (invalidates existing sessions using the old password)", ops[0].Safety())
	}
	resolver := &fakeDiffResolver{values: map[string]string{"vault:secret/svc#new": "s3cr3t"}}
	got, err := sb.ExecSQL(resolver)
	if err != nil {
		t.Fatal(err)
	}
	if want := `ALTER ROLE "svc" PASSWORD 's3cr3t';`; got != want {
		t.Errorf("ExecSQL(): got %q, want %q", got, want)
	}
}

// TestDiffRolePasswordNotComparedAgainstLiveSnapshot: a snapshot built from
// the live catalog never carries a password hash, so comparing against it
// would propose ALTER ROLE ... PASSWORD on every plan --live.
func TestDiffRolePasswordNotComparedAgainstLiveSnapshot(t *testing.T) {
	o := &ir.Role{Name: "svc", Password: strp("{{env:SVC_PW}}")}
	ops := diffRole(o, &snapshot.SnapRole{Name: "svc"}, true)
	if len(ops) != 0 {
		t.Errorf("expected no ops against a live snapshot, got %d: %v", len(ops), ops)
	}
}

// TestDiffLiveSnapshotLeavesUndeclaredRolesAlone: against a live snapshot, a
// role DPG doesn't declare (the connecting role, another application's role)
// is not dropped. Against a stored snapshot, a removed declaration still is.
func TestDiffLiveSnapshotLeavesUndeclaredRolesAlone(t *testing.T) {
	for _, live := range []bool{true, false} {
		snap := &pipeline.Snapshot{Live: live}
		_ = snap.SetObject("other_app", &snapshot.SnapObject{
			Kind: "role",
			Role: &snapshot.SnapRole{Name: "other_app"},
		})
		ops, err := New().Diff(nil, snap)
		if err != nil {
			t.Fatal(err)
		}
		dropped := containsSQL(ops, `DROP ROLE IF EXISTS "other_app";`)
		if dropped == live {
			t.Errorf("live=%v: dropped=%v, got: %v", live, dropped, sqlList(ops))
		}
	}
}

func TestDiffRolePasswordUnchangedIsNoop(t *testing.T) {
	o := &ir.Role{Name: "svc", Password: strp("{{vault:secret/svc#pw}}")}
	snap := &snapshot.SnapRole{Name: "svc", PasswordHash: hashText("{{vault:secret/svc#pw}}")}
	ops := diffRole(o, snap, false)
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged password, got %d: %v", len(ops), ops)
	}
}

func TestDiffRoleMembershipAddedAndRemoved(t *testing.T) {
	o := &ir.Role{
		Name: "svc",
		Memberships: []ir.RoleMembership{
			{Role: "reader", Direction: "IN_ROLE"},
			{Role: "new_role", Direction: "IN_ROLE"},
			{Role: "member_new", Direction: "ROLE"},
			{Role: "admin_new", Direction: "ROLE", Admin: true},
		},
	}
	snap := &snapshot.SnapRole{
		Name: "svc",
		Memberships: []snapshot.SnapRoleMembership{
			{Role: "reader", Direction: "IN_ROLE"},
			{Role: "old_role", Direction: "IN_ROLE"},
			{Role: "member_old", Direction: "ROLE"},
			{Role: "admin_old", Direction: "ROLE", Admin: true},
		},
	}
	ops := diffRole(o, snap, false)
	var sqls []string
	for _, op := range ops {
		sqls = append(sqls, op.SQL())
	}
	wantContains := []string{
		`GRANT "new_role" TO "svc";`,
		`REVOKE "old_role" FROM "svc";`,
		`GRANT "svc" TO "member_new";`,
		`REVOKE "svc" FROM "member_old";`,
		`GRANT "svc" TO "admin_new" WITH ADMIN OPTION;`,
		`REVOKE "svc" FROM "admin_old";`,
	}
	for _, want := range wantContains {
		if !slices.Contains(sqls, want) {
			t.Errorf("expected an op with SQL %q, got: %v", want, sqls)
		}
	}
	if len(ops) != len(wantContains) {
		t.Errorf("expected exactly %d ops, got %d: %v", len(wantContains), len(ops), sqls)
	}
}

func TestDiffRoleMembershipUndeclaredNeverDiffed(t *testing.T) {
	o := &ir.Role{Name: "svc"} // Memberships nil — no direction managed
	snap := &snapshot.SnapRole{Name: "svc", Memberships: []snapshot.SnapRoleMembership{
		{Role: "reader", Direction: "IN_ROLE"},
		{Role: "m", Direction: "ROLE"},
		{Role: "a", Direction: "ROLE", Admin: true},
	}}
	ops := diffRole(o, snap, false)
	if len(ops) != 0 {
		t.Errorf("expected no membership ops when membership fields are undeclared, got %d: %v", len(ops), ops)
	}
}

// TestDiffRoleMembershipSameNameBothDirectionsNoLongerAmbiguous guards the
// deliberate behavior change RFC audit item #32's unification introduced:
// pre-unification, the same role name could appear in both the plain-member
// and admin-member buckets at once with unclear, order-dependent resulting
// behavior (two separate GRANT statements racing for the same
// pg_auth_members row). Post-unification there is exactly one entry per
// (Direction, Role) key, so declaring "peer WITH ADMIN OPTION" is simply
// the row's one Admin value — no ambiguity possible.
func TestDiffRoleMembershipSameNameBothDirectionsNoLongerAmbiguous(t *testing.T) {
	o := &ir.Role{Name: "svc", Memberships: []ir.RoleMembership{
		{Role: "peer", Direction: "ROLE", Admin: true},
	}}
	ops := diffRole(o, &snapshot.SnapRole{Name: "svc"}, false)
	var sqls []string
	for _, op := range ops {
		sqls = append(sqls, op.SQL())
	}
	if !slices.Contains(sqls, `GRANT "svc" TO "peer" WITH ADMIN OPTION;`) {
		t.Errorf("expected exactly one GRANT with WITH ADMIN OPTION, got: %v", sqls)
	}
	count := 0
	for _, s := range sqls {
		if strings.Contains(s, "peer") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one op mentioning peer, got %d: %v", count, sqls)
	}
}

// TestDiffRoleMembershipInheritSetOnlyManagedWhenDeclared guards the
// "declared, so managed" convention applied to WITH INHERIT/SET (RFC audit
// item #32): an entry whose desired side never mentions INHERIT/SET must
// not be re-GRANTed just because the snapshot happens to record a concrete
// value for it (e.g. from live introspection recording a non-default
// value) — matching Grant.GrantedBy's identical precedent.
func TestDiffRoleMembershipInheritSetOnlyManagedWhenDeclared(t *testing.T) {
	recordedInherit := false
	o := &ir.Role{Name: "svc", Memberships: []ir.RoleMembership{
		{Role: "parent1", Direction: "IN_ROLE"},
	}}
	snap := &snapshot.SnapRole{Name: "svc", Memberships: []snapshot.SnapRoleMembership{
		{Role: "parent1", Direction: "IN_ROLE", Inherit: &recordedInherit},
	}}
	ops := diffRole(o, snap, false)
	if len(ops) != 0 {
		t.Errorf("expected no ops (Inherit not declared on desired side), got: %v", ops)
	}
}

// TestDiffRoleMembershipInheritChangeReGrants guards a genuine declared
// INHERIT change.
func TestDiffRoleMembershipInheritChangeReGrants(t *testing.T) {
	oldInherit := true
	newInherit := false
	o := &ir.Role{Name: "svc", Memberships: []ir.RoleMembership{
		{Role: "parent1", Direction: "IN_ROLE", Inherit: &newInherit},
	}}
	snap := &snapshot.SnapRole{Name: "svc", Memberships: []snapshot.SnapRoleMembership{
		{Role: "parent1", Direction: "IN_ROLE", Inherit: &oldInherit},
	}}
	ops := diffRole(o, snap, false)
	if !containsSQL(ops, `GRANT "parent1" TO "svc" WITH INHERIT FALSE;`) {
		t.Errorf("expected a re-GRANT with WITH INHERIT FALSE, got: %v", sqlList(ops))
	}
}

// ── UserMapping OPTIONS secret references (Secret resolution, Phase 5) ──────────

func TestCreateUserMappingSQLIsAlwaysThePlaceholderForm(t *testing.T) {
	o := &ir.UserMapping{
		User: "app", Server: "srv",
		Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password '{{vault:secret/fdw/db#pw}}')",
	}
	ops, err := createUserMapping(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	want := o.Body + ";"
	if got := ops[0].SQL(); got != want {
		t.Errorf("SQL(): got %q, want %q", got, want)
	}
	if _, ok := ops[0].(pipeline.SecretBearingOp); !ok {
		t.Fatalf("expected %T to implement pipeline.SecretBearingOp", ops[0])
	}
}

func TestCreateUserMappingExecSQLResolvesWithinOptions(t *testing.T) {
	o := &ir.UserMapping{
		User: "app", Server: "srv",
		Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password '{{vault:secret/fdw/db#pw}}')",
	}
	ops, _ := createUserMapping(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	resolver := &fakeDiffResolver{values: map[string]string{"vault:secret/fdw/db#pw": "s3cr3t"}}
	got, err := sb.ExecSQL(resolver)
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password 's3cr3t');"
	if got != want {
		t.Errorf("ExecSQL(): got %q, want %q", got, want)
	}
	// SQL() must be unaffected by the ExecSQL call above.
	if got := ops[0].SQL(); got != o.Body+";" {
		t.Errorf("SQL() changed after ExecSQL was called: got %q", got)
	}
}

func TestCreateUserMappingExecSQLPlainLiteralNeverCallsResolver(t *testing.T) {
	o := &ir.UserMapping{
		User: "app", Server: "srv",
		Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password 'hunter2')",
	}
	ops, _ := createUserMapping(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	got, err := sb.ExecSQL(&fakeDiffResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if got != o.Body+";" {
		t.Errorf("ExecSQL(): got %q, want the literal returned unchanged", got)
	}
}

func TestCreateUserMappingExecSQLResolveErrorPropagates(t *testing.T) {
	o := &ir.UserMapping{
		User: "app", Server: "srv",
		Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (password '{{vault:secret/missing}}')",
	}
	ops, _ := createUserMapping(o)
	sb := ops[0].(pipeline.SecretBearingOp)
	if _, err := sb.ExecSQL(&fakeDiffResolver{}); err == nil {
		t.Fatal("expected an error when the referenced secret doesn't resolve")
	}
}

func TestDiffUserMappingOfflineDetectsEdit(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.UserMapping{User: "app", Server: "srv", Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (password '{{vault:secret/db#old}}')"},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.UserMapping{User: "app", Server: "srv", Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (password '{{vault:secret/db#new}}')"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (DROP + CREATE), got %d: %v", len(ops), ops)
	}
	if !strings.Contains(ops[0].SQL(), "DROP USER MAPPING") {
		t.Errorf("op[0] = %q, want DROP USER MAPPING", ops[0].SQL())
	}
	if _, ok := ops[1].(pipeline.SecretBearingOp); !ok {
		t.Errorf("expected the CREATE op (%T) to implement pipeline.SecretBearingOp", ops[1])
	}
}

func TestDiffUserMappingUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "CREATE USER MAPPING FOR app SERVER srv OPTIONS (password '{{vault:secret/db#pw}}')"
	um := &ir.UserMapping{User: "app", Server: "srv", Body: body}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{um}); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff([]pipeline.IRObject{um}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged user mapping, got %d: %v", len(ops), ops)
	}
}

// TestDiffOpaqueOfflineEditEmitsStructuredDropRecreate is the regression guard
// for #3 (real update path for opaque objects): a genuine offline body edit
// must emit a structured DROP (matching what dropObject emits when the object
// is removed outright) followed by a CREATE from the new body — not the old
// "-- WARNING: ... manual DROP + recreate required" comment placeholder. All
// 17 opaque kinds are covered, including "operator" — previously excluded
// because dropObject's operator case couldn't safely build a DROP OPERATOR
// statement (PG requires a mandatory (lefttype, righttype) clause ir.Operator
// didn't capture); see ir.Operator.LeftType/RightType and ir.OperandsKey.
func TestDiffOpaqueOfflineEditEmitsStructuredDropRecreate(t *testing.T) {
	intType := ir.TypeRef{Name: "integer"}
	cases := []struct {
		name             string
		oldObj, newObj   pipeline.IRObject
		wantDropSubstr   string
		wantNewInBody    string
		wantCreateNonTxn bool // CREATE must run outside the transaction
		dropIsSafe       bool // false (Destructive) unless overridden — event_trigger is the sole exception, RFC Section 14.1
	}{
		{
			name:           "tablespace",
			oldObj:         &ir.Tablespace{Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'"},
			newObj:         &ir.Tablespace{Name: "ts", Location: "/data/ts2", Body: "CREATE TABLESPACE ts LOCATION '/data/ts2'"},
			wantDropSubstr: `DROP TABLESPACE IF EXISTS "ts";`,
			wantNewInBody:  "/data/ts2",
			// Safe but non-transactional: CREATE TABLESPACE cannot run
			// inside a transaction block — see createOpaque's doc comment.
			wantCreateNonTxn: true,
		},
		{
			name:           "fdw",
			oldObj:         &ir.ForeignDataWrapper{Name: "myfdw", Handler: "h1", Body: "CREATE FOREIGN DATA WRAPPER myfdw HANDLER h1"},
			newObj:         &ir.ForeignDataWrapper{Name: "myfdw", Handler: "h2", Body: "CREATE FOREIGN DATA WRAPPER myfdw HANDLER h2"},
			wantDropSubstr: `DROP FOREIGN DATA WRAPPER IF EXISTS "myfdw";`,
			wantNewInBody:  "h2",
		},
		{
			// A FDW-wrapper change specifically (not an OPTIONS-only change,
			// which RFC Section 14.9 gives a real, targeted ALTER SERVER for — see
			// TestDiffForeignServerOptionsChangeIsSafeAlter — real PostgreSQL
			// has no ALTER SERVER ... FOREIGN DATA WRAPPER at all, confirmed
			// via `\h ALTER SERVER`).
			name:           "server",
			oldObj:         &ir.ForeignServer{Name: "srv", FDWName: "myfdw1", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw1"},
			newObj:         &ir.ForeignServer{Name: "srv", FDWName: "myfdw2", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw2"},
			wantDropSubstr: `DROP SERVER IF EXISTS "srv";`,
			wantNewInBody:  "myfdw2",
		},
		{
			name:           "user_mapping",
			oldObj:         &ir.UserMapping{User: "alice", Server: "srv", Body: "CREATE USER MAPPING FOR alice SERVER srv OPTIONS (password 'p1')"},
			newObj:         &ir.UserMapping{User: "alice", Server: "srv", Body: "CREATE USER MAPPING FOR alice SERVER srv OPTIONS (password 'p2')"},
			wantDropSubstr: `DROP USER MAPPING IF EXISTS FOR "alice" SERVER "srv";`,
			wantNewInBody:  "'p2'",
		},
		{
			// An AllTables change specifically (not a Tables-list-only or
			// WITH (publish = ...)-only change, which RFC Section 13.1 gives a
			// real, targeted ALTER PUBLICATION for — see
			// TestDiffPublicationTablesChangeIsSafeAlter/
			// TestDiffPublicationPublishOptionsChangeIsSafeAlter — real
			// PostgreSQL has no way to convert a FOR ALL TABLES
			// publication to/from an explicit table list via ALTER,
			// confirmed via a live "Tables cannot be added to or dropped
			// from FOR ALL TABLES publications" error).
			name: "publication",
			oldObj: &ir.Publication{
				Name: "p", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true,
				Body: "CREATE PUBLICATION p FOR ALL TABLES",
			},
			newObj: &ir.Publication{
				Name: "p", Tables: []ir.PublicationTableRef{{Schema: "public", Name: "t"}},
				Insert: true, Update: true, Delete: true, Truncate: true,
				Body: "CREATE PUBLICATION p FOR TABLE public.t",
			},
			wantDropSubstr: `DROP PUBLICATION IF EXISTS "p";`,
			wantNewInBody:  "public.t",
		},
		{
			name:           "subscription",
			oldObj:         &ir.Subscription{Name: "sub", ConnInfo: "x", Body: "CREATE SUBSCRIPTION sub CONNECTION 'x' PUBLICATION p1"},
			newObj:         &ir.Subscription{Name: "sub", ConnInfo: "x", Body: "CREATE SUBSCRIPTION sub CONNECTION 'x' PUBLICATION p2"},
			wantDropSubstr: `DROP SUBSCRIPTION IF EXISTS "sub";`,
			wantNewInBody:  "p2",
			// Safe but non-transactional: CREATE SUBSCRIPTION defaults to
			// WITH (create_slot = true), which cannot run inside a
			// transaction block — see createSubscription's doc comment.
			wantCreateNonTxn: true,
		},
		{
			name:           "event_trigger",
			oldObj:         &ir.EventTrigger{Name: "et", Event: "ddl_command_start", Function: "f1", Body: "CREATE EVENT TRIGGER et ON ddl_command_start EXECUTE FUNCTION f1()"},
			newObj:         &ir.EventTrigger{Name: "et", Event: "ddl_command_start", Function: "f2", Body: "CREATE EVENT TRIGGER et ON ddl_command_start EXECUTE FUNCTION f2()"},
			wantDropSubstr: `DROP EVENT TRIGGER IF EXISTS "et";`,
			wantNewInBody:  "f2()",
			// Unlike every other opaque kind here, an event trigger holds no
			// data and nothing depends on it — RFC Section 14.1 explicitly classifies
			// its DROP+CREATE as SAFE, unlike Collation/Cast/Operator below
			// (all explicitly DESTRUCTIVE per their own RFC sections).
			dropIsSafe: true,
		},
		{
			name: "collation",
			oldObj: &ir.Collation{
				Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
				Body: "CREATE COLLATION public.c (LOCALE = 'C')",
			},
			newObj: &ir.Collation{
				Schema: "public", Name: "c", Provider: "c", Collate: strPtr("POSIX"), Ctype: strPtr("POSIX"), Deterministic: true,
				Body: "CREATE COLLATION public.c (LOCALE = 'POSIX')",
			},
			wantDropSubstr: `DROP COLLATION IF EXISTS "public"."c";`,
			wantNewInBody:  "POSIX",
		},
		{
			name: "operator",
			oldObj: &ir.Operator{Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
				Body: "CREATE OPERATOR public.=== (FUNCTION = f1, LEFTARG = integer, RIGHTARG = integer)"},
			newObj: &ir.Operator{Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
				Body: "CREATE OPERATOR public.=== (FUNCTION = f2, LEFTARG = integer, RIGHTARG = integer)"},
			wantDropSubstr: `DROP OPERATOR IF EXISTS "public".===(integer, integer);`, // symbol never quoted — see qualOperatorIdent
			wantNewInBody:  "f2",
		},
		{
			// Members must actually differ (not just Body text) here: since
			// diffOperatorClass now compares Members structurally before
			// falling back to raw BodyHash (see TestDiffOperatorClassMember*
			// below), two fixtures with identical (here: empty) Members but
			// different Body would wrongly short-circuit to a no-op —
			// exercising that would only prove a synthetic-fixture blind
			// spot, not real behavior (every real OperatorClass, from either
			// the builder or introspection, always has Members mirroring
			// Body). Populating both keeps this guarding a genuine member
			// change, same as every other kind in this table.
			name: "operator_class",
			oldObj: &ir.OperatorClass{Schema: "public", Name: "oc", AccessMethod: "gin",
				Body: "CREATE OPERATOR CLASS public.oc FOR TYPE int4 USING gin AS OPERATOR 1 = FUNCTION 1 f1()",
				Members: []pipeline.OpFamilyMember{
					{Number: 1, Name: pipeline.Identifier{Name: "="}, LeftType: "integer", RightType: "integer"},
					{IsFunction: true, Number: 1, Name: pipeline.Identifier{Name: "f1"}, LeftType: "integer", RightType: "integer"},
				},
			},
			newObj: &ir.OperatorClass{Schema: "public", Name: "oc", AccessMethod: "gin",
				Body: "CREATE OPERATOR CLASS public.oc FOR TYPE int4 USING gin AS OPERATOR 1 = FUNCTION 1 f2()",
				Members: []pipeline.OpFamilyMember{
					{Number: 1, Name: pipeline.Identifier{Name: "="}, LeftType: "integer", RightType: "integer"},
					{IsFunction: true, Number: 1, Name: pipeline.Identifier{Name: "f2"}, LeftType: "integer", RightType: "integer"},
				},
			},
			wantDropSubstr: `DROP OPERATOR CLASS IF EXISTS "public"."oc" USING gin;`,
			wantNewInBody:  "f2()",
		},
		{
			name:           "operator_family",
			oldObj:         &ir.OperatorFamily{Schema: "public", Name: "of", AccessMethod: "gin", Body: "CREATE OPERATOR FAMILY public.of USING gin"},
			newObj:         &ir.OperatorFamily{Schema: "public", Name: "of", AccessMethod: "gin", Body: "CREATE OPERATOR FAMILY public.of USING gin AS OPERATOR 1 = (int4, int4)"},
			wantDropSubstr: `DROP OPERATOR FAMILY IF EXISTS "public"."of" USING gin;`,
			wantNewInBody:  "OPERATOR 1",
		},
		{
			name:           "cast",
			oldObj:         &ir.Cast{SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "text"}, Method: "f", Function: "f1", Body: "CREATE CAST (int4 AS text) WITH FUNCTION f1(int4)"},
			newObj:         &ir.Cast{SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "text"}, Method: "f", Function: "f2", Body: "CREATE CAST (int4 AS text) WITH FUNCTION f2(int4)"},
			wantDropSubstr: `DROP CAST IF EXISTS (int4 AS text);`,
			wantNewInBody:  "f2(int4)",
		},
		{
			name: "statistics",
			oldObj: &ir.StatisticsObject{
				Schema: "public", Name: "st", Table: "public.t", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
				Body: "CREATE STATISTICS public.st (ndistinct) ON a, b FROM t",
			},
			newObj: &ir.StatisticsObject{
				Schema: "public", Name: "st", Table: "public.t", Kinds: []string{"dependencies"}, Columns: []string{"a", "b"},
				Body: "CREATE STATISTICS public.st (dependencies) ON a, b FROM t",
			},
			wantDropSubstr: `DROP STATISTICS IF EXISTS "public"."st";`,
			wantNewInBody:  "dependencies",
		},
		{
			name:           "ts_config",
			oldObj:         &ir.TSConfig{Schema: "public", Name: "tc", Body: "CREATE TEXT SEARCH CONFIGURATION public.tc (COPY = simple1)"},
			newObj:         &ir.TSConfig{Schema: "public", Name: "tc", Body: "CREATE TEXT SEARCH CONFIGURATION public.tc (COPY = simple2)"},
			wantDropSubstr: `DROP TEXT SEARCH CONFIGURATION IF EXISTS "public"."tc";`,
			wantNewInBody:  "simple2",
		},
		{
			// A TEMPLATE change specifically (not an option-only change,
			// which RFC audit item #52 gives a real, targeted
			// ALTER TEXT SEARCH DICTIONARY for — see
			// TestDiffTSDictOptionsAddedChangedRemoved — real PostgreSQL
			// has no ALTER for TEMPLATE at all, confirmed live).
			name: "ts_dict",
			oldObj: &ir.TSDict{Schema: "public", Name: "td", TemplateName: "simple",
				Body: "CREATE TEXT SEARCH DICTIONARY public.td (TEMPLATE = simple)"},
			newObj: &ir.TSDict{Schema: "public", Name: "td", TemplateName: "snowball",
				Body: "CREATE TEXT SEARCH DICTIONARY public.td (TEMPLATE = snowball)"},
			wantDropSubstr: `DROP TEXT SEARCH DICTIONARY IF EXISTS "public"."td";`,
			wantNewInBody:  "snowball",
		},
		{
			name:           "ts_parser",
			oldObj:         &ir.TSParser{Schema: "public", Name: "tp", Body: "CREATE TEXT SEARCH PARSER public.tp (START = f1, GETTOKEN = g, END = e, LEXTYPES = l)"},
			newObj:         &ir.TSParser{Schema: "public", Name: "tp", Body: "CREATE TEXT SEARCH PARSER public.tp (START = f2, GETTOKEN = g, END = e, LEXTYPES = l)"},
			wantDropSubstr: `DROP TEXT SEARCH PARSER IF EXISTS "public"."tp";`,
			wantNewInBody:  "f2",
		},
		{
			name:           "ts_template",
			oldObj:         &ir.TSTemplate{Schema: "public", Name: "tt", Body: "CREATE TEXT SEARCH TEMPLATE public.tt (LEXIZE = f1)"},
			newObj:         &ir.TSTemplate{Schema: "public", Name: "tt", Body: "CREATE TEXT SEARCH TEMPLATE public.tt (LEXIZE = f2)"},
			wantDropSubstr: `DROP TEXT SEARCH TEMPLATE IF EXISTS "public"."tt";`,
			wantNewInBody:  "f2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New()
			snap := &pipeline.Snapshot{}
			if err := snapshot.Populate(snap, []pipeline.IRObject{tc.oldObj}); err != nil {
				t.Fatal(err)
			}
			ops, err := d.Diff([]pipeline.IRObject{tc.newObj}, snap)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(ops) != 2 {
				t.Fatalf("want 2 ops (structured DROP + CREATE), got %d: %v", len(ops), ops)
			}
			dropSQL := ops[0].SQL()
			createSQL := ops[1].SQL()
			if !strings.Contains(dropSQL, tc.wantDropSubstr) {
				t.Errorf("op[0] = %q, want substring %q", dropSQL, tc.wantDropSubstr)
			}
			if !strings.Contains(createSQL, tc.wantNewInBody) {
				t.Errorf("op[1] = %q, want substring %q (new body)", createSQL, tc.wantNewInBody)
			}
			wantDropSafety := pipeline.Destructive
			if tc.dropIsSafe {
				wantDropSafety = pipeline.Safe
			}
			if ops[0].Safety() != wantDropSafety {
				t.Errorf("DROP op safety = %v, want %v", ops[0].Safety(), wantDropSafety)
			}
			if ops[1].Safety() != pipeline.Safe {
				t.Errorf("CREATE op safety = %v, want SAFE", ops[1].Safety())
			}
			if (ops[1].Mode() == pipeline.ModeNonTransactional) != tc.wantCreateNonTxn {
				t.Errorf("CREATE op mode = %v, want non-transactional %v", ops[1].Mode(), tc.wantCreateNonTxn)
			}
			for _, op := range ops {
				if strings.Contains(op.SQL(), "manual DROP + recreate required") {
					t.Errorf("structured path must not fall back to the manual warning, got: %s", op.SQL())
				}
			}
		})
	}
}

// ── TSConfig MAPPING FOR (RFC Section 12.1) ──────────────────────────────────────────
// tc.Mappings was parsed by the blockparser and copied onto ir.TSConfig by
// the builder, but never read by the differ at all — a declared MAPPING FOR
// entry silently produced no ALTER TEXT SEARCH CONFIGURATION statement
// whatsoever, found live-testing a demo project.

func TestDiffCreateTSConfigEmitsMapping(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.TSConfig{
			Schema: "public", Name: "demo_search",
			ParserSchema: "pg_catalog", ParserName: "default",
			Body: "CREATE TEXT SEARCH CONFIGURATION public.demo_search (PARSER = pg_catalog.default)",
			Mappings: []pipeline.TSMappingDef{
				{TokenTypes: []string{"word", "hword"}, Dictionaries: []pipeline.Identifier{{Name: "english_stem"}}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH CONFIGURATION "public"."demo_search" ALTER MAPPING FOR word, hword WITH english_stem;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffCreateTSConfigEmitsMappingFallbackChain guards the real
// multi-dictionary PG feature (WITH dict1, dict2 — a fallback chain).
func TestDiffCreateTSConfigEmitsMappingFallbackChain(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.TSConfig{
			Schema: "public", Name: "demo_search",
			ParserSchema: "pg_catalog", ParserName: "default",
			Body: "CREATE TEXT SEARCH CONFIGURATION public.demo_search (PARSER = pg_catalog.default)",
			Mappings: []pipeline.TSMappingDef{
				{TokenTypes: []string{"word"}, Dictionaries: []pipeline.Identifier{{Name: "unaccent"}, {Name: "english_stem"}}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH CONFIGURATION "public"."demo_search" ALTER MAPPING FOR word WITH unaccent, english_stem;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

func snapTSConfig(schema, name string, mappings []snapshot.SnapTSMapping) *pipeline.Snapshot {
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject(schema+"."+name, &snapshot.SnapObject{
		Kind: "ts_config",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_config", Schema: schema, Name: name,
			BodyHash: "", // Reconstructed-equivalent: no body-hash comparison
			Mappings: mappings,
		},
	})
	return snap
}

// TestDiffTSConfigMappingAdded guards the diff-existing-config path (not
// just create): adding a MAPPING FOR entry to an already-applied config.
func TestDiffTSConfigMappingAdded(t *testing.T) {
	d := New()
	snap := snapTSConfig("public", "demo_search", nil)
	desired := []pipeline.IRObject{
		&ir.TSConfig{
			Schema: "public", Name: "demo_search",
			ParserSchema: "pg_catalog", ParserName: "default",
			Reconstructed: true, // skip body-hash recreate; only diffing mappings here
			Mappings: []pipeline.TSMappingDef{
				{TokenTypes: []string{"word"}, Dictionaries: []pipeline.Identifier{{Name: "english_stem"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH CONFIGURATION "public"."demo_search" ALTER MAPPING FOR word WITH english_stem;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSConfigMappingChanged guards changing an existing token type's
// dictionary chain.
func TestDiffTSConfigMappingChanged(t *testing.T) {
	d := New()
	snap := snapTSConfig("public", "demo_search", []snapshot.SnapTSMapping{
		{TokenTypes: []string{"word"}, Dictionaries: []string{"simple"}},
	})
	desired := []pipeline.IRObject{
		&ir.TSConfig{
			Schema: "public", Name: "demo_search",
			ParserSchema: "pg_catalog", ParserName: "default",
			Reconstructed: true,
			Mappings: []pipeline.TSMappingDef{
				{TokenTypes: []string{"word"}, Dictionaries: []pipeline.Identifier{{Name: "english_stem"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH CONFIGURATION "public"."demo_search" ALTER MAPPING FOR word WITH english_stem;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSConfigMappingRemoved guards the removal path: DROP MAPPING FOR.
func TestDiffTSConfigMappingRemoved(t *testing.T) {
	d := New()
	snap := snapTSConfig("public", "demo_search", []snapshot.SnapTSMapping{
		{TokenTypes: []string{"word"}, Dictionaries: []string{"english_stem"}},
	})
	desired := []pipeline.IRObject{
		&ir.TSConfig{
			Schema: "public", Name: "demo_search",
			ParserSchema: "pg_catalog", ParserName: "default",
			Reconstructed: true,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH CONFIGURATION "public"."demo_search" DROP MAPPING FOR word;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSConfigMappingUnchangedIsNoop is the negative case: identical
// mappings (even if grouped differently across MAPPING FOR entries in
// source vs the flattened snapshot) must not produce a spurious ALTER on
// every plan.
func TestDiffTSConfigMappingUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := snapTSConfig("public", "demo_search", []snapshot.SnapTSMapping{
		{TokenTypes: []string{"word", "hword"}, Dictionaries: []string{"english_stem"}},
	})
	desired := []pipeline.IRObject{
		&ir.TSConfig{
			Schema: "public", Name: "demo_search",
			ParserSchema: "pg_catalog", ParserName: "default",
			Reconstructed: true,
			Mappings: []pipeline.TSMappingDef{
				// Declared as two separate entries covering the same token
				// types with the same dictionary — must flatten identically.
				{TokenTypes: []string{"word"}, Dictionaries: []pipeline.Identifier{{Name: "english_stem"}}},
				{TokenTypes: []string{"hword"}, Dictionaries: []pipeline.Identifier{{Name: "english_stem"}}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "MAPPING") {
		t.Errorf("expected no MAPPING ops for unchanged mappings, got: %v", sqlList(ops))
	}
}

// TestDiffTSDictRenamedFromEmitsAlterRename is the regression guard for
// TSDict rename detection: before this, ir.TSDict had no RenamedFrom field
// at all, so a renamed dictionary was indistinguishable from "old one
// dropped, new one added."
func TestDiffTSDictRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Body text differs by name on each side — matching what the real
	// compiler actually produces for each declaration — not a shared/
	// identical string, which would mask the exact bug this test guards
	// against (opaqueBodyForHash normalizing the name back out before
	// hashing).
	_ = snap.SetObject("public.ispell_old", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "ispell_old",
			BodyHash: hashText("CREATE TEXT SEARCH DICTIONARY public.ispell_old (TEMPLATE = ispell)"),
		},
	})
	old := "ispell_old"
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "public", Name: "ispell", RenamedFrom: &old,
			Body: "CREATE TEXT SEARCH DICTIONARY public.ispell (TEMPLATE = ispell)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TEXT SEARCH DICTIONARY") {
		t.Fatalf("renamed dictionary should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER TEXT SEARCH DICTIONARY "public"."ispell_old" RENAME TO "ispell";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER TEXT SEARCH DICTIONARY RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffTSDictOwnerChanged is the regression guard for Section 12.2's
// OWNER TO capability: previously ir.TSDict had no Owner field at all.
// TestDiffTSConfigOwnerChanged guards RFC audit item #33: ir.TSConfig had
// no Owner field at all, despite real PostgreSQL supporting ALTER TEXT
// SEARCH CONFIGURATION ... OWNER TO (confirmed live, same as TSDict).
func TestDiffTSConfigOwnerChanged(t *testing.T) {
	d := New()
	body := `CREATE TEXT SEARCH CONFIGURATION "public"."my_cfg" (PARSER = "pg_catalog"."default")`
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_cfg", &snapshot.SnapObject{
		Kind: "ts_config",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_config", Schema: "public", Name: "my_cfg",
			BodyHash: hashText(body),
		},
	})
	owner := "search_admin"
	desired := []pipeline.IRObject{
		&ir.TSConfig{Schema: "public", Name: "my_cfg", Body: body, Owner: &owner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH CONFIGURATION "public"."my_cfg" OWNER TO "search_admin";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP TEXT SEARCH CONFIGURATION") {
		t.Errorf("owner-only change should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestCreateTSConfigWithOwner proves a brand-new text search configuration
// with a declared owner applies it at creation time.
func TestCreateTSConfigWithOwner(t *testing.T) {
	d := New()
	owner := "search_admin"
	body := `CREATE TEXT SEARCH CONFIGURATION "public"."my_cfg" (PARSER = "pg_catalog"."default")`
	desired := []pipeline.IRObject{
		&ir.TSConfig{Schema: "public", Name: "my_cfg", Body: body, Owner: &owner},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE TEXT SEARCH CONFIGURATION") {
		t.Fatalf("expected a CREATE TEXT SEARCH CONFIGURATION op, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `SET ROLE "search_admin";`) {
		t.Errorf("expected owner to be applied at creation via SET ROLE/RESET ROLE, got: %v", sqlList(ops))
	}
}

// TestDiffTSDictOptionsAddedChangedRemoved guards RFC audit item #52:
// diffTSDict previously relied entirely on the raw opaque BodyHash for
// option changes, which either forced an unnecessary DESTRUCTIVE
// drop+recreate for a hand-written-source-only option edit, or detected
// nothing at all for a live-catalog-only one (BodyHash is skipped for a
// Reconstructed body). A changed option set must now use a targeted SAFE
// ALTER TEXT SEARCH DICTIONARY (...).
func TestDiffTSDictOptionsAddedChangedRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.ispell", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "ispell",
			TSDictOptionsStructured: true,
			TSDictTemplateName:      "ispell",
			TSDictOptions:           []snapshot.SnapOptionKV{{Key: "language", Value: "english"}, {Key: "stopwords", Value: "english"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "public", Name: "ispell", TemplateName: "ispell",
			Body:    `CREATE TEXT SEARCH DICTIONARY public.ispell (TEMPLATE = ispell, LANGUAGE = 'french', DICTFILE = 'french')`,
			Options: []pipeline.StorageParam{{Key: "language", Value: "french"}, {Key: "dictfile", Value: "french"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH DICTIONARY "public"."ispell" (language = 'french', dictfile = 'french', stopwords);`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP TEXT SEARCH DICTIONARY") || containsSQL(ops, "CREATE TEXT SEARCH DICTIONARY") {
		t.Errorf("an option-only change must not drop+recreate, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if op.Safety() != pipeline.Safe {
			t.Errorf("expected Safe safety for an option-only change, got [%s] %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffTSDictOptionsUnchangedIsNoop proves an unchanged option set
// produces no ALTER op at all.
func TestDiffTSDictOptionsUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.ispell", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "ispell",
			TSDictOptionsStructured: true,
			TSDictTemplateName:      "ispell",
			TSDictOptions:           []snapshot.SnapOptionKV{{Key: "language", Value: "english"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "public", Name: "ispell", TemplateName: "ispell",
			Body:    `CREATE TEXT SEARCH DICTIONARY public.ispell (TEMPLATE = ispell, LANGUAGE = 'english')`,
			Options: []pipeline.StorageParam{{Key: "language", Value: "english"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged option set, got: %v", sqlList(ops))
	}
}

// TestDiffTSDictTemplateChangedStillDropsAndRecreates proves the boundary
// of #52's fix: real PostgreSQL has no ALTER for TEMPLATE at all (confirmed
// live), so a template change must still be a DESTRUCTIVE drop+recreate
// even with structured option data available.
func TestDiffTSDictTemplateChangedStillDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_dict", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "my_dict",
			TSDictOptionsStructured: true,
			TSDictTemplateName:      "simple",
		},
	})
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "public", Name: "my_dict", TemplateName: "snowball",
			Body:    `CREATE TEXT SEARCH DICTIONARY public.my_dict (TEMPLATE = snowball, LANGUAGE = 'english')`,
			Options: []pipeline.StorageParam{{Key: "language", Value: "english"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TEXT SEARCH DICTIONARY IF EXISTS "public"."my_dict";`) {
		t.Errorf("expected a DROP for a TEMPLATE change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE TEXT SEARCH DICTIONARY") {
		t.Errorf("expected a CREATE for a TEMPLATE change, got: %v", sqlList(ops))
	}
}

// TestDiffTSDictTemplateUnqualifiedMatchesQualifiedSnapshot guards the
// deliberate name-only TEMPLATE comparison (SnapOpaque.TSDictTemplateName's
// doc comment): an unqualified built-in TEMPLATE reference in source
// (resolving via search_path to pg_catalog, confirmed live) must not
// misdetect as changed against introspection's always-schema-qualified
// name.
func TestDiffTSDictTemplateUnqualifiedMatchesQualifiedSnapshot(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.my_dict", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "my_dict",
			TSDictOptionsStructured: true,
			TSDictTemplateName:      "snowball",
		},
	})
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "public", Name: "my_dict", TemplateName: "snowball", // unqualified in source
			Body:    `CREATE TEXT SEARCH DICTIONARY public.my_dict (TEMPLATE = snowball)`,
			Options: nil,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops (template unchanged), got: %v", sqlList(ops))
	}
}

func TestDiffTSDictOwnerChanged(t *testing.T) {
	d := New()
	body := "CREATE TEXT SEARCH DICTIONARY public.ispell (TEMPLATE = ispell)"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.ispell", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "ispell",
			BodyHash: hashText(body),
		},
	})
	owner := "search_admin"
	desired := []pipeline.IRObject{
		&ir.TSDict{Schema: "public", Name: "ispell", Body: body, Owner: &owner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH DICTIONARY "public"."ispell" OWNER TO "search_admin";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP TEXT SEARCH DICTIONARY") {
		t.Errorf("owner-only change should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestDiffTSDictRenamedFromAndBodyChanged confirms a simultaneous rename +
// TEMPLATE/option change still requires DROP+CREATE, with no redundant
// separate rename — createOpaque already creates under the final (new)
// name.
func TestDiffTSDictRenamedFromAndBodyChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.ispell_old", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "public", Name: "ispell_old",
			BodyHash: hashText("CREATE TEXT SEARCH DICTIONARY public.ispell_old (TEMPLATE = ispell)"),
		},
	})
	old := "ispell_old"
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "public", Name: "ispell", RenamedFrom: &old,
			Body: "CREATE TEXT SEARCH DICTIONARY public.ispell (TEMPLATE = snowball)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TEXT SEARCH DICTIONARY IF EXISTS "public"."ispell_old";`) {
		t.Errorf("expected DROP referencing the dictionary's actual (old) name, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a separate RENAME TO alongside drop+create, got: %v", sqlList(ops))
		}
	}
}

// TestDiffTSParserRenamedFromEmitsAlterRename is TSDict's TSParser
// counterpart.
func TestDiffTSParserRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Body text differs by name on each side, matching real compiler
	// output — see TestDiffTSDictRenamedFromEmitsAlterRename's identical
	// note.
	oldBody := "CREATE TEXT SEARCH PARSER public.prs_old (START = prsd_start, GETTOKEN = prsd_nexttoken, END = prsd_end, LEXTYPES = prsd_lextype)"
	newBody := "CREATE TEXT SEARCH PARSER public.prs (START = prsd_start, GETTOKEN = prsd_nexttoken, END = prsd_end, LEXTYPES = prsd_lextype)"
	_ = snap.SetObject("public.prs_old", &snapshot.SnapObject{
		Kind: "ts_parser",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_parser", Schema: "public", Name: "prs_old", BodyHash: hashText(oldBody),
		},
	})
	old := "prs_old"
	desired := []pipeline.IRObject{
		&ir.TSParser{Schema: "public", Name: "prs", RenamedFrom: &old, Body: newBody},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TEXT SEARCH PARSER") {
		t.Fatalf("renamed parser should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER TEXT SEARCH PARSER "public"."prs_old" RENAME TO "prs";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSTemplateRenamedFromEmitsAlterRename is TSDict's TSTemplate
// counterpart.
func TestDiffTSTemplateRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Body text differs by name on each side, matching real compiler
	// output — see TestDiffTSDictRenamedFromEmitsAlterRename's identical
	// note.
	oldBody := "CREATE TEXT SEARCH TEMPLATE public.tmpl_old (LEXIZE = dsimple_lexize)"
	newBody := "CREATE TEXT SEARCH TEMPLATE public.tmpl (LEXIZE = dsimple_lexize)"
	_ = snap.SetObject("public.tmpl_old", &snapshot.SnapObject{
		Kind: "ts_template",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_template", Schema: "public", Name: "tmpl_old", BodyHash: hashText(oldBody),
		},
	})
	old := "tmpl_old"
	desired := []pipeline.IRObject{
		&ir.TSTemplate{Schema: "public", Name: "tmpl", RenamedFrom: &old, Body: newBody},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TEXT SEARCH TEMPLATE") {
		t.Fatalf("renamed template should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER TEXT SEARCH TEMPLATE "public"."tmpl_old" RENAME TO "tmpl";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSDictRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for TSDict.
func TestDiffTSDictRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.ispell", &snapshot.SnapObject{
		Kind: "ts_dict",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_dict", Schema: "old_schema", Name: "ispell",
			BodyHash: hashText("CREATE TEXT SEARCH DICTIONARY old_schema.ispell (TEMPLATE = ispell)"),
		},
	})
	old := "ispell"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.TSDict{
			Schema: "new_schema", Name: "ispell", RenamedFrom: &old, RenamedFromSchema: &oldSchema,
			Body: "CREATE TEXT SEARCH DICTIONARY new_schema.ispell (TEMPLATE = ispell)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TEXT SEARCH DICTIONARY") {
		t.Fatalf("cross-schema rename should match the existing dictionary, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER TEXT SEARCH DICTIONARY "old_schema"."ispell" SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSParserRenamedFromCrossSchema is TSDict's TSParser counterpart.
func TestDiffTSParserRenamedFromCrossSchema(t *testing.T) {
	d := New()
	body := "CREATE TEXT SEARCH PARSER old_schema.prs (START = prsd_start, GETTOKEN = prsd_nexttoken, END = prsd_end, LEXTYPES = prsd_lextype)"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.prs", &snapshot.SnapObject{
		Kind: "ts_parser",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_parser", Schema: "old_schema", Name: "prs", BodyHash: hashText(body),
		},
	})
	old := "prs"
	oldSchema := "old_schema"
	newBody := "CREATE TEXT SEARCH PARSER new_schema.prs (START = prsd_start, GETTOKEN = prsd_nexttoken, END = prsd_end, LEXTYPES = prsd_lextype)"
	desired := []pipeline.IRObject{
		&ir.TSParser{Schema: "new_schema", Name: "prs", RenamedFrom: &old, RenamedFromSchema: &oldSchema, Body: newBody},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH PARSER "old_schema"."prs" SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSTemplateRenamedFromCrossSchema is TSDict's TSTemplate
// counterpart.
func TestDiffTSTemplateRenamedFromCrossSchema(t *testing.T) {
	d := New()
	body := "CREATE TEXT SEARCH TEMPLATE old_schema.tmpl (LEXIZE = dsimple_lexize)"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.tmpl", &snapshot.SnapObject{
		Kind: "ts_template",
		Opaque: &snapshot.SnapOpaque{
			Kind: "ts_template", Schema: "old_schema", Name: "tmpl", BodyHash: hashText(body),
		},
	})
	old := "tmpl"
	oldSchema := "old_schema"
	newBody := "CREATE TEXT SEARCH TEMPLATE new_schema.tmpl (LEXIZE = dsimple_lexize)"
	desired := []pipeline.IRObject{
		&ir.TSTemplate{Schema: "new_schema", Name: "tmpl", RenamedFrom: &old, RenamedFromSchema: &oldSchema, Body: newBody},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TEXT SEARCH TEMPLATE "old_schema"."tmpl" SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTSDictRenamedFromStaleErrors mirrors every other kind's identical
// stale-RENAMED-FROM validation.
func TestDiffTSDictRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_dict"
	desired := []pipeline.IRObject{
		&ir.TSDict{Schema: "public", Name: "ispell", RenamedFrom: &old, Body: "CREATE TEXT SEARCH DICTIONARY public.ispell (TEMPLATE = ispell)"},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot dictionary")
	}
}

// TestDiffOperatorOverloadOnlyEditedOneChanges proves the QualifiedName
// widening (see ir.Operator.QualifiedName) doesn't just avoid a collision at
// rest — it also keeps two overloaded operators (same symbol, different
// operand types) independently diffable: editing one's body must produce ops
// for that one only, leaving the untouched overload alone.
func TestDiffOperatorOverloadOnlyEditedOneChanges(t *testing.T) {
	d := New()
	intType := ir.TypeRef{Name: "integer"}
	numType := ir.TypeRef{Name: "numeric"}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Operator{Schema: "public", Name: "+", LeftType: &intType, RightType: &intType,
			Body: "CREATE OPERATOR public.+ (FUNCTION = int4pl, LEFTARG = integer, RIGHTARG = integer)"},
		&ir.Operator{Schema: "public", Name: "+", LeftType: &numType, RightType: &numType,
			Body: "CREATE OPERATOR public.+ (FUNCTION = numeric_add, LEFTARG = numeric, RIGHTARG = numeric)"},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Operator{Schema: "public", Name: "+", LeftType: &intType, RightType: &intType,
			Body: "CREATE OPERATOR public.+ (FUNCTION = int4pl_v2, LEFTARG = integer, RIGHTARG = integer)"},
		&ir.Operator{Schema: "public", Name: "+", LeftType: &numType, RightType: &numType,
			Body: "CREATE OPERATOR public.+ (FUNCTION = numeric_add, LEFTARG = numeric, RIGHTARG = numeric)"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("want 2 ops (DROP+CREATE for the edited integer overload only), got %d: %v", len(ops), sqlList(ops))
	}
	if !containsSQL(ops, `DROP OPERATOR IF EXISTS "public".+(integer, integer);`) {
		t.Errorf("expected DROP for the edited integer overload, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "int4pl_v2") {
		t.Errorf("expected CREATE with the new function, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "numeric_add") {
		t.Errorf("untouched numeric overload must not be touched, got: %v", sqlList(ops))
	}
}

// ── Operator optimizer-hint diffing (RFC Section 14.3) ─────────────────────
//
// Operator was the one opaque kind with no structured fields for its
// optimizer hints (RESTRICT/JOIN/COMMUTATOR/NEGATOR/HASHES/MERGES), so any
// change to them — even hint-only, function/operand types unchanged — always
// forced the generic whole-body-hash DROP+CREATE. These tests guard the new
// diffOperator: a hint-only change must route through a real, SAFE
// ALTER OPERATOR ... SET (...) (confirmed live against PostgreSQL 17), while
// a change to an already-set COMMUTATOR/NEGATOR/HASHES/MERGES value (which
// real PostgreSQL flatly rejects via ALTER) still falls back to DESTRUCTIVE
// DROP+CREATE.

func baseOperatorSnap(restrict, join *string, commutator, negator *string, hashes, merges bool) *pipeline.Snapshot {
	intType := ir.TypeRef{Name: "integer"}
	snap := &pipeline.Snapshot{}
	_ = snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Restrict: restrict, Join: join, Commutator: commutator, Negator: negator,
			Hashes: hashes, Merges: merges,
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	})
	return snap
}

// TestDiffOperatorHintOnlyChangeIsSafe guards the core bug: a RESTRICT/JOIN
// change with the function/operand types unchanged must be a SAFE, targeted
// ALTER OPERATOR ... SET (...), not a DROP+CREATE.
func TestDiffOperatorHintOnlyChangeIsSafe(t *testing.T) {
	d := New()
	oldRestrict := "scalarltsel"
	snap := baseOperatorSnap(&oldRestrict, nil, nil, nil, false, false)

	intType := ir.TypeRef{Name: "integer"}
	newRestrict, newJoin := "scalargtsel", "scalargtjoinsel"
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Restrict: &newRestrict, Join: &newJoin,
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP OPERATOR") || strings.Contains(o.SQL(), "CREATE OPERATOR") {
			t.Errorf("hint-only change must not drop/recreate the operator, got: %s", o.SQL())
		}
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly one ALTER OPERATOR SET op, got %d: %v", len(ops), sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "RESTRICT = scalargtsel") || !strings.Contains(ops[0].SQL(), "JOIN = scalargtjoinsel") {
		t.Errorf("unexpected SQL: %s", ops[0].SQL())
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

// TestDiffOperatorRestrictClearedEmitsNone guards RESTRICT/JOIN's clearing
// path — real PostgreSQL's ALTER OPERATOR ... SET (RESTRICT = NONE) is the
// documented way to remove an already-set estimator function.
func TestDiffOperatorRestrictClearedEmitsNone(t *testing.T) {
	d := New()
	oldRestrict := "scalarltsel"
	snap := baseOperatorSnap(&oldRestrict, nil, nil, nil, false, false)

	intType := ir.TypeRef{Name: "integer"}
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", // Restrict left nil: cleared
			Body:     "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || !strings.Contains(ops[0].SQL(), "RESTRICT = NONE") {
		t.Fatalf("expected SET (RESTRICT = NONE), got: %v", sqlList(ops))
	}
}

// TestDiffOperatorCommutatorUnsetToSetIsSafe guards the unset->set direction
// for COMMUTATOR — the one direction real PostgreSQL's ALTER OPERATOR SET
// actually supports for this property.
func TestDiffOperatorCommutatorUnsetToSetIsSafe(t *testing.T) {
	d := New()
	snap := baseOperatorSnap(nil, nil, nil, nil, false, false) // no commutator yet

	intType := ir.TypeRef{Name: "integer"}
	commutator := "public.==!="
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Commutator: &commutator,
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly one ALTER OPERATOR SET op, got %d: %v", len(ops), sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), `COMMUTATOR = OPERATOR("public".==!=)`) {
		t.Errorf("expected qualified OPERATOR(...) COMMUTATOR reference, got: %s", ops[0].SQL())
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

// TestDiffOperatorCommutatorChangedIsDestructive guards the other direction:
// real PostgreSQL rejects ALTER OPERATOR SET (COMMUTATOR = ...) once already
// set ("operator attribute ... cannot be changed if it has already been
// set"), confirmed live, so changing an already-set COMMUTATOR must still
// fall back to DROP+CREATE.
func TestDiffOperatorCommutatorChangedIsDestructive(t *testing.T) {
	d := New()
	oldCommutator := "public.==!="
	snap := baseOperatorSnap(nil, nil, &oldCommutator, nil, false, false)

	intType := ir.TypeRef{Name: "integer"}
	newCommutator := "public.==!!="
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Commutator: &newCommutator,
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP OPERATOR IF EXISTS "public".===(integer, integer);`) {
		t.Errorf("expected DROP OPERATOR, got: %v", sqlList(ops))
	}
	var dropOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP OPERATOR") {
			dropOp = o
		}
	}
	if dropOp == nil || dropOp.Safety() != pipeline.Destructive {
		t.Errorf("expected the DROP op to be Destructive, got ops: %v", sqlList(ops))
	}
}

// TestDiffOperatorHashesUnsetToSetIsSafe/TestDiffOperatorMergesClearedIsDestructive
// cover HASHES/MERGES' identical one-directional asymmetry.
func TestDiffOperatorHashesUnsetToSetIsSafe(t *testing.T) {
	d := New()
	snap := baseOperatorSnap(nil, nil, nil, nil, false, false)

	intType := ir.TypeRef{Name: "integer"}
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Hashes: true,
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || !strings.Contains(ops[0].SQL(), "HASHES") || strings.Contains(ops[0].SQL(), "DROP") {
		t.Fatalf("expected a SAFE SET (HASHES) op, got: %v", sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe, got %s", ops[0].Safety())
	}
}

func TestDiffOperatorMergesClearedIsDestructive(t *testing.T) {
	d := New()
	snap := baseOperatorSnap(nil, nil, nil, nil, false, true) // MERGES already set

	intType := ir.TypeRef{Name: "integer"}
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Merges: false, // cleared
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP OPERATOR IF EXISTS "public".===(integer, integer);`) {
		t.Errorf("expected DROP OPERATOR for a MERGES clear, got: %v", sqlList(ops))
	}
}

// TestDiffOperatorUnchangedIsNoop guards against diffOperator firing
// spuriously when nothing about the operator (hints included) changed.
func TestDiffOperatorUnchangedIsNoop(t *testing.T) {
	d := New()
	restrict := "scalarltsel"
	commutator := "public.==!="
	snap := baseOperatorSnap(&restrict, nil, &commutator, nil, true, false)

	intType := ir.TypeRef{Name: "integer"}
	desired := []pipeline.IRObject{
		&ir.Operator{
			Schema: "public", Name: "===", LeftType: &intType, RightType: &intType,
			Function: "my_lt", Restrict: &restrict, Commutator: &commutator, Hashes: true,
			Body: "CREATE OPERATOR public.=== (FUNCTION = my_lt, LEFTARG = integer, RIGHTARG = integer)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no-op, got: %v", sqlList(ops))
	}
}

// VERIFY: desired side is the introspected reconstruction (Reconstructed=true),
// snap side is the stored source hash. Must NOT report spurious drift despite
// different-but-equivalent spelling.
func TestDiffCollationVerifyNoSpuriousDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Stored snapshot from source apply (has a hash).
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION c (LC_COLLATE = 'C', LC_CTYPE = 'C')",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Introspected desired side: canonical reconstruction, marked Reconstructed.
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", Reconstructed: true,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("verify: spurious drift for reconstructed collation: %d ops: %v", len(ops), ops)
	}
}

// PLAN --LIVE: desired side is source, snap side is the introspected baseline
// (Reconstructed=true → no hash). Must NOT report spurious drift.
func TestDiffCollationPlanLiveNoSpuriousDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	// Live baseline built from introspection (Reconstructed → BodyHash omitted).
	if err := snapshot.Populate(snap, []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", Reconstructed: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION c (LC_COLLATE = 'C', LC_CTYPE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("plan --live: spurious drift vs reconstructed baseline: %d ops: %v", len(ops), ops)
	}
}

// createIndex must emit the partial-index WHERE predicate and INCLUDE columns;
// omitting them silently creates an index that differs from the declared one.
func TestDiffCreateIndexWhereAndInclude(t *testing.T) {
	d := New()
	where := "active"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "int4"}}, {Name: "active", Type: ir.TypeRef{Name: "bool"}}},
			Indexes: []*ir.Index{
				{Name: "t_a_idx", Columns: []pipeline.IndexColumn{{Name: "a"}}, Include: []string{"active"}, Where: &where},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE") && strings.Contains(o.SQL(), "INDEX") {
			sql = o.SQL()
			break
		}
	}
	if sql == "" {
		t.Fatalf("no CREATE INDEX op: %v", sqlList(ops))
	}
	if !strings.Contains(sql, "INCLUDE (") || !strings.Contains(sql, `"active"`) {
		t.Errorf("missing INCLUDE clause: %s", sql)
	}
	if !strings.Contains(sql, "WHERE active") {
		t.Errorf("missing WHERE predicate: %s", sql)
	}
}

// TestDiffCreateIndexNullsNotDistinctAndWith is the regression guard for the
// S-tier index gap: WITH storage params and NULLS NOT DISTINCT were parsed
// into ir.Index (idx.With already existed, unused downstream) but createIndex
// silently dropped them from the emitted CREATE INDEX SQL — lossless-by-
// omission, same class of gap INCLUDE/WHERE used to have before they were
// wired up.
func TestDiffCreateIndexNullsNotDistinctAndWith(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "a", Type: ir.TypeRef{Name: "int4"}}},
			Indexes: []*ir.Index{
				{
					Name:             "t_a_idx",
					Unique:           true,
					Columns:          []pipeline.IndexColumn{{Name: "a"}},
					NullsNotDistinct: true,
					With:             []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}},
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, o := range ops {
		if strings.Contains(o.SQL(), "CREATE") && strings.Contains(o.SQL(), "INDEX") {
			sql = o.SQL()
			break
		}
	}
	if sql == "" {
		t.Fatalf("no CREATE INDEX op: %v", sqlList(ops))
	}
	if !strings.Contains(sql, "NULLS NOT DISTINCT") {
		t.Errorf("missing NULLS NOT DISTINCT: %s", sql)
	}
	if !strings.Contains(sql, "WITH (fillfactor=70)") {
		t.Errorf("missing WITH clause: %s", sql)
	}
	// Grammar order: ... columns ... NULLS NOT DISTINCT ... WITH ( ... ) ...
	if strings.Index(sql, "NULLS NOT DISTINCT") > strings.Index(sql, "WITH (") {
		t.Errorf("NULLS NOT DISTINCT must precede WITH per PG grammar: %s", sql)
	}
}

// baseFullIndex returns a fully-populated index exercising every property
// diffIndexes must now compare, used as the common starting point for the
// content-comparison regression guards below.
func baseFullIndex() *ir.Index {
	where := "a > 0"
	return &ir.Index{
		Name:    "t_idx",
		Unique:  true,
		Method:  "btree",
		Columns: []pipeline.IndexColumn{{Name: "a", SortOrder: "ASC"}, {Name: "b"}},
		Where:   &where,
		Include: []string{"c"},
		With:    []pipeline.StorageParam{{Key: "fillfactor", Value: "70"}},
	}
}

func baseFullIndexTable(idx *ir.Index) *ir.Table {
	return &ir.Table{
		Schema: "public",
		Name:   "t",
		Columns: []*ir.Column{
			{Name: "a", Type: ir.TypeRef{Name: "int4"}},
			{Name: "b", Type: ir.TypeRef{Name: "int4"}},
			{Name: "c", Type: ir.TypeRef{Name: "int4"}},
		},
		Indexes: []*ir.Index{idx},
	}
}

// TestDiffIndexUnchangedNoOps is the baseline for the diffIndexes content-
// comparison fix: a same-named index whose definition is byte-for-byte
// identical (every property diffIndexes now compares) must still produce zero
// ops, not a spurious recreate.
// TestDiffIndexRenamedFromEmitsAlterRename is the regression guard for index
// rename detection: before this, RENAMED FROM had no field to carry the old
// name at all, so a renamed index was indistinguishable from "old index
// dropped, new one added" — a DROP INDEX + CREATE INDEX pair instead of a
// metadata-only rename.
func TestDiffIndexRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "users",
			Columns: []snapshot.SnapColumn{{Name: "email", Type: "text"}},
			Indexes: []snapshot.SnapIndex{
				{Name: "users_email_idx_old", Method: "btree", Columns: "email"},
			},
		},
	})
	old := "users_email_idx_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "btree", Columns: []pipeline.IndexColumn{{Name: "email"}}, RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP INDEX") || containsSQL(ops, "CREATE INDEX") {
		t.Fatalf("renamed index should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER INDEX "users_email_idx_old" RENAME TO "users_email_idx";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Safe {
			t.Errorf("expected SAFE safety for ALTER INDEX RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffIndexRenamedFromAndContentChanged confirms a simultaneous rename +
// definition change (unlike Constraint, an Index's full definition IS
// compared even when matched — see TestDiffIndexContentChangeRecreates)
// still requires DROP+CREATE, but drops under the index's CURRENT (old,
// matched-snapshot) name — not the desired new name, which doesn't exist
// live yet.
func TestDiffIndexRenamedFromAndContentChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "users",
			Columns: []snapshot.SnapColumn{{Name: "email", Type: "text"}},
			Indexes: []snapshot.SnapIndex{
				{Name: "users_email_idx_old", Method: "btree", Columns: "email"},
			},
		},
	})
	old := "users_email_idx_old"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "gin", Columns: []pipeline.IndexColumn{{Name: "email"}}, RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP INDEX IF EXISTS "users_email_idx_old";`) {
		t.Errorf("expected DROP INDEX referencing the index's actual (old) name, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `CREATE INDEX "users_email_idx"`) {
		t.Errorf("expected CREATE INDEX for the new definition under the new name, got: %v", sqlList(ops))
	}
}

// TestDiffIndexRenamedFromStaleErrors mirrors diffCompositeAttrs/
// diffPartitionList/diffConstraints' identical stale-RENAMED-FROM
// validation: neither the old nor new name exists in the snapshot.
func TestDiffIndexRenamedFromStaleErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.users", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "users",
			Columns: []snapshot.SnapColumn{{Name: "email", Type: "text"}},
		},
	})
	old := "nonexistent_old_idx"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "users",
			Columns: []*ir.Column{{Name: "email", Type: ir.TypeRef{Name: "text"}}},
			Indexes: []*ir.Index{
				{Name: "users_email_idx", Method: "btree", Columns: []pipeline.IndexColumn{{Name: "email"}}, RenamedFrom: &old},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot index")
	}
}

// TestDiffMaterializedViewIndexRenamedFrom is
// TestDiffIndexRenamedFromEmitsAlterRename's materialized-view counterpart
// (diffViewIndexes, a separate function from diffIndexes).
func TestDiffMaterializedViewIndexRenamedFrom(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.mv_summary", &snapshot.SnapObject{
		Kind: "view",
		View: &snapshot.SnapView{
			Schema: "public",
			Name:   "mv_summary",
			Query:  "SELECT 1",
			Indexes: []snapshot.SnapIndex{
				{Name: "mv_summary_idx_old", Method: "btree", Columns: "id"},
			},
		},
	})
	old := "mv_summary_idx_old"
	desired := []pipeline.IRObject{
		&ir.View{
			Schema:       "public",
			Name:         "mv_summary",
			Materialized: true,
			Query:        "SELECT 1",
			Indexes: []*ir.Index{
				{Name: "mv_summary_idx", Method: "btree", Columns: []pipeline.IndexColumn{{Name: "id"}}, RenamedFrom: &old},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP INDEX") || containsSQL(ops, "CREATE INDEX") {
		t.Fatalf("renamed matview index should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER INDEX "mv_summary_idx_old" RENAME TO "mv_summary_idx";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

func TestDiffIndexUnchangedNoOps(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{baseFullIndexTable(baseFullIndex())}); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Diff([]pipeline.IRObject{baseFullIndexTable(baseFullIndex())}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("want 0 ops for an unchanged index, got %d: %v", len(ops), sqlList(ops))
	}
}

// TestDiffIndexContentChangeRecreates is the regression guard for the
// diffIndexes name-only-matching bug: previously a same-named index was
// matched purely by name, with zero comparison of its actual definition, so
// editing ANY property (method, uniqueness, columns, sort order/NULLS
// placement, WHERE, INCLUDE, WITH, or NULLS NOT DISTINCT) while keeping the
// name was a silent no-op on plan/apply. Each case here mutates exactly one
// property off the common baseFullIndex baseline and must now produce a
// structured DROP INDEX + CREATE INDEX pair reflecting the new definition.
func TestDiffIndexContentChangeRecreates(t *testing.T) {
	where2 := "a > 100"
	cases := []struct {
		name          string
		mutate        func(*ir.Index)
		wantInCreated string
	}{
		{"method", func(idx *ir.Index) { idx.Method = "gin" }, "USING gin"},
		{"unique", func(idx *ir.Index) { idx.Unique = false }, `CREATE INDEX "t_idx"`},
		{"columns_added", func(idx *ir.Index) {
			idx.Columns = append(idx.Columns, pipeline.IndexColumn{Name: "c"})
		}, `"c"`},
		{"sort_order", func(idx *ir.Index) { idx.Columns[0].SortOrder = "DESC" }, `"a" DESC`},
		{"where", func(idx *ir.Index) { idx.Where = &where2 }, "WHERE a > 100"},
		{"include", func(idx *ir.Index) { idx.Include = []string{"b"} }, `INCLUDE ("b")`},
		{"with", func(idx *ir.Index) {
			idx.With = []pipeline.StorageParam{{Key: "fillfactor", Value: "50"}}
		}, "WITH (fillfactor=50)"},
		{"nulls_not_distinct", func(idx *ir.Index) { idx.NullsNotDistinct = true }, "NULLS NOT DISTINCT"},
		{"opclass", func(idx *ir.Index) {
			idx.Columns[0].OpClass = &pipeline.Identifier{Name: "int4_ops"}
		}, `"a" int4_ops`},
		{"collation", func(idx *ir.Index) {
			idx.Columns[0].Collation = &pipeline.Identifier{Name: "C"}
		}, `"a" COLLATE "C"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New()
			snap := &pipeline.Snapshot{}
			if err := snapshot.Populate(snap, []pipeline.IRObject{baseFullIndexTable(baseFullIndex())}); err != nil {
				t.Fatal(err)
			}
			desiredIdx := baseFullIndex()
			tc.mutate(desiredIdx)
			ops, err := d.Diff([]pipeline.IRObject{baseFullIndexTable(desiredIdx)}, snap)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var dropSQL, createSQL string
			for _, o := range ops {
				sql := o.SQL()
				if strings.HasPrefix(sql, "DROP INDEX") {
					dropSQL = sql
				} else if strings.Contains(sql, "CREATE") && strings.Contains(sql, "INDEX") {
					createSQL = sql
				}
			}
			if dropSQL == "" || createSQL == "" {
				t.Fatalf("want DROP INDEX + CREATE INDEX ops, got %d ops: %v", len(ops), sqlList(ops))
			}
			if !strings.Contains(dropSQL, `DROP INDEX IF EXISTS "t_idx";`) {
				t.Errorf("unexpected DROP: %s", dropSQL)
			}
			if !strings.Contains(createSQL, tc.wantInCreated) {
				t.Errorf("recreated index missing %q: %s", tc.wantInCreated, createSQL)
			}
			for _, o := range ops {
				if o.Safety() != pipeline.Caution {
					t.Errorf("index recreate op safety = %v, want Caution: %s", o.Safety(), o.SQL())
				}
			}
		})
	}
}

// TestDiffIndexCascadeSuppressedWithSortSuffix guards translateIndexCols's
// suffix-stripping fix: SnapIndex.Columns can now carry an " ASC"/" DESC"/
// " NULLS FIRST"/" NULLS LAST" suffix (snapshot.ToSnapIndex), which
// translateIndexCols must strip before checking whether a dropped column
// takes the index with it — otherwise it would look for a column literally
// named "a DESC", never match, and emit a redundant standalone DROP INDEX
// alongside the DROP COLUMN cascade that already removes it in PG.
func TestDiffIndexCascadeSuppressedWithSortSuffix(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "a", Type: "int4"}},
			Indexes: []snapshot.SnapIndex{
				{Name: "t_a_idx", Method: "btree", Columns: "a DESC"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", Columns: []*ir.Column{}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP INDEX") {
			t.Errorf("expected no standalone DROP INDEX for a column-dropped index (DESC suffix must not break cascade detection), got: %s", o.SQL())
		}
	}
}

// ── Procedure diff ────────────────────────────────────────────────────────────
// PROCEDURE had zero diff-level test coverage anywhere in the repo (unit or
// live integration) before this — found via a coverage pass on internal/diff.

func TestDiffCreateProcedure(t *testing.T) {
	d := New()
	comment := "recalculates totals"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:   "public",
			Name:     "recalc_totals",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:    ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN NULL; END;"},
			BodyHash: ir.HashBody("BEGIN NULL; END;"),
			Comment:  &comment,
			Grants:   []ir.Grant{{Privileges: []string{"EXECUTE"}, Roles: []string{"app"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE OR REPLACE PROCEDURE") || !containsSQL(ops, `"public"."recalc_totals"`) ||
		!containsSQL(ops, "LANGUAGE plpgsql") {
		t.Errorf("expected CREATE OR REPLACE PROCEDURE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "COMMENT ON PROCEDURE") || !containsSQL(ops, "'recalculates totals'") {
		t.Errorf("expected COMMENT ON PROCEDURE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "GRANT EXECUTE ON PROCEDURE") {
		t.Errorf("expected GRANT EXECUTE ON PROCEDURE, got: %v", sqlList(ops))
	}
}

func TestDiffProcedureUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: hash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:   "public",
			Name:     "recalc_totals",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:    ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash: hash,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged procedure, got: %v", sqlList(ops))
	}
}

// TestDiffCreateProcedureEmitsDeprecatedComment guards RFC audit item #8:
// PROCEDURE { DEPRECATED '...'; } was silently discarded (no field at all)
// — mirrors Function's existing effectiveComment merge into COMMENT ON.
func TestDiffCreateProcedureEmitsDeprecatedComment(t *testing.T) {
	d := New()
	dep := "use recalc_totals_v2 instead"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "public", Name: "recalc_totals",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN NULL; END;"},
			Deprecated: &dep,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "[DEPRECATED] use recalc_totals_v2 instead") {
		t.Errorf("expected COMMENT with [DEPRECATED] marker, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionRenamedFromEmitsAlterRename guards against a previously
// entirely-untested gap: ir.Function.RenamedFrom was consumed by
// renamedFromKey for identity matching (so a rename was correctly matched,
// not DROP+CREATE'd) but diffFunction itself had no rename branch at all —
// the live function was never actually renamed, a silent no-op unlike its
// diffProcedure/diffAggregate siblings (RFC audit items #10/#11).
// TestDiffFunctionOwnerChanged guards RFC audit item #70: Function had no
// Owner field at all.
func TestDiffFunctionOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.calc_total(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "calc_total", Args: "integer",
			ReturnType: "integer", Language: "plpgsql", Volatility: "VOLATILE",
			BodyHash: "samehash", Owner: &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "calc_total",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType: ir.TypeRef{Name: "integer"},
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN NULL; END;"},
			BodyHash:   "samehash", Owner: &newOwner,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER FUNCTION "public"."calc_total"(integer) OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER FUNCTION ... OWNER TO, got: %v", sqlList(ops))
	}
}

func TestDiffFunctionRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.calc_total_old(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "calc_total_old", Args: "integer",
			ReturnType: "integer", Language: "plpgsql", Volatility: "VOLATILE",
			BodyHash: "samehash",
		},
	})
	old := "calc_total_old"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "calc_total",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType:  ir.TypeRef{Name: "integer"},
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN NULL; END;"},
			BodyHash:    "samehash",
			RenamedFrom: &old,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER FUNCTION "public"."calc_total_old"(integer) RENAME TO "calc_total";`) {
		t.Errorf("expected ALTER FUNCTION ... RENAME TO, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP FUNCTION") {
		t.Errorf("rename should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionRenamedFromCrossSchema is
// TestDiffFunctionRenamedFromEmitsAlterRename's cross-schema counterpart —
// see TestDiffRenameTableCrossSchema's identical reasoning.
func TestDiffFunctionRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.calc_total(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "old_schema", Name: "calc_total", Args: "integer",
			ReturnType: "integer", Language: "plpgsql", Volatility: "VOLATILE",
			BodyHash: "samehash",
		},
	})
	old := "calc_total"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "new_schema", Name: "calc_total_v2",
			Args:              []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType:        ir.TypeRef{Name: "integer"},
			Attrs:             ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN NULL; END;"},
			BodyHash:          "samehash",
			RenamedFrom:       &old,
			RenamedFromSchema: &oldSchema,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP FUNCTION") {
		t.Errorf("cross-schema rename should match the existing function, not DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER FUNCTION "old_schema"."calc_total"(integer) SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the function's actual (old) schema, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER FUNCTION "new_schema"."calc_total"(integer) RENAME TO "calc_total_v2";`) {
		t.Errorf("expected RENAME TO after the schema move, got: %v", sqlList(ops))
	}
}

// TestDiffFunctionDependsOnExtension is the regression guard for RFC audit
// item #71: Section 9.1's [NO] DEPENDS ON EXTENSION func-block directive,
// previously zero code anywhere.
func TestDiffFunctionDependsOnExtension(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.f(integer)", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema: "public", Name: "f", Args: "integer",
			ReturnType: "integer", Language: "sql", Volatility: "VOLATILE",
			BodyHash:            "samehash",
			DependsOnExtensions: []string{"old_ext"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			Args:                []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType:          ir.TypeRef{Name: "integer"},
			Attrs:               ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Body: "SELECT 1"},
			BodyHash:            "samehash",
			DependsOnExtensions: []string{"new_ext"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER FUNCTION "public"."f"(integer) DEPENDS ON EXTENSION "new_ext";`) {
		t.Errorf("expected ADD for new_ext, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER FUNCTION "public"."f"(integer) NO DEPENDS ON EXTENSION "old_ext";`) {
		t.Errorf("expected NO DEPENDS for the removed old_ext, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP FUNCTION") {
		t.Errorf("a DEPENDS ON EXTENSION-only change should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestCreateFunctionWithDependsOnExtension proves a brand-new function
// applies its declared extension dependencies at creation time (no inline
// CREATE FUNCTION form exists for it).
func TestCreateFunctionWithDependsOnExtension(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema: "public", Name: "f",
			Args:                []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			ReturnType:          ir.TypeRef{Name: "integer"},
			Attrs:               ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Body: "SELECT 1"},
			DependsOnExtensions: []string{"pgcrypto"},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE OR REPLACE FUNCTION") {
		t.Fatalf("expected a CREATE OR REPLACE FUNCTION op, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER FUNCTION "public"."f"(integer) DEPENDS ON EXTENSION "pgcrypto";`) {
		t.Errorf("expected the extension dependency to be applied at creation, got: %v", sqlList(ops))
	}
}

// TestDiffProcedureOwnerChanged guards RFC audit item #70: Procedure had no
// Owner field at all.
func TestDiffProcedureOwnerChanged(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: hash,
			ProcedureOwner: &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "public", Name: "recalc_totals",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:    ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash: hash, Owner: &newOwner,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER PROCEDURE "public"."recalc_totals"(integer) OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER PROCEDURE ... OWNER TO, got: %v", sqlList(ops))
	}
}

// TestDiffProcedureRenamedFromEmitsAlterRename guards RFC audit item #10: a
// procedure rename was treated as an unrelated DROP+CREATE instead of
// ALTER PROCEDURE ... RENAME TO.
func TestDiffProcedureRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals_old(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals_old", Args: "integer", BodyHash: hash,
		},
	})
	old := "recalc_totals_old"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "public", Name: "recalc_totals",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash:    hash,
			RenamedFrom: &old,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER PROCEDURE "public"."recalc_totals_old"(integer) RENAME TO "recalc_totals";`) {
		t.Errorf("expected ALTER PROCEDURE ... RENAME TO, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP PROCEDURE") {
		t.Errorf("rename should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestDiffProcedureRenamedFromCrossSchema is TestDiffRenameTableCrossSchema's
// Procedure counterpart — also confirms the arg-types signature is still
// built from the old (matched) schema, not the desired one, since a
// procedure's identity key includes "(args)".
func TestDiffProcedureRenamedFromCrossSchema(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.recalc_totals_old(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "old_schema", Name: "recalc_totals_old", Args: "integer", BodyHash: hash,
		},
	})
	old := "recalc_totals_old"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "new_schema", Name: "recalc_totals",
			Args:              []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:             ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash:          hash,
			RenamedFrom:       &old,
			RenamedFromSchema: &oldSchema,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP PROCEDURE") {
		t.Errorf("cross-schema rename should match the existing procedure, not DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER PROCEDURE "old_schema"."recalc_totals_old"(integer) SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the procedure's actual (old) schema, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER PROCEDURE "new_schema"."recalc_totals_old"(integer) RENAME TO "recalc_totals";`) {
		t.Errorf("expected RENAME TO after the schema move, got: %v", sqlList(ops))
	}
}

// TestDiffCreateAggregateEmitsDeprecatedComment and
// TestDiffAggregateRenamedFromEmitsAlterRename are the AGGREGATE
// counterparts (RFC audit items #9/#11).
func TestDiffCreateAggregateEmitsDeprecatedComment(t *testing.T) {
	d := New()
	dep := "use amount_sum_v2 instead"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public", Name: "amount_sum",
			Args:       []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:       "CREATE AGGREGATE public.amount_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)",
			Deprecated: &dep,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "[DEPRECATED] use amount_sum_v2 instead") {
		t.Errorf("expected COMMENT with [DEPRECATED] marker, got: %v", sqlList(ops))
	}
}

// TestDiffAggregateOwnerChanged guards RFC audit item #70: Aggregate had no
// Owner field at all.
// TestDiffAggregateBareFlagOptionChangeRecreates guards RFC audit item #29:
// FINALFUNC_EXTRA/MFINALFUNC_EXTRA/HYPOTHETICAL are bare presence flags —
// before the buildAggregateOptions fix, they never reached o.Options at
// all, so adding/removing one was completely undetected drift here too
// (not merely an unoptimized DROP+CREATE — real PostgreSQL has no in-place
// ALTER for any agg-option).
func TestDiffAggregateBareFlagOptionChangeRecreates(t *testing.T) {
	d := New()
	options := []pipeline.StorageParam{{Key: "sfunc", Value: "numeric_add"}, {Key: "stype", Value: "numeric"}}
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.amount_sum(numeric)", &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "amount_sum", Args: "numeric",
			AggregateOptionsStructured: true,
			AggregateOptions:           toComparableOptions(options, false),
		},
	})
	desiredOptions := append(append([]pipeline.StorageParam{}, options...), pipeline.StorageParam{Key: "finalfunc_extra"})
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public", Name: "amount_sum",
			Args:    []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:    "CREATE AGGREGATE public.amount_sum (numeric) (SFUNC = numeric_add, STYPE = numeric, FINALFUNC_EXTRA)",
			Options: desiredOptions,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP AGGREGATE IF EXISTS "public"."amount_sum"(numeric);`) {
		t.Errorf("expected a bare-flag-only option change to be detected as a DROP+CREATE (bug #29 regressed: undetected drift), got: %v", sqlList(ops))
	}
}

func TestDiffAggregateOwnerChanged(t *testing.T) {
	d := New()
	options := []pipeline.StorageParam{{Key: "SFUNC", Value: "numeric_add"}, {Key: "STYPE", Value: "numeric"}}
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.amount_sum(numeric)", &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "amount_sum", Args: "numeric",
			AggregateOptionsStructured: true,
			AggregateOptions:           toComparableOptions(options, false),
			AggregateOwner:             &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public", Name: "amount_sum",
			Args:    []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:    "CREATE AGGREGATE public.amount_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)",
			Options: options, Owner: &newOwner,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER AGGREGATE "public"."amount_sum"(numeric) OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER AGGREGATE ... OWNER TO, got: %v", sqlList(ops))
	}
}

func TestDiffAggregateRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	options := []pipeline.StorageParam{{Key: "SFUNC", Value: "numeric_add"}, {Key: "STYPE", Value: "numeric"}}
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.amount_sum_old(numeric)", &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "public", Name: "amount_sum_old", Args: "numeric",
			AggregateOptionsStructured: true,
			AggregateOptions:           toComparableOptions(options, false),
		},
	})
	old := "amount_sum_old"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "public", Name: "amount_sum",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:        "CREATE AGGREGATE public.amount_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)",
			Options:     options,
			RenamedFrom: &old,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER AGGREGATE "public"."amount_sum_old"(numeric) RENAME TO "amount_sum";`) {
		t.Errorf("expected ALTER AGGREGATE ... RENAME TO, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP AGGREGATE") {
		t.Errorf("rename should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestDiffAggregateRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for Aggregate.
func TestDiffAggregateRenamedFromCrossSchema(t *testing.T) {
	d := New()
	options := []pipeline.StorageParam{{Key: "SFUNC", Value: "numeric_add"}, {Key: "STYPE", Value: "numeric"}}
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.amount_sum(numeric)", &snapshot.SnapObject{
		Kind: "aggregate",
		Opaque: &snapshot.SnapOpaque{
			Kind: "aggregate", Schema: "old_schema", Name: "amount_sum", Args: "numeric",
			AggregateOptionsStructured: true,
			AggregateOptions:           toComparableOptions(options, false),
		},
	})
	old := "amount_sum"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.Aggregate{
			Schema: "new_schema", Name: "amount_sum",
			Args:              []ir.FuncArg{{Type: ir.TypeRef{Name: "numeric"}}},
			Body:              "CREATE AGGREGATE new_schema.amount_sum (numeric) (SFUNC = numeric_add, STYPE = numeric)",
			Options:           options,
			RenamedFrom:       &old,
			RenamedFromSchema: &oldSchema,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP AGGREGATE") {
		t.Errorf("cross-schema rename should match the existing aggregate, not DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER AGGREGATE "old_schema"."amount_sum"(numeric) SET SCHEMA "new_schema";`) {
		t.Errorf("expected SET SCHEMA referencing the aggregate's actual (old) schema, got: %v", sqlList(ops))
	}
}

// TestDiffProcedureBodyChangedUsesCreateOrReplace proves a changed procedure
// body is re-emitted via CREATE OR REPLACE (unlike aggregates/other opaque
// kinds, which need a DROP first) — PostgreSQL supports CREATE OR REPLACE
// PROCEDURE directly, so no DROP is needed or expected.
func TestDiffProcedureBodyChangedUsesCreateOrReplace(t *testing.T) {
	d := New()
	oldHash := ir.HashBody("BEGIN NULL; END;")
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: oldHash,
		},
	})
	newBody := "BEGIN UPDATE totals SET amount = amount + 1; END;"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:   "public",
			Name:     "recalc_totals",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:    ir.FuncAttrs{Language: "plpgsql", Body: newBody},
			BodyHash: ir.HashBody(newBody),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP PROCEDURE") {
		t.Errorf("unexpected DROP PROCEDURE for a body change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE OR REPLACE PROCEDURE") || !containsSQL(ops, "amount + 1") {
		t.Errorf("expected CREATE OR REPLACE PROCEDURE with new body, got: %v", sqlList(ops))
	}
}

func TestDiffProcedureCommentAdded(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: hash,
		},
	})
	comment := "recalculates totals"
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:   "public",
			Name:     "recalc_totals",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:    ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash: hash,
			Comment:  &comment,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "COMMENT ON PROCEDURE") || !containsSQL(ops, "'recalculates totals'") {
		t.Errorf("expected COMMENT ON PROCEDURE, got: %v", sqlList(ops))
	}
}

// ── FUNCTION/PROCEDURE Revocations (RFC Section 11.3) ────────────────────────────────
// Regression guard for a real bug found live-testing REVOKES against a
// demo project: ir.Function/ir.Procedure had no Revocations field at all, so
// a declared REVOKES block was parsed but silently dropped everywhere
// downstream (build, snapshot, diff, dump) — only the GRANT half ever took
// effect. Mirrors the Table/View Revocations test suite above.

func TestDiffCreateProcedureEmitsRevocation(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:      "public",
			Name:        "recalc_totals",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN NULL; END;"},
			BodyHash:    ir.HashBody("BEGIN NULL; END;"),
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE EXECUTE ON PROCEDURE") {
		t.Errorf("expected REVOKE EXECUTE ON PROCEDURE at create time, got: %v", sqlList(ops))
	}
	if containsSQL(ops, `"PUBLIC"`) {
		t.Errorf("PUBLIC must not be quoted, got: %v", sqlList(ops))
	}
}

func TestDiffProcedureRevocationAdded(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: hash,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:      "public",
			Name:        "recalc_totals",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash:    hash,
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range ops {
		if strings.Contains(o.SQL(), "REVOKE EXECUTE ON PROCEDURE") {
			found = true
			if o.Safety() != pipeline.Caution {
				t.Errorf("explicit revocation safety = %v, want Caution: %s", o.Safety(), o.SQL())
			}
		}
	}
	if !found {
		t.Errorf("expected REVOKE EXECUTE ON PROCEDURE, got: %v", sqlList(ops))
	}
}

func TestDiffProcedureRevocationRemoved(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: hash,
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:   "public",
			Name:     "recalc_totals",
			Args:     []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:    ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash: hash,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT EXECUTE ON PROCEDURE") {
		t.Errorf("expected GRANT EXECUTE ON PROCEDURE to restore the revoked privilege, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE") {
		t.Errorf("must not also emit REVOKE when the revocation itself was removed: %v", sqlList(ops))
	}
}

func TestDiffProcedureRevocationUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.recalc_totals(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "recalc_totals", Args: "integer", BodyHash: hash,
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Procedure{
			Schema:      "public",
			Name:        "recalc_totals",
			Args:        []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Body: body},
			BodyHash:    hash,
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "GRANT") || containsSQL(ops, "REVOKE") {
		t.Errorf("expected no GRANT/REVOKE for unchanged revocation, got: %v", sqlList(ops))
	}
}

func TestDiffCreateFunctionEmitsRevocation(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:      "public",
			Name:        "do_work",
			ReturnType:  ir.TypeRef{Name: "void"},
			BodyHash:    "h",
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Body: "BEGIN END;"},
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE EXECUTE ON FUNCTION") {
		t.Errorf("expected REVOKE EXECUTE ON FUNCTION at create time, got: %v", sqlList(ops))
	}
}

func TestDiffFunctionRevocationAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema:     "public",
			Name:       "get_user",
			ReturnType: "void",
			Language:   "plpgsql",
			Volatility: "VOLATILE",
			BodyHash:   "abc",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:      "public",
			Name:        "get_user",
			ReturnType:  ir.TypeRef{Name: "void"},
			BodyHash:    "abc",
			Attrs:       ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
			Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "REVOKE EXECUTE ON FUNCTION") {
		t.Errorf("expected REVOKE EXECUTE ON FUNCTION, got: %v", sqlList(ops))
	}
}

func TestDiffFunctionRevocationRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.get_user()", &snapshot.SnapObject{
		Kind: "function",
		Function: &snapshot.SnapFunction{
			Schema:      "public",
			Name:        "get_user",
			ReturnType:  "void",
			Language:    "plpgsql",
			Volatility:  "VOLATILE",
			BodyHash:    "abc",
			Revocations: []snapshot.SnapGrant{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Function{
			Schema:     "public",
			Name:       "get_user",
			ReturnType: ir.TypeRef{Name: "void"},
			BodyHash:   "abc",
			Attrs:      ir.FuncAttrs{Language: "plpgsql", Volatility: "VOLATILE", Body: "BEGIN END;"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "GRANT EXECUTE ON FUNCTION") {
		t.Errorf("expected GRANT EXECUTE ON FUNCTION to restore the revoked privilege, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "REVOKE") {
		t.Errorf("must not also emit REVOKE when the revocation itself was removed: %v", sqlList(ops))
	}
}

// ── TABLE/INDEX Tablespace (RFC Section 14.7) ────────────────────────────────────────
// Regression guard for a real bug found live-testing TABLESPACE against a
// demo project: ir.Table.Tablespace and ir.Index.Tablespace were both parsed
// from source but never read anywhere downstream — createTable/diffTable and
// createIndex/diffIndexes never referenced either field, so a declared
// TABLESPACE clause on a table or an index was a total silent no-op; `dpg
// plan` reported no changes even for a brand-new TABLESPACE declaration on
// an already-applied table.

func TestDiffCreateTableEmitsTablespace(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:     "public",
			Name:       "archive",
			Columns:    []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Tablespace: strPtr("archive_space"),
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `TABLESPACE "archive_space"`) {
		t.Errorf("expected CREATE TABLE with TABLESPACE, got: %v", sqlList(ops))
	}
}

func TestDiffTableTablespaceChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.archive", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "archive",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:     "public",
			Name:       "archive",
			Columns:    []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Tablespace: strPtr("archive_space"),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLE "public"."archive" SET TABLESPACE "archive_space";`) {
		t.Errorf("expected ALTER TABLE SET TABLESPACE, got: %v", sqlList(ops))
	}
}

func TestDiffTableTablespaceUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.archive", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:     "public",
			Name:       "archive",
			Columns:    []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Tablespace: strPtr("archive_space"),
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:     "public",
			Name:       "archive",
			Columns:    []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Tablespace: strPtr("archive_space"),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "TABLESPACE") {
		t.Errorf("expected no TABLESPACE op for unchanged tablespace, got: %v", sqlList(ops))
	}
}

// TestDiffTableReplicaIdentityChanged is the regression guard for Section
// 7.11's REPLICA IDENTITY directive: previously zero code anywhere (ir/
// diff/snapshot/introspect/dump), so a declared value had no effect at all.
func TestDiffTableReplicaIdentityChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:          "public",
			Name:            "orders",
			Columns:         []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			ReplicaIdentity: ir.ReplicaIdentity{Mode: "FULL"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders" REPLICA IDENTITY FULL;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "REPLICA IDENTITY") && o.Safety() != pipeline.Safe {
			t.Errorf("expected SAFE for REPLICA IDENTITY change (metadata-only), got %v", o.Safety())
		}
	}
}

// TestDiffTableReplicaIdentityUsingIndex covers the USING INDEX form.
func TestDiffTableReplicaIdentityUsingIndex(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:          "public",
			Name:            "orders",
			Columns:         []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			ReplicaIdentity: ir.ReplicaIdentity{Mode: "INDEX", IndexName: "orders_natural_key"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders" REPLICA IDENTITY USING INDEX "orders_natural_key";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffTableReplicaIdentityOmittedResetsToDefault proves omitting the
// directive is a real declared value (PostgreSQL's own DEFAULT), not "leave
// whatever's live alone" — matching RLSEnabled/RLSForced's identical
// always-compared-in-full convention.
func TestDiffTableReplicaIdentityOmittedResetsToDefault(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:              "public",
			Name:                "orders",
			Columns:             []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			ReplicaIdentityMode: "FULL",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders" REPLICA IDENTITY DEFAULT;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

func TestDiffTableReplicaIdentityUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:              "public",
			Name:                "orders",
			Columns:             []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			ReplicaIdentityMode: "FULL",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:          "public",
			Name:            "orders",
			Columns:         []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			ReplicaIdentity: ir.ReplicaIdentity{Mode: "FULL"},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "REPLICA IDENTITY") {
		t.Errorf("expected no REPLICA IDENTITY op for unchanged value, got: %v", sqlList(ops))
	}
}

// TestDiffTableClusterOnAddedAndRemoved is the regression guard for
// Section 7.11's CLUSTER ON directive.
func TestDiffTableClusterOnAddedAndRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "orders",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:    "public",
			Name:      "orders",
			Columns:   []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			ClusterOn: strPtr("idx_orders_created_at"),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE "public"."orders" CLUSTER ON "idx_orders_created_at";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}

	// Now removing it emits SET WITHOUT CLUSTER.
	snap2 := &pipeline.Snapshot{}
	_ = snap2.SetObject("public.orders", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:    "public",
			Name:      "orders",
			Columns:   []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			ClusterOn: strPtr("idx_orders_created_at"),
		},
	})
	desired2 := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "orders",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops2, err := d.Diff(desired2, snap2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := `ALTER TABLE "public"."orders" SET WITHOUT CLUSTER;`
	if !containsSQL(ops2, want2) {
		t.Errorf("expected %q, got: %v", want2, sqlList(ops2))
	}
}

// TestCreateTableReplicaIdentityAndClusterOn proves a brand-new table
// (never in the snapshot) also emits the ALTER statements, since real
// PostgreSQL has no CREATE TABLE-native clause for either directive —
// they're ALTER-only even at initial creation.
func TestCreateTableReplicaIdentityAndClusterOn(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:          "public",
			Name:            "orders",
			Columns:         []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			ReplicaIdentity: ir.ReplicaIdentity{Mode: "FULL"},
			ClusterOn:       strPtr("orders_pkey"),
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE TABLE") {
		t.Fatalf("expected a CREATE TABLE op, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."orders" REPLICA IDENTITY FULL;`) {
		t.Errorf("expected REPLICA IDENTITY on create, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLE "public"."orders" CLUSTER ON "orders_pkey";`) {
		t.Errorf("expected CLUSTER ON on create, got: %v", sqlList(ops))
	}
}

func TestDiffCreateIndexEmitsTablespace(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Indexes: []*ir.Index{
				{
					Name:       "t_id_idx",
					Columns:    []pipeline.IndexColumn{{Name: "id"}},
					Tablespace: strPtr("archive_space"),
				},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `TABLESPACE "archive_space"`) {
		t.Errorf("expected CREATE INDEX with TABLESPACE, got: %v", sqlList(ops))
	}
}

func TestDiffIndexTablespaceChangedRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Indexes: []snapshot.SnapIndex{{Name: "t_id_idx", Columns: "id"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Indexes: []*ir.Index{
				{
					Name:       "t_id_idx",
					Columns:    []pipeline.IndexColumn{{Name: "id"}},
					Tablespace: strPtr("archive_space"),
				},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP INDEX IF EXISTS "t_id_idx";`) {
		t.Errorf("expected DROP INDEX for tablespace change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `TABLESPACE "archive_space"`) {
		t.Errorf("expected recreated CREATE INDEX with TABLESPACE, got: %v", sqlList(ops))
	}
}

// ── RANGE/BASE type body-change diffing (RFC Section 5.3/Section 5.5) ───────────────────────
// Regression guard for a real bug found live-testing BASE/VIRTUAL types
// against a demo project: diffType had explicit handling for COMPOSITE and
// ENUM only — RANGE and BASE both fell through to just the generic COMMENT
// check, so an already-applied RANGE or BASE type whose body changed was a
// silent no-op forever. Confirmed live on a throwaway RANGE type: before the
// fix, changing SUBTYPE produced "-- (no changes)"; after, it correctly
// emits DROP TYPE CASCADE + CREATE TYPE (RFC Section 5.3's exact stated semantics).

func TestDiffRangeTypeBodyChangedRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_range", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_range", Variant: "RANGE",
			BodyHash: hashText("CREATE TYPE zz_range AS RANGE (subtype = int)"),
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "zz_range", Variant: "RANGE",
			Body: "CREATE TYPE zz_range AS RANGE (subtype = numeric)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TYPE IF EXISTS "public"."zz_range" CASCADE;`) {
		t.Errorf("expected DROP TYPE ... CASCADE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "CREATE TYPE zz_range AS RANGE (subtype = numeric)") {
		t.Errorf("expected recreated CREATE TYPE with new body, got: %v", sqlList(ops))
	}
	var dropOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP TYPE") {
			dropOp = o
		}
	}
	if dropOp == nil || dropOp.Safety() != pipeline.Destructive {
		t.Errorf("DROP TYPE safety = %v, want Destructive", dropOp)
	}
}

func TestDiffBaseTypeBodyChangedRecreatesNoCascade(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_base", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			BodyHash: hashText("CREATE TYPE zz_base (INPUT = zz_in, OUTPUT = zz_out)"),
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			Body: "CREATE TYPE zz_base (INPUT = zz_in2, OUTPUT = zz_out)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TYPE IF EXISTS "public"."zz_base";`) {
		t.Errorf("expected DROP TYPE (no CASCADE — RFC Section 5.5 doesn't specify it), got: %v", sqlList(ops))
	}
	if containsSQL(ops, "CASCADE") {
		t.Errorf("BASE type drop must not add CASCADE (RANGE-only per RFC Section 5.3), got: %v", sqlList(ops))
	}
}

// TestDiffBaseTypeAlterablePropertyChangeEmitsSafeAlter is the regression
// guard for RFC Section 5.5's 7 in-place-alterable BASE type properties:
// previously ANY change (including these) forced an unconditional
// DROP+CREATE. A STORAGE-only change (immutable properties INPUT/OUTPUT
// unchanged) must instead emit a targeted, SAFE ALTER TYPE ... SET (...).
func TestDiffBaseTypeAlterablePropertyChangeEmitsSafeAlter(t *testing.T) {
	d := New()
	body := "CREATE TYPE zz_base (INPUT = zz_in, OUTPUT = zz_out, STORAGE = plain)"
	oldStorage := "plain"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_base", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			BaseStructured:    true,
			BaseImmutableHash: hashText(snapshot.BaseBodyHashInput(body)),
			BaseStorage:       &oldStorage,
		},
	})
	newStorage := "extended"
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			Body:        "CREATE TYPE zz_base (INPUT = zz_in, OUTPUT = zz_out, STORAGE = extended)",
			BaseStorage: &newStorage,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") {
		t.Fatalf("a STORAGE-only change must not DROP+CREATE, got: %v", sqlList(ops))
	}
	want := `ALTER TYPE "public"."zz_base" SET (STORAGE = extended);`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET (") && o.Safety() != pipeline.Safe {
			t.Errorf("expected SAFE for an alterable-property change, got %v", o.Safety())
		}
	}
}

// TestDiffBaseTypeImmutablePropertyChangeStillDestructive proves an
// INPUT/OUTPUT change (immutable — no ALTER TYPE path exists in real
// PostgreSQL) still requires DROP+CREATE even under the new structured
// (BaseStructured) diffing path, not just the legacy whole-body-hash path.
func TestDiffBaseTypeImmutablePropertyChangeStillDestructive(t *testing.T) {
	d := New()
	oldBody := "CREATE TYPE zz_base (INPUT = zz_in, OUTPUT = zz_out)"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_base", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			BaseStructured:    true,
			BaseImmutableHash: hashText(snapshot.BaseBodyHashInput(oldBody)),
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			Body: "CREATE TYPE zz_base (INPUT = zz_in2, OUTPUT = zz_out)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TYPE IF EXISTS "public"."zz_base";`) {
		t.Errorf("expected DROP TYPE for an immutable-property change, got: %v", sqlList(ops))
	}
}

// TestDiffBaseTypeUnchangedIsNoop proves the structured path doesn't
// spuriously fire when nothing actually changed.
func TestDiffBaseTypeUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "CREATE TYPE zz_base (INPUT = zz_in, OUTPUT = zz_out, STORAGE = plain)"
	storage := "plain"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_base", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			BaseStructured:    true,
			BaseImmutableHash: hashText(snapshot.BaseBodyHashInput(body)),
			BaseStorage:       &storage,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "zz_base", Variant: "BASE",
			Body:        body,
			BaseStorage: &storage,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged base type, got: %v", sqlList(ops))
	}
}

func TestDiffRangeTypeBodyUnchangedIsNoop(t *testing.T) {
	d := New()
	body := "CREATE TYPE zz_range AS RANGE (subtype = numeric)"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_range", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_range", Variant: "RANGE",
			BodyHash: hashText(body),
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "zz_range", Variant: "RANGE", Body: body},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged RANGE body, got: %v", sqlList(ops))
	}
}

// TestDiffRangeTypeReconstructedSnapshotSkipsComparison guards the same
// "reconstructed body isn't byte-identical to hand-written source" false-
// positive that diffOpaqueIR's own body-hash guard exists to avoid
// (Publication/Collation/...): a snapshot entry from live introspection has
// BodyHash == "" (see introspectRangeBodies setting Reconstructed = true),
// so a differently-formatted desired Body must NOT be treated as a change.
func TestDiffRangeTypeReconstructedSnapshotSkipsComparison(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.zz_range", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "zz_range", Variant: "RANGE",
			BodyHash: "", // reconstructed snapshot entry
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "zz_range", Variant: "RANGE",
			Body: "CREATE TYPE zz_range AS RANGE (SUBTYPE = numeric)", // differently formatted
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") {
		t.Errorf("must not recreate when the snapshot side is reconstructed (unhashable), got: %v", sqlList(ops))
	}
}

// ── DOMAIN structured diffing (RFC Section 5.4) ──────────────────────────────────────
// Regression guard for a real bug found live-testing a demo project:
// diffType had no case for DOMAIN at all — an already-applied domain whose
// DEFAULT/NOT NULL/constraints changed was a silent no-op forever, only the
// generic COMMENT check ever fired. Unlike RANGE/BASE (hash-diffed opaque
// bodies), RFC Section 5.4 requires per-property diffing with distinct safety
// levels, so each case below checks both the emitted SQL and its safety.

func TestDiffCreateDomainEmitsFullDefinition(t *testing.T) {
	d := New()
	def := "1"
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "positive_integer", Variant: "DOMAIN",
			DomainBaseType: ir.TypeRef{Name: "integer"},
			DomainDefault:  &def,
			DomainNotNull:  true,
			DomainConstraints: []*ir.Constraint{
				{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	sql := sqlList(ops)
	for _, want := range []string{"CREATE DOMAIN", "AS integer", "DEFAULT 1", "NOT NULL", `CONSTRAINT "positive_only" CHECK (VALUE > 0)`} {
		if !containsSQL(ops, want) {
			t.Errorf("expected %q in CREATE DOMAIN, got: %v", want, sql)
		}
	}
}

// TestDiffCreateEnumEmitsGrant and TestDiffEnumGrantAdded/Removed guard RFC
// audit item #3 for ENUM specifically; TestDiffCreateDomainEmitsGrant and
// TestDiffDomainGrantAdded cover DOMAIN, whose diff path is structurally
// distinct (buildDomainSQL + property-level diffing, not a hash/opaque
// compare) — both needed their own coverage, not just one variant standing
// in for all 5. Real PostgreSQL's GRANT/REVOKE has no separate "ON DOMAIN"
// target (confirmed live): a domain is granted exactly like any other type,
// "GRANT ... ON TYPE domain_name ...".
func TestDiffCreateEnumEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"active", "inactive"},
			Grants:     []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON TYPE "public"."status" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON TYPE, got: %v", sqlList(ops))
	}
}

func TestDiffEnumGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "status", Variant: "ENUM", Values: []string{"active", "inactive"}},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "status", Variant: "ENUM",
			EnumValues: []string{"active", "inactive"},
			Grants:     []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON TYPE "public"."status" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON TYPE, got: %v", sqlList(ops))
	}
}

func TestDiffEnumGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "status", Variant: "ENUM", Values: []string{"active", "inactive"},
			Grants: []snapshot.SnapGrant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "status", Variant: "ENUM", EnumValues: []string{"active", "inactive"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE USAGE ON TYPE "public"."status" FROM "app_service"`) {
		t.Errorf("expected REVOKE USAGE ON TYPE, got: %v", sqlList(ops))
	}
}

func TestDiffCreateDomainEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "positive_integer", Variant: "DOMAIN",
			DomainBaseType: ir.TypeRef{Name: "integer"},
			Grants:         []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON TYPE "public"."positive_integer" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON TYPE for domain, got: %v", sqlList(ops))
	}
}

func TestDiffDomainGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.positive_integer", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "positive_integer", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "positive_integer", Variant: "DOMAIN",
			DomainBaseType: ir.TypeRef{Name: "integer"},
			Grants:         []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON TYPE "public"."positive_integer" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON TYPE for domain, got: %v", sqlList(ops))
	}
}

func TestDiffDomainDefaultAddedIsSafe(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	def := "1"
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}, DomainDefault: &def},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET DEFAULT") {
			found = o
		}
	}
	if found == nil || found.SQL() != `ALTER DOMAIN "public"."d" SET DEFAULT 1;` {
		t.Fatalf("expected SET DEFAULT op, got: %v", sqlList(ops))
	}
	if found.Safety() != pipeline.Safe {
		t.Errorf("safety = %v, want Safe (RFC Section 5.4: adding a DEFAULT is SAFE)", found.Safety())
	}
}

func TestDiffDomainDefaultDroppedIsSafe(t *testing.T) {
	d := New()
	def := "1"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer", DomainDefault: &def},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER DOMAIN "public"."d" DROP DEFAULT;`) {
		t.Fatalf("expected DROP DEFAULT op, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP DEFAULT") && o.Safety() != pipeline.Safe {
			t.Errorf("safety = %v, want Safe (RFC Section 5.4: dropping a DEFAULT is SAFE)", o.Safety())
		}
	}
}

func TestDiffDomainSetNotNullIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}, DomainNotNull: true},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "SET NOT NULL") {
			found = o
		}
	}
	if found == nil {
		t.Fatalf("expected SET NOT NULL op, got: %v", sqlList(ops))
	}
	if found.Safety() != pipeline.Caution {
		t.Errorf("safety = %v, want Caution (existing rows may violate it)", found.Safety())
	}
}

func TestDiffDomainDropNotNullIsSafe(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer", DomainNotNull: true},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP NOT NULL") {
			found = o
		}
	}
	if found == nil || found.Safety() != pipeline.Safe {
		t.Errorf("expected a Safe DROP NOT NULL op, got: %v", sqlList(ops))
	}
}

func TestDiffDomainConstraintAddedIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"},
			DomainConstraints: []*ir.Constraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD CONSTRAINT") {
			found = o
		}
	}
	if found == nil || !strings.Contains(found.SQL(), "positive_only") {
		t.Fatalf("expected ADD CONSTRAINT op, got: %v", sqlList(ops))
	}
	if found.Safety() != pipeline.Caution {
		t.Errorf("safety = %v, want Caution (RFC Section 5.4: adding a constraint is CAUTION)", found.Safety())
	}
}

// TestDiffDomainConstraintAddedNotValidAppendsClause is the regression
// guard for RFC Section 5.4's NOT VALID lifecycle: Constraint.NotValid
// already existed from the table-level feature, but diffType's domain ADD
// CONSTRAINT branch never read it, so a domain constraint declared NOT
// VALID silently validated against every existing column value anyway.
func TestDiffDomainConstraintAddedNotValidAppendsClause(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"},
			DomainConstraints: []*ir.Constraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)", NotValid: true}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER DOMAIN "public"."d" ADD CONSTRAINT "positive_only" CHECK (VALUE > 0) NOT VALID;`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffDomainConstraintValidatedEmitsValidateConstraint proves the
// second half of the lifecycle: a constraint previously added NOT VALID
// that has since had NOT VALID removed from source is validated in place,
// not silently ignored.
func TestDiffDomainConstraintValidatedEmitsValidateConstraint(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer",
			DomainConstraints: []snapshot.SnapConstraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)", NotValid: true}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"},
			DomainConstraints: []*ir.Constraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER DOMAIN "public"."d" VALIDATE CONSTRAINT "positive_only";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP CONSTRAINT") || containsSQL(ops, "ADD CONSTRAINT") {
		t.Errorf("expected only VALIDATE CONSTRAINT (expression unchanged), got: %v", sqlList(ops))
	}
}

func TestDiffDomainConstraintDroppedIsSafe(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer",
			DomainConstraints: []snapshot.SnapConstraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER DOMAIN "public"."d" DROP CONSTRAINT "positive_only";`) {
		t.Fatalf("expected DROP CONSTRAINT op, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP CONSTRAINT") && o.Safety() != pipeline.Safe {
			t.Errorf("safety = %v, want Safe (RFC Section 5.4: dropping a constraint is SAFE)", o.Safety())
		}
	}
}

func TestDiffDomainConstraintChangedDropsAndAdds(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer",
			DomainConstraints: []snapshot.SnapConstraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"},
			DomainConstraints: []*ir.Constraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 10)"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP CONSTRAINT "positive_only"`) || !containsSQL(ops, "VALUE > 10") {
		t.Errorf("expected DROP + ADD CONSTRAINT for a changed expression, got: %v", sqlList(ops))
	}
}

func TestDiffDomainBaseTypeChangedRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "bigint"}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP DOMAIN IF EXISTS "public"."d" CASCADE;`) {
		t.Errorf("expected DROP DOMAIN CASCADE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "AS bigint") {
		t.Errorf("expected recreated CREATE DOMAIN with new base type, got: %v", sqlList(ops))
	}
	var dropOp pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "DROP DOMAIN") {
			dropOp = o
		}
	}
	if dropOp == nil || dropOp.Safety() != pipeline.Destructive {
		t.Errorf("DROP DOMAIN safety = %v, want Destructive", dropOp)
	}
}

// TestDiffDomainCollationChangedRecreates proves RFC Section 5.4's "Changing the
// base type or COLLATE" row applies to a COLLATE-only change too (same base
// type): PostgreSQL has no ALTER DOMAIN for it, only DROP DOMAIN CASCADE +
// CREATE DOMAIN, the same as a base-type change.
func TestDiffDomainCollationChangedRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "text", DomainCollation: "en_US"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "text"}, DomainCollation: "de_DE"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP DOMAIN IF EXISTS "public"."d" CASCADE;`) {
		t.Errorf("expected DROP DOMAIN CASCADE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `AS text COLLATE "de_DE"`) {
		t.Errorf("expected recreated CREATE DOMAIN with new COLLATE, got: %v", sqlList(ops))
	}
}

func TestDiffDomainUnchangedIsNoop(t *testing.T) {
	d := New()
	def := "1"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.d", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: "integer",
			DomainDefault: &def, DomainNotNull: true,
			DomainConstraints: []snapshot.SnapConstraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "d", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"},
			DomainDefault: &def, DomainNotNull: true,
			DomainConstraints: []*ir.Constraint{{Name: "positive_only", Type: "CHECK", Expr: "CHECK (VALUE > 0)"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged domain, got: %v", sqlList(ops))
	}
}

// Regression guard for a real bug found live-testing a demo project's
// pre-existing email_address domain: a snapshot written before domain
// structured-field tracking existed has DomainBaseType == "" (Go zero
// value), which used to compare unequal to any real desired base type and
// spuriously trigger the destructive DROP DOMAIN CASCADE + recreate branch
// on the very first plan after upgrading — for a domain that never actually
// changed. Must instead skip structural comparison and self-heal via a
// harmless comment op so the snapshot gets refreshed on apply.
func TestDiffDomainStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.email_address", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "email_address", Variant: "DOMAIN"},
	})
	desired := []pipeline.IRObject{
		&ir.Type{
			Schema: "public", Name: "email_address", Variant: "DOMAIN",
			DomainBaseType:    ir.TypeRef{Name: "text"},
			DomainConstraints: []*ir.Constraint{{Name: "email_address_check", Type: "CHECK", Expr: "CHECK (value ~ '^[^@]+@[^@]+$')"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP DOMAIN") {
		t.Errorf("stale snapshot must not trigger DROP DOMAIN, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if o.Safety() == pipeline.Destructive {
			t.Errorf("stale snapshot transition must never be destructive, got: %v", sqlList(ops))
		}
	}
}

// ── Tablespace/Cast/EventTrigger structured diffing (G-live closure) ──────────
// Regression guards for the "G-live" gap: the 9 reconstruction-tier opaque
// kinds set Reconstructed=true on introspection, which forced BodyHash to ""
// on the live side (sourceBodyHash), silently disabling diffOpaqueIR's
// body-hash comparison on verify/plan --live — a live-catalog-only change to
// one of these 3 kinds went completely undetected. diffTablespace/diffCast/
// diffEventTrigger replace that with field-level comparison (RFC
// Section 14.7/Section 14.5/Section 14.1), which works identically whether the snapshot side came
// from source or from introspection, since it never depends on Reconstructed
// or BodyHash at all.

func TestDiffTablespaceLocationChangeIsDestructiveNonTransactional(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind: "tablespace",
		Opaque: &snapshot.SnapOpaque{
			Kind: "tablespace", Name: "ts", TablespaceLocation: "/data/ts1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts2", Body: "CREATE TABLESPACE ts LOCATION '/data/ts2'"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP TABLESPACE IF EXISTS "ts"`) {
		t.Errorf("expected DROP TABLESPACE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "/data/ts2") {
		t.Errorf("expected recreate to reflect the new location, got: %v", sqlList(ops))
	}
	// DROP TABLESPACE is Destructive and CREATE TABLESPACE is Safe; both
	// are non-transactional (neither can run inside a transaction block).
	// Safety and Mode are independent properties of an op.
	for _, op := range ops {
		switch {
		case strings.Contains(op.SQL(), "DROP TABLESPACE"):
			if op.Safety() != pipeline.Destructive || op.Mode() != pipeline.ModeNonTransactional {
				t.Errorf("expected DROP TABLESPACE to be Destructive non-transactional, got %v %v: %s", op.Safety(), op.Mode(), op.SQL())
			}
		case strings.Contains(op.SQL(), "CREATE TABLESPACE"):
			if op.Safety() != pipeline.Safe || op.Mode() != pipeline.ModeNonTransactional {
				t.Errorf("expected CREATE TABLESPACE to be Safe non-transactional, got %v %v: %s", op.Safety(), op.Mode(), op.SQL())
			}
		}
	}
}

func TestDiffTablespaceUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind: "tablespace",
		Opaque: &snapshot.SnapOpaque{
			Kind: "tablespace", Name: "ts", TablespaceLocation: "/data/ts1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged tablespace, got: %v", sqlList(ops))
	}
}

// TestDiffTablespaceOwnerChanged guards RFC audit item #80: Tablespace's
// inline OWNER clause was discarded at build time and never diffed at all.
func TestDiffTablespaceOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind: "tablespace",
		Opaque: &snapshot.SnapOpaque{
			Kind: "tablespace", Name: "ts", TablespaceLocation: "/data/ts1", TablespaceOwner: &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts OWNER app_readonly LOCATION '/data/ts1'", Owner: &newOwner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLESPACE "ts" OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER TABLESPACE ... OWNER TO, got: %v", sqlList(ops))
	}
}

// TestDiffTablespaceStorageParamsSetAndReset guards RFC Section 14.7's
// WITH (...) storage-params clause (#15): previously never diffed at any
// layer at all (worse than Table's pre-#60 gap — not even folded into the
// opaque body hash, since Tablespace bodies are always Reconstructed and so
// skip hash comparison entirely). Mirrors diffTable's identical SET/RESET
// shape via the shared diffStorageParamsSetReset helper.
func TestDiffTablespaceStorageParamsSetAndReset(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind: "tablespace",
		Opaque: &snapshot.SnapOpaque{
			Kind: "tablespace", Name: "ts", TablespaceLocation: "/data/ts1",
			TablespaceStorageParams: "seq_page_cost=1.5, random_page_cost=2.0",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{
			Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'",
			StorageParams: []pipeline.StorageParam{{Key: "seq_page_cost", Value: "1.0"}, {Key: "effective_io_concurrency", Value: "200"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TABLESPACE "ts" SET (seq_page_cost=1.0, effective_io_concurrency=200);`) {
		t.Errorf("expected SET with changed+new keys, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TABLESPACE "ts" RESET (random_page_cost);`) {
		t.Errorf("expected RESET with removed key, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if (strings.Contains(op.SQL(), " SET (") || strings.Contains(op.SQL(), " RESET (")) && op.Safety() != pipeline.Caution {
			t.Errorf("storage-params op safety = %v, want Caution: %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffTablespaceRenamedFromEmitsAlterRename guards RFC audit item #80's
// other half: Tablespace had no RenamedFrom field at all.
func TestDiffTablespaceRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts_old", &snapshot.SnapObject{
		Kind: "tablespace",
		Opaque: &snapshot.SnapOpaque{
			Kind: "tablespace", Name: "ts_old", TablespaceLocation: "/data/ts1",
		},
	})
	old := "ts_old"
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts_new", Location: "/data/ts1", Body: "CREATE TABLESPACE ts_new LOCATION '/data/ts1'", RenamedFrom: &old},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TABLESPACE") || containsSQL(ops, "CREATE TABLESPACE") {
		t.Fatalf("renamed tablespace should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER TABLESPACE "ts_old" RENAME TO "ts_new";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER TABLESPACE RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffTablespaceRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation (Differ.Diff's shared Pass 1).
func TestDiffTablespaceRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_tablespace"
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'", RenamedFrom: &old},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot tablespace")
	}
}

// TestDiffCreateTablespaceEmitsGrant and TestDiffTablespaceGrantAdded/Removed
// guard RFC audit item #4 (ir.Tablespace had no Grants/Revocations field).
func TestDiffCreateTablespaceEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Tablespace{
			Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'",
			Grants: []ir.Grant{{Privileges: []string{"CREATE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT CREATE ON TABLESPACE "ts" TO "app_service"`) {
		t.Errorf("expected GRANT CREATE ON TABLESPACE, got: %v", sqlList(ops))
	}
}

func TestDiffTablespaceGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind:   "tablespace",
		Opaque: &snapshot.SnapOpaque{Kind: "tablespace", Name: "ts", TablespaceLocation: "/data/ts1"},
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{
			Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'",
			Grants: []ir.Grant{{Privileges: []string{"CREATE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT CREATE ON TABLESPACE "ts" TO "app_service"`) {
		t.Errorf("expected GRANT CREATE ON TABLESPACE, got: %v", sqlList(ops))
	}
}

func TestDiffTablespaceGrantRemoved(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind: "tablespace",
		Opaque: &snapshot.SnapOpaque{
			Kind: "tablespace", Name: "ts", TablespaceLocation: "/data/ts1",
			Grants: []snapshot.SnapGrant{{Privileges: []string{"CREATE"}, Roles: []string{"app_service"}}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `REVOKE CREATE ON TABLESPACE "ts" FROM "app_service"`) {
		t.Errorf("expected REVOKE CREATE ON TABLESPACE, got: %v", sqlList(ops))
	}
}

// TestDiffTablespaceStaleSnapshotDoesNotRecreate is the G-live counterpart to
// TestDiffDomainStaleSnapshotDoesNotRecreate: a snapshot written before
// TablespaceLocation existed has the Go zero value "" even though the
// tablespace is real — without the guard, every already-applied tablespace
// would look like a spurious location change (DROP TABLESPACE, destructive)
// on the very first plan/apply after upgrading.
func TestDiffTablespaceStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind:   "tablespace",
		Opaque: &snapshot.SnapOpaque{Kind: "tablespace", Name: "ts"},
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts1", Body: "CREATE TABLESPACE ts LOCATION '/data/ts1'"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TABLESPACE") {
		t.Errorf("stale snapshot must not trigger DROP TABLESPACE, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if o.Safety() == pipeline.Destructive || o.Mode() == pipeline.ModeInstructionOnly {
			t.Errorf("stale snapshot transition must never be destructive/manual, got: %v", sqlList(ops))
		}
	}
}

func TestDiffCastFunctionChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("int4->text", &snapshot.SnapObject{
		Kind: "cast",
		Opaque: &snapshot.SnapOpaque{
			Kind: "cast", Name: "int4->text", CastMethod: "f", CastContext: "e", CastFunction: "public.f1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Cast{
			SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "text"},
			Method: "f", Context: "e", Function: "f2", // unqualified — must still compare equal via qualifyFuncForCompare against a *different* function
			Body: "CREATE CAST (int4 AS text) WITH FUNCTION f2(int4)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "DROP CAST IF EXISTS (int4 AS text)") {
		t.Errorf("expected DROP CAST, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "f2(int4)") {
		t.Errorf("expected recreate to reflect the new function, got: %v", sqlList(ops))
	}
}

// TestDiffCastFunctionUnqualifiedMatchesQualifiedNoOp proves
// qualifyFuncForCompare's reuse is wired correctly: introspection always
// returns a schema-qualified CastFunction ("public.f1"), while hand-written
// source commonly leaves it unqualified ("f1") relying on the default
// "public" schema — these must compare equal, not spuriously recreate.
func TestDiffCastFunctionUnqualifiedMatchesQualifiedNoOp(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("int4->text", &snapshot.SnapObject{
		Kind: "cast",
		Opaque: &snapshot.SnapOpaque{
			Kind: "cast", Name: "int4->text", CastMethod: "f", CastContext: "e", CastFunction: "public.f1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.Cast{
			SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "text"},
			Method: "f", Context: "e", Function: "f1",
			Body: "CREATE CAST (int4 AS text) WITH FUNCTION f1(int4)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a schema-unqualified-vs-qualified match, got: %v", sqlList(ops))
	}
}

// TestDiffCastStaleSnapshotDoesNotRecreate mirrors
// TestDiffTablespaceStaleSnapshotDoesNotRecreate: CastMethod == "" (Go zero
// value) must be treated as "not yet populated," not "changed."
func TestDiffCastStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("int4->text", &snapshot.SnapObject{
		Kind:   "cast",
		Opaque: &snapshot.SnapOpaque{Kind: "cast", Name: "int4->text"},
	})
	desired := []pipeline.IRObject{
		&ir.Cast{
			SourceType: ir.TypeRef{Name: "int4"}, TargetType: ir.TypeRef{Name: "text"},
			Method: "f", Context: "e", Function: "f1",
			Body: "CREATE CAST (int4 AS text) WITH FUNCTION f1(int4)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP CAST") {
		t.Errorf("stale snapshot must not trigger DROP CAST, got: %v", sqlList(ops))
	}
}

func TestDiffEventTriggerTagChangeRecreatesAndIsSafe(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et", &snapshot.SnapObject{
		Kind: "event_trigger",
		Opaque: &snapshot.SnapOpaque{
			Kind: "event_trigger", Name: "et",
			EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE"}, EventTriggerFunction: "public.f1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE", "DROP SCHEMA"}, Function: "f1",
			Body: "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE', 'DROP SCHEMA') EXECUTE FUNCTION f1()",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP EVENT TRIGGER IF EXISTS "et"`) {
		t.Errorf("expected DROP EVENT TRIGGER, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "DROP SCHEMA") {
		t.Errorf("expected recreate to reflect the new tag list, got: %v", sqlList(ops))
	}
	// RFC Section 14.1: event trigger DROP+CREATE is SAFE (no data involved),
	// unlike every other opaque-tier DROP+CREATE.
	for _, op := range ops {
		if op.Safety() != pipeline.Safe {
			t.Errorf("expected event trigger DROP+CREATE ops to be Safe, got %v: %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffEventTriggerTagOrderIsNotDrift proves tags are compared as a set
// (stringSetEqual), not positionally — PostgreSQL's evttags is an unordered
// array and WHEN TAG IN (...)'s list order carries no meaning.
func TestDiffEventTriggerTagOrderIsNotDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et", &snapshot.SnapObject{
		Kind: "event_trigger",
		Opaque: &snapshot.SnapOpaque{
			Kind: "event_trigger", Name: "et",
			EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE", "DROP SCHEMA"}, EventTriggerFunction: "public.f1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP SCHEMA", "DROP TABLE"}, Function: "f1",
			Body: "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP SCHEMA', 'DROP TABLE') EXECUTE FUNCTION f1()",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a reordered-but-identical tag set, got: %v", sqlList(ops))
	}
}

// TestDiffEventTriggerStaleSnapshotDoesNotRecreate mirrors
// TestDiffTablespaceStaleSnapshotDoesNotRecreate: EventTriggerEvent == ""
// (Go zero value) must be treated as "not yet populated," not "changed."
func TestDiffEventTriggerStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et", &snapshot.SnapObject{
		Kind:   "event_trigger",
		Opaque: &snapshot.SnapOpaque{Kind: "event_trigger", Name: "et"},
	})
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
			Body: "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP EVENT TRIGGER") {
		t.Errorf("stale snapshot must not trigger DROP EVENT TRIGGER, got: %v", sqlList(ops))
	}
}

// TestDiffEventTriggerOwnerChanged is the regression guard for Section
// 14.1's OWNER TO capability: previously ir.EventTrigger had no Owner
// field at all, so a declared owner had zero effect.
func TestDiffEventTriggerOwnerChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et", &snapshot.SnapObject{
		Kind: "event_trigger",
		Opaque: &snapshot.SnapOpaque{
			Kind: "event_trigger", Name: "et",
			EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE"}, EventTriggerFunction: "public.f1",
		},
	})
	owner := "audit_admin"
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
			Body:  "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
			Owner: &owner,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER EVENT TRIGGER "et" OWNER TO "audit_admin";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	if containsSQL(ops, "DROP EVENT TRIGGER") {
		t.Errorf("owner-only change should not DROP+CREATE, got: %v", sqlList(ops))
	}
}

// TestCreateEventTriggerWithOwner proves a brand-new event trigger with a
// declared owner applies it at creation time.
func TestCreateEventTriggerWithOwner(t *testing.T) {
	d := New()
	owner := "audit_admin"
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
			Body:  "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
			Owner: &owner,
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE EVENT TRIGGER") {
		t.Fatalf("expected a CREATE EVENT TRIGGER op, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `SET ROLE "audit_admin";`) {
		t.Errorf("expected owner to be applied at creation via SET ROLE/RESET ROLE, got: %v", sqlList(ops))
	}
}

// TestCreateEventTriggerWithEnableState proves a brand-new event trigger
// with a declared trigger-enable-state applies it via a follow-up
// ALTER EVENT TRIGGER (Section 14.1, audit item #76's remainder) — real
// CREATE EVENT TRIGGER has no inline clause for it (confirmed live), same
// reasoning as table Trigger's identical createTrigger follow-up.
func TestCreateEventTriggerWithEnableState(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
			Body:        "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
			EnableState: "ENABLE REPLICA",
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE EVENT TRIGGER") {
		t.Fatalf("expected a CREATE EVENT TRIGGER op, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER EVENT TRIGGER "et" ENABLE REPLICA;`) {
		t.Errorf("expected a follow-up ALTER EVENT TRIGGER ENABLE REPLICA, got: %v", sqlList(ops))
	}
}

// TestDiffEventTriggerEnableStateChanged guards the ALTER-side diffing for
// an already-applied event trigger's enable-state, covering all three real
// PostgreSQL verbs plus transitioning back to the default (bare ENABLE).
func TestDiffEventTriggerEnableStateChanged(t *testing.T) {
	cases := []struct {
		name       string
		fromState  string
		toState    string
		wantSuffix string
	}{
		{"defaultToDisabled", "", "DISABLED", `ALTER EVENT TRIGGER "et" DISABLE;`},
		{"defaultToReplica", "", "ENABLE REPLICA", `ALTER EVENT TRIGGER "et" ENABLE REPLICA;`},
		{"defaultToAlways", "", "ENABLE ALWAYS", `ALTER EVENT TRIGGER "et" ENABLE ALWAYS;`},
		{"disabledBackToDefault", "DISABLED", "", `ALTER EVENT TRIGGER "et" ENABLE;`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New()
			snap := &pipeline.Snapshot{}
			_ = snap.SetObject("et", &snapshot.SnapObject{
				Kind: "event_trigger",
				Opaque: &snapshot.SnapOpaque{
					Kind: "event_trigger", Name: "et",
					EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE"}, EventTriggerFunction: "public.f1",
					EventTriggerEnableState: tc.fromState,
				},
			})
			desired := []pipeline.IRObject{
				&ir.EventTrigger{
					Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
					Body:        "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
					EnableState: tc.toState,
				},
			}
			ops, err := d.Diff(desired, snap)
			if err != nil {
				t.Fatal(err)
			}
			if !containsSQL(ops, tc.wantSuffix) {
				t.Errorf("expected %q, got: %v", tc.wantSuffix, sqlList(ops))
			}
			if containsSQL(ops, "DROP EVENT TRIGGER") {
				t.Errorf("enable-state-only change should not DROP+CREATE, got: %v", sqlList(ops))
			}
			for _, op := range ops {
				if strings.Contains(op.SQL(), "ALTER EVENT TRIGGER") && op.Safety() != pipeline.Safe {
					t.Errorf("enable-state op safety = %v, want Safe: %s", op.Safety(), op.SQL())
				}
			}
		})
	}
}

// TestDiffEventTriggerEnableStateUnchangedIsNoop proves an unchanged
// enable-state produces no ALTER EVENT TRIGGER op at all.
func TestDiffEventTriggerEnableStateUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et", &snapshot.SnapObject{
		Kind: "event_trigger",
		Opaque: &snapshot.SnapOpaque{
			Kind: "event_trigger", Name: "et",
			EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE"}, EventTriggerFunction: "public.f1",
			EventTriggerEnableState: "ENABLE ALWAYS",
		},
	})
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
			Body:        "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
			EnableState: "ENABLE ALWAYS",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for unchanged enable-state, got: %v", sqlList(ops))
	}
}

// TestDiffEventTriggerRenamedFromEmitsAlterRename is the regression guard
// for EventTrigger rename detection: before this, ir.EventTrigger had no
// RenamedFrom field at all — the struct's own doc comment explicitly said
// none of ENABLE/DISABLE/OWNER TO/RENAME TO were modeled — so a renamed
// event trigger was indistinguishable from "old one dropped, new one added."
func TestDiffEventTriggerRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et_old", &snapshot.SnapObject{
		Kind: "event_trigger",
		Opaque: &snapshot.SnapOpaque{
			Kind: "event_trigger", Name: "et_old",
			EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE"}, EventTriggerFunction: "public.f1",
		},
	})
	old := "et_old"
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE"}, Function: "f1",
			RenamedFrom: &old,
			Body:        "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE') EXECUTE FUNCTION f1()",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP EVENT TRIGGER") || containsSQL(ops, "CREATE EVENT TRIGGER") {
		t.Fatalf("renamed event trigger should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER EVENT TRIGGER "et_old" RENAME TO "et";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER EVENT TRIGGER RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffEventTriggerRenamedFromAndTagChanged confirms a simultaneous
// rename + structural change (Event/Tags/Function) still requires
// DROP+CREATE (no ALTER EVENT TRIGGER path changes those), with no
// redundant separate rename — createOpaque already creates under the final
// (new) name. Drops via dropObject's own embedded snap identity (the
// matched, old name), same as diffCollation/diffType's identical reasoning.
func TestDiffEventTriggerRenamedFromAndTagChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("et_old", &snapshot.SnapObject{
		Kind: "event_trigger",
		Opaque: &snapshot.SnapOpaque{
			Kind: "event_trigger", Name: "et_old",
			EventTriggerEvent: "sql_drop", EventTriggerTags: []string{"DROP TABLE"}, EventTriggerFunction: "public.f1",
		},
	})
	old := "et_old"
	desired := []pipeline.IRObject{
		&ir.EventTrigger{
			Name: "et", Event: "sql_drop", Tags: []string{"DROP TABLE", "DROP SCHEMA"}, Function: "f1",
			RenamedFrom: &old,
			Body:        "CREATE EVENT TRIGGER et ON sql_drop WHEN TAG IN ('DROP TABLE', 'DROP SCHEMA') EXECUTE FUNCTION f1()",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP EVENT TRIGGER IF EXISTS "et_old";`) {
		t.Errorf("expected DROP EVENT TRIGGER referencing the trigger's actual (old) name, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a separate ALTER EVENT TRIGGER RENAME TO alongside drop+create, got: %v", sqlList(ops))
		}
	}
}

// TestDiffEventTriggerRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation.
func TestDiffEventTriggerRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_et"
	desired := []pipeline.IRObject{
		&ir.EventTrigger{Name: "et", Event: "sql_drop", Function: "f1", RenamedFrom: &old},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot event trigger")
	}
}

// ── FDW/ForeignServer/UserMapping structured OPTIONS diffing (G-live Tier 2) ──
// Same G-live gap as Tier 1 (Tablespace/Cast/EventTrigger): Reconstructed
// forced BodyHash to "" on the live side, silently disabling diffOpaqueIR's
// comparison on verify/plan --live. diffFDW/diffForeignServer/diffUserMapping
// replace that with field-level comparison, gated by the explicit
// OptionsStructured sentinel (none of these 3 kinds has a field guaranteed
// non-empty the way Tier 1's did).

func TestDiffFDWOptionsChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw", &snapshot.SnapObject{
		Kind: "fdw",
		Opaque: &snapshot.SnapOpaque{
			Kind: "fdw", Name: "myfdw", OptionsStructured: true,
			FDWOptions: []snapshot.SnapOptionKV{{Key: "debug", Value: "false"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", Options: []pipeline.StorageParam{{Key: "debug", Value: "true"}},
			Body: "CREATE FOREIGN DATA WRAPPER myfdw OPTIONS (debug 'true')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP FOREIGN DATA WRAPPER IF EXISTS "myfdw"`) {
		t.Errorf("expected DROP FOREIGN DATA WRAPPER, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP FOREIGN DATA WRAPPER") && op.Safety() != pipeline.Destructive {
			t.Errorf("expected DROP FOREIGN DATA WRAPPER to be Destructive (RFC Section 14.8), got %v: %s", op.Safety(), op.SQL())
		}
	}
}

func TestDiffFDWUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw", &snapshot.SnapObject{
		Kind: "fdw",
		Opaque: &snapshot.SnapOpaque{
			Kind: "fdw", Name: "myfdw", OptionsStructured: true,
			FDWHandler: "public.h1", FDWOptions: []snapshot.SnapOptionKV{{Key: "debug", Value: "true"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", Handler: "h1", // unqualified — must match "public.h1" via qualifyFuncForCompare
			Options: []pipeline.StorageParam{{Key: "debug", Value: "true"}},
			Body:    "CREATE FOREIGN DATA WRAPPER myfdw HANDLER h1 OPTIONS (debug 'true')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged FDW, got: %v", sqlList(ops))
	}
}

// TestDiffFDWOwnerChanged guards the lower-priority fresh-audit finding
// that ir.ForeignDataWrapper had no Owner support at all — mirrors
// ForeignServer's identical ALTER ... OWNER TO diffing.
func TestDiffFDWOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "alice"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw", &snapshot.SnapObject{
		Kind: "fdw",
		Opaque: &snapshot.SnapOpaque{
			Kind: "fdw", Name: "myfdw", OptionsStructured: true, FDWOwner: &oldOwner,
		},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", Body: "CREATE FOREIGN DATA WRAPPER myfdw",
			Owner: strPtr("bob"),
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER FOREIGN DATA WRAPPER "myfdw" OWNER TO "bob"`) {
		t.Errorf("expected ALTER FOREIGN DATA WRAPPER OWNER TO, got: %v", sqlList(ops))
	}
}

func TestDiffFDWOwnerUnchangedIsNoop(t *testing.T) {
	d := New()
	owner := "alice"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw", &snapshot.SnapObject{
		Kind: "fdw",
		Opaque: &snapshot.SnapOpaque{
			Kind: "fdw", Name: "myfdw", OptionsStructured: true, FDWOwner: &owner,
		},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", Body: "CREATE FOREIGN DATA WRAPPER myfdw",
			Owner: &owner,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged Owner, got: %v", sqlList(ops))
	}
}

// TestDiffCreateFDWEmitsGrant and TestDiffFDWGrantAdded guard RFC audit item
// #5 (ir.ForeignDataWrapper had no Grants/Revocations field).
func TestDiffCreateFDWEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", Body: "CREATE FOREIGN DATA WRAPPER myfdw",
			Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON FOREIGN DATA WRAPPER "myfdw" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON FOREIGN DATA WRAPPER, got: %v", sqlList(ops))
	}
}

func TestDiffFDWGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw", &snapshot.SnapObject{
		Kind:   "fdw",
		Opaque: &snapshot.SnapOpaque{Kind: "fdw", Name: "myfdw", OptionsStructured: true},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", Body: "CREATE FOREIGN DATA WRAPPER myfdw",
			Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON FOREIGN DATA WRAPPER "myfdw" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON FOREIGN DATA WRAPPER, got: %v", sqlList(ops))
	}
}

func TestDiffFDWStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw", &snapshot.SnapObject{
		Kind:   "fdw",
		Opaque: &snapshot.SnapOpaque{Kind: "fdw", Name: "myfdw"},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{Name: "myfdw", Handler: "h1", Body: "CREATE FOREIGN DATA WRAPPER myfdw HANDLER h1"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP FOREIGN DATA WRAPPER") {
		t.Errorf("stale snapshot must not trigger DROP FOREIGN DATA WRAPPER, got: %v", sqlList(ops))
	}
}

// TestDiffFDWRenamedFromEmitsAlterRename is the regression guard for FDW
// rename detection: before this, ir.ForeignDataWrapper had no RenamedFrom
// field at all, so a renamed FDW was indistinguishable from "old one
// dropped, new one added."
func TestDiffFDWRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw_old", &snapshot.SnapObject{
		Kind: "fdw",
		Opaque: &snapshot.SnapOpaque{
			Kind: "fdw", Name: "myfdw_old", OptionsStructured: true,
			FDWOptions: []snapshot.SnapOptionKV{{Key: "debug", Value: "true"}},
		},
	})
	old := "myfdw_old"
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", RenamedFrom: &old,
			Options: []pipeline.StorageParam{{Key: "debug", Value: "true"}},
			Body:    "CREATE FOREIGN DATA WRAPPER myfdw OPTIONS (debug 'true')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP FOREIGN DATA WRAPPER") {
		t.Fatalf("renamed FDW should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER FOREIGN DATA WRAPPER "myfdw_old" RENAME TO "myfdw";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER FOREIGN DATA WRAPPER RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffFDWRenamedFromAndOptionsChanged confirms a simultaneous rename +
// options change still requires DROP+CREATE (RFC's own documented FDW
// semantics — no ALTER FOREIGN DATA WRAPPER path modeled), with no
// redundant separate rename — createOpaque already creates under the final
// (new) name.
func TestDiffFDWRenamedFromAndOptionsChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("myfdw_old", &snapshot.SnapObject{
		Kind: "fdw",
		Opaque: &snapshot.SnapOpaque{
			Kind: "fdw", Name: "myfdw_old", OptionsStructured: true,
			FDWOptions: []snapshot.SnapOptionKV{{Key: "debug", Value: "false"}},
		},
	})
	old := "myfdw_old"
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{
			Name: "myfdw", RenamedFrom: &old,
			Options: []pipeline.StorageParam{{Key: "debug", Value: "true"}},
			Body:    "CREATE FOREIGN DATA WRAPPER myfdw OPTIONS (debug 'true')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP FOREIGN DATA WRAPPER IF EXISTS "myfdw_old"`) {
		t.Errorf("expected DROP referencing the FDW's actual (old) name, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a separate RENAME TO alongside drop+create, got: %v", sqlList(ops))
		}
	}
}

// TestDiffFDWRenamedFromStaleErrors mirrors every other kind's identical
// stale-RENAMED-FROM validation.
func TestDiffFDWRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_fdw"
	desired := []pipeline.IRObject{
		&ir.ForeignDataWrapper{Name: "myfdw", RenamedFrom: &old, Body: "CREATE FOREIGN DATA WRAPPER myfdw"},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot FDW")
	}
}

func TestDiffForeignServerOptionsChangeIsSafeAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv", &snapshot.SnapObject{
		Kind: "server",
		Opaque: &snapshot.SnapOpaque{
			Kind: "server", Name: "srv", OptionsStructured: true, ServerFDWName: "myfdw",
			ServerOptions: []snapshot.SnapOptionKV{{Key: "host", Value: "a"}, {Key: "port", Value: "5432"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignServer{
			Name: "srv", FDWName: "myfdw",
			Options: []pipeline.StorageParam{{Key: "host", Value: "b"}, {Key: "dbname", Value: "mydb"}},
			Body:    "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw OPTIONS (host 'b', dbname 'mydb')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (targeted ALTER SERVER, RFC Section 14.9), got %d: %v", len(ops), sqlList(ops))
	}
	sql := ops[0].SQL()
	if !strings.HasPrefix(sql, "ALTER SERVER") {
		t.Errorf("expected a targeted ALTER SERVER, not DROP+CREATE, got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER SERVER OPTIONS to be Safe (RFC Section 14.9), got %v: %s", ops[0].Safety(), sql)
	}
	if !strings.Contains(sql, "SET host 'b'") {
		t.Errorf("expected SET host 'b', got: %s", sql)
	}
	if !strings.Contains(sql, "ADD dbname 'mydb'") {
		t.Errorf("expected ADD dbname 'mydb', got: %s", sql)
	}
	if !strings.Contains(sql, "DROP port") {
		t.Errorf("expected DROP port, got: %s", sql)
	}
}

func TestDiffForeignServerVersionChangeIsSafeAlter(t *testing.T) {
	d := New()
	v1 := "1.0"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv", &snapshot.SnapObject{
		Kind: "server",
		Opaque: &snapshot.SnapOpaque{
			Kind: "server", Name: "srv", OptionsStructured: true, ServerFDWName: "myfdw", ServerVersion: &v1,
		},
	})
	v2 := "2.0"
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv", FDWName: "myfdw", Version: &v2, Body: "CREATE SERVER srv VERSION '2.0' FOREIGN DATA WRAPPER myfdw"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (targeted ALTER SERVER VERSION), got %d: %v", len(ops), sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "VERSION '2.0'") || ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected Safe ALTER SERVER VERSION '2.0', got %v: %s", ops[0].Safety(), ops[0].SQL())
	}
}

// TestDiffCreateForeignServerEmitsGrant and TestDiffForeignServerGrantAdded
// guard RFC audit item #6 (ir.ForeignServer had no Grants/Revocations
// field).
func TestDiffCreateForeignServerEmitsGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.ForeignServer{
			Name: "srv", FDWName: "myfdw", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw",
			Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON FOREIGN SERVER "srv" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON FOREIGN SERVER, got: %v", sqlList(ops))
	}
}

func TestDiffForeignServerGrantAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv", &snapshot.SnapObject{
		Kind:   "server",
		Opaque: &snapshot.SnapOpaque{Kind: "server", Name: "srv", OptionsStructured: true, ServerFDWName: "myfdw"},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignServer{
			Name: "srv", FDWName: "myfdw", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw",
			Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"app_service"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `GRANT USAGE ON FOREIGN SERVER "srv" TO "app_service"`) {
		t.Errorf("expected GRANT USAGE ON FOREIGN SERVER, got: %v", sqlList(ops))
	}
}

func TestDiffForeignServerFDWChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv", &snapshot.SnapObject{
		Kind: "server",
		Opaque: &snapshot.SnapOpaque{
			Kind: "server", Name: "srv", OptionsStructured: true, ServerFDWName: "fdw1",
		},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv", FDWName: "fdw2", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER fdw2"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP SERVER IF EXISTS "srv"`) {
		t.Errorf("expected DROP SERVER for a FDW-wrapper change, got: %v", sqlList(ops))
	}
}

func TestDiffForeignServerStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv", &snapshot.SnapObject{
		Kind:   "server",
		Opaque: &snapshot.SnapOpaque{Kind: "server", Name: "srv"},
	})
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv", FDWName: "myfdw", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP SERVER") {
		t.Errorf("stale snapshot must not trigger DROP SERVER, got: %v", sqlList(ops))
	}
}

// TestDiffForeignServerOwnerChanged guards RFC audit item #79: ForeignServer
// had no Owner field at all.
func TestDiffForeignServerOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv", &snapshot.SnapObject{
		Kind: "server",
		Opaque: &snapshot.SnapOpaque{
			Kind: "server", Name: "srv", OptionsStructured: true, ServerFDWName: "myfdw",
			ServerOwner: &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv", FDWName: "myfdw", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw", Owner: &newOwner},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER SERVER "srv" OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER SERVER ... OWNER TO, got: %v", sqlList(ops))
	}
}

// TestCreateForeignServerWithOwner proves a brand-new server declared with
// OWNER creates directly as that role (SET ROLE/RESET ROLE wrapping),
// matching real PostgreSQL creator-attribution semantics.
func TestCreateForeignServerWithOwner(t *testing.T) {
	owner := "audit_admin"
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv", FDWName: "myfdw", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw", Owner: &owner},
	}
	ops, err := New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "audit_admin"`) {
		t.Errorf("expected creation wrapped in SET ROLE audit_admin, got: %v", sqlList(ops))
	}
}

// TestDiffForeignServerRenamedFromEmitsAlterRename guards RFC audit item
// #79: ForeignServer had no RenamedFrom field at all.
func TestDiffForeignServerRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("srv_old", &snapshot.SnapObject{
		Kind: "server",
		Opaque: &snapshot.SnapOpaque{
			Kind: "server", Name: "srv_old", OptionsStructured: true, ServerFDWName: "myfdw",
		},
	})
	old := "srv_old"
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv_new", FDWName: "myfdw", Body: "CREATE SERVER srv_new FOREIGN DATA WRAPPER myfdw", RenamedFrom: &old},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP SERVER") || containsSQL(ops, "CREATE SERVER") {
		t.Fatalf("renamed server should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER SERVER "srv_old" RENAME TO "srv_new";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER SERVER RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffForeignServerRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation (Differ.Diff's shared Pass 1).
func TestDiffForeignServerRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_server"
	desired := []pipeline.IRObject{
		&ir.ForeignServer{Name: "srv", FDWName: "myfdw", Body: "CREATE SERVER srv FOREIGN DATA WRAPPER myfdw", RenamedFrom: &old},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot server")
	}
}

// TestDiffUserMappingLiveOnlyNonSensitiveOptionsChangeDetected is the actual
// G-live proof for UserMapping: on the live path (snap.BodyHash == "", i.e.
// Reconstructed), a non-sensitive OPTIONS change must now be detected via
// the new structural comparison — before this fix, diffUserMapping's sole
// check (snap.BodyHash == "" || newHash == snap.BodyHash) treated
// snap.BodyHash == "" as unconditionally "no change."
func TestDiffUserMappingLiveOnlyNonSensitiveOptionsChangeDetected(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app@srv", &snapshot.SnapObject{
		Kind: "user_mapping",
		Opaque: &snapshot.SnapOpaque{
			Kind: "user_mapping", Name: "app@srv", OptionsStructured: true,
			UserMappingOptions: []snapshot.SnapOptionKV{{Key: "user", Value: "app_v1"}},
			// BodyHash intentionally "" — simulates the introspected/live side.
		},
	})
	desired := []pipeline.IRObject{
		&ir.UserMapping{
			User: "app", Server: "srv",
			Options: []pipeline.StorageParam{{Key: "user", Value: "app_v2"}},
			Body:    "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app_v2')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP USER MAPPING IF EXISTS FOR "app" SERVER "srv"`) {
		t.Fatalf("G-live gap not closed: expected the live-only OPTIONS change to be detected, got %d ops: %v", len(ops), sqlList(ops))
	}
}

// TestDiffUserMappingLiveOnlyPasswordChangeStaysUndetected documents the
// remaining, genuinely inherent limitation (RFC Section 24): the live side can
// never expose a real password value to compare against, only a fixed
// redaction placeholder, so a live-only password change cannot be detected
// — this is a deliberate scope boundary, not an oversight. Non-sensitive
// OPTIONS on the exact same mapping still work (proven above).
func TestDiffUserMappingLiveOnlyPasswordChangeStaysUndetected(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app@srv", &snapshot.SnapObject{
		Kind: "user_mapping",
		Opaque: &snapshot.SnapOpaque{
			Kind: "user_mapping", Name: "app@srv", OptionsStructured: true,
			UserMappingOptions: nil, // password-like keys are never stored
		},
	})
	desired := []pipeline.IRObject{
		&ir.UserMapping{
			User: "app", Server: "srv",
			Options: []pipeline.StorageParam{{Key: "password", Value: "{{vault:secret/db#new}}"}},
			Body:    "CREATE USER MAPPING FOR app SERVER srv OPTIONS (password '{{vault:secret/db#new}}')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops (password changes are undetectable on the live path, by design), got: %v", sqlList(ops))
	}
}

func TestDiffUserMappingStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("app@srv", &snapshot.SnapObject{
		Kind:   "user_mapping",
		Opaque: &snapshot.SnapOpaque{Kind: "user_mapping", Name: "app@srv"},
	})
	desired := []pipeline.IRObject{
		&ir.UserMapping{User: "app", Server: "srv", Body: "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app')"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP USER MAPPING") {
		t.Errorf("stale snapshot must not trigger DROP USER MAPPING, got: %v", sqlList(ops))
	}
}

// ── Publication structured diffing (G-live Tier 3) ────────────────────────────
// Same G-live gap as Tiers 1/2: Reconstructed forced BodyHash to "" on the
// live side, silently disabling diffOpaqueIR's comparison on verify/
// plan --live. diffPublication replaces that with field-level comparison,
// gated by the explicit PublicationStructured sentinel (AllTables can
// legitimately be false and Insert/Update/Delete/Truncate default true, so
// no single field is a reliable "not yet populated" signal).

func TestDiffPublicationTablesChangeIsSafeAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true,
			PublicationTables: []string{"public.t1"},
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p", Tables: []ir.PublicationTableRef{{Schema: "public", Name: "t1"}, {Schema: "public", Name: "t2"}},
			Insert: true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p FOR TABLE public.t1, public.t2",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (targeted ALTER PUBLICATION SET TABLE, RFC Section 13.1), got %d: %v", len(ops), sqlList(ops))
	}
	sql := ops[0].SQL()
	if !strings.HasPrefix(sql, "ALTER PUBLICATION") || !strings.Contains(sql, "SET TABLE") {
		t.Errorf("expected a targeted ALTER PUBLICATION ... SET TABLE, not DROP+CREATE, got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER PUBLICATION SET TABLE to be Safe (RFC Section 13.1), got %v: %s", ops[0].Safety(), sql)
	}
	if !strings.Contains(sql, `"public"."t1"`) || !strings.Contains(sql, `"public"."t2"`) {
		t.Errorf("expected both tables in the SET TABLE list, got: %s", sql)
	}
}

func TestDiffPublicationUnqualifiedTableMatchesQualifiedNoOp(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true,
			PublicationTables: []string{"public.t1"}, // introspection always qualifies
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p", Tables: []ir.PublicationTableRef{{Name: "t1"}}, // unqualified in source
			Insert: true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p FOR TABLE t1",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a schema-unqualified-vs-qualified table match, got: %v", sqlList(ops))
	}
}

func TestDiffPublicationPublishOptionsChangeIsSafeAlter(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true, PublicationAllTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p", AllTables: true, Insert: true, Update: false, Delete: false, Truncate: true,
			Body: "CREATE PUBLICATION p FOR ALL TABLES WITH (publish = 'insert, truncate')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (targeted ALTER PUBLICATION SET (publish=...)), got %d: %v", len(ops), sqlList(ops))
	}
	sql := ops[0].SQL()
	if !strings.HasPrefix(sql, "ALTER PUBLICATION") || !strings.Contains(sql, "SET (publish") {
		t.Errorf("expected a targeted ALTER PUBLICATION ... SET (publish = ...), got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER PUBLICATION SET (publish) to be Safe, got %v: %s", ops[0].Safety(), sql)
	}
	if !strings.Contains(sql, "'insert, truncate'") {
		t.Errorf("expected publish = 'insert, truncate', got: %s", sql)
	}
}

func TestDiffPublicationAllTablesChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true, PublicationAllTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p", Tables: []ir.PublicationTableRef{{Schema: "public", Name: "t1"}},
			Insert: true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p FOR TABLE public.t1",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP PUBLICATION IF EXISTS "p"`) {
		t.Errorf("expected DROP PUBLICATION for an AllTables change, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP PUBLICATION") && op.Safety() != pipeline.Destructive {
			t.Errorf("expected DROP PUBLICATION to be Destructive (RFC Section 13.1), got %v: %s", op.Safety(), op.SQL())
		}
	}
}

func TestDiffPublicationUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true, PublicationAllTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{Name: "p", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true, Body: "CREATE PUBLICATION p FOR ALL TABLES"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged publication, got: %v", sqlList(ops))
	}
}

func TestDiffPublicationStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind:   "publication",
		Opaque: &snapshot.SnapOpaque{Kind: "publication", Name: "p"},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{Name: "p", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true, Body: "CREATE PUBLICATION p FOR ALL TABLES"},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP PUBLICATION") {
		t.Errorf("stale snapshot must not trigger DROP PUBLICATION, got: %v", sqlList(ops))
	}
}

// TestDiffPublicationRenamedFromEmitsAlterRename guards RFC audit item #78:
// Publication had no RenamedFrom field at all, so a renamed publication
// could only ever be matched as an unrelated drop+create pair.
func TestDiffPublicationRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p_old", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p_old", PublicationStructured: true, PublicationAllTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	old := "p_old"
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p_new", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p_new FOR ALL TABLES", RenamedFrom: &old,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP PUBLICATION") || containsSQL(ops, "CREATE PUBLICATION") {
		t.Fatalf("renamed publication should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER PUBLICATION "p_old" RENAME TO "p_new";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER PUBLICATION RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffPublicationRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation (Differ.Diff's shared Pass 1).
func TestDiffPublicationRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_publication"
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p FOR ALL TABLES", RenamedFrom: &old,
		},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot publication")
	}
}

// TestDiffPublicationFilteredTableChangeFallsBackToDropCreate is a
// correctness regression guard, not just a G-live proof: a Tables-set
// change on a publication using a column-list/WHERE filter (real syntax:
// FOR TABLE t (col1, col2) WHERE (expr)) must NOT be rendered as
// ALTER PUBLICATION ... SET TABLE from PublicationTableRef alone — doing
// so would silently rebuild the table list without the filter, an
// unintentional widening of what's replicated. Falling back to DROP+CREATE
// (which regenerates from the complete Body) is the only safe option here.
func TestDiffPublicationFilteredTableChangeFallsBackToDropCreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("p", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "p", PublicationStructured: true,
			PublicationTables: []string{"public.orders"}, PublicationHasFilteredTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{
			Name: "p",
			Tables: []ir.PublicationTableRef{
				{Schema: "public", Name: "orders"},
				{Schema: "public", Name: "line_items"},
			},
			HasFilteredTables: true,
			Insert:            true, Update: true, Delete: true, Truncate: true,
			Body: "CREATE PUBLICATION p FOR TABLE public.orders (id, status) WHERE (status = 'active'), public.line_items",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "SET TABLE") {
		t.Fatalf("expected the lossy ALTER PUBLICATION SET TABLE path to be avoided for a filtered publication, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `DROP PUBLICATION IF EXISTS "p"`) {
		t.Errorf("expected a fallback DROP+CREATE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "line_items") {
		t.Errorf("expected the recreate to include the new table, got: %v", sqlList(ops))
	}
}

// ── Collation structured diffing (G-live Tier 4) ───────────────────────────────
// Same G-live gap as Tiers 1-3: Reconstructed forced BodyHash to "" on the
// live side, silently disabling diffOpaqueIR's comparison on verify/
// plan --live. diffCollation replaces that with field-level comparison,
// gated by the explicit CollationStructured sentinel (Provider "c" is
// itself PostgreSQL's real default, not a reliable "unpopulated" signal).

func TestDiffCollationLocaleChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("POSIX"), Ctype: strPtr("POSIX"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'POSIX')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP COLLATION IF EXISTS "public"."c"`) {
		t.Errorf("expected DROP COLLATION, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP COLLATION") && op.Safety() != pipeline.Destructive {
			t.Errorf("expected DROP COLLATION to be Destructive (RFC Section 14.2), got %v: %s", op.Safety(), op.SQL())
		}
	}
}

func TestDiffCollationDeterministicChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "i", CollationICULocale: strPtr("und-u-ks-level2"), CollationDeterministic: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "i", ICULocale: strPtr("und-u-ks-level2"), Deterministic: false,
			Body: "CREATE COLLATION public.c (PROVIDER = icu, LOCALE = 'und-u-ks-level2', DETERMINISTIC = false)",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP COLLATION IF EXISTS "public"."c"`) {
		t.Errorf("expected DROP COLLATION for a DETERMINISTIC change, got: %v", sqlList(ops))
	}
}

// TestDiffCollationRulesChangeRecreates guards RFC audit item #111: RULES
// was entirely absent from the significant-property comparison, so a
// RULES-only change was undetected drift, not just an unoptimized
// DROP+CREATE — real PostgreSQL's ALTER COLLATION has no way to alter RULES
// in place.
func TestDiffCollationRulesChangeRecreates(t *testing.T) {
	d := New()
	oldRules := "&a < b"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "i", CollationICULocale: strPtr("und"), CollationDeterministic: true,
			CollationRules: &oldRules,
		},
	})
	newRules := "&b < a"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "i", ICULocale: strPtr("und"), Deterministic: true,
			Rules: &newRules,
			Body:  "CREATE COLLATION public.c (PROVIDER = icu, LOCALE = 'und', RULES = '&b < a')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP COLLATION IF EXISTS "public"."c"`) {
		t.Errorf("expected DROP COLLATION for a RULES change, got: %v", sqlList(ops))
	}
}

// TestDiffCollationRulesUnspecifiedIsNoop proves a source that never
// mentions RULES doesn't drift against a snapshot that has one recorded
// (nil-means-unspecified, same convention as every other optional field).
func TestDiffCollationRulesUnspecifiedIsNoop(t *testing.T) {
	d := New()
	rules := "&a < b"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "i", CollationICULocale: strPtr("und"), CollationDeterministic: true,
			CollationRules: &rules,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "i", ICULocale: strPtr("und"), Deterministic: true,
			Body: "CREATE COLLATION public.c (PROVIDER = icu, LOCALE = 'und')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops when RULES is unspecified in source, got: %v", sqlList(ops))
	}
}

func TestDiffCollationUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged collation, got: %v", sqlList(ops))
	}
}

// TestDiffCollationCopyFromSkipsStructuredComparison is the regression
// guard for RFC Section 14.2's "FROM existing_collation" copy-from
// shorthand: a FROM-declared ir.Collation has none of its Provider/Collate/
// Ctype/ICULocale/Deterministic/Rules fields populated (DPG's Builder
// processes one object at a time with no visibility into another declared
// collation, so it genuinely cannot know what they resolve to) — comparing
// those hardcoded Go zero-value defaults (Provider "c", Deterministic true)
// against introspection's REAL resolved values (here deliberately set to
// something that would never match those defaults: icu/false/a populated
// ICULocale) would otherwise produce a permanent spurious DROP+CREATE on
// every single plan against an already-applied FROM-declared collation.
func TestDiffCollationCopyFromSkipsStructuredComparison(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c1_alias", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c1_alias", CollationStructured: true,
			CollationProvider: "i", CollationICULocale: strPtr("und-u-ks-level2"), CollationDeterministic: false,
		},
	})
	from := "c1"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c1_alias", CopyFrom: &from,
			Provider: "c", Deterministic: true, // Go zero-value defaults, never DPG's real understanding
			Body: "CREATE COLLATION public.c1_alias FROM c1",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	// Unlike the stale-snapshot fallback (which may emit a harmless one-
	// time "nothing else to say" placeholder as it self-heals), a
	// FROM-declared collation's opt-out is permanent, so this must be
	// truly zero ops every time — a placeholder here would show up as a
	// no-op change in every single `dpg plan` output, forever.
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged FROM-declared collation, got: %v", sqlList(ops))
	}
}

// TestDiffCreateCollationFrom proves a brand-new FROM-declared collation
// still creates via its (deparser-reconstructed) Body text, which already
// includes the FROM clause verbatim — CREATE-time emission needed no fix,
// only the ongoing comparison did.
func TestDiffCreateCollationFrom(t *testing.T) {
	d := New()
	from := "c1"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c1_alias", CopyFrom: &from,
			Body: "CREATE COLLATION public.c1_alias FROM public.c1",
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE COLLATION public.c1_alias FROM public.c1;") {
		t.Errorf("expected the FROM clause preserved verbatim, got: %v", sqlList(ops))
	}
}

// TestDiffCollationOwnerChanged guards RFC audit item #81: Collation had no
// Owner field at all.
func TestDiffCollationOwnerChanged(t *testing.T) {
	d := New()
	oldOwner := "app_admin"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
			CollationOwner: &oldOwner,
		},
	})
	newOwner := "app_readonly"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", Owner: &newOwner,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER COLLATION "public"."c" OWNER TO "app_readonly";`) {
		t.Errorf("expected ALTER COLLATION ... OWNER TO, got: %v", sqlList(ops))
	}
}

// TestCreateCollationWithOwner proves a brand-new collation declared with
// OWNER creates directly as that role (SET ROLE/RESET ROLE wrapping).
func TestCreateCollationWithOwner(t *testing.T) {
	owner := "audit_admin"
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", Owner: &owner,
		},
	}
	ops, err := New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SET ROLE "audit_admin"`) {
		t.Errorf("expected creation wrapped in SET ROLE audit_admin, got: %v", sqlList(ops))
	}
}

// TestDiffCollationRefreshVersionEmittedEveryPlan guards RFC audit item
// #84: REFRESH VERSION is an imperative action with no persisted state to
// diff against, so it must be unconditionally re-emitted on every plan for
// as long as it remains declared, Safety Caution, transactional
// (real PostgreSQL's ALTER COLLATION REFRESH VERSION has no restriction
// against running inside a transaction).
func TestDiffCollationRefreshVersionEmittedEveryPlan(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", RefreshVersion: true,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	op := findOpSQL(ops, `ALTER COLLATION "public"."c" REFRESH VERSION;`)
	if op == nil {
		t.Fatalf("expected ALTER COLLATION ... REFRESH VERSION, got: %v", sqlList(ops))
	}
	if op.Safety() != pipeline.Caution {
		t.Errorf("expected REFRESH VERSION to be Caution safety, got %s", op.Safety())
	}
	if op.Mode() != pipeline.ModeTransactional {
		t.Error("expected REFRESH VERSION to still run inside the migration's transaction")
	}

	// Re-diffing while still declared must re-emit it, not treat it as
	// already-applied drift — there is no persisted value to compare.
	ops2, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if findOpSQL(ops2, `ALTER COLLATION "public"."c" REFRESH VERSION;`) == nil {
		t.Fatalf("expected REFRESH VERSION to be re-emitted on a second plan while still declared, got: %v", sqlList(ops2))
	}
}

// TestDiffCollationRefreshVersionUnspecifiedIsNoop proves a source that
// never mentions REFRESH VERSION never emits it.
func TestDiffCollationRefreshVersionUnspecifiedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind: "collation",
		Opaque: &snapshot.SnapOpaque{
			Kind: "collation", Schema: "public", Name: "c", CollationStructured: true,
			CollationProvider: "c", CollationCollate: strPtr("C"), CollationCtype: strPtr("C"), CollationDeterministic: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "REFRESH VERSION") {
		t.Errorf("expected no REFRESH VERSION when never declared, got: %v", sqlList(ops))
	}
}

// TestCreateCollationWithRefreshVersion proves a brand-new collation
// declared with REFRESH VERSION emits the follow-up ALTER right after
// CREATE.
func TestCreateCollationWithRefreshVersion(t *testing.T) {
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')", RefreshVersion: true,
		},
	}
	ops, err := New().Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE COLLATION") {
		t.Fatalf("expected CREATE COLLATION, got: %v", sqlList(ops))
	}
	if findOpSQL(ops, `ALTER COLLATION "public"."c" REFRESH VERSION;`) == nil {
		t.Fatalf("expected a follow-up ALTER COLLATION ... REFRESH VERSION after CREATE, got: %v", sqlList(ops))
	}
}

func TestDiffCollationStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.c", &snapshot.SnapObject{
		Kind:   "collation",
		Opaque: &snapshot.SnapOpaque{Kind: "collation", Schema: "public", Name: "c"},
	})
	desired := []pipeline.IRObject{
		&ir.Collation{
			Schema: "public", Name: "c", Provider: "c", Collate: strPtr("C"), Ctype: strPtr("C"), Deterministic: true,
			Body: "CREATE COLLATION public.c (LOCALE = 'C')",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP COLLATION") {
		t.Errorf("stale snapshot must not trigger DROP COLLATION, got: %v", sqlList(ops))
	}
}

// ── StatisticsObject structured diffing (G-live Tier 4, closing) ──────────────
// Same G-live gap as every kind before it in this tier: Reconstructed
// forced BodyHash to "" on the live side, silently disabling diffOpaqueIR's
// comparison on verify/plan --live. diffStatisticsObject replaces that with
// field-level comparison, gated by the explicit StatisticsStructured
// sentinel (an empty Kinds/Columns list is not itself a reliable
// "unpopulated" signal).

func TestDiffStatisticsObjectKindsChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct", "dependencies"}, Columns: []string{"a", "b"},
			Body: "CREATE STATISTICS public.st (ndistinct, dependencies) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP STATISTICS IF EXISTS "public"."st"`) {
		t.Errorf("expected DROP STATISTICS for a kinds change, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP STATISTICS") && op.Safety() != pipeline.Destructive {
			t.Errorf("expected DROP STATISTICS to be Destructive (RFC Section 14.6), got %v: %s", op.Safety(), op.SQL())
		}
	}
}

func TestDiffStatisticsObjectColumnsChangeRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b", "c"},
			Body: "CREATE STATISTICS public.st (ndistinct) ON a, b, c FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP STATISTICS IF EXISTS "public"."st"`) {
		t.Errorf("expected DROP STATISTICS for a columns change, got: %v", sqlList(ops))
	}
}

func TestDiffStatisticsObjectUnqualifiedTableMatchesQualifiedNoOp(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			Body: "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for a schema-unqualified-vs-qualified table match, got: %v", sqlList(ops))
	}
}

func TestDiffStatisticsObjectTargetChangeIsSafeAlter(t *testing.T) {
	d := New()
	oldTarget := 100
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
			StatisticsTarget: &oldTarget,
		},
	})
	newTarget := 500
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			StatisticsTarget: &newTarget,
			Body:             "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (targeted ALTER STATISTICS SET STATISTICS), got %d: %v", len(ops), sqlList(ops))
	}
	sql := ops[0].SQL()
	if !strings.HasPrefix(sql, "ALTER STATISTICS") || !strings.Contains(sql, "SET STATISTICS 500") {
		t.Errorf("expected ALTER STATISTICS ... SET STATISTICS 500, got: %s", sql)
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER STATISTICS SET STATISTICS to be Safe (RFC Section 14.6), got %v: %s", ops[0].Safety(), sql)
	}
}

// TestDiffStatisticsObjectRenamedFromEmitsAlterRename is the regression
// guard for StatisticsObject rename detection: before this,
// ir.StatisticsObject had no RenamedFrom field at all, so a renamed
// statistics object was indistinguishable from "old one dropped, new one
// added."
func TestDiffStatisticsObjectRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st_old", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st_old", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
		},
	})
	old := "st_old"
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			RenamedFrom: &old,
			Body:        "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP STATISTICS") {
		t.Fatalf("renamed statistics object should match by RENAMED FROM, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER STATISTICS "public"."st_old" RENAME TO "st";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") && o.Safety() != pipeline.Caution {
			t.Errorf("expected CAUTION safety for ALTER STATISTICS RENAME TO, got %v", o.Safety())
		}
	}
}

// TestDiffStatisticsObjectRenamedFromCrossSchema is diffTable's
// TestDiffRenameTableCrossSchema counterpart for StatisticsObject.
func TestDiffStatisticsObjectRenamedFromCrossSchema(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("old_schema.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "old_schema", Name: "st", StatisticsStructured: true,
			StatisticsTable: "old_schema.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
		},
	})
	old := "st"
	oldSchema := "old_schema"
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "new_schema", Name: "st", Table: "old_schema.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			RenamedFrom: &old, RenamedFromSchema: &oldSchema,
			Body: "CREATE STATISTICS new_schema.st (ndistinct) ON a, b FROM old_schema.orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP STATISTICS") {
		t.Fatalf("cross-schema rename should match the existing statistics object, not drop+create, got: %v", sqlList(ops))
	}
	want := `ALTER STATISTICS "old_schema"."st" SET SCHEMA "new_schema";`
	if !containsSQL(ops, want) {
		t.Errorf("expected %q, got: %v", want, sqlList(ops))
	}
}

// TestDiffStatisticsObjectRenamedFromAndColumnsChanged confirms a
// simultaneous rename + column-list change still requires DROP+CREATE (no
// ALTER STATISTICS path changes the column list), with no redundant
// separate rename — createOpaque already creates under the final (new)
// name.
func TestDiffStatisticsObjectRenamedFromAndColumnsChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st_old", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st_old", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a"},
		},
	})
	old := "st_old"
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			RenamedFrom: &old,
			Body:        "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP STATISTICS IF EXISTS "public"."st_old"`) {
		t.Errorf("expected DROP referencing the statistics object's actual (old) name, got: %v", sqlList(ops))
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "RENAME TO") {
			t.Errorf("did not expect a separate RENAME TO alongside drop+create, got: %v", sqlList(ops))
		}
	}
}

// TestDiffStatisticsObjectRenamedFromStaleErrors mirrors every other kind's
// identical stale-RENAMED-FROM validation.
func TestDiffStatisticsObjectRenamedFromStaleErrors(t *testing.T) {
	d := New()
	old := "nonexistent_old_st"
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{Schema: "public", Name: "st", RenamedFrom: &old, Body: "CREATE STATISTICS public.st ON a FROM orders"},
	}
	_, err := d.Diff(desired, &pipeline.Snapshot{})
	if err == nil {
		t.Fatal("expected an error for a stale RENAMED FROM matching no snapshot statistics object")
	}
}

func TestDiffStatisticsObjectTargetResetToDefault(t *testing.T) {
	d := New()
	oldTarget := 100
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
			StatisticsTarget: &oldTarget,
		},
	})
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			Body: "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || !strings.Contains(ops[0].SQL(), "SET STATISTICS -1") {
		t.Errorf("expected ALTER STATISTICS ... SET STATISTICS -1, got: %v", sqlList(ops))
	}
}

func TestDiffStatisticsObjectUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind: "statistics",
		Opaque: &snapshot.SnapOpaque{
			Kind: "statistics", Schema: "public", Name: "st", StatisticsStructured: true,
			StatisticsTable: "public.orders", StatisticsKinds: []string{"ndistinct"}, StatisticsColumns: []string{"a", "b"},
		},
	})
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			Body: "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected zero ops for an unchanged statistics object, got: %v", sqlList(ops))
	}
}

func TestDiffStatisticsObjectStaleSnapshotDoesNotRecreate(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.st", &snapshot.SnapObject{
		Kind:   "statistics",
		Opaque: &snapshot.SnapOpaque{Kind: "statistics", Schema: "public", Name: "st"},
	})
	desired := []pipeline.IRObject{
		&ir.StatisticsObject{
			Schema: "public", Name: "st", Table: "public.orders", Kinds: []string{"ndistinct"}, Columns: []string{"a", "b"},
			Body: "CREATE STATISTICS public.st (ndistinct) ON a, b FROM orders",
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP STATISTICS") {
		t.Errorf("stale snapshot must not trigger DROP STATISTICS, got: %v", sqlList(ops))
	}
}

// ── Table PROTECTED / DROP CASCADE (RFC Section 7.11) ────────────────────────────────
// Regression guard for a real bug found live-testing PROTECTED against a
// demo project: SnapTable.Protected was populated all the way from source
// but the Pass-2 deletion loop never read it back — a PROTECTED table was
// silently DROPped exactly like any other, with zero actual protection
// (RFC Section 15.10 Phase 9 Pass 3 / DPG-E022 says the diff must instead error).
// DROP CASCADE was already correctly wired (found already-working while
// investigating), so its test here is a regression guard, not a bug fix.

func TestDiffProtectedTableDropIsBlocked(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.locked", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:    "public",
			Name:      "locked",
			Columns:   []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Protected: true,
		},
	})
	_, err := d.Diff(nil, snap)
	if err == nil {
		t.Fatal("expected an error blocking the drop of a PROTECTED table, got nil")
	}
	if !strings.Contains(err.Error(), "PROTECTED") {
		t.Errorf("expected error to mention PROTECTED, got: %v", err)
	}
}

func TestDiffUnprotectedTableDropSucceeds(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.plain", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "plain",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsSQL(ops, `DROP TABLE IF EXISTS "public"."plain";`) {
		t.Errorf("expected normal DROP TABLE for an unprotected table, got: %v", sqlList(ops))
	}
}

// ── "public" schema lifecycle guard (RFC audit item C.2) ───────────────────
// Regression guard: once introspectSchemas stopped hard-excluding "public"
// (so its live Owner/Comment/Grants become visible to plan --live/verify),
// a project with a schemas/public/ directory but no explicit `SCHEMA
// public { }` declaration would otherwise get a spurious DESTRUCTIVE
// DROP SCHEMA IF EXISTS "public" on every plan --live, since desired never
// carries an object for it but the live snapshot now does. "public" always
// exists in PostgreSQL and must never be proposed for DROP regardless of
// whether desired declares it.

func TestDiffNeverDropsPublicSchemaEvenWhenAbsentFromDesired(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	owner := "dpg"
	_ = snap.SetObject("public", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "public", Owner: &owner},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if containsSQL(ops, "DROP SCHEMA") {
		t.Errorf("must never propose DROP SCHEMA for public, got: %v", sqlList(ops))
	}
}

func TestDiffOtherSchemaStillDroppedWhenAbsentFromDesired(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("staging", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "staging"},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsSQL(ops, `DROP SCHEMA IF EXISTS "staging";`) {
		t.Errorf("expected normal DROP SCHEMA for a non-public schema absent from desired, got: %v", sqlList(ops))
	}
}

func TestDiffPublicSchemaGrantChangeStillDetectedWhenDeclared(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public", &snapshot.SnapObject{
		Kind:   "schema",
		Schema: &snapshot.SnapSchema{Name: "public"},
	})
	desired := []pipeline.IRObject{
		&ir.Schema{
			Name:   "public",
			Grants: []ir.Grant{{Privileges: []string{"USAGE"}, Roles: []string{"audit_role"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsSQL(ops, `GRANT USAGE ON SCHEMA "public" TO "audit_role"`) {
		t.Errorf("expected GRANT USAGE ON SCHEMA public when explicitly declared, got: %v", sqlList(ops))
	}
}

func TestDiffTableDropCascadeEmitsCascade(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.plain", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:      "public",
			Name:        "plain",
			Columns:     []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			DropCascade: true,
		},
	})
	ops, err := d.Diff(nil, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsSQL(ops, `DROP TABLE IF EXISTS "public"."plain" CASCADE;`) {
		t.Errorf("expected DROP TABLE ... CASCADE, got: %v", sqlList(ops))
	}
}

// TestDiffTableProtectedRemovedEmitsOp guards the OTHER half of the same
// bug: PROTECTED has no PG DDL equivalent, so removing it (table still
// desired) produced zero DiffOps — and apply's len(ops)==0 "already up to
// date" short-circuit means the snapshot's stale Protected=true was never
// cleared, permanently blocking the RFC's own documented "remove PROTECTED
// first, then drop" two-step workflow. Live-confirmed via the demo project:
// this exact sequence (unprotect, apply, then remove the table, apply)
// before the fix left the table permanently undroppable.
func TestDiffTableProtectedRemovedEmitsOp(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.locked", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:    "public",
			Name:      "locked",
			Columns:   []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Protected: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "locked",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected a non-empty op set so apply doesn't short-circuit and skip the snapshot refresh")
	}
	if !containsSQL(ops, "PROTECTED") {
		t.Errorf("expected an op reflecting the PROTECTED removal, got: %v", sqlList(ops))
	}
}

func TestDiffTableProtectedUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.plain", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "plain",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "plain",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unprotected table staying unprotected, got: %v", sqlList(ops))
	}
}

// ── Column type-change USING safety (RFC Section 7.2) ────────────────────────────────
// Regression guard for a real bug found live-testing COLUMN { USING expr; }
// against a demo project: the USING expression WAS correctly appended to the
// emitted ALTER COLUMN TYPE SQL, but the op's safety was hardcoded
// destructiveOp unconditionally — so a user-supplied, correct USING
// conversion was still treated as DESTRUCTIVE (blocked without
// --allow-destructive) exactly like a bare, unguarded type change. RFC Section 7.2
// is explicit: "unless a USING expression is present ... in which case it
// is classified CAUTION."

func TestDiffColumnTypeChangeWithUsingIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "code", Type: "text"}},
		},
	})
	using := "code::integer"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "code", Type: ir.TypeRef{Name: "integer"}, Using: &using}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER COLUMN") {
			found = o
		}
	}
	if found == nil {
		t.Fatalf("expected an ALTER COLUMN TYPE op, got: %v", sqlList(ops))
	}
	if !strings.Contains(found.SQL(), "USING code::integer") {
		t.Errorf("expected USING clause in the emitted SQL, got: %s", found.SQL())
	}
	if found.Safety() != pipeline.Caution {
		t.Errorf("safety = %v, want Caution (USING clause supplied)", found.Safety())
	}
}

func TestDiffColumnTypeChangeWithoutUsingIsDestructive(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "code", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "code", Type: ir.TypeRef{Name: "integer"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	var found pipeline.DiffOp
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER COLUMN") {
			found = o
		}
	}
	if found == nil {
		t.Fatalf("expected an ALTER COLUMN TYPE op, got: %v", sqlList(ops))
	}
	if strings.Contains(found.SQL(), "USING") {
		t.Errorf("expected no USING clause when none was declared, got: %s", found.SQL())
	}
	if found.Safety() != pipeline.Destructive {
		t.Errorf("safety = %v, want Destructive (no USING clause, cast compatibility unknown offline)", found.Safety())
	}
}

// ── Table-level policy + grant emission at CREATE time ───────────────────────
// createPolicy and tableGrantOp (used for a GRANT declared inline on a new
// table) had zero unit coverage — only proven live via integration tests.

func TestDiffCreateTableWithPolicyAndGrant(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "t",
			Columns: []*ir.Column{
				{Name: "id", Type: ir.TypeRef{Name: "integer"}},
			},
			Policies: []*ir.Policy{
				{Name: "p_owner", Permissive: true, Command: "SELECT", Using: strPtr("owner_id = current_user_id()")},
			},
			Grants: []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "CREATE POLICY") || !containsSQL(ops, `"p_owner"`) ||
		!containsSQL(ops, "FOR SELECT") || !containsSQL(ops, "USING (owner_id = current_user_id())") {
		t.Errorf("expected CREATE POLICY on new table, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "GRANT SELECT") || !containsSQL(ops, "app_readonly") {
		t.Errorf("expected inline GRANT on new table, got: %v", sqlList(ops))
	}
}

// TestDiffCreatePolicyToPublicNotQuoted is createPolicy's PUBLIC-role
// sibling: a POLICY's TO clause used its own inline quoting loop (not
// roleList), carrying the same bug — PUBLIC must render bare.
func TestDiffCreatePolicyToPublicNotQuoted(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}},
			Policies: []*ir.Policy{
				{Name: "p_all", Permissive: true, Command: "SELECT", Roles: []string{"PUBLIC"}},
			},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "TO PUBLIC") {
		t.Errorf("expected unquoted TO PUBLIC, got: %v", sqlList(ops))
	}
	if containsSQL(ops, `"PUBLIC"`) {
		t.Errorf("PUBLIC must not be quoted, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyRoleListChangedIsDetected is the regression guard for RFC
// audit item #19: SnapPolicy had no Roles field at all, so diffPolicies
// could never see a TO role-list change — an edited policy silently kept
// its old role list forever, on both plan/apply and --live verify.
func TestDiffPolicyRoleListChangedIsDetected(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "p_owner", Command: "SELECT", Permissive: true, Roles: []string{"app_readonly"}},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "p_owner", Command: "SELECT", Permissive: true, Roles: []string{"app_admin"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	// RFC audit item #77: a Roles-only change is a targeted ALTER POLICY,
	// not a drop+recreate (real PostgreSQL's ALTER POLICY ... TO ... covers
	// it in one atomic, SAFE statement).
	if containsSQL(ops, "DROP POLICY") {
		t.Errorf("expected no DROP POLICY for a roles-only change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER POLICY "p_owner" ON "public"."t" TO "app_admin";`) {
		t.Errorf("expected ALTER POLICY ... TO app_admin, got: %v", sqlList(ops))
	}
	if ops[0].Safety() != pipeline.Safe {
		t.Errorf("expected ALTER POLICY to be Safe, got %s", ops[0].Safety())
	}
}

// TestDiffPolicyUsingChangedIsAlterPolicy guards RFC audit item #77's other
// half: a USING-only change also gets a targeted ALTER POLICY rather than
// drop+recreate.
func TestDiffPolicyUsingChangedIsAlterPolicy(t *testing.T) {
	d := New()
	oldUsing, newUsing := "owner_id = current_user_id()", "owner_id = current_user_id() OR is_admin()"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "p_owner", Command: "SELECT", Permissive: true, Using: oldUsing},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "p_owner", Command: "SELECT", Permissive: true, Using: &newUsing},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP POLICY") {
		t.Errorf("expected no DROP POLICY for a USING-only change, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "ALTER POLICY") || !containsSQL(ops, "is_admin()") {
		t.Errorf("expected ALTER POLICY reflecting the new USING expr, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyCommandChangedStillDropsAndRecreates proves the boundary of
// #77's fix: Command and Permissive have no ALTER POLICY clause in real
// PostgreSQL, so a change to either must still be a CAUTION drop+recreate.
func TestDiffPolicyCommandChangedStillDropsAndRecreates(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "p_owner", Command: "SELECT", Permissive: true},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "p_owner", Command: "ALL", Permissive: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP POLICY IF EXISTS "p_owner"`) {
		t.Errorf("expected DROP+CREATE for a Command change, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyUsingRemovedStillDropsAndRecreates proves the other boundary:
// ALTER POLICY can only replace an existing USING/WITH CHECK expression, it
// cannot clear one back to unset — going from set to unset needs
// drop+recreate.
func TestDiffPolicyUsingRemovedStillDropsAndRecreates(t *testing.T) {
	d := New()
	oldUsing := "owner_id = current_user_id()"
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "p_owner", Command: "SELECT", Permissive: true, Using: oldUsing},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "p_owner", Command: "SELECT", Permissive: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `DROP POLICY IF EXISTS "p_owner"`) {
		t.Errorf("expected DROP+CREATE when USING is removed, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyUnspecifiedRolesEqualsExplicitPublicIsNoop proves the
// normalization companion to the fix above: a policy that never wrote a TO
// clause (Roles == nil, PostgreSQL's own implicit default is PUBLIC) must
// not drift against a snapshot/live-introspected value of explicit
// ["PUBLIC"] — introspection always reports a concrete role, never nil.
func TestDiffPolicyUnspecifiedRolesEqualsExplicitPublicIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "p_owner", Command: "SELECT", Permissive: true, Roles: []string{"PUBLIC"}},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "p_owner", Command: "SELECT", Permissive: true},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops: unspecified TO clause must be treated as equal to explicit PUBLIC, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyRenamedFromEmitsAlterRename guards RFC Section 7.8's policy
// RENAMED FROM: previously ir.Policy had no such field at all, so a renamed
// policy misdiffed as a drop of the old name plus a create of the new one
// (losing the atomic, metadata-only ALTER POLICY ... RENAME TO real
// PostgreSQL provides), despite RFC E.19 claiming this was already closed.
func TestDiffPolicyRenamedFromEmitsAlterRename(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "view_own", Command: "SELECT", Permissive: true, Using: "true"},
			},
		},
	})
	renamedFrom := "view_own"
	using := "true"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "view_self", Command: "SELECT", Permissive: true, Using: &using, RenamedFrom: &renamedFrom},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER POLICY "view_own" ON "public"."t" RENAME TO "view_self";`) {
		t.Errorf("expected ALTER POLICY ... RENAME TO, got: %v", sqlList(ops))
	}
	if containsSQL(ops, "DROP POLICY") {
		t.Errorf("expected no DROP POLICY for a rename-only change, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "RENAME TO") && op.Safety() != pipeline.Safe {
			t.Errorf("policy rename safety = %v, want Safe (metadata-only sub-object rename): %s", op.Safety(), op.SQL())
		}
	}
}

// TestDiffPolicyRenamedFromAndUsingChanged guards RFC E.19's documented
// "renamed AND TO/USING/WITH CHECK also differ" case: two statements, RENAME
// first then ALTER POLICY under the new name.
func TestDiffPolicyRenamedFromAndUsingChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "view_own", Command: "SELECT", Permissive: true, Using: "owner_id = current_user_id()"},
			},
		},
	})
	renamedFrom := "view_own"
	newUsing := "owner_id = current_user_id() OR is_admin()"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "view_self", Command: "SELECT", Permissive: true, Using: &newUsing, RenamedFrom: &renamedFrom},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER POLICY "view_own" ON "public"."t" RENAME TO "view_self";`) {
		t.Errorf("expected the rename statement, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER POLICY "view_self" ON "public"."t"`) || !containsSQL(ops, "is_admin()") {
		t.Errorf("expected a second ALTER POLICY under the new name reflecting the USING change, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyRenamedFromPostApplyNoop proves a RENAMED FROM directive
// still present in source after a successful rename is a no-op, not an
// error or a repeated rename — the snapshot already carries the new name,
// mirroring the same three-state resolution Index/Constraint's identical
// RENAMED FROM mechanisms already use.
func TestDiffPolicyRenamedFromPostApplyNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
			Policies: []snapshot.SnapPolicy{
				{Name: "view_self", Command: "SELECT", Permissive: true, Using: "true"},
			},
		},
	})
	renamedFrom := "view_own"
	using := "true"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "view_self", Command: "SELECT", Permissive: true, Using: &using, RenamedFrom: &renamedFrom},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for a post-apply stale RENAMED FROM, got: %v", sqlList(ops))
	}
}

// TestDiffPolicyRenamedFromStaleErrors proves a RENAMED FROM directive whose
// old name matches neither the current nor a prior snapshot name errors
// clearly instead of silently doing nothing or misfiring a rename.
func TestDiffPolicyRenamedFromStaleErrors(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.t", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "t",
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "bigint"}},
		},
	})
	renamedFrom := "nonexistent_policy"
	using := "true"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "bigint"}}},
			Policies: []*ir.Policy{
				{Name: "view_self", Command: "SELECT", Permissive: true, Using: &using, RenamedFrom: &renamedFrom},
			},
		},
	}
	_, err := d.Diff(desired, snap)
	if err == nil {
		t.Fatal("expected an error for a RENAMED FROM matching neither name in the snapshot")
	}
}

// ── trivial helpers (coverage tail, see .dpg-notes/dpg-status-accounting.md Section 9's
// "#5 diff coverage push, remaining tail") ─────────────────────────────────────

// TestOpPos guards the op.Pos() accessor — trivial but was the last uncovered
// method on the pipeline.DiffOp implementation.
func TestOpPos(t *testing.T) {
	pos := pipeline.SourcePos{File: "t.dpg", Line: 3, Col: 7}
	o := safeOp("SELECT 1;", pos)
	if o.Pos() != pos {
		t.Errorf("Pos(): got %+v, want %+v", o.Pos(), pos)
	}
}

// TestPtrStr covers both branches of the nil-safe string-pointer dereference.
func TestPtrStr(t *testing.T) {
	if got := ptrStr(nil); got != "" {
		t.Errorf("ptrStr(nil): got %q, want empty", got)
	}
	s := "hello"
	if got := ptrStr(&s); got != "hello" {
		t.Errorf("ptrStr(&s): got %q, want hello", got)
	}
}

// TestInt64PtrEq covers all 4 branches of the nil-safe int64-pointer
// comparison: both nil, one nil, both set equal, both set different.
func TestInt64PtrEq(t *testing.T) {
	a, b := int64(5), int64(5)
	c := int64(6)
	cases := []struct {
		name     string
		x, y     *int64
		expected bool
	}{
		{"both nil", nil, nil, true},
		{"x nil", nil, &a, false},
		{"y nil", &a, nil, false},
		{"equal", &a, &b, true},
		{"different", &a, &c, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := int64PtrEq(tc.x, tc.y); got != tc.expected {
				t.Errorf("int64PtrEq(%v, %v): got %v, want %v", tc.x, tc.y, got, tc.expected)
			}
		})
	}
}

// ── Composite type attribute diffing (RFC Section 5.2, RFC audit items #12/#13) ────
// Regression guards for two related bugs: buildCompositeType never read
// block.Columns at all, so a declared `COLUMN attr { RENAMED FROM old; }`
// sub-block was silently ignored (a rename looked like an unrelated
// drop+add); and diffType treated ANY composite attribute change —
// including a pure addition — as a bare DROP TYPE + recreate instead of the
// RFC's granular, safety-differentiated ADD/DROP/ALTER/RENAME ATTRIBUTE ops.

func compositeType(schema, name string, attrs ...*ir.Column) *ir.Type {
	return &ir.Type{Schema: schema, Name: name, Variant: "COMPOSITE", CompositeAttrs: attrs}
}

func compositeSnap(schema, name string, attrs ...snapshot.SnapColumn) *snapshot.SnapObject {
	return &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: schema, Name: name, Variant: "COMPOSITE", CompositeAttrs: attrs},
	}
}

func TestDiffCompositeAttrUnchangedIsNoop(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr",
		snapshot.SnapColumn{Name: "a", Type: "integer"},
		snapshot.SnapColumn{Name: "b", Type: "text"},
	))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "a", Type: ir.TypeRef{Name: "integer"}},
			&ir.Column{Name: "b", Type: ir.TypeRef{Name: "text"}},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops for an unchanged composite type, got: %v", sqlList(ops))
	}
}

// TestDiffCompositeAttrAdded proves a pure addition emits the RFC's granular
// ALTER TYPE ADD ATTRIBUTE (SAFE), not a DROP TYPE + recreate.
func TestDiffCompositeAttrAdded(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr",
		snapshot.SnapColumn{Name: "a", Type: "integer"},
	))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "a", Type: ir.TypeRef{Name: "integer"}},
			&ir.Column{Name: "b", Type: ir.TypeRef{Name: "text"}},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") {
		t.Errorf("a pure addition must not DROP TYPE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "public"."addr" ADD ATTRIBUTE "b" text;`) {
		t.Errorf("expected ALTER TYPE ... ADD ATTRIBUTE, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "ADD ATTRIBUTE") && op.Safety() != pipeline.Safe {
			t.Errorf("ADD ATTRIBUTE safety = %v, want Safe", op.Safety())
		}
	}
}

// TestDiffCompositeAttrDropped proves a pure removal emits ALTER TYPE DROP
// ATTRIBUTE (DESTRUCTIVE), not a blanket DROP TYPE + recreate.
func TestDiffCompositeAttrDropped(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr",
		snapshot.SnapColumn{Name: "a", Type: "integer"},
		snapshot.SnapColumn{Name: "b", Type: "text"},
	))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "a", Type: ir.TypeRef{Name: "integer"}},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") {
		t.Errorf("a pure drop must not use DROP TYPE, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "public"."addr" DROP ATTRIBUTE "b";`) {
		t.Errorf("expected ALTER TYPE ... DROP ATTRIBUTE, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "DROP ATTRIBUTE") && op.Safety() != pipeline.Destructive {
			t.Errorf("DROP ATTRIBUTE safety = %v, want Destructive", op.Safety())
		}
	}
}

// TestDiffCompositeAttrTypeChanged proves a type change on an existing,
// unrenamed attribute emits ALTER TYPE ALTER ATTRIBUTE ... TYPE
// (DESTRUCTIVE).
func TestDiffCompositeAttrTypeChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr",
		snapshot.SnapColumn{Name: "a", Type: "integer"},
	))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "a", Type: ir.TypeRef{Name: "text"}},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TYPE "public"."addr" ALTER ATTRIBUTE "a" TYPE text;`) {
		t.Errorf("expected ALTER TYPE ... ALTER ATTRIBUTE ... TYPE, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "ALTER ATTRIBUTE") && op.Safety() != pipeline.Destructive {
			t.Errorf("ALTER ATTRIBUTE safety = %v, want Destructive", op.Safety())
		}
	}
}

// TestDiffCompositeAttrRenamed proves a builder-populated RenamedFrom (RFC
// Section 5.2: "the same [COLUMN] mechanism applies to composite type attributes")
// emits ALTER TYPE RENAME ATTRIBUTE, not an unrelated drop+add of two
// differently-named attributes.
func TestDiffCompositeAttrRenamed(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr",
		snapshot.SnapColumn{Name: "old_name", Type: "text"},
	))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "new_name", Type: ir.TypeRef{Name: "text"}, RenamedFrom: strPtr("old_name")},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP ATTRIBUTE") || containsSQL(ops, "ADD ATTRIBUTE") {
		t.Errorf("a rename must not drop+add, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "public"."addr" RENAME ATTRIBUTE "old_name" TO "new_name";`) {
		t.Errorf("expected ALTER TYPE ... RENAME ATTRIBUTE, got: %v", sqlList(ops))
	}
}

// TestDiffCompositeAttrAddedWithCollation proves RFC Section 5.2's attribute
// COLLATE clause round-trips through ADD ATTRIBUTE as a trailing clause on
// the type, not a separate statement.
func TestDiffCompositeAttrAddedWithCollation(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr"))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "street", Type: ir.TypeRef{Name: "text"}, Collation: "en_US"},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TYPE "public"."addr" ADD ATTRIBUTE "street" text COLLATE "en_US";`) {
		t.Errorf("expected ADD ATTRIBUTE with COLLATE, got: %v", sqlList(ops))
	}
}

// TestDiffCompositeAttrCollationChanged proves a COLLATE-only change (same
// type) is still DESTRUCTIVE via ALTER ATTRIBUTE TYPE, matching RFC Section 5.2's
// diffing table, which groups "type or COLLATE" as one row.
func TestDiffCompositeAttrCollationChanged(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.addr", compositeSnap("public", "addr",
		snapshot.SnapColumn{Name: "street", Type: "text", Collation: "en_US"},
	))
	desired := []pipeline.IRObject{
		compositeType("public", "addr",
			&ir.Column{Name: "street", Type: ir.TypeRef{Name: "text"}, Collation: "de_DE"},
		),
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER TYPE "public"."addr" ALTER ATTRIBUTE "street" TYPE text COLLATE "de_DE";`) {
		t.Errorf("expected ALTER ATTRIBUTE ... TYPE ... COLLATE, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), "ALTER ATTRIBUTE") && op.Safety() != pipeline.Destructive {
			t.Errorf("ALTER ATTRIBUTE (collation-only change) safety = %v, want Destructive", op.Safety())
		}
	}
}

// ── Column type change: implicit-cast safety classification ────────────────

// TestDiffColumnTypeImplicitCastNoUsingIsCaution proves RFC Section 17.2's
// "ALTER TABLE ALTER COLUMN TYPE (implicit cast) -> CAUTION" row: widening
// smallint -> integer with no USING clause must be CAUTION, not the
// previous hardcoded DESTRUCTIVE default, since PostgreSQL has a real
// implicit cast between them (verified live against a real pg_cast, see
// implicit_casts.go).
func TestDiffColumnTypeImplicitCastNoUsingIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "qty", Type: "smallint"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "qty", Type: ir.TypeRef{Name: "integer"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER COLUMN "qty" TYPE integer`) {
		t.Fatalf("expected an ALTER COLUMN TYPE op, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), `ALTER COLUMN "qty" TYPE integer`) {
			if op.Safety() != pipeline.Caution {
				t.Errorf("expected Caution for an implicit-cast widening with no USING, got %s", op.Safety())
			}
		}
	}
}

// TestDiffColumnTypeNoImplicitCastNoUsingStaysDestructive is the negative
// case: text -> integer has no implicit cast (can fail at runtime on
// non-numeric data) and no USING clause is given, so it must stay the
// conservative DESTRUCTIVE default — proving the implicit-cast table only
// ever ADDS precision, never silently downgrades a genuinely risky change.
func TestDiffColumnTypeNoImplicitCastNoUsingStaysDestructive(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "code", Type: "text"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "code", Type: ir.TypeRef{Name: "integer"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER COLUMN "code" TYPE integer`) {
		t.Fatalf("expected an ALTER COLUMN TYPE op, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), `ALTER COLUMN "code" TYPE integer`) {
			if op.Safety() != pipeline.Destructive {
				t.Errorf("expected Destructive when no implicit cast exists and no USING is given, got %s", op.Safety())
			}
		}
	}
}

// TestDiffColumnTypeExplicitUsingStillCautionRegardlessOfImplicitCast proves
// an explicit USING clause is still respected exactly as before — CAUTION —
// even for a pair that also happens to have no implicit cast, and that the
// USING clause's own SQL text still renders (the implicit-cast table must
// never suppress a user-supplied USING expression).
func TestDiffColumnTypeExplicitUsingStillCautionRegardlessOfImplicitCast(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "code", Type: "text"}},
		},
	})
	using := "code::integer"
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "widgets",
			Columns: []*ir.Column{
				{Name: "code", Type: ir.TypeRef{Name: "integer"}, Using: &using},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, "USING code::integer") {
		t.Fatalf("expected the explicit USING clause to render, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), `ALTER COLUMN "code" TYPE integer`) {
			if op.Safety() != pipeline.Caution {
				t.Errorf("expected Caution for an explicit USING clause, got %s", op.Safety())
			}
		}
	}
}

// ── Legacy bare timestamp/time/varbit snapshot self-heal ───────────────────

// TestDiffColumnsLegacyTimestampSnapshotNoDrift proves a pre-2026-08-17
// snapshot's stale "timestamp"/"time"/"varbit" Type string (the un-mapped
// short spelling, before PGCatalogName started aliasing them to their full
// canonical form) self-heals to zero drift instead of showing a permanent
// spurious DESTRUCTIVE ALTER COLUMN TYPE — the exact upgrade-compatibility
// concern the new "time"/"timestamp"/"varbit" mappings introduced, guarded
// the same way isLegacySerialTypeName already guards the SERIAL rename.
func TestDiffColumnsLegacyTimestampSnapshotNoDrift(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public",
			Name:   "widgets",
			Columns: []snapshot.SnapColumn{
				{Name: "created", Type: "timestamp"},
				{Name: "opens", Type: "time(2)"},
				{Name: "flags", Type: "varbit(8)"},
			},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "widgets",
			Columns: []*ir.Column{
				{Name: "created", Type: ir.TypeRef{Name: "timestamp without time zone"}},
				{Name: "opens", Type: ir.TypeRef{Name: "time without time zone", Mods: "(2)"}},
				{Name: "flags", Type: ir.TypeRef{Name: "bit varying", Mods: "(8)"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ALTER COLUMN") && strings.Contains(o.SQL(), "TYPE") {
			t.Errorf("expected no ALTER COLUMN TYPE against a legacy timestamp/time/varbit snapshot, got: %s", o.SQL())
		}
	}
}

// TestDiffColumnsLegacyTimestampSnapshotStillCatchesRealChange proves the
// self-heal guard is narrow — a genuine type change starting from a legacy
// snapshot value must still be detected, not accidentally swallowed by the
// new guard.
func TestDiffColumnsLegacyTimestampSnapshotStillCatchesRealChange(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "created", Type: "timestamp"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema: "public",
			Name:   "widgets",
			Columns: []*ir.Column{
				{Name: "created", Type: ir.TypeRef{Name: "timestamp with time zone"}},
			},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER COLUMN "created" TYPE timestamp with time zone`) {
		t.Errorf("expected a genuine ALTER COLUMN TYPE for timestamp -> timestamptz, got: %v", sqlList(ops))
	}
}

// ── Column type change: typmod-widening safety classification ──────────────

// TestDiffColumnTypeVarcharWideningNoUsingIsCaution proves RFC Section 7.2's own
// primary example end to end: VARCHAR(10) -> VARCHAR(20) with no USING must
// be CAUTION, not DESTRUCTIVE — this was the RFC's literal illustrative
// case for "a type change PostgreSQL can apply implicitly," and it stayed
// unimplemented even after the different-base-type implicit-cast case
// (hasImplicitCast) was fixed, since pg_cast has no entry for a type to
// itself.
func TestDiffColumnTypeVarcharWideningNoUsingIsCaution(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "label", Type: "character varying(10)"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "label", Type: ir.TypeRef{Name: "character varying", Mods: "(20)"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ALTER COLUMN "label" TYPE character varying(20)`) {
		t.Fatalf("expected an ALTER COLUMN TYPE op, got: %v", sqlList(ops))
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), `ALTER COLUMN "label" TYPE character varying(20)`) {
			if op.Safety() != pipeline.Caution {
				t.Errorf("expected Caution for VARCHAR(10) -> VARCHAR(20) with no USING, got %s", op.Safety())
			}
		}
	}
}

// TestDiffColumnTypeVarcharShrinkingNoUsingStaysDestructive is the negative
// case: shrinking must stay the conservative DESTRUCTIVE default, since it
// can genuinely fail/truncate at apply time.
func TestDiffColumnTypeVarcharShrinkingNoUsingStaysDestructive(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "label", Type: "character varying(20)"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "label", Type: ir.TypeRef{Name: "character varying", Mods: "(10)"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), `ALTER COLUMN "label" TYPE character varying(10)`) {
			if op.Safety() != pipeline.Destructive {
				t.Errorf("expected Destructive for VARCHAR(20) -> VARCHAR(10), got %s", op.Safety())
			}
		}
	}
}

// TestDiffColumnTypeNumericScaleShrinkStaysDestructive proves the genuinely
// surprising numeric case reaches the real diff path too: PostgreSQL
// silently rounds on a scale-only shrink (no error), so this must stay
// DESTRUCTIVE even though precision alone doesn't decrease.
func TestDiffColumnTypeNumericScaleShrinkStaysDestructive(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.widgets", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema:  "public",
			Name:    "widgets",
			Columns: []snapshot.SnapColumn{{Name: "price", Type: "numeric(10,4)"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{
			Schema:  "public",
			Name:    "widgets",
			Columns: []*ir.Column{{Name: "price", Type: ir.TypeRef{Name: "numeric", Mods: "(10,1)"}}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if strings.Contains(op.SQL(), `ALTER COLUMN "price" TYPE numeric(10,1)`) {
			if op.Safety() != pipeline.Destructive {
				t.Errorf("expected Destructive for numeric(10,4) -> numeric(10,1) (silent rounding), got %s", op.Safety())
			}
		}
	}
}

// ── SECURITY LABEL (RFC Section 14.11) ─────────────────────────────────────────────
// Regression guard for the whole feature: no ir kind previously had a
// SecurityLabels field at all, so a declared "SECURITY LABEL [FOR
// provider] '...'" directive had nowhere to go at any layer. Covers the
// generic diffSecurityLabelSet/createSecurityLabelOps helpers directly,
// then a representative sample of the real per-kind wiring — especially
// the keyword-selection cases that are easy to get backwards (TABLE vs
// FOREIGN TABLE, VIEW vs MATERIALIZED VIEW, TYPE vs DOMAIN, and
// PROCEDURE/AGGREGATE both using "FUNCTION" per real PostgreSQL grammar,
// confirmed live) and the opaque-kind stale-snapshot self-heal path.

func TestSecurityLabelSQL(t *testing.T) {
	classified := "classified"
	cases := []struct {
		name     string
		provider string
		label    *string
		want     string
	}{
		{"unqualified", "", &classified, "SECURITY LABEL ON TABLE \"t\" IS 'classified';"},
		{"for provider", "dummy", &classified, "SECURITY LABEL FOR \"dummy\" ON TABLE \"t\" IS 'classified';"},
		{"remove (IS NULL)", "dummy", nil, "SECURITY LABEL FOR \"dummy\" ON TABLE \"t\" IS NULL;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := securityLabelSQL(tc.provider, `TABLE "t"`, tc.label)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiffSecurityLabelSetGeneric(t *testing.T) {
	pos := pipeline.SourcePos{}
	snap := []snapshot.SnapSecurityLabel{
		{Provider: "dummy", Label: "classified"},     // unchanged
		{Provider: "selinux", Label: "unclassified"}, // removed
	}
	desired := []pipeline.SecurityLabel{
		{Provider: "dummy", Label: "classified"}, // unchanged
		{Provider: "other", Label: "secret"},     // added
	}
	ops := diffSecurityLabelSet(snap, desired, `TABLE "t"`, pos)

	var added, removed, unchanged int
	for _, op := range ops {
		sql := op.SQL()
		switch {
		case strings.Contains(sql, `FOR "selinux"`) && strings.Contains(sql, "IS NULL"):
			removed++
		case strings.Contains(sql, `FOR "other"`) && strings.Contains(sql, "'secret'"):
			added++
		case strings.Contains(sql, `FOR "dummy"`):
			unchanged++
		}
		if op.Safety() != pipeline.Safe {
			t.Errorf("SECURITY LABEL op should be Safe, got %v: %s", op.Safety(), sql)
		}
	}
	if added != 1 || removed != 1 {
		t.Fatalf("want 1 added + 1 removed, got added=%d removed=%d, ops=%v", added, removed, sqlList(ops))
	}
	if unchanged != 0 {
		t.Errorf("unchanged provider must not produce an op, got: %v", sqlList(ops))
	}
}

func TestDiffSecurityLabelSetChangedLabelSameProvider(t *testing.T) {
	pos := pipeline.SourcePos{}
	snap := []snapshot.SnapSecurityLabel{{Provider: "dummy", Label: "unclassified"}}
	desired := []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}
	ops := diffSecurityLabelSet(snap, desired, `TABLE "t"`, pos)
	if len(ops) != 1 {
		t.Fatalf("want 1 op (re-set to new label), got %d: %v", len(ops), sqlList(ops))
	}
	if !strings.Contains(ops[0].SQL(), "'classified'") {
		t.Errorf("expected the new label, got: %s", ops[0].SQL())
	}
}

func TestCreateSecurityLabelOps(t *testing.T) {
	pos := pipeline.SourcePos{}
	labels := []pipeline.SecurityLabel{
		{Provider: "dummy", Label: "classified"},
		{Label: "unclassified"},
	}
	ops := createSecurityLabelOps(labels, `TABLE "t"`, pos)
	if len(ops) != 2 {
		t.Fatalf("want 2 ops, got %d", len(ops))
	}
	for _, op := range ops {
		if op.Safety() != pipeline.Safe {
			t.Errorf("expected Safe, got %v: %s", op.Safety(), op.SQL())
		}
	}
	if !containsSQL(ops, `FOR "dummy"`) || !containsSQL(ops, "'classified'") {
		t.Errorf("expected FOR-provider entry, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, "'unclassified'") {
		t.Errorf("expected unqualified entry, got: %v", sqlList(ops))
	}
}

func TestDiffCreateTableEmitsSecurityLabels(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t",
			Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"},
				SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "secret"}}}},
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}},
		},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SECURITY LABEL FOR "dummy" ON TABLE "public"."t" IS 'classified';`) {
		t.Errorf("expected table-level SECURITY LABEL, got: %v", sqlList(ops))
	}
	if !containsSQL(ops, `SECURITY LABEL FOR "dummy" ON COLUMN "public"."t"."id" IS 'secret';`) {
		t.Errorf("expected column-level SECURITY LABEL, got: %v", sqlList(ops))
	}
}

func TestDiffForeignTableSecurityLabelUsesForeignTableKeyword(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.ft", &snapshot.SnapObject{
		Kind: "table",
		Table: &snapshot.SnapTable{
			Schema: "public", Name: "ft", Foreign: true,
			Columns: []snapshot.SnapColumn{{Name: "id", Type: "integer"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "ft", Foreign: true,
			Columns:        []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}},
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}},
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON FOREIGN TABLE "public"."ft"`) {
		t.Errorf("expected ON FOREIGN TABLE, got: %v", sqlList(ops))
	}
}

func TestDiffMaterializedViewSecurityLabelUsesMaterializedViewKeyword(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "mv", Materialized: true, Query: "SELECT 1",
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON MATERIALIZED VIEW "public"."mv"`) {
		t.Errorf("expected ON MATERIALIZED VIEW, got: %v", sqlList(ops))
	}
}

func TestDiffPlainViewSecurityLabelUsesViewKeywordNotTable(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.View{Schema: "public", Name: "v", Query: "SELECT 1",
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SECURITY LABEL FOR "dummy" ON VIEW "public"."v" IS 'classified';`) {
		t.Errorf("expected ON VIEW (not ON TABLE — real PostgreSQL's SECURITY LABEL grammar has a distinct VIEW form), got: %v", sqlList(ops))
	}
}

func TestDiffDomainSecurityLabelUsesDomainKeyword(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "dom", Variant: "DOMAIN", DomainBaseType: ir.TypeRef{Name: "integer"},
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON DOMAIN "public"."dom"`) {
		t.Errorf("expected ON DOMAIN, got: %v", sqlList(ops))
	}
}

func TestDiffEnumTypeSecurityLabelUsesTypeKeyword(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Type{Schema: "public", Name: "en", Variant: "ENUM", EnumValues: []string{"a", "b"},
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON TYPE "public"."en"`) {
		t.Errorf("expected ON TYPE (not ON DOMAIN), got: %v", sqlList(ops))
	}
}

func TestDiffAggregateSecurityLabelUsesFunctionKeyword(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Aggregate{Schema: "public", Name: "agg", Args: []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Body:           "CREATE AGGREGATE public.agg(integer) (SFUNC = int4pl, STYPE = integer)",
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `SECURITY LABEL FOR "dummy" ON FUNCTION "public"."agg"(integer) IS 'classified';`) {
		t.Errorf("expected ON FUNCTION for an aggregate (real PostgreSQL grammar, not ON AGGREGATE), got: %v", sqlList(ops))
	}
}

func TestDiffProcedureSecurityLabelAddedRemoved(t *testing.T) {
	d := New()
	body := "BEGIN NULL; END;"
	hash := ir.HashBody(body)
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.p(integer)", &snapshot.SnapObject{
		Kind: "procedure",
		Opaque: &snapshot.SnapOpaque{
			Kind: "procedure", Schema: "public", Name: "p", Args: "integer", BodyHash: hash,
			SecurityLabels: []snapshot.SnapSecurityLabel{{Provider: "dummy", Label: "classified"}},
		},
	})
	desired := []pipeline.IRObject{
		&ir.Procedure{Schema: "public", Name: "p", Args: []ir.FuncArg{{Type: ir.TypeRef{Name: "integer"}}},
			Attrs: ir.FuncAttrs{Language: "plpgsql", Body: body}, BodyHash: hash,
		},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON PROCEDURE "public"."p"(integer) IS NULL`) {
		t.Errorf("expected the removed label to emit IS NULL, got: %v", sqlList(ops))
	}
}

func TestDiffRoleSecurityLabel(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Role{Name: "myrole", SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON ROLE "myrole"`) {
		t.Errorf("expected ON ROLE, got: %v", sqlList(ops))
	}
}

func TestDiffSchemaSecurityLabel(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Schema{Name: "myschema", SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON SCHEMA "myschema"`) {
		t.Errorf("expected ON SCHEMA, got: %v", sqlList(ops))
	}
}

func TestDiffSequenceSecurityLabel(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.Sequence{Schema: "public", Name: "seq", SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON SEQUENCE "public"."seq"`) {
		t.Errorf("expected ON SEQUENCE, got: %v", sqlList(ops))
	}
}

// TestDiffTablespaceSecurityLabelStaleSnapshotSelfHeals guards the opaque-
// kind stale-snapshot self-heal branch specifically (TablespaceLocation ==
// "" — a snapshot predating structured tracking): SecurityLabels must still
// be diffed there rather than silently ignored until the next apply
// populates the structural fields.
func TestDiffTablespaceSecurityLabelStaleSnapshotSelfHeals(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("ts", &snapshot.SnapObject{
		Kind:   "tablespace",
		Opaque: &snapshot.SnapOpaque{Kind: "tablespace", Name: "ts"}, // TablespaceLocation == ""
	})
	desired := []pipeline.IRObject{
		&ir.Tablespace{Name: "ts", Location: "/data/ts", Body: "CREATE TABLESPACE ts LOCATION '/data/ts'",
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON TABLESPACE "ts"`) || !containsSQL(ops, "'classified'") {
		t.Errorf("expected SECURITY LABEL to still be diffed on the stale-snapshot self-heal path, got: %v", sqlList(ops))
	}
}

func TestDiffPublicationSecurityLabel(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("pub", &snapshot.SnapObject{
		Kind: "publication",
		Opaque: &snapshot.SnapOpaque{
			Kind: "publication", Name: "pub", PublicationStructured: true, PublicationAllTables: true,
			PublicationInsert: true, PublicationUpdate: true, PublicationDelete: true, PublicationTruncate: true,
		},
	})
	desired := []pipeline.IRObject{
		&ir.Publication{Name: "pub", AllTables: true, Insert: true, Update: true, Delete: true, Truncate: true,
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON PUBLICATION "pub"`) {
		t.Errorf("expected ON PUBLICATION, got: %v", sqlList(ops))
	}
}

func TestDiffSubscriptionSecurityLabel(t *testing.T) {
	d := New()
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("sub", &snapshot.SnapObject{
		Kind:   "subscription",
		Opaque: &snapshot.SnapOpaque{Kind: "subscription", Name: "sub", BodyHash: hashText("x")},
	})
	desired := []pipeline.IRObject{
		&ir.Subscription{Name: "sub", Body: "x",
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON SUBSCRIPTION "sub"`) {
		t.Errorf("expected ON SUBSCRIPTION, got: %v", sqlList(ops))
	}
}

func TestDiffEventTriggerSecurityLabel(t *testing.T) {
	d := New()
	desired := []pipeline.IRObject{
		&ir.EventTrigger{Name: "evt", Event: "ddl_command_start", Function: "public.f",
			Body:           "CREATE EVENT TRIGGER evt ON ddl_command_start EXECUTE FUNCTION public.f()",
			SecurityLabels: []pipeline.SecurityLabel{{Provider: "dummy", Label: "classified"}}},
	}
	ops, err := d.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSQL(ops, `ON EVENT TRIGGER "evt"`) {
		t.Errorf("expected ON EVENT TRIGGER, got: %v", sqlList(ops))
	}
}

// TestDiffEnumAddValueUsedInSameMigrationRunsFirst guards RFC Section 5.1.1:
// when another statement in the migration uses a value being added to an
// enum, the enum's ADD VALUEs run before the transaction, as IF NOT EXISTS;
// otherwise they stay inside it.
func TestDiffEnumAddValueUsedInSameMigrationRunsFirst(t *testing.T) {
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("public.status", &snapshot.SnapObject{
		Kind: "type",
		Type: &snapshot.SnapType{Schema: "public", Name: "status", Variant: "ENUM", Values: []string{"a"}},
	})
	enum := &ir.Type{Schema: "public", Name: "status", Variant: "ENUM", EnumValues: []string{"a", "b", "c"}}

	ops, err := New().Diff([]pipeline.IRObject{enum}, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ops {
		if strings.Contains(o.SQL(), "ADD VALUE") && o.Mode() != pipeline.ModeTransactional {
			t.Errorf("unused new value should stay transactional: %s (%s)", o.SQL(), o.Mode())
		}
	}

	def := "'b'::public.status"
	tbl := &ir.Table{Schema: "public", Name: "t", Columns: []*ir.Column{
		{Name: "s", Type: ir.TypeRef{Schema: "public", Name: "status"}, Default: &def},
	}}
	ops, err = New().Diff([]pipeline.IRObject{enum, tbl}, snap)
	if err != nil {
		t.Fatal(err)
	}
	var adds int
	for _, o := range ops {
		if !strings.Contains(o.SQL(), "ADD VALUE") {
			continue
		}
		adds++
		if o.Mode() != pipeline.ModePreTransactional || !strings.Contains(o.SQL(), "ADD VALUE IF NOT EXISTS") {
			t.Errorf("used new value should run before the transaction as IF NOT EXISTS: %s (%s)", o.SQL(), o.Mode())
		}
	}
	if adds != 2 {
		t.Fatalf("expected 2 ADD VALUE ops, got %d: %v", adds, sqlList(ops))
	}
}

// TestDiffRangeMultirangeNameChangeIsRename guards RFC Section 5.3: changing
// only MULTIRANGE_TYPE_NAME renames the multirange type in place instead of
// rebuilding the range type.
func TestDiffRangeMultirangeNameChangeIsRename(t *testing.T) {
	old := &ir.Type{Schema: "public", Name: "floatrange", Variant: "RANGE",
		Body: "floatrange AS RANGE (SUBTYPE = float8)", MultirangeName: "floatmultirange"}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{old}); err != nil {
		t.Fatal(err)
	}
	desired := &ir.Type{Schema: "public", Name: "floatrange", Variant: "RANGE",
		Body: "floatrange AS RANGE (SUBTYPE = float8, MULTIRANGE_TYPE_NAME = float_ranges)", MultirangeName: "float_ranges"}
	ops, err := New().Diff([]pipeline.IRObject{desired}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if containsSQL(ops, "DROP TYPE") {
		t.Fatalf("a multirange name change must not rebuild the range type: %v", sqlList(ops))
	}
	if !containsSQL(ops, `ALTER TYPE "public"."floatmultirange" RENAME TO "float_ranges";`) {
		t.Fatalf("expected a multirange rename, got: %v", sqlList(ops))
	}
}

// TestVirtualTypeColumnSnapshotRoundTrip guards RFC Section 5.6: a column
// typed as a virtual type snapshots as its json/jsonb type plus the
// qualified reference, so re-planning against that snapshot is a no-op.
// A snapshot that stored the unresolved reference as the type is also no
// drift.
func TestVirtualTypeColumnSnapshotRoundTrip(t *testing.T) {
	build := func() []pipeline.IRObject {
		objs := []pipeline.IRObject{
			makeVtype("public", "line_item", ir.VtypeComposite{
				Fields: []ir.VtypeField{{Name: "sku", Type: ir.VtypeTypeRef{Name: "text"}}},
			}),
			&ir.Table{Schema: "public", Name: "orders", Columns: []*ir.Column{
				{Name: "items", Type: ir.TypeRef{Name: "line_item", ArrayDims: 1}},
			}},
		}
		if err := ir.ResolveVirtualTypes(objs, "public"); err != nil {
			t.Fatal(err)
		}
		return objs
	}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, build()); err != nil {
		t.Fatal(err)
	}
	var so snapshot.SnapObject
	if _, err := snap.GetObject("public.orders", &so); err != nil {
		t.Fatal(err)
	}
	if c := so.Table.Columns[0]; c.Type != "jsonb[]" || c.VirtualType != "public.line_item[]" {
		t.Errorf("snapshot column = type %q, virtual_type %q", c.Type, c.VirtualType)
	}
	ops, err := New().Diff(build(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops re-planning against own snapshot, got %v", sqlList(ops))
	}

	so.Table.Columns[0].Type = "line_item[]"
	so.Table.Columns[0].VirtualType = ""
	legacy := &pipeline.Snapshot{}
	_ = legacy.SetObject("public.orders", &so)
	ops, err = New().Diff(build(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no ops against a legacy unresolved snapshot, got %v", sqlList(ops))
	}
}

// TestAddColumnTableRewriteIsCaution guards RFC Section 17.2: ADD COLUMN
// is CAUTION exactly when PostgreSQL rewrites every existing row.
func TestAddColumnTableRewriteIsCaution(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		col  *ir.Column
		want pipeline.Safety
	}{
		{"constant default", &ir.Column{Type: ir.TypeRef{Name: "integer"}, Default: str("5")}, pipeline.Safe},
		{"stable default", &ir.Column{Type: ir.TypeRef{Name: "timestamptz"}, Default: str("now()")}, pipeline.Safe},
		{"sql value function", &ir.Column{Type: ir.TypeRef{Name: "timestamptz"}, Default: str("CURRENT_TIMESTAMP")}, pipeline.Safe},
		{"immutable builtin", &ir.Column{Type: ir.TypeRef{Name: "text"}, Default: str("lower('X')")}, pipeline.Safe},
		{"volatile builtin", &ir.Column{Type: ir.TypeRef{Name: "uuid"}, Default: str("gen_random_uuid()")}, pipeline.Caution},
		{"nested volatile", &ir.Column{Type: ir.TypeRef{Name: "text"}, Default: str("coalesce(NULL, md5(random()::text))")}, pipeline.Caution},
		{"qualified volatile", &ir.Column{Type: ir.TypeRef{Name: "timestamptz"}, Default: str("pg_catalog.clock_timestamp()")}, pipeline.Caution},
		{"unknown function", &ir.Column{Type: ir.TypeRef{Name: "uuid"}, Default: str("uuid_generate_v4()")}, pipeline.Caution},
		{"declared immutable", &ir.Column{Type: ir.TypeRef{Name: "integer"}, Default: str("app.one()")}, pipeline.Safe},
		{"declared without volatility", &ir.Column{Type: ir.TypeRef{Name: "integer"}, Default: str("app.next_code()")}, pipeline.Caution},
		{"identity", &ir.Column{Type: ir.TypeRef{Name: "bigint"}, Identity: &ir.Identity{Always: true}}, pipeline.Caution},
		{"stored generated", &ir.Column{Type: ir.TypeRef{Name: "integer"}, Generated: &ir.Generated{Expr: "1", Stored: true}}, pipeline.Caution},
		{"virtual generated", &ir.Column{Type: ir.TypeRef{Name: "integer"}, Generated: &ir.Generated{Expr: "1"}}, pipeline.Safe},
		{"checked domain", &ir.Column{Type: ir.TypeRef{Schema: "app", Name: "posint"}}, pipeline.Caution},
		{"plain domain", &ir.Column{Type: ir.TypeRef{Schema: "app", Name: "label"}}, pipeline.Safe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := &pipeline.Snapshot{}
			_ = snap.SetObject("public.t", &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{
				Schema: "public", Name: "t", Columns: []snapshot.SnapColumn{{Name: "id", Type: "integer"}},
			}})
			c.col.Name = "c"
			desired := []pipeline.IRObject{
				&ir.Function{Schema: "app", Name: "one", Attrs: ir.FuncAttrs{Volatility: "IMMUTABLE"}},
				&ir.Function{Schema: "app", Name: "next_code"},
				&ir.Type{Schema: "app", Name: "posint", Variant: "DOMAIN", DomainConstraints: []*ir.Constraint{{Type: "CHECK", Expr: "VALUE > 0"}}},
				&ir.Type{Schema: "app", Name: "label", Variant: "DOMAIN"},
				&ir.Table{Schema: "public", Name: "t", Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "integer"}}, c.col}},
			}
			ops, err := New().Diff(desired, snap)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range ops {
				if strings.Contains(o.SQL(), "ADD COLUMN") {
					if o.Safety() != c.want {
						t.Errorf("%s: got %v, want %v", o.SQL(), o.Safety(), c.want)
					}
					return
				}
			}
			t.Fatalf("no ADD COLUMN op in %v", sqlList(ops))
		})
	}
}

// TestViewStarComparedWithCatalog guards RFC Section 8.1: PostgreSQL
// stores a view's `*` expanded, so against a view read from the catalog a
// declared `*` query is compared without its select list. Between two
// declared queries the comparison stays exact.
func TestViewStarComparedWithCatalog(t *testing.T) {
	view := func(q string, recon bool, cols ...string) *ir.View {
		return &ir.View{Schema: "public", Name: "v", Query: q, Reconstructed: recon, Columns: cols}
	}
	snapOf := func(v *ir.View) *pipeline.Snapshot {
		s := &pipeline.Snapshot{}
		if err := snapshot.Populate(s, []pipeline.IRObject{v}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := []struct {
		name          string
		desired, base *ir.View
		wantOps       bool
	}{
		{"live expansion of *", view("SELECT id, email FROM users", true), view("SELECT * FROM users", false), false},
		{"live expansion of t.* in a join",
			view("SELECT u.id, u.email, o.total FROM users u JOIN orders o ON o.user_id = u.id", true),
			view("SELECT u.*, o.total FROM users u JOIN orders o ON o.user_id = u.id", false), false},
		{"declared * against a live snapshot", view("SELECT * FROM users", false), view("SELECT id, email FROM users", true), false},
		{"column list not visible in the catalog", view("SELECT id AS a, email AS b FROM users", true), view("SELECT * FROM users", false, "a", "b"), false},
		{"live WHERE differs", view("SELECT id, email FROM users WHERE id > 1", true), view("SELECT * FROM users", false), true},
		{"declared * replaced by a list", view("SELECT id FROM users", false), view("SELECT * FROM users", false), true},
		{"live query differs, no *", view("SELECT id FROM users", true), view("SELECT id, email FROM users", false), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ops, err := New().Diff([]pipeline.IRObject{c.desired}, snapOf(c.base))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(ops) > 0; got != c.wantOps {
				t.Errorf("ops = %v, want ops: %v", sqlList(ops), c.wantOps)
			}
		})
	}
}

// TestNormalizeCheckExpr: the source's CHECK text and pg_get_constraintdef's
// deparsed form compare equal.
func TestNormalizeCheckExpr(t *testing.T) {
	for _, c := range [][2]string{
		{"CHECK (VALUE > 0)", "CHECK ((VALUE > 0))"},
		{"check (value > 0 AND value < 9)", "check ((value > 0 AND value < 9))"},
	} {
		if a, b := normalizeCheckExpr(c[0]), normalizeCheckExpr(c[1]); a != b {
			t.Errorf("%q → %q, %q → %q", c[0], a, c[1], b)
		}
	}
	if normalizeCheckExpr("CHECK (VALUE > 0)") == normalizeCheckExpr("CHECK (VALUE > 1)") {
		t.Error("different conditions must not compare equal")
	}
}

// TestDiffRoleConfigResetAgainstLive: against the live catalog, a declared
// RESET is satisfied when the setting has no entry; against a stored
// snapshot without it, RESET is still emitted.
func TestDiffRoleConfigResetAgainstLive(t *testing.T) {
	o := &ir.Role{Name: "r", Configs: []ir.RoleConfig{{Param: "statement_timeout", Reset: true}}}
	if ops := diffRoleConfigs(o, &snapshot.SnapRole{Name: "r"}, true, pipeline.SourcePos{}); len(ops) != 0 {
		t.Errorf("live: expected no ops, got %v", sqlList(ops))
	}
	if ops := diffRoleConfigs(o, &snapshot.SnapRole{Name: "r"}, false, pipeline.SourcePos{}); len(ops) != 1 {
		t.Errorf("stored: expected RESET, got %v", sqlList(ops))
	}
	live := &snapshot.SnapRole{Name: "r", Configs: []snapshot.SnapRoleConfig{{Param: "statement_timeout", Value: strp("5s")}}}
	if ops := diffRoleConfigs(o, live, true, pipeline.SourcePos{}); len(ops) != 1 {
		t.Errorf("live with the setting present: expected RESET, got %v", sqlList(ops))
	}
}

// TestRecursiveQueryCanon: a recursive view's source text and PostgreSQL's
// deparsed form (alias on the CTE self-reference) compare equal; a real
// change doesn't.
func TestRecursiveQueryCanon(t *testing.T) {
	src := `WITH RECURSIVE countdown(n) AS (VALUES (10) UNION ALL SELECT n - 1 FROM countdown WHERE n > 1) SELECT n FROM countdown`
	live := `WITH RECURSIVE countdown(n) AS (VALUES (10) UNION ALL SELECT countdown_1.n - 1 FROM countdown countdown_1 WHERE countdown_1.n > 1) SELECT n FROM countdown`
	a, ok1 := recursiveQueryCanon(src)
	b, ok2 := recursiveQueryCanon(live)
	if !ok1 || !ok2 || a != b {
		t.Fatalf("expected equal canonical forms:\n%s\n%s", a, b)
	}
	changed := strings.Replace(src, "n > 1", "n > 2", 1)
	if c, _ := recursiveQueryCanon(changed); c == a {
		t.Error("a changed literal must not compare equal")
	}
	if _, ok := recursiveQueryCanon("SELECT 1"); ok {
		t.Error("a non-recursive query has no canonical form here")
	}
}

func TestQualifyOwnedBy(t *testing.T) {
	cases := [][3]string{
		{"public", "orders.order_number", "public.orders.order_number"},
		{"public", "public.orders.order_number", "public.orders.order_number"},
		{"app", `"Odd.Table".id`, "app.Odd.Table.id"},
		{"public", "NONE", "NONE"},
	}
	for _, c := range cases {
		if got := qualifyOwnedBy(c[0], c[1]); got != c[2] {
			t.Errorf("qualifyOwnedBy(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// TestSeqBoundDefaults: the catalog stores NO MINVALUE / NO MAXVALUE as the
// default bound itself.
func TestSeqBoundDefaults(t *testing.T) {
	for _, c := range []struct {
		typ          string
		inc          int64
		wantMin, max int64
	}{
		{"bigint", 1, 1, math.MaxInt64},
		{"integer", 1, 1, math.MaxInt32},
		{"smallint", -1, math.MinInt16, -1},
		{"", 5, 1, math.MaxInt64},
	} {
		if mn, mx := defaultSeqBounds(c.typ, c.inc); mn != c.wantMin || mx != c.max {
			t.Errorf("%s inc %d: got %d..%d", c.typ, c.inc, mn, mx)
		}
	}
	one, other := int64(1), int64(5)
	if seqBoundChanged(nil, true, &one, false, 1) {
		t.Error("NO MINVALUE must match a live bound equal to the default")
	}
	if !seqBoundChanged(nil, true, &other, false, 1) {
		t.Error("NO MINVALUE must not match a non-default bound")
	}
}
