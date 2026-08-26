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

// MigrationsDir returns the absolute path to the migrations archive directory.
// Returns "" when migration archiving is disabled (directory = "").
func (p *Project) MigrationsDir() string {
	if p.RootConfig.Migrations.Directory == "" {
		return ""
	}
	return filepath.Join(p.RootDir, p.RootConfig.Migrations.Directory)
}

// Cluster represents a single PostgreSQL cluster within the project.
type Cluster struct {
	// Dir is the absolute path to the cluster directory.
	Dir    string
	Config config.ClusterConfig
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

// Discover walks up from startDir until it finds a dpg.toml, then builds
// and returns the full Project. Returns an error if no dpg.toml is found or
// if the project structure is invalid.
func Discover(startDir string) (*Project, error) {
	rootDir, err := findRoot(startDir)
	if err != nil {
		return nil, err
	}

	rootCfg, err := config.LoadRoot(rootDir)
	if err != nil {
		return nil, err
	}

	clusters, err := discoverClusters(rootDir)
	if err != nil {
		return nil, err
	}

	return &Project{
		RootDir:    rootDir,
		RootConfig: rootCfg,
		Clusters:   clusters,
	}, nil
}

// findRoot walks up from dir looking for a dpg.toml that is the project root
// config (i.e. contains [compiler], [linter], or [snapshots] — not [cluster]
// or [database], which identify cluster/database-level configs).
func findRoot(dir string) (string, error) {
	current := filepath.Clean(dir)
	for {
		candidate := filepath.Join(current, "dpg.toml")
		if _, err := os.Stat(candidate); err == nil && isRootConfig(candidate) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no project root dpg.toml found in %s or any parent directory", dir)
		}
		current = parent
	}
}

// isRootConfig reports whether the dpg.toml at path is a project root config
// rather than a cluster or database config. Root configs must not contain a
// [cluster] or [database] section.
func isRootConfig(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "[cluster]" || strings.HasPrefix(t, "[cluster.") || t == "[database]" {
			return false
		}
	}
	return true
}

// discoverClusters scans immediate subdirectories of rootDir. Any subdirectory
// containing a dpg.toml is treated as a cluster directory.
func discoverClusters(rootDir string) ([]*Cluster, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, fmt.Errorf("reading project root %s: %w", rootDir, err)
	}

	var clusters []*Cluster
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		clusterDir := filepath.Join(rootDir, entry.Name())
		cfgPath := filepath.Join(clusterDir, "dpg.toml")
		if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
			continue
		}
		cluster, err := loadCluster(clusterDir, cfgPath)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, cluster)
	}
	if err := checkDuplicateClusterNames(clusters); err != nil {
		return nil, err
	}
	return clusters, nil
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

	databases, err := discoverDatabases(clusterDir, cfg.Cluster.ClusterObjectsDir)
	if err != nil {
		return nil, fmt.Errorf("cluster %q: %w", cfg.Cluster.Name, err)
	}

	// Collect cluster-level .dpg source files; ignore error when ObjectsDir
	// does not exist yet (first run before any dump or manual creation).
	clusterFiles, _ := collectSourceFiles(objectsDir)

	return &Cluster{
		Dir:         clusterDir,
		Config:      cfg,
		ObjectsDir:  objectsDir,
		SourceFiles: clusterFiles,
		Databases:   databases,
	}, nil
}

// discoverDatabases scans immediate subdirectories of clusterDir. Any
// subdirectory that contains a dpg.toml and is not the cluster objects
// directory is treated as a database directory.
func discoverDatabases(clusterDir, reservedDir string) ([]*Database, error) {
	entries, err := os.ReadDir(clusterDir)
	if err != nil {
		return nil, fmt.Errorf("reading cluster directory %s: %w", clusterDir, err)
	}

	var databases []*Database
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Name() == reservedDir {
			// Normally this is just the (possibly not-yet-created) cluster
			// objects directory itself — not a database, silently skip. But
			// if it actually holds a [database]-declaring dpg.toml, that's a
			// real naming conflict (RFC Section 3.5, DPG-E004): the user tried to
			// declare a database here, and without this check it would be
			// silently discarded with no error at all, identical in shape to
			// the duplicate-name bugs fixed alongside this.
			cfgPath := filepath.Join(clusterDir, entry.Name(), "dpg.toml")
			if hasDatabaseSection(cfgPath) {
				return nil, fmt.Errorf("DPG-E004: %s: database directory name %q conflicts with the cluster's reserved objects directory name (cluster_objects_dir) — rename one of them",
					filepath.Join(clusterDir, entry.Name()), entry.Name())
			}
			continue
		}
		dbDir := filepath.Join(clusterDir, entry.Name())
		cfgPath := filepath.Join(dbDir, "dpg.toml")
		if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
			continue
		}
		db, err := loadDatabase(dbDir, cfgPath)
		if err != nil {
			return nil, err
		}
		databases = append(databases, db)
	}
	if err := checkDuplicateDatabaseNames(databases); err != nil {
		return nil, err
	}
	return databases, nil
}

// hasDatabaseSection reports whether the dpg.toml at path declares a
// [database] section — mirroring isRootConfig's line-scan technique above.
// Used to distinguish a genuine database directory that happens to share
// the cluster's reserved objects-dir name (a real conflict, DPG-E004) from
// the ordinary case of that name simply not existing yet or holding
// non-database content (no dpg.toml at all, matching how a cluster objects
// directory is actually used per RFC Section 3.5).
func hasDatabaseSection(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) == "[database]" {
			return true
		}
	}
	return false
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
