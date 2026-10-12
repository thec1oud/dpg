package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/linter"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

// planJSON is the machine-readable form of a single database plan.
type planJSON struct {
	Cluster        string   `json:"cluster"`
	Database       string   `json:"database"`
	GeneratedAt    string   `json:"generated_at"`
	SourceRevision string   `json:"source_revision,omitempty"`
	Ops            []opJSON `json:"ops"`
	Empty          bool     `json:"empty"`
}

type opJSON struct {
	SQL    string `json:"sql"`
	Safety string `json:"safety"`
	Mode   string `json:"mode"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// linterConfigFrom converts a config.LinterConfig (from dpg.toml) to the
// pipeline.LinterConfig used by Linter.Lint.
func linterConfigFrom(c config.LinterConfig) pipeline.LinterConfig {
	return pipeline.LinterConfig{
		WarnOnDeprecated:         c.WarnOnDeprecated,
		RequireColumnComments:    c.RequireColumnComments,
		ForbidHardcodedPasswords: c.ForbidHardcodedPasswords,
		MaxColumnsPerTable:       c.MaxColumnsPerTable,
		Rules:                    normalizeLinterRuleKeys(c.Rules),
	}
}

// normalizeLinterRuleKeys normalizes [linter.rules] TOML keys from
// snake_case to the actual kebab-case rule IDs (RFC audit item #17):
// Section 19.3 gives kebab-case (e.g. "security-definer-search-path") as
// authoritative, matching every real rule ID internal/linter/linter.go
// emits, but Section 19.2's own worked config example still shows the broken
// snake_case form ("security_definer_search_path = ...") as if it works.
// ApplyRuleSeverityOverrides does an exact-string lookup against the rule
// ID a diagnostic actually carries, so an unnormalized snake_case key
// never matched anything — silently no-oping the RFC's own documented
// example. No rule ID mixes hyphens and underscores (confirmed against
// Section 19.3's complete table), so a blind "_" -> "-" replacement can't
// collide two distinct keys into one.
func normalizeLinterRuleKeys(rules map[string]string) map[string]string {
	if len(rules) == 0 {
		return rules
	}
	out := make(map[string]string, len(rules))
	for k, v := range rules {
		out[strings.ReplaceAll(k, "_", "-")] = v
	}
	return out
}

func newPlanCmd() *cobra.Command {
	var (
		clusterName  string
		databaseName string
		live         bool
		format       string
		watch        bool
	)

	runOnce := func(cmd *cobra.Command) error {
		proj, err := discoverProject()
		if err != nil {
			return err
		}

		clusters, err := resolveClusters(proj, clusterName)
		if err != nil {
			return err
		}

		lintCfg := linterConfigFrom(proj.RootConfig.Linter)

		differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
		if err != nil {
			return err
		}
		emitter, err := pipeline.MustResolve[pipeline.Emitter](pipeline.Default, pipeline.KeyEmitter)
		if err != nil {
			return err
		}

		var (
			introspector   pipeline.Introspector
			secretResolver pipeline.SecretResolver
			store          pipeline.SnapshotStore
		)
		if live {
			loadEnv(proj, envFile)
			introspector, err = pipeline.MustResolve[pipeline.Introspector](pipeline.Default, pipeline.KeyIntrospector)
			if err != nil {
				return err
			}
			secretResolver, err = pipeline.MustResolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
			if err != nil {
				return err
			}
		} else {
			store, err = pipeline.MustResolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore)
			if err != nil {
				return err
			}
		}

		var migrations []pipeline.Migration

		for _, cl := range clusters {
			if len(cl.SourceFiles) > 0 {
				var clusterSnap *pipeline.Snapshot
				if live {
					clusterSnap, err = introspectClusterSnapshot(cmd.Context(), cl, secretResolver, introspector)
					if err != nil {
						return fmt.Errorf("%s (cluster): %w", cl.Name(), err)
					}
				} else {
					clusterSnap, err = store.Load(cl.Name(), cl.ClusterSnapshotKey())
					if err != nil {
						return fmt.Errorf("%s (cluster): snapshot: %w", cl.Name(), err)
					}
				}
				clusterLintCfg := lintCfg
				clusterLintCfg.MinPGVersion = cl.EffectiveMinPGVersion(proj.RootConfig)
				m, err := buildClusterPlan(cl, clusterSnap, differ, emitter, clusterLintCfg, format)
				if err != nil {
					return fmt.Errorf("%s (cluster): %w", cl.Name(), err)
				}
				if format != "json" {
					migrations = append(migrations, m)
				}
			}

			databases, err := resolveDatabases(cl, databaseName)
			if err != nil {
				return err
			}
			for _, db := range databases {
				var snap *pipeline.Snapshot
				if live {
					snap, err = introspectSnapshot(cmd.Context(), cl, db, secretResolver, introspector)
					if err != nil {
						return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
					}
				} else {
					snap, err = store.Load(cl.Name(), db.Name())
					if err != nil {
						return fmt.Errorf("%s/%s: snapshot: %w", cl.Name(), db.Name(), err)
					}
				}
				dbLintCfg := lintCfg
				dbLintCfg.MinPGVersion = db.EffectiveMinPGVersion(cl, proj.RootConfig)
				m, err := buildPlan(cl, db, snap, differ, emitter, dbLintCfg, format)
				if err != nil {
					return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
				}
				if format != "json" {
					migrations = append(migrations, m)
				}
			}
		}

		if format != "json" && len(migrations) > 0 {
			color := ui.IsColorEnabled(os.Stdout)
			return emit.RenderAll(os.Stdout, migrations, emit.RenderOptions{
				ShowSafety:    true,
				ShowSourcePos: true,
				Color:         color,
			})
		}
		return nil
	}

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Diff desired state vs snapshot and print the SQL migration",
		Long: `Compare .dpg source files against the committed snapshot (or the live
database with --live) and print the minimal SQL required to reach the
desired state. No database connection is required unless --live is set.

Caution and Destructive operations, and instruction-only steps DPG does not
perform itself, are labelled in the output.

Use --format json for machine-readable output suitable for CI or tooling.
Use --watch to re-run automatically whenever source files change.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if watch {
				return runWatch(cmd, func() error { return runOnce(cmd) })
			}
			return runOnce(cmd)
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to plan (required when multiple clusters exist)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to plan (required when multiple databases exist)")
	cmd.Flags().BoolVar(&live, "live", false, "diff against the live database instead of the stored snapshot")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().BoolVar(&watch, "watch", false, "re-run whenever source files change (polls every 500ms)")

	return cmd
}

// introspectSnapshot connects to the live database db within cl, introspects
// its catalog, and returns a Snapshot built from the live state.
func introspectSnapshot(ctx context.Context, cl *project.Cluster, db *project.Database, secretResolver pipeline.SecretResolver, introspector pipeline.Introspector) (*pipeline.Snapshot, error) {
	conn, err := connectDatabase(ctx, cl, db, secretResolver)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)

	liveObjects, err := introspector.Introspect(ctx, conn)
	if err != nil {
		return nil, ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}

	// Exclude cluster-level objects (roles, tablespaces); the database plan
	// compares against database-scoped source only.
	snap := &pipeline.Snapshot{}
	if err := snapshotpkg.Populate(snap, excludeClusterScoped(liveObjects)); err != nil {
		return nil, fmt.Errorf("build live snapshot: %w", err)
	}
	return snap, nil
}

func buildPlan(
	cl *project.Cluster,
	db *project.Database,
	snap *pipeline.Snapshot,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	lintCfg pipeline.LinterConfig,
	format string,
) (pipeline.Migration, error) {
	errColor := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, dbCompileOptions(db))
	if err != nil {
		return pipeline.Migration{}, err
	}

	diags := linter.FilterMergeDiagnostics(mergeDiags, lintCfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, lintErr := l.Lint(desired, lintCfg)
		if lintErr != nil {
			return pipeline.Migration{}, lintErr
		}
		diags = append(diags, lintDiags...)
	}
	if ui.PrintLintDiagnostics(os.Stderr, diags, errColor) {
		return pipeline.Migration{}, ui.ErrSilent
	}

	ops, err := differ.Diff(desired, snap)
	if err != nil {
		return pipeline.Migration{}, err
	}

	rev, _ := gitRevision()
	meta := pipeline.MigrationMeta{
		GeneratedAt:    time.Now().UTC(),
		SourceRevision: rev,
		Cluster:        cl.Name(),
		SourceRoot:     cl.ProjectRoot,
		Database:       db.Name(),
	}

	if format == "json" {
		return pipeline.Migration{}, renderPlanJSON(ops, meta)
	}

	return emitter.Emit(ops, meta)
}

func renderPlanJSON(ops []pipeline.DiffOp, meta pipeline.MigrationMeta) error {
	out := planJSON{
		Cluster:        meta.Cluster,
		Database:       meta.Database,
		GeneratedAt:    meta.GeneratedAt.Format(time.RFC3339),
		SourceRevision: meta.SourceRevision,
		Ops:            make([]opJSON, 0, len(ops)),
		Empty:          len(ops) == 0,
	}
	for _, o := range ops {
		pos := o.Pos()
		oj := opJSON{SQL: o.SQL(), Safety: o.Safety().String(), Mode: o.Mode().String()}
		if pos.File != "" {
			oj.File = emit.RelSourcePath(meta.SourceRoot, pos.File)
			oj.Line = pos.Line
		}
		out.Ops = append(out.Ops, oj)
	}
	return writeJSON(out)
}

// buildClusterPlan plans cluster-level objects (roles, tablespaces, etc.).
func buildClusterPlan(
	cl *project.Cluster,
	snap *pipeline.Snapshot,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	lintCfg pipeline.LinterConfig,
	format string,
) (pipeline.Migration, error) {
	errColor := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.Compile(cl.SourceFiles, cl.ObjectsDir, pipeline.Default)
	if err != nil {
		return pipeline.Migration{}, err
	}

	diags := linter.FilterMergeDiagnostics(mergeDiags, lintCfg)
	if l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter); ok {
		lintDiags, lintErr := l.Lint(desired, lintCfg)
		if lintErr != nil {
			return pipeline.Migration{}, lintErr
		}
		diags = append(diags, lintDiags...)
	}
	if ui.PrintLintDiagnostics(os.Stderr, diags, errColor) {
		return pipeline.Migration{}, ui.ErrSilent
	}

	ops, err := differ.Diff(desired, snap)
	if err != nil {
		return pipeline.Migration{}, err
	}

	rev, _ := gitRevision()
	meta := pipeline.MigrationMeta{
		GeneratedAt:    time.Now().UTC(),
		SourceRevision: rev,
		Cluster:        cl.Name(),
		SourceRoot:     cl.ProjectRoot,
		// Database is intentionally empty for cluster-level plans; the Cluster
		// field already identifies the context and _cluster must not appear in output.
	}

	if format == "json" {
		return pipeline.Migration{}, renderPlanJSON(ops, meta)
	}

	return emitter.Emit(ops, meta)
}

// introspectClusterSnapshot connects to the cluster and returns a snapshot
// containing only cluster-level objects (roles).
func introspectClusterSnapshot(ctx context.Context, cl *project.Cluster, secretResolver pipeline.SecretResolver, introspector pipeline.Introspector) (*pipeline.Snapshot, error) {
	connStr := cl.ConnectionString()
	if connStr == "" {
		return nil, fmt.Errorf("DPG-E003: cluster %q has no connection configured", cl.Name())
	}
	if cl.IsLink() {
		var err error
		connStr, err = secretResolver.Resolve(connStr)
		if err != nil {
			return nil, ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
		}
	}
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		return nil, ui.WrapDB(err)
	}
	defer conn.Close(ctx)

	allObjects, err := introspector.Introspect(ctx, conn)
	if err != nil {
		return nil, ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}

	// Keep only cluster-level objects (roles, tablespaces), matching dump's
	// isClusterScoped routing.
	var clusterObjects []pipeline.IRObject
	for _, obj := range allObjects {
		if isClusterScoped(obj) {
			clusterObjects = append(clusterObjects, obj)
		}
	}

	snap := &pipeline.Snapshot{}
	if err := snapshotpkg.Populate(snap, clusterObjects); err != nil {
		return nil, fmt.Errorf("build cluster snapshot: %w", err)
	}
	return snap, nil
}

// gitRevision returns the current HEAD short hash, or "" if git is unavailable.
func gitRevision() (string, error) {
	data, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return "", nil
	}
	head := strings.TrimSpace(string(data))
	// .git/HEAD contains either a ref pointer ("ref: refs/heads/main") or a bare
	// commit hash when in detached HEAD state.
	if strings.HasPrefix(head, "ref: ") {
		refPath := filepath.Join(".git", filepath.FromSlash(strings.TrimPrefix(head, "ref: ")))
		data, err = os.ReadFile(refPath)
		if err != nil {
			return "", nil
		}
		head = strings.TrimSpace(string(data))
	}
	if len(head) >= 7 {
		return head[:7], nil
	}
	return "", nil
}
