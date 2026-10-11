package analysis

import (
	"testing"

	"github.com/thec1oud/dpg-lsp/internal/workspace"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestCompletionContext_TopLevel(t *testing.T) {
	// No braces open — top-level
	text := "TABLE users (\n    id bigint\n);"
	pos := protocol.Position{Line: 0, Character: 0}
	got := completionContext(text, pos)
	if got != "top_level" {
		t.Errorf("context = %q, want top_level", got)
	}
}

func TestCompletionContext_InsideBraceBlock(t *testing.T) {
	// Cursor is after an open brace — inside DPG block
	text := "TABLE users (id bigint) {\n    "
	pos := protocol.Position{Line: 1, Character: 4}
	got := completionContext(text, pos)
	if got != "block_directive" {
		t.Errorf("context inside block = %q, want block_directive", got)
	}
}

func TestCompletionContext_InsideParenList(t *testing.T) {
	// Cursor is inside an open paren — type completion
	text := "TABLE users (\n    id "
	pos := protocol.Position{Line: 1, Character: 7}
	got := completionContext(text, pos)
	if got != "type" {
		t.Errorf("context inside parens = %q, want type", got)
	}
}

func TestCompletionContext_AfterReferences(t *testing.T) {
	text := "TABLE orders (\n    user_id bigint REFERENCES "
	pos := protocol.Position{Line: 1, Character: 30}
	got := completionContext(text, pos)
	if got != "reference" {
		t.Errorf("context after REFERENCES = %q, want reference", got)
	}
}

func TestCompletion_TopLevel_HasObjectKeywords(t *testing.T) {
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", "")

	items := Completion(ws, "/test/schema.dpg", protocol.Position{Line: 0, Character: 0})

	labels := make(map[string]bool)
	for _, item := range items {
		labels[item.Label] = true
	}

	for _, want := range []string{"TABLE", "VIEW", "FUNCTION", "TYPE", "MACRO", "ROLE", "SCHEMA"} {
		if !labels[want] {
			t.Errorf("top-level completions missing %q", want)
		}
	}
}

func TestCompletion_BlockDirective_HasDirectiveKeywords(t *testing.T) {
	ws := workspace.New()
	// Text with open brace so context = block_directive
	text := "TABLE users (id bigint) {\n    "
	ws.OpenDocument("/test/schema.dpg", text)
	pos := protocol.Position{Line: 1, Character: 4}

	items := Completion(ws, "/test/schema.dpg", pos)

	labels := make(map[string]bool)
	for _, item := range items {
		labels[item.Label] = true
	}
	for _, want := range []string{"COMMENT", "OWNER", "RENAMED FROM", "INDEXES", "GRANTS", "DEPRECATED", "PROTECTED"} {
		if !labels[want] {
			t.Errorf("block completions missing %q", want)
		}
	}
}

func TestCompletion_Type_HasBuiltinTypes(t *testing.T) {
	ws := workspace.New()
	text := "TABLE t (\n    id "
	ws.OpenDocument("/test/schema.dpg", text)
	pos := protocol.Position{Line: 1, Character: 7}

	items := Completion(ws, "/test/schema.dpg", pos)

	labels := make(map[string]bool)
	for _, item := range items {
		labels[item.Label] = true
	}
	for _, want := range []string{"bigint", "text", "uuid", "boolean", "timestamptz", "jsonb"} {
		if !labels[want] {
			t.Errorf("type completions missing builtin %q", want)
		}
	}
}

func TestCompletion_Type_IncludesCustomEnumsAndTypes(t *testing.T) {
	src := "TYPE user_tier AS ENUM ('free', 'pro');\n\nTABLE memberships (\n    tier "
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", src)
	pos := protocol.Position{Line: 3, Character: 9}

	items := Completion(ws, "/test/schema.dpg", pos)

	found := false
	for _, item := range items {
		if item.Label == "user_tier" {
			found = true
			break
		}
	}
	if !found {
		t.Error("type completions should include custom ENUM 'user_tier'")
	}
}

func TestCompletion_Reference_HasTableNames(t *testing.T) {
	src := "TABLE users (id bigint);\n\nTABLE orders (\n    user_id bigint REFERENCES "
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", src)
	pos := protocol.Position{Line: 3, Character: 30}

	items := Completion(ws, "/test/schema.dpg", pos)

	found := false
	for _, item := range items {
		if item.Label == "users" {
			found = true
			break
		}
	}
	if !found {
		t.Error("reference completions should include TABLE 'users'")
	}
}

func TestCompletion_TopLevel_DoesNotHaveBlockDirectives(t *testing.T) {
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", "")

	items := Completion(ws, "/test/schema.dpg", protocol.Position{})

	for _, item := range items {
		if item.Label == "COMMENT" || item.Label == "OWNER" || item.Label == "INDEXES" {
			t.Errorf("top-level completions should not contain block directive %q", item.Label)
		}
	}
}

func TestCompletion_AllItemsHaveKind(t *testing.T) {
	ws := workspace.New()
	ws.OpenDocument("/test/schema.dpg", "")
	items := Completion(ws, "/test/schema.dpg", protocol.Position{})

	for _, item := range items {
		if item.Kind == nil {
			t.Errorf("completion item %q has nil Kind", item.Label)
		}
	}
}
