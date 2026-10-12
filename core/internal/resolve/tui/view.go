package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/resolve"
)

const (
	headerLines = 2 // title, rule
	footerLines = 2 // status or prompt, key hints
)

// detailLines is the height of the details pane for a terminal height.
func detailLines(height int) int {
	switch {
	case height >= 18:
		return 6
	case height >= 12:
		return 3
	}
	return 0
}

func previewHeight(height int) int {
	h := height - headerLines - footerLines
	if h < 1 {
		h = 1
	}
	return h
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	cursorStyle = lipgloss.NewStyle().Reverse(true)
	statusStyle = lipgloss.NewStyle().Bold(true)
	groupStyles = map[drift.Group]lipgloss.Style{
		drift.Drift:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		drift.Conflict:  lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		drift.Converged: lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		drift.Unmanaged: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		drift.Warning:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		acknowledged:    lipgloss.NewStyle().Faint(true),
	}
	actionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
)

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	w := max(m.width, 1)
	fit := func(s string) string { return ansi.Truncate(s, w, "…") }

	// Header.
	title := "dpg resolve"
	switch m.mode {
	case previewMode, runningMode:
		title += " · plan"
	case helpMode:
		title += " · keys"
	}
	total := 0
	for _, r := range m.roots {
		total += len(r.leaves)
	}
	info := fmt.Sprintf("%d item(s) · %d chosen", total, len(m.chosen))
	if m.filter != "" {
		info += fmt.Sprintf(" · filter %q", m.filter)
	}
	title = titleStyle.Render(title) + "  " + dimStyle.Render(info)
	rule := dimStyle.Render(strings.Repeat("─", w))

	// Body.
	var body []string
	line := func(s string) { body = append(body, fit(s)) }
	switch {
	case m.mode == helpMode:
		help := helpText(m.prevMode)
		for i := 0; i < m.height-headerLines-footerLines; i++ {
			if i < len(help) {
				line(help[i])
			} else {
				line("")
			}
		}
	case m.mode == previewMode || m.mode == runningMode:
		for _, l := range strings.Split(m.preview.View(), "\n") {
			line(l)
		}
	default:
		m.viewList(line)
	}

	// Footer.
	status, keys := m.footer()

	// On a short screen the rows matter most: drop the rule, the key
	// hints, the title and the status line, in that order.
	h := max(m.height, 1)
	showRule, showKeys, showTitle, showStatus := true, true, true, true
	count := func() int {
		n := len(body)
		for _, b := range []bool{showRule, showKeys, showTitle, showStatus} {
			if b {
				n++
			}
		}
		return n
	}
	for _, drop := range []*bool{&showRule, &showKeys, &showTitle, &showStatus} {
		if count() <= h {
			break
		}
		*drop = false
	}
	if over := count() - h; over > 0 {
		body = body[:max(len(body)-over, 0)]
	}

	var out []string
	if showTitle {
		out = append(out, fit(title))
	}
	if showRule {
		out = append(out, rule)
	}
	out = append(out, body...)
	if showStatus {
		out = append(out, fit(status))
	}
	if showKeys {
		out = append(out, fit(keys))
	}
	return strings.Join(out, "\n")
}

func (m Model) viewList(line func(string)) {
	h := m.listHeight()
	if len(m.rows) == 0 {
		msg := "No drift: nothing to resolve."
		if m.filter != "" {
			msg = "Nothing matches the filter (esc clears it)."
		}
		line(dimStyle.Render(msg))
		h--
	}
	for i := m.offset; i < m.offset+h; i++ {
		if i < len(m.rows) {
			line(m.row(m.rows[i], i == m.cursor))
		} else {
			line("")
		}
	}
	d := detailLines(m.height)
	if d == 0 {
		return
	}
	details := m.details()
	line(dimStyle.Render(strings.Repeat("─", max(m.width, 1))))
	for i := 0; i < d-1; i++ {
		if i < len(details) {
			line(details[i])
		} else {
			line("")
		}
	}
}

// row renders one tree row.
func (m Model) row(n *node, cursor bool) string {
	indent := strings.Repeat("  ", n.depth)
	mark := "  "
	if cursor {
		mark = "› "
	}
	var s string
	switch n.kind {
	case itemNode:
		s = indent + m.tag(n) + " " + n.label
		if sum := summary(n.item); sum != "" {
			s += "  " + dimStyle.Render(sum)
		}
	default:
		arrow := "▾ "
		if !n.expanded {
			arrow = "▸ "
		}
		label := n.label
		if n.kind == groupNode {
			label = groupStyles[n.group].Render(label)
		}
		s = indent + arrow + label + dimStyle.Render(fmt.Sprintf(" (%d", len(n.leaves)))
		if c := m.chosenUnder(n); c > 0 {
			s += dimStyle.Render(fmt.Sprintf(", %d chosen", c))
		}
		s += dimStyle.Render(")")
	}
	if cursor {
		return cursorStyle.Render(mark) + s
	}
	return mark + s
}

// tag shows an item's chosen action: [adopt  ], [adopt +] (required by
// another adoption), [adopt i] / [adopt e] (macro inlined / edited).
func (m Model) tag(n *node) string {
	c, ok := m.chosen[n.key()]
	if !ok {
		return "[        ]"
	}
	suffix := " "
	switch {
	case c.auto:
		suffix = "+"
	case c.macro == resolve.MacroInline:
		suffix = "i"
	case c.macro == resolve.MacroEdit:
		suffix = "e"
	}
	return actionStyle.Render(fmt.Sprintf("[%-6s %s]", shortAction(c.action), suffix))
}

func shortAction(a resolve.Action) string {
	if a == resolve.Acknowledge {
		return "ack"
	}
	if a == resolve.Unacknowledge {
		return "unack"
	}
	return string(a)
}

func (m Model) chosenUnder(n *node) int {
	c := 0
	for _, l := range n.leaves {
		if _, ok := m.chosen[l.key()]; ok {
			c++
		}
	}
	return c
}

// summary describes an item in a few words.
func summary(it *drift.Item) string {
	var s string
	switch {
	case it.Acknowledged != nil:
		s = "acknowledged"
		if it.Acknowledged.By != "" {
			s += " by " + it.Acknowledged.By
		}
		if it.Acknowledged.Reason != "" {
			s += ": " + it.Acknowledged.Reason
		}
	case it.Group == drift.Converged:
		s = "source and database agree; the snapshot is out of date"
	case it.Change == "missing":
		s = "missing from the database"
	case it.Change == "extra" && it.Property == "":
		s = "only in the database"
	case it.Change == "extra":
		s = "only in the database"
		if it.Live != "" {
			s += ": " + oneLine(it.Live)
		}
	case it.Change == "regranted":
		s = "revoked in the source, granted in the database"
	default:
		s = oneLine(orNone(it.Snapshot)) + " → " + oneLine(orNone(it.Live))
	}
	if it.Lapsed != nil {
		s += " (acknowledgement lapsed)"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// details describes the node under the cursor.
func (m Model) details() []string {
	n := m.current()
	if n == nil {
		return nil
	}
	if n.kind != itemNode {
		counts := map[resolve.Action]int{}
		for _, l := range n.leaves {
			if c, ok := m.chosen[l.key()]; ok {
				counts[c.action]++
			}
		}
		var parts []string
		for _, a := range []resolve.Action{resolve.Adopt, resolve.Revert, resolve.Ignore, resolve.Acknowledge, resolve.Unacknowledge} {
			if counts[a] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[a], a))
			}
		}
		chosen := "nothing chosen"
		if len(parts) > 0 {
			chosen = strings.Join(parts, ", ")
		}
		return []string{titleStyle.Render(n.label), fmt.Sprintf("%d item(s): %s", len(n.leaves), chosen)}
	}
	it := n.item
	out := []string{titleStyle.Render(it.ID) + "  " + groupStyles[n.group].Render(groupName(n.group))}
	var vals []string
	if it.Snapshot != "" {
		vals = append(vals, "snapshot: "+oneLine(it.Snapshot))
	}
	if it.Live != "" {
		vals = append(vals, "database: "+oneLine(it.Live))
	}
	if it.Source != "" {
		vals = append(vals, "source: "+oneLine(it.Source))
	}
	if len(vals) > 0 {
		out = append(out, strings.Join(vals, " · "))
	}
	if len(it.SQL) > 0 {
		s := "revert runs: " + oneLine(it.SQL[0])
		if len(it.SQL) > 1 {
			s += fmt.Sprintf(" (+%d more)", len(it.SQL)-1)
		}
		out = append(out, s)
	}
	var acts []string
	for _, a := range resolve.Actions(*it) {
		acts = append(acts, string(a))
	}
	s := "can: " + strings.Join(acts, ", ")
	if by := m.requiredBy[n.key()]; len(by) > 0 {
		s += " · needed by " + firstKey(by)
		if len(by) > 1 {
			s += fmt.Sprintf(" and %d more", len(by)-1)
		}
	}
	if m.engine.MacroDeclared(m.targetOf(n), it.ID) {
		s += " · declared by a macro (m: inline / edit the macro)"
	}
	out = append(out, s)
	if a := it.Acknowledged; a != nil {
		s := "acknowledged " + a.At.Format("2006-01-02")
		if a.By != "" {
			s += " by " + a.By
		}
		if a.Reason != "" {
			s += ": " + a.Reason
		}
		if a.Expires != nil {
			s += " · expires " + a.Expires.Format("2006-01-02")
		}
		out = append(out, s)
	}
	return out
}

// footer returns the status (or prompt) line and the key hints.
func (m Model) footer() (string, string) {
	status := m.status
	if status != "" {
		status = statusStyle.Render(status)
	}
	switch m.mode {
	case filterMode:
		return "filter: " + m.input.View(), "enter apply · esc cancel"
	case reasonMode:
		return "why are these acknowledged (optional): " + m.input.View(), "enter next · esc back"
	case expiresMode:
		return "acknowledgements lapse on (YYYY-MM-DD, optional): " + m.input.View(), "enter preview · esc back"
	case previewMode:
		if status == "" && m.plan != nil && len(m.plan.Problems) > 0 {
			status = statusStyle.Render(fmt.Sprintf("%d choice(s) can't be carried out; see above", len(m.plan.Problems)))
		}
		return status, "y apply · ↑/↓ pgup/pgdn scroll · esc back · ? keys"
	case runningMode:
		if status == "" {
			status = "applying…"
		}
		return status, ""
	case confirmQuit:
		return statusStyle.Render(fmt.Sprintf("Discard %d choice(s) and quit? y/n", len(m.chosen))), ""
	case helpMode:
		return status, "any key closes"
	}
	return status, "space choose · a/r/i/n/u · tab cycle · p preview · / filter · ? keys · q quit"
}

func helpText(from mode) []string {
	if from == previewMode {
		return []string{
			"y            apply the plan",
			"↑ ↓ pgup pgdn  scroll",
			"esc  q  b    back to the items",
			"ctrl+c       quit",
		}
	}
	return []string{
		"↑ ↓  k j       move              pgup pgdn  page",
		"home g  end G  first, last",
		"→ l enter      expand            ← h        collapse",
		"E  C           expand all, collapse all",
		"",
		"space          choose (or unchoose) what is under the cursor",
		"a              adopt: keep the database's state in the source",
		"r              revert: put the database back",
		"i              ignore: add an [ignore] pattern",
		"n              acknowledge          u  unacknowledge",
		"x              unchoose             tab  next action",
		"m              macro parts: inline here / edit the macro",
		"",
		"On a group or object, a key applies to every item it allows.",
		"Adopting an undeclared object chooses what it needs ([adopt +]).",
		"",
		"/              filter by item ID (esc clears)",
		"p              preview the plan, then y to apply",
		"q              quit",
	}
}
