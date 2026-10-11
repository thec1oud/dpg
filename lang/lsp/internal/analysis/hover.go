package analysis

import (
	"fmt"
	"strings"

	"github.com/thec1oud/dpg-lsp/internal/workspace"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Hover returns hover information for the symbol at the given position.
// It scans the document for object declarations that contain the cursor position.
func Hover(ws *workspace.Workspace, path string, pos protocol.Position) *protocol.Hover {
	text := ws.GetText(path)
	if text == "" {
		return nil
	}

	word := wordAtPosition(text, pos)
	if word == "" {
		return nil
	}

	objs := workspace.ParseObjects(text, path)
	for _, obj := range objs {
		if strings.EqualFold(obj.Name, word) || strings.HasSuffix(obj.Name, "."+word) {
			md := fmt.Sprintf("**%s** `%s`", obj.Kind, obj.Name)
			if obj.Comment != "" {
				md += "\n\n" + obj.Comment
			}
			kind := protocol.MarkupKindMarkdown
			return &protocol.Hover{
				Contents: protocol.MarkupContent{Kind: kind, Value: md},
				Range: &protocol.Range{
					Start: protocol.Position{Line: uint32(obj.Line - 1), Character: 0},
					End:   protocol.Position{Line: uint32(obj.Line - 1), Character: uint32(len(obj.Kind) + len(obj.Name) + 1)},
				},
			}
		}
	}

	// Fallback: show keyword documentation
	if doc := keywordDoc(word); doc != "" {
		kind := protocol.MarkupKindMarkdown
		return &protocol.Hover{
			Contents: protocol.MarkupContent{Kind: kind, Value: doc},
		}
	}

	return nil
}

// wordAtPosition extracts the identifier word under the cursor.
func wordAtPosition(text string, pos protocol.Position) string {
	lines := strings.Split(text, "\n")
	if int(pos.Line) >= len(lines) {
		return ""
	}
	line := lines[pos.Line]
	col := int(pos.Character)
	if col > len(line) {
		col = len(line)
	}

	isIdent := func(c byte) bool {
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '$'
	}

	// If the cursor is on a non-ident character (and not at end-of-line),
	// there is no word under it.
	if col < len(line) && !isIdent(line[col]) {
		return ""
	}

	start := col
	for start > 0 && isIdent(line[start-1]) {
		start--
	}
	end := col
	for end < len(line) && isIdent(line[end]) {
		end++
	}
	return line[start:end]
}

func keywordDoc(word string) string {
	docs := map[string]string{
		// Object declarations
		"TABLE":        "**TABLE** — declares a persistent relation (DPG §3.1)",
		"EVENT":        "**EVENT TRIGGER** — fires a function in response to a DDL event (CREATE, DROP, ALTER, etc.)",
		"DEFAULT":      "**DEFAULT PRIVILEGES** — sets default access privileges for future objects in a schema",
		"TEXT":         "**TEXT SEARCH** — declares a text-search object (CONFIGURATION, DICTIONARY, PARSER, or TEMPLATE)",
		"USER":         "**USER MAPPING** — maps a local role to credentials on a foreign server",
		"UNLOGGED":     "**UNLOGGED TABLE** — unlogged table; not WAL-written, faster but not crash-safe",
		"FOREIGN":      "**FOREIGN TABLE** — foreign-data-wrapper table mapped to an external source",
		"VIEW":         "**VIEW** — declares a named query (DPG §3.2)",
		"MATERIALIZED": "**MATERIALIZED VIEW** — a view whose result is stored and refreshed explicitly",
		"RECURSIVE":    "**RECURSIVE VIEW** — a view defined with a recursive CTE",
		"FUNCTION":     "**FUNCTION** — declares a database function (DPG §3.4)",
		"PROCEDURE":    "**PROCEDURE** — declares a stored procedure (DPG §3.5)",
		"AGGREGATE":    "**AGGREGATE** — declares a user-defined aggregate function (DPG §3.6)",
		"ENUM":         "**ENUM** — `TYPE name AS ENUM (...)` declares an enumerated type",
		"TYPE":         "**TYPE** — declares an enum, composite, range, or base type",
		"VIRTUAL":      "**VIRTUAL TYPE** — gives a structural schema to a `jsonb`/`json` column. Emits no SQL; resolves to `jsonb` (or `json`) when used as a column/composite-attribute type. Body: a type reference, composite `(field type, ...)`, or `|`-separated union. (DPG §5.6)",
		"DOMAIN":       "**DOMAIN** — declares a domain (constrained base type) (DPG §3.10)",
		"SEQUENCE":     "**SEQUENCE** — declares a sequence generator (DPG §3.11)",
		"SCHEMA":       "**SCHEMA** — declares a named schema namespace (DPG §3.12)",
		"EXTENSION":    "**EXTENSION** — installs a PostgreSQL extension (DPG §3.12)",
		"ROLE":         "**ROLE** — declares a cluster-level role (DPG §3.13)",
		"TABLESPACE":   "**TABLESPACE** — declares a named storage location for database objects",
		"PUBLICATION":  "**PUBLICATION** — logical-replication publication (DPG §3.15)",
		"SUBSCRIPTION": "**SUBSCRIPTION** — logical-replication subscription (DPG §3.16)",
		"SERVER":       "**SERVER** — foreign server definition for a foreign-data wrapper",
		"COLLATION":    "**COLLATION** — declares a named collation rule",
		"OPERATOR":     "**OPERATOR** — declares a user-defined operator",
		"CAST":         "**CAST** — declares a type-cast between two types",
		"STATISTICS":   "**STATISTICS** — extended statistics object for the planner",
		"MACRO":        "**MACRO** — project-scoped column-list or block template. Defined in any `.dpg` file; available to every other file in the compilation scope. Expanded with `...name`. Nested spreads supported; circular references are a compiler error (DPG-E012). (DPG §4.7)",
		// Virtual type block directives
		"PREFERRED": "`PREFERRED JSON FORMAT json|jsonb;` — controls whether virtual type references resolve to `json` or `jsonb` (default) in generated SQL",
		// Block directives
		"INDEXES":     "`INDEXES { }` — index definitions block",
		"POLICIES":    "`POLICIES { }` — row-level security policy block",
		"TRIGGERS":    "`TRIGGERS { }` — trigger definition block",
		"COLUMNS":     "`COLUMNS { }` — inline column override directives",
		"ATTRIBUTES":  "`ATTRIBUTES { name { ... } }` — composite type attribute blocks (COMMENT, RENAMED FROM)",
		"ATTRIBUTE":   "`ATTRIBUTE name { COMMENT '...'; RENAMED FROM old; }` — one composite type attribute",
		"CONSTRAINTS": "`CONSTRAINTS { }` — constraint declarations block",
		"PARTITIONS":  "`PARTITIONS { }` — partition specification block",
		"GRANTS":      "`GRANTS { }` — privilege grant block",
		"REVOKES":     "`REVOKES { }` — privilege revoke block",
		"OWNER":       "`OWNER <role>;` — sets the owner of the object",
		"COMMENT":     "`COMMENT 'text';` — sets the object comment (shown in \\d+)",
		// Lifecycle directives
		"RENAMED":    "`RENAMED FROM <old_name>;` — tracks a rename for safe migration",
		"DEPRECATED": "`DEPRECATED \"message\";` — marks an object as deprecated",
		"PROTECTED":  "`PROTECTED;` — prevents accidental DROP (error DPG-E022)",
		"MIGRATE":    "`MIGRATE REMOVE (value) { DML }` — safe ENUM value removal",
		"DROP":       "`DROP CASCADE;` — unconditional drop lifecycle directive",
		// Role attributes
		"SUPERUSER":    "`SUPERUSER` — grants superuser privilege",
		"NOSUPERUSER":  "`NOSUPERUSER` — removes superuser privilege (default)",
		"CREATEDB":     "`CREATEDB` — allows the role to create databases",
		"NOCREATEDB":   "`NOCREATEDB` — prevents the role from creating databases (default)",
		"CREATEROLE":   "`CREATEROLE` — allows the role to create other roles",
		"NOCREATEROLE": "`NOCREATEROLE` — prevents the role from creating roles (default)",
		"LOGIN":        "`LOGIN` — allows the role to authenticate (i.e. it is a user)",
		"NOLOGIN":      "`NOLOGIN` — prevents authentication (role is a group, default)",
		"REPLICATION":  "`REPLICATION` — allows the role to initiate streaming replication",
		"BYPASSRLS":    "`BYPASSRLS` — role bypasses all row-level security policies",
		"INHERIT":      "`INHERIT` — role inherits privileges of roles it is a member of (default)",
		"NOINHERIT":    "`NOINHERIT` — role does not inherit privileges",
		// Sequence options
		"CACHE":     "`CACHE <n>` — number of sequence values to pre-allocate in memory",
		"CYCLE":     "`CYCLE` — sequence wraps around when it hits min/max value",
		"MAXVALUE":  "`MAXVALUE <n>` — upper bound of the sequence",
		"MINVALUE":  "`MINVALUE <n>` — lower bound of the sequence",
		"INCREMENT": "`INCREMENT BY <n>` — step size for sequence values",
		// Column modifiers
		"STORAGE":     "`STORAGE <strategy>` — overrides column storage (PLAIN, EXTERNAL, EXTENDED, MAIN)",
		"COMPRESSION": "`COMPRESSION <method>` — column compression method (pglz, lz4)",
		// Function / aggregate params
		"VARIADIC": "`VARIADIC` — parameter accepts a variable number of arguments",
		"INOUT":    "`INOUT` — parameter is both input and output",
	}
	return docs[strings.ToUpper(word)]
}
