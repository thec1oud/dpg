package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
)

func TestAddIgnorePattern(t *testing.T) {
	base := "kind = \"cluster\"\n\n[cluster]\nname = \"prod\" # main\n"
	cases := []struct {
		name, in, key, pattern string
		want                   []string // patterns the key must hold afterwards
	}{
		{"no table", base, "roles", "rdsadmin", []string{"rdsadmin"}},
		{"table without the key", base + "\n[ignore]\nschemas = [\"cron\"]\n", "roles", "rds_*", []string{"rds_*"}},
		{"one-line array", base + "\n[ignore]\nroles = [\"a\"] # note\n", "roles", "b", []string{"a", "b"}},
		{"empty array", base + "\n[ignore]\nroles = []\n", "roles", "b", []string{"b"}},
		{"multi-line array", base + "\n[ignore]\nroles = [\n    \"a\",\n    \"b\" # last\n]\n", "roles", "c", []string{"a", "b", "c"}},
		{"already present", base + "\n[ignore]\nroles = [\"a\"]\n", "roles", "a", []string{"a"}},
		{"table followed by another", base + "\n[ignore]\nroles = [\"a\"]\n\n[compiler]\nmin_pg_version = 16\n", "tables", "public.t", []string{"public.t"}},
		{"quote in a pattern", base, "roles", `we"ird`, []string{`we"ird`}},
	}
	for _, c := range cases {
		out, err := addIgnorePattern(c.in, c.key, c.pattern)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		path := filepath.Join(t.TempDir(), "dpg.toml")
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadCluster(path)
		if err != nil {
			t.Errorf("%s: result doesn't load: %v\n%s", c.name, err, out)
			continue
		}
		var got []string
		switch c.key {
		case "roles":
			got = cfg.Ignore.Roles
		case "tables":
			got = cfg.Ignore.Tables
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %q, want %q\n%s", c.name, got, c.want, out)
		}
		if !strings.Contains(out, "name = \"prod\" # main") {
			t.Errorf("%s: existing lines must be kept:\n%s", c.name, out)
		}
	}
}

func TestIgnorePattern(t *testing.T) {
	for _, c := range [][4]string{
		{"role", "rdsadmin", "roles", "rdsadmin"},
		{"table", "cron.job", "tables", "cron.job"},
		{"function", "public.f(integer, text)", "functions", "public.f"},
		{"operator_class", "public.ops USING btree", "operator_classes", "public.ops"},
	} {
		k, p, ok := IgnorePattern(c[0], c[1])
		if !ok || k != c[2] || p != c[3] {
			t.Errorf("IgnorePattern(%q, %q) = %q, %q, %v", c[0], c[1], k, p, ok)
		}
	}
}
