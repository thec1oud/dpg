package format

import (
	"sort"
	"strings"
)

// ── Column ordering ───────────────────────────────────────────────────────────

type colKind int

const (
	colKindDef        colKind = iota // regular/generated/identity column definition
	colKindRef                       // FOREIGN KEY constraint (references section)
	colKindConstraint                // other named or unnamed constraint
)

// classifyCol returns the kind and sort key for a column entry's raw text.
// Named constraints use the lowercased constraint name as the sort key.
func classifyCol(raw string) (colKind, string) {
	upper := strings.ToUpper(strings.TrimSpace(raw))

	if strings.HasPrefix(upper, "CONSTRAINT") {
		rest := strings.TrimSpace(upper[len("CONSTRAINT"):])
		// Extract the constraint name (first word after CONSTRAINT).
		name := rest
		if i := strings.IndexAny(rest, " \t\n\r("); i >= 0 {
			name = rest[:i]
			rest = strings.TrimSpace(rest[i:])
		} else {
			rest = ""
		}
		name = strings.ToLower(name)
		if strings.HasPrefix(rest, "FOREIGN") {
			return colKindRef, name
		}
		return colKindConstraint, name
	}

	if strings.HasPrefix(upper, "FOREIGN") {
		return colKindRef, ""
	}
	for _, pfx := range []string{"PRIMARY", "UNIQUE", "CHECK", "EXCLUDE"} {
		if strings.HasPrefix(upper, pfx) {
			return colKindConstraint, ""
		}
	}
	return colKindDef, ""
}

// sortColumns reorders cols into canonical order:
//  1. column definitions, including generated/identity columns (source order)
//  2. FOREIGN KEY constraints — references section (alphabetical by name)
//  3. other constraints — PK, UNIQUE, CHECK, EXCLUDE (alphabetical by name)
func sortColumns(cols []*ColumnNode) []*ColumnNode {
	type entry struct {
		node *ColumnNode
		kind colKind
		key  string
	}
	var defs, refs, csts []entry
	for _, col := range cols {
		k, key := classifyCol(col.RawText)
		e := entry{col, k, key}
		switch k {
		case colKindDef:
			defs = append(defs, e)
		case colKindRef:
			refs = append(refs, e)
		default:
			csts = append(csts, e)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].key < refs[j].key })
	sort.SliceStable(csts, func(i, j int) bool { return csts[i].key < csts[j].key })

	out := make([]*ColumnNode, 0, len(cols))
	for _, e := range defs {
		out = append(out, e.node)
	}
	for _, e := range refs {
		out = append(out, e.node)
	}
	for _, e := range csts {
		out = append(out, e.node)
	}
	return out
}

// ── Block directive ordering ──────────────────────────────────────────────────

// blockChunk is one directive from a { } block, together with any leading
// whitespace and comments that immediately precede it.
type blockChunk struct {
	text    string // prefix whitespace/comments + directive body
	firstKw string // first word of the directive (uppercased), used for priority
}

// blockDirPriority returns the canonical sort priority for a block directive
// identified by its first keyword.
func blockDirPriority(firstKw string) int {
	switch firstKw {
	case "RENAMED":
		return 0
	case "COMMENT":
		return 1
	case "OWNER":
		return 2
	case "DEPRECATED":
		return 3
	case "PROTECTED":
		return 4
	case "DROP":
		return 5
	case "ENABLE":
		return 6
	case "FORCE":
		return 7
	case "INDEXES", "INDEX":
		return 8
	case "COLUMNS", "COLUMN":
		return 9
	case "POLICIES", "POLICY":
		return 10
	case "TRIGGERS", "TRIGGER":
		return 11
	case "GRANTS", "GRANT":
		return 12
	case "REVOKES", "REVOKE":
		return 13
	case "PREFERRED":
		return 14
	default:
		return 99
	}
}

// splitBlockDirectives parses the inner content of a { } block (the text
// between the braces, without the braces themselves) into directive chunks.
// Each chunk contains its leading whitespace/comments and the directive body
// up to its terminator (a ; at depth 0, or a } that closes a sub-block).
// Any trailing whitespace after the last directive is returned separately.
func splitBlockDirectives(inner string) (chunks []blockChunk, trailing string) {
	tokens := Lex("", []byte(inner))

	var pre strings.Builder  // whitespace/comments before the current directive
	var body strings.Builder // current directive body
	firstKw := ""
	inDir := false
	depth := 0

	flush := func() {
		chunks = append(chunks, blockChunk{
			text:    pre.String() + body.String(),
			firstKw: firstKw,
		})
		pre.Reset()
		body.Reset()
		firstKw = ""
		inDir = false
	}

	for _, tok := range tokens {
		if tok.Type == TokEOF {
			if inDir {
				flush()
			}
			trailing = pre.String()
			return
		}

		if !inDir {
			switch tok.Type {
			case TokNewline, TokWhitespace, TokLineComment, TokBlockComment:
				pre.WriteString(tok.Text)
			default:
				inDir = true
				body.WriteString(tok.Text)
				if firstKw == "" && (tok.Type == TokKeyword || tok.Type == TokIdent) {
					firstKw = strings.ToUpper(tok.Text)
				}
			}
		} else {
			body.WriteString(tok.Text)
			switch tok.Type {
			case TokLBrace:
				depth++
			case TokRBrace:
				depth--
				if depth == 0 {
					flush()
				}
			case TokSemicolon:
				if depth == 0 {
					flush()
				}
			}
		}
	}
	return
}

// sortBlock returns the inner content of a { } block with its directives
// reordered into canonical sequence (RENAMED FROM first, then remaining
// directives in declaration order). The input and output both exclude the
// surrounding braces. Returns rawPart2 unchanged when there is nothing to sort.
func sortBlock(rawPart2 string) string {
	if strings.TrimSpace(rawPart2) == "" {
		return rawPart2
	}
	chunks, trailing := splitBlockDirectives(rawPart2)
	if len(chunks) == 0 {
		return rawPart2
	}

	type ranked struct {
		chunk blockChunk
		prio  int
		orig  int
	}
	rs := make([]ranked, len(chunks))
	for i, ch := range chunks {
		rs[i] = ranked{ch, blockDirPriority(ch.firstKw), i}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].prio != rs[j].prio {
			return rs[i].prio < rs[j].prio
		}
		return rs[i].orig < rs[j].orig
	})

	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.chunk.text)
	}
	b.WriteString(trailing)
	return b.String()
}
