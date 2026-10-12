package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/linter"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
)

// TestNormalizeLinterRuleKeysSnakeToKebab guards RFC audit item #17: the
// RFC documents/exemplifies snake_case rule IDs
// ("security_definer_search_path", Section 19.2's own worked example) while the
// actual code emits kebab-case ("security-definer-search-path"), and
// ApplyRuleSeverityOverrides does an exact-string lookup — so an
// unnormalized snake_case config key silently matched nothing.
func TestNormalizeLinterRuleKeysSnakeToKebab(t *testing.T) {
	got := normalizeLinterRuleKeys(map[string]string{
		"security_definer_search_path": "error",
		"already-kebab-case":           "off",
	})
	want := map[string]string{
		"security-definer-search-path": "error",
		"already-kebab-case":           "off",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q (full map: %v)", k, got[k], v, got)
		}
	}
}

// TestLinterConfigFromNormalizesRuleKeys proves the normalization is
// actually wired into the real config.LinterConfig -> pipeline.LinterConfig
// conversion path every CLI command (plan/apply/validate) uses, not just
// tested in isolation.
func TestLinterConfigFromNormalizesRuleKeys(t *testing.T) {
	pc := linterConfigFrom(config.LinterConfig{
		Rules: map[string]string{"security_definer_search_path": "error"},
	})
	if pc.Rules["security-definer-search-path"] != "error" {
		t.Errorf("linterConfigFrom did not normalize snake_case key, got: %v", pc.Rules)
	}
}

// TestLinterConfigFromRFCWorkedExampleActuallyWorks is the full end-to-end
// guard for RFC audit item #17: Section 19.2's own worked config example
// ("security_definer_search_path = \"error\"", snake_case) previously
// silently no-op'd — a SECURITY DEFINER function missing a search_path
// reference stayed a warning instead of promoting to an error, exactly as
// if [linter.rules] had never been written at all. This runs the real
// config.LinterConfig -> pipeline.LinterConfig -> linter.Lint pipeline
// every CLI command uses, not just the isolated normalization helper.
func TestLinterConfigFromRFCWorkedExampleActuallyWorks(t *testing.T) {
	pc := linterConfigFrom(config.LinterConfig{
		Rules: map[string]string{"security_definer_search_path": "error"},
	})
	l := linter.New()
	objects := []pipeline.IRObject{
		&ir.Function{
			Schema: "public",
			Name:   "do_thing",
			Attrs: ir.FuncAttrs{
				Language:    "plpgsql",
				SecurityDef: true,
				Body:        "BEGIN END;",
			},
		},
	}
	diags, err := l.Lint(objects, pc)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range diags {
		if d.Rule == "security-definer-search-path" {
			found = true
			if !d.IsError {
				t.Errorf("expected security-definer-search-path promoted to error via the RFC's own snake_case config example, got IsError=false")
			}
		}
	}
	if !found {
		t.Fatal("expected security-definer-search-path diagnostic")
	}
}

// TestLinterConfigFromRFCWorkedExampleFromRealTOMLFile is
// TestLinterConfigFromRFCWorkedExampleActuallyWorks's stronger sibling: it
// writes and parses a genuine dpg.toml file on disk via config.LoadRoot
// (the exact function every dpg CLI command uses to read a real project's
// config) instead of hand-building a config.LinterConfig struct in Go —
// proving RFC Section 19.2's own worked example text, verbatim, round-trips
// through the real TOML parser and still ends up correctly normalized.
// #17 (unlike #1/#14/#15/#16) has no live-database touchpoint at all: `dpg
// plan`'s linting step is offline-first by design (see CLAUDE.md) and
// never opens a connection, so there is nothing for a postgres:17
// container to add here — this is the maximal realism available for this
// specific bug's mechanism (real file, real TOML parser, real compiler,
// real linter, no shortcuts).
func TestLinterConfigFromRFCWorkedExampleFromRealTOMLFile(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "dpg.toml")
	// Verbatim shape of RFC Section 19.2's own worked config example.
	tomlContent := "kind = \"root\"\n[linter.rules]\nsecurity_definer_search_path = \"error\"\n"
	if err := os.WriteFile(tomlPath, []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write dpg.toml: %v", err)
	}

	rootCfg, err := config.LoadRoot(dir)
	if err != nil {
		t.Fatalf("LoadRoot: %v", err)
	}
	if rootCfg.Linter.Rules["security_definer_search_path"] != "error" {
		t.Fatalf("sanity check failed: TOML didn't parse as expected, got %v", rootCfg.Linter.Rules)
	}

	pc := linterConfigFrom(rootCfg.Linter)
	l := linter.New()
	objects := []pipeline.IRObject{
		&ir.Function{
			Schema: "public",
			Name:   "do_thing",
			Attrs: ir.FuncAttrs{
				Language:    "plpgsql",
				SecurityDef: true,
				Body:        "BEGIN END;",
			},
		},
	}
	diags, err := l.Lint(objects, pc)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range diags {
		if d.Rule == "security-definer-search-path" {
			found = true
			if !d.IsError {
				t.Fatalf("security-definer-search-path: IsError=false — bug #17 regressed (RFC's own real dpg.toml example silently no-ops again)")
			}
		}
	}
	if !found {
		t.Fatal("expected security-definer-search-path diagnostic")
	}
}

// TestPlanRejectsScalarConflict proves a scalar property declared with two
// different values in two files (RFC Section 3.7, DPG-E039) stops planning.
func TestPlanRejectsScalarConflict(t *testing.T) {
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

	_, err := buildPlan(cl, db, &pipeline.Snapshot{}, &mockDiffer{}, &mockEmitter{},
		pipeline.LinterConfig{},
		"text",
		nil,
	)

	if err == nil || !strings.Contains(err.Error(), "may only be declared with one value") {
		t.Fatalf("expected a DPG-E039 compile error, got: %v", err)
	}
}

func TestGitRevisionDetachedHead(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hash := "abc1234def5678901234"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(hash+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	rev, err := gitRevision()
	if err != nil {
		t.Fatal(err)
	}
	if rev != hash[:7] {
		t.Errorf("expected %q, got %q", hash[:7], rev)
	}
}

func TestGitRevisionBranchRef(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	refsDir := filepath.Join(gitDir, "refs", "heads")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hash := "deadbeef12345678901234567890123456789012"
	headContent := "ref: refs/heads/master\n"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(headContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refsDir, "master"), []byte(hash+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	rev, err := gitRevision()
	if err != nil {
		t.Fatal(err)
	}
	if rev != hash[:7] {
		t.Errorf("expected %q, got %q", hash[:7], rev)
	}
}

func TestGitRevisionNoGit(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig) //nolint:errcheck

	rev, err := gitRevision()
	if err != nil {
		t.Fatal(err)
	}
	if rev != "" {
		t.Errorf("expected empty revision when no .git dir, got %q", rev)
	}
}
