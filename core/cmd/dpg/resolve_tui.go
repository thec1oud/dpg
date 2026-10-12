package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thec1oud/dpg/internal/resolve"
	"github.com/thec1oud/dpg/internal/resolve/tui"
	"github.com/thec1oud/dpg/internal/ui"
)

// tuiEngine connects the interactive dpg resolve to the project.
type tuiEngine struct {
	env              *resolveEnv
	by               string
	color            bool
	allowDestructive bool
}

func (e *tuiEngine) Targets() []*resolve.Target { return e.env.targets }

func (e *tuiEngine) Requires(t *resolve.Target, id string) []string { return resolve.Requires(t, id) }

func (e *tuiEngine) MacroDeclared(t *resolve.Target, id string) bool {
	return t != nil && resolve.MacroDeclared(t, id)
}

func (e *tuiEngine) Prepare(ds []resolve.Decision) (*resolve.Plan, error) {
	for i := range ds {
		if ds[i].Action == resolve.Acknowledge {
			ds[i].By = e.by
		}
	}
	return resolve.Prepare(e.env.targets, ds, time.Now(), e.env.deps)
}

func (e *tuiEngine) Render(p *resolve.Plan, width int) string {
	var b strings.Builder
	renderResolvePlan(&b, p, e.env.proj.RootDir, e.color)
	if err := checkDestructive(p, e.allowDestructive); err != nil {
		fmt.Fprintf(&b, "\n%s %s\n", ui.Red("error", e.color), err)
	}
	if p.Empty() && len(p.Problems) == 0 {
		b.WriteString("Nothing to change.\n")
	}
	return strings.TrimLeft(b.String(), "\n")
}

func (e *tuiEngine) Apply(p *resolve.Plan) error {
	if err := checkDestructive(p, e.allowDestructive); err != nil {
		return err
	}
	return e.env.execute(p)
}

func (e *tuiEngine) Reload() error {
	targets, err := e.env.load()
	if err != nil {
		return err
	}
	e.env.targets = targets
	return nil
}

// runResolveTUI runs the interactive dpg resolve.
func runResolveTUI(env *resolveEnv, f resolveFlags) error {
	e := &tuiEngine{env: env, by: gitUserName(), color: ui.IsColorEnabled(os.Stdout), allowDestructive: f.allowDestructive}
	_, err := tea.NewProgram(tui.New(e), tea.WithAltScreen()).Run()
	return err
}
