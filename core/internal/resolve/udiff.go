package resolve

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a unified diff (three lines of context) of a file's
// old and new content; it is empty when they are equal.
func UnifiedDiff(name, old, new string) string {
	if old == new {
		return ""
	}
	a, b := splitLines(old), splitLines(new)
	edits := lineEdits(a, b)

	const ctx = 3
	var out strings.Builder
	fromName, toName := "a/"+name, "b/"+name
	if old == "" {
		fromName = "/dev/null"
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", fromName, toName)
	for i := 0; i < len(edits); {
		if edits[i].kind == ' ' {
			i++
			continue
		}
		// A hunk runs from ctx lines before this change to ctx lines after
		// the last change closer than 2*ctx to the previous one.
		start := max(0, i-ctx)
		end := i
		for j := i; j < len(edits); j++ {
			if edits[j].kind != ' ' {
				end = j
			} else if j-end > 2*ctx {
				break
			}
		}
		stop := min(len(edits), end+ctx+1)
		oldStart, newStart, oldN, newN := edits[start].a, edits[start].b, 0, 0
		for _, e := range edits[start:stop] {
			if e.kind != '+' {
				oldN++
			}
			if e.kind != '-' {
				newN++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(oldStart, oldN), hunkRange(newStart, newN))
		for _, e := range edits[start:stop] {
			out.WriteByte(e.kind)
			out.WriteString(e.text)
			out.WriteByte('\n')
		}
		i = stop
	}
	return out.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	if n == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineEdit is one line of a diff: ' ' kept, '-' removed, '+' added, with
// its 1-based line numbers in the old (a) and new (b) text.
type lineEdit struct {
	kind byte
	a, b int
	text string
}

// lineEdits computes a shortest line diff. The common prefix and suffix are
// trimmed first, so the quadratic table covers only the changed middle; a
// middle too large for it is shown as removed then added.
func lineEdits(a, b []string) []lineEdit {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]

	var edits []lineEdit
	ai, bi := 0, 0
	keep := func() {
		edits = append(edits, lineEdit{' ', ai + 1, bi + 1, a[ai]})
		ai++
		bi++
	}
	del := func() {
		edits = append(edits, lineEdit{'-', ai + 1, bi + 1, a[ai]})
		ai++
	}
	add := func() {
		edits = append(edits, lineEdit{'+', ai + 1, bi + 1, b[bi]})
		bi++
	}
	for range pre {
		keep()
	}
	if len(ma)*len(mb) > 4_000_000 {
		for range ma {
			del()
		}
		for range mb {
			add()
		}
	} else {
		// lcs[i][j] is the LCS length of ma[i:] and mb[j:].
		lcs := make([][]int32, len(ma)+1)
		for i := range lcs {
			lcs[i] = make([]int32, len(mb)+1)
		}
		for i := len(ma) - 1; i >= 0; i-- {
			for j := len(mb) - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(ma) || j < len(mb) {
			switch {
			case i < len(ma) && j < len(mb) && ma[i] == mb[j]:
				keep()
				i++
				j++
			case j < len(mb) && (i == len(ma) || lcs[i][j+1] > lcs[i+1][j]):
				// On a tie the line is removed first: removals before
				// additions read better.
				add()
				j++
			default:
				del()
				i++
			}
		}
	}
	for range suf {
		keep()
	}
	return edits
}
