package main

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// TestMinPGVersionCoverage guards RFC Section 3.8: each declared construct
// with a Min PG Version in Appendix F warns when the floor is one version
// below it and is silent at its own version. Compiled from real source so
// the IR fields the rule reads are proven to be populated.
func TestMinPGVersionCoverage(t *testing.T) {
	l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter)
	if !ok {
		t.Fatal("Linter not registered")
	}
	cases := []struct {
		src       string
		construct string
		version   int
	}{
		{`TABLE t (a int, b int GENERATED ALWAYS AS (a * 2) VIRTUAL);`, "VIRTUAL generated column", 18},
		{`TABLE t (a int, CONSTRAINT nn NOT NULL a);`, "NOT NULL constraint", 18},
		{`TABLE t (a int NOT NULL NO INHERIT);`, "NOT NULL constraint", 18},
		{`TABLE t (a int, CONSTRAINT ck CHECK (a > 0) NOT ENFORCED);`, "NOT ENFORCED", 18},
		{`TABLE t (id int, valid daterange, PRIMARY KEY (id, valid WITHOUT OVERLAPS));`, "temporal key", 18},
		{`TABLE p (tn int, id int, PRIMARY KEY (tn, id));
TABLE c (tn int, pid int, FOREIGN KEY (tn, pid) REFERENCES p (tn, id) ON DELETE SET NULL (pid));`, "ON DELETE SET NULL/SET DEFAULT column list", 15},
		{`TABLE t (a int, UNIQUE NULLS NOT DISTINCT (a));`, "UNIQUE NULLS NOT DISTINCT", 15},
		{`TABLE t (a int) { INDEXES { UNIQUE i (a) NULLS NOT DISTINCT; } }`, "UNIQUE index NULLS NOT DISTINCT", 15},
		{`TABLE t (k int, r int4range, EXCLUDE USING gist (k WITH =, r WITH &&)) PARTITION BY RANGE (k);`, "EXCLUDE constraint on a partitioned table", 17},
		{`TABLE t (k int) PARTITION BY RANGE (k) USING heap;`, "USING access method on a partitioned table", 17},
		{`TABLE t (a int) { GRANTS { MAINTAIN TO app; } }`, "MAINTAIN privilege", 17},
		{`UNLOGGED SEQUENCE s;`, "UNLOGGED SEQUENCE", 15},
		{`PUBLICATION p FOR TABLES IN SCHEMA public;`, "FOR TABLES IN SCHEMA", 15},
		{`TABLE t (a int); PUBLICATION p FOR TABLE t (a);`, "publication column list", 15},
		{`TABLE t (a int); PUBLICATION p FOR TABLE t WHERE (a > 0);`, "publication row filter", 15},
		{`PUBLICATION p FOR ALL TABLES WITH (publish_generated_columns = stored);`, "publish_generated_columns", 18},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, two_phase = true);`, "two_phase", 15},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, disable_on_error = true);`, "disable_on_error", 15},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, origin = none);`, "origin", 16},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, run_as_owner = true);`, "run_as_owner", 16},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, password_required = false);`, "password_required", 16},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, streaming = parallel);`, "streaming = parallel", 16},
		{`SUBSCRIPTION s CONNECTION 'host=x' PUBLICATION p WITH (connect = false, failover = true);`, "failover", 17},
		{`COLLATION c (provider = builtin, locale = 'C');`, "PROVIDER builtin", 17},
		{`COLLATION c (provider = icu, locale = 'und', rules = '&a < b');`, "COLLATION RULES", 16},
		{`EVENT TRIGGER on_login ON login EXECUTE FUNCTION f();`, "EVENT TRIGGER ON login", 17},
	}
	for _, c := range cases {
		t.Run(c.construct, func(t *testing.T) {
			file, dir := dpgTempFile(t, c.src)
			desired, _, err := compiler.CompileWith([]string{file}, dir, pipeline.Default, compiler.Options{})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			fired := func(floor int) bool {
				diags, err := l.Lint(desired, pipeline.LinterConfig{MinPGVersion: floor})
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range diags {
					if d.Rule == "min-pg-version" && strings.Contains(d.Message, c.construct) {
						return true
					}
				}
				return false
			}
			if !fired(c.version - 1) {
				t.Errorf("no min-pg-version warning for %q at floor %d", c.construct, c.version-1)
			}
			if fired(c.version) {
				t.Errorf("min-pg-version warning for %q at floor %d", c.construct, c.version)
			}
		})
	}
}
