// Package srcindex locates the source text of every declared object and of
// its parts: block directives, columns and constraints, option clauses. Its
// keys follow the property names drift detection reports (internal/drift),
// so a drift item can be traced back to the text that declares it.
package srcindex

import (
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// Declaration is one declaration of an object. An object can be declared in
// several places (block merging, RFC Section 4.9).
type Declaration struct {
	Key  string // the object's snapshot key
	Kind string // snapshot kind
	// Decl covers the whole declaration, Part1 its Part 1 text, Block the
	// text inside its braces (invalid when it has none).
	Decl, Part1, Block pipeline.Span
	// Items maps a property key to the spans declaring it, in source order:
	// "comment", "column:email", "index:orders_status_idx",
	// "column:email/comment", "connection_limit", "grants" (one span per
	// entry), ... A key with no entry here is declared nowhere in this
	// declaration (or only as part of "part1").
	Items map[string][]pipeline.Span
}

// Index holds the declarations of a compile unit.
type Index struct {
	Decls map[string][]Declaration // by object key
}

// Build compiles files and indexes every declaration.
func Build(files []string, dbDir string, reg *pipeline.Registry, opts compiler.Options) (*Index, error) {
	idx := &Index{Decls: map[string][]Declaration{}}
	opts.Declared = func(raw pipeline.RawObject, obj pipeline.IRObject) {
		kind, _, _, ok := snapshot.IdentOf(obj)
		if !ok {
			kind = strings.ToLower(raw.Kind.String())
		}
		d := Declaration{
			Key:   obj.QualifiedName(),
			Kind:  kind,
			Decl:  raw.DeclSpan,
			Part1: raw.Part1Span,
			Block: raw.Part2Span,
			Items: map[string][]pipeline.Span{},
		}
		if raw.Text != nil {
			for _, a := range raw.SchemaAttrs {
				at := a.At
				indexBlock(d.Items, "", a.Text, func(x, y int) pipeline.Span { return raw.Text.Span(at+x, at+y) })
			}
			indexPart1(&d, raw)
			if raw.HasPart2 {
				indexBlock(d.Items, "", raw.Part2, func(a, b int) pipeline.Span {
					return raw.Text.Span(raw.Part2At+a, raw.Part2At+b)
				})
			}
		}
		idx.Decls[d.Key] = append(idx.Decls[d.Key], d)
	}
	if _, _, err := compiler.CompileWith(files, dbDir, reg, opts); err != nil {
		return nil, err
	}
	for k := range idx.Decls {
		ds := idx.Decls[k]
		sort.SliceStable(ds, func(i, j int) bool {
			if ds[i].Decl.File != ds[j].Decl.File {
				return ds[i].Decl.File < ds[j].Decl.File
			}
			return ds[i].Decl.Start < ds[j].Decl.Start
		})
	}
	return idx, nil
}

// Spans returns every span declaring property of object key, across its
// declarations.
func (x *Index) Spans(key, property string) []pipeline.Span {
	var out []pipeline.Span
	for _, d := range x.Decls[key] {
		out = append(out, d.Items[property]...)
	}
	return out
}

// tok is a lexer token with its byte range in the lexed text.
type tok struct {
	format.Token
	start, end int
}

func lex(text string) []tok {
	var out []tok
	off := 0
	for _, t := range format.Lex("", []byte(text)) {
		if t.Type == format.TokEOF {
			break
		}
		out = append(out, tok{t, off, off + len(t.Text)})
		off += len(t.Text)
	}
	return out
}

func isTrivia(t tok) bool {
	switch t.Type {
	case format.TokWhitespace, format.TokNewline, format.TokLineComment, format.TokBlockComment:
		return true
	}
	return false
}

// code returns the tokens that aren't whitespace or comments.
func code(ts []tok) []tok {
	var out []tok
	for _, t := range ts {
		if !isTrivia(t) {
			out = append(out, t)
		}
	}
	return out
}

func word(t tok) string { return strings.ToUpper(t.Text) }

// identText returns an identifier's name: unquoted names fold to lower case
// as PostgreSQL folds them; quoted names are unquoted.
func identText(t tok) string {
	if t.Type == format.TokQuotedIdent && len(t.Text) >= 2 {
		return strings.ReplaceAll(t.Text[1:len(t.Text)-1], `""`, `"`)
	}
	return strings.ToLower(t.Text)
}

// escape matches the escaping of names in drift property keys.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == ':' || r == '%' || r <= ' ':
			b.WriteString("%")
			b.WriteString(strings.ToUpper(hex2(byte(r))))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&15]})
}

// statement is one block statement: a directive ending in ';', or a group
// "WORDS { ... }" with its own statements.
type statement struct {
	start, end int   // the statement's range, terminator included
	toks       []tok // code tokens before the group's '{', or of the whole statement
	body       []statement
	bodyAt     int // offset of the group's body
	group      bool
}

// splitStatements splits block text into statements at depth 0.
func splitStatements(ts []tok, base int) []statement {
	var out []statement
	i := 0
	for i < len(ts) {
		for i < len(ts) && isTrivia(ts[i]) {
			i++
		}
		if i >= len(ts) {
			break
		}
		st := statement{start: ts[i].start}
		depth := 0
		for i < len(ts) {
			t := ts[i]
			switch {
			case t.Type == format.TokLParen:
				depth++
			case t.Type == format.TokRParen:
				depth--
			case t.Type == format.TokSemicolon && depth == 0:
				st.end = t.end
				i++
				goto done
			case t.Type == format.TokLBrace && depth == 0:
				// A group: its body runs to the matching '}'.
				open := i
				braces := 1
				j := i + 1
				for j < len(ts) && braces > 0 {
					switch ts[j].Type {
					case format.TokLBrace:
						braces++
					case format.TokRBrace:
						braces--
					}
					j++
				}
				st.group = true
				st.toks = code(ts[startIndex(ts, st.start):open])
				st.bodyAt = t.end
				st.body = splitStatements(ts[open+1:j-1], base)
				st.end = ts[j-1].end
				i = j
				// An optional ';' after the group belongs to it.
				k := i
				for k < len(ts) && isTrivia(ts[k]) {
					k++
				}
				if k < len(ts) && ts[k].Type == format.TokSemicolon {
					st.end = ts[k].end
					i = k + 1
				}
				goto done
			}
			if !isTrivia(t) {
				st.toks = append(st.toks, t)
			}
			i++
		}
		st.end = ts[len(ts)-1].end
	done:
		out = append(out, st)
	}
	return out
}

func startIndex(ts []tok, start int) int {
	for i, t := range ts {
		if t.start == start {
			return i
		}
	}
	return 0
}

// groupKinds maps a plural group keyword to the element kind of its entries
// and whether entries are named (their first identifier).
var groupKinds = map[string]struct {
	elem  string
	named bool
}{
	"INDEXES": {"index", true}, "POLICIES": {"policy", true}, "TRIGGERS": {"trigger", true},
	"COLUMNS": {"column", true}, "ATTRIBUTES": {"attribute", true}, "CONSTRAINTS": {"constraint", true},
	"PARTITIONS": {"partition", true}, "GRANTS": {"grants", false}, "REVOKES": {"revocations", false},
}

// singleKinds maps a singular directive keyword to its element kind; the
// entry's name is the identifier after the keyword.
var singleKinds = map[string]string{
	"INDEX": "index", "POLICY": "policy", "TRIGGER": "trigger", "COLUMN": "column",
	"ATTRIBUTE": "attribute", "CONSTRAINT": "constraint", "PARTITION": "partition",
}

// directiveKeys maps a directive's leading keyword(s) to the property key.
var directiveKeys = map[string]string{
	"COMMENT": "comment", "OWNER": "owner", "RENAMED": "renamed_from", "DEPRECATED": "deprecated",
	"PROTECTED": "protected", "DROP": "drop_cascade", "SECURITY": "security_labels",
	"ENABLE": "rls_enabled", "FORCE": "rls_forced", "REPLICA": "replica_identity_mode",
	"CLUSTER": "cluster_on", "SET": "configs", "RESET": "configs", "IN": "memberships",
	"ROLE": "memberships", "ADMIN": "memberships", "GRANT": "grants", "REVOKE": "revocations",
	"STATISTICS": "statistics", "NOT": "domain_not_null", "DEFAULT": "domain_default",
	"MIGRATE": "migrate_remove", "DEPENDS": "depends_on_extensions", "NO": "depends_on_extensions",
	"VALUE": "enum_value_renames", "REFRESH": "refresh_version", "DETACHED": "detached_from",
	"MAPPING": "mappings", "OPERATOR": "members", "FUNCTION": "members", "NAME": "name_maps",
	"PREFERRED": "preferred_json_format", "DISABLED": "enable_state", "DISABLE": "enable_state",
}

// indexBlock records the block statements of text under prefix ("" for the
// object, "column:c/" inside a column block).
func indexBlock(items map[string][]pipeline.Span, prefix, text string, span func(a, b int) pipeline.Span) {
	ts := lex(text)
	for _, st := range splitStatements(ts, 0) {
		if len(st.toks) == 0 && !st.group {
			continue
		}
		first := ""
		if len(st.toks) > 0 {
			first = word(st.toks[0])
		}
		sp := span(st.start, st.end)
		if gk, ok := groupKinds[first]; ok && st.group {
			for _, e := range st.body {
				esp := span(e.start, e.end)
				if !gk.named || len(e.toks) == 0 {
					items[prefix+gk.elem] = append(items[prefix+gk.elem], esp)
					continue
				}
				key := prefix + gk.elem + ":" + escape(identText(e.toks[0]))
				items[key] = append(items[key], esp)
				if e.group {
					indexBlock(items, key+"/", sliceText(text, e.bodyAt, lastBrace(text, e)), offsetSpan(span, e.bodyAt))
				}
			}
			continue
		}
		if elem, ok := singleKinds[first]; ok && len(st.toks) > 1 {
			name := st.toks[1]
			if elem == "index" && strings.EqualFold(name.Text, "UNIQUE") && len(st.toks) > 2 {
				name = st.toks[2]
			}
			key := prefix + elem + ":" + escape(identText(name))
			items[key] = append(items[key], sp)
			if st.group {
				indexBlock(items, key+"/", sliceText(text, st.bodyAt, lastBrace(text, st)), offsetSpan(span, st.bodyAt))
			}
			continue
		}
		if first == "UNIQUE" && len(st.toks) > 1 { // UNIQUE name (cols) in an INDEXES-less form
			key := prefix + "index:" + escape(identText(st.toks[1]))
			items[key] = append(items[key], sp)
			continue
		}
		key, ok := directiveKeys[first]
		if !ok {
			key = strings.ToLower(first)
		}
		items[prefix+key] = append(items[prefix+key], sp)
	}
}

// lastBrace returns the offset of a group statement's closing '}'.
func lastBrace(text string, st statement) int {
	end := strings.LastIndexByte(text[:st.end], '}')
	if end < st.bodyAt {
		return st.bodyAt
	}
	return end
}

func sliceText(text string, a, b int) string {
	if a < 0 || b > len(text) || a > b {
		return ""
	}
	return text[a:b]
}

func offsetSpan(span func(a, b int) pipeline.Span, off int) func(a, b int) pipeline.Span {
	return func(a, b int) pipeline.Span { return span(off+a, off+b) }
}

// PropertyKey returns the index key declaring a drift item's property:
// the property itself, except that a grant is declared by a "grants" entry
// (on the object or its column).
func PropertyKey(property string) string {
	if i := strings.Index(property, "grant:"); i >= 0 {
		return property[:i] + "grants"
	}
	return property
}
