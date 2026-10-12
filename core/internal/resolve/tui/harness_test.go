package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/resolve"
)

// fakeEngine records what the TUI asks for.
type fakeEngine struct {
	targets  []*resolve.Target
	next     []*resolve.Target // what Reload switches to
	requires map[string][]string
	macro    map[string]bool

	mu       sync.Mutex // Apply and Reload run on the program's goroutines
	prepared [][]resolve.Decision
	problems []resolve.Problem
	applied  int
	applyErr error
	reloads  int
}

func (e *fakeEngine) appliedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applied
}

func (e *fakeEngine) Targets() []*resolve.Target { return e.targets }
func (e *fakeEngine) Requires(t *resolve.Target, id string) []string {
	return e.requires[id]
}
func (e *fakeEngine) MacroDeclared(t *resolve.Target, id string) bool { return e.macro[id] }
func (e *fakeEngine) Prepare(ds []resolve.Decision) (*resolve.Plan, error) {
	e.prepared = append(e.prepared, ds)
	p := &resolve.Plan{Decisions: ds, Problems: e.problems}
	if len(ds) > 0 {
		p.Files = []*resolve.FileChange{{Path: "x.dpg", After: []byte("x")}}
	}
	return p, nil
}
func (e *fakeEngine) Render(p *resolve.Plan, width int) string {
	var b strings.Builder
	for _, d := range p.Decisions {
		fmt.Fprintf(&b, "%s %s %s\n", d.Action, d.Target, d.ItemID)
	}
	for _, pr := range p.Problems {
		fmt.Fprintf(&b, "problem %s: %s\n", pr.ItemID, pr.Reason)
	}
	return b.String()
}
func (e *fakeEngine) Apply(p *resolve.Plan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.applied++
	return e.applyErr
}
func (e *fakeEngine) Reload() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reloads++
	if e.next != nil {
		e.targets = e.next
	}
	return nil
}

// item builds a drift item with its ID.
func item(g drift.Group, kind, object, property, change string) drift.Item {
	it := drift.Item{Group: g, Kind: kind, Object: object, Property: property, Change: change}
	it.ID = drift.ItemID(kind, object, property)
	switch change {
	case "changed":
		it.Snapshot, it.Live = "20", "50"
	case "extra":
		if property != "" {
			it.Live = "SELECT TO app"
		}
	}
	return it
}

func target(cluster, db string, items ...drift.Item) *resolve.Target {
	return &resolve.Target{Cluster: cluster, Database: db, Report: &drift.Report{Items: items}}
}

// sample is a report with every group, objects with several items, an
// acknowledged item and an unmanaged object with dependencies.
func sample() *fakeEngine {
	withSQL := item(drift.Drift, "role", "app", "connection_limit", "changed")
	withSQL.SQL = []string{`ALTER ROLE "app" CONNECTION LIMIT 20;`}
	acked := item(drift.Drift, "table", "public.orders", "comment", "changed")
	acked.Acknowledged = &drift.Ack{ID: acked.ID, By: "dana", Reason: "known", At: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	colStatus := item(drift.Drift, "table", "public.orders", "column:status", "changed")
	colStatus.SQL = []string{`ALTER TABLE "public"."orders" ALTER COLUMN "status" SET DEFAULT 'new';`}
	return &fakeEngine{
		targets: []*resolve.Target{
			target("prod", "(cluster)", withSQL, item(drift.Warning, "role", "reporting", "", "extra")),
			target("prod", "app",
				colStatus,
				item(drift.Drift, "table", "public.orders", "index:orders_idx", "extra"),
				acked,
				item(drift.Conflict, "view", "public.v", "query", "changed"),
				item(drift.Converged, "table", "public.items", "column:qty", "changed"),
				item(drift.Unmanaged, "table", "cron.run", "", "extra"),
				item(drift.Unmanaged, "table", "cron.job", "", "extra"),
				item(drift.Unmanaged, "schema", "cron", "", "extra"),
				item(drift.Warning, "table", "public.orders", "grant:SELECT:app", "extra"),
				item(drift.Drift, "table", "public.m", "column:updated_at", "changed"),
			),
		},
		requires: map[string][]string{
			"table:cron.run": {"schema:cron", "table:cron.job"},
			"table:cron.job": {"schema:cron"},
		},
		macro: map[string]bool{"table:public.m/column:updated_at": true},
	}
}

// keyMsg turns "down", "a", " ", "ctrl+c", ... into a key message.
func keyMsg(s string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab,
		"ctrl+c": tea.KeyCtrlC, "ctrl+b": tea.KeyCtrlB, "ctrl+f": tea.KeyCtrlF,
		"backspace": tea.KeyBackspace, "delete": tea.KeyDelete, " ": tea.KeySpace,
	}
	if t, ok := named[s]; ok {
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// driver feeds keys to a model, running the commands it returns the way
// the Bubble Tea runtime would (synchronously).
type driver struct {
	t    *testing.T
	m    Model
	quit bool
}

func newDriver(t *testing.T, e Engine, w, h int) *driver {
	t.Helper()
	m := New(e)
	m.now = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
	d := &driver{t: t, m: m}
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	return d
}

func (d *driver) send(msg tea.Msg) {
	d.t.Helper()
	next, cmd := d.m.Update(msg)
	d.m = next.(Model)
	for cmd != nil {
		out := cmd()
		if _, ok := out.(tea.QuitMsg); ok {
			d.quit = true
			return
		}
		if out == nil {
			return
		}
		if b, ok := out.(tea.BatchMsg); ok {
			_ = b
			return
		}
		next, cmd = d.m.Update(out)
		d.m = next.(Model)
	}
}

func (d *driver) press(keys ...string) *driver {
	d.t.Helper()
	for _, k := range keys {
		d.send(keyMsg(k))
	}
	return d
}

// typeText types s into the focused input.
func (d *driver) typeText(s string) *driver {
	for _, r := range s {
		if r == ' ' {
			d.send(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return d
}

// goTo moves the cursor to the row whose label contains s.
func (d *driver) goTo(s string) *driver {
	d.t.Helper()
	for i, r := range d.m.rows {
		if strings.Contains(r.label, s) || r.item != nil && r.item.ID == s {
			d.m.cursor = i
			d.m.clamp()
			return d
		}
	}
	var labels []string
	for _, r := range d.m.rows {
		labels = append(labels, r.label)
	}
	d.t.Fatalf("no row %q in %q", s, labels)
	return d
}

// chosen lists the choices as "action id" (with + for automatic), sorted.
func (d *driver) chosen() string {
	var out []string
	for _, r := range d.m.roots {
		for _, l := range r.leaves {
			if c, ok := d.m.chosen[l.key()]; ok {
				s := string(c.action) + " " + l.item.ID
				if c.auto {
					s += " +"
				}
				if c.macro != "" {
					s += " " + string(c.macro)
				}
				out = append(out, s)
			}
		}
	}
	return strings.Join(out, ", ")
}

func (d *driver) current() *node { return d.m.current() }

var errBoom = errors.New("boom")
