package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var (
		clusterName string
		dbName      string
		schemaName  string
		connURL     string
	)

	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Scaffold a new DPG project",
		Long: `Create the directory structure and configuration files for a new DPG project.

  dpg init                          # scaffold in the current directory
  dpg init myproject                # scaffold in ./myproject/

Layout created:

  dpg.toml                          root config
  <cluster>/
    dpg.toml                        cluster config (connection URL)
    cluster/                        cluster-level objects (roles, tablespaces)
    <database>/
      dpg.toml                      database config
      schemas/<schema>/             source directory for .dpg files
  .dpg/
    snapshots/                      snapshot storage (commit this directory)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			return runInit(abs, clusterName, dbName, schemaName, connURL)
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "production", "cluster directory name")
	cmd.Flags().StringVar(&dbName, "database", "myapp", "database directory name")
	cmd.Flags().StringVar(&schemaName, "schema", "public", "default schema name")
	cmd.Flags().StringVar(&connURL, "url", "", "PostgreSQL connection URL (can be set later)")
	return cmd
}

func runInit(root, cluster, database, schema, connURL string) error {
	dirs := []string{
		filepath.Join(root, cluster, "cluster"),
		filepath.Join(root, cluster, database, "schemas", schema),
		filepath.Join(root, ".dpg", "snapshots"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("init: create %s: %w", d, err)
		}
	}

	files := []struct {
		path    string
		content string
	}{
		{
			filepath.Join(root, "dpg.toml"),
			rootTOML(),
		},
		{
			filepath.Join(root, cluster, "dpg.toml"),
			clusterTOML(cluster, connURL),
		},
		{
			filepath.Join(root, cluster, database, "dpg.toml"),
			databaseTOML(database, schema),
		},
	}

	created := 0
	for _, f := range files {
		if _, err := os.Stat(f.path); err == nil {
			fmt.Fprintf(os.Stderr, "  skip  %s (already exists)\n", relOrAbs(root, f.path))
			continue
		}
		if err := os.WriteFile(f.path, []byte(f.content), 0644); err != nil {
			return fmt.Errorf("init: write %s: %w", f.path, err)
		}
		fmt.Fprintf(os.Stdout, "  create %s\n", relOrAbs(root, f.path))
		created++
	}
	for _, d := range dirs {
		fmt.Fprintf(os.Stdout, "  create %s/\n", relOrAbs(root, d))
	}

	fmt.Fprintf(os.Stdout, "\nProject initialised in %s\n", root)
	fmt.Fprintf(os.Stdout, "Next steps:\n")
	fmt.Fprintf(os.Stdout, "  1. Add your schema to %s\n",
		filepath.Join(cluster, database, "schemas", schema)+"/")
	fmt.Fprintf(os.Stdout, "  2. Run: dpg plan\n")
	_ = created
	return nil
}

func rootTOML() string {
	return `# DPG project root configuration.
# See: https://github.com/thec1oud/dpg

[compiler]
default_drop_behavior = "restrict"
concurrent_indexes    = true

[linter]
warn_on_deprecated          = true
forbid_hardcoded_passwords  = true

[fmt]
indent       = 4
keyword_case = "upper"

[snapshots]
directory = ".dpg/snapshots"
`
}

func clusterTOML(name, url string) string {
	urlLine := ""
	if url != "" {
		urlLine = fmt.Sprintf(`url = %q`, url) + "\n"
	} else {
		urlLine = `# url  = "postgres://user:pass@host:5432/dbname"` + "\n" +
			`# link = "env:DATABASE_URL"   # resolve from environment at connect time` + "\n"
	}
	return fmt.Sprintf(`[cluster]
name = %q
%s`, name, urlLine)
}

func databaseTOML(name, schema string) string {
	return fmt.Sprintf(`[database]
name           = %q
default_schema = %q
`, name, schema)
}

func relOrAbs(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
