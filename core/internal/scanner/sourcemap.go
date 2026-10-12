package scanner

import (
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// segment maps a range of the macro-expanded text to the original text.
type segment struct {
	exp, n        int // expanded range [exp, exp+n)
	orig, origEnd int // original range [orig, origEnd)
	// copied is true for text copied verbatim (orig..origEnd has length
	// n); otherwise the range replaces the original one wholesale: a
	// ...name spread (macro is the name) or a removed MACRO declaration.
	copied bool
	macro  string
}

// sourceMap maps offsets in the macro-expanded text back to the original.
type sourceMap struct {
	segs    []segment
	expLen  int
	origLen int
}

// mapWriter builds the expanded text and its sourceMap together.
type mapWriter struct {
	b strings.Builder
	m sourceMap
}

// copy appends src[from:to] verbatim.
func (w *mapWriter) copy(src []byte, from, to int) {
	if to <= from {
		return
	}
	exp := w.b.Len()
	w.b.Write(src[from:to])
	if n := len(w.m.segs); n > 0 {
		last := &w.m.segs[n-1]
		if last.copied && last.exp+last.n == exp && last.origEnd == from {
			last.n += to - from
			last.origEnd = to
			return
		}
	}
	w.m.segs = append(w.m.segs, segment{exp: exp, n: to - from, orig: from, origEnd: to, copied: true})
}

// replace appends text standing for src[from:to].
func (w *mapWriter) replace(text string, from, to int, macro string) {
	exp := w.b.Len()
	w.b.WriteString(text)
	w.m.segs = append(w.m.segs, segment{exp: exp, n: len(text), orig: from, origEnd: to, macro: macro})
}

func (w *mapWriter) finish(origLen int) ([]byte, *sourceMap) {
	w.m.expLen = w.b.Len()
	w.m.origLen = origLen
	return []byte(w.b.String()), &w.m
}

// identityMap maps a text to itself.
func identityMap(n int) *sourceMap {
	return &sourceMap{segs: []segment{{exp: 0, n: n, orig: 0, origEnd: n, copied: true}}, expLen: n, origLen: n}
}

// find returns the segment containing expanded offset e, preferring the one
// that starts at e (end is false) or ends at e (end is true).
func (m *sourceMap) find(e int, end bool) (segment, bool) {
	i := sort.Search(len(m.segs), func(i int) bool {
		s := m.segs[i]
		if end {
			return s.exp+s.n >= e
		}
		return s.exp+s.n > e
	})
	if i == len(m.segs) {
		return segment{}, false
	}
	return m.segs[i], true
}

// start maps an expanded offset at which a range starts; in a replaced
// range, it also returns the offset within the replacement.
func (m *sourceMap) start(e int) (int, string, int) {
	if e >= m.expLen {
		return m.origLen, "", 0
	}
	s, ok := m.find(e, false)
	if !ok {
		return m.origLen, "", 0
	}
	if s.copied {
		return s.orig + (e - s.exp), "", 0
	}
	return s.orig, s.macro, e - s.exp
}

// end maps an expanded offset at which a range ends.
func (m *sourceMap) end(e int) (int, string) {
	if e <= 0 {
		return 0, ""
	}
	s, ok := m.find(e, true)
	if !ok {
		return m.origLen, ""
	}
	if s.copied {
		return s.orig + (e - s.exp), ""
	}
	return s.origEnd, s.macro
}

// lineIndex gives the line and column of an offset in the original text.
type lineIndex []int // offset at which each line starts

func newLineIndex(src []byte) lineIndex {
	idx := lineIndex{0}
	for i, b := range src {
		if b == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (li lineIndex) lineCol(off int) (int, int) {
	l := sort.Search(len(li), func(i int) bool { return li[i] > off }) - 1
	if l < 0 {
		l = 0
	}
	return l + 1, off - li[l] + 1
}

// fileText implements pipeline.SourceText for one scanned file.
type fileText struct {
	path  string
	smap  *sourceMap
	lines lineIndex
}

// Span maps the expanded range [a, b) to a Span over the original text.
func (f *fileText) Span(a, b int) pipeline.Span {
	start, m1, at := f.smap.start(a)
	end, m2 := f.smap.end(b)
	if end < start {
		end = start
	}
	line, col := f.lines.lineCol(start)
	macro := m1
	if macro == "" {
		macro = m2
	}
	return pipeline.Span{File: f.path, Start: start, End: end, Line: line, Col: col, Macro: macro, MacroAt: at}
}

func (s *state) span(a, b int) pipeline.Span { return s.text.Span(a, b) }

// textSpan returns the span of text within the expanded range [a, b): the
// first occurrence of text there, else the whole range.
func (s *state) textSpan(a, b int, text string) pipeline.Span {
	if a < 0 || b > len(s.src) || a > b {
		return pipeline.Span{}
	}
	if i := strings.Index(string(s.src[a:b]), text); i >= 0 && text != "" {
		return s.span(a+i, a+i+len(text))
	}
	return s.span(a, b)
}
