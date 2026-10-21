package apply

import (
	"errors"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func TestTheIndexBuildsToPrepareAreTheDiffersConcurrentOnes(t *testing.T) {
	for _, c := range []struct{ sql, name, table string }{
		{`CREATE INDEX CONCURRENTLY "t_x" ON "public"."t" ("x");`, `"t_x"`, `"public"."t"`},
		{`CREATE UNIQUE INDEX CONCURRENTLY "a""b" ON ONLY "s"."p" USING gin ("x");`, `"a""b"`, `"s"."p"`},
		{`CREATE INDEX CONCURRENTLY "i" ON "t" ("x");`, `"i"`, `"t"`},
		{`CREATE INDEX "t_x" ON "public"."t" ("x");`, "", ""},
		{`DROP INDEX CONCURRENTLY "t_x";`, "", ""},
		{`GRANT SELECT ON "t" TO "r";`, "", ""},
	} {
		m := createIndexConcurrently.FindStringSubmatch(c.sql)
		switch {
		case c.name == "" && m != nil:
			t.Errorf("%s matched %q", c.sql, m)
		case c.name != "" && (m == nil || m[1] != c.name || m[2] != c.table):
			t.Errorf("%s: %q, want %s on %s", c.sql, m, c.name, c.table)
		}
	}
	if got := unquote(`"a""b"`); got != `a"b` {
		t.Errorf("unquote: %q", got)
	}
	for rel, want := range map[string]string{`"public"."t"`: "public.t", `"t"`: "t", `"a.b"."c""d"`: `a.b.c"d`} {
		if got := snapshotKey(rel); got != want {
			t.Errorf("snapshotKey(%s) = %q, want %q", rel, got, want)
		}
	}
	if got := quote(`a"b`); got != `"a""b"` {
		t.Errorf("quote: %q", got)
	}
}

type gateOp struct {
	safety pipeline.Safety
	id     string
}

func (o gateOp) SQL() string             { return "DROP TABLE t;" }
func (o gateOp) Safety() pipeline.Safety { return o.safety }
func (gateOp) Pos() pipeline.SourcePos   { return pipeline.SourcePos{} }
func (gateOp) Mode() pipeline.ExecMode   { return pipeline.ModeTransactional }

type loggedGateOp struct{ gateOp }

func (o loggedGateOp) StepID() string    { return o.id }
func (loggedGateOp) MigrationID() string { return "m" }

func TestGateRefusesWithATypedError(t *testing.T) {
	for _, c := range []struct {
		op   pipeline.DiffOp
		opts Options
		code string
		step string
	}{
		{loggedGateOp{gateOp{pipeline.Destructive, "m/3"}}, Options{}, "DPG-E025: migration m contains destructive operations", "m/3"},
		{gateOp{pipeline.Caution, ""}, Options{RefuseCaution: true}, "migration m contains CAUTION operations", ""},
	} {
		err := Gate([]pipeline.DiffOp{gateOp{pipeline.Safe, ""}, c.op}, c.opts, "migration m")
		var ge *GateError
		if !errors.As(err, &ge) || !strings.HasPrefix(ge.Reason, c.code) || strings.Contains(ge.Reason, "DROP") || ge.Step != c.step || ge.SQL != "DROP TABLE t;" {
			t.Errorf("%#v", ge)
			continue
		}
		if want := ge.Reason + "\n  first: DROP TABLE t;"; err.Error() != want {
			t.Errorf("message %q, want %q", err.Error(), want)
		}
	}
	if err := Gate([]pipeline.DiffOp{gateOp{pipeline.Destructive, ""}}, Options{AllowDestructive: true}, "m"); err != nil {
		t.Errorf("allowed: %v", err)
	}
}
