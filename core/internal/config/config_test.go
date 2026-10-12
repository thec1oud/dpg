package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// ── RootConfig defaults ───────────────────────────────────────────────────────

func TestDefaultRootConfig(t *testing.T) {
	cfg := config.DefaultRootConfig()
	if cfg.Compiler.DefaultDropBehavior != "restrict" {
		t.Errorf("DefaultDropBehavior: got %q", cfg.Compiler.DefaultDropBehavior)
	}
	if !cfg.Linter.WarnOnDeprecated {
		t.Error("WarnOnDeprecated: expected true")
	}
	if cfg.Linter.MaxColumnsPerTable != 50 {
		t.Errorf("MaxColumnsPerTable: got %d", cfg.Linter.MaxColumnsPerTable)
	}
	if cfg.Fmt.IndentSize != 4 {
		t.Errorf("Fmt.IndentSize: got %d", cfg.Fmt.IndentSize)
	}
	if cfg.Fmt.KeywordCase != "upper" {
		t.Errorf("Fmt.KeywordCase: got %q", cfg.Fmt.KeywordCase)
	}
	if cfg.Snapshots.Directory != ".dpg/snapshots" {
		t.Errorf("Snapshots.Directory: got %q", cfg.Snapshots.Directory)
	}
	if cfg.Migrations.Directory != ".dpg/migrations" {
		t.Errorf("Migrations.Directory: got %q", cfg.Migrations.Directory)
	}
}

// ── LoadRoot ──────────────────────────────────────────────────────────────────

func TestLoadRoot_MinimalFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "cascade"
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if cfg.Compiler.DefaultDropBehavior != "cascade" {
		t.Errorf("DefaultDropBehavior: got %q", cfg.Compiler.DefaultDropBehavior)
	}
	// Unset fields should retain defaults.
	if cfg.Fmt.IndentSize != 4 {
		t.Errorf("Fmt.IndentSize should default to 4, got %d", cfg.Fmt.IndentSize)
	}
}

func TestLoadRoot_FmtSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"

[fmt]
indent = 2
keyword_case = "lower"
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if cfg.Fmt.IndentSize != 2 {
		t.Errorf("Fmt.IndentSize: got %d", cfg.Fmt.IndentSize)
	}
	if cfg.Fmt.KeywordCase != "lower" {
		t.Errorf("Fmt.KeywordCase: got %q", cfg.Fmt.KeywordCase)
	}
}

func TestLoadRoot_InvalidDropBehavior(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "explode"
`)
	_, err := config.LoadRoot(dir)
	if err == nil {
		t.Fatal("expected error for invalid default_drop_behavior")
	}
}

func TestLoadRoot_MissingFile(t *testing.T) {
	_, err := config.LoadRoot(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing dpg.toml")
	}
}

// TestLoadRoot_UnknownTopLevelKey guards DPG-E001 (unknown_config_key, RFC
// Appendix C) — previously had NO enforcement anywhere in this codebase (a
// typo'd or stray key was silently ignored), unlike every other documented
// DPG-E0xx code, which was merely unlabeled rather than unimplemented.
func TestLoadRoot_UnknownTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"

[bogus_section]
foo = "bar"
`)
	_, err := config.LoadRoot(dir)
	if err == nil {
		t.Fatal("expected error for unknown top-level section")
	}
	if !strings.Contains(err.Error(), "DPG-E001") {
		t.Errorf("expected DPG-E001 in error, got: %s", err)
	}
	if !strings.Contains(err.Error(), "bogus_section") {
		t.Errorf("expected the unknown key name in error, got: %s", err)
	}
}

// TestLoadRoot_UnknownNestedKey guards a typo'd key inside an otherwise
// valid, known section (not just a wholly unknown top-level section).
func TestLoadRoot_UnknownNestedKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"
typo_key = "oops"
`)
	_, err := config.LoadRoot(dir)
	if err == nil {
		t.Fatal("expected error for unknown key inside [compiler]")
	}
	if !strings.Contains(err.Error(), "DPG-E001") || !strings.Contains(err.Error(), "typo_key") {
		t.Errorf("expected DPG-E001 mentioning typo_key, got: %s", err)
	}
}

// TestLoadRoot_NameMapsKeysNeverFlagged guards against a false positive:
// [namemaps]'s custom UnmarshalTOML must not make its own (validly-shaped)
// keys look "unknown" to checkUnknownKeys.
func TestLoadRoot_NameMapsKeysNeverFlagged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"

[namemaps]
default = "LOWER_SNAKE_CASE"
prisma = "LOWER_CAMEL_CASE"

[namemaps.column]
prisma = "UPPER_CAMEL_CASE"
`)
	_, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: unexpected error for valid [namemaps]: %v", err)
	}
}

// ── LoadCluster ───────────────────────────────────────────────────────────────

func TestLoadCluster_Basic(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"
url = "postgres://localhost/prod"
`)
	cfg, err := config.LoadCluster(path)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	if cfg.Cluster.Name != "prod" {
		t.Errorf("Name: got %q", cfg.Cluster.Name)
	}
	if cfg.Cluster.URL != "postgres://localhost/prod" {
		t.Errorf("URL: got %q", cfg.Cluster.URL)
	}
	// ClusterObjectsDir should default to "cluster".
	if cfg.Cluster.ClusterObjectsDir != "cluster" {
		t.Errorf("ClusterObjectsDir: got %q", cfg.Cluster.ClusterObjectsDir)
	}
}

func TestLoadCluster_MutuallyExclusiveURLAndLink(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"
url  = "postgres://localhost/prod"
link = "env:PROD_URL"
`)
	_, err := config.LoadCluster(path)
	if err == nil {
		t.Fatal("expected error: url and link are mutually exclusive")
	}
}

func TestLoadCluster_EmptyNameErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
url = "postgres://localhost/prod"
`)
	_, err := config.LoadCluster(path)
	if err == nil {
		t.Fatal("expected error: cluster name is required")
	}
}

// TestLoadCluster_UnknownKey is LoadRoot's DPG-E001 guard at the cluster
// config level.
func TestLoadCluster_UnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"
url = "postgres://localhost/prod"
bogus_key = true
`)
	_, err := config.LoadCluster(path)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "DPG-E001") || !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("expected DPG-E001 mentioning bogus_key, got: %s", err)
	}
}

func TestLoadCluster_ConnectionURL_PrefersURL(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "x"
url  = "postgres://localhost/x"
`)
	cfg, _ := config.LoadCluster(path)
	if cfg.Cluster.ConnectionURL() != "postgres://localhost/x" {
		t.Errorf("ConnectionURL(): got %q", cfg.Cluster.ConnectionURL())
	}
}

func TestLoadCluster_ConnectionURL_FallsBackToLink(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "x"
link = "env:X_URL"
`)
	cfg, _ := config.LoadCluster(path)
	if cfg.Cluster.ConnectionURL() != "env:X_URL" {
		t.Errorf("ConnectionURL(): got %q", cfg.Cluster.ConnectionURL())
	}
}

// ── LoadDatabase ──────────────────────────────────────────────────────────────

func TestLoadDatabase_Basic(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "mydb"
default_schema = "app"
`)
	cfg, err := config.LoadDatabase(path)
	if err != nil {
		t.Fatalf("LoadDatabase: %v", err)
	}
	if cfg.Database.Name != "mydb" {
		t.Errorf("Name: got %q", cfg.Database.Name)
	}
	if cfg.Database.DefaultSchema != "app" {
		t.Errorf("DefaultSchema: got %q", cfg.Database.DefaultSchema)
	}
}

func TestLoadDatabase_EmptyNameErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
default_schema = "app"
`)
	_, err := config.LoadDatabase(path)
	if err == nil {
		t.Fatal("expected error: database name is required")
	}
}

// TestLoadDatabase_UnknownKey is LoadRoot's DPG-E001 guard at the database
// config level.
func TestLoadDatabase_UnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "app"
bogus_key = 5
`)
	_, err := config.LoadDatabase(path)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "DPG-E001") || !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("expected DPG-E001 mentioning bogus_key, got: %s", err)
	}
}

// ── NameMapsConfig ────────────────────────────────────────────────────────────

func TestNameMaps_GlobalRules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"

[namemaps]
default = "LOWER_SNAKE_CASE"
prisma  = "LOWER_CAMEL_CASE"
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if cfg.NameMaps.Global["default"] != "LOWER_SNAKE_CASE" {
		t.Errorf("Global[default]: got %q", cfg.NameMaps.Global["default"])
	}
	if cfg.NameMaps.Global["prisma"] != "LOWER_CAMEL_CASE" {
		t.Errorf("Global[prisma]: got %q", cfg.NameMaps.Global["prisma"])
	}
}

func TestNameMaps_ByTypeRules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"

[namemaps]
default = "LOWER_SNAKE_CASE"

[namemaps.column]
prisma   = "LOWER_CAMEL_CASE"
drizzle  = "LOWER_CAMEL_CASE"

[namemaps.table]
prisma = "UPPER_CAMEL_CASE"
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if cfg.NameMaps.Global["default"] != "LOWER_SNAKE_CASE" {
		t.Errorf("Global[default]: got %q", cfg.NameMaps.Global["default"])
	}
	colMap := cfg.NameMaps.ByType["column"]
	if colMap["prisma"] != "LOWER_CAMEL_CASE" {
		t.Errorf("ByType[column][prisma]: got %q", colMap["prisma"])
	}
	if colMap["drizzle"] != "LOWER_CAMEL_CASE" {
		t.Errorf("ByType[column][drizzle]: got %q", colMap["drizzle"])
	}
	tblMap := cfg.NameMaps.ByType["table"]
	if tblMap["prisma"] != "UPPER_CAMEL_CASE" {
		t.Errorf("ByType[table][prisma]: got %q", tblMap["prisma"])
	}
}

func TestNameMaps_InvalidRule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[namemaps]
default = "SNAKE_LOWER"
`)
	_, err := config.LoadRoot(dir)
	if err == nil {
		t.Fatal("expected error for unknown rule SNAKE_LOWER, got nil")
	}
	// DPG-E030 (invalid_namemap_rule, RFC Appendix C) — embedded in the
	// error message text (this codebase's precedent for non-CompilerError
	// errors, e.g. DPG-E036/DPG-E012 elsewhere).
	if !strings.Contains(err.Error(), "DPG-E030") {
		t.Errorf("expected DPG-E030 in error, got: %s", err)
	}
}

func TestNameMaps_InvalidRuleInByType(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[namemaps.column]
prisma = "NOT_A_RULE"
`)
	_, err := config.LoadRoot(dir)
	if err == nil {
		t.Fatal("expected error for unknown rule in [namemaps.column], got nil")
	}
	if !strings.Contains(err.Error(), "DPG-E030") {
		t.Errorf("expected DPG-E030 in error, got: %s", err)
	}
}

func TestNameMaps_EmptySection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	// NameMaps should be zero-value when no [namemaps] section is present.
	if len(cfg.NameMaps.Global) != 0 {
		t.Errorf("expected empty Global map, got %v", cfg.NameMaps.Global)
	}
	if len(cfg.NameMaps.ByType) != 0 {
		t.Errorf("expected empty ByType map, got %v", cfg.NameMaps.ByType)
	}
}

func TestNameMaps_ClusterConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"
url  = "postgres://localhost/prod"

[namemaps]
default = "LOWER_SNAKE_CASE"

[namemaps.table]
prisma = "UPPER_CAMEL_CASE"
`)
	cfg, err := config.LoadCluster(path)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	if cfg.NameMaps.Global["default"] != "LOWER_SNAKE_CASE" {
		t.Errorf("Global[default]: got %q", cfg.NameMaps.Global["default"])
	}
	if cfg.NameMaps.ByType["table"]["prisma"] != "UPPER_CAMEL_CASE" {
		t.Errorf("ByType[table][prisma]: got %q", cfg.NameMaps.ByType["table"]["prisma"])
	}
}

func TestNameMaps_DatabaseConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "mydb"

[namemaps.column]
default = "LOWER_CAMEL_CASE"
`)
	cfg, err := config.LoadDatabase(path)
	if err != nil {
		t.Fatalf("LoadDatabase: %v", err)
	}
	if cfg.NameMaps.ByType["column"]["default"] != "LOWER_CAMEL_CASE" {
		t.Errorf("ByType[column][default]: got %q", cfg.NameMaps.ByType["column"]["default"])
	}
}

func TestLoadDatabase_DefaultSchema(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "bare"
`)
	cfg, err := config.LoadDatabase(path)
	if err != nil {
		t.Fatalf("LoadDatabase: %v", err)
	}
	if cfg.Database.DefaultSchema != "public" {
		t.Errorf("DefaultSchema should default to 'public', got %q", cfg.Database.DefaultSchema)
	}
}

// ── min_pg_version ────────────────────────────────────────────────────────────

func TestLoadRoot_MinPGVersionUnset(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
default_drop_behavior = "restrict"
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if cfg.Compiler.MinPGVersion != nil {
		t.Errorf("MinPGVersion: expected nil (unset), got %v", *cfg.Compiler.MinPGVersion)
	}
}

func TestLoadRoot_MinPGVersionSet(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
min_pg_version = 16
`)
	cfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if cfg.Compiler.MinPGVersion == nil || *cfg.Compiler.MinPGVersion != 16 {
		t.Errorf("MinPGVersion: got %v, want 16", cfg.Compiler.MinPGVersion)
	}
}

// TestLoadRoot_MinPGVersionBelowFloorErrors guards the RFC's own supported
// floor (Tenet 1.4: "floor 14, no ceiling") — a project can't target a PG
// version older than DPG itself supports.
func TestLoadRoot_MinPGVersionBelowFloorErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", `
kind = "root"
[compiler]
min_pg_version = 13
`)
	_, err := config.LoadRoot(dir)
	if err == nil {
		t.Fatal("expected error for min_pg_version below the floor of 14")
	}
}

// TestLoadCluster_MinPGVersion guards that a cluster-level dpg.toml can
// declare its own min_pg_version override, independent of DefaultDropBehavior
// (which stays root-only — ClusterConfig.Compiler is not validated for it).
func TestLoadCluster_MinPGVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"

[compiler]
min_pg_version = 17
`)
	cfg, err := config.LoadCluster(path)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	if cfg.Compiler.MinPGVersion == nil || *cfg.Compiler.MinPGVersion != 17 {
		t.Errorf("MinPGVersion: got %v, want 17", cfg.Compiler.MinPGVersion)
	}
}

func TestLoadCluster_MinPGVersionBelowFloorErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"

[compiler]
min_pg_version = 10
`)
	_, err := config.LoadCluster(path)
	if err == nil {
		t.Fatal("expected error for min_pg_version below the floor of 14")
	}
}

// TestLoadDatabase_MinPGVersion guards the database-level override — the
// most specific level in the database > cluster > root resolution order
// (project.Database.EffectiveMinPGVersion).
func TestLoadDatabase_MinPGVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "mydb"

[compiler]
min_pg_version = 15
`)
	cfg, err := config.LoadDatabase(path)
	if err != nil {
		t.Fatalf("LoadDatabase: %v", err)
	}
	if cfg.Compiler.MinPGVersion == nil || *cfg.Compiler.MinPGVersion != 15 {
		t.Errorf("MinPGVersion: got %v, want 15", cfg.Compiler.MinPGVersion)
	}
}

func TestLoadDatabase_MinPGVersionBelowFloorErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "mydb"

[compiler]
min_pg_version = 9
`)
	_, err := config.LoadDatabase(path)
	if err == nil {
		t.Fatal("expected error for min_pg_version below the floor of 14")
	}
}

// ── kind (RFC Section 3.1, DPG-E041) ─────────────────────────────────────────

func TestKindIsRequiredAndMustMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dpg.toml", "[compiler]\ndefault_drop_behavior = \"restrict\"\n")
	if _, err := config.LoadRoot(dir); err == nil || !strings.Contains(err.Error(), "DPG-E041") {
		t.Errorf("missing kind: expected DPG-E041, got %v", err)
	}
	path := writeFile(t, dir, "dpg.toml", "kind = \"database\"\n[cluster]\nname = \"c\"\n")
	if _, err := config.LoadCluster(path); err == nil || !strings.Contains(err.Error(), "DPG-E041") {
		t.Errorf("wrong kind: expected DPG-E041, got %v", err)
	}
	writeFile(t, dir, "dpg.toml", "kind = \"warehouse\"\n")
	if _, err := config.PeekKind(path); err == nil || !strings.Contains(err.Error(), "DPG-E041") {
		t.Errorf("unknown kind: expected DPG-E041, got %v", err)
	}
}

// ── database connection override (RFC Section 3.4) ──────────────────────────

func TestLoadDatabase_ConnectionOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", "kind = \"database\"\n[database]\nname = \"app\"\nlink = \"env:APP_DB_URL\"\n")
	cfg, err := config.LoadDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.ConnectionURL() != "env:APP_DB_URL" {
		t.Errorf("ConnectionURL: got %q", cfg.Database.ConnectionURL())
	}
	writeFile(t, dir, "dpg.toml", "kind = \"database\"\n[database]\nname = \"app\"\nurl = \"postgres://x\"\nlink = \"env:Y\"\n")
	if _, err := config.LoadDatabase(path); err == nil || !strings.Contains(err.Error(), "DPG-E002") {
		t.Errorf("url+link: expected DPG-E002, got %v", err)
	}
}

func TestLoadCluster_OptionsTableRemoved(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", "kind = \"cluster\"\n[cluster]\nname = \"c\"\n[cluster.options]\nsnapshot_on_apply = true\n")
	if _, err := config.LoadCluster(path); err == nil || !strings.Contains(err.Error(), "DPG-E001") {
		t.Errorf("[cluster.options] should be an unknown key (DPG-E001), got %v", err)
	}
}

func TestLoadCluster_IgnoreSection(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"
[ignore]
roles   = ["rdsadmin", "rds_*"]
schemas = ["cron"]
`)
	cfg, err := config.LoadCluster(path)
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	got := cfg.Ignore.ByKind()
	if len(got) != 2 || len(got["role"]) != 2 || got["schema"][0] != "cron" {
		t.Errorf("ByKind: got %v", got)
	}
}

func TestLoadCluster_IgnoreUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "cluster"
[cluster]
name = "prod"
[ignore]
rolez = ["x"]
`)
	_, err := config.LoadCluster(path)
	if err == nil || !strings.Contains(err.Error(), "DPG-E001") || !strings.Contains(err.Error(), "ignore.rolez") {
		t.Errorf("expected DPG-E001 naming ignore.rolez, got %v", err)
	}
}

func TestLoadDatabase_IgnoreInvalidPattern(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dpg.toml", `
kind = "database"
[database]
name = "app"
[ignore]
tables = ["bad["]
`)
	_, err := config.LoadDatabase(path)
	if err == nil || !strings.Contains(err.Error(), "[ignore]") {
		t.Errorf("expected an [ignore] pattern error, got %v", err)
	}
}

func TestLoadRoot_MigrationsDirectoryCantBeEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dpg.toml"), []byte("kind = \"root\"\n\n[migrations]\ndirectory = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadRoot(dir); err == nil || !strings.Contains(err.Error(), "can't be turned off") {
		t.Errorf("an empty migrations directory must be refused: %v", err)
	}
}
