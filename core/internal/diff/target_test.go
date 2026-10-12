package diff_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/thec1oud/dpg/internal/blockparser"
	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	"github.com/thec1oud/dpg/internal/pipeline"
	_ "github.com/thec1oud/dpg/internal/scanner"
	"github.com/thec1oud/dpg/internal/snapshot"
)

func compileSrc(t *testing.T, src string) []pipeline.IRObject {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.dpg")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	objs, _, err := compiler.CompileWith([]string{f}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, src)
	}
	return objs
}

// targets diffs after against the snapshot of before and lists each
// object's effect as "kind key effect[ from]", plus any op without one.
func targets(t *testing.T, before, after string) string {
	t.Helper()
	snap := &pipeline.Snapshot{}
	if before != "" {
		if err := snapshot.Populate(snap, compileSrc(t, before)); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := diff.New().Diff(compileSrc(t, after), snap)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var out []string
	for _, op := range ops {
		to, ok := op.(pipeline.TargetedOp)
		if !ok || to.Target().Key == "" {
			out = append(out, "UNTARGETED "+op.SQL())
			continue
		}
		tg := to.Target()
		s := tg.Kind + " " + tg.Key + " " + tg.Effect.String()
		if tg.From != "" {
			s += " from " + tg.From
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func TestOpTargets(t *testing.T) {
	cases := []struct{ name, before, after, want string }{
		{"create", "", "TABLE t (id int);\n",
			"table public.t create"},
		{"alter", "TABLE t (id int);\n", "TABLE t (id int, x int);\n",
			"table public.t alter"},
		{"drop", "TABLE t (id int);\n\nTABLE u (id int);\n", "TABLE t (id int);\n",
			"table public.u drop"},
		{"rename", "TABLE t (id int);\n", "TABLE t2 (id int) { RENAMED FROM t; }\n",
			"table public.t2 rename from public.t"},
		{"new schema", "", "SCHEMA s { }\n\nTABLE s.t (id int);\n",
			"schema s create\ntable s.t create"},
		{"view recreated", "TABLE t (a int, b int);\n\nVIEW v AS SELECT a, b FROM t;\n",
			"TABLE t (a int, b int);\n\nVIEW v AS SELECT b FROM t;\n",
			"view public.v recreate"},
		{"view replaced in place", "TABLE t (a int, b int);\n\nVIEW v AS SELECT a FROM t;\n",
			"TABLE t (a int, b int);\n\nVIEW v AS SELECT a FROM t WHERE b > 0;\n",
			"view public.v alter"},
		{"function return type", "FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;\n",
			"FUNCTION f() RETURNS bigint LANGUAGE sql AS $$ SELECT 1 $$;\n",
			"function public.f() recreate"},
		{"partition removed is not a recreate of the parent",
			"TABLE p (id int) PARTITION BY LIST (id) {\n    PARTITIONS {\n        p1 FOR VALUES IN (1);\n        p2 FOR VALUES IN (2);\n    }\n}\n",
			"TABLE p (id int) PARTITION BY LIST (id) {\n    PARTITIONS {\n        p1 FOR VALUES IN (1);\n    }\n}\n",
			"table public.p alter"},
		{"default privileges", "",
			"DEFAULT PRIVILEGES FOR ROLE app IN SCHEMA public {\n    GRANTS { SELECT ON TABLES TO ro; }\n}\n",
			"default_privileges DEFAULT PRIVILEGES FOR app IN public TABLES create"},
		{"enum value", "TYPE mood AS ENUM ('a');\n\nTABLE t (m mood DEFAULT 'b');\n",
			"TYPE mood AS ENUM ('a', 'b');\n\nTABLE t (m mood DEFAULT 'b');\n",
			"type public.mood alter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := targets(t, c.before, c.after); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}
