package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/apply"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

// recordOptions are dpg plan --record's and --check's.
type recordOptions struct {
	cluster, database        string
	record, amend, check     bool
	allowDestructive, rebuild bool
}

// eachTarget prepares every targeted database (and cluster-level objects)
// and calls fn with it.
func eachTarget(clusterName, databaseName string, opts applyOptions, fn func(*apply.Target, applyOptions) error) error {
	proj, err := discoverProject()
	if err != nil {
		return err
	}
	clusters, err := resolveClusters(proj, clusterName)
	if err != nil {
		return err
	}
	store, err := pipeline.MustResolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore)
	if err != nil {
		return err
	}
	differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if err != nil {
		return err
	}
	emitter, err := pipeline.MustResolve[pipeline.Emitter](pipeline.Default, pipeline.KeyEmitter)
	if err != nil {
		return err
	}
	applyExec, _ := pipeline.Resolve[pipeline.ApplyExecutor](pipeline.Default, pipeline.KeyApplyExecutor)
	secretResolver, _ := pipeline.Resolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
	opts.migrationsDir = proj.MigrationsDir()
	opts.ledger = apply.LedgerFor(proj.RootConfig)
	opts.lintCfg = apply.LinterConfig(proj.RootConfig.Linter)
	for _, cl := range clusters {
		if len(cl.SourceFiles) > 0 {
			o := opts
			o.lintCfg.MinPGVersion = cl.EffectiveMinPGVersion(proj.RootConfig)
			o.ignore = ignore.New(cl.EffectiveIgnore(proj.RootConfig))
			t, err := prepareClusterTarget(cl, store, differ, emitter, applyExec, secretResolver, o)
			if err == nil {
				err = fn(t, o)
			}
			if err != nil {
				return fmt.Errorf("%s (cluster): %w", cl.Name(), err)
			}
		}
		databases, err := resolveDatabases(cl, databaseName)
		if err != nil {
			return err
		}
		for _, db := range databases {
			o := opts
			o.lintCfg.MinPGVersion = db.EffectiveMinPGVersion(cl, proj.RootConfig)
			o.ignore = ignore.New(db.EffectiveIgnore(cl, proj.RootConfig))
			t, err := prepareDBTarget(cl, db, store, differ, emitter, applyExec, secretResolver, o)
			if err == nil {
				err = fn(t, o)
			}
			if err != nil {
				return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
			}
		}
	}
	return nil
}

// runRecord is dpg plan --record and --check.
func runRecord(ro recordOptions) error {
	opts := applyOptions{allowDestructive: ro.allowDestructive, approvePartitionRebuild: ro.rebuild}
	color := ui.IsColorEnabled(os.Stdout)
	var unrecorded []string
	err := eachTarget(ro.cluster, ro.database, opts, func(t *apply.Target, o applyOptions) error {
		if t.LogErr != nil {
			return apply.NoBaselineHint(t.LogErr)
		}
		if ro.check {
			if len(t.Ops) > 0 {
				unrecorded = append(unrecorded, fmt.Sprintf("%s: %d change(s) not recorded (first: %s)", t.Label, len(t.Ops), firstLine(t.Ops[0].SQL())))
			}
			return nil
		}
		return recordTarget(t, o, ro.amend, color)
	})
	if err != nil {
		return err
	}
	if ro.check {
		if len(unrecorded) > 0 {
			for _, u := range unrecorded {
				fmt.Fprintln(os.Stderr, u)
			}
			return &ui.ExitError{Code: 1, Err: fmt.Errorf("DPG-E056: the source has changes the migration log doesn't record; dpg plan --record records them")}
		}
		ui.PrintSuccess(os.Stdout, "Recorded", "every source change is in the migration log", color)
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// recordTarget records the source's change in the migration log without
// running it (RFC Section 18.1): every database runs it when dpg apply
// settles, or another runner runs its file. With amend, the log's last
// migration is made again from the source instead of followed.
func recordTarget(t *apply.Target, opts applyOptions, amend bool, color bool) error {
	if opts.ledger == nil {
		return fmt.Errorf("--record needs the ledger ([ledger] enabled = true): without it dpg apply never runs recorded migrations")
	}
	before := t.Stored.Objects
	parent := t.Log.Head()
	ops := t.Ops
	var replaced *migrationlog.Entry
	if amend {
		if len(t.Log.Entries) == 0 {
			return fmt.Errorf("--amend: the migration log has no migration to replace")
		}
		replaced = t.Log.Entries[len(t.Log.Entries)-1]
		parent = replaced.Parent
		var err error
		if before, err = t.Log.Replay(parent); err != nil {
			return err
		}
		base := &pipeline.Snapshot{Objects: apply.CopyObjects(before)}
		if err := t.Rules.FilterSnapshot(base); err != nil {
			return err
		}
		if ops, err = t.Differ.Diff(t.Desired, base); err != nil {
			return err
		}
	}
	if len(ops) == 0 && replaced == nil {
		ui.PrintInfo(os.Stdout, t.Label, "nothing to record: the source matches the snapshot", color)
		return nil
	}
	if err := apply.Gate(ops, opts.run(), "the migration"); err != nil {
		return err
	}

	next := *t.Stored
	var e *migrationlog.Entry
	var file string
	if len(ops) > 0 {
		full, err := t.Emitter.Emit(ops, t.Meta)
		if err != nil {
			return err
		}
		if err := emit.Render(os.Stdout, full, emit.RenderOptions{ShowSafety: true, ShowSourcePos: true, Color: color}); err != nil {
			return err
		}
		newSnap := &pipeline.Snapshot{}
		if err := snapshotpkg.Populate(newSnap, t.Desired); err != nil {
			return fmt.Errorf("build snapshot: %w", err)
		}
		next.Objects = newSnap.Objects
		if e, err = migrationlog.New(parent, apply.GuardedSteps(full, next.Objects, opts.ledger, t.Key), migrationlog.OriginRecord, before, next.Objects, time.Now()); err != nil {
			return err
		}
		if file, err = apply.RenderFile(e, t.Meta, opts.ledger, t.Key); err != nil {
			return err
		}
	} else {
		// Amending to nothing: the source matches the log without its last
		// migration, which goes.
		next.Objects = apply.CopyObjects(before)
		next.Migration = parent
	}

	// The replaced migration's files go first (two migrations after one
	// parent are no log), and come back if anything fails.
	var restore func()
	if replaced != nil {
		sqlPath, entryPath := t.Log.Paths(replaced.ID)
		sqlData, _ := os.ReadFile(sqlPath)
		entryData, _ := os.ReadFile(entryPath)
		if err := migrationlog.Remove(t.Log.Dir, replaced.ID); err != nil {
			return err
		}
		t.Log.Entries = t.Log.Entries[:len(t.Log.Entries)-1]
		restore = func() {
			os.WriteFile(sqlPath, sqlData, 0o644)
			os.WriteFile(entryPath, entryData, 0o644)
			t.Log.Entries = append(t.Log.Entries, replaced)
		}
	}
	undo := func() {}
	if e != nil {
		u, err := apply.RecordEntry(t.Log, e, file, &next)
		if err != nil {
			if restore != nil {
				restore()
			}
			return err
		}
		undo = u
	}
	if err := t.Store.Save(t.Cluster, t.Key, &next); err != nil {
		undo()
		if restore != nil {
			restore()
		}
		return fmt.Errorf("save snapshot: %w", err)
	}
	switch {
	case e == nil:
		ui.PrintSuccess(os.Stdout, "Removed", fmt.Sprintf("%s — migration %s: the source matches the log without it", t.Label, replaced.ID), color)
	case replaced != nil:
		path, _ := t.Log.Paths(e.ID)
		ui.PrintSuccess(os.Stdout, "Recorded", fmt.Sprintf("%s — %s, replacing %s (a database that ran %s must be rebuilt)\n         %s", t.Label, e.ID, replaced.ID, replaced.ID, ui.Dim(path, color)), color)
	default:
		path, _ := t.Log.Paths(e.ID)
		ui.PrintSuccess(os.Stdout, "Recorded", fmt.Sprintf("%s — not run anywhere yet; dpg apply runs it\n         %s", t.Label, ui.Dim(path, color)), color)
	}
	return nil
}

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Repair and rebase the migration log",
		Long: `The migration log records every migration of each database (RFC
Section 16.8). These commands repair it after a merge.`,
	}
	var clusterName, databaseName, keep string
	var allowDestructive, rebuild bool
	repair := &cobra.Command{
		Use:   "repair",
		Short: "Rewrite the snapshot from the migration log",
		Long: `Rewrites each snapshot as the replay of its migration log: the snapshot
is derived from the log, so a merge conflict in it is resolved by keeping
the log's entries and running this.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogRepair(clusterName, databaseName)
		},
	}
	rebase := &cobra.Command{
		Use:   "rebase --keep <migration>",
		Short: "Resolve two migrations after the same one (a merge of two branches)",
		Long: `When two branches each recorded a migration, the merged log has two
migrations after the same one (DPG-E047). rebase keeps the chain ending
at --keep (the target branch's last migration), removes the other
branch's migrations, rewrites the snapshot from the kept chain, and
records the source's change again on top of it.

A database that ran a removed migration has no point in the log any more
and must be rebuilt.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep == "" {
				return fmt.Errorf("--keep: name the last migration of the chain to keep")
			}
			return runLogRebase(clusterName, databaseName, keep, allowDestructive, rebuild)
		},
	}
	for _, c := range []*cobra.Command{repair, rebase} {
		c.Flags().StringVar(&clusterName, "cluster", "", "cluster (required when multiple clusters exist)")
		c.Flags().StringVar(&databaseName, "database", "", "database (required when multiple databases exist)")
	}
	rebase.Flags().StringVar(&keep, "keep", "", "the last migration of the chain to keep")
	rebase.Flags().BoolVar(&allowDestructive, "allow-destructive", false, "allow the re-recorded migration to have destructive operations")
	rebase.Flags().BoolVar(&rebuild, "approve-partition-rebuild", false, "allow the re-recorded migration a manual partition strategy rebuild")
	cmd.AddCommand(repair, rebase)
	return cmd
}

// logTarget is a database's (or a cluster's) snapshot and log location.
type logTarget struct {
	label, cluster, key, dir string
}

func eachLogTarget(clusterName, databaseName string, fn func(*project.Project, logTarget) error) error {
	proj, err := discoverProject()
	if err != nil {
		return err
	}
	clusters, err := resolveClusters(proj, clusterName)
	if err != nil {
		return err
	}
	for _, cl := range clusters {
		if len(cl.SourceFiles) > 0 {
			key := cl.ClusterSnapshotKey()
			if err := fn(proj, logTarget{cl.Name() + " (cluster)", cl.Name(), key, migrationlog.Dir(proj.MigrationsDir(), cl.Name(), key)}); err != nil {
				return err
			}
		}
		databases, err := resolveDatabases(cl, databaseName)
		if err != nil {
			return err
		}
		for _, db := range databases {
			if err := fn(proj, logTarget{cl.Name() + "/" + db.Name(), cl.Name(), db.Name(), migrationlog.Dir(proj.MigrationsDir(), cl.Name(), db.Name())}); err != nil {
				return err
			}
		}
	}
	return nil
}

// rewriteSnapshot saves the snapshot as the replay of chain: kept fields
// from the stored snapshot when it can be read (a merge conflict may have
// left it unreadable).
func rewriteSnapshot(store pipeline.SnapshotStore, lt logTarget, chain []*migrationlog.Entry) (bool, error) {
	l := &migrationlog.Log{Dir: lt.dir, Entries: chain}
	objects, err := l.Replay("")
	if err != nil {
		return false, err
	}
	snap, err := store.Load(lt.cluster, lt.key)
	if err != nil {
		snap = &pipeline.Snapshot{}
	}
	if snap.Migration == l.Head() {
		if a, _ := migrationlog.Hash(snap.Objects); a != "" {
			if b, _ := migrationlog.Hash(objects); a == b {
				return false, nil
			}
		}
	}
	snap.Objects, snap.Migration = objects, l.Head()
	if snap.Objects == nil {
		snap.Objects = map[string]json.RawMessage{}
	}
	return true, store.Save(lt.cluster, lt.key, snap)
}

func runLogRepair(clusterName, databaseName string) error {
	store, err := pipeline.MustResolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore)
	if err != nil {
		return err
	}
	color := ui.IsColorEnabled(os.Stdout)
	return eachLogTarget(clusterName, databaseName, func(_ *project.Project, lt logTarget) error {
		l, err := migrationlog.Load(lt.dir)
		if err != nil {
			return fmt.Errorf("%s: %w", lt.label, err)
		}
		if len(l.Entries) == 0 {
			ui.PrintInfo(os.Stdout, lt.label, "no migration log", color)
			return nil
		}
		changed, err := rewriteSnapshot(store, lt, l.Entries)
		if err != nil {
			return fmt.Errorf("%s: %w", lt.label, err)
		}
		if changed {
			ui.PrintSuccess(os.Stdout, "Repaired", lt.label+" — snapshot rewritten from the log (last migration "+l.Head()+")", color)
		} else {
			ui.PrintInfo(os.Stdout, lt.label, "the snapshot matches the log", color)
		}
		return nil
	})
}

func runLogRebase(clusterName, databaseName, keep string, allowDestructive, rebuild bool) error {
	store, err := pipeline.MustResolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore)
	if err != nil {
		return err
	}
	color := ui.IsColorEnabled(os.Stdout)
	found := false
	err = eachLogTarget(clusterName, databaseName, func(_ *project.Project, lt logTarget) error {
		entries, err := migrationlog.LoadEntries(lt.dir)
		if err != nil {
			return fmt.Errorf("%s: %w", lt.label, err)
		}
		has := false
		for _, e := range entries {
			if e.ID == keep {
				has = true
			}
			if e.Parent == keep {
				return fmt.Errorf("%s: --keep %s: migration %s follows it; name the last migration of the chain", lt.label, keep, e.ID)
			}
		}
		if !has {
			return nil
		}
		found = true
		kept, err := migrationlog.Chain(entries, keep)
		if err != nil {
			return fmt.Errorf("%s: %w", lt.label, err)
		}
		inKept := map[string]bool{}
		for _, e := range kept {
			inKept[e.ID] = true
		}
		var dropped []string
		for _, e := range entries {
			if !inKept[e.ID] {
				dropped = append(dropped, e.ID)
				if err := migrationlog.Remove(lt.dir, e.ID); err != nil {
					return err
				}
			}
		}
		if _, err := rewriteSnapshot(store, lt, kept); err != nil {
			return fmt.Errorf("%s: %w", lt.label, err)
		}
		if len(dropped) > 0 {
			ui.PrintSuccess(os.Stdout, "Rebased", fmt.Sprintf("%s — kept the chain to %s; removed %s (a database that ran them must be rebuilt)", lt.label, keep, strings.Join(dropped, ", ")), color)
		}
			return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("--keep: no migration log has %s", keep)
	}
	// The source's change, recorded again on top of the kept chain.
	return runRecord(recordOptions{cluster: clusterName, database: databaseName, record: true, allowDestructive: allowDestructive, rebuild: rebuild})
}
