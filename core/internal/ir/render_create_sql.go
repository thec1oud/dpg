package ir

import (
	"fmt"
	"regexp"
	"strings"
)

// qualIdentQuoted renders a possibly-schema-qualified identifier using this
// package's own quoteIdent (builder.go), quoting each part independently.
func qualIdentQuoted(schema, name string) string {
	if schema == "" {
		return quoteIdent(name)
	}
	return quoteIdent(schema) + "." + quoteIdent(name)
}

var dollarTagRe = regexp.MustCompile(`\$[A-Za-z0-9_]*\$`)

// quoteLit single-quotes a SQL string literal — package ir's own copy of
// internal/diff's identical helper (unexported, no shared package to pull
// from without a larger refactor).
func quoteLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// safeDollarTag picks a dollar-quote tag guaranteed not to collide with any
// $..$-shaped sequence already present in body (e.g. dynamic SQL that
// itself uses dollar-quoted string literals). Falls back from the
// conventional "$$" through "$dpg$", "$dpg1$", "$dpg2$", ... on collision.
func safeDollarTag(body string) string {
	used := make(map[string]bool)
	for _, m := range dollarTagRe.FindAllString(body, -1) {
		used[m] = true
	}
	if !used["$$"] {
		return "$$"
	}
	for i := 0; ; i++ {
		tag := "$dpg$"
		if i > 0 {
			tag = fmt.Sprintf("$dpg%d$", i)
		}
		if !used[tag] {
			return tag
		}
	}
}

// RenderCreateFunctionSQL renders o as a standalone, valid
// "CREATE OR REPLACE FUNCTION ... AS <tag>...<tag>;" statement. Used both
// for real migration SQL emission (internal/diff) and to build a compile
// shim for HashFunctionBody's plpgsql canonicalisation (internal/introspect)
// — both need identical, argument-accurate rendering, so this is the single
// shared implementation rather than parallel ones.
func RenderCreateFunctionSQL(o *Function) string {
	var b strings.Builder
	b.WriteString("CREATE OR REPLACE FUNCTION ")
	b.WriteString(qualIdentQuoted(o.Schema, o.Name))
	b.WriteString("(")
	first := true
	for _, a := range o.Args {
		// RETURNS TABLE(...) columns are declared in a separate clause
		// below, never inline here — "TABLE a integer" is not valid
		// parameter syntax.
		if a.Mode == "TABLE" {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		if a.Mode != "" && a.Mode != "IN" {
			b.WriteString(a.Mode)
			b.WriteString(" ")
		}
		if a.Name != "" {
			b.WriteString(a.Name)
			b.WriteString(" ")
		}
		b.WriteString(a.Type.String())
		if a.Default != nil {
			b.WriteString(" DEFAULT ")
			b.WriteString(*a.Default)
		}
	}
	b.WriteString(") RETURNS ")
	if tableCols := FuncTableColumns(o.Args); len(tableCols) > 0 {
		b.WriteString("TABLE(")
		b.WriteString(FormatTableColumns(tableCols))
		b.WriteString(")")
	} else {
		if o.ReturnType.SetOf {
			b.WriteString("SETOF ")
		}
		b.WriteString(o.ReturnType.String())
	}
	b.WriteString(" LANGUAGE ")
	b.WriteString(o.Attrs.Language)
	writeTransforms(&b, o.Attrs.Transforms)
	if o.Attrs.Volatility != "" && o.Attrs.Volatility != "VOLATILE" {
		b.WriteString(" ")
		b.WriteString(o.Attrs.Volatility)
	}
	if o.Attrs.Leakproof {
		b.WriteString(" LEAKPROOF")
	}
	if o.Attrs.Strict {
		b.WriteString(" STRICT")
	}
	if o.Attrs.SecurityDef {
		b.WriteString(" SECURITY DEFINER")
	}
	if o.Attrs.Parallel != "" && o.Attrs.Parallel != "UNSAFE" {
		b.WriteString(" PARALLEL ")
		b.WriteString(o.Attrs.Parallel)
	}
	if o.Attrs.Cost != nil {
		fmt.Fprintf(&b, " COST %v", *o.Attrs.Cost)
	}
	if o.Attrs.Rows != nil {
		fmt.Fprintf(&b, " ROWS %v", *o.Attrs.Rows)
	}
	writeFuncConfig(&b, o.Attrs.Config)
	writeFuncBody(&b, o.Attrs)
	b.WriteString(";")
	return b.String()
}

// writeFuncConfig renders Section 9.2's `SET param = value` / `SET param
// FROM CURRENT` clause list, shared by Function and Procedure (confirmed
// live: PostgreSQL accepts SET on both). One clause per entry, in
// declaration order — real PostgreSQL accepts (and pg_get_functiondef
// itself emits) multiple SET clauses on one CREATE FUNCTION/PROCEDURE
// statement.
func writeFuncConfig(b *strings.Builder, config []FuncConfig) {
	for _, c := range config {
		b.WriteString(" SET ")
		b.WriteString(c.Param)
		if c.FromCurrent {
			b.WriteString(" FROM CURRENT")
		} else if c.Value != nil {
			b.WriteString(" = ")
			b.WriteString(*c.Value)
		}
	}
}

// writeTransforms renders RFC audit item #26's `TRANSFORM FOR TYPE t [, FOR
// TYPE t ...]` clause, shared by Function and Procedure (real PostgreSQL
// accepts TRANSFORM on both). No-op when transforms is empty.
func writeTransforms(b *strings.Builder, transforms []TypeRef) {
	if len(transforms) == 0 {
		return
	}
	b.WriteString(" TRANSFORM ")
	for i, t := range transforms {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("FOR TYPE ")
		b.WriteString(t.String())
	}
}

// writeFuncBody renders the trailing body clause shared by
// RenderCreateFunctionSQL/RenderCreateProcedureSQL: RFC audit item #27's
// `AS 'obj_file'[, 'link_symbol']` form when ObjFile is set (LANGUAGE C/
// internal), item #28's `BEGIN ATOMIC ... END` form when AtomicBody is set
// (LANGUAGE SQL only), or the ordinary dollar-quoted `AS $$...$$` body
// otherwise. The caller appends the statement-terminating ";" itself — valid
// after all three forms.
func writeFuncBody(b *strings.Builder, attrs FuncAttrs) {
	if attrs.ObjFile != nil {
		b.WriteString(" AS ")
		b.WriteString(quoteLit(*attrs.ObjFile))
		if attrs.LinkSymbol != nil {
			b.WriteString(", ")
			b.WriteString(quoteLit(*attrs.LinkSymbol))
		}
		return
	}
	if attrs.AtomicBody {
		b.WriteString(" BEGIN ATOMIC ")
		b.WriteString(attrs.Body)
		b.WriteString(" END")
		return
	}
	tag := safeDollarTag(attrs.Body)
	b.WriteString(" AS ")
	b.WriteString(tag)
	b.WriteString(attrs.Body)
	b.WriteString(tag)
}

// RenderCreateProcedureSQL is RenderCreateFunctionSQL's PROCEDURE
// counterpart. Procedures never take STRICT/PARALLEL/COST/ROWS/LEAKPROOF in
// this codebase's existing emission (matches real PostgreSQL, which doesn't
// accept any of those on a procedure), but DO accept SECURITY DEFINER and
// SET (RFC Section 9.3's own worked example uses SECURITY DEFINER —
// confirmed live via pg_query.Parse and a real CREATE PROCEDURE, both were
// previously silently dropped here, the same "recreate uses this renderer
// even for the very first CREATE" gap the RFC's own example exposed for
// SET), plus TRANSFORM and every func-body form (RFC audit items #26-#28).
func RenderCreateProcedureSQL(o *Procedure) string {
	var b strings.Builder
	b.WriteString("CREATE OR REPLACE PROCEDURE ")
	b.WriteString(qualIdentQuoted(o.Schema, o.Name))
	b.WriteString("(")
	for i, a := range o.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Mode != "" && a.Mode != "IN" {
			b.WriteString(a.Mode)
			b.WriteString(" ")
		}
		if a.Name != "" {
			b.WriteString(a.Name)
			b.WriteString(" ")
		}
		b.WriteString(a.Type.String())
	}
	b.WriteString(") LANGUAGE ")
	b.WriteString(o.Attrs.Language)
	writeTransforms(&b, o.Attrs.Transforms)
	if o.Attrs.SecurityDef {
		b.WriteString(" SECURITY DEFINER")
	}
	writeFuncConfig(&b, o.Attrs.Config)
	writeFuncBody(&b, o.Attrs)
	b.WriteString(";")
	return b.String()
}
