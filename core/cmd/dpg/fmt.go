package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/project"
)

func newFmtCmd() *cobra.Command {
	var (
		check    bool
		diff     bool
		useStdin bool
	)

	cmd := &cobra.Command{
		Use:   "fmt [files or dirs...]",
		Short: "Format .dpg source files in place",
		Long: `Reformat .dpg source files to the canonical DPG style.

With no arguments, all .dpg files under the project root are formatted.
Pass specific files or directories to restrict formatting to those paths.
Formatting changes layout only: every clause, comment and macro spread is
kept. A file that cannot be parsed is reported, left unchanged, and makes
the command exit 1.

  --check   Exit 1 if any file would change; no files are written (CI gate).
  --diff    Print a unified diff of what would change; no files are written.
  --stdin   Read source from stdin and write formatted output to stdout.
            Used by editor integrations (e.g. Helix) that pipe file content.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := format.Options{
				IndentSize:  4,
				KeywordCase: "upper",
			}

			// Discover the project to get formatter config. If no project is
			// found (e.g. formatting standalone files), fall back to defaults.
			proj, projErr := discoverProject()
			if projErr == nil {
				if proj.RootConfig.Fmt.IndentSize > 0 {
					opts.IndentSize = proj.RootConfig.Fmt.IndentSize
				}
				if proj.RootConfig.Fmt.KeywordCase != "" {
					opts.KeywordCase = proj.RootConfig.Fmt.KeywordCase
				}
			}

			if useStdin {
				src, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("dpg fmt: reading stdin: %w", err)
				}
				out, _ := format.Format("<stdin>", src, opts)
				_, err = os.Stdout.Write(out)
				return err
			}

			targets, err := resolveFmtTargetsRaw(proj, projErr, args)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return fmt.Errorf("dpg fmt: no .dpg files found")
			}

			return runFmt(targets, opts, check, diff)
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "exit 1 if any file would change (no files written)")
	cmd.Flags().BoolVar(&diff, "diff", false, "print unified diff of changes (no files written)")
	cmd.Flags().BoolVar(&useStdin, "stdin", false, "read from stdin, write formatted output to stdout")
	return cmd
}

// resolveFmtTargetsRaw returns the list of .dpg files to format.
// proj/projErr are the results of discoverProject — proj may be nil.
// If args is empty and a project was found, all project source files are used.
// If args is empty and no project, returns an error.
// If args are given, resolves each as a file or directory regardless of project.
func resolveFmtTargetsRaw(proj *project.Project, projErr error, args []string) ([]string, error) {
	if len(args) == 0 {
		if projErr != nil {
			return nil, fmt.Errorf("dpg fmt: no files specified and no project found: %w", projErr)
		}
		var files []string
		for _, cluster := range proj.Clusters {
			files = append(files, cluster.SourceFiles...)
			for _, db := range cluster.Databases {
				files = append(files, db.SourceFiles...)
			}
		}
		return files, nil
	}

	var files []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("fmt: %w", err)
		}
		if info.IsDir() {
			walked, err := walkDpgFiles(arg)
			if err != nil {
				return nil, err
			}
			files = append(files, walked...)
		} else if strings.HasSuffix(arg, ".dpg") {
			files = append(files, arg)
		}
	}
	return files, nil
}

// walkDpgFiles recursively finds .dpg files under dir.
func walkDpgFiles(dir string) ([]string, error) {
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

// runFmt formats each file according to opts.
// In --check mode it reports which files would change and returns a non-nil
// error if any would. In --diff mode it prints a human-readable diff.
// Otherwise it writes formatted content back to disk.
func runFmt(files []string, opts format.Options, check, showDiff bool) error {
	anyChanged := false
	failed := 0

	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("fmt: reading %s: %w", path, err)
		}

		formatted, err := format.Format(path, src, opts)
		if err != nil {
			// Skip the file and keep going, but fail the run: a file
			// that doesn't parse must not pass --check.
			fmt.Fprintf(os.Stderr, "dpg fmt: could not format %s: %v\n", path, err)
			failed++
			continue
		}

		if bytes.Equal(src, formatted) {
			continue
		}

		anyChanged = true

		if check {
			fmt.Fprintf(os.Stderr, "would reformat: %s\n", path)
			continue
		}

		if showDiff {
			printUnifiedDiff(os.Stdout, path, string(src), string(formatted))
			continue
		}

		if err := os.WriteFile(path, formatted, 0644); err != nil {
			return fmt.Errorf("fmt: writing %s: %w", path, err)
		}
	}

	if failed > 0 {
		return fmt.Errorf("dpg fmt: %d file(s) could not be formatted", failed)
	}
	if check && anyChanged {
		return fmt.Errorf("dpg fmt --check: %d file(s) would be reformatted", countChanged(files, opts))
	}
	return nil
}

// countChanged returns the number of files that would be reformatted.
func countChanged(files []string, opts format.Options) int {
	n := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		formatted, err := format.Format(path, src, opts)
		if err != nil {
			continue
		}
		if !bytes.Equal(src, formatted) {
			n++
		}
	}
	return n
}

// printUnifiedDiff writes a unified diff of old vs new to w, with @@ hunk
// markers and 3 lines of context around each changed region.
func printUnifiedDiff(w *os.File, name, old, new string) {
	const ctx = 3

	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	// Build an edit list: pairs of (oldIdx, newIdx, kind) where kind is ' ',
	// '-', or '+'. We use a simple O(n) scan assuming lines align by index,
	// which is correct for formatting changes (whitespace/case only).
	type edit struct {
		kind    byte
		oldLine int // 1-based
		newLine int
		text    string
	}
	var edits []edit
	oi, ni := 0, 0
	for oi < len(oldLines) || ni < len(newLines) {
		switch {
		case oi >= len(oldLines):
			edits = append(edits, edit{'+', oi + 1, ni + 1, newLines[ni]})
			ni++
		case ni >= len(newLines):
			edits = append(edits, edit{'-', oi + 1, ni + 1, oldLines[oi]})
			oi++
		case oldLines[oi] == newLines[ni]:
			edits = append(edits, edit{' ', oi + 1, ni + 1, oldLines[oi]})
			oi++
			ni++
		default:
			edits = append(edits, edit{'-', oi + 1, ni + 1, oldLines[oi]})
			edits = append(edits, edit{'+', oi + 1, ni + 1, newLines[ni]})
			oi++
			ni++
		}
	}

	// Collect changed edit indices.
	var changed []int
	for i, e := range edits {
		if e.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return
	}

	fmt.Fprintf(w, "--- %s\n", name)
	fmt.Fprintf(w, "+++ %s\n", name)

	// Group changed lines into hunks separated by more than 2*ctx unchanged lines.
	type hunk struct{ start, end int }
	var hunks []hunk
	hunkStart := max(0, changed[0]-ctx)
	hunkEnd := min(len(edits)-1, changed[0]+ctx)
	for _, ci := range changed[1:] {
		if ci-ctx <= hunkEnd {
			hunkEnd = min(len(edits)-1, ci+ctx)
		} else {
			hunks = append(hunks, hunk{hunkStart, hunkEnd})
			hunkStart = max(0, ci-ctx)
			hunkEnd = min(len(edits)-1, ci+ctx)
		}
	}
	hunks = append(hunks, hunk{hunkStart, hunkEnd})

	for _, h := range hunks {
		// Count old and new lines in the hunk.
		oldCount, newCount := 0, 0
		oldStart, newStart := 0, 0
		for i, e := range edits[h.start : h.end+1] {
			if e.kind != '+' {
				oldCount++
				if i == 0 {
					oldStart = e.oldLine
				}
			}
			if e.kind != '-' {
				newCount++
				if i == 0 {
					newStart = e.newLine
				}
			}
		}
		if oldStart == 0 {
			oldStart = edits[h.start].oldLine
		}
		if newStart == 0 {
			newStart = edits[h.start].newLine
		}
		fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, e := range edits[h.start : h.end+1] {
			fmt.Fprintf(w, "%c%s\n", e.kind, e.text)
		}
	}
}
