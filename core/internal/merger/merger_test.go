package merger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/merger"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// assertCode checks err carries a CompilerError with the given code.
// Merge collects group errors into a pipeline.Diagnostics slice, so the code
// is searched there (or on a bare *pipeline.CompilerError).
func assertCode(t *testing.T, err error, code, desc string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s for %s, got nil", code, desc)
	}
	diags, ok := err.(pipeline.Diagnostics)
	if !ok {
		var ce *pipeline.CompilerError
		if !errors.As(err, &ce) {
			t.Fatalf("expected pipeline.Diagnostics or *pipeline.CompilerError for %s, got: %v (%T)", desc, err, err)
		}
		diags = pipeline.Diagnostics{ce}
	}
	for _, d := range diags {
		if d.Code == code {
			return
		}
	}
	t.Errorf("expected a %s diagnostic for %s, got: %v", code, desc, err)
}

func assertDPGE005(t *testing.T, err error, desc string) {
	t.Helper()
	assertCode(t, err, "DPG-E005", desc)
}

// assertConflict checks that merging objects fails with DPG-E039.
func assertConflict(t *testing.T, desc string, objects ...pipeline.IRObject) error {
	t.Helper()
	_, err := merger.New().Merge(objects)
	assertCode(t, err, "DPG-E039", desc)
	return err
}

var pos = pipeline.SourcePos{File: "a.dpg", Line: 1, Col: 1}
var pos2 = pipeline.SourcePos{File: "b.dpg", Line: 1, Col: 1}
var pos3 = pipeline.SourcePos{File: "c.dpg", Line: 1, Col: 1}

func ptr[T any](v T) *T { return &v }

func merge(t *testing.T, objects ...pipeline.IRObject) []pipeline.IRObject {
	t.Helper()
	out, err := merger.New().Merge(objects)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return out
}

// ── No-op: single objects pass through ────────────────────────────────────────

func TestMerge_SingleObjects(t *testing.T) {
	objects := []pipeline.IRObject{
		&ir.Schema{Name: "app", SrcPos: pos},
		&ir.Table{Schema: "app", Name: "users", SrcPos: pos},
	}
	out := merge(t, objects...)
	if len(out) != 2 {
		t.Errorf("expected 2, got %d", len(out))
	}
}

// ── Table merge ───────────────────────────────────────────────────────────────

func TestMerge_Table_ColumnsUnioned(t *testing.T) {
	a := &ir.Table{
		Schema: "app", Name: "users", SrcPos: pos,
		Columns: []*ir.Column{{Name: "id", SrcPos: pos}},
	}
	b := &ir.Table{
		Schema: "app", Name: "users", SrcPos: pos2,
		Columns: []*ir.Column{{Name: "email", SrcPos: pos2}},
	}
	out := merge(t, a, b)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged table, got %d", len(out))
	}
	tbl := out[0].(*ir.Table)
	if len(tbl.Columns) != 2 {
		t.Errorf("Columns: expected 2, got %d", len(tbl.Columns))
	}
	if tbl.Columns[0].Name != "id" || tbl.Columns[1].Name != "email" {
		t.Errorf("column names: got %q, %q", tbl.Columns[0].Name, tbl.Columns[1].Name)
	}
}

func TestMerge_Table_ScalarConflictIsError(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Owner: ptr("alice")}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Owner: ptr("bob")}
	assertConflict(t, "two different owners", a, b)
}

func TestMerge_Table_ScalarSetOnceIsTaken(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Owner: ptr("bob"), Tablespace: ptr("fast")}
	tbl := merge(t, a, b)[0].(*ir.Table)
	if tbl.Owner == nil || *tbl.Owner != "bob" || tbl.Tablespace == nil || *tbl.Tablespace != "fast" {
		t.Errorf("owner/tablespace declared once should be taken, got %v / %v", tbl.Owner, tbl.Tablespace)
	}
}

func TestMerge_Table_IndexesUnioned(t *testing.T) {
	a := &ir.Table{
		Schema: "app", Name: "t", SrcPos: pos,
		Indexes: []*ir.Index{{Name: "idx_a", Columns: []pipeline.IndexColumn{{Name: "a"}}}},
	}
	b := &ir.Table{
		Schema: "app", Name: "t", SrcPos: pos2,
		Indexes: []*ir.Index{{Name: "idx_b", Columns: []pipeline.IndexColumn{{Name: "b"}}}},
	}
	out := merge(t, a, b)
	tbl := out[0].(*ir.Table)
	if len(tbl.Indexes) != 2 {
		t.Errorf("Indexes: expected 2, got %d", len(tbl.Indexes))
	}
}

func TestMerge_Table_DuplicateIndexDeduped(t *testing.T) {
	idx := &ir.Index{Name: "idx_same", Columns: []pipeline.IndexColumn{{Name: "x"}}}
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Indexes: []*ir.Index{idx}}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Indexes: []*ir.Index{idx}}
	out := merge(t, a, b)
	tbl := out[0].(*ir.Table)
	if len(tbl.Indexes) != 1 {
		t.Errorf("Indexes: duplicate should be deduped; got %d", len(tbl.Indexes))
	}
}

func TestMerge_Table_IndexConflictIsError(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Indexes: []*ir.Index{
		{Name: "idx_same", Columns: []pipeline.IndexColumn{{Name: "x"}}, Pos: pos},
	}}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Indexes: []*ir.Index{
		{Name: "idx_same", Columns: []pipeline.IndexColumn{{Name: "y"}}, Pos: pos2},
	}}
	_, err := merger.New().Merge([]pipeline.IRObject{a, b})
	assertDPGE005(t, err, "conflicting index definitions")
}

func TestMerge_Table_PolicyConflictIsError(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Policies: []*ir.Policy{
		{Name: "p", Command: "SELECT", Pos: pos},
	}}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Policies: []*ir.Policy{
		{Name: "p", Command: "UPDATE", Pos: pos2},
	}}
	_, err := merger.New().Merge([]pipeline.IRObject{a, b})
	assertDPGE005(t, err, "conflicting policy definitions")
}

func TestMerge_Table_TriggerConflictIsError(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Triggers: []*ir.Trigger{
		{Name: "trg", When: "BEFORE", Events: []string{"INSERT"}, ForEach: "ROW", Function: "f", Pos: pos},
	}}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Triggers: []*ir.Trigger{
		{Name: "trg", When: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW", Function: "f", Pos: pos2},
	}}
	_, err := merger.New().Merge([]pipeline.IRObject{a, b})
	assertDPGE005(t, err, "conflicting trigger definitions")
}

func TestMerge_Table_ConstraintConflictIsError(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Constraints: []*ir.Constraint{
		{Name: "ck_t", Type: "CHECK", Expr: "CHECK (amount > 0)", Pos: pos},
	}}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Constraints: []*ir.Constraint{
		{Name: "ck_t", Type: "CHECK", Expr: "CHECK (amount > 100)", Pos: pos2},
	}}
	_, err := merger.New().Merge([]pipeline.IRObject{a, b})
	assertDPGE005(t, err, "conflicting constraint definitions")
}

func TestMerge_Table_ConstraintsUnioned(t *testing.T) {
	a := &ir.Table{
		Schema: "app", Name: "t", SrcPos: pos,
		Constraints: []*ir.Constraint{{Name: "pk_t", Type: "PRIMARY KEY", Expr: "PRIMARY KEY (id)"}},
	}
	b := &ir.Table{
		Schema: "app", Name: "t", SrcPos: pos2,
		Constraints: []*ir.Constraint{{Name: "uq_t_email", Type: "UNIQUE", Expr: "UNIQUE (email)"}},
	}
	out := merge(t, a, b)
	tbl := out[0].(*ir.Table)
	if len(tbl.Constraints) != 2 {
		t.Errorf("Constraints: expected 2, got %d", len(tbl.Constraints))
	}
}

func TestMerge_Table_ProtectedAndDropCascadeOrred(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Protected: true}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, DropCascade: true}
	out := merge(t, a, b)
	tbl := out[0].(*ir.Table)
	if !tbl.Protected {
		t.Error("Protected should be true")
	}
	if !tbl.DropCascade {
		t.Error("DropCascade should be true")
	}
}

// ── Schema merge ──────────────────────────────────────────────────────────────

func TestMerge_Schema_OwnerConflictIsError(t *testing.T) {
	a := &ir.Schema{Name: "app", SrcPos: pos, Owner: ptr("alice")}
	b := &ir.Schema{Name: "app", SrcPos: pos2, Owner: ptr("bob")}
	assertConflict(t, "two different schema owners", a, b)
}

// ── View merge ────────────────────────────────────────────────────────────────

func TestMerge_View_GrantsAppended(t *testing.T) {
	a := &ir.View{
		Schema: "app", Name: "v", SrcPos: pos,
		Grants: []ir.Grant{{Roles: []string{"r1"}, Privileges: []string{"SELECT"}}},
	}
	b := &ir.View{
		Schema: "app", Name: "v", SrcPos: pos2,
		Grants: []ir.Grant{{Roles: []string{"r2"}, Privileges: []string{"SELECT"}}},
	}
	out := merge(t, a, b)
	v := out[0].(*ir.View)
	if len(v.Grants) != 2 {
		t.Errorf("Grants: expected 2, got %d", len(v.Grants))
	}
}

// ── Type (ENUM) merge ─────────────────────────────────────────────────────────

func TestMerge_EnumValuesUnioned(t *testing.T) {
	a := &ir.Type{Schema: "app", Name: "status", Variant: "ENUM", SrcPos: pos, EnumValues: []string{"active", "inactive"}}
	b := &ir.Type{Schema: "app", Name: "status", Variant: "ENUM", SrcPos: pos2, EnumValues: []string{"inactive", "pending"}}
	out := merge(t, a, b)
	tp := out[0].(*ir.Type)
	if len(tp.EnumValues) != 3 {
		t.Errorf("EnumValues: expected 3, got %d: %v", len(tp.EnumValues), tp.EnumValues)
	}
}

// ── Function merge ────────────────────────────────────────────────────────────

func TestMerge_Function_GrantsAppended(t *testing.T) {
	a := &ir.Function{Schema: "app", Name: "f", SrcPos: pos, Grants: []ir.Grant{{Roles: []string{"r1"}, Privileges: []string{"EXECUTE"}}}}
	b := &ir.Function{Schema: "app", Name: "f", SrcPos: pos2, Grants: []ir.Grant{{Roles: []string{"r2"}, Privileges: []string{"EXECUTE"}}}}
	out := merge(t, a, b)
	f := out[0].(*ir.Function)
	if len(f.Grants) != 2 {
		t.Errorf("Grants: expected 2, got %d", len(f.Grants))
	}
}

// ── Different types with same name are separate objects ────────────────────────

func TestMerge_SameNameDifferentType(t *testing.T) {
	tbl := &ir.Table{Schema: "app", Name: "status", SrcPos: pos}
	tp := &ir.Type{Schema: "app", Name: "status", Variant: "ENUM", SrcPos: pos2}
	out := merge(t, tbl, tp)
	if len(out) != 2 {
		t.Errorf("expected 2 objects (different types, same name), got %d", len(out))
	}
}

// ── Column-level merge ────────────────────────────────────────────────────────

func TestMerge_ColumnScalarConflictIsError(t *testing.T) {
	col := func(comment string, p pipeline.SourcePos) *ir.Column {
		return &ir.Column{Name: "email", Comment: ptr(comment), SrcPos: p}
	}
	a := &ir.Table{Schema: "app", Name: "users", SrcPos: pos, Columns: []*ir.Column{col("old", pos)}}
	b := &ir.Table{Schema: "app", Name: "users", SrcPos: pos2, Columns: []*ir.Column{col("new", pos2)}}
	err := assertConflict(t, "two different column comments", a, b)
	if !strings.Contains(err.Error(), "column email comment") {
		t.Errorf("conflict should name the column property, got: %v", err)
	}
}

func TestMerge_ColumnTypeConflictIsError(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "users", SrcPos: pos,
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "int4"}, SrcPos: pos}}}
	b := &ir.Table{Schema: "app", Name: "users", SrcPos: pos2,
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "int8"}, SrcPos: pos2}}}
	assertConflict(t, "two different column types", a, b)
}

func TestMerge_ColumnIdenticalAcrossFilesMerges(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "users", SrcPos: pos,
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "int4"}, SrcPos: pos}}}
	b := &ir.Table{Schema: "app", Name: "users", SrcPos: pos2,
		Columns: []*ir.Column{{Name: "id", Type: ir.TypeRef{Name: "int4"}, Comment: ptr("pk"), SrcPos: pos2}}}
	tbl := merge(t, a, b)[0].(*ir.Table)
	if len(tbl.Columns) != 1 || tbl.Columns[0].Comment == nil || *tbl.Columns[0].Comment != "pk" {
		t.Errorf("identical column should merge, taking the comment: %+v", tbl.Columns)
	}
}

// ── scalar conflicts (RFC Section 3.7, DPG-E039) ─────────────────────────────

func TestMergeTablesOwnerNoConflictSameValue(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Owner: ptr("alice")}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Owner: ptr("alice")}
	if *merge(t, a, b)[0].(*ir.Table).Owner != "alice" {
		t.Error("identical owners should merge")
	}
}

// TestMergeTablesOwnerThreeFileLastSetByTracking: file1 and file2 agree on
// Owner, file3 disagrees. The conflict must cite file2 (the file that most
// recently set the value) as the source of the conflicting value.
func TestMergeTablesOwnerThreeFileLastSetByTracking(t *testing.T) {
	a := &ir.Table{Schema: "app", Name: "t", SrcPos: pos, Owner: ptr("alice")}
	b := &ir.Table{Schema: "app", Name: "t", SrcPos: pos2, Owner: ptr("alice")}
	c := &ir.Table{Schema: "app", Name: "t", SrcPos: pos3, Owner: ptr("carol")}
	msg := assertConflict(t, "third file disagreeing", a, b, c).Error()
	if !strings.Contains(msg, "in "+pos2.File) {
		t.Errorf("conflict should be attributed to %s, got: %s", pos2.File, msg)
	}
	if strings.Contains(msg, "in "+pos.File) {
		t.Errorf("conflict should not be attributed to %s, got: %s", pos.File, msg)
	}
}

func TestMergeScalarConflictsAcrossKinds(t *testing.T) {
	cases := []struct {
		desc string
		a, b pipeline.IRObject
	}{
		{"procedure comment",
			&ir.Procedure{Schema: "app", Name: "p", SrcPos: pos, Comment: ptr("old")},
			&ir.Procedure{Schema: "app", Name: "p", SrcPos: pos2, Comment: ptr("new")}},
		{"aggregate comment",
			&ir.Aggregate{Schema: "app", Name: "agg", SrcPos: pos, Comment: ptr("old")},
			&ir.Aggregate{Schema: "app", Name: "agg", SrcPos: pos2, Comment: ptr("new")}},
		{"sequence increment",
			&ir.Sequence{Schema: "app", Name: "s", SrcPos: pos, IncrementBy: ptr(int64(1))},
			&ir.Sequence{Schema: "app", Name: "s", SrcPos: pos2, IncrementBy: ptr(int64(2))}},
		{"extension version",
			&ir.Extension{Name: "pgcrypto", SrcPos: pos, Version: ptr("1.1")},
			&ir.Extension{Name: "pgcrypto", SrcPos: pos2, Version: ptr("1.2")}},
		{"role connection limit",
			&ir.Role{Name: "app_user", SrcPos: pos, ConnectionLimit: ptr(5)},
			&ir.Role{Name: "app_user", SrcPos: pos2, ConnectionLimit: ptr(10)}},
		{"tablespace location",
			&ir.Tablespace{Name: "fast_ssd", SrcPos: pos, Location: "/data/ssd1"},
			&ir.Tablespace{Name: "fast_ssd", SrcPos: pos2, Location: "/data/ssd2"}},
		{"fdw handler",
			&ir.ForeignDataWrapper{Name: "my_fdw", SrcPos: pos, Handler: "handler_a"},
			&ir.ForeignDataWrapper{Name: "my_fdw", SrcPos: pos2, Handler: "handler_b"}},
		{"server type",
			&ir.ForeignServer{Name: "srv", SrcPos: pos, FDWName: "postgres_fdw", Type: ptr("primary")},
			&ir.ForeignServer{Name: "srv", SrcPos: pos2, FDWName: "postgres_fdw", Type: ptr("replica")}},
		{"user mapping option",
			&ir.UserMapping{User: "alice", Server: "srv", SrcPos: pos, Options: []pipeline.StorageParam{{Key: "host", Value: "db1.internal"}}},
			&ir.UserMapping{User: "alice", Server: "srv", SrcPos: pos2, Options: []pipeline.StorageParam{{Key: "host", Value: "db2.internal"}}}},
		{"domain base type",
			&ir.Type{Schema: "app", Name: "positive_int", Variant: "DOMAIN", SrcPos: pos, DomainBaseType: ir.TypeRef{Name: "int4"}},
			&ir.Type{Schema: "app", Name: "positive_int", Variant: "DOMAIN", SrcPos: pos2, DomainBaseType: ir.TypeRef{Name: "int8"}}},
		{"function body",
			&ir.Function{Schema: "app", Name: "f", SrcPos: pos, Attrs: ir.FuncAttrs{Body: "select 1"}},
			&ir.Function{Schema: "app", Name: "f", SrcPos: pos2, Attrs: ir.FuncAttrs{Body: "select 2"}}},
	}
	for _, c := range cases {
		assertConflict(t, c.desc, c.a, c.b)
	}
}

func TestMergeRolesMembershipsAppendedNotConflicted(t *testing.T) {
	a := &ir.Role{Name: "app_user", SrcPos: pos, Memberships: []ir.RoleMembership{{Role: "readonly", Direction: "IN_ROLE"}}}
	b := &ir.Role{Name: "app_user", SrcPos: pos2, Memberships: []ir.RoleMembership{{Role: "readwrite", Direction: "IN_ROLE"}}}
	role := merge(t, a, b)[0].(*ir.Role)
	if len(role.Memberships) != 2 {
		t.Errorf("Memberships: expected 2 entries from both files, got %d: %+v", len(role.Memberships), role.Memberships)
	}
}

func TestMergeFunctionRevocationsUnioned(t *testing.T) {
	a := &ir.Function{Schema: "app", Name: "f", SrcPos: pos, Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}}}
	b := &ir.Function{Schema: "app", Name: "f", SrcPos: pos2, Revocations: []ir.Revocation{{Privileges: []string{"EXECUTE"}, Roles: []string{"anon"}}}}
	f := merge(t, a, b)[0].(*ir.Function)
	if len(f.Revocations) != 2 {
		t.Errorf("Revocations: expected both files' entries, got %+v", f.Revocations)
	}
}

func TestMergeForeignDataWrappersEmptyHandlerNotAConflict(t *testing.T) {
	// "" means the handler was not declared: a later file saying nothing
	// about it is neither a conflict nor an override.
	a := &ir.ForeignDataWrapper{Name: "my_fdw", SrcPos: pos, Handler: "handler_a"}
	b := &ir.ForeignDataWrapper{Name: "my_fdw", SrcPos: pos2, Handler: ""}
	if got := merge(t, a, b)[0].(*ir.ForeignDataWrapper).Handler; got != "handler_a" {
		t.Errorf("Handler: got %q, want handler_a", got)
	}
}

func TestMergeUserMappingsOptionsDistinctKeysNoConflict(t *testing.T) {
	a := &ir.UserMapping{User: "alice", Server: "srv", SrcPos: pos,
		Options: []pipeline.StorageParam{{Key: "host", Value: "db1.internal"}}}
	b := &ir.UserMapping{User: "alice", Server: "srv", SrcPos: pos2,
		Options: []pipeline.StorageParam{{Key: "port", Value: "5432"}}}
	if um := merge(t, a, b)[0].(*ir.UserMapping); len(um.Options) != 2 {
		t.Errorf("Options: expected both keys present, got %+v", um.Options)
	}
}

func TestMergeTypesDomainNotNullIsORed(t *testing.T) {
	a := &ir.Type{Schema: "app", Name: "d", Variant: "DOMAIN", SrcPos: pos, DomainNotNull: false}
	b := &ir.Type{Schema: "app", Name: "d", Variant: "DOMAIN", SrcPos: pos2, DomainNotNull: true}
	if !merge(t, a, b)[0].(*ir.Type).DomainNotNull {
		t.Error("DomainNotNull should be true (OR-merged)")
	}
}

func TestMergeUnmergedKindMustBeIdentical(t *testing.T) {
	a := &ir.Publication{Name: "pub", SrcPos: pos, AllTables: true}
	b := &ir.Publication{Name: "pub", SrcPos: pos2, AllTables: false}
	assertConflict(t, "two different publication definitions", a, b)
	c := &ir.Publication{Name: "pub", SrcPos: pos2, AllTables: true}
	if out := merge(t, a, c); len(out) != 1 {
		t.Errorf("identical publication declarations should merge into one, got %d", len(out))
	}
}
