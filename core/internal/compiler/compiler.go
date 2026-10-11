// Package compiler orchestrates the DPG pipeline stages from source files
// through to a sorted []pipeline.IRObject ready for diffing.
package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanutil"
)

// Compile reads all source files, runs them through every pipeline stage up to
// dependency resolution, and returns a sorted slice of fully-resolved IRObjects.
//
// dbDir is the database source root directory. Files located under
// dbDir/schemas/<name>/... have their schema context inferred from the directory
// name when no explicit SCHEMA block is present. A SCHEMA block inside the
// schemas/ hierarchy is a compile error.
//
// The second return value holds compile-time warning diagnostics (DPG-E031
// duplicate Name Map tool keys); callers apply [linter.rules] overrides to
// it before reporting.
func Compile(files []string, dbDir string, reg *pipeline.Registry) ([]pipeline.IRObject, []pipeline.LintDiagnostic, error) {
	return CompileWith(files, dbDir, reg, Options{})
}

// Options configures CompileWith.
type Options struct {
	// DefaultSchema is the database's default_schema (RFC Section 3.4),
	// used for objects with no schema qualifier outside any schema scope.
	// Empty means "public".
	DefaultSchema string
}

// CompileWith is Compile with explicit Options.
func CompileWith(files []string, dbDir string, reg *pipeline.Registry, opts Options) ([]pipeline.IRObject, []pipeline.LintDiagnostic, error) {
	tokenizer, err := pipeline.MustResolve[pipeline.Tokenizer](reg, pipeline.KeyTokenizer)
	if err != nil {
		return nil, nil, err
	}
	pgParser, err := pipeline.MustResolve[pipeline.PGSQLParser](reg, pipeline.KeyPGSQLParser)
	if err != nil {
		return nil, nil, err
	}
	blockParser, err := pipeline.MustResolve[pipeline.BlockParser](reg, pipeline.KeyBlockParser)
	if err != nil {
		return nil, nil, err
	}
	irBuilder, err := pipeline.MustResolve[pipeline.IRBuilder](reg, pipeline.KeyIRBuilder)
	if err != nil {
		return nil, nil, err
	}
	merger, err := pipeline.MustResolve[pipeline.Merger](reg, pipeline.KeyMerger)
	if err != nil {
		return nil, nil, err
	}
	resolver, err := pipeline.MustResolve[pipeline.DependencyResolver](reg, pipeline.KeyDependencyResolver)
	if err != nil {
		return nil, nil, err
	}

	var rawObjects []pipeline.RawObject
	var diags pipeline.Diagnostics
	// nameMapWarnings collects DPG-E031 findings (blockAST.NameMapWarnings)
	// from every parsed block, returned as Compile's warning diagnostics.
	var nameMapWarnings []pipeline.LintDiagnostic
	// Track unique directory-inferred schemas so we can inject synthetic declarations.
	dirSchemas := map[string]pipeline.SourcePos{}

	// Stage 0: If the tokenizer supports cross-file macro sharing, do a pre-pass
	// to collect all macro definitions before any file is tokenized.
	if seeder, ok := tokenizer.(pipeline.GlobalMacroSeeder); ok {
		for _, path := range files {
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, nil, fmt.Errorf("compiler: reading %s: %w", path, readErr)
			}
			src = scanutil.NormalizeSource(src)
			if seedErr := seeder.AddGlobalMacros(src); seedErr != nil {
				return nil, nil, fmt.Errorf("compiler: collecting macros from %s: %w", path, seedErr)
			}
		}
	}

	// Stage 1: Tokenize all source files.
	for _, path := range files {
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, nil, fmt.Errorf("compiler: reading %s: %w", path, readErr)
		}
		src = scanutil.NormalizeSource(src)
		raws, scanErr := tokenizer.Scan(path, src)
		if scanErr != nil {
			if d, ok := scanErr.(pipeline.Diagnostics); ok {
				diags = append(diags, d...)
				continue
			}
			return nil, nil, fmt.Errorf("compiler: scanning %s: %w", path, scanErr)
		}

		dirSchema := inferSchemaFromPath(dbDir, path)
		for i := range raws {
			// A file under schemas/<name>/ may declare SCHEMA <name> { ... }
			// to hold the schema's own directives (owner, comment, grants,
			// default privileges, RENAMED FROM), but no other schema.
			if dirSchema != "" && raws[i].Kind == pipeline.KindSchema {
				if declared := schemaDeclName(raws[i].Part1); declared != dirSchema {
					diags = append(diags, pipeline.ErrorfCode(raws[i].Pos, "DPG-E040",
						"SCHEMA %s is declared under schemas/%s/; a schema directory may only declare its own schema",
						declared, dirSchema))
				}
				continue
			}
			if raws[i].Schema == "" {
				raws[i].Schema = dirSchema
			}
		}

		if dirSchema != "" {
			if _, seen := dirSchemas[dirSchema]; !seen {
				pos := pipeline.SourcePos{File: path, Line: 1, Col: 1}
				dirSchemas[dirSchema] = pos
			}
		}

		rawObjects = append(rawObjects, raws...)
	}

	// Inject one synthetic SCHEMA declaration per directory-inferred schema.
	// This ensures schemas that exist only as directories appear in the desired
	// state, so the differ never generates a spurious DROP SCHEMA.
	// The synthetic raw object goes through the normal pipeline (Reconstruct →
	// "CREATE SCHEMA <name>" → pg_query → IR builder); the merger deduplicates
	// it with any explicit SCHEMA block for the same name.
	// "public" is skipped: it always exists in PostgreSQL and is never managed.
	for name, pos := range dirSchemas {
		if name == "public" {
			continue
		}
		// Always double-quote: unlike the explicit `SCHEMA name { }` form
		// (scanner.go's readSchemaDecl), a directory name is never
		// "written unquoted in source" the way an identifier is, so there's
		// no quoted/unquoted distinction to preserve — this is purely an
		// internally-generated CREATE SCHEMA statement, and quoting is
		// always valid PG syntax for any identifier. Without it, a schema
		// directory named after a reserved word (e.g. schemas/order/) hard-
		// fails pg_query with a syntax error, since the unquoted form is
		// only valid for non-reserved identifiers (confirmed live via
		// pg_query.Parse: "CREATE SCHEMA order" errors, "CREATE SCHEMA
		// \"order\"" doesn't) — same bug class as item 6k's explicit-declaration
		// fix, just never extended to this synthetic path.
		part1 := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		rawObjects = append(rawObjects, pipeline.RawObject{
			Kind:  pipeline.KindSchema,
			Part1: part1,
			Pos:   pos,
		})
	}

	if diags.HasErrors() {
		return nil, nil, diags
	}

	// Stages 2–3: Parse Part1 (PG SQL) + Part2 ({ } block) and build IR.
	var irObjects []pipeline.IRObject
	for _, raw := range rawObjects {
		// DEFAULT PRIVILEGES never goes through PGSQLParser: real
		// PostgreSQL's ALTER DEFAULT PRIVILEGES statement requires its
		// GRANT/REVOKE action inline, so raw.Part1 ("[FOR ROLE x] [IN SCHEMA
		// y]") is never valid standalone PG SQL on its own (confirmed live:
		// "syntax error at end of input") — see pipeline.DefaultPrivilegesBlock.
		// This applies whether the declaration is top-level or nested inside
		// a SCHEMA block (the scanner emits both the same way, with
		// raw.Schema set from the enclosing SCHEMA when nested).
		if raw.Kind == pipeline.KindDefaultPrivileges {
			dpBlock, dpErr := blockParser.ParseDefaultPrivileges(raw.Part1, raw.Part2, raw.Pos)
			if dpErr != nil {
				if ce, ok := dpErr.(*pipeline.CompilerError); ok {
					diags = append(diags, ce)
					continue
				}
				return nil, nil, dpErr
			}
			if dpBlock.InSchema == nil && raw.Schema != "" {
				dpBlock.InSchema = &pipeline.Identifier{Name: raw.Schema}
			} else if dpBlock.InSchema != nil && raw.Schema != "" && dpBlock.InSchema.Name != raw.Schema {
				diags = append(diags, pipeline.ErrorfCode(raw.Pos, "DPG-E040",
					"DEFAULT PRIVILEGES IN SCHEMA %s is declared inside schema %s; inside a schema scope, IN SCHEMA may only name that schema",
					dpBlock.InSchema.Name, raw.Schema))
				continue
			}
			objs, buildErr := irBuilder.BuildDefaultPrivileges(dpBlock)
			if buildErr != nil {
				if ce, ok := buildErr.(*pipeline.CompilerError); ok {
					diags = append(diags, ce)
					continue
				}
				return nil, nil, buildErr
			}
			irObjects = append(irObjects, objs...)
			continue
		}

		// PARAMETER PRIVILEGES never goes through PGSQLParser either — see
		// pipeline.ParameterPrivilegesBlock. Unlike DEFAULT PRIVILEGES it is
		// never nested inside a SCHEMA block (RFC Section 11.6: cluster-scoped,
		// configuration parameters have no schema), so there is no raw.Schema
		// fallback to apply here.
		if raw.Kind == pipeline.KindParameterPrivileges {
			ppBlock, ppErr := blockParser.ParseParameterPrivileges(raw.Part1, raw.Part2, raw.Pos)
			if ppErr != nil {
				if ce, ok := ppErr.(*pipeline.CompilerError); ok {
					diags = append(diags, ce)
					continue
				}
				return nil, nil, ppErr
			}
			objs, buildErr := irBuilder.BuildParameterPrivileges(ppBlock)
			if buildErr != nil {
				if ce, ok := buildErr.(*pipeline.CompilerError); ok {
					diags = append(diags, ce)
					continue
				}
				return nil, nil, buildErr
			}
			irObjects = append(irObjects, objs...)
			continue
		}

		pgResult, pgErr := pgParser.Parse(raw.Kind, raw.Part1, raw.Pos)
		pgResult.SchemaContext = raw.Schema
		pgResult.DefaultSchema = opts.DefaultSchema
		if pgErr != nil {
			if ce, ok := pgErr.(*pipeline.CompilerError); ok {
				diags = append(diags, ce)
				continue
			}
			return nil, nil, pgErr
		}

		blockAST, blockErr := blockParser.Parse(raw.Kind, raw.Part2, raw.Pos)
		if blockErr != nil {
			if ce, ok := blockErr.(*pipeline.CompilerError); ok {
				diags = append(diags, ce)
				continue
			}
			return nil, nil, blockErr
		}
		nameMapWarnings = append(nameMapWarnings, blockAST.NameMapWarnings...)
		for _, col := range blockAST.Columns {
			nameMapWarnings = append(nameMapWarnings, col.NameMapWarnings...)
		}

		obj, buildErr := irBuilder.Build(pgResult, blockAST)
		if buildErr != nil {
			if ce, ok := buildErr.(*pipeline.CompilerError); ok {
				diags = append(diags, ce)
				continue
			}
			return nil, nil, buildErr
		}
		irObjects = append(irObjects, obj)
	}
	if diags.HasErrors() {
		return nil, nil, diags
	}

	// Stage 4: Merge same-name declarations across files.
	merged, mergeErr := merger.Merge(irObjects)
	if mergeErr != nil {
		return nil, nameMapWarnings, mergeErr
	}

	// Stage 4.5: Resolve LIKE source_table column-list entries (Section
	// 7.1) into concrete columns/constraints now that every table in the
	// compile unit is known — must run before Sort, so the sorter's
	// dependency edges see the table's actual (resolved) column types.
	if err := irBuilder.ResolveLikeClauses(merged); err != nil {
		return nil, nameMapWarnings, err
	}

	// Stage 5: Topological sort with FK / type dependency resolution.
	sorted, sortErr := resolver.Sort(merged)
	if sortErr != nil {
		return nil, nameMapWarnings, sortErr
	}

	return sorted, nameMapWarnings, nil
}

// schemaDeclName returns the schema name a SCHEMA declaration's Part 1
// declares: a quoted name is unquoted, an unquoted one is already
// case-folded by the scanner.
func schemaDeclName(part1 string) string {
	name := strings.TrimSpace(part1)
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return name
}

// inferSchemaFromPath returns the schema name inferred from the file's position
// under dbDir/schemas/<name>/..., or "" if the file is not in that structure.
func inferSchemaFromPath(dbDir, filePath string) string {
	if dbDir == "" {
		return ""
	}
	rel, err := filepath.Rel(dbDir, filePath)
	if err != nil {
		return ""
	}
	// Use forward-slash segments on all platforms.
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 4)
	if len(parts) >= 3 && parts[0] == "schemas" && parts[1] != "" {
		return parts[1]
	}
	return ""
}
