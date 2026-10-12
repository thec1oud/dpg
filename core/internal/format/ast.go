package format

// File is the top-level FormatAST node representing one .dpg source file.
type File struct {
	Path            string
	LeadingComments []string
	Objects         []ObjectNode
	// TrailingComments are the comments after the last declaration.
	TrailingComments []string
}

// ObjectNode is the common interface for all top-level and schema-nested nodes.
type ObjectNode interface {
	objectNode()
	// GetLeadingComments returns block/line comments immediately before this node.
	GetLeadingComments() []string
}

// baseNode holds the comment fields shared by every node type.
type baseNode struct {
	LeadingComments []string // block/line comments immediately before this node
	TrailingComment string   // inline comment on the same line (after , or ;)
}

func (b *baseNode) GetLeadingComments() []string { return b.LeadingComments }

// OpaqueNode holds a declaration that the format parser does not decompose
// further (e.g. FUNCTIONs whose bodies are opaque, or any object type not
// yet handled by the detail parser). The raw text is preserved verbatim;
// only comments are re-attached.
type OpaqueNode struct {
	baseNode
	// KindKeyword is the leading keyword(s) for this object kind (e.g. "ROLE",
	// "TABLE", "MATERIALIZED VIEW"). The scanner strips these from Part1, so
	// the renderer must re-prefix them.
	KindKeyword string
	// RawPart1 is the declaration text up to (but not including) the { block,
	// with the kind keyword already stripped.
	RawPart1 string
	// RawPart2 is the { } block's inner content, or "" both when the block
	// is absent and when it is present but genuinely empty ("{ }") — see
	// HasPart2 to tell those two apart.
	RawPart2 string
	// HasPart2 is true when a "{ }" block was present in source at all
	// (even if RawPart2's content is ""), false when there was no block. The
	// renderer uses this, not RawPart2's emptiness, to decide whether to
	// emit "{ }" or a bare ";" — otherwise an explicit empty block would be
	// silently rewritten to ";" on every format.
	HasPart2 bool
}

func (n *OpaqueNode) objectNode() {}

// SchemaBlockNode is a SCHEMA name { ... } declaration.
type SchemaBlockNode struct {
	baseNode
	Name    string
	Objects []ObjectNode
	// RawAttrs is the raw schema attribute text collected from inside the block
	// (OWNER, COMMENT, GRANTS, etc.) that belongs to the schema itself.
	RawAttrs string
	// InnerTrailingComments are the comments after the last nested object
	// (or among the schema's own directives when it has none), before the
	// closing '}'.
	InnerTrailingComments []string
}

func (n *SchemaBlockNode) objectNode() {}

// TableNode is a TABLE / UNLOGGED TABLE declaration.
type TableNode struct {
	baseNode
	Unlogged bool
	Name     string
	Columns  []*ColumnNode
	// Suffix is the Part 1 text after the column list's ')' (PARTITION BY,
	// INHERITS, WITH, TABLESPACE, ...), kept as written.
	Suffix string
	// RawPart2 is the unparsed { } block's inner content; preserved verbatim
	// pending a full directive parser (planned for a future pass). Same ""
	// ambiguity as OpaqueNode.RawPart2 — see HasPart2.
	RawPart2 string
	// HasPart2 — see OpaqueNode.HasPart2.
	HasPart2 bool
}

func (n *TableNode) objectNode() {}

// MacroNode preserves a MACRO declaration verbatim. The formatter keeps macro
// bodies exactly as written since they are templates, not independent objects.
type MacroNode struct {
	baseNode
	// RawAfterKeyword is the text following the MACRO keyword — the name and
	// body (including the opening/closing delimiter), trimmed of leading space.
	RawAfterKeyword string
}

func (n *MacroNode) objectNode() {}

// ColumnNode is one column in a TABLE Part 1. It is not a top-level ObjectNode.
type ColumnNode struct {
	// BlankLineBefore is true when a blank line (empty line) precedes this
	// column's leading comments in the original source. The renderer preserves it.
	BlankLineBefore bool
	LeadingComments []string
	TrailingComment string
	// RawText is the column definition text as it appeared in the source,
	// trimmed of leading/trailing whitespace but otherwise verbatim.
	RawText string
}
