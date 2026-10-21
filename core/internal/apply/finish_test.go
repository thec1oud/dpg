package apply

import "testing"

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
