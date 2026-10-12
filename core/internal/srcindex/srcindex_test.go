package srcindex_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/thec1oud/dpg/internal/blockparser"
	"github.com/thec1oud/dpg/internal/compiler"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	"github.com/thec1oud/dpg/internal/pipeline"
	_ "github.com/thec1oud/dpg/internal/scanner"
	"github.com/thec1oud/dpg/internal/srcindex"
)

const fileA = `MACRO audit (
    created_at timestamptz NOT NULL DEFAULT now()
)

-- the orders table
TABLE orders (
    id     bigint PRIMARY KEY,
    status text NOT NULL DEFAULT 'new',
    ...audit,
    CONSTRAINT status_ok CHECK (status <> ''),
    UNIQUE (status, id)
) WITH (fillfactor = 70) {
    COMMENT 'orders';
    COLUMN status {
        COMMENT 'state';
    }
    INDEXES {
        orders_status_idx (status);
        orders_created_idx (created_at) WHERE (status = 'new');
    }
}

ROLE app LOGIN CONNECTION LIMIT 20 VALID UNTIL '2030-01-01' {
    SET work_mem = '64MB';
    COMMENT 'app';
}

SEQUENCE num_seq INCREMENT BY 2 NO MAXVALUE OWNED BY orders.id;

VIEW recent AS SELECT id FROM orders WHERE status = 'new' {
    COMMENT 'recent';
}

FUNCTION one() RETURNS int LANGUAGE sql IMMUTABLE COST 5 AS $$ SELECT 1 $$;

TYPE mood AS ENUM ('sad', 'ok');

SCHEMA reporting {
    COMMENT 'reports';
    OWNER app;
}
`

const fileB = `TABLE orders (
    note text
) {
    GRANTS {
        SELECT TO app;
        INSERT, UPDATE TO app;
    }
}
`

// TestIndexSpans: every recorded span covers exactly the text that declares
// the property, across files and through a macro spread.
func TestIndexSpans(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.dpg"), filepath.Join(dir, "b.dpg")
	for p, c := range map[string]string{a: fileA, b: fileB} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := srcindex.Build([]string{a, b}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	src := map[string]string{a: fileA, b: fileB}
	text := func(sp pipeline.Span) string { return src[sp.File][sp.Start:sp.End] }
	expect := func(key, prop string, want ...string) {
		t.Helper()
		var got []string
		for _, sp := range idx.Spans(key, prop) {
			got = append(got, text(sp))
		}
		if strings.Join(got, "\n---\n") != strings.Join(want, "\n---\n") {
			t.Errorf("%s %s:\n got  %q\n want %q", key, prop, got, want)
		}
	}

	if n := len(idx.Decls["public.orders"]); n != 2 {
		t.Fatalf("orders: %d declarations, want 2", n)
	}
	expect("public.orders", "column:id", "id     bigint PRIMARY KEY")
	// The column's definition, then its COLUMN block.
	expect("public.orders", "column:status", "status text NOT NULL DEFAULT 'new'", "COLUMN status {\n        COMMENT 'state';\n    }")
	expect("public.orders", "column:created_at", "...audit")
	expect("public.orders", "constraint:status_ok", "CONSTRAINT status_ok CHECK (status <> '')")
	expect("public.orders", "constraint", "UNIQUE (status, id)")
	expect("public.orders", "storage_params", "WITH (fillfactor = 70)")
	expect("public.orders", "comment", "COMMENT 'orders';")
	expect("public.orders", "column:status/comment", "COMMENT 'state';")
	expect("public.orders", "index:orders_status_idx", "orders_status_idx (status);")
	expect("public.orders", "index:orders_created_idx", "orders_created_idx (created_at) WHERE (status = 'new');")
	expect("public.orders", "grants", "SELECT TO app;", "INSERT, UPDATE TO app;")
	expect("public.orders", "column:note", "note text")

	expect("app", "can_login", "LOGIN")
	expect("app", "connection_limit", "CONNECTION LIMIT 20")
	expect("app", "valid_until", "VALID UNTIL '2030-01-01'")
	expect("app", "configs", "SET work_mem = '64MB';")
	expect("app", "comment", "COMMENT 'app';")

	expect("public.num_seq", "increment_by", "INCREMENT BY 2")
	expect("public.num_seq", "max_value", "NO MAXVALUE")
	expect("public.num_seq", "owned_by", "OWNED BY orders.id")

	expect("public.recent", "query", "SELECT id FROM orders WHERE status = 'new'")
	expect("public.one()", "body_hash", "AS $$ SELECT 1 $$")
	expect("public.one()", "return_type", "RETURNS int")
	expect("public.one()", "language", "LANGUAGE sql")
	expect("public.one()", "volatility", "IMMUTABLE")
	expect("public.one()", "cost", "COST 5")
	expect("public.mood", "values", "('sad', 'ok')")
	expect("reporting", "comment", "COMMENT 'reports';")
	expect("reporting", "owner", "OWNER app;")

	// A span from a macro spread names the macro.
	for _, sp := range idx.Spans("public.orders", "column:created_at") {
		if sp.Macro != "audit" {
			t.Errorf("column:created_at: Macro = %q, want audit", sp.Macro)
		}
	}
	// Line and column of a span's start.
	for _, sp := range idx.Spans("public.orders", "index:orders_status_idx") {
		if sp.Line != 18 || sp.Col != 9 {
			t.Errorf("orders_status_idx at %d:%d, want 18:9", sp.Line, sp.Col)
		}
	}
	// Every declaration's spans lie inside it.
	var bad []string
	for key, ds := range idx.Decls {
		for _, d := range ds {
			for prop, sps := range d.Items {
				for _, sp := range sps {
					if sp.File != d.Decl.File || sp.Start < d.Decl.Start || sp.End > d.Decl.End {
						bad = append(bad, key+" "+prop)
					}
				}
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("spans outside their declaration: %v", bad)
	}
}
