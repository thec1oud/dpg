package linter

import (
	"fmt"

	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// checkDeprecatedReference implements RFC Section 19.1's deprecated-reference rule:
// warn when a non-deprecated object references a deprecated object or
// column. v1 scope, deliberately narrow (see rfc/dpg-1.md Section 19.3 for
// the full residual list): FOREIGN KEY table/column references, and
// column/function-parameter/function-return TYPE references to a deprecated
// custom TYPE. NOT covered: view query-column references, function/
// procedure body references, DEFAULT-expression references — all three
// would need either real SQL-AST analysis (no parser exists for any of
// those opaque text blobs today) or a text-scan heuristic, and unlike
// internal/graph's edge-ordering heuristics (where an over-match is
// harmless), a false positive here is a visible user-facing warning.
func checkDeprecatedReference(objects []pipeline.IRObject) []pipeline.LintDiagnostic {
	var diags []pipeline.LintDiagnostic

	deprecatedTables := make(map[string]string)  // "schema.table" -> reason
	deprecatedColumns := make(map[string]string) // "schema.table.column" -> reason
	deprecatedTypes := make(map[string]string)   // "schema.name" -> reason

	for _, obj := range objects {
		switch o := obj.(type) {
		case *ir.Table:
			if o.Deprecated != nil {
				deprecatedTables[o.Schema+"."+o.Name] = *o.Deprecated
			}
			for _, col := range o.Columns {
				if col.Deprecated != nil {
					deprecatedColumns[o.Schema+"."+o.Name+"."+col.Name] = *col.Deprecated
				}
			}
		case *ir.Type:
			if o.Deprecated != nil {
				deprecatedTypes[o.Schema+"."+o.Name] = *o.Deprecated
			}
		}
	}
	if len(deprecatedTables) == 0 && len(deprecatedColumns) == 0 && len(deprecatedTypes) == 0 {
		return diags
	}

	// resolveTypeKey returns the schema-qualified lookup key for t as seen
	// from an object declared in fallbackSchema, and false when t can't
	// possibly be a reference to a custom TYPE at all (explicitly
	// pg_catalog-qualified, or empty) — same early-exit shape as
	// internal/graph/graph.go's typeRefEdge. An unqualified t.Schema=="" is
	// ambiguous between "built-in" and "same schema as the referencing
	// object" (typeRefEdge's own comment explains why); resolving it against
	// fallbackSchema and then only firing on an actual index hit is safe
	// either way, since a real built-in type will never appear in
	// deprecatedTypes.
	resolveTypeKey := func(t ir.TypeRef, fallbackSchema string) (string, bool) {
		if t.Schema == "pg_catalog" || t.Name == "" {
			return "", false
		}
		schema := t.Schema
		if schema == "" {
			schema = fallbackSchema
		}
		return schema + "." + t.Name, true
	}

	checkTypeRef := func(t ir.TypeRef, fallbackSchema string, pos pipeline.SourcePos, refDesc string) {
		key, ok := resolveTypeKey(t, fallbackSchema)
		if !ok {
			return
		}
		if reason, ok := deprecatedTypes[key]; ok {
			diags = append(diags, pipeline.LintDiagnostic{
				Pos:     pos,
				Rule:    "deprecated-reference",
				Message: fmt.Sprintf("%s references deprecated type %s: %s", refDesc, key, reason),
			})
		}
	}

	for _, obj := range objects {
		switch o := obj.(type) {
		case *ir.Table:
			if o.Deprecated == nil {
				for _, cst := range o.Constraints {
					if cst.Type != "FOREIGN KEY" || cst.RefTable == "" {
						continue
					}
					refSchema := cst.RefSchema
					if refSchema == "" {
						refSchema = o.Schema
					}
					refTableKey := refSchema + "." + cst.RefTable
					if reason, ok := deprecatedTables[refTableKey]; ok {
						diags = append(diags, pipeline.LintDiagnostic{
							Pos:  cst.Pos,
							Rule: "deprecated-reference",
							Message: fmt.Sprintf("foreign key %s on %s references deprecated table %s: %s",
								constraintLabel(cst), o.QualifiedName(), refTableKey, reason),
						})
					}
					for _, refCol := range cst.RefColumns {
						colKey := refTableKey + "." + refCol
						if reason, ok := deprecatedColumns[colKey]; ok {
							diags = append(diags, pipeline.LintDiagnostic{
								Pos:  cst.Pos,
								Rule: "deprecated-reference",
								Message: fmt.Sprintf("foreign key %s on %s references deprecated column %s: %s",
									constraintLabel(cst), o.QualifiedName(), colKey, reason),
							})
						}
					}
				}
			}
			for _, col := range o.Columns {
				if col.Deprecated != nil {
					continue
				}
				checkTypeRef(col.Type, o.Schema, col.SrcPos,
					fmt.Sprintf("column %s.%s", o.QualifiedName(), col.Name))
			}
		case *ir.Function:
			if o.Deprecated != nil {
				continue
			}
			for _, arg := range o.Args {
				checkTypeRef(arg.Type, o.Schema, o.SrcPos,
					fmt.Sprintf("function %s parameter %s", o.QualifiedName(), argLabel(arg)))
			}
			checkTypeRef(o.ReturnType, o.Schema, o.SrcPos,
				fmt.Sprintf("function %s return type", o.QualifiedName()))
		case *ir.Procedure:
			// Procedure has no Deprecated field of its own — nothing to gate on.
			for _, arg := range o.Args {
				checkTypeRef(arg.Type, o.Schema, o.SrcPos,
					fmt.Sprintf("procedure %s parameter %s", o.QualifiedName(), argLabel(arg)))
			}
		}
	}

	return diags
}

func constraintLabel(c *ir.Constraint) string {
	if c.Name != "" {
		return c.Name
	}
	return "(unnamed)"
}

func argLabel(a ir.FuncArg) string {
	if a.Name != "" {
		return a.Name
	}
	return "(unnamed)"
}
