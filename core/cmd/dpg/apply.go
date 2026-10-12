package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/linter"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

func newApplyCmd() *cobra.Command {
	var (
		clusterName             string
		databaseName            string
		yes                     bool
		allowDestructive        bool
		approvePartitionRebuild bool
		dryRun                  bool
		noSnapshot              bool
		baseline                bool
		strict                  bool
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute the planned migration and update the snapshot",
		Long: `Runs dpg plan, prompts for approval, executes the SQL against the
primary node, and updates the committed snapshot on success.

Destructive operations are blocked unless --allow-destructive is set.
Partition strategy changes additionally require --approve-partition-rebuild.

With --dry-run, the migration is computed and printed but never executed.
With --no-snapshot, the snapshot is not updated after a successful apply.
With --strict, lint warnings are promoted to errors and block the apply.

Each apply records its migration in the migration log beside the
snapshot. A project whose snapshot predates the log starts it with
--baseline, which records the snapshot as the first migration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			proj, err := discoverProject()
			if err != nil {
				return err
			}
			loadEnv(proj, envFile)

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
			applyExec, err := pipeline.MustResolve[pipeline.ApplyExecutor](pipeline.Default, pipeline.KeyApplyExecutor)
			if err != nil {
				return err
			}
			secretResolver, err := pipeline.MustResolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
			if err != nil {
				return err
			}

			opts := applyOptions{
				yes:                     yes,
				allowDestructive:        allowDestructive,
				approvePartitionRebuild: approvePartitionRebuild,
				dryRun:                  dryRun,
				noSnapshot:              noSnapshot,
				baseline:                baseline,
				strict:                  strict,
				migrationsDir:           proj.MigrationsDir(),
				lintCfg:                 linterConfigFrom(proj.RootConfig.Linter),
			}

			for _, cl := range clusters {
				// Apply cluster-level objects (roles) before databases.
				if len(cl.SourceFiles) > 0 {
					clusterOpts := opts
					clusterOpts.lintCfg.MinPGVersion = cl.EffectiveMinPGVersion(proj.RootConfig)
					clusterOpts.ignore = ignore.New(cl.EffectiveIgnore(proj.RootConfig))
					if err := runClusterApply(cl, store, differ, emitter, applyExec, secretResolver, clusterOpts); err != nil {
						return fmt.Errorf("%s (cluster): %w", cl.Name(), err)
					}
				}

				databases, err := resolveDatabases(cl, databaseName)
				if err != nil {
					return err
				}
				for _, db := range databases {
					dbOpts := opts
					dbOpts.lintCfg.MinPGVersion = db.EffectiveMinPGVersion(cl, proj.RootConfig)
					dbOpts.ignore = ignore.New(db.EffectiveIgnore(cl, proj.RootConfig))
					if err := runApply(cl, db, store, differ, emitter, applyExec, secretResolver, dbOpts); err != nil {
						return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to apply (required when multiple clusters exist)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to apply (required when multiple databases exist)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip interactive approval prompt")
	cmd.Flags().BoolVar(&allowDestructive, "allow-destructive", false, "allow destructive operations")
	cmd.Flags().BoolVar(&approvePartitionRebuild, "approve-partition-rebuild", false,
		"acknowledge an instruction-only partition strategy rebuild step")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the migration plan but do not execute or update the snapshot")
	cmd.Flags().BoolVar(&noSnapshot, "no-snapshot", false, "skip snapshot update after a successful apply")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat lint warnings as errors (blocks apply if any exist)")
	cmd.Flags().BoolVar(&baseline, "baseline", false, "start the migration log of a project whose snapshot predates it")

	return cmd
}

type applyOptions struct {
	yes                     bool
	allowDestructive        bool
	approvePartitionRebuild bool
	dryRun                  bool
	noSnapshot              bool
	baseline                bool
	strict                  bool
	migrationsDir           string
	lintCfg                 pipeline.LinterConfig
	ignore                  *ignore.Rules
}

// applyBaseline handles dpg apply --baseline for one database.
func applyBaseline(log *migrationlog.Log, logErr error, store pipeline.SnapshotStore, cluster, key, label string, stored *pipeline.Snapshot,
	desired []pipeline.IRObject, ops []pipeline.DiffOp, differ pipeline.Differ, emitter pipeline.Emitter, meta pipeline.MigrationMeta, opts applyOptions, color bool) error {
	if logErr == nil {
		ui.PrintInfo(os.Stdout, label, "the migration log already started at "+firstID(log), color)
		return nil
	}
	if opts.dryRun {
		ui.PrintInfo(os.Stdout, "dry-run", label+": would record the snapshot as the baseline migration", color)
		return nil
	}
	e, err := writeBaseline(log, store, cluster, key, stored, desired, ops, differ, emitter, meta)
	if err != nil {
		return err
	}
	path, _ := log.Paths(e.ID)
	ui.PrintSuccess(os.Stdout, "Baseline", label+" — migration log started\n         "+ui.Dim(path, color), color)
	return nil
}

func firstID(l *migrationlog.Log) string {
	if len(l.Entries) == 0 {
		return "(empty)"
	}
	return l.Entries[0].ID
}

// isInstructionOp reports whether op is an instruction-only step (RFC Section
// 17.3): shown in the plan, never sent to the executor.
func isInstructionOp(op pipeline.DiffOp) bool {
	return op.Mode() == pipeline.ModeInstructionOnly
}

func runApply(
	cl *project.Cluster,
	db *project.Database,
	store pipeline.SnapshotStore,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	applyExec pipeline.ApplyExecutor,
	secretResolver pipeline.SecretResolver,
	opts applyOptions,
) error {
	ctx := context.Background()
	color := ui.IsColorEnabled(os.Stdout)
	errColor := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, dbCompileOptions(db))
	if err != nil {
		return err
	}
	if err := opts.ignore.CheckDeclared(desired); err != nil {
		return err
	}

	snap, err := store.Load(cl.Name(), db.Name())
	if err != nil {
		return fmt.Errorf("snapshot load: %w", err)
	}
	stored := *snap
	stored.Objects = copyObjects(snap.Objects)
	log, logErr := openLog(opts.migrationsDir, cl.Name(), db.Name(), &stored)
	if logErr != nil && !(opts.baseline && errors.Is(logErr, migrationlog.ErrNoBaseline)) {
		return noBaselineHint(logErr)
	}
	if err := opts.ignore.FilterSnapshot(snap); err != nil {
		return err
	}

	lintCfg := opts.lintCfg
	lintCfg.Snapshot = snap
	diags := linter.FilterMergeDiagnostics(mergeDiags, lintCfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, lintErr := l.Lint(desired, lintCfg)
		if lintErr != nil {
			return lintErr
		}
		diags = append(diags, lintDiags...)
	}
	if opts.strict {
		for i := range diags {
			diags[i].IsError = true
		}
	}
	if ui.PrintLintDiagnostics(os.Stderr, diags, errColor) {
		return ui.ErrSilent
	}

	ops, err := differ.Diff(desired, snap)
	if err != nil {
		return err
	}

	if opts.baseline {
		return applyBaseline(log, logErr, store, cl.Name(), db.Name(), cl.Name()+"/"+db.Name(), &stored, desired, ops, differ, emitter,
			pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: db.Name()}, opts, color)
	}

	if len(ops) == 0 {
		ui.PrintInfo(os.Stdout, cl.Name()+"/"+db.Name(), "already up to date", color)
		return nil
	}

	// Warn about new tables being created without a primary key.
	warnMissingPK(os.Stderr, desired, snap, errColor)

	if !opts.allowDestructive {
		for _, op := range ops {
			if op.Safety() == pipeline.Destructive && !isInstructionOp(op) {
				return fmt.Errorf("DPG-E025: migration contains destructive operations; re-run with --allow-destructive to proceed\n  first: %s", op.SQL())
			}
		}
	}

	// Instruction-only ops are shown in the plan but never sent to the
	// executor.
	var instructionOps []pipeline.DiffOp
	var executableOps []pipeline.DiffOp
	for _, op := range ops {
		if isInstructionOp(op) {
			instructionOps = append(instructionOps, op)
		} else {
			executableOps = append(executableOps, op)
		}
	}
	// A Destructive instruction (a partition strategy rebuild) is a step
	// the operator must perform; apply refuses to continue until it is
	// acknowledged. Safe instructions are informational notes.
	if !opts.approvePartitionRebuild {
		for _, op := range instructionOps {
			if op.Safety() == pipeline.Destructive {
				return fmt.Errorf("migration requires a manual partition strategy rebuild;\nre-run with --approve-partition-rebuild to acknowledge and proceed\n  first: %s", op.SQL())
			}
		}
	}

	rev, _ := gitRevision()
	// Emit the full op list so the printed plan includes the instruction ops.
	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{
		GeneratedAt:    time.Now().UTC(),
		SourceRevision: rev,
		Cluster:        cl.Name(),
		SourceRoot:     cl.ProjectRoot,
		Database:       db.Name(),
	})
	if err != nil {
		return err
	}

	// Render plain SQL for the archive file.
	var sqlBuf strings.Builder
	if err := emit.Render(&sqlBuf, migration, emit.DefaultRenderOptions()); err != nil {
		return err
	}
	plainSQL := sqlBuf.String()

	// Print with colour to stdout.
	if err := emit.Render(os.Stdout, migration, emit.RenderOptions{
		ShowSafety:    true,
		ShowSourcePos: true,
		Color:         color,
	}); err != nil {
		return err
	}

	if opts.dryRun {
		ui.PrintInfo(os.Stdout, "dry-run", "migration not applied; snapshot not updated", color)
		return nil
	}

	if !opts.yes {
		fmt.Printf("\n%s [y/N] ", ui.Bold("Apply this migration?", color))
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
			ui.PrintInfo(os.Stdout, "", "Aborted.", color)
			return nil
		}
	}

	conn, err := connectDatabase(ctx, cl, db, secretResolver)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	// RFC Section 11.5: before any DDL runs, every role a SET ROLE/OWNER TO
	// statement in this migration will target must count the connecting
	// role as a member — checked upfront so a missing membership surfaces
	// as one DPG-E036 error, not a bare PostgreSQL failure mid-migration.
	if err := executor.ValidateOwnerMembership(ctx, conn, executableOps); err != nil {
		return err
	}

	// Execute only non-instruction ops. Instruction ops are manual steps the
	// operator must perform outside DPG; they must not be sent to the executor.
	execMigration, err := emitter.Emit(executableOps, pipeline.MigrationMeta{
		GeneratedAt:    time.Now().UTC(),
		SourceRevision: rev,
		Cluster:        cl.Name(),
		SourceRoot:     cl.ProjectRoot,
		Database:       db.Name(),
	})
	if err != nil {
		return err
	}
	if err := applyExec.Apply(ctx, execMigration, conn); err != nil {
		return ui.WrapDB(err)
	}

	// Record the migration in the log, then the snapshot it produced.
	var migPath string
	if !opts.noSnapshot {
		newSnap := &pipeline.Snapshot{}
		if err := snapshotpkg.Populate(newSnap, desired); err != nil {
			return fmt.Errorf("build snapshot: %w", err)
		}
		e, undo, err := recordMigration(log, plainSQL, migrationlog.OriginApply, stored.Objects, newSnap)
		if err != nil {
			return err
		}
		if err := store.Save(cl.Name(), db.Name(), newSnap); err != nil {
			undo()
			return fmt.Errorf("save snapshot: %w", err)
		}
		migPath, _ = log.Paths(e.ID)
	}

	detail := cl.Name() + "/" + db.Name()
	if opts.noSnapshot {
		detail += " — snapshot and migration log not updated (--no-snapshot)"
	} else {
		detail += " — snapshot updated"
	}
	if migPath != "" {
		detail += "\n         " + ui.Dim(migPath, color)
	}
	ui.PrintSuccess(os.Stdout, "Applied", detail, color)
	return nil
}

// runClusterApply applies cluster-level objects (roles, tablespaces, etc.).
func runClusterApply(
	cl *project.Cluster,
	store pipeline.SnapshotStore,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	applyExec pipeline.ApplyExecutor,
	secretResolver pipeline.SecretResolver,
	opts applyOptions,
) error {
	ctx := context.Background()
	color := ui.IsColorEnabled(os.Stdout)
	errColor := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.Compile(cl.SourceFiles, cl.ObjectsDir, pipeline.Default)
	if err != nil {
		return err
	}
	if err := opts.ignore.CheckDeclared(desired); err != nil {
		return err
	}

	diags := linter.FilterMergeDiagnostics(mergeDiags, opts.lintCfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, lintErr := l.Lint(desired, opts.lintCfg)
		if lintErr != nil {
			return lintErr
		}
		diags = append(diags, lintDiags...)
	}
	if opts.strict {
		for i := range diags {
			diags[i].IsError = true
		}
	}
	if ui.PrintLintDiagnostics(os.Stderr, diags, errColor) {
		return ui.ErrSilent
	}

	snap, err := store.Load(cl.Name(), cl.ClusterSnapshotKey())
	if err != nil {
		return fmt.Errorf("snapshot load: %w", err)
	}
	stored := *snap
	stored.Objects = copyObjects(snap.Objects)
	log, logErr := openLog(opts.migrationsDir, cl.Name(), cl.ClusterSnapshotKey(), &stored)
	if logErr != nil && !(opts.baseline && errors.Is(logErr, migrationlog.ErrNoBaseline)) {
		return noBaselineHint(logErr)
	}
	if err := opts.ignore.FilterSnapshot(snap); err != nil {
		return err
	}

	ops, err := differ.Diff(desired, snap)
	if err != nil {
		return err
	}
	if opts.baseline {
		return applyBaseline(log, logErr, store, cl.Name(), cl.ClusterSnapshotKey(), cl.Name()+" (cluster)", &stored, desired, ops, differ, emitter,
			pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: cl.ClusterSnapshotKey()}, opts, color)
	}
	if len(ops) == 0 {
		ui.PrintInfo(os.Stdout, cl.Name()+" (cluster)", "already up to date", color)
		return nil
	}

	if !opts.allowDestructive {
		for _, op := range ops {
			if op.Safety() == pipeline.Destructive && !isInstructionOp(op) {
				return fmt.Errorf("DPG-E025: cluster migration contains destructive operations; re-run with --allow-destructive\n  first: %s", op.SQL())
			}
		}
	}

	var instructionOps []pipeline.DiffOp
	var executableOps []pipeline.DiffOp
	for _, op := range ops {
		if isInstructionOp(op) {
			instructionOps = append(instructionOps, op)
		} else {
			executableOps = append(executableOps, op)
		}
	}
	if len(instructionOps) > 0 && !opts.approvePartitionRebuild {
		return fmt.Errorf("cluster migration requires manual steps (partition strategy change or similar);\nre-run with --approve-partition-rebuild to acknowledge and proceed\n  first: %s", instructionOps[0].SQL())
	}

	rev, _ := gitRevision()
	migration, err := emitter.Emit(ops, pipeline.MigrationMeta{
		GeneratedAt:    time.Now().UTC(),
		SourceRevision: rev,
		Cluster:        cl.Name(),
		SourceRoot:     cl.ProjectRoot,
		Database:       cl.ClusterSnapshotKey(),
	})
	if err != nil {
		return err
	}

	var sqlBuf strings.Builder
	if err := emit.Render(&sqlBuf, migration, emit.DefaultRenderOptions()); err != nil {
		return err
	}

	if err := emit.Render(os.Stdout, migration, emit.RenderOptions{
		ShowSafety:    true,
		ShowSourcePos: true,
		Color:         color,
	}); err != nil {
		return err
	}

	if opts.dryRun {
		ui.PrintInfo(os.Stdout, "dry-run", "migration not applied; snapshot not updated", color)
		return nil
	}

	if !opts.yes {
		fmt.Printf("\n%s [y/N] ", ui.Bold("Apply cluster changes?", color))
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
			ui.PrintInfo(os.Stdout, "", "Aborted.", color)
			return nil
		}
	}

	connStr := cl.ConnectionString()
	if connStr == "" {
		return fmt.Errorf("DPG-E003: cluster %q has no connection configured", cl.Name())
	}
	if cl.IsLink() {
		connStr, err = secretResolver.Resolve(connStr)
		if err != nil {
			return ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
		}
	}

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		return ui.WrapDB(err)
	}
	defer conn.Close(ctx)

	execMigration, err := emitter.Emit(executableOps, pipeline.MigrationMeta{
		GeneratedAt:    time.Now().UTC(),
		SourceRevision: rev,
		Cluster:        cl.Name(),
		SourceRoot:     cl.ProjectRoot,
		Database:       cl.ClusterSnapshotKey(),
	})
	if err != nil {
		return err
	}
	if err := applyExec.Apply(ctx, execMigration, conn); err != nil {
		return ui.WrapDB(err)
	}

	var migPath string
	if !opts.noSnapshot {
		newSnap := &pipeline.Snapshot{}
		if err := snapshotpkg.Populate(newSnap, desired); err != nil {
			return fmt.Errorf("build cluster snapshot: %w", err)
		}
		e, undo, err := recordMigration(log, sqlBuf.String(), migrationlog.OriginApply, stored.Objects, newSnap)
		if err != nil {
			return err
		}
		if err := store.Save(cl.Name(), cl.ClusterSnapshotKey(), newSnap); err != nil {
			undo()
			return fmt.Errorf("save cluster snapshot: %w", err)
		}
		migPath, _ = log.Paths(e.ID)
	}

	detail := cl.Name() + " (cluster)"
	if opts.noSnapshot {
		detail += " — snapshot and migration log not updated (--no-snapshot)"
	} else {
		detail += " — snapshot updated"
	}
	if migPath != "" {
		detail += "\n         " + ui.Dim(migPath, color)
	}
	ui.PrintSuccess(os.Stdout, "Applied", detail, color)
	return nil
}

// warnMissingPK writes a bold warning for every table that is being newly
// created (absent from snap) and has no PRIMARY KEY constraint.
func warnMissingPK(w io.Writer, desired []pipeline.IRObject, snap *pipeline.Snapshot, color bool) {
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
			fmt.Fprintf(w, "%s  table %s has no PRIMARY KEY — consider adding one\n",
				ui.Bold("WARNING", color), tbl.QualifiedName())
		}
	}
}
