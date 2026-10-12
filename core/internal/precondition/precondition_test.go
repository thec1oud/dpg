package precondition

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

type op struct {
	sql string
	t   pipeline.Target
}

func (o op) SQL() string             { return o.sql }
func (o op) Safety() pipeline.Safety { return pipeline.Safe }
func (o op) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (o op) Mode() pipeline.ExecMode { return pipeline.ModeTransactional }
func (o op) Target() pipeline.Target { return o.t }

func TestGuards(t *testing.T) {
	ops := []pipeline.DiffOp{
		op{"CREATE TABLE u ...", pipeline.Target{Kind: "table", Key: "public.u", Effect: pipeline.EffectCreate}},
		op{"ALTER TABLE u ...", pipeline.Target{Kind: "table", Key: "public.u", Effect: pipeline.EffectCreate}},
		op{"DROP TABLE t2", pipeline.Target{Kind: "table", Key: "public.t2", Effect: pipeline.EffectDrop}},
		op{"DROP VIEW v; CREATE VIEW v", pipeline.Target{Kind: "view", Key: "public.v", Effect: pipeline.EffectRecreate}},
		op{"ALTER TABLE t ADD c", pipeline.Target{Kind: "table", Key: "public.t", Effect: pipeline.EffectAlter}},
		op{"CREATE FUNCTION f()", pipeline.Target{Kind: "function", Key: "public.f()", Effect: pipeline.EffectCreate}},
	}
	g := Guards(ops, map[string]bool{"public.u": true, "public.v": true, "public.f(int)": true}, []string{"o'wner"})
	if len(g) != 4 {
		t.Fatalf("%d guards:\n%s", len(g), strings.Join(g, "\n---\n"))
	}
	if !strings.Contains(g[0], "DPG-E051") || !strings.Contains(g[0], "c.relname = 'u'") {
		t.Errorf("create guard:\n%s", g[0])
	}
	if !strings.Contains(g[1], "DPG-E052") || !strings.Contains(g[1], "drops table public.t2") || !strings.Contains(g[1], "'public.u'") {
		t.Errorf("drop guard:\n%s", g[1])
	}
	if !strings.Contains(g[2], "drops and recreates view public.v") {
		t.Errorf("recreate guard:\n%s", g[2])
	}
	if !strings.Contains(g[3], "pg_has_role(current_user, 'o''wner', 'MEMBER')") {
		t.Errorf("owner guard:\n%s", g[3])
	}
}

func TestManaged(t *testing.T) {
	m := map[string]bool{"public.v": true, "public.f(int)": true}
	for dep, want := range map[string]bool{
		"public.v": true, "public.w": false, "public.f(integer)": true, "public.g(integer)": false,
	} {
		if Managed(dep, m) != want {
			t.Errorf("%s: want %v", dep, want)
		}
	}
}
