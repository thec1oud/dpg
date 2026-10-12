package ignore_test

import (
	"testing"

	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func TestMatch(t *testing.T) {
	r := ignore.New(map[string][]string{
		"role":     {"rds_*", "rdsadmin"},
		"schema":   {"cron"},
		"function": {"public.pgaudit_*", "helper?"},
	})
	cases := []struct {
		kind, schema, name string
		want               bool
	}{
		{"role", "", "rds_superuser", true},
		{"role", "", "rdsadmin", true},
		{"role", "", "app", false},
		{"schema", "", "cron", true},
		{"table", "cron", "job", true}, // inside an ignored schema
		{"function", "cron", "schedule", true},
		{"table", "public", "job", false},
		{"function", "public", "pgaudit_log", true},
		{"function", "audit", "pgaudit_log", false}, // qualified pattern: public only
		{"function", "audit", "helper1", true},      // unqualified: any schema
		{"function", "audit", "helper12", false},
		{"view", "public", "rds_x", false}, // patterns apply to their own kind
	}
	for _, c := range cases {
		_, got := r.Match(c.kind, c.schema, c.name)
		if got != c.want {
			t.Errorf("Match(%q, %q, %q) = %v, want %v", c.kind, c.schema, c.name, got, c.want)
		}
	}
	var none *ignore.Rules
	if _, ok := none.Match("role", "", "x"); ok {
		t.Error("nil Rules must ignore nothing")
	}
}

func TestFilterSnapshotAndObjects(t *testing.T) {
	r := ignore.New(map[string][]string{"role": {"rds_*"}, "schema": {"cron"}})
	snap := &pipeline.Snapshot{}
	_ = snap.SetObject("rds_superuser", &snapshot.SnapObject{Kind: "role", Role: &snapshot.SnapRole{Name: "rds_superuser"}})
	_ = snap.SetObject("app", &snapshot.SnapObject{Kind: "role", Role: &snapshot.SnapRole{Name: "app"}})
	_ = snap.SetObject("cron.job", &snapshot.SnapObject{Kind: "table", Table: &snapshot.SnapTable{Schema: "cron", Name: "job"}})
	if err := r.FilterSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Objects) != 1 || snap.Objects["app"] == nil {
		t.Errorf("expected only app to remain, got %v", snap.Objects)
	}

	objs := []pipeline.IRObject{
		&ir.Role{Name: "rds_superuser"},
		&ir.Role{Name: "app"},
		&ir.Table{Schema: "cron", Name: "job"},
		&ir.Table{Schema: "public", Name: "job"},
	}
	got := r.FilterObjects(objs)
	if len(got) != 2 || got[0].QualifiedName() != "app" || got[1].QualifiedName() != "public.job" {
		var names []string
		for _, o := range got {
			names = append(names, o.QualifiedName())
		}
		t.Errorf("FilterObjects kept %v", names)
	}
}

func TestCheckDeclared(t *testing.T) {
	r := ignore.New(map[string][]string{"role": {"rds_*"}})
	err := r.CheckDeclared([]pipeline.IRObject{&ir.Role{Name: "app"}, &ir.Role{Name: "rds_custom"}})
	if err == nil {
		t.Fatal("expected DPG-E045 for a declared ignored role")
	}
	diags, ok := err.(pipeline.Diagnostics)
	if !ok || len(diags) != 1 || diags[0].Code != "DPG-E045" {
		t.Errorf("expected one DPG-E045, got %v", err)
	}
	if err := r.CheckDeclared([]pipeline.IRObject{&ir.Role{Name: "app"}}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
