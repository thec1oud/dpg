package pipeline

import (
	"strconv"
	"strings"
)

// Identifier is a (possibly schema-qualified) SQL identifier.
type Identifier struct {
	Schema string
	Name   string
}

func (id Identifier) String() string {
	if id.Schema == "" {
		return id.Name
	}
	return id.Schema + "." + id.Name
}

// StringLit is a string literal value (unquoted).
type StringLit struct {
	Value string
	Pos   SourcePos
}

// RawExpr is an opaque SQL expression stored as raw text.
type RawExpr struct {
	Text string
	Pos  SourcePos
}

// StorageParam is a key=value pair from a WITH (...) clause.
type StorageParam struct {
	Key   string
	Value string
}

// IndexColumn is one column entry in an index definition.
type IndexColumn struct {
	Name      string // column name, or "" if Expr is set
	Expr      *RawExpr
	Collation *Identifier
	OpClass   *Identifier
	// OpClassParams is RFC Section 7.7's opclass(param = value, ...) form —
	// only meaningful when OpClass is set. Reuses StorageParam (the same
	// shape a WITH (...) clause already uses) rather than inventing a
	// parallel key/value type.
	OpClassParams []StorageParam
	Nulls         string // "FIRST", "LAST", or ""
	SortOrder     string // "ASC", "DESC", or ""
}

// IndexDef is a DPG INDEXES { } entry.
type IndexDef struct {
	Name             Identifier
	Unique           bool
	Method           *Identifier
	Columns          []IndexColumn
	Where            *RawExpr
	Include          []Identifier
	NullsNotDistinct bool
	With             []StorageParam
	Tablespace       *Identifier
	Concurrently     bool
	// Only is RFC Section 7.7's ON ONLY prefix — suppresses recursion into
	// a partitioned table's own partitions, mirroring real PostgreSQL's
	// CREATE INDEX ... ON ONLY table exactly. Meaningful only when the
	// enclosing table has a PARTITION BY clause; combination validity is
	// left to PostgreSQL's own parser, same passthrough principle used
	// throughout this codebase.
	Only bool
	// RenamedFrom names the index's prior identity (RENAMED FROM, RFC
	// Section 7.7) — real PostgreSQL's ALTER INDEX ... RENAME TO takes a
	// bare new name only (moving schemas is a separate ALTER INDEX ... SET
	// SCHEMA, not modeled here), so this is a bare identifier, not the
	// qual-name cross-schema form Table/View/Function use.
	RenamedFrom *Identifier
	Comment     *StringLit
	Pos         SourcePos
}

// GrantEntry is a single GRANTS directive.
type GrantEntry struct {
	Privileges []string // "SELECT", "INSERT", etc.; nil = ALL
	Roles      []Identifier
	WithGrant  bool
	// GrantedBy is RFC audit item #90's optional "GRANTED BY role-spec"
	// clause — nil when not declared. A role-spec is either a plain
	// identifier or one of the fixed keywords CURRENT_ROLE/CURRENT_USER/
	// SESSION_USER (stored upper-cased for the keyword forms).
	GrantedBy *string
	Pos       SourcePos
}

// RevocationEntry is a single REVOKES directive.
type RevocationEntry struct {
	Privileges []string
	Roles      []Identifier
	// GrantedBy — see GrantEntry.GrantedBy's identical doc comment.
	GrantedBy *string
	Cascade   bool
	Pos       SourcePos
}

// PolicyDef is a single row-security policy definition.
type PolicyDef struct {
	Name       Identifier
	Command    string // "ALL", "SELECT", "INSERT", "UPDATE", "DELETE"
	Permissive bool   // true = PERMISSIVE (default), false = RESTRICTIVE
	Using      *RawExpr
	WithCheck  *RawExpr
	Roles      []Identifier
	// RenamedFrom names the policy's prior identity (RENAMED FROM, RFC
	// Section 7.8) — matched within the same table only, like
	// Constraint/Index's identical sub-object RENAMED FROM.
	RenamedFrom *Identifier
	Comment     *StringLit
	Pos         SourcePos
}

// TriggerDef is a single trigger definition inside a { } block.
type TriggerDef struct {
	Name    Identifier
	When    string   // "BEFORE", "AFTER", "INSTEAD OF"
	Events  []string // "INSERT", "UPDATE", "DELETE", "TRUNCATE"
	ForEach string   // "ROW", "STATEMENT"
	// UpdateOfColumns is the column list from "UPDATE OF col1, col2, ..."
	// (RFC audit item #1) — nil when the UPDATE event has no OF clause at
	// all (fires on any column update, PostgreSQL's own default). Only
	// ever set when Events contains "UPDATE".
	UpdateOfColumns []string
	// OldTransitionName/NewTransitionName are RFC Section 7.9's "REFERENCING OLD
	// TABLE AS ... NEW TABLE AS ..." transition-table names (audit item
	// #2) — nil when REFERENCING isn't present, or for whichever of
	// OLD/NEW wasn't named. A transition relation name is always a plain,
	// unqualified identifier (not a real relation, just a local alias
	// scoped to the trigger's execution), unlike Function below.
	OldTransitionName *string
	NewTransitionName *string
	Condition         *RawExpr
	Function          Identifier
	Args              []string
	// Constraint marks a constraint trigger (CREATE CONSTRAINT TRIGGER,
	// RFC Section 7.9). ConstraintFrom is its FROM table, if any;
	// Deferrable/InitiallyDeferred are its deferral clause.
	Constraint        bool
	ConstraintFrom    *Identifier
	Deferrable        bool
	InitiallyDeferred bool
	// EnableState is Section 7.9's trigger-enable-state — "DISABLED",
	// "ENABLE REPLICA", "ENABLE ALWAYS", or "" (omitted, meaning ENABLED —
	// PostgreSQL's own default for a newly-created trigger). Real
	// PostgreSQL has no such clause on CREATE TRIGGER itself (ALTER-only,
	// confirmed empirically); DPG models it declaratively anyway, the
	// same reasoning as RLS's enable-dir/force-dir (Section 7.8).
	EnableState string
	// DependsOnExtensions is Section 9.1's `DEPENDS ON EXTENSION ext`
	// directive reused verbatim for triggers (Section 7.9, audit item
	// #75) — the complete desired set, same shape as Function/Procedure's
	// identical field.
	DependsOnExtensions []string
	// RenamedFrom names the trigger's prior identity (RENAMED FROM, RFC
	// Section 7.9) — matched within the same table only, like
	// Constraint/Index/Policy's identical sub-object RENAMED FROM.
	RenamedFrom *Identifier
	Comment     *StringLit
	Pos         SourcePos
}

// ColumnBlock holds DPG-specific attributes for a single column.
type ColumnBlock struct {
	Name           Identifier
	Comment        *StringLit
	Statistics     *int
	Compression    *Identifier
	Storage        *Identifier
	Deprecated     *StringLit
	RenamedFrom    *Identifier
	Using          *RawExpr
	Grants         []GrantEntry
	Revocations    []RevocationEntry
	SecurityLabels []SecurityLabel
	NameMaps       []NameMapEntry
	// NameMapWarnings holds DPG-E031 findings (same tool key specified more
	// than once at this column's block level) — warning-only, never fails
	// compilation. NameMaps above has already been deduped to the winning
	// (last) entry per tool by the time this is populated.
	NameMapWarnings []LintDiagnostic
	Pos             SourcePos
}

// SecurityLabel is one "SECURITY LABEL [FOR provider] '...'" directive
// inside a { } block, mirroring real PostgreSQL's SECURITY LABEL statement
// (RFC Section 14.11 — only meaningful with a label provider installed, e.g.
// sepgsql). Provider == "" is the unqualified form: PostgreSQL resolves it
// to the sole loaded provider, and errors if zero or more than one is
// loaded. Unlike Comment (a single nilable value), a block may declare
// several SecurityLabel entries — one per provider — since real PostgreSQL
// lets multiple independent label providers label the same object at once.
type SecurityLabel struct {
	Provider string
	Label    string
	Pos      SourcePos
}

// ConstraintDef is an additional constraint attached in the { } block.
// Used for NOT VALID constraints or cross-file constraint additions.
type ConstraintDef struct {
	Name     Identifier
	Expr     RawExpr
	NotValid bool
	// NotEnforced is PostgreSQL 18+'s NOT ENFORCED modifier (RFC Section
	// 7.3), applicable to CHECK/FOREIGN KEY only — see ir.Constraint.NotEnforced.
	NotEnforced bool
	// RenamedFrom names the constraint's prior identity (RENAMED FROM, RFC
	// Section 7.3) — a bare identifier, matched within the same table's
	// constraint list; no cross-schema form, same as Index's (a
	// constraint's schema is always its parent table's).
	RenamedFrom *string
	Comment     *StringLit
	Pos         SourcePos
}

// PartitionBound describes a single partition's bounds, optionally with its
// own nested PARTITION BY clause and PARTITIONS block (sub-partitioning,
// RFC Section 7.13). SubStrategy is "" for a leaf partition; when non-empty,
// SubColumns/SubPartitions describe that partition's own partitioning —
// recursively, since a sub-partition may itself be further sub-partitioned.
type PartitionBound struct {
	Name        Identifier
	Bounds      RawExpr
	SubStrategy string // "RANGE"/"LIST"/"HASH"; "" means no sub-partitioning
	SubColumns  []string
	// RenamedFrom names the partition's prior identity (RFC Section 7.13's
	// RENAMED FROM addition) — matched within the same parent's partition
	// list, never cross-schema (a partition's schema is always its parent
	// table's, unlike Table/View/Function's cross-schema RENAMED FROM).
	RenamedFrom *string
	// AttachedFrom is RFC Section 7.13's "ATTACHED FROM existing_table"
	// form — attaches an already-existing standalone table (present in the
	// snapshot) as this partition instead of creating a new one. Name is
	// still set to AttachedFrom's own bare Name when this is set (the
	// existing table's identity becomes the partition's), so every
	// existing name-keyed lookup continues to work unchanged.
	AttachedFrom  *Identifier
	SubPartitions []PartitionBound
	// Foreign is RFC Section 7.13's "FOREIGN partition-name ... SERVER
	// server_name [OPTIONS (...)]" form — makes this partition a foreign
	// table (CREATE FOREIGN TABLE ... PARTITION OF) instead of a regular
	// one. Server/Options are only meaningful when Foreign is true; Server
	// is mandatory whenever Foreign is set (confirmed live: PostgreSQL's
	// own CREATE FOREIGN TABLE grammar rejects the statement outright with
	// no SERVER clause, partition or not).
	Foreign bool
	Server  *Identifier
	Options []StorageParam
	// Constraints are constraints declared directly in this partition's own
	// trailing { } body (RFC Section 7.3's "DROP CONSTRAINT ... ONLY" gap,
	// PostgreSQL 18+) — independent of whatever the parent declares, using
	// the identical CONSTRAINT/CONSTRAINTS block-directive grammar a table's
	// own { } body already accepts. Never populated for AttachedFrom (an
	// already-existing standalone table's own Constraints govern it there
	// instead).
	Constraints []ConstraintDef
	Pos         SourcePos
}

// PartitionDef is the PARTITIONS { } directive.
type PartitionDef struct {
	Partitions []PartitionBound
	Pos        SourcePos
}

// MigrateRemoveBlock is the MIGRATE REMOVE { } directive.
type MigrateRemoveBlock struct {
	Reason string
	SQL    RawExpr
	Pos    SourcePos
}

// EnumValueRenameDir is Section 5.1.1's "VALUE 'new' RENAMED FROM 'old'"
// enum-block directive — repeatable, since a single migration may rename
// more than one enum value. It is a block directive because PostgreSQL's
// CREATE TYPE ... AS ENUM value list accepts only bare string literals.
type EnumValueRenameDir struct {
	From string
	To   string
	Pos  SourcePos
}

// DefaultPrivilegeGrant is one GRANTS { } entry inside a DEFAULT PRIVILEGES
// block. Unlike a regular GrantEntry, real PostgreSQL's ALTER DEFAULT
// PRIVILEGES grammar requires an "ON <object-type>" clause per grant — the
// object type isn't implicit from an enclosing table/view the way it is for
// every other GRANTS block: "GRANT priv[, ...] ON {TABLES|SEQUENCES|
// FUNCTIONS|TYPES|SCHEMAS} TO role[, ...] [WITH GRANT OPTION]".
type DefaultPrivilegeGrant struct {
	ObjectType string // "TABLES", "SEQUENCES", "FUNCTIONS", "TYPES", "SCHEMAS"
	Privileges []string
	Roles      []Identifier
	WithGrant  bool
	Pos        SourcePos
}

// DefaultPrivilegeRevocation is REVOKES { } entry's DefaultPrivileges sibling.
type DefaultPrivilegeRevocation struct {
	ObjectType string
	Privileges []string
	Roles      []Identifier
	Cascade    bool
	Pos        SourcePos
}

// DefaultPrivilegesBlock is a DEFAULT PRIVILEGES { } entry. Real PostgreSQL's
// ALTER DEFAULT PRIVILEGES statement can never be complete without its
// GRANT/REVOKE action inline — a "[FOR ROLE x] [IN SCHEMA y]" header alone
// is not valid PG SQL on its own (confirmed live) — so, unlike every other
// DPG object kind, this is never split into a pg_query-parsed Part 1 and a
// blockparser-parsed Part 2; the whole declaration (header + block) is
// parsed here, with zero pg_query involvement. See
// blockparser.Parser.ParseDefaultPrivileges.
type DefaultPrivilegesBlock struct {
	InSchema    *Identifier
	ForRole     *Identifier
	Grants      []DefaultPrivilegeGrant
	Revocations []DefaultPrivilegeRevocation
	Pos         SourcePos
}

// ParameterGrant is one GRANTS { } entry inside a PARAMETER PRIVILEGES block
// (RFC Section 11.6, PG15+). Unlike DefaultPrivilegeGrant, there is no
// "ON <object-type>" clause — PostgreSQL's grantable parameter-ACL object
// type is always PARAMETER; Parameters (the list of GUC names after
// "ON PARAMETER") plays the role ObjectType plays there.
type ParameterGrant struct {
	Privileges []string // "SET" / "ALTER SYSTEM"; nil = ALL
	Parameters []Identifier
	Roles      []Identifier
	WithGrant  bool
	GrantedBy  *string
	Pos        SourcePos
}

// ParameterRevocation is REVOKES { } entry's ParameterPrivileges sibling.
type ParameterRevocation struct {
	Privileges []string
	Parameters []Identifier
	Roles      []Identifier
	Cascade    bool
	GrantedBy  *string
	Pos        SourcePos
}

// ParameterPrivilegesBlock is a PARAMETER PRIVILEGES { } declaration (RFC
// Section 11.6). A top-level, cluster-scoped singleton — configuration
// parameters have no schema, and unlike DEFAULT PRIVILEGES there is no FOR
// ROLE/IN SCHEMA header to split on. Real PostgreSQL's GRANT {SET|ALTER
// SYSTEM} ON PARAMETER ... statement (confirmed live) genuinely parses via
// pg_query as an ordinary GrantStmt, unlike ALTER DEFAULT PRIVILEGES — but
// the DPG "PARAMETER PRIVILEGES { GRANTS { ... } }" wrapper block itself
// still has no single native PG statement it corresponds to as a whole, so
// it is parsed here the same bypass way as DefaultPrivilegesBlock. See
// blockparser.Parser.ParseParameterPrivileges.
type ParameterPrivilegesBlock struct {
	Grants      []ParameterGrant
	Revocations []ParameterRevocation
	Pos         SourcePos
}

// TSMappingDef is a MAPPING FOR { } entry (TEXT SEARCH CONFIGURATION).
// Dictionaries is a fallback chain (real PG syntax: "WITH dict1, dict2, ...";
// dictionaries are tried in order until one recognizes the token) — a single
// entry is the common case, but the RFC's own worked example uses two.
type TSMappingDef struct {
	TokenTypes   []string
	Dictionaries []Identifier
	Pos          SourcePos
}

// OpFamilyMember is one "loose" (family-level) member declared in an
// OPERATOR FAMILY { } block — real PG only lets you attach these via
// ALTER OPERATOR FAMILY ... ADD, since a member may belong to the family
// directly rather than to any one of its operator classes (RFC Section 14.4). Each
// item's grammar is copied verbatim from that ALTER statement's own ADD
// list-item grammar, just without repeating the ALTER/family header.
type OpFamilyMember struct {
	IsFunction bool       // false = OPERATOR item, true = FUNCTION item
	Number     int        // strategy number (OPERATOR) / support number (FUNCTION)
	Name       Identifier // operator symbol ("<", "@>", ...) or function name
	LeftType   string     // op_type left; canonicalized by the IR builder
	RightType  string     // op_type right; canonicalized by the IR builder
	FuncArgs   []string   // FUNCTION only: declared argument types, canonicalized
	OrderBy    bool       // OPERATOR only: true = FOR ORDER BY, false (default) = FOR SEARCH
	SortFamily Identifier // OPERATOR + OrderBy only: the FOR ORDER BY target family
	Pos        SourcePos
}

// Key identifies this member's catalog slot: PostgreSQL's own unique indexes
// on pg_amop ((amopfamily, amoplefttype, amoprighttype, amopstrategy)) and
// pg_amproc ((amprocfamily, amproclefttype, amprocrighttype, amprocnum))
// never include the operator/function identity itself — so two members at
// the same slot with a different operator/function are the same catalog row
// changing shape (DROP the old one, ADD the new one), not two independent
// members. LeftType/RightType MUST already be canonicalized (see
// ir.ParseTypeText) before this is used as a diff key, or a same-type member
// written as "int4" on one side and "integer" on the other will misdiff.
func (m OpFamilyMember) Key() string {
	kind := "OPERATOR"
	if m.IsFunction {
		kind = "FUNCTION"
	}
	return kind + "|" + strconv.Itoa(m.Number) + "|" + m.LeftType + "|" + m.RightType
}

// AddClause renders this member's contribution to an
// "ALTER OPERATOR FAMILY ... ADD <clause>[, <clause>...]" statement.
func (m OpFamilyMember) AddClause() string {
	if m.IsFunction {
		return "FUNCTION " + strconv.Itoa(m.Number) + " (" + m.LeftType + ", " + m.RightType + ") " +
			m.Name.String() + "(" + strings.Join(m.FuncArgs, ", ") + ")"
	}
	clause := "OPERATOR " + strconv.Itoa(m.Number) + " " + m.Name.String() + "(" + m.LeftType + ", " + m.RightType + ")"
	if m.OrderBy {
		clause += " FOR ORDER BY " + m.SortFamily.String()
	}
	return clause
}

// DropClause renders this member's contribution to an
// "ALTER OPERATOR FAMILY ... DROP <clause>[, <clause>...]" statement. Real
// PG's DROP form identifies a member by slot only (number + op_types) — the
// operator/function name is never repeated.
func (m OpFamilyMember) DropClause() string {
	kind := "OPERATOR"
	if m.IsFunction {
		kind = "FUNCTION"
	}
	return kind + " " + strconv.Itoa(m.Number) + " (" + m.LeftType + ", " + m.RightType + ")"
}

// NameMapEntry is a single NAME MAP directive inside a { } block.
// Tool is the target tool identifier (e.g. "default", "prisma").
// IsLiteral=false: Value is a rule keyword (e.g. "LOWER_SNAKE_CASE").
// IsLiteral=true: Value is a literal target name (from a double-quoted string).
type NameMapEntry struct {
	Tool      string
	Value     string
	IsLiteral bool
	Pos       SourcePos
}

// ValidNameMapRules is the closed set of DPG-defined naming convention rules.
// Rule values in [namemaps] config sections and NAME MAP block directives must
// be one of these identifiers.
var ValidNameMapRules = map[string]bool{
	"LOWER_SNAKE_CASE":  true,
	"UPPER_SNAKE_CASE":  true,
	"LOWER_CAMEL_CASE":  true,
	"UPPER_CAMEL_CASE":  true,
	"LOWER_KEBAB_CASE":  true,
	"UPPER_KEBAB_CASE":  true,
	"TRAIN_CASE":        true,
	"LOWER_CASE":        true,
	"UPPER_CASE":        true,
	"PASCAL_SNAKE_CASE": true,
}

// BlockAST is the parsed representation of a DPG { } block.
// Populated by the BlockParser (Phase 4b). Fields absent from a given block
// remain at their zero value.
type BlockAST struct {
	Pos            SourcePos
	Comment        *StringLit
	Owner          *Identifier
	RenamedFrom    *Identifier
	Protected      bool
	Deprecated     *StringLit
	DropCascade    bool
	Indices        []IndexDef
	Policies       []PolicyDef
	Triggers       []TriggerDef
	Grants         []GrantEntry
	Revocations    []RevocationEntry
	SecurityLabels []SecurityLabel
	Columns        []ColumnBlock
	// Attributes holds composite-type ATTRIBUTE/ATTRIBUTES blocks (RFC
	// Section 5.2). Only Name, Comment, RenamedFrom and Pos are populated.
	Attributes          []ColumnBlock
	Constraints         []ConstraintDef
	EnableRLS           bool
	ForceRLS            bool
	Partitions          *PartitionDef
	MigrateRemove       *MigrateRemoveBlock
	DefaultPrivileges   []DefaultPrivilegesBlock
	Mappings            []TSMappingDef
	OpFamilyMembers     []OpFamilyMember
	PreferredJsonFormat string // "json" or "jsonb"; empty = not set (default jsonb)
	NameMaps            []NameMapEntry
	// NameMapWarnings holds DPG-E031 findings (same tool key specified more
	// than once at this object's top block level) — warning-only, never
	// fails compilation. NameMaps above has already been deduped to the
	// winning (last) entry per tool by the time this is populated.
	NameMapWarnings []LintDiagnostic
	// DomainDefault/DomainNotNull are RFC Section 5.4's DOMAIN-only "DEFAULT expr;"
	// and "NOT NULL;" block directives, MERGED with any DEFAULT/NOT NULL/
	// CHECK already present in the domain's Part 1 (real PG's own inline
	// CREATE DOMAIN syntax remains fully supported; this block form is
	// additive, not a replacement). CHECK constraints declared this way use
	// the existing Constraints field — a domain CHECK has the exact same
	// "CONSTRAINT name CHECK (expr);" shape as a table's.
	DomainDefault *RawExpr
	DomainNotNull bool
	// DependsOnExtensions is Section 9.1's `DEPENDS ON EXTENSION ext;`
	// func-block directive, repeatable — the complete desired set. A
	// literal `NO DEPENDS ON EXTENSION ext;` also parses (matching real
	// PostgreSQL's own ALTER FUNCTION grammar shape, mirrored here for
	// familiarity/passthrough symmetry) but contributes nothing to this
	// set — see parseDependsOnExtension's doc comment for why.
	DependsOnExtensions []string
	// ReplicaIdentity is Section 7.11's `REPLICA IDENTITY {DEFAULT|FULL|
	// NOTHING|USING INDEX name}` block directive — real PostgreSQL has no
	// CREATE TABLE-native clause for it, only ALTER TABLE, so like RLS it's
	// declared as a block directive. nil means the directive was omitted,
	// which PostgreSQL itself treats as DEFAULT.
	ReplicaIdentity *ReplicaIdentityDir
	// ClusterOn is Section 7.11's `CLUSTER ON index-name` block directive —
	// same "ALTER-only, no CREATE-time clause" reasoning as ReplicaIdentity.
	// nil means not declared; removing a previously-declared value emits
	// ALTER TABLE ... SET WITHOUT CLUSTER.
	ClusterOn *Identifier
	// RefreshVersion is Section 14.2's Collation-only `REFRESH VERSION`
	// block directive (RFC audit item #84) — a bare presence keyword with
	// no argument, describing an imperative action (re-read the OS/ICU
	// library's current collation version into the catalog) rather than
	// comparable target state, the same non-persistent shape as Sequence's
	// RESTART (Section 10). Its mere presence unconditionally re-emits
	// ALTER COLLATION ... REFRESH VERSION on every plan/apply for as long
	// as it remains declared.
	RefreshVersion bool
	// DetachedFrom is Section 7.13's `DETACHED FROM parent_table
	// [CONCURRENTLY]` block directive — the symmetric counterpart to a
	// PARTITIONS { } entry's `ATTACHED FROM` — nil when not declared.
	// Written on a standalone TABLE declaration that is currently a
	// partition of parent_table per the snapshot; converts it back into an
	// independent table instead of the plain DROP TABLE this document
	// would otherwise prescribe for "declared table disappeared from its
	// parent's PARTITIONS { } block."
	DetachedFrom *DetachedFromDirective
	// EnumValueRenames is Section 5.1.1's "VALUE 'new' RENAMED FROM 'old'"
	// enum-block directive — see EnumValueRenameDir's doc comment for why
	// it's block-level rather than the RFC's literal inline placement.
	EnumValueRenames []EnumValueRenameDir
	// TriggerEnableState is Section 14.1's DISABLED/ENABLE REPLICA/ENABLE
	// ALWAYS block directive for an EVENT TRIGGER — "" (omitted) means
	// ENABLED, PostgreSQL's own default. Same value set and vocabulary as
	// TriggerDef.EnableState (RFC's own ABNF: "trigger-enable-state ...
	// reused verbatim"), but a plain block directive here rather than an
	// inline Part-1 clause: unlike table Trigger (parsed entirely through
	// DPG's own custom grammar), EVENT TRIGGER's Part 1 is real PostgreSQL
	// DDL round-tripped through pg_query, and real CREATE EVENT TRIGGER has
	// no such inline clause at all (confirmed live: "... EXECUTE FUNCTION
	// f() ENABLE REPLICA;" is a syntax error) — the RFC's own event-
	// trigger-decl ABNF placement doesn't hold live, the same
	// RFC-documents-unparseable-PG-syntax gap domain NOT VALID had.
	TriggerEnableState string
	// RoleConfigs is RFC audit item #74's role-config-dir block directive
	// (Section 11.1), repeatable: `SET param {TO|=} value / SET param FROM
	// CURRENT / RESET param / RESET ALL`, each optionally `IN DATABASE db`.
	// Role-only in practice (the only object kind whose builder reads it),
	// but parsed generically here like every other block directive.
	RoleConfigs []RoleConfigDir
	// Memberships is RFC audit item #32's block-level membership directive
	// (Section 11.1): `IN ROLE`/`ROLE`/`ADMIN` entries carrying `WITH
	// ADMIN`/`INHERIT`/`SET` modifiers — real PostgreSQL's `CREATE ROLE`
	// inline `IN ROLE`/`ROLE`/`ADMIN` lists (parsed by the real PG parser,
	// not here) accept no such modifiers at all (confirmed live), so a
	// modifier-bearing membership can only be expressed this way, one entry
	// per directive (Mode B — no plural wrapping block, mirroring TRIGGER/
	// POLICY). Role-only in practice, same as RoleConfigs above.
	Memberships []RoleMembershipDir
}

// RoleMembershipDir is Section 11.1's block-level membership directive: `(IN
// ROLE / ROLE / ADMIN) identifier [WITH ADMIN [OPTION|boolean]] [WITH
// INHERIT boolean] [WITH SET boolean];`.
type RoleMembershipDir struct {
	// Role is the other role named in the directive.
	Role Identifier
	// Direction is "IN_ROLE" (this role becomes a member of Role — from the
	// `IN ROLE` keyword) or "ROLE" (Role becomes a member of this role —
	// from either the `ROLE` or `ADMIN` keyword; `ADMIN` is `ROLE` with
	// AdminDefault forcing the admin option on).
	Direction string
	// AdminDefault is true when the directive used the `ADMIN` keyword
	// (real PostgreSQL's `CREATE ROLE ... ADMIN` semantics: the named role
	// becomes a member WITH ADMIN OPTION) — the baseline Admin value before
	// any explicit `WITH ADMIN ...` clause overrides it.
	AdminDefault bool
	// Admin/Inherit/Set carry an explicit `WITH ADMIN`/`WITH INHERIT`/`WITH
	// SET` clause's boolean value when present — Admin is nil unless
	// explicitly declared (AdminDefault supplies the baseline otherwise);
	// Inherit/Set are nil when not declared at all (not managed — same
	// "declared, so managed" convention as GrantEntry.GrantedBy).
	Admin   *bool
	Inherit *bool
	Set     *bool
	Pos     SourcePos
}

// RoleConfigDir is Section 11.1's `ALTER ROLE ... [IN DATABASE db] SET
// param {TO|=} value` / `SET param FROM CURRENT` / `RESET param` / `RESET
// ALL` block directive.
type RoleConfigDir struct {
	// Param is the GUC name; empty when ResetAll is true.
	Param string
	// Value is the config-value text for the "SET param TO/= value" form,
	// exactly as written (a string-literal keeps its quotes, an integer/
	// boolean/identifier stays bare) — rendered back verbatim, the same
	// passthrough convention pipeline.StorageParam.Value already uses. nil
	// for FromCurrent and both RESET forms.
	Value *string
	// FromCurrent is the "SET param FROM CURRENT" form — Value is nil.
	FromCurrent bool
	// Reset marks either RESET form; ResetAll distinguishes "RESET ALL"
	// (Param empty) from "RESET param" (Param set).
	Reset    bool
	ResetAll bool
	// InDatabase is the optional "IN DATABASE db" qualifier — nil means
	// cluster-wide (every database).
	InDatabase *string
	Pos        SourcePos
}

// ReplicaIdentityDir is Section 7.11's REPLICA IDENTITY block directive.
type ReplicaIdentityDir struct {
	Mode      string // "DEFAULT", "FULL", "NOTHING", or "INDEX"
	IndexName string // only set when Mode == "INDEX"
	Pos       SourcePos
}

// DetachedFromDirective is Section 7.13's DETACHED FROM block directive.
// Table is the parent's reference (possibly schema-qualified — a
// partition's own schema always matches its parent's, but the parent
// reference itself follows the same table-ref shape RENAMED FROM/LIKE use).
// Concurrently mirrors real PostgreSQL's ALTER TABLE ... DETACH PARTITION
// ... CONCURRENTLY: runs the detach in two non-blocking steps instead of
// holding a brief ACCESS EXCLUSIVE lock on the parent. It cannot run inside
// a transaction block, so the op is non-transactional (Safety Caution).
type DetachedFromDirective struct {
	Table        Identifier
	Concurrently bool
	Pos          SourcePos
}
