package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
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

// recordMigration writes the migration taking the stored objects before to
// next's objects and points next at it. The returned function removes the
// entry again (when saving the snapshot fails).
func recordMigration(l *migrationlog.Log, sql, origin string, before map[string]json.RawMessage, next *pipeline.Snapshot) (*migrationlog.Entry, func(), error) {
	e, err := migrationlog.New(l.Head(), sql, origin, before, next.Objects, time.Now())
	if err != nil {
		return nil, nil, err
	}
	if err := l.Write(e, sql); err != nil {
		return nil, nil, fmt.Errorf("write migration %s: %w", e.ID, err)
	}
	next.Migration = e.ID
	undo := func() {
		sqlPath, entryPath := l.Paths(e.ID)
		os.Remove(entryPath)
		os.Remove(sqlPath)
		l.Entries = l.Entries[:len(l.Entries)-1]
	}
	return e, undo, nil
}

// renderSQL renders ops as a migration file (the archived form).
func renderSQL(emitter pipeline.Emitter, ops []pipeline.DiffOp, meta pipeline.MigrationMeta) (string, error) {
	m, err := emitter.Emit(ops, meta)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := emit.Render(&b, m, emit.DefaultRenderOptions()); err != nil {
		return "", err
	}
	return b.String(), nil
}

// writeBaseline starts the log of a project whose snapshot predates it: a
// baseline migration whose SQL builds the snapshot's state in an empty
// database. The source must match the snapshot (pendingOps empty), so the
// SQL describes exactly the snapshot.
func writeBaseline(l *migrationlog.Log, store pipeline.SnapshotStore, cluster, key string, stored *pipeline.Snapshot,
	desired []pipeline.IRObject, pendingOps []pipeline.DiffOp, differ pipeline.Differ, emitter pipeline.Emitter, meta pipeline.MigrationMeta) (*migrationlog.Entry, error) {
	if len(pendingOps) > 0 {
		return nil, fmt.Errorf("DPG-E048: the baseline records the snapshot, so the source must match it; the source has %d pending change(s) (first: %s). Commit or stash them, run dpg apply --baseline, then restore them", len(pendingOps), pendingOps[0].SQL())
	}
	ops, err := differ.Diff(desired, &pipeline.Snapshot{})
	if err != nil {
		return nil, err
	}
	sql, err := renderSQL(emitter, ops, meta)
	if err != nil {
		return nil, err
	}
	next := *stored
	next.Objects = copyObjects(stored.Objects)
	e, undo, err := recordMigration(l, sql, migrationlog.OriginBaseline, nil, &next)
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
