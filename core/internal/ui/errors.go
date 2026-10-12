package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// ErrSilent is returned when an error has already been printed to the terminal
// and the caller should exit with a non-zero status without printing anything
// additional.
var ErrSilent = errors.New("")

// ExitError carries the process exit code for Err. Commands with more than
// one non-zero outcome use it (dpg verify: 1 = drift, 2 = error).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the exit code for a command's error: an ExitError's
// code, else 1.
func ExitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 1
}

// DBError tags a database-origin error so the UI can distinguish it from DPG
// source errors and show a different heading.
type DBError struct{ Err error }

func (e *DBError) Error() string { return e.Err.Error() }
func (e *DBError) Unwrap() error { return e.Err }

// WrapDB wraps err as a database error. Pass nil → nil.
func WrapDB(err error) error {
	if err == nil {
		return nil
	}
	return &DBError{Err: err}
}

// PrintError classifies err and writes a consistently formatted, optionally
// coloured error block to w. It handles:
//
//   - pipeline.Diagnostics (one or more compiler errors with source positions)
//   - *pipeline.CompilerError (single compiler error with source position)
//   - *DBError (database-layer error)
//   - ErrSilent (already printed; suppressed)
//   - anything else (system / configuration error)
func PrintError(w io.Writer, err error, color bool) {
	if err == nil || errors.Is(err, ErrSilent) {
		return
	}

	// Multiple compiler errors arrive as Diagnostics.
	var diags pipeline.Diagnostics
	if errors.As(err, &diags) {
		for _, e := range diags {
			printCompilerError(w, e, color)
		}
		return
	}

	// Single compiler error.
	var ce *pipeline.CompilerError
	if errors.As(err, &ce) {
		printCompilerError(w, ce, color)
		return
	}

	// Database error.
	var dbe *DBError
	if errors.As(err, &dbe) {
		printDBError(w, dbe.Err, color)
		return
	}

	// Generic / system / config error.
	printGenericError(w, err, color)
}

func printCompilerError(w io.Writer, e *pipeline.CompilerError, color bool) {
	label := Red("error", color) + " " + Dim("[dpg]", color)
	pos := Magenta(e.Pos.String(), color)
	fmt.Fprintf(w, "%s  %s\n", label, pos)
	for line := range strings.SplitSeq(e.Message, "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
	fmt.Fprintln(w)
}

func printDBError(w io.Writer, err error, color bool) {
	label := Red("error", color) + " " + Dim("[db]", color)
	fmt.Fprintf(w, "%s\n", label)
	for line := range strings.SplitSeq(err.Error(), "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
	fmt.Fprintln(w)
}

func printGenericError(w io.Writer, err error, color bool) {
	label := Red("error", color)
	fmt.Fprintf(w, "%s\n", label)
	for line := range strings.SplitSeq(err.Error(), "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
	fmt.Fprintln(w)
}

// PrintLintDiagnostics writes each diagnostic to w and returns true if any
// were errors. Uses coloured output when color is true.
func PrintLintDiagnostics(w io.Writer, diags []pipeline.LintDiagnostic, color bool) bool {
	hasErrors := false
	for _, d := range diags {
		printLintDiagnostic(w, d, color)
		if d.IsError {
			hasErrors = true
		}
	}
	return hasErrors
}

func printLintDiagnostic(w io.Writer, d pipeline.LintDiagnostic, color bool) {
	var label string
	if d.IsError {
		label = Red("error", color) + " " + Dim("["+d.Rule+"]", color)
	} else {
		label = Yellow("warning", color) + " " + Dim("["+d.Rule+"]", color)
	}
	pos := ""
	if d.Pos.File != "" {
		pos = "  " + Magenta(d.Pos.String(), color)
	}
	fmt.Fprintf(w, "%s%s\n", label, pos)
	fmt.Fprintf(w, "  %s\n\n", d.Message)
}

// PrintInfo writes a neutral context+message line.
func PrintInfo(w io.Writer, context, message string, color bool) {
	if context != "" {
		fmt.Fprintf(w, "%s  %s\n", DimCyan(context, color), message)
	} else {
		fmt.Fprintf(w, "%s\n", message)
	}
}

// PrintSuccess writes a green action label followed by context.
func PrintSuccess(w io.Writer, action, context string, color bool) {
	fmt.Fprintf(w, "\n%s  %s\n", Green(action, color), Cyan(context, color))
}

// PrintPortabilityIssue formats a single portability finding.
func PrintPortabilityIssue(w io.Writer, pos pipeline.SourcePos, construct, alternative string, color bool) {
	posStr := ""
	if pos.File != "" {
		posStr = "  " + Magenta(fmt.Sprintf("%s:%d", pos.File, pos.Line), color)
	}
	fmt.Fprintf(w, "%s%s\n", Cyan(construct, color), posStr)
	if alternative != "" {
		fmt.Fprintf(w, "  %s %s\n\n", Dim("alternative:", color), alternative)
	} else {
		fmt.Fprintln(w)
	}
}
