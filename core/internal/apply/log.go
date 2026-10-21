package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/precondition"
	"github.com/thec1oud/dpg/internal/ui"
)

// OpenLog loads the migration log of a database (key: its name, or the
// cluster snapshot key) and checks it against the stored snapshot. A
// snapshot without a log returns the log with migrationlog.ErrNoBaseline.
func OpenLog(migrationsDir, cluster, key string, snap *pipeline.Snapshot) (*migrationlog.Log, error) {
	if migrationsDir == "" {
		return nil, fmt.Errorf("no migrations directory: the migration log needs one")
	}
	l, err := migrationlog.Load(migrationlog.Dir(migrationsDir, cluster, key))
	if err != nil {
		return nil, err
	}
	return l, l.Check(snap.Migration, snap.Objects)
}

// CopyObjects copies a snapshot's objects (the stored state, before
// [ignore] filters it in memory).
func CopyObjects(objects map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(objects))
	for k, v := range objects {
		out[k] = v
	}
	return out
}

// RecordEntry writes a migration's entry and file to the log and points
// next at it. The returned function removes the entry again (when saving
// the snapshot fails).
func RecordEntry(l *migrationlog.Log, e *migrationlog.Entry, file string, next *pipeline.Snapshot) (func(), error) {
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

// RenderFile renders a log entry as its file: its guards and steps, with
// the ledger's statements around them when the ledger is used, so any
// runner records it.
func RenderFile(e *migrationlog.Entry, meta pipeline.MigrationMeta, led *ledger.Ledger, log string) (string, error) {
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

// WriteBaseline starts the log of a project whose snapshot predates it: a
// baseline migration whose steps build the snapshot's state in an empty
// database. The source must match the snapshot (pendingOps empty), so the
// steps describe exactly the snapshot.
func WriteBaseline(l *migrationlog.Log, store pipeline.SnapshotStore, cluster, key string, stored *pipeline.Snapshot,
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
	next.Objects = CopyObjects(stored.Objects)
	e, err := migrationlog.New(l.Head(), GuardedSteps(m, next.Objects, led, key), migrationlog.OriginBaseline, nil, next.Objects, time.Now())
	if err != nil {
		return nil, err
	}
	file, err := RenderFile(e, meta, led, key)
	if err != nil {
		return nil, err
	}
	undo, err := RecordEntry(l, e, file, &next)
	if err != nil {
		return nil, err
	}
	if err := store.Save(cluster, key, &next); err != nil {
		undo()
		return nil, fmt.Errorf("save snapshot: %w", err)
	}
	return e, nil
}

// NoBaselineHint explains a missing log.
func NoBaselineHint(err error) error {
	if errors.Is(err, migrationlog.ErrNoBaseline) {
		return fmt.Errorf("%w (it records the current snapshot as the first migration; the source must match the snapshot)", err)
	}
	return err
}

// GuardedSteps are a migration's steps after its guards (RFC Section
// 16.10). after is the snapshot objects it produces: they and the objects
// it drops are the ones DPG manages. With a ledger, an object the migration
// changes must also be as DPG last saw it (its fingerprint); log is the
// database's log in the ledger.
func GuardedSteps(m pipeline.Migration, after map[string]json.RawMessage, led *ledger.Ledger, log string) []migrationlog.Step {
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

// LogPosition finds a database's place in its migration log; the zero
// value compares databases with the stored snapshot.
type LogPosition struct {
	Dir    string
	Ledger *ledger.Ledger
}

// OffLogError is a database whose last migration isn't in the migration
// log: it ran a migration of another branch, or of a newer build.
type OffLogError struct {
	Head string // the database's last migration
	// Behind is the log's last migration, which the database also ran,
	// when it has ("" when it hasn't).
	Behind string
}

func (e *OffLogError) Error() string {
	return fmt.Sprintf("DPG-E050: the last migration this database ran, %s, isn't in the migration log", e.Head)
}

// Positioned returns the snapshot a database is expected to match: its
// migration log replayed to the last migration its ledger records, and how
// many of the log's migrations follow it. A database at the log's end, one
// whose ledger records nothing, and a project without a ledger are
// compared with the stored snapshot. A database whose last migration isn't
// in the log is an *OffLogError.
func Positioned(ctx context.Context, q pipeline.Querier, lp LogPosition, cluster, key string, snap *pipeline.Snapshot) (*pipeline.Snapshot, int, error) {
	if lp.Dir == "" || lp.Ledger == nil {
		return snap, 0, nil
	}
	l, err := migrationlog.Load(migrationlog.Dir(lp.Dir, cluster, key))
	if err != nil {
		return nil, 0, err
	}
	if len(l.Entries) == 0 {
		return snap, 0, nil
	}
	st, err := lp.Ledger.Read(ctx, q, key)
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
		off := &OffLogError{Head: head}
		if r, ok := st.Rows[l.Head()]; ok && r.Status == ledger.Complete {
			off.Behind = l.Head()
		}
		return nil, 0, off
	}
	objects, err := l.Replay(head)
	if err != nil {
		return nil, 0, err
	}
	at := *snap
	at.Objects, at.Migration = objects, head
	return &at, len(l.Entries) - i - 1, nil
}

// ObjectKinds maps snapshot keys to their kinds.
func ObjectKinds(objects map[string]json.RawMessage) map[string]string {
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

// RecordFingerprints records objects' fingerprints (key → kind) in a
// database's ledger, creating it when missing (RFC Section 16.10): DPG has
// seen them as they are.
func RecordFingerprints(ctx context.Context, conn pipeline.Conn, led *ledger.Ledger, log string, objects map[string]string, by string) error {
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
