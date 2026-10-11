package main

import (
	"fmt"
	"strings"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/project"
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

// dbCompileOptions returns the compile options a database's dpg.toml implies:
// its default_schema (RFC Section 3.4).
func dbCompileOptions(db *project.Database) compiler.Options {
	return compiler.Options{DefaultSchema: db.Config.Database.DefaultSchema}
}
