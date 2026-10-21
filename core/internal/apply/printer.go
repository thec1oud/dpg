package apply

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
)

// Printer is where a run reports to people and asks for approval. The CLI
// prints to stdout and stderr; the zero Printer, a library's, prints
// nothing and never asks.
type Printer struct {
	// Out takes the plan and progress, Err notes and problems; nil drops
	// them.
	Out, Err        io.Writer
	Color, ErrColor bool
	// Confirm asks before anything runs; nil runs without asking.
	Confirm func(question string) bool
}

func (p Printer) info(label, msg string) {
	if p.Out != nil {
		ui.PrintInfo(p.Out, label, msg, p.Color)
	}
}

func (p Printer) success(action, msg string) {
	if p.Out != nil {
		ui.PrintSuccess(p.Out, action, msg, p.Color)
	}
}

// heading writes a bold line, set off by a blank one.
func (p Printer) heading(s string) {
	if p.Out != nil {
		fmt.Fprintf(p.Out, "\n%s\n", ui.Bold(s, p.Color))
	}
}

func (p Printer) render(m pipeline.Migration, sourcePos bool) error {
	if p.Out == nil {
		return nil
	}
	return emit.Render(p.Out, m, emit.RenderOptions{ShowSafety: true, ShowSourcePos: sourcePos, Color: p.Color})
}

func (p Printer) note(label, msg string) {
	if p.Err != nil {
		ui.PrintInfo(p.Err, label, msg, p.ErrColor)
	}
}

func (p Printer) confirm(question string) bool {
	return p.Confirm == nil || p.Confirm(question)
}

// lint reports lint diagnostics: printed, with ui.ErrSilent when one is an
// error; without Err, the errors come back as the error.
func (p Printer) lint(diags []pipeline.LintDiagnostic) error {
	if p.Err != nil {
		if ui.PrintLintDiagnostics(p.Err, diags, p.ErrColor) {
			return ui.ErrSilent
		}
		return nil
	}
	var msgs []string
	for _, d := range diags {
		if d.IsError {
			msgs = append(msgs, fmt.Sprintf("%s: [%s] %s", d.Pos, d.Rule, d.Message))
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New("lint: " + strings.Join(msgs, "; "))
}
