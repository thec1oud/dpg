package tui

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/resolve"
)

var allKeys = []string{
	"up", "down", "left", "right", "pgup", "pgdown", "home", "end", "enter", "esc", "tab", "shift+tab",
	"ctrl+c", "backspace", " ", "a", "r", "i", "n", "u", "x", "m", "p", "y", "q", "?", "/", "E", "C",
	"g", "G", "j", "k", "h", "l", "b", "c", "2", "-",
}

// randomEngine builds a report of n items over random groups, kinds and
// objects, with random dependencies (cycles included) and macro parts.
func randomEngine(r *rand.Rand, n int) *fakeEngine {
	groups := []drift.Group{drift.Drift, drift.Conflict, drift.Converged, drift.Unmanaged, drift.Warning}
	kinds := []string{"table", "role", "view", "schema", "type"}
	e := &fakeEngine{requires: map[string][]string{}, macro: map[string]bool{}}
	var targets []*resolve.Target
	for ti := 0; ti < 1+r.Intn(3); ti++ {
		t := target("c", fmt.Sprintf("db%d", ti))
		var unmanaged []string
		for i := 0; i < n; i++ {
			g := groups[r.Intn(len(groups))]
			kind := kinds[r.Intn(len(kinds))]
			obj := fmt.Sprintf("s.o%d", r.Intn(n/2+1))
			prop, change := "", "extra"
			if g != drift.Unmanaged && r.Intn(3) > 0 {
				prop, change = fmt.Sprintf("column:c%d", i), []string{"changed", "missing", "extra"}[r.Intn(3)]
			}
			if g == drift.Warning && prop != "" {
				prop = fmt.Sprintf("grant:SELECT:r%d", i)
			}
			it := item(g, kind, obj, prop, change)
			if r.Intn(3) == 0 {
				it.SQL = []string{"SELECT 1;"}
			}
			if r.Intn(6) == 0 {
				it.Acknowledged = &drift.Ack{ID: it.ID, At: time.Unix(0, 0)}
			}
			if r.Intn(5) == 0 {
				e.macro[it.ID] = true
			}
			dup := false
			for _, x := range t.Report.Items {
				if x.ID == it.ID {
					dup = true
				}
			}
			if dup {
				continue
			}
			if prop == "" && change == "extra" && it.Acknowledged == nil {
				unmanaged = append(unmanaged, it.ID)
			}
			t.Report.Items = append(t.Report.Items, it)
		}
		for _, id := range unmanaged {
			for j := 0; j < r.Intn(3); j++ {
				dep := unmanaged[r.Intn(len(unmanaged))]
				if dep != id {
					e.requires[id] = append(e.requires[id], dep)
				}
			}
		}
		targets = append(targets, t)
	}
	e.targets = targets
	return e
}

// checkInvariants asserts what must hold after every key.
func checkInvariants(t *testing.T, m Model, trail []string) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("after %v: "+format, append([]any{trail}, args...)...)
	}
	if len(m.rows) > 0 && (m.cursor < 0 || m.cursor >= len(m.rows)) {
		fail("cursor %d outside %d rows", m.cursor, len(m.rows))
	}
	if m.offset < 0 || m.offset > m.cursor && len(m.rows) > 0 {
		fail("offset %d, cursor %d", m.offset, m.cursor)
	}
	if len(m.rows) > 0 && m.cursor >= m.offset+m.listHeight() {
		fail("cursor %d below the screen (offset %d, height %d)", m.cursor, m.offset, m.listHeight())
	}
	for k, c := range m.chosen {
		n, ok := m.byKey[k]
		if !ok {
			fail("choice for unknown item %v", k)
		}
		if !resolve.Allowed(*n.item, c.action) {
			fail("%s chosen for %s (a %s item)", c.action, k.id, groupName(n.group))
		}
		if c.macro != "" && (c.action != resolve.Adopt || !m.engine.MacroDeclared(nil, k.id)) {
			fail("macro mode on %s (%s)", k.id, c.action)
		}
		if c.auto && len(m.requiredBy[k]) == 0 {
			fail("%s chosen automatically but nothing requires it", k.id)
		}
		// Every chosen adoption's requirements are chosen for adoption.
		if c.action == resolve.Adopt {
			for _, dep := range m.engine.Requires(nil, k.id) {
				dk := key{k.target, dep}
				if _, exists := m.byKey[dk]; !exists {
					continue
				}
				if dc, ok := m.chosen[dk]; !ok || dc.action != resolve.Adopt {
					fail("%s is adopted but %s, which it requires, is not", k.id, dep)
				}
			}
		}
	}
	for k, by := range m.requiredBy {
		for r := range by {
			if c, ok := m.chosen[r]; !ok || c.action != resolve.Adopt {
				fail("%s recorded as required by %s, which isn't adopted", k.id, r.id)
			}
		}
	}
	checkFits(t, m.View(), max(m.width, 1), max(m.height, 1))
}

// TestRandomKeys drives random reports with random keys and sizes,
// checking the invariants after every key.
func TestRandomKeys(t *testing.T) {
	seeds := 300
	if testing.Short() {
		seeds = 30
	}
	for seed := 0; seed < seeds; seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		e := randomEngine(r, 1+r.Intn(25))
		if r.Intn(4) == 0 {
			e.problems = []resolve.Problem{{ItemID: "x", Reason: "no"}}
		}
		if r.Intn(4) == 0 {
			e.applyErr = errBoom
		}
		d := newDriver(t, e, 1+r.Intn(120), 1+r.Intn(50))
		var trail []string
		for step := 0; step < 200 && !d.quit; step++ {
			if r.Intn(30) == 0 {
				size := tea.WindowSizeMsg{Width: 1 + r.Intn(120), Height: 1 + r.Intn(50)}
				trail = append(trail, fmt.Sprintf("size %dx%d", size.Width, size.Height))
				d.send(size)
			} else {
				k := allKeys[r.Intn(len(allKeys))]
				trail = append(trail, k)
				d.press(k)
			}
			if len(trail) > 12 {
				trail = trail[len(trail)-12:]
			}
			checkInvariants(t, d.m, append([]string{fmt.Sprintf("seed %d", seed)}, trail...))
		}
	}
}

// FuzzKeys is TestRandomKeys driven by the fuzzer: the input bytes pick
// keys.
func FuzzKeys(f *testing.F) {
	f.Add([]byte("aap\x00y"), int64(1))
	f.Add([]byte{3, 14, 15, 26, 18, 7, 22}, int64(7))
	f.Fuzz(func(t *testing.T, keys []byte, seed int64) {
		r := rand.New(rand.NewSource(seed))
		d := newDriver(t, randomEngine(r, 1+r.Intn(20)), 1+r.Intn(100), 1+r.Intn(40))
		var trail []string
		for _, b := range keys {
			if d.quit {
				return
			}
			k := allKeys[int(b)%len(allKeys)]
			trail = append(trail, k)
			d.press(k)
			checkInvariants(t, d.m, trail)
		}
	})
}

// TestLargeReport: 10k items render and navigate fast (no O(n²) per key).
func TestLargeReport(t *testing.T) {
	e := &fakeEngine{requires: map[string][]string{}, macro: map[string]bool{}}
	var items []drift.Item
	for i := 0; i < 10000; i++ {
		g := []drift.Group{drift.Drift, drift.Unmanaged, drift.Warning}[i%3]
		prop, change := fmt.Sprintf("column:c%d", i), "changed"
		if g == drift.Unmanaged {
			prop, change = "", "extra"
		}
		items = append(items, item(g, "table", fmt.Sprintf("public.t%d", i/4), prop, change))
	}
	e.targets = []*resolve.Target{target("prod", "app", items...)}
	start := time.Now()
	d := newDriver(t, e, 120, 50)
	build := time.Since(start)

	const n = 50
	start = time.Now()
	for i := 0; i < n; i++ {
		_ = d.m.View()
	}
	view := time.Since(start) / n

	start = time.Now()
	for i := 0; i < n; i++ {
		d.press("pgdown")
	}
	nav := time.Since(start) / n

	start = time.Now()
	d.press("home", "a") // adopt on the whole target: 10k items
	choose := time.Since(start)
	start = time.Now()
	d.press("x")
	clear := time.Since(start)

	t.Logf("build %v, view %v, page %v, choose all %v, clear all %v", build, view, nav, choose, clear)
	if view > 50*time.Millisecond || nav > 50*time.Millisecond {
		t.Errorf("too slow: view %v, page %v", view, nav)
	}
	if choose > 2*time.Second || clear > 2*time.Second || build > 2*time.Second {
		t.Errorf("too slow: build %v, choose %v, clear %v", build, choose, clear)
	}
	if !strings.Contains(d.m.View(), "10000 item(s)") {
		t.Error("count")
	}
}
