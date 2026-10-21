// Package engine applies a DPG project's migrations to a database from Go:
// where the database is in the project's migration log, what applying would
// run, applying it, and verify's drift check. It runs the same code as dpg
// apply and dpg verify.
//
// PROVISIONAL: this API is a stopgap so that tools can embed DPG (Galleon's
// adapters/dpg does) until DPG's library API is designed. Expect it to
// change, and to be replaced.
//
//	e, err := engine.Open(engine.Config{Dir: "db", Database: "app", DSN: url})
//	if err != nil { ... }
//	defer e.Close(ctx)
//	st, err := e.Status(ctx)
//	res, err := e.Apply(ctx, engine.ApplyOptions{MaxSafety: engine.Caution})
//
// The engine works on one database of a project, never on its cluster-level
// objects (roles, tablespaces). It runs the migrations the migration log
// records, as dpg apply does when it settles a database that is behind:
// Open refuses a source with changes the log doesn't record (dpg plan
// --record records them), so the engine never writes to the project. It
// needs the ledger ([ledger], on by default), which is how it knows which
// migrations the database has run.
//
// It never prompts, prints or exits: everything it has to say is in its
// results, its errors and Warnings. Its own messages name steps, never
// their SQL (RefusedError), but a statement that failed is a
// *StatementError, and a statement dpg apply's own checks refuse a
// *GateError, whose messages quote the statement, as dpg apply's errors
// do: a caller that keeps SQL out of its logs finds them with errors.As
// and names the statement by its Step field, the ID of the Step it is,
// or, when that is empty, as a guard of the migration its Migration field
// names, or as one of DPG's own (its ledger's records) when that is empty
// too. Every call stops when its context ends. An Engine is not safe for
// concurrent use.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/thec1oud/dpg/internal/apply"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"

	// Concrete implementations register into pipeline.Default via init().
	_ "github.com/thec1oud/dpg/internal/blockparser"
	_ "github.com/thec1oud/dpg/internal/diff"
	_ "github.com/thec1oud/dpg/internal/emit"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/introspect"
	_ "github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/linter"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	_ "github.com/thec1oud/dpg/internal/scanner"
	_ "github.com/thec1oud/dpg/internal/secrets"
)

// Config says which database of which project the engine works on, and
// how it reaches the database.
type Config struct {
	// Dir is the project's root directory, or a directory below it.
	Dir string
	// Cluster and Database name the database; either may be empty when
	// the project has only one.
	Cluster, Database string
	// Conn is the connection to run on; the caller keeps it, and closes
	// it after Close. When it is nil, the engine connects to DSN, as is,
	// when it first needs to. With neither, only PlanOffline works.
	Conn *pgx.Conn
	DSN  string
}

// StatementError is a statement that failed: Step is the ID of the Step
// it is ("" for one of DPG's own), Migration the migration whose step or
// guard it is ("" for the ledger's records), SQL the statement, as a Step
// has it, and Err why it failed. A guard (a check Step) runs twice, before
// and in its migration's transaction, so its Step is "" too.
type StatementError = executor.StatementError

// GateError is a statement dpg apply's own checks of safety refuse, which
// Apply's refusals (RefusedError) leave none to: Reason says why, without
// the statement, and Step is the ID of the Step it is.
type GateError = apply.GateError

// ErrNoConnection is returned by the calls that need a database when the
// Config gave none.
var ErrNoConnection = errors.New("engine: no connection: the Config has neither Conn nor DSN")

// Engine is an open project database.
type Engine struct {
	cfg    Config
	t      *apply.Target
	opts   apply.Options
	conn   *pgx.Conn
	owned  bool // conn was opened from DSN: Close closes it
	differ pipeline.Differ
	// warnings are the problems Open found that don't stop it.
	warnings []string
}

// Open discovers the project, compiles and lints the database's source, and
// checks its migration log against its snapshot. It doesn't connect.
func Open(cfg Config) (*Engine, error) {
	proj, err := project.Discover(cfg.Dir)
	if err != nil {
		return nil, err
	}
	cl, db, err := pick(proj, cfg.Cluster, cfg.Database)
	if err != nil {
		return nil, err
	}
	led := apply.LedgerFor(proj.RootConfig)
	if led == nil {
		return nil, fmt.Errorf("engine: %s/%s: the engine needs the ledger, which [ledger] turns off", cl.Name(), db.Name())
	}
	differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if err != nil {
		return nil, err
	}
	emitter, err := pipeline.MustResolve[pipeline.Emitter](pipeline.Default, pipeline.KeyEmitter)
	if err != nil {
		return nil, err
	}
	exec, err := pipeline.MustResolve[pipeline.ApplyExecutor](pipeline.Default, pipeline.KeyApplyExecutor)
	if err != nil {
		return nil, err
	}
	lintCfg := apply.LinterConfig(proj.RootConfig.Linter)
	lintCfg.MinPGVersion = db.EffectiveMinPGVersion(cl, proj.RootConfig)
	e := &Engine{cfg: cfg, differ: differ, opts: apply.Options{
		Preflight:     true,
		DPGDir:        proj.DPGDir(),
		MigrationsDir: proj.MigrationsDir(),
		LintCfg:       lintCfg,
		Ignore:        ignore.New(db.EffectiveIgnore(cl, proj.RootConfig)),
		Ledger:        led,
	}}
	// A store of its own: the registry's is shared, and dpg points it at
	// the project it runs in.
	store := &snapshotpkg.FileStore{Dir: proj.SnapshotDir(), Warn: func(msg string) { e.warnings = append(e.warnings, msg) }}
	e.t, err = apply.PrepareDatabase(cl, db, apply.Components{Store: store, Differ: differ, Emitter: emitter, Exec: exec}, e.connect, e.opts, apply.Printer{})
	if err != nil {
		return nil, fmt.Errorf("engine: %s/%s: %w", cl.Name(), db.Name(), err)
	}
	if n := len(e.t.Ops); n > 0 {
		return nil, fmt.Errorf("engine: %s: DPG-E056: the source has %d change(s) the migration log doesn't record; dpg plan --check lists them, and dpg plan --record records them", e.t.Label, n)
	}
	return e, nil
}

// pick finds the database cluster and database name, each of which may be
// empty when there is only one.
func pick(proj *project.Project, cluster, database string) (*project.Cluster, *project.Database, error) {
	var cls []*project.Cluster
	for _, cl := range proj.Clusters {
		if cluster == "" || cl.Name() == cluster {
			cls = append(cls, cl)
		}
	}
	if len(cls) != 1 {
		return nil, nil, fmt.Errorf("engine: %d clusters match %q in %s; name one", len(cls), cluster, proj.RootDir)
	}
	var dbs []*project.Database
	for _, db := range cls[0].Databases {
		if database == "" || db.Name() == database {
			dbs = append(dbs, db)
		}
	}
	if len(dbs) != 1 {
		return nil, nil, fmt.Errorf("engine: %d databases match %q in cluster %q; name one", len(dbs), database, cls[0].Name())
	}
	return cls[0], dbs[0], nil
}

// Warnings are the problems Open found that don't stop the engine, such as
// a snapshot written in another format version; dpg prints them as
// warnings.
func (e *Engine) Warnings() []string { return e.warnings }

// Close closes the connection the engine opened from DSN.
func (e *Engine) Close(ctx context.Context) error {
	if e.owned && e.conn != nil {
		err := e.conn.Close(ctx)
		e.conn = nil
		return err
	}
	return nil
}

// LedgerSchema is the schema DPG keeps its records in. dpg verify, plan
// and apply ignore what is in it, so a tool embedding the engine may keep
// its own records there too.
func (e *Engine) LedgerSchema() string { return e.opts.Ledger.Schema }

// connect returns the connection to run on, which the engine keeps.
func (e *Engine) connect(ctx context.Context) (*executor.PgxConn, error) {
	if e.conn == nil {
		switch {
		case e.cfg.Conn != nil:
			e.conn = e.cfg.Conn
		case e.cfg.DSN != "":
			conn, err := pgx.Connect(ctx, e.cfg.DSN)
			if err != nil {
				return nil, fmt.Errorf("engine: connect: %w", err)
			}
			e.conn, e.owned = conn, true
		default:
			return nil, ErrNoConnection
		}
	}
	return executor.Borrow(e.conn), nil
}

// State is where a database is in the migration log.
type State int

const (
	// UpToDate: the database has run every migration of the log.
	UpToDate State = iota + 1
	// Pending: the database has run a part of the log (or none of it),
	// and the rest can run.
	Pending
	// Drifted: the database's migrations aren't a part of the log: it ran
	// a migration the log doesn't have, of another branch or an edited
	// log, or it has the log's objects without a record of running them.
	Drifted
	// Ahead: the database ran every migration of the log, and more: a
	// newer build's.
	Ahead
)

func (s State) String() string {
	switch s {
	case UpToDate:
		return "up to date"
	case Pending:
		return "pending"
	case Drifted:
		return "drifted"
	case Ahead:
		return "ahead"
	}
	return "unknown"
}

// Status is where the database is in the migration log, as its ledger
// records. It doesn't compare the database's objects with the snapshot:
// Verify does.
type Status struct {
	State State
	// Expected is the log's last migration, Current the database's (""
	// for none).
	Expected, Current string
	// Pending are the migrations Apply would run, in order; the first may
	// be one whose steps after its transaction are still to be finished.
	Pending []string
	// Detail says more, for people.
	Detail string
}

// Status reads where the database is in the migration log. It changes
// nothing.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	conn, err := e.connect(ctx)
	if err != nil {
		return Status{}, err
	}
	log := e.t.Log
	s := Status{Expected: log.Head()}
	st, err := e.opts.Ledger.Read(ctx, conn, e.t.Key)
	if err != nil {
		return Status{}, fmt.Errorf("engine: read the ledger: %w", err)
	}
	drifted := func(detail string) (Status, error) {
		s.State, s.Detail = Drifted, detail
		return s, nil
	}
	// An attempt that never committed left nothing behind; one that
	// committed, then failed after its transaction, is finished by Apply
	// when the log has it.
	if u := st.Unfinished(); u != nil && u.Status == ledger.Committed && log.Find(u.ID) == nil {
		return drifted(fmt.Sprintf("migration %s committed in this database, but isn't in the migration log", u.ID))
	}
	head, err := st.Head()
	if err != nil {
		return drifted(err.Error())
	}
	s.Current = head
	start := 0
	switch {
	case head == "" && len(log.Entries) == 0:
	case head == "":
		fresh, err := e.t.Fresh(ctx, conn)
		if err != nil {
			return Status{}, fmt.Errorf("engine: %w", err)
		}
		if !fresh {
			return drifted("the database has objects of the migration log, but no record of running its migrations (dpg apply --baseline records how far it is)")
		}
	case log.Index(head) < 0:
		if r, ok := st.Rows[log.Head()]; ok && r.Status == ledger.Complete {
			s.State, s.Detail = Ahead, fmt.Sprintf("the database ran migration %s, after the log's last, %s", head, log.Head())
			return s, nil
		}
		return drifted(fmt.Sprintf("the database's last migration, %s, isn't in the migration log (it was made on another branch, or the log was rewritten)", head))
	default:
		start = log.Index(head) + 1
	}
	for _, en := range log.Entries[start:] {
		s.Pending = append(s.Pending, en.ID)
	}
	if len(s.Pending) == 0 {
		s.State = UpToDate
		return s, nil
	}
	s.State, s.Detail = Pending, fmt.Sprintf("%d migration(s) to run: %s", len(s.Pending), strings.Join(s.Pending, ", "))
	return s, nil
}

// Safety is how dangerous a statement is (RFC Section 17.2).
type Safety int

const (
	// Safe: no lock beyond the brief, no effect on data.
	Safe Safety = iota
	// Caution: it may lock or rewrite, or change access or integrity.
	Caution
	// Destructive: data can be lost.
	Destructive
	// UnknownSafety is a safety the log records that this engine doesn't
	// know (anything but SAFE, CAUTION or DESTRUCTIVE). It is above
	// Destructive: Apply never runs such a statement. Every call already
	// refuses a migration log with one when it loads it (DPG-E046), as dpg
	// does, so it is defensive.
	UnknownSafety
)

func (s Safety) String() string {
	if s < Safe || s > Destructive {
		return "unknown"
	}
	return pipeline.Safety(s).String()
}

// Mode is how a statement runs.
type Mode int

const (
	// InTransaction statements run in the migration's transaction.
	InTransaction Mode = iota
	// BeforeTransaction statements are committed one by one before it,
	// such as an enum value the transaction uses.
	BeforeTransaction
	// AfterTransaction statements run one by one after it commits, since
	// PostgreSQL refuses them in a transaction (CREATE INDEX
	// CONCURRENTLY). If one fails, or the context ends, the migration's
	// transaction has committed, and the next Apply runs them all again.
	// A CREATE INDEX CONCURRENTLY is made safe to run again then: an
	// index of its name that is invalid (a build that was cancelled or
	// failed) is dropped first, and a valid one is taken as built when it
	// is the index the statement builds, or refused when it isn't
	// (DPG-E058), as an index of its name on another table is (DPG-E057).
	// The drop isn't in the plan, nor its fingerprint. A build cancelled
	// may go on for a while on the server; the drop waits for it. The
	// rest are not changed: a grant repeats harmlessly, but a CREATE
	// SUBSCRIPTION that ran, or an ALTER TABLE ... DETACH PARTITION ...
	// CONCURRENTLY that was cancelled part way, still needs a person.
	AfterTransaction
	// Instruction steps are for a person to do; DPG never runs them.
	Instruction
	// UnknownMode is a stage the log records that this engine doesn't
	// know. Apply never runs a plan with such a statement. Every call
	// already refuses a migration log with one when it loads it
	// (DPG-E046), so it is defensive.
	UnknownMode
)

func (m Mode) String() string {
	switch m {
	case InTransaction:
		return "transaction"
	case BeforeTransaction:
		return "before"
	case AfterTransaction:
		return "after"
	case Instruction:
		return "instruction"
	}
	return "unknown"
}

// Step is one statement of a migration. Guards, the checks DPG runs before
// a migration's statements, aren't steps.
type Step struct {
	// ID is the migration's ID, "/" and the statement's position in it,
	// from 1.
	ID        string
	Migration string
	Safety    Safety
	Mode      Mode
	// SQL is the statement, with any secret references unresolved. The
	// engine never logs it.
	SQL string
}

// Plan is what Apply would run, in order.
type Plan struct {
	Steps []Step
}

// MaxSafety is the most dangerous of the plan's statements that DPG runs
// (Safe when there are none); a statement of a stage the engine doesn't
// know is UnknownSafety.
func (p Plan) MaxSafety() Safety {
	max := Safe
	for _, s := range p.Steps {
		if s.Mode == UnknownMode {
			return UnknownSafety
		}
		if s.Mode != Instruction && s.Safety > max {
			max = s.Safety
		}
	}
	return max
}

func stepsOf(en *migrationlog.Entry, afterOnly bool) []Step {
	var out []Step
	for i, s := range en.Steps {
		if s.Stage == migrationlog.StageCheck || afterOnly && s.Stage != migrationlog.StageAfter {
			continue
		}
		st := Step{ID: en.ID + "/" + strconv.Itoa(i+1), Migration: en.ID, SQL: s.SQL}
		switch s.Safety {
		case "SAFE":
			st.Safety = Safe
		case "CAUTION":
			st.Safety = Caution
		case "DESTRUCTIVE":
			st.Safety = Destructive
		default:
			st.Safety = UnknownSafety
		}
		switch s.Stage {
		case migrationlog.StageTransaction:
			st.Mode = InTransaction
		case migrationlog.StageBefore:
			st.Mode = BeforeTransaction
		case migrationlog.StageAfter:
			st.Mode = AfterTransaction
		case migrationlog.StageInstruction:
			st.Mode = Instruction
		default:
			st.Mode = UnknownMode
		}
		out = append(out, st)
	}
	return out
}

// work is what Apply would do, read without changing anything: the
// migrations the database hasn't run, and the one to finish.
func (e *Engine) work(ctx context.Context) (*apply.Work, apply.Options, error) {
	conn, err := e.connect(ctx)
	if err != nil {
		return nil, apply.Options{}, err
	}
	opts := e.opts
	st, err := opts.Ledger.Read(ctx, conn, e.t.Key)
	if err != nil {
		return nil, apply.Options{}, fmt.Errorf("engine: read the ledger: %w", err)
	}
	if u := st.Unfinished(); u != nil && u.Status == ledger.Committed {
		opts.Resume = true
	}
	w, err := e.t.Work(ctx, conn, nil, opts.Ledger, opts)
	if err != nil {
		return nil, apply.Options{}, fmt.Errorf("engine: %s: %w", e.t.Label, err)
	}
	return w, opts, nil
}

// Plan lists the statements Apply would run: those of the migrations the
// database hasn't run, and of one to finish, its steps after its
// transaction. It changes nothing.
func (e *Engine) Plan(ctx context.Context) (Plan, error) {
	w, _, err := e.work(ctx)
	if err != nil {
		return Plan{}, err
	}
	return planOf(w), nil
}

func planOf(w *apply.Work) Plan {
	var p Plan
	for _, en := range w.Pending {
		p.Steps = append(p.Steps, stepsOf(en, en == w.Resume)...)
	}
	return p
}

// PlanOffline lists the statements of every migration of the log, as if
// the database were empty, without a database.
func (e *Engine) PlanOffline() Plan {
	var p Plan
	for _, en := range e.t.Log.Entries {
		p.Steps = append(p.Steps, stepsOf(en, false)...)
	}
	return p
}

// ApplyOptions limit and watch what Apply does.
type ApplyOptions struct {
	// MaxSafety is the most dangerous statement Apply may run; the zero
	// value is Safe. A plan with a statement above it is refused before
	// anything runs.
	MaxSafety Safety
	// Instructions acknowledges Destructive instruction steps, which a
	// person must do (a partition strategy rebuild); a plan with one is
	// refused without it. DPG never runs them.
	Instructions bool
	// Hook, when set, runs first in each transaction of a migration, so
	// what it does commits with the migration's statements, or rolls back
	// with them; and before each statement that runs outside one, in a
	// transaction of its own. Its error stops the migration. A tool fences
	// a stale lease out here.
	Hook func(ctx context.Context, tx pgx.Tx) error
	// Observe, when set, is told as each migration starts, and returns the
	// context it runs in and the function to call with its outcome.
	Observe func(ctx context.Context, migration string) (context.Context, func(error))
}

// Result is what Apply did.
type Result struct {
	// Applied are the migrations applied, or finished, in order.
	Applied []string
}

// RefusedError is a plan Apply refused before running anything.
type RefusedError struct {
	// Steps are the statements refused.
	Steps []Step
	// Reason says why.
	Reason string
}

func (e *RefusedError) Error() string {
	ids := make([]string, len(e.Steps))
	for i, s := range e.Steps {
		ids[i] = fmt.Sprintf("%s (%s)", s.ID, s.Safety)
	}
	return fmt.Sprintf("engine: refused %s: %s; nothing was applied", strings.Join(ids, ", "), e.Reason)
}

// Apply runs the migrations the database hasn't run, in order, each in
// its transaction with its record in the ledger, and finishes one whose
// steps after its transaction failed. It refuses, before anything runs, a
// plan that opts don't allow, and a database that isn't as the migrations
// expect (dpg apply's preflight). A migration that fails stops the rest.
func (e *Engine) Apply(ctx context.Context, opts ApplyOptions) (Result, error) {
	w, run, err := e.work(ctx)
	if err != nil {
		return Result{}, err
	}
	var unknown, above, unacknowledged []Step
	for _, s := range planOf(w).Steps {
		switch {
		case s.Mode == UnknownMode || s.Mode != Instruction && s.Safety == UnknownSafety:
			unknown = append(unknown, s)
		case s.Mode == Instruction && s.Safety >= Destructive && !opts.Instructions:
			unacknowledged = append(unacknowledged, s)
		case s.Mode != Instruction && s.Safety > opts.MaxSafety:
			above = append(above, s)
		}
	}
	if len(unknown) > 0 {
		return Result{}, &RefusedError{Steps: unknown, Reason: "of a safety or stage this engine doesn't know"}
	}
	if len(above) > 0 {
		return Result{}, &RefusedError{Steps: above, Reason: "above the maximum safety, " + opts.MaxSafety.String()}
	}
	if len(unacknowledged) > 0 {
		return Result{}, &RefusedError{Steps: unacknowledged, Reason: "a person must do these, which isn't acknowledged"}
	}
	run.AllowDestructive = opts.MaxSafety >= Destructive
	run.RefuseCaution = opts.MaxSafety < Caution
	run.ApprovePartitionRebuild = opts.Instructions
	run.Hook = opts.Hook
	var res Result
	run.Observe = func(ctx context.Context, id string) (context.Context, func(error)) {
		done := func(error) {}
		if opts.Observe != nil {
			ctx, done = opts.Observe(ctx, id)
		}
		return ctx, func(err error) {
			if err == nil {
				res.Applied = append(res.Applied, id)
			}
			done(err)
		}
	}
	if err := e.t.Run(ctx, run, apply.Printer{}); err != nil {
		var pf *apply.PreflightError
		if errors.As(err, &pf) {
			err = fmt.Errorf("%w: %s", err, strings.Join(pf.Problems, "; "))
		}
		return res, fmt.Errorf("engine: %s: %w", e.t.Label, err)
	}
	return res, nil
}

// Finding is one way the database differs from the snapshot.
type Finding struct {
	// ID names the object and property, such as table:public.t/comment.
	ID string
	// Group is drift, conflict, converged, unmanaged or warning, as dpg
	// verify groups them, or log for the database's place in the
	// migration log.
	Group string
	// Failing findings are drift: dpg verify exits 1 for them. Unmanaged
	// objects, warnings and acknowledged items aren't.
	Failing bool
	Detail  string
}

// Verify compares the database's objects with the snapshot at its place in
// the migration log, as dpg verify does, with the project's
// acknowledgements. A database whose last migration isn't in the log is
// compared with the snapshot, and its place is a finding. It changes
// nothing.
func (e *Engine) Verify(ctx context.Context) ([]Finding, error) {
	conn, err := e.connect(ctx)
	if err != nil {
		return nil, err
	}
	introspector, err := pipeline.MustResolve[pipeline.Introspector](pipeline.Default, pipeline.KeyIntrospector)
	if err != nil {
		return nil, err
	}
	v := apply.Verification{
		Cluster: e.t.Cluster, Key: e.t.Key,
		Introspector: introspector, Differ: e.differ, Rules: e.t.Rules,
		Position: apply.LogPosition{Dir: e.opts.MigrationsDir, Ledger: e.opts.Ledger},
		Source:   func() ([]pipeline.IRObject, error) { return e.t.Desired, nil },
	}
	var out []Finding
	r, err := apply.Verify(ctx, conn, snapshotOf(e.t.Stored), v, apply.Printer{})
	var off *apply.OffLogError
	if errors.As(err, &off) {
		f := Finding{ID: "log:" + off.Head, Group: "log", Failing: true,
			Detail: fmt.Sprintf("the database's last migration, %s, isn't in the migration log", off.Head)}
		if off.Behind != "" {
			f.Detail += fmt.Sprintf(" (it ran the log's last, %s, before it: a newer build's)", off.Behind)
		}
		out = append(out, f)
		v.Position = apply.LogPosition{}
		r, err = apply.Verify(ctx, conn, snapshotOf(e.t.Stored), v, apply.Printer{})
	}
	if err != nil {
		return nil, fmt.Errorf("engine: %s: %w", e.t.Label, err)
	}
	if e.t.AckFile != "" {
		acks, err := drift.LoadAcks(e.t.AckFile)
		if err != nil {
			return nil, err
		}
		drift.ApplyAcks(r, acks, time.Now())
	}
	for _, it := range r.Items {
		out = append(out, Finding{ID: it.ID, Group: it.Group.String(), Failing: it.Failing(), Detail: apply.Describe(it)})
	}
	return out, nil
}

// snapshotOf copies a stored snapshot, which Verify filters in place.
func snapshotOf(s *pipeline.Snapshot) *pipeline.Snapshot {
	c := *s
	c.Objects = apply.CopyObjects(s.Objects)
	return &c
}
