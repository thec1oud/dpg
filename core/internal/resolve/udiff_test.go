package resolve

import (
	"math/rand"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	cases := []struct {
		name, new, want string
	}{
		{"equal", old, ""},
		{"insert", "a\nb\nc\nd\nX\ne\nf\ng\nh\ni\nj\n",
			"--- a/f\n+++ b/f\n@@ -2,6 +2,7 @@\n b\n c\n d\n+X\n e\n f\n g\n"},
		{"replace", "a\nb\nc\nd\nE\nf\ng\nh\ni\nj\n",
			"--- a/f\n+++ b/f\n@@ -2,7 +2,7 @@\n b\n c\n d\n-e\n+E\n f\n g\n h\n"},
		{"delete first", "b\nc\nd\ne\nf\ng\nh\ni\nj\n",
			"--- a/f\n+++ b/f\n@@ -1,4 +1,3 @@\n-a\n b\n c\n d\n"},
		{"two hunks", "A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n",
			"--- a/f\n+++ b/f\n@@ -1,4 +1,4 @@\n-a\n+A\n b\n c\n d\n@@ -7,4 +7,4 @@\n g\n h\n i\n-j\n+J\n"},
	}
	for _, c := range cases {
		if got := UnifiedDiff("f", old, c.new); got != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
	if got := UnifiedDiff("f", "", "x\n"); got != "--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+x\n" {
		t.Errorf("new file:\n%s", got)
	}
}

// The diff must reconstruct both texts and be minimal (its kept lines are
// a longest common subsequence).
func TestLineEditsRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	words := []string{"a", "b", "c", "d"}
	for n := 0; n < 2000; n++ {
		a := make([]string, r.Intn(12))
		b := make([]string, r.Intn(12))
		for i := range a {
			a[i] = words[r.Intn(len(words))]
		}
		for i := range b {
			b[i] = words[r.Intn(len(words))]
		}
		var ra, rb []string
		kept := 0
		for _, e := range lineEdits(a, b) {
			if e.kind != '+' {
				ra = append(ra, e.text)
			}
			if e.kind != '-' {
				rb = append(rb, e.text)
			}
			if e.kind == ' ' {
				kept++
			}
		}
		if strings.Join(ra, ",") != strings.Join(a, ",") || strings.Join(rb, ",") != strings.Join(b, ",") {
			t.Fatalf("%v -> %v: reconstructed %v -> %v", a, b, ra, rb)
		}
		if kept != lcsLen(a, b) {
			t.Fatalf("%v -> %v: kept %d, LCS %d", a, b, kept, lcsLen(a, b))
		}
	}
}

func lcsLen(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if a[0] == b[0] {
		return 1 + lcsLen(a[1:], b[1:])
	}
	return max(lcsLen(a[1:], b), lcsLen(a, b[1:]))
}
