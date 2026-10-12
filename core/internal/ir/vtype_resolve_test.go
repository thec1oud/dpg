package ir

import (
	"errors"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func vt(schema, name string, body VtypeBody) *VirtualType {
	return &VirtualType{Schema: schema, Name: name, Body: body}
}

// TestResolveVirtualTypesColumn guards RFC Section 5.6: a column typed as a
// virtual type stores json/jsonb (array dimensions kept) and records the
// qualified reference.
func TestResolveVirtualTypesColumn(t *testing.T) {
	tbl := &Table{Schema: "public", Name: "orders", Columns: []*Column{
		{Name: "items", Type: TypeRef{Name: "line_item", ArrayDims: 1}},
		{Name: "note", Type: TypeRef{Name: "text"}},
	}}
	objs := []pipeline.IRObject{vt("public", "line_item", VtypeTypeRef{Name: "text"}), tbl}
	if err := ResolveVirtualTypes(objs, "public"); err != nil {
		t.Fatal(err)
	}
	items := tbl.Columns[0]
	if got := items.Type.String(); got != "jsonb[]" {
		t.Errorf("Type = %q, want jsonb[]", got)
	}
	if items.VirtualType == nil || items.VirtualType.String() != "public.line_item[]" {
		t.Errorf("VirtualType = %v, want public.line_item[]", items.VirtualType)
	}
	if tbl.Columns[1].VirtualType != nil || tbl.Columns[1].Type.String() != "text" {
		t.Errorf("ordinary column changed: %+v", tbl.Columns[1])
	}
}

// TestResolveVirtualTypesLookupOrder guards RFC Section 5.6: an unqualified
// reference resolves in the referencing object's schema first, then the
// default schema.
func TestResolveVirtualTypesLookupOrder(t *testing.T) {
	own := &Table{Schema: "billing", Name: "a", Columns: []*Column{{Name: "c", Type: TypeRef{Name: "money_amount"}}}}
	other := &Table{Schema: "crm", Name: "b", Columns: []*Column{{Name: "c", Type: TypeRef{Name: "money_amount"}}}}
	vtJSON := vt("billing", "money_amount", VtypeTypeRef{Name: "numeric"})
	vtJSON.JsonFormat = "json"
	objs := []pipeline.IRObject{
		vtJSON,
		vt("app", "money_amount", VtypeTypeRef{Name: "numeric"}),
		own, other,
	}
	if err := ResolveVirtualTypes(objs, "app"); err != nil {
		t.Fatal(err)
	}
	if got := own.Columns[0].VirtualType.String(); got != "billing.money_amount" {
		t.Errorf("own-schema reference resolved to %q", got)
	}
	if got := own.Columns[0].Type.String(); got != "json" {
		t.Errorf("own-schema column type = %q, want json", got)
	}
	if got := other.Columns[0].VirtualType.String(); got != "app.money_amount" {
		t.Errorf("default-schema reference resolved to %q", got)
	}
}

// TestResolveVirtualTypesBodyRefs guards RFC Section 5.6: a body reference
// naming another virtual type is qualified; one naming a PostgreSQL type
// is left alone.
func TestResolveVirtualTypesBodyRefs(t *testing.T) {
	line := vt("public", "line_item", VtypeComposite{Fields: []VtypeField{
		{Name: "at", Type: VtypeTypeRef{Name: "point"}},
		{Name: "tags", Type: VtypeTypeRef{Name: "label", IsArray: true}},
	}})
	objs := []pipeline.IRObject{vt("public", "label", VtypeTypeRef{Name: "text"}), line}
	if err := ResolveVirtualTypes(objs, "public"); err != nil {
		t.Fatal(err)
	}
	fields := line.Body.(VtypeComposite).Fields
	if fields[0].Type.Schema != "" || fields[0].Type.Virtual {
		t.Errorf("built-in point was qualified: %+v", fields[0].Type)
	}
	if fields[1].Type.Schema != "public" || !fields[1].Type.Virtual || !fields[1].Type.IsArray {
		t.Errorf("virtual type reference not qualified: %+v", fields[1].Type)
	}
}

// TestResolveVirtualTypesNameConflicts guards RFC Section 5.6's DPG-E044.
func TestResolveVirtualTypesNameConflicts(t *testing.T) {
	cases := []struct {
		name string
		objs []pipeline.IRObject
		want string
	}{
		{"builtin", []pipeline.IRObject{vt("public", "point", VtypeTypeRef{Name: "text"})}, "built-in"},
		{"sql spelling", []pipeline.IRObject{vt("public", "integer", VtypeTypeRef{Name: "text"})}, "built-in"},
		{"table", []pipeline.IRObject{
			vt("public", "orders", VtypeTypeRef{Name: "text"}),
			&Table{Schema: "public", Name: "orders"},
		}, "TABLE"},
		{"type", []pipeline.IRObject{
			vt("public", "mood", VtypeTypeRef{Name: "text"}),
			&Type{Schema: "public", Name: "mood", Variant: "ENUM"},
		}, "TYPE"},
		{"multirange", []pipeline.IRObject{
			vt("public", "floatmultirange", VtypeTypeRef{Name: "text"}),
			&Type{Schema: "public", Name: "floatrange", Variant: "RANGE", MultirangeName: "floatmultirange"},
		}, "multirange"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ResolveVirtualTypes(c.objs, "public")
			var ce *pipeline.CompilerError
			if !errors.As(err, &ce) || ce.Code != "DPG-E044" || !strings.Contains(ce.Message, c.want) {
				t.Errorf("got %v, want DPG-E044 mentioning %q", err, c.want)
			}
		})
	}
	// The same name in a different schema is not a conflict.
	ok := []pipeline.IRObject{
		vt("api", "orders", VtypeTypeRef{Name: "text"}),
		&Table{Schema: "public", Name: "orders"},
	}
	if err := ResolveVirtualTypes(ok, "public"); err != nil {
		t.Errorf("cross-schema name rejected: %v", err)
	}
}

func TestResolveVirtualTypesRejectsTypmod(t *testing.T) {
	tbl := &Table{Schema: "public", Name: "t", Columns: []*Column{{Name: "c", Type: TypeRef{Name: "label", Mods: "(3)"}}}}
	err := ResolveVirtualTypes([]pipeline.IRObject{vt("public", "label", VtypeTypeRef{Name: "text"}), tbl}, "public")
	if err == nil || !strings.Contains(err.Error(), "type modifier") {
		t.Errorf("got %v, want a type modifier error", err)
	}
}
