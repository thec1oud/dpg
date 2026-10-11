package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
)

type portabilityJSON struct {
	Cluster  string                `json:"cluster"`
	Database string                `json:"database"`
	Issues   []portabilityIssueOut `json:"issues"`
}

type portabilityIssueOut struct {
	Construct   string `json:"construct"`
	Alternative string `json:"alternative,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Col         int    `json:"col,omitempty"`
}

func newPortabilityCmd() *cobra.Command {
	var (
		clusterName  string
		databaseName string
		format       string
	)

	cmd := &cobra.Command{
		Use:   "portability",
		Short: "Report PostgreSQL-specific constructs in use",
		Long: `Parses the .dpg source files and reports all constructs that are
PostgreSQL-specific (not covered by ISO/IEC 9075 standard SQL), along
with standard SQL alternatives where available.

This command never blocks compilation or apply.

Use --format json for machine-readable output suitable for CI or tooling.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			proj, err := discoverProject()
			if err != nil {
				return err
			}

			clusters, err := resolveClusters(proj, clusterName)
			if err != nil {
				return err
			}

			analyzer, err := pipeline.MustResolve[pipeline.PortabilityAnalyzer](pipeline.Default, pipeline.KeyPortabilityAnalyzer)
			if err != nil {
				return err
			}

			color := ui.IsColorEnabled(os.Stdout)

			for _, cl := range clusters {
				if len(cl.SourceFiles) > 0 {
					objects, _, err := compiler.Compile(cl.SourceFiles, cl.ObjectsDir, pipeline.Default)
					if err != nil {
						return fmt.Errorf("%s (cluster): %w", cl.Name(), err)
					}
					issues, err := analyzer.Analyze(objects)
					if err != nil {
						return fmt.Errorf("%s (cluster): analyze: %w", cl.Name(), err)
					}
					if format == "json" {
						if err := writePortabilityJSON(cl.Name(), "(cluster)", issues); err != nil {
							return err
						}
					} else {
						label := cl.Name() + " (cluster)"
						if len(issues) == 0 {
							ui.PrintInfo(os.Stdout, label, "no portability issues found", color)
						} else {
							fmt.Fprintf(os.Stdout, "%s  %s\n\n",
								ui.DimCyan(label, color),
								fmt.Sprintf("%d portability issue(s)", len(issues)),
							)
							for _, iss := range issues {
								ui.PrintPortabilityIssue(os.Stdout, iss.Pos, iss.Construct, iss.Alternative, color)
							}
						}
					}
				}

				databases, err := resolveDatabases(cl, databaseName)
				if err != nil {
					return err
				}
				for _, db := range databases {
					objects, _, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, dbCompileOptions(db))
					if err != nil {
						return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
					}
					issues, err := analyzer.Analyze(objects)
					if err != nil {
						return fmt.Errorf("%s/%s: analyze: %w", cl.Name(), db.Name(), err)
					}
					if format == "json" {
						if err := writePortabilityJSON(cl.Name(), db.Name(), issues); err != nil {
							return err
						}
					} else {
						label := cl.Name() + "/" + db.Name()
						if len(issues) == 0 {
							ui.PrintInfo(os.Stdout, label, "no portability issues found", color)
							continue
						}
						fmt.Fprintf(os.Stdout, "%s  %s\n\n",
							ui.DimCyan(label, color),
							fmt.Sprintf("%d portability issue(s)", len(issues)),
						)
						for _, iss := range issues {
							ui.PrintPortabilityIssue(os.Stdout, iss.Pos, iss.Construct, iss.Alternative, color)
						}
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to analyze (required when multiple clusters exist)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to analyze (required when multiple databases exist)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")

	return cmd
}

func writePortabilityJSON(cluster, database string, issues []pipeline.PortabilityIssue) error {
	out := portabilityJSON{
		Cluster:  cluster,
		Database: database,
		Issues:   make([]portabilityIssueOut, 0, len(issues)),
	}
	for _, iss := range issues {
		out.Issues = append(out.Issues, portabilityIssueOut{
			Construct:   iss.Construct,
			Alternative: iss.Alternative,
			File:        iss.Pos.File,
			Line:        iss.Pos.Line,
			Col:         iss.Pos.Col,
		})
	}
	return writeJSON(out)
}
