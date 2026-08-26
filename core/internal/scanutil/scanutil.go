// Package scanutil provides shared byte-level scanning primitives used by both
// the scanner (tokenizer) and the format lexer. All functions operate on a
// []byte source and an integer byte position, returning the updated position.
// They do not track line/column numbers; callers handle that themselves.
package scanutil

import (
	"bytes"
	"fmt"
)

// IsWordStart reports whether b can begin an SQL identifier or keyword.
func IsWordStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

// IsWordChar reports whether b can continue an SQL identifier or keyword.
func IsWordChar(b byte) bool {
	return IsWordStart(b) || (b >= '0' && b <= '9')
}

// SkipSingleQuoted advances past a single-quoted SQL string literal, handling
// ” escape sequences. pos must point at the opening '. Returns the position
// immediately after the closing ', or an error if src ends first.
func SkipSingleQuoted(src []byte, pos int) (int, error) {
	pos++ // consume opening '
	for pos < len(src) {
		if src[pos] == '\'' {
			pos++
			if pos < len(src) && src[pos] == '\'' {
				pos++ // '' escape: skip second quote and continue
				continue
			}
			return pos, nil
		}
		pos++
	}
	return pos, fmt.Errorf("unterminated string literal")
}

// PeekDollarTag checks whether src[pos] is the start of a dollar-quoted string
// (either $$ or $tag$) and returns the opening tag text. ok is false if pos
// does not point at a valid dollar-quote opener.
func PeekDollarTag(src []byte, pos int) (tag string, ok bool) {
	if pos >= len(src) || src[pos] != '$' {
		return "", false
	}
	if pos+1 < len(src) && src[pos+1] == '$' {
		return "$$", true
	}
	if pos+1 >= len(src) || !IsWordStart(src[pos+1]) {
		return "", false
	}
	for i := pos + 1; i < len(src); i++ {
		if src[i] == '$' {
			return string(src[pos : i+1]), true
		}
		if !IsWordChar(src[i]) {
			return "", false
		}
	}
	return "", false
}

// SkipDollarQuoted advances past a dollar-quoted region (opening tag through
// closing tag). pos must point at the first '$' of the opening tag. tag is the
// delimiter as returned by PeekDollarTag. Returns the position immediately
// after the closing tag, or an error if src ends first.
func SkipDollarQuoted(src []byte, pos int, tag string) (int, error) {
	pos += len(tag) // skip opening tag
	tagBytes := []byte(tag)
	for pos < len(src) {
		if pos+len(tagBytes) <= len(src) && string(src[pos:pos+len(tagBytes)]) == tag {
			return pos + len(tagBytes), nil
		}
		pos++
	}
	return pos, fmt.Errorf("unterminated dollar-quoted string %s", tag)
}

// SkipLineComment advances to the end of a -- line comment, stopping before
// the '\n' (or at EOF). pos must point at the first '-'. Returns the new
// position (pointing at '\n' or past the end of src).
func SkipLineComment(src []byte, pos int) int {
	for pos < len(src) && src[pos] != '\n' {
		pos++
	}
	return pos
}

// SkipBlockComment advances past a /* ... */ block comment, supporting
// PostgreSQL nested block comments (/* /* */ */). pos must point at '/'.
// Returns the position immediately after the closing '*/', or the end of src
// if the comment is unterminated.
func SkipBlockComment(src []byte, pos int) int {
	pos += 2 // skip /*
	depth := 1
	for pos < len(src) {
		if src[pos] == '/' && pos+1 < len(src) && src[pos+1] == '*' {
			depth++
			pos += 2
			continue
		}
		if src[pos] == '*' && pos+1 < len(src) && src[pos+1] == '/' {
			depth--
			pos += 2
			if depth == 0 {
				return pos
			}
			continue
		}
		pos++
	}
	return pos
}

// utf8BOM is the byte sequence a UTF-8 byte-order mark (U+FEFF) encodes to.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// NormalizeSource strips a leading UTF-8 byte-order mark, if present, and
// normalises all line endings (CRLF and bare CR) to LF. Every entry point
// that reads a .dpg source file (RFC Section 4.1) MUST call this on the raw
// bytes before tokenizing or parsing them, so that a file's line-ending
// style and the presence or absence of a BOM never affect scanning, dollar-
// quoted body content, or emitted SQL.
func NormalizeSource(src []byte) []byte {
	src = bytes.TrimPrefix(src, utf8BOM)
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	src = bytes.ReplaceAll(src, []byte("\r"), []byte("\n"))
	return src
}
