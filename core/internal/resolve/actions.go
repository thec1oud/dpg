// Package resolve resolves drift items (RFC Section 18.4, dpg resolve):
// adopting the database's state into the source and snapshot, reverting
// the database to the declared state, ignoring unmanaged objects, and
// acknowledging items.
package resolve

import (
	"strings"

	"github.com/thec1oud/dpg/internal/drift"
)

// Action is what to do with an item.
type Action string

const (
	// Adopt brings the database's state into the source and the snapshot.
	Adopt Action = "adopt"
	// Revert changes the database back to the declared state.
	Revert Action = "revert"
	// Ignore adds an [ignore] pattern for an object DPG doesn't manage.
	Ignore Action = "ignore"
	// Acknowledge records the item as known; it stops failing dpg verify.
	Acknowledge Action = "acknowledge"
	// Unacknowledge removes an item's acknowledgement.
	Unacknowledge Action = "unacknowledge"
)

// Actions returns what can be done with an item, in display order.
func Actions(it drift.Item) []Action {
	if it.Acknowledged != nil {
		return []Action{Unacknowledge}
	}
	switch it.Group {
	case drift.Drift, drift.Conflict:
		acts := []Action{Adopt}
		if len(it.SQL) > 0 {
			acts = append(acts, Revert)
		}
		return append(acts, Acknowledge)
	case drift.Converged:
		return []Action{Adopt, Acknowledge}
	case drift.Unmanaged:
		// No revert: DPG never drops what it doesn't manage.
		return []Action{Adopt, Ignore, Acknowledge}
	case drift.Warning:
		if isGrant(it) {
			return []Action{Adopt, Acknowledge}
		}
		return []Action{Adopt, Ignore, Acknowledge}
	}
	return nil
}

// Allowed reports whether a is one of it's actions.
func Allowed(it drift.Item, a Action) bool {
	for _, x := range Actions(it) {
		if x == a {
			return true
		}
	}
	return false
}

func isGrant(it drift.Item) bool { return strings.Contains(it.Property, "grant:") }
