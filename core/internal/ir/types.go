// Package ir defines the fully-resolved Internal Representation of all DPG
// objects. Every IR value is fully qualified, schema-scoped, and source-annotated.
// IR types are produced by the IRBuilder (Phase 5) and consumed by the Merger,
// DependencyResolver, Differ, and Emitter.
package ir

import (
	"fmt"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// ── common helpers ────────────────────────────────────────────────────────────

// qualName formats a schema-qualified name.
func qualName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// ── type references ───────────────────────────────────────────────────────────

// TypeRef is a SQL type reference extracted from a pg_query TypeName node.
type TypeRef struct {
	Schema    string // empty for built-in types (pg_catalog)
	Name      string // e.g. "int4", "text", "varchar"
	Mods      string // raw typemod text, e.g. "(255)" for varchar(255)
	ArrayDims int    // number of [] dimensions (0 = scalar)
	// SetOf is only meaningful on a Function's ReturnType (PostgreSQL's
	// RETURNS SETOF <type>) — pg_query's TypeName carries this field
	// regardless of context, but the grammar only ever sets it true when
	// parsing a function's return type, so every other TypeRef consumer
	// (columns, casts, function arguments) sees it as permanently false.
	SetOf bool
}

func (t TypeRef) String() string {
	s := qualName(t.Schema, t.Name)
	if t.Mods != "" {
		// PostgreSQL's "... with/without time zone" types take their
		// precision immediately after the base keyword, not at the end of
		// the full name — "timestamp(3) with time zone" / "timestamp(3)
		// without time zone", never "...time zone(3)" (the latter is a
		// syntax error; confirmed live via format_type() against a real
		// column, for both qualifiers). Every other modifier-bearing type
		// (numeric, character varying, plain bit/varbit) has no trailing
		// qualifier, so appending at the end is correct for them. Checked
		// in this order deliberately: " with time zone" never appears as a
		// substring of "...without time zone" (the word is "without", not
		// "with" + "out"), so there's no ambiguity, but without checks both
		// since either can be present depending on PGCatalogName's mapping.
		idx := strings.Index(s, " with time zone")
		if idx < 0 {
			idx = strings.Index(s, " without time zone")
		}
		if idx >= 0 {
			s = s[:idx] + t.Mods + s[idx:]
		} else {
			s += t.Mods
		}
	}
	for i := 0; i < t.ArrayDims; i++ {
		s += "[]"
	}
	return s
}

// ── shared sub-types ──────────────────────────────────────────────────────────

// Column is one column in a table, view-column-list, or composite type.
type Column struct {
	Name        string
	RenamedFrom *string
	Type        TypeRef
	// VirtualType is the schema-qualified VIRTUAL TYPE this column or
	// composite attribute was declared with (RFC Section 5.6), array
	// dimensions included; nil for an ordinary type. Type then holds the
	// json/jsonb type PostgreSQL actually stores. Set by
	// ResolveVirtualTypes.
	VirtualType *TypeRef
	NotNull     bool
	Default     *string    // raw expression text
	Generated   *Generated // GENERATED ALWAYS AS
	Identity    *Identity  // GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY
	// Serial holds the SERIAL sugar kind ("SMALLSERIAL", "SERIAL", or
	// "BIGSERIAL") when this column was declared (or introspected as) one
	// of PostgreSQL's SERIAL pseudo-types. Type is always normalized to the
	// real underlying integer type (smallint/integer/bigint) — Serial is a
	// sibling marker, not an alternative to Type, mirroring how Identity is
	// a sibling marker to Type rather than a replacement for it. Default is
	// always nil when Serial is set (mirrors Identity's own Default==nil
	// convention): the auto-managed nextval() default is never represented
	// as an ir.Column scalar, on either the source-built or introspected
	// side, so the differ never has anything to compare there. NotNull is
	// always true when Serial is set (SERIAL implies NOT NULL in real
	// PostgreSQL, independent of any PRIMARY KEY). RFC Section 10: the backing
	// sequence itself is never declared in DPG and never appears as a
	// separate ir.Sequence object — introspectSequences's existing
	// deptype-'a'/'i'/'e' exclusion already (and correctly) excludes
	// SERIAL-owned sequences the same way it excludes IDENTITY-owned ones.
	Serial      *string
	Comment     *string
	Statistics  *int
	Compression *string
	Storage     *string
	// StorageIsTypeDefault is set only by introspection: true when Storage
	// equals the column's type's own default storage mode (pg_type.typstorage),
	// i.e. nobody ever explicitly overrode it. dump uses this to decide
	// whether rendering STORAGE would be a genuine declaration or just noise —
	// every real column has a concrete attstorage value, so Storage itself
	// can't tell "unset" apart from "set to the default" the way a nil
	// pointer normally would. Diffing is unaffected: diffColumns only acts
	// when the DESIRED side sets Storage, never based on this flag.
	StorageIsTypeDefault bool
	Deprecated           *string
	Using                *string // USING expression for ALTER COLUMN TYPE
	// Collation is a composite-type attribute's optional COLLATE target
	// (RFC Section 5.2, "attribute type-ref [COLLATE collation-name]"); ""
	// means unspecified/default. Table columns have no COLLATE feature in
	// DPG's grammar, so this is always "" when Column represents one —
	// diffColumns never reads it, only diffCompositeAttrs does.
	Collation      string
	Grants         []Grant
	Revocations    []Revocation
	SecurityLabels []pipeline.SecurityLabel
	NameMaps       []pipeline.NameMapEntry
	SrcPos         pipeline.SourcePos
}

// Generated holds a GENERATED ALWAYS AS (expr) STORED/VIRTUAL column spec.
type Generated struct {
	Expr string // the generating expression
	// Stored is true for STORED (computed on write, occupies physical
	// storage), false for VIRTUAL (computed on read, PostgreSQL 18+ — RFC
	// Section 7.2). The keyword itself is part of the generated column's
	// identity, not just a storage hint: switching between them requires
	// dropping and re-adding the column (real PostgreSQL has no ALTER path
	// that changes it in place — confirmed live: SET EXPRESSION leaves
	// pg_attribute.attgenerated unchanged), the same as any other identity
	// change, not treated as a mere property tweak.
	Stored bool
}

// Identity holds a GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY column spec.
type Identity struct {
	Always bool // true = ALWAYS, false = BY DEFAULT
	// Sequence options backing the identity column (nil = not declared,
	// PostgreSQL applies its own default) — RFC Section 7.2's
	// identity-opts grammar, the same option set CREATE SEQUENCE takes,
	// mirroring Sequence's identical fields below.
	IncrementBy *int64
	MinValue    *int64
	MaxValue    *int64
	StartValue  *int64
	Cache       *int64
	Cycle       *bool
	// NoMinValue/NoMaxValue distinguish an explicit "NO MINVALUE"/
	// "NO MAXVALUE" from "not mentioned at all", the same nil-means-
	// unspecified convention as Sequence.NoMinValue/NoMaxValue.
	NoMinValue, NoMaxValue bool
}

// Index is a CREATE INDEX / INDEXES entry.
type Index struct {
	Name             string
	Unique           bool
	Method           string // "btree" (default), "hash", "gin", "gist", etc.
	Columns          []pipeline.IndexColumn
	Where            *string // partial index predicate
	Include          []string
	NullsNotDistinct bool // UNIQUE ... NULLS NOT DISTINCT
	With             []pipeline.StorageParam
	Tablespace       *string
	Concurrently     bool
	// Only is RFC Section 7.7's ON ONLY prefix — suppresses recursion into
	// a partitioned table's own partitions. A one-time CREATE-time
	// modifier only, like Concurrently: PostgreSQL has no catalog column
	// recording whether a given index was originally created with ONLY
	// (its effect on whether child-partition indexes exist is a
	// consequence, not a stored flag), so introspection never sets this
	// and it deliberately has no SnapIndex counterpart — including it in
	// the definition-comparison diffIndexes runs would create permanent
	// spurious drift against every already-applied index declaring it.
	Only bool
	// RenamedFrom names the index's prior identity (RENAMED FROM, Section
	// 7.7) — matched within the same table's index list; a bare name, no
	// cross-schema form (see pipeline.IndexDef.RenamedFrom's doc comment).
	RenamedFrom *string
	Comment     *string
	Pos         pipeline.SourcePos
}

// Constraint is a table or column constraint.
type Constraint struct {
	Name    string
	Type    string // "PRIMARY KEY", "UNIQUE", "CHECK", "FOREIGN KEY", "EXCLUDE", "NOT NULL"
	Expr    string // raw constraint expression/definition
	Columns []string
	// NoInherit is PostgreSQL 18+'s NO INHERIT modifier on a table-level
	// named NOT NULL constraint (RFC Section 7.3) — scopes it to this table
	// only, the same modifier CHECK constraints already carry inline (via
	// no-inherit in table-constraint-body) but this codebase has never
	// actually captured for either kind before now. Only meaningful for
	// Type == "NOT NULL" currently.
	NoInherit bool
	// NotEnforced is PostgreSQL 18+'s NOT ENFORCED modifier (RFC Section
	// 7.2/7.3) — the constraint is catalogued and visible to tooling but
	// never checked by PostgreSQL itself. Only meaningful for Type ==
	// "CHECK" or "FOREIGN KEY"; zero-value (false) matches PostgreSQL's
	// own default (ENFORCED) when the clause is omitted.
	NotEnforced bool
	// WithoutOverlaps is PostgreSQL 18+'s WITHOUT OVERLAPS modifier
	// (RFC Section 7.3, SQL:2011 temporal keys) on the trailing column of
	// a PRIMARY KEY/UNIQUE's column list — that column (Columns' last
	// element) must already be a range or multirange type; there is no
	// DPG or PostgreSQL syntax to declare a generated one (see Section
	// 7.3's own note on this — a documented former RFC error). Only
	// meaningful for Type == "PRIMARY KEY" or "UNIQUE".
	WithoutOverlaps bool
	// FkWithPeriod/PkWithPeriod mark PostgreSQL 18+'s PERIOD modifier on a
	// temporal FOREIGN KEY's local/referenced trailing column respectively
	// (RFC Section 7.3) — the local column is Columns' last element, the
	// referenced one is RefColumns' last element. Only meaningful for
	// Type == "FOREIGN KEY". Real PostgreSQL requires both sides to carry
	// PERIOD together (confirmed live) — DPG mirrors that as two
	// independent fields rather than one shared flag only because that is
	// how PostgreSQL's own grammar (and pg_query's parse tree) represents
	// it, not because the two are ever meant to differ in practice.
	FkWithPeriod bool
	PkWithPeriod bool
	// CheckColumn is the single column PostgreSQL's own auto-naming
	// algorithm considers this CHECK constraint's own (heap.c's
	// AddRelationNewConstraints: pull_var_clause over the expression, then
	// nil unless exactly one distinct column remains after dedup). Set only
	// when Type == "CHECK" and the expression references exactly one
	// distinct column; nil for zero or multiple. Populated by an AST walk,
	// not syntactic position, so it matches PG's real (expression-based)
	// behavior even for a promoted column-level CHECK that references other
	// columns too. Used solely to reconstruct PG's generated constraint name
	// (see pgAutoConstraintName in internal/diff) — unrelated to Columns,
	// which createTable uses for a different purpose (inline-rendering
	// promotion signal).
	CheckColumn *string
	// Exclude holds the structured EXCLUDE definition when Type == "EXCLUDE"
	// (nil otherwise). Expr still carries the fully rendered SQL text (built
	// from this struct — see renderExclude) for CREATE/ALTER TABLE emission
	// and dump rendering, the same way every other constraint type's Expr
	// works; Exclude exists alongside it because the exclusion elements and
	// operators can't be losslessly recovered by re-parsing Expr's text, and
	// the EXCLUDE-naming reconciliation (see pgConstraintNameLabel/
	// predictName's "excl" case in internal/diff) needs the element list
	// directly, not a re-parse of rendered SQL.
	Exclude *ExcludeSpec
	// RefSchema/RefTable/RefColumns hold the structured REFERENCES target
	// when Type == "FOREIGN KEY" (all zero otherwise) — structured siblings
	// of Expr, the same "keep the rendered SQL text AND the structured data
	// separately" shape Exclude already uses. RefSchema is "" when the
	// reference was written unqualified in source (resolves against the
	// referencing table's own schema, same convention as ir.TypeRef.Schema).
	// Populated straight from the already-parsed Pktable/PkAttrs AST data at
	// build time; exists so consumers like the deprecated-reference lint
	// rule don't need to re-parse Expr's rendered SQL text to recover the FK
	// target.
	RefSchema  string
	RefTable   string
	RefColumns []string
	// DeleteSetColumns is ON DELETE SET NULL/SET DEFAULT's column list
	// (PostgreSQL 15+, RFC Section 7.2): only these referencing columns
	// are set. Empty means all of them. Also rendered into Expr.
	DeleteSetColumns []string
	// NullsNotDistinct is UNIQUE ... NULLS NOT DISTINCT (PostgreSQL 15+).
	// Also rendered into Expr.
	NullsNotDistinct  bool
	NotValid          bool
	Deferrable        bool
	InitiallyDeferred bool
	// RenamedFrom names the constraint's prior identity (RENAMED FROM,
	// Section 7.3) — see pipeline.ConstraintDef.RenamedFrom's doc comment.
	RenamedFrom *string
	Comment     *string
	Pos         pipeline.SourcePos
}

// ExcludeSpec is the structured body of an EXCLUDE constraint: PostgreSQL's
// `EXCLUDE [USING access_method] (element WITH operator [, ...]) [WHERE (predicate)]`.
type ExcludeSpec struct {
	AccessMethod string           // index access method, e.g. "gist"; "" means PG's default (btree)
	Elements     []ExcludeElement // one per "element WITH operator" pair, in source order
	Where        string           // raw WHERE predicate text (no surrounding parens); "" if absent
}

// ExcludeElement is one column-or-expression + operator pair inside an
// EXCLUDE constraint's element list. Mirrors PostgreSQL's IndexElem grammar
// (the same production CREATE INDEX uses), plus the WITH operator EXCLUDE
// adds on top.
type ExcludeElement struct {
	Column string // plain column name; "" if Expr is set instead
	Expr   string // parenthesization-free expression text; "" if Column is set instead
	// PredictedName is set when Expr's shape lets PostgreSQL's own naming
	// rule (FigureColnameInternal, parser/parse_target.c — called via
	// FigureIndexColname from parse_utilcmd.c's transformIndexStmt BEFORE
	// transformExpr runs, confirmed live and via source that this is
	// genuinely syntax-only, no catalog/OID resolution involved) be
	// reconstructed purely from the parsed tree: a bare column reference in
	// parens ("(a)" -> "a"), a bare top-level function call ("lower(a)" ->
	// "lower", schema stripped, NEVER descending into the call's own
	// arguments regardless of their complexity — confirmed live), a
	// NULLIF(a,b) call (-> the literal "nullif"), or a type cast (-> its
	// argument's own PredictedName if that one is itself "strong" —
	// ColumnRef or FuncCall — else the cast's OWN target type name, e.g.
	// "(a + b)::text" -> "text", confirmed live). "" for any other
	// expression shape (a bare, uncast operator expression, e.g. "a + b" —
	// this syntax-only pass genuinely can't derive anything from it, matching
	// real PostgreSQL's own FigureIndexColname). This is NOT PostgreSQL's
	// final answer for such a shape, though: ChooseIndexColumnNames
	// (indexcmds.c) falls back to the literal string "expr" (deduplicated
	// like any other repeated element name) whenever indexcolname is unset —
	// confirmed live against PG 17 — so an empty PredictedName here is
	// deliberately picked up downstream by pgAutoConstraintName's "excl" case
	// (internal/diff), not left unpredictable. Used solely to reconstruct
	// PostgreSQL's own auto-generated constraint name — extracted directly
	// from the parsed tree, never by re-parsing Expr's rendered text.
	PredictedName string
	Collation     string // optional COLLATE target; "" if unspecified
	OpClass       string // optional operator class; "" if unspecified (uses the type's default)
	SortOrder     string // "ASC", "DESC", or "" (unspecified)
	Nulls         string // "FIRST", "LAST", or "" (unspecified)
	Operator      string // the WITH operator, e.g. "=", "&&"
}

// Policy is a row-security policy.
type Policy struct {
	Name       string
	Command    string // "ALL", "SELECT", etc.
	Permissive bool
	Using      *string
	WithCheck  *string
	Roles      []string
	// RenamedFrom names the policy's prior identity (RENAMED FROM, RFC
	// Section 7.8) — matched within the same table's policy list only, like
	// Constraint/Index's identical sub-object RenamedFrom fields, not the
	// cross-schema-capable RenamedFromSchema pattern top-level kinds use
	// (PostgreSQL provides no mechanism to move a policy to another table).
	RenamedFrom *string
	Comment     *string
	Pos         pipeline.SourcePos
}

// Trigger is a trigger definition.
type Trigger struct {
	Name    string
	When    string   // "BEFORE", "AFTER", "INSTEAD OF"
	Events  []string // "INSERT", "UPDATE", "DELETE", "TRUNCATE"
	ForEach string   // "ROW", "STATEMENT"
	// UpdateOfColumns is RFC audit item #1's "UPDATE OF col1, col2, ..."
	// column list — nil when the UPDATE event has no OF clause (fires on
	// any column update, PostgreSQL's own default). A trigger scoped to
	// specific columns is semantically different from an unscoped one:
	// previously tokenized and explicitly discarded by the blockparser, so
	// a column-scoped trigger silently fired on every column update
	// instead of just the declared ones.
	UpdateOfColumns []string
	// OldTransitionName/NewTransitionName are RFC Section 7.9's "REFERENCING OLD
	// TABLE AS ... NEW TABLE AS ..." transition-table names (audit item
	// #2) — nil when REFERENCING isn't present, or for whichever of
	// OLD/NEW wasn't named. Mirrors UpdateOfColumns' optional-field
	// pattern above.
	OldTransitionName *string
	NewTransitionName *string
	Condition         *string
	Function          string // qualified function name
	Args              []string
	// EnableState — see pipeline.TriggerDef.EnableState's identical doc
	// comment.
	EnableState string
	// DependsOnExtensions is Section 9.1's `[NO] DEPENDS ON EXTENSION`
	// directive reused verbatim for triggers (Section 7.9, audit item
	// #75) — the complete desired set (at most one in practice, per the
	// grammar's non-repeatable placement, but modeled as a slice for
	// uniformity with Function/Procedure's identical field and to make
	// the differ's shared stringSetDiff-based helper reusable as-is).
	DependsOnExtensions []string
	// RenamedFrom names the trigger's prior identity (RENAMED FROM,
	// Section 7.9) — see pipeline.TriggerDef.RenamedFrom's doc comment.
	RenamedFrom *string
	Comment     *string
	Pos         pipeline.SourcePos
}

// Grant is a single GRANT directive.
type Grant struct {
	Privileges []string // nil = ALL
	Roles      []string
	WithGrant  bool
	// GrantedBy is RFC audit item #90's optional GRANTED BY role-spec — nil
	// when not declared (the overwhelming common case; real PostgreSQL
	// restricts the effective grantor to current_user in practice
	// regardless of what's named here). Diffed only when the desired side
	// explicitly declares it (see diffGrantSet) — never compared against a
	// live-introspected concrete grantor when unset, the same "declared, so
	// managed" rule already used for Function Cost/Rows.
	GrantedBy *string
	Pos       pipeline.SourcePos
}

// Revocation is a single REVOKE directive.
type Revocation struct {
	Privileges []string // nil = ALL
	Roles      []string
	Cascade    bool
	// GrantedBy — see Grant.GrantedBy's identical doc comment.
	GrantedBy *string
	Pos       pipeline.SourcePos
}

// FuncArg is a function/procedure/aggregate parameter.
type FuncArg struct {
	Name    string
	Type    TypeRef
	Mode    string // "IN", "OUT", "INOUT", "VARIADIC", "TABLE"
	Default *string
}

// FuncAttrs holds function/procedure attributes extracted from pg_query.
type FuncAttrs struct {
	Language    string // "sql", "plpgsql", etc.
	Volatility  string // "VOLATILE", "STABLE", "IMMUTABLE"
	Strict      bool   // RETURNS NULL ON NULL INPUT
	SecurityDef bool   // SECURITY DEFINER
	// Leakproof is RFC audit item #25's `[NOT] LEAKPROOF` — real PostgreSQL
	// defaults to NOT LEAKPROOF, so false is the ordinary case, the same
	// bool-defaults-false convention Strict/SecurityDef already use.
	Leakproof bool
	Parallel  string // "UNSAFE", "RESTRICTED", "SAFE"
	Cost      *float64
	Rows      *float64
	// Transforms is RFC audit item #26's `TRANSFORM FOR TYPE type_name
	// [, FOR TYPE type_name ...]` — references to already-installed
	// CREATE TRANSFORM pairs, not a declaration of one (Section 23 keeps
	// CREATE TRANSFORM itself out of scope).
	Transforms []TypeRef
	// ObjFile/LinkSymbol are RFC audit item #27's `AS 'obj_file'[,
	// 'link_symbol']` form (LANGUAGE C/internal): a shared-object path plus
	// an optional symbol name, structurally distinct from Body's single
	// dollar-quoted procedural-code string. When ObjFile != nil, Body is
	// unused; LinkSymbol == nil means PostgreSQL defaults it to the SQL
	// function's own name.
	ObjFile    *string
	LinkSymbol *string
	// AtomicBody is RFC audit item #28's PG14+ `BEGIN ATOMIC ... END` form
	// (LANGUAGE SQL only) — the standard-SQL-conformant alternative to a
	// dollar-quoted AS body. When true, Body holds the raw semicolon-
	// terminated statement-list text (no BEGIN ATOMIC/END keywords),
	// canonicalised/hashed identically to a dollar-quoted LANGUAGE SQL body
	// (RFC Section 9.1) — only the recreate-time rendering differs.
	AtomicBody bool
	Body       string // raw dollar-quoted body text (or BEGIN ATOMIC statement-list text when AtomicBody)
	// Config is Section 9.2's `SET param = value` / `SET param FROM
	// CURRENT` clause list — sets or inherits a GUC for the duration of
	// each call, most commonly used to guard a SECURITY DEFINER function
	// against search-path injection via `SET search_path = ...`. Real
	// PostgreSQL's CREATE OR REPLACE fully replaces proconfig from
	// whatever this list declares (confirmed live: omitting a previously-
	// set SET clause on replace clears it, it is never additive), so a
	// change here joins the same recreate trigger as Cost/Rows/Leakproof
	// rather than needing its own imperative ALTER ... SET/RESET path.
	Config []FuncConfig
}

// FuncConfig is one FUNCTION/PROCEDURE-level SET clause (RFC Section 9.2).
type FuncConfig struct {
	Param string
	// Value is the "= value" form's value text exactly as declared (quotes
	// preserved for a string literal; comma-joined for a multi-value GUC
	// like search_path — see extractFuncAttrs' "set" case). nil when
	// FromCurrent is true.
	Value       *string
	FromCurrent bool
}

// PartitionSpec describes a table's partition strategy.
type PartitionSpec struct {
	Strategy string   // "RANGE", "LIST", "HASH"
	Columns  []string // partitioning columns/expressions
}

// Partition is one partition entry. PartitionBy/Partitions describe
// sub-partitioning (RFC Section 7.13): PartitionBy is nil for a leaf
// partition; when set, Partitions holds that partition's own nested
// partition entries, which may themselves be further sub-partitioned
// (arbitrary depth).
type Partition struct {
	Name        string
	Bounds      string // raw bounds expression
	PartitionBy *PartitionSpec
	// RenamedFrom names the partition's prior identity (RENAMED FROM,
	// Section 7.13) — matched within the same parent's partition list only;
	// a partition's schema is always its parent table's, so there is no
	// cross-schema variant the way Table/View/Function have one.
	RenamedFrom *string
	// AttachedFrom is RFC Section 7.13's "ATTACHED FROM existing_table"
	// form — nil for a normal partition. When set, this attaches an
	// already-existing standalone table (matched against a top-level
	// snapshot entry) instead of creating a new one — a possibly schema-
	// qualified "schema.name" or bare "name" string (same convention as
	// Table.Inherits), resolved against the parent's own schema when bare.
	AttachedFrom *string
	// Foreign, ForeignServer, and ForeignOptions mirror Table's identical
	// fields (RFC Section 7.13's "FOREIGN partition-name ... SERVER
	// server_name [OPTIONS (...)]" form) — a partition made a foreign
	// table via direct creation, distinct from AttachedFrom's "attach an
	// already-existing standalone table" form (which may itself already be
	// foreign, independently of this field). Foreign partitions can never
	// be further sub-partitioned (confirmed live: PostgreSQL rejects
	// PARTITION BY on a foreign table), so Partitions is always empty here.
	Foreign        bool
	ForeignServer  *string
	ForeignOptions []pipeline.StorageParam
	Partitions     []*Partition
	// Constraints holds constraints declared directly on this partition,
	// independent of the parent (RFC Section 7.3's "DROP CONSTRAINT ...
	// ONLY" — PostgreSQL 18+). A partition otherwise has no independently
	// declared structure at all (no Columns field exists here — it always
	// inherits its parent's column list); this is the sole, narrowly-scoped
	// exception, added specifically to represent a constraint that has been
	// detached from the parent via "ALTER TABLE ONLY parent DROP CONSTRAINT"
	// while remaining declared (and enforced) locally on this partition —
	// or, identically in the catalog, one added directly to this partition
	// from the start and never declared on the parent at all. Real
	// PostgreSQL's own catalog makes no distinction between these two
	// histories (confirmed live: both produce conislocal=true,
	// coninhcount=0 on the partition's own pg_constraint row), so neither
	// does this field.
	Constraints []*Constraint
	SrcPos      pipeline.SourcePos
}

// ── concrete IR object types ──────────────────────────────────────────────────

// Schema is a CREATE SCHEMA declaration.
type Schema struct {
	Name           string
	Owner          *string
	Comment        *string
	RenamedFrom    *string
	Grants         []Grant
	Revocations    []Revocation
	SecurityLabels []pipeline.SecurityLabel
	NameMaps       []pipeline.NameMapEntry
	SrcPos         pipeline.SourcePos
}

func (s *Schema) QualifiedName() string   { return s.Name }
func (s *Schema) Pos() pipeline.SourcePos { return s.SrcPos }
func (s *Schema) irObject()               {}

// Extension is a CREATE EXTENSION declaration.
type Extension struct {
	Name    string
	Schema  *string
	Version *string
	// Cascade is RFC audit item #15's create-time CASCADE flag
	// (CREATE EXTENSION ... CASCADE, real PostgreSQL grammar) — auto-
	// installs any extensions this one depends on that aren't already
	// present. Create-time only; PostgreSQL has no ALTER EXTENSION ...
	// CASCADE, so it never participates in diffing, only createExtension.
	Cascade  bool
	Comment  *string
	NameMaps []pipeline.NameMapEntry
	SrcPos   pipeline.SourcePos
}

func (e *Extension) QualifiedName() string   { return e.Name }
func (e *Extension) Pos() pipeline.SourcePos { return e.SrcPos }
func (e *Extension) irObject()               {}

// Table is a CREATE TABLE / UNLOGGED TABLE / FOREIGN TABLE declaration.
type Table struct {
	Schema      string
	Name        string
	RenamedFrom *string
	// RenamedFromSchema is the schema the RENAMED FROM name lived in, when the
	// directive is schema-qualified (a rename combined with a SET SCHEMA
	// move). Nil means RenamedFrom is unqualified — the old name lived in
	// this same Schema, matching the pre-cross-schema-rename behavior.
	RenamedFromSchema *string
	Protected         bool
	Deprecated        *string
	DropCascade       bool
	Unlogged          bool
	Foreign           bool
	ForeignServer     *string
	// ForeignOptions is FOREIGN TABLE's OPTIONS (...) clause — ordered like
	// Index.With, since it renders back into deterministic DPG source and
	// SQL text the same way.
	ForeignOptions []pipeline.StorageParam
	Owner          *string
	Comment        *string
	// OfType is RFC Section 7.1's "Form 2" typed table (`CREATE TABLE name
	// OF type_name`), backing the table's row type with a previously-
	// declared composite type instead of an independent column list.
	// Zero value (Name == "") means an ordinary table. Scoped to the bare
	// association plus table-level constraints (which already parse as
	// ordinary Constraints, e.g. a CHECK) — real PostgreSQL's per-attribute
	// "col WITH OPTIONS ..." constraint-narrowing form (further restricting
	// an inherited attribute, never adding a new column) is a deliberately
	// unmodeled corner case, not attempted here. Changing it post-creation
	// is Section 7.11's `OF type_name`/`NOT OF` ALTER, CAUTION (PostgreSQL
	// validates every column against the type's attributes at execution
	// time), not a DROP+CREATE.
	OfType TypeRef
	// AccessMethod is RFC Section 7.1's `USING method` table-access-method
	// clause; "" means PostgreSQL's own cluster default. Changing it post-
	// creation is Section 7.11's `SET ACCESS METHOD`, CAUTION (rewrites the
	// table's storage).
	AccessMethod   string
	Columns        []*Column
	Constraints    []*Constraint
	Indexes        []*Index
	Policies       []*Policy
	Triggers       []*Trigger
	Grants         []Grant
	Revocations    []Revocation
	SecurityLabels []pipeline.SecurityLabel
	RLSEnabled     bool
	RLSForced      bool
	// ReplicaIdentity is Section 7.11's REPLICA IDENTITY directive, always
	// compared in full — like RLSEnabled/RLSForced, omitting it means
	// PostgreSQL's own DEFAULT, not "leave whatever's live alone". Zero
	// value (Mode == "") is treated as "DEFAULT".
	ReplicaIdentity ReplicaIdentity
	// ClusterOn names the index a future manual CLUSTER would use (Section
	// 7.11); nil means none declared — removing a previously-declared
	// value emits ALTER TABLE ... SET WITHOUT CLUSTER.
	ClusterOn   *string
	Inherits    []string
	PartitionBy *PartitionSpec
	Partitions  []*Partition
	// StorageParams is a WITH (...) clause's key=value pairs, source order
	// preserved (a slice, not a map, matching Index.With — deterministic
	// re-emission needs a stable order, both at CREATE time and for SET/RESET
	// diffing against a snapshot).
	StorageParams []pipeline.StorageParam
	Tablespace    *string
	NameMaps      []pipeline.NameMapEntry
	// LikeClauses holds unresolved `LIKE source_table [INCLUDING/EXCLUDING
	// ...]` entries (Section 7.1) captured verbatim by buildTable. A
	// Builder builds one object at a time with no visibility into any
	// other declared table, so a LIKE clause can't be resolved there —
	// ResolveLikeClauses (called once, after every object in the compile
	// unit is built and merged) looks up each source table by qualified
	// name and splices its resolved columns/constraints in, then clears
	// this field. By the time diffing/snapshotting sees a Table, this is
	// always empty: per the RFC, "the snapshot records the resolved
	// columns/constraints, not the LIKE clause itself."
	LikeClauses []*LikeClause
	// DetachedFrom is RFC Section 7.13's DETACHED FROM block directive —
	// nil when not declared. Converts a table that is currently a partition
	// of DetachedFrom.ParentTable (matched against a nested entry in that
	// parent's own snapshot, a different key space from every other
	// RenamedFrom-style directive) back into an independent table.
	DetachedFrom *DetachedFrom
	SrcPos       pipeline.SourcePos
}

// DetachedFrom is Section 7.13's DETACHED FROM directive target — see
// ir.Table.DetachedFrom's doc comment.
type DetachedFrom struct {
	// ParentTable is a possibly schema-qualified "schema.name" or bare
	// "name" string (same convention as Table.Inherits/Partition.
	// AttachedFrom), resolved against the child's own schema when bare —
	// a partition's schema always matches its parent's.
	ParentTable string
	// Concurrently mirrors real PostgreSQL's ALTER TABLE ... DETACH
	// PARTITION ... CONCURRENTLY — run outside the transaction.
	Concurrently bool
}

// LikeClause is one `LIKE source_table [{INCLUDING|EXCLUDING} attr ...]`
// column-list entry (Section 7.1), before resolution. SourceSchema is ""
// when written unqualified — resolved against the referencing table's own
// schema by ResolveLikeClauses, not here (the table's final Schema, after
// any enclosing SCHEMA block context is merged in, isn't necessarily known
// yet at buildTable time).
type LikeClause struct {
	SourceSchema string
	SourceName   string
	// Options is the raw CREATE_TABLE_LIKE_* bitmask pg_query hands back
	// (confirmed empirically against a real parse: bit 0 COMMENTS, 1
	// COMPRESSION, 2 CONSTRAINTS, 3 DEFAULTS, 4 GENERATED, 5 IDENTITY, 6
	// INDEXES, 7 STATISTICS, 8 STORAGE — matching PostgreSQL's own
	// CREATE_TABLE_LIKE_* #defines in parsenodes.h exactly, so it's used
	// directly rather than re-mapped to a DPG-local enum).
	Options uint32
	// InsertAt is the index into Columns/Constraints at the moment this
	// clause was encountered (i.e., how many explicit columns preceded it
	// in source) — used to splice the resolved copy in at the right
	// position instead of always appending to the end.
	InsertAt int
	Pos      pipeline.SourcePos
}

// ReplicaIdentity is Table.ReplicaIdentity's structured value — see its
// doc comment.
type ReplicaIdentity struct {
	Mode      string // "" (== "DEFAULT"), "DEFAULT", "FULL", "NOTHING", or "INDEX"
	IndexName string // only set when Mode == "INDEX"
}

// LIKE option bits — see LikeClause.Options' doc comment.
const (
	LikeIncludeComments    uint32 = 1 << 0
	LikeIncludeCompression uint32 = 1 << 1
	LikeIncludeConstraints uint32 = 1 << 2
	LikeIncludeDefaults    uint32 = 1 << 3
	LikeIncludeGenerated   uint32 = 1 << 4
	LikeIncludeIdentity    uint32 = 1 << 5
	LikeIncludeIndexes     uint32 = 1 << 6
	LikeIncludeStatistics  uint32 = 1 << 7
	LikeIncludeStorage     uint32 = 1 << 8
)

func (t *Table) QualifiedName() string   { return qualName(t.Schema, t.Name) }
func (t *Table) Pos() pipeline.SourcePos { return t.SrcPos }
func (t *Table) irObject()               {}

// View is a CREATE [MATERIALIZED|RECURSIVE] VIEW declaration.
type View struct {
	Schema      string
	Name        string
	RenamedFrom *string
	// RenamedFromSchema — see Table.RenamedFromSchema's identical doc comment.
	RenamedFromSchema *string
	Materialized      bool
	Recursive         bool
	Query             string // raw query text (opaque)
	Owner             *string
	Comment           *string
	Deprecated        *string
	Grants            []Grant
	Revocations       []Revocation
	WithNoData        bool // MATERIALIZED VIEW ... WITH NO DATA
	// Tablespace is Section 8.2's TABLESPACE clause, materialized views
	// only. StorageParams is the WITH (...) list: storage parameters on a
	// materialized view (Section 8.2), or view options on a plain view
	// (Section 8.1: security_barrier, security_invoker, and check_option,
	// which also holds a trailing WITH [LOCAL|CASCADED] CHECK OPTION).
	// PostgreSQL keeps both in pg_class.reloptions.
	Tablespace    *string
	StorageParams []pipeline.StorageParam
	// Columns is the view's column-name list (RFC Section 8.1), kept only
	// when it can't be written into Query as target-list aliases (a query
	// with `*`); otherwise the builder folds it into Query and this is nil.
	Columns []string
	// Reconstructed is set when the view was read from the live catalog:
	// Query is then PostgreSQL's stored form, with any `*` expanded.
	Reconstructed bool
	// Indexes is only meaningful when Materialized is true — real PostgreSQL
	// does not support indexes on a plain or recursive view, only on a
	// materialized view (or a table). RFC Section 8.2's matview-block grammar is
	// the only view-block variant that includes indices-block.
	Indexes        []*Index
	SecurityLabels []pipeline.SecurityLabel
	NameMaps       []pipeline.NameMapEntry
	SrcPos         pipeline.SourcePos
}

func (v *View) QualifiedName() string   { return qualName(v.Schema, v.Name) }
func (v *View) Pos() pipeline.SourcePos { return v.SrcPos }
func (v *View) irObject()               {}

// Function is a CREATE FUNCTION declaration.
type Function struct {
	Schema      string
	Name        string
	Args        []FuncArg
	ReturnType  TypeRef
	Attrs       FuncAttrs
	BodyHash    string // SHA-256 of normalised body
	Comment     *string
	Deprecated  *string
	RenamedFrom *string
	// RenamedFromSchema — see Table.RenamedFromSchema's identical doc comment.
	RenamedFromSchema *string
	// DependsOnExtensions is Section 9.1's `[NO] DEPENDS ON EXTENSION`
	// func-block directive — the complete desired set of auto-drop
	// extension dependencies, diffed like Grants (added entries emit
	// ALTER FUNCTION ... DEPENDS ON EXTENSION ext, entries present in the
	// snapshot but absent here emit ALTER FUNCTION ... NO DEPENDS ON
	// EXTENSION ext). Real PostgreSQL has no ALTER AGGREGATE equivalent
	// even though Aggregate shares this same func-block grammar
	// production (passthrough principle: DPG doesn't reject it on
	// Aggregate, PostgreSQL's own parser does) — so this field only
	// exists on Function/Procedure, not Aggregate.
	DependsOnExtensions []string
	Grants              []Grant
	Revocations         []Revocation
	SecurityLabels      []pipeline.SecurityLabel
	NameMaps            []pipeline.NameMapEntry
	// Owner is RFC audit item #70's ALTER FUNCTION ... OWNER TO diffing
	// input — see ir.Publication.Owner's identical doc comment.
	Owner  *string
	SrcPos pipeline.SourcePos
}

func (f *Function) QualifiedName() string {
	return fmt.Sprintf("%s(%s)", qualName(f.Schema, f.Name), ArgsKey(f.Args))
}
func (f *Function) Pos() pipeline.SourcePos { return f.SrcPos }
func (f *Function) irObject()               {}

// Procedure is a CREATE PROCEDURE declaration.
type Procedure struct {
	Schema   string
	Name     string
	Args     []FuncArg
	Attrs    FuncAttrs
	BodyHash string // SHA-256 of normalised body
	Comment  *string
	// Deprecated/RenamedFrom mirror ir.Function's identical func-block
	// directives (RFC Section 9.3/9.4 point Function/Procedure/Aggregate at the
	// same grammar) — RFC audit items #8/#10: Procedure never got them
	// despite sharing the grammar, so DEPRECATED was silently discarded and
	// a rename was treated as an unrelated DROP+CREATE instead of
	// ALTER PROCEDURE ... RENAME TO.
	Deprecated  *string
	RenamedFrom *string
	// RenamedFromSchema — see Table.RenamedFromSchema's identical doc comment.
	RenamedFromSchema *string
	// DependsOnExtensions — see ir.Function.DependsOnExtensions' identical
	// doc comment.
	DependsOnExtensions []string
	Grants              []Grant
	Revocations         []Revocation
	SecurityLabels      []pipeline.SecurityLabel
	NameMaps            []pipeline.NameMapEntry
	// Owner is RFC audit item #70's ALTER PROCEDURE ... OWNER TO diffing
	// input — see ir.Function.Owner's identical doc comment.
	Owner  *string
	SrcPos pipeline.SourcePos
}

func (p *Procedure) QualifiedName() string {
	return fmt.Sprintf("%s(%s)", qualName(p.Schema, p.Name), ArgsKey(p.Args))
}
func (p *Procedure) Pos() pipeline.SourcePos { return p.SrcPos }
func (p *Procedure) irObject()               {}

// Aggregate is a CREATE AGGREGATE declaration.
type Aggregate struct {
	Schema string
	Name   string
	Args   []FuncArg
	Body   string // full "CREATE AGGREGATE ..." statement text, used directly for SQL emission
	// Options is the same SFUNC/STYPE/INITCOND/... key=value list as Body,
	// kept structured (and in source order) so dump can reconstruct the DPG
	// "AGGREGATE name (args) (options)" declaration without re-parsing Body.
	Options []pipeline.StorageParam
	Comment *string
	// Deprecated/RenamedFrom — see Procedure's identical fields' doc
	// comment (RFC audit items #9/#11).
	Deprecated  *string
	RenamedFrom *string
	// RenamedFromSchema — see Table.RenamedFromSchema's identical doc comment.
	RenamedFromSchema *string
	Grants            []Grant
	Revocations       []Revocation
	SecurityLabels    []pipeline.SecurityLabel
	NameMaps          []pipeline.NameMapEntry
	// Owner is RFC audit item #70's ALTER AGGREGATE ... OWNER TO diffing
	// input — see ir.Function.Owner's identical doc comment.
	Owner  *string
	SrcPos pipeline.SourcePos
}

func (a *Aggregate) QualifiedName() string {
	return fmt.Sprintf("%s(%s)", qualName(a.Schema, a.Name), ArgsKey(a.Args))
}
func (a *Aggregate) Pos() pipeline.SourcePos { return a.SrcPos }
func (a *Aggregate) irObject()               {}

// Type covers ENUM, COMPOSITE, RANGE, DOMAIN, and BASE types.
type Type struct {
	Schema         string
	Name           string
	Variant        string    // "ENUM", "COMPOSITE", "RANGE", "DOMAIN", "BASE"
	EnumValues     []string  // ENUM only
	CompositeAttrs []*Column // COMPOSITE only: ordered list of attributes
	Body           string    // raw Part1 for range/base (opaque); DOMAIN's full CREATE DOMAIN text, used only for a base-type change (DROP+CREATE)
	Reconstructed  bool      // Body rebuilt from the catalog; see Tablespace.Reconstructed. RANGE/BASE only.
	// MultirangeName is RANGE only: the name of the range's multirange
	// companion type (RFC Section 5.3), either declared with
	// MULTIRANGE_TYPE_NAME or derived by PostgreSQL's default rule
	// (DefaultMultirangeName). It lives in the range's own schema.
	MultirangeName string
	// BaseReceive/BaseSend/BaseTypmodIn/BaseTypmodOut/BaseAnalyze/
	// BaseSubscript/BaseStorage are RFC Section 5.5's 7 in-place-alterable
	// BASE type properties — real PostgreSQL's `ALTER TYPE ... SET (...)`
	// supports exactly these, nothing else; every other CREATE TYPE
	// property (INPUT/OUTPUT/INTERNALLENGTH/etc.) is immutable and any
	// change to it still requires DROP+CREATE via the Body-hash comparison.
	// nil means "not specified": for the six function-name properties this
	// is a real, meaningfully-comparable state (PostgreSQL's own default is
	// no such support function — an invalid OID, not some other concrete
	// function), populated identically whether the object was source-
	// declared or introspected. STORAGE is the one exception: PostgreSQL
	// always has a concrete storage mode ('plain' by default) even when
	// never declared, so — like Owner/Cost/Rows elsewhere in this
	// codebase — it's only diffed when the DESIRED side explicitly
	// declares it, never reset just because it's nil.
	BaseReceive   *string
	BaseSend      *string
	BaseTypmodIn  *string
	BaseTypmodOut *string
	BaseAnalyze   *string
	BaseSubscript *string
	BaseStorage   *string
	Comment       *string
	Owner         *string
	// RenamedFrom/RenamedFromSchema — see Table's identical doc comments.
	// Applies uniformly across all 5 variants (Section 5.1-5.5), reusing the
	// same generic cross-schema RENAME TO/SET SCHEMA mechanism as Table/
	// View/Function/Procedure/Aggregate.
	RenamedFrom       *string
	RenamedFromSchema *string
	Deprecated        *string
	MigrateRemove     *pipeline.MigrateRemoveBlock // ENUM only: MIGRATE REMOVE { } block
	// EnumValueRenames is ENUM only: Section 5.1.1's RENAME VALUE { }
	// directive(s) — see pipeline.EnumValueRenameDir's doc comment.
	EnumValueRenames []pipeline.EnumValueRenameDir
	// Grants/Revocations (RFC audit item #3) apply uniformly across all 5
	// variants — real PostgreSQL's GRANT/REVOKE has no separate "ON DOMAIN"
	// target; a domain is granted exactly like any other type, via
	// "... ON TYPE domain_name ..." (confirmed live). Live-verification
	// during the audit found ENUM's has_type_privilege() returning true
	// after a live apply and initially treated that as ENUM-specific
	// evidence the bug didn't reproduce there — re-traced live: that's
	// PostgreSQL's own default USAGE-to-PUBLIC grant every type variant
	// gets on creation (confirmed identically true for ENUM/COMPOSITE/
	// RANGE/DOMAIN with no DPG involvement at all), not a real difference.
	// The bug is uniform across all 5 variants.
	Grants      []Grant
	Revocations []Revocation
	NameMaps    []pipeline.NameMapEntry
	// DomainBaseType/DomainDefault/DomainNotNull/DomainConstraints (DOMAIN
	// only) are RFC Section 5.4's structured domain diffing inputs — a domain is
	// NOT purely opaque like RANGE/BASE despite sharing the Body field, so
	// property-level changes (DEFAULT, NOT NULL, individual CHECK
	// constraints) can each get their own targeted ALTER DOMAIN op instead
	// of an unconditional DROP+CREATE. Populated from whichever source the
	// user wrote: real PG's own inline "CREATE DOMAIN name AS type DEFAULT
	// expr CONSTRAINT c CHECK (...)" syntax (parsed from Part 1 via
	// pg_query), RFC Section 5.4's block-based "{ DEFAULT expr; CONSTRAINT c
	// CHECK (...); }" form, or both merged together.
	DomainBaseType TypeRef
	// DomainCollation is RFC Section 5.4's "CREATE DOMAIN name AS base_type
	// [COLLATE collation]" clause — valid only when base_type is itself
	// collatable, left to PostgreSQL's own parser to enforce (same
	// passthrough principle used throughout this codebase). "" means
	// unspecified. Unlike DEFAULT/NOT NULL/CHECK, changing it always
	// requires DROP DOMAIN CASCADE + CREATE DOMAIN (Section 5.4's own
	// diffing table), so it's compared alongside DomainBaseType rather than
	// getting its own ALTER DOMAIN branch.
	DomainCollation   string
	DomainDefault     *string
	DomainNotNull     bool
	DomainConstraints []*Constraint
	SecurityLabels    []pipeline.SecurityLabel
	SrcPos            pipeline.SourcePos
}

func (t *Type) QualifiedName() string   { return qualName(t.Schema, t.Name) }
func (t *Type) Pos() pipeline.SourcePos { return t.SrcPos }
func (t *Type) irObject()               {}

// Sequence is a CREATE SEQUENCE declaration.
type Sequence struct {
	Schema string
	Name   string
	// RenamedFrom/RenamedFromSchema name the sequence's previous identity
	// (RENAMED FROM, RFC Section 10).
	RenamedFrom       *string
	RenamedFromSchema *string
	// Unlogged is RFC Section 10's UNLOGGED prefix — trades crash-safety
	// (the sequence's current value is not WAL-logged) for reduced write
	// overhead, the same tradeoff as an unlogged table (Section 7.12).
	// Toggling it post-creation is ALTER SEQUENCE name SET LOGGED/
	// UNLOGGED, CAUTION — a targeted ALTER, not a recreate.
	Unlogged bool
	Owner    *string
	Comment  *string
	Grants   []Grant
	// Revocations is RFC Section 11.3's explicit REVOKE block support (RFC
	// audit item #24) — Grants was already populated by the builder, but
	// neither createSequence/diffSequence ever referenced Grants (a total
	// silent-discard) and there was no Revocations slot at all.
	Revocations    []Revocation
	SecurityLabels []pipeline.SecurityLabel
	NameMaps       []pipeline.NameMapEntry
	// Options (nil = use PostgreSQL default for that parameter)
	IncrementBy *int64
	MinValue    *int64
	MaxValue    *int64
	StartValue  *int64
	Cache       *int64
	// Cycle is nil when the source didn't write CYCLE or NO CYCLE at all
	// (unlike the other options above, false is a real, explicit value:
	// NO CYCLE), so it needs the same nil-means-unspecified treatment.
	Cycle *bool
	// NoMinValue/NoMaxValue distinguish an explicit "NO MINVALUE"/
	// "NO MAXVALUE" declaration from "not mentioned at all" (RFC audit item
	// #20) — pg_query represents both as the identical nil-Arg DefElem
	// shape, so MinValue/MaxValue alone (nil either way) can't tell them
	// apart. Same nil-means-unspecified convention as Cycle above: false
	// here means "source didn't explicitly write NO MINVALUE/NO MAXVALUE",
	// not "the sequence has no minimum/maximum".
	NoMinValue, NoMaxValue bool
	// AsType is RFC audit item #14's "AS type" data-type clause (nil = not
	// declared, PostgreSQL's own default is bigint) — completely
	// unimplemented before this: no IR field, no builder handling, silently
	// discarded on every create/diff. Per the RFC's own diffing table, an
	// AS type change is DESTRUCTIVE (DROP + CREATE; PostgreSQL has no
	// ALTER SEQUENCE ... AS that resizes cleanly in place per the RFC's
	// chosen semantics).
	AsType *TypeRef
	// OwnedBy is RFC audit item #14's "OWNED BY table.col" clause — nil
	// means unspecified (not managed by DPG, matching every other optional
	// sequence field's convention). The literal string "NONE" represents
	// an explicit "OWNED BY NONE" declaration (PostgreSQL's own keyword);
	// any other value holds the qualified "table.column" (or
	// "schema.table.column") text as written. Per the RFC's diffing table,
	// an OWNED BY change is SAFE (ALTER SEQUENCE ... OWNED BY ...).
	OwnedBy *string
	// Restart/RestartWith are RFC audit item #68's imperative "RESTART
	// [WITH n]" directive. Unlike every other option above, this does not
	// describe persistent, comparable state — real PostgreSQL's RESTART
	// resets the sequence's *current* value now, and nextval() calls
	// immediately begin moving it away from n again, leaving no queryable
	// "current RESTART value" for a later plan to diff against. So this is
	// deliberately NOT snapshotted the way StartValue etc. are: Restart's
	// mere presence in the desired source unconditionally re-emits
	// ALTER SEQUENCE ... RESTART [WITH n] on every plan/apply for as long
	// as it remains declared (Safety Caution), matching real PostgreSQL's
	// own documented recommendation to remove the directive from source
	// once the reset has been applied. RestartWith is nil for a bare
	// RESTART (PostgreSQL resets to the sequence's START value then).
	Restart     bool
	RestartWith *int64
	SrcPos      pipeline.SourcePos
}

func (s *Sequence) QualifiedName() string   { return qualName(s.Schema, s.Name) }
func (s *Sequence) Pos() pipeline.SourcePos { return s.SrcPos }
func (s *Sequence) irObject()               {}

// Role is a CREATE ROLE declaration (RFC Section 11.1). Every attribute is native
// PostgreSQL CREATE ROLE/ALTER ROLE grammar, extracted from
// CreateRoleStmt.Options — not a DPG-invented block directive.
//
// Every pointer/nil-slice field means "not declared, not managed by DPG for
// this role" — offline diffing only ever compares what source explicitly
// sets, never PostgreSQL's own default for an omitted option (mirrors
// Sequence's optional-param convention).
//
// Password is the raw declared PASSWORD text, verbatim — a literal, or one
// containing {{<secret-uri>}} placeholders resolved only at apply time (see
// pipeline.ResolveTemplate), same mechanism as Subscription.ConnInfo (Section 13.2).
// Never the resolved value.
type Role struct {
	Name            string
	CanLogin        *bool
	Superuser       *bool
	CreateDB        *bool
	CreateRole      *bool
	Inherit         *bool
	IsReplication   *bool
	BypassRLS       *bool
	ConnectionLimit *int
	Password        *string
	ValidUntil      *string
	// Memberships is the unified representation of every IN ROLE/ROLE/ADMIN
	// entry (RFC audit item #32, Section 11.1) — both the bare CREATE-time
	// inline forms (real PG grammar, no WITH modifiers possible) and the
	// modifier-bearing block-directive form (WITH ADMIN/INHERIT/SET, PG16+,
	// only expressible this way — confirmed live that CREATE ROLE's own
	// inline lists reject WITH entirely). One entry per (Direction, Role)
	// pair, matching real PostgreSQL's own single-row-per-pair
	// pg_auth_members model exactly — deliberately NOT two independently
	// nil-guarded lists the way an earlier revision of this field kept
	// ROLE/ADMIN apart, since that let the same role name appear in both
	// buckets at once with ambiguous, order-dependent resulting behavior
	// (PostgreSQL only ever has one admin_option value for a given pair).
	Memberships []RoleMembership
	Comment     *string
	// RenamedFrom names the role's prior identity (RENAMED FROM, Section
	// 11.1) — schema-agnostic, unlike Table/View/Type/Collation's identical
	// field: roles are cluster-level, not schema-scoped, so there is no
	// RenamedFromSchema counterpart here.
	RenamedFrom *string
	// Configs is RFC audit item #74's role-config-dir block directive set
	// (Section 11.1) — ALTER ROLE ... [IN DATABASE db] SET/RESET session
	// config parameters. Declared, so managed: only entries actually
	// present here are diffed; nothing else about a role's live session
	// config is inspected.
	Configs        []RoleConfig
	SecurityLabels []pipeline.SecurityLabel
	NameMaps       []pipeline.NameMapEntry
	SrcPos         pipeline.SourcePos
}

func (r *Role) QualifiedName() string   { return r.Name }
func (r *Role) Pos() pipeline.SourcePos { return r.SrcPos }
func (r *Role) irObject()               {}

// RoleConfig is one ALTER ROLE ... SET/RESET session-config entry (RFC
// audit item #74, Section 11.1): "[IN DATABASE db] SET param {TO|=} value",
// "SET param FROM CURRENT", "RESET param", or "RESET ALL".
type RoleConfig struct {
	// Param is the GUC name; empty when ResetAll is true.
	Param string
	// Value is the config-value text for the "SET param TO/= value" form,
	// exactly as declared (quotes preserved for a string literal) — see
	// pipeline.RoleConfigDir.Value's identical doc comment. nil for
	// FromCurrent and both RESET forms.
	Value *string
	// FromCurrent is the "SET param FROM CURRENT" form.
	FromCurrent bool
	// Reset marks either RESET form; ResetAll distinguishes "RESET ALL"
	// (Param empty) from "RESET param" (Param set).
	Reset    bool
	ResetAll bool
	// InDatabase is the optional "IN DATABASE db" qualifier — nil means
	// cluster-wide (every database).
	InDatabase *string
	Pos        pipeline.SourcePos
}

// RoleMembership is one role-membership entry (RFC audit item #32, Section
// 11.1) — the unified form of what used to be three separate lists
// (InRole/RoleMembers/AdminRoles), one entry per (Direction, Role) pair,
// matching real PostgreSQL's own single-row-per-pair pg_auth_members model.
type RoleMembership struct {
	// Role is the other role in the pair.
	Role string
	// Direction is "IN_ROLE" (this role is a member of Role — GRANT Role TO
	// thisRole) or "ROLE" (Role is a member of this role — GRANT thisRole
	// TO Role). Real PostgreSQL's single GRANT/REVOKE ROLE mechanism
	// underlies both; Direction only decides which name goes on which side
	// of the statement.
	Direction string
	// Admin renders WITH ADMIN OPTION when true — always meaningful
	// (unlike Inherit/Set below), since real PostgreSQL always has a
	// concrete admin_option value for a membership row (defaults to
	// false), matching how the legacy AdminRoles bucket already always
	// rendered WITH ADMIN OPTION unconditionally.
	Admin bool
	// Inherit/Set carry an explicitly-declared WITH INHERIT/WITH SET
	// value — nil means not declared, so not managed (the same "declared,
	// so managed" convention as Grant.GrantedBy): PostgreSQL's own default
	// for an unspecified INHERIT depends on the member role's own INHERIT
	// attribute at grant time, not a fixed value DPG could safely assume
	// or compare against.
	Inherit *bool
	Set     *bool
	Pos     pipeline.SourcePos
}

// Tablespace is a CREATE TABLESPACE declaration.
type Tablespace struct {
	Name string
	// Location is RFC Section 14.7's structured diffing input: LOCATION cannot be
	// changed after creation in real PostgreSQL, so a Location mismatch is
	// what actually decides DROP+CREATE — previously only Body's opaque
	// hash decided this, which (via Reconstructed, below) went silently
	// unset on every live path, missing any live-catalog LOCATION change.
	Location string
	Body     string // raw Part1 text
	Comment  *string
	// Reconstructed marks a Body rebuilt from the live catalog by the
	// introspector (as opposed to parsed from source). Reconstructed bodies are
	// canonical but not byte-identical to hand-written source, so the snapshot
	// omits their body hash to avoid spurious text-diff drift. See sourceBodyHash.
	Reconstructed bool
	// Grants/Revocations (RFC audit item #4) — same missing-field shape as
	// the already-fixed Schema-grants pattern, never generalized to
	// Tablespace.
	Grants         []Grant
	Revocations    []Revocation
	SecurityLabels []pipeline.SecurityLabel
	// Owner is RFC audit item #80's Section 14.7 inline `OWNER` clause —
	// unlike every block-directive Owner elsewhere in this file, real
	// PostgreSQL's own CREATE TABLESPACE grammar accepts OWNER inline in
	// Part 1 (matching Tenet 2/5, per the RFC's own explanation of why this
	// one kind differs), so o.Body already contains it verbatim at create
	// time — no SET ROLE/RESET ROLE creator-attribution wrapping needed,
	// unlike Publication/EventTrigger/TSDict's block-only Owner. This field
	// exists purely so a later change can be diffed and emitted as
	// ALTER TABLESPACE ... OWNER TO.
	Owner *string
	// RenamedFrom is RFC audit item #80's RENAME TO support — bare, like
	// Role/Publication/ForeignServer's identical field: tablespaces are
	// cluster-level, not schema-scoped.
	RenamedFrom *string
	// StorageParams is a WITH (...) clause's key=value pairs (Section 14.7),
	// source order preserved — same convention as Table.StorageParams, and
	// diffed the same SET/RESET way via ALTER TABLESPACE.
	StorageParams []pipeline.StorageParam
	SrcPos        pipeline.SourcePos
}

func (ts *Tablespace) QualifiedName() string   { return ts.Name }
func (ts *Tablespace) Pos() pipeline.SourcePos { return ts.SrcPos }
func (ts *Tablespace) irObject()               {}

// ForeignDataWrapper is a CREATE FOREIGN DATA WRAPPER declaration.
type ForeignDataWrapper struct {
	Name string
	// Handler/Validator/Options are RFC Section 14.8's structured diffing inputs:
	// "any change to a FDW requires drop + recreate" is the RFC's own
	// documented semantics (no ALTER FOREIGN DATA WRAPPER path, even
	// though real PostgreSQL has one — a deliberate DPG simplification),
	// so this drives a single "did anything change" comparison rather
	// than field-level ALTER clauses. Previously only Body's opaque hash
	// decided this, which (via Reconstructed, below) went silently unset
	// on every live path, missing any live-catalog change to a FDW's
	// definition. Handler/Validator are "" for NO HANDLER/NO VALIDATOR or
	// when omitted (both mean the same thing to PostgreSQL).
	Handler       string
	Validator     string
	Options       []pipeline.StorageParam
	Body          string
	Comment       *string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// Grants/Revocations (RFC audit item #5) — see Tablespace.Grants' doc
	// comment.
	Grants      []Grant
	Revocations []Revocation
	// RenamedFrom names the FDW's prior identity (RENAMED FROM) — bare, like
	// Role/EventTrigger's identical field: foreign data wrappers are
	// database-level, not schema-scoped, so there is no RenamedFromSchema
	// counterpart here.
	RenamedFrom *string
	// Owner is RFC Section 14.8's ALTER FOREIGN DATA WRAPPER ... OWNER TO
	// diffing input — same shape as ForeignServer.Owner (superuser-only in
	// real PostgreSQL, same as CREATE FOREIGN DATA WRAPPER itself).
	Owner  *string
	SrcPos pipeline.SourcePos
}

func (f *ForeignDataWrapper) QualifiedName() string   { return f.Name }
func (f *ForeignDataWrapper) Pos() pipeline.SourcePos { return f.SrcPos }
func (f *ForeignDataWrapper) irObject()               {}

// ForeignServer is a CREATE SERVER declaration.
type ForeignServer struct {
	Name string
	// FDWName/Type/Options are RFC Section 14.9's structured diffing inputs: a
	// FDW-wrapper change or a TYPE change (real PostgreSQL has no
	// ALTER SERVER ... TYPE) decides DROP+CREATE, same reasoning as
	// ForeignDataWrapper.Handler/Validator/Options above; a VERSION or
	// OPTIONS change gets a real, targeted ALTER SERVER per RFC Section 14.9's
	// own diffing table ("OPTIONS changed" -> SAFE). Previously only
	// Body's opaque hash decided any of this, which (via Reconstructed,
	// below) went silently unset on every live path.
	FDWName       string
	Type          *string
	Version       *string
	Options       []pipeline.StorageParam
	Body          string
	Comment       *string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// Grants/Revocations (RFC audit item #6) — see Tablespace.Grants' doc
	// comment.
	Grants []Grant
	// Owner is RFC audit item #79's ALTER SERVER ... OWNER TO diffing input
	// — see ir.Publication.Owner's identical doc comment.
	Owner *string
	// RenamedFrom is RFC audit item #79's RENAME TO support — bare, like
	// Role/Publication's identical field: foreign servers are
	// cluster-level, not schema-scoped.
	RenamedFrom *string
	Revocations []Revocation
	SrcPos      pipeline.SourcePos
}

func (f *ForeignServer) QualifiedName() string   { return f.Name }
func (f *ForeignServer) Pos() pipeline.SourcePos { return f.SrcPos }
func (f *ForeignServer) irObject()               {}

// UserMapping is a CREATE USER MAPPING declaration.
type UserMapping struct {
	User   string
	Server string
	// Options is RFC Section 14.10's structured diffing input: "any change to
	// the mapping is a full DROP USER MAPPING + CREATE USER MAPPING, not
	// a targeted ALTER USER MAPPING" is the RFC's own explicit,
	// deliberately-corrected semantics (Section 14.10's text notes an earlier
	// draft described a targeted ALTER that was never implemented), so —
	// same as ForeignDataWrapper — this drives a single "did anything
	// change" comparison, not field-level ALTER clauses. Previously only
	// Body's opaque hash decided this, which (via Reconstructed, below)
	// went silently unset on every live path. Password-like keys are
	// deliberately excluded from the diff comparison (see
	// introspect.UserMappingRedactedPlaceholder): the live side never
	// exposes the real value, only a fixed redaction placeholder, so
	// comparing it against the desired side's real declared value would
	// otherwise show permanent, spurious drift on every plan.
	Options       []pipeline.StorageParam
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	SrcPos        pipeline.SourcePos
}

func (u *UserMapping) QualifiedName() string   { return u.User + "@" + u.Server }
func (u *UserMapping) Pos() pipeline.SourcePos { return u.SrcPos }
func (u *UserMapping) irObject()               {}

// PublicationTableRef is one FOR TABLE target of a CREATE PUBLICATION
// declaration, used only to compute dependency-graph ordering (graph.go) —
// Publication itself stays opaque (Body carries the full statement text) for
// diff/snapshot/dump purposes, same as every other reconstruction-tier kind.
type PublicationTableRef struct {
	Schema string // empty for an unqualified reference
	Name   string
}

// Publication is a CREATE PUBLICATION declaration.
type Publication struct {
	Name string
	Body string
	// Owner is diffed/emitted independently of Body (ALTER PUBLICATION ...
	// OWNER TO), same reasoning as Comment below (RFC audit item #7:
	// Publication had no Owner field at all — real PostgreSQL supports
	// ALTER PUBLICATION ... OWNER TO, but the RFC's Publication model never
	// added a corresponding no-syntax-slot).
	Owner *string
	// Comment is diffed/emitted independently of Body (COMMENT ON
	// PUBLICATION), same as every other Comment-bearing opaque kind —
	// confirmed live via \h COMMENT that real PostgreSQL genuinely supports
	// this, despite Publication being excluded from the original 14-kind
	// Comment/Grant fix on the mistaken assumption that it didn't apply.
	Comment *string
	// Tables is the FOR TABLE target list (PUBLICATIONOBJ_TABLE entries
	// only — FOR TABLES IN SCHEMA/FOR ALL TABLES have no single fixed table
	// to order against). See PublicationTableRef.
	Tables []PublicationTableRef
	// AllTables/Insert/Update/Delete/Truncate are RFC Section 13.1's structured
	// diffing inputs, alongside Tables above: a FOR ALL TABLES publication
	// can never be converted to/from an explicit table list via ALTER
	// (confirmed live against a real PostgreSQL 17 server: "Tables cannot
	// be added to or dropped from FOR ALL TABLES publications"), so an
	// AllTables change decides DROP+CREATE, while a Tables or WITH
	// (publish = ...) change each get their own real, targeted
	// ALTER PUBLICATION (RFC Section 13.1's own diffing table: both rows say
	// SAFE). Insert/Update/Delete/Truncate always hold PostgreSQL's
	// concrete resolved value (true for all four when WITH (publish = ...)
	// is omitted — PostgreSQL's own default, not a DPG-invented one) on
	// both sides, so no nil/unset tri-state is needed. Previously only
	// Body's opaque hash decided any of this, which (via Reconstructed,
	// below) went silently unset on every live path. FOR TABLES IN SCHEMA
	// targets are a pre-existing, separate gap: never modeled as a
	// structured field on either side (only captured in the opaque Body
	// text), so a schema-target-only change stays undetected by this
	// comparison — the same status quo as before this fix, not a
	// regression it introduces.
	AllTables                        bool
	Insert, Update, Delete, Truncate bool
	// RenamedFrom is RFC audit item #78's RENAMED FROM support — bare, like
	// Role's identical field: publications are cluster-level, not
	// schema-scoped, so there is no RenamedFromSchema counterpart.
	RenamedFrom *string
	// HasFilteredTables is true when any FOR TABLE entry carries an
	// explicit column list or WHERE row-filter (real PostgreSQL syntax:
	// FOR TABLE t (col1, col2) WHERE (expr)) — neither is captured by
	// PublicationTableRef (schema/name only), so a Tables-set change on a
	// publication using either can't be safely rendered as a targeted
	// ALTER PUBLICATION ... SET TABLE: doing so from PublicationTableRef
	// alone would silently rebuild the table list WITHOUT the original
	// column-list/WHERE filter, an unintentional narrowing (or removal)
	// of what's actually replicated — confirmed live: pg_publication_rel.
	// prattrs/prqual are non-NULL exactly when a filter was written,
	// distinguishing this from the implicit "all columns" case (which
	// pg_publication_tables.attnames can't distinguish, since it always
	// resolves to concrete column names either way). When true on either
	// side, diffPublication falls back to DROP+CREATE for a Tables
	// change instead of the lossy ALTER — full correctness at the cost of
	// forgoing the optimization for filtered publications specifically;
	// unfiltered publications (the common case) still get the real
	// ALTER PUBLICATION ... SET TABLE this fix adds.
	HasFilteredTables bool
	Reconstructed     bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	SecurityLabels    []pipeline.SecurityLabel
	SrcPos            pipeline.SourcePos
}

func (p *Publication) QualifiedName() string   { return p.Name }
func (p *Publication) Pos() pipeline.SourcePos { return p.SrcPos }
func (p *Publication) irObject()               {}

// Subscription is a CREATE SUBSCRIPTION declaration.
//
// ConnInfo is the native CONNECTION '...' literal, verbatim — an ordinary
// libpq conninfo string, resolved at apply time via pipeline.ResolveTemplate:
// plain text is used as-is, and any {{<secret-uri>}} placeholders within it
// (the whole value, or just a fragment like the password) are substituted.
// {{...}} is the only way a secret reference is ever recognized — a real
// conninfo/DSN literal may itself contain a ':' (e.g. a postgresql:// URI),
// so nothing else triggers resolution.
type Subscription struct {
	Name          string
	ConnInfo      string
	Body          string
	Comment       *string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom names the subscription's prior identity (RENAMED FROM) —
	// bare, like ForeignDataWrapper's identical field: subscriptions are
	// database-level, not schema-scoped.
	RenamedFrom *string
	// Owner is Section 13.2's ALTER SUBSCRIPTION ... OWNER TO diffing
	// input — same shape as ForeignServer.Owner.
	Owner          *string
	SecurityLabels []pipeline.SecurityLabel
	SrcPos         pipeline.SourcePos
}

func (s *Subscription) QualifiedName() string   { return s.Name }
func (s *Subscription) Pos() pipeline.SourcePos { return s.SrcPos }
func (s *Subscription) irObject()               {}

// EventTrigger is a CREATE EVENT TRIGGER declaration.
type EventTrigger struct {
	Name string
	// Event/Tags are RFC Section 14.1's structured diffing inputs, alongside
	// Function below: PostgreSQL has no ALTER EVENT TRIGGER for any of
	// these three (only ENABLE/DISABLE/OWNER TO/RENAME TO, none modeled
	// here), so any change to Event, Tags, or Function decides DROP+CREATE
	// — previously only Body's opaque hash decided this, which (via
	// Reconstructed, below) went silently unset on every live path,
	// missing any live-catalog change to an event trigger's definition.
	Event string
	Tags  []string
	// Function is the qualified name of the EXECUTE FUNCTION target, extracted
	// separately from Body for the dependency graph — an event trigger created
	// before the function it calls exists fails at apply time (confirmed
	// live: "function ... does not exist"), and nothing else in the IR
	// records this reference since EventTrigger is otherwise fully opaque.
	Function string
	// Comment is diffed/emitted independently of Body (COMMENT ON EVENT
	// TRIGGER), same as every other Comment-bearing opaque kind — added
	// alongside Cast/Operator/OperatorClass/OperatorFamily/Collation/
	// StatisticsObject/TSParser/TSTemplate, found live-testing a demo
	// project: PostgreSQL genuinely supports COMMENT ON for all of these
	// (confirmed via \h COMMENT against a real server), but the blockparser's
	// generic { COMMENT '...'; } was silently discarded for every one of
	// them — no field existed to store it, so dpg plan reported
	// "-- (no changes)" with no error and no effect.
	Comment       *string
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom names the event trigger's prior identity (RENAMED FROM,
	// Section 14.1) — bare, like Role's identical field: event triggers are
	// database-level, not schema-scoped, so there is no RenamedFromSchema
	// counterpart here.
	RenamedFrom *string
	Owner       *string
	// EnableState is Section 14.1's DISABLED/ENABLE REPLICA/ENABLE ALWAYS
	// trigger-enable-state — "" (omitted) means ENABLED, PostgreSQL's own
	// default. Confirmed empirically that real PostgreSQL's CREATE EVENT
	// TRIGGER grammar has no inline enable-state clause at all (rejects
	// "... EXECUTE FUNCTION f() ENABLE REPLICA;" outright, "syntax error at
	// or near ENABLE") despite RFC Section 14.1's own event-trigger-decl
	// ABNF placing trigger-enable-state there — the same class of
	// RFC-documents-unparseable-PG-syntax gap domain NOT VALID had. Unlike
	// domain's inline-only grammar, event-trigger-block DOES have a working
	// { } block alternative, so — deviating from the RFC's literal Part-1
	// placement — this is parsed as a block directive instead
	// (pipeline.BlockAST.TriggerEnableState), reusing table Trigger's
	// (Section 7.9, audit item #56) identical value set/emitted-SQL shape
	// via triggerEnableStateSQL, just against a database-level target with
	// no table name.
	EnableState    string
	SecurityLabels []pipeline.SecurityLabel
	SrcPos         pipeline.SourcePos
}

func (e *EventTrigger) QualifiedName() string   { return e.Name }
func (e *EventTrigger) Pos() pipeline.SourcePos { return e.SrcPos }
func (e *EventTrigger) irObject()               {}

// Collation is a CREATE COLLATION declaration.
type Collation struct {
	Schema string
	Name   string
	// Provider/Collate/Ctype/ICULocale/Deterministic are RFC Section 14.2's
	// structured diffing inputs: "any property change requires DROP
	// COLLATION + CREATE COLLATION" (real PostgreSQL's ALTER COLLATION
	// supports only REFRESH VERSION/OWNER TO/RENAME TO/SET SCHEMA, none
	// of these five), so this is a single "did anything change"
	// comparison, same shape as ForeignDataWrapper. Comparing these
	// resolved fields directly — rather than the LOCALE-vs-LC_COLLATE/
	// LC_CTYPE shorthand text — is what actually closes the gap
	// convert.go's sourceBodyHash doc comment names as the reason
	// Collation needed Reconstructed's hash-exclusion in the first place:
	// PostgreSQL always resolves LOCALE into concrete collcollate/
	// collctype (libc/default/builtin providers) or colllocale (icu)
	// values regardless of which shorthand the source used (confirmed
	// live against a real PostgreSQL 17 server), so comparing the
	// resolved values sidesteps the shorthand-equivalence problem
	// entirely rather than needing a text-normalization function.
	// Provider is PostgreSQL's own single-letter catalog code ("c" =
	// libc, the default when PROVIDER is omitted; "i" = icu; "b" =
	// builtin). Deterministic defaults true (PostgreSQL's own default
	// when DETERMINISTIC is omitted), not a DPG-invented default.
	Provider       string
	Collate, Ctype *string
	ICULocale      *string
	Deterministic  bool
	// Rules is RFC audit item #111's PG16+ ICU tailoring rules string
	// (`RULES = 'rules'`) — nil when not declared. Part of the same
	// significant-property set as Provider/Collate/Ctype/ICULocale/
	// Deterministic above: any change decides DROP+CREATE (real
	// PostgreSQL's ALTER COLLATION has no way to alter it in place), so a
	// RULES-only change must be compared here too or it's undetected
	// drift, not just an unoptimized DROP+CREATE.
	Rules *string
	// CopyFrom is RFC Section 14.2's "FROM existing_collation" copy-from
	// shorthand — nil when not declared. CREATE-time only, deliberately
	// excluded from any ongoing diff comparison (no SnapOpaque field, same
	// "no live catalog trace of it" reasoning as ir.Index.Only): real
	// PostgreSQL resolves the referenced collation's properties into this
	// collation's own independent catalog row at creation time, with no
	// persisted link back afterward, so there is nothing to introspect —
	// and DPG's Builder processes one object at a time with no visibility
	// into another declared collation, so it can't resolve what a copied
	// collation's Provider/Collate/Ctype/ICULocale/Deterministic/Rules
	// SHOULD be either. diffCollation skips the structured 6-property
	// comparison entirely when this is set (comparing it against this
	// struct's own hardcoded Go zero-value defaults — Provider "c",
	// Deterministic true — would otherwise be actively wrong, not just
	// unhelpful, producing a permanent spurious DROP+CREATE against the
	// real resolved values introspection reports).
	CopyFrom *string
	Comment  *string // see EventTrigger.Comment
	// RenamedFrom/RenamedFromSchema — see Table's identical doc comments;
	// reuses the same generic cross-schema RENAME TO/SET SCHEMA mechanism.
	RenamedFrom       *string
	RenamedFromSchema *string
	// Owner is RFC audit item #81's ALTER COLLATION ... OWNER TO diffing
	// input — a normal block-only directive, unlike Tablespace's inline
	// OWNER (see ir.Tablespace.Owner's doc comment for that contrast).
	Owner *string
	// RefreshVersion — see pipeline.BlockAST.RefreshVersion's identical
	// doc comment (RFC audit item #84).
	RefreshVersion bool
	Body           string
	Reconstructed  bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	SrcPos         pipeline.SourcePos
}

func (c *Collation) QualifiedName() string   { return qualName(c.Schema, c.Name) }
func (c *Collation) Pos() pipeline.SourcePos { return c.SrcPos }
func (c *Collation) irObject()               {}

// Operator is a CREATE OPERATOR declaration.
type Operator struct {
	Schema string
	Name   string
	// LeftType/RightType are the operator's operand types (nil for the side
	// a unary/prefix operator omits — PostgreSQL requires RightType in
	// practice since postfix operators were removed in PG14, but this
	// mirrors the grammar's actual optionality rather than assuming it).
	LeftType  *TypeRef
	RightType *TypeRef
	// Function is the qualified name of the PROCEDURE target, extracted
	// separately from Body for the dependency graph — same reasoning as
	// EventTrigger.Function/Cast.Function: an operator created before its
	// function exists fails at apply time ("function ... does not exist"),
	// and nothing else in the IR records this reference since Operator is
	// otherwise fully opaque.
	Function string
	// Restrict/Join are the qualified names of the RESTRICT/JOIN selectivity
	// estimator functions (nil = not declared). Commutator/Negator are the
	// referenced operator's dotted schema.symbol (schema-less when
	// unqualified in source). Hashes/Merges are the bare HASHES/MERGES
	// presence keywords. All six are RFC Section 14.3's optimizer-hint
	// properties, structured here (rather than folded into the opaque Body
	// hash, the one treatment every sibling opaque kind — FDW, Foreign
	// Server, Collation, Cast, Statistics Object, Event Trigger — already
	// got) so a hint-only change can route through a real
	// ALTER OPERATOR ... SET (...) instead of forcing a DROP+CREATE.
	//
	// Real PostgreSQL's ALTER OPERATOR ... SET (confirmed live against a
	// PostgreSQL 17 server) is asymmetric: RESTRICT/JOIN are freely
	// re-settable and clearable (SET (RESTRICT = NONE)) in place; but
	// COMMUTATOR/NEGATOR/HASHES/MERGES can only move from unset to set —
	// changing (or clearing) an already-set value on any of those four is
	// flatly rejected ("operator attribute ... cannot be changed if it has
	// already been set"), so that specific transition still needs the
	// generic DROP+CREATE path (see diffOperator).
	Restrict      *string
	Join          *string
	Commutator    *string
	Negator       *string
	Hashes        bool
	Merges        bool
	Comment       *string // see EventTrigger.Comment
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	SrcPos        pipeline.SourcePos
}

// QualifiedName includes the operand types because PostgreSQL identifies an
// operator by (schema, name, lefttype, righttype) — the same symbol can be
// overloaded across operand types (e.g. integer + integer vs numeric +
// numeric) — so the flat, name-keyed snapshot and diff maps must key on all
// of it or two distinct operators would silently collide, one overwriting
// the other (the same class of bug OperatorClass/OperatorFamily had before
// their QualifiedName was widened for the same reason). The format mirrors
// PostgreSQL's own DROP OPERATOR operand-list syntax verbatim (including the
// literal NONE for a missing side) so OperandsKey doubles as both the
// identity suffix here and dropObject's DROP OPERATOR argument list.
func (o *Operator) QualifiedName() string {
	return qualName(o.Schema, o.Name) + "(" + OperandsKey(o.LeftType, o.RightType) + ")"
}
func (o *Operator) Pos() pipeline.SourcePos { return o.SrcPos }
func (o *Operator) irObject()               {}

// OperandsKey renders an operator's operand types as PostgreSQL's own DROP
// OPERATOR syntax requires them: "lefttype, righttype", substituting the
// literal NONE for whichever side a unary operator omits.
func OperandsKey(left, right *TypeRef) string {
	l, r := "NONE", "NONE"
	if left != nil {
		l = left.String()
	}
	if right != nil {
		r = right.String()
	}
	return l + ", " + r
}

// OperatorClass is a CREATE OPERATOR CLASS declaration.
type OperatorClass struct {
	Schema       string
	Name         string
	AccessMethod string // the index access method (btree/gin/gist/...); needed for DROP
	// FamilyName, when non-empty, is the class's explicit FAMILY clause target
	// (FamilySchema empty means unqualified, i.e. the class's own schema) —
	// parsed identically from hand-written source (CreateOpClassStmt.Opfamilyname)
	// and introspection. Populated purely for dependency-edge ordering (see
	// graph.go): the FAMILY clause text itself always lives in Body, since
	// introspection now always emits it explicitly (pg_dump's model — no
	// "implicit same-name family" special case). Empty FamilyName is only
	// possible for hand-written source that omits FAMILY, relying on
	// PostgreSQL's own same-name auto-creation.
	FamilySchema string
	FamilyName   string
	// Functions holds the qualified names of every FUNCTION support-item
	// target in the class body (a class can declare several, numbered per
	// support-function slot — e.g. FUNCTION 1/2/3/4 for btree). Populated
	// purely for dependency-edge ordering, same reasoning as
	// EventTrigger.Function/Cast.Function/Operator.Function: a class
	// created before one of its support functions exists fails at apply
	// time, and nothing else in the IR records these references since the
	// class is otherwise fully opaque.
	Functions []string
	// Members and StorageType are the AS-list's OPERATOR/FUNCTION items and
	// optional STORAGE clause, captured structurally (previously the builder
	// captured only FUNCTION items, and only as bare names via Functions
	// above — OPERATOR and STORAGETYPE items were silently dropped, while
	// introspection captured all three but only as flattened clause text
	// folded into Body, never structurally — an asymmetry that left the same
	// class represented inconsistently depending on which path produced it).
	// Diffing still can't be incremental the way OperatorFamily's loose
	// members are (PostgreSQL has no ALTER OPERATOR CLASS ADD/DROP at all —
	// RFC Section 14.4), so Body remains the source of truth for DROP+CREATE SQL;
	// Members/StorageType exist so the differ can compare structurally
	// instead of on raw BodyHash, avoiding a false DESTRUCTIVE diff when
	// hand-written source and an introspected reconstruction differ only in
	// cosmetic text (whitespace, operator qualification, type spelling).
	Members       []pipeline.OpFamilyMember
	StorageType   string  // STORAGE clause's type name; "" if the class declares none
	Comment       *string // see EventTrigger.Comment
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom/RenamedFromSchema — see Table's identical doc comments;
	// real PostgreSQL supports ALTER OPERATOR CLASS ... RENAME TO/SET
	// SCHEMA (Owner is a separate, still-open gap, out of scope here). The
	// rename's FROM side additionally needs a USING access_method
	// qualifier, unlike Table/View — an operator class name is only unique
	// per access method (see QualifiedName's doc comment) — so this is
	// diffed with bespoke logic in diffOperatorClass, not the shared
	// renameOpaqueIfUnchanged helper TSDict/TSParser/TSTemplate use.
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

// QualifiedName includes the access method because operator class names are
// unique only within a method, so two same-named classes under different methods
// must key distinctly in the flat, name-keyed snapshot and diff maps. See
// OperatorFamily.QualifiedName for why classes and families must not collide.
func (o *OperatorClass) QualifiedName() string {
	return qualName(o.Schema, o.Name) + " USING " + o.AccessMethod
}
func (o *OperatorClass) Pos() pipeline.SourcePos { return o.SrcPos }
func (o *OperatorClass) irObject()               {}

// OperatorFamily is a CREATE OPERATOR FAMILY declaration.
type OperatorFamily struct {
	Schema        string
	Name          string
	AccessMethod  string  // the index access method (btree/gin/gist/...); needed for DROP
	Comment       *string // see EventTrigger.Comment
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// Members are the family's "loose" members — the ones real PG only lets
	// you attach via ALTER OPERATOR FAMILY ... ADD, i.e. those that belong to
	// the family directly rather than to one of its operator classes'
	// AS-lists (RFC Section 14.4). Declared in the DPG { } block (Part 2), so Part 1
	// stays exactly the bare, valid CREATE OPERATOR FAMILY statement
	// pgparser/reconstruct.go's "CREATE OPERATOR FAMILY " prefix assumes.
	// Populated for both source-declared and introspected/reconstructed
	// families alike (unlike Body, never gated on Reconstructed) — diffed
	// structurally and incrementally (diff.diffOpFamilyMembers), unlike
	// OperatorClass's AS-list, which stays whole-body passthrough because
	// PostgreSQL genuinely offers no incremental opclass member DDL.
	Members []pipeline.OpFamilyMember
	// RenamedFrom/RenamedFromSchema — see OperatorClass's identical doc
	// comment (same USING access_method rename-qualifier shape).
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

// QualifiedName carries a trailing " FAMILY" so an operator family never
// collides with a same-named operator class in the flat, name-keyed snapshot and
// diff maps — PostgreSQL auto-creates a same-named family for every class, so the
// collision is the common case, not an edge one. The access method is included
// for the same reason as OperatorClass: names are unique only per method.
func (o *OperatorFamily) QualifiedName() string {
	return qualName(o.Schema, o.Name) + " USING " + o.AccessMethod + " FAMILY"
}
func (o *OperatorFamily) Pos() pipeline.SourcePos { return o.SrcPos }
func (o *OperatorFamily) irObject()               {}

// Cast is a CREATE CAST declaration.
type Cast struct {
	SourceType TypeRef
	TargetType TypeRef
	// Method/Context are RFC Section 14.5's structured diffing inputs, alongside
	// Function below: PostgreSQL provides no ALTER CAST at all, so any
	// change to Method, Function, or Context decides DROP+CREATE —
	// previously only Body's opaque hash decided this, which (via
	// Reconstructed, below) went silently unset on every live path,
	// missing any live-catalog change to a cast's definition. Values use
	// the same single-letter catalog vocabulary as pg_cast.castmethod
	// ("f" = WITH FUNCTION, "i" = WITH INOUT, "b" = WITHOUT FUNCTION/
	// binary-coercible) and pg_cast.castcontext ("e" = no AS clause/
	// explicit-only — PostgreSQL's grammar has no "AS EXPLICIT" — "a" =
	// AS ASSIGNMENT, "i" = AS IMPLICIT), so both sides (source-parsed and
	// introspected) always populate the same three-value vocabulary with
	// no translation needed.
	Method  string
	Context string
	// Function is the qualified name of the WITH FUNCTION target, extracted
	// separately from Body for the dependency graph — empty for WITHOUT
	// FUNCTION/WITH INOUT casts, which have no function to order against.
	// Same reasoning as EventTrigger.Function: a cast created before its
	// function exists fails at apply time ("function ... does not exist"),
	// and nothing else in the IR records this reference since Cast is
	// otherwise fully opaque.
	Function      string
	Comment       *string // see EventTrigger.Comment
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	SrcPos        pipeline.SourcePos
}

func (c *Cast) QualifiedName() string   { return c.SourceType.String() + "->" + c.TargetType.String() }
func (c *Cast) Pos() pipeline.SourcePos { return c.SrcPos }
func (c *Cast) irObject()               {}

// StatisticsObject is a CREATE STATISTICS declaration.
type StatisticsObject struct {
	Schema string
	Name   string
	// Table/Kinds/Columns/StatisticsTarget are RFC Section 14.6's structured
	// diffing inputs: real PostgreSQL's ALTER STATISTICS supports only
	// OWNER TO/RENAME TO/SET SCHEMA/SET STATISTICS (confirmed live via
	// `\h ALTER STATISTICS` against a real PostgreSQL 17 server), so a
	// Table, Kinds, or Columns change decides DROP+CREATE (RFC's own
	// "Column list or kinds changed" row, DESTRUCTIVE — Table isn't
	// separately named in the RFC's table but requires the identical
	// treatment, since there's no ALTER STATISTICS ... FROM either), while
	// a StatisticsTarget-only change gets a real, targeted
	// ALTER STATISTICS ... SET STATISTICS (RFC: SAFE). Kinds uses DPG's
	// own source spelling ("ndistinct"/"dependencies"/"mcv"), converted
	// to/from PostgreSQL's single-letter stxkind codes ('d'/'f'/'m') at
	// the introspection/builder boundary — pg_statistic_ext's 'e' code
	// (has-expressions) is an internal marker PostgreSQL adds
	// automatically, never a user-requested kind, and is excluded.
	// Columns holds both plain column names and expression text exactly
	// as pg_get_statisticsobjdef_expressions renders it (canonical
	// deparse form, confirmed live) — the builder canonicalizes
	// expressions the same way (via nodeToText's existing deparse
	// wrapping) so both sides compare equal regardless of source
	// formatting. Both Kinds and Columns are compared as sets
	// (order-independent). StatisticsTarget is nil when unset (matches
	// Column.Statistics' existing *int nil-means-default precedent) —
	// pg_statistic_ext.stxstattarget is genuinely NULL in this case,
	// confirmed live, not a sentinel value.
	Table            string
	Kinds            []string
	Columns          []string
	StatisticsTarget *int
	Comment          *string // see EventTrigger.Comment
	Body             string
	Reconstructed    bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom/RenamedFromSchema — see Table's identical doc comments;
	// real PostgreSQL supports ALTER STATISTICS ... RENAME TO/SET SCHEMA
	// (Owner is a separate, still-open gap, out of scope here).
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

func (s *StatisticsObject) QualifiedName() string   { return qualName(s.Schema, s.Name) }
func (s *StatisticsObject) Pos() pipeline.SourcePos { return s.SrcPos }
func (s *StatisticsObject) irObject()               {}

// TSConfig is a CREATE TEXT SEARCH CONFIGURATION declaration.
type TSConfig struct {
	Schema string
	Name   string
	// ParserName is always non-empty for valid source/introspection — PARSER
	// is mandatory in CREATE TEXT SEARCH CONFIGURATION's grammar, unlike
	// OperatorClass's optional FAMILY. Populated purely for dependency-edge
	// ordering (see graph.go); the PARSER clause text itself always lives in
	// Body already. ParserSchema empty means unqualified (the config's own
	// schema).
	ParserSchema string
	ParserName   string
	Body         string
	Mappings     []pipeline.TSMappingDef
	Comment      *string
	// Owner is Section 12.1's ALTER TEXT SEARCH CONFIGURATION ... OWNER TO
	// diffing input — same shape as TSDict.Owner (real PostgreSQL has an
	// OWNER concept for a config too, unlike TSParser/TSTemplate).
	Owner         *string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom/RenamedFromSchema name the configuration's previous
	// identity (RENAMED FROM, RFC Section 12.1).
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

func (t *TSConfig) QualifiedName() string   { return qualName(t.Schema, t.Name) }
func (t *TSConfig) Pos() pipeline.SourcePos { return t.SrcPos }
func (t *TSConfig) irObject()               {}

// TSDict is a CREATE TEXT SEARCH DICTIONARY declaration.
type TSDict struct {
	Schema string
	Name   string
	// TemplateName is always non-empty for valid source/introspection —
	// TEMPLATE is mandatory in CREATE TEXT SEARCH DICTIONARY's grammar.
	// Populated purely for dependency-edge ordering (see graph.go); the
	// TEMPLATE clause text itself always lives in Body already.
	// TemplateSchema empty means unqualified (the dict's own schema).
	TemplateSchema string
	TemplateName   string
	Body           string
	// Options is Section 12.2's dictionary option list (LANGUAGE, STOPWORDS,
	// ...), excluding TEMPLATE (tracked separately above) — audit item #52's
	// structured diffing input. Real PostgreSQL supports inline options at
	// CREATE time already (unlike Table's pre-#60 WITH (...) gap), so this
	// field is diffing-only; createOpaque's raw Body passthrough already
	// renders them correctly at creation.
	Options       []pipeline.StorageParam
	Comment       *string
	Owner         *string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom/RenamedFromSchema — see Table's identical doc comments;
	// real PostgreSQL supports ALTER TEXT SEARCH DICTIONARY ... RENAME TO/
	// SET SCHEMA/OWNER TO, all three now modeled.
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

func (t *TSDict) QualifiedName() string   { return qualName(t.Schema, t.Name) }
func (t *TSDict) Pos() pipeline.SourcePos { return t.SrcPos }
func (t *TSDict) irObject()               {}

// TSParser is a CREATE TEXT SEARCH PARSER declaration.
type TSParser struct {
	Schema string
	Name   string
	// Functions holds the qualified names of the parser's START/GETTOKEN/
	// END/LEXTYPES/HEADLINE support functions. Populated purely for
	// dependency-edge ordering (see graph.go) — same reasoning as
	// OperatorClass.Functions: a parser created before one of its support
	// functions exists fails at apply time, and nothing else in the IR
	// records these references since the parser is otherwise fully opaque.
	// In practice these almost always name a pg_catalog built-in (custom
	// ones require C, since the required internal return types can't be
	// produced from SQL/PLpgSQL) — never errors when unresolved, same as
	// every other soft reference in this file.
	Functions     []string
	Comment       *string // see EventTrigger.Comment
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom/RenamedFromSchema — see Table's identical doc comments;
	// real PostgreSQL supports ALTER TEXT SEARCH PARSER ... RENAME TO/SET
	// SCHEMA, but genuinely has no OWNER concept for a parser at all (unlike
	// TSDict), so there is no analogous Owner gap here to track.
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

func (t *TSParser) QualifiedName() string   { return qualName(t.Schema, t.Name) }
func (t *TSParser) Pos() pipeline.SourcePos { return t.SrcPos }
func (t *TSParser) irObject()               {}

// TSTemplate is a CREATE TEXT SEARCH TEMPLATE declaration.
type TSTemplate struct {
	Schema string
	Name   string
	// Functions holds the qualified names of the template's INIT/LEXIZE
	// support functions — same reasoning as TSParser.Functions.
	Functions     []string
	Comment       *string // see EventTrigger.Comment
	Body          string
	Reconstructed bool // Body rebuilt from the catalog; see Tablespace.Reconstructed
	// RenamedFrom/RenamedFromSchema — see TSParser's identical doc comment
	// (same "RENAME TO/SET SCHEMA exist, OWNER does not" shape).
	RenamedFrom       *string
	RenamedFromSchema *string
	SrcPos            pipeline.SourcePos
}

func (t *TSTemplate) QualifiedName() string   { return qualName(t.Schema, t.Name) }
func (t *TSTemplate) Pos() pipeline.SourcePos { return t.SrcPos }
func (t *TSTemplate) irObject()               {}

// ── VtypeBody — virtual type body DSL ────────────────────────────────────────

// VtypeBody is a discriminated union for the body of a VIRTUAL TYPE declaration.
// It is one of VtypeTypeRef, VtypeComposite, or VtypeUnion.
type VtypeBody interface{ vtypeBody() }

// VtypeTypeRef references a PostgreSQL type or another declared VIRTUAL
// TYPE. IsArray marks a [] suffix: a JSON array of that type.
type VtypeTypeRef struct {
	Schema  string // empty for unqualified references
	Name    string
	IsArray bool
	// Virtual is true when the reference names a declared VIRTUAL TYPE;
	// Schema is then always set. Set by ResolveVirtualTypes.
	Virtual bool
}

func (r VtypeTypeRef) vtypeBody() {}

func (r VtypeTypeRef) String() string {
	s := qualName(r.Schema, r.Name)
	if r.IsArray {
		s += "[]"
	}
	return s
}

// VtypeField is a named field inside a VtypeComposite body.
type VtypeField struct {
	Name string
	Type VtypeTypeRef // field types are simple type references
}

// VtypeComposite is an inline record definition: (field1 TYPE1, field2 TYPE2, ...).
type VtypeComposite struct {
	Fields []VtypeField
}

func (c VtypeComposite) vtypeBody() {}

// VtypeUnion is a union of two or more VtypeBody terms joined with |.
type VtypeUnion struct {
	Members []VtypeBody // each member is VtypeComposite or VtypeTypeRef
}

func (u VtypeUnion) vtypeBody() {}

// ── VirtualType ───────────────────────────────────────────────────────────────

// VirtualType is a VIRTUAL TYPE declaration — a DPG-native construct that gives
// a structural schema to JSON/JSONB columns and JSON array columns.  It has no
// backing PostgreSQL DDL (no CREATE/ALTER/DROP TYPE is ever emitted).  Columns
// and composite type attributes may reference a virtual type directly; DPG
// resolves those references to jsonb / jsonb[] in generated SQL.  The structured
// body is stored in the snapshot for downstream consumers (ORMs, type-safe query
// builders) that read the DPG snapshot or IR via the pkg/dpg API.
type VirtualType struct {
	Schema     string
	Name       string
	Body       VtypeBody // structured body: VtypeTypeRef | VtypeComposite | VtypeUnion
	JsonFormat string    // "json" or "jsonb"; empty means default (jsonb)
	Comment    *string
	NameMaps   []pipeline.NameMapEntry
	SrcPos     pipeline.SourcePos
}

func (v *VirtualType) QualifiedName() string   { return qualName(v.Schema, v.Name) }
func (v *VirtualType) Pos() pipeline.SourcePos { return v.SrcPos }
func (v *VirtualType) irObject()               {}

// DefaultPrivileges is a DEFAULT PRIVILEGES declaration.
type DefaultPrivileges struct {
	InSchema    *string
	ForRole     *string
	ObjectType  string
	Grants      []Grant
	Revocations []Revocation
	SrcPos      pipeline.SourcePos
}

// QualifiedName must include ObjectType: PostgreSQL's own pg_default_acl
// catalog has one row per (role, schema, object type) tuple, and a single
// DPG declaration naming multiple object types (RFC's own worked example:
// TABLES, FUNCTIONS, and SEQUENCES together) is built into one
// *DefaultPrivileges per type (see Builder.BuildDefaultPrivileges) — without
// ObjectType here, those independently-diffable objects would collide under
// one identity and the merger would silently drop all but one.
func (d *DefaultPrivileges) QualifiedName() string {
	key := "DEFAULT PRIVILEGES"
	if d.ForRole != nil {
		key += " FOR " + *d.ForRole
	}
	if d.InSchema != nil {
		key += " IN " + *d.InSchema
	}
	if d.ObjectType != "" {
		key += " " + d.ObjectType
	}
	return key
}
func (d *DefaultPrivileges) Pos() pipeline.SourcePos { return d.SrcPos }
func (d *DefaultPrivileges) irObject()               {}

// ParameterGrant is one grant entry within a PARAMETER PRIVILEGES declaration
// (RFC Section 11.6, PG15+): "GRANT {SET|ALTER SYSTEM} ON PARAMETER
// param[, ...] TO role[, ...] [WITH GRANT OPTION]".
type ParameterGrant struct {
	Privileges []string // "SET" / "ALTER SYSTEM"; nil = ALL
	Parameters []string
	Roles      []string
	WithGrant  bool
	// GrantedBy — see Grant.GrantedBy's identical doc comment; real
	// PostgreSQL's GRANT ... ON PARAMETER accepts the same optional
	// GRANTED BY role-spec as every other GRANT form (confirmed live, PG17).
	GrantedBy *string
	Pos       pipeline.SourcePos
}

// ParameterRevocation is a PARAMETER PRIVILEGES REVOKES { } entry.
type ParameterRevocation struct {
	Privileges []string
	Parameters []string
	Roles      []string
	Cascade    bool
	// GrantedBy — see Grant.GrantedBy's identical doc comment.
	GrantedBy *string
	Pos       pipeline.SourcePos
}

// ParameterPrivileges is a PARAMETER PRIVILEGES declaration (RFC Section
// 11.6, PG15+). Cluster-scoped singleton: unlike DefaultPrivileges, which
// splits into one *DefaultPrivileges per (role, schema, object type) tuple
// matching pg_default_acl's own catalog model, PostgreSQL's pg_parameter_acl
// has one row per parameter with no role/schema/type dimension to split on,
// and a DPG project declares at most one PARAMETER PRIVILEGES block — so
// QualifiedName is a fixed constant.
type ParameterPrivileges struct {
	Grants      []ParameterGrant
	Revocations []ParameterRevocation
	SrcPos      pipeline.SourcePos
}

func (p *ParameterPrivileges) QualifiedName() string   { return "PARAMETER PRIVILEGES" }
func (p *ParameterPrivileges) Pos() pipeline.SourcePos { return p.SrcPos }
func (p *ParameterPrivileges) irObject()               {}

// ── helpers ───────────────────────────────────────────────────────────────────

// ArgsKey returns a compact type-only argument key for use in qualified names
// and snapshot identity. OUT and TABLE params are excluded — PG's overload
// identity is based on IN and INOUT types only.
func ArgsKey(args []FuncArg) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a.Mode == "OUT" || a.Mode == "TABLE" {
			continue
		}
		parts = append(parts, a.Type.String())
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}

// FuncTableColumns returns just the RETURNS TABLE(...) column list from a
// function's Args, in declaration order. A function using RETURNS TABLE
// carries its output columns as ordinary Args entries with Mode "TABLE" —
// they must never be rendered inline in the main parameter list (that's a
// distinct, invalid syntax; the real columns belong inside a separate
// RETURNS TABLE(...) clause).
func FuncTableColumns(args []FuncArg) []FuncArg {
	var cols []FuncArg
	for _, a := range args {
		if a.Mode == "TABLE" {
			cols = append(cols, a)
		}
	}
	return cols
}

// FormatTableColumns renders a RETURNS TABLE(...) column list's inner text
// ("name type, name type, ..."). Used both to render the clause (dump,
// diff-generated SQL) and as the comparable snapshot representation for
// drift detection — the two must always agree, since a mismatch here would
// either silently miss a genuine column-list change or falsely report one.
func FormatTableColumns(cols []FuncArg) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, c.Name+" "+c.Type.String())
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}

// Assert that all concrete types implement pipeline.IRObject.
var (
	_ pipeline.IRObject = (*Schema)(nil)
	_ pipeline.IRObject = (*Extension)(nil)
	_ pipeline.IRObject = (*Table)(nil)
	_ pipeline.IRObject = (*View)(nil)
	_ pipeline.IRObject = (*Function)(nil)
	_ pipeline.IRObject = (*Procedure)(nil)
	_ pipeline.IRObject = (*Aggregate)(nil)
	_ pipeline.IRObject = (*Type)(nil)
	_ pipeline.IRObject = (*Sequence)(nil)
	_ pipeline.IRObject = (*Role)(nil)
	_ pipeline.IRObject = (*Tablespace)(nil)
	_ pipeline.IRObject = (*ForeignDataWrapper)(nil)
	_ pipeline.IRObject = (*ForeignServer)(nil)
	_ pipeline.IRObject = (*UserMapping)(nil)
	_ pipeline.IRObject = (*Publication)(nil)
	_ pipeline.IRObject = (*Subscription)(nil)
	_ pipeline.IRObject = (*EventTrigger)(nil)
	_ pipeline.IRObject = (*Collation)(nil)
	_ pipeline.IRObject = (*Operator)(nil)
	_ pipeline.IRObject = (*OperatorClass)(nil)
	_ pipeline.IRObject = (*OperatorFamily)(nil)
	_ pipeline.IRObject = (*Cast)(nil)
	_ pipeline.IRObject = (*StatisticsObject)(nil)
	_ pipeline.IRObject = (*TSConfig)(nil)
	_ pipeline.IRObject = (*TSDict)(nil)
	_ pipeline.IRObject = (*TSParser)(nil)
	_ pipeline.IRObject = (*TSTemplate)(nil)
	_ pipeline.IRObject = (*DefaultPrivileges)(nil)
	_ pipeline.IRObject = (*VirtualType)(nil)
)

// DefaultMultirangeName returns the multirange type name PostgreSQL derives
// for a range type with no MULTIRANGE_TYPE_NAME: the first "range" in the
// range's name becomes "multirange", or "_multirange" is appended when the
// name has no "range".
func DefaultMultirangeName(rangeName string) string {
	if i := strings.Index(rangeName, "range"); i >= 0 {
		return rangeName[:i] + "multirange" + rangeName[i+len("range"):]
	}
	return rangeName + "_multirange"
}
