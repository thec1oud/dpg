//go:build integration

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thec1oud/dpg/internal/apply"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

const tFile = "c/app/schemas/public/tables/t.dpg"

// Three versions of one table: each records one migration.
const (
	v1 = "TABLE t (id int PRIMARY KEY, x int);\n"
	// v2 adds a column, and an index CONCURRENTLY: after the migration's
	// transaction.
	v2 = "TABLE t (id int PRIMARY KEY, x int, y int) { INDEXES { CONCURRENTLY t_x (x); } }\n"
	// v3 drops the column again: Destructive.
	v3 = "TABLE t (id int PRIMARY KEY, x int) { INDEXES { CONCURRENTLY t_x (x); } }\n"
)

// newProject writes a project with one database, app, whose source is src.
func newProject(t *testing.T, src string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"dpg.toml":       "kind = \"root\"\n",
		"c/dpg.toml":     "kind = \"cluster\"\n\n[cluster]\nname = \"c\"\n",
		"c/app/dpg.toml": "kind = \"database\"\n\n[database]\nname = \"app\"\ndefault_schema = \"public\"\n",
		tFile:            src,
	} {
		writeFile(t, root, name, content)
	}
	return root
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// record records the source's change in the project's migration log as a
// developer does: dpg apply on a development database, dev.
func record(t *testing.T, root, dev string) {
	t.Helper()
	proj, err := project.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	cl := proj.Clusters[0]
	db := cl.Databases[0]
	opts := apply.Options{
		AllowDestructive: true,
		Preflight:        true,
		DPGDir:           proj.DPGDir(),
		MigrationsDir:    proj.MigrationsDir(),
		LintCfg:          apply.LinterConfig(proj.RootConfig.Linter),
		Ignore:           ignore.New(db.EffectiveIgnore(cl, proj.RootConfig)),
		Ledger:           apply.LedgerFor(proj.RootConfig),
	}
	differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if err != nil {
		t.Fatal(err)
	}
	emitter, err := pipeline.MustResolve[pipeline.Emitter](pipeline.Default, pipeline.KeyEmitter)
	if err != nil {
		t.Fatal(err)
	}
	c := apply.Components{Store: &snapshotpkg.FileStore{Dir: proj.SnapshotDir()}, Differ: differ, Emitter: emitter, Exec: executor.New()}
	connect := func(ctx context.Context) (*executor.PgxConn, error) { return executor.Connect(ctx, dev) }
	tg, err := apply.PrepareDatabase(cl, db, c, connect, opts, apply.Printer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tg.Run(context.Background(), opts, apply.Printer{}); err != nil {
		t.Fatalf("recording on the development database: %v", err)
	}
}

// build returns a project whose log records the versions in order.
func build(t *testing.T, versions ...string) string {
	t.Helper()
	root := newProject(t, versions[0])
	dev := testpg.Database(t)
	record(t, root, dev)
	for _, v := range versions[1:] {
		writeFile(t, root, tFile, v)
		record(t, root, dev)
	}
	return root
}

// copyProject copies a project, as an older build of it.
func copyProject(t *testing.T, root string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(root)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func open(t *testing.T, root string, conn *pgx.Conn) *Engine {
	t.Helper()
	e, err := Open(Config{Dir: root, Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(context.Background()) })
	return e
}

func status(t *testing.T, e *Engine) Status {
	t.Helper()
	s, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func query(t *testing.T, conn *pgx.Conn, sql string) string {
	t.Helper()
	var s string
	if err := conn.QueryRow(context.Background(), sql).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func columns(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	return query(t, conn, `SELECT coalesce(string_agg(column_name, ',' ORDER BY ordinal_position), '') FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 't'`)
}

// migrations are the IDs of the project's migrations, in order.
func migrations(t *testing.T, root string) []string {
	t.Helper()
	l, err := migrationlog.Load(filepath.Join(root, ".dpg", "migrations", "c", "app"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range l.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// TestEngineSettlesAFreshDatabaseLive: a database that has run none of the
// log is Pending; the plan lists every statement with its safety and how
// it runs; Apply runs the migrations in order; the database is then up to
// date, and applying again does nothing.
func TestEngineSettlesAFreshDatabaseLive(t *testing.T) {
	root := build(t, v1, v2)
	ids := migrations(t, root)
	if len(ids) != 2 {
		t.Fatalf("the log has %d migrations", len(ids))
	}
	conn := connect(t, testpg.Database(t))
	e := open(t, root, conn)
	ctx := context.Background()

	s := status(t, e)
	if s.State != Pending || !slices.Equal(s.Pending, ids) || s.Current != "" || s.Expected != ids[1] {
		t.Fatalf("status of a fresh database: %+v", s)
	}
	p, err := e.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, st := range p.Steps {
		if !strings.HasPrefix(st.ID, st.Migration+"/") {
			t.Errorf("step %s isn't named after its migration %s", st.ID, st.Migration)
		}
		got = append(got, st.Migration[len(st.Migration)-4:]+" "+st.Safety.String()+" "+st.Mode.String()+" "+strings.Fields(st.SQL)[0]+" "+strings.Fields(st.SQL)[1])
	}
	want := []string{
		ids[0][len(ids[0])-4:] + " SAFE transaction CREATE TABLE",
		ids[1][len(ids[1])-4:] + " SAFE transaction ALTER TABLE",
		ids[1][len(ids[1])-4:] + " CAUTION after CREATE INDEX",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p.MaxSafety() != Caution {
		t.Errorf("the plan's max safety is %s", p.MaxSafety())
	}
	if off := e.PlanOffline(); !slices.Equal(off.Steps, p.Steps) {
		t.Errorf("the offline plan of the whole log differs from the plan for an empty database:\n%v\n%v", off.Steps, p.Steps)
	}

	res, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Applied, ids) {
		t.Errorf("applied %v, want %v", res.Applied, ids)
	}
	if got := columns(t, conn); got != "id,x,y" {
		t.Errorf("t has %s", got)
	}
	if got := query(t, conn, `SELECT count(*)::text FROM pg_indexes WHERE indexname = 't_x'`); got != "1" {
		t.Errorf("%s indexes t_x", got)
	}
	if s := status(t, e); s.State != UpToDate || s.Current != ids[1] || len(s.Pending) != 0 {
		t.Errorf("status after applying: %+v", s)
	}
	if p, err := e.Plan(ctx); err != nil || len(p.Steps) != 0 {
		t.Errorf("the plan after applying: %v, %v", p.Steps, err)
	}
	if res, err := e.Apply(ctx, ApplyOptions{}); err != nil || len(res.Applied) != 0 {
		t.Errorf("applying again: %v, %v", res, err)
	}
	findings, err := e.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Failing {
			t.Errorf("verify after applying: %+v", f)
		}
	}
}

// TestEngineCatchesUpADatabaseThatIsBehindLive: a database that ran the
// first migration runs only the second.
func TestEngineCatchesUpADatabaseThatIsBehindLive(t *testing.T) {
	root := newProject(t, v1)
	dev := testpg.Database(t)
	record(t, root, dev)
	old := copyProject(t, root)
	writeFile(t, root, tFile, v2)
	record(t, root, dev)
	ids := migrations(t, root)

	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	if _, err := open(t, old, conn).Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, conn)
	if s := status(t, e); s.State != Pending || s.Current != ids[0] || !slices.Equal(s.Pending, ids[1:]) {
		t.Fatalf("status of a database behind: %+v", s)
	}
	res, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution})
	if err != nil || !slices.Equal(res.Applied, ids[1:]) {
		t.Fatalf("applied %v, %v", res.Applied, err)
	}
	if got := columns(t, conn); got != "id,x,y" {
		t.Errorf("t has %s", got)
	}
}

// TestEngineRefusesWhatTheOptionsDontAllowLive: a plan with a statement
// above MaxSafety, whose zero value is Safe, is refused before anything
// runs.
func TestEngineRefusesWhatTheOptionsDontAllowLive(t *testing.T) {
	root := build(t, v1, v2, v3)
	ids := migrations(t, root)
	conn := connect(t, testpg.Database(t))
	e := open(t, root, conn)
	ctx := context.Background()

	for _, max := range []Safety{Safe, Caution} {
		_, err := e.Apply(ctx, ApplyOptions{MaxSafety: max})
		var refused *RefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("MaxSafety %s: %v", max, err)
		}
		for _, s := range refused.Steps {
			if s.Safety <= max {
				t.Errorf("MaxSafety %s refused %s, of %s", max, s.ID, s.Safety)
			}
		}
		if strings.Contains(err.Error(), "DROP") {
			t.Errorf("the refusal quotes SQL: %v", err)
		}
		if got := columns(t, conn); got != "" {
			t.Fatalf("after a refusal, t has %s", got)
		}
		if s := status(t, e); s.State != Pending || len(s.Pending) != 3 {
			t.Fatalf("after a refusal: %+v", s)
		}
	}
	res, err := e.Apply(ctx, ApplyOptions{MaxSafety: Destructive})
	if err != nil || !slices.Equal(res.Applied, ids) {
		t.Fatalf("applied %v, %v", res.Applied, err)
	}
	if got := columns(t, conn); got != "id,x" {
		t.Errorf("t has %s", got)
	}
}

// TestEngineHookRunsInEachTransactionAndBeforeEachStatementOutsideOneLive:
// what the hook does commits with the migration's statements, and rolls
// back with them when it fails, which stops the migration; it runs before
// a statement outside the transaction, such as CREATE INDEX CONCURRENTLY.
func TestEngineHookRunsInEachTransactionAndBeforeEachStatementOutsideOneLive(t *testing.T) {
	root := build(t, v1, v2)
	ids := migrations(t, root)
	url := testpg.Database(t)
	conn := connect(t, url)
	if _, err := conn.Exec(context.Background(), `CREATE SCHEMA dpg; CREATE TABLE dpg.marks (migration text)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	refuse := errors.New("refused by the hook")

	var current string
	var indexSeen []bool // during the second migration: whether t_x exists
	opts := func(failOn string) ApplyOptions {
		return ApplyOptions{
			MaxSafety: Caution,
			Observe: func(ctx context.Context, id string) (context.Context, func(error)) {
				current = id
				return ctx, func(error) {}
			},
			Hook: func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO dpg.marks VALUES ($1)`, current); err != nil {
					return err
				}
				if current == ids[1] {
					var has bool
					if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 't_x')`).Scan(&has); err != nil {
						return err
					}
					indexSeen = append(indexSeen, has)
				}
				if current == failOn {
					return refuse
				}
				return nil
			},
		}
	}

	// The hook refuses the second migration: the first is applied, with
	// the hook's marks; the second isn't, and its marks rolled back.
	res, err := open(t, root, conn).Apply(ctx, opts(ids[1]))
	if !errors.Is(err, refuse) {
		t.Fatalf("the hook's refusal isn't the error: %v", err)
	}
	if !slices.Equal(res.Applied, ids[:1]) {
		t.Errorf("applied %v", res.Applied)
	}
	if got := query(t, conn, `SELECT count(*)::text FROM dpg.marks WHERE migration = '`+ids[1]+`'`); got != "0" {
		t.Errorf("%s marks of the refused migration committed", got)
	}
	if got := query(t, conn, `SELECT (count(*) > 0)::text FROM dpg.marks WHERE migration = '`+ids[0]+`'`); got != "true" {
		t.Errorf("no mark of the first migration committed")
	}
	if got := columns(t, conn); got != "id,x" {
		t.Errorf("t has %s", got)
	}

	// Without refusing, the second applies; the hook ran before the index
	// was made (outside the transaction) and once after (the ledger's
	// final record).
	indexSeen = nil
	res, err = open(t, root, conn).Apply(ctx, opts(""))
	if err != nil || !slices.Equal(res.Applied, ids[1:]) {
		t.Fatalf("applied %v, %v", res.Applied, err)
	}
	if i := slices.Index(indexSeen, true); i < 1 || slices.Contains(indexSeen[i:], false) {
		t.Errorf("the hook saw the index as %v: want it absent until CREATE INDEX CONCURRENTLY, and present after", indexSeen)
	}
	if got := query(t, conn, `SELECT count(*)::text FROM dpg.marks WHERE migration = '`+ids[1]+`'`); got != strconv.Itoa(len(indexSeen)) {
		t.Errorf("%s marks committed, of %d hook calls", got, len(indexSeen))
	}
}

// TestEngineFinishesAMigrationWhoseStepsAfterItsTransactionFailedLive: the
// transaction committed, then a statement after it failed; the database is
// Pending, the plan is the statements after the transaction, and the next
// Apply runs them.
func TestEngineFinishesAMigrationWhoseStepsAfterItsTransactionFailedLive(t *testing.T) {
	// The second migration only adds an index CONCURRENTLY (see
	// TestEngineGatesAMigrationBeingFinishedByWhatStillRunsLive for one
	// whose transaction changes the table too).
	root := newProject(t, v1)
	dev := testpg.Database(t)
	record(t, root, dev)
	old := copyProject(t, root)
	writeFile(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int) { INDEXES { CONCURRENTLY t_x (x); } }\n")
	record(t, root, dev)
	ids := migrations(t, root)

	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	if _, err := open(t, old, conn).Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	// The first call after the second migration's transaction committed
	// fails.
	e := open(t, root, conn)
	afterCommit := errors.New("failed after the commit")
	_, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution, Hook: func(ctx context.Context, tx pgx.Tx) error {
		var committed bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dpg.migrations WHERE id = $1 AND status = 'committed')`, ids[1]).Scan(&committed)
		if err == nil && committed {
			return afterCommit
		}
		return err
	}})
	if !errors.Is(err, afterCommit) {
		t.Fatalf("apply: %v", err)
	}
	s := status(t, e)
	if s.State != Pending || !slices.Equal(s.Pending, ids[1:]) || s.Current != ids[0] {
		t.Fatalf("status with a migration to finish: %+v", s)
	}
	p, err := e.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Mode != AfterTransaction || !strings.HasPrefix(p.Steps[0].SQL, "CREATE INDEX CONCURRENTLY") {
		t.Fatalf("the plan to finish: %+v", p.Steps)
	}
	res, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution})
	if err != nil || !slices.Equal(res.Applied, ids[1:]) {
		t.Fatalf("finishing: %v, %v", res.Applied, err)
	}
	if s := status(t, e); s.State != UpToDate {
		t.Errorf("status after finishing: %+v", s)
	}
	if got := query(t, conn, `SELECT count(*)::text FROM pg_indexes WHERE indexname = 't_x'`); got != "1" {
		t.Errorf("%s indexes t_x", got)
	}
}

// TestEngineStopsWhenTheContextEndsLive: Apply honours its context, and
// what a migration cancelled half way did rolls back.
func TestEngineStopsWhenTheContextEndsLive(t *testing.T) {
	root := build(t, v1)
	url := testpg.Database(t)
	ctx := context.Background()

	ended, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := open(t, root, connect(t, url)).Apply(ended, ApplyOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("apply with an ended context: %v", err)
	}

	// Cancelled from inside the migration's transaction, before its
	// statements: a cancelled query may close its connection, so the
	// engine runs on one of its own.
	halfway, cancel := context.WithCancel(ctx)
	defer cancel()
	e, err := Open(Config{Dir: root, DSN: url})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(ctx)
	_, err = e.Apply(halfway, ApplyOptions{Hook: func(context.Context, pgx.Tx) error {
		cancel()
		return nil
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("apply cancelled half way: %v", err)
	}
	conn := connect(t, url)
	if got := columns(t, conn); got != "" {
		t.Errorf("t has %s after the cancelled apply", got)
	}
	if s := status(t, open(t, root, conn)); s.State != Pending {
		t.Errorf("status after the cancelled apply: %+v", s)
	}
}

// TestEngineStatusTellsAheadFromDriftedLive: a database a newer build
// migrated is Ahead of the older build; one that ran migrations of another
// log, or has the log's objects with no record of running them, is
// Drifted. Neither is applied over, and Verify names the database's place.
func TestEngineStatusTellsAheadFromDriftedLive(t *testing.T) {
	root := newProject(t, v1)
	dev := testpg.Database(t)
	record(t, root, dev)
	old := copyProject(t, root)
	writeFile(t, root, tFile, v2)
	record(t, root, dev)
	ids := migrations(t, root)
	edited := build(t, "TABLE t (id int PRIMARY KEY, x int, z int);\n")

	url := testpg.Database(t)
	conn := connect(t, url)
	ctx := context.Background()
	if _, err := open(t, root, conn).Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatal(err)
	}

	older := open(t, old, conn)
	if s := status(t, older); s.State != Ahead || s.Current != ids[1] || s.Expected != ids[0] {
		t.Errorf("the older build's status: %+v", s)
	}
	if _, err := older.Apply(ctx, ApplyOptions{MaxSafety: Destructive}); err == nil {
		t.Error("the older build applied over a newer database")
	}
	findings, err := older.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f := findingIn(findings, "log"); f == nil || !f.Failing || !strings.Contains(f.Detail, "newer build") {
		t.Errorf("verify by the older build: %+v", findings)
	}

	other := open(t, edited, conn)
	if s := status(t, other); s.State != Drifted || s.Current != ids[1] {
		t.Errorf("another log's status: %+v", s)
	}
	if _, err := other.Apply(ctx, ApplyOptions{MaxSafety: Destructive}); err == nil {
		t.Error("another log applied over the database")
	}
	findings, err = other.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f := findingIn(findings, "log"); f == nil || !f.Failing {
		t.Errorf("verify by another log names no place: %+v", findings)
	}
	if f := findingIn(findings, "drift"); f == nil || !f.Failing || !strings.Contains(f.ID, "table:public.t") {
		t.Errorf("verify by another log finds no drift of t: %+v", findings)
	}
	if got := columns(t, conn); got != "id,x,y" {
		t.Errorf("t has %s", got)
	}

	unrecorded := connect(t, testpg.Database(t))
	if _, err := unrecorded.Exec(ctx, `CREATE TABLE public.t (id int PRIMARY KEY, x int)`); err != nil {
		t.Fatal(err)
	}
	if s := status(t, open(t, root, unrecorded)); s.State != Drifted || !strings.Contains(s.Detail, "no record") {
		t.Errorf("a database with the log's objects and no record of them: %+v", s)
	}
}

func findingIn(findings []Finding, group string) *Finding {
	for i := range findings {
		if findings[i].Group == group {
			return &findings[i]
		}
	}
	return nil
}

// TestEngineVerifyFindsDriftAndApplyRefusesItLive: an object changed by
// hand is drift for Verify, and Apply refuses a migration that changes it,
// saying why, before running anything.
func TestEngineVerifyFindsDriftAndApplyRefusesItLive(t *testing.T) {
	root := newProject(t, v1)
	dev := testpg.Database(t)
	record(t, root, dev)
	old := copyProject(t, root)
	writeFile(t, root, tFile, v2)
	record(t, root, dev)

	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	e := open(t, old, conn)
	if _, err := e.Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ALTER TABLE public.t ALTER COLUMN x TYPE bigint`); err != nil {
		t.Fatal(err)
	}
	findings, err := e.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := findingIn(findings, "drift")
	if f == nil || !f.Failing || !strings.HasPrefix(f.ID, "table:public.t") || f.Detail == "" {
		t.Fatalf("verify: %+v", findings)
	}
	_, err = open(t, root, conn).Apply(ctx, ApplyOptions{MaxSafety: Caution})
	if err == nil || !strings.Contains(err.Error(), "DPG-E053") || !strings.Contains(err.Error(), f.ID) {
		t.Fatalf("apply over drift: %v", err)
	}
	if got := columns(t, conn); got != "id,x" {
		t.Errorf("t has %s", got)
	}
}

// TestEngineRefusesASourceTheLogDoesntRecordLive: Open refuses a change of
// the source that isn't recorded, so the engine never writes the project.
func TestEngineRefusesASourceTheLogDoesntRecordLive(t *testing.T) {
	root := build(t, v1)
	writeFile(t, root, tFile, v2)
	if _, err := Open(Config{Dir: root}); err == nil || !strings.Contains(err.Error(), "DPG-E056") {
		t.Fatalf("open with an unrecorded change: %v", err)
	}
}

// TestEngineKeepsTheCallersConnectionOpenLive: Close leaves a connection
// the caller gave open, and closes one the engine opened.
func TestEngineKeepsTheCallersConnectionOpenLive(t *testing.T) {
	root := build(t, v1)
	url := testpg.Database(t)
	conn := connect(t, url)
	ctx := context.Background()
	e := open(t, root, conn)
	if _, err := e.Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if conn.IsClosed() {
		t.Fatal("Close closed the caller's connection")
	}

	own, err := Open(Config{Dir: root, DSN: url})
	if err != nil {
		t.Fatal(err)
	}
	if s := status(t, own); s.State != UpToDate {
		t.Fatalf("status: %+v", s)
	}
	used := own.conn
	if err := own.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !used.IsClosed() {
		t.Error("Close left the engine's own connection open")
	}
}

// withIndex is v1 with an index on x, made CONCURRENTLY after the
// migration's transaction.
const withIndex = "TABLE t (id int PRIMARY KEY, x int) { INDEXES { CONCURRENTLY t_x (x); } }\n"

// indexed returns a project whose log records v1 then withIndex, and its
// older build that records only v1.
func indexed(t *testing.T) (root, old string) {
	t.Helper()
	root = newProject(t, v1)
	dev := testpg.Database(t)
	record(t, root, dev)
	old = copyProject(t, root)
	writeFile(t, root, tFile, withIndex)
	record(t, root, dev)
	return root, old
}

// failAfterCommit is a hook that fails once the migration's transaction
// has committed, so the statements after it don't run.
func failAfterCommit(errAfter error) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		var committed bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dpg.migrations WHERE status = 'committed')`).Scan(&committed)
		if err == nil && committed {
			return errAfter
		}
		return err
	}
}

// TestEngineRebuildsAnIndexWhoseBuildWasCancelledLive: an apply cancelled
// while CREATE INDEX CONCURRENTLY waits (another transaction holds a lock
// on the table) leaves the index behind, invalid, and its migration
// committed; the next Apply drops the index and builds it again.
func TestEngineRebuildsAnIndexWhoseBuildWasCancelledLive(t *testing.T) {
	root, old := indexed(t)
	url := testpg.Database(t)
	ctx := context.Background()
	if _, err := open(t, old, connect(t, url)).Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	blocker := connect(t, url)
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE t IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := open(t, root, connect(t, url)).Apply(actx, ApplyOptions{MaxSafety: Caution})
		done <- err
	}()
	watch := connect(t, url)
	deadline := time.Now().Add(30 * time.Second)
	for query(t, watch, `SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND query LIKE 'CREATE INDEX CONCURRENTLY%' AND wait_event_type = 'Lock'`) == "0" {
		if time.Now().After(deadline) {
			t.Fatal("the index build never waited for the lock")
		}
		time.Sleep(5 * time.Millisecond) // a poll interval, not synchronization
	}
	cancel()
	err = <-done
	if err == nil || !strings.Contains(err.Error(), "the next apply finishes it") || strings.Contains(err.Error(), "--resume") {
		t.Fatalf("the cancelled apply: %v", err)
	}
	var se *StatementError
	if !errors.As(err, &se) || !strings.HasPrefix(se.SQL, `CREATE INDEX CONCURRENTLY "t_x"`) || !errors.Is(se, context.Canceled) {
		t.Errorf("the failed statement: %#v", se)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := query(t, watch, `SELECT indisvalid::text FROM pg_index WHERE indexrelid = 'public.t_x'::regclass`); got != "false" {
		t.Fatalf("the cancelled build left the index valid=%s", got)
	}

	e := open(t, root, connect(t, url))
	if s := status(t, e); s.State != Pending {
		t.Fatalf("status with the index to rebuild: %+v", s)
	}
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	if got := query(t, watch, `SELECT string_agg(indisvalid::text, ',') FROM pg_index WHERE indexrelid = 'public.t_x'::regclass`); got != "true" {
		t.Errorf("after finishing, t_x valid=%s", got)
	}
	if s := status(t, e); s.State != UpToDate {
		t.Errorf("status after finishing: %+v", s)
	}
}

// TestEngineTakesAValidIndexAsBuiltWhenFinishingLive: the index was built
// (here by hand) but the migration wasn't recorded as finished; finishing
// it doesn't build the index again.
func TestEngineTakesAValidIndexAsBuiltWhenFinishingLive(t *testing.T) {
	root, old := indexed(t)
	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	if _, err := open(t, old, conn).Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, conn)
	afterCommit := errors.New("failed after the commit")
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution, Hook: failAfterCommit(afterCommit)}); !errors.Is(err, afterCommit) {
		t.Fatalf("apply: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE INDEX t_x ON t (x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	if s := status(t, e); s.State != UpToDate {
		t.Errorf("status after finishing: %+v", s)
	}
	if got := query(t, conn, `SELECT count(*)::text FROM pg_indexes WHERE tablename = 't' AND indexname LIKE 't_x%'`); got != "1" {
		t.Errorf("%s indexes like t_x", got)
	}
}

// TestEngineGatesAMigrationBeingFinishedByWhatStillRunsLive: a migration
// that dropped a column (Destructive) in its transaction, which committed,
// has only its CREATE INDEX CONCURRENTLY (Caution) left to run: the plan
// and Apply both judge it Caution, and the preflight takes the database to
// be at the migration, without the column.
func TestEngineGatesAMigrationBeingFinishedByWhatStillRunsLive(t *testing.T) {
	root := newProject(t, v2)
	dev := testpg.Database(t)
	record(t, root, dev)
	old := copyProject(t, root)
	// v3 drops y, and here adds an index too.
	writeFile(t, root, tFile, "TABLE t (id int PRIMARY KEY, x int) { INDEXES { CONCURRENTLY t_x (x); CONCURRENTLY t_x_id (x, id); } }\n")
	record(t, root, dev)

	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	if _, err := open(t, old, conn).Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, conn)
	p, err := e.Plan(ctx)
	if err != nil || p.MaxSafety() != Destructive {
		t.Fatalf("the plan before: %v, %v", p.MaxSafety(), err)
	}
	afterCommit := errors.New("failed after the commit")
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Destructive, Hook: failAfterCommit(afterCommit)}); !errors.Is(err, afterCommit) {
		t.Fatalf("apply: %v", err)
	}
	p, err = e.Plan(ctx)
	if err != nil || p.MaxSafety() != Caution || len(p.Steps) != 1 || p.Steps[0].Mode != AfterTransaction {
		t.Fatalf("the plan to finish: %+v, %v", p.Steps, err)
	}
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Safe}); !errors.As(err, new(*RefusedError)) {
		t.Fatalf("finishing above the maximum safety: %v", err)
	}
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatalf("finishing under Caution: %v", err)
	}
	if s := status(t, e); s.State != UpToDate {
		t.Errorf("status after finishing: %+v", s)
	}
}

// TestEngineReportsWarningsLive: a snapshot of another format version is
// a warning the engine returns, not one it prints.
func TestEngineReportsWarningsLive(t *testing.T) {
	root := build(t, v1)
	p := filepath.Join(root, ".dpg", "snapshots", "c", "app.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), `"dpg_version": "`+snapshotpkg.Version+`"`, `"dpg_version": "9.0.0"`, 1))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := Open(Config{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if w := e.Warnings(); len(w) != 1 || !strings.Contains(w[0], "format 9.0.0") {
		t.Errorf("warnings %q", w)
	}
}

// TestEngineRunsAsTheDatabasesOwnerLive: no superuser is needed; the
// database's owner, with no other privilege, applies and verifies.
func TestEngineRunsAsTheDatabasesOwnerLive(t *testing.T) {
	root := build(t, v1, withIndex)
	url := testpg.Database(t)
	admin := connect(t, url)
	ctx := context.Background()
	role := "dpg_owner_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	db := query(t, admin, "SELECT current_database()")
	for _, s := range []string{
		"CREATE ROLE " + role + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE",
		"ALTER DATABASE " + db + " OWNER TO " + role,
		"ALTER SCHEMA public OWNER TO " + role,
	} {
		if _, err := admin.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// testpg.Database drops the database WITH (FORCE) after the test; the
	// role can go once it owns nothing.
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), url)
		if err != nil {
			return
		}
		defer conn.Close(context.Background())
		_, _ = conn.Exec(context.Background(), "REASSIGN OWNED BY "+role+" TO CURRENT_USER; DROP OWNED BY "+role+"; DROP ROLE "+role)
	})
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.User, cfg.Password = role, ""
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	e := open(t, root, conn)
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatalf("apply as the owner: %v", err)
	}
	if s := status(t, e); s.State != UpToDate {
		t.Fatalf("status: %+v", s)
	}
	fs, err := e.Verify(ctx)
	if err != nil {
		t.Fatalf("verify as the owner: %v", err)
	}
	for _, f := range fs {
		if f.Failing {
			t.Errorf("finding %+v", f)
		}
	}
}

// TestEngineFinishesAMigrationThenRunsTheNextOnTheSameTableLive: a
// migration whose index wasn't built yet is finished, and a later
// migration that changes the same table runs after it: the index still
// to be built isn't drift on the table.
func TestEngineFinishesAMigrationThenRunsTheNextOnTheSameTableLive(t *testing.T) {
	root, old := indexed(t)
	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	if _, err := open(t, old, conn).Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	afterCommit := errors.New("failed after the commit")
	if _, err := open(t, root, conn).Apply(ctx, ApplyOptions{MaxSafety: Caution, Hook: failAfterCommit(afterCommit)}); !errors.Is(err, afterCommit) {
		t.Fatalf("apply: %v", err)
	}
	newer := copyProject(t, root)
	writeFile(t, newer, tFile, "TABLE t (id int PRIMARY KEY, x int, note text) { INDEXES { CONCURRENTLY t_x (x); } }\n")
	record(t, newer, testpg.Database(t))
	e := open(t, newer, conn)
	if s := status(t, e); s.State != Pending || len(s.Pending) != 2 {
		t.Fatalf("status: %+v", s)
	}
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution}); err != nil {
		t.Fatalf("finishing, then the next migration: %v", err)
	}
	if s := status(t, e); s.State != UpToDate {
		t.Errorf("status: %+v", s)
	}
	if got := columns(t, conn); got != "id,x,note" {
		t.Errorf("columns %s", got)
	}
	if got := query(t, conn, `SELECT indisvalid::text FROM pg_index WHERE indexrelid = 'public.t_x'::regclass`); got != "true" {
		t.Errorf("t_x valid=%s", got)
	}
}

// TestEngineRefusesToFinishOverAnIndexOfAnotherTableLive: an index of the
// name the migration builds is on another table; finishing neither drops
// it nor takes it as built.
func TestEngineRefusesToFinishOverAnIndexOfAnotherTableLive(t *testing.T) {
	root, old := indexed(t)
	conn := connect(t, testpg.Database(t))
	ctx := context.Background()
	if _, err := open(t, old, conn).Apply(ctx, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	e := open(t, root, conn)
	afterCommit := errors.New("failed after the commit")
	if _, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution, Hook: failAfterCommit(afterCommit)}); !errors.Is(err, afterCommit) {
		t.Fatalf("apply: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE o (x int); CREATE INDEX t_x ON o (x)`); err != nil {
		t.Fatal(err)
	}
	_, err := e.Apply(ctx, ApplyOptions{MaxSafety: Caution})
	if err == nil || !strings.Contains(err.Error(), "an index of that name is on o") {
		t.Fatalf("finishing: %v", err)
	}
	if got := query(t, conn, `SELECT indrelid::regclass::text FROM pg_index WHERE indexrelid = 'public.t_x'::regclass`); got != "o" {
		t.Errorf("t_x is on %s", got)
	}
	if s := status(t, e); s.State != Pending {
		t.Errorf("status: %+v", s)
	}
}
