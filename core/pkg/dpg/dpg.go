package dpg

import (
	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/linter"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"

	// Concrete implementations register into pipeline.Default via init().
	_ "github.com/thec1oud/dpg/internal/blockparser"
	_ "github.com/thec1oud/dpg/internal/diff"
	_ "github.com/thec1oud/dpg/internal/emit"
	_ "github.com/thec1oud/dpg/internal/graph"
	_ "github.com/thec1oud/dpg/internal/merger"
	_ "github.com/thec1oud/dpg/internal/pgparser"
	_ "github.com/thec1oud/dpg/internal/scanner"
)

// ── Source positions ──────────────────────────────────────────────────────────

// SourcePos is a source location (file path, line, column) attached to
// CompilerErrors and LintDiagnostics.
type SourcePos = pipeline.SourcePos

// ── IR interface and object kinds ─────────────────────────────────────────────

// IRObject is the common interface implemented by every compiled DPG object
// (Table, View, Function, Role, …). Use a type assertion to obtain the
// concrete type and access object-specific fields.
type IRObject = pipeline.IRObject

// ObjectKind is an enumeration that identifies the type of an IRObject without
// requiring a type assertion. Use obj.Kind() to retrieve it.
type ObjectKind = pipeline.ObjectKind

// ObjectKind constants — one per supported PostgreSQL object type.
const (
	KindUnknown             = pipeline.KindUnknown
	KindSchema              = pipeline.KindSchema
	KindExtension           = pipeline.KindExtension
	KindTable               = pipeline.KindTable
	KindUnloggedTable       = pipeline.KindUnloggedTable
	KindForeignTable        = pipeline.KindForeignTable
	KindView                = pipeline.KindView
	KindMaterializedView    = pipeline.KindMaterializedView
	KindRecursiveView       = pipeline.KindRecursiveView
	KindFunction            = pipeline.KindFunction
	KindProcedure           = pipeline.KindProcedure
	KindAggregate           = pipeline.KindAggregate
	KindEnum                = pipeline.KindEnum
	KindCompositeType       = pipeline.KindCompositeType
	KindRangeType           = pipeline.KindRangeType
	KindDomainType          = pipeline.KindDomainType
	KindBaseType            = pipeline.KindBaseType
	KindSequence            = pipeline.KindSequence
	KindRole                = pipeline.KindRole
	KindTablespace          = pipeline.KindTablespace
	KindFDW                 = pipeline.KindFDW
	KindServer              = pipeline.KindServer
	KindUserMapping         = pipeline.KindUserMapping
	KindPublication         = pipeline.KindPublication
	KindSubscription        = pipeline.KindSubscription
	KindEventTrigger        = pipeline.KindEventTrigger
	KindCollation           = pipeline.KindCollation
	KindOperator            = pipeline.KindOperator
	KindOperatorClass       = pipeline.KindOperatorClass
	KindOperatorFamily      = pipeline.KindOperatorFamily
	KindCast                = pipeline.KindCast
	KindStatisticsObject    = pipeline.KindStatisticsObject
	KindTSConfig            = pipeline.KindTSConfig
	KindTSDict              = pipeline.KindTSDict
	KindTSParser            = pipeline.KindTSParser
	KindTSTemplate          = pipeline.KindTSTemplate
	KindDefaultPrivileges   = pipeline.KindDefaultPrivileges
	KindParameterPrivileges = pipeline.KindParameterPrivileges
)

// ── Concrete IR object types ──────────────────────────────────────────────────

// Schema is a compiled SCHEMA declaration. It may nest tables, views,
// functions, and other schema-scoped objects within its source block.
type Schema = ir.Schema

// Extension is a compiled EXTENSION declaration (CREATE EXTENSION).
type Extension = ir.Extension

// Table is a compiled TABLE, UNLOGGED TABLE, or FOREIGN TABLE declaration.
// Columns, constraints, indexes, policies, triggers, and grants are
// accessible as slices on this struct.
type Table = ir.Table

// View is a compiled VIEW or MATERIALIZED VIEW declaration.
type View = ir.View

// Function is a compiled FUNCTION declaration. The body is stored as a
// SHA-256 hash (BodyHash) for change detection; the raw text is not retained
// after compilation.
type Function = ir.Function

// Procedure is a compiled PROCEDURE declaration. Follows the same model as
// Function.
type Procedure = ir.Procedure

// Aggregate is a compiled AGGREGATE declaration with two parenthesised
// argument groups per PostgreSQL syntax.
type Aggregate = ir.Aggregate

// Type is a compiled composite TYPE, ENUM, range type, domain, or base type
// declaration. The Variant field identifies which PostgreSQL type category
// this object represents.
type Type = ir.Type

// Sequence is a compiled SEQUENCE declaration.
type Sequence = ir.Sequence

// Role is a compiled ROLE declaration. Roles are cluster-level objects and
// are compiled from the cluster objects directory.
type Role = ir.Role

// Tablespace is a compiled TABLESPACE declaration. Tablespaces are
// cluster-level objects.
type Tablespace = ir.Tablespace

// ForeignDataWrapper is a compiled FOREIGN DATA WRAPPER declaration for a
// custom C-implemented FDW. Cluster-level object.
type ForeignDataWrapper = ir.ForeignDataWrapper

// ForeignServer is a compiled SERVER declaration for a foreign data wrapper.
// Database-level object.
type ForeignServer = ir.ForeignServer

// UserMapping is a compiled USER MAPPING declaration associating a local
// role with credentials on a foreign server.
type UserMapping = ir.UserMapping

// Publication is a compiled PUBLICATION declaration for logical replication.
type Publication = ir.Publication

// Subscription is a compiled SUBSCRIPTION declaration for logical replication.
type Subscription = ir.Subscription

// EventTrigger is a compiled EVENT TRIGGER declaration.
type EventTrigger = ir.EventTrigger

// Collation is a compiled COLLATION declaration.
type Collation = ir.Collation

// Operator is a compiled OPERATOR declaration.
type Operator = ir.Operator

// OperatorClass is a compiled OPERATOR CLASS declaration for an index
// access method.
type OperatorClass = ir.OperatorClass

// OperatorFamily is a compiled OPERATOR FAMILY declaration.
type OperatorFamily = ir.OperatorFamily

// Cast is a compiled CAST declaration defining an implicit or assignment cast
// between two types.
type Cast = ir.Cast

// StatisticsObject is a compiled extended STATISTICS declaration for
// multi-column query planning statistics.
type StatisticsObject = ir.StatisticsObject

// TSConfig is a compiled TEXT SEARCH CONFIGURATION declaration.
type TSConfig = ir.TSConfig

// TSDict is a compiled TEXT SEARCH DICTIONARY declaration.
type TSDict = ir.TSDict

// TSParser is a compiled TEXT SEARCH PARSER declaration.
type TSParser = ir.TSParser

// TSTemplate is a compiled TEXT SEARCH TEMPLATE declaration.
type TSTemplate = ir.TSTemplate

// DefaultPrivileges is a compiled DEFAULT PRIVILEGES declaration (ALTER
// DEFAULT PRIVILEGES FOR ROLE ...).
type DefaultPrivileges = ir.DefaultPrivileges

// ParameterPrivileges is a compiled PARAMETER PRIVILEGES declaration (RFC
// Section 11.6, PG15+ — GRANT {SET|ALTER SYSTEM} ON PARAMETER ...).
type ParameterPrivileges = ir.ParameterPrivileges

// VirtualType is a VIRTUAL TYPE declaration — a DPG-native type annotation
// with no backing PostgreSQL DDL. It is stored in the snapshot for downstream
// consumers (ORM generators, type checkers) but never included in migrations.
type VirtualType = ir.VirtualType

// ── IR sub-types ──────────────────────────────────────────────────────────────

// Column is a single column in a table, view, or composite type. It includes
// type information, nullability, defaults, generated/identity specs, storage
// attributes, and column-level grants. A column declared with a virtual
// type has VirtualType set to the qualified reference and Type set to the
// json/jsonb type stored in PostgreSQL.
type Column = ir.Column

// TypeRef is a SQL type reference with optional schema qualification, type
// modifiers (e.g. VARCHAR(255), NUMERIC(10,2)), and array dimensions.
type TypeRef = ir.TypeRef

// Index is a CREATE INDEX definition attached to a table. Concurrent creation,
// partial predicates, expression columns, and covering columns are represented
// as fields.
type Index = ir.Index

// Constraint is a table-level or column-level constraint (PRIMARY KEY, UNIQUE,
// FOREIGN KEY, CHECK, EXCLUSION). The NOT VALID lifecycle flag is tracked here.
type Constraint = ir.Constraint

// Policy is a row-level security policy definition attached to a table.
type Policy = ir.Policy

// Trigger is a trigger definition attached to a table. The trigger function is
// referenced by name; it must be declared as a separate Function object.
type Trigger = ir.Trigger

// Grant is a single GRANT directive declaring a privilege on an object for a
// role. Table-level and column-level grants both use this type.
type Grant = ir.Grant

// Revocation is a single REVOKE directive. Add explicit Revocation entries to
// remove privileges that were previously granted outside DPG.
type Revocation = ir.Revocation

// FuncArg is one argument in a Function or Procedure signature, including name,
// type, mode (IN/OUT/INOUT/VARIADIC), and optional default expression.
type FuncArg = ir.FuncArg

// FuncAttrs holds the attribute flags of a function or procedure: volatility,
// strictness, security model, parallel safety, cost, and GUC settings.
type FuncAttrs = ir.FuncAttrs

// PartitionSpec describes the partitioning strategy of a partitioned table:
// the partition method (RANGE, LIST, HASH) and the partition key expression.
type PartitionSpec = ir.PartitionSpec

// Partition is one partition entry in a PARTITIONS { } block: its name and
// the FOR VALUES clause that defines its boundaries.
type Partition = ir.Partition

// Generated holds the GENERATED ALWAYS AS (expr) STORED definition of a
// generated column.
type Generated = ir.Generated

// Identity holds the GENERATED ALWAYS AS IDENTITY or GENERATED BY DEFAULT AS
// IDENTITY definition of an identity column.
type Identity = ir.Identity

// ── Pipeline AST sub-types (appear in IR field types) ────────────────────────

// RawExpr is an opaque SQL expression stored as raw text. Appears in Index.Columns
// (expression indexes), Index.Where (partial indexes), and MigrateRemoveBlock.SQL.
type RawExpr = pipeline.RawExpr

// Identifier is a (possibly schema-qualified) SQL identifier. Appears in
// Index.Columns (collation, operator class) and TSMappingDef.Dictionary.
type Identifier = pipeline.Identifier

// IndexColumn is one entry in an Index.Columns slice. Name is set for simple
// column references; Expr is set for expression indexes.
type IndexColumn = pipeline.IndexColumn

// StorageParam is a key=value pair from a WITH (...) storage clause. Appears in
// Index.With.
type StorageParam = pipeline.StorageParam

// TSMappingDef is a MAPPING FOR { } entry in a TEXT SEARCH CONFIGURATION block.
// Appears in TSConfig.Mappings.
type TSMappingDef = pipeline.TSMappingDef

// MigrateRemoveBlock is the MIGRATE REMOVE { } directive on an ENUM type. Holds
// the optional DML SQL to run against columns referencing the type before the
// enum values are removed. Appears in Type.MigrateRemove.
type MigrateRemoveBlock = pipeline.MigrateRemoveBlock

// ── Linter types ──────────────────────────────────────────────────────────────

// LintDiagnostic is a single diagnostic produced by a Linter. IsError true
// means the check is a hard error that aborts the command; false is a warning.
type LintDiagnostic = pipeline.LintDiagnostic

// LinterConfig holds configuration knobs for the built-in linter. All fields
// correspond directly to the [linter] section of dpg.toml.
type LinterConfig = pipeline.LinterConfig

// ── Snapshot ──────────────────────────────────────────────────────────────────

// Snapshot is the JSON-serialisable representation of the last successfully
// applied database state. It is committed to version control alongside source
// files and read by Diff to compute incremental migrations.
type Snapshot = pipeline.Snapshot

// ── Project structure ─────────────────────────────────────────────────────────

// Project is the fully-resolved DPG project discovered from a directory tree.
// It holds all clusters and their databases, each with source file lists.
type Project = project.Project

// Cluster is one PostgreSQL cluster within a project. It holds a connection
// string (URL or Link), cluster-level source files (roles, tablespaces), and
// a slice of Database objects.
type Cluster = project.Cluster

// Database is one PostgreSQL database within a cluster. It holds the database
// name, default schema, and the list of .dpg source files to compile.
type Database = project.Database

// ── Plugin registry ───────────────────────────────────────────────────────────

// Registry holds named pipeline stage implementations. Register custom
// implementations with Register and override any built-in stage.
// Use NewRegistry for a clean registry or Default to extend the built-ins.
type Registry = pipeline.Registry

// NewRegistry returns an empty Registry. Use it to build an isolated registry
// independent of the process-wide Default.
func NewRegistry() *Registry { return pipeline.NewRegistry() }

// Default is the process-wide registry populated by the built-in
// implementations on import. Register custom extensions here to have them
// picked up by Compile, Lint, and Diff.
var Default = pipeline.Default

// Well-known registry keys. Pass these to Default.Register / ResolveLinter etc.
// All keys are stable across minor versions within the v0.x line.
const (
	// KeyTokenizer is the registry key for the source file tokenizer stage.
	KeyTokenizer = pipeline.KeyTokenizer
	// KeyPGSQLParser is the registry key for the PostgreSQL SQL parser stage.
	KeyPGSQLParser = pipeline.KeyPGSQLParser
	// KeyBlockParser is the registry key for the DPG { } block parser stage.
	KeyBlockParser = pipeline.KeyBlockParser
	// KeyIRBuilder is the registry key for the IR builder stage.
	KeyIRBuilder = pipeline.KeyIRBuilder
	// KeyMerger is the registry key for the block merger stage.
	KeyMerger = pipeline.KeyMerger
	// KeyDependencyResolver is the registry key for the topological sort stage.
	KeyDependencyResolver = pipeline.KeyDependencyResolver
	// KeySnapshotStore is the registry key for the snapshot store.
	// Replace with a custom SnapshotStore to use an alternative persistence backend.
	KeySnapshotStore = pipeline.KeySnapshotStore
	// KeyDiffer is the registry key for the differ extension point.
	KeyDiffer = pipeline.KeyDiffer
	// KeyEmitter is the registry key for the emitter extension point.
	KeyEmitter = pipeline.KeyEmitter
	// KeyApplyExecutor is the registry key for the apply executor extension point.
	KeyApplyExecutor = pipeline.KeyApplyExecutor
	// KeyIntrospector is the registry key for the live catalog introspector.
	KeyIntrospector = pipeline.KeyIntrospector
	// KeyLinter is the registry key for the linter extension point.
	// Replace with a custom Linter or use NewChainLinter to augment the built-in.
	KeyLinter = pipeline.KeyLinter
	// KeyPortabilityAnalyzer is the registry key for the portability analyzer.
	// Replace to customize which PostgreSQL-specific constructs are reported.
	KeyPortabilityAnalyzer = pipeline.KeyPortabilityAnalyzer
	// KeySecretResolver is the registry key for the secret resolver extension point.
	KeySecretResolver = pipeline.KeySecretResolver
)

// ── Extension interfaces ──────────────────────────────────────────────────────

// Linter runs lint rules over compiled IR and returns diagnostics.
// Implement and register with Default.Register(KeyLinter, myLinter).
// Use NewChainLinter to augment rather than replace the built-in linter.
type Linter = pipeline.Linter

// Differ compares desired IR state against a snapshot and returns DiffOps.
// Implement and register with Default.Register(KeyDiffer, myDiffer).
type Differ = pipeline.Differ

// Emitter converts ordered DiffOps into a Migration.
// Implement and register with Default.Register(KeyEmitter, myEmitter).
type Emitter = pipeline.Emitter

// SecretResolver resolves secret URI strings (env:VAR, link:...) to plaintext
// connection values at runtime. Implement and register with
// Default.Register(KeySecretResolver, myResolver).
type SecretResolver = pipeline.SecretResolver

// SnapshotStore reads and writes the committed schema snapshot. The built-in
// implementation persists snapshots as JSON files under .dpg/snapshots/.
// Implement and register with Default.Register(KeySnapshotStore, myStore) to
// use an alternative backend (database, object storage, etc.).
type SnapshotStore = pipeline.SnapshotStore

// ApplyExecutor executes a compiled Migration against a live database connection.
// Implement and register with Default.Register(KeyApplyExecutor, myExec) to
// intercept or wrap migration execution (dry-run mode, audit logging, etc.).
// The conn parameter satisfies the Conn interface exported by this package.
type ApplyExecutor = pipeline.ApplyExecutor

// Introspector reads a live PostgreSQL catalog and returns an IRObject slice
// representing the live database state. Used by verify and dump commands.
// Implement and register with Default.Register(KeyIntrospector, myIntrospector).
// The conn parameter satisfies the Querier interface exported by this package.
type Introspector = pipeline.Introspector

// PortabilityAnalyzer walks the compiled IR and reports PostgreSQL-specific
// constructs. Implement and register with
// Default.Register(KeyPortabilityAnalyzer, myAnalyzer).
type PortabilityAnalyzer = pipeline.PortabilityAnalyzer

// PortabilityIssue is a single finding from a PortabilityAnalyzer: the
// PG-specific construct, the source location, and the standard SQL alternative
// if one exists.
type PortabilityIssue = pipeline.PortabilityIssue

// ── Database connection interfaces ────────────────────────────────────────────

// Conn abstracts a live database connection. It is the type of the conn
// parameter in ApplyExecutor.Apply. The built-in implementation wraps pgx.Conn;
// custom ApplyExecutor implementations receive this interface.
type Conn = pipeline.Conn

// Tx abstracts a database transaction started via Conn.Begin.
type Tx = pipeline.Tx

// Querier extends Conn with row-returning query support. It is the type
// passed to Introspector.Introspect. Custom Introspector implementations
// receive this interface.
type Querier = pipeline.Querier

// ── Types needed to implement the extension interfaces ────────────────────────

// DiffOp is a single migration operation produced by a Differ. It exposes
// the SQL text and the safety classification of the operation.
type DiffOp = pipeline.DiffOp

// Safety classifies the risk level of a migration operation.
type Safety = pipeline.Safety

const (
	// Safe indicates no data loss or locking risk. Applied automatically.
	Safe = pipeline.Safe
	// Caution indicates the operation acquires locks or may impact performance.
	Caution = pipeline.Caution
	// Destructive indicates possible data loss. Blocked by default; requires
	// --allow-destructive on dpg apply.
	Destructive = pipeline.Destructive
)

// ExecMode says how a migration operation is carried out, independently of
// its Safety (RFC Section 17.3).
type ExecMode = pipeline.ExecMode

const (
	// ModeTransactional ops run inside the migration's transaction.
	ModeTransactional = pipeline.ModeTransactional
	// ModeNonTransactional ops run after COMMIT (e.g. CREATE INDEX
	// CONCURRENTLY).
	ModeNonTransactional = pipeline.ModeNonTransactional
	// ModeInstructionOnly ops describe a step DPG does not perform; they are
	// shown in plan output and never executed.
	ModeInstructionOnly = pipeline.ModeInstructionOnly
)

// Migration is the complete output of an Emitter: ordered SQL statements
// grouped into transactional and non-transactional sections.
type Migration = pipeline.Migration

// MigrationMeta holds the header metadata written into every migration output:
// generation timestamp, source Git revision, cluster name, and database name.
type MigrationMeta = pipeline.MigrationMeta

// CompilerError is a structured error produced by any pipeline stage. It
// includes a source position (file, line, column) when available.
type CompilerError = pipeline.CompilerError

// Diagnostics is an ordered collection of CompilerErrors returned when
// compilation encounters multiple errors in a single pass.
type Diagnostics = pipeline.Diagnostics

// ── Registry helpers ──────────────────────────────────────────────────────────

// ResolveLinter returns the Linter registered in r, or (nil, false) if none.
func ResolveLinter(r *Registry) (Linter, bool) {
	return pipeline.Resolve[pipeline.Linter](r, pipeline.KeyLinter)
}

// ResolveDiffer returns the Differ registered in r, or (nil, false) if none.
func ResolveDiffer(r *Registry) (Differ, bool) {
	return pipeline.Resolve[pipeline.Differ](r, pipeline.KeyDiffer)
}

// ResolveEmitter returns the Emitter registered in r, or (nil, false) if none.
func ResolveEmitter(r *Registry) (Emitter, bool) {
	return pipeline.Resolve[pipeline.Emitter](r, pipeline.KeyEmitter)
}

// ResolveSecretResolver returns the SecretResolver registered in r, or (nil, false) if none.
func ResolveSecretResolver(r *Registry) (SecretResolver, bool) {
	return pipeline.Resolve[pipeline.SecretResolver](r, pipeline.KeySecretResolver)
}

// ResolveSnapshotStore returns the SnapshotStore registered in r, or (nil, false) if none.
func ResolveSnapshotStore(r *Registry) (SnapshotStore, bool) {
	return pipeline.Resolve[pipeline.SnapshotStore](r, pipeline.KeySnapshotStore)
}

// ResolveApplyExecutor returns the ApplyExecutor registered in r, or (nil, false) if none.
func ResolveApplyExecutor(r *Registry) (ApplyExecutor, bool) {
	return pipeline.Resolve[pipeline.ApplyExecutor](r, pipeline.KeyApplyExecutor)
}

// ResolveIntrospector returns the Introspector registered in r, or (nil, false) if none.
func ResolveIntrospector(r *Registry) (Introspector, bool) {
	return pipeline.Resolve[pipeline.Introspector](r, pipeline.KeyIntrospector)
}

// ResolvePortabilityAnalyzer returns the PortabilityAnalyzer registered in r, or (nil, false) if none.
func ResolvePortabilityAnalyzer(r *Registry) (PortabilityAnalyzer, bool) {
	return pipeline.Resolve[pipeline.PortabilityAnalyzer](r, pipeline.KeyPortabilityAnalyzer)
}

// NewChainLinter returns a Linter that runs each provided linter in order and
// merges their diagnostics. Use it to augment the built-in linter rather than
// replace it:
//
//	builtin, _ := dpg.ResolveLinter(dpg.Default)
//	chained := dpg.NewChainLinter(builtin, &myCustomLinter{})
func NewChainLinter(linters ...Linter) Linter {
	return &chainLinter{linters: linters}
}

type chainLinter struct{ linters []Linter }

func (c *chainLinter) Lint(objects []IRObject, cfg LinterConfig) ([]LintDiagnostic, error) {
	var all []LintDiagnostic
	for _, l := range c.linters {
		diags, err := l.Lint(objects, cfg)
		if err != nil {
			return nil, err
		}
		all = append(all, diags...)
	}
	return all, nil
}

// ── Public API functions ──────────────────────────────────────────────────────

// Compile reads the given .dpg source files rooted at dbDir, runs them
// through the full compilation pipeline (scan → parse → IR → merge →
// topological sort), and returns a sorted slice of fully-resolved IRObjects.
//
// The second return value holds compile-time warning diagnostics (DPG-E031
// duplicate Name Map tool keys); pass it through FilterMergeDiagnostics (with
// the same LinterConfig later given to Lint) before reporting it.
//
// The Default registry is used. All built-in pipeline stages are registered
// automatically via init() when this package is imported.
func Compile(files []string, dbDir string) ([]IRObject, []LintDiagnostic, error) {
	return compiler.Compile(files, dbDir, pipeline.Default)
}

// CompileOptions configures CompileWith (see internal/compiler.Options).
type CompileOptions = compiler.Options

// CompileWith is Compile with explicit options, such as the database's
// default_schema (RFC Section 3.4).
func CompileWith(files []string, dbDir string, opts CompileOptions) ([]IRObject, []LintDiagnostic, error) {
	return compiler.CompileWith(files, dbDir, pipeline.Default, opts)
}

// Lint runs the built-in linter rules over the compiled IR and returns
// diagnostics. It uses the Linter registered in the Default registry.
func Lint(objects []IRObject, cfg LinterConfig) ([]LintDiagnostic, error) {
	l, err := pipeline.MustResolve[pipeline.Linter](pipeline.Default, pipeline.KeyLinter)
	if err != nil {
		return nil, err
	}
	return l.Lint(objects, cfg)
}

// FilterMergeDiagnostics applies [linter.rules] overrides to the warning
// diagnostics Compile's second return value carries. Thin wrapper around
// internal/linter.FilterMergeDiagnostics, re-exported here because external
// consumers of this package cannot import internal/linter directly.
func FilterMergeDiagnostics(mergeDiags []LintDiagnostic, cfg LinterConfig) []LintDiagnostic {
	return linter.FilterMergeDiagnostics(mergeDiags, cfg)
}

// ApplyRuleSeverityOverrides applies [linter.rules] (RFC Section 19.2) per-rule
// severity overrides ("error"/"warning"/"off") to diags, matched by
// d.Rule. Thin wrapper around internal/linter.ApplyRuleSeverityOverrides,
// re-exported for the same reason as FilterMergeDiagnostics above.
func ApplyRuleSeverityOverrides(diags []LintDiagnostic, rules map[string]string) []LintDiagnostic {
	return linter.ApplyRuleSeverityOverrides(diags, rules)
}

// Diff compares desired IR state against snap and returns the ordered set of
// DiffOps needed to migrate from snap to desired. It uses the Differ registered
// in the Default registry.
func Diff(desired []IRObject, snap *Snapshot) ([]DiffOp, error) {
	differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if err != nil {
		return nil, err
	}
	return differ.Diff(desired, snap)
}

// Discover walks up from dir until it finds a dpg.toml project root, then
// builds and returns the fully-resolved Project (all clusters and databases
// with their source file lists).
func Discover(dir string) (*Project, error) {
	return project.Discover(dir)
}
