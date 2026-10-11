// Package merger implements pipeline.Merger. It merges same-object IRObject
// declarations across multiple .dpg files per RFC Section 3.7 set/scalar merge rules.
package merger

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func init() {
	pipeline.Default.Register(pipeline.KeyMerger, New())
}

// Merger implements pipeline.Merger.
type Merger struct{}

// New returns a Merger.
func New() *Merger { return &Merger{} }

// Merge groups IRObjects by QualifiedName + type, then merges within each group
// (RFC Section 3.7). Set-valued fields are unioned; a scalar field may be set
// by any number of declarations as long as they all agree. Conflicting
// same-named set members (DPG-E005) and conflicting scalar values (DPG-E039)
// are CompilerErrors, so the result never depends on file names or layout.
func (m *Merger) Merge(objects []pipeline.IRObject) ([]pipeline.IRObject, error) {
	// Group by (type-tag, qualified-name).
	type key struct{ tag, name string }
	groups := make(map[key][]pipeline.IRObject)
	var order []key

	for _, obj := range objects {
		k := key{tag: typeTag(obj), name: obj.QualifiedName()}
		if _, exists := groups[k]; !exists {
			order = append(order, k)
		}
		groups[k] = append(groups[k], obj)
	}

	// Groups are sorted by source file path so diagnostics are reported in a
	// deterministic order.
	var result []pipeline.IRObject
	var diags pipeline.Diagnostics

	for _, k := range order {
		grp := groups[k]
		if len(grp) == 1 {
			result = append(result, grp[0])
			continue
		}
		sort.Slice(grp, func(i, j int) bool {
			return grp[i].Pos().File < grp[j].Pos().File
		})
		merged, conflicts, err := mergeGroup(grp)
		for _, c := range conflicts {
			diags = append(diags, pipeline.ErrorfCode(c.Pos, "DPG-E039", "%s", c.Message))
		}
		if err != nil {
			if diag, ok := err.(*pipeline.CompilerError); ok {
				diags = append(diags, diag)
				continue
			}
			return nil, err
		}
		result = append(result, merged)
	}

	if diags.HasErrors() {
		return result, diags
	}
	return result, nil
}

// typeTag returns a short string tag for the concrete IR type.
func typeTag(obj pipeline.IRObject) string {
	switch obj.(type) {
	case *ir.Table:
		return "TABLE"
	case *ir.View:
		return "VIEW"
	case *ir.Function:
		return "FUNCTION"
	case *ir.Procedure:
		return "PROCEDURE"
	case *ir.Aggregate:
		return "AGGREGATE"
	case *ir.Type:
		return "TYPE"
	case *ir.Sequence:
		return "SEQUENCE"
	case *ir.Schema:
		return "SCHEMA"
	case *ir.Extension:
		return "EXTENSION"
	case *ir.Role:
		return "ROLE"
	case *ir.Tablespace:
		return "TABLESPACE"
	case *ir.ForeignDataWrapper:
		return "FDW"
	case *ir.ForeignServer:
		return "SERVER"
	case *ir.UserMapping:
		return "USER MAPPING"
	default:
		return fmt.Sprintf("%T", obj)
	}
}

// objDescFor returns a short, lowercase, human-readable description of obj
// for use in a scalar conflict message (e.g. "table
// public.users", "foreign data wrapper my_fdw") — distinct from typeTag's
// grouping-key tag, which favors brevity over readability.
func objDescFor(obj pipeline.IRObject) string {
	switch o := obj.(type) {
	case *ir.Table:
		return "table " + o.QualifiedName()
	case *ir.View:
		return "view " + o.QualifiedName()
	case *ir.Function:
		return "function " + o.QualifiedName()
	case *ir.Procedure:
		return "procedure " + o.QualifiedName()
	case *ir.Aggregate:
		return "aggregate " + o.QualifiedName()
	case *ir.Type:
		if o.Variant == "DOMAIN" {
			return "domain " + o.QualifiedName()
		}
		return "type " + o.QualifiedName()
	case *ir.Sequence:
		return "sequence " + o.QualifiedName()
	case *ir.Schema:
		return "schema " + o.QualifiedName()
	case *ir.Extension:
		return "extension " + o.QualifiedName()
	case *ir.Role:
		return "role " + o.QualifiedName()
	case *ir.Tablespace:
		return "tablespace " + o.QualifiedName()
	case *ir.ForeignDataWrapper:
		return "foreign data wrapper " + o.QualifiedName()
	case *ir.ForeignServer:
		return "server " + o.QualifiedName()
	case *ir.UserMapping:
		return "user mapping " + o.QualifiedName()
	default:
		return fmt.Sprintf("%T %s", obj, obj.QualifiedName())
	}
}

// mergeGroup merges a non-empty slice of same-name, same-type IRObjects.
// The returned conflicts become DPG-E039 errors in Merge.
func mergeGroup(grp []pipeline.IRObject) (pipeline.IRObject, []pipeline.LintDiagnostic, error) {
	switch grp[0].(type) {
	case *ir.Table:
		return mergeObjects(grp, tableRules)
	case *ir.Type:
		return mergeObjects(grp, typeRules)
	case *ir.View, *ir.Function, *ir.Procedure, *ir.Aggregate, *ir.Schema,
		*ir.Sequence, *ir.Extension, *ir.Role, *ir.Tablespace,
		*ir.ForeignDataWrapper, *ir.ForeignServer, *ir.UserMapping:
		return mergeObjects(grp, nil)
	default:
		// Kinds with no field-level merge (Publication, EventTrigger, Cast,
		// TS*, ...): repeated declarations must be identical.
		return mergeIdentical(grp)
	}
}

// mergeIdentical merges a group of declarations for a kind with no
// field-level merge rules: every declaration must describe the same object,
// compared through its snapshot form (which excludes source positions).
func mergeIdentical(grp []pipeline.IRObject) (pipeline.IRObject, []pipeline.LintDiagnostic, error) {
	first, err := snapForm(grp[0])
	if err != nil {
		return nil, nil, err
	}
	var conflicts []pipeline.LintDiagnostic
	for _, obj := range grp[1:] {
		next, err := snapForm(obj)
		if err != nil {
			return nil, nil, err
		}
		if next != first {
			conflicts = append(conflicts, pipeline.LintDiagnostic{
				Pos: obj.Pos(),
				Message: fmt.Sprintf("%s is declared in %s and %s with different definitions; declare it once, or identically",
					objDescFor(obj), grp[0].Pos().File, obj.Pos().File),
			})
		}
	}
	return grp[0], conflicts, nil
}

// snapForm renders obj's snapshot representation as JSON for comparison.
func snapForm(obj pipeline.IRObject) (string, error) {
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, []pipeline.IRObject{obj}); err != nil {
		return "", err
	}
	b, err := json.Marshal(snap.Objects)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ── field-level merge ────────────────────────────────────────────────────────

// fieldMerger merges one struct field of a later declaration (src) into the
// merged object (dst).
type fieldMerger func(t *conflictTracker, dst, src reflect.Value, pos pipeline.SourcePos) error

// unionFields are slice fields every kind merges by union: each entry is
// independent, so two files may contribute different entries (RFC Section
// 3.7's set-valued properties). Identical entries are deduplicated.
var unionFields = map[string]bool{
	"Grants": true, "Revocations": true, "SecurityLabels": true,
	"Memberships": true, "Configs": true, "DependsOnExtensions": true,
	"Partitions": true, "NameMaps": true,
}

var (
	sourcePosType     = reflect.TypeOf(pipeline.SourcePos{})
	storageParamsType = reflect.TypeOf([]pipeline.StorageParam(nil))
)

// tableRules are Table's name-keyed sub-object collections: a same-named
// member declared twice must be identical (DPG-E005), and columns merge
// field by field.
var tableRules = map[string]fieldMerger{
	"Indexes":     unionVia(unionIndexes),
	"Policies":    unionVia(unionPolicies),
	"Triggers":    unionVia(unionTriggers),
	"Constraints": unionVia(unionConstraints),
	"Columns":     mergeColumnsField,
}

// typeRules: enum values and domain constraints are set-valued.
var typeRules = map[string]fieldMerger{
	"EnumValues": func(t *conflictTracker, dst, src reflect.Value, pos pipeline.SourcePos) error {
		dst.Set(reflect.ValueOf(unionStrings(dst.Interface().([]string), src.Interface().([]string))))
		return nil
	},
	"DomainConstraints": unionVia(unionConstraints),
}

// unionVia adapts a typed name-keyed union function to a fieldMerger.
func unionVia[T any](union func(a, b []T) ([]T, error)) fieldMerger {
	return func(t *conflictTracker, dst, src reflect.Value, pos pipeline.SourcePos) error {
		merged, err := union(dst.Interface().([]T), src.Interface().([]T))
		if err != nil {
			return err
		}
		dst.Set(reflect.ValueOf(merged))
		return nil
	}
}

// mergeObjects merges every declaration in grp into a copy of grp[0] (RFC
// Section 3.7), field by field: see mergeFields.
func mergeObjects(grp []pipeline.IRObject, rules map[string]fieldMerger) (pipeline.IRObject, []pipeline.LintDiagnostic, error) {
	base := reflect.ValueOf(grp[0]).Elem()
	merged := reflect.New(base.Type())
	merged.Elem().Set(base)
	tracker := newConflictTracker(objDescFor(grp[0]), grp[0].Pos().File)
	for _, obj := range grp[1:] {
		next := reflect.ValueOf(obj).Elem()
		if next.Type() != base.Type() {
			continue
		}
		if err := mergeFields(tracker, "", merged.Elem(), next, obj.Pos(), rules); err != nil {
			return nil, nil, err
		}
	}
	return merged.Interface().(pipeline.IRObject), tracker.diags, nil
}

// mergeFields merges src into dst one exported field at a time:
//
//   - a field named in rules uses that merger;
//   - a union field (unionFields) is unioned;
//   - a []pipeline.StorageParam field merges key by key, each key being its
//     own scalar property;
//   - a bool is OR-ed (false cannot express "not declared");
//   - any other field is a scalar: a value declared in only one place is
//     taken, and two declared values must be equal (ignoring source
//     positions) or the property is recorded as a conflict.
//
// Source positions are never merged or compared.
func mergeFields(t *conflictTracker, prefix string, dst, src reflect.Value, pos pipeline.SourcePos, rules map[string]fieldMerger) error {
	typ := dst.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() || f.Type == sourcePosType {
			continue
		}
		d, s := dst.Field(i), src.Field(i)
		if fm, ok := rules[f.Name]; ok {
			if err := fm(t, d, s, pos); err != nil {
				return err
			}
			continue
		}
		property := prefix + propertyName(f.Name)
		switch {
		case f.Type == storageParamsType:
			merged := t.checkStorageParams(property, d.Interface().([]pipeline.StorageParam), s.Interface().([]pipeline.StorageParam), pos.File, pos)
			d.Set(reflect.ValueOf(merged))
		case f.Type.Kind() == reflect.Slice && unionFields[f.Name]:
			d.Set(unionSlice(d, s))
		case f.Type.Kind() == reflect.Bool:
			if s.Bool() {
				d.SetBool(true)
			}
		default:
			if s.IsZero() || (s.Kind() == reflect.Slice && s.Len() == 0) {
				continue
			}
			if d.IsZero() || (d.Kind() == reflect.Slice && d.Len() == 0) {
				d.Set(s)
			} else if !equalNoPos(d, s) {
				t.conflict(property, formatValue(d), formatValue(s), pos.File, pos)
			}
			t.lastFile[property] = pos.File
		}
	}
	return nil
}

// mergeColumnsField merges a table's column list: a column new to the merged
// table is appended, and a column declared in more than one file is merged
// field by field like any other object.
func mergeColumnsField(t *conflictTracker, dst, src reflect.Value, pos pipeline.SourcePos) error {
	cols := dst.Interface().([]*ir.Column)
	byName := make(map[string]int, len(cols))
	result := make([]*ir.Column, len(cols))
	for i, c := range cols {
		cp := *c
		result[i] = &cp
		byName[c.Name] = i
	}
	for _, next := range src.Interface().([]*ir.Column) {
		i, ok := byName[next.Name]
		if !ok {
			cp := *next
			result = append(result, &cp)
			byName[next.Name] = len(result) - 1
			continue
		}
		if err := mergeFields(t, "column "+next.Name+" ", reflect.ValueOf(result[i]).Elem(), reflect.ValueOf(next).Elem(), pos, nil); err != nil {
			return err
		}
	}
	dst.Set(reflect.ValueOf(result))
	return nil
}

// unionSlice appends every element of s not already present in d (compared
// ignoring source positions).
func unionSlice(d, s reflect.Value) reflect.Value {
	out := reflect.AppendSlice(reflect.MakeSlice(d.Type(), 0, d.Len()+s.Len()), d)
	for j := 0; j < s.Len(); j++ {
		dup := false
		for k := 0; k < out.Len(); k++ {
			if equalNoPos(out.Index(k), s.Index(j)) {
				dup = true
				break
			}
		}
		if !dup {
			out = reflect.Append(out, s.Index(j))
		}
	}
	return out
}

// equalNoPos is reflect.DeepEqual that ignores every pipeline.SourcePos value.
func equalNoPos(a, b reflect.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == sourcePosType {
		return true
	}
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equalNoPos(a.Elem(), b.Elem())
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equalNoPos(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !equalNoPos(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !equalNoPos(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.Len() != b.Len() {
			return false
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() || !equalNoPos(a.MapIndex(k), bv) {
				return false
			}
		}
		return true
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return a.Uint() == b.Uint()
	case reflect.Float32, reflect.Float64:
		return a.Float() == b.Float()
	case reflect.String:
		return a.String() == b.String()
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

// formatValue renders a scalar for a conflict message.
func formatValue(v reflect.Value) string {
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() == reflect.String {
		return strconv.Quote(v.String())
	}
	if v.CanInterface() {
		return fmt.Sprintf("%v", v.Interface())
	}
	return v.String()
}

// propertyName turns a Go field name into a lowercase property name for
// messages ("RenamedFrom" -> "renamed from").
func propertyName(field string) string {
	var b strings.Builder
	for i, r := range field {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(field[i-1])
			if prev < 'A' || prev > 'Z' {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// ── scalar conflict tracking ─────────────────────────────────────────────────

// conflictTracker records RFC Section 3.7 scalar conflicts for one object:
// for every scalar property, it remembers which file most recently supplied
// the property's value, so a conflict names both files involved. Merge turns
// each recorded conflict into a DPG-E039 CompilerError.
//
// baseFile is grp[0]'s source file, which owns every property it sets until
// a later file is compared against it.
type conflictTracker struct {
	objDesc  string
	baseFile string
	lastFile map[string]string
	diags    []pipeline.LintDiagnostic
}

func newConflictTracker(objDesc, baseFile string) *conflictTracker {
	return &conflictTracker{objDesc: objDesc, baseFile: baseFile, lastFile: make(map[string]string)}
}

func (t *conflictTracker) attributedFile(property string) string {
	if f, ok := t.lastFile[property]; ok {
		return f
	}
	return t.baseFile
}

func (t *conflictTracker) conflict(property, curFmt, nextFmt, file string, pos pipeline.SourcePos) {
	t.diags = append(t.diags, pipeline.LintDiagnostic{
		Pos: pos,
		Message: fmt.Sprintf("%s: %s is set to %s in %s and to %s in %s; a property may only be declared with one value",
			t.objDesc, property, curFmt, t.attributedFile(property), nextFmt, file),
	})
}

// checkStorageParams merges next into cur, key by key: each option key is
// its own scalar property (addressed as "prefix[key]" in diagnostics).
// Order-preserving: an existing key keeps its position; a new key is
// appended in the order its file declares it.
func (t *conflictTracker) checkStorageParams(propertyPrefix string, cur, next []pipeline.StorageParam, file string, pos pipeline.SourcePos) []pipeline.StorageParam {
	result := append([]pipeline.StorageParam(nil), cur...)
	index := make(map[string]int, len(result))
	for i, p := range result {
		index[p.Key] = i
	}
	for _, p := range next {
		property := propertyPrefix + "[" + p.Key + "]"
		if i, ok := index[p.Key]; ok {
			if result[i].Value != p.Value {
				t.conflict(property, strconv.Quote(result[i].Value), strconv.Quote(p.Value), file, pos)
			}
		} else {
			result = append(result, p)
			index[p.Key] = len(result) - 1
		}
		t.lastFile[property] = file
	}
	return result
}

// ── set-valued member unions (DPG-E005) ─────────────────────────────────────

// equalIgnoringPos reports whether two set-valued members are identical set
// down to every field except Pos (source position necessarily differs
// between the two files declaring "the same" member) — RFC Section 3.7:
// "Identical duplicate entries (same name AND same definition) are silently
// deduplicated. Entries with the same name but different definitions are a
// compiler error (DPG-E005, conflicting set member)."
func equalIgnoringPos[T any](a, b T, clearPos func(*T)) bool {
	ac, bc := a, b
	clearPos(&ac)
	clearPos(&bc)
	return reflect.DeepEqual(ac, bc)
}

func unionIndexes(a, b []*ir.Index) ([]*ir.Index, error) {
	seen := make(map[string]*ir.Index, len(a))
	for _, idx := range a {
		seen[idx.Name] = idx
	}
	result := append([]*ir.Index(nil), a...)
	for _, idx := range b {
		existing, exists := seen[idx.Name]
		if !exists {
			result = append(result, idx)
			seen[idx.Name] = idx
			continue
		}
		if !equalIgnoringPos(*existing, *idx, func(x *ir.Index) { x.Pos = pipeline.SourcePos{} }) {
			return nil, pipeline.ErrorfCode(idx.Pos, "DPG-E005",
				"index %q declared with conflicting definitions across files", idx.Name)
		}
		// Same name + same def → silently deduplicate.
	}
	return result, nil
}

func unionPolicies(a, b []*ir.Policy) ([]*ir.Policy, error) {
	seen := make(map[string]*ir.Policy, len(a))
	for _, p := range a {
		seen[p.Name] = p
	}
	result := append([]*ir.Policy(nil), a...)
	for _, p := range b {
		existing, exists := seen[p.Name]
		if !exists {
			result = append(result, p)
			seen[p.Name] = p
			continue
		}
		if !equalIgnoringPos(*existing, *p, func(x *ir.Policy) { x.Pos = pipeline.SourcePos{} }) {
			return nil, pipeline.ErrorfCode(p.Pos, "DPG-E005",
				"policy %q declared with conflicting definitions across files", p.Name)
		}
	}
	return result, nil
}

func unionTriggers(a, b []*ir.Trigger) ([]*ir.Trigger, error) {
	seen := make(map[string]*ir.Trigger, len(a))
	for _, t := range a {
		seen[t.Name] = t
	}
	result := append([]*ir.Trigger(nil), a...)
	for _, t := range b {
		existing, exists := seen[t.Name]
		if !exists {
			result = append(result, t)
			seen[t.Name] = t
			continue
		}
		if !equalIgnoringPos(*existing, *t, func(x *ir.Trigger) { x.Pos = pipeline.SourcePos{} }) {
			return nil, pipeline.ErrorfCode(t.Pos, "DPG-E005",
				"trigger %q declared with conflicting definitions across files", t.Name)
		}
	}
	return result, nil
}

func unionConstraints(a, b []*ir.Constraint) ([]*ir.Constraint, error) {
	seen := make(map[string]*ir.Constraint, len(a))
	for _, c := range a {
		if c.Name != "" {
			seen[c.Name] = c
		}
	}
	result := append([]*ir.Constraint(nil), a...)
	for _, c := range b {
		existing, exists := seen[c.Name]
		if c.Name == "" || !exists {
			result = append(result, c)
			if c.Name != "" {
				seen[c.Name] = c
			}
			continue
		}
		if !equalIgnoringPos(*existing, *c, func(x *ir.Constraint) { x.Pos = pipeline.SourcePos{} }) {
			return nil, pipeline.ErrorfCode(c.Pos, "DPG-E005",
				"constraint %q declared with conflicting definitions across files", c.Name)
		}
	}
	return result, nil
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}
	result := append([]string(nil), a...)
	for _, s := range b {
		if !seen[s] {
			result = append(result, s)
			seen[s] = true
		}
	}
	return result
}
