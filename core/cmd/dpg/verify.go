package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

func newVerifyCmd() *cobra.Command {
	var (
		clusterName  string
		databaseName string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check the live database for drift against the snapshot",
		Long: `Introspects the live database catalog and compares it against the committed
snapshot. Reports drift, including DPG-managed grants missing from the live
catalog, and exits 1 when drift is found.

Extra grants present in the live catalog but absent from DPG source are
not reported (additive grant model). A role that exists in the cluster but
is not in the snapshot is reported as a warning, not drift. Objects matching
an [ignore] pattern in dpg.toml are not compared.`,
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
			introspector, err := pipeline.MustResolve[pipeline.Introspector](pipeline.Default, pipeline.KeyIntrospector)
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
			secretResolver, err := pipeline.MustResolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
			if err != nil {
				return err
			}

			driftFound := false
			for _, cl := range clusters {
				if len(cl.SourceFiles) > 0 {
					hasDrift, err := runVerifyCluster(cl, store, introspector, differ, emitter, secretResolver,
						ignore.New(cl.EffectiveIgnore(proj.RootConfig)))
					if err != nil {
						return fmt.Errorf("%s (cluster): %w", cl.Name(), err)
					}
					if hasDrift {
						driftFound = true
					}
				}

				databases, err := resolveDatabases(cl, databaseName)
				if err != nil {
					return err
				}
				for _, db := range databases {
					hasDrift, err := runVerify(cl, db, store, introspector, differ, emitter, secretResolver,
						ignore.New(db.EffectiveIgnore(cl, proj.RootConfig)))
					if err != nil {
						return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
					}
					if hasDrift {
						driftFound = true
					}
				}
			}
			if driftFound {
				return fmt.Errorf("drift detected")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to verify (required when multiple clusters exist)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to verify (required when multiple databases exist)")

	return cmd
}

func runVerify(
	cl *project.Cluster,
	db *project.Database,
	store pipeline.SnapshotStore,
	introspector pipeline.Introspector,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	secretResolver pipeline.SecretResolver,
	rules *ignore.Rules,
) (bool, error) {
	ctx := context.Background()
	errColor := ui.IsColorEnabled(os.Stderr)

	snap, err := store.Load(cl.Name(), db.Name())
	if err != nil {
		return false, fmt.Errorf("load snapshot: %w", err)
	}
	if err := rules.FilterSnapshot(snap); err != nil {
		return false, err
	}

	conn, err := connectDatabase(ctx, cl, db, secretResolver)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)

	liveObjects, err := introspector.Introspect(ctx, conn)
	if err != nil {
		return false, ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}

	// Exclude cluster-level objects (roles, tablespaces); they are verified
	// against the cluster snapshot, not the database snapshot.
	databaseObjects := rules.FilterObjects(excludeClusterScoped(liveObjects))

	liveSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(liveSnap, databaseObjects); err != nil {
		return false, fmt.Errorf("build live snapshot: %w", err)
	}

	ops, err := differ.Diff(databaseObjects, snap)
	if err != nil {
		return false, fmt.Errorf("diff: %w", err)
	}

	if len(ops) == 0 {
		ui.PrintInfo(os.Stdout, cl.Name()+"/"+db.Name(), "no drift detected", ui.IsColorEnabled(os.Stdout))
		return false, nil
	}

	fmt.Fprintf(os.Stderr, "%s  %s\n\n",
		ui.Red("drift detected", errColor),
		ui.Cyan(cl.Name()+"/"+db.Name(), errColor),
	)
	migration, _ := emitter.Emit(ops, pipeline.MigrationMeta{
		Cluster:    cl.Name(),
		SourceRoot: cl.ProjectRoot,
		Database:   db.Name(),
	})
	_ = emit.Render(os.Stderr, migration, emit.RenderOptions{
		ShowSafety:    true,
		ShowSourcePos: true,
		Color:         errColor,
	})
	return true, nil
}

// runVerifyCluster checks cluster-level objects (roles, tablespaces) for drift.
func runVerifyCluster(
	cl *project.Cluster,
	store pipeline.SnapshotStore,
	introspector pipeline.Introspector,
	differ pipeline.Differ,
	emitter pipeline.Emitter,
	secretResolver pipeline.SecretResolver,
	rules *ignore.Rules,
) (bool, error) {
	ctx := context.Background()
	errColor := ui.IsColorEnabled(os.Stderr)

	snap, err := store.Load(cl.Name(), cl.ClusterSnapshotKey())
	if err != nil {
		return false, fmt.Errorf("load snapshot: %w", err)
	}
	if err := rules.FilterSnapshot(snap); err != nil {
		return false, err
	}

	connStr := cl.ConnectionString()
	if connStr == "" {
		return false, fmt.Errorf("DPG-E003: cluster %q has no connection configured (set url or link in cluster dpg.toml)", cl.Name())
	}
	if cl.IsLink() {
		connStr, err = secretResolver.Resolve(connStr)
		if err != nil {
			return false, ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
		}
	}

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		return false, ui.WrapDB(err)
	}
	defer conn.Close(ctx)

	allObjects, err := introspector.Introspect(ctx, conn)
	if err != nil {
		return false, ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}

	// Retain only cluster-level objects (roles, tablespaces). This must match
	// dump's routing (isClusterScoped): database-scoped schemaless objects like
	// FDWs and publications belong to the database, not the cluster.
	// A live role missing from the snapshot was created outside DPG; roles
	// are shared by everything on the cluster, so it is a warning, not drift.
	var clusterObjects []pipeline.IRObject
	var undeclared []string
	for _, obj := range rules.FilterObjects(allObjects) {
		if !isClusterScoped(obj) {
			continue
		}
		if r, ok := obj.(*ir.Role); ok {
			if _, inSnap := snap.Objects[r.QualifiedName()]; !inSnap {
				undeclared = append(undeclared, r.Name)
				continue
			}
		}
		clusterObjects = append(clusterObjects, obj)
	}
	printUndeclaredRoles(os.Stderr, cl.Name()+" (cluster)", undeclared, errColor)

	ops, err := differ.Diff(clusterObjects, snap)
	if err != nil {
		return false, fmt.Errorf("diff: %w", err)
	}

	label := cl.Name() + " (cluster)"
	if len(ops) == 0 {
		ui.PrintInfo(os.Stdout, label, "no drift detected", ui.IsColorEnabled(os.Stdout))
		return false, nil
	}

	fmt.Fprintf(os.Stderr, "%s  %s\n\n",
		ui.Red("drift detected", errColor),
		ui.Cyan(label, errColor),
	)
	migration, _ := emitter.Emit(ops, pipeline.MigrationMeta{
		Cluster:    cl.Name(),
		SourceRoot: cl.ProjectRoot,
		Database:   cl.ClusterSnapshotKey(),
	})
	_ = emit.Render(os.Stderr, migration, emit.RenderOptions{
		ShowSafety:    true,
		ShowSourcePos: true,
		Color:         errColor,
	})
	return true, nil
}
