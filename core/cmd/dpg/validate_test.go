package main

import (
	"encoding/json"
	"github.com/thec1oud/dpg/internal/compiler"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// stubLinter is a Linter that always returns a fixed set of diagnostics.
type stubLinter struct {
	diags []pipeline.LintDiagnostic
}

func (s *stubLinter) Lint(_ []pipeline.IRObject, _ pipeline.LinterConfig) ([]pipeline.LintDiagnostic, error) {
	return s.diags, nil
}

func dpgTempFile(t *testing.T, content string) (file, dir string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "schema.dpg")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file, dir
}

func TestRunValidateStrictOff(t *testing.T) {
	file, dir := dpgTempFile(t, "")
	stub := &stubLinter{diags: []pipeline.LintDiagnostic{
		{Rule: "deprecated", Message: "old table", IsError: false},
	}}

	hasError, err := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, stub, pipeline.LinterConfig{}, "text", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasError {
		t.Error("expected no error when strict=false and only warnings present")
	}
}

func TestRunValidateStrictOn(t *testing.T) {
	file, dir := dpgTempFile(t, "")
	stub := &stubLinter{diags: []pipeline.LintDiagnostic{
		{Rule: "deprecated", Message: "old table", IsError: false},
	}}

	hasError, err := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, stub, pipeline.LinterConfig{}, "text", true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasError {
		t.Error("expected error when strict=true promotes warning to error")
	}
}

func TestRunValidateStrictNoEffect_AlreadyErrors(t *testing.T) {
	// When diagnostics are already errors, strict has no additional effect on outcome.
	file, dir := dpgTempFile(t, "")
	stub := &stubLinter{diags: []pipeline.LintDiagnostic{
		{Rule: "hardcoded-password", Message: "bad password", IsError: true},
	}}

	withoutStrict, _ := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, stub, pipeline.LinterConfig{}, "text", false, nil)
	withStrict, _ := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, stub, pipeline.LinterConfig{}, "text", true, nil)

	if !withoutStrict {
		t.Error("expected error for error-level diagnostic (no strict)")
	}
	if !withStrict {
		t.Error("expected error for error-level diagnostic (with strict)")
	}
}

func TestRunValidateStrictNoErrorsOrWarnings(t *testing.T) {
	file, dir := dpgTempFile(t, "")
	stub := &stubLinter{diags: nil}

	hasError, err := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, stub, pipeline.LinterConfig{}, "text", true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasError {
		t.Error("expected no error when no diagnostics, even with strict=true")
	}
}

// TestRunValidateReportsScalarConflict proves a scalar property declared
// with two different values in two files (RFC Section 3.7) is a DPG-E039
// error in real `dpg validate` JSON output.
func TestRunValidateReportsScalarConflict(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.dpg")
	f2 := filepath.Join(dir, "b.dpg")
	if err := os.WriteFile(f1, []byte(`TABLE users (id BIGINT NOT NULL) { OWNER "alice"; }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte(`TABLE users (email TEXT NOT NULL) { OWNER "bob"; }`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w

	hasErr, err := runValidate("cl", "db", []string{f1, f2}, dir, compiler.Options{}, nil,
		pipeline.LinterConfig{}, "json", false, nil)

	w.Close()
	os.Stdout = orig

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasErr {
		t.Error("expected validate to report an error")
	}

	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, _ := r.Read(buf)
		if n == 0 {
			break
		}
		out.Write(buf[:n])
	}

	var parsed validateJSON
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &parsed); jsonErr != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", jsonErr, out.String())
	}
	var found bool
	for _, e := range parsed.Errors {
		if e.Rule == "DPG-E039" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a DPG-E039 error in output, got: %+v", parsed.Errors)
	}
}

// TestRunValidateMinPGVersion proves the min_pg_version project-gating
// feature is actually wired through the real `dpg validate` CLI path, not
// just internal/linter's own isolated unit tests: real .dpg source
// declaring a PG15+ construct (PARAMETER PRIVILEGES), the real registered
// Linter (pipeline.Resolve, no stub), and a LinterConfig.MinPGVersion set
// the same way newValidateCmd's RunE resolves it per cluster/database
// (project.Database.EffectiveMinPGVersion) before calling runValidate.
func TestRunValidateMinPGVersion(t *testing.T) {
	l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter)
	if !ok {
		t.Fatal("Linter not registered")
	}

	file, dir := dpgTempFile(t, `PARAMETER PRIVILEGES {
	GRANTS { SET ON PARAMETER work_mem TO app_admin; }
}`)

	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w

	_, err := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, l,
		pipeline.LinterConfig{MinPGVersion: 14}, "json", false, nil)

	w.Close()
	os.Stdout = orig

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, _ := r.Read(buf)
		if n == 0 {
			break
		}
		out.Write(buf[:n])
	}

	var parsed validateJSON
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &parsed); jsonErr != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", jsonErr, out.String())
	}
	var found bool
	for _, w := range parsed.Warnings {
		if w.Rule == "min-pg-version" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a min-pg-version warning for PARAMETER PRIVILEGES against a PG14 floor, got: %+v", parsed.Warnings)
	}
}

// TestRunValidateMinPGVersionSatisfiedOK proves the inverse: a floor that
// already meets the construct's requirement produces no warning.
func TestRunValidateMinPGVersionSatisfiedOK(t *testing.T) {
	l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter)
	if !ok {
		t.Fatal("Linter not registered")
	}

	file, dir := dpgTempFile(t, `PARAMETER PRIVILEGES {
	GRANTS { SET ON PARAMETER work_mem TO app_admin; }
}`)

	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w

	_, err := runValidate("cl", "db", []string{file}, dir, compiler.Options{}, l,
		pipeline.LinterConfig{MinPGVersion: 15}, "json", false, nil)

	w.Close()
	os.Stdout = orig

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, _ := r.Read(buf)
		if n == 0 {
			break
		}
		out.Write(buf[:n])
	}

	var parsed validateJSON
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &parsed); jsonErr != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", jsonErr, out.String())
	}
	for _, w := range parsed.Warnings {
		if w.Rule == "min-pg-version" {
			t.Errorf("expected no min-pg-version warning when min_pg_version already satisfies the construct's requirement, got: %+v", parsed.Warnings)
		}
	}
}

func TestRunValidateJSONFormat(t *testing.T) {
	file, dir := dpgTempFile(t, "")
	stub := &stubLinter{diags: []pipeline.LintDiagnostic{
		{Rule: "deprecated", Message: "old", IsError: false},
	}}

	// Capture stdout.
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w

	hasError, err := runValidate("mycluster", "mydb", []string{file}, dir, compiler.Options{}, stub, pipeline.LinterConfig{}, "json", true, nil)

	w.Close()
	os.Stdout = orig

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasError {
		t.Error("expected hasError=true with strict=true and a warning")
	}

	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, _ := r.Read(buf)
		if n == 0 {
			break
		}
		out.Write(buf[:n])
	}

	var parsed validateJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out.String())
	}
	if parsed.Cluster != "mycluster" {
		t.Errorf("cluster = %q, want %q", parsed.Cluster, "mycluster")
	}
	if parsed.Database != "mydb" {
		t.Errorf("database = %q, want %q", parsed.Database, "mydb")
	}
	// strict promotes the warning to an error, so it should appear in Errors.
	if len(parsed.Errors) == 0 {
		t.Error("expected at least one error in JSON output (promoted from warning by strict)")
	}
}
