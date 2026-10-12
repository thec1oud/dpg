// Binary gendocs generates Cobra CLI markdown documentation for the dpg tool.
// It mirrors the command tree from cmd/dpg without importing pipeline stages,
// then calls cobra/doc.GenMarkdownTreeCustom to produce Hugo-compatible output.
//
// Usage:
//
//	go run ./tools/gendocs [--output <dir>]
//
// The output directory defaults to site/content/docs/cli. Each subcommand
// produces one markdown file (e.g. dpg_plan.md). Cross-links between generated
// files use Hugo-compatible URL paths (no .md extension).
package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func main() {
	outDir := filepath.Join("site", "content", "docs", "cli")
	for i, arg := range os.Args[1:] {
		if arg == "--output" && i+1 < len(os.Args[1:]) {
			outDir = os.Args[i+2]
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	root := buildRoot()

	// filePrepender adds Hugo front matter to each generated file.
	filePrepender := func(filename string) string {
		base := filepath.Base(filename)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		title := strings.ReplaceAll(name, "_", " ")
		return "---\ntitle: \"" + title + "\"\ngenerated: true\n---\n\n"
	}

	// linkHandler converts cobra/doc cross-links to Hugo URL paths.
	// e.g. "dpg_plan.md" → "/docs/cli/dpg_plan/"
	linkHandler := func(name string) string {
		base := strings.TrimSuffix(name, filepath.Ext(name))
		return "/docs/cli/" + base + "/"
	}

	if err := doc.GenMarkdownTreeCustom(root, outDir, filePrepender, linkHandler); err != nil {
		log.Fatalf("generate docs: %v", err)
	}

	log.Printf("CLI documentation written to %s", outDir)
}

// buildRoot constructs a cobra.Command tree that mirrors cmd/dpg exactly:
// same Use, Short, Long, and Flags on every command. No RunE implementations
// are set — cobra/doc only reads static metadata.
func buildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "dpg",
		Short: "Declarative PG — schema compiler and migration tool",
		Long: `DPG is a declarative, state-based superset of PostgreSQL SQL that compiles
to idiomatic PG DDL. Describe what your database should be; DPG figures
out what needs to change.

Source: https://github.com/thec1oud/dpg`,
	}
	root.PersistentFlags().StringP("dir", "C", "", "project root directory (default: current working directory)")
	root.PersistentFlags().String("env", "", "path to .env file (default: .env in project root, if present)")

	root.AddCommand(
		planCmd(),
		applyCmd(),
		verifyCmd(),
		dumpCmd(),
		diffCmd(),
		fmtCmd(),
		portabilityCmd(),
		validateCmd(),
		initCmd(),
		docsCmd(),
	)
	root.InitDefaultCompletionCmd()
	return root
}

func noop(_ *cobra.Command, _ []string) error { return nil }

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Diff desired state vs snapshot and print the SQL migration",
		RunE:  noop,
		Long: `Compare .dpg source files against the committed snapshot (or the live
database with --live) and print the minimal SQL required to reach the
desired state. No database connection is required unless --live is set.

Use --format json for machine-readable output suitable for CI or tooling.
Use --watch to re-run automatically whenever source files change.`,
	}
	cmd.Flags().String("cluster", "", "cluster to plan (required when multiple clusters exist)")
	cmd.Flags().String("database", "", "database to plan (required when multiple databases exist)")
	cmd.Flags().Bool("live", false, "diff against the live database instead of the stored snapshot")
	cmd.Flags().String("format", "text", "output format: text or json")
	cmd.Flags().Bool("watch", false, "re-run whenever source files change (polls every 500ms)")
	return cmd
}

func applyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute the planned migration and update the snapshot",
		RunE:  noop,
		Long: `Runs dpg plan, prompts for approval, executes the SQL against the
primary node, and updates the committed snapshot on success.

Destructive operations are blocked unless --allow-destructive is set.
Partition strategy changes additionally require --approve-partition-rebuild.

With --dry-run, the migration is computed and printed but never executed.
With --no-snapshot, the snapshot is not updated after a successful apply.
With --strict, lint warnings are promoted to errors and block the apply.`,
	}
	cmd.Flags().String("cluster", "", "cluster to apply (required when multiple clusters exist)")
	cmd.Flags().String("database", "", "database to apply (required when multiple databases exist)")
	cmd.Flags().BoolP("yes", "y", false, "skip interactive approval prompt")
	cmd.Flags().Bool("allow-destructive", false, "allow destructive operations")
	cmd.Flags().Bool("approve-partition-rebuild", false,
		"acknowledge an instruction-only partition strategy rebuild step")
	cmd.Flags().Bool("dry-run", false, "print the migration plan but do not execute or update the snapshot")
	cmd.Flags().Bool("no-snapshot", false, "skip snapshot update after a successful apply")
	cmd.Flags().Bool("strict", false, "treat lint warnings as errors (blocks apply if any exist)")
	return cmd
}

func verifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check the live database for drift against the snapshot",
		RunE:  noop,
		Long: `Introspects the live database catalog and compares it against the committed
snapshot. Reports any objects that differ from the declared state (drift).

Exits 0 when no drift is detected. Exits 1 when drift is found.`,
	}
	cmd.Flags().String("cluster", "", "cluster to verify (required when multiple clusters exist)")
	cmd.Flags().String("database", "", "database to verify (required when multiple databases exist)")
	return cmd
}

func dumpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Introspect a live database and produce initial .dpg source files",
		RunE:  noop,
		Long: `Connects to the primary node, reads the live catalog, and writes
initial .dpg source files and a snapshot. Use this to bootstrap a DPG project from an existing database.

If any target file already exists, dump refuses to proceed until you explicitly
confirm the overwrite (or pass --yes) — nothing is ever silently clobbered.

Objects whose DDL cannot be cleanly reconstructed from catalog information are emitted
as comments with a -- dpg:manual marker. Review these manually and replace them with
proper DPG declarations.`,
	}
	cmd.Flags().String("cluster", "", "cluster to dump (required when multiple clusters exist)")
	cmd.Flags().String("database", "", "database to dump (required when multiple databases exist)")
	cmd.Flags().StringP("output", "o", "", "output directory (default: cluster/database/ within project root); when set, sandboxes ALL output here, including cluster-level roles.dpg and the snapshot, not just per-database source")
	cmd.Flags().BoolP("yes", "y", false, "skip the interactive overwrite confirmation (for scripts/CI)")
	return cmd
}

func diffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff two DPG source directories and print the SQL migration",
		RunE:  noop,
		Long: `Compares two DPG database-scoped source directories and prints the SQL
migration required to go from the base state (--from) to the desired state
(--to). No snapshot or database connection is required.`,
	}
	cmd.Flags().String("from", "", "source directory representing the base state (required)")
	cmd.Flags().String("to", "", "source directory representing the desired state (required)")
	return cmd
}

func fmtCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fmt [files or dirs...]",
		Short: "Format .dpg source files in place",
		RunE:  noop,
		Long: `Reformat .dpg source files to the canonical DPG style.

Without arguments, all .dpg files in the current project are formatted.
Formatting changes layout only: every clause, comment and macro spread is
kept. A file that cannot be parsed is reported, left unchanged, and makes
the command exit 1.
With --check, exits 1 if any file would change (no files are written).
With --diff, prints a unified diff of proposed changes (no files are written).
With --stdin, reads source from stdin and writes formatted output to stdout
(used by editor integrations such as Helix that pipe file content).`,
	}
	cmd.Flags().Bool("check", false, "exit 1 if any file would change (no files written)")
	cmd.Flags().Bool("diff", false, "print unified diff of changes (no files written)")
	cmd.Flags().Bool("stdin", false, "read from stdin, write formatted output to stdout")
	return cmd
}

func portabilityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "portability",
		Short: "Report PostgreSQL-specific constructs in use",
		RunE:  noop,
		Long: `Parses the .dpg source files and reports all constructs that are
PostgreSQL-specific (not covered by ISO/IEC 9075 standard SQL), along
with standard SQL alternatives where available.

This command never blocks compilation or apply.

Use --format json for machine-readable output suitable for CI or tooling.`,
	}
	cmd.Flags().String("cluster", "", "cluster to analyze (required when multiple clusters exist)")
	cmd.Flags().String("database", "", "database to analyze (required when multiple databases exist)")
	cmd.Flags().String("format", "text", "output format: text or json")
	return cmd
}

func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [file...]",
		Short: "Validate .dpg source files offline without diffing",
		RunE:  noop,
		Long: `Parse and compile all .dpg source files and run the linter. No database
connection or snapshot is required.

Exits 0 when there are no errors. Lint warnings do not cause a non-zero exit
unless --strict is set, in which case warnings are promoted to errors.

When one or more .dpg files are given as arguments, only those files are
validated (no project discovery required). This mode is used by the LSP
server to validate individual files or editor buffers.

Use --format json for machine-readable output.`,
	}
	cmd.Flags().String("cluster", "", "cluster to validate (default: all)")
	cmd.Flags().String("database", "", "database to validate (default: all)")
	cmd.Flags().String("format", "text", "output format: text or json")
	cmd.Flags().Bool("strict", false, "treat lint warnings as errors (non-zero exit)")
	return cmd
}

func docsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Serve the DPG documentation locally",
		Long: `Start a local HTTP server and serve the embedded DPG documentation.

The documentation site is compiled into the binary at release build time.
Development builds (make build) do not embed the documentation; use a
release binary or build with: make build-full`,
		RunE: noop,
	}
	cmd.Flags().IntP("port", "p", 6060, "port to serve on")
	cmd.Flags().Bool("open", false, "open the browser automatically")
	return cmd
}

func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Scaffold a new DPG project",
		RunE:  noop,
		Long: `Create the directory structure and configuration files for a new DPG project.

If dir is omitted, the current working directory is used. Existing files are
never overwritten.`,
	}
	cmd.Flags().String("cluster", "production", "cluster directory name")
	cmd.Flags().String("database", "myapp", "database directory name")
	cmd.Flags().String("schema", "public", "default schema name")
	cmd.Flags().String("url", "", "PostgreSQL connection URL (can be set later)")
	return cmd
}
