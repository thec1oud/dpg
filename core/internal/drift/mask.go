package drift

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// sourceOnlyFields are lifecycle directives kept in the snapshot but not in
// the catalog; the masked object takes the snapshot's value.
var sourceOnlyFields = map[string]bool{"Deprecated": true, "Protected": true, "DropCascade": true, "CopyFrom": true}

// identityFields name an object or sub-object; they are never masked. Schema
// is not among them: an extension's schema is a property, and every
// schema-scoped kind's snapshot records its schema anyway.
var identityFields = map[string]bool{"Name": true, "Args": true}

// opaquePrefix is the field-name prefix the snapshot's opaque record uses
// for a kind's own properties (Collation.Owner ↔ CollationOwner).
var opaquePrefix = map[string]string{
	"tablespace": "Tablespace", "collation": "Collation", "server": "Server",
	"procedure": "Procedure", "aggregate": "Aggregate", "fdw": "FDW",
	"publication": "Publication", "subscription": "Subscription",
	"event_trigger": "EventTrigger", "ts_dict": "TSDict", "ts_config": "TSConfig",
	"operator": "Operator", "operator_class": "OperatorClass", "operator_family": "OpFamily",
	"statistics": "Statistics", "cast": "Cast", "user_mapping": "UserMapping",
}

// snapStruct returns the populated per-kind struct of so.
func snapStruct(so *snapshot.SnapObject) reflect.Value {
	v := reflect.ValueOf(so).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Ptr && !f.IsNil() {
			return f.Elem()
		}
	}
	return reflect.Value{}
}

// mask returns a copy of the live object with every property the snapshot
// does not record cleared, so the differ, which compares only what its
// desired side declares, compares exactly the declared properties (RFC
// Section 18.3). live itself is not modified.
func mask(live pipeline.IRObject, so *snapshot.SnapObject) pipeline.IRObject {
	snap := snapStruct(so)
	lv := reflect.ValueOf(live)
	if !snap.IsValid() || lv.Kind() != reflect.Ptr || lv.Elem().Kind() != reflect.Struct {
		return live
	}
	out := reflect.New(lv.Elem().Type())
	out.Elem().Set(lv.Elem())
	prefix := ""
	if so.Opaque != nil {
		prefix = opaquePrefix[so.Kind]
	}
	maskStruct(out.Elem(), snap, prefix)
	unqualifyOwnSchema(out.Elem(), snap)
	if so.Opaque != nil && so.Opaque.OperatorClassFamilyImplicit {
		// The source named no family: the differ then takes the family
		// from the snapshot, as it does for the source itself.
		for _, n := range []string{"FamilySchema", "FamilyName"} {
			if f := out.Elem().FieldByName(n); f.IsValid() && f.Kind() == reflect.String {
				f.SetString("")
			}
		}
	}
	return out.Interface().(pipeline.IRObject)
}

// snapField finds the snapshot field recording the live field name, trying
// name itself and then prefix+name (a nested live struct flattened into its
// parent's snapshot record, e.g. Column.Identity.Cache ↔ IdentityCache).
func snapField(snap reflect.Value, prefix, name string) (reflect.Value, bool) {
	if f := snap.FieldByName(name); f.IsValid() {
		return f, true
	}
	if prefix != "" {
		if f := snap.FieldByName(prefix + name); f.IsValid() {
			return f, true
		}
	}
	return reflect.Value{}, false
}

// maskStruct masks the fields of the addressable struct v against snap.
// prefix is the name of the enclosing live field when v is a nested struct
// whose properties the snapshot records flattened into the parent.
func maskStruct(v, snap reflect.Value, prefix string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() || (identityFields[sf.Name] && sf.Type.Kind() == reflect.String) {
			continue
		}
		f := v.Field(i)
		if sourceOnlyFields[sf.Name] {
			if s, ok := snapField(snap, prefix, sf.Name); ok && s.Type().AssignableTo(f.Type()) {
				f.Set(s)
			}
			continue
		}
		if sf.Name == "Grants" || sf.Name == "Revocations" {
			// Compared tuple by tuple in grants.go; the differ sees exactly
			// the declared set.
			if s, ok := snapField(snap, "", sf.Name); ok && f.Kind() == reflect.Slice && s.Kind() == reflect.Slice {
				f.Set(copyGrantSlice(f, s))
			} else {
				clearUndeclared(f)
			}
			continue
		}
		s, ok := snapField(snap, prefix, sf.Name)
		if !ok {
			// No snapshot field of this name: a nested struct whose
			// properties are flattened into the snapshot record is masked
			// field by field; anything else (source-only directives, bodies
			// the snapshot keeps as a hash) is kept.
			maskNested(f, snap, sf.Name)
			continue
		}
		if s.IsZero() {
			clearUndeclared(f)
			continue
		}
		switch {
		case sf.Name == "Constraints" && isStructSlice(f) && isStructSlice(s):
			f.Set(maskSlice(pairUnnamedConstraints(f, s), s))
		case isStructSlice(f) && isStructSlice(s):
			f.Set(maskSlice(f, s))
		case structOf(f).IsValid() && structOf(s).IsValid():
			maskStructValue(f, structOf(s), "")
		case structOf(f).IsValid():
			// The snapshot records this nested struct as a scalar (e.g.
			// Column.Identity ↔ Identity "ALWAYS") plus flattened fields.
			maskNested(f, snap, sf.Name)
		}
	}
}

// clearUndeclared clears a field the snapshot does not record. Booleans and
// numbers are not cleared: their zero value is itself a declaration (a table
// not declared UNLOGGED is logged), so the differ always compares them.
func clearUndeclared(f reflect.Value) {
	switch f.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface, reflect.String:
		f.Set(reflect.Zero(f.Type()))
	case reflect.Struct:
		f.Set(reflect.Zero(f.Type()))
	}
}

// maskNested masks a struct or pointer-to-struct field against the
// enclosing snapshot record, with name as the flattening prefix.
func maskNested(f, snap reflect.Value, name string) {
	switch {
	case f.Kind() == reflect.Struct:
		maskStruct(f, snap, name)
	case f.Kind() == reflect.Ptr && !f.IsNil() && f.Elem().Kind() == reflect.Struct:
		cp := reflect.New(f.Elem().Type())
		cp.Elem().Set(f.Elem())
		maskStruct(cp.Elem(), snap, name)
		f.Set(cp)
	}
}

// maskStructValue masks a struct or pointer-to-struct field f against the
// snapshot struct s, copying a pointed-to struct before changing it.
func maskStructValue(f, s reflect.Value, prefix string) {
	switch f.Kind() {
	case reflect.Struct:
		maskStruct(f, s, prefix)
	case reflect.Ptr:
		if f.IsNil() {
			return
		}
		cp := reflect.New(f.Elem().Type())
		cp.Elem().Set(f.Elem())
		maskStruct(cp.Elem(), s, prefix)
		f.Set(cp)
	}
}

// structOf returns the struct v holds directly or through a non-nil pointer.
func structOf(v reflect.Value) reflect.Value {
	switch {
	case v.Kind() == reflect.Struct:
		return v
	case v.Kind() == reflect.Ptr && !v.IsNil() && v.Elem().Kind() == reflect.Struct:
		return v.Elem()
	}
	return reflect.Value{}
}

func isStructSlice(v reflect.Value) bool {
	if v.Kind() != reflect.Slice {
		return false
	}
	e := v.Type().Elem()
	if e.Kind() == reflect.Ptr {
		e = e.Elem()
	}
	return e.Kind() == reflect.Struct
}

// elemName returns an element's identity within its list: for an operator
// family or class member its kind, number and operand types (its name
// alone repeats); else its Name field, or for a role membership its
// direction and role; "" when it has none of these.
func elemName(v reflect.Value) string {
	s := structOf(v)
	if !s.IsValid() {
		return ""
	}
	// An operator family or class member: function or operator, strategy
	// or support number, operand types.
	if n, fn := s.FieldByName("Number"), s.FieldByName("IsFunction"); n.IsValid() && fn.IsValid() {
		return fmt.Sprintf("%v#%v(%v,%v)", fn.Interface(), n.Interface(),
			s.FieldByName("LeftType").Interface(), s.FieldByName("RightType").Interface())
	}
	if n := s.FieldByName("Name"); n.IsValid() && n.Kind() == reflect.String {
		return n.String()
	}
	if r, d := s.FieldByName("Role"), s.FieldByName("Direction"); r.IsValid() && d.IsValid() &&
		r.Kind() == reflect.String && d.Kind() == reflect.String {
		return d.String() + ":" + r.String()
	}
	return ""
}

// unqualifyOwnSchema sets a top-level string field of v to the snapshot's
// value when the catalog qualified with the object's own schema what the
// source wrote unqualified (Sequence OWNED BY public.orders.id vs
// orders.id).
func unqualifyOwnSchema(v, snap reflect.Value) {
	schema := v.FieldByName("Schema")
	if !schema.IsValid() || schema.Kind() != reflect.String || schema.String() == "" {
		return
	}
	prefix := schema.String() + "."
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Name
		if identityFields[name] || name == "Schema" {
			continue
		}
		f := v.Field(i)
		s := snap.FieldByName(name)
		if !s.IsValid() {
			continue
		}
		lv, sv, ok := stringValues(f, s)
		if ok && sv != "" && lv == prefix+sv {
			f.Set(s.Convert(f.Type()))
		}
	}
}

// stringValues returns the values of two string or *string fields of the
// same type.
func stringValues(a, b reflect.Value) (string, string, bool) {
	if a.Type() != b.Type() {
		return "", "", false
	}
	switch a.Kind() {
	case reflect.String:
		return a.String(), b.String(), true
	case reflect.Ptr:
		if a.Type().Elem().Kind() != reflect.String || a.IsNil() || b.IsNil() {
			return "", "", false
		}
		return a.Elem().String(), b.Elem().String(), true
	}
	return "", "", false
}

// maskSlice masks a slice of live sub-objects against the snapshot's. An
// element is matched by name: a matched element is masked; a live element
// the snapshot doesn't record is kept whole (an extra sub-object is drift).
// Elements without a name (grants, labels) are kept as they are.
func maskSlice(f, s reflect.Value) reflect.Value {
	byName := map[string]reflect.Value{}
	for i := 0; i < s.Len(); i++ {
		if n := elemName(s.Index(i)); n != "" {
			byName[n] = s.Index(i)
		}
	}
	out := reflect.MakeSlice(f.Type(), f.Len(), f.Len())
	for i := 0; i < f.Len(); i++ {
		e := f.Index(i)
		out.Index(i).Set(e)
		snapElem, ok := byName[elemName(e)]
		if !ok || elemName(e) == "" {
			continue
		}
		maskStructValue(out.Index(i), structOf(snapElem), "")
	}
	return out
}

// pairUnnamedConstraints returns a copy of the live constraints in which
// each one paired with a constraint the source declared without a name
// takes that constraint's empty name and expression, so the differ matches
// them structurally. PostgreSQL always names a constraint; the snapshot
// keeps the source's empty name. A live constraint pairs with an unnamed
// snapshot constraint of the same type when it is the only candidate of
// that type, else when both reference the same columns (or, for CHECK, have
// the same normalized expression).
func pairUnnamedConstraints(live, snap reflect.Value) reflect.Value {
	named := map[string]bool{}
	var unnamed []reflect.Value
	for i := 0; i < snap.Len(); i++ {
		e := structOf(snap.Index(i))
		if n := e.FieldByName("Name").String(); n != "" {
			named[n] = true
		} else {
			unnamed = append(unnamed, e)
		}
	}
	out := reflect.MakeSlice(live.Type(), live.Len(), live.Len())
	reflect.Copy(out, live)
	if len(unnamed) == 0 {
		return out
	}
	used := make([]bool, len(unnamed))
	typeOf := func(v reflect.Value) string { return strings.ToUpper(v.FieldByName("Type").String()) }
	exprOf := func(v reflect.Value) string { return v.FieldByName("Expr").String() }
	for i := 0; i < out.Len(); i++ {
		l := structOf(out.Index(i))
		if !l.IsValid() || named[l.FieldByName("Name").String()] {
			continue
		}
		var candidates []int
		for j, u := range unnamed {
			if !used[j] && typeOf(u) == typeOf(l) {
				candidates = append(candidates, j)
			}
		}
		pick := -1
		if len(candidates) == 1 {
			pick = candidates[0]
		} else {
			for _, j := range candidates {
				if constraintSignature(typeOf(l), exprOf(l)) == constraintSignature(typeOf(l), exprOf(unnamed[j])) {
					pick = j
					break
				}
			}
		}
		if pick < 0 {
			continue
		}
		used[pick] = true
		cp := reflect.New(l.Type())
		cp.Elem().Set(l)
		cp.Elem().FieldByName("Name").SetString("")
		cp.Elem().FieldByName("Expr").SetString(exprOf(unnamed[pick]))
		if out.Index(i).Kind() == reflect.Ptr {
			out.Index(i).Set(cp)
		} else {
			out.Index(i).Set(cp.Elem())
		}
	}
	return out
}

// constraintSignature reduces a constraint expression to what identifies
// it across source spelling and PostgreSQL's deparsed form: the referenced
// column list for key constraints, else the expression without quotes,
// parentheses, whitespace and case.
func constraintSignature(typ, expr string) string {
	switch typ {
	case "PRIMARY KEY", "UNIQUE", "FOREIGN KEY", "EXCLUDE":
		if open := strings.IndexByte(expr, '('); open >= 0 {
			if close := strings.IndexByte(expr[open:], ')'); close >= 0 {
				expr = expr[open+1 : open+close]
			}
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(expr) {
		switch r {
		case ' ', '\t', '\n', '(', ')', '"':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
