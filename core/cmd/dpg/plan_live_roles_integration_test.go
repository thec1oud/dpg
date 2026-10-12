//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestPlanLiveRolesNoSpuriousOps: plan --live against a cluster whose
// declared role already exists with its password must be empty. testpg's
// bootstrap superuser is "dpg" (not "postgres"), so it is also the
// connecting role; it, and a role another application created, must not be
// dropped, and the password (unreadable from the catalog) must not be
// re-set.
func TestPlanLiveRolesNoSpuriousOps(t *testing.T) {
	ctx := context.Background()
	connStr := testpg.Start(t)

	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	for _, stmt := range []string{
		`CREATE ROLE app_service LOGIN PASSWORD 's3cret'`,
		`CREATE ROLE other_app NOLOGIN`,
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(src, []byte("ROLE app_service LOGIN PASSWORD '{{env:APP_SERVICE_PW}}';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired, _, err := compiler.Compile([]string{src}, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	cl := &project.Cluster{
		Config: config.ClusterConfig{Cluster: config.ClusterDef{Name: "c", URL: connStr}},
	}
	snap, err := introspectClusterSnapshot(ctx, cl, &mockSecretResolver{}, introspect.New())
	if err != nil {
		t.Fatalf("introspectClusterSnapshot: %v", err)
	}
	if _, ok := snap.Objects["dpg"]; ok {
		t.Errorf("bootstrap superuser %q must not be introspected", "dpg")
	}
	if _, ok := snap.Objects["other_app"]; !ok {
		t.Fatalf("expected other_app in the live snapshot; objects: %v", snap.Objects)
	}

	ops, err := diff.New().Diff(desired, snap)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, op := range ops {
		t.Errorf("expected an empty plan, got: %s", op.SQL())
	}
}
