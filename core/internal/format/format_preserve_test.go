package format_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/format"
)

// preserveCases are sources whose comments or Part 1 clauses dpg fmt used to
// drop, or to corrupt by writing '{' or ';' inside a trailing line comment.
var preserveCases = map[string]string{
	"comment after semicolon": `ROLE a NOLOGIN; -- after semicolon
ROLE b NOLOGIN;`,
	"comment after closing brace": `ROLE a NOLOGIN {
    COMMENT 'a';
} -- after brace
ROLE b NOLOGIN;`,
	"comment at end of file": `ROLE a NOLOGIN;
-- end of file`,
	"comment ends part 1, block": `ROLE a NOLOGIN -- ends part 1
{
    COMMENT 'a';
}`,
	"comment on own line before block": `ROLE a NOLOGIN
-- own line before block
{
    COMMENT 'a';
}`,
	"comment ends part 1, no block": `ROLE a NOLOGIN -- ends part 1
;`,
	"comment ends view query": `VIEW v AS SELECT 1 AS a -- ends query
{
    GRANTS {
        SELECT TO PUBLIC;
    }
}`,
	"comment after table column list": `TABLE t (
    id int
) -- after column list
{
    COMMENT 't';
}`,
	"comment after column comma": `TABLE t (
    id int, -- the id
    b  int
);`,
	"comment ends constraint that gets reordered": `TABLE t (
    id int,
    CONSTRAINT z_chk CHECK (id > 0), -- z
    CONSTRAINT a_chk CHECK (id < 9) -- a
);`,
	"parenthesis inside a column comment": `TABLE t (
    id int, -- (pk
    b  int
);`,
	"comment at end of schema block": `SCHEMA s {
    ROLE a NOLOGIN;
    -- last in schema
}`,
	"comment among schema directives, no nested objects": `SCHEMA s {
    -- about s
    COMMENT 'schema s';
}`,
	"partition by": `TABLE m (
    id int,
    at date
) PARTITION BY RANGE (at);`,
	"partition of": `TABLE m_2026 PARTITION OF m FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');`,
	"inherits, with, tablespace": `TABLE child (
    extra text
) INHERITS (m) WITH (fillfactor = 70) TABLESPACE pg_default;`,
	"comment between column list and partition by": `TABLE m (
    id int
) -- partitioned
PARTITION BY HASH (id);`,
}

func TestFormat_PreservesCommentsAndClauses(t *testing.T) {
	opts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	for name, src := range preserveCases {
		t.Run(name, func(t *testing.T) {
			if _, err := format.Parse("in.dpg", []byte(src)); err != nil {
				t.Fatalf("test source does not parse: %v", err)
			}
			first, err := format.Format("in.dpg", []byte(src), opts)
			if err != nil {
				t.Fatalf("format: %v", err)
			}
			out := string(first)
			if _, err := format.Parse("out.dpg", first); err != nil {
				t.Fatalf("formatted output does not parse: %v\n%s", err, out)
			}
			second, err := format.Format("out.dpg", first, opts)
			if err != nil || string(second) != out {
				t.Errorf("not idempotent (err %v):\nfirst:\n%s\nsecond:\n%s", err, out, second)
			}
			if got, want := tokenTexts(out, true), tokenTexts(src, true); !equalStrings(got, want) {
				t.Errorf("comments changed:\nwant %q\ngot  %q\noutput:\n%s", want, got, out)
			}
			if got, want := tokenTexts(out, false), tokenTexts(src, false); !equalStrings(got, want) {
				t.Errorf("code tokens changed:\nwant %q\ngot  %q\noutput:\n%s", want, got, out)
			}
		})
	}
}

// TestFormat_CommentEndingPart1MovesBeforeDeclaration pins where such a
// comment goes: before the declaration, never after the '{' or ';'.
func TestFormat_CommentEndingPart1MovesBeforeDeclaration(t *testing.T) {
	out := formatSrc(t, "ROLE a NOLOGIN -- note\n{\n    COMMENT 'a';\n}", defaultOpts)
	if !strings.HasPrefix(out, "-- note\nROLE a NOLOGIN {") {
		t.Errorf("got:\n%s", out)
	}
}

// TestFormat_CommentAfterTerminatorStaysOnLine pins a comment after ';' on
// the same line staying there.
func TestFormat_CommentAfterTerminatorStaysOnLine(t *testing.T) {
	out := formatSrc(t, "ROLE a NOLOGIN; -- note\nROLE b NOLOGIN;", defaultOpts)
	if !strings.Contains(out, "ROLE a NOLOGIN; -- note\n") {
		t.Errorf("got:\n%s", out)
	}
}

// tokenTexts returns the sorted comment texts (comments true) or upper-cased
// code tokens (comments false) of src. Sorting makes the comparison
// insensitive to fmt's reordering of columns and block directives.
func tokenTexts(src string, comments bool) []string {
	var out []string
	for _, tok := range format.Lex("", []byte(src)) {
		isComment := tok.Type == format.TokLineComment || tok.Type == format.TokBlockComment
		switch {
		case tok.Type == format.TokWhitespace, tok.Type == format.TokNewline, tok.Type == format.TokEOF:
		case comments && isComment:
			out = append(out, strings.TrimSpace(tok.Text))
		case !comments && !isComment:
			out = append(out, strings.ToUpper(tok.Text))
		}
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
