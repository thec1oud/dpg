package linter

import (
	"fmt"
	"testing"

	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

func TestLintClean(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users"},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diags, got %d", len(diags))
	}
}

func TestLintDeprecatedTable(t *testing.T) {
	l := New()
	reason := "use accounts instead"
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Deprecated: &reason},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{WarnOnDeprecated: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) == 0 {
		t.Fatal("expected deprecated warning")
	}
	if diags[0].Rule != "deprecated" {
		t.Errorf("expected deprecated rule, got %s", diags[0].Rule)
	}
}

func TestLintMaxColumns(t *testing.T) {
	l := New()
	cols := make([]*ir.Column, 5)
	for i := range cols {
		cols[i] = &ir.Column{Name: "c", Type: ir.TypeRef{Name: "text"}}
	}
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "wide", Columns: cols},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MaxColumnsPerTable: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) == 0 {
		t.Fatal("expected column-count-exceeded error")
	}
	if !diags[0].IsError {
		t.Errorf("expected IsError=true")
	}
	if diags[0].Rule != "column-count-exceeded" {
		t.Errorf("expected column-count-exceeded rule, got %s", diags[0].Rule)
	}
}

func TestLintRequireColumnComments(t *testing.T) {
	l := New()
	comment := "the email address"
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "integer"}, Comment: nil},
			{Name: "email", Type: ir.TypeRef{Name: "text"}, Comment: &comment},
		}},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{RequireColumnComments: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "missing-column-comment" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected missing-column-comment warning for id column")
	}
	// email has a comment: no warning for it.
	warnCount := 0
	for _, d := range diags {
		if d.Rule == "missing-column-comment" {
			warnCount++
		}
	}
	if warnCount != 1 {
		t.Errorf("expected 1 missing-column-comment warning, got %d", warnCount)
	}
}

func TestLintHardcodedPassword(t *testing.T) {
	l := New()
	def := "'secret123'"
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Columns: []*ir.Column{
			{Name: "password_hash", Type: ir.TypeRef{Name: "text"}, Default: &def},
		}},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "hardcoded-password" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected hardcoded-password error")
	}
}

func TestLintHardcodedRolePassword(t *testing.T) {
	l := New()
	pw := "hunter2"
	objects := []pipeline.IRObject{&ir.Role{Name: "app_service", Password: &pw}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "hardcoded-role-password" {
			found = true
			if !d.IsError {
				t.Error("expected hardcoded ROLE PASSWORD to be an error, not a warning (RFC Section 11.1 MUST)")
			}
		}
	}
	if !found {
		t.Fatal("expected hardcoded-role-password error for a literal ROLE PASSWORD")
	}
}

func TestLintRolePasswordWithSecretReferenceOK(t *testing.T) {
	l := New()
	pw := "{{vault:secret/roles/app_service#pw}}"
	objects := []pipeline.IRObject{&ir.Role{Name: "app_service", Password: &pw}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "hardcoded-role-password" {
			t.Errorf("did not expect hardcoded-role-password for a {{...}} secret reference, got: %v", d)
		}
	}
}

func TestLintRolePasswordRuleDisabled(t *testing.T) {
	l := New()
	pw := "hunter2"
	objects := []pipeline.IRObject{&ir.Role{Name: "app_service", Password: &pw}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "hardcoded-role-password" {
			t.Errorf("did not expect hardcoded-role-password with the rule disabled, got: %v", d)
		}
	}
}

func TestLintHardcodedFDWPassword(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{&ir.UserMapping{
		User: "app", Server: "srv",
		Options: []pipeline.StorageParam{{Key: "user", Value: "app"}, {Key: "password", Value: "hunter2"}},
		Body:    "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password 'hunter2')",
	}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "hardcoded-fdw-password" {
			found = true
			if !d.IsError {
				t.Error("expected hardcoded FDW password to be an error (RFC Section 19.1 hardcoded_fdw_password)")
			}
		}
	}
	if !found {
		t.Fatal("expected hardcoded-fdw-password error for a literal OPTIONS password")
	}
}

func TestLintFDWPasswordWithSecretReferenceOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{&ir.UserMapping{
		User: "app", Server: "srv",
		Options: []pipeline.StorageParam{{Key: "user", Value: "app"}, {Key: "password", Value: "{{vault:secret/fdw/srv#pw}}"}},
		Body:    "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password '{{vault:secret/fdw/srv#pw}}')",
	}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "hardcoded-fdw-password" {
			t.Errorf("did not expect hardcoded-fdw-password for a {{...}} secret reference, got: %v", d)
		}
	}
}

func TestLintFDWPasswordRuleDisabled(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{&ir.UserMapping{
		User: "app", Server: "srv",
		Options: []pipeline.StorageParam{{Key: "password", Value: "hunter2"}},
		Body:    "CREATE USER MAPPING FOR app SERVER srv OPTIONS (password 'hunter2')",
	}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "hardcoded-fdw-password" {
			t.Errorf("did not expect hardcoded-fdw-password with the rule disabled, got: %v", d)
		}
	}
}

// TestLintFDWPasswordCatchesDumpedRedactionPlaceholder proves the
// composability `dpg dump`'s UserMapping password redaction relies on: a
// dumped mapping still carries the placeholder as a literal `password '...'`
// value (not a {{secret-uri}} reference), so re-planning/re-applying the
// dumped file unmodified must still hard-error via hardcoded-fdw-password —
// the redaction alone isn't enough, the user must be forced to replace it.
func TestLintFDWPasswordCatchesDumpedRedactionPlaceholder(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{&ir.UserMapping{
		User: "app", Server: "srv",
		Options: []pipeline.StorageParam{{Key: "user", Value: "app"}, {Key: "password", Value: introspect.UserMappingRedactedPlaceholder}},
		Body: fmt.Sprintf("CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app', password %s)",
			"'"+introspect.UserMappingRedactedPlaceholder+"'"),
	}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "hardcoded-fdw-password" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected hardcoded-fdw-password error for a dumped-but-unreplaced redaction placeholder")
	}
}

// TestLintFDWHardcodedPasswordFullKeyListParity guards the fresh-audit
// finding that hardcoded-fdw-password previously matched only the literal
// key "password", while dump's UserMapping-OPTIONS redaction (and the
// column-level hardcoded-password rule) use the broader 5-key list
// (password/passwd/pwd/secret/passphrase) — the RFC's Section 24 prose
// claims parity between them, so a literal value under any of the other 4
// keys must now be caught too, not just "password".
func TestLintFDWHardcodedPasswordFullKeyListParity(t *testing.T) {
	l := New()
	for _, key := range []string{"passwd", "pwd", "secret", "passphrase"} {
		t.Run(key, func(t *testing.T) {
			objects := []pipeline.IRObject{&ir.UserMapping{
				User: "app", Server: "srv",
				Options: []pipeline.StorageParam{{Key: key, Value: "hunter2"}},
				Body:    fmt.Sprintf("CREATE USER MAPPING FOR app SERVER srv OPTIONS (%s 'hunter2')", key),
			}}
			diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range diags {
				if d.Rule == "hardcoded-fdw-password" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected hardcoded-fdw-password for a literal value under key %q, got: %v", key, diags)
			}
		})
	}
}

func TestLintUserMappingNoPasswordOptionOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{&ir.UserMapping{
		User: "app", Server: "srv",
		Options: []pipeline.StorageParam{{Key: "user", Value: "app"}},
		Body:    "CREATE USER MAPPING FOR app SERVER srv OPTIONS (user 'app')",
	}}
	diags, err := l.Lint(objects, pipeline.LinterConfig{ForbidHardcodedPasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "hardcoded-fdw-password" {
			t.Errorf("did not expect hardcoded-fdw-password for a mapping with no password option, got: %v", d)
		}
	}
}

func TestLintSecurityDefiner(t *testing.T) {
	l := New()
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
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "security-definer-search-path" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected security-definer-search-path warning")
	}
}

// TestLintSecurityDefinerWithSearchPathSuppressesWarning proves the rule
// checks the real Section 9.2 Config list, not a substring scan of the
// function body — the prior implementation only ever matched by
// coincidence, since a SET search_path clause is a structural attribute
// that never appears in Body's text.
func TestLintSecurityDefinerWithSearchPathSuppressesWarning(t *testing.T) {
	l := New()
	val := "public"
	objects := []pipeline.IRObject{
		&ir.Function{
			Schema: "public",
			Name:   "do_thing",
			Attrs: ir.FuncAttrs{
				Language:    "plpgsql",
				SecurityDef: true,
				Body:        "BEGIN END;",
				Config:      []ir.FuncConfig{{Param: "search_path", Value: &val}},
			},
		},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "security-definer-search-path" {
			t.Fatal("expected no security-definer-search-path warning when search_path is declared via Config")
		}
	}
}

// TestLintSecurityDefinerProcedure proves checkProcedure now applies the
// same SECURITY DEFINER + search_path guard as checkFunction — previously
// missing entirely, since Procedure's SecurityDef was never wired through
// introspection/rendering/diffing at all (see ir.RenderCreateProcedureSQL).
func TestLintSecurityDefinerProcedure(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Procedure{
			Schema: "public",
			Name:   "do_thing",
			Attrs: ir.FuncAttrs{
				Language:    "plpgsql",
				SecurityDef: true,
				Body:        "BEGIN END;",
			},
		},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "security-definer-search-path" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected security-definer-search-path warning for a SECURITY DEFINER procedure with no search_path")
	}
}

func TestLintSerialSequenceDeclared(t *testing.T) {
	l := New()
	trueVal := true
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "integer"}, Identity: &ir.Identity{Always: trueVal}},
		}},
		&ir.Sequence{Schema: "public", Name: "users_id_seq"},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "serial-sequence-declared" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected serial-sequence-declared warning")
	}
}

func TestLintSerialSequenceDeclaredNoCollisionOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "integer"}},
		}},
		&ir.Sequence{Schema: "public", Name: "invoice_numbers"},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "serial-sequence-declared" {
			t.Errorf("did not expect serial-sequence-declared warning, got: %s", d.Message)
		}
	}
}

// TestLintSerialSequenceDeclaredCoversSerial extends the identity-only
// serial_sequence_declared rule to SERIAL columns too, now that SERIAL has
// a distinct IR representation (ir.Column.Serial) — this was flagged as
// out of scope for the rule until now.
func TestLintSerialSequenceDeclaredCoversSerial(t *testing.T) {
	l := New()
	marker := "SERIAL"
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Columns: []*ir.Column{
			{Name: "id", Type: ir.TypeRef{Name: "integer"}, Serial: &marker, NotNull: true},
		}},
		&ir.Sequence{Schema: "public", Name: "users_id_seq"},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "serial-sequence-declared" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected serial-sequence-declared warning for a SERIAL column")
	}
}

func TestLintUnnecessaryRevocation(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Grants:      []ir.Grant{{Privileges: []string{"SELECT"}, Roles: []string{"app_readonly"}}},
			Revocations: []ir.Revocation{{Privileges: []string{"DELETE"}, Roles: []string{"app_readonly"}}},
		},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range diags {
		if d.Rule == "unnecessary-revocation" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected unnecessary-revocation warning")
	}
}

func TestLintUnnecessaryRevocationMatchingGrantOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Grants:      []ir.Grant{{Privileges: []string{"SELECT", "DELETE"}, Roles: []string{"app_readonly"}}},
			Revocations: []ir.Revocation{{Privileges: []string{"DELETE"}, Roles: []string{"app_readonly"}}},
		},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "unnecessary-revocation" {
			t.Errorf("did not expect unnecessary-revocation warning, got: %s", d.Message)
		}
	}
}

func TestLintUnnecessaryRevocationAllPrivilegeGrantOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{
			Schema: "public", Name: "orders",
			Grants:      []ir.Grant{{Roles: []string{"app_readonly"}}}, // nil Privileges = ALL
			Revocations: []ir.Revocation{{Privileges: []string{"DELETE"}, Roles: []string{"app_readonly"}}},
		},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		if d.Rule == "unnecessary-revocation" {
			t.Errorf("did not expect unnecessary-revocation warning for a role granted ALL, got: %s", d.Message)
		}
	}
}

func TestLintRuleSeverityOverrideError(t *testing.T) {
	l := New()
	reason := "use accounts instead"
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Deprecated: &reason},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{
		WarnOnDeprecated: true,
		Rules:            map[string]string{"deprecated": "error"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) == 0 {
		t.Fatal("expected a deprecated diagnostic")
	}
	if !diags[0].IsError {
		t.Error("expected [linter.rules] override to promote deprecated to an error")
	}
}

func TestLintRuleSeverityOverrideOff(t *testing.T) {
	l := New()
	reason := "use accounts instead"
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "users", Deprecated: &reason},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{
		WarnOnDeprecated: true,
		Rules:            map[string]string{"deprecated": "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected [linter.rules] \"off\" to suppress the diagnostic entirely, got %d", len(diags))
	}
}

func TestLintRuleSeverityOverrideUnrelatedRuleUnaffected(t *testing.T) {
	l := New()
	cols := make([]*ir.Column, 5)
	for i := range cols {
		cols[i] = &ir.Column{Name: "c", Type: ir.TypeRef{Name: "text"}}
	}
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "wide", Columns: cols},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{
		MaxColumnsPerTable: 3,
		Rules:              map[string]string{"deprecated": "off"}, // unrelated rule
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) == 0 || diags[0].Rule != "column-count-exceeded" || !diags[0].IsError {
		t.Errorf("expected column-count-exceeded to be unaffected by an override for a different rule, got %+v", diags)
	}
}

// ── FilterMergeDiagnostics (scalar-merge-conflict gating) ──────────────────────

func TestFilterMergeDiagnosticsGatingDisabled(t *testing.T) {
	mergeDiags := []pipeline.LintDiagnostic{
		{Rule: "scalar-merge-conflict", Message: "table public.t: owner conflicts"},
	}
	got := FilterMergeDiagnostics(mergeDiags, pipeline.LinterConfig{WarnOnScalarMergeConflict: false})
	if len(got) != 0 {
		t.Errorf("WarnOnScalarMergeConflict=false should drop all merge diagnostics, got %v", got)
	}
}

func TestFilterMergeDiagnosticsGatingEnabled(t *testing.T) {
	mergeDiags := []pipeline.LintDiagnostic{
		{Rule: "scalar-merge-conflict", Message: "table public.t: owner conflicts"},
	}
	got := FilterMergeDiagnostics(mergeDiags, pipeline.LinterConfig{WarnOnScalarMergeConflict: true})
	if len(got) != 1 {
		t.Errorf("WarnOnScalarMergeConflict=true should pass merge diagnostics through, got %v", got)
	}
}

func TestFilterMergeDiagnosticsRuleSeverityOverrideOff(t *testing.T) {
	mergeDiags := []pipeline.LintDiagnostic{
		{Rule: "scalar-merge-conflict", Message: "table public.t: owner conflicts"},
	}
	got := FilterMergeDiagnostics(mergeDiags, pipeline.LinterConfig{
		WarnOnScalarMergeConflict: true,
		Rules:                     map[string]string{"scalar-merge-conflict": "off"},
	})
	if len(got) != 0 {
		t.Errorf("[linter.rules] \"off\" should independently suppress scalar-merge-conflict, got %v", got)
	}
}

func TestFilterMergeDiagnosticsRuleSeverityOverrideError(t *testing.T) {
	mergeDiags := []pipeline.LintDiagnostic{
		{Rule: "scalar-merge-conflict", Message: "table public.t: owner conflicts"},
	}
	got := FilterMergeDiagnostics(mergeDiags, pipeline.LinterConfig{
		WarnOnScalarMergeConflict: true,
		Rules:                     map[string]string{"scalar-merge-conflict": "error"},
	})
	if len(got) != 1 || !got[0].IsError {
		t.Errorf("[linter.rules] \"error\" should promote scalar-merge-conflict, got %v", got)
	}
}

func TestFilterMergeDiagnosticsGatingDisabledDoesNotDropOtherRules(t *testing.T) {
	mergeDiags := []pipeline.LintDiagnostic{
		{Rule: "scalar-merge-conflict", Message: "table public.t: owner conflicts"},
		{Rule: "duplicate-namemap-tool", Message: "DPG-E031: tool key \"prisma\" specified more than once"},
	}
	got := FilterMergeDiagnostics(mergeDiags, pipeline.LinterConfig{WarnOnScalarMergeConflict: false})
	if len(got) != 1 || got[0].Rule != "duplicate-namemap-tool" {
		t.Errorf("WarnOnScalarMergeConflict=false should only drop scalar-merge-conflict, got %v", got)
	}
}

func TestLintRegistration(t *testing.T) {
	l, ok := pipeline.Resolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter)
	if !ok {
		t.Fatal("Linter not registered")
	}
	if l == nil {
		t.Fatal("registered Linter is nil")
	}
}

// ── min-pg-version ────────────────────────────────────────────────────────────

func TestLintMinPGVersionDisabledByDefault(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", RefreshVersion: true},
	}
	// MinPGVersion left at its zero value (0) — no floor configured anywhere.
	diags, err := l.Lint(objects, pipeline.LinterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diags when MinPGVersion is unset (0), got %v", diags)
	}
}

func TestLintMinPGVersionCollationRefreshVersion(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", RefreshVersion: true},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Rule != "min-pg-version" {
		t.Fatalf("expected 1 min-pg-version diag, got %v", diags)
	}
}

func TestLintMinPGVersionCollationRefreshVersionSatisfiedOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", RefreshVersion: true},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 15})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diags when the floor already satisfies REFRESH VERSION's PG15 requirement, got %v", diags)
	}
}

// TestLintMinPGVersionCollationRules guards the RULES (ICU tailoring)
// half of Collation's two independently-gated properties — REFRESH
// VERSION is PG15, RULES is PG16, and a floor of 15 must catch RULES while
// leaving REFRESH VERSION alone.
func TestLintMinPGVersionCollationRules(t *testing.T) {
	l := New()
	rules := "&a < b"
	objects := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", Rules: &rules},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 15})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Rule != "min-pg-version" {
		t.Fatalf("expected 1 min-pg-version diag for RULES against a PG15 floor, got %v", diags)
	}
}

func TestLintMinPGVersionParameterPrivileges(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.ParameterPrivileges{
			Grants: []ir.ParameterGrant{{Privileges: []string{"SET"}, Parameters: []string{"work_mem"}, Roles: []string{"app_admin"}}},
		},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Rule != "min-pg-version" {
		t.Fatalf("expected 1 min-pg-version diag for PARAMETER PRIVILEGES against a PG14 floor, got %v", diags)
	}
}

func TestLintMinPGVersionEventTriggerLogin(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.EventTrigger{Name: "audit_login", Event: "login", Function: "public.f"},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Rule != "min-pg-version" {
		t.Fatalf("expected 1 min-pg-version diag for the login event trigger against a PG16 floor, got %v", diags)
	}
}

// TestLintMinPGVersionEventTriggerNonLoginOK guards that only the "login"
// event value is gated — every other event trigger event name (ddl_command_start
// etc.) is standard across every supported PG version.
func TestLintMinPGVersionEventTriggerNonLoginOK(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.EventTrigger{Name: "audit_ddl", Event: "ddl_command_start", Function: "public.f"},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diags for a non-login event trigger, got %v", diags)
	}
}

func TestLintMinPGVersionTableMaintainPrivilege(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", Grants: []ir.Grant{
			{Privileges: []string{"MAINTAIN"}, Roles: []string{"app_admin"}},
		}},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Rule != "min-pg-version" {
		t.Fatalf("expected 1 min-pg-version diag for MAINTAIN against a PG16 floor, got %v", diags)
	}
}

// TestLintMinPGVersionTableMaintainPrivilegeViaRevocation guards that the
// check also scans REVOKES, not just GRANTS. The fixture's revocation
// has no matching grant, so unnecessary-revocation also legitimately fires
// alongside it (an unrelated, correct rule) — this only asserts
// min-pg-version is among the diagnostics, not that it's the sole one.
func TestLintMinPGVersionTableMaintainPrivilegeViaRevocation(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Table{Schema: "public", Name: "t", Revocations: []ir.Revocation{
			{Privileges: []string{"maintain"}, Roles: []string{"PUBLIC"}},
		}},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 15})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range diags {
		if d.Rule == "min-pg-version" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a min-pg-version diag (case-insensitive MAINTAIN match via revocation), got %v", diags)
	}
}

// TestLintMinPGVersionStrictPromotesToError guards that --strict's existing
// blanket IsError-promotion loop (cmd/dpg, applied to every diagnostic
// uniformly) covers min-pg-version like any other rule — no special-casing
// needed in the linter itself, since that promotion happens at the CLI
// layer, not here. This test documents the expectation at the LintDiagnostic
// level: IsError is false by default (warning), matching every other
// non-hardcoded-credential rule's convention.
func TestLintMinPGVersionStrictPromotesToError(t *testing.T) {
	l := New()
	objects := []pipeline.IRObject{
		&ir.Collation{Schema: "public", Name: "c", RefreshVersion: true},
	}
	diags, err := l.Lint(objects, pipeline.LinterConfig{MinPGVersion: 14})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diag, got %d", len(diags))
	}
	if diags[0].IsError {
		t.Error("min-pg-version should be a warning (IsError=false) by default — --strict promotion happens at the CLI layer")
	}
}
