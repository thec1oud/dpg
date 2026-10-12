//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/srcindex"
	"github.com/thec1oud/dpg/internal/testpg"
)

const detectionFixture = `
ROLE drift_role LOGIN CONNECTION LIMIT 5 {
    COMMENT 'drift role';
    SET work_mem = '64MB';
}

TABLE drift_t (
    id   int PRIMARY KEY,
    name text NOT NULL DEFAULT 'x',
    note text
) {
    COMMENT 'drift table';
    INDEXES {
        drift_t_name_idx (name);
    }
    GRANTS {
        SELECT TO drift_role;
    }
}

VIEW drift_v AS SELECT id FROM drift_t {
    COMMENT 'drift view';
}

FUNCTION drift_f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$ {
    COMMENT 'drift function';
}

SEQUENCE drift_s INCREMENT BY 2;

TYPE drift_e AS ENUM ('a', 'b');
`

// detectionCase changes one thing in the live database and names the items
// verify must then report, as "group id". undo restores the applied state.
type detectionCase struct {
	name   string
	change []string
	want   []string
	undo   []string
}

var detectionCases = []detectionCase{
	{"role connection limit", []string{`ALTER ROLE drift_role CONNECTION LIMIT 9`},
		[]string{"drift role:drift_role/connection_limit"}, []string{`ALTER ROLE drift_role CONNECTION LIMIT 5`}},
	{"role login", []string{`ALTER ROLE drift_role NOLOGIN`},
		[]string{"drift role:drift_role/can_login"}, []string{`ALTER ROLE drift_role LOGIN`}},
	{"role comment", []string{`COMMENT ON ROLE drift_role IS 'changed'`},
		[]string{"drift role:drift_role/comment"}, []string{`COMMENT ON ROLE drift_role IS 'drift role'`}},
	{"role setting", []string{`ALTER ROLE drift_role SET work_mem = '1MB'`},
		[]string{"drift role:drift_role/configs"}, []string{`ALTER ROLE drift_role SET work_mem = '64MB'`}},
	{"undeclared role attribute", []string{`ALTER ROLE drift_role CREATEDB`},
		nil, []string{`ALTER ROLE drift_role NOCREATEDB`}},

	{"column default", []string{`ALTER TABLE drift_t ALTER COLUMN name DROP DEFAULT`},
		[]string{"drift table:public.drift_t/column:name"}, []string{`ALTER TABLE drift_t ALTER COLUMN name SET DEFAULT 'x'`}},
	{"column not null", []string{`ALTER TABLE drift_t ALTER COLUMN note SET NOT NULL`},
		[]string{"drift table:public.drift_t/column:note"}, []string{`ALTER TABLE drift_t ALTER COLUMN note DROP NOT NULL`}},
	{"column type", []string{`ALTER TABLE drift_t ALTER COLUMN note TYPE varchar(10)`},
		[]string{"drift table:public.drift_t/column:note"}, []string{`ALTER TABLE drift_t ALTER COLUMN note TYPE text`}},
	{"extra column", []string{`ALTER TABLE drift_t ADD COLUMN extra int`},
		[]string{"drift table:public.drift_t/column:extra"}, []string{`ALTER TABLE drift_t DROP COLUMN extra`}},
	{"undeclared column storage", []string{`ALTER TABLE drift_t ALTER COLUMN note SET STORAGE EXTERNAL`},
		nil, []string{`ALTER TABLE drift_t ALTER COLUMN note SET STORAGE EXTENDED`}},
	// The declared WITH (...) list is complete (RFC Section 7.11): a
	// parameter set live when none is declared is drift, as dpg plan
	// --live would RESET it.
	{"table storage parameter", []string{`ALTER TABLE drift_t SET (fillfactor = 50)`},
		[]string{"drift table:public.drift_t/storage_params"}, []string{`ALTER TABLE drift_t RESET (fillfactor)`}},
	{"missing index", []string{`DROP INDEX drift_t_name_idx`},
		[]string{"drift table:public.drift_t/index:drift_t_name_idx"}, []string{`CREATE INDEX drift_t_name_idx ON drift_t (name)`}},
	{"extra index", []string{`CREATE INDEX drift_t_note_idx ON drift_t (note)`},
		[]string{"drift table:public.drift_t/index:drift_t_note_idx"}, []string{`DROP INDEX drift_t_note_idx`}},
	{"table comment", []string{`COMMENT ON TABLE drift_t IS 'changed'`},
		[]string{"drift table:public.drift_t/comment"}, []string{`COMMENT ON TABLE drift_t IS 'drift table'`}},
	{"missing grant", []string{`REVOKE SELECT ON drift_t FROM drift_role`},
		[]string{"drift table:public.drift_t/grant:SELECT:drift_role"}, []string{`GRANT SELECT ON drift_t TO drift_role`}},
	{"extra grant", []string{`GRANT INSERT ON drift_t TO drift_role`},
		[]string{"warning table:public.drift_t/grant:INSERT:drift_role"}, []string{`REVOKE INSERT ON drift_t FROM drift_role`}},

	{"view query", []string{`CREATE OR REPLACE VIEW drift_v AS SELECT id, name FROM drift_t`},
		[]string{"drift view:public.drift_v/query"}, []string{`DROP VIEW drift_v`, `CREATE VIEW drift_v AS SELECT id FROM drift_t`, `COMMENT ON VIEW drift_v IS 'drift view'`}},
	{"missing view", []string{`DROP VIEW drift_v`},
		[]string{"drift view:public.drift_v"}, []string{`CREATE VIEW drift_v AS SELECT id FROM drift_t`, `COMMENT ON VIEW drift_v IS 'drift view'`}},
	{"function body", []string{`CREATE OR REPLACE FUNCTION drift_f() RETURNS int LANGUAGE sql AS $$ SELECT 2 $$`},
		[]string{"drift function:public.drift_f()/body_hash"}, []string{`CREATE OR REPLACE FUNCTION drift_f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`}},
	{"sequence increment", []string{`ALTER SEQUENCE drift_s INCREMENT BY 3`},
		[]string{"drift sequence:public.drift_s/increment_by"}, []string{`ALTER SEQUENCE drift_s INCREMENT BY 2`}},

	{"unmanaged table", []string{`CREATE TABLE drift_x (id int)`},
		[]string{"unmanaged table:public.drift_x"}, []string{`DROP TABLE drift_x`}},
	{"undeclared role", []string{`CREATE ROLE drift_other NOLOGIN`},
		[]string{"warning role:drift_other"}, []string{`DROP ROLE drift_other`}},

	// Irreversible (PostgreSQL can't drop an enum value), so it runs last.
	{"enum value added", []string{`ALTER TYPE drift_e ADD VALUE 'c'`},
		[]string{"drift type:public.drift_e/values"}, nil},
}

// TestVerifyDetectsEachChange applies a fixture, then makes one change at a
// time in the live database: verify must report exactly the items the change
// causes, and nothing once the change is undone (RFC Sections 18.3, 20.2).
func TestVerifyDetectsEachChange(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()
	differ, emitter, applyExec, store := diff.New(), emit.New(), executor.New(), newMemStore()
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(detectionFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	source, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	snap, _ := store.Load("test", "dpgtest")
	idx, err := srcindex.Build([]string{f}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	detect := func() []string {
		t.Helper()
		live, err := introspect.New().Introspect(ctx, conn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		report, err := drift.Detect(differ, live, snap, drift.Options{Source: source})
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		// A changed or missing property the source declares explicitly
		// must be traceable to the text that declares it
		// (internal/srcindex), for resolution. One declared only by
		// omission (an empty WITH list) has no text.
		for _, it := range report.Items {
			if it.Group != drift.Drift || it.Property == "" || (it.Change != "changed" && it.Change != "missing") ||
				it.Snapshot == "" || it.Snapshot == `""` {
				continue
			}
			if len(idx.Spans(it.Object, srcindex.PropertyKey(it.Property))) == 0 {
				t.Errorf("%s: no source span for %s", it.ID, srcindex.PropertyKey(it.Property))
			}
		}
		var out []string
		for _, it := range report.Items {
			if it.Group == drift.Warning && it.Kind == "role" && it.ID != "role:drift_other" {
				continue // roles of other tests on this server
			}
			out = append(out, it.Group.String()+" "+it.ID)
		}
		sort.Strings(out)
		return out
	}
	exec := func(stmts []string) {
		t.Helper()
		for _, s := range stmts {
			if _, err := conn.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}

	for _, c := range detectionCases {
		exec(c.change)
		got := detect()
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, want)
		}
		if c.undo == nil {
			continue // irreversible (ADD VALUE); must be the last case touching its object
		}
		exec(c.undo)
		if got := detect(); len(got) != 0 {
			t.Errorf("%s: after undo, got %q", c.name, got)
		}
	}
}

// TestVerifyClassifiesPendingSource: with the source changed since the
// snapshot, a live change to the same property is a conflict, or converged
// when the database already has the source's value; the pending change
// alone is not reported (RFC Section 18.3).
func TestVerifyClassifiesPendingSource(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()
	differ, emitter, applyExec, store := diff.New(), emit.New(), executor.New(), newMemStore()
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(f, []byte(detectionFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)
	snap, _ := store.Load("test", "dpgtest")

	// Pending: the source now says CONNECTION LIMIT 7 and a new column.
	edited := strings.Replace(detectionFixture, "CONNECTION LIMIT 5", "CONNECTION LIMIT 7", 1)
	edited = strings.Replace(edited, "    note text\n", "    note text,\n    added int\n", 1)
	if err := os.WriteFile(f, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	source, _, err := compiler.Compile([]string{f}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	detect := func() string {
		t.Helper()
		live, err := introspect.New().Introspect(ctx, conn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		report, err := drift.Detect(differ, live, snap, drift.Options{Source: source})
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		var out []string
		for _, it := range report.Items {
			if it.Group != drift.Warning {
				out = append(out, it.Group.String()+" "+it.ID)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	exec := func(s string) {
		t.Helper()
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	for _, c := range []struct {
		name, change, want, undo string
	}{
		{"pending only", "", "", ""},
		{"conflict", `ALTER ROLE drift_role CONNECTION LIMIT 9`, "conflict role:drift_role/connection_limit", `ALTER ROLE drift_role CONNECTION LIMIT 5`},
		{"converged", `ALTER ROLE drift_role CONNECTION LIMIT 7`, "converged role:drift_role/connection_limit", `ALTER ROLE drift_role CONNECTION LIMIT 5`},
		{"converged column", `ALTER TABLE drift_t ADD COLUMN added int`, "converged table:public.drift_t/column:added", `ALTER TABLE drift_t DROP COLUMN added`},
		{"plain drift beside a pending change", `COMMENT ON ROLE drift_role IS 'changed'`, "drift role:drift_role/comment", `COMMENT ON ROLE drift_role IS 'drift role'`},
	} {
		if c.change != "" {
			exec(c.change)
		}
		if got := detect(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		if c.undo != "" {
			exec(c.undo)
		}
	}
}
