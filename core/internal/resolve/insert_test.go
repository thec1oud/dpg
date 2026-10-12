package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/thec1oud/dpg/internal/blockparser"
	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/drift"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	"github.com/thec1oud/dpg/internal/pipeline"
	_ "github.com/thec1oud/dpg/internal/scanner"
	"github.com/thec1oud/dpg/internal/srcindex"
)

func newPlanner() *planner {
	return &planner{
		plan:  &Plan{},
		files: map[string]*FileChange{},
		snaps: map[*Target]*SnapshotChange{},
		acks:  map[*Target]*AckChange{},
		migs:  map[*Target]*MigrationChange{},
	}
}

// insertInto inserts parts into object's declaration in src, each as its
// own adoption, and returns the edited text; the result must compile.
func insertInto(t *testing.T, src, object string, parts ...originSpan) string {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.dpg")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := srcindex.Build([]string{f}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tg := &Target{Index: idx}
	pl := newPlanner()
	var edits []textEdit
	for i, p := range parts {
		es, err := pl.insertPart(tg, drift.Item{Object: object}, Decision{}, p)
		if err != nil {
			t.Fatalf("insert %q: %v", p.text, err)
		}
		for j := range es {
			es[j].seq = i
		}
		edits = append(edits, es...)
	}
	files, rejected, err := pl.edited(edits)
	if err != nil || len(rejected) > 0 {
		t.Fatalf("edited: %v %v", rejected, err)
	}
	out := string(files[f])
	if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := compiler.CompileWith([]string{f}, dir, pipeline.Default, compiler.Options{}); err != nil {
		t.Fatalf("the result doesn't compile: %v\n%s", err, out)
	}
	return out
}

func TestInsertPart(t *testing.T) {
	col := func(s string) originSpan { return originSpan{origin: "part1", text: s} }
	blk := func(s string, groups ...string) originSpan {
		return originSpan{origin: "block", text: s, groups: groups}
	}
	cases := []struct {
		name, src string
		parts     []originSpan
		want      string
	}{
		{"one-line list", "TABLE s (id int);\n", []originSpan{col("note text")},
			"TABLE s (id int, note text);\n"},
		{"empty list", "TABLE e ();\n", []originSpan{col("x int")},
			"TABLE e (x int);\n"},
		{"multi-line list", "TABLE t (\n    id int,\n    name text\n);\n", []originSpan{col("a int"), col("b int")},
			"TABLE t (\n    id int,\n    name text,\n    a int,\n    b int\n);\n"},
		{"trailing comment", "TABLE t (\n    id int, -- key\n    name text -- shown\n);\n", []originSpan{col("a int"), col("b int")},
			"TABLE t (\n    id int, -- key\n    name text, -- shown\n    a int,\n    b int\n);\n"},
		{"table clause", "TABLE t (id int);\n", []originSpan{col("WITH (fillfactor = 70)")},
			"TABLE t (id int) WITH (fillfactor = 70);\n"},
		{"role option", "ROLE app LOGIN;\n", []originSpan{col("CONNECTION LIMIT 5")},
			"ROLE app LOGIN CONNECTION LIMIT 5;\n"},
		{"new block", "TABLE t (id int);\n", []originSpan{blk("COMMENT 'c';")},
			"TABLE t (id int) {\n    COMMENT 'c';\n};\n"},
		{"empty block", "TABLE t (id int) {\n}\n", []originSpan{blk("COMMENT 'c';")},
			"TABLE t (id int) {\n    COMMENT 'c';\n}\n"},
		{"one-line block", "TABLE t (id int) { COMMENT 'c'; }\n", []originSpan{blk("OWNER app;")},
			"TABLE t (id int) { COMMENT 'c'; OWNER app; }\n"},
		{"into a group", "TABLE t (id int) {\n    INDEXES {\n        t_a (id);\n    }\n}\n", []originSpan{blk("t_b (id);", "INDEXES")},
			"TABLE t (id int) {\n    INDEXES {\n        t_a (id);\n        t_b (id);\n    }\n}\n"},
		{"new group", "TABLE t (id int) {\n    COMMENT 'c';\n}\n", []originSpan{blk("t_b (id);", "INDEXES")},
			"TABLE t (id int) {\n    COMMENT 'c';\n    INDEXES {\n        t_b (id);\n    }\n}\n"},
		{"nested groups", "TABLE t (id int);\n", []originSpan{blk("COMMENT 'k';", "COLUMNS", "id")},
			"TABLE t (id int) {\n    COLUMNS {\n        id {\n            COMMENT 'k';\n        }\n    }\n};\n"},
		{"singular column block", "TABLE t (id int) {\n    COLUMN id {\n        COMMENT 'k';\n    }\n}\n", []originSpan{blk("STATISTICS 100;", "COLUMNS", "id")},
			"TABLE t (id int) {\n    COLUMN id {\n        COMMENT 'k';\n        STATISTICS 100;\n    }\n}\n"},
		{"group entry by name, not modifier", "TABLE t (id int) {\n    INDEXES {\n        UNIQUE t_a (id) {\n            COMMENT 'a';\n        }\n    }\n}\n",
			[]originSpan{blk("COMMENT 'b';", "INDEXES", "UNIQUE t_b (id)")},
			"TABLE t (id int) {\n    INDEXES {\n        UNIQUE t_a (id) {\n            COMMENT 'a';\n        }\n        UNIQUE t_b (id) {\n            COMMENT 'b';\n        }\n    }\n}\n"},
		{"CRLF", "TABLE s (\r\n    id int\r\n);\r\n", []originSpan{col("note text")},
			"TABLE s (\r\n    id int,\r\n    note text\r\n);\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			object := "public.t"
			switch {
			case c.src[:6] == "TABLE " && c.src[6] != 't':
				object = "public." + string(c.src[6])
			case c.src[:5] == "ROLE ":
				object = "app"
			}
			if got := insertInto(t, c.src, object, c.parts...); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

func TestEnclosingGroups(t *testing.T) {
	text := []byte("{\n    COMMENT 'x';\n    INDEXES {\n        a (x);\n    }\n    COLUMN email {\n        COMMENT 'y';\n    }\n}")
	at := func(s string) int {
		for i := range text {
			if string(text[i:i+len(s)]) == s {
				return i
			}
		}
		t.Fatalf("%q not found", s)
		return 0
	}
	cases := map[string]string{"COMMENT 'x'": "", "a (x)": "INDEXES", "COMMENT 'y'": "COLUMNS/email"}
	for s, want := range cases {
		got := ""
		for i, g := range enclosingGroups(text, 1, at(s)) {
			if i > 0 {
				got += "/"
			}
			got += g
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", s, got, want)
		}
	}
}

func TestRemovalRangeEmptiesGroups(t *testing.T) {
	cases := []struct{ src, entry, want string }{
		{"TABLE t (id int) {\n    COMMENT 'c';\n    REVOKES {\n        DELETE FROM aud;\n    }\n}\n", "DELETE FROM aud;",
			"TABLE t (id int) {\n    COMMENT 'c';\n}\n"},
		{"TABLE t (id int) {\n    INDEXES {\n        a (id);\n        b (id);\n    }\n}\n", "a (id);",
			"TABLE t (id int) {\n    INDEXES {\n        b (id);\n    }\n}\n"},
		{"TABLE t (id int) {\n    COMMENT 'c';\n    COLUMNS {\n        id {\n            GRANTS {\n                SELECT TO a;\n            }\n        }\n    };\n}\n", "SELECT TO a;",
			"TABLE t (id int) {\n    COMMENT 'c';\n}\n"},
		{"TABLE t (id int) {\n    COMMENT 'c';\n    OWNER x;\n}\n", "OWNER x;",
			"TABLE t (id int) {\n    COMMENT 'c';\n}\n"},
	}
	for _, c := range cases {
		text := []byte(c.src)
		i := strings.Index(c.src, c.entry)
		block := strings.Index(c.src, "{") + 1
		s, e := removalRange(text, originSpan{span: pipeline.Span{Start: i, End: i + len(c.entry)}, origin: "block", blockStart: block})
		if got := c.src[:s] + c.src[e:]; got != c.want {
			t.Errorf("removing %q:\n%s\nwant:\n%s", c.entry, got, c.want)
		}
	}
}
