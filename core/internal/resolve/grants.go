package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/scanutil"
)

// adoptGrant plans adopting a grant item:
//
//   - extra (granted live, not declared): a GRANTS entry is added;
//   - missing (declared, not held live): the privilege is taken out of the
//     entries granting it to the role, keeping what the role does hold (a
//     declared ALL becomes the privileges held; a missing grant option
//     leaves the plain privilege);
//   - regranted (revoked in the source, held live): the privilege is taken
//     out of the REVOKES entries and a GRANTS entry is added.
func (pl *planner) adoptGrant(t *Target, it drift.Item, d Decision) ([]textEdit, []snapTask, error) {
	g := it.Grant
	if g == nil {
		return nil, nil, fmt.Errorf("not a grant item")
	}
	if g.Parameter != "" || it.Kind == "parameter_privileges" || it.Kind == "default_privileges" {
		return nil, nil, fmt.Errorf("adopting %s grants is not supported; edit the declaration by hand", it.Kind)
	}
	task := []snapTask{{key: it.Object, property: it.Property}}
	var groups []string
	prefix := ""
	if g.Holder != "" {
		kind, name, _ := strings.Cut(g.Holder, ":")
		if kind != "column" {
			return nil, nil, fmt.Errorf("grants on a %s are not supported; edit the declaration by hand", kind)
		}
		groups = []string{"COLUMNS", unescape(name)}
		prefix = g.Holder + "/"
	}
	grantEntry := func(priv string, withGrant bool) originSpan {
		s := priv + " TO " + g.Role
		if withGrant {
			s += " WITH GRANT OPTION"
		}
		return originSpan{origin: "block", text: s + ";", groups: append(append([]string(nil), groups...), "GRANTS")}
	}

	switch it.Change {
	case "extra":
		e, err := pl.insertPart(t, it, d, grantEntry(g.Privilege, g.WithGrant))
		return e, task, err

	case "missing":
		// What the role keeps of the privilege: nothing, or the plain
		// privilege when only the grant option is missing.
		keep := func(priv string) (privs []string, plain []string) {
			if strings.EqualFold(priv, "ALL") {
				// A declared ALL covers whatever the role holds (MAINTAIN
				// on PostgreSQL 17 included): it becomes exactly that.
				for p, wg := range g.Held {
					if wg || !g.WithGrant {
						privs = append(privs, p)
					} else {
						plain = append(plain, p)
					}
				}
				sort.Strings(privs)
				sort.Strings(plain)
				return privs, plain
			}
			if _, held := g.Held[strings.ToUpper(priv)]; held && g.WithGrant {
				return nil, []string{strings.ToUpper(priv)}
			}
			return nil, nil
		}
		return pl.rewriteGrants(t, it, d, prefix+"grants", g, keep, task)

	case "regranted":
		edits, _, err := pl.rewriteGrants(t, it, d, prefix+"revocations", g, func(string) ([]string, []string) { return nil, nil }, task)
		if err != nil {
			return nil, nil, err
		}
		wg := g.Held[strings.ToUpper(g.Privilege)]
		e, err := pl.insertPart(t, it, d, grantEntry(strings.ToUpper(g.Privilege), wg))
		if err != nil {
			return nil, nil, err
		}
		return append(edits, e...), task, nil
	}
	return nil, nil, fmt.Errorf("unknown grant change %q", it.Change)
}

// rewriteGrants takes g's privilege for g's role out of every entry under
// key (grants or revocations) that names them. keep returns the
// privileges the role keeps instead: privs with the entry's grant option,
// plain without it.
func (pl *planner) rewriteGrants(t *Target, it drift.Item, d Decision, key string, g *drift.GrantRef,
	keep func(priv string) (privs, plain []string), task []snapTask) ([]textEdit, []snapTask, error) {
	var edits []textEdit
	for _, dc := range t.Index.Decls[it.Object] {
		for _, sp := range dc.Items[key] {
			if sp.Macro != "" {
				return nil, nil, fmt.Errorf("declared by macro %s", sp.Macro)
			}
			fc, err := pl.file(sp.File)
			if err != nil {
				return nil, nil, err
			}
			text := scanutil.NormalizeSource(fc.Before)
			e, err := parseGrantEntry(string(text[sp.Start:sp.End]))
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %v", key, err)
			}
			out, changed := e.without(g, keep)
			if !changed {
				continue
			}
			if len(out) == 0 {
				start, end := removalRange(text, originSpan{span: sp, origin: originOf(dc, sp), blockStart: dc.Block.Start})
				edits = append(edits, textEdit{path: sp.File, start: start, end: end, decision: d})
				continue
			}
			indent := lineIndent(text, sp.Start)
			var parts []string
			for _, x := range out {
				parts = append(parts, x.render())
			}
			edits = append(edits, textEdit{path: sp.File, start: sp.Start, end: sp.End, text: strings.Join(parts, "\n"+indent), decision: d})
		}
	}
	if len(edits) == 0 {
		return nil, nil, fmt.Errorf("no %s entry in the source names %s for %s", key, g.Privilege, g.Role)
	}
	return edits, task, nil
}

// grantEntry is one GRANTS or REVOKES entry (or a GRANT/REVOKE directive),
// split into its parts as written.
type grantEntry struct {
	verb  string   // "GRANT " or "REVOKE " for a directive, else ""
	privs []string // as written; nil for ALL
	all   string   // "ALL" or "ALL PRIVILEGES" as written, for ALL
	join  string   // " TO " / " FROM " as written
	roles []string // as written
	tail  string   // " WITH GRANT OPTION", " GRANTED BY r", " CASCADE", as written
	term  string   // ";" when the entry has it
}

// parseGrantEntry splits "[GRANT|REVOKE] privs TO|FROM roles [tail];".
// Privileges with column lists are refused.
func parseGrantEntry(s string) (grantEntry, error) {
	var e grantEntry
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ";") {
		e.term, s = ";", strings.TrimSpace(s[:len(s)-1])
	}
	var words []string
	var spans [][2]int
	off := 0
	for _, t := range format.Lex("", []byte(s)) {
		if t.Type == format.TokEOF {
			break
		}
		switch t.Type {
		case format.TokLParen:
			return e, fmt.Errorf("privileges with column lists are not supported; edit %q by hand", s)
		case format.TokWhitespace, format.TokNewline, format.TokLineComment, format.TokBlockComment:
		default:
			words = append(words, t.Text)
			spans = append(spans, [2]int{off, off + len(t.Text)})
		}
		off += len(t.Text)
	}
	i := 0
	if i < len(words) && (strings.EqualFold(words[i], "GRANT") || strings.EqualFold(words[i], "REVOKE")) {
		e.verb = s[spans[i][0]:spans[i+1][0]]
		i++
	}
	// Privileges up to TO / FROM.
	start := i
	for i < len(words) && !strings.EqualFold(words[i], "TO") && !strings.EqualFold(words[i], "FROM") {
		i++
	}
	if i == len(words) || i == start {
		return e, fmt.Errorf("can't read %q", s)
	}
	privText := strings.TrimSpace(s[spans[start][0]:spans[i-1][1]])
	if up := strings.ToUpper(strings.Join(strings.Fields(privText), " ")); up == "ALL" || up == "ALL PRIVILEGES" {
		e.all = privText
	} else {
		for _, p := range strings.Split(privText, ",") {
			e.privs = append(e.privs, strings.TrimSpace(p))
		}
	}
	e.join = s[spans[i-1][1]:spans[i+1][0]]
	i++
	// Roles up to the tail.
	rs := i
	for i < len(words) && !strings.EqualFold(words[i], "WITH") && !strings.EqualFold(words[i], "GRANTED") && !strings.EqualFold(words[i], "CASCADE") {
		i++
	}
	if i == rs {
		return e, fmt.Errorf("can't read %q", s)
	}
	for _, r := range strings.Split(s[spans[rs][0]:spans[i-1][1]], ",") {
		e.roles = append(e.roles, strings.TrimSpace(r))
	}
	if i < len(words) {
		e.tail = s[spans[i-1][1]:]
	}
	return e, nil
}

func (e grantEntry) withGrant() bool {
	return strings.Contains(strings.ToUpper(e.tail), "GRANT OPTION")
}

func (e grantEntry) render() string {
	privs := e.all
	if e.privs != nil {
		privs = strings.Join(e.privs, ", ")
	}
	term := e.term
	if term == "" {
		term = ";"
	}
	return e.verb + privs + e.join + strings.Join(e.roles, ", ") + e.tail + term
}

// without returns the entries replacing e once g's privilege is taken away
// from g's role, and whether anything changed.
func (e grantEntry) without(g *drift.GrantRef, keep func(string) ([]string, []string)) ([]grantEntry, bool) {
	ri := -1
	for i, r := range e.roles {
		if roleName(r) == g.Role {
			ri = i
		}
	}
	if ri < 0 {
		return []grantEntry{e}, false
	}
	// The privileges the entry gives the role, after the change.
	var mine []string
	var plain []string
	switch {
	case strings.EqualFold(g.Privilege, "ALL"):
		if e.privs != nil {
			return []grantEntry{e}, false
		}
		mine, plain = keep("ALL")
	case e.privs == nil:
		// ALL, less the one privilege.
		for _, p := range drift.AllPrivileges(g.AllKind) {
			if !strings.EqualFold(p, g.Privilege) {
				mine = append(mine, p)
			}
		}
		_, plain = keep(g.Privilege)
	default:
		found := false
		for _, p := range e.privs {
			if strings.EqualFold(p, g.Privilege) {
				found = true
				continue
			}
			mine = append(mine, p)
		}
		if !found {
			return []grantEntry{e}, false
		}
		_, plain = keep(g.Privilege)
	}

	var out []grantEntry
	if len(e.roles) > 1 {
		rest := e
		rest.roles = append(append([]string(nil), e.roles[:ri]...), e.roles[ri+1:]...)
		out = append(out, rest)
	}
	if len(mine) > 0 {
		x := e
		x.all, x.privs, x.roles = "", mine, []string{e.roles[ri]}
		out = append(out, x)
	}
	if len(plain) > 0 && e.withGrant() {
		x := e
		x.all, x.privs, x.roles = "", plain, []string{e.roles[ri]}
		// Without WITH GRANT OPTION; GRANTED BY stays as written.
		x.tail = ""
		if i := strings.Index(strings.ToUpper(e.tail), "GRANTED"); i >= 0 {
			x.tail = " " + strings.TrimSpace(e.tail[i:])
		}
		out = append(out, x)
	}
	return out, true
}

// roleName is a role as written, as PostgreSQL names it.
func roleName(r string) string {
	if strings.HasPrefix(r, `"`) && strings.HasSuffix(r, `"`) && len(r) >= 2 {
		return strings.ReplaceAll(r[1:len(r)-1], `""`, `"`)
	}
	if strings.EqualFold(r, "PUBLIC") {
		return "PUBLIC"
	}
	return strings.ToLower(r)
}

// unescape reverses the escaping of names in item IDs.
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var c byte
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &c); err == nil {
				b.WriteByte(c)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
