package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	ws := New()
	if ws.docs == nil {
		t.Fatal("docs map not initialised")
	}
	if ws.diagnostics == nil {
		t.Fatal("diagnostics map not initialised")
	}
}

func TestSetAndGetRoot(t *testing.T) {
	ws := New()
	ws.SetRoot("/some/project")
	if got := ws.Root(); got != "/some/project" {
		t.Fatalf("Root() = %q, want %q", got, "/some/project")
	}
}

func TestOpenDocument_StoresText(t *testing.T) {
	ws := New()
	// Prevent the debounced validate from firing during the test.
	ws.SetRoot("")

	ws.OpenDocument("/fake/schema.dpg", "TABLE users (id bigint);")

	got := ws.GetText("/fake/schema.dpg")
	if got != "TABLE users (id bigint);" {
		t.Fatalf("GetText = %q, want original text", got)
	}
}

func TestUpdateDocument_ReplacesText(t *testing.T) {
	ws := New()
	ws.OpenDocument("/fake/schema.dpg", "TABLE users (id bigint);")
	ws.UpdateDocument("/fake/schema.dpg", "TABLE accounts (id bigint);")

	got := ws.GetText("/fake/schema.dpg")
	if got != "TABLE accounts (id bigint);" {
		t.Fatalf("GetText after update = %q, want updated text", got)
	}
}

func TestUpdateDocument_CreatesIfMissing(t *testing.T) {
	ws := New()
	ws.UpdateDocument("/fake/new.dpg", "TYPE status AS ENUM ('a');")

	got := ws.GetText("/fake/new.dpg")
	if got != "TYPE status AS ENUM ('a');" {
		t.Fatalf("GetText for new doc = %q", got)
	}
}

func TestCloseDocument_RemovesText(t *testing.T) {
	ws := New()
	ws.OpenDocument("/fake/schema.dpg", "TABLE t (id bigint);")
	ws.CloseDocument("/fake/schema.dpg")

	// After closing, GetText should return empty (no in-memory doc, no disk file).
	got := ws.GetText("/fake/schema.dpg")
	if got != "" {
		t.Fatalf("GetText after close = %q, want empty", got)
	}
}

func TestCloseDocument_ClearsDiagnostics(t *testing.T) {
	ws := New()
	ws.OpenDocument("/fake/schema.dpg", "")
	ws.SetDiagnostics("/fake/schema.dpg", []Diagnostic{{Message: "err", IsError: true}})
	ws.CloseDocument("/fake/schema.dpg")

	diags := ws.GetDiagnostics("/fake/schema.dpg")
	if len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics after close, got %d", len(diags))
	}
}

func TestSetAndGetDiagnostics(t *testing.T) {
	ws := New()
	input := []Diagnostic{
		{Rule: "DPG-E006", Message: "forbidden verb", IsError: true},
		{Rule: "deprecated", Message: "column is deprecated", IsError: false},
	}
	ws.SetDiagnostics("/fake/schema.dpg", input)

	got := ws.GetDiagnostics("/fake/schema.dpg")
	if len(got) != 2 {
		t.Fatalf("GetDiagnostics len = %d, want 2", len(got))
	}
	if got[0].Rule != "DPG-E006" {
		t.Errorf("got[0].Rule = %q, want DPG-E006", got[0].Rule)
	}
	if got[1].IsError {
		t.Errorf("got[1].IsError = true, want false")
	}
}

func TestSetDiagnostics_InvokesOnChange(t *testing.T) {
	ws := New()
	called := make(chan string, 1)
	ws.SetOnChange(func(path string) { called <- path })

	ws.SetDiagnostics("/fake/schema.dpg", nil)

	select {
	case p := <-called:
		if p != "/fake/schema.dpg" {
			t.Errorf("onChange called with %q, want /fake/schema.dpg", p)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("onChange not called within 100ms")
	}
}

func TestGetText_FallsBackToDisk(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "schema.dpg")
	content := "TABLE disk_table (id bigint);"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := New()
	got := ws.GetText(path)
	if got != content {
		t.Fatalf("GetText from disk = %q, want %q", got, content)
	}
}

func TestParseObjects_BasicTypes(t *testing.T) {
	src := `TABLE users (id bigint);
VIEW active_users AS SELECT * FROM users;
FUNCTION get_user() RETURNS text LANGUAGE sql AS $$ SELECT 'x' $$;
TYPE status AS ENUM ('a', 'b');
`
	objs := ParseObjects(src, "test.dpg")

	if len(objs) != 4 {
		t.Fatalf("ParseObjects returned %d objects, want 4", len(objs))
	}

	tests := []struct{ kind, name string }{
		{"TABLE", "users"},
		{"VIEW", "active_users"},
		{"FUNCTION", "get_user"},
		{"ENUM", "status"},
	}
	for i, tt := range tests {
		if objs[i].Kind != tt.kind {
			t.Errorf("objs[%d].Kind = %q, want %q", i, objs[i].Kind, tt.kind)
		}
		if objs[i].Name != tt.name {
			t.Errorf("objs[%d].Name = %q, want %q", i, objs[i].Name, tt.name)
		}
	}
}

func TestParseObjects_SchemaQualifiedName(t *testing.T) {
	src := "TABLE iam.identities (id bigint);\n"
	objs := ParseObjects(src, "test.dpg")

	if len(objs) != 1 {
		t.Fatalf("len = %d, want 1", len(objs))
	}
	if objs[0].Name != "iam.identities" {
		t.Errorf("Name = %q, want iam.identities", objs[0].Name)
	}
}

func TestParseObjects_LineNumbers(t *testing.T) {
	src := "-- comment\nTABLE foo (id bigint);\nVIEW bar AS SELECT 1;\n"
	objs := ParseObjects(src, "test.dpg")

	if len(objs) != 2 {
		t.Fatalf("len = %d, want 2", len(objs))
	}
	if objs[0].Line != 2 {
		t.Errorf("TABLE line = %d, want 2", objs[0].Line)
	}
	if objs[1].Line != 3 {
		t.Errorf("VIEW line = %d, want 3", objs[1].Line)
	}
}

func TestParseObjects_Prefixed(t *testing.T) {
	src := `UNLOGGED TABLE fast (id bigint);
MATERIALIZED VIEW mv AS SELECT 1;
`
	objs := ParseObjects(src, "test.dpg")
	if len(objs) != 2 {
		t.Fatalf("len = %d, want 2", len(objs))
	}
	if objs[0].Kind != "UNLOGGED TABLE" {
		t.Errorf("Kind = %q, want UNLOGGED TABLE", objs[0].Kind)
	}
	if objs[1].Kind != "MATERIALIZED VIEW" {
		t.Errorf("Kind = %q, want MATERIALIZED VIEW", objs[1].Kind)
	}
}

func TestParseObjects_IgnoresCommentLines(t *testing.T) {
	src := "-- TABLE not_a_table\nTABLE real_table (id bigint);\n"
	objs := ParseObjects(src, "test.dpg")
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}
	if objs[0].Name != "real_table" {
		t.Errorf("Name = %q, want real_table", objs[0].Name)
	}
}

func TestParseObjects_FilePath(t *testing.T) {
	src := "TABLE t (id bigint);\n"
	objs := ParseObjects(src, "/my/project/schemas/t.dpg")
	if objs[0].File != "/my/project/schemas/t.dpg" {
		t.Errorf("File = %q, want the provided file path", objs[0].File)
	}
}

func TestWriteTempFile(t *testing.T) {
	path, err := writeTempFile("schema.dpg", "TABLE x (id bigint);")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "TABLE x (id bigint);" {
		t.Errorf("temp file content = %q, want original text", string(data))
	}
	if filepath.Ext(path) != ".dpg" {
		t.Errorf("temp file ext = %q, want .dpg", filepath.Ext(path))
	}
}
