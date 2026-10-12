// Package drift compares the live catalog with the snapshot (RFC Section
// 18.3): only properties the snapshot records are compared, using the
// differ's own equivalence rules.
package drift

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// Group classifies a report item.
type Group int

const (
	// Drift: a declared object or property differs from the snapshot.
	Drift Group = iota
	// Conflict: drift on a property the source has also changed since the
	// snapshot, to a different value.
	Conflict
	// Converged: drift on a property the source has also changed since
	// the snapshot, to the value the database already has. The snapshot is
	// out of date.
	Converged
	// Unmanaged: a live top-level object the snapshot doesn't record.
	Unmanaged
	// Warning: reported, never drift (an undeclared role or tablespace).
	Warning
)

func (g Group) String() string {
	switch g {
	case Drift:
		return "drift"
	case Conflict:
		return "conflict"
	case Converged:
		return "converged"
	case Unmanaged:
		return "unmanaged"
	case Warning:
		return "warning"
	}
	return "unknown"
}

// Failing reports whether items of the group make dpg verify exit 1.
func (g Group) Failing() bool { return g == Drift || g == Conflict || g == Converged }

// Item is one finding.
type Item struct {
	// ID identifies the item stably: kind:object[/property] (see ItemID).
	ID     string
	Group  Group
	Kind   string // snapshot kind ("table", "role", ...)
	Object string // snapshot key
	// Property is "" for a whole object, else the property or sub-object
	// path, e.g. "grant:SELECT:app" or "column:c/grant:SELECT:app".
	Property string
	// Change is "missing" (in the snapshot, not live), "extra" (live, not
	// in the snapshot), "changed", or for a revoked privilege granted again
	// live, "regranted".
	Change string
	// Snapshot and Live describe the two values, when the item has them;
	// Source is the source's value of the property, when the source
	// declares the object.
	Snapshot, Live, Source string
	// SQL holds the statements that restore the declared state, when they
	// can be computed (the object's source declaration equals its snapshot
	// entry; see Detect).
	SQL []string
	// Acknowledged is the acknowledgement covering the item (ApplyAcks);
	// an acknowledged item doesn't fail dpg verify. Lapsed is an
	// acknowledgement of the item that no longer applies.
	Acknowledged, Lapsed *Ack
	// Grant describes a grant item's privilege (Property has "grant:").
	Grant *GrantRef
}

// Report is the result of Detect, sorted by object.
type Report struct {
	Items []Item
	// Pending is how many migrations of the log the database hasn't run:
	// it was compared with the log at its own position (set by callers).
	Pending int
}

// HasDrift reports whether any item is drift, a conflict or converged and
// not acknowledged.
func (r *Report) HasDrift() bool {
	for _, it := range r.Items {
		if it.Failing() {
			return true
		}
	}
	return false
}

// Failing reports whether the item makes dpg verify exit 1.
func (it Item) Failing() bool { return it.Group.Failing() && it.Acknowledged == nil }

// Options configures Detect.
type Options struct {
	// Ignore drops grants to ignored roles from the warnings. Callers also
	// apply it to live and stored beforehand.
	Ignore *ignore.Rules
	// Source is the compiled source. An object whose declaration still
	// equals its snapshot entry is compared from it (see Detect).
	Source []pipeline.IRObject
}

// escapeName makes a name safe inside an item ID.
func escapeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == ':' || r == '%' || r <= ' ':
			fmt.Fprintf(&b, "%%%02X", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unescapeName reverses escapeName.
func unescapeName(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var r rune
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &r); err == nil {
				b.WriteRune(r)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ItemID returns the ID of an item on object (a snapshot key) of kind at
// property ("" for the whole object).
func ItemID(kind, object, property string) string {
	id := kind + ":" + escapeName(object)
	if property != "" {
		id += "/" + property
	}
	return id
}

// reconstructed reports whether a live object was rebuilt from the catalog
// rather than read as source text (its Reconstructed field).
func reconstructed(o pipeline.IRObject) bool {
	v := structOf(reflect.ValueOf(o))
	if !v.IsValid() {
		return false
	}
	f := v.FieldByName("Reconstructed")
	return f.IsValid() && f.Kind() == reflect.Bool && f.Bool()
}

// stripBodyHashes clears the hashes of source text from a snapshot entry.
func stripBodyHashes(so *snapshot.SnapObject) {
	if so.Opaque != nil {
		so.Opaque.BodyHash = ""
	}
	if so.Type != nil {
		so.Type.BodyHash = ""
	}
}

// extraGroup is the group of a live object the snapshot doesn't record.
func extraGroup(kind string) Group {
	switch kind {
	case "role", "tablespace":
		return Warning
	}
	return Unmanaged
}

// grantOnlyKinds hold nothing but grants; compareGrants is their whole
// comparison, and an absent live object is an empty one.
var grantOnlyKinds = map[string]bool{"default_privileges": true, "parameter_privileges": true}

// Detect compares live, the introspected objects, with stored. Callers apply
// [ignore] rules to both beforehand.
//
// For an object whose source declaration (opts.Source) still equals its
// snapshot entry, the source is compared with the live catalog exactly as
// dpg plan --live does; the reported SQL restores the declared state. For
// any other object the live object, masked to the properties the snapshot
// records, is compared with the snapshot entry; the SQL then describes the
// live change.
func Detect(differ pipeline.Differ, live []pipeline.IRObject, stored *pipeline.Snapshot, opts Options) (*Report, error) {
	liveByKey := make(map[string]pipeline.IRObject, len(live))
	for _, o := range live {
		liveByKey[o.QualifiedName()] = o
	}
	sourceByKey := make(map[string]pipeline.IRObject, len(opts.Source))
	for _, o := range opts.Source {
		sourceByKey[o.QualifiedName()] = o
	}
	r := &Report{}

	for key, raw := range stored.Objects {
		var so snapshot.SnapObject
		if err := json.Unmarshal(raw, &so); err != nil {
			return nil, fmt.Errorf("snapshot entry %q: %w", key, err)
		}
		if so.Kind == "virtual_type" {
			continue // DPG-only; never in the database
		}
		lo, ok := liveByKey[key]
		if !ok && !grantOnlyKinds[so.Kind] {
			r.Items = append(r.Items, Item{Group: Drift, Kind: so.Kind, Object: key, Change: "missing"})
			continue
		}
		var ls reflect.Value
		if ok {
			ls = structOf(reflect.ValueOf(lo))
		}
		r.Items = append(r.Items, grantItems(so.Kind, key, ls, snapStruct(&so), opts.Ignore)...)
		if grantOnlyKinds[so.Kind] {
			continue
		}

		var items []Item
		var err error
		fromSource := false
		src, hasSource := sourceByKey[key]
		pending := hasSource && !sourceMatches(differ, src, key, raw)
		if hasSource && !pending {
			items, err = compareFromSource(differ, src, lo, key, raw, so.Kind)
			fromSource = err == nil
		}
		if !fromSource {
			// Also taken when the source comparison fails: a live change
			// the differ can't express as a migration from source (an enum
			// value added live reads as a value removed from source).
			items, err = compareMasked(differ, lo, key, raw, &so)
			if err == nil && pending && len(items) > 0 {
				err = classifyPending(differ, src, lo, key, raw, &so, items)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if hasSource {
			fillSource(items, src)
		}
		r.Items = append(r.Items, items...)
	}

	for key, o := range liveByKey {
		if _, ok := stored.Objects[key]; ok {
			continue
		}
		kind, _, _, ok := snapshot.IdentOf(o)
		if !ok || (kind == "schema" && key == "public") {
			continue // public always exists; declaring it is optional
		}
		r.Items = append(r.Items, Item{Group: extraGroup(kind), Kind: kind, Object: key, Change: "extra"})
	}

	for i := range r.Items {
		it := &r.Items[i]
		it.ID = ItemID(it.Kind, it.Object, it.Property)
	}
	sort.Slice(r.Items, func(i, j int) bool {
		if r.Items[i].Group != r.Items[j].Group {
			return r.Items[i].Group < r.Items[j].Group
		}
		return r.Items[i].ID < r.Items[j].ID
	})
	return r, nil
}

// grantItems reports the grant findings of one object.
func grantItems(kind, key string, live, snap reflect.Value, rules *ignore.Rules) []Item {
	var out []Item
	for _, g := range objectGrantFindings(kind, live, snap) {
		it := Item{Group: Drift, Kind: kind, Object: key, Change: g.change, Grant: &GrantRef{
			Holder: g.path, Privilege: g.tuple.priv, Parameter: g.tuple.param, Role: g.tuple.role,
			WithGrant: g.tuple.withGrant, Held: g.held, AllKind: g.kind,
		}}
		prop := "grant:" + escapeName(g.tuple.privLabel()) + ":" + escapeName(g.tuple.role)
		if g.path != "" {
			prop = g.path + "/" + prop
		}
		it.Property = prop
		switch g.change {
		case "extra":
			if _, ign := rules.Match("role", "", g.tuple.role); ign {
				continue
			}
			it.Group = Warning
			it.Live = g.tuple.String()
		case "missing":
			it.Snapshot = g.tuple.String()
			if g.all {
				it.Live = "lacks " + strings.Join(g.allList, ", ")
			}
		case "regranted":
			it.Snapshot = "REVOKE " + g.tuple.privLabel() + " FROM " + g.tuple.role
			it.Live = g.tuple.String()
		}
		out = append(out, it)
	}
	return out
}

// sourceMatches reports whether the source declaration src still equals the
// snapshot entry raw (no pending change), grants aside.
func sourceMatches(differ pipeline.Differ, src pipeline.IRObject, key string, raw json.RawMessage) bool {
	noGrants, err := stripJSONGrants(raw)
	if err != nil {
		return false
	}
	ops, err := differ.Diff([]pipeline.IRObject{stripIRGrants(src)}, &pipeline.Snapshot{Objects: map[string]json.RawMessage{key: noGrants}})
	return err == nil && len(ops) == 0
}

// compareFromSource compares the source declaration with the live object
// the way dpg plan --live does, and attributes the differences by giving
// the live entry the snapshot's value of one property at a time.
func compareFromSource(differ pipeline.Differ, src, lo pipeline.IRObject, key string, storedRaw json.RawMessage, kind string) ([]Item, error) {
	liveSnap := &pipeline.Snapshot{Live: true}
	if err := snapshot.Populate(liveSnap, []pipeline.IRObject{lo}); err != nil {
		return nil, err
	}
	liveRaw, ok := liveSnap.Objects[key]
	if !ok {
		return nil, nil
	}
	liveRaw, err := stripJSONGrants(liveRaw)
	if err != nil {
		return nil, err
	}
	storedRaw, err = stripJSONGrants(storedRaw)
	if err != nil {
		return nil, err
	}
	desired := stripIRGrants(src)
	ops, err := differ.Diff([]pipeline.IRObject{desired}, &pipeline.Snapshot{Live: true, Objects: map[string]json.RawMessage{key: liveRaw}})
	if err != nil || len(ops) == 0 {
		return nil, err
	}
	sqls := opSQL(ops)
	changes, rest, err := attribute(differ, desired, key, liveRaw, storedRaw, true, sqls)
	if err != nil {
		return nil, err
	}
	return changeItems(kind, key, changes, rest), nil
}

// compareMasked compares the live object, masked to the properties the
// snapshot records, with the snapshot entry.
func compareMasked(differ pipeline.Differ, lo pipeline.IRObject, key string, raw json.RawMessage, so *snapshot.SnapObject) ([]Item, error) {
	if reconstructed(lo) {
		// The live object is rebuilt from the catalog, which can't
		// reproduce source text (and redacts secrets). Without the source
		// body hash the differ compares the structured fields instead,
		// exactly as for dpg plan --live.
		stripBodyHashes(so)
		stripped, err := json.Marshal(so)
		if err != nil {
			return nil, err
		}
		raw = stripped
	}
	masked := mask(lo, so)
	ops, err := differ.Diff([]pipeline.IRObject{masked}, &pipeline.Snapshot{Objects: map[string]json.RawMessage{key: raw}})
	if err != nil || len(ops) == 0 {
		return nil, err
	}
	liveSO := snapshot.ToSnapObject(masked)
	if liveSO == nil {
		return []Item{{Group: Drift, Kind: so.Kind, Object: key, Change: "changed"}}, nil
	}
	liveRaw, err := json.Marshal(liveSO)
	if err != nil {
		return nil, err
	}
	changes, rest, err := attribute(differ, masked, key, raw, liveRaw, false, opSQL(ops))
	if err != nil {
		return nil, err
	}
	items := changeItems(so.Kind, key, changes, rest)
	for i := range items {
		// These ops turn the snapshot into the live state, the reverse of
		// what Item.SQL holds.
		items[i].SQL = nil
	}
	return items, nil
}

func opSQL(ops []pipeline.DiffOp) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, op.SQL())
	}
	return out
}

// changeItems turns attributed changes, and ops no property accounts for,
// into drift items.
func changeItems(kind, key string, changes []propChange, rest []string) []Item {
	var out []Item
	for _, c := range changes {
		out = append(out, Item{Group: Drift, Kind: kind, Object: key, Property: c.property,
			Change: c.change, Snapshot: c.snapshot, Live: c.live, SQL: c.sql})
	}
	if len(rest) > 0 {
		out = append(out, Item{Group: Drift, Kind: kind, Object: key, Change: "changed", SQL: rest})
	}
	return out
}

// stripIRGrants returns a copy of obj without grants or revocations, on the
// object and its columns (grants are compared by compareGrants).
func stripIRGrants(obj pipeline.IRObject) pipeline.IRObject {
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return obj
	}
	out := reflect.New(v.Elem().Type())
	out.Elem().Set(v.Elem())
	clearGrantFields(out.Elem())
	if cols := out.Elem().FieldByName("Columns"); cols.IsValid() && cols.Kind() == reflect.Slice {
		cp := reflect.MakeSlice(cols.Type(), cols.Len(), cols.Len())
		for i := 0; i < cols.Len(); i++ {
			e := cols.Index(i)
			if e.Kind() == reflect.Ptr && !e.IsNil() {
				c := reflect.New(e.Elem().Type())
				c.Elem().Set(e.Elem())
				clearGrantFields(c.Elem())
				cp.Index(i).Set(c)
			} else {
				cp.Index(i).Set(e)
			}
		}
		cols.Set(cp)
	}
	return out.Interface().(pipeline.IRObject)
}

func clearGrantFields(v reflect.Value) {
	for _, n := range []string{"Grants", "Revocations"} {
		if f := v.FieldByName(n); f.IsValid() && f.CanSet() {
			f.Set(reflect.Zero(f.Type()))
		}
	}
}

// stripJSONGrants removes grants and revocations from a snapshot entry, on
// the object and its columns.
func stripJSONGrants(raw json.RawMessage) (json.RawMessage, error) {
	var outer map[string]any
	if err := decodeExact(raw, &outer); err != nil {
		return nil, err
	}
	for k, v := range outer {
		m, ok := v.(map[string]any)
		if !ok || k == "kind" {
			continue
		}
		delete(m, "grants")
		delete(m, "revocations")
		if cols, ok := m["columns"].([]any); ok {
			for _, c := range cols {
				if cm, ok := c.(map[string]any); ok {
					delete(cm, "grants")
					delete(cm, "revocations")
				}
			}
		}
	}
	return json.Marshal(outer)
}

// classifyPending marks the drift items of an object whose source has
// changed since the snapshot: an item on a property the source changed is a
// conflict, or converged when the database already has the source's value.
func classifyPending(differ pipeline.Differ, src, lo pipeline.IRObject, key string, storedRaw json.RawMessage, so *snapshot.SnapObject, items []Item) error {
	storedNG, err := stripJSONGrants(storedRaw)
	if err != nil {
		return err
	}
	desired := stripIRGrants(src)
	srcSO := snapshot.ToSnapObject(desired)
	if srcSO == nil {
		return nil
	}
	srcRaw, err := json.Marshal(srcSO)
	if err != nil {
		return err
	}
	ops, err := differ.Diff([]pipeline.IRObject{desired}, &pipeline.Snapshot{Objects: map[string]json.RawMessage{key: storedNG}})
	if err != nil {
		return err
	}
	changes, rest, err := attribute(differ, desired, key, storedNG, srcRaw, false, opSQL(ops))
	if err != nil {
		return err
	}
	pendingProps := map[string]bool{}
	for _, c := range changes {
		pendingProps[c.property] = true
	}
	wholeObject := len(rest) > 0 // a pending change no single property accounts for

	// Converged: the live object, compared with the snapshot entry given the
	// source's value of the property, no longer differs on it.
	srcMap, _, err := innerMap(srcRaw)
	if err != nil {
		return err
	}
	for i := range items {
		it := &items[i]
		if it.Group != Drift || (!pendingProps[it.Property] && !wholeObject) {
			continue
		}
		it.Group = Conflict
		if it.Property == "" {
			continue
		}
		patched, err := patchProperty(storedNG, it.Property, srcMap)
		if err != nil || patched == nil {
			continue
		}
		var patchedSO snapshot.SnapObject
		if err := json.Unmarshal(patched, &patchedSO); err != nil {
			continue
		}
		// Masked against the patched entry: a sub-object the source added
		// is then compared on its declared properties only.
		masked := stripIRGrants(mask(lo, &patchedSO))
		left, err := differ.Diff([]pipeline.IRObject{masked}, &pipeline.Snapshot{Objects: map[string]json.RawMessage{key: patched}})
		if err != nil {
			continue
		}
		liveRaw, err := json.Marshal(snapshot.ToSnapObject(masked))
		if err != nil {
			continue
		}
		after, _, err := attribute(differ, masked, key, patched, liveRaw, false, opSQL(left))
		if err != nil {
			continue
		}
		still := false
		for _, c := range after {
			if c.property == it.Property {
				still = true
			}
		}
		if !still {
			it.Group = Converged
		}
	}
	return nil
}

// patchProperty returns the snapshot entry raw with property (a top-level
// key, or "elementkind:name" for a list element) set from src.
func patchProperty(raw json.RawMessage, property string, src map[string]any) (json.RawMessage, error) {
	var outer map[string]any
	if err := decodeExact(raw, &outer); err != nil {
		return nil, err
	}
	var inner map[string]any
	var innerKey string
	for k, v := range outer {
		if m, ok := v.(map[string]any); ok && k != "kind" {
			inner, innerKey = m, k
		}
	}
	if inner == nil {
		return nil, nil
	}
	patchPath(inner, src, property)
	outer[innerKey] = inner
	return json.Marshal(outer)
}

// patchPath gives dst src's value of property: a plain key ("comment"), a
// named element ("column:c", replaced whole), a property inside one
// ("column:c/default"), or a grant ("grant:SELECT:r", "column:c/grant:...":
// the holder's grants and revocations).
func patchPath(dst, src map[string]any, property string) {
	head, rest, nested := strings.Cut(property, "/")
	if strings.HasPrefix(head, "grant:") {
		for _, k := range []string{"grants", "revocations"} {
			if v, ok := src[k]; ok {
				dst[k] = v
			} else {
				delete(dst, k)
			}
		}
		return
	}
	if ek, name, isElem := strings.Cut(head, ":"); isElem {
		name = unescapeName(name)
		for listKey, kind := range elementKinds {
			if kind != ek {
				continue
			}
			byName, _, ok := namedElements(src[listKey])
			if !ok {
				return
			}
			e, inSrc := byName[name]
			if nested && inSrc {
				if dstByName, _, ok := namedElements(dst[listKey]); ok {
					if de, ok := dstByName[name].(map[string]any); ok {
						if se, ok := e.(map[string]any); ok {
							patchPath(de, se, rest)
							return
						}
					}
				}
			}
			dst[listKey] = replaceElement(dst[listKey], name, e, inSrc)
		}
		return
	}
	if v, ok := src[property]; ok {
		dst[property] = v
	} else {
		delete(dst, property)
	}
}

// fillSource sets each item's Source to the source's value of its
// property.
func fillSource(items []Item, src pipeline.IRObject) {
	so := snapshot.ToSnapObject(stripIRGrants(src))
	if so == nil {
		return
	}
	raw, err := json.Marshal(so)
	if err != nil {
		return
	}
	m, _, err := innerMap(raw)
	if err != nil || m == nil {
		return
	}
	for i := range items {
		p := items[i].Property
		if p == "" || strings.Contains(p, "grant:") {
			continue
		}
		if ek, name, isElem := strings.Cut(p, ":"); isElem {
			name = unescapeName(name)
			for listKey, kind := range elementKinds {
				if kind != ek {
					continue
				}
				if byName, _, ok := namedElements(m[listKey]); ok {
					items[i].Source = compact(byName[name])
				}
			}
			continue
		}
		items[i].Source = compact(m[p])
	}
}

// RestoreOps returns the operations that give the live object the source's
// value of property ("" for what no single property accounts for): the
// differ's source-to-live operations for the object, as dpg plan --live
// computes them, attributed like Detect's items.
func RestoreOps(differ pipeline.Differ, src, live pipeline.IRObject, key, property string) ([]pipeline.DiffOp, error) {
	liveSnap := &pipeline.Snapshot{Live: true}
	if err := snapshot.Populate(liveSnap, []pipeline.IRObject{live}); err != nil {
		return nil, err
	}
	liveRaw, ok := liveSnap.Objects[key]
	if !ok {
		return nil, fmt.Errorf("%s: not in the database", key)
	}
	liveRaw, err := stripJSONGrants(liveRaw)
	if err != nil {
		return nil, err
	}
	desired := stripIRGrants(src)
	ops, err := differ.Diff([]pipeline.IRObject{desired}, &pipeline.Snapshot{Live: true, Objects: map[string]json.RawMessage{key: liveRaw}})
	if err != nil || len(ops) == 0 {
		return nil, err
	}
	srcSO := snapshot.ToSnapObject(desired)
	if srcSO == nil {
		return nil, fmt.Errorf("%s: no snapshot form", key)
	}
	srcRaw, err := json.Marshal(srcSO)
	if err != nil {
		return nil, err
	}
	changes, rest, err := attribute(differ, desired, key, liveRaw, srcRaw, true, opSQL(ops))
	if err != nil {
		return nil, err
	}
	want := rest
	if property != "" {
		want = nil
		for _, c := range changes {
			if c.property == property {
				want = c.sql
			}
		}
	}
	// The ops whose SQL the property accounts for, as a multiset.
	count := map[string]int{}
	for _, s := range want {
		count[s]++
	}
	var out []pipeline.DiffOp
	for _, op := range ops {
		if count[op.SQL()] > 0 {
			count[op.SQL()]--
			out = append(out, op)
		}
	}
	return out, nil
}

// PatchProperty returns the snapshot entry raw with property (a top-level
// key, or "elementkind:name" for one element of a list) taken from the
// entry fresh.
func PatchProperty(raw, fresh json.RawMessage, property string) (json.RawMessage, error) {
	m, _, err := innerMap(fresh)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return raw, nil
	}
	out, err := patchProperty(raw, property, m)
	if err != nil || out == nil {
		return raw, err
	}
	return out, nil
}
