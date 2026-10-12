package format

import (
	"strings"
	"unicode/utf8"
)

// Format parses src (from file path) and returns the canonically formatted
// output. If the file cannot be parsed, the original source is returned
// unchanged so the formatter never corrupts a file.
func Format(path string, src []byte, opts Options) ([]byte, error) {
	f, err := Parse(path, src)
	if err != nil {
		return src, err
	}
	return []byte(render(f, opts)), nil
}

// render converts a FormatAST back to source text.
func render(f *File, opts Options) string {
	var b strings.Builder

	for _, c := range f.LeadingComments {
		b.WriteString(c)
		b.WriteByte('\n')
	}

	for i, obj := range f.Objects {
		if i > 0 {
			b.WriteByte('\n') // blank line between top-level declarations
		}
		renderObject(&b, obj, opts, 0)
		b.WriteByte('\n')
	}

	if len(f.TrailingComments) > 0 {
		b.WriteByte('\n')
		for _, c := range f.TrailingComments {
			b.WriteString(c)
			b.WriteByte('\n')
		}
	}

	return b.String()
}

func renderObject(b *strings.Builder, obj ObjectNode, opts Options, depth int) {
	ind := strings.Repeat(opts.indent(), depth)

	// Write leading comments.
	for _, c := range obj.GetLeadingComments() {
		b.WriteString(ind)
		b.WriteString(c)
		b.WriteByte('\n')
	}

	switch n := obj.(type) {
	case *OpaqueNode:
		renderOpaque(b, n, opts, ind)
	case *TableNode:
		renderTable(b, n, opts, ind)
	case *SchemaBlockNode:
		renderSchemaBlock(b, n, opts, depth)
	case *MacroNode:
		renderMacro(b, n, opts, ind)
	}

	if c := trailingComment(obj); c != "" {
		b.WriteByte(' ')
		b.WriteString(c)
	}
}

// trailingComment returns the comment that follows obj's terminator on the
// same line.
func trailingComment(obj ObjectNode) string {
	switch n := obj.(type) {
	case *OpaqueNode:
		return n.TrailingComment
	case *TableNode:
		return n.TrailingComment
	case *SchemaBlockNode:
		return n.TrailingComment
	}
	return ""
}

func renderOpaque(b *strings.Builder, n *OpaqueNode, opts Options, ind string) {
	b.WriteString(ind)
	if n.KindKeyword != "" {
		b.WriteString(opts.keyword(n.KindKeyword))
		if n.RawPart1 != "" {
			b.WriteByte(' ')
		}
	}
	b.WriteString(rekeyword(n.RawPart1, opts))
	if n.HasPart2 {
		b.WriteString(" {")
		b.WriteString(sortBlock(n.RawPart2))
		b.WriteString("}")
	} else {
		b.WriteByte(';')
	}
}

func renderTable(b *strings.Builder, n *TableNode, opts Options, ind string) {
	kw := opts.keyword("TABLE")
	if n.Unlogged {
		b.WriteString(ind)
		b.WriteString(opts.keyword("UNLOGGED"))
		b.WriteByte(' ')
		b.WriteString(kw)
	} else {
		b.WriteString(ind)
		b.WriteString(kw)
	}
	b.WriteByte(' ')
	b.WriteString(n.Name)
	b.WriteString(" (")

	colInd := ind + opts.indent()
	cols := sortColumns(n.Columns)

	// Column alignment (RFC Section 18.8): pad each genuine column definition's
	// name to the widest name in the list, so every column's type starts at
	// the same position. Table-level constraint-clause entries in the same
	// ( ) list (CONSTRAINT/PRIMARY KEY/UNIQUE/CHECK/EXCLUDE/FOREIGN KEY —
	// recognizable because their first token is a keyword, not an
	// identifier) are never aligned and never count toward the width, since
	// they have no "name" field to align in the first place — padding them
	// against genuine column names would misalign both.
	//
	// Classification and name-extraction run on the ORIGINAL RawText, before
	// rekeyword ever sees it — a column's declared name must never be passed
	// through keyword-casing at all, even when it happens to collide with a
	// recognized DPG/PostgreSQL keyword (e.g. a column literally named
	// "event" must stay "event", not become "EVENT" just because EVENT is
	// also the EVENT TRIGGER keyword — confirmed live against a real
	// project's audit_log.event column). rekeyword's own dot-adjacency guard
	// already protects a qualified reference from this same ambiguity; a
	// declaration's own name needs the equivalent protection, which it can
	// only get by being pulled out before rekeyword runs on the remainder.
	rest := make([]string, len(cols))
	names := make([]string, len(cols))
	isColumnDef := make([]bool, len(cols))
	nameWidth := 0
	for i, col := range cols {
		if col.RawText == "" {
			continue
		}
		name, tail, isCol := splitColumnName(col.RawText)
		isColumnDef[i] = isCol
		if isCol {
			names[i] = name
			rest[i] = rekeyword(tail, opts)
			if w := utf8.RuneCountInString(name); w > nameWidth {
				nameWidth = w
			}
		} else {
			rest[i] = rekeyword(col.RawText, opts)
		}
	}

	for i, col := range cols {
		// Preserve blank line before this column's section block.
		if col.BlankLineBefore {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
		for _, c := range col.LeadingComments {
			b.WriteString(colInd)
			b.WriteString(c)
			b.WriteByte('\n')
		}
		if col.RawText != "" {
			b.WriteString(colInd)
			if isColumnDef[i] {
				b.WriteString(names[i])
				b.WriteString(strings.Repeat(" ", nameWidth-utf8.RuneCountInString(names[i])+1))
			}
			b.WriteString(rest[i])
			if i < len(cols)-1 {
				b.WriteByte(',')
			}
			if col.TrailingComment != "" {
				b.WriteString("  ")
				b.WriteString(col.TrailingComment)
			}
		}
	}
	b.WriteByte('\n')
	b.WriteString(ind)
	b.WriteByte(')')
	if n.Suffix != "" {
		b.WriteByte(' ')
		b.WriteString(rekeyword(n.Suffix, opts))
	}
	if n.HasPart2 {
		b.WriteString(" {")
		b.WriteString(sortBlock(n.RawPart2))
		b.WriteString("}")
	} else {
		b.WriteByte(';')
	}
}

func renderMacro(b *strings.Builder, n *MacroNode, opts Options, ind string) {
	b.WriteString(ind)
	b.WriteString(opts.keyword("MACRO"))
	b.WriteByte(' ')
	b.WriteString(n.RawAfterKeyword)
}

func renderSchemaBlock(b *strings.Builder, n *SchemaBlockNode, opts Options, depth int) {
	ind := strings.Repeat(opts.indent(), depth)
	innerInd := strings.Repeat(opts.indent(), depth+1)

	b.WriteString(ind)
	b.WriteString(opts.keyword("SCHEMA"))
	b.WriteByte(' ')
	b.WriteString(n.Name)
	b.WriteString(" {")

	if n.RawAttrs != "" {
		// Split schema-level directives into chunks, sort them, and render each
		// at the inner indentation level.
		chunks, _ := splitBlockDirectives(sortBlock(n.RawAttrs))
		for _, chunk := range chunks {
			trimmed := strings.TrimLeft(chunk.text, " \t\r\n")
			if trimmed == "" {
				continue
			}
			b.WriteByte('\n')
			b.WriteString(innerInd)
			b.WriteString(rekeyword(trimmed, opts))
		}
	}

	for i, child := range n.Objects {
		if i == 0 && n.RawAttrs != "" {
			b.WriteByte('\n') // blank line between schema attrs and first nested object
		}
		b.WriteByte('\n')
		renderObject(b, child, opts, depth+1)
		b.WriteByte('\n')
	}
	if len(n.Objects) == 0 && (n.RawAttrs != "" || len(n.InnerTrailingComments) > 0) {
		b.WriteByte('\n')
	}
	for _, c := range n.InnerTrailingComments {
		b.WriteString(innerInd)
		b.WriteString(c)
		b.WriteByte('\n')
	}
	b.WriteString(ind)
	b.WriteString("}")
}

// rekeyword rewrites known SQL/DPG keywords in text according to opts.KeywordCase.
// It operates on a whitespace-split token stream to avoid mangling identifiers.
//
// The lexer classifies any bare word matching dpgKeywords as TokKeyword with
// zero context awareness — it can't tell "PUBLIC" the GRANT-target pseudo-role
// keyword apart from "public" used as an ordinary schema-qualifier prefix
// (e.g. "public.log_drop_attempt()"), and many other keywords in the list
// (USER, DEFAULT, ALL, TEXT, TYPE, ROLE, TIME, ...) are equally plausible as
// real schema/object names. A keyword-classified word is only re-cased here
// when it is NOT immediately adjacent to a '.' — i.e. not in identifier
// position — since no dpgKeywords entry is ever legitimately dot-qualified
// (PUBLIC the pseudo-role, PROCEDURE the object-kind keyword, etc. never
// appear next to a '.'), so this can't misfire on a genuine keyword use.
func rekeyword(text string, opts Options) string {
	// Fast path: if no case preference, return as-is.
	if opts.KeywordCase == "" {
		return text
	}
	tokens := Lex("", []byte(text))
	var b strings.Builder
	for i, tok := range tokens {
		if tok.Type == TokEOF {
			break
		}
		inIdentPosition := (i > 0 && tokens[i-1].Type == TokDot) ||
			(i+1 < len(tokens) && tokens[i+1].Type == TokDot)
		if tok.Type == TokKeyword && !inIdentPosition {
			b.WriteString(opts.keyword(tok.Text))
		} else {
			b.WriteString(tok.Text)
		}
	}
	return b.String()
}

// constraintClauseLeadWords is the exact, closed set of keywords that can
// legally open a table-level constraint clause inside a TABLE's column list
// — CONSTRAINT / PRIMARY KEY / UNIQUE / CHECK / EXCLUDE / FOREIGN KEY, real
// PostgreSQL grammar's table_constraint production. Deliberately much
// narrower than dpgKeywords, which the lexer uses to classify a token as
// TokKeyword the instant it matches ANY recognized DPG/PostgreSQL keyword
// anywhere in the whole grammar, with zero positional awareness — a column
// can legitimately be NAMED after some unrelated keyword (e.g. "event",
// colliding with EVENT TRIGGER's EVENT), and only this specific, small set
// of words is ever grammatically valid at the START of a constraint-clause
// entry. Checking against this set — not the lexer's generic TokKeyword
// classification — is what correctly tells a genuine constraint clause
// apart from a keyword-colliding column name.
var constraintClauseLeadWords = map[string]bool{
	"CONSTRAINT": true,
	"PRIMARY":    true,
	"UNIQUE":     true,
	"CHECK":      true,
	"EXCLUDE":    true,
	"FOREIGN":    true,
}

// splitColumnName splits a table-column-list entry's ORIGINAL, not-yet-
// keyword-cased RawText into its leading name token and the remainder, and
// reports whether the entry is a genuine column definition (name TYPE ...)
// rather than a table-level constraint clause — see
// constraintClauseLeadWords's doc comment for why membership there, not the
// lexer's TokKeyword classification, is the correct test.
//
// Must run before rekeyword, not after: a column's own declared name must
// never be passed through keyword-casing at all, even when it happens to
// collide with a recognized DPG/PostgreSQL keyword (e.g. a column literally
// named "event" must stay "event", not become "EVENT" just because EVENT is
// also the EVENT TRIGGER keyword — confirmed live against a real project's
// audit_log.event column). rekeyword's own dot-adjacency guard already
// protects a qualified reference from the identical ambiguity; a
// declaration's own name needs the same protection, which it can only get
// by being isolated before rekeyword ever runs on it. Used by renderTable's
// column-alignment pass (RFC Section 18.8) to align only genuine column names
// against each other and leave constraint clauses alone.
func splitColumnName(rawText string) (name, rest string, isColumnDef bool) {
	toks := Lex("", []byte(rawText))
	if len(toks) == 0 {
		return "", rawText, false
	}
	first := toks[0]
	switch {
	case first.Type == TokIdent, first.Type == TokQuotedIdent:
		// Unambiguously a name.
	case first.Type == TokKeyword && !constraintClauseLeadWords[strings.ToUpper(first.Text)]:
		// Classified as a keyword only because it happens to collide with
		// some unrelated keyword elsewhere in the grammar — still a
		// genuine column name.
	default:
		return "", rawText, false
	}
	return first.Text, strings.TrimLeft(rawText[len(first.Text):], " \t"), true
}
