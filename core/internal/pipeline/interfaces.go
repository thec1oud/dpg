package pipeline

import "context"

// Tokenizer scans .dpg source files and splits each declaration into its
// raw Part 1 (PG SQL) and Part 2 ({ } block) text.
// Default implementation: internal/scanner.
type Tokenizer interface {
	Scan(path string, src []byte) ([]RawObject, error)
}

// GlobalMacroSeeder is an optional interface that a Tokenizer may implement.
// When detected by the compiler it is called once per source file before any
// Scan calls begin, enabling cross-file macro sharing: macros defined in any
// .dpg file within the compilation scope are available to every other file.
type GlobalMacroSeeder interface {
	AddGlobalMacros(src []byte) error
}

// PGSQLParser parses the Part 1 PG SQL text of a declaration by prepending
// the correct CREATE verb and feeding it to the real PostgreSQL parser.
// Default implementation: internal/pgparser (libpg_query via pg_query_go).
type PGSQLParser interface {
	Parse(kind ObjectKind, part1 string, pos SourcePos) (PGParseResult, error)
}

// BlockParser parses the Part 2 { } block text of a declaration into a BlockAST.
// Default implementation: internal/blockparser.
type BlockParser interface {
	Parse(kind ObjectKind, part2 string, pos SourcePos) (BlockAST, error)

	// ParseDefaultPrivileges parses a top-level (non-nested) DEFAULT
	// PRIVILEGES declaration's header ("[FOR ROLE x] [IN SCHEMA y]", the
	// text between "DEFAULT PRIVILEGES" and the opening '{') and body (the
	// '{ }' content, braces excluded). DEFAULT PRIVILEGES never goes through
	// PGSQLParser: real PostgreSQL's ALTER DEFAULT PRIVILEGES statement
	// requires its GRANT/REVOKE action inline, so the header alone is never
	// valid standalone PG SQL — see DefaultPrivilegesBlock.
	ParseDefaultPrivileges(header, body string, pos SourcePos) (DefaultPrivilegesBlock, error)

	// ParseParameterPrivileges parses a top-level PARAMETER PRIVILEGES
	// declaration's header (the text between "PARAMETER PRIVILEGES" and the
	// opening '{' — always empty in valid source, since this kind has no
	// FOR ROLE/IN SCHEMA clause) and body (the '{ }' content, braces
	// excluded). Like DEFAULT PRIVILEGES, this bypasses PGSQLParser
	// entirely — see ParameterPrivilegesBlock.
	ParseParameterPrivileges(header, body string, pos SourcePos) (ParameterPrivilegesBlock, error)
}

// IRBuilder converts a (PGParseResult, BlockAST) pair into a fully-resolved
// IRObject. All names are fully qualified and cross-file references resolved.
// Default implementation: internal/ir.Builder.
type IRBuilder interface {
	Build(pg PGParseResult, block BlockAST) (IRObject, error)

	// BuildDefaultPrivileges converts a parsed DEFAULT PRIVILEGES
	// declaration into one IRObject per distinct object type referenced by
	// its grants/revocations (real PostgreSQL's pg_default_acl catalog has
	// one row per (role, schema, object type) tuple — a single DPG
	// declaration naming TABLES, FUNCTIONS, and SEQUENCES together, per the
	// RFC's own example, splits into three independently-diffable objects).
	BuildDefaultPrivileges(block DefaultPrivilegesBlock) ([]IRObject, error)

	// BuildParameterPrivileges converts a parsed PARAMETER PRIVILEGES
	// declaration into its single IRObject. Unlike DEFAULT PRIVILEGES there
	// is no per-object-type split — PostgreSQL's pg_parameter_acl catalog
	// has one row per parameter, not per (role, schema, type) tuple, and a
	// DPG project has at most one PARAMETER PRIVILEGES block.
	BuildParameterPrivileges(block ParameterPrivilegesBlock) ([]IRObject, error)

	// ResolveLikeClauses resolves every Table's pending `LIKE source_table`
	// column-list entries (Section 7.1) into concrete columns/constraints,
	// once every object in the compile unit has been built and merged.
	// Build converts one (PGParseResult, BlockAST) pair at a time and has
	// no visibility into any other declared table, so a LIKE clause can't
	// be resolved there — this runs as its own stage, after Merger.Merge
	// and before DependencyResolver.Sort, mutating objects in place.
	ResolveLikeClauses(objects []IRObject) error
}

// Merger merges same-object IRObject declarations from multiple .dpg files
// according to RFC Section 3.7: set-valued properties are unioned, and a
// scalar property declared with two different values is a DPG-E039 error.
// Default implementation: internal/merger.
type Merger interface {
	Merge(objects []IRObject) ([]IRObject, error)
}

// DependencyResolver performs topological sort and circular-FK resolution
// on the merged object graph.
// Default implementation: internal/graph.
type DependencyResolver interface {
	Sort(objects []IRObject) ([]IRObject, error)
}

// SnapshotStore reads and writes the committed schema snapshot.
// Default implementation: internal/snapshot.FileStore (JSON file on disk).
// Alternatives: GitStore (git object store), DBStore (dedicated PG table).
type SnapshotStore interface {
	Load(cluster, database string) (*Snapshot, error)
	Save(cluster, database string, s *Snapshot) error
}

// Differ compares desired IR state against the snapshot and produces an
// ordered list of DiffOps.
// Default implementation: internal/diff.StandardDiffer.
// Alternative: internal/diff.NullDiffer (always empty, for bootstrap).
type Differ interface {
	Diff(desired []IRObject, snap *Snapshot) ([]DiffOp, error)
}

// Emitter converts ordered DiffOps into a Migration.
// Default implementation: internal/emit.SQLEmitter (RFC Section 20.2 SQL format).
// Alternatives: JSONEmitter (machine-readable), DryRunEmitter (human-readable plan).
type Emitter interface {
	Emit(ops []DiffOp, meta MigrationMeta) (Migration, error)
}

// ApplyExecutor executes a Migration against a live database connection.
// Default implementation: internal/executor.PgxExecutor.
type ApplyExecutor interface {
	Apply(ctx context.Context, m Migration, conn Conn) error
}

// Introspector reads a live PG catalog and returns an IRObject slice
// equivalent to what the compiler would produce from .dpg source files.
// Default implementation: internal/introspect.CatalogIntrospector.
// Alternative: internal/introspect.SnapshotIntrospector.
type Introspector interface {
	Introspect(ctx context.Context, conn Querier) ([]IRObject, error)
}

// Linter runs lint rules over the merged IR and returns diagnostics.
// Default implementation: internal/linter.BuiltinLinter.
// Compose multiple linters with a ChainLinter.
type Linter interface {
	Lint(objects []IRObject, cfg LinterConfig) ([]LintDiagnostic, error)
}

// PortabilityAnalyzer walks the IR and reports PG-specific constructs.
// Default implementation: internal/portability.Analyzer.
type PortabilityAnalyzer interface {
	Analyze(objects []IRObject) ([]PortabilityIssue, error)
}

// SecretResolver resolves secret URIs to plaintext values at connection time.
//   - "env:VAR_NAME" → os.Getenv("VAR_NAME")
//   - "vault:<mount>/<path>#<field>", "aws-sm:...", "gcp-sm:...", "azure-kv:..."
//
// Default implementation: internal/secrets.ChainResolver, with all of the
// above registered. ChainResolver dispatches a URI to whichever resolver is
// registered for its scheme (the substring before the first ':') — not a
// fallback chain that tries each resolver in turn; an unrecognized scheme
// errors immediately rather than being silently treated as a literal value.
type SecretResolver interface {
	Resolve(uri string) (string, error)
}

// SecretBearingOp is optionally implemented by a DiffOp whose SQL() text
// contains an unresolved secret reference (e.g. a SUBSCRIPTION's CONNECTION
// clause). SQL() always returns the placeholder/reference form — used for
// plan output, migration-file archival, snapshot hashing, and error
// messages, so a resolved secret is never persisted or logged. ExecSQL
// resolves the reference via resolver and returns the actual statement to
// execute; callers MUST use the result only for the immediate execution
// call and MUST NOT log, print, wrap into an error, or otherwise persist it.
type SecretBearingOp interface {
	DiffOp
	ExecSQL(resolver SecretResolver) (string, error)
}
