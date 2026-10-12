//go:build integration

package precondition

import (
	"context"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestFingerprintsLive: a fingerprint changes with every part of an
// object's definition, and with nothing else (data, statistics, the
// session's search_path); the guard refuses a changed or unrecorded
// object.
func TestFingerprintsLive(t *testing.T) {
	ctx := context.Background()
	url := testpg.Start(t)
	conn, err := executor.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(stmts ...string) {
		t.Helper()
		for _, s := range stmts {
			if _, err := conn.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}
	exec(`CREATE SCHEMA dpg`, `CREATE TABLE dpg.objects (log text, kind text, key text, fingerprint text NOT NULL,
		pg_major int NOT NULL, migration text NOT NULL, recorded_at timestamptz NOT NULL, PRIMARY KEY (log, kind, key))`,
		`CREATE SCHEMA app`, `CREATE ROLE reader`,
		`CREATE TYPE app.mood AS ENUM ('sad', 'ok')`,
		`CREATE DOMAIN app.pos AS int CHECK (VALUE > 0)`,
		`CREATE TABLE app.t (id int PRIMARY KEY, m app.mood DEFAULT 'ok', n int)`,
		`CREATE INDEX t_n ON app.t (n)`,
		`CREATE VIEW app.v AS SELECT id, m FROM app.t`,
		`CREATE SEQUENCE app.s INCREMENT 2`,
		`CREATE FUNCTION app.f(x app.pos) RETURNS int LANGUAGE sql AS 'SELECT x + 1'`,
		`CREATE EXTENSION IF NOT EXISTS plpgsql`)
	f := Fingerprints{Schema: "dpg", Log: "app"}
	objects := [][2]string{{"table", "app.t"}, {"view", "app.v"}, {"sequence", "app.s"}, {"function", "app.f(app.pos)"},
		{"type", "app.mood"}, {"type", "app.pos"}, {"schema", "app"}, {"role", "reader"}, {"extension", "plpgsql"}}
	read := func(kind, key string) string {
		t.Helper()
		exec(f.Record(kind, key, "m"))
		rows, err := conn.QueryRows(ctx, `SELECT fingerprint FROM dpg.objects WHERE kind = $1 AND key = $2`, kind, key)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var fp string
		for rows.Next() {
			rows.Scan(&fp)
		}
		if fp == "" {
			t.Fatalf("no fingerprint for %s %s", kind, key)
		}
		return fp
	}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, o := range objects {
			out[o[0]+" "+o[1]] = read(o[0], o[1])
		}
		return out
	}
	base := snapshot()

	// Nothing that isn't the definition changes a fingerprint.
	exec(`INSERT INTO app.t (id, n) SELECT g, g FROM generate_series(1, 1000) g`,
		`ANALYZE app.t`, `VACUUM app.t`, `SELECT nextval('app.s')`, `SET search_path = app, public`)
	for k, v := range snapshot() {
		if base[k] != v {
			t.Errorf("%s changed without a definition change", k)
		}
	}
	exec(`RESET search_path`)

	// Each definition change changes its object's fingerprint, and only it.
	changes := []struct{ object, sql string }{
		{"table app.t", `COMMENT ON COLUMN app.t.n IS 'x'`},
		{"table app.t", `ALTER TABLE app.t ALTER COLUMN n SET DEFAULT 3`},
		{"table app.t", `CREATE INDEX t_m ON app.t (m)`},
		{"table app.t", `GRANT SELECT ON app.t TO reader`},
		{"table app.t", `ALTER TABLE app.t ADD CONSTRAINT n_pos CHECK (n > 0)`},
		{"view app.v", `CREATE OR REPLACE VIEW app.v AS SELECT id, m FROM app.t WHERE id > 0`},
		{"sequence app.s", `ALTER SEQUENCE app.s INCREMENT 5`},
		{"function app.f(app.pos)", `CREATE OR REPLACE FUNCTION app.f(x app.pos) RETURNS int LANGUAGE sql AS 'SELECT x + 2'`},
		{"type app.mood", `ALTER TYPE app.mood ADD VALUE 'happy'`},
		{"type app.pos", `ALTER DOMAIN app.pos SET DEFAULT 1`},
		{"schema app", `COMMENT ON SCHEMA app IS 'app'`},
		{"role reader", `ALTER ROLE reader CONNECTION LIMIT 3`},
		{"extension plpgsql", `COMMENT ON EXTENSION plpgsql IS 'pl'`},
	}
	for _, c := range changes {
		before := snapshot()
		exec(c.sql)
		after := snapshot()
		for k := range before {
			if (before[k] != after[k]) != (k == c.object) {
				t.Errorf("%s: %s changed=%v", c.sql, k, before[k] != after[k])
			}
		}
	}

	// The guard.
	exec(f.Record("table", "app.t", "m"))
	if _, err := conn.Exec(ctx, f.Guard("table", "app.t")); err != nil {
		t.Errorf("unchanged: %v", err)
	}
	exec(`ALTER TABLE app.t ALTER COLUMN n DROP DEFAULT`)
	if _, err := conn.Exec(ctx, f.Guard("table", "app.t")); err == nil || !strings.Contains(err.Error(), "DPG-E055") {
		t.Errorf("changed: %v", err)
	}
	exec(`SET dpg.allow_drift = on`)
	if _, err := conn.Exec(ctx, f.Guard("table", "app.t")); err != nil {
		t.Errorf("dpg.allow_drift: %v", err)
	}
	exec(`RESET dpg.allow_drift`, f.Forget("table", "app.t"))
	if _, err := conn.Exec(ctx, f.Guard("table", "app.t")); err == nil || !strings.Contains(err.Error(), "DPG-E054") {
		t.Errorf("unrecorded: %v", err)
	}
	exec(`UPDATE dpg.objects SET pg_major = 9`)
	exec(f.Record("view", "app.v", "m"), `UPDATE dpg.objects SET pg_major = 9 WHERE key = 'app.v'`)
	if _, err := conn.Exec(ctx, f.Guard("view", "app.v")); err == nil || !strings.Contains(err.Error(), "PostgreSQL 9") {
		t.Errorf("other major version: %v", err)
	}
	// The guard leaves the session's search_path as it was.
	exec(`SET search_path = app, public`, f.Record("view", "app.v", "m"), f.Guard("view", "app.v"))
	rows, _ := conn.QueryRows(ctx, `SHOW search_path`)
	var sp string
	for rows.Next() {
		rows.Scan(&sp)
	}
	rows.Close()
	if sp != "app, public" {
		t.Errorf("search_path after the guard: %q", sp)
	}
	// A dropped object has no fingerprint (Record writes nothing).
	exec(`DROP VIEW app.v`, f.Forget("view", "app.v"), f.Record("view", "app.v", "m"))
	rows, _ = conn.QueryRows(ctx, `SELECT count(*)::text FROM dpg.objects WHERE key = 'app.v'`)
	var n string
	for rows.Next() {
		rows.Scan(&n)
	}
	rows.Close()
	if n != "0" {
		t.Errorf("a dropped object recorded: %s", n)
	}
}
