// Package apply is how DPG brings a database to its migration log and
// source: preparing a target, settling the log's migrations it hasn't run,
// running and recording the source's change, the preflight, and verify's
// drift check. dpg apply, plan and verify, and the library engine
// (pkg/dpg/engine), all run through it, so there is one apply path.
package apply

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

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
	"github.com/thec1oud/dpg/internal/version"
)

// Target is one database, or a cluster's cluster-level objects, being
// applied: its compiled source, stored snapshot and migration log, and how
// to reach it.
type Target struct {
	Label        string // "prod/app" or "prod (cluster)"
	Cluster, Key string // Key: the database name or the cluster snapshot key (also the ledger's log)
	Store        pipeline.SnapshotStore
	Differ       pipeline.Differ
	Emitter      pipeline.Emitter
	Exec         pipeline.ApplyExecutor
	Connect      func(ctx context.Context) (*executor.PgxConn, error)
	// Live introspects the target's objects ([ignore] applied).
	Live    func(ctx context.Context, conn *executor.PgxConn) ([]pipeline.IRObject, error)
	Rules   *ignore.Rules
	AckFile string // the target's acknowledgements ("" for none)
	Desired []pipeline.IRObject
	Stored  *pipeline.Snapshot // as on disk
	Log     *migrationlog.Log
	LogErr  error // migrationlog.ErrNoBaseline when the snapshot predates its log
	Ops     []pipeline.DiffOp
	Meta    pipeline.MigrationMeta
}

// Components are the pipeline stages a target is prepared and run with.
type Components struct {
	Store   pipeline.SnapshotStore
	Differ  pipeline.Differ
	Emitter pipeline.Emitter
	Exec    pipeline.ApplyExecutor
}

// Options are a run's: dpg apply's flags, and what the project says.
type Options struct {
	AllowDestructive        bool
	ApprovePartitionRebuild bool
	// RefuseCaution refuses CAUTION statements too (the library engine's
	// safety gate; dpg apply always allows them).
	RefuseCaution bool
	DryRun        bool
	NoSnapshot    bool
	Baseline      bool
	Resume        bool
	AllowDrift    bool
	// Preflight checks the database before anything runs (dpg apply sets
	// it; it needs a connection, also for --dry-run).
	Preflight     bool
	DPGDir        string // the project's .dpg directory (acknowledgements)
	Strict        bool
	MigrationsDir string
	LintCfg       pipeline.LinterConfig
	Ignore        *ignore.Rules
	// Ledger is the databases' ledger; nil when it is turned off.
	Ledger *ledger.Ledger
	// Revision is the source's revision, recorded in the migration's
	// header.
	Revision string
	// Hook runs first in every transaction of a migration, and before each
	// of its statements outside one (see executor.Hooked); nil for none.
	Hook executor.Hook
	// Observe is told as each migration starts, and returns the context it
	// runs in and the function to call with its outcome; nil for none.
	Observe func(ctx context.Context, id string) (context.Context, func(error))
	// FinishHint says how the caller finishes a migration whose steps
	// after its transaction failed (dpg apply --resume); empty says the
	// next apply does.
	FinishHint string
}

// LedgerFor is the project's ledger, or nil when [ledger] turns it off.
func LedgerFor(root config.RootConfig) *ledger.Ledger {
	if !root.Ledger.On() {
		return nil
	}
	return &ledger.Ledger{Schema: root.Ledger.Schema, Version: version.Version}
}

// CompileOptions returns the compile options a database's dpg.toml implies:
// its default_schema (RFC Section 3.4).
func CompileOptions(db *project.Database) compiler.Options {
	return compiler.Options{DefaultSchema: db.Config.Database.DefaultSchema}
}

// Lint lints compiled objects together with the merge diagnostics; strict
// promotes warnings to errors. A diagnostic that is an error stops the
// caller (Printer.lint).
func Lint(desired []pipeline.IRObject, mergeDiags []pipeline.LintDiagnostic, cfg pipeline.LinterConfig, strict bool, p Printer) error {
	diags := linter.FilterMergeDiagnostics(mergeDiags, cfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, err := l.Lint(desired, cfg)
		if err != nil {
			return err
		}
		diags = append(diags, lintDiags...)
	}
	if strict {
		for i := range diags {
			diags[i].IsError = true
		}
	}
	return p.lint(diags)
}

// PrepareDatabase compiles and lints a database's source, loads its
// snapshot and migration log, and plans the source's change. connect
// reaches the database.
func PrepareDatabase(cl *project.Cluster, db *project.Database, c Components, connect func(ctx context.Context) (*executor.PgxConn, error), opts Options, p Printer) (*Target, error) {
	desired, mergeDiags, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, CompileOptions(db))
	if err != nil {
		return nil, err
	}
	if err := opts.Ignore.CheckDeclared(desired); err != nil {
		return nil, err
	}

	snap, err := c.Store.Load(cl.Name(), db.Name())
	if err != nil {
		return nil, fmt.Errorf("snapshot load: %w", err)
	}
	stored := *snap
	stored.Objects = CopyObjects(snap.Objects)
	log, logErr := OpenLog(opts.MigrationsDir, cl.Name(), db.Name(), &stored)
	if logErr != nil && !(opts.Baseline && errors.Is(logErr, migrationlog.ErrNoBaseline)) {
		return nil, NoBaselineHint(logErr)
	}
	if err := opts.Ignore.FilterSnapshot(snap); err != nil {
		return nil, err
	}

	lintCfg := opts.LintCfg
	lintCfg.Snapshot = snap
	if err := Lint(desired, mergeDiags, lintCfg, opts.Strict, p); err != nil {
		return nil, err
	}

	ops, err := c.Differ.Diff(desired, snap)
	if err != nil {
		return nil, err
	}
	return &Target{
		Label: cl.Name() + "/" + db.Name(), Cluster: cl.Name(), Key: db.Name(),
		Store: c.Store, Differ: c.Differ, Emitter: c.Emitter, Exec: c.Exec,
		Connect: connect,
		Live:    LiveObjects(opts.Ignore, false),
		Rules:   opts.Ignore,
		AckFile: AckFile(opts.DPGDir, cl.Name(), db.Name()),
		Desired: desired, Stored: &stored, Log: log, LogErr: logErr, Ops: ops,
		Meta: pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), SourceRevision: opts.Revision, Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: db.Name()},
	}, nil
}

// PrepareCluster is PrepareDatabase for a cluster's cluster-level objects.
func PrepareCluster(cl *project.Cluster, c Components, connect func(ctx context.Context) (*executor.PgxConn, error), opts Options, p Printer) (*Target, error) {
	desired, mergeDiags, err := compiler.Compile(cl.SourceFiles, cl.ObjectsDir, pipeline.Default)
	if err != nil {
		return nil, err
	}
	if err := opts.Ignore.CheckDeclared(desired); err != nil {
		return nil, err
	}
	if err := Lint(desired, mergeDiags, opts.LintCfg, opts.Strict, p); err != nil {
		return nil, err
	}

	snap, err := c.Store.Load(cl.Name(), cl.ClusterSnapshotKey())
	if err != nil {
		return nil, fmt.Errorf("snapshot load: %w", err)
	}
	stored := *snap
	stored.Objects = CopyObjects(snap.Objects)
	log, logErr := OpenLog(opts.MigrationsDir, cl.Name(), cl.ClusterSnapshotKey(), &stored)
	if logErr != nil && !(opts.Baseline && errors.Is(logErr, migrationlog.ErrNoBaseline)) {
		return nil, NoBaselineHint(logErr)
	}
	if err := opts.Ignore.FilterSnapshot(snap); err != nil {
		return nil, err
	}

	ops, err := c.Differ.Diff(desired, snap)
	if err != nil {
		return nil, err
	}
	return &Target{
		Label: cl.Name() + " (cluster)", Cluster: cl.Name(), Key: cl.ClusterSnapshotKey(),
		Store: c.Store, Differ: c.Differ, Emitter: c.Emitter, Exec: c.Exec,
		Connect: connect,
		Live:    LiveObjects(opts.Ignore, true),
		Rules:   opts.Ignore,
		AckFile: AckFile(opts.DPGDir, cl.Name(), cl.ClusterSnapshotKey()),
		Desired: desired, Stored: &stored, Log: log, LogErr: logErr, Ops: ops,
		Meta: pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), SourceRevision: opts.Revision, Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: cl.ClusterSnapshotKey()},
	}, nil
}

// AckFile is a target's acknowledgement file, or "" without a project.
func AckFile(dpgDir, cluster, key string) string {
	if dpgDir == "" {
		return ""
	}
	return filepath.Join(dpgDir, "acknowledged", cluster, key+".json")
}

// LiveObjects introspects a target's objects: a database's, or (cluster)
// the cluster-level ones; [ignore] applied.
func LiveObjects(rules *ignore.Rules, cluster bool) func(ctx context.Context, conn *executor.PgxConn) ([]pipeline.IRObject, error) {
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
			if IsClusterScoped(o) == cluster {
				out = append(out, o)
			}
		}
		return out, nil
	}
}

// IsClusterScoped reports whether obj lives at the cluster level (shared across
// every database) rather than inside one database. Roles, tablespaces, and
// PARAMETER PRIVILEGES (RFC Section 11.6 — configuration parameters have no
// schema and no per-database identity; the RFC's own worked example shows it
// living at "production/cluster/parameter_privileges.dpg") qualify; all other
// schemaless objects (FDWs, servers, user mappings, publications, event
// triggers, casts) are database-scoped.
func IsClusterScoped(obj pipeline.IRObject) bool {
	switch obj.(type) {
	case *ir.Role, *ir.Tablespace, *ir.ParameterPrivileges:
		return true
	}
	return false
}

// ExcludeClusterScoped returns the objects that belong to a database (i.e. not
// cluster-scoped), preserving order. Used to build database-level baselines for
// verify and plan --live so roles/tablespaces are handled only at cluster level.
func ExcludeClusterScoped(objs []pipeline.IRObject) []pipeline.IRObject {
	out := make([]pipeline.IRObject, 0, len(objs))
	for _, obj := range objs {
		if !IsClusterScoped(obj) {
			out = append(out, obj)
		}
	}
	return out
}
