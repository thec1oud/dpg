package project_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/project"
)

// buildTree creates a minimal project layout under a temp dir.
//
//	root/
//	  dpg.toml                   (root config)
//	  mycluster/
//	    dpg.toml                 (cluster config)
//	    mydb/
//	      dpg.toml               (database config)
//	      schemas/public/
//	        tables.dpg
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	writeF := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeF("dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"
`)
	writeF("mycluster/dpg.toml", `
kind = "cluster"
[cluster]
name = "mycluster"
url  = "postgres://localhost/test"
`)
	writeF("mycluster/mydb/dpg.toml", `
kind = "database"
[database]
name = "mydb"
`)
	writeF("mycluster/mydb/schemas/public/tables.dpg", `TABLE users (id BIGINT);`)

	return root
}

// ── Discover ──────────────────────────────────────────────────────────────────

func TestDiscover_FromRoot(t *testing.T) {
	root := buildTree(t)
	proj, err := project.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if proj.RootDir != root {
		t.Errorf("RootDir: got %q", proj.RootDir)
	}
	if len(proj.Clusters) != 1 {
		t.Fatalf("Clusters: expected 1, got %d", len(proj.Clusters))
	}
	cl := proj.Clusters[0]
	if cl.Name() != "mycluster" {
		t.Errorf("Cluster.Name: got %q", cl.Name())
	}
	if len(cl.Databases) != 1 {
		t.Fatalf("Databases: expected 1, got %d", len(cl.Databases))
	}
	db := cl.Databases[0]
	if db.Name() != "mydb" {
		t.Errorf("Database.Name: got %q", db.Name())
	}
	if len(db.SourceFiles) != 1 {
		t.Errorf("SourceFiles: expected 1, got %d", len(db.SourceFiles))
	}
}

func TestDiscover_FromSubdir(t *testing.T) {
	root := buildTree(t)
	// Discover should walk up from a subdirectory.
	sub := filepath.Join(root, "mycluster", "mydb")
	proj, err := project.Discover(sub)
	if err != nil {
		t.Fatalf("Discover from subdir: %v", err)
	}
	if proj.RootDir != root {
		t.Errorf("RootDir: expected %q, got %q", root, proj.RootDir)
	}
}

func TestDiscover_NoDPGToml(t *testing.T) {
	_, err := project.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error when no dpg.toml found")
	}
}

// ── Project helper methods ─────────────────────────────────────────────────────

func TestProject_DPGDir(t *testing.T) {
	root := buildTree(t)
	proj, _ := project.Discover(root)
	want := filepath.Join(root, ".dpg")
	if proj.DPGDir() != want {
		t.Errorf("DPGDir: got %q, want %q", proj.DPGDir(), want)
	}
}

func TestProject_SnapshotDir(t *testing.T) {
	root := buildTree(t)
	proj, _ := project.Discover(root)
	want := filepath.Join(root, ".dpg/snapshots")
	if proj.SnapshotDir() != want {
		t.Errorf("SnapshotDir: got %q, want %q", proj.SnapshotDir(), want)
	}
}

// ── Multi-cluster / multi-database ────────────────────────────────────────────

func TestDiscover_MultipleClustersDatabases(t *testing.T) {
	root := t.TempDir()

	writeF := func(rel, content string) {
		path := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(content), 0o644)
	}

	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("cluster-a/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"a\"\nurl = \"postgres://a\"\n")
	writeF("cluster-a/db1/dpg.toml", "kind = \"database\"\n[database]\nname = \"db1\"\n")
	writeF("cluster-a/db2/dpg.toml", "kind = \"database\"\n[database]\nname = \"db2\"\n")
	writeF("cluster-b/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"b\"\nurl = \"postgres://b\"\n")
	writeF("cluster-b/db3/dpg.toml", "kind = \"database\"\n[database]\nname = \"db3\"\n")

	proj, err := project.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(proj.Clusters) != 2 {
		t.Errorf("Clusters: expected 2, got %d", len(proj.Clusters))
	}
	totalDBs := 0
	for _, cl := range proj.Clusters {
		totalDBs += len(cl.Databases)
	}
	if totalDBs != 3 {
		t.Errorf("total databases: expected 3, got %d", totalDBs)
	}
}

func TestDiscover_DuplicateClusterNameErrors(t *testing.T) {
	root := t.TempDir()

	writeF := func(rel, content string) {
		path := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(content), 0o644)
	}

	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("cluster-a/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"shared\"\nurl = \"postgres://a\"\n")
	writeF("cluster-b/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"shared\"\nurl = \"postgres://b\"\n")

	_, err := project.Discover(root)
	if err == nil {
		t.Fatal("expected an error for two cluster directories declaring the same name")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("expected error to mention the shared name %q, got: %v", "shared", err)
	}
	if !strings.Contains(err.Error(), "cluster-a") || !strings.Contains(err.Error(), "cluster-b") {
		t.Errorf("expected error to name both directories, got: %v", err)
	}
}

func TestDiscover_DuplicateDatabaseNameErrors(t *testing.T) {
	root := t.TempDir()

	writeF := func(rel, content string) {
		path := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(content), 0o644)
	}

	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("cluster-a/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"a\"\nurl = \"postgres://a\"\n")
	writeF("cluster-a/db-x/dpg.toml", "kind = \"database\"\n[database]\nname = \"shared\"\n")
	writeF("cluster-a/db-y/dpg.toml", "kind = \"database\"\n[database]\nname = \"shared\"\n")

	_, err := project.Discover(root)
	if err == nil {
		t.Fatal("expected an error for two database directories in one cluster declaring the same name")
	}
	if !strings.Contains(err.Error(), "shared") {
		t.Errorf("expected error to mention the shared name %q, got: %v", "shared", err)
	}
	if !strings.Contains(err.Error(), "db-x") || !strings.Contains(err.Error(), "db-y") {
		t.Errorf("expected error to name both directories, got: %v", err)
	}
}

// TestDiscover_SameDatabaseNameAcrossDifferentClustersIsAllowed proves the
// duplicate-database-name check is scoped to within a single cluster only —
// the same database name recurring under a different cluster (e.g. a
// "staging" database that exists in both a local and a remote cluster) is
// normal and must not be flagged.
func TestDiscover_SameDatabaseNameAcrossDifferentClustersIsAllowed(t *testing.T) {
	root := t.TempDir()

	writeF := func(rel, content string) {
		path := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(content), 0o644)
	}

	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("cluster-a/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"a\"\nurl = \"postgres://a\"\n")
	writeF("cluster-a/staging/dpg.toml", "kind = \"database\"\n[database]\nname = \"staging\"\n")
	writeF("cluster-b/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"b\"\nurl = \"postgres://b\"\n")
	writeF("cluster-b/staging/dpg.toml", "kind = \"database\"\n[database]\nname = \"staging\"\n")

	proj, err := project.Discover(root)
	if err != nil {
		t.Fatalf("Discover: expected no error, got: %v", err)
	}
	if len(proj.Clusters) != 2 {
		t.Fatalf("Clusters: expected 2, got %d", len(proj.Clusters))
	}
}

// ── Cluster helpers ────────────────────────────────────────────────────────────

func TestCluster_IsLink(t *testing.T) {
	root := t.TempDir()
	writeF := func(rel, content string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("c/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"c\"\nlink = \"env:DB_URL\"\n")
	writeF("c/db/dpg.toml", "kind = \"database\"\n[database]\nname = \"db\"\n")

	proj, _ := project.Discover(root)
	if len(proj.Clusters) == 0 {
		t.Fatal("no clusters found")
	}
	cl := proj.Clusters[0]
	if !cl.IsLink() {
		t.Error("IsLink: expected true")
	}
	if cl.ConnectionString() != "env:DB_URL" {
		t.Errorf("ConnectionString: got %q", cl.ConnectionString())
	}
}

func TestCluster_ClusterObjectsDirExcludedFromDatabases(t *testing.T) {
	root := t.TempDir()
	writeF := func(rel, content string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("c/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"c\"\nurl = \"postgres://x\"\n")
	// "cluster" is the default ClusterObjectsDir — it must not appear as a database.
	writeF("c/cluster/roles.dpg", "ROLE app;")
	writeF("c/db/dpg.toml", "kind = \"database\"\n[database]\nname = \"db\"\n")

	proj, err := project.Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(proj.Clusters) != 1 {
		t.Fatalf("Clusters: expected 1, got %d", len(proj.Clusters))
	}
	cl := proj.Clusters[0]
	// Only "db" should be a database; "cluster" directory is the objects dir.
	if len(cl.Databases) != 1 {
		t.Errorf("Databases: expected 1, got %d", len(cl.Databases))
	}
	if cl.Databases[0].Name() != "db" {
		t.Errorf("Database name: got %q", cl.Databases[0].Name())
	}
}

// TestDiscover_DatabaseNamedLikeClusterObjectsDirErrors proves DPG-E004
// (reserved_name_conflict, documented in the RFC Section 3.5 but never enforced by
// the reference implementation): a directory that shares the cluster's
// reserved objects-dir name but actually declares a [database] section must
// be rejected with a clear error, not silently discarded as if it were the
// (empty/nonexistent) objects directory — the same "invisible ghost
// directory" failure mode as a duplicate database name, just triggered by a
// name collision with a reserved name instead of another database.
func TestDiscover_DatabaseNamedLikeClusterObjectsDirErrors(t *testing.T) {
	root := t.TempDir()
	writeF := func(rel, content string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
	writeF("dpg.toml", "kind = \"root\"\n[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	writeF("c/dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"c\"\nurl = \"postgres://x\"\n")
	// "cluster" is the default ClusterObjectsDir, but this one has a real
	// [database] dpg.toml inside it — a genuine naming conflict.
	writeF("c/cluster/dpg.toml", "kind = \"database\"\n[database]\nname = \"oops\"\n")

	_, err := project.Discover(root)
	if err == nil {
		t.Fatal("expected an error for a database directory sharing the cluster objects directory's name")
	}
	if !strings.Contains(err.Error(), "cluster") {
		t.Errorf("expected error to mention the conflicting directory name, got: %v", err)
	}
}

// ── EffectiveMinPGVersion ─────────────────────────────────────────────────────

func intPtr(n int) *int { return &n }

func TestCluster_EffectiveMinPGVersion_Unset(t *testing.T) {
	cl := &project.Cluster{}
	if got := cl.EffectiveMinPGVersion(config.RootConfig{}); got != 0 {
		t.Errorf("expected 0 (no gating) when unset everywhere, got %d", got)
	}
}

func TestCluster_EffectiveMinPGVersion_RootOnly(t *testing.T) {
	cl := &project.Cluster{}
	root := config.RootConfig{Compiler: config.CompilerConfig{MinPGVersion: intPtr(15)}}
	if got := cl.EffectiveMinPGVersion(root); got != 15 {
		t.Errorf("expected root's 15, got %d", got)
	}
}

// TestCluster_EffectiveMinPGVersion_ClusterOverridesRoot guards the
// database > cluster > root precedence's cluster half: a cluster-level
// override must win over the root's own value, not merely supplement it.
func TestCluster_EffectiveMinPGVersion_ClusterOverridesRoot(t *testing.T) {
	cl := &project.Cluster{Config: config.ClusterConfig{
		Compiler: config.CompilerConfig{MinPGVersion: intPtr(17)},
	}}
	root := config.RootConfig{Compiler: config.CompilerConfig{MinPGVersion: intPtr(15)}}
	if got := cl.EffectiveMinPGVersion(root); got != 17 {
		t.Errorf("expected cluster's 17 to override root's 15, got %d", got)
	}
}

func TestDatabase_EffectiveMinPGVersion_Unset(t *testing.T) {
	cl := &project.Cluster{}
	db := &project.Database{}
	if got := db.EffectiveMinPGVersion(cl, config.RootConfig{}); got != 0 {
		t.Errorf("expected 0 (no gating) when unset everywhere, got %d", got)
	}
}

// TestDatabase_EffectiveMinPGVersion_DatabaseOverridesClusterAndRoot guards
// the full 3-level precedence: the most specific level set wins.
func TestDatabase_EffectiveMinPGVersion_DatabaseOverridesClusterAndRoot(t *testing.T) {
	cl := &project.Cluster{Config: config.ClusterConfig{
		Compiler: config.CompilerConfig{MinPGVersion: intPtr(16)},
	}}
	db := &project.Database{Config: config.DatabaseConfig{
		Compiler: config.CompilerConfig{MinPGVersion: intPtr(18)},
	}}
	root := config.RootConfig{Compiler: config.CompilerConfig{MinPGVersion: intPtr(15)}}
	if got := db.EffectiveMinPGVersion(cl, root); got != 18 {
		t.Errorf("expected database's 18 to override cluster's 16 and root's 15, got %d", got)
	}
}

// TestDatabase_EffectiveMinPGVersion_FallsThroughToCluster guards that an
// unset database level correctly falls through to its cluster's own
// resolution (which itself may fall through to root) rather than skipping
// straight to root.
func TestDatabase_EffectiveMinPGVersion_FallsThroughToCluster(t *testing.T) {
	cl := &project.Cluster{Config: config.ClusterConfig{
		Compiler: config.CompilerConfig{MinPGVersion: intPtr(16)},
	}}
	db := &project.Database{} // unset
	root := config.RootConfig{Compiler: config.CompilerConfig{MinPGVersion: intPtr(15)}}
	if got := db.EffectiveMinPGVersion(cl, root); got != 16 {
		t.Errorf("expected fall-through to cluster's 16 (not root's 15), got %d", got)
	}
}

// ── Typed configs and nesting (RFC Sections 3.1, 3.6) ────────────────────────

func newTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const (
	rootTOML = "kind = \"root\"\n"
)

func clusterTOML(name string) string {
	return "kind = \"cluster\"\n[cluster]\nname = \"" + name + "\"\n"
}
func dbTOML(name, parent string) string {
	s := "kind = \"database\"\n"
	if parent != "" {
		s += "parent = \"" + parent + "\"\n"
	}
	return s + "[database]\nname = \"" + name + "\"\n"
}

func TestDiscover_NestedAtAnyDepth(t *testing.T) {
	root := newTree(t, map[string]string{
		"dpg.toml":                                       rootTOML,
		"envs/prod/eu/dpg.toml":                          clusterTOML("prod-eu"),
		"envs/prod/eu/apps/billing/dpg.toml":             dbTOML("billing", ""),
		"envs/prod/eu/apps/billing/schemas/public/t.dpg": "TABLE t (a int);",
		"databases/crm/dpg.toml":                         dbTOML("crm", "../../envs/prod/eu"),
	})
	proj, err := project.Discover(filepath.Join(root, "envs", "prod", "eu", "apps", "billing"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(proj.Clusters) != 1 || proj.Clusters[0].Name() != "prod-eu" {
		t.Fatalf("clusters: %+v", proj.Clusters)
	}
	var names []string
	for _, db := range proj.Clusters[0].Databases {
		names = append(names, db.Name())
	}
	// Databases are listed in directory-path order.
	if strings.Join(names, ",") != "crm,billing" {
		t.Errorf("databases: got %v, want [crm billing]", names)
	}
}

func TestDiscover_TypedConfigErrors(t *testing.T) {
	cases := []struct {
		desc  string
		files map[string]string
		code  string
	}{
		{"missing kind", map[string]string{"dpg.toml": rootTOML, "c/dpg.toml": "[cluster]\nname = \"c\"\n"}, "DPG-E041"},
		{"root below root", map[string]string{"dpg.toml": rootTOML, "c/dpg.toml": rootTOML}, "DPG-E041"},
		{"database outside any cluster", map[string]string{"dpg.toml": rootTOML, "db/dpg.toml": dbTOML("db", "")}, "DPG-E042"},
		{"parent is not a cluster", map[string]string{"dpg.toml": rootTOML, "x/keep": "", "db/dpg.toml": dbTOML("db", "../x")}, "DPG-E042"},
		{"parent outside root", map[string]string{"dpg.toml": rootTOML, "db/dpg.toml": dbTOML("db", "../..")}, "DPG-E042"},
		{"parent contradicts enclosing cluster", map[string]string{
			"dpg.toml": rootTOML, "a/dpg.toml": clusterTOML("a"), "b/dpg.toml": clusterTOML("b"),
			"a/db/dpg.toml": dbTOML("db", "../../b")}, "DPG-E042"},
		{"database in database", map[string]string{
			"dpg.toml": rootTOML, "c/dpg.toml": clusterTOML("c"),
			"c/db/dpg.toml": dbTOML("db", ""), "c/db/inner/dpg.toml": dbTOML("inner", "")}, "DPG-E043"},
		{"cluster in cluster", map[string]string{
			"dpg.toml": rootTOML, "c/dpg.toml": clusterTOML("c"), "c/d/dpg.toml": clusterTOML("d")}, "DPG-E043"},
	}
	for _, c := range cases {
		root := newTree(t, c.files)
		_, err := project.Discover(root)
		if err == nil || !strings.Contains(err.Error(), c.code) {
			t.Errorf("%s: expected %s, got %v", c.desc, c.code, err)
		}
	}
}
