package format

import (
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanner"
)

// kindKeyword returns the leading keyword(s) for a given ObjectKind, matching
// what the scanner stripped from Part1.
func kindKeyword(k pipeline.ObjectKind) string {
	switch k {
	case pipeline.KindTable:
		return "TABLE"
	case pipeline.KindUnloggedTable:
		return "UNLOGGED TABLE"
	case pipeline.KindForeignTable:
		return "FOREIGN TABLE"
	case pipeline.KindView:
		return "VIEW"
	case pipeline.KindMaterializedView:
		return "MATERIALIZED VIEW"
	case pipeline.KindRecursiveView:
		return "RECURSIVE VIEW"
	case pipeline.KindFunction:
		return "FUNCTION"
	case pipeline.KindProcedure:
		return "PROCEDURE"
	case pipeline.KindAggregate:
		return "AGGREGATE"
	case pipeline.KindVirtualType:
		return "VIRTUAL TYPE"
	case pipeline.KindEnum, pipeline.KindCompositeType, pipeline.KindRangeType, pipeline.KindBaseType:
		return "TYPE"
	case pipeline.KindDomainType:
		return "DOMAIN"
	case pipeline.KindSchema:
		return "SCHEMA"
	case pipeline.KindExtension:
		return "EXTENSION"
	case pipeline.KindSequence:
		return "SEQUENCE"
	case pipeline.KindUnloggedSequence:
		return "UNLOGGED SEQUENCE"
	case pipeline.KindRole:
		return "ROLE"
	case pipeline.KindTablespace:
		return "TABLESPACE"
	case pipeline.KindFDW:
		return "FOREIGN DATA WRAPPER"
	case pipeline.KindServer:
		return "SERVER"
	case pipeline.KindUserMapping:
		return "USER MAPPING"
	case pipeline.KindPublication:
		return "PUBLICATION"
	case pipeline.KindSubscription:
		return "SUBSCRIPTION"
	case pipeline.KindEventTrigger:
		return "EVENT TRIGGER"
	case pipeline.KindCollation:
		return "COLLATION"
	case pipeline.KindOperator:
		return "OPERATOR"
	case pipeline.KindOperatorClass:
		return "OPERATOR CLASS"
	case pipeline.KindOperatorFamily:
		return "OPERATOR FAMILY"
	case pipeline.KindCast:
		return "CAST"
	case pipeline.KindStatisticsObject:
		return "STATISTICS"
	case pipeline.KindTSConfig:
		return "TEXT SEARCH CONFIGURATION"
	case pipeline.KindTSDict:
		return "TEXT SEARCH DICTIONARY"
	case pipeline.KindTSParser:
		return "TEXT SEARCH PARSER"
	case pipeline.KindTSTemplate:
		return "TEXT SEARCH TEMPLATE"
	case pipeline.KindDefaultPrivileges:
		return "DEFAULT PRIVILEGES"
	case pipeline.KindParameterPrivileges:
		return "PARAMETER PRIVILEGES"
	default:
		return ""
	}
}

// objectEndLine returns the last source line occupied by raw (inclusive).
// This is used to exclude intra-object comments from inter-object comment
// collection.
func objectEndLine(raw pipeline.RawObject) int {
	line := raw.Pos.Line + strings.Count(raw.Part1, "\n")
	if raw.Part2 != "" {
		line += strings.Count(raw.Part2, "\n")
	}
	return line
}

// Parse builds a FormatAST from the source file at path with content src.
// The FormatAST preserves comments for re-injection by the renderer.
func Parse(path string, src []byte) (*File, error) {
	sc := scanner.New()
	raws, err := sc.Scan(path, src)
	if err != nil {
		return nil, err
	}

	tokens := Lex(path, src)

	f := &File{Path: path}

	// Collect all comment tokens from the full token stream.
	var allComments []commentEntry
	for _, tok := range tokens {
		if tok.Type == TokLineComment || tok.Type == TokBlockComment {
			allComments = append(allComments, commentEntry{tok.Line, tok.Text})
		}
	}

	// Extract MACRO declarations from the original (unpreprocessed) token stream.
	// The scanner strips macros during preprocessing, so we must recover them here.
	macros := scanMacroDecls(tokens)

	// Determine the first source line among all top-level items.
	firstLine := 0
	if len(raws) > 0 {
		firstLine = raws[0].Pos.Line
	}
	for _, m := range macros {
		if firstLine == 0 || m.startLine < firstLine {
			firstLine = m.startLine
		}
	}

	var fileLeading []string
	fileLeadingEndLine := 0
	if firstLine > 0 {
		for _, c := range allComments {
			if c.line < firstLine {
				fileLeading = append(fileLeading, c.text)
				fileLeadingEndLine = c.line
			}
		}
	} else {
		for _, c := range allComments {
			fileLeading = append(fileLeading, c.text)
		}
	}
	f.LeadingComments = fileLeading

	p := &parser{
		src:      src,
		tokens:   tokens,
		comments: allComments,
	}
	f.Objects = p.buildAll(raws, macros, fileLeadingEndLine)
	return f, nil
}

type commentEntry struct {
	line int
	text string
}

type parser struct {
	src      []byte
	tokens   []Token
	comments []commentEntry
}

// commentsInRange returns comments with prevEndLine < line < targetLine.
// prevEndLine is the last line of the previous object (so intra-object
// comments are excluded); targetLine is the start line of the next object.
func (p *parser) commentsInRange(prevEndLine, targetLine int) []string {
	var out []string
	for _, c := range p.comments {
		if c.line > prevEndLine && c.line < targetLine {
			out = append(out, c.text)
		}
	}
	return out
}

// buildAll constructs the top-level ObjectNode slice from scanned raw objects and
// extracted MACRO declarations, interleaved in source order.
// Schema blocks are reconstructed: nested raw objects (raw.Schema != "") are
// grouped under their enclosing SchemaBlockNode instead of appearing at the top level.
func (p *parser) buildAll(raws []pipeline.RawObject, macros []macroDecl, fileLeadingEndLine int) []ObjectNode {
	// Unified item list — one entry per top-level unit (macros + non-nested objects).
	type item struct {
		line    int
		isMacro bool
		raw     pipeline.RawObject
		macro   macroDecl
	}

	var items []item
	for _, raw := range raws {
		// Skip schema-nested objects here; they are collected inside buildSchemaNode.
		if raw.Schema != "" && raw.Kind != pipeline.KindSchema {
			continue
		}
		items = append(items, item{line: raw.Pos.Line, raw: raw})
	}
	for _, m := range macros {
		items = append(items, item{line: m.startLine, isMacro: true, macro: m})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].line < items[j].line })

	var nodes []ObjectNode
	prevEndLine := fileLeadingEndLine

	for _, it := range items {
		leading := p.commentsInRange(prevEndLine, it.line)

		if it.isMacro {
			nodes = append(nodes, &MacroNode{
				baseNode:        baseNode{LeadingComments: leading},
				RawAfterKeyword: it.macro.rawAfterKeyword,
			})
			prevEndLine = it.macro.endLine
			continue
		}

		if it.raw.Kind == pipeline.KindSchema {
			schemaName := it.raw.Part1
			var nested []pipeline.RawObject
			for _, raw := range raws {
				if raw.Schema == schemaName {
					nested = append(nested, raw)
				}
			}
			node := p.buildSchemaNode(it.raw, nested, leading)
			nodes = append(nodes, node)
			if len(nested) > 0 {
				prevEndLine = objectEndLine(nested[len(nested)-1])
			} else {
				prevEndLine = objectEndLine(it.raw)
			}
			continue
		}

		node := p.buildNode(it.raw, leading)
		nodes = append(nodes, node)
		prevEndLine = objectEndLine(it.raw)
	}

	return nodes
}

// buildSchemaNode creates a SchemaBlockNode from the schema's RawObject and its
// nested children. raw.Pos.Line is used as the baseline for child comment collection.
func (p *parser) buildSchemaNode(raw pipeline.RawObject, nested []pipeline.RawObject, leading []string) ObjectNode {
	childNodes := p.buildNodes(nested, raw.Pos.Line)
	return &SchemaBlockNode{
		baseNode: baseNode{LeadingComments: leading},
		Name:     raw.Part1,
		Objects:  childNodes,
		RawAttrs: raw.Part2,
	}
}

// buildNodes converts a slice of pipeline.RawObject into ObjectNodes, attaching
// inter-object comments. Used for schema-nested objects (no macros, no sub-schemas).
func (p *parser) buildNodes(raws []pipeline.RawObject, prevEndLine int) []ObjectNode {
	var nodes []ObjectNode
	for _, raw := range raws {
		leading := p.commentsInRange(prevEndLine, raw.Pos.Line)
		node := p.buildNode(raw, leading)
		nodes = append(nodes, node)
		prevEndLine = objectEndLine(raw)
	}
	return nodes
}

// buildNode converts one RawObject into an ObjectNode.
func (p *parser) buildNode(raw pipeline.RawObject, leading []string) ObjectNode {
	switch raw.Kind {
	case pipeline.KindTable, pipeline.KindUnloggedTable:
		return p.buildTableNode(raw, leading)
	default:
		return &OpaqueNode{
			baseNode:    baseNode{LeadingComments: leading},
			KindKeyword: kindKeyword(raw.Kind),
			RawPart1:    raw.Part1,
			RawPart2:    raw.Part2,
			HasPart2:    raw.HasPart2,
		}
	}
}

// buildTableNode parses a TABLE declaration's column list from Part1.
func (p *parser) buildTableNode(raw pipeline.RawObject, leading []string) ObjectNode {
	columns := parseColumns(raw.Part1)
	if len(columns) == 0 {
		return &OpaqueNode{
			baseNode:    baseNode{LeadingComments: leading},
			KindKeyword: kindKeyword(raw.Kind),
			RawPart1:    raw.Part1,
			RawPart2:    raw.Part2,
			HasPart2:    raw.HasPart2,
		}
	}

	return &TableNode{
		baseNode: baseNode{LeadingComments: leading},
		Unlogged: raw.Kind == pipeline.KindUnloggedTable,
		Name:     extractTableName(raw.Part1),
		Columns:  columns,
		RawPart2: raw.Part2,
		HasPart2: raw.HasPart2,
	}
}

// extractTableName returns the table name (and schema qualifier if present)
// from the Part1 text. Part1 looks like `schema.name (col1 type, ...)`.
func extractTableName(part1 string) string {
	part1 = strings.TrimSpace(part1)
	if cut, _, ok := strings.Cut(part1, "("); ok {
		return strings.TrimSpace(cut)
	}
	return part1
}

// parseColumns splits Part1 of a TABLE declaration into ColumnNodes.
// It uses the format lexer so that comments and blank lines within the column
// list are detected and attached correctly.
func parseColumns(part1 string) []*ColumnNode {
	// Find the outer paren that holds the column list.
	open := strings.IndexByte(part1, '(')
	if open < 0 {
		return nil
	}
	close := findMatchingParen(part1, open)
	if close < 0 {
		return nil
	}
	inner := part1[open+1 : close]
	return splitColumnDefs(inner)
}

// findMatchingParen returns the index of the ')' matching '(' at openIdx,
// accounting for single-quoted strings.
func findMatchingParen(s string, openIdx int) int {
	depth := 0
	inSQ := false
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if inSQ {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inSQ = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inSQ = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitColumnDefs splits the inner column-list text into ColumnNodes.
// It uses the format lexer to correctly handle:
//   - Blank lines before section comments (sets BlankLineBefore)
//   - Line and block comments as leading comments for the next column
//   - Nested parentheses (don't split on commas inside them)
//   - Dollar-quoted and single-quoted strings
func splitColumnDefs(inner string) []*ColumnNode {
	tokens := Lex("", []byte(inner))

	var cols []*ColumnNode

	// Current accumulator state.
	var (
		pendingComments []string
		blankBefore     bool // blank line seen before any content in this chunk
		blankPending    bool // two consecutive newlines seen (blank line)
		prevNewline     bool // last meaningful token was a newline
		colText         strings.Builder
		hasText         bool // non-WS/comment content written to colText
		depth           int
	)

	flush := func() {
		text := strings.TrimSpace(colText.String())
		if text != "" || len(pendingComments) > 0 {
			cols = append(cols, &ColumnNode{
				BlankLineBefore: blankBefore,
				LeadingComments: pendingComments,
				RawText:         text,
			})
		}
		pendingComments = nil
		blankBefore = false
		blankPending = false
		prevNewline = false
		colText.Reset()
		hasText = false
	}

	for _, tok := range tokens {
		switch tok.Type {
		case TokEOF:
			flush()

		case TokNewline:
			if prevNewline {
				blankPending = true
			}
			prevNewline = true
			if hasText {
				colText.WriteString(tok.Text)
			}

		case TokWhitespace:
			if hasText {
				colText.WriteString(tok.Text)
			}
			// Don't update prevNewline — whitespace doesn't break newline tracking.

		case TokLineComment, TokBlockComment:
			if !hasText {
				// Leading comment for the upcoming column.
				if blankPending {
					blankBefore = true
					blankPending = false
				}
				pendingComments = append(pendingComments, tok.Text)
			} else {
				colText.WriteString(tok.Text)
			}
			prevNewline = false

		case TokComma:
			if depth == 0 {
				flush()
			} else {
				colText.WriteString(tok.Text)
				hasText = true
			}
			prevNewline = false

		case TokLParen:
			depth++
			if blankPending && !hasText {
				blankBefore = true
				blankPending = false
			}
			colText.WriteString(tok.Text)
			hasText = true
			prevNewline = false

		case TokRParen:
			depth--
			if depth >= 0 {
				colText.WriteString(tok.Text)
			}
			prevNewline = false

		default:
			if blankPending && !hasText {
				blankBefore = true
				blankPending = false
			}
			colText.WriteString(tok.Text)
			hasText = true
			prevNewline = false
		}
	}

	return cols
}

// ── MACRO extraction ──────────────────────────────────────────────────────────

// macroDecl is a MACRO declaration found in the original (unpreprocessed) source.
type macroDecl struct {
	startLine       int    // 1-based line of the MACRO keyword
	endLine         int    // 1-based line of the closing delimiter
	rawAfterKeyword string // text following "MACRO " — name + body, trimmed of leading WS
}

// scanMacroDecls extracts top-level MACRO declarations from the token stream.
// Only macros at brace depth 0 are returned; macros inside SCHEMA { } or
// dollar-quoted function bodies are excluded automatically (the lexer emits a
// dollar-quoted string as one TokDollarQuote token, so its contents are opaque).
func scanMacroDecls(tokens []Token) []macroDecl {
	var result []macroDecl
	braceDepth := 0
	for i := 0; i < len(tokens); {
		tok := tokens[i]
		switch tok.Type {
		case TokEOF:
			return result
		case TokLBrace:
			braceDepth++
			i++
		case TokRBrace:
			if braceDepth > 0 {
				braceDepth--
			}
			i++
		case TokKeyword:
			if braceDepth == 0 && strings.ToUpper(tok.Text) == "MACRO" {
				decl, next := collectMacroTokens(tokens, i)
				if decl != nil {
					result = append(result, *decl)
				}
				i = next
			} else {
				i++
			}
		default:
			i++
		}
	}
	return result
}

// collectMacroTokens collects one MACRO declaration starting at tokens[start]
// (the MACRO keyword token). Returns the macroDecl and the index of the first
// token after the closing delimiter.
func collectMacroTokens(tokens []Token, start int) (*macroDecl, int) {
	if start >= len(tokens) {
		return nil, start + 1
	}
	startLine := tokens[start].Line
	i := start + 1

	// Skip whitespace/newlines to find the macro name.
	for i < len(tokens) && isMacroTrivia(tokens[i].Type) {
		i++
	}
	if i >= len(tokens) || tokens[i].Type == TokEOF {
		return nil, i
	}
	i++ // consume name token

	// Skip whitespace/newlines to find '(' or '{'.
	for i < len(tokens) && isMacroTrivia(tokens[i].Type) {
		i++
	}
	if i >= len(tokens) || tokens[i].Type == TokEOF {
		return nil, i
	}

	var openT, closeT TokType
	switch tokens[i].Type {
	case TokLParen:
		openT, closeT = TokLParen, TokRParen
	case TokLBrace:
		openT, closeT = TokLBrace, TokRBrace
	default:
		return nil, i + 1
	}
	i++ // consume opening delimiter

	depth := 1
	for i < len(tokens) && depth > 0 && tokens[i].Type != TokEOF {
		switch tokens[i].Type {
		case openT:
			depth++
		case closeT:
			depth--
		}
		i++
	}
	// i is now one past the closing delimiter

	endLine := startLine
	if i > 0 && i-1 < len(tokens) {
		endLine = tokens[i-1].Line
	}

	// Reconstruct the full raw text from the MACRO keyword through the closing delimiter.
	var sb strings.Builder
	for j := start; j < i; j++ {
		sb.WriteString(tokens[j].Text)
	}
	rawFull := strings.TrimRight(sb.String(), " \t\r\n")

	// Strip the leading "MACRO" keyword and any following whitespace.
	rawAfter := rawFull
	if len(rawFull) >= 5 && strings.ToUpper(rawFull[:5]) == "MACRO" {
		rawAfter = strings.TrimLeft(rawFull[5:], " \t")
	}

	return &macroDecl{
		startLine:       startLine,
		endLine:         endLine,
		rawAfterKeyword: rawAfter,
	}, i
}

func isMacroTrivia(t TokType) bool {
	return t == TokWhitespace || t == TokNewline
}
