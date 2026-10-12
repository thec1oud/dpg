package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/ledger"
	"github.com/thec1oud/dpg/internal/linter"
	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/ui"
	"github.com/thec1oud/dpg/internal/version"
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
		resume                  bool
		allowDrift              bool
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

Before anything runs (also with --dry-run), apply checks the database is
as the migrations expect: no drift on the objects they change, nothing
they create already there, nothing DPG doesn't manage depending on what
they drop or recreate. It refuses (exit 1) otherwise; --allow-drift
proceeds anyway. Each migration also carries these checks as guards
that any runner executes.

Each apply records its migration in the migration log beside the
snapshot, and each database records the migrations it has run in its
ledger table. apply first runs the log's migrations a database hasn't
run (a database that is behind catches up), then the source's change.

--baseline starts the log of a snapshot that predates it, and records in
a database that has the log's objects but no ledger how far along the
log it is. --resume finishes a migration whose steps after its
transaction failed.`,
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
				resume:                  resume,
				allowDrift:              allowDrift,
				preflight:               true,
				dpgDir:                  proj.DPGDir(),
				strict:                  strict,
				migrationsDir:           proj.MigrationsDir(),
				lintCfg:                 linterConfigFrom(proj.RootConfig.Linter),
				ledger:                  ledgerFor(proj.RootConfig),
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
	cmd.Flags().BoolVar(&baseline, "baseline", false, "start the migration log of a snapshot that predates it, and record how far along it each database is")
	cmd.Flags().BoolVar(&resume, "resume", false, "finish a migration whose steps after its transaction failed")
	cmd.Flags().BoolVar(&allowDrift, "allow-drift", false, "run even when the database isn't as the migration expects (and skip the migration's guards)")

	return cmd
}

type applyOptions struct {
	yes                     bool
	allowDestructive        bool
	approvePartitionRebuild bool
	dryRun                  bool
	noSnapshot              bool
	baseline                bool
	resume                  bool
	allowDrift              bool
	// preflight checks the database before anything runs (dpg apply sets
	// it; it needs a connection, also for --dry-run).
	preflight     bool
	dpgDir        string // the project's .dpg directory (acknowledgements)
	strict        bool
	migrationsDir string
	lintCfg       pipeline.LinterConfig
	ignore        *ignore.Rules
	// ledger is the databases' ledger; nil when it is turned off.
	ledger *ledger.Ledger
}

// ledgerFor is the project's ledger, or nil when [ledger] turns it off.
func ledgerFor(root config.RootConfig) *ledger.Ledger {
	if !root.Ledger.On() {
		return nil
	}
	return &ledger.Ledger{Schema: root.Ledger.Schema, Version: version.Version}
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
	t, err := prepareDBTarget(cl, db, store, differ, emitter, applyExec, secretResolver, opts)
	if err != nil {
		return err
	}
	return runTarget(t, opts)
}

// prepareDBTarget compiles and lints a database's source, loads its
// snapshot and migration log, and plans the source's change.
func prepareDBTarget(
	cl *project.Cluster,
	db *project.Database,
	store pipeline.SnapshotStore,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	applyExec pipeline.ApplyExecutor,
	secretResolver pipeline.SecretResolver,
	opts applyOptions,
) (*applyTarget, error) {
	errColor := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, dbCompileOptions(db))
	if err != nil {
		return nil, err
	}
	if err := opts.ignore.CheckDeclared(desired); err != nil {
		return nil, err
	}

	snap, err := store.Load(cl.Name(), db.Name())
	if err != nil {
		return nil, fmt.Errorf("snapshot load: %w", err)
	}
	stored := *snap
	stored.Objects = copyObjects(snap.Objects)
	log, logErr := openLog(opts.migrationsDir, cl.Name(), db.Name(), &stored)
	if logErr != nil && !(opts.baseline && errors.Is(logErr, migrationlog.ErrNoBaseline)) {
		return nil, noBaselineHint(logErr)
	}
	if err := opts.ignore.FilterSnapshot(snap); err != nil {
		return nil, err
	}

	lintCfg := opts.lintCfg
	lintCfg.Snapshot = snap
	diags := linter.FilterMergeDiagnostics(mergeDiags, lintCfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, lintErr := l.Lint(desired, lintCfg)
		if lintErr != nil {
			return nil, lintErr
		}
		diags = append(diags, lintDiags...)
	}
	if opts.strict {
		for i := range diags {
			diags[i].IsError = true
		}
	}
	if ui.PrintLintDiagnostics(os.Stderr, diags, errColor) {
		return nil, ui.ErrSilent
	}

	ops, err := differ.Diff(desired, snap)
	if err != nil {
		return nil, err
	}
	rev, _ := gitRevision()
	return &applyTarget{
		label: cl.Name() + "/" + db.Name(), cluster: cl.Name(), key: db.Name(),
		store: store, differ: differ, emitter: emitter, exec: applyExec,
		connect: func(ctx context.Context) (*executor.PgxConn, error) {
			return connectDatabase(ctx, cl, db, secretResolver)
		},
		live:    liveObjects(opts.ignore, false),
		rules:   opts.ignore,
		ackFile: ackFileFor(opts.dpgDir, cl.Name(), db.Name()),
		desired: desired, stored: &stored, log: log, logErr: logErr, ops: ops,
		meta: pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), SourceRevision: rev, Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: db.Name()},
	}, nil
}

// ackFileFor is a target's acknowledgement file, or "" without a project.
func ackFileFor(dpgDir, cluster, key string) string {
	if dpgDir == "" {
		return ""
	}
	return filepath.Join(dpgDir, "acknowledged", cluster, key+".json")
}

// connectCluster connects to a cluster's own connection (its cluster-level
// objects and their ledger).
func connectCluster(ctx context.Context, cl *project.Cluster, secretResolver pipeline.SecretResolver) (*executor.PgxConn, error) {
	connStr := cl.ConnectionString()
	if connStr == "" {
		return nil, fmt.Errorf("DPG-E003: cluster %q has no connection configured", cl.Name())
	}
	if cl.IsLink() {
		var err error
		if connStr, err = secretResolver.Resolve(connStr); err != nil {
			return nil, ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
		}
	}
	conn, err := executor.Connect(ctx, connStr)
	return conn, ui.WrapDB(err)
}

// liveObjects introspects a target's objects: a database's, or (cluster)
// the cluster-level ones; [ignore] applied.
func liveObjects(rules *ignore.Rules, cluster bool) func(ctx context.Context, conn *executor.PgxConn) ([]pipeline.IRObject, error) {
	return func(ctx context.Context, conn *executor.PgxConn) ([]pipeline.IRObject, error) {
		introspector, err := pipeline.MustResolve[pipeline.Introspector](pipeline.Default, pipeline.KeyIntrospector)
		if err != nil {
			return nil, err
		}
		all, err := introspector.Introspect(ctx, conn)
		if err != nil {
			return nil, err
		}
		var out []pipeline.IRObject
		for _, o := range rules.FilterObjects(all) {
			if isClusterScoped(o) == cluster {
				out = append(out, o)
			}
		}
		return out, nil
	}
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
	t, err := prepareClusterTarget(cl, store, differ, emitter, applyExec, secretResolver, opts)
	if err != nil {
		return err
	}
	return runTarget(t, opts)
}

// prepareClusterTarget is prepareDBTarget for a cluster's cluster-level
// objects.
func prepareClusterTarget(
	cl *project.Cluster,
	store pipeline.SnapshotStore,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	applyExec pipeline.ApplyExecutor,
	secretResolver pipeline.SecretResolver,
	opts applyOptions,
) (*applyTarget, error) {
	errColor := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.Compile(cl.SourceFiles, cl.ObjectsDir, pipeline.Default)
	if err != nil {
		return nil, err
	}
	if err := opts.ignore.CheckDeclared(desired); err != nil {
		return nil, err
	}

	diags := linter.FilterMergeDiagnostics(mergeDiags, opts.lintCfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, lintErr := l.Lint(desired, opts.lintCfg)
		if lintErr != nil {
			return nil, lintErr
		}
		diags = append(diags, lintDiags...)
	}
	if opts.strict {
		for i := range diags {
			diags[i].IsError = true
		}
	}
	if ui.PrintLintDiagnostics(os.Stderr, diags, errColor) {
		return nil, ui.ErrSilent
	}

	snap, err := store.Load(cl.Name(), cl.ClusterSnapshotKey())
	if err != nil {
		return nil, fmt.Errorf("snapshot load: %w", err)
	}
	stored := *snap
	stored.Objects = copyObjects(snap.Objects)
	log, logErr := openLog(opts.migrationsDir, cl.Name(), cl.ClusterSnapshotKey(), &stored)
	if logErr != nil && !(opts.baseline && errors.Is(logErr, migrationlog.ErrNoBaseline)) {
		return nil, noBaselineHint(logErr)
	}
	if err := opts.ignore.FilterSnapshot(snap); err != nil {
		return nil, err
	}

	ops, err := differ.Diff(desired, snap)
	if err != nil {
		return nil, err
	}
	rev, _ := gitRevision()
	return &applyTarget{
		label: cl.Name() + " (cluster)", cluster: cl.Name(), key: cl.ClusterSnapshotKey(),
		store: store, differ: differ, emitter: emitter, exec: applyExec,
		connect: func(ctx context.Context) (*executor.PgxConn, error) {
			return connectCluster(ctx, cl, secretResolver)
		},
		live:    liveObjects(opts.ignore, true),
		rules:   opts.ignore,
		ackFile: ackFileFor(opts.dpgDir, cl.Name(), cl.ClusterSnapshotKey()),
		desired: desired, stored: &stored, log: log, logErr: logErr, ops: ops,
		meta: pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), SourceRevision: rev, Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: cl.ClusterSnapshotKey()},
	}, nil
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
