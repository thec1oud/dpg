package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SourcePos identifies a location in a .dpg source file.
type SourcePos struct {
	File string
	Line int
	Col  int
}

func (p SourcePos) String() string {
	if p.File == "" {
		return "<unknown>"
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// ObjectKind identifies the type of a DPG declaration.
type ObjectKind int

const (
	KindUnknown ObjectKind = iota
	KindSchema
	KindExtension
	KindTable
	KindUnloggedTable
	KindForeignTable
	KindView
	KindMaterializedView
	KindRecursiveView
	KindFunction
	KindProcedure
	KindAggregate
	KindEnum
	KindCompositeType
	KindRangeType
	KindDomainType
	KindBaseType
	KindSequence
	KindRole
	KindTablespace
	KindFDW
	KindServer
	KindUserMapping
	KindPublication
	KindSubscription
	KindEventTrigger
	KindCollation
	KindOperator
	KindOperatorClass
	KindOperatorFamily
	KindCast
	KindStatisticsObject
	KindTSConfig
	KindTSDict
	KindTSParser
	KindTSTemplate
	KindDefaultPrivileges
	KindParameterPrivileges
	KindVirtualType
	KindMacro
	KindUnloggedSequence
)

func (k ObjectKind) String() string {
	switch k {
	case KindSchema:
		return "SCHEMA"
	case KindExtension:
		return "EXTENSION"
	case KindTable:
		return "TABLE"
	case KindUnloggedTable:
		return "UNLOGGED TABLE"
	case KindForeignTable:
		return "FOREIGN TABLE"
	case KindView:
		return "VIEW"
	case KindMaterializedView:
		return "MATERIALIZED VIEW"
	case KindRecursiveView:
		return "RECURSIVE VIEW"
	case KindFunction:
		return "FUNCTION"
	case KindProcedure:
		return "PROCEDURE"
	case KindAggregate:
		return "AGGREGATE"
	case KindEnum:
		return "ENUM"
	case KindCompositeType:
		return "COMPOSITE TYPE"
	case KindRangeType:
		return "RANGE TYPE"
	case KindDomainType:
		return "DOMAIN"
	case KindBaseType:
		return "BASE TYPE"
	case KindSequence:
		return "SEQUENCE"
	case KindUnloggedSequence:
		return "UNLOGGED SEQUENCE"
	case KindRole:
		return "ROLE"
	case KindTablespace:
		return "TABLESPACE"
	case KindFDW:
		return "FOREIGN DATA WRAPPER"
	case KindServer:
		return "SERVER"
	case KindUserMapping:
		return "USER MAPPING"
	case KindPublication:
		return "PUBLICATION"
	case KindSubscription:
		return "SUBSCRIPTION"
	case KindEventTrigger:
		return "EVENT TRIGGER"
	case KindCollation:
		return "COLLATION"
	case KindOperator:
		return "OPERATOR"
	case KindOperatorClass:
		return "OPERATOR CLASS"
	case KindOperatorFamily:
		return "OPERATOR FAMILY"
	case KindCast:
		return "CAST"
	case KindStatisticsObject:
		return "STATISTICS"
	case KindTSConfig:
		return "TEXT SEARCH CONFIGURATION"
	case KindTSDict:
		return "TEXT SEARCH DICTIONARY"
	case KindTSParser:
		return "TEXT SEARCH PARSER"
	case KindTSTemplate:
		return "TEXT SEARCH TEMPLATE"
	case KindDefaultPrivileges:
		return "DEFAULT PRIVILEGES"
	case KindParameterPrivileges:
		return "PARAMETER PRIVILEGES"
	case KindVirtualType:
		return "VIRTUAL TYPE"
	case KindMacro:
		return "MACRO"
	default:
		return "UNKNOWN"
	}
}

// RawObject is the output of the Tokenizer stage: a single DPG declaration
// split into its two parts before any deeper parsing.
type RawObject struct {
	Kind ObjectKind
	// Part1 is the raw PG SQL text of the declaration, with the leading DPG
	// keyword(s) stripped. The PGSQLParser prepends the correct CREATE verb.
	Part1 string
	// Part2 is the raw text of the trailing { } block, or "" if absent.
	Part2 string
	// HasPart2 disambiguates Part2's "" from an explicit, genuinely empty
	// "{ }" block in source — Part2 alone can't tell the two apart, since a
	// "{ }" with no content between the braces also reads as "". Consumers
	// that only care about the block's content can ignore this and keep
	// treating Part2 == "" as "nothing to do here" (an empty block and no
	// block are equivalent for parsing/diffing purposes); it exists for
	// internal/format's dpg fmt, which must round-trip an explicit "{ }" as
	// "{ }" rather than collapsing it to a bare ";".
	HasPart2 bool
	// Schema is the enclosing schema name when this declaration was found inside
	// a SCHEMA { } block. Empty for top-level declarations.
	Schema string
	Pos    SourcePos
	// End is the position just past the declaration's terminating ';' or
	// closing '}'. dpg fmt uses it to tell comments inside a declaration
	// from comments after it on the same line.
	End SourcePos
	// DeclSpan covers the whole declaration, kind keyword to terminator;
	// Part1Span the Part 1 text (as in Part1); Part2Span the text inside
	// the block's braces (as in Part2), unset when there is no block.
	DeclSpan, Part1Span, Part2Span Span
	// Text maps a range of Part1 or Part2 to the original file: Part1 starts
	// at offset Part1At of the scanned text, Part2 at Part2At. Nil when the
	// scanner doesn't record it.
	Text             SourceText
	Part1At, Part2At int
	// SchemaAttrs are a SCHEMA block's own directives (OWNER, COMMENT,
	// GRANTS, ...), each with its text and the offset of the scanned text
	// at which it starts. Part2 holds them joined.
	SchemaAttrs []SchemaAttr
}

// SchemaAttr is one directive of a SCHEMA block (see RawObject.SchemaAttrs).
type SchemaAttr struct {
	Text string
	At   int
}

// SourceText maps a range [start, end) of the text a file was scanned from,
// after macro expansion, to a Span of the original file.
type SourceText interface {
	Span(start, end int) Span
}

// Span is a range of source text: byte offsets [Start, End) into the file
// after scanutil.NormalizeSource (no BOM, LF line endings), with the line and
// column of Start. Macro names the macro when the range was produced by a
// ...name spread; Start and End then cover the spread itself.
type Span struct {
	File       string
	Start, End int
	Line, Col  int
	Macro      string
}

// Valid reports whether the span was recorded.
func (s Span) Valid() bool { return s.Line > 0 }

// Safety classifies the risk of a migration operation (RFC Section 17.2).
type Safety int

const (
	Safe        Safety = iota
	Caution            // locks, rewrites, or access/integrity changes possible
	Destructive        // data loss possible; blocked by default
)

func (s Safety) String() string {
	switch s {
	case Safe:
		return "SAFE"
	case Caution:
		return "CAUTION"
	case Destructive:
		return "DESTRUCTIVE"
	default:
		return "UNKNOWN"
	}
}

// ExecMode says how a migration operation is carried out (RFC Section 17.3),
// independently of its Safety.
type ExecMode int

const (
	// ModeTransactional ops run inside the migration's transaction.
	ModeTransactional ExecMode = iota
	// ModeNonTransactional ops run after COMMIT, each on its own, because
	// PostgreSQL refuses them inside a transaction block.
	ModeNonTransactional
	// ModePreTransactional ops are non-transactional ops that must run
	// before BEGIN, each on its own: an enum ADD VALUE whose new value is
	// used by a statement in the transaction (PostgreSQL rejects using a
	// value added in the same, uncommitted transaction).
	ModePreTransactional
	// ModeInstructionOnly ops are SQL comments describing a step DPG does
	// not perform; they are shown in the plan and never executed.
	ModeInstructionOnly
)

func (m ExecMode) String() string {
	switch m {
	case ModeTransactional:
		return "transactional"
	case ModeNonTransactional:
		return "non-transactional"
	case ModePreTransactional:
		return "non-transactional (before the transaction)"
	case ModeInstructionOnly:
		return "instruction-only"
	default:
		return "unknown"
	}
}

// DiffOp represents a single migration operation produced by the Differ.
type DiffOp interface {
	SQL() string
	Safety() Safety
	Pos() SourcePos
	// Mode says whether the op runs inside the migration's transaction,
	// after it, or not at all (an instruction for the operator).
	Mode() ExecMode
}

// PGParseResult wraps the output of the PGSQLParser.
// Raw holds the pg_query parse result (type *pg_query.ParseResult from
// github.com/pganalyze/pg_query_go/v5, added in Phase 4a). In Phase 1 it is nil.
// For passthrough kinds (KindVirtualType, KindMacro), Raw holds the raw Part1
// string rather than a pg_query parse tree.
type PGParseResult struct {
	Raw any
	// Kind is the ObjectKind that produced this result. Non-zero only for
	// passthrough kinds (KindVirtualType) where Raw is not a pg_query tree.
	Kind ObjectKind
	Pos  SourcePos
	// SchemaContext is the enclosing SCHEMA name when this object was declared
	// inside a SCHEMA { } block. The IR builder uses it as a schema fallback
	// for objects whose Part1 text has no schema qualifier.
	SchemaContext string
	// DefaultSchema is the database's default_schema (RFC Section 3.4): the
	// schema for an object with no qualifier and no enclosing schema scope.
	// Empty means "public".
	DefaultSchema string
	// SourceSQL is the full reconstructed "CREATE FUNCTION/PROCEDURE ..."
	// statement text that produced Raw (pgparser.Reconstruct(kind, part1)).
	// Used by ir.HashFunctionBody's plpgsql canonicalisation, which needs a
	// complete, argument-accurate statement to feed pg_query.ParsePlPgSqlToJSON
	// — not just the bare body. Empty for non-function/procedure kinds.
	SourceSQL string
}

// IRObject is the common interface for all fully-resolved internal representation
// objects. Concrete types are defined in internal/ir (Phase 5).
type IRObject interface {
	QualifiedName() string
	Pos() SourcePos
}

// Snapshot is the committed state written to .dpg/snapshots/ after each apply.
// Objects maps each object's QualifiedName to its JSON-encoded IR snapshot form.
type Snapshot struct {
	DPGVersion     string                     `json:"dpg_version"`
	Cluster        string                     `json:"cluster"`
	Database       string                     `json:"database,omitempty"`
	AppliedAt      string                     `json:"applied_at"`
	SourceRevision string                     `json:"source_revision"`
	Objects        map[string]json.RawMessage `json:"objects,omitempty"`

	// Live marks a snapshot built by introspecting the live catalog
	// (plan --live) rather than loaded from disk. The catalog cannot show
	// a role's password, and can't tell an object removed from source from
	// one DPG never managed, so the differ neither compares passwords nor
	// drops undeclared objects against a live snapshot (RFC Sections 11.1
	// and 18.1).
	Live bool `json:"-"`
}

// SetObject stores obj under key in the snapshot's Objects map.
func (s *Snapshot) SetObject(key string, obj any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if s.Objects == nil {
		s.Objects = make(map[string]json.RawMessage)
	}
	s.Objects[key] = data
	return nil
}

// GetObject decodes the snapshot object for key into dst.
func (s *Snapshot) GetObject(key string, dst any) (bool, error) {
	if s.Objects == nil {
		return false, nil
	}
	raw, ok := s.Objects[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dst)
}

// MigrationMeta holds the header metadata written to every migration output.
type MigrationMeta struct {
	GeneratedAt    time.Time
	SourceRevision string
	Cluster        string
	Database       string
	// SourceRoot is the project root; source positions in rendered output
	// are shown relative to it (RFC Section 17.1). Empty leaves them as is.
	SourceRoot string
}

// Migration is the output of the Emitter stage.
type Migration struct {
	Meta             MigrationMeta
	PreTransactional []DiffOp // executed before BEGIN, one at a time
	Transactional    []DiffOp // wrapped in BEGIN/COMMIT
	NonTransactional []DiffOp // executed after COMMIT
}

// LintDiagnostic is a single finding from the Linter.
type LintDiagnostic struct {
	Pos     SourcePos
	Rule    string
	Message string
	IsError bool // true when --strict promotes this warning to an error
}

// PortabilityIssue is a single finding from the PortabilityAnalyzer.
type PortabilityIssue struct {
	Pos         SourcePos
	Construct   string // the PG-specific construct name
	Alternative string // standard SQL alternative, if any
}

// LinterConfig holds the resolved linter settings passed to Linter.Lint.
// Populated from config.LinterConfig by the CLI layer.
type LinterConfig struct {
	WarnOnDeprecated         bool
	RequireColumnComments    bool
	ForbidHardcodedPasswords bool
	MaxColumnsPerTable       int
	// Rules holds per-rule-ID severity overrides from [linter.rules]:
	// "error", "warning", or "off". A rule ID absent from this map keeps
	// its own default severity. Nil/empty is equivalent to no overrides.
	Rules map[string]string
	// MinPGVersion is the effective min_pg_version floor already resolved
	// for the specific cluster/database being linted (database override,
	// else cluster override, else project root, most specific wins — see
	// project.Cluster.EffectiveMinPGVersion/project.Database.
	// EffectiveMinPGVersion). 0 means no floor is configured anywhere — the
	// min-pg-version rule is a no-op. Unlike every other LinterConfig field,
	// this is NOT sourced from a single dpg.toml's [compiler] section — the
	// caller (cmd/dpg) must resolve it per cluster/database before building
	// this struct, since the same LinterConfig otherwise gets reused across
	// every cluster/database in a project.
	MinPGVersion int
	// Snapshot is the committed snapshot for the database being linted,
	// for rules that depend on which objects already exist. Nil when none
	// is loaded; those rules are then skipped.
	Snapshot *Snapshot
}

// Conn abstracts a database connection so the pipeline package does not import pgx.
// The pgx implementation in internal/executor wraps pgx.Conn to satisfy this interface.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Begin(ctx context.Context) (Tx, error)
	Close(ctx context.Context) error
}

// Tx abstracts a database transaction.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Rows is an iterator over query result rows. Mirrors pgx.Rows.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Querier extends Conn with row-returning query support.
// Used by the Introspector to read the live PG catalog.
type Querier interface {
	Conn
	QueryRows(ctx context.Context, sql string, args ...any) (Rows, error)
}

// CompilerError is a structured error produced by any pipeline stage,
// with a precise source location.
type CompilerError struct {
	Pos     SourcePos
	Message string
	// Code is the RFC Appendix C `DPG-E0xx` code identifying this specific
	// error condition ("" when unlabeled). Threaded through only at call
	// sites that have been explicitly mapped to a documented code — the
	// vast majority of Errorf call sites across this codebase predate this
	// field and stay "" (compileErrsToDiagnostics' DPG-E000 catch-all
	// covers those, same as before this field existed). Use ErrorfCode
	// instead of Errorf to set it.
	Code string
}

func (e *CompilerError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos, e.Message)
}

// Diagnostics is an ordered collection of CompilerErrors. It implements
// the error interface so it can be returned from any stage.
type Diagnostics []*CompilerError

func (d Diagnostics) Error() string {
	msgs := make([]string, len(d))
	for i, e := range d {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

func (d Diagnostics) HasErrors() bool {
	return len(d) > 0
}

// Errorf constructs a CompilerError at the given position.
func Errorf(pos SourcePos, format string, args ...any) *CompilerError {
	return &CompilerError{Pos: pos, Message: fmt.Sprintf(format, args...)}
}

// ErrorfCode is Errorf plus a documented DPG-E0xx code (RFC Appendix C) —
// use at a call site whose error condition has been explicitly mapped to a
// code, so callers like cmd/dpg/validate.go's --format json output can
// surface the real code instead of the generic DPG-E000 fallback.
func ErrorfCode(pos SourcePos, code, format string, args ...any) *CompilerError {
	return &CompilerError{Pos: pos, Message: fmt.Sprintf(format, args...), Code: code}
}
