// Package ir implements pipeline.IRBuilder. The Builder converts a
// (pipeline.PGParseResult, pipeline.BlockAST) pair into a pipeline.IRObject.
package ir

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/thec1oud/dpg/internal/ast"
	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyIRBuilder, NewBuilder())
}

// Builder implements pipeline.IRBuilder.
type Builder struct{}

// NewBuilder returns a Builder.
func NewBuilder() *Builder { return &Builder{} }

// Build implements pipeline.IRBuilder. It dispatches on the ObjectKind embedded
// in pg.Pos (via the RawObject that produced pg) — but since PGParseResult does
// not carry the ObjectKind directly, we inspect the protobuf node type instead.
// For passthrough kinds (KindVirtualType), pg.Kind is set and pg.Raw is a string.
func (b *Builder) Build(pg pipeline.PGParseResult, block pipeline.BlockAST) (pipeline.IRObject, error) {
	if pg.Kind == pipeline.KindVirtualType {
		return b.buildVirtualType(pg.Raw.(string), block, pg.Pos, pg)
	}

	node := ast.FirstStmt(pg)
	if node == nil {
		return nil, pipeline.Errorf(pg.Pos, "empty PG parse result")
	}
	pos := pg.Pos

	// OPERATOR/FUNCTION block directives (RFC Section 14.4's OPERATOR FAMILY loose
	// members) are only meaningful on an OPERATOR FAMILY declaration — the
	// BlockParser itself has no per-kind gating (it parses Part 2 without
	// knowing what Part 1 declared), so this check has to live at the one
	// place both halves are visible together. Checked centrally here, not in
	// buildOpaque, because several kinds (Cast, Tablespace, FDW, ...) have
	// their own dedicated build* function and never go through buildOpaque
	// at all.
	if len(block.OpFamilyMembers) > 0 {
		if _, ok := node.Node.(*pg_query.Node_CreateOpFamilyStmt); !ok {
			return nil, pipeline.Errorf(block.OpFamilyMembers[0].Pos,
				"OPERATOR/FUNCTION members are only valid in an OPERATOR FAMILY { } block")
		}
	}

	// ATTRIBUTE/ATTRIBUTES blocks (RFC Section 5.2) belong to composite
	// types only, checked here for the same reason as OPERATOR/FUNCTION
	// members above.
	if len(block.Attributes) > 0 && pg.Kind != pipeline.KindCompositeType {
		return nil, pipeline.ErrorfCode(block.Attributes[0].Pos, "DPG-E024",
			"ATTRIBUTE is only valid in a composite TYPE block; table columns use COLUMN name { ... }")
	}

	var obj pipeline.IRObject
	var err error
	switch n := node.Node.(type) {
	case *pg_query.Node_CreateStmt:
		// CREATE TABLE and CREATE UNLOGGED TABLE parse to the identical
		// Node_CreateStmt type — pg_query distinguishes them only via
		// Relation.Relpersistence ("u" for unlogged), not a separate node —
		// so unlogged must be read from the parsed statement itself, not
		// hardcoded. Found live-testing a demo project: this was
		// unconditionally false regardless of source, so a declared
		// UNLOGGED TABLE always silently built as a regular table.
		unlogged := n.CreateStmt.Relation != nil && n.CreateStmt.Relation.Relpersistence == "u"
		obj, err = b.buildTable(n.CreateStmt, block, pos, unlogged, false)
	case *pg_query.Node_CreateForeignTableStmt:
		obj, err = b.buildForeignTable(n.CreateForeignTableStmt, block, pos)
	case *pg_query.Node_ViewStmt:
		obj, err = b.buildView(n.ViewStmt, block, pos, false, pg.Kind == pipeline.KindRecursiveView)
	case *pg_query.Node_CreateTableAsStmt:
		if n.CreateTableAsStmt.Objtype == pg_query.ObjectType_OBJECT_MATVIEW {
			obj, err = b.buildMaterializedView(n.CreateTableAsStmt, block, pos)
		} else {
			// Plain "CREATE TABLE ... AS SELECT ..." — not a DPG-documented
			// table-declaration form (tables are declared via an explicit
			// column list, RFC Section 4); falls through to the generic opaque
			// fallback below, unchanged from before this case existed.
			obj = &OpaqueObject{kind: "UNKNOWN", body: "", SrcPos: pos}
		}
	case *pg_query.Node_CreateFunctionStmt:
		obj, err = b.buildFunction(n.CreateFunctionStmt, pg, block, pos)
	case *pg_query.Node_CreateEnumStmt:
		obj, err = b.buildEnum(n.CreateEnumStmt, block, pos)
	case *pg_query.Node_CompositeTypeStmt:
		obj, err = b.buildCompositeType(n.CompositeTypeStmt, block, pos)
	case *pg_query.Node_CreateSchemaStmt:
		obj, err = b.buildSchema(n.CreateSchemaStmt, block, pos)
	case *pg_query.Node_CreateExtensionStmt:
		obj, err = b.buildExtension(n.CreateExtensionStmt, block, pos)
	case *pg_query.Node_CreateSeqStmt:
		obj, err = b.buildSequence(n.CreateSeqStmt, block, pos)
	case *pg_query.Node_CreateRoleStmt:
		obj, err = b.buildRole(n.CreateRoleStmt, block, pos)
	case *pg_query.Node_CreateTableSpaceStmt:
		obj, err = b.buildTablespace(n.CreateTableSpaceStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateFdwStmt:
		obj, err = b.buildFDW(n.CreateFdwStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateForeignServerStmt:
		obj, err = b.buildServer(n.CreateForeignServerStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateUserMappingStmt:
		obj, err = b.buildUserMapping(n.CreateUserMappingStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreatePublicationStmt:
		obj, err = b.buildOpaque(node, block, pos, "PUBLICATION")
	case *pg_query.Node_CreateSubscriptionStmt:
		obj, err = b.buildOpaque(node, block, pos, "SUBSCRIPTION")
	case *pg_query.Node_CreateEventTrigStmt:
		obj, err = b.buildOpaque(node, block, pos, "EVENT TRIGGER")
	case *pg_query.Node_DefineStmt:
		obj, err = b.buildDefineStmt(n.DefineStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateDomainStmt:
		obj, err = b.buildDomain(n.CreateDomainStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateRangeStmt:
		obj, err = b.buildRangeType(n.CreateRangeStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateOpClassStmt:
		obj, err = b.buildOpaque(node, block, pos, "OPERATOR CLASS")
	case *pg_query.Node_CreateOpFamilyStmt:
		obj, err = b.buildOpaque(node, block, pos, "OPERATOR FAMILY")
	case *pg_query.Node_CreateStatsStmt:
		obj, err = b.buildStatistics(n.CreateStatsStmt, block, pos, rawSQL(node))
	case *pg_query.Node_CreateOpClassItem:
		obj, err = b.buildOpaque(node, block, pos, "OPERATOR")
	case *pg_query.Node_CreateCastStmt:
		obj, err = b.buildCast(n.CreateCastStmt, block, pos, rawSQL(node))
	default:
		obj = &OpaqueObject{kind: "UNKNOWN", body: "", SrcPos: pos}
	}
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, nil
	}
	// Inside a schema scope (SCHEMA { } block or schemas/<name>/ directory),
	// a schema qualifier on the declared name may only repeat that schema
	// (RFC Section 4.4).
	if pg.SchemaContext != "" {
		if declared := declaredSchema(obj); declared != "" && declared != pg.SchemaContext {
			return nil, pipeline.ErrorfCode(pos, "DPG-E040",
				"%s is qualified with schema %s but declared inside schema %s; inside a schema scope, a qualifier may only name that schema",
				obj.QualifiedName(), declared, pg.SchemaContext)
		}
	}
	// An unqualified object takes the enclosing scope's schema, else the
	// database's default_schema (RFC Section 3.4), else "public", so desired
	// IR always carries the explicit schema name the introspector returns.
	applySchemaContext(obj, effectiveSchema(pg))
	return obj, nil
}

// foldIdentPart applies PostgreSQL identifier rules to one name part: a
// double-quoted part is unquoted, anything else is lowercased (RFC Section
// 4.10).
func foldIdentPart(part string) string {
	part = strings.TrimSpace(part)
	if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
		return strings.ReplaceAll(part[1:len(part)-1], `""`, `"`)
	}
	return strings.ToLower(part)
}

// effectiveSchema returns the schema for an unqualified object: the enclosing
// schema scope, else the database's default_schema, else "public".
func effectiveSchema(pg pipeline.PGParseResult) string {
	switch {
	case pg.SchemaContext != "":
		return pg.SchemaContext
	case pg.DefaultSchema != "":
		return pg.DefaultSchema
	default:
		return "public"
	}
}

// declaredSchema returns the schema qualifier written on a schema-scoped
// object's own name, or "" when the object has none (or has no Schema
// field at all).
func declaredSchema(obj pipeline.IRObject) string {
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return ""
	}
	f := v.Elem().FieldByName("Schema")
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

// applySchemaContext sets the Schema field on schema-scoped IR objects when it
// is empty, using the enclosing SCHEMA { } block's name as the context.
func applySchemaContext(obj pipeline.IRObject, schema string) {
	switch o := obj.(type) {
	case *Table:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *View:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Function:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Procedure:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Type:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Sequence:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Aggregate:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Operator:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *Collation:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *TSConfig:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *TSDict:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *TSParser:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *TSTemplate:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *StatisticsObject:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *OperatorClass:
		if o.Schema == "" {
			o.Schema = schema
		}
	case *OperatorFamily:
		if o.Schema == "" {
			o.Schema = schema
		}
	}
}

// ── Table ─────────────────────────────────────────────────────────────────────

func (b *Builder) buildTable(cs *pg_query.CreateStmt, block pipeline.BlockAST, pos pipeline.SourcePos, unlogged, foreign bool) (pipeline.IRObject, error) {
	tbl := &Table{
		Schema:   rangeVarSchema(cs.Relation),
		Name:     cs.Relation.Relname,
		Unlogged: unlogged,
		SrcPos:   pos,
	}

	// Columns and table-level constraints from the pg_query parse.
	for _, elt := range cs.TableElts {
		switch e := elt.Node.(type) {
		case *pg_query.Node_ColumnDef:
			col, promoted, err := b.buildColumn(e.ColumnDef, pos)
			if err != nil {
				return nil, err
			}
			tbl.Columns = append(tbl.Columns, col)
			tbl.Constraints = append(tbl.Constraints, promoted...)
		case *pg_query.Node_Constraint:
			cst := buildConstraint(e.Constraint, pos)
			tbl.Constraints = append(tbl.Constraints, cst)
		case *pg_query.Node_TableLikeClause:
			// LIKE source_table [{INCLUDING|EXCLUDING} attr ...] (Section
			// 7.1) — captured verbatim; ResolveLikeClauses (internal/ir)
			// resolves it into concrete Columns/Constraints once every
			// object in the compile unit has been built, since a single
			// Build call has no visibility into other declared tables.
			// Previously this case didn't exist at all: the element was
			// silently discarded, so "CREATE TABLE foo (LIKE bar)" built a
			// table with zero columns and no error.
			tlc := e.TableLikeClause
			tbl.LikeClauses = append(tbl.LikeClauses, &LikeClause{
				SourceSchema: rangeVarSchema(tlc.Relation),
				SourceName:   tlc.Relation.Relname,
				Options:      tlc.Options,
				InsertAt:     len(tbl.Columns),
				Pos:          pos,
			})
		}
	}

	// Inheritance
	for _, inh := range cs.InhRelations {
		if rv := inh.GetRangeVar(); rv != nil {
			tbl.Inherits = append(tbl.Inherits, qualName(rv.Schemaname, rv.Relname))
		}
	}

	// Partition strategy
	if cs.Partspec != nil {
		tbl.PartitionBy = buildPartitionSpec(cs.Partspec)
	}

	// Storage params (WITH clause)
	if len(cs.Options) > 0 {
		tbl.StorageParams = buildOrderedOptions(cs.Options)
	}

	// OF type_name (Section 7.1's typed-table "Form 2") and USING method
	// (table access method) are both real PG grammar directly on CreateStmt
	// (OfTypename/AccessMethod) — no DPG-specific parsing needed.
	if cs.OfTypename != nil {
		tbl.OfType = typeNameToRef(cs.OfTypename)
	}
	if cs.AccessMethod != "" {
		tbl.AccessMethod = cs.AccessMethod
	}
	// A typed table's parenthesized list can only ever contain per-
	// attribute "col WITH OPTIONS constraint..." narrowing entries (real
	// PostgreSQL rejects a genuinely new column there outright) or table-
	// level constraints — never a fresh ColumnDef with its own type. Any
	// tbl.Columns entry here is necessarily one of those WITH OPTIONS
	// narrowing forms, which this codebase deliberately doesn't model yet
	// (see ir.Table.OfType's doc comment) — reject explicitly rather than
	// silently dropping the declared narrowing when createTable/dump only
	// ever render OfType + Constraints for a typed table.
	if tbl.OfType.Name != "" && len(tbl.Columns) > 0 {
		return nil, pipeline.Errorf(pos,
			"typed table %s: per-attribute \"col WITH OPTIONS ...\" constraint narrowing (column %q) is not yet supported; declare only table-level constraints in the OF type_name column list",
			qualName(tbl.Schema, tbl.Name), tbl.Columns[0].Name)
	}

	// Tablespace
	if cs.Tablespacename != "" {
		ts := cs.Tablespacename
		tbl.Tablespace = &ts
	}

	// Merge in the BlockAST.
	if err := mergeTableBlock(tbl, block); err != nil {
		return nil, err
	}

	// A table-level named NOT NULL constraint (RFC Section 7.3, PostgreSQL
	// 18+) implies its column is NOT NULL the same way an inline "col type
	// NOT NULL" does — but unlike the inline form (buildColumn sets
	// col.NotNull directly), a table-level one is only ever visible via
	// buildConstraint/mergeTableBlock's promoted Constraint, which never
	// touches Column at all. Without this, a column declared NOT NULL
	// *only* via a table-level named constraint would build with
	// Column.NotNull left false, contradicting both the constraint that
	// was just built for it and real PostgreSQL's own behavior.
	// A table-level PRIMARY KEY makes every one of its columns NOT NULL,
	// the same as an inline one (RFC Section 7.2).
	for _, cst := range tbl.Constraints {
		if !(cst.Type == "NOT NULL" && len(cst.Columns) == 1) && cst.Type != "PRIMARY KEY" {
			continue
		}
		for _, name := range cst.Columns {
			for _, col := range tbl.Columns {
				if col.Name == name {
					col.NotNull = true
				}
			}
		}
	}

	return tbl, nil
}

func (b *Builder) buildForeignTable(cs *pg_query.CreateForeignTableStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	tbl, err := b.buildTable(cs.BaseStmt, block, pos, false, true)
	if err != nil {
		return nil, err
	}
	t := tbl.(*Table)
	t.Foreign = true
	if cs.Servername != "" {
		t.ForeignServer = &cs.Servername
	}
	if len(cs.Options) > 0 {
		t.ForeignOptions = buildOrderedOptions(cs.Options)
	}
	return t, nil
}

// buildColumn returns the Column and any table-level constraints promoted from
// inline column syntax (PRIMARY KEY, UNIQUE, REFERENCES).
func (b *Builder) buildColumn(cd *pg_query.ColumnDef, pos pipeline.SourcePos) (*Column, []*Constraint, error) {
	col := &Column{
		Name:   cd.Colname,
		SrcPos: pos,
	}
	if cd.TypeName != nil {
		col.Type = typeNameToRef(cd.TypeName)
		if marker := serialMarkerFromTypeName(cd.TypeName); marker != nil {
			col.Serial = marker
			col.NotNull = true // SERIAL implies NOT NULL in PG, independent of PRIMARY KEY
		}
	}
	// CollClause (RFC Section 5.2's composite attribute COLLATE) is real PG grammar
	// on ColumnDef itself — no DPG-specific parsing needed. Harmless to
	// extract unconditionally even for table columns (DPG has no
	// table-column COLLATE feature): diffColumns never reads Collation,
	// only diffCompositeAttrs does.
	if cd.CollClause != nil {
		col.Collation = qualifiedNameText(cd.CollClause.Collname)
	}

	var promoted []*Constraint
	// lastPromoted tracks the most recently promoted table-level
	// Constraint, for the trailing-attribute nodes handled by the
	// CONSTR_ATTR_* case below to attach themselves to.
	var lastPromoted *Constraint

	for _, cn := range cd.Constraints {
		cst := cn.GetConstraint()
		if cst == nil {
			continue
		}
		switch cst.Contype {
		// PostgreSQL's own grammar parses a trailing DEFERRABLE/NOT
		// DEFERRABLE/INITIALLY DEFERRED/INITIALLY IMMEDIATE/ENFORCED/NOT
		// ENFORCED clause after an inline col-constraint (e.g. "REFERENCES
		// t (id) DEFERRABLE INITIALLY DEFERRED", "CHECK (...) NOT
		// ENFORCED") as its OWN separate Constraint node with one of these
		// six CONSTR_ATTR_* contypes — confirmed live — not as fields on
		// the real constraint node it modifies (real PostgreSQL's own
		// semantic analysis, transformConstraintAttrs, merges them
		// server-side; pg_query only parses, so this codebase must do the
		// same merge itself). Found via a NOT ENFORCED test unexpectedly
		// failing: cst.IsEnforced on the CHECK node itself was always true
		// regardless of a trailing NOT ENFORCED clause, because the real
		// value lives on this separate node instead. The identical
		// oversight already existed for inline FOREIGN KEY ... DEFERRABLE/
		// INITIALLY DEFERRED (confirmed live the same way) — cst.Deferrable/
		// cst.Initdeferred on the FOREIGN KEY node were always false for an
		// inline declaration, a real pre-existing bug this fixes as one
		// mechanism rather than two.
		case pg_query.ConstrType_CONSTR_ATTR_DEFERRABLE:
			if lastPromoted != nil {
				lastPromoted.Deferrable = true
			}
		case pg_query.ConstrType_CONSTR_ATTR_NOT_DEFERRABLE:
			if lastPromoted != nil {
				lastPromoted.Deferrable = false
				lastPromoted.InitiallyDeferred = false
			}
		case pg_query.ConstrType_CONSTR_ATTR_DEFERRED:
			if lastPromoted != nil {
				lastPromoted.InitiallyDeferred = true
			}
		case pg_query.ConstrType_CONSTR_ATTR_IMMEDIATE:
			if lastPromoted != nil {
				lastPromoted.InitiallyDeferred = false
			}
		case pg_query.ConstrType_CONSTR_ATTR_ENFORCED:
			if lastPromoted != nil {
				lastPromoted.NotEnforced = false
			}
		case pg_query.ConstrType_CONSTR_ATTR_NOT_ENFORCED:
			if lastPromoted != nil {
				lastPromoted.NotEnforced = true
			}

		case pg_query.ConstrType_CONSTR_NOTNULL:
			col.NotNull = true
			// A bare "NOT NULL" (no CONSTRAINT keyword, no NO INHERIT) stays
			// the plain Column.NotNull bool — the overwhelmingly common case,
			// and the pre-existing representation this codebase has always
			// used. Only promote to a table-level Constraint (auto-named
			// like an unnamed inline PRIMARY KEY/UNIQUE/CHECK already is,
			// when the user gave no name) once there's something the bool
			// alone can't carry: an explicit name, or PostgreSQL 18+'s NO
			// INHERIT (RFC Section 7.2/7.3) — matches real PostgreSQL
			// itself, which catalogues even a bare inline NOT NULL as a
			// real named pg_constraint row (confirmed live), just one this
			// codebase doesn't need to track individually unless the user
			// wrote something beyond the ordinary case.
			if cst.Conname != "" || cst.IsNoInherit {
				tc := &Constraint{
					Name:      cst.Conname,
					Type:      "NOT NULL",
					Columns:   []string{cd.Colname},
					NoInherit: cst.IsNoInherit,
					Expr:      "NOT NULL " + quoteIdent(cd.Colname),
					Pos:       pos,
				}
				if cst.IsNoInherit {
					tc.Expr += " NO INHERIT"
				}
				promoted = append(promoted, tc)
				lastPromoted = tc
			}

		case pg_query.ConstrType_CONSTR_DEFAULT:
			if cst.RawExpr != nil {
				raw := nodeToText(cst.RawExpr)
				col.Default = &raw
			}

		case pg_query.ConstrType_CONSTR_GENERATED:
			if cst.RawExpr != nil {
				expr := nodeToText(cst.RawExpr)
				// GeneratedKind is "s" (STORED) or "v" (VIRTUAL, PG18+) —
				// real PostgreSQL always sets one of the two for this
				// constraint type, but default to STORED (true) on an
				// empty/unrecognized value rather than silently building a
				// half-formed Generated, matching this codebase's general
				// "unknown falls back to the pre-existing default" pattern.
				col.Generated = &Generated{Expr: expr, Stored: cst.GeneratedKind != "v"}
			}

		case pg_query.ConstrType_CONSTR_IDENTITY:
			id := &Identity{Always: cst.GeneratedWhen == "a"}
			// cst.Options is the identical []*Node DefElem shape
			// CreateSeqStmt.Options uses (real PostgreSQL's identity-opts
			// grammar reuses SeqOptList verbatim) — same parsing as
			// buildSequence's loop below, minus "as"/"owned_by" which
			// PostgreSQL rejects for identity columns. Previously this whole
			// list was discarded, silently downgrading any declared
			// START WITH/INCREMENT BY/MINVALUE/MAXVALUE/CACHE/CYCLE option to
			// PostgreSQL's bare defaults (RFC audit finding: real data loss,
			// not a parse error).
			for _, opt := range cst.Options {
				de := opt.GetDefElem()
				if de == nil {
					continue
				}
				v := seqOptionInt(de)
				switch de.Defname {
				case "increment":
					id.IncrementBy = v
				case "start":
					id.StartValue = v
				case "minvalue":
					if de.Arg == nil {
						id.NoMinValue = true
					} else {
						id.MinValue = v
					}
				case "maxvalue":
					if de.Arg == nil {
						id.NoMaxValue = true
					} else {
						id.MaxValue = v
					}
				case "cache":
					id.Cache = v
				case "cycle":
					if de.Arg != nil {
						if bl := de.Arg.GetBoolean(); bl != nil {
							cyc := bl.GetBoolval()
							id.Cycle = &cyc
						}
					}
				}
			}
			col.Identity = id
			col.NotNull = true // identity columns are always implicitly NOT NULL in PG

		case pg_query.ConstrType_CONSTR_PRIMARY:
			col.NotNull = true // PRIMARY KEY implies NOT NULL in PostgreSQL
			// Inline PRIMARY KEY — promote to a table-level constraint.
			tc := &Constraint{
				Name:              cst.Conname,
				Type:              "PRIMARY KEY",
				Columns:           []string{cd.Colname},
				Deferrable:        cst.Deferrable,
				InitiallyDeferred: cst.Initdeferred,
				Pos:               pos,
			}
			tc.Expr = "PRIMARY KEY (" + quoteIdent(cd.Colname) + ")"
			promoted = append(promoted, tc)
			lastPromoted = tc

		case pg_query.ConstrType_CONSTR_UNIQUE:
			// Inline UNIQUE — promote to a table-level constraint.
			tc := &Constraint{
				Name:              cst.Conname,
				Type:              "UNIQUE",
				Columns:           []string{cd.Colname},
				Deferrable:        cst.Deferrable,
				InitiallyDeferred: cst.Initdeferred,
				Pos:               pos,
			}
			nd := ""
			if cst.NullsNotDistinct {
				nd = "NULLS NOT DISTINCT "
			}
			tc.Expr = "UNIQUE " + nd + "(" + quoteIdent(cd.Colname) + ")"
			promoted = append(promoted, tc)
			lastPromoted = tc

		case pg_query.ConstrType_CONSTR_CHECK:
			// Inline CHECK — promote to a table-level constraint.
			// Columns is set to [colname] so createTable can inline it back
			// (a syntactic-position marker, deliberately independent of
			// CheckColumn below — see the Columns/CheckColumn doc comments).
			if cst.RawExpr != nil {
				expr := nodeToText(cst.RawExpr)
				tc := &Constraint{
					Name:        cst.Conname,
					Type:        "CHECK",
					Columns:     []string{cd.Colname},
					CheckColumn: checkExprSingleColumn(cst.RawExpr),
					Expr:        "CHECK (" + expr + ")",
					NotEnforced: !cst.IsEnforced,
					Pos:         pos,
				}
				promoted = append(promoted, tc)
				lastPromoted = tc
			}

		case pg_query.ConstrType_CONSTR_FOREIGN:
			// Inline REFERENCES — promote to a table-level FK constraint.
			refCols := nodeListToNames(cst.PkAttrs)
			var fkBuf strings.Builder
			fkBuf.WriteString("FOREIGN KEY (")
			fkBuf.WriteString(quoteIdent(cd.Colname))
			fkBuf.WriteString(") REFERENCES ")
			if cst.Pktable != nil {
				if cst.Pktable.Schemaname != "" {
					fkBuf.WriteString(quoteIdent(cst.Pktable.Schemaname))
					fkBuf.WriteByte('.')
				}
				fkBuf.WriteString(quoteIdent(cst.Pktable.Relname))
			}
			if len(refCols) > 0 {
				fkBuf.WriteString(" (")
				fkBuf.WriteString(strings.Join(quoteIdents(refCols), ", "))
				fkBuf.WriteByte(')')
			}
			if action := fkAction(cst.FkUpdAction); action != "" {
				fkBuf.WriteString(" ON UPDATE ")
				fkBuf.WriteString(action)
			}
			if action := fkAction(cst.FkDelAction); action != "" {
				fkBuf.WriteString(" ON DELETE ")
				fkBuf.WriteString(action)
				fkBuf.WriteString(fkDeleteSetColumns(cst.FkDelSetCols))
			}
			if cst.Deferrable {
				fkBuf.WriteString(" DEFERRABLE")
				if cst.Initdeferred {
					fkBuf.WriteString(" INITIALLY DEFERRED")
				}
			}
			tc := &Constraint{
				Name:              cst.Conname,
				Type:              "FOREIGN KEY",
				Columns:           []string{cd.Colname},
				Deferrable:        cst.Deferrable,
				InitiallyDeferred: cst.Initdeferred,
				NotEnforced:       !cst.IsEnforced,
				Expr:              fkBuf.String(),
				RefColumns:        refCols,
				DeleteSetColumns:  nodeListToNames(cst.FkDelSetCols),
				Pos:               pos,
			}
			if cst.Pktable != nil {
				tc.RefSchema = cst.Pktable.Schemaname
				tc.RefTable = cst.Pktable.Relname
			}
			promoted = append(promoted, tc)
			lastPromoted = tc
		}
	}

	// A FOREIGN KEY's Expr text is built once, inline, from cst.Deferrable/
	// cst.Initdeferred at the moment its own node is processed above — but
	// a trailing CONSTR_ATTR_DEFERRABLE/DEFERRED node (see the case above)
	// is only discovered AFTER that text is already finalized, so a
	// genuinely deferrable inline FK would otherwise build with correct
	// Constraint.Deferrable/InitiallyDeferred fields (used for diffing) but
	// an Expr silently missing the DEFERRABLE clause (used for the actual
	// CREATE TABLE SQL) — a real, previously-silent rendering bug this
	// closes as part of the same fix.
	for _, tc := range promoted {
		if tc.Type == "FOREIGN KEY" && tc.Deferrable && !strings.Contains(tc.Expr, "DEFERRABLE") {
			tc.Expr += " DEFERRABLE"
			if tc.InitiallyDeferred {
				tc.Expr += " INITIALLY DEFERRED"
			}
		}
	}

	return col, promoted, nil
}

// classifyConstraintExprType returns the Constraint.Type a block-declared
// constraint's raw Expr text implies, matching the exact Type strings
// buildConstraint's structured pg_query path already produces. Expr always
// starts with the constraint keyword (blockparser strips NOT VALID/comment
// suffixes before this ever runs) — no ambiguity between e.g. "CHECK" and
// "CHECK ..." vs the "n:" key format, since this only ever sees the
// keyword-first raw definition text, the same shape pg_get_constraintdef
// (introspection) and buildConstraint's own Expr construction both produce.
func classifyConstraintExprType(expr string) string {
	upper := strings.ToUpper(expr)
	switch {
	case strings.HasPrefix(upper, "PRIMARY KEY"):
		return "PRIMARY KEY"
	case strings.HasPrefix(upper, "UNIQUE"):
		return "UNIQUE"
	case strings.HasPrefix(upper, "CHECK"):
		return "CHECK"
	case strings.HasPrefix(upper, "FOREIGN KEY"):
		return "FOREIGN KEY"
	case strings.HasPrefix(upper, "EXCLUDE"):
		return "EXCLUDE"
	case strings.HasPrefix(upper, "NOT NULL"):
		return "NOT NULL"
	default:
		return "UNKNOWN"
	}
}

// parseNotNullExpr recovers the column name and NO INHERIT flag from a
// table-level NOT NULL constraint's raw Expr text ("NOT NULL colname" or
// "NOT NULL colname NO INHERIT") — the inverse of buildConstraint's own
// Expr construction for the same constraint kind (see its CONSTR_NOTNULL
// case), needed here because a block-declared constraint only ever reaches
// this codebase as already-rendered text, never structured pg_query data.
func parseNotNullExpr(expr string) (col string, noInherit bool, ok bool) {
	if len(expr) < len("NOT NULL ") || !strings.EqualFold(expr[:len("NOT NULL ")], "NOT NULL ") {
		return "", false, false
	}
	rest := expr[len("NOT NULL "):]
	if len(rest) >= len(" NO INHERIT") && strings.EqualFold(rest[len(rest)-len(" NO INHERIT"):], " NO INHERIT") {
		rest = rest[:len(rest)-len(" NO INHERIT")]
		noInherit = true
	}
	rest = strings.Trim(strings.TrimSpace(rest), `"`)
	if rest == "" {
		return "", false, false
	}
	return rest, noInherit, true
}

func buildConstraint(c *pg_query.Constraint, pos pipeline.SourcePos) *Constraint {
	cst := &Constraint{
		Name:              c.Conname,
		NotValid:          c.SkipValidation,
		Deferrable:        c.Deferrable,
		InitiallyDeferred: c.Initdeferred,
		Pos:               pos,
	}
	switch c.Contype {
	case pg_query.ConstrType_CONSTR_PRIMARY:
		cst.Type = "PRIMARY KEY"
		cols := nodeListToNames(c.Keys)
		cst.Columns = cols
		cst.WithoutOverlaps = c.WithoutOverlaps
		if len(cols) > 0 {
			cst.Expr = "PRIMARY KEY (" + colListWithTrailingSuffix(cols, c.WithoutOverlaps, "WITHOUT OVERLAPS") + ")"
		}

	case pg_query.ConstrType_CONSTR_UNIQUE:
		cst.Type = "UNIQUE"
		cols := nodeListToNames(c.Keys)
		cst.Columns = cols
		cst.WithoutOverlaps = c.WithoutOverlaps
		if len(cols) > 0 {
			nd := ""
			if c.NullsNotDistinct {
				nd = "NULLS NOT DISTINCT "
			}
			cst.Expr = "UNIQUE " + nd + "(" + colListWithTrailingSuffix(cols, c.WithoutOverlaps, "WITHOUT OVERLAPS") + ")"
		}

	case pg_query.ConstrType_CONSTR_CHECK:
		cst.Type = "CHECK"
		cst.NotEnforced = !c.IsEnforced
		if c.RawExpr != nil {
			expr := nodeToText(c.RawExpr)
			cst.Expr = "CHECK (" + expr + ")"
			cst.CheckColumn = checkExprSingleColumn(c.RawExpr)
		}

	case pg_query.ConstrType_CONSTR_NOTNULL:
		// Table-level named NOT NULL constraint (PostgreSQL 18+, RFC
		// Section 7.3): "CONSTRAINT name NOT NULL col_name [NO INHERIT]
		// [NOT VALID]" — grammatically always reaches here via the
		// "CONSTRAINT" wrapper (table-constraint), unlike buildColumn's
		// inline NOT NULL case above, which only promotes when the inline
		// form itself carries a name or NO INHERIT. NotValid is already
		// captured generically above from c.SkipValidation.
		cst.Type = "NOT NULL"
		cols := nodeListToNames(c.Keys)
		cst.Columns = cols
		cst.NoInherit = c.IsNoInherit
		if len(cols) > 0 {
			cst.Expr = "NOT NULL " + quoteIdent(cols[0])
			if c.IsNoInherit {
				cst.Expr += " NO INHERIT"
			}
		}

	case pg_query.ConstrType_CONSTR_FOREIGN:
		cst.Type = "FOREIGN KEY"
		cst.NotEnforced = !c.IsEnforced
		cst.FkWithPeriod = c.FkWithPeriod
		cst.PkWithPeriod = c.PkWithPeriod
		localCols := nodeListToNames(c.FkAttrs)
		refCols := nodeListToNames(c.PkAttrs)
		cst.Columns = localCols
		var b strings.Builder
		b.WriteString("FOREIGN KEY (")
		b.WriteString(colListWithTrailingPrefix(localCols, c.FkWithPeriod, "PERIOD"))
		b.WriteString(") REFERENCES ")
		if c.Pktable != nil {
			if c.Pktable.Schemaname != "" {
				b.WriteString(quoteIdent(c.Pktable.Schemaname))
				b.WriteByte('.')
			}
			b.WriteString(quoteIdent(c.Pktable.Relname))
		}
		if len(refCols) > 0 {
			b.WriteString(" (")
			b.WriteString(colListWithTrailingPrefix(refCols, c.PkWithPeriod, "PERIOD"))
			b.WriteByte(')')
		}
		if action := fkAction(c.FkUpdAction); action != "" {
			b.WriteString(" ON UPDATE ")
			b.WriteString(action)
		}
		if action := fkAction(c.FkDelAction); action != "" {
			b.WriteString(" ON DELETE ")
			b.WriteString(action)
			b.WriteString(fkDeleteSetColumns(c.FkDelSetCols))
		}
		cst.DeleteSetColumns = nodeListToNames(c.FkDelSetCols)
		if c.Deferrable {
			b.WriteString(" DEFERRABLE")
			if c.Initdeferred {
				b.WriteString(" INITIALLY DEFERRED")
			}
		}
		cst.Expr = b.String()
		cst.RefColumns = refCols
		if c.Pktable != nil {
			cst.RefSchema = c.Pktable.Schemaname
			cst.RefTable = c.Pktable.Relname
		}

	case pg_query.ConstrType_CONSTR_EXCLUSION:
		cst.Type = "EXCLUDE"
		spec := &ExcludeSpec{AccessMethod: c.AccessMethod}
		if c.WhereClause != nil {
			spec.Where = nodeToText(c.WhereClause)
		}
		var cols []string
		for _, ex := range c.Exclusions {
			pair := ex.GetList()
			if pair == nil || len(pair.Items) != 2 {
				continue
			}
			el := ExcludeElement{}
			if ie := pair.Items[0].GetIndexElem(); ie != nil {
				switch {
				case ie.Name != "":
					el.Column = ie.Name
					cols = append(cols, ie.Name)
				case ie.Expr != nil:
					el.Expr = nodeToText(ie.Expr)
					el.PredictedName, _ = FigureColname(ie.Expr)
				}
				el.Collation = qualifiedNameText(ie.Collation)
				el.OpClass = qualifiedNameText(ie.Opclass)
				switch ie.Ordering {
				case pg_query.SortByDir_SORTBY_ASC:
					el.SortOrder = "ASC"
				case pg_query.SortByDir_SORTBY_DESC:
					el.SortOrder = "DESC"
				}
				switch ie.NullsOrdering {
				case pg_query.SortByNulls_SORTBY_NULLS_FIRST:
					el.Nulls = "FIRST"
				case pg_query.SortByNulls_SORTBY_NULLS_LAST:
					el.Nulls = "LAST"
				}
			}
			if opList := pair.Items[1].GetList(); opList != nil {
				el.Operator = operatorNameText(opList.Items)
			}
			spec.Elements = append(spec.Elements, el)
		}
		cst.Columns = cols
		cst.Exclude = spec
		cst.Expr = renderExclude(spec)
	default:
		cst.Type = "UNKNOWN"
	}
	return cst
}

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// qualifiedNameText joins a (possibly schema-qualified) name-part list —
// used for an EXCLUDE element's COLLATE target and operator class, both of
// which pg_query represents the same way as an operator name — into "" (no
// parts), "name", or "schema.name".
func qualifiedNameText(nodes []*pg_query.Node) string {
	return strings.Join(nodeListToNames(nodes), ".")
}

// operatorNameText renders an EXCLUDE element's WITH operator name. PG's
// grammar allows a schema-qualified operator (OPERATOR(schema.=)); the
// pg_catalog schema is stripped since it's the implicit default for a
// built-in operator, mirroring PGCatalogName's treatment of built-in types.
func operatorNameText(nodes []*pg_query.Node) string {
	parts := nodeListToNames(nodes)
	if len(parts) == 2 && parts[0] == "pg_catalog" {
		return parts[1]
	}
	return strings.Join(parts, ".")
}

// lastNamePart returns the final component of a (possibly schema-qualified)
// name-part list — used for a FuncCall's Funcname and a TypeCast's target
// type name, mirroring PostgreSQL's own get_func_name/format_type-style
// behavior, which never includes the schema (confirmed live:
// myschema.myfunc(a) predicts the same bare "myfunc" name PG itself uses).
func lastNamePart(nodes []*pg_query.Node) string {
	names := nodeListToNames(nodes)
	if len(names) == 0 {
		return ""
	}
	return names[len(names)-1]
}

// FigureColname ports PostgreSQL's FigureColnameInternal (parser/
// parse_target.c) — called via FigureIndexColname from parse_utilcmd.c's
// transformIndexStmt for an EXCLUDE element's PredictedName, and via
// internal/diff's view-rename detection (RFC Section 8.1's "compares the columns
// produced by the view query by name and ordinal position") for an
// unaliased SELECT target's implicit output column name — BEFORE
// transformExpr runs — confirmed via source that this genuinely operates on
// the raw, untransformed parse tree (no catalog/OID resolution), and
// confirmed live across every case below (NULLIF, nested casts, a cast
// wrapping an operator, a parenthesized bare column, COALESCE, CASE, an
// array subscript, and COLLATE) that the real output matches exactly.
//
// Returns the predicted name and its strength: 0 = no information (caller
// should not predict at all), 1 = a "weak" name (a cast's own target type,
// or the literal "case" when CASE's ELSE clause gives nothing better —
// used only when nothing stronger is available), 2 = a "strong" name (a
// column, function call, COALESCE, or a CASE whose ELSE clause is itself
// strong). The strength only matters for the two nodes that fall back to
// a name of their own when their argument is weak — TypeCast (falls back
// to its own target type: confirmed live "lower(a)::text" predicts
// "lower", but "(a + b)::text" predicts "text") and CaseExpr (falls back
// to the literal "case": PG only ever consults the ELSE clause for this,
// deliberately ignoring every WHEN branch, confirmed live).
//
// Deliberately narrower than FigureColnameInternal's full node-type switch
// (which also covers SubLink, RowExpr, MinMaxExpr, GroupingFunc, etc. —
// irrelevant here since transformExpr's EXPR_KIND_INDEX_EXPRESSION already
// rejects subqueries, and the others are vanishingly rare inside an
// EXCLUDE element): ColumnRef, FuncCall, TypeCast, A_Expr's NULLIF form,
// A_Indirection (field access / array subscript), CollateClause, CaseExpr,
// and CoalesceExpr cover every shape confirmed live.
func FigureColname(node *pg_query.Node) (name string, strength int) {
	if node == nil {
		return "", 0
	}
	switch n := node.Node.(type) {
	case *pg_query.Node_ColumnRef:
		if last := lastNamePart(n.ColumnRef.Fields); last != "" {
			return last, 2
		}
		return "", 0
	case *pg_query.Node_FuncCall:
		return lastNamePart(n.FuncCall.Funcname), 2
	case *pg_query.Node_TypeCast:
		innerName, innerStrength := FigureColname(n.TypeCast.Arg)
		if innerStrength > 1 {
			return innerName, innerStrength
		}
		if n.TypeCast.TypeName != nil {
			return lastNamePart(n.TypeCast.TypeName.Names), 1
		}
		return innerName, innerStrength
	case *pg_query.Node_AExpr:
		if n.AExpr.Kind == pg_query.A_Expr_Kind_AEXPR_NULLIF {
			return "nullif", 2
		}
		return "", 0
	case *pg_query.Node_AIndirection:
		// A field-access suffix (".field", ignoring "*" and array subscripts
		// — nodeListToNames/lastNamePart already skip anything that isn't a
		// plain name, i.e. AIndices subscript nodes) wins outright; a
		// subscript-only indirection (e.g. "a[1]") has none, so this falls
		// through to the base expression's own name, unchanged strength.
		if last := lastNamePart(n.AIndirection.Indirection); last != "" {
			return last, 2
		}
		return FigureColname(n.AIndirection.Arg)
	case *pg_query.Node_CollateClause:
		return FigureColname(n.CollateClause.Arg)
	case *pg_query.Node_CaseExpr:
		// Only the ELSE clause (Defresult) is consulted — the WHEN branches
		// are deliberately ignored, matching PG's own rule — falling back to
		// the literal "case" (weak) when Defresult is absent or itself weak.
		innerName, innerStrength := FigureColname(n.CaseExpr.Defresult)
		if innerStrength > 1 {
			return innerName, innerStrength
		}
		return "case", 1
	case *pg_query.Node_CoalesceExpr:
		return "coalesce", 2
	default:
		return "", 0
	}
}

// renderExclude renders an ExcludeSpec as PostgreSQL's own EXCLUDE syntax:
// EXCLUDE [USING access_method] (element [COLLATE collation] [opclass]
// [ASC|DESC] [NULLS FIRST|LAST] WITH operator [, ...]) [WHERE (predicate)].
func renderExclude(spec *ExcludeSpec) string {
	var b strings.Builder
	b.WriteString("EXCLUDE")
	if spec.AccessMethod != "" {
		b.WriteString(" USING ")
		b.WriteString(spec.AccessMethod)
	}
	b.WriteString(" (")
	for i, el := range spec.Elements {
		if i > 0 {
			b.WriteString(", ")
		}
		if el.Column != "" {
			b.WriteString(quoteIdent(el.Column))
		} else {
			b.WriteString("(")
			b.WriteString(el.Expr)
			b.WriteString(")")
		}
		if el.Collation != "" {
			b.WriteString(" COLLATE ")
			b.WriteString(el.Collation)
		}
		if el.OpClass != "" {
			b.WriteString(" ")
			b.WriteString(el.OpClass)
		}
		if el.SortOrder != "" {
			b.WriteString(" ")
			b.WriteString(el.SortOrder)
		}
		if el.Nulls != "" {
			b.WriteString(" NULLS ")
			b.WriteString(el.Nulls)
		}
		b.WriteString(" WITH ")
		b.WriteString(el.Operator)
	}
	b.WriteString(")")
	if spec.Where != "" {
		b.WriteString(" WHERE (")
		b.WriteString(spec.Where)
		b.WriteString(")")
	}
	return b.String()
}

// nodeListToNames extracts string values from a pg_query Node list (Keys, FkAttrs, etc.).
func nodeListToNames(nodes []*pg_query.Node) []string {
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if sv := n.GetString_(); sv != nil {
			names = append(names, sv.Sval)
		}
	}
	return names
}

// quoteIdents returns a slice of double-quoted identifiers.
func quoteIdents(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}

// colListWithTrailingSuffix renders a comma-separated, quoted column list
// with a trailing keyword appended after the LAST column only when apply is
// true — PostgreSQL 18+'s "col_name WITHOUT OVERLAPS" temporal-key modifier
// (RFC Section 7.3), which the grammar only ever allows on a PRIMARY KEY/
// UNIQUE's final column.
func colListWithTrailingSuffix(cols []string, apply bool, suffix string) string {
	quoted := quoteIdents(cols)
	if apply && len(quoted) > 0 {
		quoted[len(quoted)-1] += " " + suffix
	}
	return strings.Join(quoted, ", ")
}

// colListWithTrailingPrefix is colListWithTrailingSuffix's mirror for
// PostgreSQL 18+'s "PERIOD col_name" temporal FOREIGN KEY modifier (RFC
// Section 7.3) — the keyword precedes the LAST column instead of following
// it.
func colListWithTrailingPrefix(cols []string, apply bool, prefix string) string {
	quoted := quoteIdents(cols)
	if apply && len(quoted) > 0 {
		quoted[len(quoted)-1] = prefix + " " + quoted[len(quoted)-1]
	}
	return strings.Join(quoted, ", ")
}

// fkAction converts a pg_query FK action char to its SQL keyword.
// Returns "" for NO ACTION (the PostgreSQL default) to keep DDL concise.
// fkDeleteSetColumns renders ON DELETE SET NULL/SET DEFAULT's optional
// column list (PostgreSQL 15+, RFC Section 7.2), with its leading space;
// "" when there is none.
func fkDeleteSetColumns(cols []*pg_query.Node) string {
	names := nodeListToNames(cols)
	if len(names) == 0 {
		return ""
	}
	return " (" + strings.Join(quoteIdents(names), ", ") + ")"
}

func fkAction(action string) string {
	switch action {
	case "a", "": // NO ACTION is the default; omit it
		return ""
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	}
	return ""
}

func buildPartitionSpec(ps *pg_query.PartitionSpec) *PartitionSpec {
	spec := &PartitionSpec{}
	switch ps.Strategy {
	case pg_query.PartitionStrategy_PARTITION_STRATEGY_RANGE:
		spec.Strategy = "RANGE"
	case pg_query.PartitionStrategy_PARTITION_STRATEGY_LIST:
		spec.Strategy = "LIST"
	case pg_query.PartitionStrategy_PARTITION_STRATEGY_HASH:
		spec.Strategy = "HASH"
	default:
		spec.Strategy = "RANGE"
	}
	for _, pe := range ps.PartParams {
		if pelem := pe.GetPartitionElem(); pelem != nil {
			if pelem.Name != "" {
				spec.Columns = append(spec.Columns, pelem.Name)
			}
		}
	}
	return spec
}

// buildOrderedOptions parses a DefElem OPTIONS (...)/WITH (...) list
// preserving source order — used for FOREIGN TABLE's OPTIONS clause and
// TABLE's WITH (...) storage params, where deterministic re-emission needs a
// stable order, the same reason Index.With is a slice, not a map.
func buildOrderedOptions(options []*pg_query.Node) []pipeline.StorageParam {
	var params []pipeline.StorageParam
	for _, opt := range options {
		de := opt.GetDefElem()
		if de == nil {
			continue
		}
		val := ""
		if de.Arg != nil {
			val = nodeToText(de.Arg)
		}
		params = append(params, pipeline.StorageParam{Key: de.Defname, Value: val})
	}
	return params
}

// buildAggregateOptions converts CREATE AGGREGATE's option list (SFUNC,
// STYPE, INITCOND, FINALFUNC, ...) into ordered StorageParams. Deliberately
// NOT reusing buildOrderedOptions/nodeToText: a function/type name value
// here (SFUNC, STYPE, FINALFUNC, ...) parses as a bare pg_query TypeName
// node — PG's grammar generically reuses TypeName for "any qualified name"
// in this position — which nodeToText has no case for and falls back to the
// literal string "<expr>" (confirmed via a direct pg_query.Parse probe: it
// only handles actual expression nodes via a SELECT-target deparse trick,
// and TypeName isn't a valid SELECT target). A literal value (INITCOND) is a
// bare pg_query String node, which nodeToText renders unquoted — correct for
// most other DefElem consumers, but wrong here since INITCOND must round-trip
// as a properly single-quoted SQL literal.
func buildAggregateOptions(definition []*pg_query.Node) []pipeline.StorageParam {
	var params []pipeline.StorageParam
	for _, opt := range definition {
		de := opt.GetDefElem()
		if de == nil {
			continue
		}
		if de.Arg == nil {
			// RFC audit item #29: FINALFUNC_EXTRA/MFINALFUNC_EXTRA/
			// HYPOTHETICAL are bare presence flags with no "= value" part
			// (confirmed via pg_query.Parse probe: their DefElem.Arg is
			// nil) — previously silently dropped here entirely, which both
			// hid a change to any of them from diffAggregate's structured
			// comparison (undetected drift, since real PostgreSQL's
			// ALTER AGGREGATE has no in-place path for them either) and
			// made dpg dump discard them from the regenerated declaration.
			params = append(params, pipeline.StorageParam{Key: de.Defname})
			continue
		}
		var val string
		switch {
		case de.Arg.GetTypeName() != nil:
			val = typeNameToRef(de.Arg.GetTypeName()).String()
		case de.Arg.GetString_() != nil:
			val = "'" + strings.ReplaceAll(de.Arg.GetString_().Sval, "'", "''") + "'"
		default:
			val = nodeToText(de.Arg)
		}
		params = append(params, pipeline.StorageParam{Key: de.Defname, Value: val})
	}
	return params
}

// renamedFromSchema returns a pointer to id.Schema when a RENAMED FROM
// directive is schema-qualified (a rename combined with a cross-schema move),
// or nil for an unqualified name — mirrors RenamedFrom's own *string-or-nil
// convention so the two fields stay in sync at every call site.
func renamedFromSchema(id *pipeline.Identifier) *string {
	if id == nil || id.Schema == "" {
		return nil
	}
	s := id.Schema
	return &s
}

func mergeTableBlock(tbl *Table, block pipeline.BlockAST) error {
	if block.MigrateRemove != nil {
		return pipeline.Errorf(block.MigrateRemove.Pos,
			"MIGRATE REMOVE is not supported for TABLE objects; it is only valid for TYPE (ENUM) value removal")
	}
	if block.Comment != nil {
		tbl.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		tbl.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		tbl.RenamedFrom = &block.RenamedFrom.Name
		tbl.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	tbl.Protected = block.Protected
	if block.Deprecated != nil {
		tbl.Deprecated = &block.Deprecated.Value
	}
	tbl.DropCascade = block.DropCascade
	tbl.RLSEnabled = block.EnableRLS
	tbl.RLSForced = block.ForceRLS
	if block.ReplicaIdentity != nil {
		tbl.ReplicaIdentity = ReplicaIdentity{Mode: block.ReplicaIdentity.Mode, IndexName: block.ReplicaIdentity.IndexName}
	}
	if block.ClusterOn != nil {
		name := block.ClusterOn.Name
		tbl.ClusterOn = &name
	}
	if block.DetachedFrom != nil {
		tbl.DetachedFrom = &DetachedFrom{
			ParentTable:  block.DetachedFrom.Table.String(),
			Concurrently: block.DetachedFrom.Concurrently,
		}
	}
	tbl.NameMaps = block.NameMaps

	// Indexes
	for _, idx := range block.Indices {
		tbl.Indexes = append(tbl.Indexes, blockIndexToIR(idx))
	}

	// Policies
	for _, pol := range block.Policies {
		tbl.Policies = append(tbl.Policies, blockPolicyToIR(pol))
	}

	// Triggers
	for _, tr := range block.Triggers {
		tbl.Triggers = append(tbl.Triggers, blockTriggerToIR(tr))
	}

	// Grants
	for _, g := range block.Grants {
		tbl.Grants = append(tbl.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		tbl.Revocations = append(tbl.Revocations, blockRevocationToIR(r))
	}
	tbl.SecurityLabels = block.SecurityLabels

	// Columns: per RFC Section 7.2, `COLUMN name { }` references an *existing* column
	// in the DDL. A name that doesn't match is almost always a typo (e.g.
	// "locality_ids" vs. "locality_id"); silently inventing a phantom column
	// produces broken SQL downstream (empty type, mismatched FKs), so reject
	// it at build time with a list of legal names.
	colMap := make(map[string]*Column, len(tbl.Columns))
	for _, c := range tbl.Columns {
		colMap[c.Name] = c
	}
	for _, cb := range block.Columns {
		col, ok := colMap[cb.Name.Name]
		if !ok {
			return pipeline.ErrorfCode(cb.Pos, "DPG-E018",
				"COLUMN %q is not declared in TABLE %s; the COLUMN block must reference a column listed in the table's ( ) section%s",
				cb.Name.Name, qualName(tbl.Schema, tbl.Name), suggestColumns(cb.Name.Name, tbl.Columns))
		}
		if cb.Comment != nil {
			col.Comment = &cb.Comment.Value
		}
		if cb.Statistics != nil {
			col.Statistics = cb.Statistics
		}
		if cb.Compression != nil {
			col.Compression = &cb.Compression.Name
		}
		if cb.Storage != nil {
			col.Storage = &cb.Storage.Name
		}
		if cb.Deprecated != nil {
			col.Deprecated = &cb.Deprecated.Value
		}
		if cb.RenamedFrom != nil {
			col.RenamedFrom = &cb.RenamedFrom.Name
		}
		if cb.Using != nil {
			col.Using = &cb.Using.Text
		}
		for _, g := range cb.Grants {
			col.Grants = append(col.Grants, blockGrantToIR(g))
		}
		for _, rv := range cb.Revocations {
			col.Revocations = append(col.Revocations, blockRevocationToIR(rv))
		}
		col.SecurityLabels = cb.SecurityLabels
		col.NameMaps = append(col.NameMaps, cb.NameMaps...)
	}

	// Additional constraints from block. RFC Section 7.3's grammar requires
	// "CONSTRAINT" WSP identifier for every table-constraint, so a
	// block-declared constraint is always named — unlike buildConstraint's
	// pg_query-based path (structured Contype from the parse tree), there
	// is no structured type here at all, only Expr's raw text, so Type/
	// Columns/NoInherit are inferred from it below instead. Only Type and
	// (for NOT NULL specifically) Columns/NoInherit are inferred — Columns
	// for the other kinds and CheckColumn are deliberately left unset: both
	// exist solely to reconstruct PostgreSQL's own auto-generated name for
	// an UNNAMED constraint (predictName, internal/diff), which a
	// block-declared constraint — always named per the grammar above — can
	// never need.
	for _, cst := range block.Constraints {
		tbl.Constraints = append(tbl.Constraints, buildBlockConstraint(cst))
	}

	// Partitions
	if block.Partitions != nil {
		for _, p := range block.Partitions.Partitions {
			tbl.Partitions = append(tbl.Partitions, buildPartitionBound(p))
		}
	}
	return nil
}

// buildBlockConstraint converts a single { }-block-declared
// pipeline.ConstraintDef into an ir.Constraint — shared by a table's own top-
// level Constraints (RFC Section 7.3) and a partition's local ones (RFC
// Section 7.3's "DROP CONSTRAINT ... ONLY" gap, PostgreSQL 18+), which use
// the identical grammar. See the call site above for why Type/Columns/
// NoInherit are inferred from Expr's raw text rather than structured, and
// why Columns/CheckColumn are otherwise left unset.
func buildBlockConstraint(cst pipeline.ConstraintDef) *Constraint {
	newCst := &Constraint{
		Name:        cst.Name.Name,
		Type:        classifyConstraintExprType(cst.Expr.Text),
		Expr:        cst.Expr.Text,
		NotValid:    cst.NotValid,
		NotEnforced: cst.NotEnforced,
		RenamedFrom: cst.RenamedFrom,
		Pos:         cst.Pos,
	}
	if newCst.Type == "NOT NULL" {
		if col, noInherit, ok := parseNotNullExpr(cst.Expr.Text); ok {
			newCst.Columns = []string{col}
			newCst.NoInherit = noInherit
		}
	}
	if cst.Comment != nil {
		newCst.Comment = &cst.Comment.Value
	}
	return newCst
}

// buildPartitionBound converts a parsed pipeline.PartitionBound into an
// ir.Partition, recursing into any nested sub-partitioning (RFC Section 7.13).
func buildPartitionBound(p pipeline.PartitionBound) *Partition {
	part := &Partition{
		Name:        p.Name.Name,
		Bounds:      p.Bounds.Text,
		RenamedFrom: p.RenamedFrom,
		SrcPos:      p.Pos,
	}
	if p.AttachedFrom != nil {
		s := p.AttachedFrom.String()
		part.AttachedFrom = &s
	}
	if p.Foreign {
		part.Foreign = true
		if p.Server != nil {
			s := p.Server.String()
			part.ForeignServer = &s
		}
		part.ForeignOptions = p.Options
	}
	if p.SubStrategy != "" {
		part.PartitionBy = &PartitionSpec{
			Strategy: p.SubStrategy,
			Columns:  p.SubColumns,
		}
		for _, sp := range p.SubPartitions {
			part.Partitions = append(part.Partitions, buildPartitionBound(sp))
		}
	}
	for _, cst := range p.Constraints {
		part.Constraints = append(part.Constraints, buildBlockConstraint(cst))
	}
	return part
}

// suggestColumns formats a "; did you mean ..." or "; declared columns are ..."
// hint for COLUMN-block resolution errors. Returns "" when the table has no
// columns, so callers can append it unconditionally.
func suggestColumns(want string, cols []*Column) string {
	if len(cols) == 0 {
		return ""
	}
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if best, ok := nearestColumn(want, names); ok {
		return fmt.Sprintf("; did you mean %q?", best)
	}
	return "; declared columns: " + strings.Join(names, ", ")
}

// nearestColumn returns the column name within edit distance 2 of want, or
// false if none qualify. Edit distance 2 catches typos like a single dropped
// or doubled char ("locality_ids" → "locality_id") without matching unrelated
// names — which would be more confusing than helpful.
func nearestColumn(want string, names []string) (string, bool) {
	const maxDist = 2
	best, bestDist := "", maxDist+1
	for _, n := range names {
		d := levenshtein(want, n)
		if d < bestDist {
			best, bestDist = n, d
		}
	}
	if bestDist <= maxDist {
		return best, true
	}
	return "", false
}

// levenshtein returns the edit distance between a and b. Small dedicated
// implementation — pulling in a dependency for one suggestion message is
// disproportionate.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// ── View ──────────────────────────────────────────────────────────────────────

func (b *Builder) buildView(vs *pg_query.ViewStmt, block pipeline.BlockAST, pos pipeline.SourcePos, materialized, recursive bool) (pipeline.IRObject, error) {
	v := &View{
		Schema:       rangeVarSchema(vs.View),
		Name:         vs.View.Relname,
		Materialized: materialized,
		Recursive:    recursive,
		SrcPos:       pos,
	}
	// Deparse the view query as a full statement, not as a subexpression.
	if vs.Query != nil {
		pr := &pg_query.ParseResult{Stmts: []*pg_query.RawStmt{{Stmt: vs.Query}}}
		if sql, err := pg_query.Deparse(pr); err == nil {
			v.Query = sql
		} else {
			v.Query = nodeToText(vs.Query)
		}
	}
	if len(block.Indices) > 0 {
		return nil, pipeline.Errorf(block.Indices[0].Pos,
			"INDEXES is not supported for VIEW/RECURSIVE VIEW objects; it is only valid for MATERIALIZED VIEW (real PostgreSQL does not support indexes on a plain view)")
	}
	if block.Comment != nil {
		v.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		v.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		v.RenamedFrom = &block.RenamedFrom.Name
		v.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.Deprecated != nil {
		v.Deprecated = &block.Deprecated.Value
	}
	for _, g := range block.Grants {
		v.Grants = append(v.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		v.Revocations = append(v.Revocations, blockRevocationToIR(r))
	}
	v.SecurityLabels = block.SecurityLabels
	v.NameMaps = block.NameMaps
	return v, nil
}

// buildMaterializedView handles CREATE MATERIALIZED VIEW, which — unlike a
// plain CREATE VIEW — pg_query parses as CreateTableAsStmt (Objtype ==
// OBJECT_MATVIEW), not ViewStmt: PostgreSQL's own grammar implements
// MATERIALIZED VIEW as a variant of CREATE TABLE AS, confirmed via a direct
// pg_query.Parse probe. Before this, Build's switch had no case for
// CreateTableAsStmt at all, so every MATERIALIZED VIEW silently fell through
// to the generic default case (an empty, nameless OpaqueObject) — found
// live-testing a demo project: dpg plan reported "-- (no changes)" for a
// newly-added MATERIALIZED VIEW instead of a CREATE, because the malformed
// object's QualifiedName() was "", not the declared name. This is the
// analogous fix to buildView, adapted for CreateTableAsStmt's differently
// -shaped fields (Into.Rel instead of View, Into.SkipData instead of no
// equivalent — WITH NO DATA has no ViewStmt counterpart at all).
func (b *Builder) buildMaterializedView(cta *pg_query.CreateTableAsStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	v := &View{
		Materialized: true,
		WithNoData:   cta.Into.SkipData,
		SrcPos:       pos,
	}
	if cta.Into != nil && cta.Into.Rel != nil {
		v.Schema = rangeVarSchema(cta.Into.Rel)
		v.Name = cta.Into.Rel.Relname
	}
	if cta.Into != nil {
		if cta.Into.TableSpaceName != "" {
			ts := cta.Into.TableSpaceName
			v.Tablespace = &ts
		}
		if len(cta.Into.Options) > 0 {
			v.StorageParams = buildOrderedOptions(cta.Into.Options)
		}
	}
	// Deparse the query as a full statement, not as a subexpression — same
	// pattern as buildView.
	if cta.Query != nil {
		pr := &pg_query.ParseResult{Stmts: []*pg_query.RawStmt{{Stmt: cta.Query}}}
		if sql, err := pg_query.Deparse(pr); err == nil {
			v.Query = sql
		} else {
			v.Query = nodeToText(cta.Query)
		}
	}
	if block.Comment != nil {
		v.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		v.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		v.RenamedFrom = &block.RenamedFrom.Name
		v.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.Deprecated != nil {
		v.Deprecated = &block.Deprecated.Value
	}
	for _, g := range block.Grants {
		v.Grants = append(v.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		v.Revocations = append(v.Revocations, blockRevocationToIR(r))
	}
	for _, idx := range block.Indices {
		v.Indexes = append(v.Indexes, blockIndexToIR(idx))
	}
	v.SecurityLabels = block.SecurityLabels
	v.NameMaps = block.NameMaps
	return v, nil
}

// ── Function / Procedure ──────────────────────────────────────────────────────

func (b *Builder) buildFunction(cfs *pg_query.CreateFunctionStmt, pg pipeline.PGParseResult, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	if cfs.IsProcedure {
		return b.buildProcedure(cfs, pg, block, pos)
	}

	fn := &Function{SrcPos: pos}
	if len(cfs.Funcname) > 0 {
		fn.Schema, fn.Name = extractFuncName(cfs.Funcname)
	}
	for _, p := range cfs.Parameters {
		if fp := p.GetFunctionParameter(); fp != nil {
			fn.Args = append(fn.Args, buildFuncArg(fp))
		}
	}
	if cfs.ReturnType != nil {
		fn.ReturnType = typeNameToRef(cfs.ReturnType)
	} else {
		fn.ReturnType = impliedReturnType(fn.Args)
	}
	fn.Attrs = extractFuncAttrs(cfs, pg.SourceSQL)

	fn.BodyHash = HashFunctionBody(fn.Attrs.Language, fn.Attrs.Body, pg.SourceSQL)

	if block.Comment != nil {
		fn.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		fn.RenamedFrom = &block.RenamedFrom.Name
		fn.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.Owner != nil {
		fn.Owner = &block.Owner.Name
	}
	if block.Deprecated != nil {
		fn.Deprecated = &block.Deprecated.Value
	}
	for _, g := range block.Grants {
		fn.Grants = append(fn.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		fn.Revocations = append(fn.Revocations, blockRevocationToIR(r))
	}
	fn.SecurityLabels = block.SecurityLabels
	fn.NameMaps = block.NameMaps
	fn.DependsOnExtensions = block.DependsOnExtensions
	return fn, nil
}

func (b *Builder) buildProcedure(cfs *pg_query.CreateFunctionStmt, pg pipeline.PGParseResult, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	proc := &Procedure{SrcPos: pos}
	if len(cfs.Funcname) > 0 {
		proc.Schema, proc.Name = extractFuncName(cfs.Funcname)
	}
	for _, p := range cfs.Parameters {
		if fp := p.GetFunctionParameter(); fp != nil {
			proc.Args = append(proc.Args, buildFuncArg(fp))
		}
	}
	proc.Attrs = extractFuncAttrs(cfs, pg.SourceSQL)
	proc.BodyHash = HashFunctionBody(proc.Attrs.Language, proc.Attrs.Body, pg.SourceSQL)
	if block.Comment != nil {
		proc.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		proc.RenamedFrom = &block.RenamedFrom.Name
		proc.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.Owner != nil {
		proc.Owner = &block.Owner.Name
	}
	if block.Deprecated != nil {
		proc.Deprecated = &block.Deprecated.Value
	}
	for _, g := range block.Grants {
		proc.Grants = append(proc.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		proc.Revocations = append(proc.Revocations, blockRevocationToIR(r))
	}
	proc.SecurityLabels = block.SecurityLabels
	proc.NameMaps = block.NameMaps
	proc.DependsOnExtensions = block.DependsOnExtensions
	return proc, nil
}

func extractFuncName(funcname []*pg_query.Node) (schema, name string) {
	switch len(funcname) {
	case 1:
		if sv := funcname[0].GetString_(); sv != nil {
			name = sv.Sval
		}
	case 2:
		if sv := funcname[0].GetString_(); sv != nil {
			schema = sv.Sval
		}
		if sv := funcname[1].GetString_(); sv != nil {
			name = sv.Sval
		}
	}
	return
}

func buildFuncArg(fp *pg_query.FunctionParameter) FuncArg {
	arg := FuncArg{
		Name: fp.Name,
	}
	if fp.ArgType != nil {
		arg.Type = typeNameToRef(fp.ArgType)
	}
	switch fp.Mode {
	case pg_query.FunctionParameterMode_FUNC_PARAM_IN:
		arg.Mode = "IN"
	case pg_query.FunctionParameterMode_FUNC_PARAM_OUT:
		arg.Mode = "OUT"
	case pg_query.FunctionParameterMode_FUNC_PARAM_INOUT:
		arg.Mode = "INOUT"
	case pg_query.FunctionParameterMode_FUNC_PARAM_VARIADIC:
		arg.Mode = "VARIADIC"
	case pg_query.FunctionParameterMode_FUNC_PARAM_TABLE:
		arg.Mode = "TABLE"
	default:
		arg.Mode = "IN"
	}
	return arg
}

// impliedReturnType computes the return type PostgreSQL itself infers when a
// CREATE FUNCTION's RETURNS clause is omitted entirely — valid only when at
// least one OUT/INOUT parameter is present (pg_query performs no semantic
// analysis, so cfs.ReturnType is simply nil in source here; PostgreSQL fills
// this in at CREATE time and pg_get_functiondef always reconstructs it
// explicitly). Confirmed live against postgres:17: exactly one OUT/INOUT
// parameter yields that parameter's own type; more than one yields "record"
// (matching a plain multi-column RETURNS TABLE(...) function, minus SetOf).
// Zero OUT/INOUT parameters with no RETURNS clause is itself invalid
// PostgreSQL ("function result type must be specified") — left as the zero
// TypeRef, same as before this fix, since that input was already guaranteed
// to fail on apply regardless of what DPG renders for it.
func impliedReturnType(args []FuncArg) TypeRef {
	var out []TypeRef
	for _, a := range args {
		if a.Mode == "OUT" || a.Mode == "INOUT" {
			out = append(out, a.Type)
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	if len(out) > 1 {
		return TypeRef{Name: "record"}
	}
	return TypeRef{}
}

func extractFuncAttrs(cfs *pg_query.CreateFunctionStmt, sourceSQL string) FuncAttrs {
	attrs := FuncAttrs{Volatility: "VOLATILE", Parallel: "UNSAFE"}
	for _, opt := range cfs.Options {
		de := opt.GetDefElem()
		if de == nil {
			continue
		}
		switch strings.ToLower(de.Defname) {
		case "language":
			if sv := de.Arg.GetString_(); sv != nil {
				attrs.Language = sv.Sval
			}
		case "volatility":
			if sv := de.Arg.GetString_(); sv != nil {
				attrs.Volatility = strings.ToUpper(sv.Sval)
			}
		case "strict":
			attrs.Strict = de.Arg.GetBoolean() != nil && de.Arg.GetBoolean().Boolval
		case "security":
			attrs.SecurityDef = de.Arg.GetBoolean() != nil && de.Arg.GetBoolean().Boolval
		case "leakproof":
			attrs.Leakproof = de.Arg.GetBoolean() != nil && de.Arg.GetBoolean().Boolval
		case "parallel":
			if sv := de.Arg.GetString_(); sv != nil {
				attrs.Parallel = strings.ToUpper(sv.Sval)
			}
		case "cost":
			if v, ok := numericOnlyToFloat(de.Arg); ok {
				attrs.Cost = &v
			}
		case "rows":
			if v, ok := numericOnlyToFloat(de.Arg); ok {
				attrs.Rows = &v
			}
		case "transform":
			// TRANSFORM FOR TYPE t [, FOR TYPE t ...] — a List of TypeName
			// nodes (confirmed live via pg_query.Parse; a structurally
			// different shape from every other DefElem here).
			if list := de.Arg.GetList(); list != nil {
				for _, item := range list.Items {
					if tn := item.GetTypeName(); tn != nil {
						attrs.Transforms = append(attrs.Transforms, typeNameToRef(tn))
					}
				}
			}
		case "as":
			// The body is in the Arg list as a List node. A 2-item list is
			// unambiguously LANGUAGE C's "AS 'obj_file', 'link_symbol'" form
			// (confirmed live: the grammar never produces a 2-item list any
			// other way). A 1-item list is ambiguous between a dollar-quoted
			// procedural body and a bare "AS 'obj_file'"/"AS 'name'" single-
			// string form — both parse identically (pg_query drops quote
			// style entirely) — but both already round-trip correctly
			// through Body's existing dollar-quoted re-render (PostgreSQL's
			// own pg_get_functiondef does the same for LANGUAGE internal),
			// so no further disambiguation is needed there.
			if list := de.Arg.GetList(); list != nil && len(list.Items) > 0 {
				if len(list.Items) >= 2 {
					if sv0 := list.Items[0].GetString_(); sv0 != nil {
						attrs.ObjFile = &sv0.Sval
					}
					if sv1 := list.Items[1].GetString_(); sv1 != nil {
						attrs.LinkSymbol = &sv1.Sval
					}
				} else if sv := list.Items[0].GetString_(); sv != nil {
					attrs.Body = sv.Sval
				}
			} else if sv := de.Arg.GetString_(); sv != nil {
				attrs.Body = sv.Sval
			}
		case "set":
			// SET param = value / SET param FROM CURRENT (Section 9.2) — a
			// VariableSetStmt, not a plain value like every other DefElem
			// above (confirmed live via pg_query.Parse). VAR_SET_CURRENT
			// is FROM CURRENT; VAR_SET_VALUE's Args is a list of A_Const
			// nodes, one item for a single-valued GUC, more than one for a
			// multi-valued one like search_path ("SET search_path = a,
			// b"), joined here with ", " so Value round-trips identically
			// through RenderCreateFunctionSQL's re-emission.
			if vs := de.Arg.GetVariableSetStmt(); vs != nil {
				cfg := FuncConfig{Param: vs.Name}
				if vs.Kind == pg_query.VariableSetKind_VAR_SET_CURRENT {
					cfg.FromCurrent = true
				} else {
					items := make([]string, len(vs.Args))
					for i, a := range vs.Args {
						items[i] = nodeToText(a)
					}
					val := strings.Join(items, ", ")
					cfg.Value = &val
				}
				attrs.Config = append(attrs.Config, cfg)
			}
		}
	}
	if cfs.SqlBody != nil {
		// BEGIN ATOMIC ... END (PG14+, LANGUAGE SQL only) — cfs.Options has
		// no "as" DefElem for this form at all (confirmed live via
		// pg_query.Parse), so the body must come from the reconstructed
		// source text instead. See ExtractAtomicBody's doc comment for why
		// a raw-text extraction is safe here.
		attrs.AtomicBody = true
		attrs.Body = ExtractAtomicBody(sourceSQL)
	}
	return attrs
}

// beginAtomicRe locates a PG14+ BEGIN ATOMIC body's statement-list text.
// Anchored to the end of the (single, already-isolated) source statement so
// the greedy ".*" naturally captures up to the LAST "END" in the text —
// the body's own real closing keyword — even when the statement list itself
// contains a "CASE ... END" expression, since RFC Section 9.1's
// sql-stmt-list grammar guarantees every statement (including the last) ends
// with its own ";" immediately before that final closing END.
var beginAtomicRe = regexp.MustCompile(`(?is)BEGIN\s+ATOMIC\s*(.*)\bEND\s*;?\s*$`)

// ExtractAtomicBody extracts a BEGIN ATOMIC body's raw statement-list text
// from sourceSQL — the full reconstructed "CREATE FUNCTION/PROCEDURE ..."
// statement (pipeline.PGParseResult.SourceSQL). pg_query's own AST has no
// separate body-text field for this form (unlike the dollar-quoted "AS"
// form's List-of-String_ shape) — SqlBody is a fully parsed statement tree,
// not source text — so this mirrors the same "raw string search" convention
// RFC Section 9.1 already documents for AS 'obj_file' extraction. The
// extracted text is fed straight into canonicalizeSQLBody/HashFunctionBody,
// identical to a dollar-quoted LANGUAGE SQL body once extracted.
func ExtractAtomicBody(sourceSQL string) string {
	m := beginAtomicRe.FindStringSubmatch(strings.TrimSpace(sourceSQL))
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// numericOnlyToFloat converts a pg_query NumericOnly node (COST/ROWS'
// argument) to a float64. Confirmed live that pg_query represents an
// integer-looking value (e.g. "COST 500") as an Integer node and a
// fractional one (e.g. "COST 500.5") as a Float node — never as a String,
// unlike LANGUAGE/VOLATILITY/PARALLEL's plain identifier arguments.
func numericOnlyToFloat(n *pg_query.Node) (float64, bool) {
	if iv := n.GetInteger(); iv != nil {
		return float64(iv.Ival), true
	}
	if fv := n.GetFloat(); fv != nil {
		if v, err := strconv.ParseFloat(fv.Fval, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// ── Enum ─────────────────────────────────────────────────────────────────────

func (b *Builder) buildEnum(cs *pg_query.CreateEnumStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	t := &Type{
		Variant: "ENUM",
		SrcPos:  pos,
	}
	if len(cs.TypeName) > 0 {
		t.Schema, t.Name = extractTypeName(cs.TypeName)
	}
	for _, v := range cs.Vals {
		if sv := v.GetString_(); sv != nil {
			t.EnumValues = append(t.EnumValues, sv.Sval)
		}
	}
	if block.Comment != nil {
		t.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		t.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		t.RenamedFrom = &block.RenamedFrom.Name
		t.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.Deprecated != nil {
		t.Deprecated = &block.Deprecated.Value
	}
	if block.MigrateRemove != nil {
		t.MigrateRemove = block.MigrateRemove
	}
	t.EnumValueRenames = block.EnumValueRenames
	for _, g := range block.Grants {
		t.Grants = append(t.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		t.Revocations = append(t.Revocations, blockRevocationToIR(r))
	}
	t.SecurityLabels = block.SecurityLabels
	t.NameMaps = block.NameMaps
	return t, nil
}

// buildCompositeType handles CREATE TYPE name AS (attr type, ...), which
// pg_query parses as its own distinct node type, CompositeTypeStmt — unlike
// CREATE TYPE name AS ENUM (...), which parses as CreateEnumStmt. Build's
// switch had no case for CompositeTypeStmt at all, so every composite type
// declaration silently fell through to the generic default case (an empty,
// nameless OpaqueObject) — the exact same bug shape found and fixed for
// MATERIALIZED VIEW (CreateTableAsStmt) earlier the same session: fields
// existed end-to-end downstream (ir.Type.Variant == "COMPOSITE",
// CompositeAttrs; differ.go's diffType and snapshot's toSnapType both
// already handled it), just never reachable because nothing ever built one
// from source. Confirmed live: dpg plan reported "-- (no changes)" for a
// newly-declared composite TYPE instead of a CREATE.
//
// Coldeflist elements are ColumnDef nodes, identically shaped to a table's
// column list — reuses buildColumn rather than re-implementing type/typmod
// extraction. Composite type attributes can't carry inline constraints in
// valid PostgreSQL syntax, so any constraints buildColumn would have
// promoted are simply unreachable here, not silently dropped.
func (b *Builder) buildCompositeType(cs *pg_query.CompositeTypeStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	t := &Type{
		Variant: "COMPOSITE",
		SrcPos:  pos,
	}
	if cs.Typevar != nil {
		t.Schema = rangeVarSchema(cs.Typevar)
		t.Name = cs.Typevar.Relname
	}
	for _, node := range cs.Coldeflist {
		cd := node.GetColumnDef()
		if cd == nil {
			continue
		}
		col, _, err := b.buildColumn(cd, pos)
		if err != nil {
			return nil, err
		}
		t.CompositeAttrs = append(t.CompositeAttrs, col)
	}
	if block.Comment != nil {
		t.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		t.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		t.RenamedFrom = &block.RenamedFrom.Name
		t.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.Deprecated != nil {
		t.Deprecated = &block.Deprecated.Value
	}
	for _, g := range block.Grants {
		t.Grants = append(t.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		t.Revocations = append(t.Revocations, blockRevocationToIR(r))
	}
	t.SecurityLabels = block.SecurityLabels
	t.NameMaps = block.NameMaps

	// ATTRIBUTE attr { RENAMED FROM old; COMMENT '...'; } sub-blocks (RFC
	// Section 5.2). COLUMN is a table keyword; a composite type's members
	// are attributes, matching PostgreSQL's ALTER TYPE ... ATTRIBUTE.
	if len(block.Columns) > 0 {
		return nil, pipeline.ErrorfCode(block.Columns[0].Pos, "DPG-E024",
			"COLUMN is not valid in a composite TYPE block; composite type members are declared with ATTRIBUTE name { ... }")
	}
	attrMap := make(map[string]*Column, len(t.CompositeAttrs))
	for _, a := range t.CompositeAttrs {
		attrMap[a.Name] = a
	}
	for _, ab := range block.Attributes {
		attr, ok := attrMap[ab.Name.Name]
		if !ok {
			return nil, pipeline.ErrorfCode(ab.Pos, "DPG-E018",
				"ATTRIBUTE %q is not declared in TYPE %s; the ATTRIBUTE block must reference an attribute listed in the type's ( ) section%s",
				ab.Name.Name, qualName(t.Schema, t.Name), suggestColumns(ab.Name.Name, t.CompositeAttrs))
		}
		if ab.RenamedFrom != nil {
			attr.RenamedFrom = &ab.RenamedFrom.Name
		}
		if ab.Comment != nil {
			attr.Comment = &ab.Comment.Value
		}
	}
	return t, nil
}

func extractTypeName(names []*pg_query.Node) (schema, name string) {
	switch len(names) {
	case 1:
		if sv := names[0].GetString_(); sv != nil {
			name = sv.Sval
		}
	case 2:
		if sv := names[0].GetString_(); sv != nil {
			schema = sv.Sval
		}
		if sv := names[1].GetString_(); sv != nil {
			name = sv.Sval
		}
	}
	return
}

// defElemQualifiedName finds a DefElem by name in a DefineStmt's Definition
// list and extracts its TypeName argument's qualified name — the shape
// PostgreSQL's grammar uses for CREATE TEXT SEARCH CONFIGURATION's PARSER
// and CREATE TEXT SEARCH DICTIONARY's TEMPLATE clauses (confirmed live via
// pg_query: both parse as a DefElem whose Arg is a TypeName, not a plain
// String, even though neither is really a type). Returns empty/empty if the
// DefElem isn't present (shouldn't happen for these two — both are mandatory
// in the grammar — but callers treat empty as simply "no dependency edge").
func defElemQualifiedName(definition []*pg_query.Node, defname string) (schema, name string) {
	for _, de := range definition {
		elem := de.GetDefElem()
		if elem == nil || elem.Defname != defname {
			continue
		}
		if tn := elem.Arg.GetTypeName(); tn != nil {
			return extractTypeName(tn.Names)
		}
	}
	return "", ""
}

// defElemOperatorRef extracts a CREATE/ALTER OPERATOR COMMUTATOR/NEGATOR
// value — a List of String name-part nodes ("public", "==!=" for the
// qualified "OPERATOR(public.==!=)" form, or just "==!=" unqualified;
// confirmed via pg_query.Parse probe, distinct from RESTRICT/JOIN's
// TypeName shape since an operator symbol isn't a valid type name), joined
// into the same dotted schema.symbol form Operator.Commutator/Negator
// store. Returns nil if elem's Arg isn't a List or is empty.
func defElemOperatorRef(elem *pg_query.DefElem) *string {
	lst := elem.GetArg().GetList()
	if lst == nil || len(lst.Items) == 0 {
		return nil
	}
	parts := make([]string, 0, len(lst.Items))
	for _, item := range lst.Items {
		if sv := item.GetString_(); sv != nil {
			parts = append(parts, sv.Sval)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	ref := strings.Join(parts, ".")
	return &ref
}

// joinQualName combines a schema+name pair into the single dotted string
// Trigger.Function/Cast.Function/Operator.Function/TSParser.Functions/etc.
// all use for dependency-edge lookups — schema omitted entirely when empty,
// matching how an unqualified reference is written in source.
func joinQualName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// ── Schema ────────────────────────────────────────────────────────────────────

func (b *Builder) buildSchema(cs *pg_query.CreateSchemaStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	s := &Schema{Name: cs.Schemaname, SrcPos: pos}
	if block.Comment != nil {
		s.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		s.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		s.RenamedFrom = &block.RenamedFrom.Name
	}
	for _, g := range block.Grants {
		s.Grants = append(s.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		s.Revocations = append(s.Revocations, blockRevocationToIR(r))
	}
	s.SecurityLabels = block.SecurityLabels
	s.NameMaps = block.NameMaps
	return s, nil
}

// ── Extension ─────────────────────────────────────────────────────────────────

func (b *Builder) buildExtension(cs *pg_query.CreateExtensionStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	e := &Extension{Name: cs.Extname, SrcPos: pos}
	// Schema and version come from the options list.
	for _, opt := range cs.Options {
		if de := opt.GetDefElem(); de != nil {
			switch de.Defname {
			case "schema":
				if sv := de.Arg.GetString_(); sv != nil {
					s := sv.Sval
					e.Schema = &s
				}
			case "new_version":
				if sv := de.Arg.GetString_(); sv != nil {
					v := sv.Sval
					e.Version = &v
				}
			case "cascade":
				if b := de.Arg.GetBoolean(); b != nil {
					e.Cascade = b.GetBoolval()
				}
			}
		}
	}
	if block.Comment != nil {
		e.Comment = &block.Comment.Value
	}
	e.NameMaps = block.NameMaps
	return e, nil
}

// ── Sequence ──────────────────────────────────────────────────────────────────

func (b *Builder) buildSequence(cs *pg_query.CreateSeqStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	s := &Sequence{
		Schema: rangeVarSchema(cs.Sequence),
		Name:   cs.Sequence.Relname,
		// CREATE SEQUENCE and CREATE UNLOGGED SEQUENCE parse to the
		// identical CreateSeqStmt node — pg_query distinguishes them only
		// via Sequence.Relpersistence ("u" for unlogged), the same
		// convention already used for CREATE TABLE (see the CreateStmt
		// dispatch case's identical comment).
		Unlogged: cs.Sequence != nil && cs.Sequence.Relpersistence == "u",
		SrcPos:   pos,
	}
	if block.Comment != nil {
		s.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		s.Owner = &block.Owner.Name
	}
	for _, g := range block.Grants {
		s.Grants = append(s.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		s.Revocations = append(s.Revocations, blockRevocationToIR(r))
	}
	s.SecurityLabels = block.SecurityLabels
	s.NameMaps = block.NameMaps
	for _, opt := range cs.Options {
		de := opt.GetDefElem()
		if de == nil {
			continue
		}
		v := seqOptionInt(de)
		switch de.Defname {
		case "increment":
			s.IncrementBy = v
		case "start":
			s.StartValue = v
		case "minvalue":
			if de.Arg == nil {
				// Explicit "NO MINVALUE" — same nil-Arg DefElem shape as the
				// option being omitted entirely, so this must be captured
				// here or it's indistinguishable from "not mentioned" (RFC
				// audit item #20).
				s.NoMinValue = true
			} else {
				s.MinValue = v
			}
		case "maxvalue":
			if de.Arg == nil {
				s.NoMaxValue = true
			} else {
				s.MaxValue = v
			}
		case "cache":
			s.Cache = v
		case "cycle":
			// Unlike the numeric options above, CYCLE/NO CYCLE parses as a
			// DefElem{arg: Boolean}, not an Integer/A_Const — seqOptionInt
			// (which only handles those two) always returns nil here, so this
			// must read the Boolean node directly or CYCLE is silently dropped.
			if de.Arg != nil {
				if b := de.Arg.GetBoolean(); b != nil {
					cyc := b.GetBoolval()
					s.Cycle = &cyc
				}
			}
		case "as":
			// RFC audit item #14: AS type, a TypeName arg (not
			// Integer/A_Const/Boolean like every option above), so
			// seqOptionInt never handled it either.
			if tn := de.Arg.GetTypeName(); tn != nil {
				ref := typeNameToRef(tn)
				s.AsType = &ref
			}
		case "owned_by":
			// RFC audit item #14: OWNED BY table.col, a List of String
			// name-part nodes — "none" (lowercase, single item) for the
			// explicit "OWNED BY NONE" form, otherwise the column's
			// qualified name parts (table.col, or schema.table.col).
			if lst := de.Arg.GetList(); lst != nil {
				parts := make([]string, 0, len(lst.Items))
				for _, item := range lst.Items {
					if sv := item.GetString_(); sv != nil {
						parts = append(parts, sv.Sval)
					}
				}
				if len(parts) == 1 && strings.EqualFold(parts[0], "none") {
					none := "NONE"
					s.OwnedBy = &none
				} else if len(parts) > 0 {
					owned := strings.Join(parts, ".")
					s.OwnedBy = &owned
				}
			}
		case "restart":
			// RFC audit item #68: RESTART [WITH n]. See Sequence.Restart's
			// doc comment for why this isn't persisted like the options
			// above.
			s.Restart = true
			s.RestartWith = v
		}
	}
	return s, nil
}

// seqOptionInt extracts an int64 value from a sequence DefElem node.
// pg_query represents integer sequence options as either a pg_query.Integer
// or an A_Const Integer node.
func seqOptionInt(de *pg_query.DefElem) *int64 {
	if de.Arg == nil {
		return nil
	}
	if ic := de.Arg.GetInteger(); ic != nil {
		v := int64(ic.Ival)
		return &v
	}
	if ac := de.Arg.GetAConst(); ac != nil {
		if ic2 := ac.GetIval(); ic2 != nil {
			v := int64(ic2.Ival)
			return &v
		}
	}
	return nil
}

// ── Role ──────────────────────────────────────────────────────────────────────

// roleSpecNames extracts role names from a pg_query List of RoleSpec nodes
// (IN ROLE / ROLE / ADMIN role-lists).
func roleSpecNames(list *pg_query.List) []string {
	if list == nil {
		return nil
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		if rs := item.GetRoleSpec(); rs != nil {
			names = append(names, rs.Rolename)
		}
	}
	return names
}

// buildRole extracts every CREATE ROLE option (RFC Section 11.1) directly from
// CreateRoleStmt.Options — native PostgreSQL grammar (LOGIN/SUPERUSER/
// CREATEDB/CREATEROLE/INHERIT/REPLICATION/BYPASSRLS/CONNECTION LIMIT/
// PASSWORD/VALID UNTIL/IN ROLE/ROLE/ADMIN), not a DPG block directive.
// PASSWORD's raw text (may contain {{secret-uri}} placeholders) is copied
// verbatim; resolving it happens only at apply time, never here.
func (b *Builder) buildRole(cs *pg_query.CreateRoleStmt, block pipeline.BlockAST, pos pipeline.SourcePos) (pipeline.IRObject, error) {
	r := &Role{Name: cs.Role, SrcPos: pos}
	for _, opt := range cs.Options {
		de := opt.GetDefElem()
		if de == nil {
			continue
		}
		switch de.Defname {
		case "canlogin":
			v := de.Arg.GetBoolean().Boolval
			r.CanLogin = &v
		case "superuser":
			v := de.Arg.GetBoolean().Boolval
			r.Superuser = &v
		case "createdb":
			v := de.Arg.GetBoolean().Boolval
			r.CreateDB = &v
		case "createrole":
			v := de.Arg.GetBoolean().Boolval
			r.CreateRole = &v
		case "inherit":
			v := de.Arg.GetBoolean().Boolval
			r.Inherit = &v
		case "isreplication":
			v := de.Arg.GetBoolean().Boolval
			r.IsReplication = &v
		case "bypassrls":
			v := de.Arg.GetBoolean().Boolval
			r.BypassRLS = &v
		case "connectionlimit":
			v := int(de.Arg.GetInteger().Ival)
			r.ConnectionLimit = &v
		case "password":
			v := de.Arg.GetString_().Sval
			r.Password = &v
		case "validUntil":
			v := de.Arg.GetString_().Sval
			r.ValidUntil = &v
		case "addroleto":
			// RFC audit item #32: real CREATE ROLE's inline IN ROLE list
			// accepts no WITH modifiers at all (confirmed live) — every
			// entry here is bare (Admin false, Inherit/Set unmanaged).
			for _, name := range roleSpecNames(de.Arg.GetList()) {
				r.Memberships = append(r.Memberships, RoleMembership{Role: name, Direction: "IN_ROLE", Pos: pos})
			}
		case "rolemembers":
			for _, name := range roleSpecNames(de.Arg.GetList()) {
				r.Memberships = append(r.Memberships, RoleMembership{Role: name, Direction: "ROLE", Pos: pos})
			}
		case "adminmembers":
			// Real CREATE ROLE ADMIN semantics: the named roles become
			// members of the new role WITH ADMIN OPTION (confirmed against
			// PostgreSQL's own documentation) — same Direction as
			// rolemembers, Admin forced true.
			for _, name := range roleSpecNames(de.Arg.GetList()) {
				r.Memberships = append(r.Memberships, RoleMembership{Role: name, Direction: "ROLE", Admin: true, Pos: pos})
			}
		}
	}
	if block.Comment != nil {
		r.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		r.RenamedFrom = &block.RenamedFrom.Name
	}
	for _, rc := range block.RoleConfigs {
		r.Configs = append(r.Configs, RoleConfig{
			Param: rc.Param, Value: rc.Value, FromCurrent: rc.FromCurrent,
			Reset: rc.Reset, ResetAll: rc.ResetAll, InDatabase: rc.InDatabase, Pos: rc.Pos,
		})
	}
	for _, m := range block.Memberships {
		admin := m.AdminDefault
		if m.Admin != nil {
			admin = *m.Admin
		}
		r.Memberships = append(r.Memberships, RoleMembership{
			Role: m.Role.String(), Direction: m.Direction, Admin: admin,
			Inherit: m.Inherit, Set: m.Set, Pos: m.Pos,
		})
	}
	r.SecurityLabels = block.SecurityLabels
	r.NameMaps = block.NameMaps
	return r, nil
}

// ── Tablespace ────────────────────────────────────────────────────────────────

func (b *Builder) buildTablespace(cs *pg_query.CreateTableSpaceStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	ts := &Tablespace{Name: cs.Tablespacename, Location: cs.Location, Body: body, StorageParams: buildOrderedOptions(cs.Options), SrcPos: pos}
	if cs.Owner != nil && cs.Owner.Rolename != "" {
		owner := cs.Owner.Rolename
		ts.Owner = &owner
	}
	if block.Comment != nil {
		ts.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		ts.RenamedFrom = &block.RenamedFrom.Name
	}
	for _, g := range block.Grants {
		ts.Grants = append(ts.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		ts.Revocations = append(ts.Revocations, blockRevocationToIR(r))
	}
	ts.SecurityLabels = block.SecurityLabels
	return ts, nil
}

// ── FDW / Server / User Mapping ───────────────────────────────────────────────

// defElemOptionsToStorageParams converts an OPTIONS (...) clause's DefElem
// list (CreateFdwStmt.Options/CreateForeignServerStmt.Options/
// CreateUserMappingStmt.Options — confirmed identical shape via
// pg_query.Parse probe: each entry is a DefElem whose Arg is a plain String,
// not a List, unlike HANDLER/VALIDATOR below) into StorageParams.
func defElemOptionsToStorageParams(opts []*pg_query.Node) []pipeline.StorageParam {
	var out []pipeline.StorageParam
	for _, o := range opts {
		de := o.GetDefElem()
		if de == nil {
			continue
		}
		sv := de.GetArg().GetString_()
		if sv == nil {
			continue
		}
		out = append(out, pipeline.StorageParam{Key: de.Defname, Value: sv.Sval})
	}
	return out
}

// defElemFuncName extracts a HANDLER/VALIDATOR function name from
// CreateFdwStmt.FuncOptions — each is a DefElem (Defname "handler" or
// "validator") whose Arg is a List of String nodes (the possibly-qualified
// function name, one item per name part), confirmed via pg_query.Parse
// probe. Returns "" if defname isn't present (NO HANDLER/NO VALIDATOR or
// omitted — indistinguishable to PostgreSQL, both mean "none").
func defElemFuncName(opts []*pg_query.Node, defname string) string {
	for _, o := range opts {
		de := o.GetDefElem()
		if de == nil || de.Defname != defname {
			continue
		}
		lst := de.GetArg().GetList()
		if lst == nil {
			continue
		}
		schema, name := extractTypeName(lst.Items)
		if schema != "" {
			return schema + "." + name
		}
		return name
	}
	return ""
}

func (b *Builder) buildFDW(cs *pg_query.CreateFdwStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	f := &ForeignDataWrapper{
		Name:      cs.Fdwname,
		Handler:   defElemFuncName(cs.FuncOptions, "handler"),
		Validator: defElemFuncName(cs.FuncOptions, "validator"),
		Options:   defElemOptionsToStorageParams(cs.Options),
		Body:      body, SrcPos: pos,
	}
	if block.Comment != nil {
		f.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		f.RenamedFrom = &block.RenamedFrom.Name
	}
	if block.Owner != nil {
		f.Owner = &block.Owner.Name
	}
	for _, g := range block.Grants {
		f.Grants = append(f.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		f.Revocations = append(f.Revocations, blockRevocationToIR(r))
	}
	return f, nil
}

func (b *Builder) buildServer(cs *pg_query.CreateForeignServerStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	s := &ForeignServer{
		Name: cs.Servername, FDWName: cs.Fdwname,
		Options: defElemOptionsToStorageParams(cs.Options),
		Body:    body, SrcPos: pos,
	}
	if cs.Servertype != "" {
		srvType := cs.Servertype
		s.Type = &srvType
	}
	if cs.Version != "" {
		version := cs.Version
		s.Version = &version
	}
	if block.Comment != nil {
		s.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		s.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		s.RenamedFrom = &block.RenamedFrom.Name
	}
	for _, g := range block.Grants {
		s.Grants = append(s.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		s.Revocations = append(s.Revocations, blockRevocationToIR(r))
	}
	return s, nil
}

func (b *Builder) buildUserMapping(cs *pg_query.CreateUserMappingStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	user := ""
	if cs.User != nil {
		user = cs.User.Rolename
	}
	return &UserMapping{
		User:    user,
		Server:  cs.Servername,
		Options: defElemOptionsToStorageParams(cs.Options),
		Body:    body,
		SrcPos:  pos,
	}, nil
}

// ── Domain ────────────────────────────────────────────────────────────────────

// buildDomain extracts DOMAIN's structured RFC Section 5.4 diffing inputs
// (base type, DEFAULT, NOT NULL, CHECK constraints) from Part 1's real PG
// CreateDomainStmt node, then merges in anything additionally declared via
// the { } block's DEFAULT/NOT NULL/CONSTRAINT directives — found live-
// testing a demo project: none of this was ever extracted at all before
// (buildDomain only read block.Comment), so diffType had nothing to diff a
// changed DEFAULT/constraint/NOT NULL against and fell through to
// comment-only diffing forever, contradicting RFC Section 5.4's explicit
// per-property SAFE/CAUTION semantics.
func (b *Builder) buildDomain(cs *pg_query.CreateDomainStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	schema, name := extractTypeName(cs.Domainname)
	t := &Type{
		Schema:         schema,
		Name:           name,
		Variant:        "DOMAIN",
		Body:           body,
		DomainBaseType: typeNameToRef(cs.TypeName),
		SrcPos:         pos,
	}
	if cs.CollClause != nil {
		t.DomainCollation = qualifiedNameText(cs.CollClause.Collname)
	}
	for _, cn := range cs.Constraints {
		c := cn.GetConstraint()
		if c == nil {
			continue
		}
		switch c.Contype {
		case pg_query.ConstrType_CONSTR_DEFAULT:
			if c.RawExpr != nil {
				expr := nodeToText(c.RawExpr)
				t.DomainDefault = &expr
			}
		case pg_query.ConstrType_CONSTR_NOTNULL:
			t.DomainNotNull = true
		case pg_query.ConstrType_CONSTR_CHECK:
			// NotValid is never set here: confirmed empirically that real
			// PostgreSQL's CREATE DOMAIN grammar rejects an inline NOT
			// VALID on its constraint clause outright (a domain constraint
			// can only ever be added NOT VALID via a follow-up
			// ALTER DOMAIN ... ADD CONSTRAINT), so c.SkipValidation is
			// always false for this parse path — unlike CREATE TABLE's
			// identical field, which real PostgreSQL does allow inline.
			if c.RawExpr != nil {
				expr := nodeToText(c.RawExpr)
				t.DomainConstraints = append(t.DomainConstraints, &Constraint{
					Name: c.Conname,
					Type: "CHECK",
					Expr: "CHECK (" + expr + ")",
					Pos:  pos,
				})
			}
		}
	}
	if block.Comment != nil {
		t.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		t.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		t.RenamedFrom = &block.RenamedFrom.Name
		t.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	if block.DomainDefault != nil {
		t.DomainDefault = &block.DomainDefault.Text
	}
	if block.DomainNotNull {
		t.DomainNotNull = true
	}
	for _, cst := range block.Constraints {
		t.DomainConstraints = append(t.DomainConstraints, &Constraint{
			Name:        cst.Name.Name,
			Type:        "CHECK",
			Expr:        cst.Expr.Text,
			NotValid:    cst.NotValid,
			RenamedFrom: cst.RenamedFrom,
			Pos:         cst.Pos,
		})
	}
	for _, g := range block.Grants {
		t.Grants = append(t.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		t.Revocations = append(t.Revocations, blockRevocationToIR(r))
	}
	t.SecurityLabels = block.SecurityLabels
	t.NameMaps = block.NameMaps
	return t, nil
}

// buildRangeType handles CREATE TYPE name AS RANGE (options), the third
// distinct CREATE TYPE node kind pg_query parses (alongside CreateEnumStmt
// for ENUM and CompositeTypeStmt for composite) — confirmed via a direct
// pg_query.Parse probe that it produces CreateRangeStmt, never DefineStmt.
// Build's switch had no case for CreateRangeStmt at all, so every range type
// declaration silently fell through to the generic default case (an empty,
// nameless OpaqueObject) — the same bug shape as buildMaterializedView and
// buildCompositeType, found immediately after those two the same session.
// Notably, buildDefineStmt (below) already contains dead "isRange"/
// "isComposite" detection logic for a DefineStmt-shaped CREATE TYPE that
// pg_query v6 simply never produces for RANGE or composite types anymore —
// that heuristic is only still reachable (and only ever resolves to "BASE")
// for the bare CREATE TYPE name (INPUT = ..., OUTPUT = ...) base-type form.
// Kept as Variant "RANGE" + opaque Body (like DOMAIN/BASE), matching the
// existing ir.Type.Body doc comment ("raw Part1 for range/domain/base") and
// differ.go's existing "any change = DROP TYPE CASCADE + CREATE TYPE"
// handling for that variant — no new diff/dump wiring needed.
func (b *Builder) buildRangeType(cs *pg_query.CreateRangeStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	schema, name := extractTypeName(cs.TypeName)
	t := &Type{
		Schema:         schema,
		Name:           name,
		Variant:        "RANGE",
		Body:           body,
		SrcPos:         pos,
		MultirangeName: DefaultMultirangeName(name),
	}
	for _, p := range cs.Params {
		de := p.GetDefElem()
		if de == nil || de.Defname != "multirange_type_name" || de.Arg == nil {
			continue
		}
		if tn := de.Arg.GetTypeName(); tn != nil && len(tn.Names) > 0 {
			if sv := tn.Names[len(tn.Names)-1].GetString_(); sv != nil {
				t.MultirangeName = sv.Sval
			}
		}
	}
	if block.Comment != nil {
		t.Comment = &block.Comment.Value
	}
	if block.Owner != nil {
		t.Owner = &block.Owner.Name
	}
	if block.RenamedFrom != nil {
		t.RenamedFrom = &block.RenamedFrom.Name
		t.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	for _, g := range block.Grants {
		t.Grants = append(t.Grants, blockGrantToIR(g))
	}
	for _, r := range block.Revocations {
		t.Revocations = append(t.Revocations, blockRevocationToIR(r))
	}
	t.SecurityLabels = block.SecurityLabels
	t.NameMaps = block.NameMaps
	return t, nil
}

// ── Statistics ────────────────────────────────────────────────────────────────

func (b *Builder) buildStatistics(cs *pg_query.CreateStatsStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	s := &StatisticsObject{Body: body, SrcPos: pos}
	if len(cs.Defnames) > 0 {
		s.Schema, s.Name = extractTypeName(cs.Defnames)
	}
	for _, t := range cs.StatTypes {
		if sv := t.GetString_(); sv != nil {
			s.Kinds = append(s.Kinds, sv.Sval)
		}
	}
	if len(s.Kinds) == 0 {
		// PostgreSQL's own default when no kind list is given at all
		// (bare "ON col1, col2 FROM t", no "(kind, ...)" clause): all
		// three supported kinds are created — confirmed live via a real
		// PostgreSQL 17 server (pg_statistic_ext.stxkind = {d,f,m}), not
		// an empty Kinds list, which would otherwise spuriously diff
		// against a live introspected object that has all three.
		s.Kinds = []string{"ndistinct", "dependencies", "mcv"}
	}
	for _, e := range cs.Exprs {
		el := e.GetStatsElem()
		if el == nil {
			continue
		}
		if el.Name != "" {
			s.Columns = append(s.Columns, el.Name)
		} else if el.Expr != nil {
			// Canonicalized via the same pg_query deparse nodeToText
			// already uses elsewhere, so this matches
			// pg_get_statisticsobjdef_expressions' own canonical
			// rendering (confirmed live) regardless of source formatting.
			s.Columns = append(s.Columns, nodeToText(el.Expr))
		}
	}
	if len(cs.Relations) > 0 {
		if rv := cs.Relations[0].GetRangeVar(); rv != nil {
			s.Table = qualName(rv.Schemaname, rv.Relname)
		}
	}
	if block.Comment != nil {
		s.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		s.RenamedFrom = &block.RenamedFrom.Name
		s.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
	}
	return s, nil
}

// ── DefineStmt (composite/range/base type, aggregate, operator, collation, TS objects) ──

func (b *Builder) buildDefineStmt(ds *pg_query.DefineStmt, block pipeline.BlockAST, pos pipeline.SourcePos, rawBody string) (pipeline.IRObject, error) {
	schema, name := extractTypeName(ds.Defnames)
	switch ds.Kind {
	case pg_query.ObjectType_OBJECT_TYPE:
		t := &Type{Schema: schema, Name: name, SrcPos: pos}
		if block.Comment != nil {
			t.Comment = &block.Comment.Value
		}
		if block.Owner != nil {
			t.Owner = &block.Owner.Name
		}
		if block.RenamedFrom != nil {
			t.RenamedFrom = &block.RenamedFrom.Name
			t.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		// Distinguish composite/range/base by the definition elements.
		// Composite: has list of column defs
		// Range: has "subtype" element
		// Base: has "input" element (input/output functions)
		isRange, isComposite := false, false
		for _, de := range ds.Definition {
			if elem := de.GetDefElem(); elem != nil {
				switch elem.Defname {
				case "subtype":
					isRange = true
				case "input":
					// base type
				case "column":
					isComposite = true
				}
			}
		}
		if isRange {
			t.Variant = "RANGE"
		} else if isComposite {
			t.Variant = "COMPOSITE"
			for _, de := range ds.Definition {
				elem := de.GetDefElem()
				if elem == nil || elem.Defname != "column" {
					continue
				}
				cd := elem.Arg.GetColumnDef()
				if cd == nil {
					continue
				}
				col := &Column{Name: cd.Colname}
				if cd.TypeName != nil {
					col.Type = typeNameToRef(cd.TypeName)
				}
				t.CompositeAttrs = append(t.CompositeAttrs, col)
			}
		} else {
			// rawBody was never assigned here — a declared BASE type parsed
			// with Variant set but Body permanently empty ("" is treated as
			// "nothing to create" by createType's default branch and by
			// sourceBodyHash), found live-testing a demo project. RANGE and
			// DOMAIN's own builder functions (buildRangeType/buildDomain)
			// already set Body: rawBody identically.
			t.Variant = "BASE"
			t.Body = rawBody
			for _, de := range ds.Definition {
				elem := de.GetDefElem()
				if elem == nil {
					continue
				}
				var val *string
				switch {
				case elem.Arg == nil:
					continue // bare flag (e.g. PASSEDBYVALUE) — not one of the 7 alterable properties
				case elem.Arg.GetTypeName() != nil:
					s := typeNameToRef(elem.Arg.GetTypeName()).String()
					val = &s
				default:
					s := nodeToText(elem.Arg)
					val = &s
				}
				switch strings.ToLower(elem.Defname) {
				case "receive":
					t.BaseReceive = val
				case "send":
					t.BaseSend = val
				case "typmod_in":
					t.BaseTypmodIn = val
				case "typmod_out":
					t.BaseTypmodOut = val
				case "analyze":
					t.BaseAnalyze = val
				case "subscript":
					t.BaseSubscript = val
				case "storage":
					t.BaseStorage = val
				}
			}
		}
		for _, g := range block.Grants {
			t.Grants = append(t.Grants, blockGrantToIR(g))
		}
		for _, r := range block.Revocations {
			t.Revocations = append(t.Revocations, blockRevocationToIR(r))
		}
		t.SecurityLabels = block.SecurityLabels
		t.NameMaps = block.NameMaps
		return t, nil

	case pg_query.ObjectType_OBJECT_AGGREGATE:
		agg := &Aggregate{Schema: schema, Name: name, Body: rawBody, SrcPos: pos}
		// ds.Args is NOT a flat list of FunctionParameter nodes (unlike a
		// regular function's Parameters): ds.Args[0] is itself a List node
		// wrapping the actual input-type parameter(s), and ds.Args[1] is an
		// unrelated integer sentinel (-1 for a normal aggregate; PG's
		// internal ordered-set-aggregate marker) — confirmed via a direct
		// pg_query.Parse probe. Calling GetFunctionParameter() on the
		// top-level ds.Args elements directly (as done previously) always
		// returns nil for both, so agg.Args silently stayed empty — found
		// live-testing a demo project: the RFC's own worked example
		// ("AGGREGATE product (DOUBLE PRECISION) (...)") round-tripped with
		// an empty "()" signature everywhere (CREATE AGGREGATE, COMMENT ON
		// AGGREGATE, GRANT ON FUNCTION), not just cosmetically wrong but
		// referencing a different (nonexistent, zero-arg) aggregate entirely.
		if len(ds.Args) > 0 {
			if lst := ds.Args[0].GetList(); lst != nil {
				for _, item := range lst.Items {
					if fp := item.GetFunctionParameter(); fp != nil {
						agg.Args = append(agg.Args, buildFuncArg(fp))
					}
				}
			}
			// ds.Args[0] == nil represents the "*" wildcard input list
			// (AGGREGATE name (*) (...)) — agg.Args intentionally stays
			// empty in that case too; DPG doesn't yet distinguish a
			// wildcard-arg aggregate from a genuinely zero-arg one.
		}
		agg.Options = buildAggregateOptions(ds.Definition)
		if block.Comment != nil {
			agg.Comment = &block.Comment.Value
		}
		if block.RenamedFrom != nil {
			agg.RenamedFrom = &block.RenamedFrom.Name
			agg.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		if block.Owner != nil {
			agg.Owner = &block.Owner.Name
		}
		if block.Deprecated != nil {
			agg.Deprecated = &block.Deprecated.Value
		}
		for _, g := range block.Grants {
			agg.Grants = append(agg.Grants, blockGrantToIR(g))
		}
		for _, r := range block.Revocations {
			agg.Revocations = append(agg.Revocations, blockRevocationToIR(r))
		}
		agg.SecurityLabels = block.SecurityLabels
		agg.NameMaps = block.NameMaps
		return agg, nil

	case pg_query.ObjectType_OBJECT_OPERATOR:
		op := &Operator{Schema: schema, Name: name, Body: rawBody, SrcPos: pos}
		for _, de := range ds.Definition {
			elem := de.GetDefElem()
			if elem == nil {
				continue
			}
			tn := elem.Arg.GetTypeName()
			if tn == nil {
				continue
			}
			switch elem.Defname {
			case "leftarg":
				t := typeNameToRef(tn)
				op.LeftType = &t
			case "rightarg":
				t := typeNameToRef(tn)
				op.RightType = &t
			}
		}
		if funcSchema, funcName := defElemQualifiedName(ds.Definition, "procedure"); funcName != "" {
			op.Function = funcName
			if funcSchema != "" {
				op.Function = funcSchema + "." + funcName
			}
		}
		if restrictSchema, restrictName := defElemQualifiedName(ds.Definition, "restrict"); restrictName != "" {
			r := joinQualName(restrictSchema, restrictName)
			op.Restrict = &r
		}
		if joinSchema, joinName := defElemQualifiedName(ds.Definition, "join"); joinName != "" {
			j := joinQualName(joinSchema, joinName)
			op.Join = &j
		}
		for _, de := range ds.Definition {
			elem := de.GetDefElem()
			if elem == nil {
				continue
			}
			switch elem.Defname {
			case "commutator":
				op.Commutator = defElemOperatorRef(elem)
			case "negator":
				op.Negator = defElemOperatorRef(elem)
			case "hashes":
				op.Hashes = true
			case "merges":
				op.Merges = true
			}
		}
		if block.Comment != nil {
			op.Comment = &block.Comment.Value
		}
		return op, nil

	case pg_query.ObjectType_OBJECT_COLLATION:
		col := &Collation{Schema: schema, Name: name, Body: rawBody, SrcPos: pos}
		col.Provider = "c"       // PostgreSQL's own default when PROVIDER is omitted
		col.Deterministic = true // PostgreSQL's own default when DETERMINISTIC is omitted
		var locale *string
		for _, d := range ds.Definition {
			de := d.GetDefElem()
			if de == nil {
				continue
			}
			switch de.Defname {
			case "provider":
				// PROVIDER's arg is a TypeName node (bare identifier
				// "icu"/"libc"/"builtin"), not a string literal —
				// confirmed via pg_query.Parse probe against real
				// CREATE COLLATION syntax.
				if tn := de.GetArg().GetTypeName(); tn != nil && len(tn.Names) > 0 {
					switch tn.Names[len(tn.Names)-1].GetString_().GetSval() {
					case "icu":
						col.Provider = "i"
					case "builtin":
						col.Provider = "b"
					default:
						col.Provider = "c"
					}
				}
			case "locale":
				if sv := de.GetArg().GetString_(); sv != nil {
					v := sv.Sval
					locale = &v
				}
			case "lc_collate":
				if sv := de.GetArg().GetString_(); sv != nil {
					v := sv.Sval
					col.Collate = &v
				}
			case "lc_ctype":
				if sv := de.GetArg().GetString_(); sv != nil {
					v := sv.Sval
					col.Ctype = &v
				}
			case "deterministic":
				// Confirmed via probe: DETERMINISTIC's arg is a plain
				// string "true"/"false", not a Boolean node.
				if sv := de.GetArg().GetString_(); sv != nil {
					col.Deterministic = sv.Sval == "true"
				}
			case "rules":
				// RFC audit item #111: PG16+ ICU tailoring RULES, a plain
				// string arg (confirmed via pg_query.Parse probe).
				if sv := de.GetArg().GetString_(); sv != nil {
					v := sv.Sval
					col.Rules = &v
				}
			case "from":
				// RFC Section 14.2's "FROM existing_collation" copy-from
				// shorthand — confirmed via pg_query.Parse probe: unlike
				// every other Collation DefElem, "from"'s Arg is a List of
				// String_ nodes (a possibly schema-qualified name), the same
				// shape qualifiedNameText already handles for an EXCLUDE
				// element's opclass/collation target.
				if lst := de.GetArg().GetList(); lst != nil {
					v := qualifiedNameText(lst.GetItems())
					col.CopyFrom = &v
				}
			}
		}
		if locale != nil {
			// LOCALE resolves differently per provider — confirmed live
			// against a real PostgreSQL 17 server: for libc/default/
			// builtin it sets BOTH collcollate and collctype to the same
			// value; for icu it sets colllocale only (collcollate/
			// collctype stay unset).
			if col.Provider == "i" {
				col.ICULocale = locale
			} else {
				col.Collate = locale
				col.Ctype = locale
			}
		}
		if block.Comment != nil {
			col.Comment = &block.Comment.Value
		}
		if block.RenamedFrom != nil {
			col.RenamedFrom = &block.RenamedFrom.Name
			col.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		if block.Owner != nil {
			col.Owner = &block.Owner.Name
		}
		col.RefreshVersion = block.RefreshVersion
		return col, nil

	case pg_query.ObjectType_OBJECT_TSCONFIGURATION:
		tc := &TSConfig{Schema: schema, Name: name, Body: rawBody, SrcPos: pos}
		tc.ParserSchema, tc.ParserName = defElemQualifiedName(ds.Definition, "parser")
		tc.Mappings = append(tc.Mappings, block.Mappings...)
		if block.Comment != nil {
			tc.Comment = &block.Comment.Value
		}
		if block.Owner != nil {
			tc.Owner = &block.Owner.Name
		}
		return tc, nil

	case pg_query.ObjectType_OBJECT_TSDICTIONARY:
		td := &TSDict{Schema: schema, Name: name, Body: rawBody, SrcPos: pos}
		td.TemplateSchema, td.TemplateName = defElemQualifiedName(ds.Definition, "template")
		// Options (audit item #52) is every declared option except TEMPLATE
		// (tracked separately above via defElemQualifiedName, whose Arg is a
		// qualified-name-shaped node buildOrderedOptions' generic nodeToText
		// doesn't handle — filtered out here rather than skipped during
		// extraction, since buildOrderedOptions is the same shared helper
		// Table/Tablespace/FOREIGN TABLE already use and gaining a
		// kind-specific exclusion parameter isn't worth it for one caller).
		for _, p := range buildOrderedOptions(ds.Definition) {
			if !strings.EqualFold(p.Key, "template") {
				td.Options = append(td.Options, p)
			}
		}
		if block.Comment != nil {
			// TSDict's Comment field already existed (unlike the 9 kinds
			// gaining one alongside this fix) but was never actually
			// populated here — found auditing every kind with a Comment
			// field for whether it's actually wired, not just declared.
			td.Comment = &block.Comment.Value
		}
		if block.Owner != nil {
			td.Owner = &block.Owner.Name
		}
		if block.RenamedFrom != nil {
			td.RenamedFrom = &block.RenamedFrom.Name
			td.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		return td, nil

	case pg_query.ObjectType_OBJECT_TSPARSER:
		var funcs []string
		for _, key := range []string{"start", "gettoken", "end", "lextypes", "headline"} {
			if fnSchema, fnName := defElemQualifiedName(ds.Definition, key); fnName != "" {
				funcs = append(funcs, joinQualName(fnSchema, fnName))
			}
		}
		prs := &TSParser{Schema: schema, Name: name, Functions: funcs, Body: rawBody, SrcPos: pos}
		if block.Comment != nil {
			prs.Comment = &block.Comment.Value
		}
		if block.RenamedFrom != nil {
			prs.RenamedFrom = &block.RenamedFrom.Name
			prs.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		return prs, nil

	case pg_query.ObjectType_OBJECT_TSTEMPLATE:
		var funcs []string
		for _, key := range []string{"init", "lexize"} {
			if fnSchema, fnName := defElemQualifiedName(ds.Definition, key); fnName != "" {
				funcs = append(funcs, joinQualName(fnSchema, fnName))
			}
		}
		tmpl := &TSTemplate{Schema: schema, Name: name, Functions: funcs, Body: rawBody, SrcPos: pos}
		if block.Comment != nil {
			tmpl.Comment = &block.Comment.Value
		}
		if block.RenamedFrom != nil {
			tmpl.RenamedFrom = &block.RenamedFrom.Name
			tmpl.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		return tmpl, nil
	}

	return &OpaqueObject{kind: ds.Kind.String(), body: name, SrcPos: pos}, nil
}

// ── Default Privileges ────────────────────────────────────────────────────────

// BuildDefaultPrivileges implements pipeline.IRBuilder. See
// pipeline.DefaultPrivilegesBlock for why this bypasses the normal
// pg_query-parsed-Part-1 path entirely. Splits into one *DefaultPrivileges
// per distinct object type named across the block's grants/revocations,
// matching pg_default_acl's real one-row-per-(role,schema,objtype) model —
// the RFC's own worked example declares TABLES, FUNCTIONS, and SEQUENCES
// together in one block, which must become three independently-diffable
// objects, not one.
func (b *Builder) BuildDefaultPrivileges(block pipeline.DefaultPrivilegesBlock) ([]pipeline.IRObject, error) {
	var forRole *string
	if block.ForRole != nil {
		s := block.ForRole.String()
		forRole = &s
	}
	var inSchema *string
	if block.InSchema != nil {
		s := block.InSchema.String()
		inSchema = &s
	}

	byType := make(map[string]*DefaultPrivileges)
	var order []string
	get := func(objType string) *DefaultPrivileges {
		if dp, ok := byType[objType]; ok {
			return dp
		}
		dp := &DefaultPrivileges{ForRole: forRole, InSchema: inSchema, ObjectType: objType, SrcPos: block.Pos}
		byType[objType] = dp
		order = append(order, objType)
		return dp
	}
	for _, g := range block.Grants {
		dp := get(g.ObjectType)
		dp.Grants = append(dp.Grants, Grant{Privileges: g.Privileges, WithGrant: g.WithGrant, Pos: g.Pos, Roles: identifierStrings(g.Roles)})
	}
	for _, r := range block.Revocations {
		dp := get(r.ObjectType)
		dp.Revocations = append(dp.Revocations, Revocation{Privileges: r.Privileges, Cascade: r.Cascade, Pos: r.Pos, Roles: identifierStrings(r.Roles)})
	}

	objs := make([]pipeline.IRObject, 0, len(order))
	for _, t := range order {
		objs = append(objs, byType[t])
	}
	return objs, nil
}

// BuildParameterPrivileges converts a parsed PARAMETER PRIVILEGES block into
// its single IRObject. See ParameterPrivileges's doc comment for why this,
// unlike BuildDefaultPrivileges, never splits into multiple objects.
func (b *Builder) BuildParameterPrivileges(block pipeline.ParameterPrivilegesBlock) ([]pipeline.IRObject, error) {
	pp := &ParameterPrivileges{SrcPos: block.Pos}
	for _, g := range block.Grants {
		pp.Grants = append(pp.Grants, ParameterGrant{
			Privileges: g.Privileges,
			Parameters: identifierStrings(g.Parameters),
			Roles:      identifierStrings(g.Roles),
			WithGrant:  g.WithGrant,
			GrantedBy:  g.GrantedBy,
			Pos:        g.Pos,
		})
	}
	for _, r := range block.Revocations {
		pp.Revocations = append(pp.Revocations, ParameterRevocation{
			Privileges: r.Privileges,
			Parameters: identifierStrings(r.Parameters),
			Roles:      identifierStrings(r.Roles),
			Cascade:    r.Cascade,
			GrantedBy:  r.GrantedBy,
			Pos:        r.Pos,
		})
	}
	return []pipeline.IRObject{pp}, nil
}

func identifierStrings(ids []pipeline.Identifier) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// ── Cast ──────────────────────────────────────────────────────────────────────

func (b *Builder) buildCast(cs *pg_query.CreateCastStmt, block pipeline.BlockAST, pos pipeline.SourcePos, body string) (pipeline.IRObject, error) {
	c := &Cast{Body: body, SrcPos: pos}
	if cs.Sourcetype != nil {
		c.SourceType = typeNameToRef(cs.Sourcetype)
	}
	if cs.Targettype != nil {
		c.TargetType = typeNameToRef(cs.Targettype)
	}
	switch {
	case cs.Inout:
		c.Method = "i"
	case cs.Func != nil:
		c.Method = "f"
	default:
		c.Method = "b"
	}
	switch cs.Context {
	case pg_query.CoercionContext_COERCION_ASSIGNMENT:
		c.Context = "a"
	case pg_query.CoercionContext_COERCION_IMPLICIT:
		c.Context = "i"
	default:
		// COERCION_CONTEXT_UNDEFINED (pg_query's zero value for an unset
		// field): real PostgreSQL grammar has no "AS EXPLICIT" clause — the
		// absence of any AS clause is what "explicit-only" means, matching
		// pg_cast.castcontext's own "e" catalog code.
		c.Context = "e"
	}
	if cs.Func != nil {
		funcSchema, funcName := extractTypeName(cs.Func.Objname)
		c.Function = funcName
		if funcSchema != "" {
			c.Function = funcSchema + "." + funcName
		}
	}
	if block.Comment != nil {
		c.Comment = &block.Comment.Value
	}
	return c, nil
}

// ── opaque fallback ───────────────────────────────────────────────────────────

// OpaqueObject stores any IR object that doesn't have a dedicated concrete type yet.
type OpaqueObject struct {
	kind   string
	body   string
	SrcPos pipeline.SourcePos
}

func (o *OpaqueObject) QualifiedName() string   { return o.body }
func (o *OpaqueObject) Pos() pipeline.SourcePos { return o.SrcPos }
func (o *OpaqueObject) irObject()               {}

// rawSQL deparsed a single node back to SQL, returning "" on error.
func rawSQL(node *pg_query.Node) string {
	pr := &pg_query.ParseResult{Stmts: []*pg_query.RawStmt{{Stmt: node}}}
	sql, err := pg_query.Deparse(pr)
	if err != nil {
		return ""
	}
	return sql
}

// buildSubscription constructs a Subscription from its native CONNECTION
// literal (may contain {{secret-uri}} placeholders, resolved only at apply
// time — see pipeline.ResolveTemplate) and the { } block's COMMENT, if any.
func (b *Builder) buildSubscription(stmt *pg_query.CreateSubscriptionStmt, block pipeline.BlockAST, pos pipeline.SourcePos, sql string) (pipeline.IRObject, error) {
	sub := &Subscription{Name: stmt.Subname, ConnInfo: stmt.Conninfo, Body: sql, SrcPos: pos}
	if block.Comment != nil {
		sub.Comment = &block.Comment.Value
	}
	if block.RenamedFrom != nil {
		sub.RenamedFrom = &block.RenamedFrom.Name
	}
	if block.Owner != nil {
		sub.Owner = &block.Owner.Name
	}
	sub.SecurityLabels = block.SecurityLabels
	return sub, nil
}

func (b *Builder) buildOpaque(node *pg_query.Node, block pipeline.BlockAST, pos pipeline.SourcePos, kind string) (pipeline.IRObject, error) {
	sql := rawSQL(node)
	switch n := node.Node.(type) {
	case *pg_query.Node_CreatePublicationStmt:
		pub := &Publication{
			Name: n.CreatePublicationStmt.Pubname, AllTables: n.CreatePublicationStmt.ForAllTables,
			// PostgreSQL's own default when WITH (publish = ...) is
			// omitted entirely — confirmed via pg_publication.pubinsert
			// etc., all NOT NULL and true by default — not a DPG-invented
			// default.
			Insert: true, Update: true, Delete: true, Truncate: true,
			Body: sql, SrcPos: pos,
		}
		if block.Comment != nil {
			pub.Comment = &block.Comment.Value
		}
		if block.Owner != nil {
			pub.Owner = &block.Owner.Name
		}
		for _, obj := range n.CreatePublicationStmt.Pubobjects {
			spec := obj.GetPublicationObjSpec()
			if spec == nil || spec.Pubobjtype != pg_query.PublicationObjSpecType_PUBLICATIONOBJ_TABLE {
				continue
			}
			pubtable := spec.GetPubtable()
			rv := pubtable.GetRelation()
			if rv == nil || rv.Relname == "" {
				continue
			}
			pub.Tables = append(pub.Tables, PublicationTableRef{Schema: rv.Schemaname, Name: rv.Relname})
			if pubtable.GetWhereClause() != nil || len(pubtable.GetColumns()) > 0 {
				pub.HasFilteredTables = true
			}
		}
		for _, o := range n.CreatePublicationStmt.Options {
			de := o.GetDefElem()
			if de == nil || de.Defname != "publish" {
				continue
			}
			sv := de.GetArg().GetString_()
			if sv == nil {
				continue
			}
			// PostgreSQL normalises this to a comma-separated list, e.g.
			// "insert, update" (confirmed via pg_query.Parse probe) —
			// explicitly specifying WITH (publish = ...) at all means only
			// the listed operations are enabled, so every operation not
			// named must be turned off from the all-true default above.
			pub.Insert, pub.Update, pub.Delete, pub.Truncate = false, false, false, false
			for op := range strings.SplitSeq(sv.Sval, ",") {
				switch strings.TrimSpace(op) {
				case "insert":
					pub.Insert = true
				case "update":
					pub.Update = true
				case "delete":
					pub.Delete = true
				case "truncate":
					pub.Truncate = true
				}
			}
		}
		pub.SecurityLabels = block.SecurityLabels
		if block.RenamedFrom != nil {
			pub.RenamedFrom = &block.RenamedFrom.Name
		}
		return pub, nil
	case *pg_query.Node_CreateSubscriptionStmt:
		return b.buildSubscription(n.CreateSubscriptionStmt, block, pos, sql)
	case *pg_query.Node_CreateEventTrigStmt:
		funcSchema, funcName := extractTypeName(n.CreateEventTrigStmt.Funcname)
		function := funcName
		if funcSchema != "" {
			function = funcSchema + "." + funcName
		}
		evt := &EventTrigger{
			Name:     n.CreateEventTrigStmt.Trigname,
			Event:    n.CreateEventTrigStmt.Eventname,
			Function: function,
			Body:     sql, SrcPos: pos,
		}
		// WHEN TAG IN (...) is a single DefElem (Defname == "tag") whose Arg
		// is a List of String nodes — confirmed via pg_query.Parse probe
		// against a real "WHEN TAG IN (...)" clause, not assumed.
		for _, w := range n.CreateEventTrigStmt.Whenclause {
			de := w.GetDefElem()
			if de == nil || de.Defname != "tag" {
				continue
			}
			lst := de.GetArg().GetList()
			if lst == nil {
				continue
			}
			for _, item := range lst.Items {
				if sv := item.GetString_(); sv != nil {
					evt.Tags = append(evt.Tags, sv.Sval)
				}
			}
		}
		if block.Comment != nil {
			evt.Comment = &block.Comment.Value
		}
		if block.Owner != nil {
			evt.Owner = &block.Owner.Name
		}
		if block.RenamedFrom != nil {
			evt.RenamedFrom = &block.RenamedFrom.Name
		}
		evt.EnableState = block.TriggerEnableState
		evt.SecurityLabels = block.SecurityLabels
		return evt, nil
	case *pg_query.Node_CreateOpClassStmt:
		schema, name := extractTypeName(n.CreateOpClassStmt.Opclassname)
		famSchema, famName := extractTypeName(n.CreateOpClassStmt.Opfamilyname)
		// itemtype 2 == OPCLASS_ITEM_FUNCTION (parsenodes.h; 1 == OPERATOR,
		// 3 == STORAGETYPE — pg_query exposes no named constant, confirmed
		// against PostgreSQL's own C source, not assumed from a probe alone).
		const (
			opclassItemOperator    = 1
			opclassItemFunction    = 2
			opclassItemStorageType = 3
		)
		var functions []string
		var members []pipeline.OpFamilyMember
		var storageType string
		for _, item := range n.CreateOpClassStmt.Items {
			it := item.GetCreateOpClassItem()
			if it == nil {
				continue
			}
			switch it.Itemtype {
			case opclassItemFunction:
				if it.Name == nil {
					continue
				}
				fnSchema, fnName := extractTypeName(it.Name.Objname)
				if fnSchema != "" {
					functions = append(functions, fnSchema+"."+fnName)
				} else {
					functions = append(functions, fnName)
				}
				members = append(members, opClassFunctionMember(it, n.CreateOpClassStmt.Datatype, pos))
			case opclassItemOperator:
				if it.Name == nil {
					continue
				}
				members = append(members, opClassOperatorMember(it, n.CreateOpClassStmt.Datatype, pos))
			case opclassItemStorageType:
				if it.Storedtype != nil {
					storageType = typeNameToRef(it.Storedtype).String()
				}
			}
		}
		opc := &OperatorClass{
			Schema: schema, Name: name, AccessMethod: n.CreateOpClassStmt.Amname,
			FamilySchema: famSchema, FamilyName: famName, Functions: functions,
			Members: members, StorageType: storageType,
			Body: sql, SrcPos: pos,
		}
		if block.Comment != nil {
			opc.Comment = &block.Comment.Value
		}
		if block.RenamedFrom != nil {
			opc.RenamedFrom = &block.RenamedFrom.Name
			opc.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		return opc, nil
	case *pg_query.Node_CreateOpFamilyStmt:
		schema, name := extractTypeName(n.CreateOpFamilyStmt.Opfamilyname)
		opf := &OperatorFamily{Schema: schema, Name: name, AccessMethod: n.CreateOpFamilyStmt.Amname, Body: sql, SrcPos: pos}
		if block.Comment != nil {
			opf.Comment = &block.Comment.Value
		}
		if block.RenamedFrom != nil {
			opf.RenamedFrom = &block.RenamedFrom.Name
			opf.RenamedFromSchema = renamedFromSchema(block.RenamedFrom)
		}
		members, err := normalizeOpFamilyMembers(block.OpFamilyMembers)
		if err != nil {
			return nil, err
		}
		opf.Members = members
		return opf, nil
	}
	return &OpaqueObject{kind: kind, body: kind, SrcPos: pos}, nil
}

// normalizeOpFamilyMembers canonicalizes and validates an OPERATOR FAMILY
// { } block's raw parsed members (RFC Section 14.4) into their final IR form:
// op_types run through ParseTypeText so a hand-written "int4" compares equal
// to introspection's canonical "integer", and duplicate catalog slots
// (same Key(), see pipeline.OpFamilyMember.Key's doc comment) are rejected
// at compile time rather than surfacing only as a live "ALTER OPERATOR
// FAMILY... ADD" error on apply.
func normalizeOpFamilyMembers(raw []pipeline.OpFamilyMember) ([]pipeline.OpFamilyMember, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]pipeline.SourcePos, len(raw))
	members := make([]pipeline.OpFamilyMember, len(raw))
	for i, m := range raw {
		kind := "OPERATOR"
		if m.IsFunction {
			kind = "FUNCTION"
		}
		if m.Number <= 0 {
			return nil, pipeline.Errorf(m.Pos, "%s member's strategy/support number must be positive, got %d", kind, m.Number)
		}
		m.LeftType = ParseTypeText(m.LeftType).String()
		m.RightType = ParseTypeText(m.RightType).String()
		if m.IsFunction {
			// The optional "(op_type[, op_type])" was already resolved into
			// LeftType/RightType by the parser when the user wrote it; when
			// omitted, PostgreSQL's own documented default (the function's
			// own input types) is only correct for a 2-argument function —
			// for any other arity the true amproclefttype/amprocrighttype
			// is the opclass's OWN input type, not derivable from the
			// function's signature at all, so require it explicitly rather
			// than silently guess wrong.
			if m.LeftType == "" && m.RightType == "" {
				if len(m.FuncArgs) != 2 {
					return nil, pipeline.Errorf(m.Pos,
						"FUNCTION %d %s: op_type must be given explicitly as (op_type, op_type) — it can only be inferred from the function's own arguments when it takes exactly 2, this one takes %d",
						m.Number, m.Name.String(), len(m.FuncArgs))
				}
				m.LeftType = ParseTypeText(m.FuncArgs[0]).String()
				m.RightType = ParseTypeText(m.FuncArgs[1]).String()
			}
			for j, a := range m.FuncArgs {
				m.FuncArgs[j] = ParseTypeText(a).String()
			}
		}
		key := m.Key()
		if prev, ok := seen[key]; ok {
			return nil, pipeline.Errorf(m.Pos,
				"duplicate operator family member for slot %s %d (%s, %s) — already declared at %s",
				kind, m.Number, m.LeftType, m.RightType, prev)
		}
		seen[key] = m.Pos
		members[i] = m
	}
	return members, nil
}

// opClassOperandType converts an explicit "(op_type, op_type)" node pair from
// a CreateOpClassItem into canonical left/right type strings; when types
// weren't given explicitly, both default to the operator class's own
// Datatype — the behavior documented for CREATE OPERATOR CLASS's OPERATOR and
// FUNCTION items (unlike ALTER OPERATOR FAMILY ADD, which has no enclosing
// class datatype to fall back to, see normalizeOpFamilyMembers).
//
// Run through ParseTypeText, same as normalizeOpFamilyMembers's own
// LeftType/RightType canonicalization — required by OpFamilyMember.Key's own
// doc comment ("a same-type member written as int4 on one side and integer
// on the other will misdiff"). Missing here meant a class's own AS-list
// members (unlike a standalone OPERATOR FAMILY block's, which does go
// through normalizeOpFamilyMembers) never matched introspection's always-
// canonical `::regtype::text` output, so opClassMembersEqual always found
// them "different" — silently masked pre-C.5-fix because diffOperatorClass
// used to fall through to diffOpaqueIR's live-BodyHash blind spot regardless
// (RFC audit item C.5's fix uncovered this as a real, separate live-drift
// false positive: `plan --live` on an untouched operator class proposed a
// spurious DROP+CREATE using "int4" vs the class's declared "int4" only
// because the roundtrip through the catalog normalizes it to "integer").
func opClassOperandType(explicit []*pg_query.Node, datatype *pg_query.TypeName) (left, right string) {
	if len(explicit) == 2 {
		return ParseTypeText(typeNameToRef(explicit[0].GetTypeName()).String()).String(),
			ParseTypeText(typeNameToRef(explicit[1].GetTypeName()).String()).String()
	}
	d := ParseTypeText(typeNameToRef(datatype).String()).String()
	return d, d
}

// opClassOperatorMember converts an OPCLASS_ITEM_OPERATOR item into the same
// structured pipeline.OpFamilyMember shape ALTER OPERATOR FAMILY ADD members
// use — the two are catalog-identical (pg_amop has no notion of "belongs to
// a class" vs "belongs to a family directly").
func opClassOperatorMember(it *pg_query.CreateOpClassItem, datatype *pg_query.TypeName, pos pipeline.SourcePos) pipeline.OpFamilyMember {
	schema, name := extractTypeName(it.Name.Objname)
	left, right := opClassOperandType(it.Name.Objargs, datatype)
	m := pipeline.OpFamilyMember{
		Number: int(it.Number), Name: pipeline.Identifier{Schema: schema, Name: name},
		LeftType: left, RightType: right, Pos: pos,
	}
	if len(it.OrderFamily) > 0 {
		m.OrderBy = true
		sfSchema, sfName := extractTypeName(it.OrderFamily)
		m.SortFamily = pipeline.Identifier{Schema: sfSchema, Name: sfName}
	}
	return m
}

// opClassFunctionMember is opClassOperatorMember's OPCLASS_ITEM_FUNCTION
// counterpart. ClassArgs (not Name.Objargs) carries the item's explicit
// "(op_type, op_type)" when given — that pair is the support function's
// applicable operand types, a separate concept from Name.Objargs (the
// function's own argument-type signature, captured here as FuncArgs).
func opClassFunctionMember(it *pg_query.CreateOpClassItem, datatype *pg_query.TypeName, pos pipeline.SourcePos) pipeline.OpFamilyMember {
	schema, name := extractTypeName(it.Name.Objname)
	left, right := opClassOperandType(it.ClassArgs, datatype)
	funcArgs := make([]string, len(it.Name.Objargs))
	for i, a := range it.Name.Objargs {
		// Canonicalized for the same reason opClassOperandType's LeftType/
		// RightType are — introspection's fn_args always comes out of
		// format_type(), so an uncanonicalized "int4" here would never
		// match its "integer".
		funcArgs[i] = ParseTypeText(typeNameToRef(a.GetTypeName()).String()).String()
	}
	return pipeline.OpFamilyMember{
		IsFunction: true, Number: int(it.Number), Name: pipeline.Identifier{Schema: schema, Name: name},
		LeftType: left, RightType: right, FuncArgs: funcArgs, Pos: pos,
	}
}

// ── conversion helpers ────────────────────────────────────────────────────────

func rangeVarSchema(rv *pg_query.RangeVar) string {
	if rv == nil {
		return ""
	}
	return rv.Schemaname
}

// nodeToText produces a best-effort text representation of a pg_query Node.
// For simple cases (string literals, identifiers) it returns the exact value.
// For complex expressions it returns the pg_query JSON representation (debug only).
func nodeToText(n *pg_query.Node) string {
	if n == nil {
		return ""
	}
	if sv := n.GetString_(); sv != nil {
		return sv.Sval
	}
	if ic := n.GetInteger(); ic != nil {
		return fmt.Sprintf("%d", ic.Ival)
	}
	if fc := n.GetFloat(); fc != nil {
		return fc.Fval
	}
	if bv := n.GetBoolean(); bv != nil {
		if bv.Boolval {
			return "true"
		}
		return "false"
	}
	// A_Const: typed literal constant (string, int, float, boolean, null).
	if ac := n.GetAConst(); ac != nil {
		if ac.GetIsnull() {
			return "NULL"
		}
		if sv := ac.GetSval(); sv != nil {
			return "'" + sv.Sval + "'"
		}
		if iv := ac.GetIval(); iv != nil {
			return fmt.Sprintf("%d", iv.Ival)
		}
		if fv := ac.GetFval(); fv != nil {
			return fv.Fval
		}
		if bv := ac.GetBoolval(); bv != nil {
			if bv.Boolval {
				return "true"
			}
			return "false"
		}
	}
	// For expression nodes (FuncCall, TypeCast, etc.) deparse by wrapping in
	// SELECT so pg_query sees a full statement, then strip the SELECT prefix.
	selectStmt := &pg_query.Node{
		Node: &pg_query.Node_SelectStmt{
			SelectStmt: &pg_query.SelectStmt{
				TargetList: []*pg_query.Node{
					{Node: &pg_query.Node_ResTarget{
						ResTarget: &pg_query.ResTarget{Val: n},
					}},
				},
			},
		},
	}
	pr := &pg_query.ParseResult{
		Stmts: []*pg_query.RawStmt{{Stmt: selectStmt}},
	}
	if sql, err := pg_query.Deparse(pr); err == nil {
		if after, ok := strings.CutPrefix(sql, "SELECT "); ok {
			return after
		}
		return sql
	}
	return "<expr>"
}

// fmt is imported for Sprintf in nodeToText; declare the import.

// checkExprSingleColumn returns the single column a CHECK expression
// references, if it references exactly one distinct column — mirroring
// PostgreSQL's own default-name selection for CHECK constraints (heap.c's
// AddRelationNewConstraints: pull_var_clause(expr) then list_union to dedup;
// nil unless exactly one Var remains). Returns nil for zero or multiple
// distinct columns, or when n is nil.
func checkExprSingleColumn(n *pg_query.Node) *string {
	if n == nil {
		return nil
	}
	seen := make(map[string]bool)
	walkColumnRefs(n.ProtoReflect(), seen)
	if len(seen) != 1 {
		return nil
	}
	for name := range seen {
		return &name
	}
	return nil
}

// walkColumnRefs recursively visits every populated message field (including
// repeated ones) reachable from m, recording the final (rightmost — i.e. the
// unqualified column name, since a CHECK expression's ColumnRef has no table
// qualifier) Fields entry of every pg_query.ColumnRef it finds. Walking
// generically via protobuf reflection, rather than special-casing every
// expression node type (FuncCall args, CASE, COALESCE, BoolExpr, TypeCast,
// nested A_Expr, ...) by hand, is what makes this equivalent to PostgreSQL's
// own pull_var_clause for any CHECK expression that (like the vast majority
// of real-world constraints) references only its own table's columns.
func walkColumnRefs(m protoreflect.Message, seen map[string]bool) {
	if !m.IsValid() {
		return
	}
	if cr, ok := m.Interface().(*pg_query.ColumnRef); ok {
		if names := nodeListToNames(cr.Fields); len(names) > 0 {
			seen[names[len(names)-1]] = true
		}
		return
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		if fd.IsList() {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				walkColumnRefs(list.Get(i).Message(), seen)
			}
			return true
		}
		walkColumnRefs(v.Message(), seen)
		return true
	})
}

func blockGrantToIR(g pipeline.GrantEntry) Grant {
	gr := Grant{WithGrant: g.WithGrant, Privileges: g.Privileges, GrantedBy: g.GrantedBy, Pos: g.Pos}
	for _, r := range g.Roles {
		gr.Roles = append(gr.Roles, r.String())
	}
	return gr
}

func blockRevocationToIR(r pipeline.RevocationEntry) Revocation {
	rev := Revocation{Cascade: r.Cascade, Privileges: r.Privileges, GrantedBy: r.GrantedBy, Pos: r.Pos}
	for _, role := range r.Roles {
		rev.Roles = append(rev.Roles, role.String())
	}
	return rev
}

func blockIndexToIR(idx pipeline.IndexDef) *Index {
	ir := &Index{
		Name:         idx.Name.Name,
		Unique:       idx.Unique,
		Concurrently: idx.Concurrently,
		Only:         idx.Only,
		Columns:      idx.Columns,
		Pos:          idx.Pos,
	}
	if idx.Method != nil {
		ir.Method = idx.Method.Name
	} else {
		ir.Method = "btree"
	}
	if idx.Where != nil {
		ir.Where = &idx.Where.Text
	}
	for _, inc := range idx.Include {
		ir.Include = append(ir.Include, inc.Name)
	}
	ir.NullsNotDistinct = idx.NullsNotDistinct
	ir.With = idx.With
	if idx.Tablespace != nil {
		ir.Tablespace = &idx.Tablespace.Name
	}
	if idx.Comment != nil {
		ir.Comment = &idx.Comment.Value
	}
	if idx.RenamedFrom != nil {
		ir.RenamedFrom = &idx.RenamedFrom.Name
	}
	return ir
}

func blockPolicyToIR(pol pipeline.PolicyDef) *Policy {
	p := &Policy{
		Name:       pol.Name.Name,
		Command:    pol.Command,
		Permissive: pol.Permissive,
		Pos:        pol.Pos,
	}
	if pol.Using != nil {
		p.Using = &pol.Using.Text
	}
	if pol.WithCheck != nil {
		p.WithCheck = &pol.WithCheck.Text
	}
	for _, r := range pol.Roles {
		p.Roles = append(p.Roles, r.String())
	}
	if pol.RenamedFrom != nil {
		p.RenamedFrom = &pol.RenamedFrom.Name
	}
	if pol.Comment != nil {
		p.Comment = &pol.Comment.Value
	}
	return p
}

func blockTriggerToIR(tr pipeline.TriggerDef) *Trigger {
	t := &Trigger{
		Name:                tr.Name.Name,
		When:                tr.When,
		Events:              tr.Events,
		ForEach:             tr.ForEach,
		UpdateOfColumns:     tr.UpdateOfColumns,
		OldTransitionName:   tr.OldTransitionName,
		NewTransitionName:   tr.NewTransitionName,
		Args:                tr.Args,
		EnableState:         tr.EnableState,
		DependsOnExtensions: tr.DependsOnExtensions,
		Pos:                 tr.Pos,
	}
	t.Function = tr.Function.String()
	if tr.Condition != nil {
		t.Condition = &tr.Condition.Text
	}
	if tr.RenamedFrom != nil {
		t.RenamedFrom = &tr.RenamedFrom.Name
	}
	if tr.Comment != nil {
		t.Comment = &tr.Comment.Value
	}
	return t
}

// ── VirtualType ───────────────────────────────────────────────────────────────

// buildVirtualType parses a VIRTUAL TYPE declaration from the raw Part1 text.
// Part1 format: [schema.]name AS body
func (b *Builder) buildVirtualType(part1 string, block pipeline.BlockAST, pos pipeline.SourcePos, pg pipeline.PGParseResult) (*VirtualType, error) {
	// Find the standalone AS keyword by scanning word-by-word.
	upper := strings.ToUpper(part1)
	asIdx := -1
	for i := 0; i < len(upper); {
		for i < len(upper) && isWS(upper[i]) {
			i++
		}
		if i >= len(upper) {
			break
		}
		if isWordChar(upper[i]) {
			start := i
			for i < len(upper) && isWordChar(upper[i]) {
				i++
			}
			if upper[start:i] == "AS" {
				asIdx = start
				break
			}
		} else {
			i++ // skip non-word characters (e.g. '.')
		}
	}
	if asIdx < 0 {
		return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: expected AS keyword in %q", part1)
	}

	namePart := strings.TrimSpace(part1[:asIdx])
	bodyText := strings.TrimSpace(part1[asIdx+2:]) // skip "AS"

	// Parse the name (possibly schema-qualified: schema.name).
	var schema, name string
	if dotIdx := strings.LastIndex(namePart, "."); dotIdx >= 0 {
		schema = foldIdentPart(namePart[:dotIdx])
		name = foldIdentPart(namePart[dotIdx+1:])
	} else {
		name = foldIdentPart(namePart)
	}
	if schema != "" && pg.SchemaContext != "" && schema != pg.SchemaContext {
		return nil, pipeline.ErrorfCode(pos, "DPG-E040",
			"VIRTUAL TYPE %s.%s is declared inside schema %s; inside a schema scope, a qualifier may only name that schema",
			schema, name, pg.SchemaContext)
	}
	if schema == "" {
		schema = effectiveSchema(pg)
	}

	body, err := parseVtypeBody(bodyText, pos)
	if err != nil {
		return nil, err
	}

	// Section 5.6: the { } block accepts only COMMENT and
	// PREFERRED JSON FORMAT (plus Name Maps, Appendix D.2's
	// cross-cutting directive available on every object block). Any
	// other directive is DPG-E015.
	if block.Owner != nil || block.RenamedFrom != nil || block.Protected ||
		block.Deprecated != nil || block.DropCascade ||
		len(block.Indices) > 0 || len(block.Policies) > 0 || len(block.Triggers) > 0 ||
		len(block.Grants) > 0 || len(block.Revocations) > 0 || len(block.SecurityLabels) > 0 ||
		len(block.Columns) > 0 || len(block.Constraints) > 0 ||
		block.EnableRLS || block.ForceRLS || block.Partitions != nil ||
		block.MigrateRemove != nil || len(block.DefaultPrivileges) > 0 ||
		len(block.Mappings) > 0 || len(block.OpFamilyMembers) > 0 ||
		block.DomainDefault != nil || block.DomainNotNull ||
		len(block.DependsOnExtensions) > 0 {
		return nil, pipeline.ErrorfCode(pos, "DPG-E015",
			"VIRTUAL TYPE %s: the { } block only accepts COMMENT and PREFERRED JSON FORMAT", namePart)
	}

	vt := &VirtualType{
		Schema:     schema,
		Name:       name,
		Body:       body,
		JsonFormat: block.PreferredJsonFormat,
		SrcPos:     pos,
	}
	if block.Comment != nil {
		vt.Comment = &block.Comment.Value
	}
	vt.NameMaps = block.NameMaps
	return vt, nil
}

// parseVtypeBody parses the body expression of a VIRTUAL TYPE declaration.
// Grammar:
//
//	vtype-body  = vtype-union
//	vtype-union = vtype-term *( "|" vtype-term )
//	vtype-term  = vtype-composite | vtype-typeref
//	vtype-composite = "(" vtype-field *( "," vtype-field ) ")"
//	vtype-field = identifier vtype-typeref
//	vtype-typeref = [ schema "." ] name [ "[]" ]
func parseVtypeBody(s string, pos pipeline.SourcePos) (VtypeBody, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: body must not be empty")
	}
	members, err := splitVtypeUnion(s, pos)
	if err != nil {
		return nil, err
	}
	if len(members) == 1 {
		return parseVtypeTerm(members[0], pos)
	}
	union := VtypeUnion{Members: make([]VtypeBody, 0, len(members))}
	for _, m := range members {
		term, err := parseVtypeTerm(m, pos)
		if err != nil {
			return nil, err
		}
		union.Members = append(union.Members, term)
	}
	return union, nil
}

// splitVtypeUnion splits a vtype body string by | at parenthesis depth 0.
func splitVtypeUnion(s string, pos pipeline.SourcePos) ([]string, error) {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: unmatched ')' in body")
			}
		case '|':
			if depth == 0 {
				part := strings.TrimSpace(s[start:i])
				if part == "" {
					return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: empty union member")
				}
				parts = append(parts, part)
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: unclosed '(' in body")
	}
	last := strings.TrimSpace(s[start:])
	if last == "" {
		return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: empty union member")
	}
	parts = append(parts, last)
	return parts, nil
}

// parseVtypeTerm parses a single union member: composite or type ref.
func parseVtypeTerm(s string, pos pipeline.SourcePos) (VtypeBody, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") {
		return parseVtypeComposite(s, pos)
	}
	return parseVtypeTypeRef(s, pos)
}

// parseVtypeComposite parses "(field1 TYPE1, field2 TYPE2, ...)".
func parseVtypeComposite(s string, pos pipeline.SourcePos) (VtypeComposite, error) {
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return VtypeComposite{}, pipeline.Errorf(pos, "VIRTUAL TYPE: composite body must be wrapped in parentheses, got %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return VtypeComposite{}, pipeline.Errorf(pos, "VIRTUAL TYPE: composite body must have at least one field")
	}
	fieldStrs, err := splitVtypeFields(inner, pos)
	if err != nil {
		return VtypeComposite{}, err
	}
	comp := VtypeComposite{Fields: make([]VtypeField, 0, len(fieldStrs))}
	for _, f := range fieldStrs {
		field, err := parseVtypeField(f, pos)
		if err != nil {
			return VtypeComposite{}, err
		}
		comp.Fields = append(comp.Fields, field)
	}
	return comp, nil
}

// splitVtypeFields splits composite fields by comma at depth 0.
func splitVtypeFields(s string, pos pipeline.SourcePos) ([]string, error) {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				part := strings.TrimSpace(s[start:i])
				if part == "" {
					return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: empty composite field")
				}
				parts = append(parts, part)
				start = i + 1
			}
		}
	}
	last := strings.TrimSpace(s[start:])
	if last == "" {
		return nil, pipeline.Errorf(pos, "VIRTUAL TYPE: empty composite field")
	}
	parts = append(parts, last)
	return parts, nil
}

// parseVtypeField parses "fieldname TypeRef".
func parseVtypeField(s string, pos pipeline.SourcePos) (VtypeField, error) {
	s = strings.TrimSpace(s)
	// Split on first whitespace: name is before, type is after.
	idx := strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t'
	})
	if idx < 0 {
		return VtypeField{}, pipeline.Errorf(pos, "VIRTUAL TYPE: field %q missing type", s)
	}
	fieldName := strings.TrimSpace(s[:idx])
	typeStr := strings.TrimSpace(s[idx+1:])
	if fieldName == "" {
		return VtypeField{}, pipeline.Errorf(pos, "VIRTUAL TYPE: empty field name")
	}
	typeRef, err := parseVtypeTypeRef(typeStr, pos)
	if err != nil {
		return VtypeField{}, err
	}
	return VtypeField{Name: strings.ToLower(fieldName), Type: typeRef}, nil
}

// parseVtypeTypeRef parses a type reference: [schema.]name[[]].
func parseVtypeTypeRef(s string, pos pipeline.SourcePos) (VtypeTypeRef, error) {
	s = strings.TrimSpace(s)
	isArray := false
	if strings.HasSuffix(s, "[]") {
		isArray = true
		s = strings.TrimSpace(s[:len(s)-2])
	}
	if s == "" {
		return VtypeTypeRef{}, pipeline.Errorf(pos, "VIRTUAL TYPE: empty type reference")
	}
	// Validate: only word chars and one optional dot for schema qualification.
	var schema, name string
	if dotIdx := strings.LastIndex(s, "."); dotIdx >= 0 {
		schema = s[:dotIdx]
		name = s[dotIdx+1:]
	} else {
		name = s
	}
	if name == "" {
		return VtypeTypeRef{}, pipeline.Errorf(pos, "VIRTUAL TYPE: empty type name in reference %q", s)
	}
	return VtypeTypeRef{Schema: strings.ToLower(schema), Name: strings.ToLower(name), IsArray: isArray}, nil
}

func isWS(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
