// Package tui is the interactive dpg resolve: a tree of the drift items
// (target, group, object, item) in which items are chosen for an action,
// a preview of the plan, and applying it.
//
// The model is pure: every side effect goes through Engine, so every key in
// every state is testable without a terminal or a database.
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/thec1oud/dpg/internal/resolve"
)

// Engine does the work the TUI asks for.
type Engine interface {
	// Targets returns the targets and their reports (with acknowledgements
	// applied).
	Targets() []*resolve.Target
	// Requires returns the item IDs adopting item id of t needs too.
	Requires(t *resolve.Target, id string) []string
	// MacroDeclared reports whether adopting item id changes a part a macro
	// declares.
	MacroDeclared(t *resolve.Target, id string) bool
	// Prepare plans decisions; nothing is written.
	Prepare(decisions []resolve.Decision) (*resolve.Plan, error)
	// Render shows a plan as text at most width columns wide.
	Render(plan *resolve.Plan, width int) string
	// Apply carries out a plan (the round-trip check included).
	Apply(plan *resolve.Plan) error
	// Reload detects drift again; Targets then returns the new reports.
	Reload() error
}

// mode is what the screen shows.
type mode int

const (
	listMode    mode = iota // the tree
	helpMode                // the key help over the tree
	filterMode              // typing a filter
	reasonMode              // typing an acknowledgement's reason
	expiresMode             // typing an acknowledgement's expiry date
	previewMode             // the plan
	runningMode             // applying
	confirmQuit             // discard the selection?
)

// choice is the action chosen for an item.
type choice struct {
	action resolve.Action
	macro  resolve.MacroMode
	auto   bool // selected because a chosen adoption requires it
}

// Model is the TUI's state.
type Model struct {
	engine  Engine
	targets []*resolve.Target
	roots   []*node
	rows    []*node
	byKey   map[key]*node

	cursor, offset int
	width, height  int
	mode           mode
	prevMode       mode

	chosen map[key]choice
	// requiredBy maps an automatically chosen item to the chosen
	// adoptions that need it.
	requiredBy map[key]map[key]bool

	filter  string
	input   textinput.Model
	reason  string
	expires *time.Time

	plan      *resolve.Plan
	decisions []resolve.Decision
	preview   viewport.Model

	status   string
	quitting bool
	now      func() time.Time
}

// New returns the model for the engine's targets.
func New(e Engine) Model {
	m := Model{engine: e, width: 80, height: 24, now: time.Now}
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.Cursor.SetMode(cursor.CursorStatic)
	m.preview = viewport.New(80, 20)
	m.load()
	return m
}

// load (re)builds the tree from the engine's targets and clears the choices.
func (m *Model) load() {
	m.targets = m.engine.Targets()
	m.roots = buildTree(m.targets)
	m.byKey = map[key]*node{}
	for _, r := range m.roots {
		for _, l := range r.leaves {
			m.byKey[l.key()] = l
		}
	}
	m.chosen = map[key]choice{}
	m.requiredBy = map[key]map[key]bool{}
	m.reason, m.expires = "", nil
	m.refresh()
	if m.filter != "" && len(m.rows) == 0 {
		// What the filter matched is resolved.
		m.filter = ""
		m.refresh()
	}
	m.cursor, m.offset = 0, 0
}

// refresh recomputes the visible rows and keeps the cursor on them.
func (m *Model) refresh() {
	var current *node
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		current = m.rows[m.cursor]
	}
	m.rows = flatten(m.roots, m.filter)
	m.cursor = 0
	for i, r := range m.rows {
		if r == current {
			m.cursor = i
		}
	}
	m.clamp()
}

// listHeight is the number of tree rows on screen.
func (m *Model) listHeight() int {
	h := m.height - headerLines - footerLines - detailLines(m.height)
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) clamp() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.offset > m.cursor {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if max := len(m.rows) - h; m.offset > max {
		m.offset = max
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// applied is the result of Apply (and the reload after it).
type applied struct {
	n   int
	err error
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.preview.Width, m.preview.Height = m.width, previewHeight(m.height)
		m.clamp()
		return m, nil
	case applied:
		m.mode = listMode
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			if err := m.engine.Reload(); err == nil {
				m.load()
			}
			return m, nil
		}
		m.load()
		m.status = fmt.Sprintf("resolved %d item(s)", msg.n)
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	switch m.mode {
	case helpMode:
		m.mode = m.prevMode
		return m, nil
	case runningMode:
		if s == "ctrl+c" {
			m.status = "applying; wait for it to finish"
		}
		return m, nil
	case confirmQuit:
		switch s {
		case "y", "Y", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		default:
			m.mode = listMode
			m.status = ""
		}
		return m, nil
	case filterMode, reasonMode, expiresMode:
		return m.inputKey(k)
	case previewMode:
		return m.previewKey(k)
	}
	return m.listKey(s)
}

func (m Model) listKey(s string) (tea.Model, tea.Cmd) {
	m.status = ""
	h := m.listHeight()
	switch s {
	case "q", "ctrl+c", "esc":
		if s == "esc" && m.filter != "" {
			m.filter = ""
			m.refresh()
			return m, nil
		}
		if len(m.chosen) > 0 {
			m.mode = confirmQuit
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "?":
		m.prevMode, m.mode = m.mode, helpMode
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup", "ctrl+b":
		m.cursor -= h
	case "pgdown", "ctrl+f", "f":
		m.cursor += h
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(m.rows) - 1
	case "right", "l", "enter":
		if n := m.current(); n != nil && n.kind != itemNode {
			n.expanded = true
			m.refresh()
		}
	case "left", "h":
		if n := m.current(); n != nil {
			if n.kind != itemNode && n.expanded {
				n.expanded = false
			} else if n.parent != nil {
				// Collapse the parent and land on it.
				n.parent.expanded = false
				m.refresh()
				for i, r := range m.rows {
					if r == n.parent {
						m.cursor = i
					}
				}
			}
			m.refresh()
		}
	case "E":
		setExpanded(m.roots, true)
		m.refresh()
	case "C":
		setExpanded(m.roots, false)
		m.refresh()
	case " ":
		m.toggle()
	case "a":
		m.choose(resolve.Adopt)
	case "r":
		m.choose(resolve.Revert)
	case "i":
		m.choose(resolve.Ignore)
	case "n":
		m.choose(resolve.Acknowledge)
	case "u":
		m.choose(resolve.Unacknowledge)
	case "x", "backspace", "delete":
		m.clear()
	case "tab":
		m.cycle(1)
	case "shift+tab":
		m.cycle(-1)
	case "m":
		m.cycleMacro()
	case "/":
		m.mode = filterMode
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		m.input.Focus()
	case "p":
		return m.startPreview()
	}
	m.clamp()
	return m, nil
}

func setExpanded(ns []*node, v bool) {
	for _, n := range ns {
		if n.kind != itemNode {
			n.expanded = v
			setExpanded(n.children, v)
		}
	}
}

func (m *Model) current() *node {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

// targetOf returns the target of an item node.
func (m *Model) targetOf(n *node) *resolve.Target { return m.targets[n.target] }

// toggle chooses each item under the cursor for its first allowed action,
// or, when any of them is chosen, clears them.
func (m *Model) toggle() {
	n := m.current()
	if n == nil {
		return
	}
	for _, l := range n.leaves {
		if _, ok := m.chosen[l.key()]; ok {
			m.clear()
			return
		}
	}
	var refused []string
	for _, l := range n.leaves {
		if acts := resolve.Actions(*l.item); len(acts) > 0 {
			if why := m.set(l, acts[0]); why != "" {
				refused = append(refused, why)
			}
		}
	}
	m.report(refused)
}

// choose sets action on every item under the cursor that allows it.
func (m *Model) choose(a resolve.Action) {
	n := m.current()
	if n == nil {
		return
	}
	any := false
	var refused []string
	for _, l := range n.leaves {
		if resolve.Allowed(*l.item, a) {
			any = true
			if why := m.set(l, a); why != "" {
				refused = append(refused, why)
			}
		}
	}
	if !any {
		m.status = fmt.Sprintf("%s is not possible here", a)
		return
	}
	m.report(refused)
}

func (m *Model) report(refused []string) {
	if len(refused) > 0 {
		m.status = refused[0]
		if len(refused) > 1 {
			m.status += fmt.Sprintf(" (and %d more)", len(refused)-1)
		}
	}
}

// clear removes the choices under the cursor. An item another chosen
// adoption requires stays chosen.
func (m *Model) clear() {
	n := m.current()
	if n == nil {
		return
	}
	// Repeat until nothing changes: an item is released once what needs
	// it (also being unchosen) is gone.
	for {
		var refused []string
		progress := false
		for _, l := range n.leaves {
			if _, ok := m.chosen[l.key()]; !ok {
				continue
			}
			if why := m.unset(l.key()); why != "" {
				refused = append(refused, why)
			} else {
				progress = true
			}
		}
		if !progress || len(refused) == 0 {
			m.report(refused)
			return
		}
	}
}

// cycle moves the item under the cursor to its next (dir 1) or previous
// allowed action, through "not chosen".
func (m *Model) cycle(dir int) {
	n := m.current()
	if n == nil || n.kind != itemNode {
		m.status = "tab cycles an item's action; on a group use a, r, i, n, u or space"
		return
	}
	acts := resolve.Actions(*n.item)
	if len(acts) == 0 {
		return
	}
	// Positions: 0 = not chosen, 1.. = acts.
	pos := 0
	if c, ok := m.chosen[n.key()]; ok {
		for i, a := range acts {
			if a == c.action {
				pos = i + 1
			}
		}
	}
	pos = (pos + dir + len(acts) + 1) % (len(acts) + 1)
	if pos == 0 {
		if why := m.unset(n.key()); why != "" {
			m.status = why
		}
		return
	}
	if why := m.set(n, acts[pos-1]); why != "" {
		m.status = why
	}
}

// cycleMacro switches a macro-declared adoption between editing the macro
// and inlining it.
func (m *Model) cycleMacro() {
	n := m.current()
	if n == nil {
		return
	}
	changed := 0
	for _, l := range n.leaves {
		c, ok := m.chosen[l.key()]
		if !ok || c.action != resolve.Adopt || !m.engine.MacroDeclared(m.targetOf(l), l.item.ID) {
			continue
		}
		switch c.macro {
		case resolve.MacroInline:
			c.macro = resolve.MacroEdit
		default:
			c.macro = resolve.MacroInline
		}
		m.chosen[l.key()] = c
		changed++
	}
	if changed == 0 {
		m.status = "m chooses how to adopt a part a macro declares; nothing chosen here comes from a macro"
	}
}

// set chooses action a for item l, with the items an adoption requires.
// It returns why it couldn't, or "".
func (m *Model) set(l *node, a resolve.Action) string {
	k := l.key()
	if !resolve.Allowed(*l.item, a) {
		return fmt.Sprintf("%s: %s is not possible for a %s item", l.item.ID, a, groupName(l.group))
	}
	var requires []*node
	if a == resolve.Adopt {
		if why := m.adoptable(l, map[key]bool{}); why != "" {
			return why
		}
		for _, id := range m.engine.Requires(m.targetOf(l), l.item.ID) {
			if dn, ok := m.byKey[key{l.target, id}]; ok {
				requires = append(requires, dn)
			}
		}
	}
	old, had := m.chosen[k]
	if had && old.action == resolve.Adopt && a != resolve.Adopt {
		if by := m.blockers(k); len(by) > 0 {
			return fmt.Sprintf("%s is needed by %s", l.item.ID, firstKey(by))
		}
	}
	c := choice{action: a}
	if had && old.action == a {
		c = old
		c.auto = false
	}
	if a == resolve.Adopt && m.engine.MacroDeclared(m.targetOf(l), l.item.ID) && c.macro == "" {
		c.macro = resolve.MacroInline
	}
	m.chosen[k] = c
	if had && old.action == resolve.Adopt && a != resolve.Adopt {
		m.dropRequired(k)
	}
	for _, dn := range requires {
		dk := dn.key()
		if m.requiredBy[dk] == nil {
			m.requiredBy[dk] = map[key]bool{}
		}
		m.requiredBy[dk][k] = true
		if _, ok := m.chosen[dk]; !ok {
			m.set(dn, resolve.Adopt) // adoptable checked it
			dc := m.chosen[dk]
			dc.auto = true
			m.chosen[dk] = dc
		}
	}
	return ""
}

// adoptable reports why item l, with everything it requires, can't be
// chosen for adoption, or "".
func (m *Model) adoptable(l *node, seen map[key]bool) string {
	if seen[l.key()] {
		return ""
	}
	seen[l.key()] = true
	for _, id := range m.engine.Requires(m.targetOf(l), l.item.ID) {
		dn, ok := m.byKey[key{l.target, id}]
		if !ok {
			continue
		}
		if !resolve.Allowed(*dn.item, resolve.Adopt) {
			return fmt.Sprintf("%s needs %s, which can't be adopted (%s)", l.item.ID, id, groupName(dn.group))
		}
		if c, ok := m.chosen[dn.key()]; ok && c.action != resolve.Adopt {
			return fmt.Sprintf("%s needs %s, which is chosen to %s; adopt it instead", l.item.ID, id, c.action)
		}
		if why := m.adoptable(dn, seen); why != "" {
			return why
		}
	}
	return ""
}

// unset clears item k's choice unless a chosen adoption requires it.
func (m *Model) unset(k key) string {
	c, ok := m.chosen[k]
	if !ok {
		return ""
	}
	if c.action == resolve.Adopt {
		if by := m.blockers(k); len(by) > 0 {
			return fmt.Sprintf("%s is needed by %s", k.id, firstKey(by))
		}
	}
	delete(m.chosen, k)
	if c.action == resolve.Adopt {
		m.dropRequired(k)
	}
	return ""
}

// blockers are the chosen adoptions that require item k and would stay
// chosen without it: k can't be unchosen while they are. An automatically
// chosen requirer that only k keeps chosen (directly, or through others
// that only k keeps) goes with k, so it doesn't block; that is what lets a
// cycle of requirements be unchosen.
func (m *Model) blockers(k key) map[key]bool {
	gone := map[key]bool{k: true}
	for changed := true; changed; {
		changed = false
		for a, by := range m.requiredBy {
			if gone[a] {
				continue
			}
			if c, ok := m.chosen[a]; !ok || !c.auto {
				continue
			}
			all := true
			for r := range by {
				if !gone[r] {
					all = false
					break
				}
			}
			if all {
				gone[a], changed = true, true
			}
		}
	}
	out := map[key]bool{}
	for r := range m.requiredBy[k] {
		if _, ok := m.chosen[r]; ok && !gone[r] {
			out[r] = true
		}
	}
	return out
}

// dropRequired forgets that adoption k requires other items, unchoosing
// the automatically chosen ones nothing else requires.
func (m *Model) dropRequired(k key) {
	for dk, by := range m.requiredBy {
		if !by[k] {
			continue
		}
		delete(by, k)
		if len(by) == 0 {
			delete(m.requiredBy, dk)
			if c, ok := m.chosen[dk]; ok && c.auto {
				delete(m.chosen, dk)
				m.dropRequired(dk)
			}
		}
	}
}

func firstKey(ks map[key]bool) string {
	var ids []string
	for k := range ks {
		ids = append(ids, k.id)
	}
	sort.Strings(ids)
	return ids[0]
}

// Decisions returns the chosen decisions, in tree order.
func (m *Model) Decisions() []resolve.Decision {
	var out []resolve.Decision
	for _, r := range m.roots {
		for _, l := range r.leaves {
			c, ok := m.chosen[l.key()]
			if !ok {
				continue
			}
			d := resolve.Decision{Target: m.targetOf(l).Label(), ItemID: l.item.ID, Action: c.action, Macro: c.macro}
			if c.action == resolve.Acknowledge {
				d.Reason, d.Expires = m.reason, m.expires
			}
			out = append(out, d)
		}
	}
	return out
}

func (m *Model) hasAck() bool {
	for _, c := range m.chosen {
		if c.action == resolve.Acknowledge {
			return true
		}
	}
	return false
}

// startPreview asks for an acknowledgement's reason and expiry when one is
// chosen, then plans.
func (m Model) startPreview() (tea.Model, tea.Cmd) {
	if len(m.chosen) == 0 {
		m.status = "nothing chosen: space chooses the item under the cursor"
		return m, nil
	}
	if m.hasAck() {
		m.mode = reasonMode
		m.input.SetValue(m.reason)
		m.input.CursorEnd()
		m.input.Focus()
		return m, nil
	}
	return m.prepare()
}

func (m Model) prepare() (tea.Model, tea.Cmd) {
	m.decisions = m.Decisions()
	plan, err := m.engine.Prepare(m.decisions)
	if err != nil {
		m.mode = listMode
		m.status = "error: " + err.Error()
		return m, nil
	}
	m.plan = plan
	m.mode = previewMode
	m.preview.Width, m.preview.Height = m.width, previewHeight(m.height)
	m.preview.SetContent(m.engine.Render(plan, m.width))
	m.preview.GotoTop()
	return m, nil
}

func (m Model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+c":
		m.input.Blur()
		m.mode = listMode
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		m.input.Blur()
		switch m.mode {
		case filterMode:
			m.filter = v
			m.mode = listMode
			m.refresh()
			if len(m.rows) == 0 {
				m.status = "nothing matches " + v
			}
			return m, nil
		case reasonMode:
			m.reason = v
			m.mode = expiresMode
			if m.expires != nil {
				m.input.SetValue(m.expires.Format("2006-01-02"))
			} else {
				m.input.SetValue("")
			}
			m.input.CursorEnd()
			m.input.Focus()
			return m, nil
		case expiresMode:
			if v == "" {
				m.expires = nil
			} else {
				t, err := time.Parse("2006-01-02", v)
				if err != nil {
					m.status = "the date must be YYYY-MM-DD"
					m.input.Focus()
					return m, nil
				}
				if !t.After(m.now()) {
					m.status = "the date must be in the future"
					m.input.Focus()
					return m, nil
				}
				m.expires = &t
			}
			return m.prepare()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m Model) previewKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "b":
		m.mode = listMode
		return m, nil
	case "ctrl+c":
		m.mode = confirmQuit
		return m, nil
	case "?":
		m.prevMode, m.mode = m.mode, helpMode
		return m, nil
	case "y", "Y":
		if len(m.plan.Problems) > 0 {
			m.status = "the plan has problems; go back (esc) and change the choices"
			return m, nil
		}
		if m.plan.Empty() {
			m.status = "nothing to change"
			return m, nil
		}
		m.mode = runningMode
		plan, n, e := m.plan, len(m.decisions), m.engine
		return m, func() tea.Msg {
			if err := e.Apply(plan); err != nil {
				return applied{err: err}
			}
			if err := e.Reload(); err != nil {
				return applied{err: fmt.Errorf("resolved, but detecting drift again failed: %w", err)}
			}
			return applied{n: n}
		}
	}
	var cmd tea.Cmd
	m.preview, cmd = m.preview.Update(k)
	return m, cmd
}

// Quitting reports whether the user quit.
func (m Model) Quitting() bool { return m.quitting }
