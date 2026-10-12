---
title: "RFC DPG-1: Declarative PG"
rfc_number: "DPG-001"
rfc_status: "Standards Track"
rfc_version: "0.8.1"
rfc_date: "2026-05-13"
rfc_target: "PostgreSQL 14-18"
rfc_authors: "Daniel Tsegaw"
layout: "rfc"
weight: 1
description: "Full normative specification for DPG — a declarative, state-based superset of PostgreSQL SQL that compiles to idiomatic PG DDL."
---

```
DPG Working Group                                          D. Tsegaw
Request for Comments: 1                                    Independent
Category: Standards Track                                  May 2026
ISSN: (N/A — project-internal specification)


         Declarative PG (DPG): A Declarative State-Based Superset
                        of PostgreSQL DDL

Abstract

   This document specifies Declarative PG (DPG), a declarative,
   state-based superset of PostgreSQL SQL that compiles to idiomatic
   PostgreSQL DDL.  DPG source files describe the desired state of a
   PostgreSQL database; the DPG compiler computes the minimal, safe,
   ordered set of DDL statements required to transition the current
   state to the desired state.  This specification defines the DPG
   source language, the compilation pipeline, the snapshot interchange
   format, the migration output format, and all associated tooling
   behaviour.

Status of This Memo

   This document specifies a Standards Track protocol for the DPG
   project ecosystem.  Distribution of this memo is unlimited.

   This document is the authoritative specification for DPG.
   Implementations MUST conform to every normative statement in this
   document.

Copyright Notice

   Copyright (c) 2026 Daniel Tsegaw.  All rights reserved.

   Redistribution and use of this specification, with or without
   modification, is permitted provided that the above copyright notice
   and this permission notice appear in all copies.
```

---

## Table of Contents

```
1.  Introduction
    1.1.  Purpose and Scope
    1.2.  Problem Statement
    1.3.  Prior Art
    1.4.  Core Design Tenets
    1.5.  Terminology
2.  Conventions Used in This Document
    2.1.  Requirements Notation
    2.2.  Syntax Notation
    2.3.  Examples
3.  Project Structure and Configuration
    3.1.  Directory Layout
    3.2.  Root dpg.toml
    3.3.  Cluster dpg.toml
    3.4.  Database dpg.toml
    3.5.  Cluster-Level Objects Directory
    3.6.  Discovery Algorithm
    3.7.  Block Merge Conflict Resolution
    3.8.  Minimum PostgreSQL Version Targeting
    3.9.  Ignored Objects
4.  Language Fundamentals
    4.1.  Source File Format
    4.2.  The Two-Part Syntax Model
    4.3.  The No-Verb Mandate
    4.4.  Structural Scoping
    4.5.  Statement Terminators
    4.6.  Dollar-Quoted String Parsing
    4.7.  Macro Preprocessor
    4.8.  Dual Definition Modes
    4.9.  Block Merging
    4.10. Identifiers
5.  Type System
    5.1.  ENUM Types
    5.2.  Composite Types
    5.3.  Range Types
    5.4.  Domain Types
    5.5.  Base (Shell) Types
    5.6.  Virtual Types
6.  Schema and Namespace Objects
    6.1.  SCHEMA
    6.2.  EXTENSION
7.  Tables
    7.1.  Table Declaration Syntax
    7.2.  Column Definitions
    7.3.  Constraints
    7.4.  The COLUMN Reference Block
    7.5.  Column-Level Grants
    7.6.  Column Renaming
    7.7.  Indexes
    7.8.  Row Level Security
    7.9.  Triggers
    7.10. Table-Level Grants and Revocations
    7.11. Table Lifecycle Directives
    7.12. Unlogged and Foreign Tables
    7.13. Partitioned Tables
8.  Views
    8.1.  Regular Views
    8.2.  Materialized Views
    8.3.  Recursive Views
9.  Functions and Procedures
    9.1.  Function Declaration Syntax
    9.2.  Function Attributes
    9.3.  Procedures
    9.4.  Aggregate Functions
    9.5.  Function Body Diffing Semantics
10. Sequences
11. Access Control
    11.1.  Roles
    11.2.  Grants — The Additive Model
    11.3.  Revocations
    11.4.  Default Privileges
    11.5.  Owner Impersonation at Object Creation
    11.6.  Parameter Privileges
12. Full-Text Search Objects
    12.1.  Text Search Configurations
    12.2.  Text Search Dictionaries
    12.3.  Text Search Parsers
    12.4.  Text Search Templates
13. Logical Replication
    13.1.  Publications
    13.2.  Subscriptions
14. Advanced PostgreSQL Objects
    14.1.  Event Triggers
    14.2.  Collations
    14.3.  Operators
    14.4.  Operator Classes and Families
    14.5.  Casts
    14.6.  Extended Statistics Objects
    14.7.  Tablespaces
    14.8.  Foreign Data Wrappers
    14.9.  Foreign Servers
    14.10. User Mappings
    14.11. Security Labels
15. Compilation Pipeline
    15.1.  Phases Overview
    15.2.  Phase 1 — File Discovery
    15.3.  Phase 2 — Macro Preprocessing
    15.4.  Phase 3 — Tokenization
    15.5.  Phase 4a — PostgreSQL Parsing (Part 1)
    15.6.  Phase 4b — Block Parsing (Part 2)
    15.7.  Phase 5 — IR Construction
    15.8.  Phase 6 — Merging
    15.9.  Phase 7 — Dependency Resolution
    15.10. Phase 8 — Linting
    15.11. Phase 9 — Differencing
    15.12. Phase 10 — Emission
16. Snapshot Format
    16.1.  Purpose and Placement
    16.2.  Top-Level Fields
    16.3.  Per-Object Snapshot Schema
    16.4.  Versioning
    16.5.  Further Object Records
    16.6.  Cluster-Level Snapshot File
    16.7.  Source Revision Detection
17. Migration Output Format
    17.1.  Output Structure
    17.2.  Safety Classification
    17.3.  Execution Modes
    17.4.  Idempotency Requirement
18. CLI Commands
    18.1.  dpg plan
    18.2.  dpg apply
    18.3.  dpg verify
    18.4.  dpg resolve
    18.5.  dpg dump
    18.6.  dpg diff
    18.7.  dpg validate
    18.8.  dpg fmt
    18.9.  dpg portability
    18.10. dpg init
    18.11. dpg completion
    18.12. dpg docs
    18.13. Target Selection
19. The Linter
    19.1.  Built-in Rules
    19.2.  Configuration
    19.3.  Rule Scope
20. Introspection Engine
    20.1.  Catalog Tables Read
    20.2.  Drift Detection
21. Per-Object Diff Algorithms
22. Dependency Ordering
    22.1.  Topological Sort
    22.2.  Circular Dependency Resolution
23. Out of Scope
24. Security Considerations
25. Feature Coverage Matrix
Appendix A.  ABNF Grammar Summary
Appendix B.  Complete Example Project
Appendix C.  Error Code Reference
Appendix D.  Supplementary Specifications
    D.1.  Secret Resolution
    D.2.  Name Maps
    D.3.  SERIAL / BIGSERIAL / SMALLSERIAL Columns
Appendix E.  Revision History
Appendix F.  Standard SQL / PostgreSQL-Specific Classification
Normative References
Informative References
Author's Address
```

---

## 1. Introduction

### 1.1. Purpose and Scope

   This document specifies the Declarative PG (DPG) language, its
   compilation model, and all associated tooling.  DPG is a
   declarative, state-based superset of PostgreSQL SQL.  A DPG source
   tree describes the desired state of one or more PostgreSQL databases.
   The DPG compiler ingests that description, compares it against a
   committed schema snapshot, and emits the minimal ordered set of
   PostgreSQL DDL statements required to transition the database to the
   desired state.

   The scope of this specification covers:

   a)  The DPG source language syntax, including all object declaration
       forms, the two-part syntax model, the macro preprocessor, and
       all lifecycle directives.

   b)  Every category of PostgreSQL object that DPG can manage,
       including the precise DDL each declaration produces.

   c)  The full compilation pipeline from file discovery through DDL
       emission, including intermediate representations.

   d)  The snapshot interchange format and its versioning contract.

   e)  The migration output format, safety classification system, and
       idempotency guarantees.

   f)  All CLI commands, their flags, and their observable behaviour.

   g)  The static analysis linter and its built-in rules.

   h)  The live catalog introspection engine and drift detection.

   This specification does NOT cover:

   -   Any implementation's internal data structures beyond what is
       required to define observable behaviour.
   -   PostgreSQL runtime behaviour (query planning, execution, etc.).
   -   Data manipulation language (SELECT, INSERT, UPDATE, DELETE),
       except the statements a `MIGRATE REMOVE` block supplies (Section
       5.1.2).
   -   Inline data seeding (out of scope; DPG is a schema tool).

### 1.2. Problem Statement

   PostgreSQL DDL is fundamentally imperative: to change a database one
   issues commands (`CREATE TABLE`, `ALTER TABLE ADD COLUMN`,
   `DROP INDEX`).  SQL files in version control therefore describe
   *actions taken at a point in time*, not the *current intended state*
   of the schema.  This creates four well-known failure modes:

   (1) **Schema drift.**  A production database that has been patched,
       hotfixed, or manually altered over time no longer matches its
       migration history.  There is no reliable way to tell whether a
       given migration file has been applied.

   (2) **No single source of truth.**  To understand the current schema
       a developer must mentally replay every migration, in order, from
       the beginning.  This is error-prone and does not scale.

   (3) **Idempotency is illusory.**  Running a migration file twice
       fails or corrupts state.  Most teams work around this with
       `IF NOT EXISTS` guards that must be written manually.

   (4) **Redundant context.**  PostgreSQL forces re-statement of context
       that is already structurally known.  `ALTER TABLE public.users
       ADD CONSTRAINT ...` repeats schema and table name in every
       alteration even when the context is fixed.

   DPG resolves all four problems by inverting the model: the developer
   writes a *description of the desired state*; the compiler produces
   the imperative commands.

### 1.3. Prior Art

   **Migration-history tools (Flyway, Liquibase, Sqitch, and the
   migration systems of most ORMs)** store the sequence of imperative
   changes, not the desired state.  The current schema exists only as
   the replay of every migration.

   **Tools with their own schema language (Atlas HCL, Prisma Schema)**
   describe the desired state in a vocabulary that is not SQL, so
   objects PostgreSQL already names have to be relearned, and
   PostgreSQL features the vocabulary does not model cannot be
   expressed.

   **Plain-SQL desired-state tools (psqldef, migra, pg-schema-diff,
   Atlas's SQL schema mode, Supabase declarative schemas)** take a set
   of `CREATE` statements as the desired state and diff it against a
   database.  This keeps SQL, but three gaps remain:

   1.  Properties PostgreSQL only sets through separate statements
       (owner, grants, comments, row level security, column statistics,
       replica identity) have no declarative home.  They are written as
       `ALTER`/`GRANT`/`COMMENT` statements, which the tool must then
       interpret as state.
   2.  Intent that the end state cannot show (a rename, a deprecation,
       protection from drops, migrating rows off an enum value) cannot
       be expressed, so the tool guesses or drops and recreates.
   3.  Several compute the diff by loading the SQL into a scratch
       database, so planning needs a running PostgreSQL.

   **The DPG position:** PostgreSQL's `CREATE` statements already
   describe objects well.  DPG keeps them as written, minus the verb,
   and adds the three missing pieces: a `{ }` block that holds the
   properties PostgreSQL sets separately, lifecycle directives for
   intent, and an offline diff against a committed snapshot.

### 1.4. Core Design Tenets

   **Tenet 1 — Full PostgreSQL feature parity.**
   DPG MUST be capable of expressing anything that raw PostgreSQL DDL
   can express.  A PostgreSQL feature that cannot be declared in DPG
   is a defect in DPG, not an out-of-scope request — with the sole
   exception of the specific, itemized carve-outs enumerated in
   Section 23 ("Out of Scope"), each justified there on its own
   terms (e.g. a feature that is runtime/session-scoped rather than
   schema DDL, or genuinely superseded by another PostgreSQL mechanism
   DPG already manages).  Section 23's list is closed, not a general
   license to omit — anything not named there is bound by this tenet
   without exception.

   **Tenet 2 — Prefer PG syntax exactly.**
   When PostgreSQL already has a declarative way to express something,
   DPG uses it verbatim.  DPG removes imperative verbs and adds
   structural scoping but does not invent new keywords for concepts
   PostgreSQL already names well.

   **Tenet 3 — Standard SQL / PG-extension boundary is tracked
   internally.**
   The compiler knows which constructs are ISO/IEC 9075 Standard SQL
   and which are PostgreSQL-specific.  Users never annotate portability.
   The compiler surfaces this via the `dpg portability` command.  The
   full per-construct classification is Appendix F.

   **PostgreSQL versions:** this version of the specification covers
   **PostgreSQL 14 through 18**.  A construct requiring a version after
   14 is marked as such where it is specified and in Appendix F.
   Constructs introduced by later PostgreSQL releases are added by
   amendments to this specification (see Versioning, below).  This
   range is distinct from the per-project `min_pg_version` setting
   (Section 3.8), which declares the oldest server a particular project
   deploys to.

   **Versioning:** this specification is versioned `MAJOR.MINOR.PATCH`.
   An amendment that only adds (support for a new PostgreSQL release's
   constructs, a new optional directive, a new error code) increases
   MINOR.  An amendment that changes the meaning of valid source, or
   removes or renames a keyword, increases MAJOR.  Editorial
   corrections that change no requirement increase PATCH.

   **Tenet 4 — Offline-first diffing.**
   DPG MUST NOT require a live database connection to generate a
   migration.  The primary workflow compares `.dpg` source files against
   a committed schema snapshot.  Live catalog introspection is available
   for verification and bootstrap but is never required for day-to-day
   operation.

   **Tenet 5 — The `{ }` block holds only what PG SQL cannot.**
   The native PostgreSQL DDL definition of an object — its column list,
   its options, its clauses, its dollar-quoted body — MUST be written
   exactly as PostgreSQL SQL dictates.  The trailing `{ }` block exists
   exclusively for things PostgreSQL SQL expresses as separate DDL
   statements (indexes, grants, policies, comments, per-column storage
   attributes) and for DPG lifecycle directives (`RENAMED FROM`,
   `PROTECTED`, `DEPRECATED`).  Nothing that has a natural place in
   PostgreSQL SQL's own syntax SHALL be moved into the `{ }` block.

### 1.5. Terminology

   The following terms carry precise meanings throughout this document:

   **DPG source file** — A UTF-8 text file with the `.dpg` extension
   containing one or more DPG object declarations.

   **Part 1** — The native PostgreSQL SQL portion of a DPG declaration,
   written with the leading imperative verb removed.

   **Part 2** — The optional trailing `{ }` block of a DPG declaration,
   holding sub-objects and lifecycle directives.

   **Object** — A named, independently manageable PostgreSQL schema
   entity (table, view, function, type, role, etc.).

   **Snapshot** — A committed JSON file representing the compiler's
   normalised view of the database state after the most recent
   successful `dpg apply`.

   **IR** (Internal Representation) — The fully-qualified, typed
   in-memory form of a DPG object, produced by the IR Builder phase.

   **DiffOp** — A single DDL statement produced by the Differ,
   annotated with safety class and source position.

   **Migration** — The complete ordered output of the Emitter: a set of
   transactional DiffOps and a set of non-transactional DiffOps,
   together with a header block.

   **Safety class** — One of `SAFE`, `CAUTION`, or `DESTRUCTIVE`.
   Defined in Section 17.2.

   **Execution mode** — One of transactional, non-transactional, or
   instruction-only.  Independent of the safety class.  Defined in
   Section 17.3.

   **Cluster** — A single running PostgreSQL instance, hosting one or
   more databases.  Maps to one cluster directory in the project tree.

   **Database** — A single named PostgreSQL database within a cluster.
   Maps to one database directory in the project tree.

---

## 2. Conventions Used in This Document

### 2.1. Requirements Notation

   The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
   "SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and
   "OPTIONAL" in this document are to be interpreted as described in
   BCP 14 [RFC2119] [RFC8174] when, and only when, they appear in all
   capitals, as shown here.

### 2.2. Syntax Notation

   ABNF grammar rules are specified using the notation defined in
   [RFC5234], Augmented BNF for Syntax Specifications.  The following
   core rules from [RFC5234] Appendix B are used without redefinition:
   `ALPHA`, `DIGIT`, `SP`, `HTAB`, `CRLF`, `LF`, `DQUOTE`.

   Where existing PostgreSQL DDL syntax is referenced, it is cited in
   terms of the PostgreSQL 14+ `CREATE` statement grammar as documented
   in the PostgreSQL official documentation [PGDOC14].

   Inline examples appear in monospace code blocks.  Within prose, DPG
   keywords appear in `monospace`.  Non-terminal grammar symbols appear
   in *italics*.  Normative text and examples are separated; an example
   does not in itself constitute a normative requirement unless
   explicitly stated.

### 2.3. Examples

   Examples marked `-- OK` illustrate valid DPG source.  Examples
   marked `-- ERROR` illustrate input that MUST be rejected by the
   compiler with a diagnostic.  Example PostgreSQL DDL output is marked
   `-- emitted SQL`.

---

## 3. Project Structure and Configuration

### 3.1. Directory Layout

   [DPG] A DPG project is a directory tree.  Three kinds of `dpg.toml`
   file mark its structure, each declaring its kind on a top-level
   `kind` key:

   -   `kind = "root"`: the project root.  Exactly one, at the top of
       the tree.
   -   `kind = "cluster"`: a cluster directory (one PostgreSQL server,
       Section 1.5).
   -   `kind = "database"`: a database directory (one database within a
       cluster).

   Cluster and database directories MAY sit at any depth below the
   root.  A typical layout:

```
<project-root>/
├── dpg.toml                        kind = "root"
│
├── <cluster>/                      (any depth below the root)
│   ├── dpg.toml                    kind = "cluster"
│   ├── <cluster-objects-dir>/      (default: "cluster")
│   │   ├── roles.dpg
│   │   └── tablespaces.dpg
│   │
│   └── <database>/                 (any depth below its cluster)
│       ├── dpg.toml                kind = "database"
│       ├── extensions.dpg
│       └── schemas/
│           └── <schema>/
│               ├── schema.dpg      (optional: the schema's own directives)
│               ├── types.dpg
│               ├── tables/
│               │   └── <table>.dpg
│               ├── views.dpg
│               └── functions.dpg
│
└── .dpg/
    ├── snapshots/<cluster>/<database>.json
    └── migrations/<cluster>/<database>/
```

   **Structure rules:**

   1.  The project root is the directory whose `dpg.toml` has
       `kind = "root"`.  A `dpg.toml` with no `kind`, or with any other
       value, is error DPG-E041; so is `kind = "root"` anywhere below
       the root.

   2.  A database belongs to one cluster: the cluster directory named by
       its `parent` key (Section 3.4) when set, otherwise its nearest
       ancestor directory with `kind = "cluster"`.  A database with
       neither is error DPG-E042.  A database therefore MAY live outside
       its cluster's directory, for example in a shared `databases/`
       folder, by naming the cluster with `parent`.

   3.  A cluster directory MUST NOT be inside another cluster or
       database directory, and a database directory MUST NOT be inside
       another database directory (error DPG-E043).

   4.  All `.dpg` files at any depth under a database directory belong
       to that database.  No in-file database header keyword exists.

   5.  `.dpg` files under a cluster's objects directory (Section 3.5)
       belong to the cluster, with no database context.

   6.  Files whose names do not end in `.dpg`, and directories whose
       names begin with `.`, are ignored.

   7.  [DPG] `.dpg` files under `<database>/schemas/<name>/`, at any
       depth, are scoped to schema `<name>`: an unqualified declaration
       there belongs to that schema, exactly as if it were written inside
       `SCHEMA <name> { }`.  The directory name is used verbatim as the
       schema name, with no case folding.  A file in that directory MAY
       declare `SCHEMA <name> { ... }` to hold the schema's own
       directives (`OWNER`, `COMMENT`, `GRANTS`/`REVOKES`, default
       privileges, `RENAMED FROM`); by convention this is
       `schemas/<name>/schema.dpg`.  Declaring any other schema there is
       error DPG-E040.  The schema itself exists in the desired state
       whenever its directory contains at least one `.dpg` file.

   Reason for typed configs: a directory's role is stated, not guessed
   from which sections its `dpg.toml` happens to contain, so a project
   can be organised to any depth and a misplaced file is reported
   instead of silently changing the project's structure.

### 3.2. Root dpg.toml

   The root `dpg.toml` configures global compiler and linter behaviour.
   `kind` is REQUIRED; every other field is OPTIONAL and takes the
   default shown.

```toml
kind = "root"

[compiler]
# default_drop_behavior controls whether DROP statements include
# CASCADE or RESTRICT. Valid values: "restrict" (default), "cascade".
# Per-object DROP CASCADE overrides this setting.
default_drop_behavior = "restrict"

# Minimum PostgreSQL major version this project targets (Section 3.8).
# Unset (the default) means no version gating. Overridable per-cluster
# and per-database (Sections 3.3/3.4); most specific wins.
# min_pg_version = 15

[linter]
# Emit a warning when any DEPRECATED object or column is referenced.
warn_on_deprecated = true

# Emit an error when any column lacks a COMMENT.
require_column_comments = false

# Emit an error when a ROLE PASSWORD value is a literal rather than a
# {{secret-uri}} placeholder.
forbid_hardcoded_passwords = true

# Emit a warning when a table has more than this many columns.
# 0 = disabled.
max_columns_per_table = 50

[snapshots]
# Directory where snapshot JSON files are stored, relative to the
# project root.
directory = ".dpg/snapshots"

[fmt]
# Number of spaces per indentation level. Default: 4.
indent = 4

# Keyword casing applied to all DPG and PostgreSQL keywords.
# Valid values: "upper" (default), "lower".
keyword_case = "upper"

[migrations]
# Relative path from the project root where applied migration SQL
# files are archived after a successful `dpg apply`. Default:
# ".dpg/migrations". Set to "" to disable archiving entirely.
directory = ".dpg/migrations"

[ignore]
# Objects that exist in a database but are not managed by DPG
# (Section 3.9). OPTIONAL; allowed in every dpg.toml.
# extensions = ["pg_stat_statements"]
```

   The compiler MUST reject any key in any `dpg.toml` that is not
   listed in this section, Section 3.3, or Section 3.4 for that file's
   kind, with error DPG-E001 (unknown configuration key).

   `[fmt]` controls `dpg fmt` (Section 18.8).  `[migrations]` controls where
   `dpg apply` (Section 18.2) archives applied SQL, one file per successful
   apply, at `<directory>/<cluster>/<database>/<timestamp>_<short-hash>.sql`;
   this directory SHOULD be committed to version control.

### 3.3. Cluster dpg.toml

   Located in a cluster directory.  Configures the cluster's name and
   connection.

```toml
kind = "cluster"

[cluster]
# Human-readable name. Used in snapshot file paths and migration
# headers. REQUIRED.
name = "production"

# Directory, relative to this cluster directory, holding cluster-level
# objects (Section 3.5).
cluster_objects_dir = "cluster"   # default

# Inline PostgreSQL connection string (libpq URI or keyword/value
# format). Mutually exclusive with `link`. OPTIONAL.
url = "postgresql://user@host:5432/postgres"

# Secrets-provider URI resolved at connection time. Supported schemes
# (see Appendix D.1): env:<VAR>, vault:<mount>/<path>#<field>, aws-sm:<secret-id>,
# gcp-sm:<project>/<secret-id>, azure-kv:<vault-name>/<secret-name>.
# Mutually exclusive with `url`. OPTIONAL.
# link = "env:PRIMARY_DB_URL"

[compiler]
# Overrides the project root's min_pg_version (Section 3.2) for
# every database in this cluster, and for this cluster's own
# cluster-level objects (roles, tablespaces, PARAMETER PRIVILEGES).
# OPTIONAL; unset falls through to the root's value. See Section 3.8.
# min_pg_version = 16

[ignore]
# Objects this environment has that DPG does not manage, such as a
# hosting platform's roles (Section 3.9). OPTIONAL.
# roles = ["rdsadmin", "rds_*"]
```

   [DPG] **Connections.**  `url` and `link` are both OPTIONAL: they are
   needed only by commands that connect (`dpg apply`, `dpg verify`,
   `dpg dump`, `dpg plan --live`).  The cluster connection is used:

   -   as written, for cluster-level objects (Section 3.5); its own
       database (for example `postgres`) is the database those commands
       connect to;
   -   for each database that sets no connection of its own (Section
       3.4), with the connection's database name replaced by that
       database's `name`.

   **Constraints:** `url` and `link` are mutually exclusive (error
   DPG-E002).  A command that needs a connection and finds none for its
   target fails with error DPG-E003.  `name` is required (error
   DPG-E032) and MUST be unique across every cluster in the project
   (error DPG-E034, naming both directories).

### 3.4. Database dpg.toml

   Located in a database directory.

```toml
kind = "database"

# Path, relative to this directory, of the cluster directory this
# database belongs to. OPTIONAL; default: the nearest ancestor
# directory whose dpg.toml has kind = "cluster" (Section 3.1).
# parent = "../../clusters/production"

[database]
# The name of the PostgreSQL database as it appears in pg_database.
# REQUIRED.
name = "myapp"

# The schema for objects declared with no schema qualifier outside any
# schema scope (Section 4.4). OPTIONAL; default "public".
default_schema = "public"

# This database's own connection, replacing the cluster's for this
# database only. Used verbatim: the database name in it is not
# rewritten. Same forms as the cluster's url/link (Section 3.3).
# Mutually exclusive. OPTIONAL.
# url  = "postgresql://myapp_owner@pgbouncer:6432/myapp_pool"
# link = "env:MYAPP_DB_URL"

[compiler]
# Overrides the cluster's (and, transitively, the project root's)
# min_pg_version (Sections 3.2/3.3) for this database only. OPTIONAL;
# unset falls through to the cluster's value. See Section 3.8.
# min_pg_version = 17

[ignore]
# Objects in this database that DPG does not manage (Section 3.9).
# OPTIONAL.
# schemas = ["cron"]
```

   [DPG] `parent` MUST resolve inside the project root to a directory
   whose `dpg.toml` has `kind = "cluster"`.  When the database
   directory is itself inside a cluster directory, `parent` MAY only
   name that same cluster.  Any other value is error DPG-E042.

   [DPG] A database-level `url`/`link` serves cases a single cluster
   connection cannot: a different role per database, or a connection
   pooler whose pool name differs from the database name.

   **Constraints:** `url` and `link` are mutually exclusive (error
   DPG-E002).  `name` is required (error DPG-E033) and MUST be unique
   among the databases of the same cluster (error DPG-E035, naming both
   directories); the same name MAY recur under a different cluster.

### 3.5. Cluster-Level Objects Directory

   Files in the cluster objects directory declare objects that belong to
   the cluster, not to any individual database: roles, tablespaces,
   parameter privileges, and (in the rare case of custom C-implemented
   FDWs) foreign data wrappers.  The directory is
   `<cluster-dir>/<cluster_objects_dir>`.  A database directory at or
   below it is error DPG-E004 (reserved name conflict).

### 3.6. Discovery Algorithm

   File discovery MUST produce the result of the following steps:

   1.  Locate the project root: starting at the current working
       directory and moving upward, the first directory whose
       `dpg.toml` has `kind = "root"`.  A `dpg.toml` met on the way with
       no `kind` is error DPG-E041.

   2.  Walk the whole tree below the root, skipping directories whose
       names begin with `.`, and read the `kind` of every `dpg.toml`
       found (Section 3.1, rule 1).

   3.  Load every cluster (Section 3.3), checking rule 3 of Section 3.1.

   4.  Load every database (Section 3.4), checking rule 3, and attach it
       to its cluster (rule 2).

   5.  For each database, collect every `.dpg` file under its directory;
       for each cluster, collect every `.dpg` file under its objects
       directory.  The compiled result never depends on the order of
       these files (Section 3.7).

   6.  Validate names: cluster names non-empty and unique project-wide
       (DPG-E032, DPG-E034); database names non-empty and unique within
       their cluster (DPG-E033, DPG-E035).  Reason: `--cluster` and
       `--database` selection (Section 18.13), snapshot files, the
       migrations archive, and `dpg dump`'s output path are all keyed by
       name, so an empty or repeated name would make one target
       unreachable or make two targets share files.

### 3.7. Block Merge Conflict Resolution

   [DPG] All declarations across all `.dpg` files for the same logical
   database are compiled together.  When the same named object is
   declared in more than one file, the declarations are merged:

   **Set-valued properties** — columns, constraints, indexes, policies,
   triggers, grants, revokes, partitions, role memberships, enum
   values — are merged by taking the union of all declared entries.
   Identical entries (same name and same definition) are deduplicated.
   Two entries with the same name but different definitions are error
   DPG-E005 (conflicting set member).

   **Scalar properties** — every other property: owner, comment,
   tablespace, `RENAMED FROM`, `DEPRECATED`, a column's type or default,
   a sequence option, and so on — may be declared in any number of the
   files, but every declaration MUST give the same value.  A property
   declared in only one file is taken from that file.  Two different
   values for the same property are error DPG-E039 (conflicting scalar
   property); the error names both files.  Option lists (`OPTIONS`,
   `WITH (...)`) are compared key by key, so files may set different
   keys.  Presence flags (`PROTECTED`, `DROP CASCADE`, `ENABLE ROW LEVEL
   SECURITY`, ...) are set if any declaration sets them.

   Object kinds with no per-property merge (publications, event
   triggers, casts, operators, text search objects, and similar) MAY be
   declared in more than one file only if every declaration is
   identical; otherwise the compiler reports DPG-E039.

   Reason: the compiled result must not depend on file names, file
   order, or how source is split across files.

### 3.8. Minimum PostgreSQL Version Targeting

   `min_pg_version` (`[compiler]`, Sections 3.2-3.4) declares the oldest
   PostgreSQL major version a project — or one of its clusters or
   databases — is deployed against.  This is a purely *semantic* gate,
   not a parser restriction: the compiler always parses `.dpg` source
   against its newest supported grammar, since PostgreSQL's own SQL
   grammar is overwhelmingly additive across major versions and a
   project's source is not expected to vary its own syntax by target
   version.  What `min_pg_version` controls is whether the linter (Section
   19) warns — or, under `--strict`, errors — when a construct the
   compiler *did* successfully parse requires a PostgreSQL version newer
   than the effective floor.  This lets a project declare "PARAMETER
   PRIVILEGES on a PG14 target" and be told about the mismatch before
   `dpg apply` fails against a real, older server, without DPG needing a
   distinct parser build per PostgreSQL release.

   **Resolution order:** `min_pg_version` MAY be set at the project root
   (Section 3.2), overridden per-cluster (Section 3.3), and overridden
   again per-database (Section 3.4).  The most specific level that sets
   it wins; an unset level falls through to the next level up.
   Unset at every level means no gating at all.  A cluster's own
   cluster-level objects (roles, tablespaces, `PARAMETER PRIVILEGES`)
   are gated by that cluster's own resolved value, since they have no
   database of their own to resolve one from.

   **Validation:** a declared `min_pg_version` below 14 (this
   specification's own supported floor, Section 1.4) MUST be rejected
   at config-load time as an error, at whichever level declared it.

   **Enforcement:** implemented as the linter's `min-pg-version` rule
   (Section 19.1, Section 19.3) — a `LintDiagnostic` warning by default,
   promoted to an error under `--strict` like any other rule, wired into
   `dpg plan`/`dpg apply`/`dpg validate` (Section 18) the same way every
   other linter rule already is; no separate CLI flag or command-specific
   wiring is needed.  Every version-gated construct this specification
   documents is catalogued in Appendix F's `Min PG Version` column — a
   blank cell there means the construct has been available since this
   specification's own floor (PostgreSQL 14) and is never gated,
   regardless of `min_pg_version`.

   **Coverage:** the rule MUST check every declared construct that has
   a value in Appendix F's `Min PG Version` column, except rows marked
   *(on change)*.  Those are statements DPG emits only to apply a
   particular change to an object that already exists (for example
   `ALTER COLUMN ... SET EXPRESSION`); nothing in the source shows them,
   so PostgreSQL itself rejects them when the migration runs against an
   older server.  SQL inside expressions and bodies (view queries,
   function bodies, `DEFAULT` and `CHECK` expressions) is not checked.

---

### 3.9. Ignored Objects

   [DPG] The `[ignore]` section of a `dpg.toml` lists name patterns of
   objects that exist in a database but are not managed by DPG, such as
   the roles, schemas and extensions a hosting platform creates.  The
   lists of the project root, the cluster and the database apply
   together, so patterns for one environment belong in that cluster's
   `dpg.toml`.

```toml
[ignore]
roles      = ["rdsadmin", "rds_*"]
schemas    = ["cron"]
extensions = ["pg_stat_statements"]
functions  = ["public.pgaudit_*"]
```

   Each key names an object kind; its value is a list of patterns, in
   which `*` matches any run of characters and `?` any one character.
   For a schema-scoped kind, a pattern containing `.` matches
   `schema.name` and any other pattern matches the name in every
   schema.  An object in an ignored schema is ignored too.

   Keys: `roles`, `tablespaces`, `schemas`, `extensions`, `tables`
   (including foreign tables), `views` (including recursive and
   materialized views), `functions`, `procedures`, `aggregates`, `types`
   (enums, composites, ranges, domains and base types), `sequences`,
   `collations`, `operators`, `operator_classes`, `operator_families`,
   `casts` (named `source->target`), `statistics`,
   `foreign_data_wrappers`, `servers`, `user_mappings` (named
   `user@server`), `publications`, `subscriptions`, `event_triggers`,
   `text_search_configurations`, `text_search_dictionaries`,
   `text_search_parsers`, `text_search_templates`.  Any other key is
   error DPG-E001; a malformed pattern is a configuration error.

   An ignored object is removed from the live catalog and from the
   snapshot before every comparison (`dpg plan`, `dpg apply`,
   `dpg verify`), and `dpg dump` does not write it.  Declaring an object
   that matches a pattern is error DPG-E045.  Predefined roles (`pg_*`)
   and the bootstrap superuser are never read from the catalog (Section
   20.1) and need no pattern.

## 4. Language Fundamentals

### 4.1. Source File Format

   DPG source files MUST be encoded in UTF-8 [RFC3629].  Byte-order
   marks (U+FEFF) MUST be silently stripped if present at the start of
   a file.  Line endings MAY be LF (U+000A) or CRLF (U+000D U+000A);
   the compiler MUST normalise all line endings to LF before processing.

   Comments follow PostgreSQL's double-dash convention (`--`) and C-
   style block comments (`/* ... */`).  Comments are stripped by the
   tokenizer before any deeper parsing.  Block comments do NOT nest.

   Identifiers are case-insensitive in conformance with PostgreSQL's
   unquoted identifier rules, except when enclosed in double-quotes,
   in which case they are case-sensitive and may contain any Unicode
   character.

### 4.2. The Two-Part Syntax Model

   Every DPG object declaration consists of at most two parts:

   **Part 1 — The native PG SQL definition.**
   Written exactly as PostgreSQL SQL dictates with only the leading
   imperative verb (CREATE, ALTER, DROP) removed.  Part 1 uses the
   same keywords, the same clause ordering, and the same syntax as the
   corresponding PostgreSQL `CREATE` statement.  The compiler prepends
   the correct verb internally when invoking the PostgreSQL parser; the
   developer never writes it.

   **Part 2 — The DPG structural block `{ ... }`.**
   An OPTIONAL trailing block that contains exclusively things
   PostgreSQL SQL expresses as *separate* DDL statements (`CREATE INDEX`,
   `GRANT`, `COMMENT ON`, `ALTER TABLE ... ALTER COLUMN SET STATISTICS`)
   plus DPG lifecycle directives (`RENAMED FROM`, `PROTECTED`,
   `DEPRECATED`, `DROP CASCADE`).

   **Decision rule:** If PostgreSQL writes it as part of `CREATE OBJECT`,
   it is Part 1.  If PostgreSQL writes it as a separate statement, it
   is Part 2.  This rule MUST be applied consistently; no clause that
   belongs in Part 1 SHALL be moved to Part 2, and no Part-2-only
   directive SHALL appear in Part 1.

```abnf
dpg-declaration  = object-keyword WSP object-body terminator
                 ; object-body is the PG SQL text with CREATE verb
                 ; absent; terminator is defined in Section 4.5

part2            = "{" *( WSP* block-item ) WSP* "}"
block-item       = block-directive WSP* ";"     ; e.g. COMMENT '...';
                 / block-collection              ; e.g. GRANTS { ... }
                 ; a block-collection ends at its own "}"; a ";" after
                 ; it is permitted and never required

object-keyword   = "SCHEMA" / "TABLE" / "UNLOGGED TABLE" /
                   "FOREIGN TABLE" / "VIEW" / "MATERIALIZED VIEW" /
                   "RECURSIVE VIEW" / "FUNCTION" / "PROCEDURE" /
                   "AGGREGATE" / "TYPE" / "DOMAIN" /
                   "SEQUENCE" / "ROLE" / "TABLESPACE" /
                   "FOREIGN DATA WRAPPER" / "SERVER" / "USER MAPPING" /
                   "PUBLICATION" / "SUBSCRIPTION" / "EVENT TRIGGER" /
                   "COLLATION" / "OPERATOR" / "OPERATOR CLASS" /
                   "OPERATOR FAMILY" / "CAST" / "STATISTICS" /
                   "TEXT SEARCH CONFIGURATION" /
                   "TEXT SEARCH DICTIONARY" /
                   "TEXT SEARCH PARSER" / "TEXT SEARCH TEMPLATE" /
                   "DEFAULT PRIVILEGES" / "PARAMETER PRIVILEGES" /
                   "VIRTUAL TYPE" / "EXTENSION" / "MACRO"
```

#### 4.2.1. Common Part 2 Directives

   These directives have the same meaning in every `{ }` block that
   accepts them.  Each object section lists which ones it accepts.  A
   directive is tagged `[PG: ...]` when it declares state that
   PostgreSQL sets with the separate statement named, and `[DPG]` when
   PostgreSQL has no such state (it is information for the compiler).

   | Directive | Origin | Meaning |
   |-----------|--------|---------|
   | `OWNER role` | [PG: `ALTER <kind> ... OWNER TO`] | The object's owner.  The object is also created as that role (Section 11.5). |
   | `COMMENT 'text'` | [PG: `COMMENT ON <kind> ...`] | The object's comment. |
   | `GRANTS { ... }` | [PG: `GRANT ... ON <kind> ...`] | Privileges granted on the object (Sections 7.10, 11.2). |
   | `REVOKES { ... }` | [PG: `REVOKE ... ON <kind> ...`] | Privileges revoked on the object (Section 11.3). |
   | `SECURITY LABEL [FOR provider] 'label'` | [PG: `SECURITY LABEL ... ON <kind> ...`] | A security label; one per provider (Section 14.11). |
   | `DEPENDS ON EXTENSION ext` | [PG: `ALTER <kind> ... DEPENDS ON EXTENSION`] | Marks the object as dropped with the extension. |
   | `RENAMED FROM [schema.]old` | [DPG] | The object's previous name; produces a rename (and `SET SCHEMA` for a schema change) instead of drop and create (Section 7.6). |
   | `DEPRECATED 'msg'` | [DPG] | Marks the object deprecated; its comment gets a `[DEPRECATED]` prefix and the linter warns on use (Section 19). |
   | `PROTECTED` | [DPG] | The compiler refuses to drop the object (DPG-E022). |
   | `DROP CASCADE` | [DPG] | Drops the object with `CASCADE`, overriding `default_drop_behavior`. |
   | `NAME MAP ...` / `NAME MAPS { ... }` | [DPG] | Naming rules for downstream tools (Appendix D.2). |

```abnf
owner-dir        = "OWNER" WSP identifier
comment-dir      = "COMMENT" WSP string-literal
renamed-from-dir = "RENAMED FROM" WSP qual-name
deprecated-dir   = "DEPRECATED" WSP string-literal
protected-dir    = "PROTECTED"
drop-cascade-dir = "DROP CASCADE"
```

   `grants-block` and `revocations-block` are defined in Section 7.10,
   `security-label-decl` in Section 14.11, and the Name Map directives
   in Appendix D.2.3.

#### 4.2.2. Object Section Layout

   Sections 5 to 14 describe each object kind in the same order:

   1.  **PG equivalent:** the PostgreSQL statement Part 1 corresponds
       to, linked to its PostgreSQL reference page.
   2.  **Part 1:** PostgreSQL's grammar for that statement, without the
       leading verb, for the PostgreSQL versions this specification
       targets (Section 1.4).  It is defined by reference and not
       repeated here.  ABNF appears only for what DPG adds to Part 1.
   3.  **Part 2:** the common directives the block accepts (Section
       4.2.1), then the kind's own directives, each tagged `[DPG]` or
       `[PG: ...]`, with ABNF for the block.
   4.  **Diffing semantics:** the statements DPG emits for each change,
       and their safety class (Section 17.2).
   5.  **Notes:** DPG-specific rules.  PostgreSQL's own meaning of a
       clause is not restated; where a note concerns PostgreSQL
       behaviour that changes what DPG does, it is tagged `[PG]`.

### 4.3. The No-Verb Mandate

   The keywords `CREATE`, `ALTER`, and `DROP` are PROHIBITED in DPG
   source files at the declaration level.  The compiler MUST reject any
   source file containing these keywords outside of:

   a)  Dollar-quoted function, procedure, or aggregate bodies
       (`AS $$...$$`, `AS $tag$...$tag$`), which are opaque text and
       not interpreted by the compiler (Section 4.6).

   b)  `MIGRATE REMOVE { ... }` blocks on enum types (Section 5.1),
       whose body is DML passthrough.

   Rationale: the presence of imperative verbs indicates a migration
   file rather than a state description.  Prohibiting them enforces the
   declarative contract at parse time, producing an early diagnostic
   (DPG-E006) rather than silent misuse.

### 4.4. Structural Scoping

   The `{ }` block of any container provides a scope.  Nested object
   declarations inherit their containing context:

   -   A `TABLE` declared inside a `SCHEMA { }` block inherits the
       schema name; the developer does not repeat it.

   -   An `INDEX` declared inside a `TABLE`'s `{ }` block inherits the
       table and schema; the developer writes only the index name and
       column list.

   -   A `POLICY` declared inside a `TABLE`'s `POLICIES { }` block
       inherits the table, schema, and database.

   Schemas have no `( )` list.  Their `{ }` block directly holds all
   schema-level attributes and nested objects.

   [DPG] **Explicit schema qualification.**  Outside any schema scope, a
   declaration MAY qualify its own name with any schema.  Inside a
   schema scope (a `SCHEMA { }` block or a `schemas/<name>/` directory,
   Section 3.1), a qualifier on the declared object's own name MUST name
   that same schema; any other schema is error DPG-E040.  This applies
   only to the name being declared: references (`REFERENCES other.t`,
   function bodies) and `RENAMED FROM other_schema.old` (Section 7.6) are
   unaffected.  Reason: a declaration's schema is decided by where it is
   written, so a qualifier may confirm the scope but never contradict it.

   An unqualified declaration outside any schema scope belongs to the
   database's `default_schema` (Section 3.4).

### 4.5. Statement Terminators

   [DPG] One rule governs how every declaration ends:

   1.  Part 1 ends at its own last token, whatever that is (see the
       table below).
   2.  If a `{ }` block follows, it begins immediately after Part 1:
       a `;` between Part 1 and `{` is error DPG-E037.
   3.  If no `{ }` block follows, Part 1 MUST end with `;` (error
       DPG-E038).
   4.  A `;` immediately after a block's closing `}` is permitted and
       never required.

   The single exception is `SCHEMA` (Section 6.1): it has no Part 1
   terminator, because its body is always its `{ }` block.

```abnf
terminator = ";" / WSP* part2 [ WSP* ";" ]
```

   For reference, Part 1's last token by kind (informative):

   | Part 1 ends with | Kinds |
   |------------------|-------|
   | `)`, or the last of `WITH`/`TABLESPACE`/`INHERITS`/`PARTITION BY`/`SERVER ... OPTIONS (...)` | tables, composite, enum, range and base types, aggregates |
   | the closing `$$`/`$tag$` | functions and procedures with a dollar-quoted body |
   | `END` | `BEGIN ATOMIC ... END` bodies (Section 9.1) |
   | the last token of the declaration text | every other kind (views, domains, sequences, roles, extensions, ...) |

   Reason: one rule with no per-kind exceptions, so a reader and an
   implementation never need to know a declaration's kind to know how
   it ends.

   Examples:

```sql
-- table with a { } block: no ';' before '{'
TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY,
    CONSTRAINT pk PRIMARY KEY (id)
) {
    INDEXES { idx_email (email); }
}

-- view with a { } block
VIEW active_users AS SELECT id FROM users WHERE active {
    GRANTS { SELECT TO app_readonly; }
}

-- function with a { } block: '{' directly after the closing $$
FUNCTION foo() RETURNS TEXT LANGUAGE sql STABLE
AS $$
    SELECT 'hello';
$$ {
    COMMENT 'Returns hello';
}

-- no { } block: ';' required
TABLE log (
    id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    msg  TEXT NOT NULL
);
```

### 4.6. Dollar-Quoted String Parsing

   Dollar-quoted strings use the PostgreSQL syntax `$$...$$` or
   `$tag$...$tag$` where *tag* is any identifier string (including the
   empty string).

   The compiler's dollar-quote parser MUST implement the following
   algorithm:

   1.  On encountering the token `AS` followed by optional whitespace
       followed by a dollar-quoted delimiter `$[tag]$`, record the
       opening delimiter string `D`.

   2.  Scan forward byte-by-byte without interpreting any content.
       No brace counting, no keyword scanning, no SQL parsing occurs
       inside the dollar-quoted region.  Embedded `{`, `}`, `;`,
       `CREATE`, `ALTER`, `DROP`, or any other DPG keyword are treated
       as plain text.

   3.  The first occurrence of the exact byte sequence `D` (the same
       opening delimiter) encountered during the scan terminates the
       dollar-quoted region.  Partial matches do NOT terminate.

   4.  The bytes immediately following the closing `D` are `;` when no
       `{ }` block follows, or the block's opening `{` when one does
       (Section 4.5).

   5.  Named dollar-quoting (`$body$`, `$func$`, `$sql$`, or any
       `$identifier$`) is fully supported.  The opening and closing tag
       MUST match exactly, including case.

   This algorithm allows function bodies to contain any content
   including embedded SQL DML, PL/pgSQL blocks, Python, Perl,
   JavaScript, nested dollar-quoted strings, or arbitrary binary data
   encoded as text — without any escaping or modification.

### 4.7. Macro Preprocessor

   The macro preprocessor runs as the first phase of compilation,
   before any parsing.  Macros are source-level text fragments that are
   expanded inline at points of use.

#### 4.7.1. Macro Declaration

   A macro declaration uses the `MACRO` keyword and has one of two
   body forms.

```abnf
macro-decl  = "MACRO" WSP identifier WSP paren-body
            / "MACRO" WSP identifier WSP brace-body

paren-body  = "(" *( column-def "," ) column-def ")"
brace-body  = "{" *( block-directive ";" ) "}"
```

   -   A **paren-body** macro contains a comma-separated list of column
       definitions, exactly as they would appear inside a `TABLE ( )`
       list.  The opening `(` and closing `)` are part of the body and
       are stripped when the macro is expanded.

   -   A **brace-body** macro contains zero or more block directives,
       exactly as they would appear inside a `{ }` block.  The opening
       `{` and closing `}` are part of the body and are stripped when
       the macro is expanded.

   A `MACRO` declaration generates no SQL whatsoever.  It MUST appear
   at the top level of a `.dpg` file (not inside any `{ }` or `( )`
   block).  A `MACRO` declaration inside a block is a compiler error
   (DPG-E007).

   Examples:

```sql
MACRO common_timestamps (
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ
)

MACRO audit_block {
    OWNER app_admin;
    ENABLE ROW LEVEL SECURITY;
}
```

#### 4.7.2. Macro Spread

   The spread operator `...name` expands a macro inline at the point
   of use.

```abnf
spread = "..." identifier
```

   A paren-body macro MUST only be spread inside a `( )` list.
   Spreading it inside a `{ }` block is a compiler error (DPG-E008).

   A brace-body macro MUST only be spread inside a `{ }` block.
   Spreading it inside a `( )` list is a compiler error (DPG-E009).

   Spreading an undefined macro name is a compiler error (DPG-E010).

   Example:

```sql
TABLE accounts (
    id         UUID NOT NULL DEFAULT gen_random_uuid(),
    ...common_timestamps,
    CONSTRAINT pk_accounts PRIMARY KEY (id)
)
{
    ...audit_block
}
```

   The compiler expands `...common_timestamps` to:

```
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ
```

   and `...audit_block` to:

```
    OWNER app_admin;
    ENABLE ROW LEVEL SECURITY;
```

   Expansion is performed textually before tokenization.  The resulting
   source text is then tokenized and parsed as if written verbatim.

#### 4.7.3. Macro Scoping Rules

   -   Macros are scoped to one compilation unit: a database, or a
       cluster's objects directory (Section 3.1).  A macro defined in any
       `.dpg` file of the unit is visible in every other file of the same
       unit, and in no other unit.  The compiler performs a global collection pre-pass over
       all source files before any file is tokenized, so declaration
       order across files does not matter.

   -   When the same macro name is defined in multiple files, the
       file-local definition takes precedence over any globally-collected
       definition.  This allows individual files to override a shared
       macro with a specialised version.

   -   A macro name MUST be unique within its file.  Redefining a macro
       name in the same file is a compiler error (DPG-E011).

   -   Macros MAY NOT be recursive (a paren-body may not contain a
       spread of itself or of any other macro that eventually spreads
       it).  Circular macro references are a compiler error (DPG-E012).

   -   The `MACRO` keyword is a DPG preprocessor keyword and does not
       violate the No-Verb Mandate (Section 4.3).

### 4.8. Dual Definition Modes

   For every collection-typed sub-object (indexes, policies, triggers,
   grants, revocations, partitions, column blocks), DPG supports two
   equivalent syntactic modes that MAY be freely mixed within the same
   file:

   **Mode A — Plural block:** The collection keyword is the block
   header; individual entries omit the singular keyword.

```sql
INDEXES {
    idx_email  (email);
    idx_status (status) WHERE (status = 'active');
}
```

   **Mode B — Singular keyword:** The singular keyword precedes each
   individual entry outside a plural block.

```sql
INDEX idx_email  (email);
INDEX idx_status (status) WHERE (status = 'active');
```

   Both forms are semantically identical.  The compiler MUST merge them
   into a single logical collection before differencing.

   [DPG] The singular keyword is always PostgreSQL's own keyword for
   the sub-object, and the plural block header is that keyword with an
   `S` appended.  Any future collection kind follows the same rule.  The
   complete mapping is:

   | Plural block header  | Singular keyword   |
   |----------------------|--------------------|
   | `INDEXES`            | `INDEX`            |
   | `POLICIES`           | `POLICY`           |
   | `TRIGGERS`           | `TRIGGER`          |
   | `GRANTS`             | `GRANT`            |
   | `REVOKES`            | `REVOKE`           |
   | `PARTITIONS`         | `PARTITION`        |
   | `COLUMNS`            | `COLUMN`           |
   | `ATTRIBUTES`         | `ATTRIBUTE`        |
   | `CONSTRAINTS`        | `CONSTRAINT`       |
   | `NAME MAPS`          | `NAME MAP`         |

### 4.9. Block Merging

   When the same object is declared across multiple `.dpg` files (the
   same schema-qualified name and kind), the compiler MUST merge all
   declarations into a single logical object. Merge semantics follow
   Section 3.7.

   Merging operates on parsed declarations, not raw source text: each
   declaration is parsed and built on its own (Sections 15.4-15.7), and
   declarations of the same name and kind are then merged (Section
   15.8).

### 4.10. Identifiers

   DPG follows PostgreSQL's identifier rules:

   -   An **unquoted identifier** consists of letters (including Unicode
       letters), digits, dollar signs, and underscores.  It MUST begin
       with a letter or underscore.  Unquoted identifiers are
       case-folded to lowercase.

   -   A **quoted identifier** is enclosed in double-quotes (`"`).  It
       may contain any character except a double-quote.  To include a
       literal double-quote, write `""`.  Quoted identifiers are
       case-sensitive and preserve their original casing.

   [DPG] These rules apply identically inside `{ }` blocks: an unquoted
   name in a block directive (`OWNER App_Admin`, `COLUMN Email`, an index
   column) is folded to lowercase exactly as PostgreSQL folds the same
   name in Part 1.  In a role list, `PUBLIC`, `CURRENT_USER`,
   `CURRENT_ROLE`, and `SESSION_USER` are keywords, not role names.

   Schema-qualified names use the form `schema.name` or, for nested
   objects, `schema.table.column`.  The compiler resolves unqualified
   names using the enclosing scope context established by `SCHEMA { }`
   blocks, falling back to the `database.default_schema` configuration
   value.

---

## 5. Type System

### 5.1. ENUM Types

   **PG equivalent:** [`CREATE TYPE name AS ENUM (...)`](https://www.postgresql.org/docs/current/sql-createtype.html)

   **Part 1:** PostgreSQL's `CREATE TYPE ... AS ENUM` without `CREATE`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section 4.2.1), and:

   | Directive | Origin |
   |-----------|--------|
   | `VALUE 'new' RENAMED FROM 'old'` | [PG: `ALTER TYPE ... RENAME VALUE`] |
   | `MIGRATE REMOVE ('value', ...) { dml }` | [DPG] |

```abnf
enum-block     = *( enum-directive )
enum-directive = ( comment-dir / owner-dir / renamed-from-dir
                 / value-renamed-dir ) ";"
               / migrate-remove-dir

value-renamed-dir  = "VALUE" WSP string-literal WSP
                     "RENAMED FROM" WSP string-literal
migrate-remove-dir = "MIGRATE REMOVE" WSP "(" string-literal
                     *( "," WSP string-literal ) ")" WSP
                     "{" <DML statements> "}"
```

   `VALUE ... RENAMED FROM` names the current label first, in the same
   order as every other `RENAMED FROM`.  It is a block directive because
   PostgreSQL's value list accepts only string literals.

   Examples:

```sql
TYPE user_status AS ENUM ('active', 'suspended', 'deleted');

TYPE invoice_status AS ENUM ('draft', 'sent', 'paid', 'void', 'overdue') {
    COMMENT 'Billing lifecycle states for customer invoices';
}

-- Remove 'cancelled', moving its rows to 'delivered' first
TYPE order_status AS ENUM ('pending', 'confirmed', 'shipped', 'delivered') {
    MIGRATE REMOVE ('cancelled') {
        UPDATE orders SET status = 'delivered' WHERE status = 'cancelled';
    }
}

-- 'fulfilled' in the snapshot becomes 'delivered'
TYPE order_status AS ENUM ('pending', 'confirmed', 'shipped', 'delivered') {
    VALUE 'delivered' RENAMED FROM 'fulfilled';
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New type | `CREATE TYPE ... AS ENUM (...)` | `SAFE` |
   | Value added | `ALTER TYPE name ADD VALUE 'v' [BEFORE \| AFTER 'anchor']` (Section 5.1.1) | `SAFE` |
   | Value renamed (`VALUE ... RENAMED FROM`) | `ALTER TYPE name RENAME VALUE 'old' TO 'new'` | `SAFE` |
   | Value removed, covered by `MIGRATE REMOVE` | Type rebuild (Section 5.1.2) | `DESTRUCTIVE` |
   | Value removed, not covered | Error DPG-E014, unless `--allow-destructive` (Section 5.1.2) | `DESTRUCTIVE` |
   | Values reordered | Remove and add (Section 5.1.3) | `DESTRUCTIVE` |
   | Comment changed | `COMMENT ON TYPE name IS '...'` | `SAFE` |
   | Owner changed | `ALTER TYPE name OWNER TO role` | `SAFE` |
   | Type renamed (`RENAMED FROM`) | `ALTER TYPE old RENAME TO new`; `SET SCHEMA` when the schema differs (Section 7.6) | `CAUTION` / `SAFE` |
   | Type removed | `DROP TYPE name` | `DESTRUCTIVE` |

#### 5.1.1. Adding and Renaming ENUM Values

   A value present in the source but absent from the snapshot emits:

```sql
ALTER TYPE <schema>.<name> ADD VALUE '<new_value>' [BEFORE | AFTER '<anchor>'];
```

   Safety `SAFE`.  It runs inside the migration's transaction, except
   in one case.  [PG] PostgreSQL rejects using an enum value inside the
   transaction that added it.  So when any other statement in the same
   migration uses a value being added (a column `DEFAULT`, a `CHECK`, a
   partial index predicate, a view), every `ADD VALUE` for that enum is
   emitted as `ADD VALUE IF NOT EXISTS` and runs before the transaction
   begins (Section 17.3).  `IF NOT EXISTS` makes a retry harmless if the
   transaction later fails.

   [DPG] **Position.** A new value's position is taken from where it
   sits in the source list.  Walking the list left to right, each new
   value is emitted `AFTER` the nearest earlier value that already
   exists (including one added or renamed earlier in the same
   migration), or `BEFORE` the nearest later existing value when there
   is no earlier one.  With neither, it is emitted without an anchor.

   [DPG] **Renaming.** `VALUE 'new' RENAMED FROM 'old';` resolves like
   every other `RENAMED FROM` (Section 7.6):

   -   `'old'` in the snapshot and `'new'` absent: emit `ALTER TYPE
       <schema>.<name> RENAME VALUE 'old' TO 'new'` (`SAFE`).
   -   `'new'` already in the snapshot: no-op.
   -   Neither in the snapshot: error DPG-E021 (stale `RENAMED FROM`).

#### 5.1.2. Removing ENUM Values

   [PG] PostgreSQL cannot remove a value from an enum.  [DPG] `MIGRATE
   REMOVE` names the removed values and the DML that moves existing data
   off them; the type is then rebuilt.

   When a value is absent from the source but present in the snapshot,
   and a `MIGRATE REMOVE` directive covers it, the compiler MUST emit
   the following sequence, all within a single transaction:

   1.  `CREATE TYPE <schema>.<name>__dpg_new AS ENUM (<reduced-values>);`
       — a new type with the value removed.

   2.  Execute each DML statement in the `MIGRATE REMOVE` body verbatim.

   3.  Verify that no rows in any column typed as `<name>` still hold
       the removed value.  If any remain, the compiler MUST abort the
       transaction and report error DPG-E013 with a table-by-table row
       count.

   4.  For each table column typed as `<name>`:

       ```sql
       ALTER TABLE <schema>.<table>
           ALTER COLUMN <col> TYPE <schema>.<name>__dpg_new
           USING <col>::text::<schema>.<name>__dpg_new;
       ```

   5.  `DROP TYPE <schema>.<name>;`

   6.  `ALTER TYPE <schema>.<name>__dpg_new RENAME TO <name>;`

   On any failure in steps 2–6: `DROP TYPE IF EXISTS <schema>.<name>__dpg_new;`
   and rollback.

   When a value is absent from the source but no `MIGRATE REMOVE`
   directive covers it, the compiler MUST emit a diagnostic (DPG-E014,
   unguarded ENUM value removal) and refuse to proceed without the
   `--allow-destructive` flag.  Even with `--allow-destructive`, the
   compiler MUST attempt to verify that no rows hold the removed value
   before proceeding.

   `MIGRATE REMOVE` is the only place DPG runs user DML (Section 1.1).

#### 5.1.3. Reordering ENUM Values

   [PG] An enum's value order is fixed.  [DPG] Reordering existing
   values is treated as a remove and an add and classified as
   `DESTRUCTIVE`, unless every moved value is new (not in the snapshot).

### 5.2. Composite Types

   **PG equivalent:** [`CREATE TYPE name AS (...)`](https://www.postgresql.org/docs/current/sql-createtype.html)

   **Part 1:** PostgreSQL's `CREATE TYPE ... AS (...)` without `CREATE`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section 4.2.1), and:

   | Directive | Origin |
   |-----------|--------|
   | `ATTRIBUTE name { ... }` / `ATTRIBUTES { name { ... } ... }` | [DPG] container for one attribute's directives |
   | `COMMENT 'text'` (in an attribute) | [PG: `COMMENT ON COLUMN type.attr`] |
   | `RENAMED FROM old` (in an attribute) | [PG: `ALTER TYPE ... RENAME ATTRIBUTE`] |

```abnf
composite-block     = *( composite-directive )
composite-directive = ( comment-dir / owner-dir / renamed-from-dir ) ";"
                    / attribute-block
                    / attributes-block

attribute-block  = "ATTRIBUTE" WSP identifier WSP "{" *attr-directive "}"
attributes-block = "ATTRIBUTES" WSP "{"
                     *( identifier WSP "{" *attr-directive "}" ) "}"
attr-directive   = ( comment-dir / "RENAMED FROM" WSP identifier ) ";"
```

   [DPG] `ATTRIBUTE`/`ATTRIBUTES` use PostgreSQL's own keyword for a
   composite type member.  An `ATTRIBUTE` block naming an attribute
   absent from the `( )` list is error DPG-E018.  `COLUMN` in a
   composite type block, and `ATTRIBUTE` in any other block, is error
   DPG-E024.

   Example:

```sql
SCHEMA public {
    TYPE address AS (
        street      TEXT COLLATE "en_US",
        city        TEXT,
        state       CHAR(2),
        postal_code TEXT,
        country     CHAR(2)
    )
    {
        OWNER schema_admin;
        ATTRIBUTE postal_code { COMMENT 'ZIP or postcode'; }
    }
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Attribute added | `ALTER TYPE name ADD ATTRIBUTE attr type` | `SAFE` |
   | Attribute dropped | `ALTER TYPE name DROP ATTRIBUTE attr` | `DESTRUCTIVE` |
   | Attribute type or `COLLATE` changed | `ALTER TYPE name ALTER ATTRIBUTE attr TYPE new [COLLATE c]` | `DESTRUCTIVE` |
   | Attribute renamed (`RENAMED FROM`, resolved as in Section 7.6) | `ALTER TYPE name RENAME ATTRIBUTE old TO new` | `SAFE` |
   | Attribute comment changed | `COMMENT ON COLUMN name.attr IS '...'` (or `IS NULL`) | `SAFE` |
   | Owner changed | `ALTER TYPE name OWNER TO role` | `SAFE` |
   | Type renamed (`RENAMED FROM`) | `ALTER TYPE old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |

### 5.3. Range Types

   **PG equivalent:** [`CREATE TYPE name AS RANGE (...)`](https://www.postgresql.org/docs/current/sql-createtype.html)

   **Part 1:** PostgreSQL's `CREATE TYPE ... AS RANGE (...)` without
   `CREATE`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section 4.2.1).

```abnf
range-block = *( ( comment-dir / owner-dir / renamed-from-dir ) ";" )
```

   Example:

```sql
SCHEMA public {
    TYPE float8range AS RANGE (
        SUBTYPE      = float8,
        SUBTYPE_DIFF = float8mi
    );
}
```

   **Multirange types:** [PG] PostgreSQL creates a multirange type with
   every range type and drops it with the range, so DPG never declares
   it separately.  Its name is PostgreSQL's default (the range's name
   with its first `range` replaced by `multirange`, `floatrange` giving
   `floatmultirange`, or with `_multirange` appended when the name
   contains no `range`) unless Part 1 sets `MULTIRANGE_TYPE_NAME = name`.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Only the multirange name changed | `ALTER TYPE schema.old_multirange RENAME TO new_multirange` | `CAUTION` |
   | Any other option changed | `DROP TYPE CASCADE` + `CREATE TYPE` | `DESTRUCTIVE` |
   | Owner changed | `ALTER TYPE name OWNER TO role` | `SAFE` |
   | Type renamed (`RENAMED FROM`) | `ALTER TYPE old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |

### 5.4. Domain Types

   **PG equivalent:** [`CREATE DOMAIN`](https://www.postgresql.org/docs/current/sql-createdomain.html)

   **Part 1:** PostgreSQL's `CREATE DOMAIN` without `CREATE`, so
   `DEFAULT` and constraints are written there, as PostgreSQL writes
   them.  DPG adds two things to a domain constraint:

```abnf
domain-constraint = <PostgreSQL's domain_constraint>
                    [ WSP "NOT VALID" ]
                    [ WSP "RENAMED FROM" WSP identifier ]
```

   -   [DPG] `NOT VALID` on a domain constraint.  [PG] `CREATE DOMAIN`
       has no `NOT VALID`; it exists only on `ALTER DOMAIN ... ADD
       CONSTRAINT`.  It therefore takes effect only when the constraint
       is added to a domain that already exists, and is left out of the
       `CREATE DOMAIN` for a new domain.  Removing it later validates the
       constraint.
   -   [DPG] `RENAMED FROM old` on a named constraint renames it instead
       of dropping and re-adding it.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `GRANTS`, `REVOKES`
   (Section 4.2.1).

```abnf
domain-block = *( ( comment-dir / owner-dir / renamed-from-dir
                   / grants-block / revocations-block ) ";" )
```

   Example:

```sql
SCHEMA public {
    DOMAIN positive_integer AS INTEGER
        DEFAULT 1
        CONSTRAINT positive_only  CHECK (VALUE > 0)
        CONSTRAINT reasonable_max CHECK (VALUE < 1000000);
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `DEFAULT` added or changed | `ALTER DOMAIN name SET DEFAULT expr` | `SAFE` |
   | `DEFAULT` removed | `ALTER DOMAIN name DROP DEFAULT` | `SAFE` |
   | `NOT NULL` added / removed | `ALTER DOMAIN name SET NOT NULL` / `DROP NOT NULL` | `CAUTION` / `SAFE` |
   | Constraint added | `ALTER DOMAIN name ADD CONSTRAINT c CHECK (...) [NOT VALID]` | `CAUTION` |
   | `NOT VALID` removed | `ALTER DOMAIN name VALIDATE CONSTRAINT c` | `CAUTION` |
   | Constraint renamed (`RENAMED FROM`) | `ALTER DOMAIN name RENAME CONSTRAINT old TO new` | `SAFE` |
   | Constraint dropped | `ALTER DOMAIN name DROP CONSTRAINT c` | `SAFE` |
   | Base type or `COLLATE` changed | `DROP DOMAIN CASCADE` + `CREATE DOMAIN` | `DESTRUCTIVE` |
   | Owner changed | `ALTER DOMAIN name OWNER TO role` | `SAFE` |
   | Domain renamed (`RENAMED FROM`) | `ALTER DOMAIN old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |

### 5.5. Base (Shell) Types

   **PG equivalent:** [`CREATE TYPE name (INPUT = ..., OUTPUT = ..., ...)`](https://www.postgresql.org/docs/current/sql-createtype.html), or the shell form `CREATE TYPE name`

   **Part 1:** PostgreSQL's base-type and shell-type `CREATE TYPE`
   forms without `CREATE`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section 4.2.1).

```abnf
base-type-block = *( ( comment-dir / owner-dir / renamed-from-dir ) ";" )
```

```sql
SCHEMA public {
    TYPE mytype (
        INPUT          = mytype_in,
        OUTPUT         = mytype_out,
        INTERNALLENGTH = 16,
        ALIGNMENT      = double
    );
}
```

   **Shell types:** `TYPE name;` declares a shell type on its own.

   [DPG] **Support functions that reference the type.**  When a new
   base type's support functions are also new and take or return the
   type itself (`FUNCTION mytype_in(cstring) RETURNS mytype`), the
   dependency graph (Section 22) drops the function-to-type ordering
   edge for them, so the `CREATE FUNCTION` statements are emitted before
   the `CREATE TYPE`.  [PG] PostgreSQL creates the shell type itself when
   such a function references a type that doesn't exist yet, and the full
   `CREATE TYPE` then completes it.  The source declares only the full
   type.

   **Diffing semantics:** [PG] `ALTER TYPE name SET (...)` can change
   only `RECEIVE`, `SEND`, `TYPMOD_IN`, `TYPMOD_OUT`, `ANALYZE`,
   `SUBSCRIPT` and `STORAGE`; every other property is fixed at creation.
   Properties are compared one by one:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Only `RECEIVE`/`SEND`/`TYPMOD_IN`/`TYPMOD_OUT`/`ANALYZE`/`SUBSCRIPT`/`STORAGE` differ | `ALTER TYPE name SET (key = value \| NONE, ...)` | `SAFE` |
   | Any other property differs | `DROP TYPE CASCADE` + `CREATE TYPE` | `DESTRUCTIVE` |
   | Owner changed | `ALTER TYPE name OWNER TO role` | `SAFE` |
   | Type renamed (`RENAMED FROM`) | `ALTER TYPE old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | Type removed | `DROP TYPE name [CASCADE]` | `DESTRUCTIVE` |

### 5.6. Virtual Types

   Virtual types are DPG-native DDL constructs that give a structural
   schema to `JSON` / `JSONB` columns and `JSON` array columns.  They
   have no backing PostgreSQL type — no `CREATE TYPE`, `ALTER TYPE`, or
   `DROP TYPE` is ever emitted.  A table column or composite type
   attribute may declare its type as a virtual type name; DPG resolves
   that reference to `jsonb` (scalar) or `jsonb[]` (array) in all
   generated SQL.  The structured body is stored in the snapshot so
   downstream consumers (ORM generators, type-safe query builders) can
   read type information from the snapshot JSON.

   **PG equivalent:** none.  The whole declaration is DPG grammar
   ([DPG]).

   **Part 2:** `COMMENT` (Section 4.2.1, kept in the snapshot only) and
   `PREFERRED JSON FORMAT json | jsonb` [DPG].

```abnf
virtual-type-decl = "VIRTUAL TYPE" WSP schema-name WSP "AS" WSP
                    vtype-body ";" [ "{" vtype-block "}" ]

vtype-body      = vtype-union
vtype-union     = vtype-term *( WSP "|" WSP vtype-term )
vtype-term      = vtype-composite / vtype-typeref
vtype-composite = "(" vtype-field *( "," WSP vtype-field ) ")"
vtype-field     = identifier WSP vtype-typeref
vtype-typeref   = [ schema-name "." ] identifier [ "[]" ]

vtype-block = *( comment-dir / preferred-json-format-dir ) ";"

preferred-json-format-dir = "PREFERRED" WSP "JSON" WSP "FORMAT" WSP
                            ( "json" / "jsonb" )
```

   The compiler MUST parse and validate the body.  A `vtype-typeref`
   that appears in a column or composite attribute position resolves to
   the virtual type's preferred JSON format (`json` or `jsonb`, default
   `jsonb`) in SQL output; the virtual type name is never written to the
   database.

   **Body forms:**

   -   **Type reference** — a single PG built-in or user-defined type
       name, or a reference to another declared `VIRTUAL TYPE`:

       ```sql
       VIRTUAL TYPE label AS text;
       VIRTUAL TYPE metric AS numeric;
       VIRTUAL TYPE location AS point;           -- PostgreSQL's point type
       VIRTUAL TYPE tags AS label[];             -- a JSON array of label values
       ```

   -   **Composite** — an inline record with named, typed fields.
       Field types MAY themselves be virtual type references:

       ```sql
       VIRTUAL TYPE coordinate AS (x float8, y float8);

       VIRTUAL TYPE line_item AS (
           sku      text,
           quantity integer,
           price    numeric,
           tags     label[]
       );
       ```

   -   **Union** — two or more terms joined with `|`.  Any combination
       of composites and type references is valid:

       ```sql
       VIRTUAL TYPE payment AS
           (kind text, amount numeric, currency text)
           | (kind text, token text)
           | text;
       ```

   **Usage as a column or field type:**

   ```sql
   TABLE orders (
       id    bigint,
       items line_item[]   -- virtual type ref: emits "jsonb[]" in SQL
   ) { }

   TYPE address AS (
       street text,
       detail address_detail   -- virtual type ref: emits "jsonb"
   );
   ```

   **Arrays.**  `[]` means an array of the type it follows, in the
   position where it is written:

   -   Inside a virtual type body, `T[]` is a JSON array whose elements
       are `T` values (`tags label[]` above).
   -   On a column or composite attribute, `T[]` is a PostgreSQL array,
       as it is for every other type: `items line_item[]` is a `jsonb[]`
       column, and each element is one `line_item` value.  A column that
       holds a single JSON array uses a virtual type whose body is the
       array (`VIRTUAL TYPE line_items AS line_item[];`, then `items
       line_items`, a `jsonb` column).

   **Name resolution.**  A virtual type shares PostgreSQL's type
   namespace, so a type name in DPG source never means two things:

   -   A virtual type's name MUST NOT be a PostgreSQL built-in type name
       (any type in `pg_catalog`, or a SQL spelling of one, such as
       `integer` or `boolean`).  Its qualified name MUST NOT already be
       used by a declared type (including a range's multirange type) or
       by a table, view, materialized view, or foreign table, each of
       which has a row type of the same name.  Either is error DPG-E044.
       The same name in a different schema is not a conflict.
   -   An unqualified type reference on a column, a composite attribute,
       or inside a virtual type body names a virtual type when one of
       that name exists in the referencing object's own schema, or else
       in the database's `default_schema` (Section 3.4).  Otherwise it is
       an ordinary PostgreSQL type name.  A schema-qualified reference
       names exactly that schema's virtual type, if one exists.

   **Logical type in the snapshot.**  The snapshot records the full
   declared type of every column that uses a virtual type, for tools
   such as client generators.  A column record holds `type` (the
   `json`/`jsonb` type PostgreSQL stores) and `virtual_type` (the
   schema-qualified reference as declared, for example
   `"public.line_item[]"`).  Inside a virtual type record, every type
   reference that names a virtual type carries `"virtual": true` and its
   schema; any other reference names a PostgreSQL type (Section 16.3).

   **Rules:**

   -   `VIRTUAL TYPE` MAY be schema-qualified; an unqualified one belongs
       to its schema scope, or else to `default_schema` (Section 4.4).

   -   No `CREATE TYPE`, `ALTER TYPE`, or `DROP TYPE` is EVER emitted
       for a virtual type.

   -   Virtual types appear in the snapshot under `"kind": "virtual_type"`
       for round-trip consistency.  `dpg plan` produces no SQL for
       additions, modifications, or removals of virtual types.

   -   When a column or composite attribute type resolves to a virtual
       type, the generated SQL uses `jsonb` / `jsonb[]`.  The virtual
       type name is never written to the database catalog.

   -   The `{ }` block accepts `COMMENT` and `PREFERRED JSON FORMAT`.
       Any other directive is a compiler error (DPG-E015).

   -   `PREFERRED JSON FORMAT json` causes DPG to emit `json` / `json[]`
       instead of the default `jsonb` / `jsonb[]` when this virtual type
       is used as a column or attribute type.

---

## 6. Schema and Namespace Objects

### 6.1. SCHEMA

   **PG equivalent:** [`CREATE SCHEMA`](https://www.postgresql.org/docs/current/sql-createschema.html)

   **Part 1:** none.  [DPG] A schema is declared by its name and a
   `{ }` block, which also holds the objects declared in it (Section
   4.4); `AUTHORIZATION role` is written as `OWNER role`.  A file under
   `schemas/<name>/` declares the schema's own settings with the same
   form (Section 3.1).

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `GRANTS` (Section
   4.2.1), `DEFAULT PRIVILEGES` (Section 11.4), and nested object
   declarations.

```abnf
schema-decl = "SCHEMA" WSP identifier
              "{" schema-block "}"

schema-block = *( schema-directive / nested-object )

schema-directive = owner-dir
                 / comment-dir
                 / renamed-from-dir
                 / grants-block
                 / default-privileges-decl
```

   Examples:

```sql
SCHEMA analytics {
    OWNER analytics_role;
    COMMENT 'Derived tables and event aggregations';

    TABLE events ( ... ) { ... }
    FUNCTION compute_daily() RETURNS VOID LANGUAGE plpgsql AS $$ ... $$;
}

SCHEMA reporting {
    RENAMED FROM old_reporting;
}
```

   [PG] From PostgreSQL 15, a new cluster's `public` schema no longer
   grants `CREATE` to `PUBLIC`.  A project that relies on that grant
   declares it (`GRANTS { CREATE TO PUBLIC; }` on `public`).

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New schema | `CREATE SCHEMA name` | `SAFE` |
   | `RENAMED FROM` | `ALTER SCHEMA old RENAME TO new` | `CAUTION` |
   | Owner changed | `ALTER SCHEMA name OWNER TO role` | `SAFE` |
   | Comment changed | `COMMENT ON SCHEMA name IS '...'` | `SAFE` |
   | Schema removed | `DROP SCHEMA name [RESTRICT\|CASCADE]` | `DESTRUCTIVE` |

### 6.2. EXTENSION

   Extensions are database-level objects and MUST be declared in a
   database `.dpg` file, not in the cluster objects directory.

   **PG equivalent:** [`CREATE EXTENSION`](https://www.postgresql.org/docs/current/sql-createextension.html)

   **Part 1:** PostgreSQL's `CREATE EXTENSION` without `CREATE` (and
   without `IF NOT EXISTS`, which DPG adds itself).

   **Part 2:** `COMMENT` (Section 4.2.1).

   Examples:

```sql
EXTENSION pgcrypto;
EXTENSION postgis SCHEMA public VERSION '3.3';
EXTENSION pg_trgm CASCADE;
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New extension | `CREATE EXTENSION IF NOT EXISTS name [SCHEMA ...] [VERSION ...] [CASCADE]` | `SAFE` |
   | `VERSION` changed | `ALTER EXTENSION name UPDATE TO version` | `CAUTION` |
   | `SCHEMA` changed | `ALTER EXTENSION name SET SCHEMA new_schema` | `SAFE` |
   | Comment changed | `COMMENT ON EXTENSION name IS '...'` | `SAFE` |
   | Extension removed | `DROP EXTENSION name [CASCADE]` | `DESTRUCTIVE` |

   [PG] `SET SCHEMA` works only for a relocatable extension; PostgreSQL
   rejects it for the others.

---

## 7. Tables

### 7.1. Table Declaration Syntax

   **PG equivalent:** [`CREATE [UNLOGGED] TABLE`](https://www.postgresql.org/docs/current/sql-createtable.html), [`CREATE FOREIGN TABLE`](https://www.postgresql.org/docs/current/sql-createforeigntable.html)

   **Part 1:** PostgreSQL's `CREATE TABLE` (including the `OF
   type_name` form and `PARTITION BY`) or `CREATE FOREIGN TABLE`,
   without `CREATE`.  DPG adds a `RENAMED FROM` suffix to a table
   constraint (Section 7.3).  `PARTITION OF` is not written in Part 1;
   partitions are declared in the parent's `PARTITIONS` block (Section
   7.13).

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `DEPRECATED`,
   `PROTECTED`, `DROP CASCADE`, `GRANTS`, `REVOKES`, `SECURITY LABEL`,
   `NAME MAP` (Section 4.2.1), and:

   | Directive | Origin | Section |
   |-----------|--------|---------|
   | `COLUMN name { ... }` / `COLUMNS { ... }` | [DPG] container for one column's directives | 7.4 |
   | `CONSTRAINT name ...` / `CONSTRAINTS { ... }` | [PG: `ALTER TABLE ... ADD CONSTRAINT`] | 7.3 |
   | `INDEXES { ... }` / `INDEX ...` | [PG: `CREATE INDEX`] | 7.7 |
   | `ENABLE ROW LEVEL SECURITY` / `FORCE ROW LEVEL SECURITY` | [PG: `ALTER TABLE ... ENABLE` / `FORCE ROW LEVEL SECURITY`] | 7.8 |
   | `POLICIES { ... }` / `POLICY ...` | [PG: `CREATE POLICY`] | 7.8 |
   | `TRIGGERS { ... }` / `TRIGGER ...` | [PG: `CREATE TRIGGER`] | 7.9 |
   | `REPLICA IDENTITY ...` | [PG: `ALTER TABLE ... REPLICA IDENTITY`] | 7.11 |
   | `CLUSTER ON index` | [PG: `ALTER TABLE ... CLUSTER ON`] | 7.11 |
   | `PARTITIONS { ... }` | [PG: `CREATE TABLE ... PARTITION OF`] | 7.13 |
   | `DETACHED FROM parent` | [DPG] | 7.13 |

```abnf
table-block  = *( table-directive )

table-directive = owner-dir / comment-dir / renamed-from-dir
                / detached-from-dir / protected-dir / deprecated-dir
                / drop-cascade-dir / rls-enable-dir / rls-force-dir
                / replica-identity-dir / cluster-dir
                / column-block / indices-block / policies-block
                / triggers-block / grants-block / revocations-block
                / constraint-decl / partitions-block
```

   **Notes:**

   -   [DPG] `LIKE source_table` is resolved by the compiler: the
       source must be a table declared in the same compile unit, and the
       table is diffed and snapshotted with the resolved columns and
       constraints, not the `LIKE` clause.  `LIKE` options apply in
       order, as in PostgreSQL.
   -   [DPG] A table declared `OF type_name` is compared column by
       column like any other.  Gaining, losing or changing `OF
       type_name` later emits `ALTER TABLE t OF type_name` / `NOT OF`
       (Section 7.11).
   -   [DPG] Changing `USING method`, `INHERITS (...)`, `WITH (...)` or
       `TABLESPACE` after creation emits the targeted `ALTER TABLE`
       statements in Section 7.11; the table is not recreated.

```sql
TABLE order_items_archive (
    LIKE order_items INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

TYPE address AS (street TEXT, city TEXT, zip TEXT);

TABLE shipping_address OF address (
    street WITH OPTIONS NOT NULL,
    CONSTRAINT zip_format CHECK (zip ~ '^[0-9]{5}$')
);
```

### 7.2. Column Definitions

   **Part 1:** columns are written in the `( )` list with PostgreSQL's
   `CREATE TABLE` column syntax.  [DPG] A column's type MAY also be a
   virtual type (Section 5.6) or `SERIAL`/`BIGSERIAL`/`SMALLSERIAL`
   (Appendix D.3).

   **Notes:**

   -   [DPG] **PRIMARY KEY implies NOT NULL.**  The compiler treats
       every `PRIMARY KEY` column, inline or table-level, as `NOT NULL`,
       as PostgreSQL does.  A redundant `NOT NULL` is accepted; the
       compiler emits no redundant `NOT NULL` in `CREATE TABLE` and no
       `SET NOT NULL`/`DROP NOT NULL` for a primary-key column.
   -   [DPG] **Inline and table-level constraints are equivalent.**  The
       compiler emits single-column `PRIMARY KEY`, `UNIQUE`, `CHECK` and
       `REFERENCES` constraints inline in `CREATE TABLE`, named ones
       included; multi-column constraints stay table-level.
   -   [DPG] **Generated and identity columns** are diffed per Section
       7.4's tables.

```sql
-- Inline form
TABLE accounts (
    id   UUID    NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    slug TEXT    NOT NULL UNIQUE,
    org  UUID    NOT NULL REFERENCES organisations (id) ON DELETE CASCADE
);

-- Table-level (needed for multi-column constraints)
TABLE order_items (
    order_id   BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    CONSTRAINT pk_order_items PRIMARY KEY (order_id, product_id)
);
```

   **Column type changes.**  `ALTER TABLE t ALTER COLUMN c TYPE new
   [USING expr]` rewrites the table under an `ACCESS EXCLUSIVE` lock, so
   it is at least `CAUTION`.  [DPG] It is `CAUTION` when a `USING`
   expression is given (`COLUMN c { USING expr; }`, Section 7.4) or when
   PostgreSQL converts the stored values without loss, and `DESTRUCTIVE`
   otherwise.  PostgreSQL converts without loss:

   -   between two different types that have an implicit cast
       (`pg_cast.castcontext = 'i'`, for example `smallint` to
       `integer`);
   -   within one type, when the modifier widens:

       | Type | `CAUTION` when |
       |------|----------------|
       | `character varying(n)`, `bit varying(n)` | the length grows, or is removed |
       | `character(n)` | the length grows (bare `character` means `character(1)`) |
       | `bit(n)` | never: PostgreSQL rejects any length change of fixed `bit` |
       | `numeric(p,s)` | neither precision nor scale shrinks, or both are removed |
       | `time(p)`, `timestamp(p)` (with or without time zone), `interval(p)` | the precision grows, or is removed |
       | arrays of any of these | never |

   Reason: a shrinking `numeric` scale or time precision does not fail;
   PostgreSQL silently rounds or truncates the stored values, so it is
   treated as data loss.

### 7.3. Constraints

   **Part 1:** PostgreSQL's table and column constraint syntax, in the
   `( )` list.

   **Part 2:** `CONSTRAINT name ...` entries in the `{ }` block, which
   describe constraints added to an existing table [PG: `ALTER TABLE
   ... ADD CONSTRAINT`].  [DPG] A constraint in either place MAY carry a
   `RENAMED FROM` suffix.

```abnf
constraint-decl = "CONSTRAINT" WSP identifier WSP
                  <PostgreSQL's table constraint, after its name>
                  [ WSP "RENAMED FROM" WSP identifier ] ";"
                / "CONSTRAINTS" WSP "{" *( identifier WSP
                  <PostgreSQL's table constraint, after its name>
                  [ WSP "RENAMED FROM" WSP identifier ] ";" ) "}"
```

   [DPG] Constraints are identified by name and merged across the `( )`
   list and the `{ }` block: the same name and definition in both is one
   constraint; the same name with different definitions is DPG-E005.

   **`NOT VALID` lifecycle** [DPG]:

   1.  A constraint declared `NOT VALID` on an existing table is added
       with `ALTER TABLE t ADD CONSTRAINT c ... NOT VALID` (`CAUTION`).
   2.  Removing `NOT VALID` in source emits `ALTER TABLE t VALIDATE
       CONSTRAINT c` (`CAUTION`).
   3.  The validated constraint MAY then be moved into the `( )` list;
       it is matched by name and nothing is emitted.

   `NOT VALID` MAY be written in the `( )` list or on a `{ }` block
   entry.  A new table is created with its constraints in the `( )`
   list; the `{ }` block form describes a constraint added to an
   existing table.

   **Diffing semantics** [DPG]:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Constraint added | `ALTER TABLE t ADD CONSTRAINT c ...` | `CAUTION` |
   | `NOT VALID` removed | `ALTER TABLE t VALIDATE CONSTRAINT c` | `CAUTION` |
   | Renamed only (`RENAMED FROM`) | `ALTER TABLE t RENAME CONSTRAINT old TO new` | `SAFE` |
   | `FOREIGN KEY` deferrability or `ENFORCED` only | `ALTER TABLE t ALTER CONSTRAINT c [NOT] DEFERRABLE [INITIALLY ...] [[NOT] ENFORCED]` | `SAFE` |
   | `NOT NULL` constraint's `NO INHERIT` only | `ALTER TABLE t ALTER CONSTRAINT c [NO] INHERIT` | `SAFE` |
   | Any other change (a `CHECK`'s `ENFORCED` included) | `ALTER TABLE t DROP CONSTRAINT c` + `ADD CONSTRAINT` | `DESTRUCTIVE` |
   | Constraint dropped | `ALTER TABLE t DROP CONSTRAINT c` | `DESTRUCTIVE` |

   **Foreign key cycles** [DPG]: when tables reference each other and
   the foreign keys in the cycle are `DEFERRABLE`, the tables are
   created first and those foreign keys added afterwards with `ALTER
   TABLE ... ADD CONSTRAINT ... DEFERRABLE`.  A cycle with no
   `DEFERRABLE` foreign key is error DPG-E017, listing the cycle.

   **Temporal keys** [DPG]: a `PRIMARY KEY`/`UNIQUE` with `WITHOUT
   OVERLAPS`, or a `FOREIGN KEY` with `PERIOD`, is diffed as the
   `PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` PostgreSQL catalogues it as.
   [PG] Non-range key columns need the `btree_gist` extension.

```sql
TABLE room_bookings (
    room_id  BIGINT NOT NULL,
    valid_at DATERANGE NOT NULL,
    CONSTRAINT no_double_booking
        PRIMARY KEY (room_id, valid_at WITHOUT OVERLAPS)
);
```

   **Constraints kept on partitions** [DPG]: [PG] `ALTER TABLE ONLY
   parent DROP CONSTRAINT name` (PostgreSQL 18) drops a constraint from
   a partitioned parent while keeping it, as a local constraint, on
   every existing partition.  To declare that, a partition's
   `PARTITIONS { }` entry MAY carry its own `{ }` body with `CONSTRAINT`
   / `CONSTRAINTS` entries:

```abnf
partition-decl       =/ ... [ WSP "{" partition-block-body "}" ]
partition-block-body = *( ( "PARTITIONS" WSP "{" *partition-decl "}" )
                         / constraint-decl )
```

   When a constraint disappears from the parent while a direct
   partition declares a constraint of the same name, the compiler emits
   `ALTER TABLE ONLY parent DROP CONSTRAINT name` (`SAFE`) instead of
   the ordinary cascading drop.  Otherwise a partition's own constraints
   are added and dropped with ordinary `ALTER TABLE partition ADD`/`DROP
   CONSTRAINT`.

   -   This is the only structure a partition declares independently of
       its parent.  It does not apply to an `ATTACHED FROM` entry, whose
       constraints come from its own `TABLE` declaration.
   -   `ONLY` keeps the constraint on every existing partition.  Keeping
       it on only some takes two migrations: the `ONLY` drop, then
       ordinary drops on the others.
   -   Only direct partitions are matched against the parent.

   Example:

```sql
TABLE orders (
    id         BIGINT GENERATED ALWAYS AS IDENTITY,
    account_id BIGINT NOT NULL,
    amount     NUMERIC(10,2) NOT NULL,
    CONSTRAINT pk_orders PRIMARY KEY (id)
)
{
    -- added to an existing table, validated in a later migration
    CONSTRAINT ck_amount_positive CHECK (amount > 0) NOT VALID;

    CONSTRAINT fk_account FOREIGN KEY (account_id)
        REFERENCES accounts (id)
        ON DELETE CASCADE
        DEFERRABLE INITIALLY DEFERRED;
}
```

### 7.4. The COLUMN Reference Block

   [DPG] Inside a table's `{ }` block, `COLUMN name { ... }` refers to a
   column declared in the `( )` list and holds what PostgreSQL sets on a
   column with separate statements.

```abnf
column-block    = "COLUMN" WSP col-name WSP "{" col-block-body "}"
               /  "COLUMNS" WSP "{" *col-named-block "}"

col-named-block = col-name WSP "{" col-block-body "}"

col-block-body  = *( col-block-directive ";" )

col-block-directive
    = comment-dir
    / "STATISTICS" WSP ( integer / "DEFAULT" )
    / "COMPRESSION" WSP identifier
    / "STORAGE" WSP identifier
    / deprecated-dir
    / "USING" WSP expr
    / "RENAMED FROM" WSP col-name
    / grants-block
    / revocations-block
    / security-label-decl
```

   | Directive | Origin | Emits |
   |-----------|--------|-------|
   | `COMMENT 'text'` | [PG] | `COMMENT ON COLUMN t.c IS '...'` |
   | `STATISTICS n` | [PG] | `ALTER TABLE t ALTER COLUMN c SET STATISTICS n` |
   | `STATISTICS DEFAULT` | [PG] | `ALTER TABLE t ALTER COLUMN c SET STATISTICS -1` |
   | `COMPRESSION method` | [PG] | `ALTER TABLE t ALTER COLUMN c SET COMPRESSION method` |
   | `STORAGE mode` | [PG] | `ALTER TABLE t ALTER COLUMN c SET STORAGE mode` |
   | `GRANTS { ... }` / `REVOKES { ... }` | [PG] | `GRANT`/`REVOKE priv (c) ON TABLE t ...` (Section 7.5) |
   | `SECURITY LABEL ...` | [PG] | `SECURITY LABEL ... ON COLUMN t.c` |
   | `DEPRECATED 'msg'` | [DPG] | `COMMENT ON COLUMN t.c IS '[DEPRECATED] msg'` |
   | `USING expr` | [DPG] | supplies `USING expr` to a type change of the column |
   | `RENAMED FROM old` | [DPG] | `ALTER TABLE t RENAME COLUMN old TO c` (Section 7.6) |

   **Rules** [DPG]:

   -   `COLUMN name` MUST name a column in the `( )` list (DPG-E018).
       A `COLUMN` block never declares a column.
   -   After a rename, the block uses the new name; `RENAMED FROM`
       carries the old one.  Any index or constraint in the `{ }` block
       that still names the old column is error DPG-E019.
   -   A statistics target above `10000` is error DPG-E020.

   **Generated columns** [DPG]: a change to a column's `GENERATED
   ALWAYS AS (expr) STORED | VIRTUAL` clause is diffed as follows,
   rather than as a type change.  [PG] PostgreSQL cannot turn a plain
   column into a generated one, switch `STORED`/`VIRTUAL`, or drop the
   expression of a `VIRTUAL` column in place.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Generation added to a plain column | `ALTER TABLE t DROP COLUMN c; ALTER TABLE t ADD COLUMN c type GENERATED ALWAYS AS (expr) STORED\|VIRTUAL` | `DESTRUCTIVE` |
   | `STORED`↔`VIRTUAL` changed | Same drop and re-add | `DESTRUCTIVE` |
   | Expression changed, kind unchanged | `ALTER TABLE t ALTER COLUMN c SET EXPRESSION AS (expr)` | `CAUTION` |
   | Generation removed, column kept (`STORED`) | `ALTER TABLE t ALTER COLUMN c DROP EXPRESSION IF EXISTS` | `SAFE` |
   | Generation removed, column kept (`VIRTUAL`) | Drop and re-add as a plain column | `DESTRUCTIVE` |

   **Identity columns** [DPG]: a change to `GENERATED {ALWAYS | BY
   DEFAULT} AS IDENTITY [(options)]` is diffed as follows:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Identity added to an existing column | `ALTER TABLE t ALTER COLUMN c ADD GENERATED {ALWAYS\|BY DEFAULT} AS IDENTITY [(options)]` | `CAUTION` |
   | `ALWAYS`↔`BY DEFAULT` changed | `ALTER TABLE t ALTER COLUMN c SET GENERATED {ALWAYS\|BY DEFAULT}` | `SAFE` |
   | Sequence options changed | `ALTER TABLE t ALTER COLUMN c SET option ...`, one per changed option | `SAFE` |
   | Identity removed, column kept | `ALTER TABLE t ALTER COLUMN c DROP IDENTITY [IF EXISTS]` | `SAFE` |

### 7.5. Column-Level Grants

   `GRANTS { }` and `REVOKES { }` inside a `COLUMN name { }` block
   [PG: `GRANT`/`REVOKE privilege (col) ON TABLE ...`] apply to that
   column.  They follow the same additive model as table-level grants
   (Section 11.2): DPG emits `GRANT` and never revokes on its own.

```sql
GRANT privilege (col) ON TABLE schema.table TO role;
```

```sql
TABLE users ( id BIGINT ..., email TEXT, ssn TEXT )
{
    COLUMN email {
        GRANTS {
            SELECT TO reporting_role;
            SELECT TO analytics_role;
        }
    }

    COLUMN ssn {
        STORAGE extended;
        GRANTS  { SELECT TO compliance_role; }
        REVOKES { ALL PRIVILEGES FROM PUBLIC; }
    }

    GRANTS { SELECT, INSERT, UPDATE TO app_service; }
}
```

### 7.6. Column Renaming

   [DPG] A column rename is declared with `COLUMN new_name { RENAMED
   FROM old_name; }`.  The new name appears in the `( )` list and the
   `COLUMN` block; the old name appears only after `RENAMED FROM`.

```sql
TABLE users (
    email_address TEXT NOT NULL,
    CONSTRAINT uq_users_email UNIQUE (email_address)
)
{
    COLUMN email_address {
        RENAMED FROM email;
        COMMENT 'Verified email address';
    }
}
```

   **Resolution** (the same for every `RENAMED FROM`):

   1.  Old name in the snapshot, new name not: emit `ALTER TABLE users
       RENAME COLUMN email TO email_address` (`CAUTION`).
   2.  New name already in the snapshot: the rename has been applied;
       `RENAMED FROM` is a no-op.
   3.  Neither in the snapshot: error DPG-E021 (stale `RENAMED FROM`).

   After the rename, every index and constraint in the block MUST use
   the new name (DPG-E019).

   Objects resolve the same way, by qualified name:
   `TABLE user_accounts { RENAMED FROM users; }` emits `ALTER TABLE users
   RENAME TO user_accounts`.

   **Moves between schemas.**  For a schema-scoped object, the old name
   MAY be schema-qualified (`renamed-from-dir` takes a `qual-name`).  An
   unqualified old name is in the object's own schema.  When the old
   schema differs, the compiler emits `ALTER <kind> old_schema.name SET
   SCHEMA new_schema` and then, if the name also changed, `RENAME TO`.
   [PG] PostgreSQL has no single statement that does both.

   -   `TABLE archive.orders { RENAMED FROM public.orders; }` emits
       `ALTER TABLE public.orders SET SCHEMA archive`.
   -   `VIEW archive.order_summary { RENAMED FROM public.orders_summary; }`
       emits `ALTER VIEW public.orders_summary SET SCHEMA archive` and
       `ALTER VIEW archive.orders_summary RENAME TO order_summary`.

### 7.7. Indexes

   **PG equivalent:** [`CREATE INDEX`](https://www.postgresql.org/docs/current/sql-createindex.html)

   **Part 2 entry** [PG: `CREATE INDEX`]: an index is declared in the
   table's `INDEXES { }` block, or with the singular `INDEX` form
   (Section 4.8).  An entry is PostgreSQL's `CREATE INDEX` without
   `CREATE`, `INDEX` and `ON table`, which the block supplies, plus a
   DPG `RENAMED FROM` suffix:

```abnf
index-decl = [ "UNIQUE" WSP ] [ "CONCURRENTLY" WSP ] [ "ONLY" WSP ]
             index-name WSP
             <the rest of PostgreSQL's CREATE INDEX, from USING or
              the element list onward>
             [ WSP "RENAMED FROM" WSP index-name ] ";"
```

   `UNIQUE`, `CONCURRENTLY` and `ONLY` keep PostgreSQL's order.  The
   `INDEX` form writes `[UNIQUE] INDEX [CONCURRENTLY] [ONLY] name ...`,
   with `INDEX` where PostgreSQL puts it.

   Examples:

```sql
TABLE users ( email TEXT, status user_status, ... )
{
    INDEXES {
        idx_email             (email);
        UNIQUE idx_unique_slug (slug);
        idx_tenant_created    (tenant_id ASC, created_at DESC);
        idx_active_users      (email) WHERE (status = 'active');
        idx_location          USING gist (location);
        idx_lower_email       (lower(email));
        idx_covering          (user_id) INCLUDE (email, created_at);
        idx_brin              USING brin (created_at)
                                  WITH (pages_per_range = 128);
        idx_search_doc        USING gist (doc tsvector_ops(siglen = 32));
        UNIQUE idx_optional_email (email) NULLS NOT DISTINCT;
        idx_email_v2          (email) RENAMED FROM idx_email_old;
        CONCURRENTLY idx_payload (payload);
    }
}

UNIQUE INDEX idx_unique_slug (slug);
INDEX CONCURRENTLY idx_payload (payload);
```

   **Notes** [DPG]:

   -   An index is identified by its name within its schema; two
       different definitions under one name is error DPG-E005.
   -   `CONCURRENTLY` is written per index; there is no project-wide
       default.  On an index added to an existing table it emits `CREATE
       INDEX CONCURRENTLY`, a non-transactional `CAUTION` step run after
       `COMMIT`.  On a new table's or materialized view's index it has
       no effect: [PG] that index is created in the same transaction as
       the relation, where PostgreSQL refuses `CONCURRENTLY`; the linter
       warns (`concurrently-new-table`, Section 19.1).
   -   `WHERE` predicates and index expressions are compared as text,
       with runs of whitespace collapsed.
   -   `RENAMED FROM` renames the index only when nothing else about it
       changed; otherwise the index is dropped and recreated.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New index, new table or materialized view | `CREATE [UNIQUE] INDEX name ON ...`, in the same transaction | `SAFE` |
   | New index, existing table | `CREATE [UNIQUE] INDEX [CONCURRENTLY] [IF NOT EXISTS] name ON ...` | `CAUTION` (non-transactional with `CONCURRENTLY`) |
   | Renamed only (`RENAMED FROM`) | `ALTER INDEX old RENAME TO new` | `SAFE` |
   | Any other change (`INCLUDE` included) | Drop and recreate | `CAUTION` |
   | Index removed | `DROP INDEX [CONCURRENTLY] name` | `CAUTION` |

### 7.8. Row Level Security

   **PG equivalent:** [`ALTER TABLE ... ENABLE/FORCE ROW LEVEL SECURITY`](https://www.postgresql.org/docs/current/sql-altertable.html), [`CREATE POLICY`](https://www.postgresql.org/docs/current/sql-createpolicy.html)

   **Part 2:** `ENABLE ROW LEVEL SECURITY` and `FORCE ROW LEVEL
   SECURITY` [PG: `ALTER TABLE ... ENABLE`/`FORCE ROW LEVEL SECURITY`];
   `POLICIES { }` entries or the singular `POLICY` form [PG: `CREATE
   POLICY`].  A policy entry is PostgreSQL's `CREATE POLICY` without
   `CREATE POLICY` and `ON table`, plus a DPG `RENAMED FROM` suffix:

```abnf
rls-enable-dir = "ENABLE ROW LEVEL SECURITY"
rls-force-dir  = "FORCE ROW LEVEL SECURITY"

policy-decl    = policy-name
                 <the rest of PostgreSQL's CREATE POLICY after ON table:
                  [AS ...] [FOR ...] [TO ...] [USING (...)]
                  [WITH CHECK (...)]>
                 [ WSP "RENAMED FROM" WSP policy-name ] ";"
```

   Example:

```sql
TABLE orders ( ... )
{
    ENABLE ROW LEVEL SECURITY;
    FORCE ROW LEVEL SECURITY;

    POLICIES {
        view_own FOR SELECT
            USING (user_id = auth.uid());

        update_own FOR UPDATE
            USING      (user_id = auth.uid())
            WITH CHECK (user_id = auth.uid() AND status != 'locked');

        restrict_deleted AS RESTRICTIVE FOR ALL
            USING (deleted_at IS NULL);

        admin_all FOR ALL
            TO admin_role
            USING (true);
    }
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `ENABLE ROW LEVEL SECURITY` added | `ALTER TABLE t ENABLE ROW LEVEL SECURITY` | `SAFE` |
   | `FORCE ROW LEVEL SECURITY` added | `ALTER TABLE t FORCE ROW LEVEL SECURITY` | `SAFE` |
   | `FORCE` removed, `ENABLE` kept | `ALTER TABLE t NO FORCE ROW LEVEL SECURITY` | `SAFE` |
   | Both removed | `ALTER TABLE t DISABLE ROW LEVEL SECURITY` | `SAFE` |
   | New policy | `CREATE POLICY name ON t ...` | `SAFE` |
   | Only `TO`/`USING`/`WITH CHECK` differ | `ALTER POLICY name ON t [TO ...] [USING (...)] [WITH CHECK (...)]` | `SAFE` |
   | Renamed only (`RENAMED FROM`) | `ALTER POLICY old ON t RENAME TO new` | `SAFE` |
   | Renamed, and `TO`/`USING`/`WITH CHECK` differ | `ALTER POLICY old ON t RENAME TO new`, then `ALTER POLICY new ON t ...` | `SAFE` |
   | `FOR` or `AS PERMISSIVE`/`RESTRICTIVE` differs | `DROP POLICY name ON t; CREATE POLICY ...` | `CAUTION` |
   | Permissive policy removed | `DROP POLICY name ON t` | `SAFE` (only narrows access) |
   | Restrictive policy removed | `DROP POLICY name ON t` | `CAUTION` (widens access) |

   **Notes** [DPG]:

   -   `ALTER POLICY` is used whenever the change allows it, so a policy
       is never briefly absent.  [PG] `FOR` and `AS` cannot be altered,
       and a rename cannot be combined with other changes in one `ALTER
       POLICY`.
   -   A policy is identified by its table and name; `RENAMED FROM`
       takes a bare name, since a policy cannot move to another table.
   -   RLS settings and policies on a partitioned table are not copied to
       its partitions; DPG doesn't propagate them.

### 7.9. Triggers

   **PG equivalent:** [`CREATE [CONSTRAINT] TRIGGER`](https://www.postgresql.org/docs/current/sql-createtrigger.html)

   **Part 2 entry** [PG: `CREATE TRIGGER`]: a trigger is declared in the
   table's `TRIGGERS { }` block, or with the singular `TRIGGER` form
   (Section 4.8).  An entry is PostgreSQL's `CREATE [CONSTRAINT]
   TRIGGER` without `CREATE`, `TRIGGER` and `ON table`, with
   `CONSTRAINT` written after the name, plus DPG suffixes:

```abnf
trigger-decl = trigger-name [ WSP "CONSTRAINT" ] WSP
               <the rest of PostgreSQL's CREATE TRIGGER after the name,
                without ON table>
               [ WSP trigger-enable-state ]
               [ WSP depends-on-extension-dir ]
               [ WSP "RENAMED FROM" WSP trigger-name ]
               ";"

trigger-enable-state = "DISABLED" / "ENABLE REPLICA" / "ENABLE ALWAYS"
```

   -   `trigger-enable-state` [PG: `ALTER TABLE ... DISABLE`/`ENABLE
       REPLICA`/`ENABLE ALWAYS TRIGGER`].  Omitted means enabled,
       PostgreSQL's default.
   -   `DEPENDS ON EXTENSION ext` [PG: `ALTER TRIGGER ... DEPENDS ON
       EXTENSION`].
   -   `RENAMED FROM old` [DPG], a bare name: a trigger cannot move to
       another table.

   Example:

```sql
TABLE users ( ... )
{
    TRIGGERS {
        before_insert BEFORE INSERT
            FOR EACH ROW
            EXECUTE FUNCTION set_defaults();

        after_email_change AFTER UPDATE OF email
            FOR EACH ROW
            WHEN (OLD.email IS DISTINCT FROM NEW.email)
            EXECUTE FUNCTION notify_email_change();

        audit_changes AFTER INSERT OR UPDATE OR DELETE
            REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
            FOR EACH STATEMENT
            EXECUTE FUNCTION audit_table_changes();

        check_ref CONSTRAINT AFTER INSERT OR UPDATE
            FROM orders
            DEFERRABLE INITIALLY DEFERRED
            FOR EACH ROW
            EXECUTE FUNCTION check_ref_integrity();
    }
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New trigger | `CREATE [CONSTRAINT] TRIGGER name ... ON t ...` | `SAFE` |
   | Trigger changed | `DROP TRIGGER name ON t; CREATE TRIGGER ...` | `CAUTION` |
   | Renamed only (`RENAMED FROM`) | `ALTER TRIGGER old ON t RENAME TO new` | `SAFE` |
   | Enable state changed only | `ALTER TABLE t ENABLE/DISABLE [REPLICA\|ALWAYS] TRIGGER name` | `SAFE` |
   | `DEPENDS ON EXTENSION` changed | `ALTER TRIGGER name ON t [NO] DEPENDS ON EXTENSION ext` | `SAFE` |
   | Trigger removed | `DROP TRIGGER name ON t` | `CAUTION` (stops whatever the trigger enforced) |

   **Notes** [DPG]:

   -   A trigger is identified by its table and name.
   -   When a rename comes with another change that recreates the
       trigger, it is created under the new name directly.  [PG] `ALTER
       TRIGGER` cannot rename and change `DEPENDS ON EXTENSION` in one
       statement, so those are emitted as two statements, the rename
       first.
   -   Trigger clauses are passed to PostgreSQL unchanged; DPG does not
       check clause combinations.  PostgreSQL reports an invalid one when
       the migration runs.
   -   `CREATE RULE` is out of scope (Section 23), so rule enable states
       are not modelled.

### 7.10. Table-Level Grants and Revocations

   **Part 2:** `GRANTS { }` [PG: `GRANT ... ON TABLE`] and `REVOKES { }`
   [PG: `REVOKE ... ON TABLE`].  Every other object kind that accepts
   grants uses the same blocks.

```abnf
grants-block      = "GRANTS" WSP "{" *( grant-entry ";" ) "}"
revocations-block = "REVOKES" WSP "{" *( revoke-entry ";" ) "}"

grant-entry  = privilege-list WSP "TO" WSP role-list
               [ WSP "WITH GRANT OPTION" ]
               [ WSP "GRANTED BY" WSP role-spec ]
revoke-entry = ( privilege-list / "ALL PRIVILEGES" ) WSP
               "FROM" WSP role-list
               [ WSP "GRANTED BY" WSP role-spec ]
               [ WSP "CASCADE" ]

privilege-list = privilege *( "," privilege )
privilege      = "SELECT" / "INSERT" / "UPDATE" / "DELETE" /
                 "TRUNCATE" / "REFERENCES" / "TRIGGER" /
                 "USAGE" / "EXECUTE" / "CREATE" / "CONNECT" /
                 "TEMPORARY" / "MAINTAIN" / "ALL" / "ALL PRIVILEGES"
```

   Grants follow the additive model (Section 11.2).  [PG] Which
   privileges apply to which object kind is PostgreSQL's rule; it rejects
   a privilege that doesn't apply.  `MAINTAIN` needs PostgreSQL 17
   (Section 3.8).

### 7.11. Table Lifecycle Directives

   The common directives (Section 4.2.1) behave on a table as follows:

   -   `PROTECTED` [DPG]: the compiler refuses to emit `DROP TABLE` for
       the table, even when it is absent from the desired state
       (DPG-E022).  Removing it requires removing `PROTECTED` first.
   -   `DEPRECATED 'msg'` [DPG]: the table comment gets a `[DEPRECATED]`
       prefix, and the linter warns on references to the table
       (`warn_on_deprecated`).
   -   `DROP CASCADE` [DPG]: the table is dropped with `CASCADE`,
       overriding `default_drop_behavior`.
   -   `RENAMED FROM` [DPG]: Section 7.6.

   **`REPLICA IDENTITY` and `CLUSTER ON`** [PG: `ALTER TABLE ... REPLICA
   IDENTITY` / `CLUSTER ON`]:

```abnf
replica-identity-dir = "REPLICA IDENTITY" WSP
                        ( "DEFAULT" / "FULL" / "NOTHING"
                        / "USING INDEX" WSP index-name )
cluster-dir           = "CLUSTER ON" WSP index-name
```

```sql
TABLE orders ( ... )
{
    REPLICA IDENTITY FULL;
    CLUSTER ON idx_orders_created_at;
}
```

   Omitting `REPLICA IDENTITY` means `DEFAULT`.  `CLUSTER ON` records
   the index a manual `CLUSTER` would use; the `CLUSTER` command itself
   is out of scope (Section 23).

   **Changes after creation** [DPG]: these Part 1 clauses are changed in
   place rather than by recreating the table:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `REPLICA IDENTITY` changed | `ALTER TABLE t REPLICA IDENTITY ...` | `SAFE` |
   | `CLUSTER ON` added / removed | `ALTER TABLE t CLUSTER ON idx` / `SET WITHOUT CLUSTER` | `SAFE` |
   | `OF type_name` gained, changed or removed | `ALTER TABLE t OF type_name` / `NOT OF` | `CAUTION` |
   | `USING method` changed | `ALTER TABLE t SET ACCESS METHOD method` (`DEFAULT` when removed) | `CAUTION` |
   | `INHERITS` parent added / removed | `ALTER TABLE t INHERIT parent` / `NO INHERIT parent` | `SAFE` |
   | `WITH (...)` changed | `ALTER TABLE t SET (...)` / `RESET (...)` | `CAUTION` |
   | `TABLESPACE` changed | `ALTER TABLE t SET TABLESPACE ts` | `SAFE` |
   | `UNLOGGED` toggled | `ALTER TABLE t SET LOGGED` / `SET UNLOGGED` | `CAUTION` |

### 7.12. Unlogged and Foreign Tables

   **Unlogged tables** are declared with `UNLOGGED TABLE` (Part 1) and
   emit `CREATE UNLOGGED TABLE`.  Toggling it emits `ALTER TABLE ... SET
   LOGGED`/`SET UNLOGGED` (`CAUTION`, Section 7.11).

```sql
UNLOGGED TABLE session_cache (
    key   TEXT NOT NULL PRIMARY KEY,
    value JSONB
);
```

   **Temporary tables** [DPG] are session-scoped, and DPG does not
   manage them.  `TEMPORARY TABLE` in a `.dpg` file is error DPG-E023.

   **Foreign tables:** `SERVER` and `OPTIONS` are Part 1 clauses.

```sql
FOREIGN TABLE remote_events (
    id         BIGINT,
    payload    JSONB,
    created_at TIMESTAMPTZ
) SERVER log_server OPTIONS (table_name 'events', schema_name 'public')
{
    COLUMN id { COMMENT 'Remote event primary key'; }
    GRANTS { SELECT TO app_readonly; }
}
```

   **Diffing semantics:** a foreign table is diffed as a table (Section
   21), with two additions:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `OPTIONS` changed | `ALTER FOREIGN TABLE t OPTIONS (ADD/SET/DROP key 'value', ...)` | `SAFE` |
   | `SERVER` changed | `DROP FOREIGN TABLE t; CREATE FOREIGN TABLE t ... SERVER new_server ...` | `DESTRUCTIVE` |

   [PG] `ALTER FOREIGN TABLE` cannot change the server.


### 7.13. Partitioned Tables

   **PG equivalent:** [`CREATE TABLE ... PARTITION BY`, `CREATE TABLE ... PARTITION OF`](https://www.postgresql.org/docs/current/sql-createtable.html), [`ALTER TABLE ... ATTACH`/`DETACH PARTITION`](https://www.postgresql.org/docs/current/sql-altertable.html)

   **Part 1:** `PARTITION BY` is written in the parent's Part 1, as in
   PostgreSQL.

   **Part 2:** `PARTITIONS { }` [PG: `CREATE TABLE ... PARTITION OF`]
   declares the partitions.  An entry is the partition's name followed
   by PostgreSQL's partition bound (`FOR VALUES ...` or `DEFAULT`); a
   partition never restates its parent's columns.

```abnf
partitions-block = "PARTITIONS" WSP "{" *partition-decl "}"

partition-decl   = [ "FOREIGN" WSP ] partition-name WSP
                   <PostgreSQL's partition_bound_spec>
                   [ WSP "SERVER" WSP identifier
                     [ WSP "OPTIONS" WSP "(" option-list ")" ] ]
                   [ WSP "RENAMED FROM" WSP partition-name ]
                   [ WSP <PostgreSQL's PARTITION BY clause> WSP
                     "{" "PARTITIONS" WSP "{" *partition-decl "}" "}" ]
                   ";"
                 / attached-partition-decl
```

   -   `FOREIGN name ... SERVER s [OPTIONS (...)]` makes the partition a
       foreign table [PG: `CREATE FOREIGN TABLE ... PARTITION OF`].
   -   A partition MAY be partitioned again, with its own `PARTITION BY`
       and nested `PARTITIONS` block, to any depth.
   -   A partition MAY carry a `{ }` body with its own constraints
       (Section 7.3).
   -   `RENAMED FROM old` [DPG] renames a partition already attached to
       this parent, with a bare name (a partition is in its parent's
       schema).

```sql
TABLE events ( id BIGINT, region TEXT, payload JSONB, created_at TIMESTAMPTZ )
    PARTITION BY RANGE (created_at)
{
    PARTITIONS {
        events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')
            PARTITION BY LIST (region) {
                PARTITIONS {
                    events_2024_us FOR VALUES IN ('us-east', 'us-west');
                    events_2024_eu FOR VALUES IN ('eu-west', 'eu-central');
                }
            };
        FOREIGN events_archive DEFAULT
            SERVER archive_server OPTIONS (table_name 'events_archive');
    }
}
```

   **Attaching and detaching existing tables** [DPG]: to turn an
   existing standalone table into a partition, or a partition back into a
   standalone table, without recreating it:

```abnf
; a PARTITIONS { } entry
attached-partition-decl = "ATTACHED FROM" WSP table-ref WSP
                          <PostgreSQL's partition_bound_spec> ";"

; a standalone TABLE { } block directive
detached-from-dir = "DETACHED FROM" WSP table-ref [ WSP "CONCURRENTLY" ] ";"
```

   -   `ATTACHED FROM existing_table FOR VALUES ...`, where the table is
       a standalone table in the snapshot, emits `ALTER TABLE parent
       ATTACH PARTITION existing_table FOR VALUES ...`.
   -   A standalone `TABLE` with `DETACHED FROM parent [CONCURRENTLY]`,
       where the table is a partition of `parent` in the snapshot, emits
       `ALTER TABLE parent DETACH PARTITION name [CONCURRENTLY]` instead
       of dropping the partition.
   -   `RENAMED FROM` cannot be combined with `ATTACHED FROM` or
       `DETACHED FROM` on the same entry; that takes two migrations.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New partition | `CREATE TABLE name PARTITION OF parent FOR VALUES ...` | `SAFE` |
   | Partition renamed only (`RENAMED FROM`) | `ALTER TABLE old RENAME TO new` | `CAUTION` |
   | Partition attached (`ATTACHED FROM`) | `ALTER TABLE parent ATTACH PARTITION name FOR VALUES ...` | `CAUTION` |
   | Partition detached (`DETACHED FROM`) | `ALTER TABLE parent DETACH PARTITION name [CONCURRENTLY]` | `CAUTION` (non-transactional with `CONCURRENTLY`) |
   | Partition removed (absent, not detached) | `DROP TABLE name` | `DESTRUCTIVE` |
   | Partition strategy changed | Instruction-only step (see below) | `DESTRUCTIVE` |
   | Parent constraint dropped, kept by a direct partition (Section 7.3) | `ALTER TABLE ONLY parent DROP CONSTRAINT name` | `SAFE` |
   | Partition's own constraint added (Section 7.3) | `ALTER TABLE partition ADD CONSTRAINT name ...` | `CAUTION` |
   | Partition's own constraint dropped (Section 7.3) | `ALTER TABLE partition DROP CONSTRAINT name` | `DESTRUCTIVE` |

   **Partition strategy change** [DPG]: [PG] a table's partitioning
   cannot be altered; it has to be rebuilt:

   1.  Create the new partitioned table with the target strategy.
   2.  Create all declared partitions.
   3.  `INSERT INTO new_table SELECT * FROM old_table`.
   4.  Verify row counts match.
   5.  `DROP TABLE old_table`.
   6.  `ALTER TABLE new_table RENAME TO old_table`.
   7.  Recreate all indexes, constraints, grants, and policies.

   DPG does not run this sequence.  It emits one `DESTRUCTIVE`
   instruction-only step (Section 17.3) describing it, and `dpg apply`
   stops until `--approve-partition-rebuild` acknowledges it.

   **Notes** [DPG]: an index declared on the parent is created once on
   the parent; [PG] PostgreSQL creates it on every partition, so DPG
   emits no per-partition `CREATE INDEX`.  RLS and policies are not
   propagated to partitions (Section 7.8).

---

## 8. Views

### 8.1. Regular Views

   **PG equivalent:** [`CREATE VIEW`](https://www.postgresql.org/docs/current/sql-createview.html)

   **Part 1:** PostgreSQL's `CREATE VIEW` without `CREATE` (and without
   `OR REPLACE`, which DPG decides itself).  `WITH [LOCAL | CASCADED]
   CHECK OPTION` ends Part 1, as in PostgreSQL.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `DEPRECATED`,
   `GRANTS`, `REVOKES`, `SECURITY LABEL`, `NAME MAP` (Section 4.2.1).

```abnf
view-block     = *( view-directive ";" )
view-directive = owner-dir / comment-dir / renamed-from-dir
               / deprecated-dir / grants-block / revocations-block
               / security-label-decl
```

   **Temporary views** [DPG] are excluded on the same terms as
   temporary tables (Section 7.12): `TEMPORARY VIEW` is error DPG-E023.

   Examples:

```sql
SCHEMA public {
    VIEW active_users AS
        SELECT id, email, created_at
        FROM users
        WHERE status = 'active' AND deleted_at IS NULL;

    VIEW user_summary (user_id, email, order_count) AS
        SELECT u.id, u.email, COUNT(o.id)
        FROM users u
        LEFT JOIN orders o ON o.user_id = u.id
        GROUP BY u.id, u.email;

    VIEW secure_view WITH (security_barrier = true) AS
        SELECT id, email FROM users WHERE tenant_id = current_tenant();

    VIEW active_orders AS
        SELECT * FROM orders WHERE status != 'cancelled'
        WITH LOCAL CHECK OPTION;

    VIEW admin_summary AS
        SELECT id, email, created_at FROM users WHERE role = 'admin' {
        COMMENT 'Admin user summary view';
        GRANTS { SELECT TO app_readonly; }
    }
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New view | `CREATE VIEW ...` | `SAFE` |
   | Query changed, same output column list | `CREATE OR REPLACE VIEW name [WITH (options)] AS ...`, restating every declared option | `SAFE` |
   | View options changed (`WITH (...)` or check option), query unchanged | `ALTER VIEW name SET (...)` / `RESET (...)` | `CAUTION` |
   | Output column list changed | `DROP VIEW CASCADE; CREATE VIEW` | `DESTRUCTIVE` |
   | Owner changed | `ALTER VIEW name OWNER TO role` | `SAFE` |
   | Comment changed | `COMMENT ON VIEW name IS '...'` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER VIEW old RENAME TO new`; `SET SCHEMA` when the schema differs (Section 7.6) | `CAUTION` / `SAFE` |
   | View removed | `DROP VIEW name [CASCADE]` | `DESTRUCTIVE` |

   **Notes:**

   -   [PG] `WITH [LOCAL | CASCADED] CHECK OPTION` is the view option
       `check_option = local | cascaded`, and PostgreSQL stores both
       spellings the same way, so DPG treats them as one declaration.  A
       view option without a value (`WITH (security_barrier)`) means
       `true`.  [PG] `CREATE OR REPLACE VIEW` resets any option it does
       not restate.
   -   [DPG] **Output columns.**  The output column list is compared by
       name and position.  It is known for a plain `SELECT` (or a set
       operation, from its leftmost branch) without `*` in its select
       list.  When it can't be determined, a query change is emitted as
       `CREATE OR REPLACE VIEW`, and PostgreSQL rejects it if the columns
       did change.
   -   [DPG] **Comparison with the live catalog.**  [PG] PostgreSQL
       stores a view's query with every `*` expanded and keeps no
       separate column-name list.  When a declared view is compared with
       one read from the live catalog (`dpg verify`, `dpg plan --live`),
       a declared query whose select list contains `*` is compared
       without its select list, and the column-name list is not compared.
       A change made outside DPG to such a view's select list is
       therefore not detected.

### 8.2. Materialized Views

   **PG equivalent:** [`CREATE MATERIALIZED VIEW`](https://www.postgresql.org/docs/current/sql-creatematerializedview.html)

   **Part 1:** PostgreSQL's `CREATE MATERIALIZED VIEW` without `CREATE`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `DEPRECATED`,
   `GRANTS`, `REVOKES`, `SECURITY LABEL`, `NAME MAP` (Section 4.2.1), and
   `INDEXES { }` [PG: `CREATE INDEX`] (Section 7.7).

```abnf
matview-block     = *( matview-directive ";" )
matview-directive = owner-dir / comment-dir / renamed-from-dir
                  / deprecated-dir / indices-block / grants-block
                  / revocations-block / security-label-decl
```

   Example:

```sql
SCHEMA analytics {
    MATERIALIZED VIEW product_stats
    WITH (fillfactor = 90)
    TABLESPACE analytics_space AS
        SELECT product_id, COUNT(*) AS purchases, AVG(price) AS avg_price
        FROM order_items
        GROUP BY product_id
    WITH NO DATA {
        INDEXES { idx_product_stats_id (product_id); }
        GRANTS  { SELECT TO app_readonly; }
    }
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New materialized view | `CREATE MATERIALIZED VIEW ...` | `SAFE` |
   | Query changed | `DROP MATERIALIZED VIEW; CREATE MATERIALIZED VIEW` | `DESTRUCTIVE` |
   | `TABLESPACE` changed | `ALTER MATERIALIZED VIEW name SET TABLESPACE ts` | `SAFE` |
   | `WITH (...)` changed | `ALTER MATERIALIZED VIEW name SET (...)` / `RESET (...)` | `CAUTION` |
   | `WITH NO DATA` changed | Instruction-only note to refresh the view | `SAFE` |
   | Owner, comment, rename | As for a view (Section 8.1), with `ALTER MATERIALIZED VIEW` | as Section 8.1 |
   | Removed | `DROP MATERIALIZED VIEW name [CASCADE]` | `DESTRUCTIVE` |

   `REFRESH MATERIALIZED VIEW` is a runtime operation and out of scope
   (Section 23).

### 8.3. Recursive Views

   **PG equivalent:** [`CREATE RECURSIVE VIEW`](https://www.postgresql.org/docs/current/sql-createview.html)

   **Part 1:** PostgreSQL's `CREATE RECURSIVE VIEW` without `CREATE`.
   **Part 2:** as Section 8.1.

```sql
SCHEMA public {
    RECURSIVE VIEW org_tree (id, parent_id, depth, path) AS
        SELECT id, parent_id, 0, ARRAY[id]
        FROM departments WHERE parent_id IS NULL
        UNION ALL
        SELECT d.id, d.parent_id, t.depth + 1, t.path || d.id
        FROM departments d JOIN org_tree t ON d.parent_id = t.id;
}
```

   Diffing is as for regular views.  [DPG] Adding or removing
   `RECURSIVE` drops and recreates the view (`DESTRUCTIVE`).

---

## 9. Functions and Procedures

### 9.1. Function Declaration Syntax

   **PG equivalent:** [`CREATE FUNCTION`](https://www.postgresql.org/docs/current/sql-createfunction.html)

   **Part 1:** PostgreSQL's `CREATE FUNCTION` without `CREATE` (and
   without `OR REPLACE`, which DPG decides itself): arguments, `RETURNS`,
   every attribute, and the body in any of PostgreSQL's forms
   (dollar-quoted, `AS 'obj_file', 'symbol'`, `BEGIN ATOMIC ... END`).

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `DEPRECATED`,
   `GRANTS`, `REVOKES`, `SECURITY LABEL`, `NAME MAP`, `DEPENDS ON
   EXTENSION` (Section 4.2.1).

```abnf
func-block     = *( func-directive ";" )
func-directive = comment-dir / owner-dir / grants-block / revocations-block
               / deprecated-dir / renamed-from-dir / security-label-decl
               / depends-on-extension-dir

depends-on-extension-dir = [ "NO" WSP ] "DEPENDS ON EXTENSION" WSP identifier
```

   **Notes:**

   -   [DPG] Function identity is `(schema, name, argument types)`,
       excluding `OUT` arguments as PostgreSQL's overloading rules do.
   -   [DPG] A `LANGUAGE C` or `internal` body (`AS 'obj_file',
       'link_symbol'` / `AS 'name'`) is compared as an attribute, not
       through the body hash (Section 9.5).
   -   [DPG] `DEPENDS ON EXTENSION` is diffed as a set: an added entry
       emits `ALTER FUNCTION name(...) DEPENDS ON EXTENSION ext`, a
       removed one `NO DEPENDS ON EXTENSION ext` (both `SAFE`).
   -   [DPG] `TRANSFORM FOR TYPE` refers to an installed transform;
       `CREATE TRANSFORM` itself is out of scope (Section 23).

   **Diffing semantics** (Section 9.5 for the body):

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New function | `CREATE FUNCTION ...` | `SAFE` |
   | Body or a replaceable attribute changed | `CREATE OR REPLACE FUNCTION ...` | `SAFE` |
   | Argument list or return type changed | `DROP FUNCTION IF EXISTS` + `CREATE FUNCTION` | `DESTRUCTIVE` |
   | Owner changed | `ALTER FUNCTION name(...) OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER FUNCTION old(...) RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | Function removed | `DROP FUNCTION name(...)` | `DESTRUCTIVE` |

### 9.2. Function Attributes

   [PG] Every `CREATE FUNCTION` attribute (`VOLATILE`/`STABLE`/
   `IMMUTABLE`, `STRICT`, `SECURITY DEFINER`, `LEAKPROOF`, `PARALLEL`,
   `COST`, `ROWS`, `SUPPORT`, `SET`, `WINDOW`, `TRANSFORM`) is written
   and behaves as in PostgreSQL.  [PG] Setting `LEAKPROOF` needs a
   superuser.

   [DPG] The linter warns when a `SECURITY DEFINER` function has no
   `SET search_path` (rule `security-definer-search-path`).

   Examples:

```sql
SCHEMA public {
    FUNCTION active_user_count() RETURNS BIGINT
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$
        SELECT COUNT(*) FROM users WHERE status = 'active';
    $$;

    FUNCTION get_user(p_email TEXT) RETURNS users
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path = public
    AS $$
    DECLARE v_user users;
    BEGIN
        SELECT * INTO STRICT v_user FROM users WHERE email = p_email;
        RETURN v_user;
    EXCEPTION
        WHEN NO_DATA_FOUND THEN
            RAISE EXCEPTION 'User not found: %', p_email;
    END;
    $$ {
        COMMENT 'Fetch a user record by verified email address';
        GRANTS { EXECUTE TO app_service; }
    }

    -- Named dollar-quote, for a body that contains $$
    FUNCTION format_price(p_amount NUMERIC) RETURNS TEXT
    LANGUAGE plpgsql IMMUTABLE STRICT
    AS $func$
    BEGIN
        RETURN '$' || TO_CHAR(p_amount, 'FM999,999,990.00');
    END;
    $func$ {
        GRANTS { EXECUTE TO app_readonly, app_service; }
    }
}
```

### 9.3. Procedures

   **PG equivalent:** [`CREATE PROCEDURE`](https://www.postgresql.org/docs/current/sql-createprocedure.html)

   **Part 1:** PostgreSQL's `CREATE PROCEDURE` without `CREATE`.
   **Part 2:** as for functions (Section 9.1).

```sql
SCHEMA public {
    PROCEDURE process_settlements()
    LANGUAGE plpgsql SECURITY DEFINER
    AS $$
    DECLARE v_id settlements.id%TYPE;
    BEGIN
        FOR v_id IN SELECT id FROM settlements WHERE processed = false LOOP
            PERFORM settle_order(v_id);
            COMMIT;
        END LOOP;
    END;
    $$ {
        GRANTS { EXECUTE TO scheduler_role; }
    }
}
```

   Diffing is as for functions (Sections 9.1, 9.5), with `PROCEDURE` in
   place of `FUNCTION`.  Identity is `(schema, name, argument types)`,
   excluding `OUT` arguments.

### 9.4. Aggregate Functions

   **PG equivalent:** [`CREATE AGGREGATE`](https://www.postgresql.org/docs/current/sql-createaggregate.html)

   **Part 1:** PostgreSQL's `CREATE AGGREGATE` without `CREATE`: the
   input types and the option list, both in `( )`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `DEPRECATED`,
   `GRANTS`, `REVOKES`, `SECURITY LABEL`, `NAME MAP` (Section 4.2.1).
   [PG] `ALTER AGGREGATE` has no `DEPENDS ON EXTENSION`, so an aggregate
   block does not accept it.

```sql
SCHEMA public {
    AGGREGATE product (DOUBLE PRECISION) (
        SFUNC    = float8mul,
        STYPE    = DOUBLE PRECISION,
        INITCOND = '1'
    )
    {
        COMMENT 'Multiplicative aggregate over DOUBLE PRECISION values';
        GRANTS { EXECUTE TO app_service; }
    }
}
```

   **Diffing semantics:** identity is `(schema, name, input types)`.
   [PG] `ALTER AGGREGATE` can only rename, change the owner and move the
   schema.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New aggregate | `CREATE AGGREGATE ...` | `SAFE` |
   | Any option changed | `DROP AGGREGATE IF EXISTS` + `CREATE AGGREGATE` | `DESTRUCTIVE` |
   | Owner changed | `ALTER AGGREGATE name (types) OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER AGGREGATE name (types) RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | Aggregate removed | `DROP AGGREGATE name (types)` | `DESTRUCTIVE` |

### 9.5. Function Body Diffing Semantics

   [DPG] The snapshot stores a SHA-256 hash of the normalised function
   body (Section 16.3).  Normalisation depends on the `LANGUAGE`:

   -   **`LANGUAGE SQL`** (dollar-quoted or `BEGIN ATOMIC`): the body is
       parsed with PostgreSQL's grammar and deparsed again, then hashed,
       which absorbs whitespace, quoting and formatting differences.  If
       it doesn't parse, the plain normalisation below is used.
   -   **`LANGUAGE plpgsql`:** the body is parsed with PostgreSQL's
       PL/pgSQL grammar, as part of the whole statement so parameter
       references resolve.  Source positions are removed and every
       embedded SQL expression and statement is deparsed again before
       hashing, which absorbs whitespace, blank-line and comment
       differences.  A fragment that doesn't parse keeps its text; the
       build is never blocked.
   -   **Every other language:** leading and trailing whitespace is
       stripped and internal runs of whitespace are collapsed to one
       space.

   Any other change to the body changes the hash and emits `CREATE OR
   REPLACE FUNCTION`.  This is syntactic, not semantic: an equivalent
   but differently structured body (a rewritten join, `a OR b` written
   as `NOT (NOT a AND NOT b)`) counts as a change.

   **Attribute and signature changes:**

   -   An attribute `CREATE OR REPLACE FUNCTION` can change (`LANGUAGE`,
       `SECURITY DEFINER`, `STRICT`, volatility, `LEAKPROOF`, `PARALLEL`,
       `COST`, `ROWS`, `SET`): `CREATE OR REPLACE FUNCTION` (`SAFE`).
   -   The argument list or return type: [PG] `CREATE OR REPLACE` cannot
       change them, so DPG emits `DROP FUNCTION IF EXISTS` and `CREATE
       FUNCTION` (`DESTRUCTIVE`).

---

## 10. Sequences

   **PG equivalent:** [`CREATE [UNLOGGED] SEQUENCE`](https://www.postgresql.org/docs/current/sql-createsequence.html)

   **Part 1:** PostgreSQL's `CREATE SEQUENCE` without `CREATE`, plus one
   DPG option:

```abnf
sequence-option =/ "RESTART" [ WSP "WITH" WSP integer ]   ; [DPG]
```

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `GRANTS`, `REVOKES`,
   `SECURITY LABEL`, `NAME MAP` (Section 4.2.1).

```abnf
sequence-block = *( ( owner-dir / comment-dir / renamed-from-dir
                    / grants-block / revocations-block
                    / security-label-decl ) ";" )
```

```sql
SCHEMA public {
    SEQUENCE order_number_seq
        AS BIGINT
        START WITH  10000
        INCREMENT BY 1
        MINVALUE     10000
        MAXVALUE     99999999
        CACHE        50
        NO CYCLE
        OWNED BY orders.order_number;
}
```

   **Notes:**

   -   [DPG] `RESTART [WITH n]` resets the sequence's current value.
       [PG] `RESTART` is an action, not state PostgreSQL keeps, so it is
       not stored in the snapshot.  While it is in the source, every
       plan emits `ALTER SEQUENCE name RESTART [WITH n]` (`CAUTION`)
       followed by a note to remove it once applied.
   -   [DPG] `OWNED BY NONE` detaches the sequence from its column;
       omitting `OWNED BY` leaves its ownership unchanged.
   -   [DPG] A sequence backing an identity or `SERIAL` column is
       managed by PostgreSQL and MUST NOT be declared.  The linter warns
       when a declared sequence is named `<table>_<column>_seq` for such
       a column (rule `serial-sequence-declared`).
   -   [PG] `UNLOGGED` needs PostgreSQL 15 (Section 3.8).

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New sequence | `CREATE SEQUENCE ...` | `SAFE` |
   | Increment/min/max/start/cache/cycle changed | `ALTER SEQUENCE name ...` | `SAFE` |
   | `OWNED BY` changed (to or from `NONE` included) | `ALTER SEQUENCE name OWNED BY {table.col\|NONE}` | `SAFE` |
   | `RESTART [WITH n]` in the source | `ALTER SEQUENCE name RESTART [WITH n]`, every plan while present | `CAUTION` |
   | `UNLOGGED` toggled | `ALTER SEQUENCE name SET LOGGED` / `SET UNLOGGED` | `CAUTION` |
   | Owner / comment changed | `ALTER SEQUENCE name OWNER TO role` / `COMMENT ON SEQUENCE ...` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER SEQUENCE old RENAME TO new`; `SET SCHEMA` when the schema differs (Section 7.6) | `CAUTION` / `SAFE` |
   | `AS type` changed | `DROP SEQUENCE; CREATE SEQUENCE` | `DESTRUCTIVE` |
   | Sequence removed | `DROP SEQUENCE name` | `DESTRUCTIVE` |

---

## 11. Access Control

### 11.1. Roles

   Roles are cluster-level objects, declared in the cluster objects
   directory.

   **PG equivalent:** [`CREATE ROLE`](https://www.postgresql.org/docs/current/sql-createrole.html)

   **Part 1:** PostgreSQL's `CREATE ROLE` without `CREATE`, including
   `IN ROLE`, `ROLE` and `ADMIN` member lists.  [DPG] `PASSWORD` MAY
   contain `{{<secret-uri>}}` placeholders (below).

   **Part 2:** `COMMENT`, `RENAMED FROM`, `SECURITY LABEL`, `NAME MAP`
   (Section 4.2.1), and:

   | Directive | Origin |
   |-----------|--------|
   | `IN ROLE r` / `ROLE r` / `ADMIN r` with `WITH ADMIN`, `WITH INHERIT`, `WITH SET` | [PG: `GRANT role TO role WITH ...`] |
   | `SET param {TO \| =} value` / `SET param FROM CURRENT` / `RESET param` / `RESET ALL`, each with optional `IN DATABASE db` | [PG: `ALTER ROLE ... [IN DATABASE db] SET` / `RESET`] |

```abnf
role-block = *( ( comment-dir / renamed-from-dir / security-label-decl
                / membership-dir / role-config-dir ) ";" )

membership-dir = ( "IN ROLE" / "ROLE" / "ADMIN" ) WSP identifier
                 *( WSP "WITH" WSP membership-opt )
membership-opt = "ADMIN" WSP ( "OPTION" / boolean )
               / "INHERIT" WSP boolean
               / "SET" WSP boolean

role-config-dir = "SET" WSP identifier WSP ( "TO" / "=" ) WSP config-value
                    [ WSP "IN DATABASE" WSP identifier ]
                / "SET" WSP identifier WSP "FROM CURRENT"
                    [ WSP "IN DATABASE" WSP identifier ]
                / "RESET" WSP ( identifier / "ALL" )
                    [ WSP "IN DATABASE" WSP identifier ]

config-value = string-literal / integer / boolean / identifier
```

   Example:

```sql
-- production/cluster/roles.dpg

ROLE app_readonly NOLOGIN
{
    COMMENT 'Read-only access for reporting tools';
}

ROLE app_service
    LOGIN
    PASSWORD '{{vault:secret/roles/app_service#pw}}'
    CONNECTION LIMIT 20
    VALID UNTIL '2030-01-01'
{
    SET statement_timeout = '30s';
}

ROLE app_admin
    LOGIN
    INHERIT
    IN ROLE pg_read_all_stats
{
    ROLE app_readonly WITH INHERIT FALSE;
}
```

   **Notes** [DPG]:

   -   Only options the source sets are managed; an omitted option is
       never compared with PostgreSQL's default.
   -   **Passwords.**  Each `{{<secret-uri>}}` placeholder in `PASSWORD`
       is resolved (Appendix D.1) immediately before the `CREATE ROLE` or
       `ALTER ROLE ... PASSWORD` runs, never during `plan` or `diff`; a
       value without placeholders is used as written.  The linter
       reports a password with no placeholder as an error (rule
       `hardcoded-role-password`, `forbid_hardcoded_passwords`).
   -   **Password changes** are detected offline: the snapshot stores a
       hash of the declared text (placeholder or literal, never the
       resolved value).  [PG] Passwords are never read back from the
       catalog, since PostgreSQL shows them only to superusers, so a
       comparison with the live catalog (`dpg plan --live`) does not
       compare passwords.
   -   **Undeclared roles.**  Roles are shared by every database on the
       cluster.  In a comparison with the live catalog (`dpg plan
       --live`, `dpg verify`), a role that is neither declared nor
       ignored (Section 3.9) is reported as a warning and otherwise
       left alone; it is never dropped and is not drift.  `DROP ROLE`
       is emitted only for a role removed from the source since the
       snapshot was written.
   -   **Memberships** are synced like any other role property:
       removing one emits `REVOKE`, unlike the additive model for object
       grants (Section 11.2).
   -   **Renames.**  [PG] PostgreSQL refuses to drop a role that owns
       objects, so a role is renamed with `ALTER ROLE ... RENAME TO`,
       never dropped and recreated.  Roles are not schema-scoped, so
       `RENAMED FROM` takes a bare name.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New role | `CREATE ROLE name <options>` | `SAFE` |
   | `LOGIN`/`SUPERUSER`/`CREATEDB`/`CREATEROLE`/`INHERIT`/`REPLICATION`/`BYPASSRLS`/`CONNECTION LIMIT`/`VALID UNTIL` changed | `ALTER ROLE name <changed options>` | `SAFE` |
   | `PASSWORD` changed | `ALTER ROLE name PASSWORD '<resolved>'` | `CAUTION` (clients using the old password stop connecting) |
   | Membership added | `GRANT role TO member [WITH ADMIN OPTION] [WITH INHERIT bool] [WITH SET bool]` | `SAFE` |
   | Membership `WITH` option changed | `GRANT role TO member WITH <option>` | `SAFE` |
   | Membership removed | `REVOKE role FROM member` | `CAUTION` |
   | `SET`/`RESET` changed | `ALTER ROLE name [IN DATABASE db] SET ...` / `RESET ...` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER ROLE old RENAME TO new` | `CAUTION` |
   | Role removed | `DROP ROLE name` | `DESTRUCTIVE` |

### 11.2. Grants — The Additive Model

   [DPG] DPG follows PostgreSQL's additive privilege model:

   -   Declaring a grant emits `GRANT`.
   -   Removing a grant declaration emits nothing.  Revoking needs an
       explicit `REVOKES { }` entry (Section 11.3).
   -   Grants made outside DPG are not touched.  `dpg verify` reports a
       declared grant missing from the live catalog as drift, and a live
       grant DPG didn't declare as a warning (Section 20.2), except
       grants to an ignored role (Section 3.9) and the privileges
       PostgreSQL grants `PUBLIC` by default.
   -   Adding `WITH GRANT OPTION` emits `GRANT ... WITH GRANT OPTION`;
       removing it emits nothing (revoking the option is done manually).

   [DPG] The `privilege` list (Section 7.10) is shared by every object
   kind; DPG does not check offline that a privilege applies to the
   object (`EXECUTE` on a table, say).  [PG] PostgreSQL rejects such a
   grant when the migration runs.

### 11.3. Revocations

   [PG: `REVOKE`] `REVOKES { }` entries emit `REVOKE ... FROM role`.
   [PG] Revoking a privilege that isn't held succeeds without error, and
   `REVOKE` has no `IF EXISTS`.  A role that doesn't exist is an error
   when the migration runs; DPG does not create roles named only in
   `REVOKES`.

   [DPG] The linter warns about a revocation with no matching grant in
   the same declaration (rule `unnecessary-revocation`, Section 19.3).

### 11.4. Default Privileges

   **PG equivalent:** [`ALTER DEFAULT PRIVILEGES`](https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html)

   [DPG] Declared as a `DEFAULT PRIVILEGES` block, at top level or in a
   `SCHEMA` block (which supplies `IN SCHEMA`).  Each `GRANTS`/`REVOKES`
   entry names the object type with `ON`:

```abnf
default-privileges-decl =
    "DEFAULT PRIVILEGES"
    [ WSP "FOR ROLE" WSP identifier ]
    [ WSP "IN SCHEMA" WSP identifier ]
    "{" dp-block "}"

dp-block = *( ( grants-block / revocations-block ) ";" )
```

```sql
SCHEMA public {
    DEFAULT PRIVILEGES FOR ROLE app_admin {
        GRANTS {
            SELECT   ON TABLES    TO app_readonly;
            EXECUTE  ON FUNCTIONS TO app_service;
            USAGE    ON SEQUENCES TO app_service;
        }
    }
}
```

   Emits one statement per entry:

```sql
ALTER DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public
    GRANT SELECT ON TABLES TO app_readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public
    GRANT EXECUTE ON FUNCTIONS TO app_service;
ALTER DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public
    GRANT USAGE ON SEQUENCES TO app_service;
```

### 11.5. Owner Impersonation at Object Creation

   [PG] Default privileges (Section 11.4) apply according to the role
   that runs the `CREATE`, not the object's eventual owner.  An `OWNER`
   applied afterwards with `ALTER ... OWNER TO` would therefore never
   match a `DEFAULT PRIVILEGES FOR ROLE` block.

   [DPG] So the compiler MUST create every object that declares an
   `OWNER` as that role.  Its `CREATE` statement (or, for an object
   created outside a transaction such as `TABLESPACE`, that statement)
   is wrapped as:

```sql
SET ROLE app_admin;
CREATE TABLE ...;
RESET ROLE;
```

   `SET ROLE` needs only membership in the target role, not superuser,
   and leaves `session_user` unchanged.  Changing the owner of an
   existing object still uses `ALTER ... OWNER TO`, unwrapped.

   **Pre-flight membership check.**  Before running any statement,
   `apply` MUST check that the connecting role is a member of every role
   named in an `OWNER` directive in the migration (`pg_has_role(
   current_user, owner, 'MEMBER')`).  If any check fails, `apply` aborts
   with error DPG-E036, naming every such role.

### 11.6. Parameter Privileges

   **PG equivalent:** [`GRANT { SET | ALTER SYSTEM } ON PARAMETER`](https://www.postgresql.org/docs/current/sql-grant.html)

   [DPG] Declared as one top-level, cluster-level `PARAMETER PRIVILEGES`
   block (parameters have no schema).  It governs who may change a
   setting; `ALTER SYSTEM` itself remains out of scope (Section 23).
   [PG] Needs PostgreSQL 15 (Section 3.8).

```abnf
parameter-privileges-decl =
    "PARAMETER PRIVILEGES"
    "{" pp-block "}"

pp-block = *( ( pp-grants-block / pp-revocations-block ) ";" )

pp-grants-block      = "GRANTS" WSP "{" *( pp-grant-entry ";" ) "}"
pp-revocations-block = "REVOKES" WSP "{" *( pp-revoke-entry ";" ) "}"

pp-grant-entry  = pp-privilege-list WSP "ON PARAMETER" WSP identifier-list
                  WSP "TO" WSP role-list
                  [ WSP "WITH GRANT OPTION" ]
pp-revoke-entry = ( pp-privilege-list / "ALL PRIVILEGES" ) WSP
                  "ON PARAMETER" WSP identifier-list
                  WSP "FROM" WSP role-list
                  [ WSP "CASCADE" ]

pp-privilege-list = pp-privilege *( "," WSP pp-privilege )
pp-privilege      = "SET" / "ALTER SYSTEM" / "ALL" / "ALL PRIVILEGES"
identifier-list   = identifier *( "," WSP identifier )
```

```sql
-- production/cluster/parameter_privileges.dpg

PARAMETER PRIVILEGES {
    GRANTS {
        SET ON PARAMETER work_mem, statement_timeout TO app_admin;
    }
}
```

   Emits `GRANT SET ON PARAMETER work_mem, statement_timeout TO
   app_admin`.  Diffing follows the additive model (Section 11.2); both
   `GRANT` and `REVOKE` are `SAFE`.

---

## 12. Full-Text Search Objects

### 12.1. Text Search Configurations

   **PG equivalent:** [`CREATE TEXT SEARCH CONFIGURATION`](https://www.postgresql.org/docs/current/sql-createtsconfig.html)

   **Part 1:** PostgreSQL's `CREATE TEXT SEARCH CONFIGURATION` without
   `CREATE`.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section 4.2.1), and
   `MAPPING` entries [PG: `ALTER TEXT SEARCH CONFIGURATION ... ALTER
   MAPPING`]:

```abnf
tsconfig-block = *( ( comment-dir / owner-dir / renamed-from-dir
                    / mapping-dir ) ";" )

mapping-dir = "MAPPING FOR" WSP token-type-list
              WSP "WITH" WSP dict-list
            / "MAPPING FOR" WSP token-type-list
              WSP "REPLACE" WSP qual-name WSP "WITH" WSP qual-name
            / "MAPPING" WSP "REPLACE" WSP qual-name WSP "WITH" WSP qual-name
```

   `REPLACE` swaps one dictionary for another, for the listed token
   types or, without `FOR`, for all of them, as PostgreSQL's two `ALTER
   MAPPING ... REPLACE` forms do.

```sql
SCHEMA public {
    TEXT SEARCH CONFIGURATION english_unaccented (COPY = pg_catalog.english) {
        MAPPING FOR hword, hword_part, word
            WITH unaccent, english_stem;
    }
}
```

   Emits:

```sql
CREATE TEXT SEARCH CONFIGURATION public.english_unaccented (COPY = pg_catalog.english);
ALTER TEXT SEARCH CONFIGURATION public.english_unaccented
    ALTER MAPPING FOR hword, hword_part, word WITH unaccent, english_stem;
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New configuration | `CREATE TEXT SEARCH CONFIGURATION ...` | `SAFE` |
   | Mapping added or changed | `ALTER TEXT SEARCH CONFIGURATION ... ALTER MAPPING FOR ...` | `SAFE` |
   | Mapping removed | `ALTER TEXT SEARCH CONFIGURATION ... DROP MAPPING FOR ...` | `SAFE` |
   | `REPLACE` declared | `ALTER TEXT SEARCH CONFIGURATION ... ALTER MAPPING [FOR ...] REPLACE old WITH new` | `SAFE` |
   | Owner changed | `ALTER TEXT SEARCH CONFIGURATION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER TEXT SEARCH CONFIGURATION old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | Configuration removed | `DROP TEXT SEARCH CONFIGURATION name` | `DESTRUCTIVE` |

### 12.2. Text Search Dictionaries

   **PG equivalent:** [`CREATE TEXT SEARCH DICTIONARY`](https://www.postgresql.org/docs/current/sql-createtsdictionary.html)

   **Part 1:** PostgreSQL's `CREATE TEXT SEARCH DICTIONARY` without
   `CREATE`.  **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section
   4.2.1).

```sql
SCHEMA public {
    TEXT SEARCH DICTIONARY english_ispell (
        TEMPLATE  = ispell,
        DictFile  = english,
        AffFile   = english,
        StopWords = english
    )
    {
        OWNER search_admin;
        COMMENT 'Ispell-based English dictionary';
    }
}
```

   **Diffing semantics:** [PG] `TEMPLATE` is fixed at creation; every
   other option can be altered in place.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Option other than `TEMPLATE` added, changed or removed | `ALTER TEXT SEARCH DICTIONARY name (key [= value], ...)` | `SAFE` |
   | `TEMPLATE` changed | `DROP TEXT SEARCH DICTIONARY` + recreate | `DESTRUCTIVE` |
   | Owner changed | `ALTER TEXT SEARCH DICTIONARY name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER TEXT SEARCH DICTIONARY old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | Dictionary removed | `DROP TEXT SEARCH DICTIONARY name` | `DESTRUCTIVE` |

### 12.3. Text Search Parsers

   **PG equivalent:** [`CREATE TEXT SEARCH PARSER`](https://www.postgresql.org/docs/current/sql-createtsparser.html)

   **Part 1:** PostgreSQL's `CREATE TEXT SEARCH PARSER` without
   `CREATE`.  **Part 2:** `COMMENT`, `RENAMED FROM` (Section 4.2.1).
   [PG] A parser has no owner.

```sql
SCHEMA public {
    TEXT SEARCH PARSER my_parser (
        START    = prsd_start,
        GETTOKEN = prsd_nexttoken,
        END      = prsd_end,
        LEXTYPES = prsd_lextype,
        HEADLINE = prsd_headline
    )
    {
        COMMENT 'Custom document parser';
    }
}
```

   **Diffing semantics:** any change to the definition drops and
   recreates the parser (`DESTRUCTIVE`).  A comment change is `SAFE`;
   `RENAMED FROM` emits `RENAME TO` (`CAUTION`) and `SET SCHEMA` (`SAFE`).

### 12.4. Text Search Templates

   **PG equivalent:** [`CREATE TEXT SEARCH TEMPLATE`](https://www.postgresql.org/docs/current/sql-createtstemplate.html)

   **Part 1:** PostgreSQL's `CREATE TEXT SEARCH TEMPLATE` without
   `CREATE`.  **Part 2:** `COMMENT`, `RENAMED FROM` (Section 4.2.1).
   [PG] A template has no owner.

```sql
SCHEMA public {
    TEXT SEARCH TEMPLATE ispell_template (
        LEXIZE = dispell_lexize,
        INIT   = dispell_init
    )
    {
        COMMENT 'Ispell-family dictionary template';
    }
}
```

   **Diffing semantics:** as for parsers (Section 12.3).

---

## 13. Logical Replication

### 13.1. Publications

   Publications are database-level objects; they are not in a schema.

   **PG equivalent:** [`CREATE PUBLICATION`](https://www.postgresql.org/docs/current/sql-createpublication.html)

   **Part 1:** PostgreSQL's `CREATE PUBLICATION` without `CREATE`.
   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `SECURITY LABEL`
   (Section 4.2.1).  [PG] PostgreSQL has no privileges on publications,
   so there are no `GRANTS`.

```abnf
pub-block = *( ( comment-dir / owner-dir / renamed-from-dir
               / security-label-decl ) ";" )
```

```sql
PUBLICATION user_data
    FOR TABLE users, profiles
    WITH (publish = 'insert, update, delete') {
    COMMENT 'Primary replication stream for user data';
}

PUBLICATION all_tables FOR ALL TABLES;

PUBLICATION filtered_orders
    FOR TABLE orders (id, customer_id, status, total)
    WHERE (status != 'draft');
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New publication | `CREATE PUBLICATION ...` | `SAFE` |
   | Table list changed | `ALTER PUBLICATION name SET TABLE ...` | `SAFE` |
   | Options changed | `ALTER PUBLICATION name SET (...)` | `SAFE` |
   | Owner changed | `ALTER PUBLICATION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER PUBLICATION old RENAME TO new` | `CAUTION` |
   | Publication removed | `DROP PUBLICATION name` | `DESTRUCTIVE` |

### 13.2. Subscriptions

   Subscriptions are database-level objects; they are not in a schema.

   **PG equivalent:** [`CREATE SUBSCRIPTION`](https://www.postgresql.org/docs/current/sql-createsubscription.html)

   **Part 1:** PostgreSQL's `CREATE SUBSCRIPTION` without `CREATE`.
   [DPG] The `CONNECTION` string MAY contain `{{<secret-uri>}}`
   placeholders.  **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`,
   `SECURITY LABEL` (Section 4.2.1).

```abnf
subscription-block = *( ( comment-dir / owner-dir / renamed-from-dir
                        / security-label-decl ) ";" )
```

```sql
SUBSCRIPTION replica_users
    CONNECTION 'host=primary.db.internal dbname=myapp user=replicator'
    PUBLICATION user_data
    WITH (enabled = true, copy_data = true);

SUBSCRIPTION replica_orders
    CONNECTION 'host=primary.db.internal dbname=myapp user=replicator password={{vault:secret/repl/orders#pw}}'
    PUBLICATION order_data;

SUBSCRIPTION replica_billing
    CONNECTION '{{vault:secret/repl/billing#conninfo}}'
    PUBLICATION billing_data
{
    COMMENT 'Billing replica, syncs nightly';
}
```

   **Notes** [DPG]:

   -   **Secret placeholders.**  Each `{{<secret-uri>}}` in `CONNECTION`
       is resolved (Appendix D.1) immediately before `CREATE
       SUBSCRIPTION` runs, and substituted in place; only `{{...}}`
       triggers resolution.  `plan` and `diff` compare the placeholder
       text, and the snapshot, migration archives and error messages
       show only the placeholder.
   -   **Reading back.**  [PG] `pg_subscription.subconninfo` is not
       readable by an ordinary role, and a resolved secret can't be turned
       back into its placeholder.  A subscription read from the live
       catalog therefore gets an inert placeholder `CONNECTION` with
       `connect`, `create_slot` and `enabled` set to `false`; a dumped
       subscription needs its `CONNECTION` filled in by hand.  A
       subscription read from the catalog is never compared by body, so
       the placeholder causes no drift.
   -   **Transactions.**  [PG] `CREATE SUBSCRIPTION` (with its default
       `create_slot = true`) and `DROP SUBSCRIPTION` can't run in a
       transaction block, so both are non-transactional (Section 17.3),
       and so are a new subscription's comment and security labels.

   **Diffing semantics:** Part 1 is compared as a whole.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New subscription | `CREATE SUBSCRIPTION ...` [+ `COMMENT ON SUBSCRIPTION ...`] | `SAFE`, non-transactional |
   | Part 1 changed (`CONNECTION`, `PUBLICATION`, `WITH`) | `DROP SUBSCRIPTION` + `CREATE SUBSCRIPTION` | `DESTRUCTIVE` then `SAFE`, non-transactional |
   | Comment changed | `COMMENT ON SUBSCRIPTION ... IS ...` | `SAFE` |
   | Owner changed | `ALTER SUBSCRIPTION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER SUBSCRIPTION old RENAME TO new` | `CAUTION` |
   | Subscription removed | `DROP SUBSCRIPTION name` | `DESTRUCTIVE`, non-transactional |

---

## 14. Advanced PostgreSQL Objects

### 14.1. Event Triggers

   Event triggers are database-level objects; they are not in a schema.

   **PG equivalent:** [`CREATE EVENT TRIGGER`](https://www.postgresql.org/docs/current/sql-createeventtrigger.html)

   **Part 1:** PostgreSQL's `CREATE EVENT TRIGGER` without `CREATE`.
   [DPG] `WHEN TAG IN (...)` accepts any command tag string, so tags
   added by later PostgreSQL releases need no grammar change.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `SECURITY LABEL`
   (Section 4.2.1), and the enable state `DISABLED` / `ENABLE REPLICA` /
   `ENABLE ALWAYS` [PG: `ALTER EVENT TRIGGER ... DISABLE`/`ENABLE ...`].
   Omitted means enabled.

```abnf
event-trigger-block = *( ( comment-dir / owner-dir / renamed-from-dir
                         / security-label-decl / trigger-enable-state ) ";" )
```

```sql
EVENT TRIGGER prevent_drop_table
    ON sql_drop
    WHEN TAG IN ('DROP TABLE', 'DROP SCHEMA')
    EXECUTE FUNCTION abort_drop();

EVENT TRIGGER audit_ddl
    ON ddl_command_end
    EXECUTE FUNCTION log_ddl_command()
{
    ENABLE REPLICA;
    OWNER audit_admin;
}
```

   [PG] The `login` event needs PostgreSQL 17 (Section 3.8) and takes no
   `WHEN TAG IN`.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New event trigger | `CREATE EVENT TRIGGER ...` | `SAFE` |
   | Event, `WHEN TAG IN` or function changed | `DROP EVENT TRIGGER` + `CREATE EVENT TRIGGER` | `SAFE` (no data involved) |
   | Enable state changed | `ALTER EVENT TRIGGER name ENABLE [REPLICA\|ALWAYS]` / `DISABLE` | `SAFE` |
   | Owner changed | `ALTER EVENT TRIGGER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER EVENT TRIGGER old RENAME TO new` | `CAUTION` |
   | Event trigger removed | `DROP EVENT TRIGGER name` | `SAFE` |

### 14.2. Collations

   **PG equivalent:** [`CREATE COLLATION`](https://www.postgresql.org/docs/current/sql-createcollation.html)

   **Part 1:** PostgreSQL's `CREATE COLLATION` without `CREATE`,
   including the `FROM existing_collation` form.

   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM` (Section 4.2.1), and
   `REFRESH VERSION` [PG: `ALTER COLLATION ... REFRESH VERSION`]:

```abnf
collation-block     = *( ( comment-dir / owner-dir / renamed-from-dir
                         / refresh-version-dir ) ";" )
refresh-version-dir = "REFRESH VERSION"
```

```sql
SCHEMA public {
    COLLATION case_insensitive (
        PROVIDER      = icu,
        LOCALE        = 'und-u-ks-level2',
        DETERMINISTIC = false
    );

    COLLATION case_insensitive_alias FROM case_insensitive;

    COLLATION custom_digits_first (
        PROVIDER = icu,
        LOCALE   = 'en-u-kn-true',
        RULES    = '&0 < a-z'
    )
    {
        OWNER search_admin;
        COMMENT 'Numeric-aware sort with a custom tailoring rule';
    }
}
```

   [DPG] `REFRESH VERSION` is an action, like a sequence's `RESTART`
   (Section 10): while it is in the source, every plan emits `ALTER
   COLLATION name REFRESH VERSION` (`CAUTION`) with a note to remove it
   once applied.

   **Diffing semantics:** Part 1 is compared as a whole.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Owner changed | `ALTER COLLATION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER COLLATION old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | `REFRESH VERSION` in the source | `ALTER COLLATION name REFRESH VERSION`, every plan while present | `CAUTION` |
   | Any Part 1 change (`LOCALE`, `PROVIDER`, `RULES`, `FROM` target, ...) | `DROP COLLATION` + `CREATE COLLATION` | `DESTRUCTIVE` |

### 14.3. Operators

   **PG equivalent:** [`CREATE OPERATOR`](https://www.postgresql.org/docs/current/sql-createoperator.html)

   **Part 1:** PostgreSQL's `CREATE OPERATOR` without `CREATE`.
   **Part 2:** `COMMENT` (Section 4.2.1).

```sql
SCHEMA public {
    OPERATOR === (
        LEFTARG    = complex,
        RIGHTARG   = complex,
        PROCEDURE  = complex_eq,
        COMMUTATOR = ===,
        NEGATOR    = !==,
        RESTRICT   = eqsel,
        JOIN       = eqjoinsel,
        HASHES,
        MERGES
    );
}
```

   **Diffing semantics:** identity is `(schema, symbol, left type, right
   type)`.  [PG] `ALTER OPERATOR` can set, clear or change `RESTRICT` and
   `JOIN`, but `COMMUTATOR`, `NEGATOR`, `HASHES` and `MERGES` only from
   unset to set.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New operator | `CREATE OPERATOR ...` | `SAFE` |
   | `PROCEDURE`/`FUNCTION` changed | `DROP OPERATOR; CREATE OPERATOR` | `DESTRUCTIVE` |
   | `RESTRICT`/`JOIN` set, cleared or changed | `ALTER OPERATOR ... SET (...)` | `SAFE` |
   | `COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` newly set | `ALTER OPERATOR ... SET (...)` | `SAFE` |
   | `COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` changed or cleared | `DROP OPERATOR; CREATE OPERATOR` | `DESTRUCTIVE` |
   | Operator removed | `DROP OPERATOR symbol (left, right)` | `DESTRUCTIVE` |

### 14.4. Operator Classes and Families

   **PG equivalent:** [`CREATE OPERATOR CLASS`](https://www.postgresql.org/docs/current/sql-createopclass.html), [`CREATE OPERATOR FAMILY`](https://www.postgresql.org/docs/current/sql-createopfamily.html), [`ALTER OPERATOR FAMILY ... ADD`/`DROP`](https://www.postgresql.org/docs/current/sql-alteropfamily.html)

   **Part 1:** PostgreSQL's `CREATE OPERATOR CLASS` / `CREATE OPERATOR
   FAMILY` without `CREATE`.

   **Part 2:** `COMMENT`, `RENAMED FROM` (Section 4.2.1).  An operator
   family's block also holds loose members [PG: `ALTER OPERATOR FAMILY
   ... ADD`]: each entry is an `OPERATOR` or `FUNCTION` item of `ALTER
   OPERATOR FAMILY ... ADD`, without the `ALTER`/family header.

```sql
SCHEMA public {
    OPERATOR FAMILY my_family USING btree {
        OPERATOR 1 <(int4, int8),
        OPERATOR 3 =(int4, int8),
        FUNCTION 1 (int4, int8) btint48cmp(int4, int8)
    };

    OPERATOR CLASS my_ops FOR TYPE mytype USING btree FAMILY my_family AS
        OPERATOR 1 <,
        OPERATOR 2 <=,
        OPERATOR 3 =,
        OPERATOR 4 >=,
        OPERATOR 5 >,
        FUNCTION 1 mytype_cmp(mytype, mytype);
}
```

   **Notes:**

   -   [DPG] An operator class is identified by `(schema, name, access
       method)`.  [PG] Its `AS` member list cannot be altered, so any
       change drops and recreates the class (`DESTRUCTIVE`).
   -   [DPG] A loose member is identified by `(kind, strategy or support
       number, left type, right type)`, with types compared in canonical
       form (`integer`, not `int4`).  A `FUNCTION` item MAY omit `(left,
       right)` only when the function takes two arguments.
   -   [DPG] A member already in one of the family's classes MUST NOT be
       declared again in the family block ([PG] PostgreSQL rejects the
       duplicate).  A family created implicitly by `CREATE OPERATOR
       CLASS` has no block; declare it explicitly to add loose members.

   **Diffing semantics (loose members):**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Member added | `ALTER OPERATOR FAMILY f USING am ADD OPERATOR ...` / `ADD FUNCTION ...` | `SAFE` |
   | Member changed in place | `ALTER ... DROP ...` then `ALTER ... ADD ...` | `DESTRUCTIVE` |
   | Member removed | `ALTER OPERATOR FAMILY f USING am DROP OPERATOR n (lt, rt)` / `DROP FUNCTION n (lt, rt)` | `DESTRUCTIVE` |
   | Family dropped and recreated | every declared member added again after the `CREATE` | as the family change |

   [DPG] Removing a member is `DESTRUCTIVE` although no data is lost:
   it can silently change query plans, and a missing member is usually
   an accidental omission.

### 14.5. Casts

   **PG equivalent:** [`CREATE CAST`](https://www.postgresql.org/docs/current/sql-createcast.html)

   **Part 1:** PostgreSQL's `CREATE CAST` without `CREATE`.
   **Part 2:** `COMMENT` (Section 4.2.1).

```sql
SCHEMA public {
    CAST (mytype AS TEXT)
        WITH FUNCTION mytype_to_text(mytype)
        AS IMPLICIT;

    CAST (TEXT AS mytype)
        WITH FUNCTION text_to_mytype(TEXT);
}
```

   **Diffing semantics:** identity is `(source type, target type)`.
   [PG] There is no `ALTER CAST`, so any change is `DROP CAST` and
   `CREATE CAST` (`DESTRUCTIVE`).

### 14.6. Extended Statistics Objects

   **PG equivalent:** [`CREATE STATISTICS`](https://www.postgresql.org/docs/current/sql-createstatistics.html)

   **Part 1:** PostgreSQL's `CREATE STATISTICS` without `CREATE`,
   expressions included.

   **Part 2:** `COMMENT`, `RENAMED FROM` (Section 4.2.1), and
   `STATISTICS n | DEFAULT` [PG: `ALTER STATISTICS ... SET STATISTICS`]:

```abnf
statistics-block = *( ( comment-dir / renamed-from-dir
                      / "STATISTICS" WSP ( integer / "DEFAULT" ) ) ";" )
```

```sql
SCHEMA public {
    STATISTICS orders_stats (dependencies, ndistinct, mcv)
        ON customer_id, created_at
        FROM orders
    {
        STATISTICS 500;
    }

    STATISTICS orders_date_stats (ndistinct)
        ON (date_trunc('month', created_at)), customer_id
        FROM orders;
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New statistics object | `CREATE STATISTICS ...`, then `ALTER STATISTICS name SET STATISTICS n` when a target is declared | `SAFE` |
   | Target changed | `ALTER STATISTICS name SET STATISTICS n` (`-1` for `DEFAULT` or no directive) | `SAFE` |
   | Column list, expressions or kinds changed | `DROP STATISTICS; CREATE STATISTICS` | `DESTRUCTIVE` |
   | Renamed (`RENAMED FROM`) | `ALTER STATISTICS old RENAME TO new`; `SET SCHEMA` when the schema differs | `CAUTION` / `SAFE` |
   | Object removed | `DROP STATISTICS name` | `SAFE` |

### 14.7. Tablespaces

   Tablespaces are cluster-level objects.

   **PG equivalent:** [`CREATE TABLESPACE`](https://www.postgresql.org/docs/current/sql-createtablespace.html)

   **Part 1:** PostgreSQL's `CREATE TABLESPACE` without `CREATE`; the
   owner is its `OWNER` clause.  **Part 2:** `COMMENT`, `RENAMED FROM`,
   `GRANTS`, `REVOKES`, `SECURITY LABEL` (Section 4.2.1).

```abnf
tablespace-block = *( ( comment-dir / renamed-from-dir / grants-block
                      / revocations-block / security-label-decl ) ";" )
role-spec        = identifier / "CURRENT_ROLE" / "CURRENT_USER" / "SESSION_USER"
```

```sql
-- production/cluster/tablespaces.dpg

TABLESPACE fast_ssd LOCATION '/mnt/nvme/pg_data'
    WITH (random_page_cost = 1.1, seq_page_cost = 1.0);
TABLESPACE archive  LOCATION '/mnt/hdd/pg_archive';
```

   [PG] `CREATE TABLESPACE` and `DROP TABLESPACE` can't run in a
   transaction block, so both are non-transactional (Section 17.3).

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Owner changed | `ALTER TABLESPACE name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER TABLESPACE old RENAME TO new` | `CAUTION` |
   | `WITH (...)` changed | `ALTER TABLESPACE name SET (...)` / `RESET (...)` | `CAUTION` |
   | `LOCATION` changed | `DROP TABLESPACE` + `CREATE TABLESPACE` | `DESTRUCTIVE` |
   | Tablespace removed | `DROP TABLESPACE name` | `DESTRUCTIVE` |

   [PG] Dropping a tablespace that still holds objects fails; DPG emits
   a warning comment saying so alongside the `DROP`.

### 14.8. Foreign Data Wrappers

   A custom foreign data wrapper is declared in the cluster objects
   directory; one installed by an extension needs no declaration.

   **PG equivalent:** [`CREATE FOREIGN DATA WRAPPER`](https://www.postgresql.org/docs/current/sql-createforeigndatawrapper.html)

   **Part 1:** PostgreSQL's `CREATE FOREIGN DATA WRAPPER` without
   `CREATE`.  **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `GRANTS`,
   `REVOKES` (Section 4.2.1).

```sql
FOREIGN DATA WRAPPER myfdw
    HANDLER   myfdw_handler
    VALIDATOR myfdw_validator
{
    OWNER fdw_admin;
    COMMENT 'Custom C-implemented FDW';
    GRANTS { USAGE TO app_service; }
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New wrapper | `CREATE FOREIGN DATA WRAPPER ...` | `SAFE` |
   | `HANDLER`/`VALIDATOR`/`OPTIONS` changed | `DROP FOREIGN DATA WRAPPER` + `CREATE FOREIGN DATA WRAPPER` | `DESTRUCTIVE` |
   | Owner changed | `ALTER FOREIGN DATA WRAPPER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER FOREIGN DATA WRAPPER old RENAME TO new` | `CAUTION` |
   | Grants changed | `GRANT`/`REVOKE ... ON FOREIGN DATA WRAPPER name` | `SAFE` |
   | Wrapper removed | `DROP FOREIGN DATA WRAPPER name` | `DESTRUCTIVE` |

### 14.9. Foreign Servers

   **PG equivalent:** [`CREATE SERVER`](https://www.postgresql.org/docs/current/sql-createserver.html)

   **Part 1:** PostgreSQL's `CREATE SERVER` without `CREATE`.
   **Part 2:** `OWNER`, `COMMENT`, `RENAMED FROM`, `GRANTS`, `REVOKES`
   (Section 4.2.1).

```sql
SERVER analytics_warehouse
    FOREIGN DATA WRAPPER postgres_fdw
    OPTIONS (host 'warehouse.internal', dbname 'analytics', port '5432')
{
    OWNER fdw_admin;
    COMMENT 'Read replica warehouse connection';
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New server | `CREATE SERVER ...` | `SAFE` |
   | `OPTIONS` changed | `ALTER SERVER name OPTIONS (ADD/SET/DROP key 'value', ...)` | `SAFE` |
   | `VERSION` changed only | `ALTER SERVER name VERSION 'new_version'` | `SAFE` |
   | Owner changed | `ALTER SERVER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER SERVER old RENAME TO new` | `CAUTION` |
   | Grants changed | `GRANT`/`REVOKE ... ON FOREIGN SERVER name` | `SAFE` |
   | Foreign data wrapper changed | Drop and recreate | `DESTRUCTIVE` |
   | Server removed | `DROP SERVER name [CASCADE]` | `DESTRUCTIVE` |

### 14.10. User Mappings

   **PG equivalent:** [`CREATE USER MAPPING`](https://www.postgresql.org/docs/current/sql-createusermapping.html)

   **Part 1:** PostgreSQL's `CREATE USER MAPPING` without `CREATE`.
   [DPG] Any `OPTIONS` value MAY contain `{{<secret-uri>}}` placeholders.
   **Part 2:** none ([PG] there is no `COMMENT ON USER MAPPING`).

```sql
USER MAPPING FOR app_service
    SERVER analytics_warehouse
    OPTIONS (user 'fdw_user', password '{{vault:secret/fdw/analytics#password}}');
```

   **Notes** [DPG]:

   -   Placeholders are resolved (Appendix D.1) in the whole statement,
       whatever option they appear under, immediately before `CREATE
       USER MAPPING` runs, never during `plan` or `diff`.  The snapshot,
       archives and error messages show only the placeholder.
   -   The linter rejects a literal value under a password-like option
       key (`password`, `passwd`, `pwd`, `secret`, `passphrase`) when
       `forbid_hardcoded_passwords` is on (rule `hardcoded-fdw-password`).

   **Diffing semantics:** Part 1 is compared as a whole.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New mapping | `CREATE USER MAPPING ...` | `SAFE` |
   | `OPTIONS` changed | `DROP USER MAPPING ...` + `CREATE USER MAPPING ...` | `DESTRUCTIVE` then `SAFE` |
   | Mapping removed | `DROP USER MAPPING FOR user SERVER server` | `DESTRUCTIVE` |

---

### 14.11. Security Labels

   **PG equivalent:** [`SECURITY LABEL`](https://www.postgresql.org/docs/current/sql-security-label.html)

   **Part 2 directive** [PG: `SECURITY LABEL ... ON <object> IS ...`]:
   the object is the enclosing declaration, so `ON object_type
   object_name` is not written.  A block MAY hold one entry per provider.

```abnf
security-label-decl =
    "SECURITY" WSP "LABEL" [ WSP "FOR" WSP identifier ] WSP string-literal ";"
```

```sql
TABLE orders (
    id  BIGINT PRIMARY KEY,
    ssn TEXT
) {
    SECURITY LABEL FOR sepgsql 'system_u:object_r:sepgsql_table_t:s0';

    COLUMN ssn {
        SECURITY LABEL FOR sepgsql 'system_u:object_r:sepgsql_secret_table_t:s0';
    }
}
```

   Accepted on tables (foreign tables included), columns, views,
   materialized views, functions, procedures, aggregates (emitted `ON
   FUNCTION`), every type and domain, schemas, sequences, roles,
   tablespaces, publications, subscriptions and event triggers.  [PG]
   PostgreSQL also labels databases, large objects and languages, which
   DPG doesn't model (Section 23).  [PG] With no `FOR provider`,
   PostgreSQL uses the only loaded provider, and fails if there are none
   or several.

   **Diffing semantics** [DPG]: labels are compared per provider.
   Removing an entry clears that label with `IS NULL`.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Label added or changed | `SECURITY LABEL [FOR provider] ON ... IS '...'` | `SAFE` |
   | Label removed | `SECURITY LABEL [FOR provider] ON ... IS NULL` | `SAFE` |

---

## 15. Compilation Pipeline

### 15.1. Phases Overview

   Compilation runs in the following phases.  Implementations MAY
   structure their code differently, but MUST produce the results
   specified here.

```
Phase 1:  File Discovery
Phase 2:  Macro Preprocessing
Phase 3:  Tokenization
Phase 4a: PostgreSQL Parsing (Part 1)
Phase 4b: Block Parsing (Part 2)
Phase 5:  IR Construction
Phase 6:  Merging
Phase 7:  Dependency Resolution
Phase 8:  Linting               [dpg validate / dpg plan / dpg apply only]
Phase 9:  Differencing
Phase 10: Emission
```

   Phase 8's diagnostics are warnings by default; `--strict` on `dpg
   validate` and `dpg apply` turns them into errors (Section 15.10).

### 15.2. Phase 1 — File Discovery

   Implements Section 3.6.  The output is, for each cluster and each of
   its databases, the set of `.dpg` files belonging to it.

### 15.3. Phase 2 — Macro Preprocessing

   Implements Section 4.7, over every file of one compilation unit:

   1.  Collect every `MACRO` declaration in the unit.  A declaration
       inside a block is DPG-E007; a name declared twice in one file is
       DPG-E011.  A file's own definition takes precedence over one
       from another file.
   2.  Expand every `...name` spread, including spreads inside macro
       bodies.  An unknown name is DPG-E010, a body spread into the
       wrong kind of list is DPG-E008/DPG-E009, and a circular
       reference is DPG-E012.
   3.  Remove the `MACRO` declarations.

   Line numbers of all remaining source text MUST be unchanged by this
   phase, so source positions (Section 17.1) refer to the file as
   written.

### 15.4. Phase 3 — Tokenization

   Splits the preprocessed text into declarations.  For each, it
   records: the object kind (from the leading keyword or keywords),
   Part 1 with the leading keyword removed, Part 2 without its braces
   (empty when there is no block), the enclosing schema scope if any
   (Section 4.4), and the declaration's source position.

   The tokenizer MUST:

   -   ignore comments (`--`, `/* ... */`) when detecting keywords;
   -   treat dollar-quoted text as opaque (Section 4.6);
   -   track brace depth so nested blocks stay inside their Part 2;
   -   treat `SCHEMA name { ... }` as a container whose declarations
       take `name` as their schema scope;
   -   enforce the terminator rule (Section 4.5, DPG-E037/DPG-E038);
   -   report DPG-E006 for `CREATE`, `ALTER`, or `DROP` at declaration
       level outside dollar-quoted text.

### 15.5. Phase 4a — PostgreSQL Parsing (Part 1)

   Part 1 is parsed by prepending the `CREATE` verb (`CREATE OR
   REPLACE` where PostgreSQL offers it) and running PostgreSQL's own SQL
   grammar for the newest supported version (Section 1.4).  Part 1 MUST
   NOT be changed in any other way.  A grammar error is reported at the
   declaration's source position.

   `VIRTUAL TYPE` and `MACRO` have no PostgreSQL equivalent and are not
   parsed this way.

### 15.6. Phase 4b — Block Parsing (Part 2)

   Parses Part 2 into its directives, using the grammar given for each
   object kind.  An unknown directive is DPG-E024; a directive
   renamed to match PostgreSQL's spelling is reported with its
   replacement (for example `INDICES`, renamed `INDEXES`).

### 15.7. Phase 5 — IR Construction

   Combines one declaration's Part 1 parse and Part 2 directives into a
   typed, fully-qualified IR object:

   -   An unqualified name takes the enclosing schema scope, else the
       database's `default_schema` (Section 4.4); a qualifier
       contradicting the scope is DPG-E040.
   -   Unquoted identifiers are case-folded (Section 4.10).
   -   PRIMARY KEY implies NOT NULL (Section 7.2).
   -   Function body hashes are computed (Section 9.5).
   -   Each object and sub-object keeps its source position.
   -   A `COLUMN`/`ATTRIBUTE` block naming a column or attribute absent
       from the `( )` list is DPG-E018.

### 15.8. Phase 6 — Merging

   Merges every declaration of the same object (same qualified name and
   kind) in the compilation unit into one, per Section 3.7 (DPG-E005,
   DPG-E039).  The output contains each object once.

### 15.9. Phase 7 — Dependency Resolution

   Orders the merged objects so each appears after everything it
   depends on, per Section 22.

### 15.10. Phase 8 — Linting

   Runs the rules of Section 19 over the ordered objects, on `dpg
   validate`, `dpg plan`, and `dpg apply` only.  Every diagnostic is a
   warning unless its rule is configured as an error (Section 19.2);
   `--strict` on `dpg validate` and `dpg apply` makes every diagnostic
   an error.  `dpg plan` has no `--strict`.  Linting never changes the
   objects or the diff.

### 15.11. Phase 9 — Differencing

   Compares the ordered objects with the snapshot (Section 16) and
   produces the operations that bring the database from the snapshot's
   state to the desired one:

   1.  **Renames:** each `RENAMED FROM` is resolved (Section 7.6); a
       renamed object is compared against its old snapshot entry.
   2.  **Creates and alters:** an object absent from the snapshot is
       created; one present is compared property by property
       (Section 21).
   3.  **Drops:** a snapshot object absent from the desired state, and
       not renamed, is dropped, unless it is `PROTECTED` (DPG-E022).

   Creates and alters follow dependency order; drops follow, in reverse
   dependency order.

### 15.12. Phase 10 — Emission

   Groups the operations by execution mode (Section 17.3) into the
   migration (Section 17).

---

## 16. Snapshot Format

### 16.1. Purpose and Placement

   The snapshot is a committed JSON file that represents the compiler's
   normalised view of the database state after the most recent
   successful `dpg apply`.  It is the "current state" input to the
   Differ.

   The snapshot MUST be committed to version control.  Snapshots are
   not secrets (they contain no plaintext passwords; see Sections 11.1
   and 14.10).

   **Path:** `.dpg/snapshots/<cluster-name>/<database-name>.json`

   The snapshot directory is configurable via `[snapshots] directory`
   in the root `dpg.toml` (Section 3.2).

### 16.2. Top-Level Fields

```json
{
  "dpg_version":     "0.8.1",
  "cluster":         "production",
  "database":        "myapp",
  "applied_at":      "2025-09-15T14:32:00Z",
  "source_revision": "a3f7c91",
  "objects": { ... }
}
```

   | Field | Type | Description |
   |-------|------|-------------|
   | `dpg_version` | string | The DPG version that wrote this snapshot. |
   | `cluster` | string | The cluster name from the cluster `dpg.toml`. |
   | `database` | string | The database name from the database `dpg.toml`. Absent for cluster-level snapshots. |
   | `applied_at` | RFC 3339 string | UTC timestamp of the last successful `dpg apply`. |
   | `source_revision` | string | The git commit hash at apply time, if available. Empty if git is unavailable; see Section 16.7 for the exact detection algorithm. |
   | `objects` | object | Map from `QualifiedName()` to per-object snapshot record. |

### 16.3. Per-Object Snapshot Schema

   Each entry in `objects` maps the object's `QualifiedName()` to a
   `SnapObject` — a **discriminated union wrapper**, not a flat record.
   Its `kind` field (REQUIRED) selects which sub-object field is
   populated:

```json
{
  "public.users": {
    "kind": "table",
    "table": { <SnapTable fields, shown below> }
  },
  "public.get_user(text)": {
    "kind": "function",
    "function": { <SnapFunction fields, shown below> }
  },
  "public.user_state": {
    "kind": "virtual_type",
    "virtual_type": { <SnapVirtualType fields, shown below> }
  }
}
```

   The `kind` string values and their corresponding sub-object fields:

   | `kind` value | Sub-object field | Object types covered |
   |---|---|---|
   | `"table"` | `table` | TABLE, UNLOGGED TABLE, FOREIGN TABLE |
   | `"view"` | `view` | VIEW, MATERIALIZED VIEW, RECURSIVE VIEW |
   | `"function"` | `function` | FUNCTION |
   | `"type"` | `type` | ENUM, COMPOSITE, RANGE, DOMAIN, BASE |
   | `"schema"` | `schema` | SCHEMA |
   | `"extension"` | `extension` | EXTENSION |
   | `"sequence"` | `sequence` | SEQUENCE |
   | `"role"` | `role` | ROLE |
   | `"virtual_type"` | `virtual_type` | VIRTUAL TYPE |
   | `"procedure"`, `"aggregate"`, `"tablespace"`, `"fdw"`, `"server"`, `"user_mapping"`, `"publication"`, `"subscription"`, `"event_trigger"`, `"collation"`, `"operator"`, `"operator_class"`, `"operator_family"`, `"cast"`, `"statistics"`, `"ts_config"`, `"ts_dict"`, `"ts_parser"`, `"ts_template"` | `opaque` | All passthrough objects (Section 16.3.1) |

   **SnapTable:**

```json
{
  "schema": "public",
  "name": "users",
  "owner": "app_role",
  "comment": "Primary identity store",
  "rls_enabled": true,
  "rls_forced": false,
  "protected": false,
  "drop_cascade": false,
  "unlogged": false,
  "columns": [ ... ],
  "constraints": [ ... ],
  "indexes": [ ... ],
  "policies": [ ... ],
  "triggers": [ ... ],
  "grants": [ ... ]
}
```

   `columns`, `constraints`, `indexes`, `policies`, `triggers`, and
   `grants` are all **ordered slices (arrays)**, never maps — each
   element's own `name` field identifies it.

   **Column snapshot record:**

```json
{
  "name": "email",
  "type": "text",
  "virtual_type": null,
  "not_null": true,
  "default": null,
  "identity": null,
  "generated": null,
  "comment": "Verified email address",
  "statistics": 300,
  "compression": null,
  "storage": null,
  "deprecated": null,
  "renamed_from": null,
  "grants": []
}
```

   `not_null` is `true` when the column IS `NOT NULL` — note the
   inverted sense relative to a `nullable` field.  `virtual_type` is set
   only for a column declared with a virtual type (Section 5.6): the
   schema-qualified reference, with any `[]`, while `type` holds the
   stored `json`/`jsonb` type.  `identity` holds the
   string `"ALWAYS"` or `"BY DEFAULT"` (or `null`), not a nested object.

   **Constraint snapshot record:**

```json
{
  "name": "pk_users",
  "type": "PRIMARY KEY",
  "expr": "(id)",
  "not_valid": false,
  "deferrable": false
}
```

   There is no top-level `columns` array or `initially_deferred`
   field; `expr` is the raw constraint expression/definition text.

   **Index snapshot record:**

```json
{
  "name": "idx_users_email",
  "unique": false,
  "method": "btree",
  "columns": "email",
  "where": null
}
```

   `columns` is a single comma-separated string, not an array of
   `{name, direction, nulls}` objects.

   **Policy snapshot record:**

```json
{
  "name": "view_own",
  "command": "SELECT",
  "permissive": true,
  "using": "user_id = auth.uid()",
  "with_check": null,
  "roles": []
}
```

   **Trigger snapshot record:**

```json
{
  "name": "after_email_change",
  "when": "AFTER",
  "events": "UPDATE",
  "for_each": "ROW",
  "update_of_columns": "email",
  "old_transition_name": "",
  "new_transition_name": "",
  "function": "public.notify_email_change",
  "condition": "OLD.email IS DISTINCT FROM NEW.email"
}
```

   `SnapTrigger` is simpler than a trigger declaration: it has no
   `args` field, and `events` is a single comma-separated string, not
   an array.  `update_of_columns` (comma-separated) and `condition` are
   flat string fields.
   `old_transition_name`/`new_transition_name` hold the `REFERENCING
   OLD TABLE AS ...`/`NEW TABLE AS ...` names (Section 7.9); empty string
   means that side of `REFERENCING` wasn't present, the same
   empty-means-unspecified convention `condition` itself already uses.

   **Function snapshot record:**

```json
{
  "schema": "public",
  "name": "get_user",
  "args": [{ "name": "p_email", "type": "text", "mode": "IN" }],
  "return_type": "public.users",
  "language": "plpgsql",
  "volatility": "STABLE",
  "strict": false,
  "security_definer": true,
  "parallel": "UNSAFE",
  "body_hash": "sha256:a3f7c91d...",
  "comment": "Fetch a user record by verified email address",
  "grants": [{ "privileges": ["EXECUTE"], "roles": ["app_service"], "with_grant": false }]
}
```

   **Virtual type snapshot record:**

```json
{
  "schema": "public",
  "name": "line_item",
  "body": {
    "kind": "composite",
    "fields": [
      { "name": "sku",  "type": { "kind": "type_ref", "name": "text" } },
      { "name": "tags", "type": { "kind": "type_ref", "schema": "public",
                                  "name": "label", "array": true,
                                  "virtual": true } }
    ]
  },
  "comment": null
}
```

   `body.kind` is `type_ref`, `composite` (with `fields`), or `union`
   (with `members`, each itself a body).  `json_format` is omitted when
   the virtual type uses the default, `jsonb`.

   **Grant record (used in all per-object grant arrays):**

```json
{ "privileges": ["SELECT"], "roles": ["app_readonly", "app_readonly2"], "with_grant": false }
```

   `roles` is an array of role names; there is no singular `grantee`
   field.

   **Function body hash:** The `body_hash` field is the string
   `"sha256:"` followed by the lowercase hex-encoded SHA-256 digest of
   the normalised function body (Section 9.5).  The full body text is
   NOT stored in the snapshot; it lives in the `.dpg` source files.

#### 16.3.1. SnapOpaque — Passthrough Object Records

   Objects whose diff is body-text based (procedures, aggregates,
   tablespaces, FDWs, servers, user mappings, publications,
   subscriptions, event triggers, collations, operators, operator
   classes, operator families, casts, statistics objects, and all four
   text search object types) are stored as `SnapOpaque`:

```json
{
  "kind": "procedure",
  "opaque": {
    "kind": "procedure",
    "schema": "public",
    "name": "process_settlements",
    "args": "",
    "body_hash": "sha256:b4f2a1...",
    "comment": null,
    "grants": []
  }
}
```

   Fields:

   | Field | Type | Description |
   |-------|------|-------------|
   | `kind` | string | Object kind (same as outer `kind`). |
   | `schema` | string | Schema name; empty for cluster-level objects. |
   | `name` | string | Object name. |
   | `args` | string | Type-only argument key for overloaded objects (procedures, aggregates). Empty for non-overloaded. |
   | `body_hash` | string | `"sha256:<hex>"` of the normalised Part 1 body text. Empty string means body was empty. |
   | `comment` | string\|null | Comment text if any. |
   | `grants` | array | Grant records (for aggregate and procedure grants). |

   The differ compares `body_hash` for changes.  Any change to the body
   hash causes the compiler to emit `DROP ... CASCADE` + `CREATE ...`
   for the object (Safety class per Section 17.2 for each type).

   See Sections 16.5 and 16.6 for the complete `SnapSchema`, `SnapExtension`,
   `SnapType`, `SnapSequence`, `SnapRole`, and cluster-level snapshot
   file records — not repeated here since they follow the same shape
   conventions established above.

### 16.4. Versioning

   The `dpg_version` field in the snapshot records the compiler version
   that wrote it.  Future major versions of DPG that change the snapshot
   schema MUST provide a migration path.  Minor-version changes MUST be
   backward compatible: a newer compiler MUST be able to read a snapshot
   written by an older minor version without data loss.

   The compiler MUST emit a warning when reading a snapshot whose
   `dpg_version` major component differs from the running compiler.

### 16.5. Further Object Records

   Complete records for all named sub-object types:

   **SnapSchema:**

```json
{
  "name": "analytics",
  "owner": "analytics_role",
  "comment": "Derived tables",
  "renamed_from": null
}
```

   **SnapExtension:**

```json
{
  "name": "pgcrypto",
  "schema": null,
  "version": null
}
```

   **SnapType** (ENUM):

```json
{
  "schema": "public",
  "name": "invoice_status",
  "variant": "ENUM",
  "values": ["draft", "sent", "paid", "void", "overdue"],
  "comment": "Billing lifecycle states"
}
```

   **SnapType** (COMPOSITE):

```json
{
  "schema": "public",
  "name": "address",
  "variant": "COMPOSITE",
  "composite_attrs": [
    { "name": "street", "type": "text" },
    { "name": "city",   "type": "text" }
  ]
}
```

   **SnapSequence:**

```json
{
  "schema": "public",
  "name": "order_number_seq",
  "unlogged": false,
  "owner": null,
  "comment": null,
  "grants": [],
  "revocations": [],
  "increment_by": 1,
  "min_value": 10000,
  "max_value": 99999999,
  "no_min_value": false,
  "no_max_value": false,
  "start_value": 10000,
  "cache": 50,
  "cycle": false,
  "as_type": "bigint",
  "owned_by": null,
  "security_labels": [],
  "name_maps": []
}
```

   **SnapRole:**

```json
{
  "name": "app_service",
  "can_login": true,
  "superuser": false,
  "create_db": false,
  "create_role": false,
  "inherit": true,
  "is_replication": false,
  "bypass_rls": false,
  "connection_limit": null,
  "password_hash": "sha256:...",
  "valid_until": null,
  "comment": null,
  "memberships": [],
  "configs": [],
  "security_labels": [],
  "name_maps": []
}
```

   A role's attributes are stored, including `password_hash`, a hash of
   the declared `PASSWORD` text (literal or `{{secret-uri}}` reference),
   so a changed password declaration is detected offline.


### 16.6. Cluster-Level Snapshot File

   Cluster-level objects (roles, tablespaces) are stored in a SEPARATE
   snapshot file from database-level objects.  The path is:

   `.dpg/snapshots/<cluster-name>/_cluster.json`

   The `database` field in the top-level snapshot record is absent
   (empty string / omitted) for cluster-level snapshots.  The compiler
   identifies a snapshot as cluster-level when `database` is empty.


### 16.7. Source Revision Detection

   The `source_revision` field in the snapshot and migration header is
   populated by reading `.git/HEAD` directly (no `git` binary required).

   **Algorithm:**

   1.  Read `.git/HEAD` relative to the current working directory.
       If not present, `source_revision` is empty string.

   2.  If the content starts with `"ref: "`, strip the prefix and read
       the file at `.git/<ref-path>`.  Example: `"ref: refs/heads/main"`
       → read `.git/refs/heads/main`.

   3.  Otherwise, the content itself is the commit hash (detached HEAD).

   4.  Trim whitespace.  If the result is at least 7 characters, take
       the first 7 characters as the short hash.  Otherwise, empty.

   This needs no `git` binary.  Packed refs and shallow clones get no
   special handling.


---

## 17. Migration Output Format

### 17.1. Output Structure

   The migration output is a plain-text SQL file (or stream) in a
   standardised format.  It consists of:

   1.  A **header block** with metadata comments.
   2.  A **transactional section** wrapped in `BEGIN;` / `COMMIT;`.
   3.  Optionally a **non-transactional section** outside any
       transaction block.

   When there are no changes, the output is the header block followed
   by `-- (no changes)`.

   **Header format:**

```sql
-- DPG Migration
-- Generated:       <RFC 3339 UTC timestamp>
-- Source revision: <git SHA or empty>
-- Cluster:         <cluster name>
-- Database:        <database name>
```

   **Per-operation annotations:**

   Each DiffOp is preceded by an annotation comment line:

```
-- [source: <file>:<line>][, safety: <class>][, mode: instruction-only]
```

   [DPG] **Source positions** are informational.  They are recomputed
   from the current source on every run, never stored in the snapshot,
   and never part of an object's identity, so adding comments or
   reorganising files after an apply never changes a diff.  Rules:

   -   The path is relative to the project root.
   -   The line is the line of the declaration that produced the op (for
       an object declared in several files, the declaration in the
       first file by path).  Positions are those of the source as
       written: macro definitions and spreads (Section 4.7) never shift
       the line numbers of the code around them.
   -   An op with no declaration (a `DROP` of an object no longer in
       source) has no `source:` annotation.

   The `source:` annotation is always present when the source position
   is known.  The `safety:` annotation is omitted for `SAFE` ops
   (since that is the expected normal case) and present for `CAUTION`
   and `DESTRUCTIVE` ops.  `mode: instruction-only` marks a step DPG
   does not perform (Section 17.3); transactional and non-transactional
   ops are identified by the section they appear in.

   **Full example output:**

```sql
-- DPG Migration
-- Generated:       2025-09-15T14:32:00Z
-- Source revision: a3f7c91
-- Cluster:         production
-- Database:        myapp

-- transactional
BEGIN;

-- source: schemas/public/tables/users.dpg:4
CREATE TABLE public.users (
    id    BIGINT GENERATED ALWAYS AS IDENTITY CONSTRAINT "pk_users" PRIMARY KEY,
    email TEXT NOT NULL CONSTRAINT "uq_users_email" UNIQUE
);

-- source: schemas/public/tables/users.dpg:4
COMMENT ON TABLE public.users IS 'Primary identity store';

-- source: schemas/public/tables/users.dpg:12
COMMENT ON COLUMN public.users.email IS 'Verified email address';

-- source: schemas/public/tables/users.dpg:13
ALTER TABLE public.users ALTER COLUMN email SET STATISTICS 300;

-- source: schemas/public/tables/users.dpg:8
GRANT SELECT         ON TABLE public.users TO app_readonly;

-- source: schemas/public/tables/users.dpg:8
GRANT SELECT (email) ON TABLE public.users TO reporting_role;

COMMIT;

--------

-- non-transactional
-- source: schemas/public/tables/users.dpg:20, safety: CAUTION
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON public.users (email);
```

   **Multiple databases.**  A PostgreSQL transaction cannot span
   databases.  When one command targets several databases, each
   database is a separate migration with its own transaction:

   -   Output shows one block per database, introduced by a `--
       Database: <name>` label (`(cluster)` for cluster-level objects),
       each with its own transactional and non-transactional sections.
   -   `dpg apply` applies a cluster's cluster-level objects first, then
       its databases one at a time in discovery order (Section 3.6).
       Each database's snapshot is updated only after that database's
       migration succeeds.  On the first failure, apply stops: databases
       already applied stay applied with updated snapshots, and the
       failing and remaining databases are untouched.

### 17.2. Safety Classification

   Every DiffOp MUST be assigned exactly one safety class.  [DPG] The
   class measures what can go wrong when the op runs: data loss
   (`DESTRUCTIVE`); locks, rewrites, or a change in what roles may see
   or what integrity rules hold (`CAUTION`); or none of these (`SAFE`).
   How the op runs is a separate property, its execution mode (Section
   17.3).

   | Class | Criteria | Default behaviour |
   |-------|----------|-------------------|
   | `SAFE` | No data loss possible; no excessive locking. | Applied automatically. |
   | `CAUTION` | Acquires `ACCESS EXCLUSIVE` lock for non-trivial duration; or reorders data; or may affect query plans. | Applied with a warning logged to stderr. |
   | `DESTRUCTIVE` | Data loss is possible (DROP TABLE, DROP COLUMN, type change without USING, etc.). | Blocked unless `--allow-destructive` is passed to `dpg apply`. |

   The full per-operation safety classification table is:

   | Operation | Safety |
   |-----------|--------|
   | `CREATE TABLE` | `SAFE` |
   | `CREATE TABLE ... PARTITION OF ...` | `SAFE` |
   | `ALTER TABLE ADD COLUMN ... [DEFAULT ...]` | `SAFE` |
   | `ALTER TABLE ADD COLUMN ... NOT NULL` with no `DEFAULT` | `CAUTION` (fails if the table has rows) |
   | `ALTER TABLE ADD COLUMN` that rewrites the table (see below) | `CAUTION` |
   | `ALTER TABLE ALTER COLUMN TYPE` (implicit cast) | `CAUTION` |
   | `ALTER TABLE ALTER COLUMN TYPE ... USING` | `CAUTION` |
   | `ALTER TABLE ALTER COLUMN TYPE` (no implicit cast, no USING) | `DESTRUCTIVE` |
   | `ALTER TABLE DROP COLUMN` | `DESTRUCTIVE` |
   | `ALTER TABLE ADD CONSTRAINT ... NOT VALID` | `CAUTION` |
   | `ALTER TABLE VALIDATE CONSTRAINT` | `CAUTION` |
   | `ALTER TABLE RENAME COLUMN` | `CAUTION` |
   | `ALTER TABLE RENAME TO` | `CAUTION` |
   | `ALTER TABLE ENABLE ROW LEVEL SECURITY` | `SAFE` |
   | `ALTER TABLE DISABLE ROW LEVEL SECURITY` | `SAFE` |
   | `DROP TABLE` | `DESTRUCTIVE` |
   | `CREATE INDEX` (new table) | `SAFE` |
   | `CREATE INDEX` (existing table, not concurrent) | `CAUTION` |
   | `CREATE INDEX CONCURRENTLY` | `CAUTION` (non-transactional) |
   | `DROP INDEX` | `CAUTION` |
   | `CREATE VIEW` | `SAFE` |
   | `CREATE OR REPLACE VIEW` | `SAFE` |
   | `DROP VIEW CASCADE` | `DESTRUCTIVE` |
   | `CREATE MATERIALIZED VIEW` | `SAFE` |
   | `DROP MATERIALIZED VIEW CASCADE` | `DESTRUCTIVE` |
   | `CREATE FUNCTION` / `CREATE OR REPLACE FUNCTION` | `SAFE` |
   | `DROP FUNCTION CASCADE` | `DESTRUCTIVE` |
   | `ALTER TYPE ... ADD VALUE` | `SAFE` (Section 1.4's version floor of 14 is past PostgreSQL 12's transaction-block restriction) |
   | `DROP TYPE CASCADE` | `DESTRUCTIVE` |
   | `CREATE POLICY` | `SAFE` |
   | `DROP POLICY` (permissive) | `SAFE` |
   | `DROP POLICY` (restrictive) | `CAUTION` |
   | `CREATE TRIGGER` | `SAFE` |
   | `DROP TRIGGER` | `CAUTION` |
   | `GRANT ...` | `SAFE` |
   | `REVOKE ...` | `SAFE` |
   | `CREATE SEQUENCE` | `SAFE` |
   | `DROP SEQUENCE` | `DESTRUCTIVE` |
   | `ALTER SCHEMA RENAME TO` | `CAUTION` |
   | `DROP SCHEMA CASCADE` | `DESTRUCTIVE` |
   | `DROP EXTENSION CASCADE` | `DESTRUCTIVE` |
   | `DROP ROLE` | `DESTRUCTIVE` |

   **`ADD COLUMN` that rewrites the table.**  `ADD COLUMN` is `CAUTION`
   when PostgreSQL must write a value into every existing row, which
   rewrites the table under an `ACCESS EXCLUSIVE` lock:

   -   an identity column, a `SERIAL`-family column (Section 10), or a
       `STORED` generated column;
   -   a `DEFAULT` that calls a volatile function;
   -   a column whose type is a domain with a `NOT NULL` or `CHECK`
       constraint.

   A constant or non-volatile `DEFAULT` is stored in the catalog without
   touching rows, and a `VIRTUAL` generated column is computed on read;
   both stay `SAFE`.  Volatility is decided offline.  A built-in
   function has its `pg_catalog` volatility (a name with any `VOLATILE`
   overload counts as volatile).  A function declared in the project has
   its declared volatility, `VOLATILE` when none is declared, as in
   PostgreSQL.  Any other function has unknown volatility and counts as
   volatile.

### 17.3. Execution Modes

   [DPG] Every DiffOp has exactly one execution mode, independent of
   its safety class:

   -   **Transactional** — runs inside the migration's transaction.  The
       default for every op.

   -   **Non-transactional** — runs on its own, outside the migration's
       transaction.  Usually it runs after `COMMIT`, because PostgreSQL
       refuses the statement inside a transaction block:
       `CREATE`/`DROP INDEX CONCURRENTLY`, `ALTER TABLE ... DETACH
       PARTITION ... CONCURRENTLY`, `CREATE`/`DROP TABLESPACE`, `CREATE`/
       `DROP SUBSCRIPTION`, and statements that must follow one of these
       (a new subscription's comment, a new tablespace's grants).  An
       enum `ADD VALUE` whose new value the transaction uses runs before
       `BEGIN` instead (Section 5.1.1).

   -   **Instruction-only** — an SQL comment describing a step DPG does
       not perform.  It is shown in the plan and never executed.  A
       `DESTRUCTIVE` instruction (a partition strategy rebuild, Section
       7.13) blocks `dpg apply` until `--approve-partition-rebuild`
       acknowledges it; a `SAFE` instruction is an informational note
       (for example, a reminder to remove a one-shot `RESTART`).

   The executor MUST execute all transactional ops within a single
   transaction per database.  If any transactional op fails, the
   entire transaction is rolled back.

   Non-transactional ops are executed sequentially after `COMMIT`,
   each as an individual statement outside any transaction.  If a
   non-transactional op fails, it does NOT roll back the already-
   committed transactional ops.  The operator MUST manually handle the
   partial failure.

### 17.4. Idempotency Requirement

   The idempotency guarantee is:

   > Running `dpg apply` on a database that already exactly matches
   > the desired state MUST produce zero SQL statements.

   Any violation of this guarantee is a defect in the DPG compiler.

   Idempotency is enforced through:

   -   The snapshot accurately reflecting the post-apply state.
   -   The Differ emitting ops only when the snapshot and desired state
       genuinely differ.
   -   `CREATE INDEX IF NOT EXISTS` in concurrent index creation.
   -   `CREATE EXTENSION IF NOT EXISTS` for extensions.

   If a `dpg apply` is interrupted partway through, the snapshot will
   not have been updated (the snapshot is written only upon full
   success).  On the next `dpg apply`, the transactional ops will be
   re-attempted.  The already-executed non-transactional ops (e.g.,
   concurrent index creations) will result in `IF NOT EXISTS` no-ops.

---

## 18. CLI Commands

   **Global options**, accepted before the subcommand name by every
   command below:

```
dpg [global options] <command> ...

Global options:
  --dir <path>, -C <path>   Project root directory (default: current
                             working directory).
  --env <path>              Path to a .env file used to resolve link =
                             connection strings (default:
                             <project-root>/.env, if present; a missing
                             file is not an error). Only consulted for
                             commands that may need a live database
                             connection: dpg apply, dpg verify, dpg dump,
                             and dpg plan --live.
```

   **`.env` loading rules:**

   1.  Loading is only performed when at least one cluster uses a
       `link` connection string; clusters using an inline `url =`
       never trigger it.
   2.  Path resolution order: the path given by `--env <path>`, if
       provided; otherwise `<project-root>/.env`, if it exists.
   3.  Existing process environment variables are NEVER overwritten —
       the file only sets variables not already present in the
       process environment ("process env wins").
   4.  File format: blank lines and lines starting with `#` are
       ignored; a leading `export ` is stripped; entries are
       `KEY=VALUE` or `KEY = VALUE`; single/double-quoted values have
       the quotes stripped.
   5.  A missing `.env` file is not an error — the command proceeds
       using only the process environment.

### 18.1. dpg plan

   Computes the migration that would be applied and prints it to
   stdout.  No database connection is required by default.

```
dpg plan [options] [<cluster>[/<database>]]

Options:
  --live                 Diff against the live catalog instead of the
                         committed snapshot. Requires a database connection.
  --format <fmt>         Output format: text (default), json. The text
                         format is the emitted SQL migration itself.
  --watch                Re-run whenever source files change (polls every 500ms).
  --cluster <name>       Target a specific cluster (default: all clusters).
  --database <name>      Target a specific database (default: all databases).
```

   [DPG] With `--live`, an object in the live catalog that the source
   doesn't declare is left alone: `dpg plan --live` never proposes
   dropping it, and lists it after the migration (roles and tablespaces
   as warnings, other objects as unmanaged).  The live catalog can't
   tell an object removed from the source from one DPG never managed;
   removing a declared object is planned against the snapshot.

   `dpg plan` always shows the full computed migration, including any
   `DESTRUCTIVE` operations — there is no `--allow-destructive` or
   `--no-color` flag on `plan`; those only gate `dpg apply` (Section 18.2),
   since `plan` never executes anything.  `dpg plan` runs the linter
   (Phase 8, Section 15.10) but has no `--strict` flag either — its lint
   diagnostics are always advisory-only, never blocking; use
   `dpg validate --strict` or `dpg apply --strict` to promote them to
   errors.

   Exit codes: 0 = success (the plan was computed and printed, whether or
   not it contains any operations); non-zero = error (compilation failure,
   lint error, connection error under `--live`). Unlike `dpg verify`
   (Section 18.3), `dpg plan` does not use its exit code to signal whether
   changes were found — check the printed output, or `--format json`'s
   `"empty"` field, for that.

   With `--format json`, each targeted database's plan is serialised to
   stdout as one JSON object:

```json
{
  "cluster":         "production",
  "database":        "myapp",
  "generated_at":    "2026-05-13T14:32:00Z",
  "source_revision": "a3f7c91",
  "ops": [
    {
      "sql":    "CREATE TABLE public.users (...);",
      "safety": "SAFE",
      "file":   "schemas/public/tables/users.dpg",
      "line":   4
    }
  ],
  "empty": false
}
```

   | Field | Type | Description |
   |-------|------|-------------|
   | `cluster` | string | Cluster name. |
   | `database` | string | Database name; empty for cluster-level plans. |
   | `generated_at` | RFC 3339 string | UTC timestamp of plan generation. |
   | `source_revision` | string | Git short SHA, or empty if unavailable. |
   | `ops` | array | Ordered list of DiffOp objects. |
   | `ops[].sql` | string | The SQL statement text. |
   | `ops[].safety` | string | One of `"SAFE"`, `"CAUTION"`, `"DESTRUCTIVE"`. |
   | `ops[].mode` | string | One of `"transactional"`, `"non-transactional"`, `"instruction-only"` (Section 17.3). |
   | `ops[].file` | string | Source file path relative to project root, or omitted if unknown. |
   | `ops[].line` | integer | 1-based source line number, or omitted if unknown. |
   | `empty` | boolean | `true` when `ops` is empty (no changes). |

   When targeting multiple databases in one run, each produces its own
   complete JSON object; multiple objects are printed sequentially,
   separated by newlines (NDJSON, [NDJSON]).

### 18.2. dpg apply

   Runs the linter, computes the migration, prompts for operator
   approval, executes the SQL, and updates the snapshot.

```
dpg apply [options] [<cluster>[/<database>]]

Options:
  --allow-destructive        Allow DESTRUCTIVE operations (required if any
                             exist; operator must confirm interactively unless
                             --yes is also set).
  --approve-partition-rebuild  Acknowledge instruction-only partition-rebuild steps.
  --yes / -y                 Skip interactive approval prompt (non-interactive
                             mode; implies --allow-destructive is acknowledged).
  --dry-run                  Compute and print the migration but do not execute.
  --no-snapshot              Do not update the snapshot after apply.
  --strict                   Promote linter warnings to errors. Apply is
                             blocked if any lint errors exist.
  --cluster <name>
  --database <name>
```

   **Apply procedure:**

   1.  Run the linter.  If any error-level diagnostics exist (or
       any warnings with `--strict`), abort and print diagnostics.
   2.  Compute the migration (`dpg plan`).  If there are no operations,
       print "already up to date" and exit successfully — none of the
       remaining steps run.
   3.  If the migration contains executable `DESTRUCTIVE` ops and
       `--allow-destructive` is absent, abort with error DPG-E025. This check runs before
       `--dry-run`'s early exit (step 5) — `--dry-run` alone does not
       bypass the destructive-operations gate; a preview of a destructive
       migration still requires `--allow-destructive`.
   4.  If the migration contains a `DESTRUCTIVE` instruction-only step
       (a partition strategy rebuild) and `--approve-partition-rebuild` is
       absent, abort naming the step.  `SAFE` instruction-only notes never
       block apply.
   5.  Print the migration SQL. If `--dry-run`, stop here — the migration
       is not executed and the snapshot is not updated.
   6.  Unless `--yes`, prompt: `Apply this migration? [y/N]`.  Abort if
       the operator does not confirm.
   7.  Execute transactional ops in a single `BEGIN`/`COMMIT` per
       database.
   8.  Execute non-transactional ops sequentially. Instruction-only
       steps are never sent to the executor — they are the operator's
       own responsibility outside of `dpg apply`.
   9.  Update the snapshot atomically (write to a temp file, rename),
       unless `--no-snapshot` is set.
   10. Print `Applied` to stdout, including the archived migration
       file's path, unless `--no-snapshot` was set (noted instead).

   If step 7 fails, the transaction is rolled back.  The snapshot is
   not updated.  The error from the database is printed with the
   failing SQL statement highlighted.

### 18.3. dpg verify

   Introspects the live catalog and compares it with the snapshot
   (Section 20.2).

```
dpg verify [options]

Options:
  --cluster <name>    Target a specific cluster (default: all clusters).
  --database <name>   Target a specific database (default: all databases).
  --format <fmt>      Output format: text (default), json.
```

   Each finding is an item in one of five groups:

   -   **drift:** a declared object or property differs from the
       snapshot, is missing from the live catalog, or a declared grant
       or revocation no longer holds.
   -   **conflict:** drift on a property the source has also changed
       since the snapshot was written, to a different value.
   -   **converged:** drift on a property the source has also changed,
       to the value the live catalog already has; the snapshot is out
       of date.
   -   **unmanaged:** a live object the snapshot doesn't record.  Not
       drift.
   -   **warning:** a live role or tablespace the snapshot doesn't
       record, or a live grant the source didn't declare.  Not drift.

   Objects matching an `[ignore]` pattern (Section 3.9) are not
   compared.  Every item has a stable ID:
   `<kind>:<object>[/<property>]`, where `<object>` is the snapshot key
   and `<property>` names a property (`connection_limit`), a sub-object
   (`column:email`, `index:orders_status_idx`) or a grant
   (`grant:SELECT:app_ro`, `column:email/grant:SELECT:app_ro`).  The
   characters `/`, `:`, `%` and whitespace in a name are written as `%`
   followed by two hexadecimal digits.

   The text format groups the items per cluster and database and shows,
   for each, the snapshot and live values and, when they can be computed,
   the statements that would restore the declared state.  They can't be
   computed for an object whose source declaration has changed since the
   snapshot was written.  `--format json` prints
   one document:

```json
{
  "drift": true,
  "targets": [
    {
      "cluster": "production",
      "database": "(cluster)",
      "items": [
        {
          "id": "role:app_service/connection_limit",
          "group": "drift",
          "kind": "role",
          "object": "app_service",
          "property": "connection_limit",
          "change": "changed",
          "snapshot": "20",
          "live": "50",
          "sql": ["ALTER ROLE \"app_service\" CONNECTION LIMIT 20;"]
        }
      ]
    }
  ]
}
```

   `change` is `changed`, `missing` (in the snapshot only), `extra`
   (live only) or `regranted` (a privilege the source revokes is granted
   live).  An item also carries `source`, the source's value of the
   property when the source declares it, and, once acknowledged
   (Section 18.4), `acknowledged` (the acknowledgement) or `lapsed`
   (`true`: an acknowledgement of the item no longer applies).

   An acknowledged item is listed in its own section and doesn't count
   as drift.  A lapsed acknowledgement is noted on its item.

   Exit codes: 0 = no drift (unmanaged objects, warnings and
   acknowledged items allowed); 1 = drift, a conflict or a converged
   item in at least one targeted cluster or database; 2 = error.

### 18.4. dpg resolve

   Resolves the items `dpg verify` reports (Section 18.3).  Needs a live
   database connection.

```
dpg resolve [options]

Options:
  --cluster <name>     Target a specific cluster (default: all clusters).
  --database <name>    Target a specific database (default: all databases).
  --adopt <id>         Make the source and the snapshot match the live
                       catalog.
  --revert <id>        Make the live catalog match the declared state.
  --ignore <id>        Add an [ignore] pattern for the item's object.
  --ack <id>           Acknowledge the item.
  --unack <id>         Remove the item's acknowledgement.
  --reason <text>      Why the items are acknowledged.
  --expires <date>     The acknowledgements lapse on this date
                       (YYYY-MM-DD).
  --macro <mode>       Adopting a part a macro declares: edit or inline.
  --allow-destructive  Allow a revert to run DESTRUCTIVE statements.
  --dry-run            Show the changes; change nothing.
  --yes, -y            Skip the confirmation prompt.
```

   Without an action option, in a terminal, `dpg resolve` is
   interactive: the items are shown as a tree (cluster or database,
   group, object, item), chosen with keys for each action, on an item
   or on a whole object, group or database, then previewed and
   applied.  Without a terminal and without an action option it is an
   error.

   Each action option takes item IDs (Section 18.3) and may be repeated
   or given a comma-separated list.  An ID must name an item in exactly
   one targeted cluster or database.  `--ack` and `--unack` also take an
   object's ID (`<kind>:<object>`), standing for each of its items.

   **Actions per group** [DPG]:

   | Group | adopt | revert | ignore | acknowledge |
   |-------|-------|--------|--------|-------------|
   | drift, conflict | yes | when `dpg verify` shows the restoring statements | — | yes |
   | converged | yes | — | — | yes |
   | unmanaged | yes | — | yes | yes |
   | warning: role, tablespace | yes | — | yes | yes |
   | warning: grant | yes | — | — | yes |

   An acknowledged item can only be unacknowledged.  An unmanaged
   object is never reverted: DPG doesn't drop what it doesn't manage.

   **adopt** [DPG] edits the source the way the change would have been
   written, and updates the snapshot entry from the edited source.  Live
   values are written as `dpg dump` writes them (Section 18.5).

   -   A changed property: the text declaring it is replaced.
   -   A part the source lacks: a column or table constraint is added at
       the end of the column list, laid out like its entries; another
       Part 1 clause at the end of Part 1; a block statement at the end
       of the block, inside the group it belongs to (`INDEXES { }`,
       `COLUMNS { c { } }`, `GRANTS { }`, ...), which is created when
       missing.  A declaration without a `{ }` block gets one.
   -   A part missing live: its text is removed, with its separator, and
       so is a group it was the only entry of.
   -   A live object the source doesn't declare: its declaration is
       appended to `schemas/<schema>/tables/<table>.dpg` for a table;
       `views.dpg`, `functions.dpg` (functions, procedures, aggregates),
       `types.dpg` or `sequences.dpg` in `schemas/<schema>/` for those
       kinds; `schemas/<schema>/schema.dpg` for a schema and its other
       objects; `objects.dpg` in the database directory for an object
       outside a schema; `roles.dpg` or `tablespaces.dpg` in the
       cluster objects directory.  The undeclared objects it depends on
       (its schema, the types it uses, the tables its foreign keys
       reference, and theirs) are adopted with it.
   -   An object missing live: its declarations are removed.
   -   A grant: a live grant is added as a `GRANTS` entry; a declared
       privilege the role doesn't hold is taken out of the entries
       granting it (a declared `ALL` becomes the privileges the role
       holds; a missing `WITH GRANT OPTION` leaves the plain privilege);
       a privilege the source revokes and the role holds is taken out
       of `REVOKES` and added to `GRANTS`.
   -   A converged item: only the snapshot changes.
   -   A part produced by a macro spread (Section 4.7) needs `--macro`:
       `edit` changes the macro's definition, and so every use of it,
       and needs the part's text to occur once in the definition;
       `inline` replaces this spread with the macro's text and changes
       it there.

   A change adopt can't make is refused with the reason: grants on
   parameters, default privileges and privileges with column lists, a
   part from a macro defined outside the database's source, a part
   declared in different groups in the source and in `dpg dump`'s
   form.

   **revert** [DPG] runs the statements `dpg verify` shows for the item
   the way `dpg apply` runs a migration (Section 18.2): the owner
   membership check, the migration archive, and DESTRUCTIVE statements
   only with `--allow-destructive`.  The snapshot doesn't change.

   **ignore** [DPG] adds the object's pattern to the `[ignore]` table
   (Section 3.9) of the database's `dpg.toml`, or the cluster's for a
   cluster-level object, keeping the file's comments and layout.  A
   routine's pattern omits its argument list.

   **acknowledge** [DPG] records the item in
   `.dpg/acknowledged/<cluster>/<database>.json` (`_cluster` as the
   database for cluster-level objects):

```json
{
  "version": 1,
  "acknowledgements": [
    {
      "id": "role:app_service/connection_limit",
      "fingerprint": "4f0c2b9e1d7a3c55",
      "reason": "raised during the incident",
      "by": "Dana Smith",
      "at": "2026-10-12T12:00:00Z",
      "expires": "2026-11-01T00:00:00Z"
    }
  ]
}
```

   `by` is git's `user.name`, else `$USER`.  The fingerprint covers the
   item's snapshot, live and source values.  An acknowledgement lapses
   when they change or its expiry date passes; the item is then
   reported again.  **unacknowledge** removes the item's entry; the
   file is removed with its last entry.

   **Process** [DPG]:

   1.  Every decision is planned before anything is written.  When one
       can't be carried out, the reasons are printed and nothing
       changes.
   2.  The plan is shown: a diff of each file, the snapshot entries,
       the revert statements and the acknowledgement changes.  Then
       confirmation is asked (`--yes` skips it; `--dry-run` stops).
   3.  Reverts run; then the files, snapshots and acknowledgement
       files are written.
   4.  The project is compiled and drift detected again.  If a
       resolved item is still reported (an acknowledged one isn't
       acknowledged, an unacknowledged one still is) or a new item
       appears, every file, snapshot and acknowledgement change is
       undone.  A revert that ran is not undone.

   Exit codes: 0 = resolved, or nothing to change; 2 = error (an
   unknown item, a decision that can't be carried out, a failed check).

### 18.5. dpg dump

   Introspects a live database catalog and produces an initial `.dpg`
   source tree and snapshot, suitable for bootstrapping a new DPG
   project from an existing database.

```
dpg dump [options]

Options:
  --cluster <name>    REQUIRED. The cluster to introspect.
  --database <name>   REQUIRED. The database to introspect.
  --output / -o <dir> Output directory for .dpg files
                      (default: <cluster>/<database>/ within project root).
                      When set, sandboxes ALL output under this directory —
                      including cluster-level roles.dpg and the snapshot,
                      not just the per-database source files — so a dump
                      run this way never touches the real project.
  --yes / -y          Skip the interactive overwrite confirmation below
                      (for scripts/CI). The warning still always prints.
```

   Overwrite protection: if any target `.dpg` file already exists on disk,
   `dpg dump` refuses to proceed silently. It prints a warning naming
   every file that would be overwritten, then requires two separate
   confirmations before continuing — an initial `[y/N]` prompt, followed
   by typing the literal word `overwrite` — matching the type-the-word
   confirmation pattern other tools use for an action this hard to
   reverse. `--yes` skips both prompts for non-interactive use, but the
   warning listing the affected files is still always printed first,
   regardless. When nothing targeted by this dump already exists (a
   brand-new project, or a fresh `-o` directory), there is no warning and
   no prompt at all — re-running `dpg dump` is exactly as safe as before
   in that case. The generated snapshot is deliberately excluded from
   this check: it is a derived/managed artifact `dump` is always expected
   to refresh, not hand-authored source.

   `--output`/`-o` sandboxing detail: without `-o`, cluster-scoped output
   (`roles.dpg`) and the snapshot are written to their real, permanent
   project locations — the cluster's own objects directory and the
   registered snapshot store — exactly as if `dpg dump` were run with no
   flags at all. With `-o <dir>` set, both are redirected under `<dir>`
   instead: `roles.dpg` to `<dir>/<cluster-name>/cluster/roles.dpg`
   (namespaced by cluster name so a single `-o` value spanning multiple
   clusters in one invocation cannot mix their roles together), and the
   snapshot to `<dir>/.dpg/snapshots/`, mirroring the real project's own
   layout convention.  With `-o`, nothing is written to the real
   project.

   The dump output is a best-effort conversion.  An individual object
   whose DDL cannot be cleanly reconstructed from catalog information at
   all (e.g. a `COLLATION` or `OPERATOR CLASS` introspected with no
   reconstructable attributes/members left after DPG's own filtering) is
   omitted from the dump entirely rather than emitted as invalid DDL —
   and `dump` prints a
   `warning` line to stderr naming the omitted object and its kind, so
   the operator knows their dump is incomplete and can re-declare that
   object by hand. There is no `-- dpg:manual`-style marker comment in
   the generated source itself, only the stderr warning at dump time.

### 18.6. dpg diff

   Diffs two DPG source directories targeting the same logical database,
   without requiring a snapshot or a live database connection.

```
dpg diff --from <dir> --to <dir>

Options:
  --from <dir>         REQUIRED. "Before" source directory.
  --to <dir>           REQUIRED. "After" source directory.
```

   The result is always printed as SQL to stdout — there is no `--format`
   or `--allow-destructive` flag; unlike `dpg apply`, `dpg diff` never
   executes anything, so a destructive-operations gate does not apply.

   Both directories MUST contain a `dpg.toml` with a `[database]`
   section identifying the same logical database.

### 18.7. dpg validate

   Compiles and lints `.dpg` source files offline.  No snapshot or
   database connection required.

```
dpg validate [file...] [options]

Options:
  --cluster <name>   cluster to validate (default: all)
  --database <name>  database to validate (default: all)
  --format <fmt>     output format: text or json (default: text)
  --strict           promote all lint warnings to errors (non-zero exit)
```

   When one or more `.dpg` files are given as arguments, only those
   files are validated, with no project discovery — this mode is what
   an editor/LSP integration uses to validate a single file or buffer.

   Exit codes: 0 = no errors; non-zero = errors found or internal
   error.

   With `--format json` the output is a **single JSON object per
   cluster/database scope**, not an array — multiple scopes each emit
   a separate JSON object (one line per scope is NOT guaranteed; each
   is a complete JSON object):

```json
{
  "cluster": "production",
  "database": "myapp",
  "objects": 42,
  "errors": [
    {
      "rule": "hardcoded-password",
      "message": "column 'password' has a hardcoded string default",
      "file": "schemas/public/tables/users.dpg",
      "line": 12,
      "col": 5
    }
  ],
  "warnings": []
}
```

   Fields:

   | Field | Type | Description |
   |-------|------|-------------|
   | `cluster` | string | Cluster name. |
   | `database` | string | Database name; `"(cluster)"` for cluster-level objects. |
   | `objects` | integer | Number of IR objects successfully compiled. |
   | `errors` | array | Diagnostics with `IsError = true`. Empty array if none. |
   | `warnings` | array | Diagnostics with `IsError = false`. Empty array if none. |

   Each diagnostic object has `rule`, `message`, `file`, `line`, `col`.
   `rule` uses hyphen-separated IDs (e.g., `"hardcoded-password"`), per
   Section 19.3.

### 18.8. dpg fmt

   Reformats `.dpg` source files in place according to a canonical style.

```
dpg fmt [options] [<file> ...]

Options:
  --check    Exit with code 1 if any file would be reformatted.
             Does not modify files. Useful as a CI gate.
  --diff     Print a unified diff of the proposed reformatting.
             Does not modify files.
  --stdin    Read source from stdin, write formatted output to stdout.
             Ignores any <file> arguments and --check/--diff; for editor
             integrations that pipe a buffer's content through the
             formatter rather than operating on a file on disk.
```

   Configured via the root `dpg.toml` `[fmt]` section (Section 3.2); the
   TOML keys are `indent` (default 4) and `keyword_case` (`"upper"` or
   `"lower"`, default `"upper"`).

   Canonical style rules:

   -   Indentation: configurable (default 4 spaces).
   -   Keyword casing: `"upper"` uppercases DPG/PostgreSQL keywords;
       `"lower"` lowercases them.
   -   Column alignment: column names and types are aligned in `( )`
       lists.
   -   Trailing whitespace: stripped.
   -   Blank lines: one blank line between top-level declarations.
   -   Comment style: `--` for single-line, `/* */` for multi-line.

   Formatting changes layout only, never meaning: every clause of Part 1
   and Part 2, every comment, and every macro spread (`...name`) is kept
   as written.  A comment that ends Part 1 (just before the `{` or `;`)
   is moved to the line before the declaration; a comment after the
   terminating `;` or `}` stays on that line.  A file that cannot be
   parsed is reported and left unchanged, and the command exits with
   code 1, with or without `--check`.  `--stdin` instead writes such
   input back unchanged.

   `dpg fmt` deliberately does NOT touch identifier casing — unlike
   keyword casing, identifiers are never rewritten, in either
   direction.  This is intentional, not a gap: DPG's macro
   preprocessor (Section 4.7) spreads identifiers across files verbatim, and
   PostgreSQL identifiers are case-sensitive once quoted, so a
   formatter that normalized casing could silently change which
   objects a macro-expanded reference resolves to.  A case mismatch
   that changes meaning is a compiler error (see the linter/compiler),
   never something the formatter guesses at or silently corrects.

### 18.9. dpg portability

   Reports all PostgreSQL-specific constructs in use with SQL standard
   alternatives noted where available.

```
dpg portability [options]

Options:
  --cluster <name>   cluster to analyze (default: all)
  --database <name>  database to analyze (default: all)
  --format <fmt>     output format: text or json (default: text)
```

   `--format json` emits one JSON object per cluster/database analyzed
   (`{"cluster", "database", "issues": [...]}`, each issue carrying
   `construct`/`alternative`/`file`/`line`/`col`) — suitable for CI or
   other tooling, as an alternative to the default human-readable text
   output.

   This command is OPTIONAL; it MUST NOT be a compilation gate.

### 18.10. dpg init

   Scaffolds a new project with the standard directory layout and
   `dpg.toml` files.

```
dpg init [options] [<dir>]

Options:
  --cluster <name>   Cluster directory name (default: "production").
  --database <name>  Database directory name (default: "myapp").
  --schema <name>    Default schema name (default: "public").
  --url <url>        PostgreSQL connection URL (can be set later in dpg.toml).
```

   Existing files are skipped (not overwritten).  Directories are
   created unconditionally.  Files created:

```
<dir>/dpg.toml                              root config
<dir>/<cluster>/dpg.toml                    cluster config
<dir>/<cluster>/<database>/dpg.toml         database config
<dir>/<cluster>/cluster/                    cluster objects dir (empty)
<dir>/<cluster>/<database>/schemas/<schema>/  schema source dir (empty)
<dir>/.dpg/snapshots/                       snapshot storage
```

### 18.11. dpg completion

   Generates shell completion scripts.

```
dpg completion <shell>

<shell>: bash | zsh | fish | powershell
```

### 18.12. dpg docs

   Serves the DPG documentation locally over HTTP.

```
dpg docs [options]

Options:
  --port <n>, -p <n>   Port to serve on (default: 6060).
  --open                Open the browser automatically.
```

   The documentation site is compiled into the `dpg` binary at release
   build time (`make build-full`, tag `embeddata`) and served entirely
   offline — no network access or live database connection is required.
   A development build (plain `make build`) does not embed the
   documentation; running `dpg docs` from such a build prints an error.

### 18.13. Target Selection

   When `--cluster` or `--database` are not specified, the compiler
   applies the following auto-selection algorithm:

   **Cluster auto-selection:**

   1.  If there is exactly one cluster in the project, it is selected
       automatically.  No `--cluster` flag is required.
   2.  If there are multiple clusters and `--cluster` is not set, the
       compiler MUST emit error DPG-E026 listing all available cluster
       names.
   3.  If `--cluster` is set to a name that does not exist, the
       compiler MUST emit error DPG-E027 with the available cluster
       names.

   **Database auto-selection** (within a selected cluster):

   1.  If there is exactly one database in the cluster, it is selected
       automatically.
   2.  If there are multiple databases and `--database` is not set, the
       compiler MUST emit error DPG-E028 listing the available database
       names.
   3.  If `--database` is set to a name that does not exist, the
       compiler MUST emit error DPG-E029 with the available database
       names.

   These rules apply to: `dpg plan`, `dpg apply`, `dpg verify`,
   `dpg dump`.


---

## 19. The Linter

### 19.1. Built-in Rules

   Rule IDs use hyphens, not underscores (a `[linter.rules]` key given
   with underscores is still accepted — Section 19.2 — but the ID as
   reported in diagnostics and `dpg validate --format json`'s `rule`
   field is always the hyphenated form below):

   | Rule ID | Description | Default Level |
   |---------|-------------|---------------|
   | `hardcoded-password` | A table column's `DEFAULT` is a hardcoded string literal, for a column whose name contains `password`/`passwd`/`pwd`/`secret`/`passphrase` (case-insensitive substring match). | Error |
   | `hardcoded-role-password` | A `ROLE`'s `PASSWORD` is a literal value with no `{{secret-uri}}` placeholder. A separate rule from `hardcoded-password` above — different check, different object kind. | Error |
   | `hardcoded-fdw-password` | A `USER MAPPING`'s `OPTIONS` has a literal value under a password-like key (same 5-key list as `hardcoded-password` above), with no `{{secret-uri}}` placeholder. | Error |
   | `deprecated` | An object or column is itself marked `DEPRECATED`. | Warning |
   | `deprecated-reference` | A non-deprecated `FOREIGN KEY` references a deprecated table/column, or a non-deprecated column/function-parameter/function-return-type references a deprecated custom `TYPE`. Does not cover references inside a view query, a function or procedure body, or a column `DEFAULT`. | Warning |
   | `missing-column-comment` | A column has no `COMMENT` and `require_column_comments = true`. | Warning (configurable to Error) |
   | `column-count-exceeded` | A table has more columns than `max_columns_per_table`. | Error |
   | `security-definer-search-path` | A `SECURITY DEFINER` function has no explicit `SET search_path`. | Warning |
   | `serial-sequence-declared` | A hand-declared `SEQUENCE` collides with the auto-managed sequence name for a `GENERATED ... AS IDENTITY` column or a `SERIAL`/`BIGSERIAL`/`SMALLSERIAL` column's owned sequence (`<table>_<column>_seq`) in the same desired state. | Warning |
   | `unnecessary-revocation` | A `REVOKES` entry names a (role, privilege) pair with no matching `GRANTS` entry in that *same object's own declaration*. | Warning |
   | `min-pg-version` | A declared construct requires a PostgreSQL major version newer than the project's effective `min_pg_version` (Section 3.8). | Warning |
   | `concurrently-new-table` | An index declares `CONCURRENTLY` on a table or materialized view that is not in the snapshot, where it has no effect (Section 7.7). Checked only when a snapshot is loaded (`dpg plan`, `dpg apply`). | Warning |
   | `stale-renamed-from` | `RENAMED FROM` directive references a name not in the snapshot (DPG-E021). Detected while differencing; see below. | Error |
   | `unguarded-enum-removal` | An ENUM value is removed without a `MIGRATE REMOVE` block (DPG-E014). Detected while differencing; see below. | Error |
   | `protected-drop-attempt` | The diff would drop a `PROTECTED` object (DPG-E022). Detected while differencing; see below. | Error |

   `stale-renamed-from`, `unguarded-enum-removal`, and
   `protected-drop-attempt` need the snapshot, so they are checked
   during differencing (Phase 9) rather than linting.  They always stop
   `plan` and `apply`, and `[linter.rules]` cannot change their
   severity.
### 19.2. Configuration

   All linter rules are configurable in the root `dpg.toml`
   `[linter]` section.  The `warn_on_deprecated`, `require_column_comments`,
   `forbid_hardcoded_passwords`, and `max_columns_per_table` fields are
   described in Section 3.2.

   Individual rules MAY be set to `"error"`, `"warning"`, or `"off"`
   via `[linter.rules]`. Keys may be written with hyphens or underscores
   interchangeably — underscores are normalized to hyphens before
   matching a rule ID:

```toml
[linter.rules]
security-definer-search-path = "error"
serial-sequence-declared      = "off"
```

### 19.3. Rule Scope

   [DPG] Rule identifiers use hyphens.  Notes on the scope of rules
   whose table entry needs more detail:

   -   **`hardcoded-password`** checks a table column's `DEFAULT`: an
       error when the column name contains `password`, `passwd`, `pwd`,
       `secret`, or `passphrase` (case-insensitive) and the default is a
       string literal.  `hardcoded-role-password` checks a role's
       `PASSWORD` for the absence of any `{{...}}` placeholder.
   -   **`serial-sequence-declared`** fires when a declared `SEQUENCE`
       is named `<table>_<column>_seq` for an identity or `SERIAL`-family
       column in the same desired state, the name PostgreSQL manages
       itself (Section 19.3).
   -   **`unnecessary-revocation`** compares `REVOKES` with `GRANTS`
       within one object's own declaration; it does not consult grant
       history.
   -   **`deprecated-reference`** fires when a non-deprecated object
       references a deprecated one through a `FOREIGN KEY` (to a
       deprecated table or column), or through the declared type of a
       column, function parameter, function return value, or procedure
       parameter (a deprecated custom `TYPE`).  An object that is itself
       deprecated does not also fire it.  References inside a view
       query, a function or procedure body, or a column `DEFAULT` are
       not checked, since they would need analysis of opaque SQL text
       that could produce false warnings.


---

## 20. Introspection Engine

### 20.1. Catalog Tables Read

   The introspection engine
   connects to a live PostgreSQL 14+ catalog and reads the following
   system tables and views:

   | Catalog object | Used for |
   |----------------|----------|
   | `pg_class` | Tables, views, materialized views, sequences, indexes |
   | `pg_attribute` | Column definitions (incl. `attstattarget`, `attcompression`, `attstorage`) |
   | `pg_constraint` | Table constraints |
   | `pg_index` | Index definitions |
   | `pg_proc` | Functions, procedures, aggregates |
   | `pg_trigger` | Trigger definitions |
   | `pg_policy` | Row security policies |
   | `pg_type` | Types (ENUMs, composites, domains, ranges, base) |
   | `pg_enum` | ENUM values |
   | `pg_namespace` | Schemas |
   | `pg_extension` | Installed extensions |
   | `pg_publication` | Publications |
   | `pg_subscription` | Subscriptions (all attributes except `subconninfo`, Section 13.2) |
   | `pg_foreign_table` | Foreign tables |
   | `pg_foreign_server` | Foreign servers |
   | `pg_user_mapping` | User mappings |
   | `pg_foreign_data_wrapper` | Foreign data wrappers |
   | `pg_statistic_ext` | Extended statistics objects |
   | `pg_event_trigger` | Event triggers |
   | `pg_collation` | Collations |
   | `pg_operator` | Operators |
   | `pg_opclass` | Operator classes |
   | `pg_opfamily` | Operator families |
   | `pg_cast` | Casts |
   | `pg_partitioned_table` | Partitioning metadata |
   | `pg_inherits` | Table inheritance |
   | `pg_sequence` | Sequence parameters |
   | `pg_ts_config` | Text search configurations |
   | `pg_ts_dict` | Text search dictionaries |
   | `pg_ts_parser` | Text search parsers |
   | `pg_ts_template` | Text search templates |
   | `information_schema.column_privileges` | Column-level grants |
   | `pg_roles` (or `pg_authid`) | Roles and role memberships, except `pg_*` roles and the bootstrap superuser (OID 10) |
   | `pg_tablespace` | Tablespaces |

### 20.2. Drift Detection

   `dpg verify` (Section 18.3) compares the live catalog with the
   snapshot.  Only properties the snapshot records, which are the
   properties the source declared, are compared, using the same
   equivalence rules as `dpg plan`.  A property PostgreSQL reports but
   the source never declared (an owner, a storage mode, a role
   attribute left at its default) is never drift.

   1.  Introspect the live catalog and remove ignored objects (Section
       3.9) from it and from the snapshot.
   2.  For each snapshot entry, find the live object with the same
       key.  None: the object is missing (drift).  Otherwise, compare
       each property the entry records with the live value.  A
       sub-object (column, constraint, index, trigger, policy,
       partition, ...) the snapshot doesn't record is drift on its
       object.  An unnamed constraint in the source is matched with
       the constraint PostgreSQL named for it.
   3.  [PG] A value the catalog can't show is never compared: role
       passwords, subscription `CONNECTION`, password options of a user
       mapping.  For an object PostgreSQL reconstructs from the catalog
       rather than storing as source text, its structured properties
       are compared instead of its text.
   4.  Grants are compared privilege by privilege and role by role; a
       declared `ALL` holds when the role has every privilege of the
       object kind.
   5.  A live object with no snapshot entry is unmanaged, or a warning
       for a role or tablespace.

   **Drift policies.**

   | Object | Missing live | Changed | Extra live |
   |--------|--------------|---------|------------|
   | Role, tablespace | drift | drift | warning |
   | Role membership of a declared role | drift | drift | drift |
   | Grant | drift | — | warning |
   | Privilege named in `REVOKES { }` | — | granted live: drift | — |
   | Other top-level object | drift | drift | unmanaged |
   | Sub-object (column, constraint, index, trigger, policy, partition, enum value, ...) | drift | drift | drift |

   [DPG] A virtual type (Section 5.6) exists only in the source and the
   snapshot and is never compared.

---

## 21. Per-Object Diff Algorithms

   This section specifies, for each object type, the precise field
   comparison performed by the Differ and the DDL emitted for each
   type of change.

   **TABLE:**

   | Field | Change detection | DDL | Safety |
   |-------|-----------------|-----|--------|
   | Columns (added) | New col name not in snapshot | `ALTER TABLE t ADD COLUMN c TYPE [DEFAULT ...]` | `SAFE`; `CAUTION` when it rewrites the table (Section 17.2) |
   | Columns (dropped) | Col name in snapshot, absent in desired | `ALTER TABLE t DROP COLUMN c` | `DESTRUCTIVE` |
   | Column type changed | `TypeRef.String()` differs | `ALTER TABLE t ALTER COLUMN c TYPE newtype [USING expr]` | `CAUTION` or `DESTRUCTIVE` |
   | Column NOT NULL added | `nullable` false→true (was true in snap) | `ALTER TABLE t ALTER COLUMN c SET NOT NULL` | `CAUTION` |
   | Column NOT NULL removed | `nullable` true→false | `ALTER TABLE t ALTER COLUMN c DROP NOT NULL` | `SAFE` |
   | Column DEFAULT added | `default` was nil | `ALTER TABLE t ALTER COLUMN c SET DEFAULT expr` | `SAFE` |
   | Column DEFAULT changed | `default` text differs | `ALTER TABLE t ALTER COLUMN c SET DEFAULT expr` | `SAFE` |
   | Column DEFAULT removed | `default` is nil | `ALTER TABLE t ALTER COLUMN c DROP DEFAULT` | `SAFE` |
   | Column statistics | `statistics_target` differs | `ALTER TABLE t ALTER COLUMN c SET STATISTICS n` (or `-1` to reset) | `SAFE` |
   | Column compression | `compression` differs | `ALTER TABLE t ALTER COLUMN c SET COMPRESSION m` | `SAFE` |
   | Column storage | `storage` differs | `ALTER TABLE t ALTER COLUMN c SET STORAGE s` | `SAFE` |
   | Column comment | `comment` differs | `COMMENT ON COLUMN t.c IS '...'` | `SAFE` |
   | Constraint added | Name absent in snapshot | `ALTER TABLE t ADD CONSTRAINT name ...` | `CAUTION` |
   | Constraint dropped | Name absent in desired | `ALTER TABLE t DROP CONSTRAINT name` | `DESTRUCTIVE` |
   | Constraint renamed only (`RENAMED FROM`, Section 7.3) | `renamed_from` set, body unchanged | `ALTER TABLE t RENAME CONSTRAINT old TO new` | `SAFE` |
   | Constraint deferrability-only change (FK, Section 7.3) | Only `DEFERRABLE`/`INITIALLY ...` differs | `ALTER TABLE t ALTER CONSTRAINT name ...` | `SAFE` |
   | `NOT NULL` constraint inheritability-only change (PG18, Section 7.3) | Only `NO INHERIT` differs | `ALTER TABLE t ALTER CONSTRAINT name [NO] INHERIT` | `SAFE` |
   | Constraint dropped from a partitioned parent while a direct partition declares it locally (PG18+, Section 7.3) | Name absent from parent's desired, present in a direct partition's own `{ }` body | `ALTER TABLE ONLY t DROP CONSTRAINT name` | `SAFE` |
   | Constraint changed | Body text differs | Drop + re-add | `DESTRUCTIVE` |
   | NOT VALID removed | `not_valid` false in desired | `ALTER TABLE t VALIDATE CONSTRAINT name` | `CAUTION` |
   | Generated-column expression added/changed/dropped (Section 7.4) | `GENERATED ... AS (expr)` differs | `ADD GENERATED`/`SET EXPRESSION`/`DROP EXPRESSION` (Section 7.4 table) | `SAFE`/`CAUTION` |
   | Identity clause added/changed/dropped (Section 7.4) | identity clause or sequence options differ | `ADD GENERATED AS IDENTITY`/`SET GENERATED`/`SET <option>`/`DROP IDENTITY` (Section 7.4 table) | `SAFE`/`CAUTION` |
   | Index added (existing table) | Name absent in snapshot | `CREATE [UNIQUE] INDEX [CONCURRENTLY] ...` | `CAUTION` |
   | Index dropped | Name absent in desired | `DROP INDEX [CONCURRENTLY] name` | `CAUTION` |
   | Index renamed only (`RENAMED FROM`, Section 7.7) | Name changed, nothing else differs | `ALTER INDEX old RENAME TO new` | `SAFE` |
   | Index changed | Any other field differs | Drop + recreate | `CAUTION` |
   | RLS enabled | `rls_enabled` changed | `ALTER TABLE t ENABLE ROW LEVEL SECURITY` | `SAFE` |
   | RLS disabled | `rls_enabled` changed | `ALTER TABLE t DISABLE ROW LEVEL SECURITY` | `SAFE` |
   | RLS force removed (enable still set, Section 7.8) | `rls_forced` false in desired, `rls_enabled` still true | `ALTER TABLE t NO FORCE ROW LEVEL SECURITY` | `SAFE` |
   | Policy added | Name absent in snapshot | `CREATE POLICY name ON t ...` | `SAFE` |
   | Policy `TO`/`USING`/`WITH CHECK` changed only (Section 7.8) | `FOR`/`AS` unchanged | `ALTER POLICY name ON t ...` | `SAFE` |
   | Policy `FOR`/`AS` changed | Either field differs | Drop + recreate | `CAUTION` |
   | Policy dropped | Name absent in desired | `DROP POLICY name ON t` | `SAFE` if permissive, `CAUTION` if restrictive |
   | Trigger added | Name absent in snapshot | `CREATE TRIGGER name ...` | `SAFE` |
   | Trigger changed | Any field differs | Drop + recreate | `CAUTION` |
   | Trigger enable state changed only (Section 7.9) | `trigger-enable-state` differs, rest unchanged | `ALTER TABLE t ENABLE/DISABLE TRIGGER name` (or `ENABLE REPLICA/ALWAYS TRIGGER`) | `SAFE` |
   | Trigger dropped | Name absent in desired | `DROP TRIGGER name ON t` | `CAUTION` |
   | Grant added | Not in snapshot grant list | `GRANT privs ON TABLE t TO role` | `SAFE` |
   | Owner changed | `owner` differs | `ALTER TABLE t OWNER TO role` | `SAFE` |
   | Comment changed | `comment` differs | `COMMENT ON TABLE t IS '...'` | `SAFE` |
   | Table renamed | `renamed_from` set | `ALTER TABLE old RENAME TO new` | `CAUTION` |
   | Table moved to another schema (`RENAMED FROM` schema-qualified, Section 7.6) | Schema component of `renamed_from` differs | `ALTER TABLE old_schema.name SET SCHEMA new_schema` | `SAFE` |
   | Tablespace changed | `tablespace` (Section 7.1's `TABLESPACE` clause) differs | `ALTER TABLE t SET TABLESPACE ts` | `CAUTION` |
   | Access method changed | `USING` method (Section 7.1) differs | `ALTER TABLE t SET ACCESS METHOD method` | `CAUTION` |
   | Storage parameters changed | `WITH (...)` (Section 7.1) params differ | `ALTER TABLE t SET (...)` / `ALTER TABLE t RESET (...)` | `SAFE` |
   | Parent added/removed | `INHERITS (...)` (Section 7.1) list differs | `ALTER TABLE child INHERIT parent` / `ALTER TABLE child NO INHERIT parent` | `SAFE` |
   | Typed-table association added/switched/removed | `OF type_name` (Section 7.1) differs | `ALTER TABLE t OF type_name` / `ALTER TABLE t NOT OF` | `CAUTION` |
   | `REPLICA IDENTITY` changed | `replica-identity-dir` (Section 7.11) differs | `ALTER TABLE t REPLICA IDENTITY ...` | `SAFE` |
   | `CLUSTER ON` changed/removed | `cluster-dir` (Section 7.11) differs | `ALTER TABLE t CLUSTER ON index` / `ALTER TABLE t SET WITHOUT CLUSTER` | `SAFE` |
   | `LOGGED`/`UNLOGGED` toggled (Section 7.12) | `UNLOGGED` prefix differs | `ALTER TABLE t SET LOGGED` / `ALTER TABLE t SET UNLOGGED` | `CAUTION` |
   | Partition attached/detached (Section 7.13) | `ATTACHED FROM`/`DETACHED FROM` present | `ALTER TABLE parent ATTACH/DETACH PARTITION ...` | `CAUTION` |
   | Table dropped | Absent in desired, not PROTECTED | `DROP TABLE t [CASCADE]` | `DESTRUCTIVE` |

   **FUNCTION / PROCEDURE:**

   | Field | Change | DDL | Safety |
   |-------|--------|-----|--------|
   | New function | Absent in snapshot | `CREATE OR REPLACE FUNCTION ...` | `SAFE` |
   | Body hash changed | `body_hash` differs | `CREATE OR REPLACE FUNCTION ...` | `SAFE` |
   | Attribute changed (volatility, strict, security, leakproof, parallel, cost, rows, set options) | Field differs | `CREATE OR REPLACE FUNCTION ...` | `SAFE` |
   | Argument list or return type changed | Type key differs | `DROP FUNCTION CASCADE; CREATE FUNCTION` | `DESTRUCTIVE` |
   | `[NO] DEPENDS ON EXTENSION` changed (Function/Procedure, Sections 9.1/9.2) | Extension-dependency set differs | `ALTER FUNCTION ... [NO] DEPENDS ON EXTENSION ext` | `SAFE` |
   | Grant added | Not in snapshot | `GRANT EXECUTE ON FUNCTION ...` | `SAFE` |
   | Revocation added | Not in snapshot | `REVOKE EXECUTE ON FUNCTION ... FROM role` | `SAFE` |
   | Owner changed | `owner` differs | `ALTER FUNCTION ... OWNER TO role` | `SAFE` |
   | Comment changed | `comment` differs | `COMMENT ON FUNCTION ...` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `renamed_from` set | `ALTER FUNCTION ... RENAME TO new` | `CAUTION` |
   | Moved to another schema (`RENAMED FROM` schema-qualified) | Schema component differs | `ALTER FUNCTION ... SET SCHEMA new_schema` | `SAFE` |
   | Function dropped | Absent in desired | `DROP FUNCTION name(...) [CASCADE]` | `DESTRUCTIVE` |

   Aggregate reuses this same table for its three metadata-only
   operations (rename/owner/schema-move, Section 9.4); every other Aggregate
   field change is `DESTRUCTIVE` per Section 9.4's own table, unlike Function/
   Procedure's mostly-`SAFE` attribute set above.

   **VIEW:**

   | Change | DDL | Safety |
   |--------|-----|--------|
   | New view | `CREATE VIEW ...` | `SAFE` |
   | Query changed, same column list | `CREATE OR REPLACE VIEW ...` | `SAFE` |
   | Column list changed | `DROP VIEW CASCADE; CREATE VIEW` | `DESTRUCTIVE` |
   | View/Matview renamed (`RENAMED FROM`, Sections 8.1/8.2) | `ALTER [MATERIALIZED] VIEW old RENAME TO new` | `CAUTION` |
   | View/Matview moved to another schema (`RENAMED FROM` schema-qualified) | `ALTER [MATERIALIZED] VIEW old_schema.name SET SCHEMA new_schema` | `SAFE` |
   | View dropped | `DROP VIEW CASCADE` | `DESTRUCTIVE` |

   **ENUM:**

   | Change | DDL | Safety |
   |--------|-----|--------|
   | New value | `ALTER TYPE name ADD VALUE 'v' [BEFORE/AFTER 'existing']` | `SAFE` |
   | Value renamed (`RENAMED FROM`, Section 5.1.1) | `ALTER TYPE name RENAME VALUE 'old' TO 'new'` | `SAFE` |
   | Value removed (guarded) | MIGRATE REMOVE procedure (Section 5.1.2) | `DESTRUCTIVE` |
   | Value removed (unguarded) | Error DPG-E014 (or with `--allow-destructive`) | `DESTRUCTIVE` |
   | Comment changed | `COMMENT ON TYPE name IS '...'` | `SAFE` |
   | Owner changed | `ALTER TYPE name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM` on the type) | `ALTER TYPE old RENAME TO new` | `CAUTION` |
   | Moved to another schema (`RENAMED FROM` schema-qualified) | `ALTER TYPE old_schema.name SET SCHEMA new_schema` | `SAFE` |

   Composite, Range, Domain, and Base types follow the same
   Owner/Renamed/Moved-to-another-schema rows as TYPE above AS ENUM (Section 5.1's
   cross-reference) — not repeated per kind here; each kind's own
   section (Sections 5.2-5.5) documents any additional kind-specific rows
   (Composite attribute rename, Domain constraint `NOT VALID`/rename,
   etc.).

   **SEQUENCE:**

   | Change | DDL | Safety |
   |--------|-----|--------|
   | New sequence | `CREATE SEQUENCE ...` | `SAFE` |
   | Numeric parameters changed | `ALTER SEQUENCE name [INCREMENT BY n] [MINVALUE n] ...` | `SAFE` |
   | `OWNED BY` changed (incl. to/from `NONE`) | `ALTER SEQUENCE name OWNED BY {table.col\|NONE}` | `SAFE` |
   | `RESTART [WITH n]` present in source (Section 10) | `ALTER SEQUENCE name RESTART [WITH n]` (re-emitted every apply while present) | `CAUTION` |
   | `UNLOGGED` toggled (Section 10) | `ALTER SEQUENCE name SET LOGGED/UNLOGGED` | `CAUTION` |
   | `AS type` changed | Drop + recreate | `DESTRUCTIVE` |
   | Sequence dropped | `DROP SEQUENCE name` | `DESTRUCTIVE` |

   **ROLE:**

   | Change | DDL | Safety |
   |--------|-----|--------|
   | New role | `CREATE ROLE name WITH ...` | `SAFE` |
   | Any option changed | `ALTER ROLE name WITH [options]` | `SAFE` |
   | Membership added/changed (`WITH ADMIN`/`INHERIT`/`SET`, Section 11.1) | `GRANT role TO member [WITH ...]` | `SAFE` |
   | Membership removed | `REVOKE role FROM member` | `CAUTION` |
   | `SET`/`RESET` config param changed (Section 11.1) | `ALTER ROLE name [IN DATABASE db] SET/RESET ...` | `SAFE` |
   | Renamed (`RENAMED FROM`, Section 11.1) | `ALTER ROLE old RENAME TO new` | `CAUTION` |
   | Role dropped | `DROP ROLE name` | `DESTRUCTIVE` |

   **EVENT TRIGGER:**

   | Change | DDL | Safety |
   |--------|-----|--------|
   | New event trigger | `CREATE EVENT TRIGGER ...` | `SAFE` |
   | Event/tags/function changed | `DROP EVENT TRIGGER` + `CREATE EVENT TRIGGER` | `SAFE` |
   | Enable state changed only (Section 14.1) | `ALTER EVENT TRIGGER name ENABLE/DISABLE [REPLICA/ALWAYS]` | `SAFE` |
   | Owner changed | `ALTER EVENT TRIGGER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER EVENT TRIGGER old RENAME TO new` | `CAUTION` |
   | Event trigger dropped | `DROP EVENT TRIGGER name` | `SAFE` |

---

## 22. Dependency Ordering

### 22.1. Topological Sort

   The dependency resolver builds a directed acyclic graph (DAG) where
   nodes are `IRObject` values and a directed edge from A to B means
   "A depends on B" (B must be created before A).

   **Edge sources:**

   1.  A table column whose type is a user-defined type or domain
       creates an edge from the table to the type/domain.

   2.  A `REFERENCES` FK constraint creates an edge from the source
       table to the target table.

   3.  A view's query that references table or view B creates an edge
       from the view to B.  This is real static analysis of the parsed
       query — every table/view reference reachable via `FROM`, `JOIN`,
       a CTE, or a subquery — not a blanket "depends on every table in
       the object set" approximation; a view that never mentions B
       gets no edge to B.

   4.  A function's `search_path` or `SECURITY DEFINER` context
       creates an edge from the function to the schema.

   5.  An index that uses a custom operator class creates an edge from
       the index (and transitively its table) to the operator class.

   6.  A trigger function reference creates an edge from the table to
       the trigger function.

   7.  A domain whose base type is a user-defined type creates an edge
       from the domain to the type.

   8.  A partition creates an edge from the partition to its parent
       partitioned table.

   9.  A `LANGUAGE sql` function/procedure body that statically
       references table or view B (a `FROM`/`JOIN` clause, an
       `INSERT`/`UPDATE`/`DELETE` target, a CTE, or a subquery)
       creates an edge from the function/procedure to B.  Dynamic SQL
       (an `EXECUTE` whose argument is built at runtime rather than a
       literal query text) is invisible to this analysis — a known,
       accepted limitation matching real PostgreSQL's own inability to
       validate dynamic SQL either.  A `LANGUAGE plpgsql`
       function/procedure body is deliberately **not** analysed for
       table references, even though it is analysed for embedded SQL
       when computing its body hash (Section 16.3) — see the note below this
       list for why.  A function/procedure body in any other language
       is not analysed for table references.

       **Why `plpgsql` is exempt:** PostgreSQL compiles a `plpgsql`
       body lazily — the embedded SQL statements are not resolved
       against the catalog until the function is first called, so a
       `plpgsql` function can be created referencing a table (or
       another function) that does not exist yet.  Adding a
       function→table edge for `plpgsql` bodies is therefore never
       required for a successful `apply`, and doing so anyway can
       manufacture an unresolvable cycle for an entirely ordinary
       pattern: a validation or audit trigger function whose body
       queries the very table the trigger is attached to.  That shape
       combines with edge source 6 (table→trigger-function) into a
       2-node cycle with no `FOREIGN KEY` anywhere in it, which Section 22.2's
       cycle-breaker cannot resolve (step 2a's `DEFERRABLE` check has
       nothing to examine, since the cycle contains no FK edge at
       all).  Exempting `plpgsql` here mirrors the reasoning already
       used for function-calls-function edges: a `LANGUAGE sql` body
       calling another function creates an edge (`sql` is validated
       eagerly), a `plpgsql` body calling another function does not.

   The topological sort MUST use Kahn's algorithm or an equivalent
   O(V + E) algorithm.  The sort is deterministic: among nodes with no
   remaining incoming edges, the one with the lexicographically smallest
   `QualifiedName()` is selected first.

### 22.2. Circular Dependency Resolution

   When the dependency graph contains a cycle, the resolver applies the
   following procedure:

   1.  Identify all strongly connected components (SCCs) using
       Tarjan's algorithm.

   2.  For each SCC with more than one node:

       a.  Verify that every FK edge within the SCC is `DEFERRABLE`.
           If any non-deferrable FK is found, emit error DPG-E017
           listing all nodes in the cycle.

       b.  Remove the circular FK edges from the graph (these will be
           emitted as `ALTER TABLE ADD CONSTRAINT ... DEFERRABLE`
           after all tables in the SCC are created).

       c.  Re-run the topological sort on the cycle-free graph.

   3.  After the topological sort, append `ALTER TABLE ADD CONSTRAINT`
       ops for all deferred circular FKs.

---

## 23. Out of Scope

   The following are outside DPG's scope.  This list is closed (Tenet
   1, Section 1.4): any PostgreSQL schema feature not listed here is in
   scope.

   **Rule (REWRITE) objects:**
   PostgreSQL `CREATE RULE` is a legacy feature superseded by triggers
   and updatable views.  DPG explicitly does not manage rules.

   **`IMPORT FOREIGN SCHEMA`:**
   Runtime discovery operation; not appropriate for declarative schema
   management.

   **`REFRESH MATERIALIZED VIEW`:**
   Runtime DML operation; out of scope.

   **Temporary tables:**
   Session-scoped; cannot be meaningfully managed by a schema tool.

   **Database management (`CREATE DATABASE` / `ALTER DATABASE` /
   `DROP DATABASE`):**
   Permanently out of scope, not merely deferred. DPG's project model
   treats a database directory (Section 3.4) as proof the database already
   exists — the directory's presence is what makes a database
   discoverable and in scope for diffing, not a declaration that DPG
   should create it. Making DPG create-and-manage databases as objects
   would require redefining what that directory means, or inventing a
   second declaration surface alongside it; either way, database
   creation/deletion is a cluster-provisioning operation, a tier below
   the schema-management scope this specification defines, and is not
   planned for any future version.

   **Large Objects (`lo_create`/`lo_import`/`lo_export` etc.):**
   Permanently out of scope, not merely deferred. A large object has no
   `CREATE`/`ALTER`/`DROP` DDL statement at all — it's created,
   imported, and exported entirely through server-side functions
   (`lo_create`, `lo_import`, `lo_export`, `lo_unlink`), the same
   runtime/DML tier as the row data those functions typically move.
   There is no declarative surface for DPG's no-verb object model
   (Section 4) to attach to. Not planned for any future version.

   **`REASSIGN OWNED BY` / `DROP OWNED BY`:**
   Out of scope. Both are one-shot cluster-maintenance commands, closer
   in spirit to `VACUUM` than to a declared object — there is no
   steady-state "desired" form of either to diff against, which is
   exactly the same reason `ALTER` is structurally excluded from DPG's
   no-verb object model (Section 4). Not planned for any future version.

   **`CLUSTER` (the maintenance command, not `CLUSTER ON`):**
   Out of scope. Like `REASSIGN OWNED BY`/`DROP OWNED BY` above, `CLUSTER`
   is a one-shot table-rewrite maintenance command with no steady-state
   "desired" form to diff against — running it twice in a row is not
   idempotent in the way a declared object's DDL must be (Section
   17.4). DPG's `CLUSTER ON name` directive (Section 7.11) declares
   which index a *future, manually-run* `CLUSTER` would use; it does
   not itself invoke the command. PostgreSQL 17's `CLUSTER (VERBOSE,
   ...)` parenthesized-options syntax is a variant of the same command
   and is out of scope for the same reason. Not planned for any future
   version.

   **`ALTER SYSTEM`:**
   Out of scope. It writes a GUC override to `postgresql.auto.conf`, a
   cluster-wide configuration file outside any database's catalog —
   nothing DPG introspects, diffs, or otherwise treats as schema state.
   Even where a value it sets does persist (unlike `REASSIGN OWNED
   BY`/`DROP OWNED BY` above), the object it changes is server
   configuration, not a schema object; many of the settings it touches
   also require a server reload or restart to take effect, a cluster-
   provisioning concern a tier below this specification's scope — the
   same distinction that puts `CREATE`/`ALTER`/`DROP DATABASE` out of
   scope above. Not planned for any future version.

   **`CREATE ACCESS METHOD`:**
   Out of scope. The motivating real-world case — installing a custom
   index access method such as `pgvector`'s `hnsw`/`ivfflat` or
   `bloom` — never actually requires a bare `CREATE ACCESS METHOD`
   statement in practice: the access method is registered as a side
   effect of `CREATE EXTENSION`, which DPG already manages, and is
   then only ever *referenced* by name (`USING hnsw`) on an index or
   operator class, both already supported. A bare `CREATE ACCESS
   METHOD` only serves someone implementing a new access method in C
   — extension authors, not DPG's application-schema audience. Not
   planned for any future version unless a concrete use case outside
   the extension-install path emerges.

   **`CREATE CONVERSION`:**
   Out of scope. Character-set conversions were relevant when a
   cluster mixed encodings; the near-universal use of UTF-8 end to end
   today leaves no realistic scenario where an application schema
   hand-declares one. Not planned for any future version.

   **`CREATE [PROCEDURAL] LANGUAGE`:**
   Out of scope. Every commonly used procedural language
   (`plpython3u`, `plperl`, `plv8`, etc.) installs via `CREATE
   EXTENSION`, which DPG already manages, exactly as PostgreSQL's own
   documentation recommends; a bare `CREATE LANGUAGE` statement is
   largely a pre-9.1 pattern predating the extension mechanism. Not
   planned for any future version.

   **`CREATE TRANSFORM`:**
   Out of scope. Type transforms (e.g. for PL/Python) are always
   bundled with a specific extension pairing (`hstore_plpython`,
   `ltree_plpython`) and are never hand-declared independently of
   installing that extension, which DPG already manages. Not planned
   for any future version.

   **`ALTER EXTENSION ADD`/`DROP member_object`:**
   Out of scope. This reassigns which specific catalog objects (a
   function, a type, an operator, etc.) an extension's own internal
   membership list claims — normally only relevant when packaging a new
   extension version or working around a broken/partial installation,
   both extension-authoring concerns rather than something an
   application schema project hand-manages. Section 6.2 already manages
   installing, updating, and removing an extension as a whole; picking
   apart its internal member list is a tier below that scope, the same
   distinction that excludes `CREATE ACCESS METHOD`/`CREATE LANGUAGE`/
   `CREATE TRANSFORM` above. Not planned for any future version unless
   a concrete use case outside extension-authoring emerges.

---

## 24. Security Considerations

   **Secret handling:** DPG MUST NOT store plaintext secret values in
   any persisted file.  This includes:

   -   Connection strings in `dpg.toml`: if `link = "env:VAR"` is used,
       the resolved value is never written to disk (Appendix D.1).  If `url =` is
       used, the connection string may contain embedded credentials and
       SHOULD NOT be committed to a public repository; this is the operator's
       responsibility.
   -   Subscription `CONNECTION` strings (Section 13.2): MAY hold one or more
       `{{secret-uri}}` placeholders embedded in an otherwise-literal
       conninfo (or the whole value), resolved (Appendix D.1) immediately before `CREATE SUBSCRIPTION` executes — never
       during `plan`/`diff`, never written to the snapshot, an archived
       migration file, or any error message. A `CONNECTION` value with no
       `{{...}}` is literal text, the operator's responsibility as with
       `url =` above.
   -   Role `PASSWORD` (Section 11.1): same `{{secret-uri}}` mechanism as
       Subscription `CONNECTION` above. The snapshot stores a hash of the
       declared text (never the resolved value), so a changed reference
       is detected.
   -   User Mapping `OPTIONS` (Section 14.10): same `{{secret-uri}}` mechanism,
       applied to the entire opaque statement text rather than one
       isolated field — `OPTIONS` keys are foreign-data-wrapper-specific,
       not fixed by DPG, so there's no single clause to target the way
       `CONNECTION`/`PASSWORD` are. The snapshot stores a hash of the
       whole declared body (never the resolved value), same as every
       other opaque-tier kind's existing diffing.

   FDW-level `OPTIONS` (as opposed to User Mapping's) are not covered —
   FDW connection details are typically non-sensitive (host, port,
   dbname), with the credential living in the User Mapping.

   **Introspection-side redaction:** `dump`, `verify`, and `plan
   --live` read the live catalog, so they too MUST NOT write resolved
   secrets to files.  A subscription's connection string is never read
   from the catalog (Section 13.2).  A user mapping's `OPTIONS` must be
   read to reconstruct it, so `dump` replaces the value of every
   password-like key (`password`, `passwd`, `pwd`, `secret`,
   `passphrase`, matched case-insensitively as a substring) with a fixed
   placeholder.  The placeholder contains no `{{...}}`, so the
   `hardcoded-fdw-password` rule (Section 19.1) rejects the dumped file
   until the operator replaces it with a real `{{secret-uri}}`
   reference.  PostgreSQL stores only resolved credentials, so `dump`
   cannot recover the original reference.

   **SQL injection in generated DDL:** All identifier names read from
   source files are validated against PostgreSQL's identifier rules
   before being interpolated into generated SQL.  The compiler MUST
   quote all identifiers using PostgreSQL's double-quote quoting
   (`"identifier"`) in generated DDL to prevent injection via crafted
   identifier names.

   **SECURITY DEFINER functions:** The linter warns on `SECURITY
   DEFINER` functions lacking explicit `SET search_path` to mitigate
   search path injection attacks (rule: `security-definer-search-path`).

   **Snapshot integrity:** The snapshot is a plain JSON file.  It MUST
   be committed to version control to prevent tampering.  An attacker
   with write access to the snapshot could cause the differ to omit
   real changes or generate incorrect migrations.  Snapshot integrity
   SHOULD be enforced via the same commit signing and branch protection
   mechanisms applied to source code.

   **Privilege escalation via DEFAULT PRIVILEGES:** `ALTER DEFAULT
   PRIVILEGES` grants can confer privileges on future objects.  Operators
   SHOULD review all `DEFAULT PRIVILEGES` declarations carefully before
   applying.

   **`DROP ... CASCADE`:** The `DROP CASCADE` directive and the
   `default_drop_behavior = "cascade"` setting cause the compiler to
   emit `DROP ... CASCADE`, which silently drops all dependent objects.
   Operators MUST review `DESTRUCTIVE` ops carefully.  DPG's safety
   classification system exists precisely to make this review tractable.

   **Row Level Security on partitioned tables:** Enabling RLS and
   declaring policies on a partitioned parent protects only queries
   against the parent — it does not protect direct queries against a
   leaf partition, which PostgreSQL evaluates independently and does
   not inherit the parent's RLS state for (Section 7.8).  Operators
   who need to protect direct partition access MUST also declare RLS
   and equivalent policies on each partition individually.

---

## 25. Feature Coverage Matrix

   **Legend:**
   - **Origin** — `PG`: a PostgreSQL feature, written as PostgreSQL writes it (Part 1, or a Part 2 directive replacing a PostgreSQL statement); `DPG`: DPG's own, with no PostgreSQL equivalent.
   - **Declared** — DPG syntax fully specified in this document.
   - **Diffed** — The compiler computes structured per-field changes and emits precise DDL.
   - **Passthrough** — Treated as opaque text; diffed by text equality only.
   - **No SQL** — DPG-native; generates no PostgreSQL DDL.
   - **Out of scope** — Not managed by DPG.
   - **Deferred** — Explicitly out of scope for this version; planned.

   | Feature | Origin | Status | Notes |
   |---------|--------|--------|-------|
   | Tables (regular) | PG | Declared, Diffed | Full per-field diff, incl. `LIKE`, typed (`OF type_name`), `USING` access method, `REPLICA IDENTITY`, `CLUSTER ON`, `ATTACH`/`DETACH PARTITION`, cheap constraint rename |
   | Tables (unlogged) | PG | Declared, Diffed | `UNLOGGED` prefix; toggling post-creation uses `SET LOGGED`/`SET UNLOGGED`, not drop+recreate |
   | Tables (temporary) | PG | Out of scope | Session-scoped |
   | Columns — all built-in types | PG | Declared, Diffed | In `()` list |
   | Columns — generated (`ALWAYS AS ... STORED`/`VIRTUAL`) | PG | Declared, Diffed | In `()` list; `VIRTUAL` is PG18+ (Section 7.2) |
   | Columns — identity (`AS IDENTITY`) | PG | Declared, Diffed | In `()` list |
   | Column `COMPRESSION` | PG | Declared, Diffed | `COLUMN c { COMPRESSION m; }` |
   | Column `STORAGE` | PG | Declared, Diffed | `COLUMN c { STORAGE s; }` |
   | Column statistics targets | PG | Declared, Diffed | `COLUMN c { STATISTICS n \| DEFAULT; }` — `DEFAULT` is emitted as `-1` |
   | Column comments | PG | Declared, Diffed | `COLUMN c { COMMENT '...'; }` |
   | Column `DEPRECATED` | DPG | Declared, Diffed | `COLUMN c { DEPRECATED '...'; }` |
   | Column `USING` (type change) | DPG | Declared, Diffed | `COLUMN c { USING expr; }` |
   | Column renames | DPG | Declared, Diffed | `COLUMN new { RENAMED FROM old; }` |
   | Column-level grants | PG | Declared, Diffed | `COLUMN c { GRANTS { ... } }` |
   | Column-level revocations | PG | Declared, Diffed | `COLUMN c { REVOKES { ... } }` |
   | Inline constraints (PK, UNIQUE, CHECK, FK, `NOT NULL`) | PG | Declared, Diffed | Single-column emitted inline; table-level named `NOT NULL` is PG18+ (Section 7.3) |
   | Named constraints in `()` list | PG | Declared, Diffed | Emitted inline for single-column |
   | Constraints in `{}` block | PG | Declared, Diffed | Constraints added to an existing table (Section 7.3) |
   | `EXCLUSION` constraints | PG | Declared, Diffed | In `()` or `{}` block; on partitioned tables since PG17 (Section 7.3, passthrough) |
   | `NOT VALID` / `VALIDATE CONSTRAINT` | PG | Declared, Diffed | Multi-migration lifecycle; extended to `NOT NULL` constraints in PG18 (Section 7.3) |
   | `ENFORCED`/`NOT ENFORCED` (`CHECK`/`FOREIGN KEY`) | PG | Declared, Diffed | PG18+ (Sections 7.2/7.3) |
   | Temporal keys (`WITHOUT OVERLAPS`/`PERIOD`) | PG | Declared, Diffed | PG18+; references an existing range/multirange column, catalogued as an ordinary `PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY`, not `EXCLUDE` (Section 7.3) |
   | FK `ON DELETE SET NULL`/`SET DEFAULT (col-list)` | PG | Declared, Diffed | PG15+, column-scoped variant of the existing bare form (Section 7.2) |
   | Indexes — all access methods | PG | Declared, Diffed | btree, hash, gin, gist, brin, spgist, bloom |
   | Indexes — partial | PG | Declared, Diffed | `WHERE` predicate as text |
   | Indexes — expression | PG | Declared, Diffed | Expression as text |
   | Indexes — covering (`INCLUDE`) | PG | Declared, Diffed | Drop + recreate on change |
   | Indexes — concurrent creation | PG | Declared, non-transactional | `CREATE INDEX CONCURRENTLY` |
   | Indexes — `ON ONLY`, opclass parameters, `NULLS [NOT] DISTINCT`, rename | PG | Declared, Diffed | `ONLY` suppresses partition recursion; rename is metadata-only (Section 7.7) |
   | ENUM types | PG | Declared, Diffed | `MIGRATE REMOVE` for value removal; `BEFORE`/`AFTER` positional `ADD VALUE`, `RENAME VALUE`, `OWNER`, `RENAMED FROM`/`SET SCHEMA` |
   | Composite types | PG | Declared, Diffed | Attribute `COLLATE`; `OWNER`, `RENAMED FROM`/`SET SCHEMA` |
   | Range types | PG | Declared, Diffed | Any option change = DESTRUCTIVE; `OWNER`, `RENAMED FROM`/`SET SCHEMA`; multirange companion auto-created, never separately declared |
   | Domain types | PG | Declared, Diffed | `COLLATE`; `NOT VALID`/`VALIDATE CONSTRAINT`/`RENAME CONSTRAINT` lifecycle; `OWNER`, `RENAMED FROM`/`SET SCHEMA` |
   | Base (shell) types | PG | Declared, Passthrough | Bare forward-declaration shell + automatic cycle-breaking for self-referential support functions (Section 5.5); `OWNER`, `RENAMED FROM`/`SET SCHEMA`; support-function/storage property changes use non-destructive `ALTER TYPE ... SET (...)` where PostgreSQL allows it |
   | Virtual types | DPG | Declared, No SQL | DPG-native; snapshot only |
   | Views | PG | Declared, Diffed | Column list change = DESTRUCTIVE; `RENAMED FROM` and cross-schema `SET SCHEMA` supported (Section 7.6, Section 8.1) |
   | Materialized views | PG | Declared, Diffed | Query change = DESTRUCTIVE; `RENAMED FROM`/`SET SCHEMA` supported (Section 8.2) |
   | Recursive views | PG | Declared, Diffed | |
   | Functions — all languages | PG | Declared, Passthrough body | Body hash-diffed; `LEAKPROOF`, `TRANSFORM FOR TYPE`, C/`internal` `AS` forms, PG14+ `BEGIN ATOMIC`, `[NO] DEPENDS ON EXTENSION`, `OWNER`, `REVOKES`, `RENAMED FROM`/`SET SCHEMA` |
   | Procedures | PG | Declared, Passthrough body | Same additions as Functions above (`[NO] DEPENDS ON EXTENSION`, `OWNER`, `REVOKES`, `RENAMED FROM`/`SET SCHEMA`) |
   | Aggregates | PG | Declared, Diffed | Every option (Section 9.4) diffed; any option change = DESTRUCTIVE; `RENAME TO`/`OWNER TO`/`SET SCHEMA` are the only non-destructive ALTER operations, as PostgreSQL's `ALTER AGGREGATE` allows |
   | Window functions | PG | Declared, Passthrough body | |
   | Row Level Security | PG | Declared, Diffed | `TO`/`USING`/`WITH CHECK`-only policy changes use non-destructive `ALTER POLICY`, avoiding a zero-active-policy window; policy `RENAMED FROM` also supported (Section 7.8) |
   | Triggers | PG | Declared, Diffed | Constraint triggers (`CONSTRAINT`, `FROM`, `DEFERRABLE`), enable state, `DEPENDS ON EXTENSION`; `RENAMED FROM` via `ALTER TRIGGER` (Section 7.9) |
   | Event triggers | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. Enable-state (`DISABLED`/`ENABLE REPLICA`/`ENABLE ALWAYS`), `OWNER`, `RENAMED FROM` diffed structurally, not part of the hash. `login` event (PG17+) also supported (Section 14.1) |
   | Sequences | PG | Declared, Diffed | `UNLOGGED` (PG15+), `OWNED BY NONE`, `REVOKES`, `RESTART [WITH n]`, `RENAMED FROM`/`SET SCHEMA` (Section 10) |
   | Schemas | PG | Declared, Diffed | |
   | Extensions | PG | Declared, Diffed | `SCHEMA` change uses non-destructive `ALTER EXTENSION ... SET SCHEMA` for relocatable extensions (Section 6.2) |
   | Roles | PG | Declared, Diffed | Cluster-level; `PASSWORD` (Section 11.1) never live-introspected (superuser-only in PG), diffed offline via a hash of the declared text. `RENAMED FROM`/`RENAME TO` and `SET`/`RESET` session-config-parameter (Section 11.1); membership `WITH ADMIN`/`INHERIT`/`SET` options (PG16+) also supported |
   | Table-level grants | PG | Declared, Diffed | Additive model; `MAINTAIN` privilege (PG17) and `GRANTED BY role` (Section 7.10) also supported |
   | Parameter Privileges (Section 11.6) | PG | Declared, Diffed | Cluster-level; `GRANT {SET\|ALTER SYSTEM} ON PARAMETER`, additive model like Table-level grants |
   | Column-level grants | PG | Declared, Diffed | Additive model |
   | Explicit revocations | PG | Declared, Diffed | |
   | Default Privileges | PG | Declared, Diffed | |
   | Security Labels (Section 14.11) | PG | Declared, Diffed | Keyed by provider; every kind PostgreSQL's own `SECURITY LABEL` grammar supports and DPG models |
   | Tablespaces | PG | Declared, Passthrough | Cluster-level; reconstructed from catalog, hash-diffed. `CREATE`-time `WITH (...)` storage params, `OWNER TO`, `RENAME TO`, and non-destructive `SET`/`RESET` on `WITH (...)` options are all supported (Section 14.7) |
   | Foreign Data Wrappers | PG | Declared, Passthrough | Cluster-level; reconstructed from catalog, hash-diffed |
   | Foreign Servers | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`, `RENAMED FROM`, bare `VERSION`-only change also supported (Section 14.9) |
   | User Mappings | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OPTIONS` may hold a `{{secret-uri}}` reference (Section 14.10, Appendix D.1), resolved only immediately before `CREATE USER MAPPING` executes |
   | Foreign Tables | PG | Declared, Diffed | `SERVER`/`OPTIONS` after `)`; Section 7.12 now documents the ALTER-semantics table directly (`OPTIONS` change is `SAFE` in place, `SERVER` change is `DESTRUCTIVE` drop+recreate, column add/drop follows regular `TABLE` rules) |
   | Partitioned Tables | PG | Declared, Diffed | Partition `RENAMED FROM` supported, same mechanism as a plain table rename (Section 7.13) |
   | Sub-partitioning | PG | Declared, Diffed | |
   | Publications | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`, `RENAMED FROM` also supported (Section 13.1) |
   | Subscriptions | PG | Declared, Passthrough | Reconstructed from the catalog; hash-diffed. `CONNECTION` alone is never introspected (`subconninfo` has no PUBLIC grant, and even a privileged read can't recover the original `{{secret-uri}}`) — reconstructed as a fixed placeholder instead, excluded from the drift comparison like every other reconstructed body (Section 13.2). `CONNECTION` may hold a `{{secret-uri}}` reference in source (Section 13.2, Appendix D.1), resolved only immediately before `CREATE SUBSCRIPTION` executes |
   | Collations | PG | Declared, Passthrough | Reconstructed from catalog, hash-diffed; property changes (`LOCALE`/`PROVIDER`/`RULES`/`FROM` target) = DESTRUCTIVE. `FROM existing_collation` and `RULES` (PG16+) forms, `OWNER`/`RENAMED FROM`/`SET SCHEMA`, and `REFRESH VERSION` (PG15+) also supported (Section 14.2) |
   | Operators | PG | Declared, Passthrough | Reconstructed from catalog, hash-diffed; any change = DESTRUCTIVE |
   | Operator Classes | PG | Declared, Passthrough | Reconstructed from catalog, hash-diffed; any `AS` member-list change = DESTRUCTIVE (PostgreSQL has no incremental `ALTER OPERATOR CLASS`) |
   | Operator Families | PG | Declared, Passthrough + Diffed | Header (name/access method) reconstructed from catalog, hash-diffed; loose members (Section 14.4, `ALTER OPERATOR FAMILY ... ADD`) are structured and diffed incrementally per member, live-path included — not gated on `Reconstructed` the way the bare header hash is |
   | Casts | PG | Declared, Passthrough | Reconstructed from catalog, hash-diffed; any change = DESTRUCTIVE |
   | Extended Statistics Objects | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `STATISTICS n` target and `RENAMED FROM`/`SET SCHEMA` diffed structurally (Section 14.6) |
   | Text Search Configurations | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`, `RENAMED FROM`/`SET SCHEMA`, `ALTER MAPPING REPLACE` (bulk and per-token-type) also supported (Section 12.1) |
   | Text Search Dictionaries | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`/`COMMENT`/`RENAMED FROM` (Section 12.2) |
   | Text Search Parsers | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `COMMENT`/`RENAMED FROM`, no `OWNER` (PostgreSQL has none, Section 12.3) |
   | Text Search Templates | PG | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `COMMENT`/`RENAMED FROM`, no `OWNER` (Section 12.4) |
   | Object renames and schema moves (`RENAMED FROM`) | DPG | Declared, Diffed | Every kind with a name; `ALTER ... RENAME TO` / `SET SCHEMA` (Section 7.6) |
   | `DEPRECATED` | DPG | Declared, Diffed | Comment prefix and lint warning (Sections 4.2.1, 19) |
   | `PROTECTED` | DPG | Declared | Tables; refuses the drop (DPG-E022) |
   | `DROP CASCADE` | DPG | Declared | Tables; per-object drop behaviour |
   | ENUM value removal (`MIGRATE REMOVE`) | DPG | Declared, Diffed | Type rebuild with a data migration (Section 5.1.2) |
   | Partition attach/detach (`ATTACHED FROM`/`DETACHED FROM`) | DPG | Declared, Diffed | Section 7.13 |
   | Secret placeholders (`{{secret-uri}}`) | DPG | Declared | Role `PASSWORD`, Subscription `CONNECTION`, User Mapping `OPTIONS` (Appendix D.1) |
   | Name Maps | DPG | Declared, No SQL | Naming rules for downstream tools (Appendix D.2) |
   | Macro preprocessor | DPG | Declared, No SQL | Compile-time text expansion |
   | Cross-file macro sharing | DPG | Declared, No SQL | Macros defined in any file in the compilation scope are available to all others |
   | Ignored objects (`[ignore]`) | DPG | Configuration | Objects a database has that DPG does not manage; excluded from every comparison and from `dpg dump` (Section 3.9) |
   | Rules (REWRITE) | PG | Out of scope | Legacy |
   | `IMPORT FOREIGN SCHEMA` | PG | Out of scope | Runtime discovery |
   | `REFRESH MATERIALIZED VIEW` | PG | Out of scope | Runtime DML |
   | Temporary tables | PG | Out of scope | Session-scoped |
   | Temporary views | PG | Out of scope | Session-scoped, same reasoning as temporary tables (Section 8.1) |
   | Inline data seeding | — | Out of scope | DPG is a schema tool; data management is outside its scope |
   | Database management (`CREATE`/`ALTER`/`DROP DATABASE`) | PG | Out of scope | Cluster-provisioning, not schema management; see Section 23 — permanent, not deferred |
   | `REASSIGN OWNED BY` / `DROP OWNED BY` | PG | Out of scope | One-shot maintenance command, not a declarable object; see Section 23 |
   | `ALTER SYSTEM` | PG | Out of scope | Cluster-wide GUC configuration, not schema state; see Section 23 |
   | `CREATE ACCESS METHOD` | PG | Out of scope | Covered by extension install (`CREATE EXTENSION`); see Section 23 |
   | `CREATE CONVERSION` | PG | Out of scope | No realistic use case under UTF-8 dominance; see Section 23 |
   | `CREATE [PROCEDURAL] LANGUAGE` | PG | Out of scope | Covered by extension install (`CREATE EXTENSION`); see Section 23 |
   | `CREATE TRANSFORM` | PG | Out of scope | Always bundled with a specific extension pairing; see Section 23 |
   | `ALTER EXTENSION ADD`/`DROP member_object` | PG | Out of scope | Extension-authoring concern, a tier below application-schema scope; see Section 23 |
   | `CLUSTER` (maintenance command) | PG | Out of scope | One-shot table rewrite, not idempotent as declared state; `CLUSTER ON` (Section 7.11) declares the target index only, doesn't invoke the command; see Section 23 |
   | Large Objects (`lo_create`/`lo_import`/`lo_export` etc.) | PG | Out of scope | No `CREATE`/`ALTER`/`DROP` DDL surface to attach to; see Section 23 |

---

## Appendix A. ABNF Grammar Summary

   This appendix collects DPG's own grammar.  Part 1 of a declaration
   is PostgreSQL's grammar for the corresponding statement and is not
   repeated (Section 4.2.2); each object section gives the block grammar
   for its kind.

```abnf
; Top-level source file
dpg-file = *( WSP / comment / macro-decl / top-level-decl )

top-level-decl = schema-decl                ; Section 6.1
               / virtual-type-decl          ; Section 5.6
               / default-privileges-decl    ; Section 11.4
               / parameter-privileges-decl  ; Section 11.6
               / object-decl

; Every other kind: the object keyword (Section 4.2), PostgreSQL's
; CREATE statement for that kind without CREATE (and without
; OR REPLACE / IF NOT EXISTS), any DPG additions to Part 1 its section
; defines, the terminator (Section 4.5), and the kind's block.
object-decl = object-keyword WSP <PostgreSQL's CREATE statement for the
                                  kind, without CREATE>
              terminator [ "{" <the kind's block> "}" ]

; Objects declared inside a SCHEMA block are the schema-scoped kinds.
nested-object = object-decl / virtual-type-decl

; Common terminals
WSP      = *( SP / HTAB / CRLF / LF )
SQUOTE   = %x27                        ; single quote '
DQUOTE   = %x22                        ; double quote "
integer  = [ "-" ] 1*DIGIT
boolean  = "true" / "false"
expr     = <arbitrary SQL expression text>
qual-name = identifier *( "." identifier )
schema-name = identifier *( "." identifier )

; Identifier
identifier = unquoted-id / quoted-id
unquoted-id = ( ALPHA / "_" ) *( ALPHA / DIGIT / "_" / "$" )
quoted-id   = DQUOTE *( safe-char / DQUOTE DQUOTE ) DQUOTE
safe-char   = <any Unicode character except DQUOTE>

; Common directives
owner-dir        = "OWNER" WSP identifier
comment-dir      = "COMMENT" WSP string-literal
; qual-name: a schema-qualified old name moves the object (Section 7.6)
renamed-from-dir = "RENAMED FROM" WSP qual-name
protected-dir    = "PROTECTED"
deprecated-dir   = "DEPRECATED" WSP string-literal
drop-cascade-dir = "DROP CASCADE"

; Names
col-name       = identifier
index-name     = identifier
partition-name = identifier
policy-name    = identifier
trigger-name   = identifier
table-ref      = qual-name

; Comments in source (Section 4.1)
comment = "--" <text up to the end of the line>
        / "/*" <text> "*/"

; Block structure (Section 4.2)
object-body      = <PostgreSQL's CREATE statement for the kind, without CREATE>
block-directive  = <a directive the kind's block accepts>
block-collection = <a collection directive: GRANTS, REVOKES, COLUMNS,
                    ATTRIBUTES, CONSTRAINTS, INDEXES, POLICIES, TRIGGERS,
                    PARTITIONS, NAME MAPS>
column-def       = <a PostgreSQL column definition>

; Collections in a table block (Sections 7.7-7.9)
indices-block  = "INDEXES" WSP "{" *index-decl "}" / "INDEX" WSP index-decl
policies-block = "POLICIES" WSP "{" *policy-decl "}" / "POLICY" WSP policy-decl
triggers-block = "TRIGGERS" WSP "{" *trigger-decl "}" / "TRIGGER" WSP trigger-decl

; Dollar-quoted string
dollar-string = dollar-delim *<any byte> dollar-delim
dollar-delim  = "$" *( ALPHA / DIGIT / "_" ) "$"

; String literal
string-literal = SQUOTE <text> SQUOTE

; WITH (...) option list, used where DPG grammar contains one
; (a FOREIGN partition's OPTIONS, Section 7.13).
option-list  = option-item *( "," WSP option-item )
option-item  = identifier [ WSP ( "=" WSP / "" ) option-value ]
option-value = string-literal / integer / boolean / identifier

; Text Search Configuration MAPPING FOR ... WITH ... (Section 12.1)
token-type-list = identifier *( "," WSP identifier )
dict-list       = qual-name *( "," WSP qual-name )

; Grants
grants-block      = "GRANTS" WSP "{" *( grant-entry ";" ) "}"
revocations-block = "REVOKES" WSP "{" *( revoke-entry ";" ) "}"
grant-entry  = privilege-list WSP "TO" WSP role-list
               [ WSP "WITH GRANT OPTION" ]
               [ WSP "GRANTED BY" WSP role-spec ]
revoke-entry = ( privilege-list / "ALL PRIVILEGES" ) WSP
               "FROM" WSP role-list
               [ WSP "GRANTED BY" WSP role-spec ]
               [ WSP "CASCADE" ]
privilege-list = privilege *( "," WSP privilege )
privilege = "SELECT" / "INSERT" / "UPDATE" / "DELETE" / "TRUNCATE" /
            "REFERENCES" / "TRIGGER" / "USAGE" / "EXECUTE" / "CREATE" /
            "CONNECT" / "TEMPORARY" / "MAINTAIN" / "ALL" / "ALL PRIVILEGES"
role-list  = identifier *( "," WSP identifier )
; role-spec is defined locally at its first use (Section 14.7, Tablespace
; OWNER) and reused verbatim by GRANTED BY above and by Role membership
; WITH clauses (Section 11.1) — not redefined per site.
role-spec  = identifier / "CURRENT_ROLE" / "CURRENT_USER" / "SESSION_USER"

; Macro preprocessor
macro-decl  = "MACRO" WSP identifier WSP ( paren-body / brace-body )
paren-body  = "(" *column-def ")"
brace-body  = "{" *( block-directive ";" ) "}"
spread      = "..." identifier

; See individual sections for all other productions.
```

---

## Appendix B. Complete Example Project

   This appendix shows a complete, coherent DPG project for a
   multi-tenant SaaS application.

```toml
# dpg.toml
kind = "root"

[compiler]
default_drop_behavior = "restrict"

[linter]
warn_on_deprecated           = true
forbid_hardcoded_passwords   = true
max_columns_per_table        = 50

[snapshots]
directory = ".dpg/snapshots"
```

```toml
# production/dpg.toml
kind = "cluster"

[cluster]
name                = "production"
cluster_objects_dir = "cluster"
link                = "env:PRODUCTION_DB_URL"
```

```toml
# production/myapp/dpg.toml
kind = "database"

[database]
name           = "myapp"
default_schema = "public"
```

```sql
-- production/cluster/roles.dpg

ROLE app_service
    LOGIN
    PASSWORD '{{vault:secret/roles/app_service#pw}}'
    CONNECTION LIMIT 20;

ROLE app_readonly NOLOGIN;

ROLE app_admin
    LOGIN
    NOSUPERUSER
    NOCREATEDB
    INHERIT;
```

```sql
-- production/myapp/extensions.dpg

EXTENSION pgcrypto;
EXTENSION pg_trgm CASCADE;
```

   Files under `schemas/<schema>/` do not wrap their declarations in a
   `SCHEMA { }` block: the schema is taken from the directory name
   (Section 3.1, rule 7).  The schema's own directives, if any, go in
   `schemas/<schema>/schema.dpg` as `SCHEMA <schema> { ... }`.

```sql
-- production/myapp/schemas/public/types.dpg

TYPE account_status AS ENUM ('trial', 'active', 'suspended', 'cancelled') {
    COMMENT 'Top-level account lifecycle states';
}

TYPE invoice_status AS ENUM ('draft', 'sent', 'paid', 'void', 'overdue') {
    COMMENT 'Billing lifecycle states for customer invoices';
}

DOMAIN positive_money AS NUMERIC(12, 2)
    CONSTRAINT must_be_positive CHECK (VALUE >= 0);
```

```sql
-- production/myapp/schemas/public/tables/accounts.dpg

MACRO audit_timestamps (
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
)

TABLE accounts (
    id     UUID           NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    name   TEXT           NOT NULL,
    status account_status NOT NULL DEFAULT 'trial',
    ...audit_timestamps
)
{
    COMMENT 'Top-level tenant accounts';
    ENABLE ROW LEVEL SECURITY;

    COLUMN status     { STATISTICS 300; }
    COLUMN created_at { STATISTICS 200; }

    INDEXES {
        idx_accounts_status (status) WHERE (deleted_at IS NULL);
    }

    POLICIES {
        isolate_tenants FOR ALL
            USING (id = current_setting('app.account_id')::UUID);
    }

    GRANTS {
        SELECT, INSERT, UPDATE TO app_service;
        SELECT                 TO app_readonly;
    }

    REVOKES {
        ALL PRIVILEGES FROM PUBLIC;
    }
}
```

```sql
-- production/myapp/schemas/public/tables/invoices.dpg

TABLE invoices (
    id         UUID           NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    account_id UUID           NOT NULL,
    status     invoice_status NOT NULL DEFAULT 'draft',
    total      positive_money NOT NULL DEFAULT 0,
    issued_at  TIMESTAMPTZ,
    due_at     TIMESTAMPTZ,
    created_at TIMESTAMPTZ    NOT NULL DEFAULT now(),
    CONSTRAINT fk_account FOREIGN KEY (account_id)
        REFERENCES accounts (id) ON DELETE CASCADE,
    CONSTRAINT ck_due_after_issued CHECK (due_at IS NULL OR due_at > issued_at)
)
{
    COLUMN created_at { STATISTICS 200; }

    INDEXES {
        idx_invoices_account (account_id);
        idx_invoices_status  (status) WHERE (status NOT IN ('paid', 'void'));
        idx_invoices_due     (due_at) WHERE (status = 'sent');
    }

    ENABLE ROW LEVEL SECURITY;

    POLICIES {
        isolate_tenants FOR ALL
            USING (account_id = current_setting('app.account_id')::UUID);
    }

    TRIGGERS {
        after_status_change AFTER UPDATE OF status
            FOR EACH ROW
            WHEN (OLD.status IS DISTINCT FROM NEW.status)
            EXECUTE FUNCTION notify_invoice_status_change();
    }

    GRANTS { SELECT, INSERT, UPDATE TO app_service; }
}
```

```sql
-- production/myapp/schemas/public/functions.dpg

FUNCTION notify_invoice_status_change() RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    PERFORM pg_notify(
        'invoice_status_changed',
        json_build_object(
            'invoice_id', NEW.id,
            'old_status', OLD.status,
            'new_status', NEW.status
        )::TEXT
    );
    RETURN NEW;
END;
$$;

MATERIALIZED VIEW account_billing_summary AS
    SELECT
        a.id,
        a.name,
        COUNT(i.id)                                                  AS total_invoices,
        COALESCE(SUM(i.total) FILTER (WHERE i.status = 'paid'),   0) AS paid_total,
        COALESCE(SUM(i.total) FILTER (WHERE i.status = 'overdue'),0) AS overdue_total
    FROM accounts a
    LEFT JOIN invoices i ON i.account_id = a.id
    WHERE a.deleted_at IS NULL
    GROUP BY a.id, a.name
WITH NO DATA {
    GRANTS { SELECT TO app_readonly; }
}
```

---

## Appendix C. Error Code Reference

   | Code | Name | Description |
   |------|------|-------------|
   | DPG-E000 | `unlabeled_compile_error` | A compile-time error with no specific code below (`dpg validate --format json`'s catch-all `rule` value). |
   | DPG-E001 | `unknown_config_key` | Unknown key in `dpg.toml`. |
   | DPG-E002 | `ambiguous_connection` | Both `url` and `link` set in cluster config. |
   | DPG-E003 | `no_connection_configured` | Command requires a connection but neither `url` nor `link` is set. |
   | DPG-E004 | `reserved_name_conflict` | A database directory name matches the cluster objects directory name. |
   | DPG-E005 | `conflicting_set_member` | Same-named set-valued property has conflicting definitions across files. |
   | DPG-E006 | `forbidden_verb` | `CREATE`, `ALTER`, or `DROP` at declaration level in a `.dpg` file. |
   | DPG-E007 | `macro_inside_block` | `MACRO` declaration found inside a block. |
   | DPG-E008 | `paren_macro_in_block` | Paren-body macro spread inside a `{ }` block. |
   | DPG-E009 | `brace_macro_in_paren` | Brace-body macro spread inside a `( )` list. |
   | DPG-E010 | `undefined_macro` | Spread of undefined macro name. |
   | DPG-E011 | `duplicate_macro` | Macro name redeclared in the same file. |
   | DPG-E012 | `circular_macro` | Circular macro reference detected. |
   | DPG-E013 | `enum_migration_data_remains` | Rows still hold a removed ENUM value after the MIGRATE REMOVE DML ran. |
   | DPG-E014 | `unguarded_enum_removal` | ENUM value removed without a `MIGRATE REMOVE` block. |
   | DPG-E015 | `invalid_virtual_type_directive` | `VIRTUAL TYPE { }` block contains a directive other than `COMMENT`. |
   | DPG-E016 | *(unused)* | Reserved; never assigned to a condition. |
   | DPG-E017 | `unresolvable_cycle` | Circular FK dependency with no `DEFERRABLE` FK. |
   | DPG-E018 | `unknown_column_reference` | `COLUMN name { }` block references a column not in the `( )` list. |
   | DPG-E019 | `stale_column_name_in_index` | Index or constraint references old column name after a rename. |
   | DPG-E020 | `statistics_target_out_of_range` | `STATISTICS n` value is outside `[-1, 10000]`. |
   | DPG-E021 | `stale_renamed_from` | `RENAMED FROM` references a name absent from both source and snapshot. |
   | DPG-E022 | `protected_drop_attempt` | Diff would drop a `PROTECTED` object. |
   | DPG-E023 | `temporary_table_declared` | `TEMPORARY TABLE` keyword found in a `.dpg` file. |
   | DPG-E024 | `unknown_block_directive` | Unknown directive for this object kind in a `{ }` block. |
   | DPG-E025 | `destructive_ops_blocked` | Migration contains `DESTRUCTIVE` ops but `--allow-destructive` not passed. |
   | DPG-E026 | `multiple_clusters_no_flag` | Multiple clusters found; `--cluster` required. |
   | DPG-E027 | `cluster_not_found` | `--cluster` value does not match any cluster. |
   | DPG-E028 | `multiple_databases_no_flag` | Multiple databases found; `--database` required. |
   | DPG-E029 | `database_not_found` | `--database` value does not match any database. |
   | DPG-E030 | `invalid_namemap_rule` | Unknown rule keyword in `[namemaps]` config or `NAME MAP` block directive. |
   | DPG-E031 | `duplicate_namemap_tool` | Same tool key specified more than once at the same block level (warning only; last entry wins). |
   | DPG-E032 | `cluster_name_required` | Cluster `dpg.toml` is missing a `[cluster] name` (Section 3.3). |
   | DPG-E033 | `database_name_required` | Database `dpg.toml` is missing a `[database] name` (Section 3.4). |
   | DPG-E034 | `duplicate_cluster_name` | Two cluster directories declare the same `name` (Section 3.6). |
   | DPG-E035 | `duplicate_database_name` | Two database directories within the same cluster declare the same `name` (Section 3.6). |
   | DPG-E036 | `owner_role_not_a_member` | Connecting role is not a member of one or more declared `OWNER` roles (Section 11.5). |
   | DPG-E037 | `semicolon_before_block` | A `;` appears between Part 1 and an immediately-following `{ }` block (Section 4.5). |
   | DPG-E038 | `missing_terminator` | No `{ }` block follows a declaration and Part 1 does not end with `;` (Section 4.5). |
   | DPG-E039 | `conflicting_scalar_property` | The same object is declared in more than one file with two different values for one scalar property, or an object kind with no per-property merge is declared in more than one file with different definitions (Section 3.7). |
   | DPG-E040 | `schema_scope_conflict` | Inside a schema scope (`SCHEMA { }` block or `schemas/<name>/` directory), a declared name or `DEFAULT PRIVILEGES IN SCHEMA` names a different schema, or a schema directory declares a different `SCHEMA` (Sections 3.1, 4.4). |
   | DPG-E041 | `invalid_config_kind` | A `dpg.toml` has no `kind`, an unknown `kind`, a `kind` that does not match how it is loaded, or `kind = "root"` below the project root (Section 3.1). |
   | DPG-E042 | `unresolved_database_cluster` | A database is not inside a cluster directory and sets no `parent`, or its `parent` does not resolve to a cluster directory inside the project, or contradicts the cluster directory it is inside (Sections 3.1, 3.4). |
   | DPG-E043 | `nested_scope_directory` | A cluster directory is inside another cluster or database directory, or a database directory is inside another database directory (Section 3.1). |
   | DPG-E044 | `virtual_type_name_conflict` | A `VIRTUAL TYPE` is named after a PostgreSQL built-in type, or its qualified name is already used by a declared type or relation (Section 5.6). |
   | DPG-E045 | `ignored_object_declared` | A declared object matches an `[ignore]` pattern (Section 3.9). |

---

## Appendix D. Supplementary Specifications

   This appendix specifies three features in full: secret resolution,
   Name Maps, and the `SERIAL` column types.  It has the same normative
   weight as the main sections.

### D.1. Secret Resolution

   A secret reference is a URI whose scheme names the backend that
   resolves it to a plaintext value at connection or apply time.  The
   five schemes below are defined; an unknown scheme is an error naming
   the scheme and the supported ones.

   Every backend authenticates through that provider's own standard
   credential chain (environment variables, config files, managed
   identity, CLI login).  Only resolving a reference can fail; merely
   configuring a project never contacts a backend.

   **`env:<VAR_NAME>`**

   Resolves to the environment variable's value; an unset or empty
   variable is an error.  The variable name
   MUST consist only of ASCII letters, digits, and underscores.

   Example: `link = "env:PRODUCTION_DB_URL"` → resolves `PRODUCTION_DB_URL`
   from the process environment (which may have been populated from the
   `.env` file per Section 18's global `--env` option).

   **`vault:<mount>/<path>#<field>`**

   Reads a HashiCorp Vault KV version 2 secret (`VAULT_ADDR`/`VAULT_TOKEN`
   ambient auth, the same convention the `vault` CLI uses). `mount` is the
   KV v2 engine's mount path, `path` is the secret's path within it. The
   `#field` suffix is REQUIRED — a Vault secret is always a key-value map,
   never a bare string, so there is no unambiguous "whole value" reading
   to fall back to. Example: `vault:secret/myapp/db#password`.

   **`aws-sm:<secret-id>[#<json-field>]`**

   Reads an AWS Secrets Manager secret via the AWS SDK's default
   credential chain. `secret-id` is the secret's ARN or name (may contain
   `/`). Without `#json-field`, resolves to the secret's raw
   `SecretString`. With it, `SecretString` is parsed as JSON and that key
   extracted. Examples: `aws-sm:myapp/prod/db-password`,
   `aws-sm:myapp/prod/db#password`.

   **`gcp-sm:<project>/<secret-id>[/<version>][#<json-field>]`**

   Reads a GCP Secret Manager secret via Application Default Credentials.
   `version` defaults to `"latest"` if omitted. Without `#json-field`,
   resolves to the raw payload as UTF-8 text; with it, the payload is
   parsed as JSON and that key extracted. Example:
   `gcp-sm:my-project/db-password`.

   **`azure-kv:<vault-name>/<secret-name>[/<version>][#<json-field>]`**

   Reads an Azure Key Vault secret via `DefaultAzureCredential`.
   `vault-name` resolves to `https://<vault-name>.vault.azure.net/`.
   `version` omitted (or empty) means the latest version. Without
   `#json-field`, resolves to the raw secret value; with it, the value is
   parsed as JSON and that key extracted. Example:
   `azure-kv:my-vault/db-password`.

   **Embedding a reference inside a literal: `{{<secret-uri>}}`**

   The five schemes above resolve a URI that occupies an *entire* field
   (`link` in `dpg.toml`). Some fields are themselves a larger literal that
   only a *part* of should come from a secret — a SUBSCRIPTION `CONNECTION`
   conninfo string being the first case (Section 13.2): most of it (host, dbname,
   user) is not sensitive, and forcing the whole value into a secret
   (duplicating the non-sensitive parts into the backend, or requiring a
   backend-side template) is worse than just resolving the one sensitive
   part in place.  Each `{{<secret-uri>}}` placeholder in such a field is
   replaced by the resolved value, and the rest of the text is left as
   written; text with no placeholder is used as is.  The same mechanism
   applies to Role `PASSWORD` (Section 11.1) and User Mapping `OPTIONS`
   (Section 14.10).
   `{{...}}` is deliberately the *only* trigger for resolution in such a
   field: unlike `link`, a real literal in one of these fields may itself
   contain a `:` (a conninfo string can be a `postgresql://user:pass@host/db`
   URI), so treating any colon-prefixed substring as a candidate secret
   reference would risk misreading a literal as broken. Curly braces never
   appear in legitimate conninfo/option syntax, so `{{...}}` cannot collide.

---

### D.2. Name Maps

   Name Maps provide a mechanism for attaching tool-specific naming
   conventions to DPG schema objects.  They allow downstream consumers
   — ORM generators, type-safe query builders, API code generators —
   to transform DPG identifier names into the naming convention
   appropriate for their target language or framework, without
   requiring any changes to the DPG source files.

   Name Maps operate at two orthogonal layers:

   -   **Configuration layer** (`dpg.toml`): project-wide defaults,
       settable at root, cluster, and database scope.
   -   **Block layer** (the `{ }` DPG block): per-object or per-column
       overrides declared inline in the source file.

   Resolution is most-specific-wins: block-level beats database
   config, database config beats cluster config, cluster config beats
   root config.  Resolution is applied independently per tool key per
   object type.

#### D.2.1. Rule Keywords

   DPG defines a closed set of naming-convention rule keywords.  All
   keywords are case-insensitive in TOML config but stored and
   validated in their canonical upper-case form.  A rule keyword MUST
   be written without quotation marks in the DPG block syntax.

   | Rule Keyword | Example transformation of `user_profile_id` |
   |---|---|
   | `LOWER_SNAKE_CASE` | `user_profile_id` |
   | `UPPER_SNAKE_CASE` | `USER_PROFILE_ID` |
   | `LOWER_CAMEL_CASE` | `userProfileId` |
   | `UPPER_CAMEL_CASE` | `UserProfileId` |
   | `LOWER_KEBAB_CASE` | `user-profile-id` |
   | `UPPER_KEBAB_CASE` | `USER-PROFILE-ID` |
   | `TRAIN_CASE` | `User-Profile-Id` |
   | `LOWER_CASE` | `userprofileid` |
   | `UPPER_CASE` | `USERPROFILEID` |
   | `PASCAL_SNAKE_CASE` | `User_Profile_Id` |

   A literal target name (not a rule) is written as a double-quoted
   identifier in the DPG block syntax (e.g., `"MySpecialName"`).
   Literal names are stored verbatim in the snapshot and are passed
   through to consumers unchanged.  Literal names are only supported
   in the block layer, not in `dpg.toml`.

   The reserved tool key `default` MUST NOT be used as an actual tool
   name.  It represents the catch-all rule applied by any tool for
   which no explicit rule is configured.

#### D.2.2. Configuration Layer (`dpg.toml`)

   All three configuration files (root, cluster, database) accept a
   `[namemaps]` section.  Within this section:

   -   Scalar string entries are **global rules** keyed by tool name.
   -   Subtable entries (`[namemaps.<type>]`) are **per-object-type
       rules** keyed by tool name, where `<type>` matches the DPG
       object-type identifier (e.g., `column`, `table`, `view`).

   Deeper configuration scopes override shallower ones for the same
   (tool, type) pair.

```toml
# Root dpg.toml — project-wide defaults
[namemaps]
default  = "LOWER_SNAKE_CASE"   # catch-all for all tools
prisma   = "LOWER_CAMEL_CASE"   # Prisma-specific global rule
drizzle  = "LOWER_CAMEL_CASE"

[namemaps.table]
prisma  = "UPPER_CAMEL_CASE"    # Prisma table names: PascalCase
drizzle = "LOWER_CAMEL_CASE"    # Drizzle table names: camelCase

[namemaps.column]
prisma  = "LOWER_CAMEL_CASE"
drizzle = "LOWER_CAMEL_CASE"
```

```toml
# Cluster or database dpg.toml — narrows/overrides root config
[namemaps]
sqlc = "LOWER_SNAKE_CASE"

[namemaps.function]
sqlc = "LOWER_SNAKE_CASE"
```

   Rules MUST be one of the ten keywords listed in Appendix D.2.1.  Unknown
   keywords MUST cause a validation error (DPG-E030).  Literal names
   are NOT supported in `dpg.toml`; only rule keywords are accepted.

#### D.2.3. Block Layer — Inline Directives

   Name map directives appear in the `{ }` blocks of the objects a
   downstream tool names: tables and their columns, views, materialized
   views, functions, procedures, aggregates, types, domains, virtual
   types, schemas, extensions, sequences and roles.  Elsewhere they are
   error DPG-E024.  Two forms are supported.

**Singular form:**

```
NAME MAP TO <rule> ;
NAME MAP <tool> TO <rule> ;
NAME MAP <tool> TO "LiteralName" ;
```

   -   When `<tool>` is omitted, the directive applies to the `default`
       tool key.
   -   `<rule>` is an unquoted rule keyword (case-insensitive).
   -   `"LiteralName"` is a double-quoted literal target identifier.
       Double quotes follow the PostgreSQL identifier quoting convention
       (identifiers, not string values).

**Grouped form:**

```
NAME MAPS {
  <tool> TO <rule> ;
  <tool> TO "LiteralName" ;
  ...
}
```

   -   The `NAME MAPS { }` form requires explicit tool names; the
       implicit `default` shorthand is NOT available inside the group.
   -   Multiple singular `NAME MAP` directives and `NAME MAPS` blocks
       MAY be mixed freely within the same `{ }` block.
   -   If the same tool key appears more than once in a single `{ }`
       block the last entry wins.

**Examples:**

```sql
TABLE users (
  id       BIGINT GENERATED ALWAYS AS IDENTITY,
  email    TEXT NOT NULL,
  username TEXT NOT NULL
) {
  -- All tools: use LOWER_SNAKE_CASE (default catch-all)
  NAME MAP TO LOWER_SNAKE_CASE;

  -- Prisma and drizzle: override to camelCase
  NAME MAPS {
    prisma  TO LOWER_CAMEL_CASE;
    drizzle TO LOWER_CAMEL_CASE;
  }

  COLUMNS {
    username {
      -- Override: literal name for the sqlc tool only
      NAME MAP sqlc TO "UserName";
    }
  }
}
```

```sql
TYPE user_status AS ENUM ('active', 'inactive', 'banned') {
  NAME MAP prisma TO UPPER_CAMEL_CASE;
}
```

#### D.2.4. Snapshot Representation

   Name maps are serialised to the snapshot `name_maps` array field
   present on all typed snap objects (`SnapTable`, `SnapColumn`,
   `SnapView`, `SnapFunction`, `SnapType`, `SnapSchema`, `SnapExtension`,
   `SnapSequence`, `SnapRole`, `SnapVirtualType`, `SnapOpaque`).

   Each entry is a `SnapNameMapEntry`:

```json
{
  "tool": "prisma",
  "rule": "LOWER_CAMEL_CASE"
}
```

   or, for a literal name:

```json
{
  "tool": "sqlc",
  "name": "UserName"
}
```

   Fields:

   | Field | Type | Required | Description |
   |-------|------|----------|-------------|
   | `tool` | string | YES | Tool key (e.g., `"prisma"`, `"default"`). |
   | `rule` | string | one of rule/name | One of the ten rule keywords. Present when the value is a rule. |
   | `name` | string | one of rule/name | Literal target identifier. Present when the value is a literal. |

   Exactly one of `rule` or `name` MUST be non-empty.  Both being
   populated in the same entry is invalid.  The `name_maps` field is
   omitted entirely from the JSON when the array is empty (`omitempty`).

   The differ does not generate SQL for name map changes — name maps
   are metadata annotations consumed by downstream tools, not by
   PostgreSQL.  A change to `name_maps` triggers a snapshot update
   only (no SQL emitted, no migration step added).

#### D.2.5. Error Codes

   | Error ID | Code String | Condition |
   |----------|-------------|-----------|
   | DPG-E030 | `invalid_namemap_rule` | Unknown rule keyword in `[namemaps]` config or `NAME MAP` block directive. |
   | DPG-E031 | `duplicate_namemap_tool` | Same tool key specified more than once at the same block level (warning only; last entry wins). |

---

### D.3. SERIAL / BIGSERIAL / SMALLSERIAL Columns

   [PG] `SERIAL`, `BIGSERIAL`, and `SMALLSERIAL` are PostgreSQL's
   shorthand for an integer column with an owned sequence and a
   `nextval()` default.  [DPG] DPG treats them as follows.

   **Normalization:** the compiler records the column's real type and
   remembers which shorthand was written:

   | Declared spelling(s) | Stored type | Shorthand recorded |
   |---|---|---|
   | `SERIAL`, `SERIAL4` | `integer` | `SERIAL` |
   | `SMALLSERIAL`, `SERIAL2` | `smallint` | `SMALLSERIAL` |
   | `BIGSERIAL`, `SERIAL8` | `bigint` | `BIGSERIAL` |

   Everything that reasons about the column's type (indexes,
   constraints, casts) sees the real integer type.  Only an
   unqualified, case-insensitive match triggers this rule; a
   schema-qualified type named `serial` is an ordinary user type.

   **`NOT NULL`:** a `SERIAL`-family column is always `NOT NULL`, as in
   PostgreSQL.

   **Emitted DDL:** `CREATE TABLE` and `ADD COLUMN` write the shorthand
   keyword and omit `NOT NULL` and `DEFAULT`, which PostgreSQL creates
   itself.  `ADD COLUMN` of such a column is `CAUTION`, like an identity
   column, because filling it rewrites the table (Section 17.2).

   **Introspection:** a column is read back as `SERIAL`-family when it
   is an integer column whose default is `nextval()` on a sequence the
   column owns (a `pg_depend` auto dependency).  A `nextval()` default on
   any other column is an ordinary default.

   **Dump:** `dpg dump` writes the shorthand keyword, never the
   expanded default, since dump does not declare the owned sequence as
   its own `SEQUENCE`.

   **Snapshot:** a column's snapshot record carries an optional
   `serial` field naming the shorthand.

   **Linter:** `serial-sequence-declared` (Section 19.3) fires when a
   declared `SEQUENCE` has the owned sequence's name
   (`<table>_<column>_seq`).

---

## Appendix E. Revision History

   This appendix lists every revision of this specification.  The
   history of the drafts before 1.0 is kept separately in
   `rfc/HISTORY.md` and is not part of this specification.

   | Revision | Date | Description |
   |----------|------|-------------|
   | 1.0.0 | (lock date) | Initial version. |

---

## Appendix F. Standard SQL / PostgreSQL-Specific Classification

   This appendix satisfies Tenet 3 (Section 1.4): for every construct this
   specification documents, whether it is ISO/IEC 9075 (Standard SQL)
   or PostgreSQL-specific — the classification `dpg portability`
   reports to users.  Three classifications are used:

   -   **Standard** — part of ISO/IEC 9075 in some edition (a
       standard-conformant database may support a different dialect of
       the same feature, e.g. a different `CREATE TABLE` grammar, but
       the *concept* is standardized).
   -   **PGSpecific** — a PostgreSQL extension with no ISO/IEC 9075
       equivalent, or a PostgreSQL-only spelling of a concept another
       vendor would express differently.
   -   **N/A** — not a real PostgreSQL DDL construct at all: DPG-native
       syntax that generates no SQL of its own (RFC Section 23's "No SQL"
       classification in Section 25's coverage matrix), or a DPG lifecycle/
       tooling directive (`RENAMED FROM`, `PROTECTED`, `DEPRECATED`,
       `MIGRATE REMOVE`, Name Maps, macros).  Tenet 3's Standard/
       PGSpecific dichotomy doesn't apply to these; `dpg portability`
       never reports them.

   A row classifies the construct as a whole; a construct with both a
   standard core and PostgreSQL-specific optional clauses is marked
   **Mixed**, with the PG-specific sub-clauses named in Notes.

   **`Min PG Version`:** the version-gating input for `min_pg_version`
   (Section 3.8) and its `min-pg-version` linter rule (Section 19.3).
   A blank cell means the construct has been available since this
   specification's own supported floor, PostgreSQL 14 (Section 1.4),
   and is never gated regardless of a project's configured
   `min_pg_version`.  A value means the construct requires at least
   that PostgreSQL major version; a value with a parenthetical
   qualifies which part of a Mixed row's construct the gate applies to
   when the row's other part is floor-14.  *(on change)* marks a
   statement DPG emits only to change an existing object, not a
   construct written in source.  Which of these the `min-pg-version`
   rule checks is specified in Section 3.8.

   | Section | Construct | Classification | Min PG Version | Notes |
   |---|---|---|---|---|
   | Sections 4.1-4.6 | Basic lexical rules (identifiers, comments, dollar-quoting, statement terminators) | N/A | — | DPG source-file syntax, not PostgreSQL DDL. |
   | Section 5.1 | `CREATE TYPE ... AS ENUM` | PGSpecific | — | ISO SQL has no enumerated type; closest standard analogue is a `CHECK` constraint or `DOMAIN`. |
   | Section 5.2 | `CREATE TYPE ... AS (...)` (composite) | PGSpecific | — | Structured/row types exist in SQL:1999+ but with different syntax and semantics (`CREATE TYPE ... AS (...)` the PostgreSQL way is not the standard's `CREATE TYPE` for UDTs). |
   | Section 5.3 | `CREATE TYPE ... AS RANGE` | PGSpecific | — | No standard range type. |
   | Section 5.4 | `CREATE DOMAIN` core (name, base type) | Standard | — | SQL:1999+ `CREATE DOMAIN`. |
   | Section 5.4 | Domain `DEFAULT`/`CONSTRAINT ... CHECK`/`NOT NULL` | Standard | — | Same standard `CREATE DOMAIN` clauses. |
   | Section 5.4 | Domain `COLLATE` | PGSpecific | — | PostgreSQL-specific collation attachment syntax (the standard has collations, but not this clause shape). |
   | Section 5.5 | `CREATE TYPE (...)` (base/shell type) | PGSpecific | — | C-level storage type definition; no standard equivalent. |
   | Section 5.6 | `VIRTUAL TYPE` | N/A | — | DPG-native, generates no SQL. |
   | Section 6 | `CREATE EXTENSION` | PGSpecific | — | PostgreSQL's own extension-packaging mechanism. |
   | Sections 7.1-7.2 | `CREATE TABLE` core (columns, types, `PRIMARY KEY`/`UNIQUE`/`CHECK`/`FOREIGN KEY`) | Standard | — | Core relational DDL. |
   | Section 7.1 | `WITH (storage_params)` | PGSpecific | — | PostgreSQL storage parameters. |
   | Section 7.1 | `TABLESPACE` | PGSpecific | — | No standard tablespace concept. |
   | Section 7.1 | `INHERITS` | PGSpecific | — | PostgreSQL table inheritance; not in the standard. |
   | Section 7.1 | `UNLOGGED` | PGSpecific | — | PostgreSQL crash-safety trade-off, no standard equivalent. |
   | Section 7.1 | `USING method` (table access method) | PGSpecific | 17 (partitioned tables only) | Floor-14 on an ordinary table. |
   | Section 7.11 | `ALTER TABLE ... SET ACCESS METHOD` | PGSpecific | 15 (on change; `DEFAULT` form 17) | Emitted when a table's declared access method changes. |
   | Section 7.2 | Generated columns (`GENERATED ALWAYS AS ... STORED`) | Standard | — | SQL:2003 generated columns. |
   | Section 7.2 | Generated columns (`GENERATED ALWAYS AS ... VIRTUAL`) | Standard | 18 | SQL:2003 generated columns also standardize `VIRTUAL`; PostgreSQL only implemented the `STORED` variant until PostgreSQL 18 added this one. |
   | Section 7.2 | Identity columns (`GENERATED ... AS IDENTITY`) | Standard | — | SQL:2003 identity columns. |
   | Section 7.2 | `SERIAL`/`BIGSERIAL`/`SMALLSERIAL` | PGSpecific | — | Pre-standard PostgreSQL sequence sugar; `IDENTITY` is the standard-conformant replacement. |
   | Section 7.2 | Column `COMPRESSION`/`STORAGE` | PGSpecific | — | PostgreSQL TOAST-related storage tuning. |
   | Section 7.2 | `EXCLUDE` constraints | PGSpecific | — | No standard exclusion-constraint concept. |
   | Section 7.2 | `NOT VALID`/`VALIDATE CONSTRAINT` | PGSpecific | — | PostgreSQL's incremental constraint-validation lifecycle. |
   | Section 7.2 | `NOT NULL ... NO INHERIT` | PGSpecific | 18 | Extends the pre-existing `CHECK ... NO INHERIT` modifier (floor-14 PGSpecific) to `NOT NULL` constraints for the first time. |
   | Section 7.2 | `ENFORCED`/`NOT ENFORCED` (`CHECK`/`FOREIGN KEY`) | PGSpecific | 18 | PostgreSQL's own catalog-recorded-but-unchecked constraint mode; no SQL-standard citation for this exact clause. |
   | Section 7.2 | FK `ON DELETE SET NULL`/`SET DEFAULT (col-list)` | PGSpecific | 15 | Column-scoped refinement of the standard bare `SET NULL`/`SET DEFAULT` form (the bare form is covered by the `CREATE TABLE` core row above); the column-list itself has no SQL-standard equivalent. |
   | Section 7.3 | Table-level named `NOT NULL` constraint | Standard | 18 | PostgreSQL 18 gave `NOT NULL` the same named, catalogued constraint treatment `CHECK`/`PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` already had; additive to the pre-existing inline column-level form (Section 7.2 row above), not a replacement. |
   | Sections 7.2-7.3, 7.7 | `UNIQUE ... NULLS NOT DISTINCT` (constraint or index) | Standard | 15 | SQL:2023 null treatment for unique constraints (feature F292). |
   | Section 7.3 | `NOT VALID` on a `NOT NULL` constraint | PGSpecific | 18 | Extends the existing `NOT VALID` lifecycle to `NOT NULL` constraints. |
   | Section 7.3 | `ALTER CONSTRAINT ... [NO] INHERIT` | PGSpecific | 18 (on change) | Emitted when only a `NOT NULL` constraint's `NO INHERIT` changes. |
   | Section 7.3 | `EXCLUDE` constraint on a partitioned table | PGSpecific | 17 | Exclusion constraints themselves are floor-14 (Section 7.2 row above). |
   | Section 7.3 | Temporal keys (`WITHOUT OVERLAPS`/`PERIOD`, incl. temporal `FOREIGN KEY`) | Mixed | 18 | SQL:2011 standardizes temporal `PRIMARY KEY`/`UNIQUE`; PostgreSQL enforces it via an exclusion search under the hood while keeping the constraint catalogued as an ordinary `PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` — the concept is standard, the grammar and mechanism are PostgreSQL's own dialect. |
   | Section 7.4 | Column `STATISTICS n` / `STATISTICS DEFAULT` | PGSpecific | — | Planner statistics-target tuning; no standard equivalent.  `DEFAULT` is emitted as `SET STATISTICS -1`, which every supported version accepts. |
   | Section 7.4 | `ALTER COLUMN ... SET EXPRESSION` | PGSpecific | 17 (on change) | Emitted when a generated column's expression changes and its `STORED`/`VIRTUAL` kind does not. |
   | Section 7.7 | `CREATE INDEX` core | Mixed | — | Indexes exist informally across all vendors but are not in ISO/IEC 9075 at all — classified PGSpecific as a whole (see next row), since "index" is not a standard DDL concept, only a near-universal vendor extension. |
   | Section 7.7 | `CREATE INDEX` (all forms: access methods, partial, expression, covering, opclass) | PGSpecific | — | No SQL-standard `CREATE INDEX` statement exists; every vendor's index DDL is proprietary. |
   | Section 7.8 | `ROW LEVEL SECURITY`/`CREATE POLICY` | PGSpecific | — | No standard row-security mechanism. |
   | Section 7.9 | `CREATE TRIGGER` core (`BEFORE`/`AFTER`, events, `FOR EACH ROW`) | Standard | — | SQL:1999+ triggers. |
   | Section 7.9 | `REFERENCING OLD/NEW TABLE` (transition tables) | Standard | — | SQL:1999+ triggers include transition tables. |
   | Section 7.9 | `WHEN (condition)`, `EXECUTE FUNCTION` | PGSpecific | — | PostgreSQL trigger-function-calling model differs from the standard's inline trigger action. |
   | Section 7.10 | `GRANTED BY role` | Standard | — | PostgreSQL accepts this SQL-standard clause but restricts the effective grantor to `current_user`. |
   | Section 7.11 | `RENAMED FROM`, `PROTECTED`, `DEPRECATED`, `DROP CASCADE` (directives) | N/A | — | DPG-native lifecycle metadata. |
   | Section 7.11 | `OWNER` | PGSpecific | — | PostgreSQL's object-ownership model (`ALTER ... OWNER TO`) has no standard equivalent (the standard ties privileges to the creating authorization identifier with no separate transferable "owner"). |
   | Section 7.12 | `UNLOGGED TABLE` | PGSpecific | — | See Section 7.1 row. |
   | Section 7.12 | `CREATE FOREIGN TABLE` | Standard | — | SQL/MED (ISO/IEC 9075-9) standardizes foreign tables; PostgreSQL's FDW mechanism implements it. |
   | Section 7.13 | `PARTITION BY`/`PARTITION OF` | Standard | — | SQL:2016 adds declarative partitioning as a standard concept, though exact grammar varies by vendor; PostgreSQL's own syntax is a PG-specific dialect of a standardized concept — classified Mixed in spirit, PGSpecific in literal grammar. |
   | Section 7.3 | `DROP CONSTRAINT ... ONLY` on a partitioned table (partition-local constraint declaration) | PGSpecific | 18 (on change) | PostgreSQL's own declarative-partitioning constraint-inheritance mechanism; no standard concept of detaching a constraint from a partitioned parent while it stays local to existing partitions. |
   | Section 8 | `CREATE VIEW` | Standard | — | Core SQL. |
   | Section 8 | `CREATE MATERIALIZED VIEW` | PGSpecific | — | Materialized views are a common vendor extension, not in ISO/IEC 9075. |
   | Section 8 | `RECURSIVE VIEW`/`WITH RECURSIVE` | Standard | — | SQL:1999+ recursive query support. |
   | Sections 9.1-9.2 | `CREATE FUNCTION` core (name, args, `RETURNS`, `LANGUAGE`) | Standard | — | SQL/PSM (ISO/IEC 9075-4) standardizes stored routines; PostgreSQL's concrete grammar and `LANGUAGE` mechanism are its own dialect of the standardized concept. |
   | Section 9.1 | `LANGUAGE sql`/`plpgsql` dollar-quoted bodies | PGSpecific | — | Dollar-quoting itself, and `plpgsql`, are PostgreSQL-specific; SQL/PSM's own procedural language differs. |
   | Section 9.1 | PG14+ `sql_body`/`BEGIN ATOMIC` form | Standard | — | The ISO-standard-conformant alternative to dollar-quoting for `LANGUAGE sql` functions; available since this specification's own floor, not a later gate. |
   | Section 9.2 | `VOLATILE`/`STABLE`/`IMMUTABLE`, `PARALLEL SAFE`/etc., `COST`/`ROWS`, `SUPPORT` | PGSpecific | — | Query-planner hints with no standard equivalent. |
   | Section 9.2 | `SECURITY DEFINER`/`SECURITY INVOKER` | Standard | — | SQL/PSM standardizes routine security characteristics (exact keyword differs by vendor, but the concept is standard). |
   | Section 9.3 | `CREATE PROCEDURE` | Standard | — | SQL/PSM standardizes procedures distinct from functions. |
   | Section 9.4 | `CREATE AGGREGATE` | PGSpecific | — | User-defined aggregates with this declaration shape (`SFUNC`/`STYPE`/etc.) are PostgreSQL-specific; the standard has no equivalent `CREATE AGGREGATE`. |
   | Section 10 | `CREATE SEQUENCE` | Standard | — | SQL:2003+ standardizes sequences (`AS`/`INCREMENT`/`MINVALUE`/`MAXVALUE`/`START`/`CACHE`/`CYCLE` are all standard clauses). |
   | Section 10 | `UNLOGGED SEQUENCE` | PGSpecific | 15 | PostgreSQL crash-safety trade-off, no standard equivalent. |
   | Section 10 | `OWNED BY` | PGSpecific | — | PostgreSQL's sequence-to-column ownership link has no standard equivalent. |
   | Section 11.1 | `CREATE ROLE`/`CREATE USER` core (`LOGIN`, `PASSWORD`) | Standard | — | SQL standardizes authorization identifiers, though PostgreSQL's unified role model (merging users and groups) is its own design. |
   | Section 11.1 | `SUPERUSER`/`CREATEDB`/`CREATEROLE`/`REPLICATION`/`BYPASSRLS`/`CONNECTION LIMIT` | PGSpecific | — | PostgreSQL-specific role attributes. |
   | Section 11.2 | `GRANT`/`REVOKE` core | Standard | — | Core SQL privilege model. |
   | Section 7.10 | `MAINTAIN` privilege | PGSpecific | 17 | PostgreSQL-specific privilege covering VACUUM/ANALYZE/CLUSTER/REINDEX; gated on tables, columns, views, materialized views, and default privileges. |
   | Section 11.1 | Role membership (`GRANT role TO role`, `WITH ADMIN OPTION`/`WITH INHERIT`/`WITH SET`) | Mixed | 16 (`WITH INHERIT`/`WITH SET` only) | Standard SQL has roles and role grants; `WITH ADMIN OPTION` is standard and floor-14, `WITH INHERIT`/`WITH SET` are PostgreSQL-specific refinements of role-attribute inheritance. |
   | Section 11.4 | `ALTER DEFAULT PRIVILEGES` | PGSpecific | — | No standard mechanism for privilege templates applied to not-yet-created objects. |
   | Section 11.5 | Owner impersonation (`SET ROLE`/`RESET ROLE`) | Standard | — | SQL standardizes `SET ROLE`; PostgreSQL's specific creator-attribution semantics this section documents are PostgreSQL's own catalog behavior. |
   | Section 11.6 | `PARAMETER PRIVILEGES` (`GRANT {SET\|ALTER SYSTEM} ON PARAMETER`) | PGSpecific | 15 | PostgreSQL's grantable parameter-ACL object type; no standard equivalent. |
   | Section 12.1 | `CREATE TEXT SEARCH CONFIGURATION`/`MAPPING FOR ... WITH ...` | PGSpecific | — | PostgreSQL full-text search is entirely proprietary. |
   | Sections 12.2-12.4 | Text Search Dictionary/Parser/Template | PGSpecific | — | Same as above. |
   | Section 13.1 | `CREATE PUBLICATION` | PGSpecific | — | PostgreSQL's own logical-replication publish/subscribe model. |
   | Section 13.1 | Publication `FOR TABLES IN SCHEMA`, per-table column lists, per-table `WHERE` row filters | PGSpecific | 15 | |
   | Section 13.1 | Publication option `publish_generated_columns` | PGSpecific | 18 | |
   | Section 13.2 | `CREATE SUBSCRIPTION` | PGSpecific | — | Same. |
   | Section 13.2 | Subscription options `two_phase`, `disable_on_error` | PGSpecific | 15 | |
   | Section 13.2 | Subscription options `origin`, `run_as_owner`, `password_required`, `streaming = parallel` | PGSpecific | 16 | |
   | Section 13.2 | Subscription option `failover` | PGSpecific | 17 | |
   | Section 14.1 | `CREATE EVENT TRIGGER` | PGSpecific | — | No standard DDL-event trigger mechanism. |
   | Section 14.1 | Event Trigger `login` event | PGSpecific | 17 | One value of the `ON <event>` clause's event-type enum; every other value (`ddl_command_start`/`ddl_command_end`/`table_rewrite`/`sql_drop`) is floor-14. |
   | Section 14.2 | `CREATE COLLATION` | Standard | — | SQL:2003+ standardizes collations; PostgreSQL's ICU/libc provider mechanism is its own extension of the standard concept. |
   | Section 14.2 | Collation `PROVIDER = builtin` | PGSpecific | 17 | PostgreSQL's own built-in collation provider. |
   | Section 14.2 | Collation `REFRESH VERSION` | PGSpecific | 15 | Updates the catalog's recorded collation-provider version; no standard equivalent. |
   | Section 14.2 | Collation `RULES` (ICU tailoring) | PGSpecific | 16 | PostgreSQL's own ICU collation-tailoring integration; no standard equivalent. |
   | Section 14.3 | `CREATE CAST` | Standard | — | SQL standardizes user-defined casts (`CREATE CAST`), though PostgreSQL's `WITH FUNCTION`/`WITHOUT FUNCTION`/`WITH INOUT` grammar has PostgreSQL-specific shorthand forms. |
   | Section 14.4 | `CREATE OPERATOR`/`OPERATOR CLASS`/`OPERATOR FAMILY` | PGSpecific | — | User-defined operators and index access-method integration are PostgreSQL-specific; the standard has no equivalent. |
   | Section 14.5 | (Cast — see Section 14.3) | — | — | Cross-reference; see Section 14.3 row. |
   | Section 14.6 | `CREATE STATISTICS` (extended statistics objects) | PGSpecific | — | PostgreSQL planner-statistics extension; expression-based statistics (not just plain columns) are floor-14, passed through as opaque text. |
   | Section 14.7 | `CREATE TABLESPACE` | PGSpecific | — | No standard physical-storage-location concept. |
   | Section 14.8 | `CREATE FOREIGN DATA WRAPPER` | Standard | — | SQL/MED standardizes the FDW concept (`CREATE FOREIGN DATA WRAPPER`), though HANDLER/VALIDATOR functions are PostgreSQL's own extension mechanism. |
   | Section 14.9 | `CREATE SERVER` | Standard | — | SQL/MED standardizes foreign servers. |
   | Section 14.10 | `CREATE USER MAPPING` | Standard | — | SQL/MED standardizes user mappings for foreign servers. |
   | Section 14.11 | `SECURITY LABEL` | PGSpecific | — | PostgreSQL's MAC/label-security integration point (SELinux/sepgsql etc.); no standard equivalent. |
   | Sections 15-21 | Compilation pipeline, snapshot format, CLI, linter, safety classification, diffing semantics | N/A | — | DPG tooling, not PostgreSQL DDL. |
   | Section 22 | Dependency graph / topological sort | N/A | — | DPG compiler internals. |
   | Throughout | `COMMENT ON ...` | Standard | — | SQL:1999+ standardizes `COMMENT ON`, though PostgreSQL supports it on a broader set of object kinds than the standard requires. |
   | Throughout | `{ }` block itself, Name Maps, macros | N/A | — | DPG-native source syntax; generates no SQL directly. |

   **How this table is maintained:** a construct newly documented by a
   future revision of this specification MUST be classified here in
   the same revision (Appendix E's entry for that revision should note
   the addition) — this is what keeps Tenet 3 checkable rather than
   aspirational, the defect this appendix was added to close.  A
   construct gated by `min_pg_version` above its own floor of
   PostgreSQL 14 MUST have its `Min PG Version` cell populated in that
   same revision, for the identical reason applied to Section 3.8's
   `min-pg-version` rule.

---

## Normative References

   [RFC2119]  Bradner, S., "Key words for use in RFCs to Indicate
              Requirement Levels", BCP 14, RFC 2119,
              DOI 10.17487/RFC2119, March 1997,
              <https://www.rfc-editor.org/rfc/rfc2119>.

   [RFC5234]  Crocker, D. and P. Overell, "Augmented BNF for Syntax
              Specifications: ABNF", STD 68, RFC 5234,
              DOI 10.17487/RFC5234, January 2008,
              <https://www.rfc-editor.org/rfc/rfc5234>.

   [RFC8174]  Leiba, B., "Ambiguity of Uppercase vs Lowercase in
              RFC 2119 Key Words", BCP 14, RFC 8174,
              DOI 10.17487/RFC8174, May 2017,
              <https://www.rfc-editor.org/rfc/rfc8174>.

   [RFC3629]  Yergeau, F., "UTF-8, a transformation format of
              ISO 10646", STD 63, RFC 3629,
              DOI 10.17487/RFC3629, November 2003,
              <https://www.rfc-editor.org/rfc/rfc3629>.

   [PGDOC14]  The PostgreSQL Global Development Group, "PostgreSQL 14
              Documentation", 2021,
              <https://www.postgresql.org/docs/14/>.

## Informative References

   [ATLAS]    Ariga, "Atlas — Database Schema Management",
              <https://atlasgo.io/>.

   [PRISMA]   Prisma Data, "Prisma ORM — Schema reference",
              <https://www.prisma.io/docs/concepts/components/prisma-schema>.

   [FLYWAY]   Redgate, "Flyway — Database Migrations Made Easy",
              <https://flywaydb.org/>.

   [KAHN62]   Kahn, A.B., "Topological sorting of large networks",
              Communications of the ACM, 5(11), pp. 558–562, 1962.

   [TARJAN72] Tarjan, R., "Depth-first search and linear graph
              algorithms", SIAM Journal on Computing, 1(2),
              pp. 146–160, 1972.

   [NDJSON]   "Newline Delimited JSON", <http://ndjson.org/>.

---

## Author's Address

   Daniel Tsegaw
   Independent

   Email: danieltsegaw.b@gmail.com

---

*End of RFC 1 — Declarative PG (DPG) v0.8.1*
