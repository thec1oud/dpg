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
