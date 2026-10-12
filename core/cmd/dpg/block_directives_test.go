package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// TestUnknownBlockDirective guards RFC Section 4.2.2 and DPG-E024: a
// directive an object kind doesn't accept is an error, not silently
// ignored, while each kind's own directives compile.
func TestUnknownBlockDirective(t *testing.T) {
	compile := func(src string) error {
		file, dir := dpgTempFile(t, src)
		_, _, err := compiler.CompileWith([]string{file}, dir, pipeline.Default, compiler.Options{})
		return err
	}
	rejected := []struct{ src, want string }{
		{`VIEW v AS SELECT 1 AS x { PROTECTED; }`, "PROTECTED is not accepted in a { } block of VIEW"},
		{`FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$ { DROP CASCADE; }`, "DROP CASCADE is not accepted in a { } block of FUNCTION"},
		{`SEQUENCE s { DEPRECATED 'old'; }`, "DEPRECATED is not accepted in a { } block of SEQUENCE"},
		{`EXTENSION pgcrypto { OWNER app; }`, "OWNER is not accepted in a { } block of EXTENSION"},
		{`TYPE mood AS ENUM ('a') { ENABLE ROW LEVEL SECURITY; }`, "ENABLE ROW LEVEL SECURITY is not accepted"},
		{`COLLATION c (provider = icu, locale = 'und') { GRANTS { USAGE TO app; } }`, "GRANTS is not accepted in a { } block of COLLATION"},
	}
	for _, c := range rejected {
		err := compile(c.src)
		diags, _ := err.(pipeline.Diagnostics)
		var ce *pipeline.CompilerError
		if len(diags) == 1 {
			ce = diags[0]
		} else {
			errors.As(err, &ce)
		}
		if ce == nil || ce.Code != "DPG-E024" || !strings.Contains(ce.Message, c.want) {
			t.Errorf("%s: got %v, want DPG-E024 %q", c.src, err, c.want)
		}
	}
	accepted := []string{
		`TABLE t (id int PRIMARY KEY, a int) {
    OWNER app; COMMENT 't'; PROTECTED; DEPRECATED 'x'; DROP CASCADE;
    ENABLE ROW LEVEL SECURITY; FORCE ROW LEVEL SECURITY;
    REPLICA IDENTITY FULL;
    COLUMN a { COMMENT 'a'; STATISTICS 100; }
    INDEXES { i (a); }
    GRANTS { SELECT TO app; }
}`,
		`MATERIALIZED VIEW mv AS SELECT 1 AS x { OWNER app; DEPRECATED 'x'; INDEXES { mi (x); } }`,
		`SEQUENCE s { OWNER app; COMMENT 's'; RENAMED FROM s0; GRANTS { USAGE TO app; } }`,
		`TYPE mood AS ENUM ('a', 'b') { OWNER app; DEPRECATED 'x'; VALUE 'b' RENAMED FROM 'c'; }`,
		`TEXT SEARCH CONFIGURATION cfg (PARSER = default) { OWNER app; RENAMED FROM cfg0; }`,
		`EXTENSION pgcrypto { COMMENT 'crypto'; }`,
	}
	for _, src := range accepted {
		if err := compile(src); err != nil {
			t.Errorf("%s: unexpected error %v", src, err)
		}
	}
}
