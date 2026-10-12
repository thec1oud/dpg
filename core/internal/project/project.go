package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thec1oud/dpg/internal/config"
)

// Project is the fully-resolved DPG project, rooted at a directory containing dpg.toml.
type Project struct {
	RootDir    string
	RootConfig config.RootConfig
	Clusters   []*Cluster
}

// DPGDir returns the absolute path to the project's .dpg working directory.
func (p *Project) DPGDir() string {
	return filepath.Join(p.RootDir, ".dpg")
}

// SnapshotDir returns the absolute path to the snapshot directory.
func (p *Project) SnapshotDir() string {
	return filepath.Join(p.RootDir, p.RootConfig.Snapshots.Directory)
}

// MigrationsDir returns the absolute path to the migration log.
func (p *Project) MigrationsDir() string {
	return filepath.Join(p.RootDir, p.RootConfig.Migrations.Directory)
}

// Cluster represents a single PostgreSQL cluster within the project.
type Cluster struct {
	// Dir is the absolute path to the cluster directory.
	Dir string
	// ProjectRoot is the absolute path to the project root.
	ProjectRoot string
	Config      config.ClusterConfig
	// ObjectsDir is the absolute path to the cluster-level objects directory
	// (roles, tablespaces, FDWs). May not exist yet.
	ObjectsDir string
	// SourceFiles is the ordered list of .dpg files found in ObjectsDir.
	// Empty when ObjectsDir does not exist yet.
	SourceFiles []string
	Databases   []*Database
}

// ClusterSnapshotKey returns the snapshot store key for cluster-level objects.
// The leading underscore ensures it never collides with a real database name.
func (c *Cluster) ClusterSnapshotKey() string { return "_cluster" }

// Name returns the cluster name from config.
func (c *Cluster) Name() string { return c.Config.Cluster.Name }

// ConnectionString returns the raw URL or Link value from cluster config.
// Callers that support secrets (apply, verify, dump) must check whether the
// value is a link URI and resolve it via the SecretResolver before connecting.
func (c *Cluster) ConnectionString() string {
	return c.Config.Cluster.ConnectionURL()
}

// IsLink reports whether the connection string is a secrets-provider URI
// (i.e. the Link field is set rather than URL).
func (c *Cluster) IsLink() bool {
	return c.Config.Cluster.Link != "" && c.Config.Cluster.URL == ""
}

// EffectiveMinPGVersion resolves the min_pg_version floor for this cluster's
// own linting (cluster-level objects: roles, tablespaces, PARAMETER
// PRIVILEGES): this cluster's own override, else the project root's, else 0
// (no floor configured anywhere — the min-pg-version lint rule is a no-op).
func (c *Cluster) EffectiveMinPGVersion(root config.RootConfig) int {
	if c.Config.Compiler.MinPGVersion != nil {
		return *c.Config.Compiler.MinPGVersion
	}
	if root.Compiler.MinPGVersion != nil {
		return *root.Compiler.MinPGVersion
	}
	return 0
}

// EffectiveIgnore returns the [ignore] patterns for this cluster's own
// objects: the project root's and this cluster's, together.
func (c *Cluster) EffectiveIgnore(root config.RootConfig) map[string][]string {
	return mergeIgnore(root.Ignore, c.Config.Ignore)
}

// Database represents a single PostgreSQL database within a cluster.
type Database struct {
	// Dir is the absolute path to the database source directory.
	Dir    string
	Config config.DatabaseConfig
	// SourceFiles is the ordered list of absolute paths to all .dpg files
	// found recursively within Dir.
	SourceFiles []string
}

// Name returns the database name from config.
func (d *Database) Name() string { return d.Config.Database.Name }

// EffectiveMinPGVersion resolves the min_pg_version floor for this
// database's own linting: this database's own override, else its cluster's
// (via Cluster.EffectiveMinPGVersion), else the project root's, else 0 (no
// gating). cl must be this database's own owning cluster.
func (d *Database) EffectiveMinPGVersion(cl *Cluster, root config.RootConfig) int {
	if d.Config.Compiler.MinPGVersion != nil {
		return *d.Config.Compiler.MinPGVersion
	}
	return cl.EffectiveMinPGVersion(root)
}

// EffectiveIgnore returns the [ignore] patterns for this database: the
// project root's, its cluster's and its own, together. cl must be this
// database's own owning cluster.
func (d *Database) EffectiveIgnore(cl *Cluster, root config.RootConfig) map[string][]string {
	return mergeIgnore(root.Ignore, cl.Config.Ignore, d.Config.Ignore)
}

func mergeIgnore(levels ...config.IgnoreConfig) map[string][]string {
	out := map[string][]string{}
	for _, l := range levels {
		for kind, patterns := range l.ByKind() {
			out[kind] = append(out[kind], patterns...)
		}
	}
	return out
}

// Discover walks up from startDir to the project root (the nearest dpg.toml
// with kind = "root"), then discovers every cluster and database below it
// (RFC Section 3.6).
func Discover(startDir string) (*Project, error) {
	rootDir, err := findRoot(startDir)
	if err != nil {
		return nil, err
	}

	rootCfg, err := config.LoadRoot(rootDir)
	if err != nil {
		return nil, err
	}

	clusters, err := discoverTree(rootDir)
	if err != nil {
		return nil, err
	}

	return &Project{
		RootDir:    rootDir,
		RootConfig: rootCfg,
		Clusters:   clusters,
	}, nil
}

// findRoot walks up from dir looking for a dpg.toml whose kind is "root".
func findRoot(dir string) (string, error) {
	current := filepath.Clean(dir)
	for {
		candidate := filepath.Join(current, "dpg.toml")
		if _, err := os.Stat(candidate); err == nil {
			kind, err := config.PeekKind(candidate)
			if err != nil {
				return "", err
			}
			if kind == "" {
				return "", fmt.Errorf("DPG-E041: %s: missing kind; every dpg.toml must declare kind = \"root\", \"cluster\", or \"database\"", candidate)
			}
			if kind == config.KindRoot {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no project root dpg.toml (kind = \"root\") found in %s or any parent directory", dir)
		}
		current = parent
	}
}

// discoverTree finds every cluster and database dpg.toml under rootDir, at
// any depth, and assembles them into clusters (RFC Section 3.6). A database
// belongs to the cluster its parent key names, else to its nearest ancestor
// cluster directory.
func discoverTree(rootDir string) ([]*Cluster, error) {
	kinds := map[string]string{} // dir -> kind
	var dirs []string
	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != rootDir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		cfgPath := filepath.Join(path, "dpg.toml")
		if _, statErr := os.Stat(cfgPath); statErr != nil {
			return nil
		}
		kind, kerr := config.PeekKind(cfgPath)
		if kerr != nil {
			return kerr
		}
		switch {
		case path == rootDir:
			return nil
		case kind == "":
			return fmt.Errorf("DPG-E041: %s: missing kind; add kind = \"cluster\" or kind = \"database\" as the first line", cfgPath)
		case kind == config.KindRoot:
			return fmt.Errorf("DPG-E041: %s: kind \"root\" may only appear at the project root %s", cfgPath, rootDir)
		}
		kinds[path] = kind
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// nearestAncestor returns the closest strict ancestor of dir (inside the
	// project) whose kind is in want, or "".
	nearestAncestor := func(dir string, want ...string) string {
		for p := filepath.Dir(dir); p != rootDir && strings.HasPrefix(p, rootDir); p = filepath.Dir(p) {
			for _, w := range want {
				if kinds[p] == w {
					return p
				}
			}
		}
		return ""
	}

	byDir := map[string]*Cluster{}
	var clusters []*Cluster
	for _, dir := range dirs {
		if kinds[dir] != config.KindCluster {
			continue
		}
		if outer := nearestAncestor(dir, config.KindCluster, config.KindDatabase); outer != "" {
			return nil, fmt.Errorf("DPG-E043: %s: a cluster directory cannot be nested inside the %s directory %s", dir, kinds[outer], outer)
		}
		cl, err := loadCluster(dir, filepath.Join(dir, "dpg.toml"))
		if err != nil {
			return nil, err
		}
		cl.ProjectRoot = rootDir
		byDir[dir] = cl
		clusters = append(clusters, cl)
	}

	for _, dir := range dirs {
		if kinds[dir] != config.KindDatabase {
			continue
		}
		if outer := nearestAncestor(dir, config.KindDatabase); outer != "" {
			return nil, fmt.Errorf("DPG-E043: %s: a database directory cannot be nested inside the database directory %s", dir, outer)
		}
		db, err := loadDatabase(dir, filepath.Join(dir, "dpg.toml"))
		if err != nil {
			return nil, err
		}
		clusterDir, err := resolveDatabaseCluster(rootDir, dir, db.Config.Parent, kinds, nearestAncestor)
		if err != nil {
			return nil, err
		}
		cl := byDir[clusterDir]
		if dir == cl.ObjectsDir || strings.HasPrefix(dir, cl.ObjectsDir+string(filepath.Separator)) {
			return nil, fmt.Errorf("DPG-E004: %s: a database directory cannot be inside cluster %q's cluster objects directory (cluster_objects_dir) %s",
				dir, cl.Name(), cl.ObjectsDir)
		}
		cl.Databases = append(cl.Databases, db)
	}

	for _, cl := range clusters {
		if err := checkDuplicateDatabaseNames(cl.Databases); err != nil {
			return nil, fmt.Errorf("cluster %q: %w", cl.Name(), err)
		}
	}
	if err := checkDuplicateClusterNames(clusters); err != nil {
		return nil, err
	}
	return clusters, nil
}

// resolveDatabaseCluster returns the cluster directory a database belongs
// to: the directory its parent key names (relative to the database
// directory, and inside the project), else its nearest ancestor cluster
// directory. Errors are DPG-E042.
func resolveDatabaseCluster(rootDir, dbDir, parent string, kinds map[string]string, nearestAncestor func(string, ...string) string) (string, error) {
	cfgPath := filepath.Join(dbDir, "dpg.toml")
	if parent == "" {
		if cl := nearestAncestor(dbDir, config.KindCluster); cl != "" {
			return cl, nil
		}
		return "", fmt.Errorf("DPG-E042: %s: database is not inside a cluster directory and sets no parent", cfgPath)
	}
	target := filepath.Clean(filepath.Join(dbDir, parent))
	if target != rootDir && !strings.HasPrefix(target, rootDir+string(filepath.Separator)) {
		return "", fmt.Errorf("DPG-E042: %s: parent %q resolves to %s, outside the project root %s", cfgPath, parent, target, rootDir)
	}
	if kinds[target] != config.KindCluster {
		return "", fmt.Errorf("DPG-E042: %s: parent %q resolves to %s, which has no dpg.toml with kind = \"cluster\"", cfgPath, parent, target)
	}
	if cl := nearestAncestor(dbDir, config.KindCluster); cl != "" && cl != target {
		return "", fmt.Errorf("DPG-E042: %s: parent %q names cluster directory %s, but the database is inside cluster directory %s", cfgPath, parent, target, cl)
	}
	return target, nil
}

// checkDuplicateClusterNames returns an error naming both directories when
// two or more discovered clusters declare the same [cluster] name. Cluster
// selection (resolveClusters, cmd/dpg/targets.go) matches purely by this
// declared name, not by directory — an undetected duplicate silently makes
// the second cluster permanently unreachable via --cluster, and since
// snapshot storage, the migrations archive, and dpg dump's default output
// path are all keyed by this same name, it also makes two unrelated
// (possibly differently-hosted) clusters silently share those files on disk.
func checkDuplicateClusterNames(clusters []*Cluster) error {
	seen := make(map[string]*Cluster, len(clusters))
	for _, cl := range clusters {
		name := cl.Name()
		if existing, ok := seen[name]; ok {
			return fmt.Errorf("DPG-E034: duplicate cluster name %q declared in both %s and %s", name, existing.Dir, cl.Dir)
		}
		seen[name] = cl
	}
	return nil
}

// loadCluster loads a single cluster from its dpg.toml inside clusterDir.
func loadCluster(clusterDir, cfgPath string) (*Cluster, error) {
	cfg, err := config.LoadCluster(cfgPath)
	if err != nil {
		return nil, err
	}

	objectsDir := filepath.Join(clusterDir, cfg.Cluster.ClusterObjectsDir)

	// Collect cluster-level .dpg source files; ignore error when ObjectsDir
	// does not exist yet (first run before any dump or manual creation).
	clusterFiles, _ := collectSourceFiles(objectsDir)

	return &Cluster{
		Dir:         clusterDir,
		Config:      cfg,
		ObjectsDir:  objectsDir,
		SourceFiles: clusterFiles,
	}, nil
}

// checkDuplicateDatabaseNames returns an error naming both directories when
// two or more database directories within the same cluster declare the same
// [database] name — the same failure mode as checkDuplicateClusterNames, one
// level down. Database name uniqueness is scoped to a single cluster only
// (resolveDatabases, cmd/dpg/targets.go, only ever compares within one
// cluster's own Databases; snapshot/migration paths nest cluster-then-
// database), so the same database name recurring under a *different*
// cluster is legitimate and must not be flagged here. No cluster-identifying
// prefix here: discoverDatabases's only caller, loadCluster, already wraps
// every error it returns with "cluster %q: %w", so adding one here would
// just double it up.
func checkDuplicateDatabaseNames(databases []*Database) error {
	seen := make(map[string]*Database, len(databases))
	for _, db := range databases {
		name := db.Name()
		if existing, ok := seen[name]; ok {
			return fmt.Errorf("DPG-E035: duplicate database name %q declared in both %s and %s", name, existing.Dir, db.Dir)
		}
		seen[name] = db
	}
	return nil
}

// loadDatabase loads a single database from its dpg.toml inside dbDir.
func loadDatabase(dbDir, cfgPath string) (*Database, error) {
	cfg, err := config.LoadDatabase(cfgPath)
	if err != nil {
		return nil, err
	}

	sourceFiles, err := collectSourceFiles(dbDir)
	if err != nil {
		return nil, fmt.Errorf("database %q: collecting source files: %w", cfg.Database.Name, err)
	}

	return &Database{
		Dir:         dbDir,
		Config:      cfg,
		SourceFiles: sourceFiles,
	}, nil
}

// collectSourceFiles recursively finds all .dpg files under dir, sorted by path.
func collectSourceFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".dpg") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}
