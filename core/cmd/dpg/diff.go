package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

func newDiffCmd() *cobra.Command {
	var (
		fromDir string
		toDir   string
	)

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Diff two DPG source directories and print the SQL migration",
		Long: `Compares two DPG database-scoped source directories and prints the SQL
required to migrate from the --from state to the --to state.

No snapshot or database connection is required.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromDir == "" {
				return fmt.Errorf("--from is required")
			}
			if toDir == "" {
				return fmt.Errorf("--to is required")
			}

			differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
			if err != nil {
				return err
			}
			emitter, err := pipeline.MustResolve[pipeline.Emitter](pipeline.Default, pipeline.KeyEmitter)
			if err != nil {
				return err
			}

			fromFiles, err := collectDPGFiles(fromDir)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			toFiles, err := collectDPGFiles(toDir)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}

			// Compile the "from" source as the base state.
			fromObjects, _, err := compiler.CompileWith(fromFiles, fromDir, pipeline.Default, dirCompileOptions(fromDir))
			if err != nil {
				return fmt.Errorf("compile --from: %w", err)
			}

			// Build a synthetic snapshot from the from-objects.
			fromSnap := &pipeline.Snapshot{}
			if err := snapshot.Populate(fromSnap, fromObjects); err != nil {
				return fmt.Errorf("build base snapshot: %w", err)
			}

			// Compile the "to" source as the desired state.
			toObjects, _, err := compiler.CompileWith(toFiles, toDir, pipeline.Default, dirCompileOptions(toDir))
			if err != nil {
				return fmt.Errorf("compile --to: %w", err)
			}

			ops, err := differ.Diff(toObjects, fromSnap)
			if err != nil {
				return fmt.Errorf("diff: %w", err)
			}

			migration, err := emitter.Emit(ops, pipeline.MigrationMeta{
				GeneratedAt: time.Now().UTC(),
			})
			if err != nil {
				return err
			}

			return emit.Render(os.Stdout, migration, emit.RenderOptions{
				ShowSafety:    true,
				ShowSourcePos: true,
				Color:         ui.IsColorEnabled(os.Stdout),
			})
		},
	}

	cmd.Flags().StringVar(&fromDir, "from", "", "source directory representing the base state (required)")
	cmd.Flags().StringVar(&toDir, "to", "", "source directory representing the desired state (required)")

	return cmd
}

// collectDPGFiles returns all .dpg files recursively under dir.
func collectDPGFiles(dir string) ([]string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}

	var files []string
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".dpg" {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// dirCompileOptions returns compile options for a bare source directory: when
// the directory holds a database dpg.toml, its default_schema applies.
func dirCompileOptions(dir string) compiler.Options {
	cfg, err := config.LoadDatabase(filepath.Join(dir, "dpg.toml"))
	if err != nil {
		return compiler.Options{}
	}
	return compiler.Options{DefaultSchema: cfg.Database.DefaultSchema}
}
