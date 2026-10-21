package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/thec1oud/dpg/internal/apply"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/ui"
)

// checkLogOffline checks a database's migration log against its stored
// snapshot without a database: a broken log is an error; a snapshot that
// predates its log is a note on stderr.
func checkLogOffline(store pipeline.SnapshotStore, migrationsDir, cluster, key, label string) error {
	snap, err := store.Load(cluster, key)
	if err != nil {
		return err
	}
	if _, err := apply.OpenLog(migrationsDir, cluster, key, snap); err != nil {
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

// logPositionFor finds the project's databases in their migration logs.
func logPositionFor(proj *project.Project) apply.LogPosition {
	return apply.LogPosition{Dir: proj.MigrationsDir(), Ledger: apply.LedgerFor(proj.RootConfig)}
}
