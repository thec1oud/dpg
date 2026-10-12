package srcindex

import (
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// indexPart1 records the parts of a declaration's Part 1: always "part1"
// (the whole text), and per kind its columns, constraints, option clauses,
// query or body.
func indexPart1(d *Declaration, raw pipeline.RawObject) {
	text := raw.Part1
	if text == "" || raw.Kind == pipeline.KindSchema {
		// PARAMETER PRIVILEGES has no Part 1; a schema's is just its name.
		return
	}
	span := func(a, b int) pipeline.Span { return raw.Text.Span(raw.Part1At+a, raw.Part1At+b) }
	d.Items["part1"] = []pipeline.Span{span(0, len(text))}
	ts := lex(text)
	switch raw.Kind {
	case pipeline.KindTable, pipeline.KindUnloggedTable, pipeline.KindForeignTable:
		indexTable(d.Items, ts, span)
	case pipeline.KindRole:
		indexOptions(d.Items, code(ts)[1:], roleOptions, span)
	case pipeline.KindSequence, pipeline.KindUnloggedSequence:
		indexOptions(d.Items, code(ts)[1:], sequenceOptions, span)
	case pipeline.KindView, pipeline.KindMaterializedView, pipeline.KindRecursiveView:
		if q, ok := afterAS(ts); ok {
			d.Items["query"] = []pipeline.Span{span(q, len(text))}
		}
	case pipeline.KindFunction, pipeline.KindProcedure:
		indexRoutine(d.Items, ts, span)
	case pipeline.KindEnum:
		if open, close, ok := firstParens(ts); ok {
			d.Items["values"] = []pipeline.Span{span(open, close)}
		}
	}
}

// firstParens returns the range of the first parenthesized list, parens
// included.
func firstParens(ts []tok) (int, int, bool) {
	depth, open := 0, -1
	for _, t := range ts {
		switch t.Type {
		case format.TokLParen:
			if depth == 0 {
				open = t.start
			}
			depth++
		case format.TokRParen:
			depth--
			if depth == 0 && open >= 0 {
				return open, t.end, true
			}
		}
	}
	return 0, 0, false
}

// afterAS returns the offset of the first code token after the first AS at
// paren depth 0.
func afterAS(ts []tok) (int, bool) {
	depth := 0
	for i, t := range ts {
		switch t.Type {
		case format.TokLParen:
			depth++
		case format.TokRParen:
			depth--
		}
		if depth == 0 && word(t) == "AS" {
			for _, n := range ts[i+1:] {
				if !isTrivia(n) {
					return n.start, true
				}
			}
		}
	}
	return 0, false
}

// tableConstraintWords start a table-level entry of a column list.
var tableConstraintWords = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "UNIQUE": true, "CHECK": true,
	"FOREIGN": true, "EXCLUDE": true, "NOT": true,
}

// tableClauseKeys maps the keyword starting a clause after the column list
// to its property key.
var tableClauseKeys = map[string]string{
	"WITH": "storage_params", "TABLESPACE": "tablespace", "INHERITS": "inherits",
	"PARTITION": "partition_by", "USING": "access_method", "SERVER": "foreign_server",
	"OPTIONS": "foreign_options",
}

// indexTable records a table's columns ("column:name"), named constraints
// ("constraint:name"), unnamed constraints ("constraint") and the clauses
// after the column list.
func indexTable(items map[string][]pipeline.Span, ts []tok, span func(a, b int) pipeline.Span) {
	open, close, ok := firstParens(ts)
	if !ok {
		return
	}
	// Entries of the column list, split at depth-1 commas.
	var entry []tok
	flush := func() {
		c := code(entry)
		entry = nil
		if len(c) == 0 {
			return
		}
		sp := span(c[0].start, c[len(c)-1].end)
		first := word(c[0])
		switch {
		case first == "LIKE" && c[0].Type == format.TokKeyword:
			items["like"] = append(items["like"], sp)
		case first == "CONSTRAINT" && len(c) > 1:
			key := "constraint:" + escape(identText(c[1]))
			items[key] = append(items[key], sp)
		case tableConstraintWords[first] && c[0].Type == format.TokKeyword:
			items["constraint"] = append(items["constraint"], sp)
		default:
			key := "column:" + escape(identText(c[0]))
			items[key] = append(items[key], sp)
		}
	}
	depth := 0
	for _, t := range ts {
		if t.start < open || t.end > close {
			continue
		}
		switch t.Type {
		case format.TokLParen:
			depth++
			if depth == 1 {
				continue
			}
		case format.TokRParen:
			depth--
			if depth == 0 {
				flush()
				continue
			}
		case format.TokComma:
			if depth == 1 {
				flush()
				continue
			}
		}
		entry = append(entry, t)
	}

	// Clauses after the column list.
	var rest []tok
	for _, t := range code(ts) {
		if t.start >= close {
			rest = append(rest, t)
		}
	}
	indexClauses(items, rest, tableClauseKeys, span)
}

// indexClauses records clauses that start with a keyword in keys, each
// running to the next such keyword at paren depth 0.
func indexClauses(items map[string][]pipeline.Span, ts []tok, keys map[string]string, span func(a, b int) pipeline.Span) {
	depth := 0
	cur, start, last := "", -1, -1
	flush := func() {
		if cur != "" && start >= 0 {
			items[cur] = append(items[cur], span(start, last))
		}
	}
	for _, t := range ts {
		if depth == 0 {
			if k, ok := keys[word(t)]; ok && t.Type == format.TokKeyword || ok && t.Type == format.TokIdent {
				flush()
				cur, start = k, t.start
			}
		}
		switch t.Type {
		case format.TokLParen:
			depth++
		case format.TokRParen:
			depth--
		}
		last = t.end
	}
	flush()
}

// option describes a CREATE ROLE / CREATE SEQUENCE option: the keyword(s)
// starting it and its property key.
type option struct {
	words []string
	key   string
}

var roleOptions = []option{
	{[]string{"LOGIN"}, "can_login"}, {[]string{"NOLOGIN"}, "can_login"},
	{[]string{"SUPERUSER"}, "superuser"}, {[]string{"NOSUPERUSER"}, "superuser"},
	{[]string{"CREATEDB"}, "create_db"}, {[]string{"NOCREATEDB"}, "create_db"},
	{[]string{"CREATEROLE"}, "create_role"}, {[]string{"NOCREATEROLE"}, "create_role"},
	{[]string{"INHERIT"}, "inherit"}, {[]string{"NOINHERIT"}, "inherit"},
	{[]string{"REPLICATION"}, "is_replication"}, {[]string{"NOREPLICATION"}, "is_replication"},
	{[]string{"BYPASSRLS"}, "bypass_rls"}, {[]string{"NOBYPASSRLS"}, "bypass_rls"},
	{[]string{"CONNECTION", "LIMIT"}, "connection_limit"},
	{[]string{"ENCRYPTED", "PASSWORD"}, "password_hash"}, {[]string{"PASSWORD"}, "password_hash"},
	{[]string{"VALID", "UNTIL"}, "valid_until"},
	{[]string{"IN", "ROLE"}, "memberships"}, {[]string{"IN", "GROUP"}, "memberships"},
	{[]string{"ROLE"}, "memberships"}, {[]string{"ADMIN"}, "memberships"}, {[]string{"USER"}, "memberships"},
	{[]string{"SYSID"}, "sysid"},
}

var sequenceOptions = []option{
	{[]string{"AS"}, "as_type"}, {[]string{"INCREMENT"}, "increment_by"},
	{[]string{"NO", "MINVALUE"}, "min_value"}, {[]string{"MINVALUE"}, "min_value"},
	{[]string{"NO", "MAXVALUE"}, "max_value"}, {[]string{"MAXVALUE"}, "max_value"},
	{[]string{"START"}, "start_value"}, {[]string{"CACHE"}, "cache"},
	{[]string{"NO", "CYCLE"}, "cycle"}, {[]string{"CYCLE"}, "cycle"},
	{[]string{"OWNED", "BY"}, "owned_by"},
}

// indexOptions records option clauses: each runs from its keyword to the
// next option's keyword. ts excludes the object's name.
func indexOptions(items map[string][]pipeline.Span, ts []tok, opts []option, span func(a, b int) pipeline.Span) {
	match := func(i int) (option, bool) {
		for _, o := range opts {
			if i+len(o.words) > len(ts) {
				continue
			}
			ok := true
			for j, w := range o.words {
				if word(ts[i+j]) != w {
					ok = false
					break
				}
			}
			if ok {
				return o, true
			}
		}
		return option{}, false
	}
	cur, start, last := "", -1, -1
	for i := 0; i < len(ts); i++ {
		if o, ok := match(i); ok && !(o.key == "memberships" && cur == "memberships" && word(ts[i]) == "ROLE" && i > 0 && word(ts[i-1]) == "IN") {
			if cur != "" {
				items[cur] = append(items[cur], span(start, last))
			}
			cur, start = o.key, ts[i].start
			i += len(o.words) - 1
			last = ts[i].end
			continue
		}
		last = ts[i].end
	}
	if cur != "" {
		items[cur] = append(items[cur], span(start, last))
	}
}

// routineOptions are the clauses of CREATE FUNCTION / PROCEDURE after the
// argument list, longest first where one starts another.
var routineOptions = []option{
	{[]string{"RETURNS", "NULL", "ON", "NULL", "INPUT"}, "strict"},
	{[]string{"CALLED", "ON", "NULL", "INPUT"}, "strict"}, {[]string{"STRICT"}, "strict"},
	{[]string{"RETURNS"}, "return_type"}, {[]string{"LANGUAGE"}, "language"},
	{[]string{"IMMUTABLE"}, "volatility"}, {[]string{"STABLE"}, "volatility"}, {[]string{"VOLATILE"}, "volatility"},
	{[]string{"EXTERNAL", "SECURITY"}, "security_def"}, {[]string{"SECURITY"}, "security_def"},
	{[]string{"NOT", "LEAKPROOF"}, "leakproof"}, {[]string{"LEAKPROOF"}, "leakproof"},
	{[]string{"PARALLEL"}, "parallel"}, {[]string{"COST"}, "cost"}, {[]string{"ROWS"}, "rows"},
	{[]string{"SET"}, "config"}, {[]string{"TRANSFORM"}, "transforms"}, {[]string{"WINDOW"}, "window"},
	{[]string{"AS"}, "body_hash"}, {[]string{"BEGIN", "ATOMIC"}, "body_hash"},
}

// indexRoutine records a function's or procedure's clauses after its
// argument list; the body is "body_hash" (the AS clause or BEGIN ATOMIC
// block).
func indexRoutine(items map[string][]pipeline.Span, ts []tok, span func(a, b int) pipeline.Span) {
	_, close, ok := firstParens(ts)
	if !ok {
		return
	}
	var rest []tok
	for _, t := range code(ts) {
		if t.start >= close {
			rest = append(rest, t)
		}
	}
	// A RETURNS TABLE (...) list or a type with parentheses stays inside
	// its clause: indexOptions only splits at option keywords, which
	// never appear inside parentheses here except SET in a SET clause.
	indexOptions(items, rest, routineOptions, span)
}
