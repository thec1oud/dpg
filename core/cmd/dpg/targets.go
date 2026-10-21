package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/ui"
)

// resolveClusters returns which clusters to operate on.
//   - If clusterFlag is set it must match exactly one cluster.
//   - If there is exactly one cluster it is selected automatically.
//   - If there are multiple clusters and no flag, an error is returned with the
//     list of available names so the user knows what to pass.
func resolveClusters(proj *project.Project, clusterFlag string) ([]*project.Cluster, error) {
	if len(proj.Clusters) == 0 {
		return nil, fmt.Errorf("no clusters found under %s\n  (each cluster must be a subdirectory containing dpg.toml)", proj.RootDir)
	}
	if clusterFlag != "" {
		for _, cl := range proj.Clusters {
			if cl.Name() == clusterFlag {
				return []*project.Cluster{cl}, nil
			}
		}
		return nil, fmt.Errorf("DPG-E027: cluster %q not found; available: %s",
			clusterFlag, strings.Join(clusterNames(proj.Clusters), ", "))
	}
	if len(proj.Clusters) == 1 {
		return proj.Clusters, nil
	}
	return nil, fmt.Errorf("DPG-E026: multiple clusters found (%s); use --cluster to select one",
		strings.Join(clusterNames(proj.Clusters), ", "))
}

// resolveDatabases returns which databases to operate on within cl.
//   - If dbFlag is set it must match exactly one database.
//   - If there is exactly one database it is selected automatically.
//   - If there are multiple databases and no flag, an error is returned.
func resolveDatabases(cl *project.Cluster, dbFlag string) ([]*project.Database, error) {
	if len(cl.Databases) == 0 {
		return nil, fmt.Errorf("cluster %q has no databases configured", cl.Name())
	}
	if dbFlag != "" {
		for _, db := range cl.Databases {
			if db.Name() == dbFlag {
				return []*project.Database{db}, nil
			}
		}
		return nil, fmt.Errorf("DPG-E029: database %q not found in cluster %q; available: %s",
			dbFlag, cl.Name(), strings.Join(dbNames(cl.Databases), ", "))
	}
	if len(cl.Databases) == 1 {
		return cl.Databases, nil
	}
	return nil, fmt.Errorf("DPG-E028: cluster %q has multiple databases (%s); use --database to select one",
		cl.Name(), strings.Join(dbNames(cl.Databases), ", "))
}

func clusterNames(cls []*project.Cluster) []string {
	names := make([]string, len(cls))
	for i, cl := range cls {
		names[i] = cl.Name()
	}
	return names
}

func dbNames(dbs []*project.Database) []string {
	names := make([]string, len(dbs))
	for i, db := range dbs {
		names[i] = db.Name()
	}
	return names
}

// connectDatabase connects to db (RFC Section 3.4): through the database's own
// url/link when it sets one, used verbatim, otherwise through the cluster's
// url/link with the database name replaced by db's name. A link is resolved
// through secretResolver. Returned errors are ready to print.
func connectDatabase(ctx context.Context, cl *project.Cluster, db *project.Database, secretResolver pipeline.SecretResolver) (*executor.PgxConn, error) {
	def := db.Config.Database
	if own := def.ConnectionURL(); own != "" {
		connStr := own
		if def.URL == "" {
			resolved, err := secretResolver.Resolve(own)
			if err != nil {
				return nil, ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
			}
			connStr = resolved
		}
		conn, err := executor.Connect(ctx, connStr)
		if err != nil {
			return nil, ui.WrapDB(withMissingDatabaseHint(err))
		}
		return conn, nil
	}

	connStr := cl.ConnectionString()
	if connStr == "" {
		return nil, fmt.Errorf("DPG-E003: database %q has no connection configured (set url or link in its cluster's dpg.toml, or in its own)", db.Name())
	}
	if cl.IsLink() {
		resolved, err := secretResolver.Resolve(connStr)
		if err != nil {
			return nil, ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
		}
		connStr = resolved
	}
	conn, err := executor.ConnectToDatabase(ctx, connStr, db.Name())
	if err != nil {
		return nil, ui.WrapDB(withMissingDatabaseHint(err))
	}
	return conn, nil
}

// withMissingDatabaseHint adds a hint to a connection error caused by the
// target database not existing (SQLSTATE 3D000): DPG never creates
// databases (RFC Section 23), so the database must be created first.
func withMissingDatabaseHint(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "3D000" {
		return fmt.Errorf("%w\nhint: DPG does not create databases (RFC Section 23); create the database before running dpg against it", err)
	}
	return err
}
