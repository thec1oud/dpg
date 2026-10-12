package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/ui"
)

// ── mock implementations ─────────────────────────────────────────────────────

type mockDiffOp struct{}

func (mockDiffOp) SQL() string             { return "SELECT 1;" }
func (mockDiffOp) Safety() pipeline.Safety { return pipeline.Safe }
func (mockDiffOp) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (mockDiffOp) Mode() pipeline.ExecMode { return pipeline.ModeTransactional }

type mockDiffer struct{ ops []pipeline.DiffOp }

func (m *mockDiffer) Diff(_ []pipeline.IRObject, _ *pipeline.Snapshot) ([]pipeline.DiffOp, error) {
	return m.ops, nil
}

type mockEmitter struct{}

func (m *mockEmitter) Emit(_ []pipeline.DiffOp, meta pipeline.MigrationMeta) (pipeline.Migration, error) {
	return pipeline.Migration{Meta: meta}, nil
}

type mockStore struct{ saveCount int }

func (m *mockStore) Load(_, _ string) (*pipeline.Snapshot, error) {
	return &pipeline.Snapshot{}, nil
}
func (m *mockStore) Save(_, _ string, _ *pipeline.Snapshot) error {
	m.saveCount++
	return nil
}

type mockApplyExec struct{ applyCount int }

func (m *mockApplyExec) Apply(_ context.Context, _ pipeline.Migration, _ pipeline.Conn) error {
	m.applyCount++
	return nil
}

type mockSecretResolver struct{}

func (m *mockSecretResolver) Resolve(uri string) (string, error) { return uri, nil }

// ── helpers ───────────────────────────────────────────────────────────────────

// applyTestFixture builds minimal project.Cluster and project.Database
// with a single empty .dpg source file. The cluster has no connection URL
// so any code path that tries to connect will fail — this is intentional
// for tests that must stop before reaching the execution step.
func applyTestFixture(t *testing.T) (*project.Cluster, *project.Database, string) {
	t.Helper()
	dir := t.TempDir()
	dpgFile := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(dpgFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	cl := &project.Cluster{
		Dir: dir,
		Config: config.ClusterConfig{
			Cluster: config.ClusterDef{Name: "test-cluster"},
		},
		ObjectsDir: dir,
	}
	db := &project.Database{
		Dir:         dir,
		Config:      config.DatabaseConfig{Database: config.DatabaseDef{Name: "test-db"}},
		SourceFiles: []string{dpgFile},
	}
	return cl, db, t.TempDir()
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestApplyDryRunSkipsExecution verifies that --dry-run prints the plan but
// never calls the executor or saves the snapshot.
func TestApplyDryRunSkipsExecution(t *testing.T) {
	cl, db, migrationsDir := applyTestFixture(t)

	store := &mockStore{}
	exec := &mockApplyExec{}

	err := runApply(cl, db, store,
		&mockDiffer{ops: []pipeline.DiffOp{mockDiffOp{}}},
		&mockEmitter{},
		exec,
		&mockSecretResolver{},
		applyOptions{
			yes:           true,
			dryRun:        true,
			migrationsDir: migrationsDir,
		},
	)
	if err != nil {
		t.Fatalf("runApply --dry-run returned error: %v", err)
	}
	if exec.applyCount != 0 {
		t.Errorf("executor called %d time(s); want 0 with --dry-run", exec.applyCount)
	}
	if store.saveCount != 0 {
		t.Errorf("store.Save called %d time(s); want 0 with --dry-run", store.saveCount)
	}
}

// TestApplyDryRunNoOps verifies that --dry-run on an already-up-to-date
// database (no diff ops) still exits cleanly without executing.
func TestApplyDryRunNoOps(t *testing.T) {
	cl, db, migrationsDir := applyTestFixture(t)

	store := &mockStore{}
	exec := &mockApplyExec{}

	err := runApply(cl, db, store,
		&mockDiffer{ops: nil},
		&mockEmitter{},
		exec,
		&mockSecretResolver{},
		applyOptions{yes: true, dryRun: true, migrationsDir: migrationsDir},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.applyCount != 0 || store.saveCount != 0 {
		t.Errorf("executor or store called unexpectedly (applyCount=%d, saveCount=%d)",
			exec.applyCount, store.saveCount)
	}
}

// TestApplyStrictBlocksOnLintWarnings verifies that --strict causes apply to
// abort before execution when the linter produces warnings.
func TestApplyStrictBlocksOnLintWarnings(t *testing.T) {
	dir := t.TempDir()
	// A deprecated table triggers a WarnOnDeprecated warning from the real linter.
	src := `SCHEMA public {
TABLE legacy_data (
    id BIGINT GENERATED ALWAYS AS IDENTITY,
    CONSTRAINT pk_legacy_data PRIMARY KEY (id)
) {
    DEPRECATED 'replaced by new_data';
}
}`
	dpgFile := filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(dpgFile, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cl := &project.Cluster{
		Dir: dir,
		Config: config.ClusterConfig{
			Cluster: config.ClusterDef{Name: "test-cluster"},
		},
		ObjectsDir: dir,
	}
	db := &project.Database{
		Dir:         dir,
		Config:      config.DatabaseConfig{Database: config.DatabaseDef{Name: "test-db"}},
		SourceFiles: []string{dpgFile},
	}

	store := &mockStore{}
	exec := &mockApplyExec{}

	err := runApply(cl, db, store,
		&mockDiffer{ops: []pipeline.DiffOp{mockDiffOp{}}},
		&mockEmitter{},
		exec,
		&mockSecretResolver{},
		applyOptions{
			yes:           true,
			strict:        true,
			migrationsDir: t.TempDir(),
			lintCfg:       pipeline.LinterConfig{WarnOnDeprecated: true},
		},
	)

	// If the linter is registered, apply must be blocked by --strict.
	if err == nil {
		// Linter not registered (e.g. minimal test build); skip meaningful check.
		if exec.applyCount != 0 {
			t.Errorf("executor called %d time(s) despite strict mode", exec.applyCount)
		}
		return
	}
	if err != ui.ErrSilent {
		t.Errorf("expected ui.ErrSilent, got: %v", err)
	}
	if exec.applyCount != 0 {
		t.Errorf("executor called %d time(s); want 0 when blocked by --strict", exec.applyCount)
	}
}

// TestApplyRejectsScalarConflict proves a scalar property declared with two
// different values in two files (RFC Section 3.7, DPG-E039) stops apply
// before anything is executed, without --strict.
func TestApplyRejectsScalarConflict(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.dpg")
	f2 := filepath.Join(dir, "b.dpg")
	if err := os.WriteFile(f1, []byte(`TABLE users (id BIGINT NOT NULL) { OWNER "alice"; }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte(`TABLE users (email TEXT NOT NULL) { OWNER "bob"; }`), 0o644); err != nil {
		t.Fatal(err)
	}

	cl := &project.Cluster{
		Dir:        dir,
		Config:     config.ClusterConfig{Cluster: config.ClusterDef{Name: "test-cluster"}},
		ObjectsDir: dir,
	}
	db := &project.Database{
		Dir:         dir,
		Config:      config.DatabaseConfig{Database: config.DatabaseDef{Name: "test-db"}},
		SourceFiles: []string{f1, f2},
	}

	store := &mockStore{}
	exec := &mockApplyExec{}

	err := runApply(cl, db, store,
		&mockDiffer{ops: []pipeline.DiffOp{mockDiffOp{}}},
		&mockEmitter{},
		exec,
		&mockSecretResolver{},
		applyOptions{
			yes:           true,
			migrationsDir: t.TempDir(),
		},
	)

	if err == nil || !strings.Contains(err.Error(), "may only be declared with one value") {
		t.Fatalf("expected a DPG-E039 compile error, got: %v", err)
	}
	if exec.applyCount != 0 {
		t.Errorf("executor called %d time(s); want 0 when compilation fails", exec.applyCount)
	}
}

type instrOp struct{ safety pipeline.Safety }

func (o instrOp) SQL() string             { return "-- a step DPG does not perform" }
func (o instrOp) Safety() pipeline.Safety { return o.safety }
func (instrOp) Pos() pipeline.SourcePos   { return pipeline.SourcePos{} }
func (instrOp) Mode() pipeline.ExecMode   { return pipeline.ModeInstructionOnly }

// TestApplyInstructionGate guards RFC Section 17.3: a Destructive
// instruction-only op (a partition strategy rebuild) blocks apply until
// --approve-partition-rebuild is given, while a Safe instruction-only note
// never does. Instruction ops are never sent to the executor.
func TestApplyInstructionGate(t *testing.T) {
	run := func(op pipeline.DiffOp, approve bool) (error, *mockApplyExec) {
		cl, db, migrationsDir := applyTestFixture(t)
		exec := &mockApplyExec{}
		err := runApply(cl, db, &mockStore{},
			&mockDiffer{ops: []pipeline.DiffOp{op}},
			&mockEmitter{},
			exec,
			&mockSecretResolver{},
			applyOptions{yes: true, dryRun: true, migrationsDir: migrationsDir, approvePartitionRebuild: approve},
		)
		return err, exec
	}
	if err, _ := run(instrOp{pipeline.Safe}, false); err != nil {
		t.Errorf("a Safe note must not require --approve-partition-rebuild, got: %v", err)
	}
	if err, _ := run(instrOp{pipeline.Destructive}, false); err == nil || !strings.Contains(err.Error(), "--approve-partition-rebuild") {
		t.Errorf("a Destructive instruction must require --approve-partition-rebuild, got: %v", err)
	}
	if err, exec := run(instrOp{pipeline.Destructive}, true); err != nil || exec.applyCount != 0 {
		t.Errorf("approved instruction: err %v, executor calls %d (want nil, 0)", err, exec.applyCount)
	}
}
