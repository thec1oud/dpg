package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/resolve"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

// checkFits asserts the view is at most w columns wide and h lines high.
func checkFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Errorf("%d lines at height %d", len(lines), h)
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > w {
			t.Errorf("line %d is %d wide at width %d: %q", i, ansi.StringWidth(l), w, l)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestViewGolden(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []string
		at   string
	}{
		{"list-100x30", 100, 30, nil, ""},
		{"list-80x24-chosen", 80, 24, []string{"r"}, "connection_limit"},
		{"list-80x24-required", 80, 24, []string{"a"}, "table cron.run"},
		{"list-80x24-acknowledged", 80, 24, nil, "comment"},
		{"list-60x14-scrolled", 60, 14, []string{"end"}, ""},
		{"list-40x10", 40, 10, nil, ""},
		{"list-20x5", 20, 5, nil, ""},
		{"help-80x24", 80, 24, []string{"?"}, ""},
		{"filter-80x24", 80, 24, []string{"/", "c", "r", "o", "n"}, ""},
		{"preview-80x24", 80, 24, []string{"a", "p"}, "connection_limit"},
		{"reason-80x24", 80, 24, []string{"n", "p"}, "connection_limit"},
		{"quit-80x24", 80, 24, []string{"a", "q"}, "connection_limit"},
		{"macro-80x24", 80, 24, []string{"a", "m"}, "column:updated_at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDriver(t, sample(), c.w, c.h)
			if c.at != "" {
				d.goTo(c.at)
			}
			d.press(c.keys...)
			v := d.m.View()
			checkFits(t, v, c.w, c.h)
			golden(t, c.name, v)
		})
	}
}

func TestViewEmptyAndOdd(t *testing.T) {
	long := strings.Repeat("very_long_name_", 20)
	e := &fakeEngine{targets: []*resolve.Target{
		target("prod", "app"),
		target("prod", "δοκιμή",
			item(drift.Drift, "table", "public.\"Ünïcödé 表\"", "column:名前", "changed"),
			item(drift.Unmanaged, "table", "public."+long, "", "extra"),
			item(drift.Drift, "table", "public.t", "column:"+long, "changed"),
		),
	}}
	for _, size := range [][2]int{{80, 24}, {30, 8}, {12, 4}, {1, 1}, {200, 3}} {
		d := newDriver(t, e, size[0], size[1])
		for _, k := range []string{"", "end", "a", "?", "x", "p"} {
			if k != "" {
				d.press(k)
			}
			checkFits(t, d.m.View(), size[0], size[1])
		}
	}
	d := newDriver(t, &fakeEngine{}, 80, 24)
	if v := d.m.View(); !strings.Contains(v, "No drift") {
		t.Errorf("no targets:\n%s", v)
	}
	d = newDriver(t, e, 80, 24)
	golden(t, "unicode-80x24", d.m.View())
}
