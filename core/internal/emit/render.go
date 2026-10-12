package emit

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
)

// RenderOptions controls what the renderer writes.
type RenderOptions struct {
	// ShowSafety annotates each statement with its safety class.
	ShowSafety bool
	// ShowSourcePos annotates each statement with its source location.
	ShowSourcePos bool
	// Color enables ANSI colour output.
	Color bool
}

// DefaultRenderOptions returns options matching the RFC Section 20.2 format (no colour).
func DefaultRenderOptions() RenderOptions {
	return RenderOptions{ShowSafety: true, ShowSourcePos: true}
}

// ColoredRenderOptions returns options with colour output enabled.
func ColoredRenderOptions() RenderOptions {
	return RenderOptions{ShowSafety: true, ShowSourcePos: true, Color: true}
}

// Render writes a Migration to w in the RFC Section 20.2 SQL format.
func Render(w io.Writer, m pipeline.Migration, opts RenderOptions) error {
	c := opts.Color
	dim := func(s string) string { return ui.Dim(s, c) }
	cyan := func(s string) string { return ui.Cyan(s, c) }

	genAt := m.Meta.GeneratedAt
	if genAt.IsZero() {
		genAt = time.Now().UTC()
	}

	// Header block.
	fmt.Fprintf(w, "%s\n", dim("-- DPG Migration"))
	fmt.Fprintf(w, "%s %s\n", dim("-- Generated:      "), genAt.UTC().Format(time.RFC3339))
	if m.Meta.SourceRevision != "" {
		fmt.Fprintf(w, "%s %s\n", dim("-- Source revision:"), m.Meta.SourceRevision)
	}
	if m.Meta.Cluster != "" {
		fmt.Fprintf(w, "%s %s", dim("-- Cluster:        "), cyan(m.Meta.Cluster))
	}
	if m.Meta.Database != "" {
		fmt.Fprintf(w, "%s %s\n", dim("-- Database:       "), cyan(m.Meta.Database))
	}

	if len(m.Transactional) == 0 && len(m.NonTransactional) == 0 {
		fmt.Fprintf(w, "\n%s\n", dim("-- (no changes)"))
		return nil
	}

	writeMigrationBody(w, m, opts)
	return nil
}

// RenderAll writes multiple migrations as a single document with one shared
// header. Operations are grouped into two top-level sections — transactional
// and non-transactional — each containing every cluster/database that has ops
// in that section, introduced by "-- Database:" labels. The two sections are
// divided by a dashed rule. Only the section that has ops is printed.
func RenderAll(w io.Writer, migrations []pipeline.Migration, opts RenderOptions) error {
	if len(migrations) == 0 {
		return nil
	}

	c := opts.Color
	dim := func(s string) string { return ui.Dim(s, c) }
	cyan := func(s string) string { return ui.Cyan(s, c) }

	first := migrations[0]
	genAt := first.Meta.GeneratedAt
	if genAt.IsZero() {
		genAt = time.Now().UTC()
	}

	// Shared header — printed once for the whole document.
	fmt.Fprintf(w, "%s\n", dim("-- DPG Migration"))
	fmt.Fprintf(w, "%s %s\n", dim("-- Generated:      "), genAt.UTC().Format(time.RFC3339))
	if first.Meta.SourceRevision != "" {
		fmt.Fprintf(w, "%s %s\n", dim("-- Source revision:"), first.Meta.SourceRevision)
	}
	if first.Meta.Cluster != "" {
		fmt.Fprintf(w, "%s %s", dim("-- Cluster:        "), cyan(first.Meta.Cluster))
	}

	hasTransactional := func() bool {
		for _, m := range migrations {
			if len(m.Transactional) > 0 {
				return true
			}
		}
		return false
	}()
	hasNonTransactional := func() bool {
		for _, m := range migrations {
			if len(m.NonTransactional) > 0 {
				return true
			}
		}
		return false
	}()

	if !hasTransactional && !hasNonTransactional {
		fmt.Fprintf(w, "\n\n%s\n", dim("-- (no changes)"))
		return nil
	}

	// Transactional super-section: one BEGIN/COMMIT wraps all databases.
	if hasTransactional {
		fmt.Fprintf(w, "\n\n%s\n", dim("-- transactional"))
		fmt.Fprintf(w, "\n%s\n", ui.HighlightSQL("BEGIN;", c))
		for _, m := range migrations {
			if len(m.Transactional) == 0 {
				continue
			}
			if m.Meta.Database != "" {
				fmt.Fprintf(w, "\n%s %s\n", dim("-- Database:       "), cyan(m.Meta.Database))
			}
			for _, op := range m.Transactional {
				writeOp(w, op, opts, m.Meta.SourceRoot)
			}
		}
		fmt.Fprintf(w, "\n%s\n", ui.HighlightSQL("COMMIT;", c))
	}

	// Separator between the two top-level sections.
	if hasTransactional && hasNonTransactional {
		fmt.Fprintf(w, "\n%s\n", dim("--------"))
	}

	// Non-transactional super-section: cluster first, then each database.
	if hasNonTransactional {
		fmt.Fprintf(w, "\n%s\n", dim("-- non-transactional"))
		for _, m := range migrations {
			if len(m.NonTransactional) == 0 {
				continue
			}
			if m.Meta.Database != "" {
				fmt.Fprintf(w, "\n%s %s\n", dim("-- Database:       "), cyan(m.Meta.Database))
			}
			for _, op := range m.NonTransactional {
				writeOp(w, op, opts, m.Meta.SourceRoot)
			}
		}
	}

	return nil
}

// writeMigrationBody renders the two sections of a migration: a transactional
// block wrapped in BEGIN/COMMIT, and a non-transactional block for operations
// that must run outside a transaction (e.g. CREATE INDEX CONCURRENTLY). Both
// sections share the same structure and are separated by a dashed rule.
func writeMigrationBody(w io.Writer, m pipeline.Migration, opts RenderOptions) {
	c := opts.Color
	dim := func(s string) string { return ui.Dim(s, c) }

	if len(m.Transactional) > 0 {
		fmt.Fprintf(w, "\n%s\n", dim("-- transactional"))
		fmt.Fprintf(w, "%s\n", ui.HighlightSQL("BEGIN;", c))
		for _, op := range m.Transactional {
			writeOp(w, op, opts, m.Meta.SourceRoot)
		}
		fmt.Fprintf(w, "\n%s\n", ui.HighlightSQL("COMMIT;", c))
	}

	if len(m.Transactional) > 0 && len(m.NonTransactional) > 0 {
		fmt.Fprintf(w, "\n%s\n", dim("--------"))
	}

	if len(m.NonTransactional) > 0 {
		fmt.Fprintf(w, "\n%s\n", dim("-- non-transactional"))
		for _, op := range m.NonTransactional {
			writeOp(w, op, opts, m.Meta.SourceRoot)
		}
	}
}

func writeOp(w io.Writer, op pipeline.DiffOp, opts RenderOptions, root string) {
	c := opts.Color
	var parts []string

	if opts.ShowSourcePos {
		if pos := op.Pos(); pos.File != "" {
			posStr := fmt.Sprintf("%s:%d", RelSourcePath(root, pos.File), pos.Line)
			parts = append(parts, "source: "+ui.Magenta(posStr, c))
		}
	}
	if opts.ShowSafety && op.Safety() != pipeline.Safe {
		parts = append(parts, "safety: "+safetyLabel(op.Safety(), c))
	}

	fmt.Fprintln(w)
	if len(parts) > 0 {
		fmt.Fprintf(w, "%s %s\n", ui.Dim("--", c), strings.Join(parts, ui.Dim(", ", c)))
	}
	fmt.Fprintf(w, "%s\n", ui.HighlightSQL(op.SQL(), c))
}

// RelSourcePath returns file relative to root (RFC Section 17.1), or file
// unchanged when root is empty or file is not under it.
func RelSourcePath(root, file string) string {
	if root == "" || !filepath.IsAbs(file) {
		return file
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return file
	}
	return filepath.ToSlash(rel)
}

func safetyLabel(s pipeline.Safety, color bool) string {
	switch s {
	case pipeline.Caution:
		return ui.Yellow(s.String(), color)
	case pipeline.Destructive:
		return ui.Red(s.String(), color)
	case pipeline.Manual:
		return ui.Blue(s.String(), color)
	default:
		return s.String()
	}
}
