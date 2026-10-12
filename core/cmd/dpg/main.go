package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
	"github.com/thec1oud/dpg/internal/version"

	// Import default pipeline implementations to trigger their init() registration.
	_ "github.com/thec1oud/dpg/internal/blockparser"
	_ "github.com/thec1oud/dpg/internal/diff"
	_ "github.com/thec1oud/dpg/internal/emit"
	_ "github.com/thec1oud/dpg/internal/executor"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/introspect"
	_ "github.com/thec1oud/dpg/internal/ir"
	_ "github.com/thec1oud/dpg/internal/linter"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	_ "github.com/thec1oud/dpg/internal/portability"
	_ "github.com/thec1oud/dpg/internal/scanner"
	_ "github.com/thec1oud/dpg/internal/secrets"
	_ "github.com/thec1oud/dpg/internal/secrets/awssm"
	_ "github.com/thec1oud/dpg/internal/secrets/azurekv"
	_ "github.com/thec1oud/dpg/internal/secrets/gcpsm"
	_ "github.com/thec1oud/dpg/internal/secrets/vault"
	_ "github.com/thec1oud/dpg/internal/snapshot"
)

var (
	projectDir string
	envFile    string
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		if !errors.Is(err, ui.ErrSilent) {
			ui.PrintError(os.Stderr, err, ui.IsColorEnabled(os.Stderr))
		}
		os.Exit(ui.ExitCode(err))
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "dpg",
		Short:   "Declarative PG — schema compiler and migration tool",
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.Commit, version.Date),
		Long: `DPG is a declarative, state-based superset of PostgreSQL SQL that compiles
to idiomatic PG DDL. Describe what your database should be; DPG figures
out what needs to change.

Source: https://github.com/thec1oud/dpg`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVarP(
		&projectDir, "dir", "C", "",
		"project root directory (default: current working directory)",
	)
	root.PersistentFlags().StringVar(
		&envFile, "env", "",
		"path to .env file (default: .env in project root, if present)",
	)

	root.AddCommand(
		newPlanCmd(),
		newApplyCmd(),
		newVerifyCmd(),
		newResolveCmd(),
		newLogCmd(),
		newDumpCmd(),
		newDiffCmd(),
		newFmtCmd(),
		newPortabilityCmd(),
		newValidateCmd(),
		newInitCmd(),
		newDocsCmd(),
	)

	return root
}

// resolveProjectDir returns the effective project root directory.
func resolveProjectDir() (string, error) {
	if projectDir != "" {
		abs, err := absPath(projectDir)
		if err != nil {
			return "", fmt.Errorf("--dir: %w", err)
		}
		return abs, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot get working directory: %w", err)
	}
	return dir, nil
}

// discoverProject resolves the project root, discovers clusters/databases, and
// configures the snapshot store to use the project's snapshot directory.
func discoverProject() (*project.Project, error) {
	dir, err := resolveProjectDir()
	if err != nil {
		return nil, err
	}
	proj, err := project.Discover(dir)
	if err != nil {
		return nil, err
	}
	if store, ok := pipeline.Resolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore); ok {
		if fs, ok := store.(*snapshotpkg.FileStore); ok {
			fs.Dir = proj.SnapshotDir()
		}
	}
	return proj, nil
}

func absPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	return abs, nil
}
