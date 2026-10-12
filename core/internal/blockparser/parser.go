// Package blockparser implements pipeline.BlockParser. It parses the raw text
// from a DPG { } block into a pipeline.BlockAST, handling all Part 2 directives
// defined in the DPG RFC Section 7.
package blockparser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyBlockParser, New())
}

// Parser implements pipeline.BlockParser.
type Parser struct{}

// New returns a Parser ready to use.
func New() *Parser { return &Parser{} }

// Parse implements pipeline.BlockParser. part2 is the raw text INSIDE the { }
// braces (not including the braces themselves). pos is the position of the
// original declaration.
func (p *Parser) Parse(kind pipeline.ObjectKind, part2 string, pos pipeline.SourcePos) (pipeline.BlockAST, error) {
	if strings.TrimSpace(part2) == "" {
		return pipeline.BlockAST{Pos: pos}, nil
	}
	bp := &blockParser{
		src:  []byte(part2),
		file: pos.File,
		line: pos.Line,
		col:  pos.Col,
	}
	return bp.parseBlock(pos)
}

// ParseDefaultPrivileges implements pipeline.BlockParser.
func (p *Parser) ParseDefaultPrivileges(header, body string, pos pipeline.SourcePos) (pipeline.DefaultPrivilegesBlock, error) {
	return ParseDefaultPrivileges(header, body, pos)
}

// ParseParameterPrivileges implements pipeline.BlockParser.
func (p *Parser) ParseParameterPrivileges(header, body string, pos pipeline.SourcePos) (pipeline.ParameterPrivilegesBlock, error) {
	return ParseParameterPrivileges(header, body, pos)
}

// ── internal parser ───────────────────────────────────────────────────────────

type blockParser struct {
	src  []byte
	pos  int
	file string
	line int
	col  int
}

type bpCursor struct{ pos, line, col int }

func (b *blockParser) cur() bpCursor { return bpCursor{b.pos, b.line, b.col} }
func (b *blockParser) restore(c bpCursor) {
	b.pos = c.pos
	b.line = c.line
	b.col = c.col
}

func (b *blockParser) eof() bool { return b.pos >= len(b.src) }

func (b *blockParser) peek() byte {
	if b.eof() {
		return 0
	}
	return b.src[b.pos]
}

func (b *blockParser) peekAt(n int) byte {
	if b.pos+n >= len(b.src) {
		return 0
	}
	return b.src[b.pos+n]
}

func (b *blockParser) advance() byte {
	if b.eof() {
		return 0
	}
	ch := b.src[b.pos]
	b.pos++
	if ch == '\n' {
		b.line++
		b.col = 1
	} else {
		b.col++
	}
	return ch
}

func (b *blockParser) srcPos() pipeline.SourcePos {
	return pipeline.SourcePos{File: b.file, Line: b.line, Col: b.col}
}

func (b *blockParser) errorf(format string, args ...any) error {
	pos := b.srcPos()
	return pipeline.Errorf(pos, format, args...)
}

// ── whitespace, comments, strings ────────────────────────────────────────────

func isWordStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}
func isWordChar(ch byte) bool { return isWordStart(ch) || (ch >= '0' && ch <= '9') }
func isDigit(ch byte) bool    { return ch >= '0' && ch <= '9' }

func (b *blockParser) skipWS() {
	for !b.eof() {
		switch b.peek() {
		case ' ', '\t', '\r', '\n':
			b.advance()
		case '-':
			if b.peekAt(1) == '-' {
				for !b.eof() && b.peek() != '\n' {
					b.advance()
				}
			} else {
				return
			}
		case '/':
			if b.peekAt(1) == '*' {
				b.advance()
				b.advance()
				for !b.eof() {
					if b.peek() == '*' && b.peekAt(1) == '/' {
						b.advance()
						b.advance()
						break
					}
					b.advance()
				}
			} else {
				return
			}
		default:
			return
		}
	}
}

func (b *blockParser) readWord() string {
	var buf []byte
	for !b.eof() && isWordChar(b.peek()) {
		buf = append(buf, b.advance())
	}
	return string(buf)
}

func (b *blockParser) peekWord() string {
	c := b.cur()
	w := b.readWord()
	b.restore(c)
	return w
}

// readQuotedString reads a double-quoted DPG string literal (RFC Section 3).
// The opening " must NOT have been consumed. Returns the unquoted content.
func (b *blockParser) readQuotedString() (string, error) {
	if b.peek() != '"' {
		return "", b.errorf("expected '\"', got %q", b.peek())
	}
	b.advance() // consume "
	var buf []byte
	for !b.eof() {
		ch := b.advance()
		if ch == '"' {
			if b.peek() == '"' {
				buf = append(buf, '"')
				b.advance()
			} else {
				return string(buf), nil
			}
		} else {
			buf = append(buf, ch)
		}
	}
	return "", b.errorf("unterminated string literal")
}

// readSingleQuotedString reads a SQL single-quoted string. Opening ' must not be consumed.
func (b *blockParser) readSingleQuotedString() (string, error) {
	if b.peek() != '\'' {
		return "", b.errorf("expected \"'\", got %q", b.peek())
	}
	b.advance()
	var buf []byte
	for !b.eof() {
		ch := b.advance()
		if ch == '\'' {
			if b.peek() == '\'' {
				buf = append(buf, '\'')
				b.advance()
			} else {
				return string(buf), nil
			}
		} else {
			buf = append(buf, ch)
		}
	}
	return "", b.errorf("unterminated string literal")
}

// expect reads the next word and errors if it doesn't match.
func (b *blockParser) expect(word string) error {
	b.skipWS()
	got := b.readWord()
	if !strings.EqualFold(got, word) {
		return b.errorf("expected %s, got %q", word, got)
	}
	return nil
}

// expectSemi consumes the trailing ';' after a directive.
func (b *blockParser) expectSemi() error {
	b.skipWS()
	if b.peek() != ';' {
		return b.errorf("expected ';' after directive, got %q", b.peek())
	}
	b.advance()
	return nil
}

// parseTrailingCommentBlock parses an INDEXES/POLICIES/TRIGGERS/CONSTRAINTS
// entry's terminator: a bare ";" (no comment), or a "{ COMMENT '...'; }"
// block — the same "bare ; vs { } block" convention every other
// comment-bearing kind already uses (mirrors dump's writeViewBlock/
// writeFuncBlock rendering), rather than a bespoke trailing clause. None of
// these four sub-kinds support any block-level directive today, so this is
// the minimal shared addition rather than one bespoke parser per kind.
func (b *blockParser) parseTrailingCommentBlock() (*pipeline.StringLit, error) {
	b.skipWS()
	if b.peek() == '{' {
		b.advance() // consume {
		b.skipWS()
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		if word != "COMMENT" {
			return nil, b.errorf("expected COMMENT inside block, got %q", word)
		}
		comment, err := b.parseStringDirective(dirPos)
		if err != nil {
			return nil, err
		}
		b.skipWS()
		if b.peek() != '}' {
			return nil, b.errorf("expected '}' to close block, got %q", b.peek())
		}
		b.advance()
		return comment, nil
	}
	if b.peek() == ';' {
		b.advance()
	}
	return nil, nil
}

// readIdentifier reads a (possibly schema-qualified) identifier.
// foldIdent applies PostgreSQL's unquoted-identifier case folding (RFC
// Section 4.10): ASCII letters are lowercased, everything else is kept, the
// same as PostgreSQL's own downcase_identifier for UTF-8 databases. Quoted
// identifiers are read separately and never folded.
// readRoleIdentifier reads a grantee in a role list. PUBLIC and the
// CURRENT_USER/CURRENT_ROLE/SESSION_USER role specs are keywords, kept
// uppercase so they compare equal to introspected grantees; every other
// name follows readIdentifier's folding rules.
func (b *blockParser) readRoleIdentifier() (pipeline.Identifier, error) {
	b.skipWS()
	if b.peek() != '"' {
		switch w := strings.ToUpper(b.peekWord()); w {
		case "PUBLIC", "CURRENT_USER", "CURRENT_ROLE", "SESSION_USER":
			b.readWord()
			return pipeline.Identifier{Name: w}, nil
		}
	}
	return b.readIdentifier()
}

// identToken turns one raw identifier token into the name PostgreSQL would
// resolve: a double-quoted token is unquoted (with "" unescaped), anything
// else is case-folded by foldIdent.
func identToken(tok string) string {
	if len(tok) >= 2 && tok[0] == '"' && tok[len(tok)-1] == '"' {
		return strings.ReplaceAll(tok[1:len(tok)-1], `""`, `"`)
	}
	return foldIdent(tok)
}

func foldIdent(word string) string {
	for i := 0; i < len(word); i++ {
		if c := word[i]; c >= 'A' && c <= 'Z' {
			buf := []byte(word)
			for j := i; j < len(buf); j++ {
				if buf[j] >= 'A' && buf[j] <= 'Z' {
					buf[j] += 'a' - 'A'
				}
			}
			return string(buf)
		}
	}
	return word
}

func (b *blockParser) readIdentifier() (pipeline.Identifier, error) {
	b.skipWS()
	var name string
	if b.peek() == '"' {
		s, err := b.readQuotedString()
		if err != nil {
			return pipeline.Identifier{}, err
		}
		name = s
	} else {
		name = foldIdent(b.readWord())
		if name == "" {
			return pipeline.Identifier{}, b.errorf("expected identifier, got %q", b.peek())
		}
	}
	// Check for schema.name
	if b.peek() == '.' {
		b.advance()
		b.skipWS()
		var n2 string
		if b.peek() == '"' {
			s, err := b.readQuotedString()
			if err != nil {
				return pipeline.Identifier{}, err
			}
			n2 = s
		} else {
			n2 = foldIdent(b.readWord())
		}
		return pipeline.Identifier{Schema: name, Name: n2}, nil
	}
	return pipeline.Identifier{Name: name}, nil
}

// readRawUntil reads raw bytes stopping at any byte in stopChars at brace/paren depth 0,
// outside strings and comments. Stop char is NOT consumed.
func (b *blockParser) readRawUntil(stopChars string) (string, error) {
	start := b.pos
	parenDepth := 0
	braceDepth := 0
	for !b.eof() {
		ch := b.peek()
		if parenDepth == 0 && braceDepth == 0 && strings.ContainsRune(stopChars, rune(ch)) {
			return string(b.src[start:b.pos]), nil
		}
		switch ch {
		case '(':
			parenDepth++
			b.advance()
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
			b.advance()
		case '{':
			braceDepth++
			b.advance()
		case '}':
			if braceDepth > 0 {
				braceDepth--
				b.advance()
			} else {
				return string(b.src[start:b.pos]), nil
			}
		case '\'':
			if _, err := b.readSingleQuotedString(); err != nil {
				return "", err
			}
		case '-':
			if b.peekAt(1) == '-' {
				for !b.eof() && b.peek() != '\n' {
					b.advance()
				}
			} else {
				b.advance()
			}
		case '/':
			if b.peekAt(1) == '*' {
				b.advance()
				b.advance()
				for !b.eof() {
					if b.peek() == '*' && b.peekAt(1) == '/' {
						b.advance()
						b.advance()
						break
					}
					b.advance()
				}
			} else {
				b.advance()
			}
		default:
			b.advance()
		}
	}
	return string(b.src[start:b.pos]), nil
}

// readBraceBlock reads the content of a { } block. The opening { must already
// have been consumed.
func (b *blockParser) readBraceBlock() (string, error) {
	start := b.pos
	depth := 1
	for !b.eof() {
		ch := b.peek()
		switch ch {
		case '{':
			depth++
			b.advance()
		case '}':
			depth--
			if depth == 0 {
				text := string(b.src[start:b.pos])
				b.advance() // consume }
				return text, nil
			}
			b.advance()
		case '\'':
			if _, err := b.readSingleQuotedString(); err != nil {
				return "", err
			}
		case '-':
			if b.peekAt(1) == '-' {
				for !b.eof() && b.peek() != '\n' {
					b.advance()
				}
			} else {
				b.advance()
			}
		case '/':
			if b.peekAt(1) == '*' {
				b.advance()
				b.advance()
				for !b.eof() {
					if b.peek() == '*' && b.peekAt(1) == '/' {
						b.advance()
						b.advance()
						break
					}
					b.advance()
				}
			} else {
				b.advance()
			}
		default:
			b.advance()
		}
	}
	return "", b.errorf("unterminated { } block")
}

// consumeBrace expects and consumes the next '{'.
func (b *blockParser) consumeBrace() error {
	b.skipWS()
	if b.peek() != '{' {
		return b.errorf("expected '{', got %q", b.peek())
	}
	b.advance()
	return nil
}

// ── top-level block parser ────────────────────────────────────────────────────

func (b *blockParser) parseBlock(pos pipeline.SourcePos) (pipeline.BlockAST, error) {
	ast := pipeline.BlockAST{Pos: pos}
	for {
		b.skipWS()
		if b.eof() {
			break
		}
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		if word == "" {
			return ast, b.errorf("unexpected character %q in block", b.peek())
		}

		var err error
		switch word {
		case "COMMENT":
			ast.Comment, err = b.parseStringDirective(dirPos)
		case "OWNER":
			ast.Owner, err = b.parseIdentDirective(dirPos)
		case "RENAMED":
			ast.RenamedFrom, err = b.parseRenamedFrom(dirPos)
		case "PROTECTED":
			ast.Protected = true
			err = b.expectSemi()
		case "DEPRECATED":
			ast.Deprecated, err = b.parseStringDirective(dirPos)
		case "DROP":
			err = b.parseDrop(&ast)
		case "ENABLE":
			err = b.parseEnableOrTriggerState(&ast, dirPos)
		case "DISABLED":
			// Section 14.1's EVENT TRIGGER trigger-enable-state, bare form —
			// same vocabulary as TriggerDef.EnableState, reused verbatim per
			// the RFC's own ABNF, just as a block directive here (see
			// BlockAST.TriggerEnableState's doc comment for why).
			ast.TriggerEnableState = "DISABLED"
			err = b.expectSemi()
		case "FORCE":
			err = b.parseForce(&ast, dirPos)
		case "REPLICA":
			ast.ReplicaIdentity, err = b.parseReplicaIdentity(dirPos)
		case "CLUSTER":
			ast.ClusterOn, err = b.parseClusterOn(dirPos)
		case "DETACHED":
			ast.DetachedFrom, err = b.parseDetachedFrom(dirPos)
		case "SET":
			// RFC audit item #74 (Section 11.1) — Role-only in practice.
			var rc pipeline.RoleConfigDir
			rc, err = b.parseRoleConfigSet(dirPos)
			ast.RoleConfigs = append(ast.RoleConfigs, rc)
		case "RESET":
			var rc pipeline.RoleConfigDir
			rc, err = b.parseRoleConfigReset(dirPos)
			ast.RoleConfigs = append(ast.RoleConfigs, rc)
		case "IN":
			// RFC audit item #32 (Section 11.1) — block-level "IN ROLE
			// identifier [WITH ...];", the only way to attach WITH ADMIN/
			// INHERIT/SET modifiers (real CREATE ROLE's inline IN ROLE
			// list accepts none, confirmed live).
			if err = b.expect("ROLE"); err != nil {
				return ast, err
			}
			var m pipeline.RoleMembershipDir
			m, err = b.parseMembershipEntry("IN_ROLE", false, dirPos)
			ast.Memberships = append(ast.Memberships, m)
		case "ROLE":
			var m pipeline.RoleMembershipDir
			m, err = b.parseMembershipEntry("ROLE", false, dirPos)
			ast.Memberships = append(ast.Memberships, m)
		case "ADMIN":
			var m pipeline.RoleMembershipDir
			m, err = b.parseMembershipEntry("ROLE", true, dirPos)
			ast.Memberships = append(ast.Memberships, m)
		case "VALUE":
			var r pipeline.EnumValueRenameDir
			r, err = b.parseEnumValueRename(dirPos)
			if err == nil {
				ast.EnumValueRenames = append(ast.EnumValueRenames, r)
			}
		case "RENAME":
			return ast, fmt.Errorf("%s: DPG-E024: RENAME VALUE is not a DPG directive; an enum value rename is written VALUE 'new' RENAMED FROM 'old';", dirPos)
		case "REFRESH":
			// RFC audit item #84: Collation-only REFRESH VERSION, a bare
			// presence keyword with no argument.
			b.skipWS()
			c := b.cur()
			w2 := strings.ToUpper(b.readWord())
			if w2 != "VERSION" {
				b.restore(c)
				return ast, b.errorf("expected VERSION after REFRESH, got %q", w2)
			}
			ast.RefreshVersion = true
			err = b.expectSemi()
		case "INDEXES":
			var indices []pipeline.IndexDef
			indices, err = b.parseIndices(dirPos)
			ast.Indices = append(ast.Indices, indices...)
		case "INDEX":
			// Mode B (Section 4.8 Dual Definition Modes): the singular keyword precedes
			// a single entry outside a plural block, so unlike INDEXES this must
			// not consume a wrapping '{ }'.
			var idx pipeline.IndexDef
			idx, err = b.parseOneIndex(false)
			ast.Indices = append(ast.Indices, idx)
		case "UNIQUE":
			// Mode B's "UNIQUE INDEX name (...)" form, mirroring real
			// PostgreSQL's CREATE UNIQUE INDEX order exactly (UNIQUE has no
			// other meaning as a bare top-level block directive). presetUnique
			// tells parseOneIndex UNIQUE was already consumed here, so it
			// doesn't try to read it again.
			b.skipWS()
			c := b.cur()
			w2 := strings.ToUpper(b.readWord())
			if w2 != "INDEX" {
				b.restore(c)
				return ast, b.errorf("expected INDEX after UNIQUE, got %q", w2)
			}
			var idx pipeline.IndexDef
			idx, err = b.parseOneIndex(true)
			ast.Indices = append(ast.Indices, idx)
		case "COLUMN":
			var col pipeline.ColumnBlock
			col, err = b.parseColumnBlock(dirPos)
			ast.Columns = append(ast.Columns, col)
		case "COLUMNS":
			var cols []pipeline.ColumnBlock
			cols, err = b.parseColumnsBlock(dirPos)
			ast.Columns = append(ast.Columns, cols...)
		case "ATTRIBUTE":
			var attr pipeline.ColumnBlock
			attr, err = b.parseAttributeBlock(dirPos)
			ast.Attributes = append(ast.Attributes, attr)
		case "ATTRIBUTES":
			var attrs []pipeline.ColumnBlock
			attrs, err = b.parseAttributesBlock(dirPos)
			ast.Attributes = append(ast.Attributes, attrs...)
		case "CONSTRAINT":
			var cst pipeline.ConstraintDef
			cst, err = b.parseConstraint(dirPos)
			ast.Constraints = append(ast.Constraints, cst)
		case "CONSTRAINTS":
			// Mode A (Section 4.8 Dual Definition Modes): plural block header wrapping
			// multiple entries, each omitting the singular keyword — completes
			// the pattern already offered for the other 7 collection types.
			var csts []pipeline.ConstraintDef
			csts, err = b.parseConstraintsBlock(dirPos)
			ast.Constraints = append(ast.Constraints, csts...)
		case "POLICIES":
			var policies []pipeline.PolicyDef
			policies, err = b.parsePolicies(dirPos)
			ast.Policies = append(ast.Policies, policies...)
		case "POLICY":
			// Mode B (Section 4.8 Dual Definition Modes): singular entry, no wrapping '{ }'.
			var pol pipeline.PolicyDef
			pol, err = b.parseOnePolicy()
			ast.Policies = append(ast.Policies, pol)
		case "TRIGGERS":
			var triggers []pipeline.TriggerDef
			triggers, err = b.parseTriggers(dirPos)
			ast.Triggers = append(ast.Triggers, triggers...)
		case "TRIGGER":
			// Mode B: singular entry, no wrapping '{ }'.
			var trig pipeline.TriggerDef
			trig, err = b.parseOneTrigger()
			ast.Triggers = append(ast.Triggers, trig)
		case "GRANTS":
			var grants []pipeline.GrantEntry
			grants, err = b.parseGrantsBlock(dirPos)
			ast.Grants = append(ast.Grants, grants...)
		case "GRANT":
			// Mode B (Section 4.8 Dual Definition Modes): singular entry, no wrapping '{ }'.
			var g pipeline.GrantEntry
			g, err = b.parseOneGrant(dirPos)
			ast.Grants = append(ast.Grants, g)
		case "REVOKES":
			var revs []pipeline.RevocationEntry
			revs, err = b.parseRevocationsBlock(dirPos)
			ast.Revocations = append(ast.Revocations, revs...)
		case "REVOKE":
			// Mode B: singular entry, no wrapping '{ }'.
			var r pipeline.RevocationEntry
			r, err = b.parseOneRevocation(dirPos)
			ast.Revocations = append(ast.Revocations, r)
		case "SECURITY":
			// Repeatable, one per label provider (RFC Section 14.11) — unlike
			// COMMENT, real PostgreSQL lets several independent providers
			// label the same object simultaneously.
			var sl pipeline.SecurityLabel
			sl, err = b.parseSecurityLabel(dirPos)
			ast.SecurityLabels = append(ast.SecurityLabels, sl)
		case "PARTITIONS":
			// PartitionDef wraps a list, so a PARTITIONS block must MERGE into
			// any partitions a prior Mode-B PARTITION entry already added —
			// assigning outright would silently discard them.
			var pd *pipeline.PartitionDef
			pd, err = b.parsePartitionsBlock(dirPos)
			if err == nil {
				if ast.Partitions == nil {
					ast.Partitions = pd
				} else {
					ast.Partitions.Partitions = append(ast.Partitions.Partitions, pd.Partitions...)
				}
			}
		case "PARTITION":
			// Mode B: singular entry, no wrapping '{ }'. PartitionDef wraps a list
			// (unlike Policies/Triggers/Indices/Grants/Revocations, PARTITIONS
			// isn't merged elsewhere), so lazily create it on first use.
			var bound pipeline.PartitionBound
			bound, err = b.parseOnePartitionBound()
			if err == nil {
				if ast.Partitions == nil {
					ast.Partitions = &pipeline.PartitionDef{Pos: dirPos}
				}
				ast.Partitions.Partitions = append(ast.Partitions.Partitions, bound)
			}
		case "MIGRATE":
			ast.MigrateRemove, err = b.parseMigrateRemove(dirPos)
		case "DEPENDS":
			var ext string
			ext, err = b.parseDependsOnExtension(dirPos)
			if err == nil {
				ast.DependsOnExtensions = append(ast.DependsOnExtensions, ext)
			}
		case "NO":
			// RFC Section 9.1's "NO DEPENDS ON EXTENSION ext;" — mirrors real
			// PostgreSQL's own ALTER FUNCTION grammar shape (which allows
			// removing a dependency the same way it was added) for
			// familiarity, but contributes nothing to DependsOnExtensions:
			// a purely declarative model already expresses "this function
			// does not depend on ext" by simply never listing it, the same
			// way Owner/Comment/every other directive here works — there
			// is no symmetric "explicit negative" story for those either.
			// Parsed (not rejected) so writing it doesn't error, matching
			// the passthrough principle used throughout this grammar.
			_, err = b.parseNoDependsOnExtension(dirPos)
		case "DEFAULT":
			// Overloaded keyword, disambiguated by what follows: "DEFAULT
			// PRIVILEGES ..." (existing) vs. RFC Section 5.4's DOMAIN-only bare
			// "DEFAULT expr;" directive. peekWord doesn't consume, so a
			// non-match falls through to parseDefaultPrivileges's own
			// b.expect("PRIVILEGES") — which would just fail with a
			// clearer error for an actually-malformed DEFAULT PRIVILEGES,
			// so this stays a strict two-way split rather than a silent
			// fallback.
			b.skipWS()
			if strings.ToUpper(b.peekWord()) == "PRIVILEGES" {
				var dp pipeline.DefaultPrivilegesBlock
				dp, err = b.parseDefaultPrivileges(dirPos)
				ast.DefaultPrivileges = append(ast.DefaultPrivileges, dp)
			} else {
				raw, e2 := b.readRawUntil(";")
				if e2 != nil {
					err = e2
				} else {
					ast.DomainDefault = &pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: dirPos}
					b.advance() // consume ;
				}
			}
		case "NOT":
			// RFC Section 5.4's DOMAIN-only "NOT NULL;" block directive.
			b.skipWS()
			w2 := strings.ToUpper(b.readWord())
			if w2 != "NULL" {
				return ast, b.errorf("expected NULL after NOT, got %q", w2)
			}
			ast.DomainNotNull = true
			err = b.expectSemi()
		case "MAPPING":
			var m pipeline.TSMappingDef
			m, err = b.parseTSMapping(dirPos)
			ast.Mappings = append(ast.Mappings, m)
		case "OPERATOR", "FUNCTION":
			// RFC Section 14.4's OPERATOR FAMILY { } members — real PG's own
			// ALTER OPERATOR FAMILY ... ADD list-item grammar, minus the
			// ALTER/family header. Each item restates its own OPERATOR/
			// FUNCTION keyword (unlike the real ALTER statement's single
			// "ADD" prefix), so this is dispatched exactly like every other
			// top-level directive rather than as a nested sub-list.
			var m pipeline.OpFamilyMember
			m, err = b.parseOpFamilyMember(dirPos, word == "FUNCTION")
			ast.OpFamilyMembers = append(ast.OpFamilyMembers, m)
		case "STATISTICS":
			// An extended statistics object's target (RFC Section 14.6);
			// DEFAULT parses to nil.
			var n *int
			n, err = b.parseStatisticsValue(dirPos)
			ast.StatisticsTarget = n
			ast.StatisticsTargetSet = err == nil
		case "PREFERRED":
			ast.PreferredJsonFormat, err = b.parsePreferredJsonFormat(dirPos)
		case "NAME":
			b.skipWS()
			w2 := strings.ToUpper(b.readWord())
			switch w2 {
			case "MAP":
				var entry pipeline.NameMapEntry
				entry, err = b.parseNameMapSingular(dirPos)
				if err == nil {
					ast.NameMaps = append(ast.NameMaps, entry)
				}
			case "MAPS":
				var entries []pipeline.NameMapEntry
				entries, err = b.parseNameMapsBlock(dirPos)
				if err == nil {
					ast.NameMaps = append(ast.NameMaps, entries...)
				}
			default:
				err = fmt.Errorf("%s: expected MAP or MAPS after NAME, got %q", dirPos, w2)
			}
		default:
			return ast, fmt.Errorf("%s: DPG-E024: unknown block directive %q%s", dirPos, word, pipeline.RenamedKeywordHint(word))
		}
		if err != nil {
			return ast, err
		}
	}
	ast.NameMaps, ast.NameMapWarnings = dedupeNameMapsLastWins(ast.NameMaps)
	return ast, nil
}

// ── simple directives ─────────────────────────────────────────────────────────

// parsePreferredJsonFormat reads: JSON FORMAT ( json | jsonb ) ;
func (b *blockParser) parsePreferredJsonFormat(pos pipeline.SourcePos) (string, error) {
	b.skipWS()
	if strings.ToUpper(b.readWord()) != "JSON" {
		return "", b.errorf("PREFERRED: expected JSON after PREFERRED at %s", pos)
	}
	b.skipWS()
	if strings.ToUpper(b.readWord()) != "FORMAT" {
		return "", b.errorf("PREFERRED JSON: expected FORMAT at %s", pos)
	}
	b.skipWS()
	val := strings.ToLower(b.readWord())
	if val != "json" && val != "jsonb" {
		return "", b.errorf("PREFERRED JSON FORMAT: expected 'json' or 'jsonb', got %q at %s", val, pos)
	}
	if err := b.expectSemi(); err != nil {
		return "", err
	}
	return val, nil
}

// parseStringDirective reads: 'text'; and returns a *StringLit.
func (b *blockParser) parseStringDirective(pos pipeline.SourcePos) (*pipeline.StringLit, error) {
	b.skipWS()
	val, err := b.readSingleQuotedString()
	if err != nil {
		return nil, err
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return &pipeline.StringLit{Value: val, Pos: pos}, nil
}

// parseIdentDirective reads: "name"; or name; and returns a *Identifier.
func (b *blockParser) parseIdentDirective(pos pipeline.SourcePos) (*pipeline.Identifier, error) {
	b.skipWS()
	var name string
	var err error
	if b.peek() == '"' {
		name, err = b.readQuotedString()
		if err != nil {
			return nil, err
		}
	} else {
		name = foldIdent(b.readWord())
		if name == "" {
			return nil, b.errorf("expected identifier after directive")
		}
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return &pipeline.Identifier{Name: name}, nil
}

// parseConfigValue reads a role-config-dir config-value (Appendix A): a
// single-quoted string literal or a bare token (integer/boolean/
// identifier) — returned as literal SQL text exactly as written (a string
// literal keeps its quotes), the same passthrough convention
// pipeline.StorageParam.Value already uses, rendered back verbatim rather
// than re-interpreted.
func (b *blockParser) parseConfigValue() (string, error) {
	b.skipWS()
	start := b.pos
	if b.peek() == '\'' {
		if _, err := b.readSingleQuotedString(); err != nil {
			return "", err
		}
		return string(b.src[start:b.pos]), nil
	}
	for !b.eof() {
		c := b.peek()
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' {
			break
		}
		b.advance()
	}
	if b.pos == start {
		return "", b.errorf("expected a config value")
	}
	return string(b.src[start:b.pos]), nil
}

// parseOptionalInDatabase reads an optional trailing "IN DATABASE db"
// qualifier shared by every role-config-dir form, setting d.InDatabase.
func (b *blockParser) parseOptionalInDatabase(d *pipeline.RoleConfigDir) error {
	b.skipWS()
	if strings.ToUpper(b.peekWord()) != "IN" {
		return nil
	}
	b.readWord()
	if err := b.expect("DATABASE"); err != nil {
		return err
	}
	b.skipWS()
	db, err := b.readIdentifier()
	if err != nil {
		return err
	}
	name := db.String()
	d.InDatabase = &name
	return nil
}

// parseRoleConfigSet parses RFC audit item #74's "SET param {TO|=} value
// [IN DATABASE db];" / "SET param FROM CURRENT [IN DATABASE db];" block
// directive (the "SET" keyword itself already consumed by the caller's
// dispatch).
func (b *blockParser) parseRoleConfigSet(pos pipeline.SourcePos) (pipeline.RoleConfigDir, error) {
	d := pipeline.RoleConfigDir{Pos: pos}
	b.skipWS()
	param, err := b.readIdentifier()
	if err != nil {
		return d, err
	}
	d.Param = param.String()

	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.peekWord()) == "FROM" {
		b.readWord()
		if err := b.expect("CURRENT"); err != nil {
			return d, err
		}
		d.FromCurrent = true
	} else {
		b.restore(c)
		b.skipWS()
		if b.peek() == '=' {
			b.advance()
		} else if err := b.expect("TO"); err != nil {
			return d, err
		}
		val, err := b.parseConfigValue()
		if err != nil {
			return d, err
		}
		d.Value = &val
	}

	if err := b.parseOptionalInDatabase(&d); err != nil {
		return d, err
	}
	if err := b.expectSemi(); err != nil {
		return d, err
	}
	return d, nil
}

// parseRoleConfigReset parses RFC audit item #74's "RESET param [IN
// DATABASE db];" / "RESET ALL [IN DATABASE db];" block directive (the
// "RESET" keyword itself already consumed by the caller's dispatch).
func (b *blockParser) parseRoleConfigReset(pos pipeline.SourcePos) (pipeline.RoleConfigDir, error) {
	d := pipeline.RoleConfigDir{Pos: pos, Reset: true}
	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.peekWord()) == "ALL" {
		b.readWord()
		d.ResetAll = true
	} else {
		b.restore(c)
		param, err := b.readIdentifier()
		if err != nil {
			return d, err
		}
		d.Param = param.String()
	}

	if err := b.parseOptionalInDatabase(&d); err != nil {
		return d, err
	}
	if err := b.expectSemi(); err != nil {
		return d, err
	}
	return d, nil
}

// parseBoolWord reads a bare "true"/"false" keyword token (RFC Appendix A's
// `boolean` production — case-insensitive, matching every other DPG
// keyword), confirmed live via pg_query.Parse as real PostgreSQL's own
// GrantRoleStmt WITH-option spelling (a bare Boolean node, not a quoted
// string).
func (b *blockParser) parseBoolWord() (bool, error) {
	b.skipWS()
	w := strings.ToUpper(b.readWord())
	switch w {
	case "TRUE":
		return true, nil
	case "FALSE":
		return false, nil
	default:
		return false, b.errorf("expected true or false, got %q", w)
	}
}

// parseMembershipEntry parses one block-level membership directive's tail —
// "identifier [WITH ADMIN [OPTION|boolean]] [WITH INHERIT boolean] [WITH SET
// boolean];" — RFC audit item #32 (Section 11.1). direction/adminDefault are
// supplied by the caller's dispatch on which leading keyword (IN ROLE/ROLE/
// ADMIN) was consumed.
func (b *blockParser) parseMembershipEntry(direction string, adminDefault bool, pos pipeline.SourcePos) (pipeline.RoleMembershipDir, error) {
	m := pipeline.RoleMembershipDir{Direction: direction, AdminDefault: adminDefault, Pos: pos}
	b.skipWS()
	role, err := b.readIdentifier()
	if err != nil {
		return m, err
	}
	m.Role = role

	for {
		b.skipWS()
		c := b.cur()
		if strings.ToUpper(b.peekWord()) != "WITH" {
			b.restore(c)
			break
		}
		b.readWord()
		b.skipWS()
		switch strings.ToUpper(b.peekWord()) {
		case "ADMIN":
			b.readWord()
			b.skipWS()
			c2 := b.cur()
			if strings.ToUpper(b.peekWord()) == "OPTION" {
				b.readWord()
				v := true
				m.Admin = &v
			} else {
				b.restore(c2)
				v, err := b.parseBoolWord()
				if err != nil {
					return m, err
				}
				m.Admin = &v
			}
		case "INHERIT":
			b.readWord()
			v, err := b.parseBoolWord()
			if err != nil {
				return m, err
			}
			m.Inherit = &v
		case "SET":
			b.readWord()
			v, err := b.parseBoolWord()
			if err != nil {
				return m, err
			}
			m.Set = &v
		default:
			return m, b.errorf("expected ADMIN, INHERIT, or SET after WITH, got %q", b.peekWord())
		}
	}

	if err := b.expectSemi(); err != nil {
		return m, err
	}
	return m, nil
}

// parseEnumValueRename reads: 'new' RENAMED FROM 'old'; (the leading VALUE
// keyword is consumed by the caller's dispatch — Section 5.1.1). The current
// name comes first, matching every other RENAMED FROM directive.
func (b *blockParser) parseEnumValueRename(pos pipeline.SourcePos) (pipeline.EnumValueRenameDir, error) {
	b.skipWS()
	to, err := b.readSingleQuotedString()
	if err != nil {
		return pipeline.EnumValueRenameDir{}, err
	}
	if err := b.expect("RENAMED"); err != nil {
		return pipeline.EnumValueRenameDir{}, err
	}
	if err := b.expect("FROM"); err != nil {
		return pipeline.EnumValueRenameDir{}, err
	}
	b.skipWS()
	from, err := b.readSingleQuotedString()
	if err != nil {
		return pipeline.EnumValueRenameDir{}, err
	}
	if err := b.expectSemi(); err != nil {
		return pipeline.EnumValueRenameDir{}, err
	}
	return pipeline.EnumValueRenameDir{From: from, To: to, Pos: pos}, nil
}

// parseSecurityLabel reads: LABEL [FOR provider] 'label'; (the "SECURITY"
// directive keyword itself is consumed by the caller's dispatch, mirroring
// how "ROW LEVEL SECURITY" splits ROW/LEVEL/SECURITY across
// parseEnableOrTriggerState/parseForce and their own ENABLE/FORCE dispatch
// keyword).
func (b *blockParser) parseSecurityLabel(pos pipeline.SourcePos) (pipeline.SecurityLabel, error) {
	if err := b.expect("LABEL"); err != nil {
		return pipeline.SecurityLabel{}, err
	}
	sl := pipeline.SecurityLabel{Pos: pos}
	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.readWord()) == "FOR" {
		b.skipWS()
		var err error
		if b.peek() == '"' {
			sl.Provider, err = b.readQuotedString()
		} else {
			sl.Provider = b.readWord()
			if sl.Provider == "" {
				err = b.errorf("expected provider name after FOR")
			}
		}
		if err != nil {
			return pipeline.SecurityLabel{}, err
		}
	} else {
		b.restore(c)
	}
	b.skipWS()
	val, err := b.readSingleQuotedString()
	if err != nil {
		return pipeline.SecurityLabel{}, err
	}
	sl.Label = val
	if err := b.expectSemi(); err != nil {
		return pipeline.SecurityLabel{}, err
	}
	return sl, nil
}

// parseRenamedFrom reads: FROM old_name;
func (b *blockParser) parseRenamedFrom(pos pipeline.SourcePos) (*pipeline.Identifier, error) {
	if err := b.expect("FROM"); err != nil {
		return nil, err
	}
	b.skipWS()
	id, err := b.readIdentifier()
	if err != nil {
		return nil, err
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return &id, nil
}

// parseDrop reads: CASCADE;
func (b *blockParser) parseDrop(ast *pipeline.BlockAST) error {
	if err := b.expect("CASCADE"); err != nil {
		return err
	}
	ast.DropCascade = true
	return b.expectSemi()
}

// parseEnableOrTriggerState dispatches the bare "ENABLE" directive between
// its two unrelated meanings sharing the same leading keyword: Table's
// "ENABLE ROW LEVEL SECURITY" (Section 7.8) and EVENT TRIGGER's
// trigger-enable-state "ENABLE [REPLICA|ALWAYS]" (Section 14.1,
// BlockAST.TriggerEnableState's doc comment) — distinguished by peeking the
// next word.
func (b *blockParser) parseEnableOrTriggerState(ast *pipeline.BlockAST, pos pipeline.SourcePos) error {
	b.skipWS()
	c := b.cur()
	w := strings.ToUpper(b.readWord())
	switch w {
	case "ROW":
		if err := b.expect("LEVEL"); err != nil {
			return err
		}
		if err := b.expect("SECURITY"); err != nil {
			return err
		}
		ast.EnableRLS = true
		return b.expectSemi()
	case "REPLICA":
		ast.TriggerEnableState = "ENABLE REPLICA"
		return b.expectSemi()
	case "ALWAYS":
		ast.TriggerEnableState = "ENABLE ALWAYS"
		return b.expectSemi()
	default:
		// Bare "ENABLE" (no REPLICA/ALWAYS) is rejected, mirroring
		// TriggerDef's identical parser exactly: it would be a redundant
		// no-op directive (omitting trigger-enable-state entirely already
		// means enabled, PostgreSQL's own default), not a distinct state to
		// express.
		b.restore(c)
		return fmt.Errorf("%s: expected ROW LEVEL SECURITY, REPLICA, or ALWAYS after ENABLE, got %q", pos, w)
	}
}

// parseForce reads: ROW LEVEL SECURITY;
func (b *blockParser) parseForce(ast *pipeline.BlockAST, pos pipeline.SourcePos) error {
	b.skipWS()
	w := strings.ToUpper(b.readWord())
	if w != "ROW" {
		return fmt.Errorf("%s: expected ROW LEVEL SECURITY after FORCE, got %q", pos, w)
	}
	if err := b.expect("LEVEL"); err != nil {
		return err
	}
	if err := b.expect("SECURITY"); err != nil {
		return err
	}
	ast.ForceRLS = true
	return b.expectSemi()
}

// parseReplicaIdentity reads: IDENTITY (DEFAULT|FULL|NOTHING|USING INDEX name);
func (b *blockParser) parseReplicaIdentity(pos pipeline.SourcePos) (*pipeline.ReplicaIdentityDir, error) {
	if err := b.expect("IDENTITY"); err != nil {
		return nil, err
	}
	b.skipWS()
	w := strings.ToUpper(b.readWord())
	dir := &pipeline.ReplicaIdentityDir{Pos: pos}
	switch w {
	case "DEFAULT", "FULL", "NOTHING":
		dir.Mode = w
	case "USING":
		if err := b.expect("INDEX"); err != nil {
			return nil, err
		}
		b.skipWS()
		name := foldIdent(b.readWord())
		if name == "" {
			return nil, b.errorf("expected an index name after REPLICA IDENTITY USING INDEX")
		}
		dir.Mode = "INDEX"
		dir.IndexName = name
	default:
		return nil, fmt.Errorf("%s: expected DEFAULT, FULL, NOTHING, or USING INDEX after REPLICA IDENTITY, got %q", pos, w)
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return dir, nil
}

// parseClusterOn reads: ON index-name;
func (b *blockParser) parseClusterOn(pos pipeline.SourcePos) (*pipeline.Identifier, error) {
	if err := b.expect("ON"); err != nil {
		return nil, err
	}
	b.skipWS()
	id, err := b.readIdentifier()
	if err != nil {
		return nil, err
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return &id, nil
}

// parseDetachedFrom reads: FROM parent_table [CONCURRENTLY];
func (b *blockParser) parseDetachedFrom(pos pipeline.SourcePos) (*pipeline.DetachedFromDirective, error) {
	if err := b.expect("FROM"); err != nil {
		return nil, err
	}
	b.skipWS()
	tbl, err := b.readIdentifier()
	if err != nil {
		return nil, err
	}
	d := &pipeline.DetachedFromDirective{Table: tbl, Pos: pos}
	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.readWord()) == "CONCURRENTLY" {
		d.Concurrently = true
	} else {
		b.restore(c)
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return d, nil
}

// ── INDEXES ───────────────────────────────────────────────────────────────────

// parseIndices reads: { idx1 [UNIQUE] (cols) [USING m] [WHERE pred] [INCLUDE (...)] [WITH (...)] [TABLESPACE t] [CONCURRENTLY bool]; ... }
func (b *blockParser) parseIndices(pos pipeline.SourcePos) ([]pipeline.IndexDef, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var indices []pipeline.IndexDef
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		idx, err := b.parseOneIndex(false)
		if err != nil {
			return nil, err
		}
		indices = append(indices, idx)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close INDEXES block")
	}
	b.advance()
	return indices, nil
}

// parseOneIndex parses one index declaration. presetUnique is true when the
// caller (Mode B's top-level "UNIQUE INDEX ..." dispatch) already consumed a
// leading UNIQUE keyword itself — Mode A (an entry inside INDEXES { }, with
// no INDEX keyword) has parseOneIndex consume its own leading UNIQUE.
func (b *blockParser) parseOneIndex(presetUnique bool) (pipeline.IndexDef, error) {
	pos := b.srcPos()

	unique := presetUnique
	if !presetUnique {
		b.skipWS()
		c := b.cur()
		w := strings.ToUpper(b.readWord())
		if w == "UNIQUE" {
			unique = true
		} else {
			b.restore(c)
		}
	}

	// [CONCURRENTLY] is a bare presence keyword, mirroring real PostgreSQL's
	// own CREATE [UNIQUE] INDEX [CONCURRENTLY] name — CONCURRENTLY has no
	// boolean value in real PG (it's either written or it isn't), so DPG
	// must not invent one either. This used to be a trailing
	// "CONCURRENTLY <bool>;" clause with no PG equivalent; presence here
	// means "explicitly concurrent", absence means "let the compiler apply
	// the project's concurrent_indexes default" (see internal/diff
	// createIndex), not "explicitly non-concurrent" — PG has no way to
	// spell that either.
	b.skipWS()
	c := b.cur()
	w := strings.ToUpper(b.readWord())
	concurrently := false
	if w == "CONCURRENTLY" {
		concurrently = true
	} else {
		b.restore(c)
	}

	// [ONLY] is a bare presence keyword too (RFC Section 7.7), positioned
	// exactly where real PostgreSQL's own CREATE [UNIQUE] INDEX
	// [CONCURRENTLY] [ONLY] table_name puts it — after CONCURRENTLY,
	// before the index name (DPG's INDEXES block omits the implicit
	// "ON table_name" itself, but keeps every prefix keyword's own order).
	b.skipWS()
	c = b.cur()
	w = strings.ToUpper(b.readWord())
	only := false
	if w == "ONLY" {
		only = true
	} else {
		b.restore(c)
	}

	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.IndexDef{}, err
	}
	idx := pipeline.IndexDef{Name: name, Unique: unique, Concurrently: concurrently, Only: only, Pos: pos}

	// Check optional USING method — mirrors real PostgreSQL's own
	// CREATE INDEX name ON table USING method (columns) order, and matches
	// the RFC's ABNF and every one of its worked examples (e.g.
	// "idx_location USING gist (location);"). This used to be parsed only
	// after the column list, which silently rejected the RFC's own
	// examples — confirmed live: `idx USING gin (tags);` failed to parse
	// with "expected '(' for index columns after index name idx" until
	// this fix.
	b.skipWS()
	c = b.cur()
	w = strings.ToUpper(b.readWord())
	if w == "USING" {
		b.skipWS()
		method, err2 := b.readIdentifier()
		if err2 != nil {
			return idx, err2
		}
		idx.Method = &method
	} else {
		b.restore(c)
	}

	// Expect (columns)
	b.skipWS()
	if b.peek() != '(' {
		return idx, b.errorf("expected '(' for index columns after index name %s", name)
	}
	b.advance() // consume (
	colsRaw, err := b.readRawUntil(")")
	if err != nil {
		return idx, err
	}
	b.advance() // consume )
	idx.Columns = parseIndexColumns(colsRaw)

	// Parse optional clauses
	for {
		b.skipWS()
		c := b.cur()
		kw := strings.ToUpper(b.peekWord())
		switch kw {
		case "WHERE":
			b.readWord()
			b.skipWS()
			if b.peek() == '(' {
				// A parenthesized predicate (RFC Section 7.7's own grammar
				// shape, "WHERE" WSP "(" predicate ")") is read as a
				// balanced expression — like INCLUDE/WITH below — so a
				// subsequent clause (RENAMED FROM, TABLESPACE) isn't
				// swallowed into it too. A raw run to the next statement
				// terminator had no way to know the predicate ended at its
				// own closing paren rather than the next ';'.
				b.advance() // consume (
				inner, err2 := b.readRawUntil(")")
				if err2 != nil {
					return idx, err2
				}
				b.advance() // consume )
				idx.Where = &pipeline.RawExpr{Text: "(" + strings.TrimSpace(inner) + ")", Pos: b.srcPos()}
			} else {
				// A bare (unparenthesized) predicate has no delimiter of
				// its own to bound a balanced read, so it's read exactly as
				// it always has been — to the next statement terminator. A
				// clause written after a bare WHERE (e.g. RENAMED FROM) is
				// not separable from it this way; write WHERE (...) with
				// parens instead if that combination is needed.
				raw, err2 := b.readRawUntil(";,}")
				if err2 != nil {
					return idx, err2
				}
				idx.Where = &pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: b.srcPos()}
			}
		case "INCLUDE":
			b.readWord()
			b.skipWS()
			if b.peek() != '(' {
				return idx, b.errorf("expected '(' after INCLUDE")
			}
			b.advance()
			raw, err2 := b.readRawUntil(")")
			if err2 != nil {
				return idx, err2
			}
			b.advance()
			for _, s := range strings.Split(raw, ",") {
				// Unquote or case-fold like parseIndexColumnEntry does for key
				// columns, so the differ (which quotes on output) gets the
				// identifier PostgreSQL would resolve.
				s = identToken(strings.TrimSpace(s))
				if s != "" {
					idx.Include = append(idx.Include, pipeline.Identifier{Name: s})
				}
			}
		case "NULLS":
			b.readWord()
			b.skipWS()
			w2 := strings.ToUpper(b.readWord())
			switch w2 {
			case "NOT":
				b.skipWS()
				w3 := strings.ToUpper(b.readWord())
				if w3 != "DISTINCT" {
					return idx, b.errorf("expected DISTINCT after NULLS NOT, got %q", w3)
				}
				idx.NullsNotDistinct = true
			case "DISTINCT":
				// Explicit spelling of the default; nothing to record.
			default:
				return idx, b.errorf("expected NOT or DISTINCT after NULLS, got %q", w2)
			}
		case "WITH":
			b.readWord()
			b.skipWS()
			if b.peek() != '(' {
				return idx, b.errorf("expected '(' after WITH")
			}
			b.advance()
			raw, err2 := b.readRawUntil(")")
			if err2 != nil {
				return idx, err2
			}
			b.advance()
			idx.With = parseStorageParams(raw)
		case "TABLESPACE":
			b.readWord()
			b.skipWS()
			ts, err2 := b.readIdentifier()
			if err2 != nil {
				return idx, err2
			}
			idx.Tablespace = &ts
		case "RENAMED":
			b.readWord()
			b.skipWS()
			if err2 := b.expect("FROM"); err2 != nil {
				return idx, err2
			}
			b.skipWS()
			oldName, err2 := b.readIdentifier()
			if err2 != nil {
				return idx, err2
			}
			idx.RenamedFrom = &oldName
		default:
			b.restore(c)
			goto doneIndexClauses
		}
	}
doneIndexClauses:

	comment, err := b.parseTrailingCommentBlock()
	if err != nil {
		return idx, err
	}
	idx.Comment = comment
	return idx, nil
}

func parseIndexColumns(raw string) []pipeline.IndexColumn {
	var cols []pipeline.IndexColumn
	for _, part := range splitTopLevel(raw, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cols = append(cols, parseIndexColumnEntry(part))
	}
	return cols
}

// splitTopLevel splits s on sep, ignoring separators nested inside parentheses
// (so an expression column like "coalesce(a, b)" is not split at its comma).
func splitTopLevel(s string, sep rune) []string {
	var parts []string
	var cur strings.Builder
	depth := 0
	for _, ch := range s {
		switch {
		case ch == '(':
			depth++
			cur.WriteRune(ch)
		case ch == ')':
			depth--
			cur.WriteRune(ch)
		case ch == sep && depth == 0:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(ch)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// splitIndexIdentToken reads one identifier token from the front of s: a
// double-quoted identifier (with "" escaping) or a bare word up to the next
// whitespace or '(' — used by parseIndexColumnEntry's left-to-right walk
// over an index-col entry's trailing clauses. Returns the raw token
// (quotes included when quoted; callers Trim them) and the remaining text,
// both with surrounding whitespace already stripped.
func splitIndexIdentToken(s string) (token, rest string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		i := 1
		for i < len(s) {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					i += 2
					continue
				}
				i++
				break
			}
			i++
		}
		return s[:i], strings.TrimSpace(s[i:])
	}
	i := strings.IndexAny(s, " \t\n\r(")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// splitIndexParenGroup finds s's leading "(...)" group (s must start with
// '(') and returns its inner text and everything after the closing paren.
// ok is false for an unbalanced/missing group, in which case rest is
// unspecified.
func splitIndexParenGroup(s string) (inner, rest string, ok bool) {
	depth := 0
	for i, ch := range s {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

// parseIndexColumnEntry parses one index-col entry per real PostgreSQL's own
// CREATE INDEX grammar and left-to-right clause order (verified live via
// direct pg_query.Parse — RFC Section 7.7's own ABNF lists ASC/DESC/NULLS before
// COLLATE/opclass, the reverse of what PostgreSQL's parser actually accepts,
// e.g. "a DESC COLLATE x" is a syntax error but "a COLLATE x DESC" is not):
// column name or "(expr)", then optional COLLATE identifier, then optional
// opclass [(params)], then optional ASC/DESC, then optional NULLS
// FIRST/LAST. It mirrors the introspector's parseIndexColumn so a dumped
// index column round-trips to the same IndexColumn rather than being stored
// — and then quoted — as one literal identifier.
//
// The previous version only ever stripped a trailing ASC/DESC/NULLS suffix
// and treated anything else containing '(' as one opaque expression — which
// silently mishandled COLLATE and swallowed an opclass with parameters
// (e.g. "doc tsvector_ops(siglen = 32)") into a single bogus expression
// column, producing invalid SQL. COLLATE/opclass were declared on
// IndexColumn already but never reached by this parser at all.
func parseIndexColumnEntry(s string) pipeline.IndexColumn {
	col := pipeline.IndexColumn{}
	s = strings.TrimSpace(s)

	switch {
	case strings.HasPrefix(s, "("):
		// A fully parenthesized expression (RFC's "(" expr ")" production).
		// The stored Expr.Text is the INNER text only, without the marker
		// parens — dump.go's renderIndex re-adds exactly one wrapping
		// layer when rendering back to DPG source, so this round-trips.
		if inner, rest, ok := splitIndexParenGroup(s); ok {
			col.Expr = &pipeline.RawExpr{Text: inner}
			s = rest
		} else {
			col.Expr = &pipeline.RawExpr{Text: s}
			return col
		}
	default:
		i := strings.IndexAny(s, " \t\n\r(")
		if i >= 0 && s[i] == '(' {
			// An identifier immediately followed by '(' with no
			// intervening whitespace: real PostgreSQL's func_expr_
			// windowless index_elem alternative (confirmed live) — the
			// identifier and its call are one inseparable expression, not
			// a column name that happens to be followed by an opclass
			// (which always has at least one space before it, e.g.
			// pg_get_indexdef's own "tsvector_ops (siglen='32')"
			// reconstruction). Not stripped/re-wrapped like the leading-
			// paren case above: dump.go writes col.Expr.Text verbatim for
			// this shape (see its own doc comment).
			if inner, rest, ok := splitIndexParenGroup(s[i:]); ok {
				col.Expr = &pipeline.RawExpr{Text: s[:i] + "(" + inner + ")"}
				s = rest
			} else {
				col.Expr = &pipeline.RawExpr{Text: s}
				return col
			}
		} else {
			var name string
			name, s = splitIndexIdentToken(s)
			col.Name = identToken(name)
		}
	}

	// COLLATE/opclass never follow an expression column in this parser
	// (RFC audit item #10's own worked example, like every other case
	// this codebase has needed so far, pairs opclass with a plain column
	// name — a deliberately unmodeled corner case, not a limitation of
	// the grammar itself).
	if col.Expr == nil {
		if word, rest := splitIndexIdentToken(s); strings.ToUpper(word) == "COLLATE" {
			var collName string
			collName, s = splitIndexIdentToken(rest)
			col.Collation = &pipeline.Identifier{Name: identToken(collName)}
		}

		if s != "" {
			if word, rest := splitIndexIdentToken(s); !isIndexColTrailingKeyword(word) {
				col.OpClass = &pipeline.Identifier{Name: identToken(word)}
				s = rest
				if strings.HasPrefix(s, "(") {
					if inner, rest2, ok := splitIndexParenGroup(s); ok {
						col.OpClassParams = parseStorageParams(inner)
						s = rest2
					}
				}
			}
		}
	}

	if word, rest := splitIndexIdentToken(s); strings.ToUpper(word) == "DESC" {
		col.SortOrder = "DESC"
		s = rest
	} else if strings.ToUpper(word) == "ASC" {
		col.SortOrder = "ASC"
		s = rest
	}

	if word, rest := splitIndexIdentToken(s); strings.ToUpper(word) == "NULLS" {
		if word2, _ := splitIndexIdentToken(rest); strings.ToUpper(word2) == "FIRST" {
			col.Nulls = "FIRST"
		} else if strings.ToUpper(word2) == "LAST" {
			col.Nulls = "LAST"
		}
	}

	return col
}

// isIndexColTrailingKeyword reports whether word is one of index-col's
// trailing clause keywords (ASC/DESC/NULLS) rather than an opclass name —
// used to decide whether the text remaining after a column/COLLATE is an
// opclass at all.
func isIndexColTrailingKeyword(word string) bool {
	switch strings.ToUpper(word) {
	case "ASC", "DESC", "NULLS":
		return true
	default:
		return false
	}
}

func parseStorageParams(raw string) []pipeline.StorageParam {
	var params []pipeline.StorageParam
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			params = append(params, pipeline.StorageParam{
				Key:   strings.TrimSpace(kv[0]),
				Value: strings.TrimSpace(kv[1]),
			})
		}
	}
	return params
}

// ── ATTRIBUTE / ATTRIBUTES ───────────────────────────────────────────────────

// parseAttributeBlock reads: name { COMMENT '...'; RENAMED FROM old; } — a
// composite type attribute block (RFC Section 5.2). The leading ATTRIBUTE
// keyword is consumed by the caller.
func (b *blockParser) parseAttributeBlock(pos pipeline.SourcePos) (pipeline.ColumnBlock, error) {
	b.skipWS()
	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.ColumnBlock{}, err
	}
	attr := pipeline.ColumnBlock{Name: name, Pos: pos}
	if err := b.consumeBrace(); err != nil {
		return attr, err
	}
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		switch word {
		case "COMMENT":
			attr.Comment, err = b.parseStringDirective(dirPos)
		case "RENAMED":
			attr.RenamedFrom, err = b.parseRenamedFrom(dirPos)
		default:
			return attr, fmt.Errorf("%s: DPG-E024: unknown attribute directive %q; an ATTRIBUTE block accepts only COMMENT and RENAMED FROM", dirPos, word)
		}
		if err != nil {
			return attr, err
		}
	}
	b.skipWS()
	if b.peek() != '}' {
		return attr, b.errorf("expected '}' to close ATTRIBUTE %s block", name)
	}
	b.advance()
	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return attr, nil
}

// parseAttributesBlock reads: { name { ... } name { ... } } — the plural
// Mode A form of parseAttributeBlock.
func (b *blockParser) parseAttributesBlock(pos pipeline.SourcePos) ([]pipeline.ColumnBlock, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var attrs []pipeline.ColumnBlock
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		attr, err := b.parseAttributeBlock(b.srcPos())
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, attr)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close ATTRIBUTES block")
	}
	b.advance()
	return attrs, nil
}

// ── COLUMN / COLUMNS ──────────────────────────────────────────────────────────

func (b *blockParser) parseColumnBlock(pos pipeline.SourcePos) (pipeline.ColumnBlock, error) {
	b.skipWS()
	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.ColumnBlock{}, err
	}
	col := pipeline.ColumnBlock{Name: name, Pos: pos}

	if err := b.consumeBrace(); err != nil {
		return col, err
	}
	if err := b.fillColumnBlock(&col); err != nil {
		return col, err
	}
	col.NameMaps, col.NameMapWarnings = dedupeNameMapsLastWins(col.NameMaps)
	b.skipWS()
	if b.peek() != '}' {
		return col, b.errorf("expected '}' to close COLUMN %s block", name)
	}
	b.advance()
	// Optional trailing ;
	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return col, nil
}

func (b *blockParser) parseColumnsBlock(pos pipeline.SourcePos) ([]pipeline.ColumnBlock, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var cols []pipeline.ColumnBlock
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		dirPos := b.srcPos()
		col, err := b.parseColumnBlock(dirPos)
		if err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close COLUMNS block")
	}
	b.advance()
	return cols, nil
}

func (b *blockParser) fillColumnBlock(col *pipeline.ColumnBlock) error {
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		var err error
		switch word {
		case "COMMENT":
			col.Comment, err = b.parseStringDirective(dirPos)
		case "STATISTICS":
			n, e2 := b.parseStatisticsValue(dirPos)
			if e2 != nil {
				err = e2
			} else {
				col.Statistics = n
			}
		case "COMPRESSION":
			col.Compression, err = b.parseIdentDirective(dirPos)
		case "STORAGE":
			col.Storage, err = b.parseIdentDirective(dirPos)
		case "DEPRECATED":
			col.Deprecated, err = b.parseStringDirective(dirPos)
		case "RENAMED":
			col.RenamedFrom, err = b.parseRenamedFrom(dirPos)
		case "USING":
			b.skipWS()
			raw, e2 := b.readRawUntil(";")
			if e2 != nil {
				err = e2
			} else {
				col.Using = &pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: dirPos}
				b.advance() // consume ;
			}
		case "GRANTS":
			grants, e2 := b.parseGrantsBlock(dirPos)
			if e2 != nil {
				err = e2
			} else {
				col.Grants = append(col.Grants, grants...)
			}
		case "GRANT":
			// Mode B (Section 4.8 Dual Definition Modes): singular entry, no wrapping '{ }'.
			g, e2 := b.parseOneGrant(dirPos)
			if e2 != nil {
				err = e2
			} else {
				col.Grants = append(col.Grants, g)
			}
		case "REVOKES":
			revs, e2 := b.parseRevocationsBlock(dirPos)
			if e2 != nil {
				err = e2
			} else {
				col.Revocations = append(col.Revocations, revs...)
			}
		case "REVOKE":
			// Mode B: singular entry, no wrapping '{ }'.
			r, e2 := b.parseOneRevocation(dirPos)
			if e2 != nil {
				err = e2
			} else {
				col.Revocations = append(col.Revocations, r)
			}
		case "SECURITY":
			sl, e2 := b.parseSecurityLabel(dirPos)
			if e2 != nil {
				err = e2
			} else {
				col.SecurityLabels = append(col.SecurityLabels, sl)
			}
		case "NAME":
			b.skipWS()
			w2 := strings.ToUpper(b.readWord())
			switch w2 {
			case "MAP":
				entry, e2 := b.parseNameMapSingular(dirPos)
				if e2 != nil {
					err = e2
				} else {
					col.NameMaps = append(col.NameMaps, entry)
				}
			case "MAPS":
				entries, e2 := b.parseNameMapsBlock(dirPos)
				if e2 != nil {
					err = e2
				} else {
					col.NameMaps = append(col.NameMaps, entries...)
				}
			default:
				err = fmt.Errorf("%s: expected MAP or MAPS after NAME, got %q", dirPos, w2)
			}
		default:
			return fmt.Errorf("%s: DPG-E024: unknown column directive %q%s", dirPos, word, pipeline.RenamedKeywordHint(word))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// parseStatisticsValue reads either an integer target or the literal
// keyword DEFAULT (RFC audit item #112 — real PostgreSQL's own
// ALTER ... SET STATISTICS DEFAULT resets the target to
// default_statistics_target, the same end state nil already produces
// here: the differ/emit path already treats a nil Statistics as "emit
// SET STATISTICS -1", so DEFAULT is just an explicit spelling of that,
// letting a user reset a customized target back to default without
// having to delete the directive line entirely).
func (b *blockParser) parseStatisticsValue(pos pipeline.SourcePos) (*int, error) {
	b.skipWS()
	if strings.ToUpper(b.peekWord()) == "DEFAULT" {
		b.readWord()
		if err := b.expectSemi(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var buf []byte
	if b.peek() == '-' {
		buf = append(buf, b.advance())
	}
	for !b.eof() && isDigit(b.peek()) {
		buf = append(buf, b.advance())
	}
	if len(buf) == 0 || (len(buf) == 1 && buf[0] == '-') {
		return nil, fmt.Errorf("%s: expected an integer or DEFAULT after STATISTICS", pos)
	}
	n, err := strconv.Atoi(string(buf))
	if err != nil {
		return nil, fmt.Errorf("%s: invalid STATISTICS value: %w", pos, err)
	}
	// Section 7.4: STATISTICS n MUST be in [-1, 10000] — real PostgreSQL's
	// own valid range for a column's statistics target (-1 restores the
	// system default; there is no equivalent lower sentinel above 10000).
	if n < -1 || n > 10000 {
		return nil, fmt.Errorf("%s: DPG-E020: STATISTICS value %d is outside the valid range [-1, 10000]", pos, n)
	}
	if err := b.expectSemi(); err != nil {
		return nil, err
	}
	return &n, nil
}

// parseDependsOnExtension reads: ON EXTENSION ext_name; — the caller has
// already consumed the leading "DEPENDS" keyword (either directly, for the
// positive form, or via parseNoDependsOnExtension after "NO", for the
// negative form, RFC Section 9.1).
func (b *blockParser) parseDependsOnExtension(pos pipeline.SourcePos) (string, error) {
	name, err := b.parseOnExtensionName()
	if err != nil {
		return "", err
	}
	if err := b.expectSemi(); err != nil {
		return "", err
	}
	return name, nil
}

// parseNoDependsOnExtension reads: DEPENDS ON EXTENSION ext_name; — the
// caller has already consumed the leading "NO" keyword (RFC Section 9.1's negative
// form).
func (b *blockParser) parseNoDependsOnExtension(pos pipeline.SourcePos) (string, error) {
	if err := b.expect("DEPENDS"); err != nil {
		return "", err
	}
	return b.parseDependsOnExtension(pos)
}

// parseOnExtensionName reads: ON EXTENSION ext_name — no trailing semicolon,
// for use where DEPENDS ON EXTENSION appears as one of several optional
// trailing clauses before a single shared terminating ";" (Trigger's
// trigger-decl, Section 7.9) rather than as its own semicolon-terminated
// block directive (Function/Procedure's func-block, Section 9.1).
func (b *blockParser) parseOnExtensionName() (string, error) {
	if err := b.expect("ON"); err != nil {
		return "", err
	}
	if err := b.expect("EXTENSION"); err != nil {
		return "", err
	}
	b.skipWS()
	name, err := b.readIdentifier()
	if err != nil {
		return "", err
	}
	return name.Name, nil
}

// ── CONSTRAINT ────────────────────────────────────────────────────────────────

func (b *blockParser) parseConstraint(pos pipeline.SourcePos) (pipeline.ConstraintDef, error) {
	b.skipWS()
	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.ConstraintDef{}, err
	}
	cst := pipeline.ConstraintDef{Name: name, Pos: pos}

	// Read everything up to ';', "NOT VALID;", or a trailing "{ COMMENT
	// '...'; }" block — the brace is a real terminator here, not part of the
	// expression, so it must stop readRawUntil the same way ';' does.
	raw, err := b.readRawUntil(";{")
	if err != nil {
		return cst, err
	}
	raw = strings.TrimSpace(raw)
	// Check for a trailing RENAMED FROM clause first (RFC Section 7.3: the
	// last optional clause on a table-constraint, appearing after NOT VALID
	// when both are present).
	if rm := renamedFromSuffixRe.FindStringSubmatch(raw); rm != nil {
		renamed := unquoteRawIdent(rm[2])
		cst.RenamedFrom = &renamed
		raw = strings.TrimSpace(rm[1])
	}
	// Check for a trailing ENFORCED/NOT ENFORCED clause (PostgreSQL 18+,
	// RFC Section 7.3) — positioned here, between RENAMED FROM and NOT
	// VALID, to match table-constraint's actual clause order (body
	// [NOT VALID] [enforced-clause] [DEFERRABLE ...] [RENAMED FROM]):
	// stripping suffixes right-to-left, DEFERRABLE isn't tracked for a
	// block-declared constraint at all yet (a separate, pre-existing gap,
	// not touched here), but enforced-clause must still be peeled off
	// before the NOT VALID check below, or e.g. "CHECK (...) NOT VALID
	// NOT ENFORCED" would leave the string not ending in "NOT VALID" and
	// silently miss it entirely.
	upper := strings.ToUpper(raw)
	switch {
	case strings.HasSuffix(upper, "NOT ENFORCED"):
		cst.NotEnforced = true
		raw = strings.TrimSpace(raw[:len(raw)-len("NOT ENFORCED")])
	case strings.HasSuffix(upper, "ENFORCED"):
		raw = strings.TrimSpace(raw[:len(raw)-len("ENFORCED")])
	}
	// Check for NOT VALID suffix
	upper = strings.ToUpper(raw)
	if strings.HasSuffix(upper, "NOT VALID") {
		cst.NotValid = true
		raw = strings.TrimSpace(raw[:len(raw)-len("NOT VALID")])
	}
	cst.Expr = pipeline.RawExpr{Text: raw, Pos: pos}
	comment, err := b.parseTrailingCommentBlock()
	if err != nil {
		return cst, err
	}
	cst.Comment = comment
	return cst, nil
}

// parseConstraintsBlock parses a CONSTRAINTS { } block (Mode A), each entry
// omitting the singular CONSTRAINT keyword. Mirrors parsePolicies/
// parseTriggers's relationship to their own per-entry parser; unlike those,
// parseConstraint takes an explicit pos rather than computing its own, so
// each entry's position is captured fresh here before calling it.
func (b *blockParser) parseConstraintsBlock(pos pipeline.SourcePos) ([]pipeline.ConstraintDef, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var constraints []pipeline.ConstraintDef
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		cPos := b.srcPos()
		cst, err := b.parseConstraint(cPos)
		if err != nil {
			return nil, err
		}
		constraints = append(constraints, cst)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close CONSTRAINTS block")
	}
	b.advance()
	return constraints, nil
}

// ── POLICIES ─────────────────────────────────────────────────────────────────

func (b *blockParser) parsePolicies(pos pipeline.SourcePos) ([]pipeline.PolicyDef, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var policies []pipeline.PolicyDef
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		pol, err := b.parseOnePolicy()
		if err != nil {
			return nil, err
		}
		policies = append(policies, pol)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close POLICIES block")
	}
	b.advance()
	return policies, nil
}

// Policy syntax: name [FOR command] [AS PERMISSIVE|RESTRICTIVE]
//
//	[TO role, ...]
//	[USING (expr)]
//	[WITH CHECK (expr)];
func (b *blockParser) parseOnePolicy() (pipeline.PolicyDef, error) {
	pos := b.srcPos()
	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.PolicyDef{}, err
	}
	pol := pipeline.PolicyDef{Name: name, Permissive: true, Pos: pos}

	for {
		b.skipWS()
		c := b.cur()
		kw := strings.ToUpper(b.readWord())
		switch kw {
		case "FOR":
			b.skipWS()
			pol.Command = strings.ToUpper(b.readWord())
		case "AS":
			b.skipWS()
			perm := strings.ToUpper(b.readWord())
			pol.Permissive = perm != "RESTRICTIVE"
		case "TO":
			for {
				b.skipWS()
				r, err2 := b.readRoleIdentifier()
				if err2 != nil {
					return pol, err2
				}
				pol.Roles = append(pol.Roles, r)
				b.skipWS()
				if b.peek() != ',' {
					break
				}
				b.advance()
			}
		case "USING":
			b.skipWS()
			if b.peek() != '(' {
				return pol, b.errorf("expected '(' after USING")
			}
			b.advance()
			raw, err2 := b.readRawUntil(")")
			if err2 != nil {
				return pol, err2
			}
			b.advance()
			pol.Using = &pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: pos}
		case "WITH":
			if err := b.expect("CHECK"); err != nil {
				return pol, err
			}
			b.skipWS()
			if b.peek() != '(' {
				return pol, b.errorf("expected '(' after WITH CHECK")
			}
			b.advance()
			raw, err2 := b.readRawUntil(")")
			if err2 != nil {
				return pol, err2
			}
			b.advance()
			pol.WithCheck = &pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: pos}
		case "RENAMED":
			// RFC Section 7.8: the last optional clause on a policy-decl,
			// right before the terminating ';' — no semicolon of its own to
			// consume here (unlike the top-level BLOCK "RENAMED FROM old;"
			// directive parseRenamedFrom handles), matching Column's inline
			// RENAMED FROM within a single larger decl.
			if err := b.expect("FROM"); err != nil {
				return pol, err
			}
			b.skipWS()
			renamed, err2 := b.readIdentifier()
			if err2 != nil {
				return pol, err2
			}
			pol.RenamedFrom = &renamed
		default:
			b.restore(c)
			goto donePolicy
		}
	}
donePolicy:
	comment, err := b.parseTrailingCommentBlock()
	if err != nil {
		return pol, err
	}
	pol.Comment = comment
	return pol, nil
}

// ── TRIGGERS ─────────────────────────────────────────────────────────────────

func (b *blockParser) parseTriggers(pos pipeline.SourcePos) ([]pipeline.TriggerDef, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var triggers []pipeline.TriggerDef
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		trig, err := b.parseOneTrigger()
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, trig)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close TRIGGERS block")
	}
	b.advance()
	return triggers, nil
}

// Trigger syntax: name BEFORE|AFTER|INSTEAD OF event[, event]
//
//	FOR EACH ROW|STATEMENT
//	[WHEN (cond)]
//	EXECUTE FUNCTION func_name(args);
func (b *blockParser) parseOneTrigger() (pipeline.TriggerDef, error) {
	pos := b.srcPos()
	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.TriggerDef{}, err
	}
	trig := pipeline.TriggerDef{Name: name, Pos: pos}

	// Optional CONSTRAINT: a constraint trigger (CREATE CONSTRAINT TRIGGER).
	b.skipWS()
	if strings.EqualFold(b.peekWord(), "CONSTRAINT") {
		b.readWord()
		trig.Constraint = true
	}

	// Timing: BEFORE | AFTER | INSTEAD OF
	b.skipWS()
	timing := strings.ToUpper(b.readWord())
	switch timing {
	case "BEFORE", "AFTER":
		trig.When = timing
	case "INSTEAD":
		if err := b.expect("OF"); err != nil {
			return trig, err
		}
		trig.When = "INSTEAD OF"
	default:
		return trig, b.errorf("expected BEFORE/AFTER/INSTEAD OF, got %q", timing)
	}

	// Events: INSERT | UPDATE [OF cols] | DELETE | TRUNCATE [OR ...]
	for {
		b.skipWS()
		evt := strings.ToUpper(b.readWord())
		switch evt {
		case "INSERT", "DELETE", "TRUNCATE":
			trig.Events = append(trig.Events, evt)
		case "UPDATE":
			trig.Events = append(trig.Events, "UPDATE")
			// Optional OF col, col, ...
			b.skipWS()
			c := b.cur()
			if strings.ToUpper(b.peekWord()) == "OF" {
				b.readWord()
				for {
					b.skipWS()
					// RFC audit item #1: the column list was tokenized and
					// explicitly discarded here — a trigger declared to
					// fire only on specific columns actually fired on
					// every column update instead, a real semantics
					// divergence, not cosmetic.
					col := b.readWord()
					if col != "" {
						trig.UpdateOfColumns = append(trig.UpdateOfColumns, col)
					}
					b.skipWS()
					if b.peek() != ',' {
						break
					}
					b.advance()
					// Check not OR
					b.skipWS()
					if strings.ToUpper(b.peekWord()) != "" &&
						!strings.EqualFold(b.peekWord(), "OR") {
						continue
					}
					break
				}
			} else {
				b.restore(c)
			}
		default:
			return trig, b.errorf("expected trigger event, got %q", evt)
		}
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "OR" {
			b.readWord()
		} else {
			break
		}
	}

	// Constraint trigger clauses, after the event list as in PostgreSQL:
	// [FROM table] [NOT DEFERRABLE | DEFERRABLE] [INITIALLY DEFERRED |
	// INITIALLY IMMEDIATE].
	b.skipWS()
	if strings.EqualFold(b.peekWord(), "FROM") {
		b.readWord()
		from, ferr := b.readIdentifier()
		if ferr != nil {
			return trig, ferr
		}
		trig.ConstraintFrom = &from
	}
	for {
		b.skipWS()
		switch strings.ToUpper(b.peekWord()) {
		case "NOT":
			b.readWord()
			if err := b.expect("DEFERRABLE"); err != nil {
				return trig, err
			}
			trig.Deferrable = false
			continue
		case "DEFERRABLE":
			b.readWord()
			trig.Deferrable = true
			continue
		case "INITIALLY":
			b.readWord()
			b.skipWS()
			switch w := strings.ToUpper(b.readWord()); w {
			case "DEFERRED":
				trig.Deferrable, trig.InitiallyDeferred = true, true
			case "IMMEDIATE":
				trig.InitiallyDeferred = false
			default:
				return trig, b.errorf("expected DEFERRED or IMMEDIATE after INITIALLY, got %q", w)
			}
			continue
		}
		break
	}

	// Optional REFERENCING { OLD | NEW } TABLE [AS] name [...] — real
	// PostgreSQL grammar places this after the event list, before FOR EACH
	// (RFC Section 7.9, audit item #2).
	b.skipWS()
	refMark := b.cur()
	if strings.ToUpper(b.peekWord()) == "REFERENCING" {
		b.readWord()
		for {
			b.skipWS()
			which := strings.ToUpper(b.peekWord())
			if which != "OLD" && which != "NEW" {
				break
			}
			b.readWord()
			if err := b.expect("TABLE"); err != nil {
				return trig, err
			}
			b.skipWS()
			if strings.ToUpper(b.peekWord()) == "AS" {
				b.readWord()
			}
			b.skipWS()
			var tname string
			if b.peek() == '"' {
				var qerr error
				tname, qerr = b.readQuotedString()
				if qerr != nil {
					return trig, qerr
				}
			} else {
				tname = foldIdent(b.readWord())
				if tname == "" {
					return trig, b.errorf("expected transition table name after %s TABLE", which)
				}
			}
			if which == "OLD" {
				trig.OldTransitionName = &tname
			} else {
				trig.NewTransitionName = &tname
			}
		}
	} else {
		b.restore(refMark)
	}

	// FOR EACH ROW | STATEMENT
	if err := b.expect("FOR"); err != nil {
		return trig, err
	}
	if err := b.expect("EACH"); err != nil {
		return trig, err
	}
	b.skipWS()
	trig.ForEach = strings.ToUpper(b.readWord())

	// Optional WHEN (cond)
	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.peekWord()) == "WHEN" {
		b.readWord()
		b.skipWS()
		if b.peek() != '(' {
			return trig, b.errorf("expected '(' after WHEN")
		}
		b.advance()
		raw, err2 := b.readRawUntil(")")
		if err2 != nil {
			return trig, err2
		}
		b.advance()
		trig.Condition = &pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: pos}
	} else {
		b.restore(c)
	}

	// EXECUTE FUNCTION func_name(args)
	if err := b.expect("EXECUTE"); err != nil {
		return trig, err
	}
	b.skipWS()
	// FUNCTION or PROCEDURE
	b.readWord()
	b.skipWS()
	fn, err := b.readIdentifier()
	if err != nil {
		return trig, err
	}
	trig.Function = fn
	// Args
	b.skipWS()
	if b.peek() == '(' {
		b.advance()
		raw, err2 := b.readRawUntil(")")
		if err2 != nil {
			return trig, err2
		}
		b.advance()
		for _, a := range strings.Split(raw, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				trig.Args = append(trig.Args, a)
			}
		}
	}

	// trigger-enable-state (Section 7.9): DISABLED / ENABLE REPLICA /
	// ENABLE ALWAYS, appearing after EXECUTE FUNCTION and before any
	// DEPENDS ON EXTENSION clause. Omitted means ENABLED, PostgreSQL's
	// own default — matches how RLS's enable-dir/force-dir work.
	b.skipWS()
	enableMark := b.cur()
	switch strings.ToUpper(b.peekWord()) {
	case "DISABLED":
		b.readWord()
		trig.EnableState = "DISABLED"
	case "ENABLE":
		b.readWord()
		b.skipWS()
		switch strings.ToUpper(b.peekWord()) {
		case "REPLICA":
			b.readWord()
			trig.EnableState = "ENABLE REPLICA"
		case "ALWAYS":
			b.readWord()
			trig.EnableState = "ENABLE ALWAYS"
		default:
			return trig, b.errorf("expected REPLICA or ALWAYS after ENABLE in trigger-enable-state, got %q", b.peekWord())
		}
	default:
		b.restore(enableMark)
	}

	// [NO] DEPENDS ON EXTENSION ext (Section 9.1, reused verbatim for
	// triggers per Section 7.9, audit item #75) — the ABNF's single
	// square brackets mark this as optional (0 or 1), not repeatable like
	// Function/Procedure's func-block version, and it's one of several
	// trailing clauses sharing the trigger-decl's own single terminating
	// ";" rather than being its own semicolon-terminated directive.
	b.skipWS()
	dependsMark := b.cur()
	switch strings.ToUpper(b.peekWord()) {
	case "DEPENDS":
		b.readWord()
		ext, derr := b.parseOnExtensionName()
		if derr != nil {
			return trig, derr
		}
		trig.DependsOnExtensions = append(trig.DependsOnExtensions, ext)
	case "NO":
		b.readWord()
		if err := b.expect("DEPENDS"); err != nil {
			return trig, err
		}
		// Contributes nothing to DependsOnExtensions — see
		// pipeline.BlockAST.DependsOnExtensions' identical doc comment
		// for why the negative form is parsed but a no-op.
		if _, derr := b.parseOnExtensionName(); derr != nil {
			return trig, derr
		}
	default:
		b.restore(dependsMark)
	}

	// RENAMED FROM (Section 7.9): the last optional clause on a
	// trigger-decl, right before the terminating ';' — same inline,
	// no-own-semicolon shape as Constraint/Index/Policy's identical
	// sub-object RENAMED FROM, not the top-level BLOCK directive
	// parseRenamedFrom handles.
	b.skipWS()
	renameMark := b.cur()
	if strings.ToUpper(b.peekWord()) == "RENAMED" {
		b.readWord()
		if err := b.expect("FROM"); err != nil {
			return trig, err
		}
		b.skipWS()
		renamed, rerr := b.readIdentifier()
		if rerr != nil {
			return trig, rerr
		}
		trig.RenamedFrom = &renamed
	} else {
		b.restore(renameMark)
	}

	comment, err := b.parseTrailingCommentBlock()
	if err != nil {
		return trig, err
	}
	trig.Comment = comment
	return trig, nil
}

// ── GRANTS ────────────────────────────────────────────────────────────────────

// GRANTS { SELECT, INSERT TO role1, role2; ... }
func (b *blockParser) parseGrantsBlock(pos pipeline.SourcePos) ([]pipeline.GrantEntry, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var grants []pipeline.GrantEntry
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		g, err := b.parseOneGrant(pos)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close GRANTS block")
	}
	b.advance()
	return grants, nil
}

// Syntax: [ALL PRIVILEGES | priv1, priv2] TO role1[, role2] [WITH GRANT OPTION];
func (b *blockParser) parseOneGrant(pos pipeline.SourcePos) (pipeline.GrantEntry, error) {
	g := pipeline.GrantEntry{Pos: pos}

	// Privileges
	b.skipWS()
	c := b.cur()
	first := strings.ToUpper(b.readWord())
	if first == "ALL" {
		b.skipWS()
		// optional PRIVILEGES keyword
		c2 := b.cur()
		if strings.ToUpper(b.peekWord()) == "PRIVILEGES" {
			b.readWord()
		} else {
			b.restore(c2)
		}
		g.Privileges = nil // nil = ALL
	} else {
		b.restore(c)
		// Read comma-separated privileges
		for {
			b.skipWS()
			priv := strings.ToUpper(b.readWord())
			if priv == "" {
				break
			}
			g.Privileges = append(g.Privileges, priv)
			b.skipWS()
			if b.peek() != ',' {
				break
			}
			b.advance()
			// Stop if next token is TO
			b.skipWS()
			if strings.ToUpper(b.peekWord()) == "TO" {
				break
			}
		}
	}

	// TO
	if err := b.expect("TO"); err != nil {
		return g, err
	}

	// Roles
	for {
		b.skipWS()
		r, err := b.readRoleIdentifier()
		if err != nil {
			return g, err
		}
		g.Roles = append(g.Roles, r)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "WITH" {
			break
		}
	}

	// Optional WITH GRANT OPTION
	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "WITH" {
		b.readWord()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "GRANT" {
			b.readWord()
			if err := b.expect("OPTION"); err != nil {
				return g, err
			}
			g.WithGrant = true
		} else {
			b.restore(c)
		}
	}

	// Optional GRANTED BY role-spec (RFC audit item #90, Section 7.10) —
	// real PostgreSQL restricts the effective grantor to current_user in
	// practice, but the clause itself is genuine, accepted grammar.
	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "GRANTED" {
		b.readWord()
		if err := b.expect("BY"); err != nil {
			return g, err
		}
		spec, err := b.parseRoleSpec()
		if err != nil {
			return g, err
		}
		g.GrantedBy = &spec
	} else {
		b.restore(c)
	}

	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return g, nil
}

// parseRoleSpec reads the shared role-spec production (Appendix A,
// referenced by GRANTED BY here and by Role membership WITH clauses):
// either a plain (possibly quoted) identifier, or one of the three fixed
// keywords CURRENT_ROLE/CURRENT_USER/SESSION_USER (returned upper-cased —
// PostgreSQL's own RoleSpec AST node treats these as keywords, not names).
func (b *blockParser) parseRoleSpec() (string, error) {
	b.skipWS()
	c := b.cur()
	switch strings.ToUpper(b.peekWord()) {
	case "CURRENT_ROLE", "CURRENT_USER", "SESSION_USER":
		return strings.ToUpper(b.readWord()), nil
	}
	b.restore(c)
	id, err := b.readIdentifier()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// ── REVOKES ───────────────────────────────────────────────────────────────

func (b *blockParser) parseRevocationsBlock(pos pipeline.SourcePos) ([]pipeline.RevocationEntry, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var revs []pipeline.RevocationEntry
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		r, err := b.parseOneRevocation(pos)
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close REVOKES block")
	}
	b.advance()
	return revs, nil
}

// Syntax: [ALL PRIVILEGES | priv1, priv2] FROM role1[, role2] [CASCADE];
func (b *blockParser) parseOneRevocation(pos pipeline.SourcePos) (pipeline.RevocationEntry, error) {
	r := pipeline.RevocationEntry{Pos: pos}

	b.skipWS()
	c := b.cur()
	first := strings.ToUpper(b.readWord())
	if first == "ALL" {
		b.skipWS()
		c2 := b.cur()
		if strings.ToUpper(b.peekWord()) == "PRIVILEGES" {
			b.readWord()
		} else {
			b.restore(c2)
		}
		r.Privileges = nil
	} else {
		b.restore(c)
		for {
			b.skipWS()
			priv := strings.ToUpper(b.readWord())
			if priv == "" {
				break
			}
			r.Privileges = append(r.Privileges, priv)
			b.skipWS()
			if b.peek() != ',' {
				break
			}
			b.advance()
			b.skipWS()
			if strings.ToUpper(b.peekWord()) == "FROM" {
				break
			}
		}
	}

	if err := b.expect("FROM"); err != nil {
		return r, err
	}

	for {
		b.skipWS()
		role, err := b.readRoleIdentifier()
		if err != nil {
			return r, err
		}
		r.Roles = append(r.Roles, role)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "CASCADE" {
			break
		}
	}

	// Optional GRANTED BY role-spec — see parseOneGrant's identical clause;
	// grammar order here is GRANTED BY before CASCADE (RFC's revoke-entry).
	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "GRANTED" {
		b.readWord()
		if err := b.expect("BY"); err != nil {
			return r, err
		}
		spec, err := b.parseRoleSpec()
		if err != nil {
			return r, err
		}
		r.GrantedBy = &spec
	} else {
		b.restore(c)
	}

	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "CASCADE" {
		b.readWord()
		r.Cascade = true
	} else {
		b.restore(c)
	}

	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return r, nil
}

// ── PARTITIONS ────────────────────────────────────────────────────────────────

func (b *blockParser) parsePartitionsBlock(pos pipeline.SourcePos) (*pipeline.PartitionDef, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	pd := &pipeline.PartitionDef{Pos: pos}
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		bound, err := b.parseOnePartitionBound()
		if err != nil {
			return nil, err
		}
		pd.Partitions = append(pd.Partitions, bound)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close PARTITIONS block")
	}
	b.advance()
	return pd, nil
}

// parsePartitionBlockBody parses a single partition entry's own trailing
// { } body — a narrow subset of the general object block grammar, holding
// only two possible directives: PARTITIONS { ... } (sub-partitioning,
// RFC Section 7.13, only legal when this entry itself has a PARTITION BY
// clause — allowSubPartitions gates it) and CONSTRAINT/CONSTRAINTS (RFC
// Section 7.3's "DROP CONSTRAINT ... ONLY" gap, PostgreSQL 18+ — a partition
// declaring one or more constraints independently of its parent, using the
// identical grammar a table's own { } body already accepts for the same
// directive). Both may appear together in either order, since real
// PostgreSQL treats "further sub-partitioned" and "carries its own local
// constraints" as entirely independent facts about a partition.
func (b *blockParser) parsePartitionBlockBody(allowSubPartitions bool) ([]pipeline.PartitionBound, []pipeline.ConstraintDef, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, nil, err
	}
	var subPartitions []pipeline.PartitionBound
	var constraints []pipeline.ConstraintDef
	for {
		b.skipWS()
		if b.eof() {
			return nil, nil, b.errorf("unexpected EOF in partition block")
		}
		if b.peek() == '}' {
			break
		}
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		switch word {
		case "PARTITIONS":
			if !allowSubPartitions {
				return nil, nil, b.errorf("PARTITIONS block requires a preceding PARTITION BY clause on this partition")
			}
			pd, err := b.parsePartitionsBlock(dirPos)
			if err != nil {
				return nil, nil, err
			}
			subPartitions = append(subPartitions, pd.Partitions...)
		case "CONSTRAINT":
			cst, err := b.parseConstraint(dirPos)
			if err != nil {
				return nil, nil, err
			}
			constraints = append(constraints, cst)
		case "CONSTRAINTS":
			csts, err := b.parseConstraintsBlock(dirPos)
			if err != nil {
				return nil, nil, err
			}
			constraints = append(constraints, csts...)
		default:
			return nil, nil, b.errorf("unexpected block directive %q in partition entry (only CONSTRAINT/CONSTRAINTS, and PARTITIONS when PARTITION BY is present, are allowed)", word)
		}
	}
	b.advance() // consume '}'
	return subPartitions, constraints, nil
}

// subPartitionByRe matches a trailing "PARTITION BY <strategy> (<cols>)" clause
// at the end of a partition entry's raw bounds text, i.e. RFC Section 7.13
// sub-partitioning. Bounds-clause literals never contain the word PARTITION,
// so a plain trailing match is unambiguous.
var subPartitionByRe = regexp.MustCompile(`(?is)^(.*?)\bPARTITION\s+BY\s+(RANGE|LIST|HASH)\s*\(([^)]*)\)\s*$`)

// renamedFromSuffixRe matches a trailing "RENAMED FROM name" clause at the
// end of a raw-text-captured entry (a partition's bounds, Section 7.13; a
// table constraint's body, Section 7.3) — the shape shared by every
// declaration whose body is captured as opaque raw text rather than
// tokenized field-by-field, so RENAMED FROM can't be read via readIdentifier
// mid-parse the way it is for a proper `{ }` block directive. For a
// partition entry specifically, this is applied to whatever text remains
// after subPartitionByRe has already stripped off any trailing sub-
// partitioning suffix — RENAMED FROM sits before PARTITION BY in that
// grammar. The identifier alternative accepts a bare word or a double-quoted
// identifier (unquoted below via unquoteRawIdent), the same two forms
// readIdentifier accepts elsewhere in this parser. Same assumption as
// subPartitionByRe above: a raw body is never expected to end in something
// that looks like "RENAMED FROM identifier" unless it genuinely is one, so a
// plain trailing match is unambiguous in practice.
var renamedFromSuffixRe = regexp.MustCompile(`(?is)^(.*?)\bRENAMED\s+FROM\s+("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)\s*$`)

// unquoteRawIdent strips a double-quoted identifier's surrounding quotes and
// un-escapes doubled internal quotes ("" -> "), matching readQuotedString's
// own escaping rules. Returns s unchanged if it isn't quoted.
func unquoteRawIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

// parseForeignServerClause splits raw (a FOREIGN partition's captured bounds
// text, with any RENAMED FROM/sub-partitioning suffix already stripped) into
// the actual bounds expression and its trailing "SERVER ident [OPTIONS
// (key 'value', ...)]" clause (RFC Section 7.13). Re-parses with a fresh
// blockParser instance over just this substring (the same sub-parsing
// convention ParseDefaultPrivileges already uses) rather than a regex: an
// OPTIONS value is an arbitrary single-quoted string that may itself contain
// commas, parens, or even the word SERVER, and only a real tokenizer (via
// readSingleQuotedString's own ” escaping) can tell those apart from actual
// clause punctuation. SERVER is mandatory here — confirmed live that
// PostgreSQL's own CREATE FOREIGN TABLE grammar rejects the statement
// outright with no SERVER clause at all ("syntax error at end of input"),
// partition or not.
func parseForeignServerClause(raw string, pos pipeline.SourcePos) (string, pipeline.Identifier, []pipeline.StorageParam, error) {
	fp := &blockParser{src: []byte(raw), file: pos.File, line: pos.Line, col: pos.Col}
	serverPos := findTopLevelWord(fp, "SERVER")
	if serverPos < 0 {
		return "", pipeline.Identifier{}, nil, fp.errorf("FOREIGN partition requires a SERVER clause")
	}
	boundsText := string(fp.src[:serverPos])
	// fp's line/col drift during the scan above is never surfaced (this
	// sub-parser's positions only feed its own error messages, not the
	// returned bound's SourcePos), so resetting only pos is enough to
	// resume reading forward correctly.
	fp.pos = serverPos
	if err := fp.expect("SERVER"); err != nil {
		return "", pipeline.Identifier{}, nil, err
	}
	server, err := fp.readIdentifier()
	if err != nil {
		return "", pipeline.Identifier{}, nil, err
	}
	var opts []pipeline.StorageParam
	fp.skipWS()
	if strings.EqualFold(fp.peekWord(), "OPTIONS") {
		fp.readWord()
		fp.skipWS()
		if fp.peek() != '(' {
			return "", pipeline.Identifier{}, nil, fp.errorf("expected '(' after OPTIONS, got %q", fp.peek())
		}
		fp.advance()
		fp.skipWS()
		for fp.peek() != ')' {
			key, err := fp.readIdentifier()
			if err != nil {
				return "", pipeline.Identifier{}, nil, err
			}
			fp.skipWS()
			val, err := fp.readSingleQuotedString()
			if err != nil {
				return "", pipeline.Identifier{}, nil, err
			}
			opts = append(opts, pipeline.StorageParam{Key: key.Name, Value: val})
			fp.skipWS()
			switch fp.peek() {
			case ',':
				fp.advance()
				fp.skipWS()
			case ')':
				// Loop condition below exits on the next check.
			default:
				return "", pipeline.Identifier{}, nil, fp.errorf("expected ',' or ')' in OPTIONS list, got %q", fp.peek())
			}
		}
		fp.advance() // consume ')'
	}
	fp.skipWS()
	if !fp.eof() {
		return "", pipeline.Identifier{}, nil, fp.errorf("unexpected trailing text after SERVER clause: %q", string(fp.src[fp.pos:]))
	}
	return boundsText, server, opts, nil
}

// findTopLevelWord scans fp from its current position for word as a
// case-insensitive whole word at paren depth 0, outside any single-quoted
// string, and returns its starting offset (or -1 if not found). Mutates
// fp's cursor as a side effect of the scan; callers that need the original
// position afterward must restore it themselves.
func findTopLevelWord(fp *blockParser, word string) int {
	depth := 0
	for !fp.eof() {
		ch := fp.peek()
		switch {
		case ch == '(':
			depth++
			fp.advance()
		case ch == ')':
			if depth > 0 {
				depth--
			}
			fp.advance()
		case ch == '\'':
			if _, err := fp.readSingleQuotedString(); err != nil {
				return -1
			}
		case depth == 0 && isWordStart(ch):
			start := fp.pos
			w := fp.readWord()
			if strings.EqualFold(w, word) {
				return start
			}
		default:
			fp.advance()
		}
	}
	return -1
}

// parseOnePartitionBound parses a single "name bounds [PARTITION BY strategy
// (cols) { PARTITIONS {...} }];" entry, shared by parsePartitionsBlock
// (Mode A, inside a PARTITIONS { } block) and the PARTITION singular-keyword
// dispatch case (Mode B). The trailing PARTITION BY clause is optional and
// describes sub-partitioning (RFC Section 7.13): a partition entry may itself be
// further partitioned, recursively.
func (b *blockParser) parseOnePartitionBound() (pipeline.PartitionBound, error) {
	pPos := b.srcPos()
	b.skipWS()
	c := b.cur()
	firstWord := strings.ToUpper(b.readWord())
	// RFC Section 7.13's "FOREIGN partition-name ... SERVER server_name
	// [OPTIONS (...)]" form — makes this partition a foreign table instead
	// of a regular one. Checked before ATTACHED below: the two forms don't
	// combine (attaching an already-existing table's own declaration
	// already determines whether it's foreign; restating FOREIGN there
	// would be redundant, and the RFC doesn't define the combination).
	isForeign := false
	if firstWord == "FOREIGN" {
		isForeign = true
		b.skipWS()
		c = b.cur()
		firstWord = strings.ToUpper(b.readWord())
	}
	if isForeign && firstWord == "ATTACHED" {
		return pipeline.PartitionBound{}, b.errorf("FOREIGN cannot be combined with ATTACHED FROM — the referenced table's own declaration already determines whether it is a foreign table")
	}
	if firstWord == "ATTACHED" {
		// RFC Section 7.13's "ATTACHED FROM existing_table" form — attaches
		// an already-existing standalone table instead of creating a new
		// one. No trailing sub-partitioning/RENAMED FROM suffix support
		// here (the RFC doesn't specify combining ATTACHED FROM with
		// either): the ref is read, then everything up to the terminator
		// is taken verbatim as the bounds clause ("FOR VALUES ..." or
		// "DEFAULT"), the same shape createPartitionOps already expects.
		if err := b.expect("FROM"); err != nil {
			return pipeline.PartitionBound{}, err
		}
		b.skipWS()
		ref, err := b.readIdentifier()
		if err != nil {
			return pipeline.PartitionBound{}, err
		}
		raw, err := b.readRawUntil(";{}")
		if err != nil {
			return pipeline.PartitionBound{}, err
		}
		bound := pipeline.PartitionBound{
			Name:         ref,
			AttachedFrom: &ref,
			Bounds:       pipeline.RawExpr{Text: strings.TrimSpace(raw), Pos: pPos},
			Pos:          pPos,
		}
		b.skipWS()
		if b.peek() == ';' {
			b.advance()
		}
		return bound, nil
	}
	b.restore(c)

	name, err := b.readIdentifier()
	if err != nil {
		return pipeline.PartitionBound{}, err
	}
	// Read everything up to ';', a nested sub-partition '{', or end of block.
	raw, err := b.readRawUntil(";{}")
	if err != nil {
		return pipeline.PartitionBound{}, err
	}

	bound := pipeline.PartitionBound{Name: name, Pos: pPos}

	// Strip a trailing sub-partitioning suffix first (it's expected last in
	// the grammar), then a trailing RENAMED FROM from whatever remains
	// (RFC Section 7.13: RENAMED FROM precedes the PARTITION BY suffix) — in
	// either order relative to each other, only one strip applies per
	// clause, so extracting sub-partitioning before RENAMED FROM (rather
	// than the reverse) is just the fixed order the grammar declares.
	boundsText := raw
	hasSubPartition := false
	var subStrategy string
	var subColumns []string
	if m := subPartitionByRe.FindStringSubmatch(raw); m != nil {
		hasSubPartition = true
		boundsText = m[1]
		subStrategy = strings.ToUpper(m[2])
		for _, c := range splitTopLevel(m[3], ',') {
			c = strings.TrimSpace(c)
			if c != "" {
				subColumns = append(subColumns, c)
			}
		}
	}
	if rm := renamedFromSuffixRe.FindStringSubmatch(boundsText); rm != nil {
		renamed := unquoteRawIdent(rm[2])
		bound.RenamedFrom = &renamed
		boundsText = rm[1]
	}
	if isForeign {
		// Foreign tables can never be further partitioned (confirmed live:
		// real PostgreSQL rejects PARTITION BY on a foreign table with a
		// syntax error), so the sub-partitioning suffix stripped above must
		// never have matched here.
		if hasSubPartition {
			return pipeline.PartitionBound{}, b.errorf("FOREIGN partition %q cannot declare its own PARTITION BY — a foreign table cannot be further partitioned", name.Name)
		}
		bt, server, opts, serr := parseForeignServerClause(boundsText, pPos)
		if serr != nil {
			return pipeline.PartitionBound{}, serr
		}
		boundsText = bt
		bound.Foreign = true
		bound.Server = &server
		bound.Options = opts
	}
	bound.Bounds = pipeline.RawExpr{Text: strings.TrimSpace(boundsText), Pos: pPos}

	if hasSubPartition {
		bound.SubStrategy = subStrategy
		bound.SubColumns = subColumns
		if b.peek() != '{' {
			return pipeline.PartitionBound{}, b.errorf("expected '{' after PARTITION BY %s clause", subStrategy)
		}
	}
	if b.peek() == '{' {
		subParts, csts, err := b.parsePartitionBlockBody(hasSubPartition)
		if err != nil {
			return pipeline.PartitionBound{}, err
		}
		bound.SubPartitions = subParts
		bound.Constraints = csts
	}

	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return bound, nil
}

// ── MIGRATE REMOVE ────────────────────────────────────────────────────────────

func (b *blockParser) parseMigrateRemove(pos pipeline.SourcePos) (*pipeline.MigrateRemoveBlock, error) {
	if err := b.expect("REMOVE"); err != nil {
		return nil, err
	}
	// Optional reason in parens
	b.skipWS()
	var reason string
	if b.peek() == '(' {
		b.advance()
		raw, err := b.readRawUntil(")")
		if err != nil {
			return nil, err
		}
		b.advance()
		reason = strings.TrimSpace(raw)
	}
	// consumeBrace consumes '{'; readBraceBlock reads content until '}' (inclusive).
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	sqlRaw, err := b.readBraceBlock()
	if err != nil {
		return nil, err
	}
	return &pipeline.MigrateRemoveBlock{
		Reason: reason,
		SQL:    pipeline.RawExpr{Text: strings.TrimSpace(sqlRaw), Pos: pos},
		Pos:    pos,
	}, nil
}

// ── DEFAULT PRIVILEGES ────────────────────────────────────────────────────────

// parseDefaultPrivileges is the nested-directive entry point (dispatched
// from parseBlock's "DEFAULT" case when DEFAULT PRIVILEGES appears inside
// an enclosing object's block, e.g. nested inside SCHEMA — the caller has
// already consumed "DEFAULT").
func (b *blockParser) parseDefaultPrivileges(pos pipeline.SourcePos) (pipeline.DefaultPrivilegesBlock, error) {
	if err := b.expect("PRIVILEGES"); err != nil {
		return pipeline.DefaultPrivilegesBlock{}, err
	}
	return b.parseDefaultPrivilegesBody(pos)
}

// ParseDefaultPrivileges is the top-level entry point for a DEFAULT
// PRIVILEGES declaration that is NOT nested inside another object's block.
// Unlike every other DPG object kind, DEFAULT PRIVILEGES is never split into
// a pg_query-parsed Part 1 and a blockparser-parsed Part 2: real
// PostgreSQL's ALTER DEFAULT PRIVILEGES statement requires its GRANT/REVOKE
// action inline in the same statement, so a header-only fragment like
// "FOR ROLE x IN SCHEMA y" is never valid PG SQL on its own (confirmed live:
// "syntax error at end of input"). header is the raw text between "DEFAULT
// PRIVILEGES" and the opening '{' (e.g. "FOR ROLE x IN SCHEMA y"); body is
// the text inside that '{ }' (matching pipeline.BlockParser.Parse's own
// part2 convention — braces excluded).
func ParseDefaultPrivileges(header, body string, pos pipeline.SourcePos) (pipeline.DefaultPrivilegesBlock, error) {
	hp := &blockParser{src: []byte(header), file: pos.File, line: pos.Line, col: pos.Col}
	dp, err := hp.parseDefaultPrivilegesHeader(pos)
	if err != nil {
		return dp, err
	}
	bp := &blockParser{src: []byte(body), file: pos.File, line: pos.Line, col: pos.Col}
	if err := bp.parseDefaultPrivilegesEntries(&dp); err != nil {
		return dp, err
	}
	return dp, nil
}

// parseDefaultPrivilegesBody parses "[IN SCHEMA x] [FOR ROLE y] { entries }"
// from a single source (the nested-directive case, where header and braces
// all come from the same enclosing block's text).
func (b *blockParser) parseDefaultPrivilegesBody(pos pipeline.SourcePos) (pipeline.DefaultPrivilegesBlock, error) {
	dp, err := b.parseDefaultPrivilegesHeader(pos)
	if err != nil {
		return dp, err
	}
	if err := b.consumeBrace(); err != nil {
		return dp, err
	}
	if err := b.parseDefaultPrivilegesEntries(&dp); err != nil {
		return dp, err
	}
	b.skipWS()
	if b.peek() != '}' {
		return dp, b.errorf("expected '}' to close DEFAULT PRIVILEGES block")
	}
	b.advance()
	return dp, nil
}

// parseDefaultPrivilegesHeader parses the optional "[IN SCHEMA x] [FOR ROLE
// y]" clauses, in either order, consuming nothing past them (no brace).
func (b *blockParser) parseDefaultPrivilegesHeader(pos pipeline.SourcePos) (pipeline.DefaultPrivilegesBlock, error) {
	dp := pipeline.DefaultPrivilegesBlock{Pos: pos}
	for {
		b.skipWS()
		c := b.cur()
		word := strings.ToUpper(b.peekWord())
		switch word {
		case "IN":
			if dp.InSchema != nil {
				return dp, b.errorf("IN SCHEMA specified more than once")
			}
			b.readWord()
			if err := b.expect("SCHEMA"); err != nil {
				return dp, err
			}
			b.skipWS()
			s, err := b.readIdentifier()
			if err != nil {
				return dp, err
			}
			dp.InSchema = &s
		case "FOR":
			if dp.ForRole != nil {
				return dp, b.errorf("FOR ROLE specified more than once")
			}
			b.readWord()
			b.skipWS()
			_ = b.readWord() // ROLE keyword
			b.skipWS()
			r, err := b.readIdentifier()
			if err != nil {
				return dp, err
			}
			dp.ForRole = &r
		default:
			b.restore(c)
			return dp, nil
		}
	}
}

// parseDefaultPrivilegesEntries parses zero or more GRANTS/GRANT/
// REVOKES/REVOKE directives (Mode A/B, same dual-definition
// convention as every other block) until EOF or an unconsumed '}'.
func (b *blockParser) parseDefaultPrivilegesEntries(dp *pipeline.DefaultPrivilegesBlock) error {
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			return nil
		}
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		switch word {
		case "GRANTS":
			grants, err := b.parseDefaultPrivilegeGrantsBlock(dirPos)
			if err != nil {
				return err
			}
			dp.Grants = append(dp.Grants, grants...)
		case "GRANT":
			// Mode B (Section 4.8 Dual Definition Modes): singular entry, no wrapping '{ }'.
			g, err := b.parseOneDefaultPrivilegeGrant(dirPos)
			if err != nil {
				return err
			}
			dp.Grants = append(dp.Grants, g)
		case "REVOKES":
			revs, err := b.parseDefaultPrivilegeRevocationsBlock(dirPos)
			if err != nil {
				return err
			}
			dp.Revocations = append(dp.Revocations, revs...)
		case "REVOKE":
			// Mode B: singular entry, no wrapping '{ }'.
			r, err := b.parseOneDefaultPrivilegeRevocation(dirPos)
			if err != nil {
				return err
			}
			dp.Revocations = append(dp.Revocations, r)
		default:
			return fmt.Errorf("%s: DPG-E024: unexpected directive %q in DEFAULT PRIVILEGES block%s", dirPos, word, pipeline.RenamedKeywordHint(word))
		}
	}
}

func (b *blockParser) parseDefaultPrivilegeGrantsBlock(pos pipeline.SourcePos) ([]pipeline.DefaultPrivilegeGrant, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var grants []pipeline.DefaultPrivilegeGrant
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		g, err := b.parseOneDefaultPrivilegeGrant(b.srcPos())
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close GRANTS block")
	}
	b.advance()
	return grants, nil
}

// parseOneDefaultPrivilegeGrant parses "priv[, ...] ON objtype TO role[, ...]
// [WITH GRANT OPTION];" — real PostgreSQL's own ALTER DEFAULT PRIVILEGES
// grant-clause grammar, confirmed against real PG (not a DPG invention):
// the object type is part of the GRANT clause itself, unlike a regular
// table/view GRANTS block where the object is implicit from the enclosing
// declaration.
func (b *blockParser) parseOneDefaultPrivilegeGrant(pos pipeline.SourcePos) (pipeline.DefaultPrivilegeGrant, error) {
	g := pipeline.DefaultPrivilegeGrant{Pos: pos}
	b.skipWS()
	c := b.cur()
	first := strings.ToUpper(b.readWord())
	if first == "ALL" {
		b.skipWS()
		c2 := b.cur()
		if strings.ToUpper(b.peekWord()) == "PRIVILEGES" {
			b.readWord()
		} else {
			b.restore(c2)
		}
		g.Privileges = nil // nil = ALL
	} else {
		b.restore(c)
		for {
			b.skipWS()
			priv := strings.ToUpper(b.readWord())
			if priv == "" {
				break
			}
			g.Privileges = append(g.Privileges, priv)
			b.skipWS()
			if b.peek() != ',' {
				break
			}
			b.advance()
			b.skipWS()
			if strings.ToUpper(b.peekWord()) == "ON" {
				break
			}
		}
	}
	if err := b.expect("ON"); err != nil {
		return g, err
	}
	b.skipWS()
	g.ObjectType = strings.ToUpper(b.readWord())
	if g.ObjectType == "" {
		return g, b.errorf("expected an object type (TABLES/SEQUENCES/FUNCTIONS/TYPES/SCHEMAS) after ON")
	}
	if err := b.expect("TO"); err != nil {
		return g, err
	}
	for {
		b.skipWS()
		r, err := b.readRoleIdentifier()
		if err != nil {
			return g, err
		}
		g.Roles = append(g.Roles, r)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "WITH" {
			break
		}
	}
	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "WITH" {
		b.readWord()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "GRANT" {
			b.readWord()
			if err := b.expect("OPTION"); err != nil {
				return g, err
			}
			g.WithGrant = true
		} else {
			b.restore(c)
		}
	}
	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return g, nil
}

func (b *blockParser) parseDefaultPrivilegeRevocationsBlock(pos pipeline.SourcePos) ([]pipeline.DefaultPrivilegeRevocation, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var revs []pipeline.DefaultPrivilegeRevocation
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		r, err := b.parseOneDefaultPrivilegeRevocation(b.srcPos())
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close REVOKES block")
	}
	b.advance()
	return revs, nil
}

// parseOneDefaultPrivilegeRevocation mirrors parseOneDefaultPrivilegeGrant —
// real PostgreSQL's ALTER DEFAULT PRIVILEGES ... REVOKE clause: "priv[, ...]
// ON objtype FROM role[, ...] [CASCADE];".
func (b *blockParser) parseOneDefaultPrivilegeRevocation(pos pipeline.SourcePos) (pipeline.DefaultPrivilegeRevocation, error) {
	r := pipeline.DefaultPrivilegeRevocation{Pos: pos}
	b.skipWS()
	c := b.cur()
	first := strings.ToUpper(b.readWord())
	if first == "ALL" {
		b.skipWS()
		c2 := b.cur()
		if strings.ToUpper(b.peekWord()) == "PRIVILEGES" {
			b.readWord()
		} else {
			b.restore(c2)
		}
		r.Privileges = nil
	} else {
		b.restore(c)
		for {
			b.skipWS()
			priv := strings.ToUpper(b.readWord())
			if priv == "" {
				break
			}
			r.Privileges = append(r.Privileges, priv)
			b.skipWS()
			if b.peek() != ',' {
				break
			}
			b.advance()
			b.skipWS()
			if strings.ToUpper(b.peekWord()) == "ON" {
				break
			}
		}
	}
	if err := b.expect("ON"); err != nil {
		return r, err
	}
	b.skipWS()
	r.ObjectType = strings.ToUpper(b.readWord())
	if r.ObjectType == "" {
		return r, b.errorf("expected an object type (TABLES/SEQUENCES/FUNCTIONS/TYPES/SCHEMAS) after ON")
	}
	if err := b.expect("FROM"); err != nil {
		return r, err
	}
	for {
		b.skipWS()
		role, err := b.readRoleIdentifier()
		if err != nil {
			return r, err
		}
		r.Roles = append(r.Roles, role)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "CASCADE" {
			break
		}
	}
	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "CASCADE" {
		b.readWord()
		r.Cascade = true
	} else {
		b.restore(c)
	}
	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return r, nil
}

// ── PARAMETER PRIVILEGES ──────────────────────────────────────────────────────

// ParseParameterPrivileges is the top-level entry point for a PARAMETER
// PRIVILEGES declaration (RFC Section 11.6, PG15+). header is the raw text
// between "PARAMETER PRIVILEGES" and the opening '{' — always empty for
// valid source, since unlike DEFAULT PRIVILEGES this kind has no FOR
// ROLE/IN SCHEMA clause (parameters have no schema and the grant targets a
// role directly); body is the text inside that '{ }' (braces excluded,
// matching pipeline.BlockParser.Parse's own part2 convention).
func ParseParameterPrivileges(header, body string, pos pipeline.SourcePos) (pipeline.ParameterPrivilegesBlock, error) {
	if h := strings.TrimSpace(header); h != "" {
		return pipeline.ParameterPrivilegesBlock{}, fmt.Errorf("%s: unexpected %q before '{' in PARAMETER PRIVILEGES declaration", pos, h)
	}
	pp := pipeline.ParameterPrivilegesBlock{Pos: pos}
	bp := &blockParser{src: []byte(body), file: pos.File, line: pos.Line, col: pos.Col}
	if err := bp.parseParameterPrivilegesEntries(&pp); err != nil {
		return pp, err
	}
	return pp, nil
}

// parseParameterPrivilegesEntries parses zero or more GRANTS/REVOKES
// blocks until EOF or an unconsumed '}'. Unlike DEFAULT PRIVILEGES' pp-block
// grammar, RFC Section 11.6 defines only the block forms (pp-grants-block /
// pp-revocations-block) — no singular Mode B "GRANT"/"REVOKE" directive.
func (b *blockParser) parseParameterPrivilegesEntries(pp *pipeline.ParameterPrivilegesBlock) error {
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			return nil
		}
		dirPos := b.srcPos()
		word := strings.ToUpper(b.readWord())
		switch word {
		case "GRANTS":
			grants, err := b.parseParameterGrantsBlock(dirPos)
			if err != nil {
				return err
			}
			pp.Grants = append(pp.Grants, grants...)
		case "REVOKES":
			revs, err := b.parseParameterRevocationsBlock(dirPos)
			if err != nil {
				return err
			}
			pp.Revocations = append(pp.Revocations, revs...)
		default:
			return fmt.Errorf("%s: DPG-E024: unexpected directive %q in PARAMETER PRIVILEGES block%s", dirPos, word, pipeline.RenamedKeywordHint(word))
		}
	}
}

func (b *blockParser) parseParameterGrantsBlock(pos pipeline.SourcePos) ([]pipeline.ParameterGrant, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var grants []pipeline.ParameterGrant
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		g, err := b.parseOneParameterGrant(b.srcPos())
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close GRANTS block")
	}
	b.advance()
	return grants, nil
}

// parsePPPrivilegeList parses a comma-separated pp-privilege-list: "SET" /
// "ALTER SYSTEM" / "ALL" / "ALL PRIVILEGES" (RFC Section 11.6's pp-privilege
// production). "ALTER SYSTEM" is the one two-word privilege token in this
// grammar — every other GRANTS/REVOKES block in DPG has single-word
// privileges only. "ALL"/"ALL PRIVILEGES" is represented as nil, matching
// every other privilege list's ALL convention (privStr renders nil back to
// "ALL").
func (b *blockParser) parsePPPrivilegeList() ([]string, error) {
	b.skipWS()
	c := b.cur()
	first := strings.ToUpper(b.readWord())
	if first == "ALL" {
		b.skipWS()
		c2 := b.cur()
		if strings.ToUpper(b.peekWord()) == "PRIVILEGES" {
			b.readWord()
		} else {
			b.restore(c2)
		}
		return nil, nil
	}
	b.restore(c)
	var privs []string
	for {
		b.skipWS()
		word := strings.ToUpper(b.readWord())
		if word == "" {
			return nil, b.errorf("expected a privilege (SET/ALTER SYSTEM/ALL) in PARAMETER PRIVILEGES entry")
		}
		if word == "ALTER" {
			b.skipWS()
			if strings.ToUpper(b.peekWord()) != "SYSTEM" {
				return nil, b.errorf("expected SYSTEM after ALTER in PARAMETER PRIVILEGES entry")
			}
			b.readWord()
			word = "ALTER SYSTEM"
		}
		privs = append(privs, word)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
	}
	return privs, nil
}

// parseOneParameterGrant parses "pp-privilege-list ON PARAMETER
// identifier-list TO role-list [WITH GRANT OPTION];" (RFC Section 11.6's
// pp-grant-entry) — real PostgreSQL's own GRANT ... ON PARAMETER grammar
// (confirmed live), just with "ON PARAMETER" as two fixed words rather than
// DefaultPrivilegeGrant's single-word "ON <object-type>" clause.
func (b *blockParser) parseOneParameterGrant(pos pipeline.SourcePos) (pipeline.ParameterGrant, error) {
	g := pipeline.ParameterGrant{Pos: pos}
	privs, err := b.parsePPPrivilegeList()
	if err != nil {
		return g, err
	}
	g.Privileges = privs
	if err := b.expect("ON"); err != nil {
		return g, err
	}
	if err := b.expect("PARAMETER"); err != nil {
		return g, err
	}
	for {
		b.skipWS()
		p, err := b.readIdentifier()
		if err != nil {
			return g, err
		}
		g.Parameters = append(g.Parameters, p)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
	}
	if err := b.expect("TO"); err != nil {
		return g, err
	}
	for {
		b.skipWS()
		r, err := b.readRoleIdentifier()
		if err != nil {
			return g, err
		}
		g.Roles = append(g.Roles, r)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "WITH" {
			break
		}
	}
	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.peekWord()) == "WITH" {
		b.readWord()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "GRANT" {
			b.readWord()
			if err := b.expect("OPTION"); err != nil {
				return g, err
			}
			g.WithGrant = true
		} else {
			b.restore(c)
		}
	}

	// Optional GRANTED BY role-spec — see parseOneGrant's identical clause;
	// real PostgreSQL's GRANT ... ON PARAMETER accepts it the same as every
	// other GRANT form (confirmed live, PG17).
	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "GRANTED" {
		b.readWord()
		if err := b.expect("BY"); err != nil {
			return g, err
		}
		spec, err := b.parseRoleSpec()
		if err != nil {
			return g, err
		}
		g.GrantedBy = &spec
	} else {
		b.restore(c)
	}

	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return g, nil
}

func (b *blockParser) parseParameterRevocationsBlock(pos pipeline.SourcePos) ([]pipeline.ParameterRevocation, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var revs []pipeline.ParameterRevocation
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		r, err := b.parseOneParameterRevocation(b.srcPos())
		if err != nil {
			return nil, err
		}
		revs = append(revs, r)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close REVOKES block")
	}
	b.advance()
	return revs, nil
}

// parseOneParameterRevocation mirrors parseOneParameterGrant — RFC Section
// 11.6's pp-revoke-entry: "(pp-privilege-list / ALL PRIVILEGES) ON PARAMETER
// identifier-list FROM role-list [CASCADE];".
func (b *blockParser) parseOneParameterRevocation(pos pipeline.SourcePos) (pipeline.ParameterRevocation, error) {
	r := pipeline.ParameterRevocation{Pos: pos}
	privs, err := b.parsePPPrivilegeList()
	if err != nil {
		return r, err
	}
	r.Privileges = privs
	if err := b.expect("ON"); err != nil {
		return r, err
	}
	if err := b.expect("PARAMETER"); err != nil {
		return r, err
	}
	for {
		b.skipWS()
		p, err := b.readIdentifier()
		if err != nil {
			return r, err
		}
		r.Parameters = append(r.Parameters, p)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
	}
	if err := b.expect("FROM"); err != nil {
		return r, err
	}
	for {
		b.skipWS()
		role, err := b.readRoleIdentifier()
		if err != nil {
			return r, err
		}
		r.Roles = append(r.Roles, role)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
		b.skipWS()
		if strings.ToUpper(b.peekWord()) == "CASCADE" {
			break
		}
	}

	// Optional GRANTED BY role-spec — see parseOneRevocation's identical
	// clause; grammar order here is GRANTED BY before CASCADE.
	b.skipWS()
	c := b.cur()
	if strings.ToUpper(b.peekWord()) == "GRANTED" {
		b.readWord()
		if err := b.expect("BY"); err != nil {
			return r, err
		}
		spec, err := b.parseRoleSpec()
		if err != nil {
			return r, err
		}
		r.GrantedBy = &spec
	} else {
		b.restore(c)
	}

	b.skipWS()
	c = b.cur()
	if strings.ToUpper(b.peekWord()) == "CASCADE" {
		b.readWord()
		r.Cascade = true
	} else {
		b.restore(c)
	}
	b.skipWS()
	if b.peek() == ';' {
		b.advance()
	}
	return r, nil
}

// ── NAME MAP / NAME MAPS ──────────────────────────────────────────────────────

// parseNameMapSingular parses the tail of a NAME MAP directive:
//
//	TO <value> ;               (implicit "default" tool)
//	<tool> TO <value> ;        (explicit tool name)
func (b *blockParser) parseNameMapSingular(pos pipeline.SourcePos) (pipeline.NameMapEntry, error) {
	b.skipWS()
	c := b.cur()
	next := strings.ToUpper(b.peekWord())
	var tool string
	if next == "TO" {
		b.readWord() // consume TO
		tool = "default"
	} else {
		b.restore(c)
		tool = strings.ToLower(b.readWord())
		if tool == "" {
			return pipeline.NameMapEntry{}, b.errorf("expected tool name or TO after NAME MAP")
		}
		if err := b.expect("TO"); err != nil {
			return pipeline.NameMapEntry{}, err
		}
	}
	return b.parseNameMapValue(pos, tool)
}

// parseNameMapsBlock parses a grouped NAME MAPS { ... } block where each
// entry is: <tool> TO <value> ;
func (b *blockParser) parseNameMapsBlock(pos pipeline.SourcePos) ([]pipeline.NameMapEntry, error) {
	if err := b.consumeBrace(); err != nil {
		return nil, err
	}
	var entries []pipeline.NameMapEntry
	for {
		b.skipWS()
		if b.eof() || b.peek() == '}' {
			break
		}
		entryPos := b.srcPos()
		tool := strings.ToLower(b.readWord())
		if tool == "" {
			return nil, b.errorf("expected tool name in NAME MAPS block")
		}
		if err := b.expect("TO"); err != nil {
			return nil, err
		}
		entry, err := b.parseNameMapValue(entryPos, tool)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	b.skipWS()
	if b.peek() != '}' {
		return nil, b.errorf("expected '}' to close NAME MAPS block")
	}
	b.advance()
	return entries, nil
}

// parseNameMapValue parses the value part of a NAME MAP directive:
//
//	"LiteralName" ;   → IsLiteral=true
//	RULE_KEYWORD ;    → IsLiteral=false, must be in ValidNameMapRules
func (b *blockParser) parseNameMapValue(pos pipeline.SourcePos, tool string) (pipeline.NameMapEntry, error) {
	b.skipWS()
	if b.peek() == '"' {
		name, err := b.readQuotedString()
		if err != nil {
			return pipeline.NameMapEntry{}, err
		}
		if err := b.expectSemi(); err != nil {
			return pipeline.NameMapEntry{}, err
		}
		return pipeline.NameMapEntry{Tool: tool, Value: name, IsLiteral: true, Pos: pos}, nil
	}
	rule := strings.ToUpper(b.readWord())
	if !pipeline.ValidNameMapRules[rule] {
		return pipeline.NameMapEntry{}, fmt.Errorf("%s: DPG-E030: unknown name map rule %q; valid rules: LOWER_SNAKE_CASE, UPPER_SNAKE_CASE, LOWER_CAMEL_CASE, UPPER_CAMEL_CASE, LOWER_KEBAB_CASE, UPPER_KEBAB_CASE, TRAIN_CASE, LOWER_CASE, UPPER_CASE, PASCAL_SNAKE_CASE", pos, rule)
	}
	if err := b.expectSemi(); err != nil {
		return pipeline.NameMapEntry{}, err
	}
	return pipeline.NameMapEntry{Tool: tool, Value: rule, IsLiteral: false, Pos: pos}, nil
}

// dedupeNameMapsLastWins collapses entries to the last occurrence per tool
// key (RFC Appendix C, DPG-E031: "same tool key specified more than once at
// the same block level ... warning only; last entry wins") and returns a
// DPG-E031 LintDiagnostic for each tool that had more than one entry. Order
// of the surviving entries follows their last occurrence's position in the
// input.
func dedupeNameMapsLastWins(entries []pipeline.NameMapEntry) ([]pipeline.NameMapEntry, []pipeline.LintDiagnostic) {
	if len(entries) < 2 {
		return entries, nil
	}
	lastIdx := make(map[string]int, len(entries))
	for i, e := range entries {
		lastIdx[e.Tool] = i
	}
	deduped := make([]pipeline.NameMapEntry, 0, len(lastIdx))
	for i, e := range entries {
		if i != lastIdx[e.Tool] {
			continue // shadowed by a later entry for the same tool
		}
		deduped = append(deduped, e)
	}
	dupCount := make(map[string]int, len(lastIdx))
	var tools []string
	var warnings []pipeline.LintDiagnostic
	for _, e := range entries {
		if dupCount[e.Tool] == 0 {
			tools = append(tools, e.Tool)
		}
		dupCount[e.Tool]++
	}
	for _, tool := range tools {
		if dupCount[tool] > 1 {
			warnings = append(warnings, pipeline.LintDiagnostic{
				Pos:     entries[lastIdx[tool]].Pos,
				Rule:    "duplicate-namemap-tool",
				Message: fmt.Sprintf("DPG-E031: tool key %q specified more than once at this block level; last entry wins", tool),
			})
		}
	}
	return deduped, warnings
}

// ── TEXT SEARCH MAPPING ───────────────────────────────────────────────────────

// MAPPING FOR token_type [, ...] WITH dictionary;
func (b *blockParser) parseTSMapping(pos pipeline.SourcePos) (pipeline.TSMappingDef, error) {
	if err := b.expect("FOR"); err != nil {
		return pipeline.TSMappingDef{}, err
	}
	m := pipeline.TSMappingDef{Pos: pos}
	for {
		b.skipWS()
		tt := b.readWord()
		if tt == "" {
			break
		}
		m.TokenTypes = append(m.TokenTypes, tt)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
	}
	if err := b.expect("WITH"); err != nil {
		return m, err
	}
	for {
		b.skipWS()
		dict, err := b.readIdentifier()
		if err != nil {
			return m, err
		}
		m.Dictionaries = append(m.Dictionaries, dict)
		b.skipWS()
		if b.peek() != ',' {
			break
		}
		b.advance()
	}
	if err := b.expectSemi(); err != nil {
		return m, err
	}
	return m, nil
}

// ── OPERATOR FAMILY loose members ────────────────────────────────────────────

// isOperatorChar reports whether ch is one of PostgreSQL's own operator
// characters (real PG grammar, not a DPG invention).
func isOperatorChar(ch byte) bool {
	return strings.IndexByte("+-*/<>=~!@#%^&|`?", ch) >= 0
}

// readOperatorSymbol reads a (possibly schema-qualified) operator name —
// real PG's "any_operator" production (ColId '.' any_operator | all_Op),
// e.g. "<", "@>", "pg_catalog.=". Word characters and operator characters
// are disjoint, so a leading schema and the operator symbol never collide.
func (b *blockParser) readOperatorSymbol() (pipeline.Identifier, error) {
	b.skipWS()
	schema := ""
	if isWordStart(b.peek()) {
		c := b.cur()
		w := b.readWord()
		b.skipWS()
		if b.peek() == '.' {
			b.advance()
			schema = w
		} else {
			b.restore(c)
		}
	}
	b.skipWS()
	var buf []byte
	for !b.eof() && isOperatorChar(b.peek()) {
		buf = append(buf, b.advance())
	}
	if len(buf) == 0 {
		return pipeline.Identifier{}, b.errorf("expected an operator symbol, got %q", b.peek())
	}
	return pipeline.Identifier{Schema: schema, Name: string(buf)}, nil
}

// readTypeListParen reads a parenthesized, comma-separated list of raw type
// text, e.g. "(int4, int8)" or "(numeric(10,2))" or "()" (empty). The
// opening '(' must not have been consumed yet. readRawUntil is paren/brace/
// quote-depth aware, so type modifiers like numeric(10,2) survive intact.
func (b *blockParser) readTypeListParen() ([]string, error) {
	b.skipWS()
	if b.peek() != '(' {
		return nil, b.errorf("expected '(', got %q", b.peek())
	}
	b.advance()
	b.skipWS()
	var types []string
	if b.peek() == ')' {
		b.advance()
		return types, nil
	}
	for {
		raw, err := b.readRawUntil(",)")
		if err != nil {
			return nil, err
		}
		types = append(types, strings.TrimSpace(raw))
		b.skipWS()
		if b.peek() == ',' {
			b.advance()
			continue
		}
		break
	}
	if b.peek() != ')' {
		return nil, b.errorf("expected ')', got %q", b.peek())
	}
	b.advance()
	return types, nil
}

// parseOpFamilyMember parses one OPERATOR/FUNCTION item inside an
// OPERATOR FAMILY { } block (RFC Section 14.4) — real PG's own
// "ALTER OPERATOR FAMILY ... ADD" list-item grammar, just without the
// ALTER/family header (already stated by the family's own declaration). The
// leading OPERATOR/FUNCTION keyword has already been consumed by the
// caller's directive dispatch. Terminates on ',' (more members follow),
// ';' (directive ends), or EOF (last member in the block) — all three are
// accepted so both the approved comma-separated list style and DPG's usual
// one-directive-per-';' style parse identically.
func (b *blockParser) parseOpFamilyMember(pos pipeline.SourcePos, isFunction bool) (pipeline.OpFamilyMember, error) {
	kind := "OPERATOR"
	if isFunction {
		kind = "FUNCTION"
	}
	m := pipeline.OpFamilyMember{IsFunction: isFunction, Pos: pos}

	b.skipWS()
	var numBuf []byte
	for !b.eof() && isDigit(b.peek()) {
		numBuf = append(numBuf, b.advance())
	}
	if len(numBuf) == 0 {
		return m, b.errorf("expected a strategy/support number after %s", kind)
	}
	n, err := strconv.Atoi(string(numBuf))
	if err != nil {
		return m, b.errorf("invalid %s number: %v", kind, err)
	}
	m.Number = n

	if isFunction {
		// Optional "(op_type [, op_type])" — defaults are resolved later by
		// the IR builder (ir.normalizeOpFamilyMembers), not here; the parser
		// only records what was actually written.
		b.skipWS()
		if b.peek() == '(' {
			types, err := b.readTypeListParen()
			if err != nil {
				return m, err
			}
			switch len(types) {
			case 1:
				m.LeftType, m.RightType = types[0], types[0]
			case 2:
				m.LeftType, m.RightType = types[0], types[1]
			default:
				return m, b.errorf("FUNCTION member's optional (op_type[, op_type]) must have 1 or 2 entries, got %d", len(types))
			}
		}
		name, err := b.readIdentifier()
		if err != nil {
			return m, err
		}
		m.Name = name
		args, err := b.readTypeListParen()
		if err != nil {
			return m, err
		}
		m.FuncArgs = args
	} else {
		sym, err := b.readOperatorSymbol()
		if err != nil {
			return m, err
		}
		m.Name = sym
		types, err := b.readTypeListParen()
		if err != nil {
			return m, err
		}
		if len(types) != 2 {
			return m, b.errorf("OPERATOR member requires exactly 2 op_types (left, right), got %d", len(types))
		}
		m.LeftType, m.RightType = types[0], types[1]

		b.skipWS()
		if strings.EqualFold(b.peekWord(), "FOR") {
			b.readWord()
			b.skipWS()
			w2 := strings.ToUpper(b.readWord())
			switch w2 {
			case "SEARCH":
				// Default; nothing to set.
			case "ORDER":
				if err := b.expect("BY"); err != nil {
					return m, err
				}
				fam, err := b.readIdentifier()
				if err != nil {
					return m, err
				}
				m.OrderBy = true
				m.SortFamily = fam
			default:
				return m, b.errorf("expected SEARCH or ORDER BY after FOR, got %q", w2)
			}
		}
	}

	b.skipWS()
	switch b.peek() {
	case ',', ';':
		b.advance()
	case 0:
		// EOF: last member in the block, no trailing punctuation required.
	default:
		return m, b.errorf("expected ',' or ';' after operator family member, got %q", b.peek())
	}
	return m, nil
}
