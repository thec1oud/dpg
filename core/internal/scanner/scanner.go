// Package scanner implements the pipeline.Tokenizer interface by scanning
// .dpg source files and splitting each declaration into a pipeline.RawObject.
//
// The scanner understands just enough structure to:
//   - identify object-kind keywords and find declaration boundaries
//   - separate Part 1 (PG SQL text) from Part 2 ({ } block text)
//   - track dollar-quoted strings, string literals, paren depth, and comments
//     so that none of their internal content is mistaken for structure
//   - recurse into SCHEMA { } bodies, emitting nested objects with Schema set
//   - enforce the no-verb mandate (CREATE/ALTER/DROP are illegal at declaration level)
//
// The scanner never interprets PG SQL syntax — that is the PGSQLParser's job.
package scanner

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyTokenizer, New())
}

// Scanner implements pipeline.Tokenizer and pipeline.GlobalMacroSeeder.
type Scanner struct {
	global macroStore // macros collected from all files during the pre-pass
}

// New returns a Scanner ready to use.
func New() *Scanner { return &Scanner{} }

// AddGlobalMacros implements pipeline.GlobalMacroSeeder. It collects MACRO
// definitions from src and adds them to the shared store. Definitions from
// later calls override earlier ones when names conflict.
func (sc *Scanner) AddGlobalMacros(src []byte) error {
	local, err := collectMacros(src)
	if err != nil {
		return err
	}
	if sc.global == nil {
		sc.global = make(macroStore, len(local))
	}
	for k, v := range local {
		sc.global[k] = v
	}
	return nil
}

// Scan implements pipeline.Tokenizer. It scans path/src and returns one
// RawObject per declaration, including objects nested inside SCHEMA { } blocks.
// MACRO declarations are collected and expanded before the main scan. Global
// macros seeded via AddGlobalMacros are available to all files; file-local
// definitions take precedence over global ones.
func (sc *Scanner) Scan(path string, src []byte) ([]pipeline.RawObject, error) {
	expanded, err := preprocessMacrosWithGlobal(src, sc.global)
	if err != nil {
		return nil, fmt.Errorf("%s: macro preprocessing: %w", path, err)
	}
	s := &state{src: expanded, path: path, line: 1, col: 1}
	objects, _, err := s.scanBody("", false)
	return objects, err
}

// ── internal state ────────────────────────────────────────────────────────────

// state is the mutable scanning cursor for a single file.
type state struct {
	src  []byte
	pos  int
	path string
	line int
	col  int
}

// cursor captures the full scanner position for save/restore.
type cursor struct{ pos, line, col int }

func (s *state) cur() cursor { return cursor{s.pos, s.line, s.col} }
func (s *state) restore(c cursor) {
	s.pos = c.pos
	s.line = c.line
	s.col = c.col
}

func (s *state) eof() bool { return s.pos >= len(s.src) }

func (s *state) peek() byte {
	if s.eof() {
		return 0
	}
	return s.src[s.pos]
}

func (s *state) peekAt(n int) byte {
	if s.pos+n >= len(s.src) {
		return 0
	}
	return s.src[s.pos+n]
}

func (s *state) advance() byte {
	if s.eof() {
		return 0
	}
	b := s.src[s.pos]
	s.pos++
	if b == '\n' {
		s.line++
		s.col = 1
	} else {
		s.col++
	}
	return b
}

func (s *state) srcPos() pipeline.SourcePos {
	return pipeline.SourcePos{File: s.path, Line: s.line, Col: s.col}
}

// ── whitespace & comments ─────────────────────────────────────────────────────

// skipWS skips whitespace and comments (-- and /* */).
func (s *state) skipWS() {
	for !s.eof() {
		switch s.peek() {
		case ' ', '\t', '\r', '\n':
			s.advance()
		case '-':
			if s.peekAt(1) == '-' {
				for !s.eof() && s.peek() != '\n' {
					s.advance()
				}
			} else {
				return
			}
		case '/':
			if s.peekAt(1) == '*' {
				s.advance()
				s.advance()
				for !s.eof() {
					if s.peek() == '*' && s.peekAt(1) == '/' {
						s.advance()
						s.advance()
						break
					}
					s.advance()
				}
			} else {
				return
			}
		default:
			return
		}
	}
}

// ── identifier / keyword helpers ──────────────────────────────────────────────

func isWordStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isWordChar(b byte) bool {
	return isWordStart(b) || (b >= '0' && b <= '9')
}

// readWord reads one SQL identifier/keyword without advancing past trailing
// whitespace. Returns the raw case-preserved text.
func (s *state) readWord() string {
	var buf []byte
	for !s.eof() && isWordChar(s.peek()) {
		buf = append(buf, s.advance())
	}
	return string(buf)
}

// peekWord returns the next word without consuming it.
func (s *state) peekWord() string {
	c := s.cur()
	w := s.readWord()
	s.restore(c)
	return w
}

// skipDottedName consumes a (possibly schema-qualified) name matching RFC's
// schema-name production (identifier *("." identifier)) — every dotted
// segment, not just the first. See detectTypeKind's call sites for why this
// matters: readWord() alone stops at "." and leaves it unconsumed.
func (s *state) skipDottedName() {
	s.readWord()
	for s.peek() == '.' {
		s.advance()
		s.readWord()
	}
}

// readQuotedIdentifier reads a double-quoted SQL identifier (e.g. "My Schema"
// or "select"), unescaping "" to a literal ". The opening " must not have
// been consumed yet. Returns the unquoted, unescaped identifier text.
func (s *state) readQuotedIdentifier() (string, error) {
	s.advance() // consume opening "
	var buf []byte
	for {
		if s.eof() {
			return "", fmt.Errorf("unterminated quoted identifier")
		}
		b := s.advance()
		if b == '"' {
			if s.peek() == '"' {
				buf = append(buf, '"')
				s.advance()
				continue
			}
			return string(buf), nil
		}
		buf = append(buf, b)
	}
}

// readWordOrQuotedIdentifier reads either a bare word or a double-quoted
// identifier, whichever is present at the current position.
func (s *state) readWordOrQuotedIdentifier() (string, error) {
	if s.peek() == '"' {
		return s.readQuotedIdentifier()
	}
	return s.readWord(), nil
}

// ── string / dollar-quote helpers ─────────────────────────────────────────────

// skipSingleQuoted advances past a single-quoted SQL string literal,
// handling ” escape sequences. The opening ' must NOT have been consumed yet.
func (s *state) skipSingleQuoted() error {
	s.advance() // consume opening '
	for !s.eof() {
		b := s.advance()
		if b == '\'' {
			if s.peek() == '\'' {
				s.advance() // '' escape: skip second quote and continue
			} else {
				return nil
			}
		}
	}
	return fmt.Errorf("unterminated string literal")
}

// peekDollarTag checks whether the current position is the start of a
// dollar-quoted string (either $$ or $tag$) and returns the opening tag.
func (s *state) peekDollarTag() (string, bool) {
	if s.peek() != '$' {
		return "", false
	}
	next := s.peekAt(1)
	if next == '$' {
		return "$$", true
	}
	if !isWordStart(next) {
		return "", false
	}
	for i := s.pos + 1; i < len(s.src); i++ {
		b := s.src[i]
		if b == '$' {
			return string(s.src[s.pos : i+1]), true
		}
		if !isWordChar(b) {
			return "", false
		}
	}
	return "", false
}

// skipDollarQuoted consumes a dollar-quoted string including its opening and
// closing tags. tag is the opening delimiter (e.g. "$$" or "$body$").
func (s *state) skipDollarQuoted(tag string) error {
	for range tag {
		s.advance()
	}
	tagBytes := []byte(tag)
	for !s.eof() {
		if s.pos+len(tagBytes) <= len(s.src) &&
			string(s.src[s.pos:s.pos+len(tagBytes)]) == tag {
			for range tag {
				s.advance()
			}
			return nil
		}
		s.advance()
	}
	return fmt.Errorf("unterminated dollar-quoted string %s", tag)
}

// skipLineComment advances to the end of a -- line comment (not consuming \n).
func (s *state) skipLineComment() {
	for !s.eof() && s.peek() != '\n' {
		s.advance()
	}
}

// skipBlockComment advances past a /* */ block comment.
// The opening / must NOT have been consumed.
func (s *state) skipBlockComment() {
	s.advance() // /
	s.advance() // *
	for !s.eof() {
		if s.peek() == '*' && s.peekAt(1) == '/' {
			s.advance()
			s.advance()
			return
		}
		s.advance()
	}
}

// ── generic content readers ───────────────────────────────────────────────────

// readRawUntil reads raw bytes until a byte in stopChars is found at
// paren-depth 0, outside string literals, dollar-quoted strings, and comments.
// The stop character is NOT consumed. Returns an error if EOF is reached first.
// errUnterminatedRawText is readRawUntil's EOF-without-stop-char error,
// distinguished from its other failure modes (an unterminated string
// literal or dollar-quoted body, surfaced via skipSingleQuoted/
// skipDollarQuoted's own errors) so a caller reading a Part-1-ending "{;"
// span can upgrade specifically this case to a clean DPG-E038 diagnostic
// (RFC Section 4.5's "; mandatory when no { } block follows") without
// mislabeling a genuinely different syntax error the same way.
var errUnterminatedRawText = errors.New("unexpected EOF before a terminator")

func (s *state) readRawUntil(stopChars string) (string, error) {
	start := s.pos
	depth := 0
	for !s.eof() {
		b := s.peek()
		if depth == 0 && strings.ContainsRune(stopChars, rune(b)) {
			return string(s.src[start:s.pos]), nil
		}
		switch b {
		case '(':
			depth++
			s.advance()
		case ')':
			if depth > 0 {
				depth--
			}
			s.advance()
		case '\'':
			if err := s.skipSingleQuoted(); err != nil {
				return "", err
			}
		case '$':
			if tag, ok := s.peekDollarTag(); ok {
				if err := s.skipDollarQuoted(tag); err != nil {
					return "", err
				}
			} else {
				s.advance()
			}
		case '-':
			if s.peekAt(1) == '-' {
				s.skipLineComment()
			} else {
				s.advance()
			}
		case '/':
			if s.peekAt(1) == '*' {
				s.skipBlockComment()
			} else {
				s.advance()
			}
		default:
			s.advance()
		}
	}
	return "", fmt.Errorf("%w; expected one of: %q", errUnterminatedRawText, stopChars)
}

// readBraceBlock reads the content of a { } block, tracking nested braces,
// parens, strings, dollar-quotes, and comments. The opening { must already
// have been consumed. Returns the raw inner text (excluding the braces).
func (s *state) readBraceBlock() (string, error) {
	start := s.pos
	depth := 1
	for !s.eof() {
		b := s.peek()
		switch b {
		case '{':
			depth++
			s.advance()
		case '}':
			depth--
			if depth == 0 {
				text := string(s.src[start:s.pos])
				s.advance() // consume closing }
				return text, nil
			}
			s.advance()
		case '\'':
			if err := s.skipSingleQuoted(); err != nil {
				return "", err
			}
		case '$':
			if tag, ok := s.peekDollarTag(); ok {
				if err := s.skipDollarQuoted(tag); err != nil {
					return "", err
				}
			} else {
				s.advance()
			}
		case '-':
			if s.peekAt(1) == '-' {
				s.skipLineComment()
			} else {
				s.advance()
			}
		case '/':
			if s.peekAt(1) == '*' {
				s.skipBlockComment()
			} else {
				s.advance()
			}
		default:
			s.advance()
		}
	}
	return "", fmt.Errorf("unterminated { } block")
}

// readFunctionPart1 reads a FUNCTION or PROCEDURE Part 1, which includes the
// full PG SQL signature and the dollar-quoted body, ending at (but not
// including) the closing "$$" or "$tag$" that terminates the body. Unlike
// the generic readRawUntil("{;") path used by every other kind, this can't
// stop at the first ';'/'{' it sees, since either may appear literally
// inside the dollar-quoted body — but it must otherwise behave the same way
// those kinds do: it does NOT consume a following ';', leaving that to
// scanBody's shared "consume a terminating ';' if present" logic. A prior
// version consumed the ';' here and returned it as part of Part1 itself,
// which double-terminated on re-format: the renderer always appends its own
// ';' when Part2 is empty, so a Part1 that already ended in ';' rendered as
// ";;", worsening by one extra ';' every time `dpg fmt` ran.
func (s *state) readFunctionPart1() (string, error) {
	start := s.pos
	// inAtomic/caseDepth track RFC audit item #28's "BEGIN ATOMIC ... END"
	// form (PG14+, no dollar-quote at all — see the AS-string-literal/
	// BEGIN-ATOMIC branches below). caseDepth counts nested CASE ... END
	// expressions inside the atomic body's own SQL statements (real
	// PostgreSQL SQL, not PL/pgSQL, so no nested BEGIN blocks are possible
	// there) so the true closing END — the one at caseDepth 0 — isn't
	// mistaken for one of those.
	inAtomic := false
	caseDepth := 0
	for !s.eof() {
		b := s.peek()
		switch {
		case b == '$':
			if tag, ok := s.peekDollarTag(); ok {
				if err := s.skipDollarQuoted(tag); err != nil {
					return "", err
				}
				return strings.TrimSpace(string(s.src[start:s.pos])), nil
			}
			s.advance()
		case b == '\'':
			if err := s.skipSingleQuoted(); err != nil {
				return "", err
			}
		case b == '-' && s.peekAt(1) == '-':
			s.skipLineComment()
		case b == '/' && s.peekAt(1) == '*':
			s.skipBlockComment()
		case isWordStart(b):
			switch strings.ToUpper(s.readWord()) {
			case "BEGIN":
				if !inAtomic {
					c := s.cur()
					s.skipWS()
					if strings.EqualFold(s.peekWord(), "ATOMIC") {
						s.readWord()
						inAtomic = true
					} else {
						s.restore(c)
					}
				}
			case "CASE":
				if inAtomic {
					caseDepth++
				}
			case "END":
				if inAtomic {
					if caseDepth > 0 {
						caseDepth--
					} else {
						return strings.TrimSpace(string(s.src[start:s.pos])), nil
					}
				}
			}
		case (b == ';' || b == '{') && !inAtomic:
			// RFC audit item #27: the "AS 'obj_file'[, 'link_symbol']" form
			// (LANGUAGE C/internal) has no dollar-quote and no BEGIN
			// ATOMIC body to bound it — its own trailing string literal(s)
			// are already skipped whole by skipSingleQuoted above, so a
			// bare top-level ';' or '{' genuinely marks the statement's
			// natural end here, the same way the generic
			// readRawUntil("{;") path every other object kind uses already
			// treats it (any earlier single-quoted default-value literal
			// containing either character is already protected by
			// skipSingleQuoted above, so a raw, unquoted occurrence here
			// can only be the real terminator). Left unconsumed, matching
			// this function's existing "does NOT consume a following ';'
			// or '{'" contract (see scanBody/readOptionalPart2).
			return strings.TrimSpace(string(s.src[start:s.pos])), nil
		default:
			s.advance()
		}
	}
	return "", fmt.Errorf("unexpected EOF in function/procedure body (missing closing dollar-quote?)")
}

// readOptionalPart2 reads the optional trailing { } block that follows a Part 1.
// If the next non-whitespace character is '{', it is consumed and the block
// content is returned along with has=true — even when the block is
// genuinely empty ("{ }"), which reads as content "" but is still a block
// that was present, distinct from no block at all. Otherwise "", false is
// returned without advancing.
func (s *state) readOptionalPart2() (text string, has bool, err error) {
	s.skipWS()
	if s.peek() != '{' {
		return "", false, nil
	}
	s.advance() // consume '{'
	text, err = s.readBraceBlock()
	return text, true, err
}

// ── kind detection ────────────────────────────────────────────────────────────

// detectKind reads the leading keyword sequence and returns the ObjectKind.
// The scanner position is left immediately after all kind keywords.
// Returns an error for unknown keywords.
func (s *state) detectKind(pos pipeline.SourcePos) (pipeline.ObjectKind, error) {
	word := s.readWord()
	if word == "" {
		return pipeline.KindUnknown, pipeline.Errorf(pos, "unexpected character %q", s.peek())
	}

	switch strings.ToUpper(word) {
	case "CREATE", "ALTER", "DROP":
		return pipeline.KindUnknown, pipeline.ErrorfCode(pos, "DPG-E006",
			"%s is not allowed at declaration level — DPG source describes desired state, not commands",
			strings.ToUpper(word))
	case "TEMPORARY":
		s.skipWS()
		s.readWord() // consume TABLE (or whatever follows)
		return pipeline.KindUnknown, pipeline.ErrorfCode(pos, "DPG-E023", "TEMPORARY TABLE is not supported by DPG")

	case "TABLE":
		return pipeline.KindTable, nil
	case "UNLOGGED":
		s.skipWS()
		next := s.readWord()
		switch strings.ToUpper(next) {
		case "TABLE":
			return pipeline.KindUnloggedTable, nil
		case "SEQUENCE":
			return pipeline.KindUnloggedSequence, nil
		default:
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected TABLE or SEQUENCE after UNLOGGED, got %q", next)
		}
	case "FOREIGN":
		s.skipWS()
		next := s.readWord()
		switch strings.ToUpper(next) {
		case "TABLE":
			return pipeline.KindForeignTable, nil
		case "DATA":
			s.skipWS()
			if w := s.readWord(); strings.ToUpper(w) != "WRAPPER" {
				return pipeline.KindUnknown, pipeline.Errorf(pos, "expected WRAPPER after FOREIGN DATA, got %q", w)
			}
			return pipeline.KindFDW, nil
		default:
			return pipeline.KindUnknown, pipeline.Errorf(pos, "unexpected keyword after FOREIGN: %q", next)
		}

	case "VIEW":
		return pipeline.KindView, nil
	case "MATERIALIZED":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "VIEW" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected VIEW after MATERIALIZED, got %q", w)
		}
		return pipeline.KindMaterializedView, nil
	case "RECURSIVE":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "VIEW" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected VIEW after RECURSIVE, got %q", w)
		}
		return pipeline.KindRecursiveView, nil

	case "FUNCTION":
		return pipeline.KindFunction, nil
	case "PROCEDURE":
		return pipeline.KindProcedure, nil
	case "AGGREGATE":
		return pipeline.KindAggregate, nil

	case "ENUM":
		return pipeline.KindUnknown, pipeline.Errorf(pos,
			"enum types are declared as TYPE name AS ENUM (...), matching PostgreSQL's CREATE TYPE")
	case "TYPE":
		return s.detectTypeKind(pos)
	case "DOMAIN":
		return pipeline.KindDomainType, nil

	case "SCHEMA":
		return pipeline.KindSchema, nil
	case "EXTENSION":
		return pipeline.KindExtension, nil
	case "SEQUENCE":
		return pipeline.KindSequence, nil

	case "ROLE":
		return pipeline.KindRole, nil
	case "TABLESPACE":
		return pipeline.KindTablespace, nil

	case "SERVER":
		return pipeline.KindServer, nil
	case "USER":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "MAPPING" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected MAPPING after USER, got %q", w)
		}
		return pipeline.KindUserMapping, nil

	case "PUBLICATION":
		return pipeline.KindPublication, nil
	case "SUBSCRIPTION":
		return pipeline.KindSubscription, nil
	case "EVENT":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "TRIGGER" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected TRIGGER after EVENT, got %q", w)
		}
		return pipeline.KindEventTrigger, nil

	case "COLLATION":
		return pipeline.KindCollation, nil
	case "OPERATOR":
		s.skipWS()
		c := s.cur()
		next := s.readWord()
		switch strings.ToUpper(next) {
		case "CLASS":
			return pipeline.KindOperatorClass, nil
		case "FAMILY":
			return pipeline.KindOperatorFamily, nil
		default:
			s.restore(c) // operator symbol follows; don't consume
			return pipeline.KindOperator, nil
		}

	case "CAST":
		return pipeline.KindCast, nil
	case "STATISTICS":
		return pipeline.KindStatisticsObject, nil

	case "TEXT":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "SEARCH" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected SEARCH after TEXT, got %q", w)
		}
		s.skipWS()
		sub := s.readWord()
		switch strings.ToUpper(sub) {
		case "CONFIGURATION":
			return pipeline.KindTSConfig, nil
		case "DICTIONARY":
			return pipeline.KindTSDict, nil
		case "PARSER":
			return pipeline.KindTSParser, nil
		case "TEMPLATE":
			return pipeline.KindTSTemplate, nil
		default:
			return pipeline.KindUnknown, pipeline.Errorf(pos,
				"expected CONFIGURATION/DICTIONARY/PARSER/TEMPLATE after TEXT SEARCH, got %q", sub)
		}

	case "DEFAULT":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "PRIVILEGES" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected PRIVILEGES after DEFAULT, got %q", w)
		}
		return pipeline.KindDefaultPrivileges, nil

	case "PARAMETER":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "PRIVILEGES" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected PRIVILEGES after PARAMETER, got %q", w)
		}
		return pipeline.KindParameterPrivileges, nil

	case "VIRTUAL":
		s.skipWS()
		if w := s.readWord(); strings.ToUpper(w) != "TYPE" {
			return pipeline.KindUnknown, pipeline.Errorf(pos, "expected TYPE after VIRTUAL, got %q", w)
		}
		return pipeline.KindVirtualType, nil

	default:
		return pipeline.KindUnknown, pipeline.Errorf(pos, "unrecognised declaration keyword %q%s", word, pipeline.RenamedKeywordHint(strings.ToUpper(word)))
	}
}

// detectTypeKind disambiguates TYPE into composite, range, or base by peeking
// at what follows the name. The TYPE keyword has already been consumed.
func (s *state) detectTypeKind(pos pipeline.SourcePos) (pipeline.ObjectKind, error) {
	// Read the type name (we must restore it so readPart1 includes it).
	// The name follows RFC's schema-name production
	// (identifier *("." identifier)) — a schema-qualified TYPE declaration
	// like "TYPE archive.mood AS ENUM (...)" is valid grammar, so every
	// dotted segment must be consumed here too, not just the first one:
	// readWord() alone stops at the "." (not a word char), leaving it
	// unconsumed and making the immediately-following skipWS()/peekWord()
	// see "." as the next token — peekWord() returns "" for that (it only
	// captures word characters), which previously made every qualified
	// TYPE name fail with "cannot determine TYPE variant; expected AS or
	// (, got \"\"" regardless of the real variant that followed.
	c := s.cur()
	s.skipWS()
	s.skipDottedName() // name
	s.skipWS()
	// peekWord() only captures word characters — it silently returns "" when
	// the next token is "(" (BASE type's own form, RFC Section 5.5's "TYPE name
	// (INPUT = ..., ...)"), so that case must be checked directly against the
	// next byte, not against peekWord()'s result. This previously meant a
	// BASE type could never be recognised at all — detectTypeKind always
	// fell through to the "expected AS or (" error for the exact syntax that
	// error message claims to accept, whether hand-written or dumped.
	nextIsParen := s.peek() == '('
	peeked := strings.ToUpper(s.peekWord())
	s.restore(c) // restore so Part1 includes the name

	switch {
	case nextIsParen:
		return pipeline.KindBaseType, nil
	case peeked == "AS":
		// Could be composite (AS (...)) or range (AS RANGE (...)).
		// Save and peek two words ahead.
		c2 := s.cur()
		s.skipWS()
		s.skipDottedName() // name
		s.skipWS()
		s.readWord() // AS
		s.skipWS()
		after := strings.ToUpper(s.peekWord())
		s.restore(c2)
		switch after {
		case "RANGE":
			return pipeline.KindRangeType, nil
		case "ENUM":
			return pipeline.KindEnum, nil
		}
		return pipeline.KindCompositeType, nil
	default:
		return pipeline.KindUnknown, pipeline.Errorf(pos, "cannot determine TYPE variant; expected AS or (, got %q", peeked)
	}
}

// ── schema attribute detection ────────────────────────────────────────────────

// schemaAttrKeywords are the keywords that, when found inside a SCHEMA { } body,
// are DPG attribute directives belonging to the schema itself rather than nested
// object declarations.
var schemaAttrKeywords = func() map[string]bool {
	words := []string{
		"OWNER", "COMMENT", "RENAMED", "GRANTS", "GRANT",
		"REVOKES", "REVOKE", "DEPRECATED", "PROTECTED", "SECURITY",
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()

// tryReadSchemaAttr tries to read one DPG attribute directive from inside a
// SCHEMA { } body. If the current keyword is a schema attribute, it reads the
// full directive (including its ';' or '{ }' terminator) and returns the raw
// text and true. Otherwise it restores the cursor and returns ("", false, nil).
func (s *state) tryReadSchemaAttr() (string, bool, error) {
	c := s.cur()
	start := s.pos
	word := s.readWord()
	if !schemaAttrKeywords[strings.ToUpper(word)] {
		s.restore(c)
		return "", false, nil
	}

	upper := strings.ToUpper(word)
	hasBlock := upper == "GRANTS" || upper == "GRANT" ||
		upper == "REVOKES" || upper == "REVOKE"

	var err error
	if hasBlock {
		// These may end with { } or just ;.
		if _, err = s.readRawUntil("{;"); err != nil {
			return "", false, err
		}
		if s.peek() == '{' {
			s.advance()
			if _, err = s.readBraceBlock(); err != nil {
				return "", false, err
			}
		} else {
			s.advance() // consume ;
		}
	} else {
		if _, err = s.readRawUntil(";"); err != nil {
			return "", false, err
		}
		s.advance() // consume ;
	}

	return string(s.src[start:s.pos]), true, nil
}

// ── main scan loop ────────────────────────────────────────────────────────────

// scanBody scans zero or more declarations from the current position.
//
//   - schemaName: non-empty when scanning inside a SCHEMA { } body.
//   - stopAtBrace: true when scanning inside a SCHEMA { } body; the loop stops
//     when it sees '}' (which the caller consumes).
//
// Returns:
//   - the emitted RawObjects (including recursively nested schema objects)
//   - the raw schema attribute text for the enclosing SCHEMA's Part2 (only
//     meaningful when stopAtBrace==true)
//   - any error
func (s *state) scanBody(schemaName string, stopAtBrace bool) ([]pipeline.RawObject, string, error) {
	var objects []pipeline.RawObject
	var attrBuf strings.Builder

	for {
		s.skipWS()
		if s.eof() || (stopAtBrace && s.peek() == '}') {
			break
		}

		declPos := s.srcPos()

		// Inside a schema body, try to read a schema attribute directive first.
		if stopAtBrace {
			attr, ok, err := s.tryReadSchemaAttr()
			if err != nil {
				return nil, "", err
			}
			if ok {
				attrBuf.WriteString(attr)
				attrBuf.WriteByte('\n')
				continue
			}
		}

		// Detect the object kind (also enforces no-verb mandate).
		kind, err := s.detectKind(declPos)
		if err != nil {
			return nil, "", err
		}

		// SCHEMA is handled specially: recurse into its body.
		if kind == pipeline.KindSchema {
			nested, schemaObj, err := s.readSchemaDecl(declPos)
			if err != nil {
				return nil, "", err
			}
			objects = append(objects, schemaObj)
			objects = append(objects, nested...)
			continue
		}

		// All other objects.
		part1, err := s.readPart1(kind, declPos)
		if err != nil {
			return nil, "", err
		}
		// RFC Section 4.5 (Rules T1-T3, unified): a ';' immediately before an
		// immediately-following '{ }' block is forbidden for every kind
		// (DPG-E037); when no block follows at all, a ';' terminator is
		// mandatory (DPG-E038). This is the single, kind-agnostic
		// enforcement point for both directions — it runs identically
		// whichever readPart1 branch produced part1 above (readRawUntil's
		// "{;"-based kinds always stop exactly at ';' or '{'; the function/
		// procedure branch stops right after its closing dollar-quote/END
		// with no guarantee about what follows, which is what made the
		// missing-mandatory-semicolon direction a real, previously
		// unenforced gap for that kind specifically).
		s.skipWS()
		sawSemi := false
		if s.peek() == ';' {
			sawSemi = true
			s.advance()
			s.skipWS()
		}
		blockFollows := s.peek() == '{'
		switch {
		case sawSemi && blockFollows:
			return nil, "", pipeline.ErrorfCode(declPos, "DPG-E037",
				"unexpected ';' before '{ }' block (Rule T1/T2/T3, Section 4.5): remove it — a semicolon must never appear immediately before a block")
		case !sawSemi && !blockFollows:
			return nil, "", pipeline.ErrorfCode(declPos, "DPG-E038",
				"missing ';' terminator (Rule T1/T2/T3, Section 4.5): a declaration with no { } block must end with ';'")
		}
		part2, hasPart2, err := s.readOptionalPart2()
		if err != nil {
			return nil, "", err
		}
		// Consume an optional trailing ';' after a '{ }' block.
		s.skipWS()
		if s.peek() == ';' {
			s.advance()
		}

		objects = append(objects, pipeline.RawObject{
			Kind:     kind,
			Part1:    part1,
			Part2:    part2,
			HasPart2: hasPart2,
			Schema:   schemaName,
			Pos:      declPos,
		})
	}

	return objects, strings.TrimSpace(attrBuf.String()), nil
}

// readSchemaDecl reads a complete SCHEMA name { ... } declaration. The SCHEMA
// keyword has already been consumed by detectKind. It returns:
//   - the nested RawObjects found inside the schema body
//   - the schema's own RawObject (with schema DPG attributes as Part2)
func (s *state) readSchemaDecl(pos pipeline.SourcePos) (nested []pipeline.RawObject, schemaObj pipeline.RawObject, err error) {
	s.skipWS()
	quoted := s.peek() == '"'
	name, err := s.readWordOrQuotedIdentifier()
	if err != nil {
		return nil, pipeline.RawObject{}, pipeline.Errorf(s.srcPos(), "%s", err.Error())
	}
	if name == "" {
		return nil, pipeline.RawObject{}, pipeline.Errorf(pos, "expected schema name after SCHEMA")
	}
	// part1 feeds into "CREATE SCHEMA <part1>" for pg_query parsing (see
	// pgparser.Reconstruct), so a name that was quoted in source (reserved
	// word or special characters) must stay quoted here — pg_query itself
	// unescapes it back to the same value scanBody uses as schemaName below.
	part1 := name
	if quoted {
		part1 = `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}

	s.skipWS()
	if s.peek() != '{' {
		return nil, pipeline.RawObject{}, pipeline.Errorf(s.srcPos(),
			"expected '{' after SCHEMA %s, got %q", name, s.peek())
	}
	s.advance() // consume '{'

	// Recursively scan the schema body.
	nestedObjs, schemaAttrs, err := s.scanBody(name, true)
	if err != nil {
		return nil, pipeline.RawObject{}, err
	}

	// Consume the closing '}'.
	s.skipWS()
	if s.peek() != '}' {
		return nil, pipeline.RawObject{}, pipeline.Errorf(s.srcPos(),
			"expected '}' to close SCHEMA %s", name)
	}
	s.advance()

	obj := pipeline.RawObject{
		Kind:  pipeline.KindSchema,
		Part1: part1,
		Part2: schemaAttrs,
		Pos:   pos,
	}
	return nestedObjs, obj, nil
}

// part1EOFErr upgrades a readRawUntil("{;") EOF failure to a clean
// DPG-E038 diagnostic (RFC Section 4.5, Rules T1/T3: a ';' terminator is
// mandatory when no '{ }' block follows) — leaving any other readRawUntil
// error (an unterminated string literal or dollar-quoted body) as-is,
// since those are a genuinely different problem, not a missing terminator.
func part1EOFErr(pos pipeline.SourcePos, err error) error {
	if errors.Is(err, errUnterminatedRawText) {
		return pipeline.ErrorfCode(pos, "DPG-E038",
			"missing ';' terminator (Rule T1/T3, Section 4.5): a declaration with no { } block must end with ';'")
	}
	return pipeline.Errorf(pos, "%v", err)
}

// readPart1 reads the Part 1 text for the given kind. The kind keyword(s) have
// already been consumed. The returned string is trimmed of leading/trailing
// whitespace; internal whitespace and comments are preserved verbatim.
func (s *state) readPart1(kind pipeline.ObjectKind, pos pipeline.SourcePos) (string, error) {
	switch kind {
	case pipeline.KindFunction, pipeline.KindProcedure:
		return s.readFunctionPart1()

	case pipeline.KindTable,
		pipeline.KindUnloggedTable,
		pipeline.KindForeignTable,
		pipeline.KindAggregate,
		pipeline.KindBaseType:
		// Part 1 ends at '{' or ';' at paren depth 0. The '(' paren block(s)
		// are part of Part 1 and are tracked correctly by readRawUntil.
		text, err := s.readRawUntil("{;")
		if err != nil {
			return "", part1EOFErr(pos, err)
		}
		return strings.TrimSpace(text), nil

	case pipeline.KindSchema:
		// Handled by readSchemaDecl; should not reach here.
		return "", pipeline.Errorf(pos, "internal: readPart1 called for SCHEMA")

	default:
		// For all other kinds, Part 1 is everything up to '{' or ';'.
		// (Views, enums, extensions, sequences, roles, etc.)
		text, err := s.readRawUntil("{;")
		if err != nil {
			return "", part1EOFErr(pos, err)
		}
		return strings.TrimSpace(text), nil
	}
}

// ── utilities used by tests ───────────────────────────────────────────────────

// KindNames returns a sorted slice of all ObjectKind string representations
// for use in diagnostics and tests.
func KindNames() []string {
	names := []string{
		pipeline.KindSchema.String(),
		pipeline.KindExtension.String(),
		pipeline.KindTable.String(),
		pipeline.KindUnloggedTable.String(),
		pipeline.KindForeignTable.String(),
		pipeline.KindView.String(),
		pipeline.KindMaterializedView.String(),
		pipeline.KindRecursiveView.String(),
		pipeline.KindFunction.String(),
		pipeline.KindProcedure.String(),
		pipeline.KindAggregate.String(),
		pipeline.KindEnum.String(),
		pipeline.KindCompositeType.String(),
		pipeline.KindRangeType.String(),
		pipeline.KindDomainType.String(),
		pipeline.KindBaseType.String(),
		pipeline.KindSequence.String(),
		pipeline.KindUnloggedSequence.String(),
		pipeline.KindRole.String(),
		pipeline.KindTablespace.String(),
		pipeline.KindFDW.String(),
		pipeline.KindServer.String(),
		pipeline.KindUserMapping.String(),
		pipeline.KindPublication.String(),
		pipeline.KindSubscription.String(),
		pipeline.KindEventTrigger.String(),
		pipeline.KindCollation.String(),
		pipeline.KindOperator.String(),
		pipeline.KindOperatorClass.String(),
		pipeline.KindOperatorFamily.String(),
		pipeline.KindCast.String(),
		pipeline.KindStatisticsObject.String(),
		pipeline.KindTSConfig.String(),
		pipeline.KindTSDict.String(),
		pipeline.KindTSParser.String(),
		pipeline.KindTSTemplate.String(),
		pipeline.KindDefaultPrivileges.String(),
		pipeline.KindParameterPrivileges.String(),
		pipeline.KindVirtualType.String(),
	}
	sort.Strings(names)
	return names
}
