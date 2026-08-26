package ir_test

import (
	"strings"
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/thec1oud/dpg/internal/ir"
)

func TestRenderCreateFunctionSQLDollarQuoteCollision(t *testing.T) {
	fn := &ir.Function{
		Schema:     "public",
		Name:       "f",
		ReturnType: ir.TypeRef{Name: "text"},
		Args:       []ir.FuncArg{{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}}},
		Attrs: ir.FuncAttrs{
			Language: "sql",
			Body:     "SELECT $$literal$$",
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with a body containing a literal $$ failed to parse: %v\nSQL: %s", err, sql)
	}
}

func TestRenderCreateFunctionSQLArgModes(t *testing.T) {
	fn := &ir.Function{
		Schema:     "public",
		Name:       "f",
		ReturnType: ir.TypeRef{Name: "record"},
		Args: []ir.FuncArg{
			{Name: "a", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
			{Name: "b", Mode: "OUT", Type: ir.TypeRef{Name: "integer"}},
			{Name: "rest", Mode: "VARIADIC", Type: ir.TypeRef{Name: "text"}},
		},
		Attrs: ir.FuncAttrs{
			Language: "sql",
			Body:     "SELECT a, rest",
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with IN/OUT/VARIADIC args failed to parse: %v\nSQL: %s", err, sql)
	}
}

func TestRenderCreateFunctionSQLReturnsTable(t *testing.T) {
	fn := &ir.Function{
		Schema: "public",
		Name:   "f",
		Args: []ir.FuncArg{
			{Name: "n", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
			{Name: "col1", Mode: "TABLE", Type: ir.TypeRef{Name: "integer"}},
		},
		Attrs: ir.FuncAttrs{
			Language: "sql",
			Body:     "SELECT n",
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with RETURNS TABLE failed to parse: %v\nSQL: %s", err, sql)
	}
}

func TestRenderCreateFunctionSQLLeakproofAndTransform(t *testing.T) {
	fn := &ir.Function{
		Schema:     "public",
		Name:       "f",
		ReturnType: ir.TypeRef{Name: "integer"},
		Args:       []ir.FuncArg{{Name: "x", Mode: "IN", Type: ir.TypeRef{Name: "hstore"}}},
		Attrs: ir.FuncAttrs{
			Language:   "sql",
			Leakproof:  true,
			Transforms: []ir.TypeRef{{Name: "hstore"}},
			Body:       "SELECT 1",
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with LEAKPROOF/TRANSFORM failed to parse: %v\nSQL: %s", err, sql)
	}
	if !strings.Contains(sql, "LEAKPROOF") {
		t.Errorf("expected LEAKPROOF in rendered SQL, got: %s", sql)
	}
	if !strings.Contains(sql, "TRANSFORM FOR TYPE hstore") {
		t.Errorf("expected TRANSFORM FOR TYPE hstore in rendered SQL, got: %s", sql)
	}
}

func TestRenderCreateFunctionSQLObjFileLinkSymbol(t *testing.T) {
	objFile, linkSymbol := "$libdir/pgcrypto", "pg_digest"
	fn := &ir.Function{
		Schema:     "public",
		Name:       "digest",
		ReturnType: ir.TypeRef{Name: "bytea"},
		Args: []ir.FuncArg{
			{Name: "data", Mode: "IN", Type: ir.TypeRef{Name: "text"}},
			{Name: "typ", Mode: "IN", Type: ir.TypeRef{Name: "text"}},
		},
		Attrs: ir.FuncAttrs{
			Language:   "c",
			ObjFile:    &objFile,
			LinkSymbol: &linkSymbol,
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with AS 'obj_file', 'link_symbol' failed to parse: %v\nSQL: %s", err, sql)
	}
	if !strings.Contains(sql, "'$libdir/pgcrypto'") || !strings.Contains(sql, "'pg_digest'") {
		t.Errorf("expected both obj_file and link_symbol string literals in rendered SQL, got: %s", sql)
	}
}

func TestRenderCreateFunctionSQLAtomicBody(t *testing.T) {
	fn := &ir.Function{
		Schema:     "public",
		Name:       "f",
		ReturnType: ir.TypeRef{Name: "integer"},
		Args:       []ir.FuncArg{{Name: "x", Mode: "IN", Type: ir.TypeRef{Name: "integer"}}},
		Attrs: ir.FuncAttrs{
			Language:   "sql",
			AtomicBody: true,
			Body:       "SELECT x + 1;",
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered SQL with BEGIN ATOMIC failed to parse: %v\nSQL: %s", err, sql)
	}
	if !strings.Contains(sql, "BEGIN ATOMIC") || !strings.Contains(sql, "END") {
		t.Errorf("expected BEGIN ATOMIC ... END in rendered SQL, got: %s", sql)
	}
}

func TestRenderCreateProcedureSQLAtomicBodyAndTransform(t *testing.T) {
	proc := &ir.Procedure{
		Schema: "public",
		Name:   "p",
		Args:   []ir.FuncArg{{Name: "x", Mode: "IN", Type: ir.TypeRef{Name: "integer"}}},
		Attrs: ir.FuncAttrs{
			Language:   "sql",
			AtomicBody: true,
			Body:       "SELECT x;",
		},
	}
	sql := ir.RenderCreateProcedureSQL(proc)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered PROCEDURE SQL with BEGIN ATOMIC failed to parse: %v\nSQL: %s", err, sql)
	}
	if !strings.Contains(sql, "BEGIN ATOMIC") {
		t.Errorf("expected BEGIN ATOMIC in rendered SQL, got: %s", sql)
	}
}

func TestRenderCreateProcedureSQLArgNames(t *testing.T) {
	proc := &ir.Procedure{
		Schema: "public",
		Name:   "p",
		Args: []ir.FuncArg{
			{Name: "a", Mode: "IN", Type: ir.TypeRef{Name: "integer"}},
			{Name: "b", Mode: "INOUT", Type: ir.TypeRef{Name: "integer"}},
		},
		Attrs: ir.FuncAttrs{
			Language: "plpgsql",
			Body:     "BEGIN b := a + 1; END;",
		},
	}
	sql := ir.RenderCreateProcedureSQL(proc)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered PROCEDURE SQL failed to parse: %v\nSQL: %s", err, sql)
	}
}

// TestRenderCreateFunctionSQLSetConfig guards the Phase 3 launch-readiness
// audit fix: RenderCreateFunctionSQL previously had no rendering for
// Section 9.2's SET clause list at all, so a change that otherwise
// triggered a recreate (e.g. a Cost change) would silently drop any
// declared SET clause from the re-emitted CREATE OR REPLACE.
func TestRenderCreateFunctionSQLSetConfig(t *testing.T) {
	val := "public"
	fn := &ir.Function{
		Schema:     "public",
		Name:       "f",
		ReturnType: ir.TypeRef{Name: "int"},
		Attrs: ir.FuncAttrs{
			Language: "sql",
			Body:     "SELECT 1",
			Config: []ir.FuncConfig{
				{Param: "search_path", Value: &val},
				{Param: "work_mem", FromCurrent: true},
			},
		},
	}
	sql := ir.RenderCreateFunctionSQL(fn)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered FUNCTION SQL with SET failed to parse: %v\nSQL: %s", err, sql)
	}
	if !strings.Contains(sql, "SET search_path = public") {
		t.Errorf("expected SET search_path = public in rendered SQL, got: %s", sql)
	}
	if !strings.Contains(sql, "SET work_mem FROM CURRENT") {
		t.Errorf("expected SET work_mem FROM CURRENT in rendered SQL, got: %s", sql)
	}
}

// TestRenderCreateProcedureSQLSecurityDefinerAndSetConfig guards the Phase 3
// launch-readiness audit finding: RFC Section 9.3's own worked example
// declares a PROCEDURE with SECURITY DEFINER, but RenderCreateProcedureSQL
// never rendered SecurityDef or Config at all — since createProcedure uses
// this same renderer even for the very first CREATE (not just later
// recreates), a procedure declaring either was silently stripped of it on
// every apply, confirmed live against a real CREATE PROCEDURE.
func TestRenderCreateProcedureSQLSecurityDefinerAndSetConfig(t *testing.T) {
	val := "public"
	proc := &ir.Procedure{
		Schema: "public",
		Name:   "p",
		Attrs: ir.FuncAttrs{
			Language:    "plpgsql",
			SecurityDef: true,
			Body:        "BEGIN NULL; END;",
			Config:      []ir.FuncConfig{{Param: "search_path", Value: &val}},
		},
	}
	sql := ir.RenderCreateProcedureSQL(proc)
	if _, err := pg_query.Parse(sql); err != nil {
		t.Fatalf("rendered PROCEDURE SQL with SECURITY DEFINER/SET failed to parse: %v\nSQL: %s", err, sql)
	}
	if !strings.Contains(sql, "SECURITY DEFINER") {
		t.Errorf("expected SECURITY DEFINER in rendered SQL, got: %s", sql)
	}
	if !strings.Contains(sql, "SET search_path = public") {
		t.Errorf("expected SET search_path = public in rendered SQL, got: %s", sql)
	}
}
