package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/precondition"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

// Gate refuses statements the options don't allow: DESTRUCTIVE ones without
// --allow-destructive, a DESTRUCTIVE manual step without
// --approve-partition-rebuild, and CAUTION ones under RefuseCaution.
func Gate(ops []pipeline.DiffOp, opts Options, what string) error {
	for _, op := range ops {
		instruction := op.Mode() == pipeline.ModeInstructionOnly
		if op.Safety() == pipeline.Destructive && !instruction && !opts.AllowDestructive {
			return fmt.Errorf("DPG-E025: %s contains destructive operations; re-run with --allow-destructive to proceed\n  first: %s", what, op.SQL())
		}
		if op.Safety() == pipeline.Destructive && instruction && !opts.ApprovePartitionRebuild {
			return fmt.Errorf("%s requires a manual partition strategy rebuild;\nre-run with --approve-partition-rebuild to acknowledge and proceed\n  first: %s", what, op.SQL())
		}
		if op.Safety() == pipeline.Caution && !instruction && opts.RefuseCaution {
			return fmt.Errorf("%s contains CAUTION operations, which aren't allowed here\n  first: %s", what, op.SQL())
		}
	}
	return nil
}

func allOps(m pipeline.Migration) []pipeline.DiffOp {
	return append(append(append([]pipeline.DiffOp(nil), m.PreTransactional...), m.Transactional...), m.NonTransactional...)
}

func executable(ops []pipeline.DiffOp) []pipeline.DiffOp {
	var out []pipeline.DiffOp
	for _, op := range ops {
		if op.Mode() != pipeline.ModeInstructionOnly {
			out = append(out, op)
		}
	}
	return out
}

// Work is what a run does: the log's migrations the database hasn't run,
// then the source's change.
type Work struct {
	// Pending are the log's migrations the database hasn't run, in order;
	// Resume, one of them, is the one whose steps after its transaction
	// are to be finished.
	Pending []*migrationlog.Entry
	Resume  *migrationlog.Entry
	// Head is the database's last migration ("" for none).
	Head string
	// Next is the source's change, as a log entry; nil without one.
	Next    *migrationlog.Entry
	full    pipeline.Migration
	newSnap *pipeline.Snapshot
}

// ToRun are the migrations a run runs, in order.
func (w *Work) ToRun() []*migrationlog.Entry {
	out := append([]*migrationlog.Entry(nil), w.Pending...)
	if w.Next != nil {
		out = append(out, w.Next)
	}
	return out
}

// Work finds what a run does. With a ledger it reads where the database is
// (conn); w, when not nil, forgets an attempt that never committed of a
// migration that was never recorded (nil leaves the database unchanged).
func (t *Target) Work(ctx context.Context, conn *executor.PgxConn, w pipeline.Conn, led *ledger.Ledger, opts Options) (*Work, error) {
	work := &Work{Head: t.Log.Head()}
	if led != nil {
		var err error
		if work.Pending, work.Resume, work.Head, err = t.pending(ctx, conn, w, led, opts); err != nil {
			return nil, err
		}
	}

	// The source's change, as a log entry: its guards, then its steps.
	if len(t.Ops) > 0 {
		var err error
		if work.full, err = t.Emitter.Emit(t.Ops, t.Meta); err != nil {
			return nil, err
		}
		work.newSnap = &pipeline.Snapshot{}
		if err := snapshotpkg.Populate(work.newSnap, t.Desired); err != nil {
			return nil, fmt.Errorf("build snapshot: %w", err)
		}
		if work.Next, err = migrationlog.New(t.Log.Head(), GuardedSteps(work.full, work.newSnap.Objects, opts.Ledger, t.Key), migrationlog.OriginApply, t.Stored.Objects, work.newSnap.Objects, time.Now()); err != nil {
			return nil, err
		}
	}
	return work, nil
}

// observe starts a migration's observation (Options.Observe).
func (opts Options) observe(ctx context.Context, id string) (context.Context, func(error)) {
	if opts.Observe == nil {
		return ctx, func(error) {}
	}
	return opts.Observe(ctx, id)
}

// Run applies the target: it settles the log's migrations the database
// hasn't run (in order, through the ledger), then plans, runs and records
// the source's pending change. Before anything runs, the preflight checks
// the database against what the migrations expect (RFC Section 16.10).
func (t *Target) Run(ctx context.Context, opts Options, p Printer) error {
	if opts.Baseline {
		return t.baseline(ctx, opts, p)
	}
	led := opts.Ledger
	if opts.NoSnapshot {
		led = nil // nothing is recorded: not the log, not the ledger
	}

	var conn *executor.PgxConn
	var w pipeline.Conn
	defer func() {
		if conn != nil {
			conn.Close(ctx)
		}
	}()
	connect := func() error {
		if conn != nil {
			return nil
		}
		var err error
		if conn, err = t.Connect(ctx); err != nil {
			return err
		}
		w = conn
		if opts.Hook != nil {
			w = executor.Hooked(conn, opts.Hook)
		}
		return nil
	}
	if led != nil {
		if err := connect(); err != nil {
			return err
		}
	}
	work, err := t.Work(ctx, conn, w, led, opts)
	if err != nil {
		return err
	}
	pending, resume, head, next := work.Pending, work.Resume, work.Head, work.Next
	toRun := work.ToRun()
	if next != nil && led != nil {
		t.noteUnguarded(next, p)
	}

	// Gate and show everything that will run.
	if err := Gate(t.Ops, opts, "the migration"); err != nil {
		return err
	}
	for _, e := range toRun {
		m := e.Migration(t.Meta, true, false)
		ops := allOps(m)
		if e == resume {
			// Its transaction has committed: only the statements after it
			// run, so only they need allowing.
			ops = m.NonTransactional
		}
		if err := Gate(ops, opts, "migration "+e.ID); err != nil {
			return err
		}
	}
	if len(toRun) == 0 && resume == nil {
		p.info(t.Label, "already up to date")
		return nil
	}
	if resume != nil {
		p.info(t.Label, "finishing migration "+resume.ID+" (the steps after its transaction)")
	}
	for _, e := range pending {
		if e == resume {
			continue
		}
		p.heading(fmt.Sprintf("-- %s: migration %s (%s), from the migration log", t.Label, e.ID, e.Origin))
		if err := p.render(e.Migration(t.Meta, true, false), false); err != nil {
			return err
		}
	}
	if next != nil {
		if p.Err != nil {
			warnMissingPK(p, t.Desired, t.Stored)
		}
		if len(pending) > 0 {
			p.heading("-- " + t.Label + ": the source's change")
		}
		if err := p.render(work.full, true); err != nil {
			return err
		}
	}

	// The preflight: the database is as the migrations expect.
	var verified map[string]string
	if opts.Preflight {
		if err := connect(); err != nil {
			return err
		}
		if verified, err = t.preflight(ctx, conn, head, resume, toRun, opts, p); err != nil {
			return err
		}
	}
	if opts.DryRun {
		p.info("dry-run", "nothing run; snapshot, migration log and ledger not updated")
		return nil
	}
	if !p.confirm("Apply?") {
		p.info("", "Aborted.")
		return nil
	}
	if err := connect(); err != nil {
		return err
	}

	// RFC Section 11.5: the connecting role must be a member of every role
	// a SET ROLE/OWNER TO statement targets, checked before anything runs.
	var all []pipeline.DiffOp
	for _, e := range toRun {
		all = append(all, executable(allOps(e.Migration(t.Meta, false, false)))...)
	}
	if err := executor.ValidateOwnerMembership(ctx, conn, all); err != nil {
		return err
	}

	// The objects the preflight checked are recorded as seen, so the
	// migrations' fingerprint guards pass.
	if led != nil && !opts.AllowDrift && len(verified) > 0 {
		if err := RecordFingerprints(ctx, w, led, t.Key, verified, "preflight"); err != nil {
			return err
		}
	}

	// Settle.
	checks := !opts.AllowDrift
	for _, e := range pending {
		mctx, done := opts.observe(ctx, e.ID)
		if e == resume {
			m := e.Migration(t.Meta, false, false)
			after, err := prepareFinish(mctx, conn, m.NonTransactional)
			if err != nil {
				err = fmt.Errorf("finishing migration %s: %w", e.ID, ui.WrapDB(err))
				done(err)
				return err
			}
			finish := led.Finished(pipeline.Migration{Meta: t.Meta, NonTransactional: after}, ledger.Of(t.Key, e))
			if err := t.Exec.Apply(mctx, finish, w); err != nil {
				err = fmt.Errorf("finishing migration %s: %w", e.ID, ui.WrapDB(err))
				done(err)
				return err
			}
			done(nil)
			p.success("Finished", t.Label+" — migration "+e.ID)
			continue
		}
		if err := t.Exec.Apply(mctx, led.Wrap(e.Migration(t.Meta, false, checks), ledger.Of(t.Key, e)), w); err != nil {
			err = afterCommitHint(e.ID, err, opts.FinishHint)
			done(err)
			return err
		}
		done(nil)
		p.success("Applied", t.Label+" — migration "+e.ID+" from the log")
	}
	if next == nil {
		return nil
	}

	// The source's change: run it, then record it in the log and the
	// snapshot (also when the steps after its transaction failed: the
	// database has the transaction's changes).
	execM := next.Migration(t.Meta, false, checks)
	fileM := next.Migration(t.Meta, true, true)
	if led != nil {
		execM = led.Wrap(execM, ledger.Of(t.Key, next))
		fileM = led.Wrap(fileM, ledger.Of(t.Key, next))
	}
	mctx, done := opts.observe(ctx, next.ID)
	runErr := t.Exec.Apply(mctx, execM, w)
	done(runErr)
	var after *executor.AfterCommitError
	if runErr != nil && !errors.As(runErr, &after) {
		return ui.WrapDB(runErr)
	}
	if opts.NoSnapshot {
		if runErr != nil {
			return ui.WrapDB(runErr)
		}
		p.success("Applied", t.Label+" — snapshot and migration log not updated (--no-snapshot)")
		return nil
	}
	var file strings.Builder
	if err := emit.Render(&file, fileM, emit.DefaultRenderOptions()); err != nil {
		return err
	}
	undo, err := RecordEntry(t.Log, next, file.String(), work.newSnap)
	if err != nil {
		return err
	}
	if err := t.Store.Save(t.Cluster, t.Key, work.newSnap); err != nil {
		undo()
		return fmt.Errorf("save snapshot: %w", err)
	}
	if runErr != nil {
		return afterCommitHint(next.ID, runErr, opts.FinishHint)
	}
	path, _ := t.Log.Paths(next.ID)
	p.success("Applied", t.Label+" — snapshot updated\n         "+ui.Dim(path, p.Color))
	return nil
}

func afterCommitHint(id string, err error, hint string) error {
	var after *executor.AfterCommitError
	if errors.As(err, &after) {
		if hint == "" {
			hint = "the next apply finishes it"
		}
		return fmt.Errorf("migration %s: its transaction committed, then a step after it failed: %w\n  %s", id, ui.WrapDB(after.Err), hint)
	}
	return fmt.Errorf("migration %s: %w", id, ui.WrapDB(err))
}

// pending returns the log's migrations the database hasn't run, after its
// ledger's last one, the migration to finish with --resume, and the
// database's last migration.
func (t *Target) pending(ctx context.Context, conn *executor.PgxConn, w pipeline.Conn, led *ledger.Ledger, opts Options) ([]*migrationlog.Entry, *migrationlog.Entry, string, error) {
	st, err := led.Read(ctx, conn, t.Key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read the ledger: %w", ui.WrapDB(err))
	}
	var resume *migrationlog.Entry
	if u := st.Unfinished(); u != nil {
		e := t.Log.Find(u.ID)
		switch {
		case u.Status == ledger.Started && e == nil:
			// An attempt whose transaction never committed, of a migration
			// that was never recorded: nothing of it remains.
			if w != nil {
				if _, err := w.Exec(ctx, led.Forget(ledger.Migration{Log: t.Key, ID: u.ID}, ledger.Started)); err != nil {
					return nil, nil, "", ui.WrapDB(err)
				}
			}
			delete(st.Rows, u.ID)
		case u.Status == ledger.Committed && e == nil:
			return nil, nil, "", fmt.Errorf("DPG-E049: migration %s committed in this database but isn't in the migration log", u.ID)
		case u.Status == ledger.Committed && !opts.Resume:
			return nil, nil, "", fmt.Errorf("DPG-E049: migration %s failed after its transaction committed; dpg apply --resume finishes it", u.ID)
		case u.Status == ledger.Committed:
			resume = e
		}
	}
	head, err := st.Head()
	if err != nil {
		return nil, nil, "", err
	}
	if opts.Resume && resume == nil {
		return nil, nil, "", fmt.Errorf("--resume: no migration of %s is waiting to be finished", t.Label)
	}
	start := 0
	switch {
	case head != "":
		i := t.Log.Index(head)
		if i < 0 {
			return nil, nil, "", fmt.Errorf("DPG-E050: the last migration this database ran, %s, isn't in the migration log (was it made on another branch?)", head)
		}
		start = i + 1
	case len(t.Log.Entries) > 0:
		fresh, err := t.Fresh(ctx, conn)
		if err != nil {
			return nil, nil, "", err
		}
		if !fresh {
			return nil, nil, "", fmt.Errorf("DPG-E050: %s has objects of the migration log but no record of running its migrations; if it already has them, dpg apply --baseline records how far it is", t.Label)
		}
	}
	pending := t.Log.Entries[start:]
	if resume != nil && (len(pending) == 0 || pending[0] != resume) {
		return nil, nil, "", fmt.Errorf("DPG-E049: migration %s is waiting to be finished but doesn't follow this database's last migration", resume.ID)
	}
	return pending, resume, head, nil
}

// Fresh reports whether none of the objects the migration log ends with
// exist in the database (the public schema aside): such a database runs the
// whole log.
func (t *Target) Fresh(ctx context.Context, conn *executor.PgxConn) (bool, error) {
	objects, err := t.Log.Replay("")
	if err != nil {
		return false, err
	}
	live, err := t.Live(ctx, conn)
	if err != nil {
		return false, ui.WrapDB(err)
	}
	for _, o := range live {
		if k := o.QualifiedName(); k != "public" && objects[k] != nil {
			return false, nil
		}
	}
	return true, nil
}

// baseline is dpg apply --baseline: it starts the migration log of a
// snapshot that predates it, and records in the database's ledger how far
// along the log the database is.
func (t *Target) baseline(ctx context.Context, opts Options, p Printer) error {
	switch {
	case errors.Is(t.LogErr, migrationlog.ErrNoBaseline) && opts.DryRun:
		p.info("dry-run", t.Label+": would record the snapshot as the baseline migration")
	case errors.Is(t.LogErr, migrationlog.ErrNoBaseline):
		e, err := WriteBaseline(t.Log, t.Store, t.Cluster, t.Key, t.Stored, t.Desired, t.Ops, t.Differ, t.Emitter, t.Meta, opts.Ledger)
		if err != nil {
			return err
		}
		path, _ := t.Log.Paths(e.ID)
		p.success("Baseline", t.Label+" — migration log started\n         "+ui.Dim(path, p.Color))
	case t.LogErr != nil:
		return t.LogErr
	case len(t.Log.Entries) == 0:
		p.info(t.Label, "nothing to record: the snapshot is empty")
		return nil
	default:
		p.info(t.Label, "the migration log started at "+t.Log.Entries[0].ID)
	}
	if opts.Ledger == nil || len(t.Log.Entries) == 0 {
		return nil
	}

	conn, err := t.Connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	st, err := opts.Ledger.Read(ctx, conn, t.Key)
	if err != nil {
		return ui.WrapDB(err)
	}
	if head, err := st.Head(); err != nil {
		return err
	} else if head != "" {
		p.info(t.Label, "the database already records its migrations (last: "+head+")")
		return nil
	}
	// The newest point of the log the database matches.
	live, err := t.Live(ctx, conn)
	if err != nil {
		return ui.WrapDB(err)
	}
	at := -1
	var clean map[string]string
	for i := len(t.Log.Entries) - 1; i >= 0 && at < 0; i-- {
		objects, err := t.Log.Replay(t.Log.Entries[i].ID)
		if err != nil {
			return err
		}
		report, err := drift.Detect(t.Differ, live, &pipeline.Snapshot{Objects: objects}, drift.Options{Ignore: t.Rules})
		if err != nil {
			return err
		}
		if !report.HasDrift() {
			at = i
			clean = CleanObjects(ObjectKinds(objects), report)
		}
	}
	if at < 0 {
		return fmt.Errorf("DPG-E050: %s doesn't match any point of the migration log; dpg verify shows how it differs from the snapshot", t.Label)
	}
	upTo := t.Log.Entries[at]
	if opts.DryRun {
		p.info("dry-run", fmt.Sprintf("%s: would record migrations up to %s (%d of %d)", t.Label, upTo.ID, at+1, len(t.Log.Entries)))
		return nil
	}
	m := pipeline.Migration{Meta: t.Meta}
	for _, s := range opts.Ledger.Bootstrap() {
		m.Transactional = append(m.Transactional, ledgerStmt(s))
	}
	for _, e := range t.Log.Entries[:at+1] {
		m.Transactional = append(m.Transactional, ledgerStmt(opts.Ledger.Record(ledger.Of(t.Key, e))))
	}
	if err := t.Exec.Apply(ctx, m, conn); err != nil {
		return ui.WrapDB(err)
	}
	if err := RecordFingerprints(ctx, conn, opts.Ledger, t.Key, clean, "baseline"); err != nil {
		return err
	}
	p.success("Recorded", fmt.Sprintf("%s — at migration %s (%d of %d); dpg apply runs the rest", t.Label, upTo.ID, at+1, len(t.Log.Entries)))
	return nil
}

// ledgerStmt is a ledger statement run in a transaction.
type ledgerStmt string

func (s ledgerStmt) SQL() string             { return string(s) }
func (s ledgerStmt) Safety() pipeline.Safety { return pipeline.Safe }
func (s ledgerStmt) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (s ledgerStmt) Mode() pipeline.ExecMode { return pipeline.ModeTransactional }

// PreflightError is the preflight's refusal: the database isn't as the
// migrations expect. Problems lists why.
type PreflightError struct {
	Problems []string
	Err      error
}

func (e *PreflightError) Error() string { return e.Err.Error() }
func (e *PreflightError) Unwrap() error { return e.Err }

// preflight checks the database against what the migrations about to run
// expect (RFC Section 16.10): no drift, conflict or converged item on an
// object they change (an acknowledged one only on a property they don't
// change), no object they create already there, nothing DPG doesn't manage
// depending on an object they drop or recreate, no grant made outside DPG
// on an object they recreate. Drift elsewhere is a note. head is the
// database's position in the log.
func (t *Target) preflight(ctx context.Context, conn *executor.PgxConn, head string, resume *migrationlog.Entry, toRun []*migrationlog.Entry, opts Options, p Printer) (map[string]string, error) {
	// The state the database should be in: the log at its position. A
	// migration being finished has committed its transaction, so the
	// database is at it, less what its statements after the transaction
	// add, which are reported but don't block.
	if resume != nil {
		head = resume.ID
	}
	var expected map[string]json.RawMessage
	atHead := head == t.Log.Head()
	switch {
	case atHead:
		expected = CopyObjects(t.Stored.Objects)
	case head == "":
		expected = map[string]json.RawMessage{}
	default:
		var err error
		if expected, err = t.Log.Replay(head); err != nil {
			return nil, err
		}
	}
	snap := &pipeline.Snapshot{Objects: expected}
	if err := t.Rules.FilterSnapshot(snap); err != nil {
		return nil, err
	}
	live, err := t.Live(ctx, conn)
	if err != nil {
		return nil, ui.WrapDB(err)
	}
	var source []pipeline.IRObject
	if atHead {
		source = t.Desired // the source is the database's next state only at the log's end
	}
	report, err := drift.Detect(t.Differ, live, snap, drift.Options{Ignore: t.Rules, Source: source})
	if err != nil {
		return nil, err
	}
	if t.AckFile != "" {
		acks, err := drift.LoadAcks(t.AckFile)
		if err != nil {
			return nil, err
		}
		drift.ApplyAcks(report, acks, time.Now())
	}

	// What the migrations do to each object.
	type touch struct {
		kind                    string
		created, dropped, recre bool
	}
	touched := map[string]*touch{}
	changed := map[string]map[string]bool{}
	state := CopyObjects(expected)
	for _, e := range toRun {
		if e == resume {
			continue // expected already has its changes
		}
		for _, s := range e.Steps {
			if s.Target == nil || s.Target.Key == "" || s.Target.Kind == "ledger" {
				continue
			}
			for _, k := range []string{s.Target.Key, s.Target.From} {
				if k == "" {
					continue
				}
				tc := touched[k]
				if tc == nil {
					tc = &touch{kind: s.Target.Kind}
					touched[k] = tc
				}
				switch s.Target.Effect {
				case pipeline.EffectCreate:
					tc.created = true
				case pipeline.EffectDrop:
					tc.dropped = true
				case pipeline.EffectRecreate:
					tc.recre = true
				}
			}
		}
		for k, v := range e.Set {
			cp, err := drift.ChangedProperties(state[k], v)
			if err != nil {
				return nil, err
			}
			if changed[k] == nil {
				changed[k] = map[string]bool{}
			}
			for p := range cp {
				changed[k][p] = true
			}
		}
		for _, k := range e.Remove {
			if changed[k] == nil {
				changed[k] = map[string]bool{}
			}
			changed[k][""] = true
		}
		e.Apply(state)
	}

	var blocks, notes []string
	destructive := false
	for _, it := range report.Items {
		tc := touched[it.Object]
		switch {
		case tc == nil:
			if it.Failing() {
				notes = append(notes, it.ID)
			}
		case it.Group == drift.Unmanaged:
			if tc.created {
				blocks = append(blocks, fmt.Sprintf("%s: the migration creates it, but it already exists (dpg resolve can adopt or ignore it)", it.ID))
			}
		case it.Group == drift.Warning && it.Grant != nil:
			if tc.recre {
				blocks = append(blocks, fmt.Sprintf("%s: the migration recreates the object, which loses this grant made outside DPG (adopt it with dpg resolve)", it.ID))
			}
		case it.Group == drift.Drift || it.Group == drift.Conflict || it.Group == drift.Converged:
			if it.Acknowledged == nil {
				blocks = append(blocks, fmt.Sprintf("%s: %s on an object the migration changes", it.ID, it.Group))
			} else if drift.Touches(it, changed[it.Object]) {
				blocks = append(blocks, fmt.Sprintf("%s: acknowledged, but the migration changes it", it.ID))
			}
		}
	}
	managed := map[string]bool{}
	for k := range state {
		managed[k] = true
	}
	for k, tc := range touched {
		if tc.dropped {
			managed[k] = true
		}
	}
	var keys []string
	for k := range touched {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tc := touched[k]
		if !tc.dropped && !tc.recre {
			continue
		}
		deps, err := precondition.Unmanaged(ctx, conn, tc.kind, k, managed)
		if err != nil {
			return nil, ui.WrapDB(err)
		}
		for _, d := range deps {
			destructive = true
			blocks = append(blocks, fmt.Sprintf("%s:%s: the migration drops or recreates it, and %s depends on it but DPG doesn't manage it", tc.kind, k, d))
		}
	}

	if len(notes) > 0 {
		sort.Strings(notes)
		p.note("note", fmt.Sprintf("%s: drift on objects these migrations don't touch: %s", t.Label, strings.Join(notes, ", ")))
	}
	// The objects the migrations change that the database has, checked:
	// DPG has seen them as they are (their fingerprints).
	verified := map[string]string{}
	for k, tc := range touched {
		if expected[k] != nil {
			verified[k] = tc.kind
		}
	}
	if len(blocks) == 0 {
		return verified, nil
	}
	sort.Strings(blocks)
	if p.Err != nil {
		fmt.Fprintf(p.Err, "\n%s\n", ui.Bold(fmt.Sprintf("%s: the database isn't as the migrations expect", t.Label), p.ErrColor))
		for _, b := range blocks {
			fmt.Fprintf(p.Err, "  %s\n", b)
		}
	}
	if opts.AllowDrift && (!destructive || opts.AllowDestructive) {
		p.note("warn", "continuing anyway (--allow-drift)")
		return nil, nil
	}
	if opts.AllowDrift {
		return nil, &ui.ExitError{Code: 1, Err: &PreflightError{Problems: blocks, Err: fmt.Errorf("DPG-E052: running it would drop objects DPG doesn't manage; --allow-drift with --allow-destructive proceeds")}}
	}
	return nil, &ui.ExitError{Code: 1, Err: &PreflightError{Problems: blocks, Err: fmt.Errorf("DPG-E053: %d problem(s); dpg resolve fixes them, or --allow-drift proceeds anyway", len(blocks))}}
}

// noteUnguarded notes the objects a migration changes whose kind has no
// fingerprint: another runner's guards can't tell whether they drifted.
func (t *Target) noteUnguarded(e *migrationlog.Entry, p Printer) {
	seen := map[string]bool{}
	var out []string
	for _, st := range e.Steps {
		tg := st.Target
		if tg == nil || tg.Kind == "ledger" || tg.Effect == pipeline.EffectCreate || seen[tg.Kind+":"+tg.Key] {
			continue
		}
		seen[tg.Kind+":"+tg.Key] = true
		if precondition.Fingerprint(tg.Kind, tg.Key) == "" {
			out = append(out, tg.Kind+":"+tg.Key)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		p.note("note", fmt.Sprintf("%s: run by another tool, this migration can't check these for drift (no fingerprint for their kind): %s", t.Label, strings.Join(out, ", ")))
	}
}

// warnMissingPK writes a bold warning for every table that is being newly
// created (absent from snap) and has no PRIMARY KEY constraint.
func warnMissingPK(p Printer, desired []pipeline.IRObject, snap *pipeline.Snapshot) {
	for _, obj := range desired {
		tbl, ok := obj.(*ir.Table)
		if !ok {
			continue
		}
		// Only warn for new tables — existing ones are the user's responsibility.
		if _, exists := snap.Objects[tbl.QualifiedName()]; exists {
			continue
		}
		hasPK := false
		for _, cst := range tbl.Constraints {
			if cst.Type == "PRIMARY KEY" {
				hasPK = true
				break
			}
		}
		if !hasPK {
			fmt.Fprintf(p.Err, "%s  table %s has no PRIMARY KEY — consider adding one\n",
				ui.Bold("WARNING", p.ErrColor), tbl.QualifiedName())
		}
	}
}
