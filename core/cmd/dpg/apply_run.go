package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/precondition"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

// applyTarget is one database, or a cluster's cluster-level objects, being
// applied: its compiled source, stored snapshot and migration log, and how
// to reach it.
type applyTarget struct {
	label        string // "prod/app" or "prod (cluster)"
	cluster, key string // key: the database name or the cluster snapshot key (also the ledger's log)
	store        pipeline.SnapshotStore
	differ       pipeline.Differ
	emitter      pipeline.Emitter
	exec         pipeline.ApplyExecutor
	connect      func(ctx context.Context) (*executor.PgxConn, error)
	// live introspects the target's objects ([ignore] applied).
	live    func(ctx context.Context, conn *executor.PgxConn) ([]pipeline.IRObject, error)
	rules   *ignore.Rules
	ackFile string // the target's acknowledgements ("" for none)
	desired []pipeline.IRObject
	stored  *pipeline.Snapshot // as on disk
	log     *migrationlog.Log
	logErr  error // migrationlog.ErrNoBaseline when the snapshot predates its log
	ops     []pipeline.DiffOp
	meta    pipeline.MigrationMeta
}

// confirm asks the operator unless --yes.
func confirm(question string, opts applyOptions, color bool) bool {
	if opts.yes {
		return true
	}
	fmt.Printf("\n%s [y/N] ", ui.Bold(question, color))
	scanner := bufio.NewScanner(os.Stdin)
	return scanner.Scan() && strings.EqualFold(strings.TrimSpace(scanner.Text()), "y")
}

// gate refuses statements the flags don't allow: DESTRUCTIVE ones without
// --allow-destructive, a DESTRUCTIVE manual step without
// --approve-partition-rebuild.
func gate(ops []pipeline.DiffOp, opts applyOptions, what string) error {
	for _, op := range ops {
		instruction := op.Mode() == pipeline.ModeInstructionOnly
		if op.Safety() == pipeline.Destructive && !instruction && !opts.allowDestructive {
			return fmt.Errorf("DPG-E025: %s contains destructive operations; re-run with --allow-destructive to proceed\n  first: %s", what, op.SQL())
		}
		if op.Safety() == pipeline.Destructive && instruction && !opts.approvePartitionRebuild {
			return fmt.Errorf("%s requires a manual partition strategy rebuild;\nre-run with --approve-partition-rebuild to acknowledge and proceed\n  first: %s", what, op.SQL())
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

// runTarget applies one target: it settles the log's migrations the
// database hasn't run (in order, through the ledger), then plans, runs and
// records the source's pending change. Before anything runs, the preflight
// checks the database against what the migrations expect (RFC Section
// 16.10).
func runTarget(t *applyTarget, opts applyOptions) error {
	ctx := context.Background()
	color := ui.IsColorEnabled(os.Stdout)
	if opts.baseline {
		return t.baseline(ctx, opts, color)
	}
	led := opts.ledger
	if opts.noSnapshot {
		led = nil // nothing is recorded: not the log, not the ledger
	}

	var conn *executor.PgxConn
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
		conn, err = t.connect(ctx)
		return err
	}
	var pending []*migrationlog.Entry
	var resume *migrationlog.Entry
	head := t.log.Head()
	if led != nil {
		if err := connect(); err != nil {
			return err
		}
		var err error
		if pending, resume, head, err = t.pending(ctx, conn, led, opts); err != nil {
			return err
		}
	}

	// The source's change, as a log entry: its guards, then its steps.
	var full pipeline.Migration
	var next *migrationlog.Entry
	var newSnap *pipeline.Snapshot
	if len(t.ops) > 0 {
		var err error
		if full, err = t.emitter.Emit(t.ops, t.meta); err != nil {
			return err
		}
		newSnap = &pipeline.Snapshot{}
		if err := snapshotpkg.Populate(newSnap, t.desired); err != nil {
			return fmt.Errorf("build snapshot: %w", err)
		}
		if next, err = migrationlog.New(t.log.Head(), guardedSteps(full, newSnap.Objects), migrationlog.OriginApply, t.stored.Objects, newSnap.Objects, time.Now()); err != nil {
			return err
		}
	}
	toRun := append([]*migrationlog.Entry(nil), pending...)
	if next != nil {
		toRun = append(toRun, next)
	}

	// Gate and show everything that will run.
	if err := gate(t.ops, opts, "the migration"); err != nil {
		return err
	}
	for _, e := range toRun {
		if err := gate(allOps(e.Migration(t.meta, true, false)), opts, "migration "+e.ID); err != nil {
			return err
		}
	}
	if len(toRun) == 0 && resume == nil {
		ui.PrintInfo(os.Stdout, t.label, "already up to date", color)
		return nil
	}
	if resume != nil {
		ui.PrintInfo(os.Stdout, t.label, "finishing migration "+resume.ID+" (the steps after its transaction)", color)
	}
	for _, e := range pending {
		if e == resume {
			continue
		}
		fmt.Printf("\n%s\n", ui.Bold(fmt.Sprintf("-- %s: migration %s (%s), from the migration log", t.label, e.ID, e.Origin), color))
		if err := emit.Render(os.Stdout, e.Migration(t.meta, true, false), emit.RenderOptions{ShowSafety: true, Color: color}); err != nil {
			return err
		}
	}
	if next != nil {
		warnMissingPK(os.Stderr, t.desired, t.stored, ui.IsColorEnabled(os.Stderr))
		if len(pending) > 0 {
			fmt.Printf("\n%s\n", ui.Bold("-- "+t.label+": the source's change", color))
		}
		if err := emit.Render(os.Stdout, full, emit.RenderOptions{ShowSafety: true, ShowSourcePos: true, Color: color}); err != nil {
			return err
		}
	}

	// The preflight: the database is as the migrations expect.
	if opts.preflight {
		if err := connect(); err != nil {
			return err
		}
		if err := t.preflight(ctx, conn, head, toRun, opts, color); err != nil {
			return err
		}
	}
	if opts.dryRun {
		ui.PrintInfo(os.Stdout, "dry-run", "nothing run; snapshot, migration log and ledger not updated", color)
		return nil
	}
	if !confirm("Apply?", opts, color) {
		ui.PrintInfo(os.Stdout, "", "Aborted.", color)
		return nil
	}
	if err := connect(); err != nil {
		return err
	}

	// RFC Section 11.5: the connecting role must be a member of every role
	// a SET ROLE/OWNER TO statement targets, checked before anything runs.
	var all []pipeline.DiffOp
	for _, e := range toRun {
		all = append(all, executable(allOps(e.Migration(t.meta, false, false)))...)
	}
	if err := executor.ValidateOwnerMembership(ctx, conn, all); err != nil {
		return err
	}

	// Settle.
	checks := !opts.allowDrift
	for _, e := range pending {
		if e == resume {
			m := e.Migration(t.meta, false, false)
			finish := led.Finished(pipeline.Migration{Meta: t.meta, NonTransactional: m.NonTransactional}, ledger.Of(t.key, e))
			if err := t.exec.Apply(ctx, finish, conn); err != nil {
				return fmt.Errorf("finishing migration %s: %w", e.ID, ui.WrapDB(err))
			}
			ui.PrintSuccess(os.Stdout, "Finished", t.label+" — migration "+e.ID, color)
			continue
		}
		if err := t.exec.Apply(ctx, led.Wrap(e.Migration(t.meta, false, checks), ledger.Of(t.key, e)), conn); err != nil {
			return afterCommitHint(e.ID, err)
		}
		ui.PrintSuccess(os.Stdout, "Applied", t.label+" — migration "+e.ID+" from the log", color)
	}
	if next == nil {
		return nil
	}

	// The source's change: run it, then record it in the log and the
	// snapshot (also when the steps after its transaction failed: the
	// database has the transaction's changes).
	execM := next.Migration(t.meta, false, checks)
	fileM := next.Migration(t.meta, true, true)
	if led != nil {
		execM = led.Wrap(execM, ledger.Of(t.key, next))
		fileM = led.Wrap(fileM, ledger.Of(t.key, next))
	}
	runErr := t.exec.Apply(ctx, execM, conn)
	var after *executor.AfterCommitError
	if runErr != nil && !errors.As(runErr, &after) {
		return ui.WrapDB(runErr)
	}
	if opts.noSnapshot {
		if runErr != nil {
			return ui.WrapDB(runErr)
		}
		ui.PrintSuccess(os.Stdout, "Applied", t.label+" — snapshot and migration log not updated (--no-snapshot)", color)
		return nil
	}
	var file strings.Builder
	if err := emit.Render(&file, fileM, emit.DefaultRenderOptions()); err != nil {
		return err
	}
	undo, err := recordEntry(t.log, next, file.String(), newSnap)
	if err != nil {
		return err
	}
	if err := t.store.Save(t.cluster, t.key, newSnap); err != nil {
		undo()
		return fmt.Errorf("save snapshot: %w", err)
	}
	if runErr != nil {
		return afterCommitHint(next.ID, runErr)
	}
	path, _ := t.log.Paths(next.ID)
	ui.PrintSuccess(os.Stdout, "Applied", t.label+" — snapshot updated\n         "+ui.Dim(path, color), color)
	return nil
}

func afterCommitHint(id string, err error) error {
	var after *executor.AfterCommitError
	if errors.As(err, &after) {
		return fmt.Errorf("migration %s: its transaction committed, then a step after it failed: %w\n  dpg apply --resume finishes it", id, ui.WrapDB(after.Err))
	}
	return fmt.Errorf("migration %s: %w", id, ui.WrapDB(err))
}

// pending returns the log's migrations the database hasn't run, after its
// ledger's last one, the migration to finish with --resume, and the
// database's last migration.
func (t *applyTarget) pending(ctx context.Context, conn *executor.PgxConn, led *ledger.Ledger, opts applyOptions) ([]*migrationlog.Entry, *migrationlog.Entry, string, error) {
	st, err := led.Read(ctx, conn, t.key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read the ledger: %w", ui.WrapDB(err))
	}
	var resume *migrationlog.Entry
	if u := st.Unfinished(); u != nil {
		e := t.log.Find(u.ID)
		switch {
		case u.Status == ledger.Started && e == nil:
			// An attempt whose transaction never committed, of a migration
			// that was never recorded: nothing of it remains.
			if _, err := conn.Exec(ctx, led.Forget(ledger.Migration{Log: t.key, ID: u.ID}, ledger.Started)); err != nil {
				return nil, nil, "", ui.WrapDB(err)
			}
			delete(st.Rows, u.ID)
		case u.Status == ledger.Committed && e == nil:
			return nil, nil, "", fmt.Errorf("DPG-E049: migration %s committed in this database but isn't in the migration log", u.ID)
		case u.Status == ledger.Committed && !opts.resume:
			return nil, nil, "", fmt.Errorf("DPG-E049: migration %s failed after its transaction committed; dpg apply --resume finishes it", u.ID)
		case u.Status == ledger.Committed:
			resume = e
		}
	}
	head, err := st.Head()
	if err != nil {
		return nil, nil, "", err
	}
	if opts.resume && resume == nil {
		return nil, nil, "", fmt.Errorf("--resume: no migration of %s is waiting to be finished", t.label)
	}
	start := 0
	switch {
	case head != "":
		i := t.log.Index(head)
		if i < 0 {
			return nil, nil, "", fmt.Errorf("DPG-E050: the last migration this database ran, %s, isn't in the migration log (was it made on another branch?)", head)
		}
		start = i + 1
	case len(t.log.Entries) > 0:
		fresh, err := t.fresh(ctx, conn)
		if err != nil {
			return nil, nil, "", err
		}
		if !fresh {
			return nil, nil, "", fmt.Errorf("DPG-E050: %s has objects of the migration log but no record of running its migrations; if it already has them, dpg apply --baseline records how far it is", t.label)
		}
	}
	pending := t.log.Entries[start:]
	if resume != nil && (len(pending) == 0 || pending[0] != resume) {
		return nil, nil, "", fmt.Errorf("DPG-E049: migration %s is waiting to be finished but doesn't follow this database's last migration", resume.ID)
	}
	return pending, resume, head, nil
}

// fresh reports whether none of the objects the migration log ends with
// exist in the database (the public schema aside): such a database runs the
// whole log.
func (t *applyTarget) fresh(ctx context.Context, conn *executor.PgxConn) (bool, error) {
	objects, err := t.log.Replay("")
	if err != nil {
		return false, err
	}
	live, err := t.live(ctx, conn)
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
func (t *applyTarget) baseline(ctx context.Context, opts applyOptions, color bool) error {
	switch {
	case errors.Is(t.logErr, migrationlog.ErrNoBaseline) && opts.dryRun:
		ui.PrintInfo(os.Stdout, "dry-run", t.label+": would record the snapshot as the baseline migration", color)
	case errors.Is(t.logErr, migrationlog.ErrNoBaseline):
		e, err := writeBaseline(t.log, t.store, t.cluster, t.key, t.stored, t.desired, t.ops, t.differ, t.emitter, t.meta, opts.ledger)
		if err != nil {
			return err
		}
		path, _ := t.log.Paths(e.ID)
		ui.PrintSuccess(os.Stdout, "Baseline", t.label+" — migration log started\n         "+ui.Dim(path, color), color)
	case t.logErr != nil:
		return t.logErr
	case len(t.log.Entries) == 0:
		ui.PrintInfo(os.Stdout, t.label, "nothing to record: the snapshot is empty", color)
		return nil
	default:
		ui.PrintInfo(os.Stdout, t.label, "the migration log started at "+t.log.Entries[0].ID, color)
	}
	if opts.ledger == nil || len(t.log.Entries) == 0 {
		return nil
	}

	conn, err := t.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	st, err := opts.ledger.Read(ctx, conn, t.key)
	if err != nil {
		return ui.WrapDB(err)
	}
	if head, err := st.Head(); err != nil {
		return err
	} else if head != "" {
		ui.PrintInfo(os.Stdout, t.label, "the database already records its migrations (last: "+head+")", color)
		return nil
	}
	// The newest point of the log the database matches.
	live, err := t.live(ctx, conn)
	if err != nil {
		return ui.WrapDB(err)
	}
	at := -1
	for i := len(t.log.Entries) - 1; i >= 0 && at < 0; i-- {
		objects, err := t.log.Replay(t.log.Entries[i].ID)
		if err != nil {
			return err
		}
		report, err := drift.Detect(t.differ, live, &pipeline.Snapshot{Objects: objects}, drift.Options{Ignore: t.rules})
		if err != nil {
			return err
		}
		if !report.HasDrift() {
			at = i
		}
	}
	if at < 0 {
		return fmt.Errorf("DPG-E050: %s doesn't match any point of the migration log; dpg verify shows how it differs from the snapshot", t.label)
	}
	upTo := t.log.Entries[at]
	if opts.dryRun {
		ui.PrintInfo(os.Stdout, "dry-run", fmt.Sprintf("%s: would record migrations up to %s (%d of %d)", t.label, upTo.ID, at+1, len(t.log.Entries)), color)
		return nil
	}
	m := pipeline.Migration{Meta: t.meta}
	for _, s := range opts.ledger.Bootstrap() {
		m.Transactional = append(m.Transactional, ledgerStmt(s))
	}
	for _, e := range t.log.Entries[:at+1] {
		m.Transactional = append(m.Transactional, ledgerStmt(opts.ledger.Record(ledger.Of(t.key, e))))
	}
	if err := t.exec.Apply(ctx, m, conn); err != nil {
		return ui.WrapDB(err)
	}
	ui.PrintSuccess(os.Stdout, "Recorded", fmt.Sprintf("%s — at migration %s (%d of %d); dpg apply runs the rest", t.label, upTo.ID, at+1, len(t.log.Entries)), color)
	return nil
}

// ledgerStmt is a ledger statement run in a transaction.
type ledgerStmt string

func (s ledgerStmt) SQL() string             { return string(s) }
func (s ledgerStmt) Safety() pipeline.Safety { return pipeline.Safe }
func (s ledgerStmt) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (s ledgerStmt) Mode() pipeline.ExecMode { return pipeline.ModeTransactional }

// preflight checks the database against what the migrations about to run
// expect (RFC Section 16.10): no drift, conflict or converged item on an
// object they change (an acknowledged one only on a property they don't
// change), no object they create already there, nothing DPG doesn't manage
// depending on an object they drop or recreate, no grant made outside DPG
// on an object they recreate. Drift elsewhere is a note. head is the
// database's position in the log.
func (t *applyTarget) preflight(ctx context.Context, conn *executor.PgxConn, head string, toRun []*migrationlog.Entry, opts applyOptions, color bool) error {
	// The state the database should be in: the log at its position.
	var expected map[string]json.RawMessage
	atHead := head == t.log.Head()
	switch {
	case atHead:
		expected = copyObjects(t.stored.Objects)
	case head == "":
		expected = map[string]json.RawMessage{}
	default:
		var err error
		if expected, err = t.log.Replay(head); err != nil {
			return err
		}
	}
	snap := &pipeline.Snapshot{Objects: expected}
	if err := t.rules.FilterSnapshot(snap); err != nil {
		return err
	}
	live, err := t.live(ctx, conn)
	if err != nil {
		return ui.WrapDB(err)
	}
	var source []pipeline.IRObject
	if atHead {
		source = t.desired // the source is the database's next state only at the log's end
	}
	report, err := drift.Detect(t.differ, live, snap, drift.Options{Ignore: t.rules, Source: source})
	if err != nil {
		return err
	}
	if t.ackFile != "" {
		acks, err := drift.LoadAcks(t.ackFile)
		if err != nil {
			return err
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
	state := copyObjects(expected)
	for _, e := range toRun {
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
				return err
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
			return ui.WrapDB(err)
		}
		for _, d := range deps {
			destructive = true
			blocks = append(blocks, fmt.Sprintf("%s:%s: the migration drops or recreates it, and %s depends on it but DPG doesn't manage it", tc.kind, k, d))
		}
	}

	if len(notes) > 0 {
		sort.Strings(notes)
		ui.PrintInfo(os.Stderr, "note", fmt.Sprintf("%s: drift on objects these migrations don't touch: %s", t.label, strings.Join(notes, ", ")), ui.IsColorEnabled(os.Stderr))
	}
	if len(blocks) == 0 {
		return nil
	}
	sort.Strings(blocks)
	fmt.Fprintf(os.Stderr, "\n%s\n", ui.Bold(fmt.Sprintf("%s: the database isn't as the migrations expect", t.label), ui.IsColorEnabled(os.Stderr)))
	for _, b := range blocks {
		fmt.Fprintf(os.Stderr, "  %s\n", b)
	}
	if opts.allowDrift && (!destructive || opts.allowDestructive) {
		ui.PrintInfo(os.Stderr, "warn", "continuing anyway (--allow-drift)", ui.IsColorEnabled(os.Stderr))
		return nil
	}
	if opts.allowDrift {
		return &ui.ExitError{Code: 1, Err: fmt.Errorf("DPG-E052: running it would drop objects DPG doesn't manage; --allow-drift with --allow-destructive proceeds")}
	}
	return &ui.ExitError{Code: 1, Err: fmt.Errorf("DPG-E053: %d problem(s); dpg resolve fixes them, or --allow-drift proceeds anyway", len(blocks))}
}
