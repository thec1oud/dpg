package scanner_test

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanner"
)

// TestScanSpans: each declaration's spans cover exactly its text in the
// original file, macro spreads included (they cover the ...name text).
func TestScanSpans(t *testing.T) {
	src := "MACRO ts (\n    created_at timestamptz\n)\n\n" +
		"ROLE app LOGIN; -- note\n" +
		"TABLE t (\n    id int,\n    ...ts\n) {\n    COMMENT 'x';\n}\n" +
		"SCHEMA s {\n    VIEW v AS SELECT 1 AS a;\n}\n"
	objs, err := scanner.New().Scan("f.dpg", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	text := func(sp pipeline.Span) string { return src[sp.Start:sp.End] }
	want := map[string][3]string{ // name → {decl, part1, part2}
		"app": {"ROLE app LOGIN;", "app LOGIN", ""},
		"t": {
			"TABLE t (\n    id int,\n    ...ts\n) {\n    COMMENT 'x';\n}",
			"t (\n    id int,\n    ...ts\n)",
			"\n    COMMENT 'x';\n"},
		"v": {"VIEW v AS SELECT 1 AS a;", "v AS SELECT 1 AS a", ""},
		"s": {"SCHEMA s {\n    VIEW v AS SELECT 1 AS a;\n}", "", ""},
	}
	found := 0
	for _, o := range objs {
		w, ok := want[strings.Fields(o.Part1)[0]]
		if !ok {
			t.Errorf("unexpected object %q", o.Part1)
			continue
		}
		found++
		if got := text(o.DeclSpan); got != w[0] {
			t.Errorf("%q DeclSpan = %q, want %q", o.Part1, got, w[0])
		}
		if w[1] != "" {
			if got := text(o.Part1Span); got != w[1] {
				t.Errorf("%q Part1Span = %q, want %q", o.Part1, got, w[1])
			}
		}
		if w[2] != "" {
			if got := text(o.Part2Span); got != w[2] {
				t.Errorf("%q Part2Span = %q, want %q", o.Part1, got, w[2])
			}
		}
		if o.DeclSpan.Line != o.Pos.Line {
			t.Errorf("%q DeclSpan line %d, Pos line %d", o.Part1, o.DeclSpan.Line, o.Pos.Line)
		}
	}
	if found != len(want) {
		t.Errorf("found %d of %d objects", found, len(want))
	}
}
