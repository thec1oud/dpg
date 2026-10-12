package emit_test

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// TestRenderHeaderLines: each header field is on its own line.
func TestRenderHeaderLines(t *testing.T) {
	var b strings.Builder
	m := pipeline.Migration{Meta: pipeline.MigrationMeta{Cluster: "production", Database: "myapp"}}
	if err := emit.Render(&b, m, emit.RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "-- Cluster:         production\n") || !strings.Contains(out, "\n-- Database:        myapp\n") {
		t.Errorf("got:\n%s", out)
	}
}
