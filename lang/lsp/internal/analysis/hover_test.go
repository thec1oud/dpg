package analysis

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg-lsp/internal/workspace"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// hoverText asserts Contents is a MarkupContent and returns its Value.
func hoverText(t *testing.T, h *protocol.Hover) string {
	t.Helper()
	mc, ok := h.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("Contents is %T, want protocol.MarkupContent", h.Contents)
	}
	return mc.Value
}

func TestWordAtPosition_MiddleOfWord(t *testing.T) {
	text := "TABLE users (id bigint);"
	pos := protocol.Position{Line: 0, Character: 8} // cursor inside "users"
	got := wordAtPosition(text, pos)
	if got != "users" {
		t.Errorf("wordAtPosition = %q, want 'users'", got)
	}
}

func TestWordAtPosition_StartOfWord(t *testing.T) {
	text := "TABLE users (id bigint);"
	pos := protocol.Position{Line: 0, Character: 6}
	got := wordAtPosition(text, pos)
	if got != "users" {
		t.Errorf("wordAtPosition at start = %q, want 'users'", got)
	}
}

func TestWordAtPosition_Whitespace(t *testing.T) {
	text := "TABLE users (id bigint);"
	pos := protocol.Position{Line: 0, Character: 5} // space between TABLE and users
	got := wordAtPosition(text, pos)
	if got != "" {
		t.Errorf("wordAtPosition on space = %q, want empty", got)
	}
}

func TestWordAtPosition_MultiLine(t *testing.T) {
	text := "-- comment\nTABLE orders ("
	pos := protocol.Position{Line: 1, Character: 7} // inside "orders"
	got := wordAtPosition(text, pos)
	if got != "orders" {
		t.Errorf("wordAtPosition multiline = %q, want 'orders'", got)
	}
}

func TestWordAtPosition_LineOutOfRange(t *testing.T) {
	text := "TABLE t (id bigint);"
	pos := protocol.Position{Line: 99, Character: 0}
	got := wordAtPosition(text, pos)
	if got != "" {
		t.Errorf("out-of-range line = %q, want empty", got)
	}
}

func TestWordAtPosition_EndOfLine(t *testing.T) {
	text := "TABLE users"
	pos := protocol.Position{Line: 0, Character: 11} // past end
	got := wordAtPosition(text, pos)
	if got != "users" {
		t.Errorf("at end of word = %q, want 'users'", got)
	}
}

func TestHover_ObjectFound(t *testing.T) {
	src := "TABLE actor_type (\n    id integer\n);\n"
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", src)

	// Hover over "actor_type" on line 0, char 6
	pos := protocol.Position{Line: 0, Character: 8}
	got := Hover(ws, "/test/schema.dpg", pos)

	if got == nil {
		t.Fatal("Hover returned nil, want a hover response")
	}
	val := hoverText(t, got)
	if !strings.Contains(val, "TABLE") {
		t.Errorf("hover value = %q, should contain TABLE", val)
	}
	if !strings.Contains(val, "actor_type") {
		t.Errorf("hover value = %q, should contain actor_type", val)
	}
}

func TestHover_SchemaQualifiedName(t *testing.T) {
	src := "TABLE iam.roles (\n    id bigint\n);\n"
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", src)

	pos := protocol.Position{Line: 0, Character: 10} // inside "roles"
	got := Hover(ws, "/test/schema.dpg", pos)

	if got == nil {
		t.Fatal("Hover returned nil for schema-qualified name")
	}
	val := hoverText(t, got)
	if !strings.Contains(val, "iam.roles") {
		t.Errorf("hover value = %q, should contain iam.roles", val)
	}
}

func TestHover_KeywordDoc(t *testing.T) {
	src := "INDEXES { idx_name (col); }"
	ws := workspace.New()
	ws.OpenDocument("/test/block.dpg", src)

	pos := protocol.Position{Line: 0, Character: 3} // inside "INDEXES"
	got := Hover(ws, "/test/block.dpg", pos)

	if got == nil {
		t.Fatal("Hover returned nil for keyword INDEXES")
	}
	if hoverText(t, got) == "" {
		t.Error("Hover for INDEXES should return non-empty documentation")
	}
}

func TestHover_Nil_OnWhitespace(t *testing.T) {
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", "TABLE users (id bigint);")

	pos := protocol.Position{Line: 0, Character: 5} // space
	got := Hover(ws, "/test/schema.dpg", pos)

	if got != nil {
		t.Errorf("Hover on whitespace = %+v, want nil", got)
	}
}

func TestHover_EmptyDoc_ReturnsNil(t *testing.T) {
	ws := workspace.New()
	got := Hover(ws, "/nonexistent/schema.dpg", protocol.Position{})
	if got != nil {
		t.Errorf("Hover on empty/missing doc = %+v, want nil", got)
	}
}

func TestKeywordDoc_KnownKeywords(t *testing.T) {
	for _, kw := range []string{"TABLE", "VIEW", "FUNCTION", "ENUM", "MACRO", "RENAMED", "DEPRECATED", "PROTECTED"} {
		if doc := keywordDoc(kw); doc == "" {
			t.Errorf("keywordDoc(%q) = empty, want documentation", kw)
		}
	}
}

func TestKeywordDoc_UnknownKeyword(t *testing.T) {
	if doc := keywordDoc("UNKNOWNKEYWORD"); doc != "" {
		t.Errorf("keywordDoc(unknown) = %q, want empty", doc)
	}
}

func TestKeywordDoc_CaseInsensitive(t *testing.T) {
	lower := keywordDoc("table")
	upper := keywordDoc("TABLE")
	if lower != upper {
		t.Errorf("keywordDoc is not case-insensitive: lower=%q upper=%q", lower, upper)
	}
}
