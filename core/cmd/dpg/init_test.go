package main

import (
	"os"
	"testing"

	"github.com/thec1oud/dpg/internal/project"
)

// TestInitProjectIsDiscoverable guards that every dpg.toml dpg init writes is
// valid: the new project loads with no DPG-E001/DPG-E041 error and has the
// requested cluster and database.
func TestInitProjectIsDiscoverable(t *testing.T) {
	root := t.TempDir()
	devNull, _ := os.Open(os.DevNull)
	orig := os.Stdout
	os.Stdout = devNull
	err := runInit(root, "local", "app", "public", "postgres://localhost/app")
	os.Stdout = orig
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}
	proj, err := project.Discover(root)
	if err != nil {
		t.Fatalf("Discover on a freshly initialised project: %v", err)
	}
	if len(proj.Clusters) != 1 || proj.Clusters[0].Name() != "local" {
		t.Fatalf("clusters: %+v", proj.Clusters)
	}
	if dbs := proj.Clusters[0].Databases; len(dbs) != 1 || dbs[0].Name() != "app" {
		t.Fatalf("databases: %+v", dbs)
	}
}
