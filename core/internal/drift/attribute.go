package drift

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// skipKeys are snapshot keys never reported as a property: identity, and
// grants and revocations (compared by compareGrants).
var skipKeys = map[string]bool{
	"name": true, "schema": true, "args": true, "kind": true,
	"grants": true, "revocations": true,
}

// elementKinds names the elements of the snapshot's named sub-object lists.
var elementKinds = map[string]string{
	"columns": "column", "constraints": "constraint", "indexes": "index",
	"policies": "policy", "triggers": "trigger", "partitions": "partition",
	"attributes": "attribute", "configs": "config", "memberships": "membership",
	"security_labels": "security_label",
}

// propChange is one property found to differ.
type propChange struct {
	property       string // "connection_limit", "column:c", "column:c/default"
	change         string // "changed", "missing" (snapshot only), "extra" (live only)
	snapshot, live string // JSON values
	sql            []string
}

// innerMap returns the per-kind object of a marshalled SnapObject and the
// key it sits under ("table", "role", "opaque", ...).
func innerMap(raw []byte) (map[string]any, string, error) {
	var outer map[string]any
	if err := decodeExact(raw, &outer); err != nil {
		return nil, "", err
	}
	for k, v := range outer {
		if k == "kind" {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			return m, k, nil
		}
	}
	return nil, "", nil
}

// decodeExact decodes JSON into generic values keeping numbers exact
// (json.Number), so a large int64 such as a sequence's MAXVALUE survives
// being decoded and encoded again.
func decodeExact(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

func compact(v any) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// namedElements indexes a JSON array of objects by their "name" field. ok is
// false when v is not such an array.
func namedElements(v any) (map[string]any, []string, bool) {
	arr, isArr := v.([]any)
	if !isArr && v != nil {
		return nil, nil, false
	}
	byName := map[string]any{}
	var order []string
	for _, e := range arr {
		m, isObj := e.(map[string]any)
		if !isObj {
			return nil, nil, false
		}
		n, hasName := m["name"].(string)
		if !hasName || n == "" {
			return nil, nil, false
		}
		if _, dup := byName[n]; dup {
			return nil, nil, false
		}
		byName[n] = e
		order = append(order, n)
	}
	return byName, order, true
}

// attribute splits the ops the differ found for one object into property
// changes. desired is the differ's desired side; base the snapshot entry it
// was compared with; target the other state's entry. A property counts
// only when giving base the target's value of that property removes ops,
// so a difference the differ treats as equal is never reported. Ops no
// property accounts for are returned in rest. baseIsLive says which of
// base and target is the live state (for the reported values and change).
func attribute(differ pipeline.Differ, desired pipeline.IRObject, key string, base, target json.RawMessage, baseIsLive bool, fullOps []string) (changes []propChange, rest []string, err error) {
	stored := base
	sm, inner, err := innerMap(base)
	if err != nil || sm == nil {
		return nil, fullOps, err
	}
	lm, _, err := innerMap(target)
	if err != nil || lm == nil {
		return nil, fullOps, err
	}
	// values returns (snapshot, live) display values for base value b and
	// target value t; change orientation follows the same swap.
	values := func(b, t any) (string, string) {
		if baseIsLive {
			return compact(t), compact(b)
		}
		return compact(b), compact(t)
	}

	// opsWith diffs masked against the stored entry with patch applied.
	opsWith := func(patch func(m map[string]any)) ([]string, error) {
		var cp map[string]any
		b, _ := json.Marshal(sm)
		_ = decodeExact(b, &cp)
		patch(cp)
		var outer map[string]any
		_ = decodeExact(stored, &outer)
		outer[inner] = cp
		raw, _ := json.Marshal(outer)
		ops, err := differ.Diff([]pipeline.IRObject{desired}, &pipeline.Snapshot{Live: baseIsLive, Objects: map[string]json.RawMessage{key: raw}})
		if err != nil {
			return nil, err
		}
		var out []string
		for _, op := range ops {
			out = append(out, op.SQL())
		}
		return out, nil
	}
	remaining := append([]string(nil), fullOps...)
	explain := func(prop, change string, sv, lv string, patch func(m map[string]any)) error {
		ops, err := opsWith(patch)
		if err != nil {
			return err
		}
		removed := subtract(fullOps, ops)
		if len(removed) == 0 {
			return nil
		}
		changes = append(changes, propChange{property: prop, change: change, snapshot: sv, live: lv, sql: removed})
		remaining = subtract(remaining, removed)
		return nil
	}

	keys := map[string]bool{}
	for k := range sm {
		keys[k] = true
	}
	for k := range lm {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		if !skipKeys[k] {
			sorted = append(sorted, k)
		}
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		sv, lv := sm[k], lm[k]
		if reflect.DeepEqual(sv, lv) {
			continue
		}
		sEl, sOrder, sOK := namedElements(sv)
		lEl, lOrder, lOK := namedElements(lv)
		if ek, named := elementKinds[k]; named && sOK && lOK {
			for _, n := range unionNames(sOrder, lOrder) {
				se, inS := sEl[n]
				le, inL := lEl[n]
				if reflect.DeepEqual(se, le) {
					continue
				}
				prop := ek + ":" + escapeName(n)
				inSnapshot, inLive := inS, inL
				if baseIsLive {
					inSnapshot, inLive = inL, inS
				}
				change := "changed"
				switch {
				case !inLive:
					change = "missing"
				case !inSnapshot:
					change = "extra"
				}
				sv, lv := values(se, le)
				n, k := n, k
				if err := explain(prop, change, sv, lv, func(m map[string]any) {
					m[k] = replaceElement(m[k], n, le, inL)
				}); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		k := k
		bs, ls := values(sv, lv)
		if err := explain(k, "changed", bs, ls, func(m map[string]any) {
			if lv == nil {
				delete(m, k)
			} else {
				m[k] = lv
			}
		}); err != nil {
			return nil, nil, err
		}
	}
	return changes, remaining, nil
}

func unionNames(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range append(append([]string(nil), a...), b...) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// replaceElement returns arr with element name replaced by e (appended when
// absent), or removed when keep is false.
func replaceElement(arr any, name string, e any, keep bool) any {
	list, _ := arr.([]any)
	var out []any
	found := false
	for _, x := range list {
		if m, ok := x.(map[string]any); ok && m["name"] == name {
			found = true
			if keep {
				out = append(out, e)
			}
			continue
		}
		out = append(out, x)
	}
	if !found && keep {
		out = append(out, e)
	}
	return out
}

// subtract returns a minus b as multisets, keeping a's order.
func subtract(a, b []string) []string {
	count := map[string]int{}
	for _, s := range b {
		count[s]++
	}
	var out []string
	for _, s := range a {
		if count[s] > 0 {
			count[s]--
			continue
		}
		out = append(out, s)
	}
	return out
}
