package main

import (
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
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/precondition"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/ui"
)

// openLog loads the migration log of a database (key: its name, or the
// cluster snapshot key) and checks it against the stored snapshot. A
// snapshot without a log returns the log with migrationlog.ErrNoBaseline.
func openLog(migrationsDir, cluster, key string, snap *pipeline.Snapshot) (*migrationlog.Log, error) {
	if migrationsDir == "" {
		return nil, fmt.Errorf("no migrations directory: the migration log needs one")
	}
	l, err := migrationlog.Load(migrationlog.Dir(migrationsDir, cluster, key))
	if err != nil {
		return nil, err
	}
	return l, l.Check(snap.Migration, snap.Objects)
}

// copyObjects copies a snapshot's objects (the stored state, before
// [ignore] filters it in memory).
func copyObjects(objects map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(objects))
	for k, v := range objects {
		out[k] = v
	}
	return out
}

// recordEntry writes a migration's entry and file to the log and points
// next at it. The returned function removes the entry again (when saving
// the snapshot fails).
func recordEntry(l *migrationlog.Log, e *migrationlog.Entry, file string, next *pipeline.Snapshot) (func(), error) {
	if err := l.Write(e, file); err != nil {
		return nil, fmt.Errorf("write migration %s: %w", e.ID, err)
	}
	next.Migration = e.ID
	return func() {
		sqlPath, entryPath := l.Paths(e.ID)
		os.Remove(entryPath)
		os.Remove(sqlPath)
		l.Entries = l.Entries[:len(l.Entries)-1]
	}, nil
}

// renderFile renders a log entry as its file: its guards and steps, with
// the ledger's statements around them when the ledger is used, so any
// runner records it.
func renderFile(e *migrationlog.Entry, meta pipeline.MigrationMeta, led *ledger.Ledger, log string) (string, error) {
	m := e.Migration(meta, true, true)
	if led != nil {
		m = led.Wrap(m, ledger.Of(log, e))
	}
	var b strings.Builder
	if err := emit.Render(&b, m, emit.DefaultRenderOptions()); err != nil {
		return "", err
	}
	return b.String(), nil
}

// writeBaseline starts the log of a project whose snapshot predates it: a
// baseline migration whose steps build the snapshot's state in an empty
// database. The source must match the snapshot (pendingOps empty), so the
// steps describe exactly the snapshot.
func writeBaseline(l *migrationlog.Log, store pipeline.SnapshotStore, cluster, key string, stored *pipeline.Snapshot,
	desired []pipeline.IRObject, pendingOps []pipeline.DiffOp, differ pipeline.Differ, emitter pipeline.Emitter, meta pipeline.MigrationMeta, led *ledger.Ledger) (*migrationlog.Entry, error) {
	if len(pendingOps) > 0 {
		return nil, fmt.Errorf("DPG-E048: the baseline records the snapshot, so the source must match it; the source has %d pending change(s) (first: %s). Commit or stash them, run dpg apply --baseline, then restore them", len(pendingOps), pendingOps[0].SQL())
	}
	ops, err := differ.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		return nil, err
	}
	m, err := emitter.Emit(ops, meta)
	if err != nil {
		return nil, err
	}
	next := *stored
	next.Objects = copyObjects(stored.Objects)
	e, err := migrationlog.New(l.Head(), guardedSteps(m, next.Objects, led, key), migrationlog.OriginBaseline, nil, next.Objects, time.Now())
	if err != nil {
		return nil, err
	}
	file, err := renderFile(e, meta, led, key)
	if err != nil {
		return nil, err
	}
	undo, err := recordEntry(l, e, file, &next)
	if err != nil {
		return nil, err
	}
	if err := store.Save(cluster, key, &next); err != nil {
		undo()
		return nil, fmt.Errorf("save snapshot: %w", err)
	}
	return e, nil
}

// noBaselineHint explains a missing log.
func noBaselineHint(err error) error {
	if errors.Is(err, migrationlog.ErrNoBaseline) {
		return fmt.Errorf("%w (it records the current snapshot as the first migration; the source must match the snapshot)", err)
	}
	return err
}

// checkLogOffline checks a database's migration log against its stored
// snapshot without a database: a broken log is an error; a snapshot that
// predates its log is a note on stderr.
func checkLogOffline(store pipeline.SnapshotStore, migrationsDir, cluster, key, label string) error {
	snap, err := store.Load(cluster, key)
	if err != nil {
		return err
	}
	if _, err := openLog(migrationsDir, cluster, key, snap); err != nil {
		if errors.Is(err, migrationlog.ErrNoBaseline) {
			fmt.Fprintf(os.Stderr, "note: %s: the snapshot has no migration log yet; dpg apply --baseline starts it\n", label)
			return nil
		}
		return err
	}
	return nil
}

// recordInLedger records migrations as complete in a database's ledger
// without running them: the database already has their state.
func recordInLedger(ctx context.Context, connect func() (*executor.PgxConn, error), led *ledger.Ledger, log string, entries ...*migrationlog.Entry) error {
	conn, err := connect()
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	stmts := led.Bootstrap()
	for _, e := range entries {
		stmts = append(stmts, led.Record(ledger.Of(log, e)))
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("record in the ledger: %w", ui.WrapDB(err))
		}
	}
	return nil
}

// guardedSteps are a migration's steps after its guards (RFC Section
// 16.10). after is the snapshot objects it produces: they and the objects
// it drops are the ones DPG manages. With a ledger, an object the migration
// changes must also be as DPG last saw it (its fingerprint); log is the
// database's log in the ledger.
func guardedSteps(m pipeline.Migration, after map[string]json.RawMessage, led *ledger.Ledger, log string) []migrationlog.Step {
	ops := append(append(append([]pipeline.DiffOp(nil), m.PreTransactional...), m.Transactional...), m.NonTransactional...)
	managed := map[string]bool{}
	for k := range after {
		managed[k] = true
	}
	var runnable []pipeline.DiffOp
	for _, op := range ops {
		if to, ok := op.(pipeline.TargetedOp); ok && to.Target().Effect == pipeline.EffectDrop {
			managed[to.Target().Key] = true
		}
		if op.Mode() != pipeline.ModeInstructionOnly {
			runnable = append(runnable, op)
		}
	}
	guards := precondition.Guards(ops, managed, executor.DistinctOwnerRoles(runnable))
	if led != nil {
		fp := precondition.Fingerprints{Schema: led.Schema, Log: log}
		seen := map[string]bool{}
		for _, op := range ops {
			to, ok := op.(pipeline.TargetedOp)
			if !ok {
				continue
			}
			t := to.Target()
			key := t.Key
			switch t.Effect {
			case pipeline.EffectCreate:
				continue
			case pipeline.EffectRename:
				key = t.From
			}
			if id := t.Kind + "\x00" + key; !seen[id] {
				seen[id] = true
				if g := fp.Guard(t.Kind, key); g != "" {
					guards = append(guards, g)
				}
			}
		}
	}
	var steps []migrationlog.Step
	for _, g := range guards {
		steps = append(steps, migrationlog.Step{SQL: g, Safety: "SAFE", Stage: migrationlog.StageCheck})
	}
	return append(steps, migrationlog.StepsOf(m)...)
}

// logPosition finds a database's place in its migration log; the zero
// value compares databases with the stored snapshot.
type logPosition struct {
	dir    string
	ledger *ledger.Ledger
}

func logPositionFor(proj *project.Project) logPosition {
	return logPosition{dir: proj.MigrationsDir(), ledger: ledgerFor(proj.RootConfig)}
}

// positioned returns the snapshot a database is expected to match: its
// migration log replayed to the last migration its ledger records, and how
// many of the log's migrations follow it. A database at the log's end, one
// whose ledger records nothing, and a project without a ledger are
// compared with the stored snapshot.
func positioned(ctx context.Context, q pipeline.Querier, lp logPosition, cluster, key string, snap *pipeline.Snapshot) (*pipeline.Snapshot, int, error) {
	if lp.dir == "" || lp.ledger == nil {
		return snap, 0, nil
	}
	l, err := migrationlog.Load(migrationlog.Dir(lp.dir, cluster, key))
	if err != nil {
		return nil, 0, err
	}
	if len(l.Entries) == 0 {
		return snap, 0, nil
	}
	st, err := lp.ledger.Read(ctx, q, key)
	if err != nil {
		return nil, 0, ui.WrapDB(err)
	}
	head, err := st.Head()
	if err != nil {
		return nil, 0, err
	}
	if head == "" || head == l.Head() {
		return snap, 0, nil
	}
	i := l.Index(head)
	if i < 0 {
		return nil, 0, fmt.Errorf("DPG-E050: the last migration this database ran, %s, isn't in the migration log", head)
	}
	objects, err := l.Replay(head)
	if err != nil {
		return nil, 0, err
	}
	at := *snap
	at.Objects, at.Migration = objects, head
	return &at, len(l.Entries) - i - 1, nil
}

// objectKinds maps snapshot keys to their kinds.
func objectKinds(objects map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for k, raw := range objects {
		var o struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &o) == nil && o.Kind != "" {
			out[k] = o.Kind
		}
	}
	return out
}

// cleanObjects keeps the objects no item of the report is about.
func cleanObjects(kinds map[string]string, r *drift.Report) map[string]string {
	dirty := map[string]bool{}
	for _, it := range r.Items {
		dirty[it.Object] = true
	}
	out := map[string]string{}
	for k, kind := range kinds {
		if !dirty[k] {
			out[k] = kind
		}
	}
	return out
}

// recordFingerprints records objects' fingerprints (key → kind) in a
// database's ledger, creating it when missing (RFC Section 16.10): DPG has
// seen them as they are.
func recordFingerprints(ctx context.Context, conn pipeline.Conn, led *ledger.Ledger, log string, objects map[string]string, by string) error {
	fp := precondition.Fingerprints{Schema: led.Schema, Log: log}
	stmts := led.Bootstrap()
	keys := make([]string, 0, len(objects))
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s := fp.Record(objects[k], k, by); s != "" {
			stmts = append(stmts, s)
		}
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("record fingerprints: %w", ui.WrapDB(err))
		}
	}
	return nil
}
