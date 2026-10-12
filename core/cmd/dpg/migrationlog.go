package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
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

// renderFile renders a migration as its log file: with the ledger's
// statements around it when the ledger is used, so any runner records it.
func renderFile(m pipeline.Migration, led *ledger.Ledger, lm ledger.Migration) (string, error) {
	if led != nil {
		m = led.Wrap(m, lm)
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
	e, err := migrationlog.New(l.Head(), migrationlog.StepsOf(m), migrationlog.OriginBaseline, nil, next.Objects, time.Now())
	if err != nil {
		return nil, err
	}
	file, err := renderFile(m, led, ledger.Of(key, e))
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
