package analysis

import (
	"strings"

	"github.com/thec1oud/dpg-lsp/internal/workspace"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

var topLevelKeywords = []string{
	"TABLE", "UNLOGGED TABLE", "FOREIGN TABLE",
	"VIEW", "MATERIALIZED VIEW", "RECURSIVE VIEW",
	"FUNCTION", "PROCEDURE", "AGGREGATE",
	"TYPE", "DOMAIN", "VIRTUAL TYPE",
	"SEQUENCE", "ROLE", "TABLESPACE",
	"SCHEMA", "EXTENSION",
	"PUBLICATION", "SUBSCRIPTION",
	"EVENT TRIGGER",
	"DEFAULT PRIVILEGES",
	"FOREIGN DATA WRAPPER", "SERVER", "USER MAPPING",
	"TEXT SEARCH CONFIGURATION", "TEXT SEARCH DICTIONARY",
	"TEXT SEARCH PARSER", "TEXT SEARCH TEMPLATE",
	"COLLATION", "OPERATOR", "OPERATOR CLASS", "OPERATOR FAMILY",
	"CAST", "STATISTICS",
	"MACRO",
}

var blockDirectiveKeywords = []string{
	"COMMENT", "OWNER",
	"RENAMED FROM", "PROTECTED", "DEPRECATED", "DROP CASCADE",
	"INDEXES", "POLICIES", "TRIGGERS", "COLUMNS", "ATTRIBUTES", "CONSTRAINTS",
	"ATTRIBUTE", "VALUE",
	"GRANTS", "REVOKES", "PARTITIONS",
	"MIGRATE REMOVE",
	"PREFERRED JSON FORMAT",
	// OPERATOR FAMILY { } loose members only (RFC §14.4).
	"OPERATOR", "FUNCTION",
}

var pgBuiltinTypes = []string{
	"bigint", "bigserial", "boolean", "bytea", "char", "character",
	"date", "decimal", "double precision", "float4", "float8",
	"inet", "int", "int2", "int4", "int8", "integer", "interval",
	"json", "jsonb", "money", "numeric", "oid",
	"real", "serial", "serial2", "serial4", "serial8", "smallint", "smallserial",
	"text", "time", "timestamp", "timestamptz", "timetz",
	"uuid", "varchar", "xml",
}

// Completion returns context-sensitive completions for the given position.
func Completion(ws *workspace.Workspace, path string, pos protocol.Position) []protocol.CompletionItem {
	text := ws.GetText(path)
	ctx := completionContext(text, pos)

	switch ctx {
	case "block_directive":
		return keywordCompletions(blockDirectiveKeywords)
	case "type":
		return typeCompletions(ws, path, text)
	case "reference":
		return tableCompletions(ws, path, text)
	default:
		return keywordCompletions(topLevelKeywords)
	}
}

type compCtx string

func completionContext(text string, pos protocol.Position) string {
	lines := strings.Split(text, "\n")
	if int(pos.Line) >= len(lines) {
		return "top_level"
	}

	// Count brace depth up to cursor
	prefix := strings.Join(lines[:pos.Line], "\n")
	depth := 0
	inStr := false
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if c == '\'' {
			inStr = !inStr
		}
		if inStr {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
		}
	}

	if depth > 0 {
		// Inside a DPG block — check if we're after REFERENCES or a type position
		line := lines[pos.Line]
		upper := strings.ToUpper(strings.TrimSpace(line))
		if strings.Contains(upper, "REFERENCES") {
			return "reference"
		}
		return "block_directive"
	}

	// At top level — check if we're inside a column list (parens)
	parenDepth := 0
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		if c == '(' {
			parenDepth++
		} else if c == ')' {
			parenDepth--
		}
	}
	if parenDepth > 0 {
		line := lines[pos.Line]
		if strings.Contains(strings.ToUpper(line), "REFERENCES") {
			return "reference"
		}
		return "type"
	}

	return "top_level"
}

func keywordCompletions(keywords []string) []protocol.CompletionItem {
	items := make([]protocol.CompletionItem, 0, len(keywords))
	kind := protocol.CompletionItemKindKeyword
	for _, kw := range keywords {
		kw := kw
		items = append(items, protocol.CompletionItem{
			Label: kw,
			Kind:  &kind,
		})
	}
	return items
}

func typeCompletions(ws *workspace.Workspace, path, text string) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindClass
	items := make([]protocol.CompletionItem, 0, len(pgBuiltinTypes)+16)
	for _, t := range pgBuiltinTypes {
		t := t
		items = append(items, protocol.CompletionItem{Label: t, Kind: &kind})
	}
	// Add custom types from the current file
	for _, obj := range workspace.ParseObjects(text, path) {
		switch obj.Kind {
		case "ENUM", "TYPE", "DOMAIN", "VIRTUAL TYPE":
			obj := obj
			items = append(items, protocol.CompletionItem{Label: obj.Name, Kind: &kind})
		}
	}
	return items
}

func tableCompletions(ws *workspace.Workspace, path, text string) []protocol.CompletionItem {
	kind := protocol.CompletionItemKindClass
	var items []protocol.CompletionItem
	for _, obj := range workspace.ParseObjects(text, path) {
		if obj.Kind == "TABLE" {
			obj := obj
			items = append(items, protocol.CompletionItem{Label: obj.Name, Kind: &kind})
		}
	}
	return items
}
