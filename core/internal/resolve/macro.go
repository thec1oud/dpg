package resolve

import (
	"fmt"
	"strings"

	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanutil"
	"github.com/thec1oud/dpg/internal/srcindex"
)

// MacroMode is how a part declared by a macro spread is adopted.
type MacroMode string

const (
	MacroAsk    MacroMode = ""       // refuse, naming the choices
	MacroEdit   MacroMode = "edit"   // change the macro's definition
	MacroInline MacroMode = "inline" // expand this spread and change it here
)

// partEdit plans replacing a part's text with *text, or removing it when
// text is nil. A part a macro produced is changed in the macro or inlined,
// as the decision says.
func (pl *planner) partEdit(t *Target, s originSpan, text *string, d Decision) ([]textEdit, error) {
	fc, err := pl.file(s.span.File)
	if err != nil {
		return nil, err
	}
	src := scanutil.NormalizeSource(fc.Before)
	if s.span.Macro == "" {
		if text == nil {
			start, end := removalRange(src, s)
			return []textEdit{{path: s.span.File, start: start, end: end, decision: d}}, nil
		}
		return []textEdit{{path: s.span.File, start: s.span.Start, end: s.span.End,
			text: reindent(*text, lineIndent(src, s.span.Start)), decision: d}}, nil
	}
	if s.macro == nil {
		return nil, fmt.Errorf("declared in part by macro %s; edit it by hand", s.span.Macro)
	}
	mp := s.macro
	switch d.Macro {
	case MacroInline:
		// The spread becomes its expansion, with the part changed.
		exp := []byte(mp.Expansion)
		part := originSpan{span: pipeline.Span{Start: mp.Offset, End: mp.Offset + mp.Len}, origin: "part1"}
		var out string
		if text == nil {
			a, b := removalRange(exp, part)
			out = string(exp[:a]) + string(exp[b:])
		} else {
			out = string(exp[:mp.Offset]) + oneLineText(*text) + string(exp[mp.Offset+mp.Len:])
		}
		out = strings.TrimSpace(out)
		if out == "" {
			start, end := removalRange(src, originSpan{span: mp.Spread, origin: "part1"})
			return []textEdit{{path: s.span.File, start: start, end: end, decision: d}}, nil
		}
		return []textEdit{{path: s.span.File, start: mp.Spread.Start, end: mp.Spread.End,
			text: layoutEntries(src, mp.Spread.Start, out), decision: d}}, nil

	case MacroEdit:
		file, start, end, err := pl.findInMacro(t, s.span.Macro, mp.Expansion[mp.Offset:mp.Offset+mp.Len])
		if err != nil {
			return nil, err
		}
		mfc, err := pl.file(file)
		if err != nil {
			return nil, err
		}
		msrc := scanutil.NormalizeSource(mfc.Before)
		if text == nil {
			a, b := removalRange(msrc, originSpan{span: pipeline.Span{Start: start, End: end}, origin: "part1"})
			return []textEdit{{path: file, start: a, end: b, decision: d}}, nil
		}
		return []textEdit{{path: file, start: start, end: end, text: reindent(*text, lineIndent(msrc, start)), decision: d}}, nil
	}
	return nil, fmt.Errorf("declared by macro %s: choose to edit the macro (every use changes) or to inline it here", s.span.Macro)
}

// findInMacro finds the text of part in macro name's definition, compared
// token by token (an expansion is the body on one line). It must occur
// exactly once.
func (pl *planner) findInMacro(t *Target, name, part string) (string, int, int, error) {
	want := codeTexts(lexAt(part, 0), part)
	if len(want) == 0 {
		return "", 0, 0, fmt.Errorf("nothing to find in macro %s", name)
	}
	var found []struct {
		file       string
		start, end int
	}
	defined := false
	for _, f := range t.SourceFiles {
		fc, err := pl.file(f)
		if err != nil {
			return "", 0, 0, err
		}
		src := scanutil.NormalizeSource(fc.Before)
		bs, be, ok := macroBody(src, name)
		if !ok {
			continue
		}
		defined = true
		ts := lexAt(string(src[bs:be]), bs)
		var code []tok
		for _, x := range ts {
			if !isTriviaTok(x) {
				code = append(code, x)
			}
		}
		for i := 0; i+len(want) <= len(code); i++ {
			match := true
			for j, w := range want {
				if string(src[code[i+j].start:code[i+j].end]) != w {
					match = false
					break
				}
			}
			if match {
				found = append(found, struct {
					file       string
					start, end int
				}{f, code[i].start, code[i+len(want)-1].end})
			}
		}
	}
	switch {
	case !defined:
		return "", 0, 0, fmt.Errorf("macro %s is not defined in this database's source (a global macro?); edit it by hand", name)
	case len(found) == 0:
		return "", 0, 0, fmt.Errorf("can't find %q in macro %s (it may come from a nested macro); edit it by hand", part, name)
	case len(found) > 1:
		return "", 0, 0, fmt.Errorf("%q occurs %d times in macro %s; edit it by hand", part, len(found), name)
	}
	return found[0].file, found[0].start, found[0].end, nil
}

// macroBody returns the inside of the parentheses of "MACRO name ( ... )".
func macroBody(src []byte, name string) (int, int, bool) {
	ts := lexAt(string(src), 0)
	var code []tok
	for _, x := range ts {
		if !isTriviaTok(x) {
			code = append(code, x)
		}
	}
	for i := 0; i+2 < len(code); i++ {
		if !strings.EqualFold(string(src[code[i].start:code[i].end]), "MACRO") ||
			string(src[code[i+1].start:code[i+1].end]) != name || code[i+2].typ != format.TokLParen {
			continue
		}
		depth := 0
		for _, x := range code[i+2:] {
			switch x.typ {
			case format.TokLParen:
				depth++
			case format.TokRParen:
				depth--
				if depth == 0 {
					return code[i+2].end, x.start, true
				}
			}
		}
	}
	return 0, 0, false
}

func codeTexts(ts []tok, s string) []string {
	var out []string
	for _, x := range ts {
		if !isTriviaTok(x) {
			out = append(out, s[x.start:x.end])
		}
	}
	return out
}

// oneLineText puts a rendered part on one line, as expansions are.
func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// layoutEntries lays out an inlined expansion at the spread at off: one
// entry per line, at the spread's indentation, when the spread starts its
// line; else as it is.
func layoutEntries(src []byte, off int, exp string) string {
	indent := lineIndent(src, off)
	if off-len(indent) > 0 && src[off-len(indent)-1] != '\n' || off != lineStart(src, off)+len(indent) {
		return exp
	}
	var entries []string
	depth, last := 0, 0
	for _, x := range lexAt(exp, 0) {
		switch x.typ {
		case format.TokLParen:
			depth++
		case format.TokRParen:
			depth--
		case format.TokComma:
			if depth == 0 {
				entries = append(entries, strings.TrimSpace(exp[last:x.start]))
				last = x.end
			}
		}
	}
	entries = append(entries, strings.TrimSpace(exp[last:]))
	return strings.Join(entries, ",\n"+indent)
}

func lineStart(src []byte, off int) int {
	for off > 0 && src[off-1] != '\n' {
		off--
	}
	return off
}

// MacroDeclared reports whether adopting item id would change a part a
// macro spread declares (and so needs a MacroMode).
func MacroDeclared(t *Target, id string) bool {
	it, ok := findItem(t.Report, id)
	if !ok || it.Property == "" || t.Index == nil {
		return false
	}
	key := it.Property
	if strings.Contains(key, "grant:") {
		return false
	}
	for _, sp := range t.Index.Spans(it.Object, srcindex.PropertyKey(key)) {
		if sp.Macro != "" {
			return true
		}
	}
	return false
}
