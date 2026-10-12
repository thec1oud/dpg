//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/testpg"
)

// captureStderr returns what fn writes to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestIgnoreAndUndeclaredRolesLive covers the [ignore] section and the
// undeclared-role warning against a real cluster: verify (cluster and
// database), plan --live, DPG-E045, and dump's introspection filter.
func TestIgnoreAndUndeclaredRolesLive(t *testing.T) {
	ctx := context.Background()
	connStr := testpg.Start(t)
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	exec := func(stmts ...string) {
		t.Helper()
		for _, s := range stmts {
			if _, err := conn.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}

	dir := t.TempDir()
	store := &snapshot.FileStore{Dir: filepath.Join(dir, "snapshots")}
	resolver := &mockSecretResolver{}
	rolesFile := filepath.Join(dir, "roles.dpg")
	if err := os.WriteFile(rolesFile, []byte("ROLE app_service LOGIN;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cl := &project.Cluster{
		Config:      config.ClusterConfig{Cluster: config.ClusterDef{Name: "c", URL: connStr}},
		ObjectsDir:  dir,
		SourceFiles: []string{rolesFile},
	}
	db := &project.Database{Config: config.DatabaseConfig{Database: config.DatabaseDef{Name: "dpgtest", DefaultSchema: "public"}}}

	// The committed state: app_service and public.t.
	exec(`CREATE ROLE app_service LOGIN`, `CREATE TABLE public.t (id int)`)
	live, err := introspect.New().Introspect(ctx, conn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	clusterSnap, dbSnap := &pipeline.Snapshot{}, &pipeline.Snapshot{}
	var clusterObjs []pipeline.IRObject
	for _, o := range live {
		if isClusterScoped(o) {
			clusterObjs = append(clusterObjs, o)
		}
	}
	if err := snapshot.Populate(clusterSnap, clusterObjs); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Populate(dbSnap, excludeClusterScoped(live)); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("c", cl.ClusterSnapshotKey(), clusterSnap); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("c", "dpgtest", dbSnap); err != nil {
		t.Fatal(err)
	}

	// Changes made outside DPG afterwards.
	exec(`CREATE ROLE other_app NOLOGIN`, `CREATE ROLE rds_admin NOLOGIN`,
		`CREATE SCHEMA cron`, `CREATE TABLE cron.job (id int)`)

	clusterRules := ignore.New(map[string][]string{"role": {"rds_*"}})
	dbRules := ignore.New(map[string][]string{"schema": {"cron"}})

	// verify, cluster: undeclared roles warn, they are not drift; ignored
	// roles and the bootstrap superuser are not mentioned at all.
	report, err := runVerifyCluster(cl, store, introspect.New(), diff.New(), resolver, clusterRules, logPosition{})
	if err != nil {
		t.Fatalf("verify cluster: %v", err)
	}
	if got := itemIDs(report, ""); got != "warning role:other_app" {
		t.Errorf("verify cluster: got %q, want only the other_app warning", got)
	}

	// verify, database: the ignored schema and its table are not reported;
	// without the rule they are unmanaged, which is not drift.
	report, err = runVerify(cl, db, store, introspect.New(), diff.New(), resolver, dbRules, logPosition{})
	if err != nil || len(report.Items) != 0 {
		t.Errorf("ignored schema reported (err %v): %q", err, itemIDs(report, ""))
	}
	report, err = runVerify(cl, db, store, introspect.New(), diff.New(), resolver, nil, logPosition{})
	if err != nil || report.HasDrift() || !strings.Contains(itemIDs(report, ""), "unmanaged schema:cron") {
		t.Errorf("without [ignore], cron must be unmanaged, not drift (err %v): %q", err, itemIDs(report, ""))
	}

	// plan --live, cluster: empty plan, the same warning.
	liveSnap, err := introspectClusterSnapshot(ctx, cl, resolver, introspect.New())
	if err != nil {
		t.Fatalf("introspectClusterSnapshot: %v", err)
	}
	var m pipeline.Migration
	stderr := captureStderr(t, func() {
		m, err = buildClusterPlan(cl, liveSnap, diff.New(), emit.New(), pipeline.LinterConfig{}, "text", clusterRules)
	})
	if err != nil {
		t.Fatalf("plan --live: %v", err)
	}
	for _, op := range append(append(m.PreTransactional, m.Transactional...), m.NonTransactional...) {
		t.Errorf("expected an empty plan, got: %s", op.SQL())
	}
	if !strings.Contains(stderr, "role other_app") || strings.Contains(stderr, "rds_admin") {
		t.Errorf("plan --live: expected a warning naming other_app only, got:\n%s", stderr)
	}

	// DPG-E045: declaring an ignored object.
	if err := os.WriteFile(rolesFile, []byte("ROLE app_service LOGIN;\nROLE rds_custom NOLOGIN;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = buildClusterPlan(cl, &pipeline.Snapshot{}, diff.New(), emit.New(), pipeline.LinterConfig{}, "text", clusterRules)
	var diags pipeline.Diagnostics
	if !errors.As(err, &diags) || len(diags) != 1 || diags[0].Code != "DPG-E045" {
		t.Errorf("expected one DPG-E045, got %v", err)
	}

	// dump: ignored objects are dropped from the introspection result.
	objs, err := ignoringIntrospector{introspect.New(), ignore.New(map[string][]string{
		"role": {"rds_*"}, "schema": {"cron"},
	})}.Introspect(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if r, ok := o.(*ir.Role); ok && r.Name == "rds_admin" {
			t.Error("dump introspection kept the ignored role rds_admin")
		}
		if strings.HasPrefix(o.QualifiedName(), "cron") {
			t.Errorf("dump introspection kept %s from the ignored schema", o.QualifiedName())
		}
	}

	// A declared role dropped outside DPG is still drift.
	exec(`DROP ROLE app_service`)
	report, err = runVerifyCluster(cl, store, introspect.New(), diff.New(), resolver, clusterRules, logPosition{})
	if err != nil || !report.HasDrift() || !strings.Contains(itemIDs(report, "drift"), "role:app_service") {
		t.Errorf("a dropped declared role must be drift (err %v): %q", err, itemIDs(report, ""))
	}
}

// itemIDs lists "group id" for each item, or only the IDs of group when
// group is not "".
func itemIDs(r *drift.Report, group string) string {
	if r == nil {
		return ""
	}
	var out []string
	for _, it := range r.Items {
		if group == "" {
			out = append(out, it.Group.String()+" "+it.ID)
		} else if it.Group.String() == group {
			out = append(out, it.ID)
		}
	}
	return strings.Join(out, ", ")
}
