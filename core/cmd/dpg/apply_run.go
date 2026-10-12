package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
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
// records the source's pending change.
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
	var pending []*migrationlog.Entry
	var resume *migrationlog.Entry
	if led != nil {
		var err error
		if conn, err = t.connect(ctx); err != nil {
			return err
		}
		pending, resume, err = t.pending(ctx, conn, led, opts)
		if err != nil {
			return err
		}
	}

	// Gate and show everything that will run.
	for _, e := range pending {
		if err := gate(allOps(e.Migration(t.meta, true)), opts, "migration "+e.ID); err != nil {
			return err
		}
	}
	if err := gate(t.ops, opts, "the migration"); err != nil {
		return err
	}
	if len(pending) == 0 && resume == nil && len(t.ops) == 0 {
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
		if err := emit.Render(os.Stdout, e.Migration(t.meta, true), emit.RenderOptions{ShowSafety: true, Color: color}); err != nil {
			return err
		}
	}
	var full pipeline.Migration
	if len(t.ops) > 0 {
		warnMissingPK(os.Stderr, t.desired, t.stored, ui.IsColorEnabled(os.Stderr))
		var err error
		full, err = t.emitter.Emit(t.ops, t.meta)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			fmt.Printf("\n%s\n", ui.Bold("-- "+t.label+": the source's change", color))
		}
		if err := emit.Render(os.Stdout, full, emit.RenderOptions{ShowSafety: true, ShowSourcePos: true, Color: color}); err != nil {
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
	if conn == nil {
		var err error
		if conn, err = t.connect(ctx); err != nil {
			return err
		}
	}

	// RFC Section 11.5: the connecting role must be a member of every role
	// a SET ROLE/OWNER TO statement targets, checked before anything runs.
	var toRun []pipeline.DiffOp
	for _, e := range pending {
		toRun = append(toRun, executable(allOps(e.Migration(t.meta, false)))...)
	}
	toRun = append(toRun, executable(t.ops)...)
	if err := executor.ValidateOwnerMembership(ctx, conn, toRun); err != nil {
		return err
	}

	// Settle.
	for _, e := range pending {
		if e == resume {
			m := e.Migration(t.meta, false)
			finish := pipeline.Migration{Meta: t.meta, NonTransactional: m.NonTransactional}
			finish = led.Finished(finish, ledger.Of(t.key, e))
			if err := t.exec.Apply(ctx, finish, conn); err != nil {
				return fmt.Errorf("finishing migration %s: %w", e.ID, ui.WrapDB(err))
			}
			ui.PrintSuccess(os.Stdout, "Finished", t.label+" — migration "+e.ID, color)
			continue
		}
		if err := t.exec.Apply(ctx, led.Wrap(e.Migration(t.meta, false), ledger.Of(t.key, e)), conn); err != nil {
			return afterCommitHint(e.ID, err)
		}
		ui.PrintSuccess(os.Stdout, "Applied", t.label+" — migration "+e.ID+" from the log", color)
	}
	if len(t.ops) == 0 {
		return nil
	}

	// The source's change: run it, then record it in the log and the
	// snapshot (also when the steps after its transaction failed: the
	// database has the transaction's changes).
	execM, err := t.emitter.Emit(executable(t.ops), t.meta)
	if err != nil {
		return err
	}
	newSnap := &pipeline.Snapshot{}
	if err := snapshotpkg.Populate(newSnap, t.desired); err != nil {
		return fmt.Errorf("build snapshot: %w", err)
	}
	e, err := migrationlog.New(t.log.Head(), migrationlog.StepsOf(full), migrationlog.OriginApply, t.stored.Objects, newSnap.Objects, time.Now())
	if err != nil {
		return err
	}
	fileM := full
	if led != nil {
		execM = led.Wrap(execM, ledger.Of(t.key, e))
		fileM = led.Wrap(full, ledger.Of(t.key, e))
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
	undo, err := recordEntry(t.log, e, file.String(), newSnap)
	if err != nil {
		return err
	}
	if err := t.store.Save(t.cluster, t.key, newSnap); err != nil {
		undo()
		return fmt.Errorf("save snapshot: %w", err)
	}
	if runErr != nil {
		return afterCommitHint(e.ID, runErr)
	}
	path, _ := t.log.Paths(e.ID)
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
// ledger's last one, and the migration to finish with --resume.
func (t *applyTarget) pending(ctx context.Context, conn *executor.PgxConn, led *ledger.Ledger, opts applyOptions) ([]*migrationlog.Entry, *migrationlog.Entry, error) {
	st, err := led.Read(ctx, conn, t.key)
	if err != nil {
		return nil, nil, fmt.Errorf("read the ledger: %w", ui.WrapDB(err))
	}
	var resume *migrationlog.Entry
	if u := st.Unfinished(); u != nil {
		e := t.log.Find(u.ID)
		switch {
		case u.Status == ledger.Started && e == nil:
			// An attempt whose transaction never committed, of a migration
			// that was never recorded: nothing of it remains.
			if _, err := conn.Exec(ctx, led.Forget(ledger.Migration{Log: t.key, ID: u.ID}, ledger.Started)); err != nil {
				return nil, nil, ui.WrapDB(err)
			}
			delete(st.Rows, u.ID)
		case u.Status == ledger.Committed && e == nil:
			return nil, nil, fmt.Errorf("DPG-E049: migration %s committed in this database but isn't in the migration log", u.ID)
		case u.Status == ledger.Committed && !opts.resume:
			return nil, nil, fmt.Errorf("DPG-E049: migration %s failed after its transaction committed; dpg apply --resume finishes it", u.ID)
		case u.Status == ledger.Committed:
			resume = e
		}
	}
	head, err := st.Head()
	if err != nil {
		return nil, nil, err
	}
	if opts.resume && resume == nil {
		return nil, nil, fmt.Errorf("--resume: no migration of %s is waiting to be finished", t.label)
	}
	start := 0
	switch {
	case head != "":
		i := t.log.Index(head)
		if i < 0 {
			return nil, nil, fmt.Errorf("DPG-E050: the last migration this database ran, %s, isn't in the migration log (was it made on another branch?)", head)
		}
		start = i + 1
	case len(t.log.Entries) > 0:
		fresh, err := t.fresh(ctx, conn)
		if err != nil {
			return nil, nil, err
		}
		if !fresh {
			return nil, nil, fmt.Errorf("DPG-E050: %s has objects of the migration log but no record of running its migrations; if it already has them, dpg apply --baseline records how far it is", t.label)
		}
	}
	pending := t.log.Entries[start:]
	if resume != nil && (len(pending) == 0 || pending[0] != resume) {
		return nil, nil, fmt.Errorf("DPG-E049: migration %s is waiting to be finished but doesn't follow this database's last migration", resume.ID)
	}
	return pending, resume, nil
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
