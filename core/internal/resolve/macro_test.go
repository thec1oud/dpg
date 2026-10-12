package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanutil"
	"github.com/thec1oud/dpg/internal/srcindex"
)

const macroSrc = `MACRO audit (
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz
)

TABLE t (
    id int,
    ...audit
);
`

func TestPartEditMacro(t *testing.T) {
	changed := "created_at timestamptz NOT NULL DEFAULT clock_timestamp()"
	cases := []struct {
		name string
		mode MacroMode
		text *string
		want string
	}{
		{"edit, change", MacroEdit, &changed, `MACRO audit (
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz
)

TABLE t (
    id int,
    ...audit
);
`},
		{"edit, remove", MacroEdit, nil, `MACRO audit (
    updated_at timestamptz
)

TABLE t (
    id int,
    ...audit
);
`},
		{"inline, change", MacroInline, &changed, `MACRO audit (
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz
)

TABLE t (
    id int,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz
);
`},
		{"inline, remove", MacroInline, nil, `MACRO audit (
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz
)

TABLE t (
    id int,
    updated_at timestamptz
);
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			f := filepath.Join(dir, "a.dpg")
			if err := os.WriteFile(f, []byte(macroSrc), 0o644); err != nil {
				t.Fatal(err)
			}
			idx, err := srcindex.Build([]string{f}, dir, pipeline.Default, compiler.Options{})
			if err != nil {
				t.Fatal(err)
			}
			tg := &Target{Index: idx, SourceFiles: []string{f}}
			pl := newPlanner()
			spans := spansWithOrigin(idx, "public.t", "column:created_at", func(string) []byte {
				return scanutil.NormalizeSource([]byte(macroSrc))
			})
			if len(spans) != 1 || spans[0].macro == nil {
				t.Fatalf("spans: %+v", spans)
			}
			if _, err := pl.partEdit(tg, spans[0], c.text, Decision{}); err == nil {
				t.Fatal("without a choice, a macro part must be refused")
			}
			edits, err := pl.partEdit(tg, spans[0], c.text, Decision{Macro: c.mode})
			if err != nil {
				t.Fatal(err)
			}
			files, rejected, err := pl.edited(edits)
			if err != nil || len(rejected) > 0 {
				t.Fatalf("%v %v", rejected, err)
			}
			got := string(files[f])
			if got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
			if err := os.WriteFile(f, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := compiler.CompileWith([]string{f}, dir, pipeline.Default, compiler.Options{}); err != nil {
				t.Errorf("doesn't compile: %v", err)
			}
		})
	}
}
