package tui

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestProgram runs the model in a real Bubble Tea program: keys arrive
// concurrently, Apply runs as a command on another goroutine, and the
// program ends on q.
func TestProgram(t *testing.T) {
	e := sample()
	p := tea.NewProgram(New(e), tea.WithInput(nil), tea.WithOutput(io.Discard))
	done := make(chan tea.Model, 1)
	go func() {
		m, err := p.Run()
		if err != nil {
			t.Error(err)
		}
		done <- m
	}()
	p.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	// Down to role app's connection_limit (row 3), adopt, preview, apply.
	for _, k := range []string{"down", "down", "down", "a", "p", "y"} {
		p.Send(keyMsg(k))
	}
	deadline := time.After(10 * time.Second)
	for e.appliedCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("apply never ran")
		case <-time.After(10 * time.Millisecond):
		}
	}
	p.Send(keyMsg("q"))
	select {
	case m := <-done:
		if !m.(Model).Quitting() {
			t.Error("the program ended without quitting")
		}
	case <-deadline:
		t.Fatal("q didn't end the program")
	}
	if len(e.prepared) != 1 || len(e.prepared[0]) != 1 || e.prepared[0][0].ItemID != "role:app/connection_limit" {
		t.Errorf("prepared: %+v", e.prepared)
	}
}
