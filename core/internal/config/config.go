package config

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// NameMapsConfig holds the parsed [namemaps] configuration at any config level.
// Global maps tool name to rule for all object types (from direct key-value
// pairs in [namemaps]). ByType maps object-type name to (tool → rule), from
// [namemaps.<type>] subsections (e.g. [namemaps.column]).
// Only rule keywords are permitted at the config level; literal names may only
// be specified in block-level NAME MAP directives.
type NameMapsConfig struct {
	Global map[string]string
	ByType map[string]map[string]string
}

// UnmarshalTOML implements toml.Unmarshaler so that a mixed [namemaps] table
// (string values for global rules + subtables for per-type rules) decodes
// into the structured NameMapsConfig.
func (n *NameMapsConfig) UnmarshalTOML(data interface{}) error {
	m, ok := data.(map[string]interface{})
	if !ok {
		return nil
	}
	for k, v := range m {
		switch val := v.(type) {
		case string:
			rule := strings.ToUpper(val)
			if !pipeline.ValidNameMapRules[rule] {
				return fmt.Errorf("DPG-E030: [namemaps]: unknown rule %q for tool %q", val, k)
			}
			if n.Global == nil {
				n.Global = make(map[string]string)
			}
			n.Global[k] = rule
		case map[string]interface{}:
			typeMap := make(map[string]string)
			for tool, ruleVal := range val {
				r, ok := ruleVal.(string)
				if !ok {
					return fmt.Errorf("[namemaps.%s]: expected string rule for tool %q", k, tool)
				}
				rule := strings.ToUpper(r)
				if !pipeline.ValidNameMapRules[rule] {
					return fmt.Errorf("DPG-E030: [namemaps.%s]: unknown rule %q for tool %q", k, r, tool)
				}
				typeMap[tool] = rule
			}
			if n.ByType == nil {
				n.ByType = make(map[string]map[string]string)
			}
			n.ByType[k] = typeMap
		}
	}
	return nil
}

// Config kinds (RFC Section 3.1). Every dpg.toml declares exactly one.
const (
	KindRoot     = "root"
	KindCluster  = "cluster"
	KindDatabase = "database"
)

// RootConfig represents the contents of dpg.toml at the project root.
type RootConfig struct {
	// Kind must be "root".
	Kind       string           `toml:"kind"`
	Compiler   CompilerConfig   `toml:"compiler"`
	Linter     LinterConfig     `toml:"linter"`
	Fmt        FmtConfig        `toml:"fmt"`
	Snapshots  SnapshotsConfig  `toml:"snapshots"`
	Migrations MigrationsConfig `toml:"migrations"`
	Ledger     LedgerConfig     `toml:"ledger"`
	NameMaps   NameMapsConfig   `toml:"namemaps"`
	Ignore     IgnoreConfig     `toml:"ignore"`
}

// IgnoreConfig is the [ignore] section (RFC Section 3.9): name patterns of
// objects that exist in a database but are not managed by DPG. Allowed at
// every config level; the lists of all levels apply together.
type IgnoreConfig struct {
	Roles                    []string `toml:"roles"`
	Tablespaces              []string `toml:"tablespaces"`
	Schemas                  []string `toml:"schemas"`
	Extensions               []string `toml:"extensions"`
	Tables                   []string `toml:"tables"`
	Views                    []string `toml:"views"`
	Functions                []string `toml:"functions"`
	Procedures               []string `toml:"procedures"`
	Aggregates               []string `toml:"aggregates"`
	Types                    []string `toml:"types"`
	Sequences                []string `toml:"sequences"`
	Collations               []string `toml:"collations"`
	Operators                []string `toml:"operators"`
	OperatorClasses          []string `toml:"operator_classes"`
	OperatorFamilies         []string `toml:"operator_families"`
	Casts                    []string `toml:"casts"`
	Statistics               []string `toml:"statistics"`
	ForeignDataWrappers      []string `toml:"foreign_data_wrappers"`
	Servers                  []string `toml:"servers"`
	UserMappings             []string `toml:"user_mappings"`
	Publications             []string `toml:"publications"`
	Subscriptions            []string `toml:"subscriptions"`
	EventTriggers            []string `toml:"event_triggers"`
	TextSearchConfigurations []string `toml:"text_search_configurations"`
	TextSearchDictionaries   []string `toml:"text_search_dictionaries"`
	TextSearchParsers        []string `toml:"text_search_parsers"`
	TextSearchTemplates      []string `toml:"text_search_templates"`
}

// ByKind returns the patterns keyed by snapshot object kind ("role",
// "table", ...). Kinds with no patterns are omitted.
func (c IgnoreConfig) ByKind() map[string][]string {
	all := map[string][]string{
		"role":            c.Roles,
		"tablespace":      c.Tablespaces,
		"schema":          c.Schemas,
		"extension":       c.Extensions,
		"table":           c.Tables,
		"view":            c.Views,
		"function":        c.Functions,
		"procedure":       c.Procedures,
		"aggregate":       c.Aggregates,
		"type":            c.Types,
		"sequence":        c.Sequences,
		"collation":       c.Collations,
		"operator":        c.Operators,
		"operator_class":  c.OperatorClasses,
		"operator_family": c.OperatorFamilies,
		"cast":            c.Casts,
		"statistics":      c.Statistics,
		"fdw":             c.ForeignDataWrappers,
		"server":          c.Servers,
		"user_mapping":    c.UserMappings,
		"publication":     c.Publications,
		"subscription":    c.Subscriptions,
		"event_trigger":   c.EventTriggers,
		"ts_config":       c.TextSearchConfigurations,
		"ts_dict":         c.TextSearchDictionaries,
		"ts_parser":       c.TextSearchParsers,
		"ts_template":     c.TextSearchTemplates,
	}
	for k, v := range all {
		if len(v) == 0 {
			delete(all, k)
		}
	}
	return all
}

// validate rejects a malformed pattern.
func (c IgnoreConfig) validate() error {
	for _, patterns := range c.ByKind() {
		for _, p := range patterns {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("invalid pattern %q", p)
			}
		}
	}
	return nil
}

// FmtConfig holds formatter settings (dpg fmt).
type FmtConfig struct {
	// IndentSize is the number of spaces per indent level. Default: 4.
	IndentSize int `toml:"indent"`
	// KeywordCase controls keyword casing: "upper" (default) or "lower".
	KeywordCase string `toml:"keyword_case"`
}

// CompilerConfig holds compiler-wide defaults. At the cluster/database level
// (ClusterConfig.Compiler/DatabaseConfig.Compiler) only MinPGVersion is
// recognized — DefaultDropBehavior stays root-only (RFC's original "global
// compiler ... behaviour" framing; no override mechanism was requested for
// it, unlike MinPGVersion).
type CompilerConfig struct {
	// DefaultDropBehavior controls whether DROPs cascade or restrict.
	// Valid values: "restrict" (default), "cascade". Root-level only.
	DefaultDropBehavior string `toml:"default_drop_behavior"`
	// MinPGVersion is the minimum PostgreSQL major version this project (or
	// this cluster/database, when set at that level) targets. DPG always
	// parses against its newest supported grammar — PostgreSQL's own SQL
	// grammar is overwhelmingly additive across versions — so this gates
	// *semantically* instead: a declared construct that requires a newer PG
	// version than the effective floor is flagged by the linter's
	// min-pg-version rule (warning by default, error under --strict). nil =
	// unset at this level (falls through to the next level up; unset
	// everywhere = no gating at all). Overridable at the cluster and
	// database level, most specific wins: database > cluster > root — see
	// project.Cluster.EffectiveMinPGVersion/project.Database.
	// EffectiveMinPGVersion.
	MinPGVersion *int `toml:"min_pg_version"`
}

// LinterConfig holds the linter rule settings.
type LinterConfig struct {
	WarnOnDeprecated         bool `toml:"warn_on_deprecated"`
	RequireColumnComments    bool `toml:"require_column_comments"`
	ForbidHardcodedPasswords bool `toml:"forbid_hardcoded_passwords"`
	MaxColumnsPerTable       int  `toml:"max_columns_per_table"`
	// Rules holds per-rule-ID severity overrides from a [linter.rules]
	// subtable (RFC Section 19.2), e.g. `security-definer-search-path = "error"`.
	// Values are "error", "warning", or "off"; validated in cmd/dpg at the
	// point a LinterConfig is turned into a pipeline.LinterConfig, not here
	// (this package has no dependency on the actual set of rule IDs).
	Rules map[string]string `toml:"rules"`
}

// SnapshotsConfig controls snapshot file locations.
type SnapshotsConfig struct {
	// Directory is the path (relative to the project root) where snapshot
	// JSON files are stored. Default: ".dpg/snapshots".
	Directory string `toml:"directory"`
}

// MigrationsConfig controls where the migration log is kept.
type MigrationsConfig struct {
	// Directory is the path (relative to the project root) of the migration
	// log. Default: ".dpg/migrations". It can't be empty: the log can't be
	// turned off.
	Directory string `toml:"directory"`
}

// LedgerConfig controls the ledger table each database records the
// migrations it has run in (RFC Section 16.9).
type LedgerConfig struct {
	// Enabled turns the ledger on (the default).
	Enabled *bool `toml:"enabled"`
	// Schema is the schema of the ledger table. Default: "dpg".
	Schema string `toml:"schema"`
}

// On reports whether the ledger is used.
func (l LedgerConfig) On() bool { return l.Enabled == nil || *l.Enabled }

// DefaultRootConfig returns a RootConfig populated with the RFC defaults.
func DefaultRootConfig() RootConfig {
	return RootConfig{
		Compiler: CompilerConfig{
			DefaultDropBehavior: "restrict",
		},
		Linter: LinterConfig{
			WarnOnDeprecated:         true,
			RequireColumnComments:    false,
			ForbidHardcodedPasswords: true,
			MaxColumnsPerTable:       50,
		},
		Fmt: FmtConfig{
			IndentSize:  4,
			KeywordCase: "upper",
		},
		Snapshots: SnapshotsConfig{
			Directory: ".dpg/snapshots",
		},
		Migrations: MigrationsConfig{
			Directory: ".dpg/migrations",
		},
		Ledger: LedgerConfig{
			Schema: "dpg",
		},
	}
}

// checkUnknownKeys implements DPG-E001 (RFC Appendix C): meta.Undecoded()
// lists every TOML key/table path present in the file that wasn't mapped to
// any destination struct field — real BurntSushi/toml behavior, confirmed
// via a direct decode probe, including both a bogus table itself
// ("bogus_section") and its own keys ("bogus_section.foo") as separate
// entries. A key inside [namemaps] is never reported here even if it looks
// wrong: NameMapsConfig implements toml.Unmarshaler, so BurntSushi/toml
// treats that whole subtree as consumed once the custom unmarshaler runs
// (confirmed via the same probe) — NameMapsConfig's own UnmarshalTOML
// already validates its own keys/values directly (DPG-E030), a different,
// already-covered code path.
//
// Previously this codebase had NO unknown-key enforcement anywhere — a
// typo'd or stray key in dpg.toml was silently ignored instead of erroring,
// contrary to what Appendix C documents. This function is called right
// after every toml.DecodeFile in this file.
func checkUnknownKeys(meta toml.MetaData, path string) error {
	undecoded := meta.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}
	keys := make([]string, len(undecoded))
	for i, k := range undecoded {
		keys[i] = k.String()
	}
	return fmt.Errorf("DPG-E001: %s: unknown key(s): %s", path, strings.Join(keys, ", "))
}

// LoadRoot loads and parses dpg.toml from dir.
// Missing optional fields default to DefaultRootConfig values.
func LoadRoot(dir string) (RootConfig, error) {
	cfg := DefaultRootConfig()
	path := filepath.Join(dir, "dpg.toml")
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return RootConfig{}, fmt.Errorf("loading %s: %w", path, err)
	}
	if err := checkUnknownKeys(meta, path); err != nil {
		return RootConfig{}, err
	}
	if err := checkKind(path, cfg.Kind, KindRoot); err != nil {
		return RootConfig{}, err
	}
	if err := cfg.Compiler.validate(); err != nil {
		return RootConfig{}, fmt.Errorf("%s: [compiler]: %w", path, err)
	}
	if err := cfg.Ignore.validate(); err != nil {
		return RootConfig{}, fmt.Errorf("%s: [ignore]: %w", path, err)
	}
	if strings.TrimSpace(cfg.Migrations.Directory) == "" {
		return RootConfig{}, fmt.Errorf("%s: [migrations]: directory can't be empty: the migration log can't be turned off", path)
	}
	if strings.TrimSpace(cfg.Ledger.Schema) == "" {
		return RootConfig{}, fmt.Errorf("%s: [ledger]: schema can't be empty", path)
	}
	return cfg, nil
}

func (c CompilerConfig) validate() error {
	switch c.DefaultDropBehavior {
	case "restrict", "cascade":
	default:
		return fmt.Errorf("default_drop_behavior must be \"restrict\" or \"cascade\", got %q", c.DefaultDropBehavior)
	}
	return validateMinPGVersion(c.MinPGVersion)
}

// validateMinPGVersion rejects a min_pg_version below the RFC's own
// supported floor (Tenet 1.4: "floor 14, no ceiling") — used at every level
// (root/cluster/database) a [compiler] section can appear, unlike
// DefaultDropBehavior's validation above which is root-only.
func validateMinPGVersion(v *int) error {
	if v == nil {
		return nil
	}
	if *v < 14 {
		return fmt.Errorf("min_pg_version must be >= 14 (this project's own supported floor), got %d", *v)
	}
	return nil
}

// ClusterConfig represents the dpg.toml file inside a cluster directory.
type ClusterConfig struct {
	// Kind must be "cluster".
	Kind     string         `toml:"kind"`
	Cluster  ClusterDef     `toml:"cluster"`
	NameMaps NameMapsConfig `toml:"namemaps"`
	// Compiler holds this cluster's compiler overrides — only MinPGVersion
	// is meaningful here (see CompilerConfig's doc comment). A stray
	// default_drop_behavior declared at this level is a genuine field on
	// CompilerConfig (shared with root/database), so checkUnknownKeys
	// (DPG-E001) does not flag it even though it has no effect at this
	// level — narrower than unknown-key detection, out of scope here.
	Compiler CompilerConfig `toml:"compiler"`
	Ignore   IgnoreConfig   `toml:"ignore"`
}

// ClusterDef holds the cluster connection and options.
type ClusterDef struct {
	Name string `toml:"name"`
	// ClusterObjectsDir is the subdirectory within the cluster directory
	// that holds cluster-level objects (roles, tablespaces, FDWs).
	// Default: "cluster". This name is reserved — no database may share it.
	ClusterObjectsDir string `toml:"cluster_objects_dir"`
	// URL is an inline PostgreSQL connection string for the primary node.
	// Mutually exclusive with Link. May be omitted for offline-only usage.
	URL string `toml:"url"`
	// Link is a secrets-provider URI (e.g. "env:PRIMARY_DB_URL") resolved at
	// connection time. Mutually exclusive with URL.
	Link string `toml:"link"`
}

// ConnectionURL returns the effective connection string, preferring URL over Link.
// Callers that need secret resolution should check Link first.
func (c ClusterDef) ConnectionURL() string {
	if c.URL != "" {
		return c.URL
	}
	return c.Link
}

// DefaultClusterConfig returns a ClusterConfig with sensible defaults.
func DefaultClusterConfig() ClusterConfig {
	return ClusterConfig{
		Cluster: ClusterDef{
			ClusterObjectsDir: "cluster",
		},
	}
}

// LoadCluster loads and parses the dpg.toml inside a cluster directory.
func LoadCluster(path string) (ClusterConfig, error) {
	cfg := DefaultClusterConfig()
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return ClusterConfig{}, fmt.Errorf("loading %s: %w", path, err)
	}
	if err := checkUnknownKeys(meta, path); err != nil {
		return ClusterConfig{}, err
	}
	if err := checkKind(path, cfg.Kind, KindCluster); err != nil {
		return ClusterConfig{}, err
	}
	if err := cfg.Cluster.validate(path); err != nil {
		return ClusterConfig{}, err
	}
	if err := validateMinPGVersion(cfg.Compiler.MinPGVersion); err != nil {
		return ClusterConfig{}, fmt.Errorf("%s: [compiler]: %w", path, err)
	}
	if err := cfg.Ignore.validate(); err != nil {
		return ClusterConfig{}, fmt.Errorf("%s: [ignore]: %w", path, err)
	}
	return cfg, nil
}

func (c ClusterDef) validate(path string) error {
	if c.Name == "" {
		return fmt.Errorf("DPG-E032: %s: cluster name is required (set name in [cluster])", path)
	}
	if c.URL != "" && c.Link != "" {
		return fmt.Errorf("DPG-E002: %s: url and link are mutually exclusive", path)
	}
	return nil
}

// DatabaseConfig represents the dpg.toml file inside a database directory.
type DatabaseConfig struct {
	// Kind must be "database".
	Kind string `toml:"kind"`
	// Parent is the path, relative to this file's directory, of the cluster
	// directory this database belongs to. Empty means the nearest ancestor
	// directory whose dpg.toml has kind "cluster" (RFC Section 3.6).
	Parent   string         `toml:"parent"`
	Database DatabaseDef    `toml:"database"`
	NameMaps NameMapsConfig `toml:"namemaps"`
	// Compiler holds this database's compiler overrides — see
	// ClusterConfig.Compiler's identical doc comment.
	Compiler CompilerConfig `toml:"compiler"`
	Ignore   IgnoreConfig   `toml:"ignore"`
}

// DatabaseDef holds per-database settings.
type DatabaseDef struct {
	Name          string `toml:"name"`
	DefaultSchema string `toml:"default_schema"`
	// URL and Link optionally replace the cluster's connection for this
	// database only (RFC Section 3.4). Mutually exclusive. The value is
	// used verbatim; the database name in it is not rewritten.
	URL  string `toml:"url"`
	Link string `toml:"link"`
}

// ConnectionURL returns this database's own connection override, preferring
// URL over Link, or "" when none is set.
func (d DatabaseDef) ConnectionURL() string {
	if d.URL != "" {
		return d.URL
	}
	return d.Link
}

// DefaultDatabaseConfig returns a DatabaseConfig with sensible defaults.
func DefaultDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		Database: DatabaseDef{
			DefaultSchema: "public",
		},
	}
}

// LoadDatabase loads and parses the dpg.toml inside a database directory.
func LoadDatabase(path string) (DatabaseConfig, error) {
	cfg := DefaultDatabaseConfig()
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return DatabaseConfig{}, fmt.Errorf("loading %s: %w", path, err)
	}
	if err := checkUnknownKeys(meta, path); err != nil {
		return DatabaseConfig{}, err
	}
	if err := checkKind(path, cfg.Kind, KindDatabase); err != nil {
		return DatabaseConfig{}, err
	}
	if err := cfg.Database.validate(path); err != nil {
		return DatabaseConfig{}, err
	}
	if err := validateMinPGVersion(cfg.Compiler.MinPGVersion); err != nil {
		return DatabaseConfig{}, fmt.Errorf("%s: [compiler]: %w", path, err)
	}
	if err := cfg.Ignore.validate(); err != nil {
		return DatabaseConfig{}, fmt.Errorf("%s: [ignore]: %w", path, err)
	}
	return cfg, nil
}

func (d DatabaseDef) validate(path string) error {
	if d.Name == "" {
		return fmt.Errorf("DPG-E033: %s: database name is required (set name in [database])", path)
	}
	if d.URL != "" && d.Link != "" {
		return fmt.Errorf("DPG-E002: %s: url and link are mutually exclusive", path)
	}
	return nil
}

// checkKind implements DPG-E041: a dpg.toml must declare the kind its
// location requires.
func checkKind(path, got, want string) error {
	if got == want {
		return nil
	}
	if got == "" {
		return fmt.Errorf("DPG-E041: %s: missing kind; add kind = %q as the first line", path, want)
	}
	return fmt.Errorf("DPG-E041: %s: kind is %q, expected %q", path, got, want)
}

// PeekKind returns the kind a dpg.toml declares without validating the rest
// of the file. A file with no kind returns "".
func PeekKind(path string) (string, error) {
	var probe struct {
		Kind string `toml:"kind"`
	}
	if _, err := toml.DecodeFile(path, &probe); err != nil {
		return "", fmt.Errorf("loading %s: %w", path, err)
	}
	switch probe.Kind {
	case "", KindRoot, KindCluster, KindDatabase:
		return probe.Kind, nil
	default:
		return "", fmt.Errorf("DPG-E041: %s: kind must be %q, %q, or %q, got %q", path, KindRoot, KindCluster, KindDatabase, probe.Kind)
	}
}
