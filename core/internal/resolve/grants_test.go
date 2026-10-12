package resolve

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/drift"
)

func TestParseGrantEntry(t *testing.T) {
	for _, s := range []string{
		"SELECT TO app;",
		"SELECT, insert TO app, \"Other\" WITH GRANT OPTION;",
		"GRANT ALL PRIVILEGES TO app GRANTED BY owner;",
		"REVOKE UPDATE FROM PUBLIC CASCADE;",
		"ALL TO a",
	} {
		e, err := parseGrantEntry(s)
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		want := s
		if !strings.HasSuffix(want, ";") {
			want += ";"
		}
		if got := e.render(); got != want {
			t.Errorf("round trip: %q -> %q", s, got)
		}
	}
	if _, err := parseGrantEntry("UPDATE (a, b) TO app;"); err == nil {
		t.Error("column lists must be refused")
	}
	if _, err := parseGrantEntry("SELECT app;"); err == nil {
		t.Error("an entry without TO must be refused")
	}
}

func TestGrantEntryWithout(t *testing.T) {
	none := func(string) ([]string, []string) { return nil, nil }
	cases := []struct {
		entry string
		g     drift.GrantRef
		keep  func(string) ([]string, []string)
		want  string // entries joined by " | "; "unchanged"
	}{
		{"SELECT TO app;", drift.GrantRef{Privilege: "SELECT", Role: "app"}, none, ""},
		{"SELECT, INSERT TO app;", drift.GrantRef{Privilege: "INSERT", Role: "app"}, none, "SELECT TO app;"},
		{"SELECT TO app, other;", drift.GrantRef{Privilege: "SELECT", Role: "app"}, none, "SELECT TO other;"},
		{"SELECT, INSERT TO other, app;", drift.GrantRef{Privilege: "SELECT", Role: "app"}, none, "SELECT, INSERT TO other; | INSERT TO app;"},
		{"SELECT TO other;", drift.GrantRef{Privilege: "SELECT", Role: "app"}, none, "unchanged"},
		{"INSERT TO app;", drift.GrantRef{Privilege: "SELECT", Role: "app"}, none, "unchanged"},
		{`SELECT TO "App";`, drift.GrantRef{Privilege: "SELECT", Role: "App"}, none, ""},
		// Only the grant option is missing: the plain privilege stays.
		{"SELECT, INSERT TO app WITH GRANT OPTION GRANTED BY boss;", drift.GrantRef{Privilege: "SELECT", Role: "app", WithGrant: true},
			func(string) ([]string, []string) { return nil, []string{"SELECT"} },
			"INSERT TO app WITH GRANT OPTION GRANTED BY boss; | SELECT TO app GRANTED BY boss;"},
		// A declared ALL becomes what is held.
		{"ALL TO app;", drift.GrantRef{Privilege: "ALL", Role: "app", AllKind: "sequence"},
			func(string) ([]string, []string) { return []string{"SELECT", "USAGE"}, nil }, "SELECT, USAGE TO app;"},
		// One privilege out of a declared ALL.
		{"ALL TO app;", drift.GrantRef{Privilege: "UPDATE", Role: "app", AllKind: "sequence"}, none, "SELECT, USAGE TO app;"},
		{"GRANT SELECT TO app;", drift.GrantRef{Privilege: "SELECT", Role: "app"}, none, ""},
		{"REVOKE SELECT, UPDATE FROM app CASCADE;", drift.GrantRef{Privilege: "UPDATE", Role: "app"}, none, "REVOKE SELECT FROM app CASCADE;"},
	}
	for _, c := range cases {
		e, err := parseGrantEntry(c.entry)
		if err != nil {
			t.Fatal(err)
		}
		out, changed := e.without(&c.g, c.keep)
		var parts []string
		for _, x := range out {
			parts = append(parts, x.render())
		}
		got := strings.Join(parts, " | ")
		if !changed {
			got = "unchanged"
		}
		if got != c.want {
			t.Errorf("%s without %s for %s:\n got %q\nwant %q", c.entry, c.g.Privilege, c.g.Role, got, c.want)
		}
	}
}
