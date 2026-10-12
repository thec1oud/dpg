package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/ignore"
	dpglinter "github.com/thec1oud/dpg/internal/linter"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
)

func newValidateCmd() *cobra.Command {
	var (
		clusterName  string
		databaseName string
		format       string
		strict       bool
	)

	cmd := &cobra.Command{
		Use:   "validate [file...]",
		Short: "Validate .dpg source files offline without diffing",
		Long: `Parse and compile all .dpg source files and run the linter. No database
connection or snapshot is required.

Exits 0 when there are no errors. Lint warnings do not cause a non-zero exit
unless --strict is set, in which case warnings are promoted to errors.

When one or more .dpg files are given as arguments, only those files are
validated (no project discovery required). This mode is used by the LSP
server to validate individual files or editor buffers.

Use --format json for machine-readable output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			linter, _ := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter)

			// File-argument mode: validate specific files without project discovery.
			if len(args) > 0 {
				// Use the directory of the first file as dbDir so schema inference works.
				dbDir := filepath.Dir(args[0])
				errored, err := runValidate("(none)", "(standalone)", args, dbDir, compiler.Options{}, linter, pipeline.LinterConfig{}, format, strict, nil)
				if err != nil {
					return err
				}
				if errored {
					return ui.ErrSilent
				}
				return nil
			}

			proj, err := discoverProject()
			if err != nil {
				return err
			}

			clusters, err := resolveClusters(proj, clusterName)
			if err != nil {
				return err
			}

			lintCfg := linterConfigFrom(proj.RootConfig.Linter)

			hasError := false
			for _, cl := range clusters {
				if len(cl.SourceFiles) > 0 {
					clusterLintCfg := lintCfg
					clusterLintCfg.MinPGVersion = cl.EffectiveMinPGVersion(proj.RootConfig)
					errored, err := runValidate(cl.Name(), "(cluster)", cl.SourceFiles, cl.ObjectsDir, compiler.Options{}, linter, clusterLintCfg, format, strict,
						ignore.New(cl.EffectiveIgnore(proj.RootConfig)))
					if err != nil {
						return err
					}
					if errored {
						hasError = true
					}
				}

				databases, err := resolveDatabases(cl, databaseName)
				if err != nil {
					return err
				}
				for _, db := range databases {
					dbLintCfg := lintCfg
					dbLintCfg.MinPGVersion = db.EffectiveMinPGVersion(cl, proj.RootConfig)
					errored, err := runValidate(cl.Name(), db.Name(), db.SourceFiles, db.Dir, dbCompileOptions(db), linter, dbLintCfg, format, strict,
						ignore.New(db.EffectiveIgnore(cl, proj.RootConfig)))
					if err != nil {
						return err
					}
					if errored {
						hasError = true
					}
				}
			}

			if hasError {
				return ui.ErrSilent
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to validate (default: all)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to validate (default: all)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat lint warnings as errors (non-zero exit)")
	return cmd
}

type validateJSON struct {
	Cluster  string          `json:"cluster"`
	Database string          `json:"database"`
	Objects  int             `json:"objects"`
	Errors   []diagnosticOut `json:"errors"`
	Warnings []diagnosticOut `json:"warnings"`
}

type diagnosticOut struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
}

// runValidate compiles and lints one database or cluster scope. Returns (hasError, err).
func runValidate(
	clusterName, dbName string,
	files []string, dbDir string,
	opts compiler.Options,
	linter pipeline.Linter,
	lintCfg pipeline.LinterConfig,
	format string,
	strict bool,
	rules *ignore.Rules,
) (bool, error) {
	color := ui.IsColorEnabled(os.Stderr)

	desired, mergeDiags, err := compiler.CompileWith(files, dbDir, pipeline.Default, opts)
	if err == nil {
		err = rules.CheckDeclared(desired)
	}
	if err != nil {
		if format == "json" {
			errors := compileErrsToDiagnostics(err)
			out := validateJSON{
				Cluster:  clusterName,
				Database: dbName,
				Errors:   errors,
				Warnings: []diagnosticOut{},
			}
			return true, writeJSON(out)
		}
		return true, err
	}

	diags := dpglinter.FilterMergeDiagnostics(mergeDiags, lintCfg)
	if linter != nil {
		lintDiags, err := linter.Lint(desired, lintCfg)
		if err != nil {
			return true, err
		}
		diags = append(diags, lintDiags...)
	}

	// --strict promotes all warnings to errors.
	if strict {
		for i := range diags {
			diags[i].IsError = true
		}
	}

	if format == "json" {
		return emitValidateJSON(clusterName, dbName, len(desired), diags)
	}

	// Text output.
	hasError := false
	if len(diags) == 0 {
		fmt.Fprintf(os.Stdout, "%s/%s: %d object(s) — OK\n", clusterName, dbName, len(desired))
	} else {
		fmt.Fprintf(os.Stdout, "%s/%s: %d object(s)\n", clusterName, dbName, len(desired))
		if ui.PrintLintDiagnostics(os.Stderr, diags, color) {
			hasError = true
		}
	}
	return hasError, nil
}

func emitValidateJSON(cluster, database string, objectCount int, diags []pipeline.LintDiagnostic) (bool, error) {
	out := validateJSON{
		Cluster:  cluster,
		Database: database,
		Objects:  objectCount,
		Errors:   []diagnosticOut{},
		Warnings: []diagnosticOut{},
	}
	hasError := false
	for _, d := range diags {
		entry := diagnosticOut{
			Rule:    d.Rule,
			Message: d.Message,
			File:    d.Pos.File,
			Line:    d.Pos.Line,
			Col:     d.Pos.Col,
		}
		if d.IsError {
			hasError = true
			out.Errors = append(out.Errors, entry)
		} else {
			out.Warnings = append(out.Warnings, entry)
		}
	}
	return hasError, writeJSON(out)
}

// compileErrsToDiagnostics converts a compiler error to a slice of diagnosticOut,
// preserving file/line/col from *pipeline.Diagnostics when available.
func compileErrsToDiagnostics(err error) []diagnosticOut {
	if diags, ok := err.(pipeline.Diagnostics); ok {
		out := make([]diagnosticOut, 0, len(diags))
		for _, d := range diags {
			// DPG-E000 is the generic fallback rule label for an
			// unlabeled CompilerError — most call sites still predate
			// the Code field (see CompilerError.Code's doc comment).
			rule := d.Code
			if rule == "" {
				rule = "DPG-E000"
			}
			out = append(out, diagnosticOut{
				Rule:    rule,
				Message: d.Message,
				File:    d.Pos.File,
				Line:    d.Pos.Line,
				Col:     d.Pos.Col,
			})
		}
		return out
	}
	return []diagnosticOut{{Message: err.Error()}}
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
