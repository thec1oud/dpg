# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0-alpha.1] — 2026-10-12

### Added

- Record migrations without a database; CI check; log repair and rebase
- Object fingerprints; migrations refuse to change drifted objects
- Apply preflight and migration guards; verify at the database's position
- Database ledger; apply settles the log's migrations a database hasn't run
- Migration log with offline replay, baselines and adoption migrations
- Ops name their object; warn on a snapshot format mismatch
- Adopt insertions, grants, macro parts and dependencies; interactive mode
- Dpg resolve and drift acknowledgements
- Record source spans and index where each property is declared
- Compare declared properties only and classify drift
- [ignore] config section and undeclared-role handling
- Declare an extended statistics object's target
- Support constraint triggers
- Reject block directives an object kind doesn't accept (DPG-E024)
- Check every declared version-gated construct
- Warn on CONCURRENTLY for a new table's indexes
- Resolve virtual type references at compile time (DPG-E044)
- Model a range type's multirange name and rename it in place
- Enforce unified ;/{ terminator rules (DPG-E037/E038)
- Implement Section 9.2 SET param = value / FROM CURRENT
- Warn when an unreconstructable object is omitted
- Implement DPG-E019 stale-column-reference detection
- Implement DPG-E005 conflicting-set-member detection
- Implement E007/E008/E009 misuse detection
- Label 13 mislabeled DPG-Ennn codes, implement 3 missing ones
- Implement RENAMED FROM support

### Fixed

- Table and matview SET TABLESPACE are CAUTION
- Depend on introspected column types written schema-qualified
- Report block directive errors at the directive
- Five plan --live false positives
- End the migration header's Cluster line
- Hint that DPG does not create databases
- Keep every comment, Part 1 clause and macro spread
- Reject COMMENT on a user mapping
- Rename sequences and text search configurations in place
- Compare a `*` view with the live catalog without its select list
- Reset an extended statistics target with -1, not DEFAULT
- Classify indexes created with a new table or view as SAFE
- Classify an ADD COLUMN that rewrites the table as CAUTION
- Carry plain view options, check option and column list
- Keep the column list of ON DELETE SET NULL/SET DEFAULT
- Make a table-level PRIMARY KEY's columns NOT NULL
- Keep source positions accurate and root-relative
- Remove stray semicolon-before-block and own-line brace bugs
- Tag DPG-E021/E022 correctly; emit sequence RESTART reminder
- Parse bare ENUM shorthand; fix doubled AS ENUM on dump
- Normalize BOM and CRLF/CR line endings on source read
- Correct MAINTAIN privilege version-gate's RFC section citation

### Changed

- Rewrite the roles page in the RFC's role syntax
- One PG/DPG layout for every object section
- Fix statistics target, constraint trigger and sequence rename pages
- Specify virtual type naming, view options and version gating
- Read-through revision of the DPG specification
- Split MANUAL into safety class and execution mode
- Typed dpg.toml files and per-database connections
- Enforce schema scope and honour default_schema (DPG-E040)
- Make conflicting scalar declarations an error (DPG-E039)
- Align DPG block keywords with PostgreSQL spelling
- Sync docs-site examples with unified terminator rules
- Unify Section 4.5 statement-terminator rules
- Phase 3 section-by-section audit against core/ and docs site
- Sync docs-site pages with actual diff/introspect behavior
- Retract DPG-E016, its premise was factually wrong
- Document that partition RLS does not protect direct access
- Fix semantic self-consistency gaps from architectural audit
- Fix internal self-consistency gaps found in pre-lock audit
- Document npm trusted-publisher toggle for grammar-only releases

## [0.5.5-alpha.6] — 2026-08-26

- No changes.

## [0.5.5-alpha.5] — 2026-08-26

### Fixed

- Pass explicit --tag to npm publish for grammar package
- Switch grammar npm/crates.io publish to OIDC trusted publishing
- Drop 'intellij' from plugin ID per Marketplace validation
- Pass --cleanDestinationDir to hugo builds

### Changed

- Namespace plugin ID under dev.thec1oud reverse-DNS

## [grammar-v0.5.2-alpha.15] — 2026-08-26

### Fixed

- Pass explicit --tag to npm publish for grammar package

## [grammar-v0.5.2-alpha.14] — 2026-08-26

### Fixed

- Switch grammar npm/crates.io publish to OIDC trusted publishing

## [grammar-v0.5.2-alpha.13] — 2026-08-26

### Fixed

- Drop 'intellij' from plugin ID per Marketplace validation
- Pass --cleanDestinationDir to hugo builds
- Add missing secret-uri shortcode template

### Changed

- Namespace plugin ID under dev.thec1oud reverse-DNS

## [0.5.5-alpha.4] — 2026-08-26

### Fixed

- Add missing secret-uri shortcode template

## [idea-v0.5.2-alpha.15] — 2026-08-26

- No changes.

## [vscode-v0.5.2-alpha.12] — 2026-08-26

- No changes.

## [grammar-v0.5.2-alpha.12] — 2026-08-26

- No changes.

## [lsp-v0.5.5-alpha.3] — 2026-08-26

- No changes.

## [0.5.5-alpha.3] — 2026-08-26

- No changes.

## [nvim-v0.5.2-alpha.11] — 2026-08-26

### Added

- Implement DROP CONSTRAINT ... ONLY on partitioned tables (PG18+)
- Thread DPG-E0xx codes through documented sites, implement DPG-E001
- Materialized-view TABLESPACE/WITH storage-params support
- Structured optimizer-hint fields, safe ALTER OPERATOR SET diffing
- Parse and diff identity-column sequence options (Section 7.4)
- Implement foreign table as partition (RFC Section 7.13)
- Unify membership lists and implement WITH ADMIN/INHERIT/SET
- Implement GRANTED BY role and Role SET/RESET IN DATABASE
- Implement LEAKPROOF, TRANSFORM FOR TYPE, C obj_file/link_symbol, and BEGIN ATOMIC bodies
- Implement TSConfig owner and TSDict option-change diffing
- Implement ENUM positional ADD VALUE and RENAME VALUE
- Implement DISABLED/ENABLE REPLICA/ENABLE ALWAYS
- Diff and emit WITH (...) storage-params SET/RESET
- Implement ATTACHED FROM / DETACHED FROM partition directives
- Implement UNLOGGED prefix
- Implement ON ONLY prefix and opclass parameters
- Implement typed tables (OF type_name) and access method
- Implement composite attribute and domain COLLATE
- Implement WITHOUT OVERLAPS/PERIOD temporal keys (PG18)
- Implement ENFORCED/NOT ENFORCED on CHECK/FOREIGN KEY (PG18)
- Implement table-level named NOT NULL constraint (PG18)
- Implement generated-column VIRTUAL and its diffing
- Implement min_pg_version project-gating (Section 23)
- Implement PARAMETER PRIVILEGES (Section 11.6, PG15+)
- Implement OWNER TO
- Implement OWNER TO
- Implement Owner and RENAMED FROM
- Implement RENAMED FROM
- Implement RESTART and REFRESH VERSION
- Implement trigger enable-state (DISABLED / ENABLE REPLICA / ENABLE ALWAYS)
- Implement [NO] DEPENDS ON EXTENSION (RFC audit items #71, #75)
- Implement OWNER TO (RFC audit item #34, half)
- Implement OWNER TO (RFC audit item #76, half)
- Accept STATISTICS DEFAULT; fix missing SET STATISTICS on new tables
- Structured diffing for BASE type's 7 in-place-alterable properties
- Implement REPLICA IDENTITY and CLUSTER ON block directives
- Implement column alignment for dpg fmt (RFC §18.7)
- Add SECURITY LABEL support (RFC §14.11)
- Give OperatorClass structured member capture
- Add Aggregate Revocations and Extension Comment
- Support COMMENT ON Constraint/Index/Policy/Trigger
- Wire schema GRANT/REVOKE through all layers
- Require confirmation before dpg dump overwrites files
- Implement scalar-merge-conflict lint rule (full scope)
- Implement deprecated-reference lint rule
- Model SERIAL/BIGSERIAL/SMALLSERIAL as a first-class IR concept
- Implement 2 more RFC-documented rules and per-rule severity overrides
- Implement operator family loose members (RFC §14.4)
- Implement remaining unimplemented RFC object kinds and fix real bugs found exhaustively live-testing every DPG feature
- Reconstruct Subscriptions from the live catalog
- Support secret references in USER MAPPING OPTIONS
- Implement full Role attribute system with PASSWORD secret refs
- Support secret references in SUBSCRIPTION CONNECTION
- Implement Vault, AWS/GCP/Azure secret manager backends
- Support CONSTRAINTS plural block (RFC §4.8)
- Support WITH/NULLS NOT DISTINCT and Mode-B index syntax
- Introspect deferred-tier opaque objects (operators, TS, opclass/family)
- Introspect reliable-tier opaque objects; fix operator-class DROP
- Add support for macro spread references, resolution logic, and related tests across LSP and IDEA plugin
- Enforce blank line between schema attributes and first nested object in formatter and add related test
- Add support for DPG code formatting in IDEA plugin, including indentation, blank lines, and spacing adjustments
- Add support for macro declarations, rendering, and schema-level nesting in formatter
- Add support for macro references, completions, and spread expressions in IDEA plugin

### Fixed

- Close 5 lower-priority fresh-audit-2026-08-26 gaps
- Exclude default-privileges self-grant from ACL introspection
- Compact ROLE-direction WITH INHERIT when it matches the grantee's default
- Suppress extension comments matching the control-file default
- Capture GRANTED BY grantor from live ACL entries
- Exclude owner self-grants from ACL introspection
- Diff Strict/SecurityDef on existing functions
- Tablespace WITH params, policy rename, RLSForced dump, operator class SET SCHEMA
- Implement FROM existing_collation copy-from
- Diff and emit the LOGGED/UNLOGGED toggle
- Capture and diff every agg-option, not just six
- Diff PG16+ ICU RULES
- Use ALTER POLICY for TO/USING/WITH CHECK-only changes
- Use ALTER EXTENSION SET SCHEMA instead of DROP+CREATE
- Wire domain constraint NOT VALID and VALIDATE CONSTRAINT
- Emit ALTER ... SET SCHEMA for the remaining cross-schema rename kinds
- Implement CREATE TABLE (LIKE source_table [...]) instead of discarding it
- Emit ALTER ... SET SCHEMA for cross-schema RENAMED FROM moves
- Detect and rename Subscription, FDW, OperatorClass, OperatorFamily, StatisticsObject
- Detect and rename TSDict, TSParser, TSTemplate, Role, EventTrigger
- Detect and rename Type (all variants) and Collation
- Detect and rename Constraint and Index instead of drop+recreate
- Detect and rename renamed partitions instead of drop+recreate
- Support cross-schema RENAMED FROM and fix Function rename
- Emit ALTER TYPE ADD VALUE as transactional, not MANUAL
- Prevent spurious cycles from plpgsql table references
- Fix Phase 4 state-audit bugs #28/#30/#2
- Fix Phase 3 state-audit bugs #15+16/#14/#1/#17
- Fix Phase 2 state-audit bugs #3-6/#7/#8-11/#20/#24/#25/#26/#27
- Fix Phase 1 state-audit bugs #23/#21/#19/#12+13/#29
- Error on a case-mismatched VIRTUAL TYPE reference
- Make plan --live detect real OperatorClass AS-list changes
- Make the public schema visible to introspection, guard it from DROP
- Stop RANGE types from crashing dpg via an infinite Sort recursion
- Synthesize PUBLIC EXECUTE revocation for explicitly-revoked functions/procedures/aggregates
- Compare Aggregate options structurally instead of raw BodyHash
- Enforce DPG-E004 reserved-name conflict, deduplicate error wording
- Validate cluster/database name uniqueness and non-emptiness
- Connect per-database commands to the selected database, not whatever the cluster URL happens to point at
- Diff and introspect trigger WHEN conditions
- Apply NOT VALID on CREATE TABLE, ignore paren-only expression drift
- Stop double-rendering NOT VALID on dump
- Make BASE types actually round-trip
- Wire Type.Owner and fix dump comment loss
- Stop treating inherited columns/constraints as drift
- Render and correctly apply Table.Inherits
- Classify same-base-type typmod widening as CAUTION
- Classify implicit-cast column type changes as CAUTION
- Sandbox dpg dump -o's cluster-scoped output and snapshot
- Canonicalize embedded plpgsql expression fragments, closing the last hashing gap
- Stop keyword-casing from mangling identifiers, reconcile rule-ID naming
- Stop dpg fmt from doubling ';' and collapsing empty '{ }' blocks
- Canonicalize plpgsql body hashing, closing the last "Inherent" item
- Predict PostgreSQL's real fallback name for unpredictable EXCLUDE elements
- Close G-live gap for StatisticsObject, completing Workstream 2
- Close G-live gap for Collation
- Close G-live gap for Publication, closing Tier 2
- Close G-live gap for FDW/ForeignServer/UserMapping
- Close G-live gap for Tablespace/Cast/EventTrigger
- Canonicalize LANGUAGE SQL bodies before hashing to stop spurious CREATE OR REPLACE
- Redact password-like OPTIONS on USER MAPPING introspection
- Quote synthetic CREATE SCHEMA for directory-inferred schemas
- Add 3 missing dependency-graph edges (publication→table, sql-function→function, function-type→type)
- Implement RFC §5.4 DOMAIN structured diffing, fix introspectCasts internal-dependency bug
- Fix 9 real bugs found live-testing every DPG object kind against a real demo project
- Implement ChainResolver, fix silent scheme-passthrough bug
- Adopt pg_dump's model for operator families
- Infer implied return type for functions with omitted RETURNS clause
- Capture OUT/INOUT/VARIADIC argument modes on introspection
- Reconstruct RETURNS TABLE(...) functions correctly
- Add SETOF representation for function return types
- Capture and diff FUNCTION PARALLEL/COST/ROWS attributes
- Reconcile unnamed EXCLUDE constraint names against PostgreSQL's real auto-naming algorithm
- Parse EXCLUDE constraint bodies and fix two SQL-text rename bugs
- Capture operator operand types so DROP OPERATOR is valid and overloads don't collide
- Recognize PostgreSQL's auto-generated constraint names for unnamed CHECK constraints
- Render FUNCTION/PROCEDURE bodies instead of dropping them from dump output
- Recognize PostgreSQL's auto-generated constraint names for unnamed PRIMARY KEY/UNIQUE/FOREIGN KEY
- Render table/schema/sequence OWNER and column attributes; fix inert sequence ownership
- Fix sequence CYCLE parsing and add PROCEDURE test coverage
- Support reserved-word/quoted SCHEMA names
- Normalize trigger function references to avoid spurious drift
- Accept POLICY/TRIGGER/PARTITION singular keyword
- Emit REVOKE for explicit table/column/view REVOCATION
- Accept GRANT/REVOCATION singular keyword outside plural blocks
- Compare index definitions instead of matching by name only
- Emit structured DROP+CREATE for opaque object updates
- Make dumped projects recompile and re-apply faithfully

### Changed

- Document dpg fmt's undocumented --stdin flag
- Fix 6 stale doc-only items from fresh-audit-2026-08-26
- Fix stale SnapRole/SnapSequence examples and portability --format claim
- Promote min_pg_version out of Deferred Features, refresh Appendix F
- Add RENAMED FROM grammar for Policy, Trigger, Partition, TS Config
- Spell out RFC section reference instead of section-sign
- Replace section-sign character with spelled-out Section/Appendix
- Close final mop-up items and fix appendix physical section order
- Add newer-PostgreSQL-version grammar (PG15-18); fix ALTER TYPE ADD VALUE transaction-cutoff error
- Add namespace/storage/FDW/replication grammar; close remaining Phase 5 destructive-to-safe swaps
- Add full-text search grammar for silent PG feature gaps
- Add access-control grammar for silent PG feature gaps
- Add type/domain/function/aggregate grammar for silent PG feature gaps
- Add table/view/index/sequence grammar for silent PG feature gaps
- Fix CLI reference drift, undefined grammar, and Domain syntax; add PG version/portability classification
- Drop redundant { CONNECTION } block form, add COMMENT
- Update coverage matrix and clarify introspection behaviors
- Simplify schema node comment collection logic in parser
- Migrated idea plugin from LSP-based implementation to plugin-native parsing, highlighting, and tooling

## [idea-v0.5.2-alpha.14] — 2026-08-26

### Added

- Implement DROP CONSTRAINT ... ONLY on partitioned tables (PG18+)
- Thread DPG-E0xx codes through documented sites, implement DPG-E001
- Materialized-view TABLESPACE/WITH storage-params support
- Structured optimizer-hint fields, safe ALTER OPERATOR SET diffing
- Parse and diff identity-column sequence options (Section 7.4)
- Implement foreign table as partition (RFC Section 7.13)
- Unify membership lists and implement WITH ADMIN/INHERIT/SET
- Implement GRANTED BY role and Role SET/RESET IN DATABASE
- Implement LEAKPROOF, TRANSFORM FOR TYPE, C obj_file/link_symbol, and BEGIN ATOMIC bodies
- Implement TSConfig owner and TSDict option-change diffing
- Implement ENUM positional ADD VALUE and RENAME VALUE
- Implement DISABLED/ENABLE REPLICA/ENABLE ALWAYS
- Diff and emit WITH (...) storage-params SET/RESET
- Implement ATTACHED FROM / DETACHED FROM partition directives
- Implement UNLOGGED prefix
- Implement ON ONLY prefix and opclass parameters
- Implement typed tables (OF type_name) and access method
- Implement composite attribute and domain COLLATE
- Implement WITHOUT OVERLAPS/PERIOD temporal keys (PG18)
- Implement ENFORCED/NOT ENFORCED on CHECK/FOREIGN KEY (PG18)
- Implement table-level named NOT NULL constraint (PG18)
- Implement generated-column VIRTUAL and its diffing
- Implement min_pg_version project-gating (Section 23)
- Implement PARAMETER PRIVILEGES (Section 11.6, PG15+)
- Implement OWNER TO
- Implement OWNER TO
- Implement Owner and RENAMED FROM
- Implement RENAMED FROM
- Implement RESTART and REFRESH VERSION
- Implement trigger enable-state (DISABLED / ENABLE REPLICA / ENABLE ALWAYS)
- Implement [NO] DEPENDS ON EXTENSION (RFC audit items #71, #75)
- Implement OWNER TO (RFC audit item #34, half)
- Implement OWNER TO (RFC audit item #76, half)
- Accept STATISTICS DEFAULT; fix missing SET STATISTICS on new tables
- Structured diffing for BASE type's 7 in-place-alterable properties
- Implement REPLICA IDENTITY and CLUSTER ON block directives
- Implement column alignment for dpg fmt (RFC §18.7)
- Add SECURITY LABEL support (RFC §14.11)
- Give OperatorClass structured member capture
- Add Aggregate Revocations and Extension Comment
- Support COMMENT ON Constraint/Index/Policy/Trigger
- Wire schema GRANT/REVOKE through all layers
- Require confirmation before dpg dump overwrites files
- Implement scalar-merge-conflict lint rule (full scope)
- Implement deprecated-reference lint rule
- Model SERIAL/BIGSERIAL/SMALLSERIAL as a first-class IR concept
- Implement 2 more RFC-documented rules and per-rule severity overrides
- Implement operator family loose members (RFC §14.4)
- Implement remaining unimplemented RFC object kinds and fix real bugs found exhaustively live-testing every DPG feature
- Reconstruct Subscriptions from the live catalog
- Support secret references in USER MAPPING OPTIONS
- Implement full Role attribute system with PASSWORD secret refs
- Support secret references in SUBSCRIPTION CONNECTION
- Implement Vault, AWS/GCP/Azure secret manager backends
- Support CONSTRAINTS plural block (RFC §4.8)
- Support WITH/NULLS NOT DISTINCT and Mode-B index syntax
- Introspect deferred-tier opaque objects (operators, TS, opclass/family)
- Introspect reliable-tier opaque objects; fix operator-class DROP

### Fixed

- Close 5 lower-priority fresh-audit-2026-08-26 gaps
- Exclude default-privileges self-grant from ACL introspection
- Compact ROLE-direction WITH INHERIT when it matches the grantee's default
- Suppress extension comments matching the control-file default
- Capture GRANTED BY grantor from live ACL entries
- Exclude owner self-grants from ACL introspection
- Diff Strict/SecurityDef on existing functions
- Tablespace WITH params, policy rename, RLSForced dump, operator class SET SCHEMA
- Implement FROM existing_collation copy-from
- Diff and emit the LOGGED/UNLOGGED toggle
- Capture and diff every agg-option, not just six
- Diff PG16+ ICU RULES
- Use ALTER POLICY for TO/USING/WITH CHECK-only changes
- Use ALTER EXTENSION SET SCHEMA instead of DROP+CREATE
- Wire domain constraint NOT VALID and VALIDATE CONSTRAINT
- Emit ALTER ... SET SCHEMA for the remaining cross-schema rename kinds
- Implement CREATE TABLE (LIKE source_table [...]) instead of discarding it
- Emit ALTER ... SET SCHEMA for cross-schema RENAMED FROM moves
- Detect and rename Subscription, FDW, OperatorClass, OperatorFamily, StatisticsObject
- Detect and rename TSDict, TSParser, TSTemplate, Role, EventTrigger
- Detect and rename Type (all variants) and Collation
- Detect and rename Constraint and Index instead of drop+recreate
- Detect and rename renamed partitions instead of drop+recreate
- Support cross-schema RENAMED FROM and fix Function rename
- Emit ALTER TYPE ADD VALUE as transactional, not MANUAL
- Prevent spurious cycles from plpgsql table references
- Fix Phase 4 state-audit bugs #28/#30/#2
- Fix Phase 3 state-audit bugs #15+16/#14/#1/#17
- Fix Phase 2 state-audit bugs #3-6/#7/#8-11/#20/#24/#25/#26/#27
- Fix Phase 1 state-audit bugs #23/#21/#19/#12+13/#29
- Error on a case-mismatched VIRTUAL TYPE reference
- Make plan --live detect real OperatorClass AS-list changes
- Make the public schema visible to introspection, guard it from DROP
- Stop RANGE types from crashing dpg via an infinite Sort recursion
- Synthesize PUBLIC EXECUTE revocation for explicitly-revoked functions/procedures/aggregates
- Compare Aggregate options structurally instead of raw BodyHash
- Enforce DPG-E004 reserved-name conflict, deduplicate error wording
- Validate cluster/database name uniqueness and non-emptiness
- Connect per-database commands to the selected database, not whatever the cluster URL happens to point at
- Diff and introspect trigger WHEN conditions
- Apply NOT VALID on CREATE TABLE, ignore paren-only expression drift
- Stop double-rendering NOT VALID on dump
- Make BASE types actually round-trip
- Wire Type.Owner and fix dump comment loss
- Stop treating inherited columns/constraints as drift
- Render and correctly apply Table.Inherits
- Classify same-base-type typmod widening as CAUTION
- Classify implicit-cast column type changes as CAUTION
- Sandbox dpg dump -o's cluster-scoped output and snapshot
- Canonicalize embedded plpgsql expression fragments, closing the last hashing gap
- Stop keyword-casing from mangling identifiers, reconcile rule-ID naming
- Stop dpg fmt from doubling ';' and collapsing empty '{ }' blocks
- Canonicalize plpgsql body hashing, closing the last "Inherent" item
- Predict PostgreSQL's real fallback name for unpredictable EXCLUDE elements
- Close G-live gap for StatisticsObject, completing Workstream 2
- Close G-live gap for Collation
- Close G-live gap for Publication, closing Tier 2
- Close G-live gap for FDW/ForeignServer/UserMapping
- Close G-live gap for Tablespace/Cast/EventTrigger
- Canonicalize LANGUAGE SQL bodies before hashing to stop spurious CREATE OR REPLACE
- Redact password-like OPTIONS on USER MAPPING introspection
- Quote synthetic CREATE SCHEMA for directory-inferred schemas
- Add 3 missing dependency-graph edges (publication→table, sql-function→function, function-type→type)
- Implement RFC §5.4 DOMAIN structured diffing, fix introspectCasts internal-dependency bug
- Fix 9 real bugs found live-testing every DPG object kind against a real demo project
- Implement ChainResolver, fix silent scheme-passthrough bug
- Adopt pg_dump's model for operator families
- Infer implied return type for functions with omitted RETURNS clause
- Capture OUT/INOUT/VARIADIC argument modes on introspection
- Reconstruct RETURNS TABLE(...) functions correctly
- Add SETOF representation for function return types
- Capture and diff FUNCTION PARALLEL/COST/ROWS attributes
- Reconcile unnamed EXCLUDE constraint names against PostgreSQL's real auto-naming algorithm
- Parse EXCLUDE constraint bodies and fix two SQL-text rename bugs
- Capture operator operand types so DROP OPERATOR is valid and overloads don't collide
- Recognize PostgreSQL's auto-generated constraint names for unnamed CHECK constraints
- Render FUNCTION/PROCEDURE bodies instead of dropping them from dump output
- Recognize PostgreSQL's auto-generated constraint names for unnamed PRIMARY KEY/UNIQUE/FOREIGN KEY
- Render table/schema/sequence OWNER and column attributes; fix inert sequence ownership
- Fix sequence CYCLE parsing and add PROCEDURE test coverage
- Support reserved-word/quoted SCHEMA names
- Normalize trigger function references to avoid spurious drift
- Accept POLICY/TRIGGER/PARTITION singular keyword
- Emit REVOKE for explicit table/column/view REVOCATION
- Accept GRANT/REVOCATION singular keyword outside plural blocks
- Compare index definitions instead of matching by name only
- Emit structured DROP+CREATE for opaque object updates
- Make dumped projects recompile and re-apply faithfully

### Changed

- Document dpg fmt's undocumented --stdin flag
- Fix 6 stale doc-only items from fresh-audit-2026-08-26
- Fix stale SnapRole/SnapSequence examples and portability --format claim
- Promote min_pg_version out of Deferred Features, refresh Appendix F
- Add RENAMED FROM grammar for Policy, Trigger, Partition, TS Config
- Spell out RFC section reference instead of section-sign
- Replace section-sign character with spelled-out Section/Appendix
- Close final mop-up items and fix appendix physical section order
- Add newer-PostgreSQL-version grammar (PG15-18); fix ALTER TYPE ADD VALUE transaction-cutoff error
- Add namespace/storage/FDW/replication grammar; close remaining Phase 5 destructive-to-safe swaps
- Add full-text search grammar for silent PG feature gaps
- Add access-control grammar for silent PG feature gaps
- Add type/domain/function/aggregate grammar for silent PG feature gaps
- Add table/view/index/sequence grammar for silent PG feature gaps
- Fix CLI reference drift, undefined grammar, and Domain syntax; add PG version/portability classification
- Drop redundant { CONNECTION } block form, add COMMENT
- Update coverage matrix and clarify introspection behaviors

## [vscode-v0.5.2-alpha.11] — 2026-08-26

### Added

- Implement DROP CONSTRAINT ... ONLY on partitioned tables (PG18+)
- Thread DPG-E0xx codes through documented sites, implement DPG-E001
- Materialized-view TABLESPACE/WITH storage-params support
- Structured optimizer-hint fields, safe ALTER OPERATOR SET diffing
- Parse and diff identity-column sequence options (Section 7.4)
- Implement foreign table as partition (RFC Section 7.13)
- Unify membership lists and implement WITH ADMIN/INHERIT/SET
- Implement GRANTED BY role and Role SET/RESET IN DATABASE
- Implement LEAKPROOF, TRANSFORM FOR TYPE, C obj_file/link_symbol, and BEGIN ATOMIC bodies
- Implement TSConfig owner and TSDict option-change diffing
- Implement ENUM positional ADD VALUE and RENAME VALUE
- Implement DISABLED/ENABLE REPLICA/ENABLE ALWAYS
- Diff and emit WITH (...) storage-params SET/RESET
- Implement ATTACHED FROM / DETACHED FROM partition directives
- Implement UNLOGGED prefix
- Implement ON ONLY prefix and opclass parameters
- Implement typed tables (OF type_name) and access method
- Implement composite attribute and domain COLLATE
- Implement WITHOUT OVERLAPS/PERIOD temporal keys (PG18)
- Implement ENFORCED/NOT ENFORCED on CHECK/FOREIGN KEY (PG18)
- Implement table-level named NOT NULL constraint (PG18)
- Implement generated-column VIRTUAL and its diffing
- Implement min_pg_version project-gating (Section 23)
- Implement PARAMETER PRIVILEGES (Section 11.6, PG15+)
- Implement OWNER TO
- Implement OWNER TO
- Implement Owner and RENAMED FROM
- Implement RENAMED FROM
- Implement RESTART and REFRESH VERSION
- Implement trigger enable-state (DISABLED / ENABLE REPLICA / ENABLE ALWAYS)
- Implement [NO] DEPENDS ON EXTENSION (RFC audit items #71, #75)
- Implement OWNER TO (RFC audit item #34, half)
- Implement OWNER TO (RFC audit item #76, half)
- Accept STATISTICS DEFAULT; fix missing SET STATISTICS on new tables
- Structured diffing for BASE type's 7 in-place-alterable properties
- Implement REPLICA IDENTITY and CLUSTER ON block directives
- Implement column alignment for dpg fmt (RFC §18.7)
- Add SECURITY LABEL support (RFC §14.11)
- Give OperatorClass structured member capture
- Add Aggregate Revocations and Extension Comment
- Support COMMENT ON Constraint/Index/Policy/Trigger
- Wire schema GRANT/REVOKE through all layers
- Require confirmation before dpg dump overwrites files
- Implement scalar-merge-conflict lint rule (full scope)
- Implement deprecated-reference lint rule
- Model SERIAL/BIGSERIAL/SMALLSERIAL as a first-class IR concept
- Implement 2 more RFC-documented rules and per-rule severity overrides
- Implement operator family loose members (RFC §14.4)
- Implement remaining unimplemented RFC object kinds and fix real bugs found exhaustively live-testing every DPG feature
- Reconstruct Subscriptions from the live catalog
- Support secret references in USER MAPPING OPTIONS
- Implement full Role attribute system with PASSWORD secret refs
- Support secret references in SUBSCRIPTION CONNECTION
- Implement Vault, AWS/GCP/Azure secret manager backends
- Support CONSTRAINTS plural block (RFC §4.8)
- Support WITH/NULLS NOT DISTINCT and Mode-B index syntax
- Introspect deferred-tier opaque objects (operators, TS, opclass/family)
- Introspect reliable-tier opaque objects; fix operator-class DROP
- Add support for macro spread references, resolution logic, and related tests across LSP and IDEA plugin
- Enforce blank line between schema attributes and first nested object in formatter and add related test
- Add support for DPG code formatting in IDEA plugin, including indentation, blank lines, and spacing adjustments
- Add support for macro declarations, rendering, and schema-level nesting in formatter
- Add support for macro references, completions, and spread expressions in IDEA plugin

### Fixed

- Close 5 lower-priority fresh-audit-2026-08-26 gaps
- Exclude default-privileges self-grant from ACL introspection
- Compact ROLE-direction WITH INHERIT when it matches the grantee's default
- Suppress extension comments matching the control-file default
- Capture GRANTED BY grantor from live ACL entries
- Exclude owner self-grants from ACL introspection
- Diff Strict/SecurityDef on existing functions
- Tablespace WITH params, policy rename, RLSForced dump, operator class SET SCHEMA
- Implement FROM existing_collation copy-from
- Diff and emit the LOGGED/UNLOGGED toggle
- Capture and diff every agg-option, not just six
- Diff PG16+ ICU RULES
- Use ALTER POLICY for TO/USING/WITH CHECK-only changes
- Use ALTER EXTENSION SET SCHEMA instead of DROP+CREATE
- Wire domain constraint NOT VALID and VALIDATE CONSTRAINT
- Emit ALTER ... SET SCHEMA for the remaining cross-schema rename kinds
- Implement CREATE TABLE (LIKE source_table [...]) instead of discarding it
- Emit ALTER ... SET SCHEMA for cross-schema RENAMED FROM moves
- Detect and rename Subscription, FDW, OperatorClass, OperatorFamily, StatisticsObject
- Detect and rename TSDict, TSParser, TSTemplate, Role, EventTrigger
- Detect and rename Type (all variants) and Collation
- Detect and rename Constraint and Index instead of drop+recreate
- Detect and rename renamed partitions instead of drop+recreate
- Support cross-schema RENAMED FROM and fix Function rename
- Emit ALTER TYPE ADD VALUE as transactional, not MANUAL
- Prevent spurious cycles from plpgsql table references
- Fix Phase 4 state-audit bugs #28/#30/#2
- Fix Phase 3 state-audit bugs #15+16/#14/#1/#17
- Fix Phase 2 state-audit bugs #3-6/#7/#8-11/#20/#24/#25/#26/#27
- Fix Phase 1 state-audit bugs #23/#21/#19/#12+13/#29
- Error on a case-mismatched VIRTUAL TYPE reference
- Make plan --live detect real OperatorClass AS-list changes
- Make the public schema visible to introspection, guard it from DROP
- Stop RANGE types from crashing dpg via an infinite Sort recursion
- Synthesize PUBLIC EXECUTE revocation for explicitly-revoked functions/procedures/aggregates
- Compare Aggregate options structurally instead of raw BodyHash
- Enforce DPG-E004 reserved-name conflict, deduplicate error wording
- Validate cluster/database name uniqueness and non-emptiness
- Connect per-database commands to the selected database, not whatever the cluster URL happens to point at
- Diff and introspect trigger WHEN conditions
- Apply NOT VALID on CREATE TABLE, ignore paren-only expression drift
- Stop double-rendering NOT VALID on dump
- Make BASE types actually round-trip
- Wire Type.Owner and fix dump comment loss
- Stop treating inherited columns/constraints as drift
- Render and correctly apply Table.Inherits
- Classify same-base-type typmod widening as CAUTION
- Classify implicit-cast column type changes as CAUTION
- Sandbox dpg dump -o's cluster-scoped output and snapshot
- Canonicalize embedded plpgsql expression fragments, closing the last hashing gap
- Stop keyword-casing from mangling identifiers, reconcile rule-ID naming
- Stop dpg fmt from doubling ';' and collapsing empty '{ }' blocks
- Canonicalize plpgsql body hashing, closing the last "Inherent" item
- Predict PostgreSQL's real fallback name for unpredictable EXCLUDE elements
- Close G-live gap for StatisticsObject, completing Workstream 2
- Close G-live gap for Collation
- Close G-live gap for Publication, closing Tier 2
- Close G-live gap for FDW/ForeignServer/UserMapping
- Close G-live gap for Tablespace/Cast/EventTrigger
- Canonicalize LANGUAGE SQL bodies before hashing to stop spurious CREATE OR REPLACE
- Redact password-like OPTIONS on USER MAPPING introspection
- Quote synthetic CREATE SCHEMA for directory-inferred schemas
- Add 3 missing dependency-graph edges (publication→table, sql-function→function, function-type→type)
- Implement RFC §5.4 DOMAIN structured diffing, fix introspectCasts internal-dependency bug
- Fix 9 real bugs found live-testing every DPG object kind against a real demo project
- Implement ChainResolver, fix silent scheme-passthrough bug
- Adopt pg_dump's model for operator families
- Infer implied return type for functions with omitted RETURNS clause
- Capture OUT/INOUT/VARIADIC argument modes on introspection
- Reconstruct RETURNS TABLE(...) functions correctly
- Add SETOF representation for function return types
- Capture and diff FUNCTION PARALLEL/COST/ROWS attributes
- Reconcile unnamed EXCLUDE constraint names against PostgreSQL's real auto-naming algorithm
- Parse EXCLUDE constraint bodies and fix two SQL-text rename bugs
- Capture operator operand types so DROP OPERATOR is valid and overloads don't collide
- Recognize PostgreSQL's auto-generated constraint names for unnamed CHECK constraints
- Render FUNCTION/PROCEDURE bodies instead of dropping them from dump output
- Recognize PostgreSQL's auto-generated constraint names for unnamed PRIMARY KEY/UNIQUE/FOREIGN KEY
- Render table/schema/sequence OWNER and column attributes; fix inert sequence ownership
- Fix sequence CYCLE parsing and add PROCEDURE test coverage
- Support reserved-word/quoted SCHEMA names
- Normalize trigger function references to avoid spurious drift
- Accept POLICY/TRIGGER/PARTITION singular keyword
- Emit REVOKE for explicit table/column/view REVOCATION
- Accept GRANT/REVOCATION singular keyword outside plural blocks
- Compare index definitions instead of matching by name only
- Emit structured DROP+CREATE for opaque object updates
- Make dumped projects recompile and re-apply faithfully

### Changed

- Document dpg fmt's undocumented --stdin flag
- Fix 6 stale doc-only items from fresh-audit-2026-08-26
- Fix stale SnapRole/SnapSequence examples and portability --format claim
- Promote min_pg_version out of Deferred Features, refresh Appendix F
- Add RENAMED FROM grammar for Policy, Trigger, Partition, TS Config
- Spell out RFC section reference instead of section-sign
- Replace section-sign character with spelled-out Section/Appendix
- Close final mop-up items and fix appendix physical section order
- Add newer-PostgreSQL-version grammar (PG15-18); fix ALTER TYPE ADD VALUE transaction-cutoff error
- Add namespace/storage/FDW/replication grammar; close remaining Phase 5 destructive-to-safe swaps
- Add full-text search grammar for silent PG feature gaps
- Add access-control grammar for silent PG feature gaps
- Add type/domain/function/aggregate grammar for silent PG feature gaps
- Add table/view/index/sequence grammar for silent PG feature gaps
- Fix CLI reference drift, undefined grammar, and Domain syntax; add PG version/portability classification
- Drop redundant { CONNECTION } block form, add COMMENT
- Update coverage matrix and clarify introspection behaviors
- Simplify schema node comment collection logic in parser
- Migrated idea plugin from LSP-based implementation to plugin-native parsing, highlighting, and tooling

## [grammar-v0.5.2-alpha.11] — 2026-08-26

### Added

- Implement DROP CONSTRAINT ... ONLY on partitioned tables (PG18+)
- Thread DPG-E0xx codes through documented sites, implement DPG-E001
- Materialized-view TABLESPACE/WITH storage-params support
- Structured optimizer-hint fields, safe ALTER OPERATOR SET diffing
- Parse and diff identity-column sequence options (Section 7.4)
- Implement foreign table as partition (RFC Section 7.13)
- Unify membership lists and implement WITH ADMIN/INHERIT/SET
- Implement GRANTED BY role and Role SET/RESET IN DATABASE
- Implement LEAKPROOF, TRANSFORM FOR TYPE, C obj_file/link_symbol, and BEGIN ATOMIC bodies
- Implement TSConfig owner and TSDict option-change diffing
- Implement ENUM positional ADD VALUE and RENAME VALUE
- Implement DISABLED/ENABLE REPLICA/ENABLE ALWAYS
- Diff and emit WITH (...) storage-params SET/RESET
- Implement ATTACHED FROM / DETACHED FROM partition directives
- Implement UNLOGGED prefix
- Implement ON ONLY prefix and opclass parameters
- Implement typed tables (OF type_name) and access method
- Implement composite attribute and domain COLLATE
- Implement WITHOUT OVERLAPS/PERIOD temporal keys (PG18)
- Implement ENFORCED/NOT ENFORCED on CHECK/FOREIGN KEY (PG18)
- Implement table-level named NOT NULL constraint (PG18)
- Implement generated-column VIRTUAL and its diffing
- Implement min_pg_version project-gating (Section 23)
- Implement PARAMETER PRIVILEGES (Section 11.6, PG15+)
- Implement OWNER TO
- Implement OWNER TO
- Implement Owner and RENAMED FROM
- Implement RENAMED FROM
- Implement RESTART and REFRESH VERSION
- Implement trigger enable-state (DISABLED / ENABLE REPLICA / ENABLE ALWAYS)
- Implement [NO] DEPENDS ON EXTENSION (RFC audit items #71, #75)
- Implement OWNER TO (RFC audit item #34, half)
- Implement OWNER TO (RFC audit item #76, half)
- Accept STATISTICS DEFAULT; fix missing SET STATISTICS on new tables
- Structured diffing for BASE type's 7 in-place-alterable properties
- Implement REPLICA IDENTITY and CLUSTER ON block directives
- Implement column alignment for dpg fmt (RFC §18.7)
- Add SECURITY LABEL support (RFC §14.11)
- Give OperatorClass structured member capture
- Add Aggregate Revocations and Extension Comment
- Support COMMENT ON Constraint/Index/Policy/Trigger
- Wire schema GRANT/REVOKE through all layers
- Require confirmation before dpg dump overwrites files
- Implement scalar-merge-conflict lint rule (full scope)
- Implement deprecated-reference lint rule
- Model SERIAL/BIGSERIAL/SMALLSERIAL as a first-class IR concept
- Implement 2 more RFC-documented rules and per-rule severity overrides
- Implement operator family loose members (RFC §14.4)
- Implement remaining unimplemented RFC object kinds and fix real bugs found exhaustively live-testing every DPG feature
- Reconstruct Subscriptions from the live catalog
- Support secret references in USER MAPPING OPTIONS
- Implement full Role attribute system with PASSWORD secret refs
- Support secret references in SUBSCRIPTION CONNECTION
- Implement Vault, AWS/GCP/Azure secret manager backends
- Support CONSTRAINTS plural block (RFC §4.8)
- Support WITH/NULLS NOT DISTINCT and Mode-B index syntax
- Introspect deferred-tier opaque objects (operators, TS, opclass/family)
- Introspect reliable-tier opaque objects; fix operator-class DROP
- Add support for macro spread references, resolution logic, and related tests across LSP and IDEA plugin
- Enforce blank line between schema attributes and first nested object in formatter and add related test
- Add support for DPG code formatting in IDEA plugin, including indentation, blank lines, and spacing adjustments
- Add support for macro declarations, rendering, and schema-level nesting in formatter
- Add support for macro references, completions, and spread expressions in IDEA plugin

### Fixed

- Close 5 lower-priority fresh-audit-2026-08-26 gaps
- Exclude default-privileges self-grant from ACL introspection
- Compact ROLE-direction WITH INHERIT when it matches the grantee's default
- Suppress extension comments matching the control-file default
- Capture GRANTED BY grantor from live ACL entries
- Exclude owner self-grants from ACL introspection
- Diff Strict/SecurityDef on existing functions
- Tablespace WITH params, policy rename, RLSForced dump, operator class SET SCHEMA
- Implement FROM existing_collation copy-from
- Diff and emit the LOGGED/UNLOGGED toggle
- Capture and diff every agg-option, not just six
- Diff PG16+ ICU RULES
- Use ALTER POLICY for TO/USING/WITH CHECK-only changes
- Use ALTER EXTENSION SET SCHEMA instead of DROP+CREATE
- Wire domain constraint NOT VALID and VALIDATE CONSTRAINT
- Emit ALTER ... SET SCHEMA for the remaining cross-schema rename kinds
- Implement CREATE TABLE (LIKE source_table [...]) instead of discarding it
- Emit ALTER ... SET SCHEMA for cross-schema RENAMED FROM moves
- Detect and rename Subscription, FDW, OperatorClass, OperatorFamily, StatisticsObject
- Detect and rename TSDict, TSParser, TSTemplate, Role, EventTrigger
- Detect and rename Type (all variants) and Collation
- Detect and rename Constraint and Index instead of drop+recreate
- Detect and rename renamed partitions instead of drop+recreate
- Support cross-schema RENAMED FROM and fix Function rename
- Emit ALTER TYPE ADD VALUE as transactional, not MANUAL
- Prevent spurious cycles from plpgsql table references
- Fix Phase 4 state-audit bugs #28/#30/#2
- Fix Phase 3 state-audit bugs #15+16/#14/#1/#17
- Fix Phase 2 state-audit bugs #3-6/#7/#8-11/#20/#24/#25/#26/#27
- Fix Phase 1 state-audit bugs #23/#21/#19/#12+13/#29
- Error on a case-mismatched VIRTUAL TYPE reference
- Make plan --live detect real OperatorClass AS-list changes
- Make the public schema visible to introspection, guard it from DROP
- Stop RANGE types from crashing dpg via an infinite Sort recursion
- Synthesize PUBLIC EXECUTE revocation for explicitly-revoked functions/procedures/aggregates
- Compare Aggregate options structurally instead of raw BodyHash
- Enforce DPG-E004 reserved-name conflict, deduplicate error wording
- Validate cluster/database name uniqueness and non-emptiness
- Connect per-database commands to the selected database, not whatever the cluster URL happens to point at
- Diff and introspect trigger WHEN conditions
- Apply NOT VALID on CREATE TABLE, ignore paren-only expression drift
- Stop double-rendering NOT VALID on dump
- Make BASE types actually round-trip
- Wire Type.Owner and fix dump comment loss
- Stop treating inherited columns/constraints as drift
- Render and correctly apply Table.Inherits
- Classify same-base-type typmod widening as CAUTION
- Classify implicit-cast column type changes as CAUTION
- Sandbox dpg dump -o's cluster-scoped output and snapshot
- Canonicalize embedded plpgsql expression fragments, closing the last hashing gap
- Stop keyword-casing from mangling identifiers, reconcile rule-ID naming
- Stop dpg fmt from doubling ';' and collapsing empty '{ }' blocks
- Canonicalize plpgsql body hashing, closing the last "Inherent" item
- Predict PostgreSQL's real fallback name for unpredictable EXCLUDE elements
- Close G-live gap for StatisticsObject, completing Workstream 2
- Close G-live gap for Collation
- Close G-live gap for Publication, closing Tier 2
- Close G-live gap for FDW/ForeignServer/UserMapping
- Close G-live gap for Tablespace/Cast/EventTrigger
- Canonicalize LANGUAGE SQL bodies before hashing to stop spurious CREATE OR REPLACE
- Redact password-like OPTIONS on USER MAPPING introspection
- Quote synthetic CREATE SCHEMA for directory-inferred schemas
- Add 3 missing dependency-graph edges (publication→table, sql-function→function, function-type→type)
- Implement RFC §5.4 DOMAIN structured diffing, fix introspectCasts internal-dependency bug
- Fix 9 real bugs found live-testing every DPG object kind against a real demo project
- Implement ChainResolver, fix silent scheme-passthrough bug
- Adopt pg_dump's model for operator families
- Infer implied return type for functions with omitted RETURNS clause
- Capture OUT/INOUT/VARIADIC argument modes on introspection
- Reconstruct RETURNS TABLE(...) functions correctly
- Add SETOF representation for function return types
- Capture and diff FUNCTION PARALLEL/COST/ROWS attributes
- Reconcile unnamed EXCLUDE constraint names against PostgreSQL's real auto-naming algorithm
- Parse EXCLUDE constraint bodies and fix two SQL-text rename bugs
- Capture operator operand types so DROP OPERATOR is valid and overloads don't collide
- Recognize PostgreSQL's auto-generated constraint names for unnamed CHECK constraints
- Render FUNCTION/PROCEDURE bodies instead of dropping them from dump output
- Recognize PostgreSQL's auto-generated constraint names for unnamed PRIMARY KEY/UNIQUE/FOREIGN KEY
- Render table/schema/sequence OWNER and column attributes; fix inert sequence ownership
- Fix sequence CYCLE parsing and add PROCEDURE test coverage
- Support reserved-word/quoted SCHEMA names
- Normalize trigger function references to avoid spurious drift
- Accept POLICY/TRIGGER/PARTITION singular keyword
- Emit REVOKE for explicit table/column/view REVOCATION
- Accept GRANT/REVOCATION singular keyword outside plural blocks
- Compare index definitions instead of matching by name only
- Emit structured DROP+CREATE for opaque object updates
- Make dumped projects recompile and re-apply faithfully

### Changed

- Document dpg fmt's undocumented --stdin flag
- Fix 6 stale doc-only items from fresh-audit-2026-08-26
- Fix stale SnapRole/SnapSequence examples and portability --format claim
- Promote min_pg_version out of Deferred Features, refresh Appendix F
- Add RENAMED FROM grammar for Policy, Trigger, Partition, TS Config
- Spell out RFC section reference instead of section-sign
- Replace section-sign character with spelled-out Section/Appendix
- Close final mop-up items and fix appendix physical section order
- Add newer-PostgreSQL-version grammar (PG15-18); fix ALTER TYPE ADD VALUE transaction-cutoff error
- Add namespace/storage/FDW/replication grammar; close remaining Phase 5 destructive-to-safe swaps
- Add full-text search grammar for silent PG feature gaps
- Add access-control grammar for silent PG feature gaps
- Add type/domain/function/aggregate grammar for silent PG feature gaps
- Add table/view/index/sequence grammar for silent PG feature gaps
- Fix CLI reference drift, undefined grammar, and Domain syntax; add PG version/portability classification
- Drop redundant { CONNECTION } block form, add COMMENT
- Update coverage matrix and clarify introspection behaviors
- Simplify schema node comment collection logic in parser
- Migrated idea plugin from LSP-based implementation to plugin-native parsing, highlighting, and tooling

## [lsp-v0.5.5-alpha.2] — 2026-08-26

### Added

- Implement DROP CONSTRAINT ... ONLY on partitioned tables (PG18+)
- Thread DPG-E0xx codes through documented sites, implement DPG-E001
- Materialized-view TABLESPACE/WITH storage-params support
- Structured optimizer-hint fields, safe ALTER OPERATOR SET diffing
- Parse and diff identity-column sequence options (Section 7.4)
- Implement foreign table as partition (RFC Section 7.13)
- Unify membership lists and implement WITH ADMIN/INHERIT/SET
- Implement GRANTED BY role and Role SET/RESET IN DATABASE
- Implement LEAKPROOF, TRANSFORM FOR TYPE, C obj_file/link_symbol, and BEGIN ATOMIC bodies
- Implement TSConfig owner and TSDict option-change diffing
- Implement ENUM positional ADD VALUE and RENAME VALUE
- Implement DISABLED/ENABLE REPLICA/ENABLE ALWAYS
- Diff and emit WITH (...) storage-params SET/RESET
- Implement ATTACHED FROM / DETACHED FROM partition directives
- Implement UNLOGGED prefix
- Implement ON ONLY prefix and opclass parameters
- Implement typed tables (OF type_name) and access method
- Implement composite attribute and domain COLLATE
- Implement WITHOUT OVERLAPS/PERIOD temporal keys (PG18)
- Implement ENFORCED/NOT ENFORCED on CHECK/FOREIGN KEY (PG18)
- Implement table-level named NOT NULL constraint (PG18)
- Implement generated-column VIRTUAL and its diffing
- Implement min_pg_version project-gating (Section 23)
- Implement PARAMETER PRIVILEGES (Section 11.6, PG15+)
- Implement OWNER TO
- Implement OWNER TO
- Implement Owner and RENAMED FROM
- Implement RENAMED FROM
- Implement RESTART and REFRESH VERSION
- Implement trigger enable-state (DISABLED / ENABLE REPLICA / ENABLE ALWAYS)
- Implement [NO] DEPENDS ON EXTENSION (RFC audit items #71, #75)
- Implement OWNER TO (RFC audit item #34, half)
- Implement OWNER TO (RFC audit item #76, half)
- Accept STATISTICS DEFAULT; fix missing SET STATISTICS on new tables
- Structured diffing for BASE type's 7 in-place-alterable properties
- Implement REPLICA IDENTITY and CLUSTER ON block directives
- Implement column alignment for dpg fmt (RFC §18.7)
- Add SECURITY LABEL support (RFC §14.11)
- Give OperatorClass structured member capture
- Add Aggregate Revocations and Extension Comment
- Support COMMENT ON Constraint/Index/Policy/Trigger
- Wire schema GRANT/REVOKE through all layers
- Require confirmation before dpg dump overwrites files
- Implement scalar-merge-conflict lint rule (full scope)
- Implement deprecated-reference lint rule
- Model SERIAL/BIGSERIAL/SMALLSERIAL as a first-class IR concept
- Implement 2 more RFC-documented rules and per-rule severity overrides
- Implement operator family loose members (RFC §14.4)
- Implement remaining unimplemented RFC object kinds and fix real bugs found exhaustively live-testing every DPG feature
- Reconstruct Subscriptions from the live catalog
- Support secret references in USER MAPPING OPTIONS
- Implement full Role attribute system with PASSWORD secret refs
- Support secret references in SUBSCRIPTION CONNECTION
- Implement Vault, AWS/GCP/Azure secret manager backends
- Support CONSTRAINTS plural block (RFC §4.8)
- Support WITH/NULLS NOT DISTINCT and Mode-B index syntax
- Introspect deferred-tier opaque objects (operators, TS, opclass/family)
- Introspect reliable-tier opaque objects; fix operator-class DROP
- Add support for macro spread references, resolution logic, and related tests across LSP and IDEA plugin

### Fixed

- Close 5 lower-priority fresh-audit-2026-08-26 gaps
- Exclude default-privileges self-grant from ACL introspection
- Compact ROLE-direction WITH INHERIT when it matches the grantee's default
- Suppress extension comments matching the control-file default
- Capture GRANTED BY grantor from live ACL entries
- Exclude owner self-grants from ACL introspection
- Diff Strict/SecurityDef on existing functions
- Tablespace WITH params, policy rename, RLSForced dump, operator class SET SCHEMA
- Implement FROM existing_collation copy-from
- Diff and emit the LOGGED/UNLOGGED toggle
- Capture and diff every agg-option, not just six
- Diff PG16+ ICU RULES
- Use ALTER POLICY for TO/USING/WITH CHECK-only changes
- Use ALTER EXTENSION SET SCHEMA instead of DROP+CREATE
- Wire domain constraint NOT VALID and VALIDATE CONSTRAINT
- Emit ALTER ... SET SCHEMA for the remaining cross-schema rename kinds
- Implement CREATE TABLE (LIKE source_table [...]) instead of discarding it
- Emit ALTER ... SET SCHEMA for cross-schema RENAMED FROM moves
- Detect and rename Subscription, FDW, OperatorClass, OperatorFamily, StatisticsObject
- Detect and rename TSDict, TSParser, TSTemplate, Role, EventTrigger
- Detect and rename Type (all variants) and Collation
- Detect and rename Constraint and Index instead of drop+recreate
- Detect and rename renamed partitions instead of drop+recreate
- Support cross-schema RENAMED FROM and fix Function rename
- Emit ALTER TYPE ADD VALUE as transactional, not MANUAL
- Prevent spurious cycles from plpgsql table references
- Fix Phase 4 state-audit bugs #28/#30/#2
- Fix Phase 3 state-audit bugs #15+16/#14/#1/#17
- Fix Phase 2 state-audit bugs #3-6/#7/#8-11/#20/#24/#25/#26/#27
- Fix Phase 1 state-audit bugs #23/#21/#19/#12+13/#29
- Error on a case-mismatched VIRTUAL TYPE reference
- Make plan --live detect real OperatorClass AS-list changes
- Make the public schema visible to introspection, guard it from DROP
- Stop RANGE types from crashing dpg via an infinite Sort recursion
- Synthesize PUBLIC EXECUTE revocation for explicitly-revoked functions/procedures/aggregates
- Compare Aggregate options structurally instead of raw BodyHash
- Enforce DPG-E004 reserved-name conflict, deduplicate error wording
- Validate cluster/database name uniqueness and non-emptiness
- Connect per-database commands to the selected database, not whatever the cluster URL happens to point at
- Diff and introspect trigger WHEN conditions
- Apply NOT VALID on CREATE TABLE, ignore paren-only expression drift
- Stop double-rendering NOT VALID on dump
- Make BASE types actually round-trip
- Wire Type.Owner and fix dump comment loss
- Stop treating inherited columns/constraints as drift
- Render and correctly apply Table.Inherits
- Classify same-base-type typmod widening as CAUTION
- Classify implicit-cast column type changes as CAUTION
- Sandbox dpg dump -o's cluster-scoped output and snapshot
- Canonicalize embedded plpgsql expression fragments, closing the last hashing gap
- Stop keyword-casing from mangling identifiers, reconcile rule-ID naming
- Stop dpg fmt from doubling ';' and collapsing empty '{ }' blocks
- Canonicalize plpgsql body hashing, closing the last "Inherent" item
- Predict PostgreSQL's real fallback name for unpredictable EXCLUDE elements
- Close G-live gap for StatisticsObject, completing Workstream 2
- Close G-live gap for Collation
- Close G-live gap for Publication, closing Tier 2
- Close G-live gap for FDW/ForeignServer/UserMapping
- Close G-live gap for Tablespace/Cast/EventTrigger
- Canonicalize LANGUAGE SQL bodies before hashing to stop spurious CREATE OR REPLACE
- Redact password-like OPTIONS on USER MAPPING introspection
- Quote synthetic CREATE SCHEMA for directory-inferred schemas
- Add 3 missing dependency-graph edges (publication→table, sql-function→function, function-type→type)
- Implement RFC §5.4 DOMAIN structured diffing, fix introspectCasts internal-dependency bug
- Fix 9 real bugs found live-testing every DPG object kind against a real demo project
- Implement ChainResolver, fix silent scheme-passthrough bug
- Adopt pg_dump's model for operator families
- Infer implied return type for functions with omitted RETURNS clause
- Capture OUT/INOUT/VARIADIC argument modes on introspection
- Reconstruct RETURNS TABLE(...) functions correctly
- Add SETOF representation for function return types
- Capture and diff FUNCTION PARALLEL/COST/ROWS attributes
- Reconcile unnamed EXCLUDE constraint names against PostgreSQL's real auto-naming algorithm
- Parse EXCLUDE constraint bodies and fix two SQL-text rename bugs
- Capture operator operand types so DROP OPERATOR is valid and overloads don't collide
- Recognize PostgreSQL's auto-generated constraint names for unnamed CHECK constraints
- Render FUNCTION/PROCEDURE bodies instead of dropping them from dump output
- Recognize PostgreSQL's auto-generated constraint names for unnamed PRIMARY KEY/UNIQUE/FOREIGN KEY
- Render table/schema/sequence OWNER and column attributes; fix inert sequence ownership
- Fix sequence CYCLE parsing and add PROCEDURE test coverage
- Support reserved-word/quoted SCHEMA names
- Normalize trigger function references to avoid spurious drift
- Accept POLICY/TRIGGER/PARTITION singular keyword
- Emit REVOKE for explicit table/column/view REVOCATION
- Accept GRANT/REVOCATION singular keyword outside plural blocks
- Compare index definitions instead of matching by name only
- Emit structured DROP+CREATE for opaque object updates
- Make dumped projects recompile and re-apply faithfully

### Changed

- Document dpg fmt's undocumented --stdin flag
- Fix 6 stale doc-only items from fresh-audit-2026-08-26
- Fix stale SnapRole/SnapSequence examples and portability --format claim
- Promote min_pg_version out of Deferred Features, refresh Appendix F
- Add RENAMED FROM grammar for Policy, Trigger, Partition, TS Config
- Spell out RFC section reference instead of section-sign
- Replace section-sign character with spelled-out Section/Appendix
- Close final mop-up items and fix appendix physical section order
- Add newer-PostgreSQL-version grammar (PG15-18); fix ALTER TYPE ADD VALUE transaction-cutoff error
- Add namespace/storage/FDW/replication grammar; close remaining Phase 5 destructive-to-safe swaps
- Add full-text search grammar for silent PG feature gaps
- Add access-control grammar for silent PG feature gaps
- Add type/domain/function/aggregate grammar for silent PG feature gaps
- Add table/view/index/sequence grammar for silent PG feature gaps
- Fix CLI reference drift, undefined grammar, and Domain syntax; add PG version/portability classification
- Drop redundant { CONNECTION } block form, add COMMENT
- Update coverage matrix and clarify introspection behaviors

## [0.5.5-alpha.2] — 2026-08-26

### Added

- Implement DROP CONSTRAINT ... ONLY on partitioned tables (PG18+)
- Thread DPG-E0xx codes through documented sites, implement DPG-E001
- Materialized-view TABLESPACE/WITH storage-params support
- Structured optimizer-hint fields, safe ALTER OPERATOR SET diffing
- Parse and diff identity-column sequence options (Section 7.4)
- Implement foreign table as partition (RFC Section 7.13)
- Unify membership lists and implement WITH ADMIN/INHERIT/SET
- Implement GRANTED BY role and Role SET/RESET IN DATABASE
- Implement LEAKPROOF, TRANSFORM FOR TYPE, C obj_file/link_symbol, and BEGIN ATOMIC bodies
- Implement TSConfig owner and TSDict option-change diffing
- Implement ENUM positional ADD VALUE and RENAME VALUE
- Implement DISABLED/ENABLE REPLICA/ENABLE ALWAYS
- Diff and emit WITH (...) storage-params SET/RESET
- Implement ATTACHED FROM / DETACHED FROM partition directives
- Implement UNLOGGED prefix
- Implement ON ONLY prefix and opclass parameters
- Implement typed tables (OF type_name) and access method
- Implement composite attribute and domain COLLATE
- Implement WITHOUT OVERLAPS/PERIOD temporal keys (PG18)
- Implement ENFORCED/NOT ENFORCED on CHECK/FOREIGN KEY (PG18)
- Implement table-level named NOT NULL constraint (PG18)
- Implement generated-column VIRTUAL and its diffing
- Implement min_pg_version project-gating (Section 23)
- Implement PARAMETER PRIVILEGES (Section 11.6, PG15+)
- Implement OWNER TO
- Implement OWNER TO
- Implement Owner and RENAMED FROM
- Implement RENAMED FROM
- Implement RESTART and REFRESH VERSION
- Implement trigger enable-state (DISABLED / ENABLE REPLICA / ENABLE ALWAYS)
- Implement [NO] DEPENDS ON EXTENSION (RFC audit items #71, #75)
- Implement OWNER TO (RFC audit item #34, half)
- Implement OWNER TO (RFC audit item #76, half)
- Accept STATISTICS DEFAULT; fix missing SET STATISTICS on new tables
- Structured diffing for BASE type's 7 in-place-alterable properties
- Implement REPLICA IDENTITY and CLUSTER ON block directives
- Implement column alignment for dpg fmt (RFC §18.7)
- Add SECURITY LABEL support (RFC §14.11)
- Give OperatorClass structured member capture
- Add Aggregate Revocations and Extension Comment
- Support COMMENT ON Constraint/Index/Policy/Trigger
- Wire schema GRANT/REVOKE through all layers
- Require confirmation before dpg dump overwrites files
- Implement scalar-merge-conflict lint rule (full scope)
- Implement deprecated-reference lint rule
- Model SERIAL/BIGSERIAL/SMALLSERIAL as a first-class IR concept
- Implement 2 more RFC-documented rules and per-rule severity overrides
- Implement operator family loose members (RFC §14.4)
- Implement remaining unimplemented RFC object kinds and fix real bugs found exhaustively live-testing every DPG feature
- Reconstruct Subscriptions from the live catalog
- Support secret references in USER MAPPING OPTIONS
- Implement full Role attribute system with PASSWORD secret refs
- Support secret references in SUBSCRIPTION CONNECTION
- Implement Vault, AWS/GCP/Azure secret manager backends
- Support CONSTRAINTS plural block (RFC §4.8)
- Support WITH/NULLS NOT DISTINCT and Mode-B index syntax
- Introspect deferred-tier opaque objects (operators, TS, opclass/family)
- Introspect reliable-tier opaque objects; fix operator-class DROP
- Add support for macro spread references, resolution logic, and related tests across LSP and IDEA plugin

### Fixed

- Close 5 lower-priority fresh-audit-2026-08-26 gaps
- Exclude default-privileges self-grant from ACL introspection
- Compact ROLE-direction WITH INHERIT when it matches the grantee's default
- Suppress extension comments matching the control-file default
- Capture GRANTED BY grantor from live ACL entries
- Exclude owner self-grants from ACL introspection
- Diff Strict/SecurityDef on existing functions
- Tablespace WITH params, policy rename, RLSForced dump, operator class SET SCHEMA
- Implement FROM existing_collation copy-from
- Diff and emit the LOGGED/UNLOGGED toggle
- Capture and diff every agg-option, not just six
- Diff PG16+ ICU RULES
- Use ALTER POLICY for TO/USING/WITH CHECK-only changes
- Use ALTER EXTENSION SET SCHEMA instead of DROP+CREATE
- Wire domain constraint NOT VALID and VALIDATE CONSTRAINT
- Emit ALTER ... SET SCHEMA for the remaining cross-schema rename kinds
- Implement CREATE TABLE (LIKE source_table [...]) instead of discarding it
- Emit ALTER ... SET SCHEMA for cross-schema RENAMED FROM moves
- Detect and rename Subscription, FDW, OperatorClass, OperatorFamily, StatisticsObject
- Detect and rename TSDict, TSParser, TSTemplate, Role, EventTrigger
- Detect and rename Type (all variants) and Collation
- Detect and rename Constraint and Index instead of drop+recreate
- Detect and rename renamed partitions instead of drop+recreate
- Support cross-schema RENAMED FROM and fix Function rename
- Emit ALTER TYPE ADD VALUE as transactional, not MANUAL
- Prevent spurious cycles from plpgsql table references
- Fix Phase 4 state-audit bugs #28/#30/#2
- Fix Phase 3 state-audit bugs #15+16/#14/#1/#17
- Fix Phase 2 state-audit bugs #3-6/#7/#8-11/#20/#24/#25/#26/#27
- Fix Phase 1 state-audit bugs #23/#21/#19/#12+13/#29
- Error on a case-mismatched VIRTUAL TYPE reference
- Make plan --live detect real OperatorClass AS-list changes
- Make the public schema visible to introspection, guard it from DROP
- Stop RANGE types from crashing dpg via an infinite Sort recursion
- Synthesize PUBLIC EXECUTE revocation for explicitly-revoked functions/procedures/aggregates
- Compare Aggregate options structurally instead of raw BodyHash
- Enforce DPG-E004 reserved-name conflict, deduplicate error wording
- Validate cluster/database name uniqueness and non-emptiness
- Connect per-database commands to the selected database, not whatever the cluster URL happens to point at
- Diff and introspect trigger WHEN conditions
- Apply NOT VALID on CREATE TABLE, ignore paren-only expression drift
- Stop double-rendering NOT VALID on dump
- Make BASE types actually round-trip
- Wire Type.Owner and fix dump comment loss
- Stop treating inherited columns/constraints as drift
- Render and correctly apply Table.Inherits
- Classify same-base-type typmod widening as CAUTION
- Classify implicit-cast column type changes as CAUTION
- Sandbox dpg dump -o's cluster-scoped output and snapshot
- Canonicalize embedded plpgsql expression fragments, closing the last hashing gap
- Stop keyword-casing from mangling identifiers, reconcile rule-ID naming
- Stop dpg fmt from doubling ';' and collapsing empty '{ }' blocks
- Canonicalize plpgsql body hashing, closing the last "Inherent" item
- Predict PostgreSQL's real fallback name for unpredictable EXCLUDE elements
- Close G-live gap for StatisticsObject, completing Workstream 2
- Close G-live gap for Collation
- Close G-live gap for Publication, closing Tier 2
- Close G-live gap for FDW/ForeignServer/UserMapping
- Close G-live gap for Tablespace/Cast/EventTrigger
- Canonicalize LANGUAGE SQL bodies before hashing to stop spurious CREATE OR REPLACE
- Redact password-like OPTIONS on USER MAPPING introspection
- Quote synthetic CREATE SCHEMA for directory-inferred schemas
- Add 3 missing dependency-graph edges (publication→table, sql-function→function, function-type→type)
- Implement RFC §5.4 DOMAIN structured diffing, fix introspectCasts internal-dependency bug
- Fix 9 real bugs found live-testing every DPG object kind against a real demo project
- Implement ChainResolver, fix silent scheme-passthrough bug
- Adopt pg_dump's model for operator families
- Infer implied return type for functions with omitted RETURNS clause
- Capture OUT/INOUT/VARIADIC argument modes on introspection
- Reconstruct RETURNS TABLE(...) functions correctly
- Add SETOF representation for function return types
- Capture and diff FUNCTION PARALLEL/COST/ROWS attributes
- Reconcile unnamed EXCLUDE constraint names against PostgreSQL's real auto-naming algorithm
- Parse EXCLUDE constraint bodies and fix two SQL-text rename bugs
- Capture operator operand types so DROP OPERATOR is valid and overloads don't collide
- Recognize PostgreSQL's auto-generated constraint names for unnamed CHECK constraints
- Render FUNCTION/PROCEDURE bodies instead of dropping them from dump output
- Recognize PostgreSQL's auto-generated constraint names for unnamed PRIMARY KEY/UNIQUE/FOREIGN KEY
- Render table/schema/sequence OWNER and column attributes; fix inert sequence ownership
- Fix sequence CYCLE parsing and add PROCEDURE test coverage
- Support reserved-word/quoted SCHEMA names
- Normalize trigger function references to avoid spurious drift
- Accept POLICY/TRIGGER/PARTITION singular keyword
- Emit REVOKE for explicit table/column/view REVOCATION
- Accept GRANT/REVOCATION singular keyword outside plural blocks
- Compare index definitions instead of matching by name only
- Emit structured DROP+CREATE for opaque object updates
- Make dumped projects recompile and re-apply faithfully

### Changed

- Document dpg fmt's undocumented --stdin flag
- Fix 6 stale doc-only items from fresh-audit-2026-08-26
- Fix stale SnapRole/SnapSequence examples and portability --format claim
- Promote min_pg_version out of Deferred Features, refresh Appendix F
- Add RENAMED FROM grammar for Policy, Trigger, Partition, TS Config
- Spell out RFC section reference instead of section-sign
- Replace section-sign character with spelled-out Section/Appendix
- Close final mop-up items and fix appendix physical section order
- Add newer-PostgreSQL-version grammar (PG15-18); fix ALTER TYPE ADD VALUE transaction-cutoff error
- Add namespace/storage/FDW/replication grammar; close remaining Phase 5 destructive-to-safe swaps
- Add full-text search grammar for silent PG feature gaps
- Add access-control grammar for silent PG feature gaps
- Add type/domain/function/aggregate grammar for silent PG feature gaps
- Add table/view/index/sequence grammar for silent PG feature gaps
- Fix CLI reference drift, undefined grammar, and Domain syntax; add PG version/portability classification
- Drop redundant { CONNECTION } block form, add COMMENT
- Update coverage matrix and clarify introspection behaviors

## [idea-v0.5.2-alpha.13] — 2026-05-22

### Added

- Add support for macro spread references, resolution logic, and related tests across LSP and IDEA plugin
- Enforce blank line between schema attributes and first nested object in formatter and add related test
- Add support for DPG code formatting in IDEA plugin, including indentation, blank lines, and spacing adjustments
- Add support for macro declarations, rendering, and schema-level nesting in formatter

### Changed

- Simplify schema node comment collection logic in parser

## [0.5.5-alpha.1] — 2026-05-21

### Added

- Enforce blank line between schema attributes and first nested object in formatter and add related test
- Add support for DPG code formatting in IDEA plugin, including indentation, blank lines, and spacing adjustments
- Add support for macro declarations, rendering, and schema-level nesting in formatter
- Add support for macro references, completions, and spread expressions in IDEA plugin

### Changed

- Simplify schema node comment collection logic in parser
- Migrated idea plugin from LSP-based implementation to plugin-native parsing, highlighting, and tooling

## [lsp-v0.5.5-alpha.1] — 2026-05-21

- No changes.

## [lsp-v0.5.2-alpha.11] — 2026-05-21

### Added

- Enforce blank line between schema attributes and first nested object in formatter and add related test
- Add support for DPG code formatting in IDEA plugin, including indentation, blank lines, and spacing adjustments
- Add support for macro declarations, rendering, and schema-level nesting in formatter
- Add support for macro references, completions, and spread expressions in IDEA plugin

### Changed

- Simplify schema node comment collection logic in parser
- Migrated idea plugin from LSP-based implementation to plugin-native parsing, highlighting, and tooling

## [idea-v0.5.2-alpha.12] — 2026-05-20

### Added

- Add support for macro references, completions, and spread expressions in IDEA plugin

## [idea-v0.5.2-alpha.11] — 2026-05-20

### Changed

- Migrated idea plugin from LSP-based implementation to plugin-native parsing, highlighting, and tooling

## [0.5.4] — 2026-05-17

- No changes.

## [0.5.3] — 2026-05-17

- No changes.

## [0.5.2-alpha.21] — 2026-05-17

- No changes.

## [0.5.2-alpha.20] — 2026-05-17

- No changes.

## [0.5.2-alpha.18] — 2026-05-17

- No changes.

## [0.5.2-alpha.17] — 2026-05-17

- No changes.

## [0.5.2-alpha.16] — 2026-05-17

- No changes.

## [0.5.2-alpha.15] — 2026-05-17

- No changes.

## [0.5.2-alpha.14] — 2026-05-17

- No changes.

## [0.5.2-alpha.13] — 2026-05-17

- No changes.

## [0.5.2-alpha.12] — 2026-05-17

- No changes.

## [0.5.2-alpha.11] — 2026-05-17

- No changes.

## [idea-v0.5.2-alpha.10] — 2026-05-17

- No changes.

## [vscode-v0.5.2-alpha.10] — 2026-05-17

- No changes.

## [nvim-v0.5.2-alpha.10] — 2026-05-17

### Added

- Introduce Name Maps with tool-specific naming conventions
- Enhance syntax highlighting, macros, and object parsing
- Support `PREFERRED JSON FORMAT` directive for virtual types
- Add support for structured virtual types with JSONB resolution
- Add support for nested macro expansion with circular reference detection
- Enable project-scoped macro sharing across `.dpg` files
- Add support for `DEFAULT PRIVILEGES` in snapshots and diffs
- Add support for LSP methods `SetTrace`, `TextDocumentDidSave`, and `WorkspaceDidChangeWatchedFiles`
- Add shortcodes, workflows, and versioning improvements for docs and LSP
- Add install-lsp script and integrate versioning enhancements for dpg-lsp
- Enable local-first Gradle distribution for faster offline builds
- Add test data and refactor LSP smoke testing root logic
- Add installer script and documentation for streamlined binary installation
- Add workflow support and bindings for editor integrations
- Add comprehensive DPG support to editors, tests, and tooling
- Introduce RFC DPG-1 specification for Declarative PG
- Add RFC for Declarative PG (DPG) specification
- Add comprehensive documentation for all schema objects and advanced features
- Add RenderAll for multi-migration output and refactor migration planning
- Add `VIRTUAL TYPE` and macro preprocessing support
- Inline single-column constraints and suppress redundant NOT NULL for PostgreSQL
- Normalize NOT NULL handling for PostgreSQL PRIMARY KEY columns
- Add macro preprocessing and support for virtual type declaration
- Improve introspection and schema handling for PostgreSQL
- Add minimal home page layout for website
- Add new commands and editor integration for improved `.dpg` workflow
- Add plugin example for custom linter registration and chaining
- Enhance aggregate and enum diffing with grants and migration support
- Enhance column and partition handling in snapshot diffing
- Enhance snapshot diffing with grant tracking and structural updates
- Introduce base infrastructure for DPG compiler, formatter, and public API

### Fixed

- Improve LSP script handling by downloading before execution
- Correctly handle array expansion for LSP install arguments in install script
- Update prerelease detection regex in release workflows
- Return error for missing body in `createOpaque` to prevent silent no-op

### Changed

- Switch COMMENT and DEPRECATED directives to single quotes for consistency
- Update snapshot schema spec for enhanced readability and structure
- Update `introspect_integration_test` to use `introspect.New()` instead of `New()` for improved clarity and consistency
- Rename `website` to `site` for consistent naming convention
- Move core logic and tests into `core` directory for better modularity
- Standardize directory structure by renaming `editors` to `plugins` and `editors/lsp` to `lang/lsp`
- Simplify argument signature generation with ArgsKey and dropObject cleanup
- prefer user-local Hugo binary in Makefile and setup script
- Add editor integration guide and extend `dpg fmt` documentation
- Improve `setup.sh` script and update website configs
- add development guide and setup script
- add CLI documentation generation and embedding in release builds
- Add comprehensive CLI and documentation overhaul
- Update documentation for commands, directory structure, and test workflows
- Filled in gaps and added test containers for integrations tests
- Enforce column existence and reject unknown columns in TABLE builds per RFC §7.2

## [lsp-v0.5.2-alpha.10] — 2026-05-17

### Added

- Introduce Name Maps with tool-specific naming conventions
- Enhance syntax highlighting, macros, and object parsing
- Support `PREFERRED JSON FORMAT` directive for virtual types
- Add support for structured virtual types with JSONB resolution
- Add support for nested macro expansion with circular reference detection
- Enable project-scoped macro sharing across `.dpg` files
- Add support for `DEFAULT PRIVILEGES` in snapshots and diffs
- Add support for LSP methods `SetTrace`, `TextDocumentDidSave`, and `WorkspaceDidChangeWatchedFiles`

### Fixed

- Improve LSP script handling by downloading before execution
- Correctly handle array expansion for LSP install arguments in install script
- Update prerelease detection regex in release workflows

### Changed

- Switch COMMENT and DEPRECATED directives to single quotes for consistency
- Update snapshot schema spec for enhanced readability and structure
- Update `introspect_integration_test` to use `introspect.New()` instead of `New()` for improved clarity and consistency
- Rename `website` to `site` for consistent naming convention
- Move core logic and tests into `core` directory for better modularity
- Standardize directory structure by renaming `editors` to `plugins` and `editors/lsp` to `lang/lsp`

## [grammar-v0.5.2-alpha.10] — 2026-05-17

### Added

- Introduce Name Maps with tool-specific naming conventions
- Enhance syntax highlighting, macros, and object parsing
- Support `PREFERRED JSON FORMAT` directive for virtual types
- Add support for structured virtual types with JSONB resolution
- Add support for nested macro expansion with circular reference detection
- Enable project-scoped macro sharing across `.dpg` files
- Add support for `DEFAULT PRIVILEGES` in snapshots and diffs
- Add support for LSP methods `SetTrace`, `TextDocumentDidSave`, and `WorkspaceDidChangeWatchedFiles`
- Add shortcodes, workflows, and versioning improvements for docs and LSP
- Add install-lsp script and integrate versioning enhancements for dpg-lsp
- Enable local-first Gradle distribution for faster offline builds
- Add test data and refactor LSP smoke testing root logic
- Add installer script and documentation for streamlined binary installation
- Add workflow support and bindings for editor integrations
- Add comprehensive DPG support to editors, tests, and tooling
- Introduce RFC DPG-1 specification for Declarative PG
- Add RFC for Declarative PG (DPG) specification
- Add comprehensive documentation for all schema objects and advanced features
- Add RenderAll for multi-migration output and refactor migration planning
- Add `VIRTUAL TYPE` and macro preprocessing support
- Inline single-column constraints and suppress redundant NOT NULL for PostgreSQL
- Normalize NOT NULL handling for PostgreSQL PRIMARY KEY columns
- Add macro preprocessing and support for virtual type declaration
- Improve introspection and schema handling for PostgreSQL
- Add minimal home page layout for website
- Add new commands and editor integration for improved `.dpg` workflow
- Add plugin example for custom linter registration and chaining
- Enhance aggregate and enum diffing with grants and migration support
- Enhance column and partition handling in snapshot diffing
- Enhance snapshot diffing with grant tracking and structural updates
- Introduce base infrastructure for DPG compiler, formatter, and public API

### Fixed

- Improve LSP script handling by downloading before execution
- Correctly handle array expansion for LSP install arguments in install script
- Update prerelease detection regex in release workflows
- Return error for missing body in `createOpaque` to prevent silent no-op

### Changed

- Switch COMMENT and DEPRECATED directives to single quotes for consistency
- Update snapshot schema spec for enhanced readability and structure
- Update `introspect_integration_test` to use `introspect.New()` instead of `New()` for improved clarity and consistency
- Rename `website` to `site` for consistent naming convention
- Move core logic and tests into `core` directory for better modularity
- Standardize directory structure by renaming `editors` to `plugins` and `editors/lsp` to `lang/lsp`
- Simplify argument signature generation with ArgsKey and dropObject cleanup
- prefer user-local Hugo binary in Makefile and setup script
- Add editor integration guide and extend `dpg fmt` documentation
- Improve `setup.sh` script and update website configs
- add development guide and setup script
- add CLI documentation generation and embedding in release builds
- Add comprehensive CLI and documentation overhaul
- Update documentation for commands, directory structure, and test workflows
- Filled in gaps and added test containers for integrations tests
- Enforce column existence and reject unknown columns in TABLE builds per RFC §7.2

## [0.5.2-alpha.10] — 2026-05-17

- No changes.

## [0.5.2-alpha.9] — 2026-05-17

- No changes.

## [0.5.2-alpha.8] — 2026-05-17

- No changes.

## [0.5.2-alpha.7] — 2026-05-17

- No changes.

## [0.5.2-alpha.6] — 2026-05-17

- No changes.

## [0.5.2-alpha.5] — 2026-05-17

- No changes.

## [vscode-v0.5.2] — 2026-05-17

- No changes.

## [idea-v0.5.2-alpha.4] — 2026-05-17

### Added

- Introduce Name Maps with tool-specific naming conventions
- Enhance syntax highlighting, macros, and object parsing
- Support `PREFERRED JSON FORMAT` directive for virtual types
- Add support for structured virtual types with JSONB resolution
- Add support for nested macro expansion with circular reference detection
- Enable project-scoped macro sharing across `.dpg` files
- Add support for `DEFAULT PRIVILEGES` in snapshots and diffs
- Add support for LSP methods `SetTrace`, `TextDocumentDidSave`, and `WorkspaceDidChangeWatchedFiles`

### Fixed

- Improve LSP script handling by downloading before execution
- Correctly handle array expansion for LSP install arguments in install script
- Update prerelease detection regex in release workflows

### Changed

- Switch COMMENT and DEPRECATED directives to single quotes for consistency
- Update snapshot schema spec for enhanced readability and structure
- Update `introspect_integration_test` to use `introspect.New()` instead of `New()` for improved clarity and consistency
- Rename `website` to `site` for consistent naming convention
- Move core logic and tests into `core` directory for better modularity
- Standardize directory structure by renaming `editors` to `plugins` and `editors/lsp` to `lang/lsp`

## [vscode-v0.5.2-alpha.4] — 2026-05-17

### Added

- Introduce Name Maps with tool-specific naming conventions
- Enhance syntax highlighting, macros, and object parsing
- Support `PREFERRED JSON FORMAT` directive for virtual types
- Add support for structured virtual types with JSONB resolution
- Add support for nested macro expansion with circular reference detection
- Enable project-scoped macro sharing across `.dpg` files
- Add support for `DEFAULT PRIVILEGES` in snapshots and diffs
- Add support for LSP methods `SetTrace`, `TextDocumentDidSave`, and `WorkspaceDidChangeWatchedFiles`

### Fixed

- Improve LSP script handling by downloading before execution
- Correctly handle array expansion for LSP install arguments in install script

### Changed

- Switch COMMENT and DEPRECATED directives to single quotes for consistency
- Update snapshot schema spec for enhanced readability and structure
- Update `introspect_integration_test` to use `introspect.New()` instead of `New()` for improved clarity and consistency
- Rename `website` to `site` for consistent naming convention
- Move core logic and tests into `core` directory for better modularity
- Standardize directory structure by renaming `editors` to `plugins` and `editors/lsp` to `lang/lsp`

## [0.5.2-alpha.4] — 2026-05-17

### Changed

- Switch COMMENT and DEPRECATED directives to single quotes for consistency

## [0.5.2-alpha.3] — 2026-05-17

### Added

- Introduce Name Maps with tool-specific naming conventions

## [0.5.2-alpha.2] — 2026-05-17

### Added

- Enhance syntax highlighting, macros, and object parsing
- Support `PREFERRED JSON FORMAT` directive for virtual types
- Add support for structured virtual types with JSONB resolution
- Add support for nested macro expansion with circular reference detection
- Enable project-scoped macro sharing across `.dpg` files
- Add support for `DEFAULT PRIVILEGES` in snapshots and diffs

### Changed

- Update snapshot schema spec for enhanced readability and structure
- Update `introspect_integration_test` to use `introspect.New()` instead of `New()` for improved clarity and consistency
- Rename `website` to `site` for consistent naming convention
- Move core logic and tests into `core` directory for better modularity
- Standardize directory structure by renaming `editors` to `plugins` and `editors/lsp` to `lang/lsp`

## [0.5.2] — 2026-05-16

### Added

- Add support for LSP methods `SetTrace`, `TextDocumentDidSave`, and `WorkspaceDidChangeWatchedFiles`

### Fixed

- Improve LSP script handling by downloading before execution
- Correctly handle array expansion for LSP install arguments in install script

## [0.5.1] — 2026-05-16

### Fixed

- Update prerelease detection regex in release workflows

## [0.5.1-alpha.9-rc.10] — 2026-05-16

- No changes.

## [0.5.1-alpha.9-rc.9] — 2026-05-16

- No changes.

## [0.5.1-alpha.9-rc.8] — 2026-05-16

- No changes.

## [0.5.1-alpha.9-rc.7] — 2026-05-16

- No changes.

## [0.5.1-alpha.9-rc.6] — 2026-05-16

- No changes.

## [0.1.0] — 2026-04-29

Initial release.

### Added

**CLI**
- `dpg plan` — diff source files against the committed snapshot and print the minimal SQL migration; supports `--live` to diff against a live database instead
- `dpg apply` — lint, diff, prompt for approval, execute the SQL migration, and update the committed snapshot; supports `--allow-destructive` and `--yes`
- `dpg verify` — connect to a live database and report any drift from the committed snapshot
- `dpg dump` — introspect a live database and generate initial `.dpg` source files and an initial snapshot
- `dpg diff` — diff two `.dpg` source directories and print the SQL between them (no database required)
- `dpg portability` — report PostgreSQL-specific constructs in use and suggest standard SQL alternatives
- `--cluster` and `--database` flags on all commands for multi-cluster/multi-database projects
- Cluster-level objects (roles) planned, applied, and snapshotted independently from databases

**Compiler**
- Source file scanning, parsing via `pg_query_go`, and IR construction for all supported object types
- Schema context inference from directory layout
- Dependency-ordered compilation with topological sort and `DEFERRABLE` cycle handling for circular foreign keys

**Object support**
- Tables: columns (including `IDENTITY`, `GENERATED`, computed), inline single-column constraints, table-level constraints, indexes, RLS, comments, grants
- Views and materialized views
- Functions and procedures
- Types: `ENUM`, `DOMAIN`, composite
- Sequences (user-defined; identity-owned sequences are filtered)
- Roles
- Extensions
- Schemas

**Differ**
- `CREATE`, `ALTER`, and `DROP` generation for all supported object types
- Safety classification: every generated statement tagged `SAFE`, `CAUTION`, `DESTRUCTIVE`, or `MANUAL`
- Destructive operations blocked by default; require `--allow-destructive`
- Warning on `dpg apply` for new tables created without a primary key

**Snapshot**
- JSON snapshot format committed alongside source files
- `dpg dump` rebuilds the snapshot from compiled source to ensure the first `dpg plan` after a dump produces no diff

**Linter**
- Configurable static analysis: deprecated column detection, hardcoded password detection, missing column comments
- Lint diagnostics printed to stderr before any migration is emitted

**Emit**
- Transactional wrapper (`BEGIN` / `COMMIT`) for all safe operations
- Non-transactional post-commit block for `CREATE INDEX CONCURRENTLY` and similar
- Safety labels and source position annotations in rendered output
- ANSI colour support

**Portability analysis**
- Detection and reporting of PostgreSQL-specific constructs with standard SQL alternatives

**Project structure**
- `dpg.toml` discovery with cluster and database directory layout
- Secret resolution via `env:` and `link:` URI schemes
- Migration archiving to a configurable directory

[Unreleased]: https://github.com/thec1oud/dpg/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/thec1oud/dpg/releases/tag/v0.1.0
