package resolve

import (
	"fmt"
	"strings"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/scanutil"
	"github.com/thec1oud/dpg/internal/srcindex"
)

// insertPart plans adding a part the source lacks (r, cut from the
// database's rendering) to the object's declaration: a column or table
// constraint goes at the end of the column list, another Part 1 clause at
// the end of Part 1, and a block statement at the end of the block, which
// is created when the declaration has none.
func (pl *planner) insertPart(t *Target, it drift.Item, d Decision, r originSpan) ([]textEdit, error) {
	decl, err := insertionDecl(t.Index.Decls[it.Object], r.origin)
	if err != nil {
		return nil, err
	}
	if decl.Decl.Macro != "" {
		return nil, fmt.Errorf("declared by macro %s; add it by hand", decl.Decl.Macro)
	}
	fc, err := pl.file(decl.Decl.File)
	if err != nil {
		return nil, err
	}
	text := scanutil.NormalizeSource(fc.Before)
	edit := func(at int, s string) textEdit {
		return textEdit{path: decl.Decl.File, start: at, end: at, text: s, decision: d}
	}
	declIndent := lineIndent(text, decl.Decl.Start)

	switch r.origin {
	case "part1":
		p1 := decl.Part1
		ts := lexAt(string(text[p1.Start:p1.End]), p1.Start)
		if strings.Contains(decl.Kind, "table") && isListEntry(r.text) {
			open, close, ok := parens(ts)
			if !ok {
				return nil, fmt.Errorf("%s has no column list to add to", it.Object)
			}
			return listInsert(text, ts, open, close, r.text, edit), nil
		}
		end := trimRight(text, p1.Start, p1.End)
		return []textEdit{edit(end, " "+reindent(r.text, declIndent))}, nil

	case "block":
		b := decl.Block
		if !b.Valid() {
			end := trimRight(text, decl.Part1.Start, decl.Part1.End)
			inner := declIndent + "    "
			return []textEdit{edit(end, " {\n"+inner+wrapGroups(r.groups, r.text, inner)+"\n"+declIndent+"}")}, nil
		}
		// Walk down the groups the source already has.
		start, end, outer, groups := b.Start, b.End, declIndent, r.groups
		for len(groups) > 0 {
			bs, be, ok := findGroup(text, start, end, headerName(groups[0]))
			n := 1
			if one := singular[strings.ToUpper(headerName(groups[0]))]; !ok && len(groups) > 1 && one != "" {
				// COLUMN name { ... } is COLUMNS { name { ... } }.
				bs, be, ok = findGroup(text, start, end, strings.ToLower(one), headerName(groups[1]))
				n = 2
			}
			if !ok {
				break
			}
			outer = lineIndent(text, bs-1)
			start, end, groups = bs, be, groups[n:]
		}
		bodyEnd := end
		end = trimRight(text, start, end)
		first := start
		for first < end && isSpace(text[first]) {
			first++
		}
		if first == end {
			// An empty body: { }.
			inner := outer + "    "
			return []textEdit{{path: decl.Decl.File, start: start, end: bodyEnd, decision: d,
				text: "\n" + inner + wrapGroups(groups, r.text, inner) + "\n" + outer}}, nil
		}
		if !strings.Contains(string(text[start:end]), "\n") {
			return []textEdit{edit(end, " "+wrapGroups(groups, r.text, ""))}, nil
		}
		inner := lineIndent(text, first)
		return []textEdit{edit(end, "\n"+inner+wrapGroups(groups, r.text, inner))}, nil
	}
	return nil, fmt.Errorf("can't place a %s part", r.origin)
}

// insertionDecl picks the declaration a part goes into: the first with a
// Part 1 for Part 1 parts; for block parts the first with a block, else
// the first with a Part 1.
func insertionDecl(decls []srcindex.Declaration, origin string) (srcindex.Declaration, error) {
	if origin == "block" {
		for _, d := range decls {
			if d.Block.Valid() {
				return d, nil
			}
		}
	}
	for _, d := range decls {
		if d.Part1.Valid() && d.Part1.End > d.Part1.Start {
			return d, nil
		}
	}
	return srcindex.Declaration{}, fmt.Errorf("the source has no declaration to add it to")
}

// isListEntry reports whether a rendered Part 1 snippet is a column-list
// entry (a column or table constraint) rather than a clause.
func isListEntry(s string) bool {
	switch w := strings.ToUpper(firstWord(s)); w {
	case "PARTITION", "INHERITS", "WITH", "WITHOUT", "TABLESPACE", "USING", "SERVER", "OPTIONS", "ON":
		return false
	}
	return true
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t\n("); i >= 0 {
		return s[:i]
	}
	return s
}

// listInsert adds entry at the end of the parenthesised list [open, close)
// (close is the offset just past ")"), laid out like the list: on its own
// line at the last entry's indentation when entries are on separate lines,
// else after ", ". A comment trailing the last entry stays with it.
func listInsert(text []byte, ts []tok, open, close int, entry string, edit func(int, string) textEdit) []textEdit {
	var last *tok
	for i := range ts {
		tk := &ts[i]
		if tk.start > open && tk.end < close && !isTriviaTok(*tk) {
			last = tk
		}
	}
	if last == nil {
		return []textEdit{edit(close-1, entry)}
	}
	firstEntry := open + 1
	for firstEntry < close && isSpace(text[firstEntry]) {
		firstEntry++
	}
	if !strings.Contains(string(text[open:firstEntry]), "\n") {
		return []textEdit{edit(last.end, ", "+entry)}
	}
	indent := lineIndent(text, last.start)
	eol := last.end
	for eol < close-1 && text[eol] != '\n' {
		eol++
	}
	if eol == last.end || strings.TrimSpace(string(text[last.end:eol])) == "" {
		return []textEdit{edit(last.end, ",\n"+indent+reindent(entry, indent))}
	}
	// "name text -- comment": the comma after the entry, the new entry on
	// the next line.
	comma := edit(last.end, ",")
	comma.once = true
	e := edit(eol, "\n"+indent+reindent(entry, indent))
	e.listEntry = true
	return []textEdit{e, comma}
}

// tok is a lexed token with absolute offsets.
type tok struct {
	typ        format.TokType
	start, end int
}

func lexAt(s string, base int) []tok {
	var out []tok
	off := 0
	for _, t := range format.Lex("", []byte(s)) {
		if t.Type == format.TokEOF {
			break
		}
		out = append(out, tok{t.Type, base + off, base + off + len(t.Text)})
		off += len(t.Text)
	}
	return out
}

func isTriviaTok(t tok) bool {
	switch t.typ {
	case format.TokWhitespace, format.TokNewline, format.TokLineComment, format.TokBlockComment:
		return true
	}
	return false
}

// parens returns the first top-level parenthesised group: the offset of
// "(" and the offset just past its ")".
func parens(ts []tok) (int, int, bool) {
	depth, open := 0, -1
	for _, t := range ts {
		switch t.typ {
		case format.TokLParen:
			if depth == 0 {
				open = t.start
			}
			depth++
		case format.TokRParen:
			depth--
			if depth == 0 && open >= 0 {
				return open, t.end, true
			}
		}
	}
	return 0, 0, false
}

func trimRight(text []byte, start, end int) int {
	for end > start && isSpace(text[end-1]) {
		end--
	}
	return end
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// wrapGroups wraps a block statement in the groups it belongs to
// ("INDEXES", or "COLUMNS" then a column's name), laid out from indent.
func wrapGroups(groups []string, snippet, indent string) string {
	if len(groups) == 0 {
		return reindent(snippet, indent)
	}
	inner := indent + "    "
	return groups[0] + " {\n" + inner + wrapGroups(groups[1:], snippet, inner) + "\n" + indent + "}"
}

// enclosingGroups returns the headers of the brace groups enclosing off,
// scanning from start (the inside of an object's block): ["INDEXES"] for an
// index entry, ["COLUMNS", "email"] for a column's comment.
func enclosingGroups(text []byte, start, off int) []string {
	var stack []string
	var cur []string
	for _, t := range lexAt(string(text[start:off]), start) {
		switch t.typ {
		case format.TokLBrace:
			// COLUMN name { ... } is COLUMNS { name { ... } }.
			if len(cur) >= 2 {
				if plural, ok := pluralOf[strings.ToUpper(cur[0])]; ok {
					stack = append(stack, plural)
					cur = cur[1:]
				}
			}
			stack = append(stack, strings.Join(cur, " "))
			cur = nil
		case format.TokRBrace:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			cur = nil
		case format.TokSemicolon:
			cur = nil
		default:
			if !isTriviaTok(t) {
				cur = append(cur, string(text[t.start:t.end]))
			}
		}
	}
	return stack
}

// singular maps a group keyword to the directive declaring one entry.
var singular = map[string]string{
	"INDEXES": "INDEX", "POLICIES": "POLICY", "TRIGGERS": "TRIGGER", "COLUMNS": "COLUMN",
	"ATTRIBUTES": "ATTRIBUTE", "CONSTRAINTS": "CONSTRAINT", "PARTITIONS": "PARTITION",
}

var pluralOf = func() map[string]string {
	m := map[string]string{}
	for p, s := range singular {
		m[s] = p
	}
	return m
}()

// findGroup finds the group statement in [start, end) whose header starts
// with the names want (see headerName), returning the inside of its braces.
func findGroup(text []byte, start, end int, want ...string) (int, int, bool) {
	depth := 0
	var words []string
	bodyStart := -1
	for _, t := range lexAt(string(text[start:end]), start) {
		switch t.typ {
		case format.TokLBrace:
			if depth == 0 && len(words) >= len(want) && strings.Join(words[:len(want)], " ") == strings.Join(want, " ") {
				bodyStart = t.end
			}
			depth++
		case format.TokRBrace:
			depth--
			if depth == 0 {
				if bodyStart >= 0 {
					return bodyStart, t.start, true
				}
				words = nil
			}
		case format.TokSemicolon:
			if depth == 0 {
				words = nil
			}
		default:
			if depth == 0 && !isTriviaTok(t) {
				if w := headerName(string(text[t.start:t.end])); !modifier[strings.ToUpper(w)] {
					words = append(words, w)
				}
			}
		}
	}
	return 0, 0, false
}

// modifier words come before an index entry's name.
var modifier = map[string]bool{"UNIQUE": true, "CONCURRENTLY": true, "ONLY": true}

// headerName is the name a group header starts with, as an identifier
// (unquoted words fold to lower case, quoted ones are unquoted), after any
// index modifiers: "INDEXES" -> "indexes", "UNIQUE r_v_idx (v)" -> "r_v_idx".
func headerName(h string) string {
	h = strings.TrimSpace(h)
	for {
		w := firstWord(h)
		if w == "" || !modifier[strings.ToUpper(w)] || len(w) == len(h) {
			break
		}
		h = strings.TrimSpace(h[len(w):])
	}
	if strings.HasPrefix(h, `"`) {
		if i := strings.Index(h[1:], `"`); i >= 0 {
			return h[1 : i+1]
		}
	}
	return strings.ToLower(firstWord(h))
}
