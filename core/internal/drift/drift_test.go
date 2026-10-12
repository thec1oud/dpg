package drift

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func boolp(b bool) *bool      { return &b }
func intp(i int) *int         { return &i }
func int64p(i int64) *int64   { return &i }
func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }

// TestMaskRoleClearsUndeclared: a role declared with only LOGIN keeps
// LOGIN; the attributes PostgreSQL reports for every role are cleared.
func TestMaskRoleClearsUndeclared(t *testing.T) {
	live := &ir.Role{Name: "svc", CanLogin: boolp(true), Superuser: boolp(false), CreateDB: boolp(false),
		Inherit: boolp(true), ConnectionLimit: intp(-1), ValidUntil: strp("infinity")}
	so := &snapshot.SnapObject{Kind: "role", Role: &snapshot.SnapRole{Name: "svc", CanLogin: boolp(true)}}
	m := mask(live, so).(*ir.Role)
	if m.CanLogin == nil || !*m.CanLogin {
		t.Error("declared LOGIN must be kept")
	}
	if m.Superuser != nil || m.CreateDB != nil || m.Inherit != nil || m.ConnectionLimit != nil || m.ValidUntil != nil {
		t.Errorf("undeclared attributes must be cleared: %+v", m)
	}
	if live.Superuser == nil {
		t.Error("mask must not modify the live object")
	}
}

// TestMaskColumnIdentityFlattened: the snapshot records identity options
// flattened (IdentityCache, ...); only declared ones are kept.
func TestMaskColumnIdentityFlattened(t *testing.T) {
	live := &ir.Table{Schema: "public", Name: "t", Columns: []*ir.Column{{
		Name: "id", Identity: &ir.Identity{Always: true, IncrementBy: int64p(1), Cache: int64p(1), StartValue: int64p(5)},
		Storage: strp("plain"),
	}}}
	so := &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{Schema: "public", Name: "t", Columns: []snapshot.SnapColumn{{
		Name: "id", Identity: strp("ALWAYS"), IdentityStartValue: int64p(5),
	}}}}
	m := mask(live, so).(*ir.Table)
	id := m.Columns[0].Identity
	if id == nil || !id.Always || id.StartValue == nil || *id.StartValue != 5 {
		t.Fatalf("declared identity options must be kept: %+v", id)
	}
	if id.IncrementBy != nil || id.Cache != nil {
		t.Errorf("undeclared identity options must be cleared: %+v", id)
	}
	if m.Columns[0].Storage != nil {
		t.Error("undeclared column storage must be cleared")
	}
	if live.Columns[0].Identity.Cache == nil || live.Columns[0].Storage == nil {
		t.Error("mask must not modify the live columns")
	}
}

// TestMaskKeepsExtraColumn: a live column the snapshot doesn't record is
// kept whole, so the differ reports it.
func TestMaskKeepsExtraColumn(t *testing.T) {
	live := &ir.Table{Schema: "public", Name: "t", Columns: []*ir.Column{
		{Name: "id", Storage: strp("plain")}, {Name: "extra", Storage: strp("extended")},
	}}
	so := &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{Schema: "public", Name: "t",
		Columns: []snapshot.SnapColumn{{Name: "id"}}}}
	m := mask(live, so).(*ir.Table)
	if len(m.Columns) != 2 || m.Columns[0].Storage != nil || m.Columns[1].Storage == nil {
		t.Errorf("got %+v %+v", m.Columns[0], m.Columns[1])
	}
}

// TestMaskFunctionAttrs: FuncAttrs is matched field by field with the
// snapshot's flat attribute fields.
func TestMaskFunctionAttrs(t *testing.T) {
	live := &ir.Function{Schema: "public", Name: "f", Attrs: ir.FuncAttrs{Language: "sql", Volatility: "VOLATILE", Parallel: "UNSAFE", Cost: f64p(100)}}
	so := &snapshot.SnapObject{Kind: "function", Function: &snapshot.SnapFunction{Schema: "public", Name: "f", Language: "sql"}}
	m := mask(live, so).(*ir.Function)
	if m.Attrs.Language != "sql" || m.Attrs.Volatility != "" || m.Attrs.Parallel != "" || m.Attrs.Cost != nil {
		t.Errorf("got %+v", m.Attrs)
	}
}

// TestMaskGrantsUseSnapshot: the masked object carries exactly the declared
// grants (compared separately by compareGrants).
func TestMaskGrantsUseSnapshot(t *testing.T) {
	live := &ir.Table{Schema: "public", Name: "t", Grants: []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"x"}}}}
	so := &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{Schema: "public", Name: "t",
		Grants: []snapshot.SnapGrant{{Privileges: []string{"INSERT"}, Roles: []string{"app"}, WithGrant: true}}}}
	m := mask(live, so).(*ir.Table)
	if len(m.Grants) != 1 || m.Grants[0].Roles[0] != "app" || m.Grants[0].Privileges[0] != "INSERT" || !m.Grants[0].WithGrant {
		t.Errorf("got %+v", m.Grants)
	}
}

func grantsOf(gs ...ir.Grant) reflect.Value { return reflect.ValueOf(gs) }
func snapGrantsOf(gs ...snapshot.SnapGrant) reflect.Value {
	return reflect.ValueOf(gs)
}

func findingStrings(fs []grantFinding) []string {
	var out []string
	for _, f := range fs {
		s := f.change + " " + f.tuple.String()
		if f.path != "" {
			s = f.path + " " + s
		}
		out = append(out, s)
	}
	return out
}

func TestCompareGrants(t *testing.T) {
	cases := []struct {
		name string
		kind string
		live reflect.Value
		snap reflect.Value
		revs reflect.Value
		want []string
	}{
		{"exact", "table",
			grantsOf(ir.Grant{Privileges: []string{"INSERT", "SELECT"}, Roles: []string{"a"}}, ir.Grant{Privileges: []string{"INSERT", "SELECT"}, Roles: []string{"b"}}),
			snapGrantsOf(snapshot.SnapGrant{Privileges: []string{"select", "insert"}, Roles: []string{"a", "b"}}),
			reflect.Value{}, nil},
		{"missing and extra", "table",
			grantsOf(ir.Grant{Privileges: []string{"SELECT", "DELETE"}, Roles: []string{"a"}}),
			snapGrantsOf(snapshot.SnapGrant{Privileges: []string{"SELECT", "INSERT"}, Roles: []string{"a"}}),
			reflect.Value{}, []string{"extra DELETE TO a", "missing INSERT TO a"}},
		{"grant option lost", "table",
			grantsOf(ir.Grant{Privileges: []string{"SELECT"}, Roles: []string{"a"}}),
			snapGrantsOf(snapshot.SnapGrant{Privileges: []string{"SELECT"}, Roles: []string{"a"}, WithGrant: true}),
			reflect.Value{}, []string{"missing SELECT TO a WITH GRANT OPTION"}},
		{"ALL satisfied, MAINTAIN covered", "table",
			grantsOf(ir.Grant{Privileges: []string{"DELETE", "INSERT", "MAINTAIN", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"}, Roles: []string{"a"}}),
			snapGrantsOf(snapshot.SnapGrant{Roles: []string{"a"}}),
			reflect.Value{}, nil},
		{"ALL partly revoked", "table",
			grantsOf(ir.Grant{Privileges: []string{"SELECT"}, Roles: []string{"a"}}),
			snapGrantsOf(snapshot.SnapGrant{Roles: []string{"a"}}),
			reflect.Value{}, []string{"missing ALL TO a"}},
		{"default PUBLIC EXECUTE is not extra", "function",
			grantsOf(ir.Grant{Privileges: []string{"EXECUTE"}, Roles: []string{"PUBLIC"}}, ir.Grant{Privileges: []string{"EXECUTE"}, Roles: []string{"a"}}),
			snapGrantsOf(snapshot.SnapGrant{Privileges: []string{"EXECUTE"}, Roles: []string{"a"}}),
			reflect.Value{}, nil},
		{"revoked privilege granted again", "table",
			grantsOf(ir.Grant{Privileges: []string{"SELECT"}, Roles: []string{"PUBLIC"}}),
			reflect.Value{},
			snapGrantsOf(snapshot.SnapGrant{Privileges: []string{"SELECT"}, Roles: []string{"PUBLIC"}}),
			[]string{"regranted SELECT TO PUBLIC"}},
	}
	for _, c := range cases {
		got := findingStrings(compareGrants(c.kind, "", c.live, c.snap, c.revs))
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestItemID(t *testing.T) {
	if got := ItemID("table", "public.my/odd:name", "column:c/grant:SELECT:app"); got != "table:public.my%2Fodd%3Aname/column:c/grant:SELECT:app" {
		t.Errorf("got %q", got)
	}
	if got := ItemID("role", "svc", ""); got != "role:svc" {
		t.Errorf("got %q", got)
	}
}

// TestStripJSONGrantsKeepsLargeIntegers: generic decoding must keep int64
// values exact (a sequence's default MAXVALUE is 2^63-1).
func TestStripJSONGrantsKeepsLargeIntegers(t *testing.T) {
	raw := []byte(`{"kind":"sequence","sequence":{"schema":"public","name":"s","max_value":9223372036854775807,"grants":[{"roles":["a"]}]}}`)
	out, err := stripJSONGrants(raw)
	if err != nil {
		t.Fatal(err)
	}
	var so snapshot.SnapObject
	if err := json.Unmarshal(out, &so); err != nil {
		t.Fatalf("re-decode: %v (%s)", err, out)
	}
	if so.Sequence.MaxValue == nil || *so.Sequence.MaxValue != 9223372036854775807 || len(so.Sequence.Grants) != 0 {
		t.Errorf("got %s", out)
	}
}

// TestMaskRoleMemberships: memberships are matched by direction and role;
// options the snapshot doesn't record are cleared.
func TestMaskRoleMemberships(t *testing.T) {
	live := &ir.Role{Name: "r", Memberships: []ir.RoleMembership{
		{Role: "p", Direction: "IN_ROLE", Inherit: boolp(true), Set: boolp(true)},
		{Role: "c", Direction: "ROLE", Inherit: boolp(true), Set: boolp(false)},
	}}
	so := &snapshot.SnapObject{Kind: "role", Role: &snapshot.SnapRole{Name: "r", Memberships: []snapshot.SnapRoleMembership{
		{Role: "p", Direction: "IN_ROLE"},
		{Role: "c", Direction: "ROLE", Set: boolp(false)},
	}}}
	m := mask(live, so).(*ir.Role)
	if m.Memberships[0].Inherit != nil || m.Memberships[0].Set != nil {
		t.Errorf("undeclared options must be cleared: %+v", m.Memberships[0])
	}
	if m.Memberships[1].Inherit != nil || m.Memberships[1].Set == nil {
		t.Errorf("declared SET must be kept, INHERIT cleared: %+v", m.Memberships[1])
	}
}

// TestMaskOwnSchemaQualification: the catalog's schema-qualified OWNED BY
// equals the source's unqualified one.
func TestMaskOwnSchemaQualification(t *testing.T) {
	live := &ir.Sequence{Schema: "public", Name: "s", OwnedBy: strp("public.orders.id")}
	so := &snapshot.SnapObject{Kind: "sequence", Sequence: &snapshot.SnapSequence{Schema: "public", Name: "s", OwnedBy: strp("orders.id")}}
	if m := mask(live, so).(*ir.Sequence); m.OwnedBy == nil || *m.OwnedBy != "orders.id" {
		t.Errorf("got %v", m.OwnedBy)
	}
	other := &ir.Sequence{Schema: "public", Name: "s", OwnedBy: strp("public.other.id")}
	if m := mask(other, so).(*ir.Sequence); *m.OwnedBy != "public.other.id" {
		t.Errorf("a different target must stay: %v", *m.OwnedBy)
	}
}

// TestMaskSourceOnlyFields: DEPRECATED is not in the catalog; the masked
// object takes the snapshot's value.
func TestMaskSourceOnlyFields(t *testing.T) {
	live := &ir.Table{Schema: "public", Name: "t"}
	so := &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{Schema: "public", Name: "t", Deprecated: strp("use t2"), Protected: true}}
	m := mask(live, so).(*ir.Table)
	if m.Deprecated == nil || *m.Deprecated != "use t2" || !m.Protected {
		t.Errorf("got %+v", m)
	}
}

func TestUnescapeName(t *testing.T) {
	for _, n := range []string{"plain", "with/slash:colon%pct and space", "üñï"} {
		if got := unescapeName(escapeName(n)); got != n {
			t.Errorf("%q → %q → %q", n, escapeName(n), got)
		}
	}
}

// TestMaskOperatorClassMembers: members are matched by number and operand
// types; the pg_catalog schema the catalog reports for a default operator
// is cleared when the source didn't write one.
func TestMaskOperatorClassMembers(t *testing.T) {
	live := &ir.OperatorClass{Schema: "public", Name: "ops", AccessMethod: "btree", Members: []pipeline.OpFamilyMember{
		{Number: 1, Name: pipeline.Identifier{Schema: "pg_catalog", Name: "<"}, LeftType: "integer", RightType: "integer"},
		{Number: 1, IsFunction: true, Name: pipeline.Identifier{Schema: "pg_catalog", Name: "btint4cmp"}, LeftType: "integer", RightType: "integer"},
	}}
	so := &snapshot.SnapObject{Kind: "operator_class", Opaque: &snapshot.SnapOpaque{Kind: "operator_class", Schema: "public", Name: "ops",
		OperatorClassMembers: []snapshot.SnapOpFamilyMember{
			{Number: 1, Name: "<", LeftType: "integer", RightType: "integer"},
			{Number: 1, IsFunction: true, NameSchema: "pg_catalog", Name: "btint4cmp", LeftType: "integer", RightType: "integer"},
		}}}
	m := mask(live, so).(*ir.OperatorClass)
	if m.Members[0].Name.Schema != "" {
		t.Errorf("undeclared operator schema must be cleared: %+v", m.Members[0].Name)
	}
	if m.Members[1].Name.Schema != "pg_catalog" {
		t.Errorf("declared function schema must be kept: %+v", m.Members[1].Name)
	}
	if live.Members[0].Name.Schema != "pg_catalog" {
		t.Error("mask must not modify the live members")
	}
}
