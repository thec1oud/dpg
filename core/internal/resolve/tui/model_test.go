package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/resolve"
)

func labels(rows []*node) string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.Repeat(".", r.depth)+r.label)
	}
	return strings.Join(out, "\n")
}

func TestTree(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	want := `prod (cluster)
.drift
..role app
...connection_limit
.warning
..role reporting
prod/app
.drift
..table public.m
...column:updated_at
..table public.orders
...column:status
...index:orders_idx
.conflict
..view public.v
...query
.converged
..table public.items
...column:qty
.unmanaged
..schema cron
..table cron.job
..table cron.run
.warning
..table public.orders
...grant:SELECT:app
.acknowledged
..table public.orders
...comment`
	if got := labels(d.m.rows); got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	// Leaves: a header's are every item under it.
	if n := len(d.m.roots[1].leaves); n != 10 {
		t.Errorf("prod/app has %d leaves, want 10", n)
	}
}

func TestEmptyReport(t *testing.T) {
	d := newDriver(t, &fakeEngine{targets: []*resolve.Target{target("prod", "app")}}, 80, 24)
	if len(d.m.rows) != 0 {
		t.Fatalf("rows: %s", labels(d.m.rows))
	}
	// Every key is harmless on an empty tree.
	d.press("down", "up", "end", "home", "pgdown", "pgup", "right", "left", " ", "a", "r", "i", "n", "u", "x", "tab", "shift+tab", "m", "E", "C")
	if d.m.cursor != 0 || len(d.m.chosen) != 0 {
		t.Errorf("cursor %d, chosen %v", d.m.cursor, d.m.chosen)
	}
	d.press("p")
	if d.m.mode != listMode || !strings.Contains(d.m.status, "nothing chosen") {
		t.Errorf("preview with nothing chosen: mode %d, status %q", d.m.mode, d.m.status)
	}
	if !strings.Contains(d.m.View(), "No drift") {
		t.Errorf("view:\n%s", d.m.View())
	}
	d.press("q")
	if !d.quit {
		t.Error("q must quit when nothing is chosen")
	}
}

func TestNavigation(t *testing.T) {
	d := newDriver(t, sample(), 100, 12) // list height 12-2-2-3 = 5
	n := len(d.m.rows)
	d.press("up")
	if d.m.cursor != 0 {
		t.Errorf("up at the top: %d", d.m.cursor)
	}
	d.press("down", "j")
	if d.m.cursor != 2 {
		t.Errorf("down twice: %d", d.m.cursor)
	}
	d.press("k")
	if d.m.cursor != 1 {
		t.Errorf("k: %d", d.m.cursor)
	}
	d.press("end")
	if d.m.cursor != n-1 || d.m.offset != n-d.m.listHeight() {
		t.Errorf("end: cursor %d offset %d (rows %d, height %d)", d.m.cursor, d.m.offset, n, d.m.listHeight())
	}
	d.press("down")
	if d.m.cursor != n-1 {
		t.Errorf("down at the bottom: %d", d.m.cursor)
	}
	d.press("home")
	if d.m.cursor != 0 || d.m.offset != 0 {
		t.Errorf("home: %d %d", d.m.cursor, d.m.offset)
	}
	d.press("pgdown")
	if d.m.cursor != d.m.listHeight() {
		t.Errorf("pgdown: %d", d.m.cursor)
	}
	d.press("pgup")
	if d.m.cursor != 0 {
		t.Errorf("pgup: %d", d.m.cursor)
	}
	d.press("G")
	if d.m.cursor != n-1 {
		t.Errorf("G: %d", d.m.cursor)
	}
	d.press("g")
	if d.m.cursor != 0 {
		t.Errorf("g: %d", d.m.cursor)
	}

	// Collapse and expand a group; the cursor stays on it.
	d.goTo("unmanaged").press("left")
	if d.current().label != "unmanaged" || d.current().expanded {
		t.Fatalf("left on a group: %q expanded=%v", d.current().label, d.current().expanded)
	}
	if strings.Contains(labels(d.m.rows), "cron.job") {
		t.Error("collapsed group still shows its items")
	}
	d.press("right")
	if !d.current().expanded || !strings.Contains(labels(d.m.rows), "cron.job") {
		t.Error("right must expand")
	}
	// Left on an item collapses its parent and lands on it.
	d.goTo("table cron.job").press("h")
	if d.current().label != "unmanaged" || d.current().expanded {
		t.Errorf("h on an item: on %q", d.current().label)
	}
	d.press("enter")
	if !d.current().expanded {
		t.Error("enter must expand")
	}
	// Collapse all, expand all.
	d.press("C")
	if len(d.m.rows) != 2 {
		t.Errorf("C: rows\n%s", labels(d.m.rows))
	}
	d.press("E")
	if len(d.m.rows) != n {
		t.Errorf("E: %d rows, want %d", len(d.m.rows), n)
	}
}

func TestChoose(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)

	// Space: the first allowed action; again: unchosen.
	d.goTo("connection_limit").press(" ")
	if got := d.chosen(); got != "adopt role:app/connection_limit" {
		t.Errorf("space: %q", got)
	}
	d.press(" ")
	if got := d.chosen(); got != "" {
		t.Errorf("space again: %q", got)
	}
	// Letters.
	d.press("r")
	if got := d.chosen(); got != "revert role:app/connection_limit" {
		t.Errorf("r: %q", got)
	}
	d.press("n")
	if got := d.chosen(); got != "acknowledge role:app/connection_limit" {
		t.Errorf("n: %q", got)
	}
	d.press("x")
	if got := d.chosen(); got != "" {
		t.Errorf("x: %q", got)
	}
	// Not allowed: ignore on drift, revert without SQL, unack on a live item.
	for _, k := range []string{"i", "u"} {
		d.press(k)
		if d.chosen() != "" || !strings.Contains(d.m.status, "not possible") {
			t.Errorf("%s on drift: chosen %q, status %q", k, d.chosen(), d.m.status)
		}
	}
	d.goTo("index:orders_idx").press("r")
	if d.chosen() != "" {
		t.Errorf("revert without SQL: %q", d.chosen())
	}
	// Unmanaged: no revert, ever.
	d.goTo("table cron.job").press("r")
	if d.chosen() != "" {
		t.Errorf("revert on unmanaged: %q", d.chosen())
	}
	d.press("i")
	if got := d.chosen(); got != "ignore table:cron.job" {
		t.Errorf("i on unmanaged: %q", got)
	}
	d.press("x")
	// Acknowledged: only unack.
	d.goTo("acknowledged").press("a")
	if d.chosen() != "" {
		t.Errorf("adopt on acknowledged: %q", d.chosen())
	}
	d.press("u")
	if got := d.chosen(); got != "unacknowledge table:public.orders/comment" {
		t.Errorf("u on acknowledged: %q", got)
	}
	d.press("x")

	// On a group: every item that allows the action.
	d.goTo("prod/app").press("r")
	if got := d.chosen(); got != "revert table:public.orders/column:status" {
		t.Errorf("r on a target: %q", got)
	}
	d.press("x")
	if d.chosen() != "" {
		t.Errorf("x on a target: %q", d.chosen())
	}
	d.goTo("conflict").press("n")
	if got := d.chosen(); got != "acknowledge view:public.v/query" {
		t.Errorf("n on a group: %q", got)
	}
	// Space on a header with something chosen under it unchooses it all.
	d.press(" ")
	if d.chosen() != "" {
		t.Errorf("space on a header with choices: %q", d.chosen())
	}
	d.goTo("conflict").press(" ")
	if got := d.chosen(); got != "adopt view:public.v/query" {
		t.Errorf("space on a header: %q", got)
	}
}

func TestCycle(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.goTo("connection_limit")
	var seen []string
	for i := 0; i < 4; i++ {
		d.press("tab")
		seen = append(seen, d.chosen())
	}
	want := []string{"adopt role:app/connection_limit", "revert role:app/connection_limit", "acknowledge role:app/connection_limit", ""}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Errorf("tab: %q", seen)
	}
	d.press("shift+tab")
	if got := d.chosen(); got != "acknowledge role:app/connection_limit" {
		t.Errorf("shift+tab from nothing: %q", got)
	}
	// Unmanaged cycles adopt, ignore, acknowledge: never revert.
	d.press("x").goTo("table cron.job")
	seen = nil
	for i := 0; i < 4; i++ {
		d.press("tab")
		seen = append(seen, d.chosen())
	}
	if strings.Join(seen, "|") != "adopt schema:cron +, adopt table:cron.job|ignore table:cron.job|acknowledge table:cron.job|" {
		t.Errorf("tab on unmanaged: %q", seen)
	}
	// Tab on a header explains itself.
	d.goTo("unmanaged").press("tab")
	if !strings.Contains(d.m.status, "tab cycles") {
		t.Errorf("tab on a header: %q", d.m.status)
	}
}

func TestRequiredAdoptions(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.goTo("table cron.run").press("a")
	if got := d.chosen(); got != "adopt schema:cron +, adopt table:cron.job +, adopt table:cron.run" {
		t.Fatalf("adopting cron.run: %q", got)
	}
	// What it needs can't be unchosen, or switched, while it is chosen.
	for _, k := range []string{"x", " ", "i", "n", "tab"} {
		d.goTo("schema cron").press(k)
		if got := d.chosen(); got != "adopt schema:cron +, adopt table:cron.job +, adopt table:cron.run" {
			t.Errorf("%q on a required item changed the choices: %q", k, got)
		}
		if !strings.Contains(d.m.status, "needed by") {
			t.Errorf("%q on a required item: status %q", k, d.m.status)
		}
	}
	// Choosing a required item yourself makes it yours: it stays when
	// what required it goes.
	d.goTo("table cron.job").press("a")
	d.goTo("table cron.run").press("x")
	if got := d.chosen(); got != "adopt schema:cron +, adopt table:cron.job" {
		t.Errorf("after unchoosing cron.run: %q", got)
	}
	d.goTo("table cron.job").press("x")
	if got := d.chosen(); got != "" {
		t.Errorf("after unchoosing cron.job: %q", got)
	}
	// Switching the requirer to another action releases what it needed.
	d.goTo("table cron.run").press("a", "i")
	if got := d.chosen(); got != "ignore table:cron.run" {
		t.Errorf("adopt then ignore: %q", got)
	}
	// On the group: everything, the dependencies counted once.
	d.press("x").goTo("unmanaged").press("a")
	if got := d.chosen(); got != "adopt schema:cron, adopt table:cron.job, adopt table:cron.run" {
		t.Errorf("a on the group: %q", got)
	}
	d.press("x")
	if got := d.chosen(); got != "" {
		t.Errorf("x on the group: %q", got)
	}
}

func TestRequiredCycle(t *testing.T) {
	e := sample()
	e.requires = map[string][]string{"table:cron.run": {"table:cron.job"}, "table:cron.job": {"table:cron.run"}}
	d := newDriver(t, e, 100, 40)
	d.goTo("table cron.run").press("a")
	if got := d.chosen(); got != "adopt table:cron.job +, adopt table:cron.run" {
		t.Fatalf("cycle: %q", got)
	}
	d.press("x")
	if got := d.chosen(); got != "" {
		t.Errorf("unchoosing one side of a cycle must release both: %q (status %q)", got, d.m.status)
	}
}

func TestMacroChoice(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.goTo("column:updated_at").press("a")
	if got := d.chosen(); got != "adopt table:public.m/column:updated_at inline" {
		t.Errorf("adopting a macro part: %q", got)
	}
	d.press("m")
	if got := d.chosen(); got != "adopt table:public.m/column:updated_at edit" {
		t.Errorf("m: %q", got)
	}
	d.press("m")
	if got := d.chosen(); got != "adopt table:public.m/column:updated_at inline" {
		t.Errorf("m again: %q", got)
	}
	d.goTo("connection_limit").press("a", "m")
	if !strings.Contains(d.m.status, "macro") {
		t.Errorf("m on a non-macro item: %q", d.m.status)
	}
	ds := d.m.Decisions()
	if len(ds) != 2 || ds[1].Macro != resolve.MacroInline {
		t.Errorf("decisions: %+v", ds)
	}
}

func TestFilter(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.press("/").typeText("cron").press("enter")
	if d.m.filter != "cron" || d.m.mode != listMode {
		t.Fatalf("filter %q mode %d", d.m.filter, d.m.mode)
	}
	want := "prod/app\n.unmanaged\n..schema cron\n..table cron.job\n..table cron.run"
	if got := labels(d.m.rows); got != want {
		t.Errorf("filtered:\n%s", got)
	}
	// Choosing on a filtered header still covers only what it shows? No:
	// a header stands for all its items; the filter only hides rows.
	d.press("esc")
	if d.m.filter != "" || len(d.m.rows) < 20 {
		t.Errorf("esc must clear the filter: %q, %d rows", d.m.filter, len(d.m.rows))
	}
	d.press("/").typeText("nothing-like-this").press("enter")
	if len(d.m.rows) != 0 || !strings.Contains(d.m.status, "nothing matches") {
		t.Errorf("no match: %d rows, status %q", len(d.m.rows), d.m.status)
	}
	d.press("/", "esc")
	if d.m.mode != listMode || d.m.filter != "nothing-like-this" {
		t.Errorf("esc while typing keeps the filter: %q", d.m.filter)
	}
	d.press("esc")
	if d.m.filter != "" {
		t.Errorf("esc clears: %q", d.m.filter)
	}
}

func TestAcknowledgeInput(t *testing.T) {
	e := sample()
	d := newDriver(t, e, 100, 40)
	d.goTo("connection_limit").press("n", "p")
	if d.m.mode != reasonMode {
		t.Fatalf("p with an acknowledgement asks why: mode %d", d.m.mode)
	}
	d.typeText("load test").press("enter")
	if d.m.mode != expiresMode {
		t.Fatalf("then when it lapses: mode %d", d.m.mode)
	}
	for _, bad := range []string{"tomorrow", "2026-13-01", "2026-10-12", "2020-01-01"} {
		d.typeText(bad).press("enter")
		if d.m.mode != expiresMode || d.m.status == "" {
			t.Errorf("%q accepted (mode %d)", bad, d.m.mode)
		}
		for range bad {
			d.press("backspace")
		}
	}
	d.typeText("2026-11-01").press("enter")
	if d.m.mode != previewMode {
		t.Fatalf("valid date: mode %d, status %q", d.m.mode, d.m.status)
	}
	got := e.prepared[len(e.prepared)-1]
	if len(got) != 1 || got[0].Reason != "load test" || got[0].Expires == nil || got[0].Expires.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("decision: %+v", got)
	}
	// Back, and again: the reason is remembered; an empty date is no expiry.
	d.press("esc", "p")
	if d.m.input.Value() != "load test" {
		t.Errorf("reason not remembered: %q", d.m.input.Value())
	}
	d.press("enter")
	for range "2026-11-01" {
		d.press("backspace")
	}
	d.press("enter")
	if got := e.prepared[len(e.prepared)-1]; got[0].Expires != nil {
		t.Errorf("empty date: %+v", got[0])
	}
	// Esc while typing goes back to the list.
	d.press("esc", "p", "esc")
	if d.m.mode != listMode {
		t.Errorf("esc in the reason: mode %d", d.m.mode)
	}
}

func TestPreviewAndApply(t *testing.T) {
	e := sample()
	e.next = []*resolve.Target{target("prod", "app", item(drift.Unmanaged, "table", "public.left", "", "extra"))}
	d := newDriver(t, e, 100, 40)
	d.goTo("connection_limit").press("r")
	d.goTo("table cron.run").press("a")
	d.press("p")
	if d.m.mode != previewMode {
		t.Fatalf("mode %d status %q", d.m.mode, d.m.status)
	}
	view := d.m.View()
	for _, want := range []string{"revert prod (cluster) role:app/connection_limit", "adopt prod/app table:cron.run", "adopt prod/app schema:cron"} {
		if !strings.Contains(view, want) {
			t.Errorf("preview lacks %q:\n%s", want, view)
		}
	}
	// Scrolling keys go to the preview.
	d.press("down", "pgdown", "up")
	if d.m.mode != previewMode {
		t.Errorf("scrolling left the preview")
	}
	d.press("y")
	if e.applied != 1 || e.reloads != 1 {
		t.Fatalf("applied %d, reloads %d", e.applied, e.reloads)
	}
	if d.m.mode != listMode || !strings.Contains(d.m.status, "resolved 4 item(s)") {
		t.Errorf("after apply: mode %d status %q", d.m.mode, d.m.status)
	}
	if len(d.m.chosen) != 0 || labels(d.m.rows) != "prod/app\n.unmanaged\n..table public.left" {
		t.Errorf("not reloaded: chosen %v rows\n%s", d.m.chosen, labels(d.m.rows))
	}
}

func TestReloadKeepsOrDropsFilter(t *testing.T) {
	e := sample()
	e.next = []*resolve.Target{target("prod", "app", item(drift.Unmanaged, "table", "cron.left", "", "extra"),
		item(drift.Unmanaged, "table", "public.other", "", "extra"))}
	d := newDriver(t, e, 100, 40)
	d.press("/").typeText("cron").press("enter")
	d.goTo("table cron.job").press("i", "p", "y")
	if d.m.filter != "cron" || labels(d.m.rows) != "prod/app\n.unmanaged\n..table cron.left" {
		t.Errorf("a filter that still matches stays: %q\n%s", d.m.filter, labels(d.m.rows))
	}
	e.next = []*resolve.Target{target("prod", "app", item(drift.Unmanaged, "table", "public.other", "", "extra"))}
	d.goTo("table cron.left").press("i", "p", "y")
	if d.m.filter != "" || len(d.m.rows) != 3 {
		t.Errorf("a filter that matches nothing any more is dropped: %q\n%s", d.m.filter, labels(d.m.rows))
	}
}

func TestPreviewProblemsBlockApply(t *testing.T) {
	e := sample()
	e.problems = []resolve.Problem{{Target: "prod/app", ItemID: "x", Reason: "can't"}}
	d := newDriver(t, e, 100, 40)
	d.goTo("connection_limit").press("a", "p", "y")
	if e.applied != 0 || d.m.mode != previewMode || !strings.Contains(d.m.status, "problems") {
		t.Errorf("applied %d mode %d status %q", e.applied, d.m.mode, d.m.status)
	}
	if !strings.Contains(d.m.View(), "problem x: can't") {
		t.Errorf("problems not shown:\n%s", d.m.View())
	}
	d.press("b")
	if d.m.mode != listMode || d.chosen() == "" {
		t.Errorf("back keeps the choices: mode %d chosen %q", d.m.mode, d.chosen())
	}
}

func TestApplyError(t *testing.T) {
	e := sample()
	e.applyErr = errBoom
	d := newDriver(t, e, 100, 40)
	d.goTo("connection_limit").press("a", "p", "y")
	if !strings.Contains(d.m.status, "boom") || d.m.mode != listMode {
		t.Errorf("status %q mode %d", d.m.status, d.m.mode)
	}
	if e.reloads != 1 {
		t.Errorf("a failed apply must detect drift again (the database may have changed): %d", e.reloads)
	}
}

func TestRunningIgnoresKeys(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.goTo("connection_limit").press("a", "p")
	next, _ := d.m.Update(keyMsg("y")) // don't run the command
	d.m = next.(Model)
	if d.m.mode != runningMode {
		t.Fatalf("mode %d", d.m.mode)
	}
	d.press("q", "esc", "down", "ctrl+c")
	if d.quit || d.m.mode != runningMode || !strings.Contains(d.m.status, "wait") {
		t.Errorf("while applying: quit %v mode %d status %q", d.quit, d.m.mode, d.m.status)
	}
	if !strings.Contains(d.m.View(), "dpg resolve") {
		t.Error("running view")
	}
	d.send(applied{n: 1})
	if d.m.mode != listMode {
		t.Errorf("after the result: %d", d.m.mode)
	}
}

func TestQuit(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.goTo("connection_limit").press("a", "q")
	if d.quit || d.m.mode != confirmQuit {
		t.Fatalf("q with choices must ask: quit %v mode %d", d.quit, d.m.mode)
	}
	if !strings.Contains(d.m.View(), "Discard 1 choice(s)") {
		t.Errorf("view:\n%s", d.m.View())
	}
	d.press("n")
	if d.quit || d.m.mode != listMode || d.chosen() == "" {
		t.Errorf("n: quit %v mode %d", d.quit, d.m.mode)
	}
	d.press("ctrl+c", "y")
	if !d.quit || !d.m.Quitting() || d.m.View() != "" {
		t.Errorf("ctrl+c then y must quit")
	}
	d = newDriver(t, sample(), 100, 40)
	d.goTo("connection_limit").press("a", "ctrl+c", "ctrl+c")
	if !d.quit {
		t.Error("ctrl+c twice quits")
	}
	// From the preview, ctrl+c asks too.
	d = newDriver(t, sample(), 100, 40)
	d.goTo("connection_limit").press("a", "p", "ctrl+c")
	if d.m.mode != confirmQuit {
		t.Errorf("ctrl+c in the preview: %d", d.m.mode)
	}
}

func TestHelp(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.press("?")
	if d.m.mode != helpMode || !strings.Contains(d.m.View(), "adopt: keep the database") {
		t.Fatalf("help:\n%s", d.m.View())
	}
	d.press("a")
	if d.m.mode != listMode || d.chosen() != "" {
		t.Errorf("any key closes help without acting: mode %d chosen %q", d.m.mode, d.chosen())
	}
	d.goTo("connection_limit").press("a", "p", "?")
	if d.m.mode != helpMode || !strings.Contains(d.m.View(), "apply the plan") {
		t.Errorf("help in the preview:\n%s", d.m.View())
	}
	d.press("x")
	if d.m.mode != previewMode {
		t.Errorf("help closes back to the preview: %d", d.m.mode)
	}
}

func TestResize(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.press("end")
	d.send(tea.WindowSizeMsg{Width: 30, Height: 8})
	if d.m.cursor != len(d.m.rows)-1 || d.m.cursor < d.m.offset || d.m.cursor >= d.m.offset+d.m.listHeight() {
		t.Errorf("cursor %d not visible at offset %d height %d", d.m.cursor, d.m.offset, d.m.listHeight())
	}
	d.send(tea.WindowSizeMsg{Width: 200, Height: 100})
	if d.m.offset != 0 {
		t.Errorf("everything fits: offset %d", d.m.offset)
	}
	d.send(tea.WindowSizeMsg{Width: 1, Height: 1})
	_ = d.m.View()
	d.press("down", "up", "end")
}

func TestDecisionsOrderAndTargets(t *testing.T) {
	d := newDriver(t, sample(), 100, 40)
	d.goTo("table cron.job").press("i")
	d.goTo("connection_limit").press("a")
	d.goTo("acknowledged").press("u")
	ds := d.m.Decisions()
	var got []string
	for _, x := range ds {
		got = append(got, x.Target+" "+string(x.Action)+" "+x.ItemID)
	}
	want := "prod (cluster) adopt role:app/connection_limit|prod/app ignore table:cron.job|prod/app unacknowledge table:public.orders/comment"
	if strings.Join(got, "|") != want {
		t.Errorf("decisions:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	_ = time.Now
}
