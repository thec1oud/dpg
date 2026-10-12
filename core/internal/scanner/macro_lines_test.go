package scanner_test

import (
	"testing"

	"github.com/thec1oud/dpg/internal/scanner"
)

// TestMacroKeepsSourceLines guards RFC Section 17.1's source positions:
// removing MACRO declarations and expanding spreads must not shift the line
// numbers of later declarations.
func TestMacroKeepsSourceLines(t *testing.T) {
	src := "MACRO ts (\n    a int, -- first\n    b int\n)\n\nTABLE t1 (\n    ...ts,\n    c int\n);\n\nTABLE t2 (x int);\n"
	objs, err := scanner.New().Scan("f.dpg", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Pos.Line != 6 || objs[1].Pos.Line != 11 {
		for _, o := range objs {
			t.Logf("%q at line %d", o.Part1, o.Pos.Line)
		}
		t.Fatalf("expected declarations at lines 6 and 11")
	}
}

// TestBareEllipsisKeepsSourceLines: "..." with no macro name is not a
// spread and is kept as written, including the line break after it.
func TestBareEllipsisKeepsSourceLines(t *testing.T) {
	src := "TABLE t1 (\n    id int,\n    ...\n);\n\nTABLE t2 (x int);\n"
	objs, err := scanner.New().Scan("f.dpg", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[1].Pos.Line != 6 || objs[0].End.Line != 4 {
		for _, o := range objs {
			t.Logf("%q at %d..%d", o.Part1, o.Pos.Line, o.End.Line)
		}
		t.Fatalf("expected t1 to end on line 4 and t2 to start on line 6")
	}
}

// TestScanRecordsEnd: End is just past the terminating ';' or '}'.
func TestScanRecordsEnd(t *testing.T) {
	src := "ROLE a NOLOGIN; -- c\nROLE b NOLOGIN {\n    COMMENT 'b';\n} -- d\nSCHEMA s {\n    ROLE c NOLOGIN;\n}\n"
	objs, err := scanner.New().Scan("f.dpg", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{"a NOLOGIN": {1, 16}, "b NOLOGIN": {4, 2}, "s": {7, 2}, "c NOLOGIN": {6, 20}}
	for _, o := range objs {
		w, ok := want[o.Part1]
		if !ok {
			continue
		}
		if o.End.Line != w[0] || o.End.Col != w[1] {
			t.Errorf("%q: End = %d:%d, want %d:%d", o.Part1, o.End.Line, o.End.Col, w[0], w[1])
		}
		delete(want, o.Part1)
	}
	if len(want) != 0 {
		t.Errorf("objects not found: %v", want)
	}
}
