package tui

import (
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/resolve"
)

// nodeKind is the level of a tree node.
type nodeKind int

const (
	targetNode nodeKind = iota // a cluster's cluster-level objects, or a database
	groupNode                  // drift, conflict, ..., acknowledged
	objectNode                 // an object with several items
	itemNode                   // one item
)

// acknowledged is the display group of acknowledged items.
const acknowledged drift.Group = -1

// groupOrder is the order groups are shown in.
var groupOrder = []drift.Group{drift.Drift, drift.Conflict, drift.Converged, drift.Unmanaged, drift.Warning, acknowledged}

func groupName(g drift.Group) string {
	if g == acknowledged {
		return "acknowledged"
	}
	return g.String()
}

// node is a row of the tree.
type node struct {
	kind     nodeKind
	target   int // index into Model.targets
	group    drift.Group
	label    string // what the row shows (an object's name, an item's property)
	item     *drift.Item
	children []*node
	parent   *node
	expanded bool
	depth    int
	// leaves are the item nodes under this node (itself for an item).
	leaves []*node
}

// key identifies an item across targets.
type key struct {
	target int
	id     string
}

func (n *node) key() key { return key{n.target, n.item.ID} }

// buildTree makes the tree of the targets' reports: target, group, object,
// item. An object with one whole-object item is a single item row.
func buildTree(targets []*resolve.Target) []*node {
	var roots []*node
	for ti, t := range targets {
		if t.Report == nil || len(t.Report.Items) == 0 {
			continue
		}
		tn := &node{kind: targetNode, target: ti, label: t.Label(), expanded: true}
		byGroup := map[drift.Group][]*drift.Item{}
		for i := range t.Report.Items {
			it := &t.Report.Items[i]
			g := it.Group
			if it.Acknowledged != nil {
				g = acknowledged
			}
			byGroup[g] = append(byGroup[g], it)
		}
		for _, g := range groupOrder {
			items := byGroup[g]
			if len(items) == 0 {
				continue
			}
			gn := &node{kind: groupNode, target: ti, group: g, label: groupName(g), expanded: true, parent: tn, depth: 1}
			byObject := map[string][]*drift.Item{}
			var objects []string
			for _, it := range items {
				ok := it.Kind + ":" + it.Object
				if _, seen := byObject[ok]; !seen {
					objects = append(objects, ok)
				}
				byObject[ok] = append(byObject[ok], it)
			}
			sort.Strings(objects)
			for _, ok := range objects {
				its := byObject[ok]
				if len(its) == 1 && its[0].Property == "" {
					in := &node{kind: itemNode, target: ti, group: g, label: objectLabel(its[0]), item: its[0], parent: gn, depth: 2}
					in.leaves = []*node{in}
					gn.children = append(gn.children, in)
					continue
				}
				on := &node{kind: objectNode, target: ti, group: g, label: objectLabel(its[0]), parent: gn, depth: 2, expanded: true}
				sort.Slice(its, func(i, j int) bool { return its[i].ID < its[j].ID })
				for _, it := range its {
					label := it.Property
					if label == "" {
						label = "(object)"
					}
					in := &node{kind: itemNode, target: ti, group: g, label: label, item: it, parent: on, depth: 3}
					in.leaves = []*node{in}
					on.children = append(on.children, in)
				}
				gn.children = append(gn.children, on)
			}
			tn.children = append(tn.children, gn)
		}
		roots = append(roots, tn)
	}
	for _, r := range roots {
		collectLeaves(r)
	}
	return roots
}

func objectLabel(it *drift.Item) string {
	return strings.ReplaceAll(it.Kind, "_", " ") + " " + it.Object
}

func collectLeaves(n *node) []*node {
	if n.kind == itemNode {
		return n.leaves
	}
	n.leaves = nil
	for _, c := range n.children {
		n.leaves = append(n.leaves, collectLeaves(c)...)
	}
	return n.leaves
}

// flatten returns the visible rows: expanded nodes' children, and only
// nodes with a leaf matching filter (when set).
func flatten(roots []*node, filter string) []*node {
	filter = strings.ToLower(filter)
	var out []*node
	var walk func(n *node)
	walk = func(n *node) {
		if filter != "" && !anyMatch(n, filter) {
			return
		}
		out = append(out, n)
		if n.kind == itemNode || !n.expanded {
			return
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

func anyMatch(n *node, filter string) bool {
	for _, l := range n.leaves {
		if strings.Contains(strings.ToLower(l.item.ID), filter) {
			return true
		}
	}
	return false
}
