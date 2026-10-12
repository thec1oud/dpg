package scanner

import (
	"fmt"
	"strings"
)

// MacroDef is a named reusable body of text.
// ParenStyle is true for MACRO name (...) definitions; false for MACRO name { ... }.
type MacroDef struct {
	Body       string
	ParenStyle bool // true = paren-body (column list), false = brace-body (block items)
}

// macroStore maps macro names to their definitions.
type macroStore map[string]MacroDef

// preprocessMacrosWithGlobal performs two passes over src:
//  1. Collect all MACRO name (...) and MACRO name {...} definitions from src.
//  2. Merge with global (file-local definitions take precedence).
//  3. Expand ...name spreads in src, erroring on undefined ones.
//
// MACRO declarations are removed from the output. The result is valid DPG
// source with all spreads inlined.
func preprocessMacrosWithGlobal(src []byte, global macroStore, keepSpreads bool) ([]byte, error) {
	if keepSpreads {
		return expandMacros(src, nil, true)
	}
	local, err := collectMacros(src)
	if err != nil {
		return nil, err
	}
	var store macroStore
	if len(global) == 0 {
		store = local
	} else {
		store = make(macroStore, len(global)+len(local))
		for k, v := range global {
			store[k] = v
		}
		// File-local definitions override globals.
		for k, v := range local {
			store[k] = v
		}
	}
	if err := resolveStoreBodies(store); err != nil {
		return nil, err
	}
	return expandMacros(src, store, false)
}

// collectMacros scans src for MACRO declarations and returns the store.
// It is a read-only pass that does not modify src.
func collectMacros(src []byte) (macroStore, error) {
	store := macroStore{}
	p := &macroParser{src: src}
	for {
		p.skipWS()
		if p.eof() {
			break
		}
		pos := p.pos

		// Skip string literals and dollar-quoted strings at the top level.
		if p.peek() == '\'' {
			if err := p.skipSingleQuoted(); err != nil {
				return nil, err
			}
			continue
		}
		if tag, ok := p.peekDollarTag(); ok {
			if err := p.skipDollarQuoted(tag); err != nil {
				return nil, err
			}
			continue
		}
		// Skip line comments.
		if p.peek() == '-' && p.peekAt(1) == '-' {
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
			continue
		}
		// Skip block comments.
		if p.peek() == '/' && p.peekAt(1) == '*' {
			p.advance()
			p.advance()
			for !p.eof() {
				if p.peek() == '*' && p.peekAt(1) == '/' {
					p.advance()
					p.advance()
					break
				}
				p.advance()
			}
			continue
		}

		// Detect MACRO keyword.
		if !isWordStart(p.peek()) {
			p.advance()
			continue
		}
		wordStart := p.pos
		word := p.readWord()
		if strings.ToUpper(word) != "MACRO" {
			// Skip to the next declaration-level boundary.
			// We need to skip over the entire declaration so we don't
			// accidentally pick up a MACRO keyword inside a function body.
			if err := p.skipDeclaration(pos, wordStart); err != nil {
				return nil, err
			}
			continue
		}

		// Read macro name.
		p.skipWS()
		if p.eof() {
			return nil, fmt.Errorf("MACRO: expected name after MACRO keyword")
		}
		name := p.readWord()
		if name == "" {
			return nil, fmt.Errorf("MACRO: expected identifier after MACRO, got %q", p.peek())
		}
		if _, dup := store[name]; dup {
			return nil, fmt.Errorf("MACRO %s: macro name redeclared in the same file (DPG-E011)", name)
		}

		// Read body: either (...) or {...}.
		p.skipWS()
		switch p.peek() {
		case '(':
			p.advance()
			body, err := p.readBalanced('(', ')')
			if err != nil {
				return nil, fmt.Errorf("MACRO %s: %v", name, err)
			}
			store[name] = MacroDef{Body: body, ParenStyle: true}
		case '{':
			p.advance()
			body, err := p.readBalanced('{', '}')
			if err != nil {
				return nil, fmt.Errorf("MACRO %s: %v", name, err)
			}
			store[name] = MacroDef{Body: body, ParenStyle: false}
		default:
			return nil, fmt.Errorf("MACRO %s: expected '(' or '{', got %q", name, p.peek())
		}
	}
	return store, nil
}

// expandMacros replaces ...name occurrences in src with the corresponding macro body.
// MACRO declarations are removed from the output. With keepSpreads, spreads
// are written as they are instead.
func expandMacros(src []byte, store macroStore, keepSpreads bool) ([]byte, error) {
	var out strings.Builder
	p := &macroParser{src: src}
	// brackets tracks the stack of currently-open '(' / '{' delimiters in
	// the real source text, so a spread site can be checked against its
	// innermost enclosing bracket type (DPG-E008/E009, Section 4.7: "A
	// paren-body macro MUST only be spread inside a ( ) list" / "A
	// brace-body macro MUST only be spread inside a { } block"). A
	// substituted macro body's own internal brackets are written as a bulk
	// string, bypassing this loop entirely, so they never affect the stack.
	var brackets []byte

	for !p.eof() {
		// Preserve string literals verbatim.
		if p.peek() == '\'' {
			start := p.pos
			if err := p.skipSingleQuoted(); err != nil {
				return nil, err
			}
			out.Write(src[start:p.pos])
			continue
		}
		// Preserve dollar-quoted strings verbatim.
		if tag, ok := p.peekDollarTag(); ok {
			start := p.pos
			if err := p.skipDollarQuoted(tag); err != nil {
				return nil, err
			}
			out.Write(src[start:p.pos])
			continue
		}
		// Preserve comments verbatim.
		if p.peek() == '-' && p.peekAt(1) == '-' {
			start := p.pos
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
			out.Write(src[start:p.pos])
			continue
		}
		if p.peek() == '/' && p.peekAt(1) == '*' {
			start := p.pos
			p.advance()
			p.advance()
			for !p.eof() {
				if p.peek() == '*' && p.peekAt(1) == '/' {
					p.advance()
					p.advance()
					break
				}
				p.advance()
			}
			out.Write(src[start:p.pos])
			continue
		}

		// Detect MACRO declarations — remove them from the output.
		if isWordStart(p.peek()) {
			c := p.pos
			word := p.readWord()
			if strings.ToUpper(word) == "MACRO" {
				// Consume the macro definition entirely (name + body).
				p.skipWS()
				p.readWord() // name
				p.skipWS()
				if !p.eof() {
					switch p.peek() {
					case '(':
						p.advance()
						if _, err := p.readBalanced('(', ')'); err != nil {
							return nil, err
						}
					case '{':
						p.advance()
						if _, err := p.readBalanced('{', '}'); err != nil {
							return nil, err
						}
					}
				}
				// Consume optional trailing semicolon.
				p.skipWS()
				if !p.eof() && p.peek() == ';' {
					p.advance()
				}
				// Keep the declaration's line breaks so every later source
				// position keeps its original line number.
				out.WriteString(strings.Repeat("\n", strings.Count(string(src[c:p.pos]), "\n")))
				continue
			}
			// Not MACRO — write the word verbatim.
			out.Write(src[c:p.pos])
			continue
		}

		// Detect spread: ...name
		if p.peek() == '.' && p.peekAt(1) == '.' && p.peekAt(2) == '.' {
			spreadStart := p.pos
			p.advance()
			p.advance()
			p.advance()
			afterDots := p.pos
			p.skipWS()
			name := p.readWord()
			if name == "" {
				// Not a spread: keep the text skipped above, so line numbers
				// after it don't shift.
				out.WriteString("...")
				out.Write(src[afterDots:p.pos])
				continue
			}
			if keepSpreads {
				out.Write(src[spreadStart:p.pos])
				continue
			}
			def, ok := store[name]
			if !ok {
				return nil, fmt.Errorf("spread ...%s: macro %q is not defined (DPG-E010)", name, name)
			}
			if len(brackets) > 0 {
				switch innermost := brackets[len(brackets)-1]; {
				case def.ParenStyle && innermost == '{':
					return nil, fmt.Errorf("spread ...%s: paren-body macro %q spread inside a { } block, must be spread inside a ( ) list (DPG-E008)", name, name)
				case !def.ParenStyle && innermost == '(':
					return nil, fmt.Errorf("spread ...%s: brace-body macro %q spread inside a ( ) list, must be spread inside a { } block (DPG-E009)", name, name)
				}
			}
			// The expansion stays on the spread's own line, so later source
			// positions keep their original line numbers.
			out.WriteString(oneLine(def.Body))
			// Consume optional trailing comma/semicolon that was the spread's separator.
			// We leave it to the caller to provide correct separators around the spread.
			continue
		}

		switch p.peek() {
		case '(', '{':
			brackets = append(brackets, p.peek())
		case ')', '}':
			if len(brackets) > 0 {
				brackets = brackets[:len(brackets)-1]
			}
		}
		out.WriteByte(src[p.pos])
		p.advance()
	}
	return []byte(out.String()), nil
}

// oneLine rewrites an expanded macro body onto a single line: line comments
// are dropped and line breaks outside quoted text become spaces. Line breaks
// inside string literals or dollar-quoted text are kept unchanged.
func oneLine(body string) string {
	p := &macroParser{src: []byte(body)}
	var out strings.Builder
	for !p.eof() {
		switch {
		case p.peek() == '\'':
			start := p.pos
			if err := p.skipSingleQuoted(); err != nil {
				out.WriteString(body[start:])
				return out.String()
			}
			out.Write(p.src[start:p.pos])
		case p.peek() == '-' && p.peekAt(1) == '-':
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
		case p.peek() == '\n' || p.peek() == '\r':
			out.WriteByte(' ')
			p.advance()
		default:
			if tag, ok := p.peekDollarTag(); ok {
				start := p.pos
				if err := p.skipDollarQuoted(tag); err != nil {
					out.WriteString(body[start:])
					return out.String()
				}
				out.Write(p.src[start:p.pos])
				continue
			}
			out.WriteByte(p.src[p.pos])
			p.advance()
		}
	}
	return out.String()
}

// resolveStoreBodies fully expands all macro bodies in-place so that nested
// ...name spreads inside a body work correctly. It uses DFS over the store;
// if a cycle is detected it returns a DPG-E012 error.
func resolveStoreBodies(store macroStore) error {
	resolved := make(map[string]bool, len(store))
	visiting := make(map[string]bool)

	var resolve func(name string) error
	resolve = func(name string) error {
		if resolved[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("spread ...%s: circular macro reference (DPG-E012)", name)
		}
		visiting[name] = true
		def := store[name]
		body, err := expandBodyText(def.Body, store, resolve)
		if err != nil {
			return fmt.Errorf("macro %q: %w", name, err)
		}
		def.Body = body
		store[name] = def
		visiting[name] = false
		resolved[name] = true
		return nil
	}

	for name := range store {
		if err := resolve(name); err != nil {
			return err
		}
	}
	return nil
}

// expandBodyText expands ...name spreads within a macro body string, using
// resolve to ensure each referenced macro is itself fully resolved first.
// String literals, dollar-quoted strings, and comments are preserved verbatim.
func expandBodyText(body string, store macroStore, resolve func(string) error) (string, error) {
	p := &macroParser{src: []byte(body)}
	var out strings.Builder
	for !p.eof() {
		if p.peek() == '\'' {
			start := p.pos
			if err := p.skipSingleQuoted(); err != nil {
				return "", err
			}
			out.Write(p.src[start:p.pos])
			continue
		}
		if tag, ok := p.peekDollarTag(); ok {
			start := p.pos
			if err := p.skipDollarQuoted(tag); err != nil {
				return "", err
			}
			out.Write(p.src[start:p.pos])
			continue
		}
		if p.peek() == '-' && p.peekAt(1) == '-' {
			start := p.pos
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
			out.Write(p.src[start:p.pos])
			continue
		}
		if p.peek() == '/' && p.peekAt(1) == '*' {
			start := p.pos
			p.advance()
			p.advance()
			for !p.eof() {
				if p.peek() == '*' && p.peekAt(1) == '/' {
					p.advance()
					p.advance()
					break
				}
				p.advance()
			}
			out.Write(p.src[start:p.pos])
			continue
		}
		if p.peek() == '.' && p.peekAt(1) == '.' && p.peekAt(2) == '.' {
			p.advance()
			p.advance()
			p.advance()
			p.skipWS()
			name := p.readWord()
			if name == "" {
				out.WriteString("...")
				continue
			}
			if _, ok := store[name]; !ok {
				return "", fmt.Errorf("spread ...%s: macro %q is not defined (DPG-E010)", name, name)
			}
			if err := resolve(name); err != nil {
				return "", err
			}
			out.WriteString(store[name].Body)
			continue
		}
		out.WriteByte(p.src[p.pos])
		p.advance()
	}
	return out.String(), nil
}

// ── macroParser ───────────────────────────────────────────────────────────────

// macroParser is a minimal cursor used by the macro preprocessor.
type macroParser struct {
	src []byte
	pos int
}

func (p *macroParser) eof() bool { return p.pos >= len(p.src) }
func (p *macroParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}
func (p *macroParser) peekAt(n int) byte {
	if p.pos+n >= len(p.src) {
		return 0
	}
	return p.src[p.pos+n]
}
func (p *macroParser) advance() {
	if !p.eof() {
		p.pos++
	}
}

func (p *macroParser) skipWS() {
	for !p.eof() {
		ch := p.peek()
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			p.advance()
		} else {
			break
		}
	}
}

func (p *macroParser) readWord() string {
	if p.eof() || !isWordStart(p.peek()) {
		return ""
	}
	start := p.pos
	p.advance()
	for !p.eof() && isWordCharM(p.peek()) {
		p.advance()
	}
	return string(p.src[start:p.pos])
}

// isWordCharM checks if a byte can continue an identifier (digits OK after first char).
func isWordCharM(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func (p *macroParser) skipSingleQuoted() error {
	p.advance() // consume '
	for !p.eof() {
		ch := p.src[p.pos]
		p.advance()
		if ch == '\'' {
			if !p.eof() && p.peek() == '\'' {
				p.advance()
			} else {
				return nil
			}
		}
	}
	return fmt.Errorf("unterminated string literal")
}

func (p *macroParser) peekDollarTag() (string, bool) {
	if p.peek() != '$' {
		return "", false
	}
	next := p.peekAt(1)
	if next == '$' {
		return "$$", true
	}
	if !isWordStart(next) {
		return "", false
	}
	for i := p.pos + 1; i < len(p.src); i++ {
		b := p.src[i]
		if b == '$' {
			return string(p.src[p.pos : i+1]), true
		}
		if !isWordCharM(b) {
			return "", false
		}
	}
	return "", false
}

func (p *macroParser) skipDollarQuoted(tag string) error {
	for range tag {
		p.advance()
	}
	tagBytes := []byte(tag)
	for !p.eof() {
		if p.pos+len(tagBytes) <= len(p.src) &&
			string(p.src[p.pos:p.pos+len(tagBytes)]) == tag {
			for range tag {
				p.advance()
			}
			return nil
		}
		p.advance()
	}
	return fmt.Errorf("unterminated dollar-quoted string %s", tag)
}

// readBalanced reads content until the matching close delimiter,
// tracking nested open/close pairs. The opening delimiter must already be consumed.
// Returns the content between the delimiters (not including them).
func (p *macroParser) readBalanced(open, close byte) (string, error) {
	start := p.pos
	depth := 1
	for !p.eof() {
		ch := p.peek()
		if ch == open {
			depth++
			p.advance()
		} else if ch == close {
			depth--
			if depth == 0 {
				body := string(p.src[start:p.pos])
				p.advance() // consume closing delimiter
				return body, nil
			}
			p.advance()
		} else if ch == '\'' {
			if err := p.skipSingleQuoted(); err != nil {
				return "", err
			}
		} else if tag, ok := p.peekDollarTag(); ok {
			if err := p.skipDollarQuoted(tag); err != nil {
				return "", err
			}
		} else if ch == '-' && p.peekAt(1) == '-' {
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
		} else if ch == '/' && p.peekAt(1) == '*' {
			p.advance()
			p.advance()
			for !p.eof() {
				if p.peek() == '*' && p.peekAt(1) == '/' {
					p.advance()
					p.advance()
					break
				}
				p.advance()
			}
		} else {
			p.advance()
		}
	}
	return "", fmt.Errorf("unterminated %c...%c block", open, close)
}

// skipDeclaration advances past a non-MACRO top-level declaration.
// This is used during macro collection to skip over declarations that might
// contain the word "MACRO" in a body (e.g. a function named macro_helper).
// wordStart is the position where the first keyword started. Returns an
// error (DPG-E007) if a genuine MACRO keyword is found nested inside the
// declaration's { } block — MACRO is only valid as a top-level declaration
// (Section 4.7).
func (p *macroParser) skipDeclaration(declStart, _ int) error {
	// Skip to ';' or end of a { } block at depth 0.
	// This is a best-effort skip — we track strings and dollar-quotes.
	for !p.eof() {
		ch := p.peek()
		if ch == ';' {
			p.advance()
			return nil
		}
		if ch == '{' {
			p.advance()
			if err := p.scanBlockRejectingMacro(); err != nil {
				return err
			}
			// After a { } block, optionally a trailing ';'.
			p.skipWS()
			if !p.eof() && p.peek() == ';' {
				p.advance()
			}
			return nil
		}
		if ch == '\'' {
			_ = p.skipSingleQuoted()
			continue
		}
		if tag, ok := p.peekDollarTag(); ok {
			if err := p.skipDollarQuoted(tag); err != nil {
				return nil
			}
			p.skipWS()
			if !p.eof() && p.peek() == ';' {
				p.advance()
			}
			return nil
		}
		_ = declStart
		p.advance()
	}
	return nil
}

// scanBlockRejectingMacro consumes a { } block (the opening '{' must already
// be consumed) exactly like readBalanced('{', '}'), except it additionally
// rejects a bare MACRO keyword found at any nesting depth inside the block —
// MACRO declarations found inside a block (DPG-E007, Section 4.7's "It MUST
// appear [at the top level]") rather than at the top level of a .dpg file.
func (p *macroParser) scanBlockRejectingMacro() error {
	depth := 1
	for !p.eof() {
		ch := p.peek()
		switch {
		case ch == '{':
			depth++
			p.advance()
		case ch == '}':
			depth--
			p.advance()
			if depth == 0 {
				return nil
			}
		case ch == '\'':
			if err := p.skipSingleQuoted(); err != nil {
				return err
			}
		case ch == '-' && p.peekAt(1) == '-':
			for !p.eof() && p.peek() != '\n' {
				p.advance()
			}
		case ch == '/' && p.peekAt(1) == '*':
			p.advance()
			p.advance()
			for !p.eof() {
				if p.peek() == '*' && p.peekAt(1) == '/' {
					p.advance()
					p.advance()
					break
				}
				p.advance()
			}
		default:
			if tag, ok := p.peekDollarTag(); ok {
				if err := p.skipDollarQuoted(tag); err != nil {
					return err
				}
				continue
			}
			if isWordStart(ch) {
				word := p.readWord()
				if strings.ToUpper(word) == "MACRO" {
					return fmt.Errorf("MACRO: declaration found inside a block — MACRO is only valid as a top-level declaration (DPG-E007)")
				}
				continue
			}
			p.advance()
		}
	}
	return fmt.Errorf("unterminated {...} block")
}
