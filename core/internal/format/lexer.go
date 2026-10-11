package format

import (
	"strings"

	"github.com/thec1oud/dpg/internal/scanutil"
)

// TokType identifies the type of a format token.
type TokType int

const (
	TokKeyword      TokType = iota // SQL / DPG keyword
	TokIdent                       // unquoted identifier
	TokQuotedIdent                 // "double-quoted" identifier
	TokStringLit                   // 'single-quoted' literal
	TokDollarQuote                 // $$...$$ or $tag$...$tag$ (entire region)
	TokNumber                      // integer or decimal literal
	TokLineComment                 // -- ... to end of line (content preserved)
	TokBlockComment                // /* ... */ (content preserved)
	TokNewline                     // \n (for blank-line tracking)
	TokWhitespace                  // run of non-newline whitespace
	TokLParen                      // (
	TokRParen                      // )
	TokLBrace                      // {
	TokRBrace                      // }
	TokSemicolon                   // ;
	TokComma                       // ,
	TokDot                         // .
	TokOperator                    // any other punctuation / operator char
	TokEOF
)

// Token is a single lexeme from a .dpg source file.
type Token struct {
	Type TokType
	Text string
	File string
	Line int
	Col  int
}

// dpgKeywords is the set of DPG/SQL keywords (uppercased).
var dpgKeywords = func() map[string]bool {
	words := []string{
		// Object type keywords
		"TABLE", "UNLOGGED", "FOREIGN", "VIEW", "MATERIALIZED", "RECURSIVE",
		"FUNCTION", "PROCEDURE", "AGGREGATE", "ENUM", "TYPE", "DOMAIN", "VIRTUAL",
		"SCHEMA", "EXTENSION", "SEQUENCE", "ROLE", "TABLESPACE",
		"SERVER", "USER", "MAPPING", "PUBLICATION", "SUBSCRIPTION",
		"EVENT", "TRIGGER", "TRIGGERS", "COLLATION", "OPERATOR", "CLASS", "FAMILY",
		"CAST", "STATISTICS", "TEXT", "SEARCH", "CONFIGURATION", "FULLTEXT",
		"DICTIONARY", "PARSER", "TEMPLATE", "DEFAULT", "PRIVILEGES", "DATA", "WRAPPER",
		"MACRO", "DROP", "PREFERRED", "FORMAT", "JSON", "JSONB",
		// Column / constraint keywords
		"NOT", "NULL", "PRIMARY", "KEY", "UNIQUE", "CHECK", "REFERENCES",
		"CONSTRAINT", "CONSTRAINTS", "GENERATED", "ALWAYS", "BY", "IDENTITY", "STORED",
		"AS", "IN", "DEFAULT", "ON", "DELETE", "UPDATE", "CASCADE",
		"RESTRICT", "NO", "ACTION", "SET", "MATCH", "FULL", "PARTIAL",
		"SIMPLE", "DEFERRABLE", "INITIALLY", "DEFERRED", "IMMEDIATE",
		"WITH", "WITHOUT", "OIDS", "INHERITS", "PARTITION", "RANGE", "LIST", "HASH",
		"COLUMN", "STORAGE", "COMPRESSION",
		// Index
		"INCLUDE", "EXCLUDE", "SPATIAL", "WHERE", "WHEN",
		// Sequence
		"CACHE", "CYCLE", "MAXVALUE", "MINVALUE", "INCREMENT", "START", "OWNED",
		// Role attributes
		"SUPERUSER", "NOSUPERUSER", "CREATEDB", "NOCREATEDB",
		"CREATEROLE", "NOCREATEROLE", "INHERIT", "NOINHERIT",
		"LOGIN", "NOLOGIN", "REPLICATION", "NOREPLICATION",
		"BYPASSRLS", "NOBYPASSRLS", "PASSWORD", "VALID", "UNTIL",
		// Function / aggregate params
		"INOUT", "OUT", "VARIADIC", "HANDLER", "VALIDATOR",
		"SFUNC", "STYPE", "INITCOND", "FINALFUNC", "COMBINEFUNC", "SERIALFUNC",
		// DPG block keywords
		"OWNER", "COMMENT", "RENAMED", "FROM", "DEPRECATED", "PROTECTED",
		"GRANTS", "GRANT", "REVOKES", "REVOKE", "INDEXES", "INDEX",
		"POLICIES", "POLICY", "PERMISSIVE", "RESTRICTIVE",
		"USING", "FOR", "ALL", "SELECT", "INSERT", "UPDATE",
		"DELETE", "TRUNCATE", "EXECUTE", "USAGE", "CONNECT", "TEMPORARY",
		"RULE", "REFERENCES", "CREATE", "MAINTAIN",
		"LANGUAGE", "ROWS", "COLUMNS", "SEQUENCES", "FUNCTIONS", "PROCEDURES",
		"TABLES", "ROUTINES", "PARTITIONS", "MIGRATE", "REMOVE",
		// DPG block-level labels
		"RLS", "ENABLE", "FORCE",
		// Function / trigger attrs
		"VOLATILE", "STABLE", "IMMUTABLE", "STRICT", "SECURITY", "DEFINER",
		"INVOKER", "PARALLEL", "SAFE", "UNSAFE", "RESTRICTED", "COST",
		"AFTER", "BEFORE", "EACH", "INSTEAD", "STATEMENT", "ROW",
		// Misc SQL / DPG
		"RETURNS", "BEGIN", "END", "TO",
		"ASC", "DESC", "FIRST", "LAST", "NULLS", "LIMIT", "OFFSET",
		"TRUE", "FALSE", "VALUES", "TIME", "ZONE",
		"ADMIN", "OF", "DATABASE", "LOCATION", "VERSION", "PUBLIC",
		"OPTION", "OPTIONS", "CONNECTION", "IMPLICIT", "ASSIGNMENT", "TAG",
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}()

// Lex tokenises src from the given file path and returns the flat token stream.
// Every byte of src is represented in the output (tokens are contiguous).
func Lex(file string, src []byte) []Token {
	l := &lexer{src: src, file: file, line: 1, col: 1}
	return l.tokenize()
}

type lexer struct {
	src  []byte
	file string
	pos  int
	line int
	col  int
}

func (l *lexer) eof() bool { return l.pos >= len(l.src) }

func (l *lexer) peek() byte {
	if l.eof() {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) peekAt(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func (l *lexer) advance() byte {
	if l.eof() {
		return 0
	}
	b := l.src[l.pos]
	l.pos++
	if b == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return b
}

// advanceTo moves pos to newPos, updating line/col by scanning the skipped bytes.
func (l *lexer) advanceTo(newPos int) {
	for l.pos < newPos {
		l.advance()
	}
}

func (l *lexer) emit(typ TokType, start int, startLine, startCol int) Token {
	return Token{
		Type: typ,
		Text: string(l.src[start:l.pos]),
		File: l.file,
		Line: startLine,
		Col:  startCol,
	}
}

func (l *lexer) tokenize() []Token {
	var toks []Token
	for !l.eof() {
		start := l.pos
		startLine := l.line
		startCol := l.col
		b := l.peek()

		switch {
		case b == '\n':
			l.advance()
			toks = append(toks, Token{TokNewline, "\n", l.file, startLine, startCol})

		case b == ' ' || b == '\t' || b == '\r':
			for !l.eof() && (l.peek() == ' ' || l.peek() == '\t' || l.peek() == '\r') {
				l.advance()
			}
			toks = append(toks, l.emit(TokWhitespace, start, startLine, startCol))

		case b == '-' && l.peekAt(1) == '-':
			newPos := scanutil.SkipLineComment(l.src, l.pos)
			l.advanceTo(newPos)
			toks = append(toks, l.emit(TokLineComment, start, startLine, startCol))

		case b == '/' && l.peekAt(1) == '*':
			newPos := scanutil.SkipBlockComment(l.src, l.pos)
			l.advanceTo(newPos)
			toks = append(toks, l.emit(TokBlockComment, start, startLine, startCol))

		case b == '\'':
			newPos, _ := scanutil.SkipSingleQuoted(l.src, l.pos)
			l.advanceTo(newPos)
			toks = append(toks, l.emit(TokStringLit, start, startLine, startCol))

		case b == '"':
			l.advance() // opening "
			for !l.eof() {
				c := l.advance()
				if c == '"' {
					if l.peek() == '"' {
						l.advance() // "" escape
					} else {
						break
					}
				}
			}
			toks = append(toks, l.emit(TokQuotedIdent, start, startLine, startCol))

		case b == '$':
			if tag, ok := scanutil.PeekDollarTag(l.src, l.pos); ok {
				newPos, _ := scanutil.SkipDollarQuoted(l.src, l.pos, tag)
				l.advanceTo(newPos)
				toks = append(toks, l.emit(TokDollarQuote, start, startLine, startCol))
			} else {
				l.advance()
				toks = append(toks, l.emit(TokOperator, start, startLine, startCol))
			}

		case b >= '0' && b <= '9':
			for !l.eof() && ((l.peek() >= '0' && l.peek() <= '9') || l.peek() == '.') {
				l.advance()
			}
			toks = append(toks, l.emit(TokNumber, start, startLine, startCol))

		case scanutil.IsWordStart(b):
			for !l.eof() && scanutil.IsWordChar(l.peek()) {
				l.advance()
			}
			text := string(l.src[start:l.pos])
			typ := TokIdent
			if dpgKeywords[strings.ToUpper(text)] {
				typ = TokKeyword
			}
			toks = append(toks, Token{typ, text, l.file, startLine, startCol})

		case b == '(':
			l.advance()
			toks = append(toks, l.emit(TokLParen, start, startLine, startCol))
		case b == ')':
			l.advance()
			toks = append(toks, l.emit(TokRParen, start, startLine, startCol))
		case b == '{':
			l.advance()
			toks = append(toks, l.emit(TokLBrace, start, startLine, startCol))
		case b == '}':
			l.advance()
			toks = append(toks, l.emit(TokRBrace, start, startLine, startCol))
		case b == ';':
			l.advance()
			toks = append(toks, l.emit(TokSemicolon, start, startLine, startCol))
		case b == ',':
			l.advance()
			toks = append(toks, l.emit(TokComma, start, startLine, startCol))
		case b == '.':
			l.advance()
			toks = append(toks, l.emit(TokDot, start, startLine, startCol))

		default:
			l.advance()
			toks = append(toks, l.emit(TokOperator, start, startLine, startCol))
		}
	}
	toks = append(toks, Token{Type: TokEOF, File: l.file, Line: l.line, Col: l.col})
	return toks
}
