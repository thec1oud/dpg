package resolve

import (
	"sort"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/graph"
)

// Requires returns the IDs of the target's other undeclared live objects
// (unmanaged objects, undeclared roles) that adopting the object of item id
// needs declared too: the schema it is in, the types it uses, the tables
// its foreign keys reference, and theirs in turn.
func Requires(t *Target, id string) []string {
	it, ok := findItem(t.Report, id)
	if !ok || !isNewObject(it) {
		return nil
	}
	deps := graph.Dependencies(t.Live)
	undeclared := map[string]string{} // object key → item ID
	for _, x := range t.Report.Items {
		if isNewObject(x) {
			undeclared[x.Object] = x.ID
		}
	}
	seen := map[string]bool{it.Object: true}
	var out []string
	var walk func(key string)
	walk = func(key string) {
		for _, dep := range deps[key] {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			if depID, ok := undeclared[dep]; ok {
				out = append(out, depID)
				walk(dep)
			}
		}
	}
	walk(it.Object)
	sort.Strings(out)
	return out
}

// isNewObject reports whether an item is a live object the source doesn't
// declare.
func isNewObject(it drift.Item) bool {
	return it.Property == "" && it.Change == "extra" && it.Acknowledged == nil
}

// withRequired adds, for each adoption of an undeclared object, adoptions
// of the undeclared objects it requires; Plan.Required records why.
func withRequired(byLabel map[string]*Target, adopts []Decision, plan *Plan) []Decision {
	chosen := map[string]bool{}
	for _, d := range adopts {
		chosen[d.Target+"\x00"+d.ItemID] = true
	}
	out := append([]Decision(nil), adopts...)
	for _, d := range adopts {
		t := byLabel[d.Target]
		for _, id := range Requires(t, d.ItemID) {
			k := d.Target + "\x00" + id
			if chosen[k] {
				continue
			}
			chosen[k] = true
			nd := d
			nd.ItemID = id
			out = append(out, nd)
			plan.Required = append(plan.Required, Required{Target: d.Target, ItemID: id, By: d.ItemID})
		}
	}
	return out
}
