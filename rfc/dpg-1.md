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
    18.4.  dpg dump
    18.5.  dpg diff
    18.6.  dpg validate
    18.7.  dpg fmt
    18.8.  dpg portability
    18.9.  dpg init
    18.10. dpg completion
    18.11. dpg docs
    18.12. Target Selection
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
   -   Data manipulation language (SELECT, INSERT, UPDATE, DELETE).
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
```

   The compiler MUST reject any key in any `dpg.toml` that is not
   listed in this section, Section 3.3, or Section 3.4 for that file's
   kind, with error DPG-E001 (unknown configuration key).

   `[fmt]` controls `dpg fmt` (Section 18.7).  `[migrations]` controls where
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
       `--database` selection (Section 18.12), snapshot files, the
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

   **Coverage:** the rule MUST check at least collation `REFRESH
   VERSION` and `RULES`, `PARAMETER PRIVILEGES`, the event trigger
   `login` event, and the `MAINTAIN` privilege.  The other minimum
   versions in Appendix F are informative: PostgreSQL itself rejects
   those constructs when the migration runs against an older server.

---

---

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

---

## 5. Type System

### 5.1. ENUM Types

   **PG equivalent:** `CREATE TYPE name AS ENUM ('v1', 'v2', ...)`

   Part 1 is PostgreSQL's own `TYPE name AS ENUM (...)` with the
   `CREATE` verb removed.  The optional `{ }` block holds the type's
   comment, owner, rename, value renames, and value-removal migrations.

```abnf
enum-decl      = "TYPE" WSP schema-name WSP "AS" WSP "ENUM" WSP
                 "(" enum-values ")" terminator
                 [ "{" enum-block "}" ]

enum-values    = enum-value *( "," WSP enum-value )
enum-value     = string-literal

enum-block     = *( enum-directive )

enum-directive = comment-dir ";"
               / owner-dir ";"
               / renamed-from-dir ";"
               / value-renamed-dir ";"
               / migrate-remove-dir

value-renamed-dir = "VALUE" WSP string-literal WSP
                    "RENAMED FROM" WSP string-literal
```

   [DPG] `value-renamed-dir` names the value's current label first and
   its previous label after `RENAMED FROM`, the same order as every
   other `RENAMED FROM` directive.  It is a `{ }` block directive
   because PostgreSQL's enum value list accepts only string literals.

   `OWNER` and `RENAMED FROM` behave as for every other type (Section
   5.2's change table): `ALTER TYPE name OWNER TO role` (`SAFE`), `ALTER
   TYPE old RENAME TO new` (`CAUTION`), and, for a schema-qualified old
   name, `ALTER TYPE old_schema.name SET SCHEMA new_schema` (`SAFE`,
   Section 7.6).

   Examples:

```sql
-- Minimal enum
TYPE user_status AS ENUM ('active', 'suspended', 'deleted');

-- Enum with a comment
TYPE invoice_status AS ENUM ('draft', 'sent', 'paid', 'void', 'overdue') {
    COMMENT 'Billing lifecycle states for customer invoices';
}

-- Enum with a value removal
TYPE order_status AS ENUM ('pending', 'confirmed', 'shipped', 'delivered') {
    COMMENT 'Order lifecycle states';
    MIGRATE REMOVE ('cancelled') {
        UPDATE orders SET status = 'delivered' WHERE status = 'cancelled';
    }
}

-- Enum with a value rename ('fulfilled' in the snapshot becomes 'delivered')
TYPE order_status AS ENUM ('pending', 'confirmed', 'shipped', 'delivered') {
    VALUE 'delivered' RENAMED FROM 'fulfilled';
}
```

#### 5.1.1. Adding and Renaming ENUM Values

   A value present in the source but absent from the snapshot emits:

```sql
ALTER TYPE <schema>.<name> ADD VALUE '<new_value>' [BEFORE | AFTER '<anchor>'];
```

   Safety `SAFE`.  It runs inside the migration's transaction, except
   in one case.  PostgreSQL rejects using an enum value inside the
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

   Removing a value from an ENUM is not directly supported by
   PostgreSQL.  The `MIGRATE REMOVE` directive provides a safe migration
   path.  The `MIGRATE REMOVE` body is DML that runs *before* the type
   is rebuilt to migrate existing data away from the removed value.

   When a value is absent from the DPG source but present in the
   snapshot, and a `MIGRATE REMOVE` directive covers it, the compiler
   MUST emit the following sequence, all within a single transaction:

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

#### 5.1.3. Reordering ENUM Values

   PostgreSQL ENUM values have a fixed ordering.  Reordering values
   is treated as a remove + add and classified as `DESTRUCTIVE` unless
   all affected values are brand new (not in the snapshot).

### 5.2. Composite Types

   Composite types declare a row-structured type with named, typed
   attributes.  The attribute list uses `( )`; Section 4.5 governs how
   the declaration ends.

   **PG equivalent:** `CREATE TYPE name AS (attr1 type1 [COLLATE collation], attr2 type2, ...)`

```abnf
composite-decl = "TYPE" WSP schema-name WSP "AS" WSP
                 "(" attribute-list ")"
                 [ ";" ]
                 [ "{" composite-block "}" ]

attribute-list = attribute *( "," WSP attribute )
attribute      = identifier WSP type-ref [ WSP "COLLATE" WSP collation-name ]

composite-block = *( composite-directive )
composite-directive = ( comment-dir / owner-dir / renamed-from-dir ) ";"
                    / attribute-block
                    / attributes-block

attribute-block  = "ATTRIBUTE" WSP identifier WSP "{" *attr-directive "}"
attributes-block = "ATTRIBUTES" WSP "{"
                     *( identifier WSP "{" *attr-directive "}" ) "}"
attr-directive   = ( comment-dir / "RENAMED FROM" WSP identifier ) ";"
```

   [DPG] `ATTRIBUTE`/`ATTRIBUTES` use PostgreSQL's own keyword for a
   composite type member (`ALTER TYPE ... ADD/DROP/ALTER/RENAME
   ATTRIBUTE`).  `COMMENT` emits `COMMENT ON COLUMN type.attr IS '...'`,
   PostgreSQL's statement for commenting on a composite type attribute.
   An `ATTRIBUTE` block naming an attribute absent from the `( )` list
   is error DPG-E018.  `COLUMN` in a composite type block, and
   `ATTRIBUTE` in any other block, is error DPG-E024.

   **`COLLATE`** is valid only on attributes whose type has a
   collatable base type (`text`, `varchar`, etc.) — DPG performs no
   validation of this itself, left to PostgreSQL's own parser (same
   passthrough principle used throughout this document).

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
    }
}
```

   **Diffing semantics:**

   -   Adding an attribute: `ALTER TYPE <name> ADD ATTRIBUTE <col> <type>` — `SAFE`.
   -   Dropping an attribute: `ALTER TYPE <name> DROP ATTRIBUTE <col>` — `DESTRUCTIVE`.
   -   Changing an attribute type or `COLLATE`: `ALTER TYPE <name> ALTER ATTRIBUTE <col> TYPE <new> [COLLATE collation]` — `DESTRUCTIVE`.
   -   Renaming an attribute: `ATTRIBUTE new_name { RENAMED FROM old_name; }` —
       resolved like table column renames (Section 7.6) — emits
       `ALTER TYPE <name> RENAME ATTRIBUTE old_name TO new_name` — `SAFE`.
   -   Attribute comment changed: `COMMENT ON COLUMN <name>.<attr> IS '...'` (or `IS NULL`) — `SAFE`.
   -   Owner changed: `ALTER TYPE <name> OWNER TO role` — `SAFE`.
   -   Renamed (`RENAMED FROM` on the type itself, Section 5.1's cross-reference): `ALTER TYPE old RENAME TO new` — `CAUTION`; schema-qualified additionally emits `ALTER TYPE old_schema.name SET SCHEMA new_schema` — `SAFE`.

### 5.3. Range Types

   Range types use two `( )` groups: the first is the keyword `AS RANGE`
   following the type name; the body is the options list.

   **PG equivalent:** `CREATE TYPE name AS RANGE (options)`

```abnf
range-decl = "TYPE" WSP schema-name WSP "AS RANGE" WSP
             "(" range-options ")"
             [ ";" ]
             [ "{" range-block "}" ]

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

   **Diffing semantics:** Any change to a range type's options requires
   `DROP TYPE CASCADE` followed by `CREATE TYPE`.  This is classified
   as `DESTRUCTIVE`.  `OWNER`/`RENAMED FROM` follow the standard rules
   (Section 5.1): `ALTER TYPE name OWNER TO role` (`SAFE`); `ALTER TYPE old
   RENAME TO new` (`CAUTION`), plus `SET SCHEMA` when schema-qualified
   (`SAFE`).

   **Multirange types:** PostgreSQL creates a multirange companion
   type with every range type and drops it with the range; DPG never
   declares it separately.  Its name is PostgreSQL's default, the
   range's name with its first `range` replaced by `multirange`
   (`floatrange` gives `floatmultirange`), or with `_multirange`
   appended when the name contains no `range` (`price_span` gives
   `price_span_multirange`).  To choose another name, set
   `MULTIRANGE_TYPE_NAME = name` in Part 1's option list.

   [DPG] The multirange name is diffed on its own: when it is the only
   thing that changed, the compiler emits `ALTER TYPE
   <schema>.<old_multirange> RENAME TO <new_multirange>` (`CAUTION`)
   instead of rebuilding the range type.

### 5.4. Domain Types

   Domains add a default and constraints to an existing base type.
   The base type appears after `AS`.  `DEFAULT` and constraints are
   native `CREATE DOMAIN` clauses, so they appear in Part 1 exactly as
   PostgreSQL writes them (Tenet 5).  `ALTER DOMAIN ... SET DEFAULT`/
   `ADD CONSTRAINT` are how an existing domain is changed, which the
   compiler emits from the diff.

   **PG equivalent:**
   `CREATE DOMAIN name AS base_type [COLLATE collation] [DEFAULT expr] [[CONSTRAINT name] {NOT NULL | NULL | CHECK (expr) [NOT VALID]}] ...`

```abnf
domain-decl = "DOMAIN" WSP schema-name WSP "AS" WSP type-name
              [ WSP "COLLATE" WSP collation-name ]
              [ WSP "DEFAULT" WSP expr ]
              *( WSP domain-constraint )
              ";"
              [ "{" domain-block "}" ]

domain-constraint = [ "CONSTRAINT" WSP identifier WSP ]
                     ( "NOT NULL" / "NULL" / "CHECK" WSP "(" expr ")" [ WSP "NOT VALID" ] )
                     [ WSP "RENAMED FROM" WSP identifier ]

domain-block = *( ( comment-dir / owner-dir / renamed-from-dir
                   / grants-block / revocations-block ) ";" )
```

   The `{ }` block holds only what has no place in native `CREATE
   DOMAIN` syntax: `COMMENT`, `OWNER`, `RENAMED FROM` (Section 7.6's generic
   cross-schema `SET SCHEMA` extension applies here identically),
   `GRANTS`, `REVOKES` — the same directive set Tenet 5 already
   sanctions for every other object kind.

   **`COLLATE`** on the domain itself is valid only when the base type
   is collatable — left to PostgreSQL's own parser to enforce, same
   passthrough principle as everywhere else in this document.

   **`NOT VALID`:** Real PostgreSQL's `CREATE DOMAIN` grammar has no
   `NOT VALID` option — it exists only on `ALTER DOMAIN ... ADD
   CONSTRAINT`. `domain-constraint`'s `NOT VALID` is therefore only
   meaningful when the differ is *adding a constraint to an
   already-existing domain* (the domain itself present in the
   snapshot, the named constraint absent): that case emits `ALTER
   DOMAIN <name> ADD CONSTRAINT <name> CHECK (...) NOT VALID` —
   `CAUTION`.  When the whole `CREATE DOMAIN` statement itself is being
   emitted for a brand-new domain, `NOT VALID` on any of its
   constraints is silently omitted from that statement (there is
   nothing yet to skip validating) — this mirrors the "no separate
   opt-in step needed at creation" logic table constraints already
   follow, without needing table's placement restriction (Section 7.3), since
   Domain has no `( )`-list-vs-`{ }`-block distinction to enforce here.

   **`VALIDATE CONSTRAINT`:** When `NOT VALID` is removed from an
   existing constraint in source, the compiler emits `ALTER DOMAIN
   <name> VALIDATE CONSTRAINT <name>` — `CAUTION` — the same two-step
   lifecycle as table constraints (Section 7.3).

   **`RENAMED FROM` on a constraint** emits `ALTER DOMAIN <name> RENAME
   CONSTRAINT old TO new` — `SAFE` — instead of the drop-and-recreate a
   name-only difference would otherwise trigger under name-based
   constraint identity.

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

   -   Adding a `DEFAULT`: `ALTER DOMAIN <name> SET DEFAULT <expr>` — `SAFE`.
   -   Dropping a `DEFAULT`: `ALTER DOMAIN <name> DROP DEFAULT` — `SAFE`.
   -   Adding a constraint: `ALTER DOMAIN <name> ADD CONSTRAINT <name> CHECK (...) [NOT VALID]` — `CAUTION`.
   -   `NOT VALID` removed from an existing constraint: `ALTER DOMAIN <name> VALIDATE CONSTRAINT <name>` — `CAUTION`.
   -   Constraint renamed only (`RENAMED FROM`): `ALTER DOMAIN <name> RENAME CONSTRAINT old TO new` — `SAFE`.
   -   Dropping a constraint: `ALTER DOMAIN <name> DROP CONSTRAINT <name>` — `SAFE`.
   -   Changing the base type or `COLLATE`: requires `DROP DOMAIN CASCADE` + `CREATE DOMAIN` — `DESTRUCTIVE`.
   -   Owner changed: `ALTER DOMAIN <name> OWNER TO role` — `SAFE`.
   -   Renamed (`RENAMED FROM` on the domain itself): `ALTER DOMAIN old RENAME TO new` — `CAUTION`; schema-qualified additionally emits `ALTER DOMAIN old_schema.name SET SCHEMA new_schema` — `SAFE`.

### 5.5. Base (Shell) Types

   Base types implement a custom storage type using C-defined input and
   output functions.  The body is the PostgreSQL `CREATE TYPE` options
   list.  New base types are `SAFE`; removal is `DESTRUCTIVE`.  A
   property *change* is diffed per-key rather than as a single text
   hash (see below), since real PostgreSQL's `ALTER TYPE ... SET (...)`
   can update a specific subset of properties in place.

   **PG equivalent:** `CREATE TYPE name (INPUT = func, OUTPUT = func, ...)` — or, forward-declared, bare `CREATE TYPE name;`

```abnf
base-type-decl = "TYPE" WSP schema-name
                 ( WSP "(" storage-params ")" [ ";" ]
                 / ";" )
                 [ "{" base-type-block "}" ]

base-type-block = *( ( comment-dir / owner-dir / renamed-from-dir ) ";" )
```

   **Bare forward-declaration shell:** `TYPE name;` with no option list
   and no support functions is a complete, standalone declaration — real
   PostgreSQL's "shell type," a placeholder with only a name and an
   owner.  This is the first half of the standard workflow for a
   self-referential base type: the type's own `INPUT`/`OUTPUT`
   functions (Section 9, `LANGUAGE C`) must declare an argument or return type
   of `mytype` before `mytype` itself has a full definition, which is
   impossible unless the shell already exists.

   **Automatic cycle-breaking:** When a full-option `base-type-decl`
   (e.g. `TYPE mytype (INPUT = mytype_in, OUTPUT = mytype_out, ...)`) is
   new and its `INPUT`/`OUTPUT`/etc. functions are *also* new and
   themselves reference `mytype` in their own argument or return type
   (Section 9, e.g. `FUNCTION mytype_in(cstring) RETURNS mytype ...`), the
   dependency graph (Section 22) detects the resulting circular reference the
   same way it already detects circular `DEFERRABLE` foreign keys
   (Section 22.2) — a distinct cycle *kind*, broken by a distinct mechanism
   specific to this case rather than Section 22.2's FK-specific algorithm:
   the compiler simply omits the usual Function-depends-on-its-param/
   return-type ordering edge for this specific case, so the `CREATE
   FUNCTION` statements that reference `mytype` get emitted *before* the
   full `CREATE TYPE mytype (INPUT = ..., OUTPUT = ..., ...)`, with no
   separate bare-shell statement.  PostgreSQL itself creates the shell
   type when a `LANGUAGE internal`/`LANGUAGE c` function references a
   not-yet-existing type, and the full `CREATE TYPE` then replaces that
   shell.  A user writing a self-referential base type declares only
   the single full-option form in source.

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

   **Property-change diffing:** Real PostgreSQL's `ALTER TYPE name SET
   (...)` can update exactly seven support-function/storage properties
   in place, without a rebuild: `RECEIVE`, `SEND`, `TYPMOD_IN`,
   `TYPMOD_OUT`, `ANALYZE`, `SUBSCRIPT` (each settable to a function
   name or `NONE`), and `STORAGE` (a storage mode, not a function —
   note that changing it only affects columns created *after* the
   change, not existing ones, a PostgreSQL-level caveat, not a DPG one).
   Every other property (`INPUT`, `OUTPUT`, `INTERNALLENGTH`,
   `ALIGNMENT`, `PASSEDBYVALUE`, `CATEGORY`, `PREFERRED`, `DEFAULT`,
   `ELEMENT`, `DELIMITER`, `COLLATABLE`, `LIKE`) is fixed at creation
   with no `ALTER TYPE` equivalent at all.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Only `RECEIVE`/`SEND`/`TYPMOD_IN`/`TYPMOD_OUT`/`ANALYZE`/`SUBSCRIPT`/`STORAGE` differ | `ALTER TYPE name SET (key = value \| NONE, ...)` | `SAFE` |
   | Any other property differs (alone or alongside the above) | `DROP TYPE CASCADE` + `CREATE TYPE` | `DESTRUCTIVE` |
   | Type removed | `DROP TYPE name [CASCADE]` | `DESTRUCTIVE` |

   `OWNER`/`RENAMED FROM` follow the standard rules (Section 5.1): `ALTER TYPE
   name OWNER TO role` (`SAFE`); `ALTER TYPE old RENAME TO new`
   (`CAUTION`), plus `SET SCHEMA` when schema-qualified (`SAFE`).

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
       VIRTUAL TYPE named_point AS point;        -- references virtual type "point"
       VIRTUAL TYPE tags AS text[];              -- array form
       ```

   -   **Composite** — an inline record with named, typed fields.
       Field types MAY themselves be virtual type references:

       ```sql
       VIRTUAL TYPE point AS (x float8, y float8);

       VIRTUAL TYPE line_item AS (
           sku      text,
           quantity integer,
           price    numeric
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

   Adding `[]` to the reference signals a JSON array column and causes
   DPG to emit `jsonb[]` instead of `jsonb`.

   **Rules:**

   -   `VIRTUAL TYPE` MAY be schema-qualified; if unqualified it
       defaults to `default_schema`.

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

   Schemas have no `( )` list.  Their `{ }` block directly holds all
   schema-level attributes and nested objects.

   **PG equivalent:** `CREATE SCHEMA [IF NOT EXISTS] name [AUTHORIZATION role]`

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
SCHEMA public {
    -- Objects in the public schema declared inline here
}

SCHEMA analytics {
    OWNER analytics_role;
    COMMENT 'Derived tables and event aggregations';

    TABLE events ( ... ) { ... }
    FUNCTION compute_daily() RETURNS VOID LANGUAGE plpgsql AS $$ ... $$;
}
```

   **Renaming:**

```sql
SCHEMA reporting {
    RENAMED FROM old_reporting;
}
```

   Emits: `ALTER SCHEMA old_reporting RENAME TO reporting;` — Safety `CAUTION`.

   **`public` schema default privileges (PostgreSQL 15+):** PostgreSQL
   15 changed a fresh cluster's `public` schema to no longer grant
   `CREATE` to `PUBLIC` by default (versions before 15 did) — a
   behavioral default, not a DPG grammar concern, but worth stating
   explicitly per Tenet 3: a project targeting a pre-15 server may find
   `public`'s live privileges differ from what a project first
   introspected against 15+ would show, through no DPG action of its
   own. DPG does not special-case this — a declared `GRANTS { CREATE TO
   PUBLIC; }` on `public` (Sections 7.10-style grants, applied to the schema)
   reproduces the pre-15 default explicitly and portably regardless of
   server version, the same way any other declared grant does.

   **`RENAMED FROM` with no other content** is valid:

```sql
SCHEMA new_name {
    RENAMED FROM old_name;
}
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New schema | `CREATE SCHEMA name` | `SAFE` |
   | RENAMED FROM | `ALTER SCHEMA old RENAME TO new` | `CAUTION` |
   | OWNER change | `ALTER SCHEMA name OWNER TO role` | `SAFE` |
   | COMMENT change | `COMMENT ON SCHEMA name IS '...'` | `SAFE` |
   | Schema removed | `DROP SCHEMA name [RESTRICT\|CASCADE]` | `DESTRUCTIVE` |

### 6.2. EXTENSION

   Extensions are database-level objects and MUST be declared in a
   database `.dpg` file (not in the cluster objects directory).

   **PG equivalent:**
   `CREATE EXTENSION [IF NOT EXISTS] name [SCHEMA schema] [VERSION version] [CASCADE]`

```abnf
extension-decl = "EXTENSION" WSP identifier
                 [ WSP "SCHEMA" WSP identifier ]
                 [ WSP "VERSION" WSP SQUOTE version SQUOTE ]
                 [ WSP "CASCADE" ]
                 ";"
```

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
   | VERSION change | `ALTER EXTENSION name UPDATE TO version` | `CAUTION` |
   | SCHEMA change | `ALTER EXTENSION name SET SCHEMA new_schema` | `SAFE` |
   | Extension removed | `DROP EXTENSION name [CASCADE]` | `DESTRUCTIVE` |

   `SCHEMA` changes use `ALTER EXTENSION ... SET SCHEMA` rather than
   drop-and-recreate — real PostgreSQL supports moving any *relocatable*
   extension this way without dropping it (most extensions are
   relocatable; a handful, e.g. ones with hardcoded schema-qualified
   references in their own SQL objects, are not — PostgreSQL itself
   rejects the statement for those, left to its own error per the
   passthrough-validation principle used throughout this document).

---

---

## 7. Tables

### 7.1. Table Declaration Syntax

   Tables are the most syntactically rich object type in DPG.  The
   complete grammar is:

```abnf
table-decl  = [ "UNLOGGED" WSP ] "TABLE" WSP schema-table-name WSP
              ( "(" column-list ")"
              / "OF" WSP type-ref [ WSP "(" typed-column-list ")" ] )
              *( table-clause )
              [ ";" ]
              [ "{" table-block "}" ]
            / "FOREIGN TABLE" WSP schema-table-name WSP
              "(" column-list ")"
              *( table-clause )
              WSP "SERVER" WSP identifier
              [ WSP "OPTIONS" WSP "(" option-list ")" ]
              [ ";" ]
              [ "{" table-block "}" ]

column-list = column-item *( "," WSP column-item )
column-item = column-def / table-constraint / like-clause

; CREATE TABLE ... (LIKE source_table [{INCLUDING|EXCLUDING} ...]) — a
; column-list item copying another table's column definitions (and,
; selectively, its constraints/indexes/defaults/etc.) rather than
; restating them. May appear anywhere column-def/table-constraint may,
; including multiple times and mixed with either.
like-clause  = "LIKE" WSP table-ref *( WSP like-option )
like-option  = ( "INCLUDING" / "EXCLUDING" ) WSP like-attribute
like-attribute = "COMMENTS" / "COMPRESSION" / "CONSTRAINTS" / "DEFAULTS"
               / "GENERATED" / "IDENTITY" / "INDEXES" / "STATISTICS"
               / "STORAGE" / "ALL"

; CREATE TABLE ... OF type_name — "Form 2" typed tables, backing a
; previously-declared composite type (Section 5.2) instead of an independent
; column list. The optional parenthesized list may only narrow
; constraints on the type's existing attributes (WITH OPTIONS) or add
; table-level constraints — it cannot add new columns, matching real
; PostgreSQL's own restriction.
typed-column-list = typed-column-item *( "," WSP typed-column-item )
typed-column-item = col-name WSP "WITH OPTIONS" WSP *( col-constraint )
                   / table-constraint

table-clause = WITH "(" storage-params ")"
             / TABLESPACE identifier
             / INHERITS "(" table-ref-list ")"
             / partition-by-clause
             / "USING" WSP method

table-block  = *( table-directive )

table-directive = owner-dir
                / comment-dir
                / renamed-from-dir
                / detached-from-dir
                / protected-dir
                / deprecated-dir
                / drop-cascade-dir
                / rls-enable-dir
                / rls-force-dir
                / replica-identity-dir
                / cluster-dir
                / column-block
                / indices-block
                / policies-block
                / triggers-block
                / grants-block
                / revocations-block
                / constraint-dir
                / partitions-block
```

   **`LIKE source_table`:** May appear anywhere inside the `( )` list,
   mixed freely with ordinary column definitions and table constraints,
   including more than once.  Each `{INCLUDING|EXCLUDING} <attribute>`
   pair is emitted verbatim into the generated `CREATE TABLE` in
   declaration order, matching real PostgreSQL's own left-to-right
   `LIKE` option semantics (a later option for the same attribute wins).
   Omitting all `{INCLUDING|EXCLUDING}` clauses copies only column names
   and types, PostgreSQL's own default.  Diffing treats a table declared
   via `LIKE` no differently from one with an equivalent explicit column
   list — the snapshot records the *resolved* columns/constraints, not
   the `LIKE` clause itself, since `source_table` may change or
   disappear independently after the copy is made.

```sql
TABLE order_items_archive (
    LIKE order_items INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

   **`OF type_name` (typed tables):** Backs the table's row type with a
   previously-declared composite type (Section 5.2) instead of an independent
   column list.  `WITH OPTIONS` narrows a constraint (`NOT NULL`,
   `DEFAULT`, etc.) on one of the type's existing attributes — real
   PostgreSQL rejects any attempt to introduce a *new* column this way,
   and DPG does not attempt to detect that error itself; it is left to
   PostgreSQL's own parser (per the passthrough principle already
   applied to every other clause-combination rule in this document, see
   e.g. Section 7.9's `REFERENCING` note).  Adding, changing, or removing the
   `OF type_name` association after creation is an `ALTER`-side
   operation — see Section 7.11's `OF type_name`/`NOT OF` entry.

```sql
TYPE address AS (street TEXT, city TEXT, zip TEXT) {}

TABLE shipping_address OF address (
    street WITH OPTIONS NOT NULL,
    CONSTRAINT zip_format CHECK (zip ~ '^[0-9]{5}$')
);
```

   **`USING method` (table access method):** Selects the storage access
   method (e.g. a columnar extension's method, once installed via
   `CREATE EXTENSION`) instead of the cluster's default.  Positioned
   among the other `table-clause` alternatives, matching real
   PostgreSQL's clause ordering after the column list / `OF type_name`
   and before `WITH (...)`/`TABLESPACE`.  Changing `USING` post-creation
   is a separate, ALTER-side concern — see Section 21's `SET ACCESS METHOD` row.

### 7.2. Column Definitions

   Column definitions appear inside the `( )` list and follow
   PostgreSQL's `CREATE TABLE` column syntax exactly. `type-ref` also
   accepts the `SERIAL`/`BIGSERIAL`/`SMALLSERIAL` pseudo-type spellings;
   see Appendix D.3 for their normalization and diffing behavior.

```abnf
column-def  = col-name WSP type-ref
              *( col-constraint )

col-constraint = "NOT NULL" [ WSP "NO INHERIT" ]
               / "NULL"
               / "DEFAULT" WSP expr
               / "GENERATED ALWAYS AS" WSP "(" expr ")" WSP ( "STORED" / "VIRTUAL" )
               / "GENERATED ALWAYS AS IDENTITY" [ identity-opts ]
               / "GENERATED BY DEFAULT AS IDENTITY" [ identity-opts ]
               / "PRIMARY KEY" [ conflict-clause ]
               / "UNIQUE" [ nulls-distinct ] [ conflict-clause ]
               / "REFERENCES" WSP table-ref [ ref-opts ] [ WSP enforced-clause ]
               / "CHECK" WSP "(" expr ")" [ no-inherit ] [ WSP enforced-clause ]
               / "CONSTRAINT" WSP identifier WSP col-constraint
               / "COMPRESSION" WSP method
               / "COLLATE" WSP collation-name

identity-opts = "(" "START WITH" int
                    [ "INCREMENT BY" int ]
                    [ "MINVALUE" int / "NO MINVALUE" ]
                    [ "MAXVALUE" int / "NO MAXVALUE" ]
                    [ "CACHE" int ]
                    [ "CYCLE" / "NO CYCLE" ]
                ")"

ref-opts      = [ "MATCH FULL" / "MATCH PARTIAL" / "MATCH SIMPLE" ]
                [ "ON DELETE" ref-action ]
                [ "ON UPDATE" ref-action ]

ref-action    = "NO ACTION" / "RESTRICT" / "CASCADE" /
                "SET NULL" [ WSP "(" col-list ")" ] /
                "SET DEFAULT" [ WSP "(" col-list ")" ]

; PostgreSQL 18+: CHECK/FOREIGN KEY constraints may be declared
; metadata-only (validated by application logic instead of PostgreSQL
; itself) — a real Tenet-1 gap distinct from NOT VALID (which still
; enforces the constraint once validated; NOT ENFORCED never enforces
; it at all).
enforced-clause = "ENFORCED" / "NOT ENFORCED"
```

   **`VIRTUAL` generated columns** (PostgreSQL 18+): computed on read
   rather than stored — the alternative to the existing `STORED` form.
   Diffed per Section 7.4's generated-column diffing table — the
   `STORED`/`VIRTUAL` keyword itself is part of the generated-column
   identity the differ compares, so switching between them is a change
   like any other, but real PostgreSQL's own `ALTER` surface is narrower
   for `VIRTUAL` than for `STORED`: there is no in-place path for adding
   generation to a plain column or for switching `STORED`↔`VIRTUAL`
   (both always drop-and-recreate the column, regardless of kind), `SET
   EXPRESSION` works identically for either kind, and `DROP EXPRESSION`
   — unlike `STORED` — is rejected outright for `VIRTUAL` (see Section
   7.4's table for the exact transitions and DDL).

   [PG] `NOT NULL ... NO INHERIT` (inline, or as a named table-level
   constraint, Section 7.3), `ENFORCED`/`NOT ENFORCED` on `CHECK` and
   `FOREIGN KEY` (PostgreSQL 18+), and `ON DELETE`/`ON UPDATE SET
   NULL`/`SET DEFAULT (col-list)` (PostgreSQL 15+) are PostgreSQL's own
   clauses, passed through unchanged and diffed as part of the column or
   constraint definition.

   **PRIMARY KEY implies NOT NULL:** PostgreSQL enforces that every
   PRIMARY KEY column is implicitly NOT NULL.  The DPG compiler MUST
   apply the same inference:

   -   Writing `NOT NULL` on a PRIMARY KEY column in the source is
       accepted but is silently treated as redundant.
   -   The compiler MUST NOT emit a redundant `NOT NULL` clause for
       PRIMARY KEY columns in generated `CREATE TABLE` DDL.
   -   The compiler MUST NOT emit a spurious `ALTER COLUMN SET NOT NULL`
       when diffing a PRIMARY KEY column.

   **Inline vs. table-level constraints:** Both forms are accepted and
   treated as semantically equivalent.  The compiler MUST normalise
   single-column `PRIMARY KEY`, `UNIQUE`, `CHECK`, and `REFERENCES`
   constraints to the inline form in its emitted `CREATE TABLE`.
   Multi-column constraints MUST remain table-level.  Named
   single-column constraints (e.g. `CONSTRAINT pk_x PRIMARY KEY`) are
   emitted inline in the `CREATE TABLE` output.

   Examples:

```sql
-- Inline form (preferred for single-column constraints)
TABLE accounts (
    id   UUID    NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    slug TEXT    NOT NULL UNIQUE,
    org  UUID    NOT NULL REFERENCES organisations (id) ON DELETE CASCADE
);

-- Named inline constraint
TABLE accounts (
    id UUID NOT NULL DEFAULT gen_random_uuid()
        CONSTRAINT pk_accounts PRIMARY KEY
);

-- Table-level (required for multi-column constraints)
TABLE order_items (
    order_id   BIGINT NOT NULL,
    product_id BIGINT NOT NULL,
    CONSTRAINT pk_order_items PRIMARY KEY (order_id, product_id)
);
```

   **Column type change diffing:**

   -   A type change that PostgreSQL can apply implicitly
       (e.g., `VARCHAR(10)` → `VARCHAR(20)`) is classified `CAUTION`.
   -   A type change requiring an explicit cast is classified
       `DESTRUCTIVE` unless a `USING` expression is present in the
       `COLUMN name { USING expr; }` block, in which case it is
       classified `CAUTION`.
   -   The `ALTER TABLE <t> ALTER COLUMN <c> TYPE <new> [USING <expr>]`
       statement acquires an `ACCESS EXCLUSIVE` lock for the duration
       of the rewrite, which is why it is always at least `CAUTION`.

   [DPG] **Which type changes are `CAUTION`.**  A column type change
   without `USING` is `CAUTION` when PostgreSQL converts the stored
   values without loss, and `DESTRUCTIVE` otherwise:

   -   Between two different types: `CAUTION` when PostgreSQL has an
       implicit cast between them (`pg_cast.castcontext = 'i'`, for
       example `smallint` to `integer`).
   -   Within one type, a widened modifier:

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

   Table constraints may appear in the `( )` list, in the `{ }` block,
   or in both.  The compiler identifies constraints by name and merges
   them into a single logical set.  Same name + same definition =
   deduplicated.  Same name + different definition = DPG-E005.

```abnf
table-constraint = "CONSTRAINT" WSP identifier WSP table-constraint-body
                   [ "NOT VALID" ]
                   [ WSP enforced-clause ]
                   [ "DEFERRABLE" [ "INITIALLY DEFERRED" / "INITIALLY IMMEDIATE" ] ]
                   [ WSP "RENAMED FROM" WSP identifier ]

table-constraint-body
    = "PRIMARY KEY" WSP "(" col-list [ "," WSP col-name WSP "WITHOUT OVERLAPS" ] ")"
    / "UNIQUE" [ "NULLS NOT DISTINCT" ] WSP "(" col-list [ "," WSP col-name WSP "WITHOUT OVERLAPS" ] ")"
    / "CHECK" WSP "(" expr ")" [ "NO INHERIT" ]
    / "FOREIGN KEY" WSP "(" col-list [ "," WSP "PERIOD" WSP col-name ] ")" WSP
      "REFERENCES" WSP table-ref WSP "(" col-list [ "," WSP "PERIOD" WSP col-name ] ")" ref-opts
    / "EXCLUDE" WSP "USING" WSP method WSP "(" excl-list ")"
      [ "WHERE" "(" expr ")" ]
    / "NOT NULL" WSP col-name [ WSP "NO INHERIT" ]
```

   **`NOT VALID` lifecycle:**

   1.  First migration: `ALTER TABLE <t> ADD CONSTRAINT <name> ... NOT VALID;`
       — Safety `CAUTION`.

   2.  When `NOT VALID` is removed in source, the compiler emits:
       `ALTER TABLE <t> VALIDATE CONSTRAINT <name>;` — Safety `CAUTION`.

   3.  After validation, the constraint MAY be moved to the `( )` list.
       The compiler identifies it by name and treats it as already
       existing (no new DDL emitted).

   **`NOT VALID` placement:** `NOT VALID` MAY be written inline in the
   `( )` list (PostgreSQL's `CREATE TABLE` accepts it on `CHECK`,
   `FOREIGN KEY`, and, from PostgreSQL 18, table-level `NOT NULL`
   constraints) or on a `{ }` block `CONSTRAINT` entry.  The two serve
   different situations: a new table is created with its constraints in
   the `( )` list, while a constraint added to an existing table needs
   `ALTER TABLE ... ADD CONSTRAINT`, which the `{ }` block form
   describes declaratively.

   **DEFERRABLE FK cycles:** When two tables have a circular foreign
   key dependency and both FKs are `DEFERRABLE`, the compiler emits
   both `CREATE TABLE` statements first, then the circular FK as a
   subsequent `ALTER TABLE ADD CONSTRAINT ... DEFERRABLE`.  If a cycle
   exists with no `DEFERRABLE` FK, the compiler emits error DPG-E017
   with the full dependency cycle listed.

   **`RENAMED FROM` on a constraint** (like index `RENAMED FROM`, Section 7.7)
   emits `ALTER TABLE t RENAME CONSTRAINT old TO new` — `SAFE`,
   metadata-only — instead of the drop-and-recreate that a name-only
   difference would otherwise trigger under name-based constraint
   identity (Section 7.3's opening paragraph).  MUST appear in the `{ }`
   block — unlike `NOT VALID` above, there is no `CREATE TABLE`-native
   form of a rename at all (`RENAME CONSTRAINT` is inherently an
   `ALTER TABLE`-only concept), so the `{ }` block is its only possible
   placement, not a choice between two accepted forms.

   **Deferrability-only changes:** When an existing `FOREIGN KEY`
   constraint's `DEFERRABLE`/`INITIALLY DEFERRED`/`INITIALLY IMMEDIATE`
   flags differ from the snapshot but every other part of the
   constraint (columns, `REFERENCES` target, `ref-opts`) is unchanged,
   the compiler emits `ALTER TABLE t ALTER CONSTRAINT name [[NOT]
   DEFERRABLE] [INITIALLY DEFERRED|IMMEDIATE]` — `SAFE` — instead of
   drop + re-add.  Real PostgreSQL's `ALTER CONSTRAINT` supports only
   this narrow deferrability-timing change and only for `FOREIGN KEY`
   constraints; any other simultaneous change, or a non-FK constraint,
   falls back to the general "constraint changed" drop-and-recreate row
   below.

   **`NOT NULL` constraint inheritability (PostgreSQL 18+):** When an
   existing table-level `NOT NULL` constraint's `NO INHERIT` flag
   differs from the snapshot but the column it applies to is unchanged,
   the compiler emits `ALTER TABLE t ALTER CONSTRAINT name [NO]
   INHERIT` — `SAFE` — instead of drop + re-add.  This is a separate,
   distinctly-named `ALTER CONSTRAINT` variant from the FK deferrability
   one above: real PostgreSQL's `ALTER CONSTRAINT` grammar is
   constraint-kind-specific (deferrability for FK, inheritability for
   `NOT NULL`), not one shared option set.

   **Table-level `NOT NULL` (PostgreSQL 18+):** `NOT NULL` became a
   proper named, catalogued constraint in PostgreSQL 18 — `CONSTRAINT
   name NOT NULL col_name [NO INHERIT] [NOT VALID]` is now addable via
   `ALTER TABLE ... ADD CONSTRAINT` the same way `CHECK` already is,
   including a `NOT VALID`/`VALIDATE CONSTRAINT` lifecycle identical to
   Section 7.3's existing one — retroactively marking an *existing* `NOT NULL`
   column's constraint `NOT VALID` (skipping the immediate full-table
   scan) was not possible before PostgreSQL 18 at all.  This is
   additive to the inline column-level `NOT NULL [NO INHERIT]` form
   (Section 7.2) real PostgreSQL has supported since 18 as well — both forms
   describe the same underlying named constraint; DPG does not require
   either one specifically.

   **Temporal keys (`WITHOUT OVERLAPS`/`PERIOD`, PostgreSQL 18+):** [PG]
   A trailing `col_name WITHOUT OVERLAPS` on `PRIMARY KEY`/`UNIQUE`, and
   a trailing `PERIOD col_name` in a `FOREIGN KEY`'s two column lists,
   name an existing range or multirange column as the temporal key.
   [DPG] The constraint is diffed as the `PRIMARY KEY`/`UNIQUE`/`FOREIGN
   KEY` it is catalogued as.  Non-range key columns need the
   `btree_gist` extension, which PostgreSQL itself requires:

```sql
TABLE room_bookings (
    room_id  BIGINT NOT NULL,
    valid_at DATERANGE NOT NULL,
    CONSTRAINT no_double_booking
        PRIMARY KEY (room_id, valid_at WITHOUT OVERLAPS)
);
```


   **Constraints kept on partitions (PostgreSQL 18+):** [PG] `ALTER
   TABLE ONLY parent DROP CONSTRAINT name` drops a constraint from a
   partitioned parent while keeping it on every existing partition,
   where it becomes a local constraint.  [DPG] To declare that, a
   partition's `PARTITIONS { }` entry MAY carry its own trailing `{ }`
   body declaring constraints independently of the parent, with the
   same `CONSTRAINT`/`CONSTRAINTS` directives a table's block accepts:

```abnf
partition-decl       =/ ... [ WSP "{" partition-block-body "}" ]
partition-block-body = *( ( "PARTITIONS" WSP "{" *partition-decl "}" )
                         / constraint-decl )       -- CONSTRAINT/CONSTRAINTS
```

   `PARTITIONS { }` sub-partitioning (Section 7.13) and `CONSTRAINT`/
   `CONSTRAINTS` entries may appear together in this body, in either
   order.  When a constraint disappears from the parent's declaration
   while a direct partition declares a constraint of the same name, the
   compiler emits `ALTER TABLE ONLY parent DROP CONSTRAINT name`
   (`SAFE`: catalog-only, and the constraint keeps enforcing wherever it
   applies) instead of the ordinary cascading drop.  Otherwise a
   partition's own constraint is added and dropped with ordinary `ALTER
   TABLE partition ADD`/`DROP CONSTRAINT`.

   Rules and limits:

   -   This is the only structure a partition declares independently of
       its parent; columns and other attributes always come from the
       parent.  It does not apply to an `ATTACHED FROM` entry, whose
       constraints come from its own standalone `TABLE` declaration.
   -   `ONLY` demotes the constraint on every existing partition at once.
       Keeping it on only some partitions takes two migrations: the
       `ONLY` drop, then ordinary drops on the partitions that should not
       keep it.
   -   Only direct partitions are matched against the parent; deeper
       sub-partitions use ordinary, independent constraint handling.

   Examples:

```sql
TABLE orders (
    id         BIGINT GENERATED ALWAYS AS IDENTITY,
    account_id BIGINT NOT NULL,
    amount     NUMERIC(10,2) NOT NULL,
    CONSTRAINT pk_orders PRIMARY KEY (id)
)
{
    -- added to an existing table: validated later (see the lifecycle above)
    CONSTRAINT ck_amount_positive CHECK (amount > 0) NOT VALID;

    CONSTRAINT fk_account FOREIGN KEY (account_id)
        REFERENCES accounts (id)
        ON DELETE CASCADE
        DEFERRABLE INITIALLY DEFERRED;
}
```

### 7.4. The COLUMN Reference Block

   Inside a table's `{ }` block, `COLUMN name { ... }` references an
   existing column declared in the `( )` list and holds attributes
   that PostgreSQL expresses as separate `ALTER TABLE ... ALTER COLUMN`
   statements.

```abnf
column-block    = "COLUMN" WSP col-name WSP "{" col-block-body "}"
               /  "COLUMNS" WSP "{" *col-named-block "}"

col-named-block = col-name WSP "{" col-block-body "}"

col-block-body  = *( col-block-directive ";" )

col-block-directive
    = "COMMENT" WSP SQUOTE text SQUOTE
    / "STATISTICS" WSP ( integer / "DEFAULT" )
    / "COMPRESSION" WSP method
    / "STORAGE" WSP storage-type
    / "DEPRECATED" WSP SQUOTE text SQUOTE
    / "USING" WSP expr
    / "RENAMED FROM" WSP col-name
    / grants-block
    / revocations-block
```

   The complete attribute table:

   | Directive | PostgreSQL DDL emitted |
   |-----------|------------------------|
   | `COMMENT 'text'` | `COMMENT ON COLUMN t.c IS '...'` |
   | `STATISTICS n` | `ALTER TABLE t ALTER COLUMN c SET STATISTICS n` |
   | `STATISTICS DEFAULT` (PostgreSQL 17+) | `ALTER TABLE t ALTER COLUMN c SET STATISTICS DEFAULT` |
   | `COMPRESSION method` | `ALTER TABLE t ALTER COLUMN c SET COMPRESSION m` |
   | `STORAGE type` | `ALTER TABLE t ALTER COLUMN c SET STORAGE s` |
   | `DEPRECATED 'msg'` | `COMMENT ON COLUMN t.c IS '[DEPRECATED] msg'` |
   | `USING expr` | `ALTER TABLE t ALTER COLUMN c TYPE ... USING expr` |
   | `RENAMED FROM old` | `ALTER TABLE t RENAME COLUMN old TO new` |
   | `GRANTS { ... }` | `GRANT priv (col) ON TABLE t TO role` |
   | `REVOKES { ... }` | `REVOKE priv (col) ON TABLE t FROM role` |

   **Validation rules:**

   -   Every `COLUMN name { }` MUST reference a column name that exists
       in the `( )` list.  A reference to a non-existent column is a
       compiler error (DPG-E018).

   -   `COLUMN` blocks do NOT declare new columns.  New columns are
       declared only in the `( )` list.

   -   After a rename, the `COLUMN` block MUST use the new name;
       `RENAMED FROM` carries the old name.

   -   After any column rename, all index and constraint declarations in
       the `{ }` block MUST reference the new column name.  Any
       reference to the old column name is a compiler error (DPG-E019).

   [PG] `STATISTICS`, `STORAGE`, and `COMPRESSION` take PostgreSQL's own
   values (`-1` to `10000` or `DEFAULT`; `plain`/`main`/`extended`/
   `external`; `pglz`/`lz4`).  [DPG] A statistics target above `10000`
   is error DPG-E020.

   **Generated-column expression changes:** No separate directive is
   needed — the `GENERATED ALWAYS AS ( expr ) STORED`/`VIRTUAL` clause is
   already part of `column-def` (Section 7.2).  The differ recognises
   four distinct transitions on it and emits the matching targeted
   `ALTER` (or, where real PostgreSQL has no in-place `ALTER` path at
   all, a drop-and-recreate of the column), instead of falling through
   to the generic "column type changed" row:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `GENERATED ... AS (expr)` added where none existed | `ALTER TABLE t DROP COLUMN c; ALTER TABLE t ADD COLUMN c type GENERATED ALWAYS AS (expr) STORED\|VIRTUAL` (PostgreSQL has no in-place form for turning a plain column into a generated one) | `DESTRUCTIVE` (the column's prior values are discarded, not merely rewritten in place) |
   | `STORED`↔`VIRTUAL` changed, expression unchanged | Same drop-and-recreate as above (`SET EXPRESSION` cannot change the kind) | `DESTRUCTIVE` |
   | Existing generated expression text changed, `STORED`/`VIRTUAL` kind unchanged | `ALTER TABLE t ALTER COLUMN c SET EXPRESSION AS (expr)` (PostgreSQL 18+) | `CAUTION` |
   | `GENERATED ... AS (expr)` removed, column kept (`STORED`) | `ALTER TABLE t ALTER COLUMN c DROP EXPRESSION IF EXISTS` | `SAFE` (freezes the column's current values, no rewrite) |
   | `GENERATED ... AS (expr)` removed, column kept (`VIRTUAL`) | Same drop-and-recreate as the "added" row above, re-adding the column as plain (PostgreSQL rejects `DROP EXPRESSION` for a `VIRTUAL` column) | `DESTRUCTIVE` (a `VIRTUAL` column has no stored value to freeze in the first place; the re-added column comes back `NULL`, not the last computed value) |

   **Identity-column changes:** Likewise driven entirely by `column-def`'s
   already-declarative `GENERATED ALWAYS/BY DEFAULT AS IDENTITY
   [identity-opts]` (Section 7.2) — no new grammar, only new diffing rules,
   distinguished from a generic column-type change:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Identity clause added where none existed | `ALTER TABLE t ALTER COLUMN c ADD GENERATED {ALWAYS\|BY DEFAULT} AS IDENTITY [(seq-options)]` | `CAUTION` |
   | `ALWAYS`↔`BY DEFAULT` changed, options unchanged | `ALTER TABLE t ALTER COLUMN c SET GENERATED {ALWAYS\|BY DEFAULT}` | `SAFE` |
   | `identity-opts` (increment/min/max/cache/cycle) changed | `ALTER TABLE t ALTER COLUMN c SET <option> ...` (one `SET` per changed option, PostgreSQL's own identity-sequence syntax — distinct from `ALTER SEQUENCE`, no sequence name involved) | `SAFE` |
   | Identity clause removed, column kept | `ALTER TABLE t ALTER COLUMN c DROP IDENTITY [IF EXISTS]` | `SAFE` |

### 7.5. Column-Level Grants

   Column-level grants use the `GRANTS { }` / `REVOKES { }` syntax
   inside a `COLUMN name { }` block.  The column scope is inferred by
   the compiler.  The emitted DDL is:

```sql
GRANT privilege (col) ON TABLE schema.table TO role;
```

   Column-level grants follow the same additive model as table-level
   grants (Section 11.2): DPG only emits `GRANT`; it NEVER auto-revokes.

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
        GRANTS       { SELECT TO compliance_role; }
        REVOKES  { ALL PRIVILEGES FROM PUBLIC; }
    }

    GRANTS { SELECT, INSERT, UPDATE TO app_service; }
}
```

### 7.6. Column Renaming

   Column renames are DPG lifecycle directives declared in
   `COLUMN new_name { RENAMED FROM old_name; }`.  The new name appears
   in both the `( )` list and the `COLUMN` block.  The old name appears
   only inside `RENAMED FROM`.

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

   **Compiler resolution algorithm:**

   1.  The compiler sees `email_address` in the `( )` list and
       `COLUMN email_address { RENAMED FROM email; }` in the `{ }` block.

   2.  It looks up `email` in the snapshot.
       -   If `email` is in the snapshot and `email_address` is NOT:
           this is State A (fresh rename). Emit
           `ALTER TABLE users RENAME COLUMN email TO email_address;`
           (Safety: `CAUTION`).
       -   If `email_address` is already in the snapshot:
           the rename has already been applied (State B). Treat as a
           normal column update; the `RENAMED FROM` directive is a no-op.
       -   If neither `email` nor `email_address` is in the snapshot:
           this is a new column with a stale `RENAMED FROM`. Emit error
           DPG-E021 (stale RENAMED FROM directive).

   3.  After emitting the rename, all constraint and index declarations
       MUST use `email_address`.  The compiler validates and emits
       DPG-E019 on any reference to `email`.

   **`RENAMED FROM` for tables and schemas** follows the same algorithm
   with `QualifiedName()` substituted for column name:

   -   `TABLE user_accounts { RENAMED FROM users; }` →
       `ALTER TABLE users RENAME TO user_accounts;`
   -   `SCHEMA reporting { RENAMED FROM old_reporting; }` →
       `ALTER SCHEMA old_reporting RENAME TO reporting;`

   **Cross-schema moves (`SET SCHEMA`):** For every schema-scoped object
   kind whose `RENAMED FROM` uses the generic `renamed-from-dir`
   production (Appendix A) — Table, View, Materialized View, Sequence,
   and the kinds covered in Sections 9/12/14 that reuse the same directive —
   `identifier` there is in fact `qual-name` (`[schema.]name`), so the
   old name MAY be schema-qualified.  When the resolved old name's
   schema differs from the object's current declared schema, the
   compiler emits `ALTER <kind> old_schema.name SET SCHEMA new_schema`
   *in addition to* `RENAME TO`, in that order (`SET SCHEMA` first,
   since a still-in-flight rename could otherwise collide with an
   existing name in the target schema) — matching the fact that real
   PostgreSQL has no single statement that does both at once for any of
   these kinds:

   -   `TABLE archive.orders { RENAMED FROM public.orders; }` →
       `ALTER TABLE public.orders SET SCHEMA archive;` (name unchanged,
       schema-only move; `RENAME TO` omitted since the bare name is the
       same).
   -   `VIEW archive.order_summary { RENAMED FROM public.orders_summary; }`
       → `ALTER VIEW public.orders_summary SET SCHEMA archive;` then
       `ALTER VIEW archive.orders_summary RENAME TO order_summary;`
       (both schema and name changed).

   An unqualified `RENAMED FROM old_name` is always resolved within the
   object's *current* declared schema — this is a strictly additive
   grammar change; every existing same-schema `RENAMED FROM` in this
   document continues to mean exactly what it always has.

### 7.7. Indexes

   Indexes are declared in the `INDEXES { }` block (or using the
   singular `INDEX` keyword) inside a table's `{ }` block.

```abnf
index-decl  = [ "UNIQUE" WSP ]
              [ "CONCURRENTLY" WSP ]
              [ "ONLY" WSP ]
              index-name WSP
              [ "USING" WSP method WSP ]
              "(" index-col-list ")"
              [ "INCLUDE" WSP "(" col-list ")" ]
              [ WSP "NULLS" WSP ( "DISTINCT" / "NOT DISTINCT" ) ]
              [ "WITH" WSP "(" storage-params ")" ]
              [ "WHERE" WSP "(" predicate ")" ]
              [ "TABLESPACE" WSP identifier ]
              [ WSP "RENAMED FROM" WSP index-name ]
              ";"

index-col-list = index-col *( "," index-col )
index-col   = ( col-name / "(" expr ")" )
              [ "ASC" / "DESC" ]
              [ "NULLS FIRST" / "NULLS LAST" ]
              [ "COLLATE" WSP identifier ]
              [ WSP identifier [ "(" storage-params ")" ] ]  -- opclass [(params)]
```

   `UNIQUE`, `CONCURRENTLY`, and `ONLY` are all prefix keywords before the
   index name, in that fixed order — mirroring real PostgreSQL's own
   `CREATE UNIQUE INDEX CONCURRENTLY name ON ONLY table USING method (columns)`
   exactly (only the implicit `INDEX`/`ON table` are dropped, since DPG's
   `INDEXES { }` block already establishes both). In **Mode B** (Section 4.8),
   which does carry the literal `INDEX` keyword, the same order applies
   with `INDEX` inserted where PostgreSQL puts it:
   `[ "UNIQUE" WSP ] "INDEX" WSP [ "CONCURRENTLY" WSP ] [ "ONLY" WSP ] index-name ...`.

   **`ONLY`** suppresses recursion into a partitioned table's own
   partitions — the index is created solely on the parent, matching real
   PostgreSQL's `CREATE INDEX ... ON ONLY table` exactly.  Meaningful only
   when the enclosing table has a `PARTITION BY` clause (Section 7.13); DPG
   performs no validation of this and leaves the combination to
   PostgreSQL's own parser, per the passthrough principle already applied
   to other clause-combination rules in this document (e.g. Section 7.9's
   `REFERENCING` note).

   **Index opclass parameters:** `index-col`'s trailing `identifier
   [ "(" storage-params ")" ]` is the operator class name, optionally
   followed by its own parameters (`opclass(param = value, ...)`) — the
   identical shape already used by `excl-list`'s `EXCLUDE` element
   (Appendix A), reused here rather than inventing a second name for it.

```sql
INDEXES {
    idx_search_doc USING gist (doc tsvector_ops(siglen = 32));
}
```

   [PG] `NULLS [NOT] DISTINCT` applies to `UNIQUE` indexes, as in
   PostgreSQL; it is accepted in the same three places PostgreSQL
   accepts it (inline constraint, table constraint, index).

```sql
INDEXES {
    UNIQUE idx_unique_optional_email (email) NULLS NOT DISTINCT;
}
```

   **`CONCURRENTLY` is a bare presence keyword, not a boolean** — this
   matches real PostgreSQL exactly: `CONCURRENTLY` is either written or it
   isn't, there is no `CONCURRENTLY false` and no project-wide default that
   turns it on implicitly. Writing it is the only way an index is ever
   created concurrently; omitting it always means plain `CREATE INDEX`.

   **Concurrency behaviour:**

   -   Writing `CONCURRENTLY` on an individual index emits
       `CREATE INDEX CONCURRENTLY`, a `CAUTION` non-transactional
       operation (emitted after `COMMIT`) — *except* for a
       brand-new table's own indexes (see below), where it is silently
       ignored.

   -   Omitting `CONCURRENTLY` emits plain `CREATE INDEX` (transactional;
       Safety `CAUTION` for index additions on non-empty tables). There is
       no configuration knob that changes this — matching real PostgreSQL,
       which has no such default either.

   -   **Indexes on a brand-new table are always non-concurrent —
       this is a hard PostgreSQL restriction, not a preference.** A new
       table's indexes are emitted in the SAME transactional migration as
       its `CREATE TABLE`, and PostgreSQL rejects `CREATE INDEX
       CONCURRENTLY` inside a transaction block. The compiler therefore
       forces these non-concurrent unconditionally — an explicit
       `CONCURRENTLY` on such an index has no effect until a later
       migration adds the index to the now-existing table.

   **Index identity:** An index is uniquely identified by its name
   within a schema.  Two indexes with the same name but different
   definitions are a compiler error (DPG-E005).

   **Index renaming:** `RENAMED FROM old_name` follows the same
   algorithm as Table/Schema renaming (Sections 7.6/7.11) — a metadata-only
   `ALTER INDEX old_name RENAME TO new_name` is emitted instead of the
   usual drop-and-recreate path, and a rename combined with any other
   structural change still falls back to drop + recreate (the rename
   carve-out applies only when nothing else about the index differs).

```sql
INDEXES {
    idx_email_lower (lower(email)) RENAMED FROM idx_lower_email;
}
```

   **Partial index predicates** are stored as normalised text and
   diffed by text equality.  Whitespace normalisation is applied:
   all runs of whitespace are collapsed to a single space.

   **Expression indexes:** The column expression `( expr )` is treated
   as opaque text and diffed by text equality after whitespace
   normalisation.

   **Covering indexes:** `INCLUDE (col1, col2)` adds columns to the
   index leaf pages without participating in the search key.  Adding or
   removing `INCLUDE` columns requires dropping and recreating the
   index (Safety: `CAUTION`; no data loss but requires a lock and a
   full index rebuild).

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New index (existing table) | `CREATE [UNIQUE] INDEX [CONCURRENTLY] [IF NOT EXISTS] name ON ...` | `CAUTION` (non-transactional if `CONCURRENTLY` written) |
   | Index removed | `DROP INDEX [CONCURRENTLY] name` | `CAUTION` |
   | Index renamed only (`RENAMED FROM`, nothing else differs) | `ALTER INDEX old_name RENAME TO new_name` | `SAFE` |
   | Any other structural change | Drop + recreate | `CAUTION` |

   Indexes on new tables (emitted in the same migration as the
   `CREATE TABLE`) are always emitted as non-concurrent `CREATE INDEX` in
   the same transactional block — see **Concurrency behaviour** above.

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
        idx_tags              USING gin  (tags);
        idx_lower_email       (lower(email));
        idx_covering          (user_id) INCLUDE (email, created_at);
        idx_brin              USING brin (created_at)
                                  WITH (pages_per_range = 128);
        idx_archived          (archived_at) TABLESPACE archive_space;
        CONCURRENTLY idx_forced_concurrent (payload);
        UNIQUE CONCURRENTLY idx_unique_concurrent (external_id);
    }
}

-- Mode B (Section 4.8): the literal INDEX keyword carries UNIQUE/CONCURRENTLY in
-- the exact same order PostgreSQL's own CREATE UNIQUE INDEX CONCURRENTLY
-- statement does.
UNIQUE INDEX idx_unique_slug (slug);
INDEX CONCURRENTLY idx_forced_concurrent (payload);
UNIQUE INDEX CONCURRENTLY idx_unique_concurrent (external_id);
```

### 7.8. Row Level Security

   Row Level Security (RLS) is enabled and configured in the table's
   `{ }` block.

```abnf
rls-enable-dir = "ENABLE ROW LEVEL SECURITY"
rls-force-dir  = "FORCE ROW LEVEL SECURITY"

policy-decl    = policy-name WSP "FOR" WSP command
                 [ WSP "AS" WSP permissiveness ]
                 [ WSP "TO" WSP role-list ]
                 [ WSP "USING" WSP "(" expr ")" ]
                 [ WSP "WITH CHECK" WSP "(" expr ")" ]
                 [ WSP "RENAMED FROM" WSP policy-name ]
                 ";"

command        = "ALL" / "SELECT" / "INSERT" / "UPDATE" / "DELETE"
permissiveness = "PERMISSIVE" / "RESTRICTIVE"
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `ENABLE ROW LEVEL SECURITY` added | `ALTER TABLE t ENABLE ROW LEVEL SECURITY` | `SAFE` |
   | `FORCE ROW LEVEL SECURITY` added | `ALTER TABLE t FORCE ROW LEVEL SECURITY` | `SAFE` |
   | `FORCE ROW LEVEL SECURITY` removed, `ENABLE` still present | `ALTER TABLE t NO FORCE ROW LEVEL SECURITY` | `SAFE` |
   | Both removed | `ALTER TABLE t DISABLE ROW LEVEL SECURITY` | `SAFE` |
   | New policy | `CREATE POLICY name ON t FOR ... TO ... USING (...) WITH CHECK (...)` | `SAFE` |
   | Only `TO`/`USING`/`WITH CHECK` differ (`FOR`/`AS` unchanged) | `ALTER POLICY name ON t [TO ...] [USING (...)] [WITH CHECK (...)]` | `SAFE` |
   | Renamed only (`RENAMED FROM`, `FOR`/`AS`/`TO`/`USING`/`WITH CHECK` all unchanged) | `ALTER POLICY old_name ON t RENAME TO new_name` | `SAFE` |
   | Renamed AND `TO`/`USING`/`WITH CHECK` also differ (`FOR`/`AS` unchanged) | `ALTER POLICY old_name ON t RENAME TO new_name;` then `ALTER POLICY new_name ON t [TO ...] [USING (...)] [WITH CHECK (...)]` (two statements) | `SAFE` |
   | `FOR command` or `AS PERMISSIVE/RESTRICTIVE` differs | `DROP POLICY name ON t; CREATE POLICY ...` | `CAUTION` |
   | Permissive policy removed | `DROP POLICY name ON t` | `SAFE` (only narrows access) |
   | Restrictive policy removed | `DROP POLICY name ON t` | `CAUTION` (widens access) |

   **`ALTER POLICY` vs. drop-and-recreate:** Real PostgreSQL's `ALTER
   POLICY` can change `TO`/`USING`/`WITH CHECK` in place, atomically —
   `FOR command` and `AS PERMISSIVE/RESTRICTIVE` are fixed at creation
   with no `ALTER POLICY` equivalent, forcing drop-and-recreate for
   those two fields specifically. Preferring `ALTER POLICY` whenever
   possible matters beyond avoiding an extra statement: a pure
   `USING`/`WITH CHECK`/`TO`-role change is exactly the kind of edit
   `DROP POLICY; CREATE POLICY` turns into a real (if brief) window
   with zero active policy for that command — `ALTER POLICY` closes
   that window entirely.

   **`RENAMED FROM` on a policy** (like Constraint/Index `RENAMED
   FROM`, Sections 7.3/7.7) matches an existing policy by its old name
   within the same table and emits `ALTER POLICY old_name ON t RENAME
   TO new_name` — `SAFE`, metadata-only — instead of the drop-and-
   recreate a name-only difference would otherwise trigger under name-
   based policy identity. Policy identity is `(schema, table,
   policy_name)`; PostgreSQL provides no mechanism to move a policy to
   a different table via rename, so — unlike Table/View/Function's
   cross-schema `RENAMED FROM` extension (Section 7.6) — there is no
   schema- or table-qualified form here. Real PostgreSQL's `ALTER
   POLICY` cannot rename and modify `TO`/`USING`/`WITH CHECK` in a
   single statement (they are two distinct, mutually exclusive clause
   forms), so when both change in the same migration the compiler
   emits the `RENAME TO` first, then a second `ALTER POLICY` statement
   under the new name for the remaining changes. When `FOR command` or
   `AS PERMISSIVE/RESTRICTIVE` also differs, the existing drop-and-
   recreate path already creates the policy under its final (new) name
   directly, so no separate `RENAME TO` is emitted in that case.

   Example:

```sql
TABLE orders ( ... )
{
    ENABLE ROW LEVEL SECURITY;
    FORCE ROW LEVEL SECURITY;

    POLICIES {
        view_own FOR SELECT
            USING (user_id = auth.uid());

        insert_own FOR INSERT
            WITH CHECK (user_id = auth.uid());

        update_own FOR UPDATE
            USING     (user_id = auth.uid())
            WITH CHECK (user_id = auth.uid() AND status != 'locked');

        restrict_deleted AS RESTRICTIVE FOR ALL
            USING (deleted_at IS NULL);

        admin_all FOR ALL
            TO admin_role
            USING (true);

        service_read FOR SELECT
            TO service_role, readonly_role
            USING (true);
    }
}
```

   [DPG] RLS settings and policies declared on a partitioned table are
   not copied to its partitions.  In PostgreSQL they apply only to
   queries through the parent, so protecting direct access to a
   partition requires declaring RLS and policies on that partition too.

### 7.9. Triggers

   Triggers are declared in the `TRIGGERS { }` block inside a table's
   `{ }` block.

```abnf
trigger-decl = trigger-name WSP timing WSP event-list
               [ WSP "FROM" WSP table-ref ]
               [ WSP deferrable-clause ]
               [ WSP referencing-clause ]
               WSP for-each
               [ WSP "WHEN" WSP "(" expr ")" ]
               WSP "EXECUTE FUNCTION" WSP func-ref "(" arg-list ")"
               [ WSP trigger-enable-state ]
               [ WSP depends-on-extension-dir ]
               [ WSP "RENAMED FROM" WSP trigger-name ]
               ";"

timing       = "BEFORE" / "AFTER" / "INSTEAD OF"
event-list   = event *( "OR" WSP event )
event        = "INSERT" / "UPDATE" [ "OF" col-list ] / "DELETE" / "TRUNCATE"
for-each     = "FOR EACH ROW" / "FOR EACH STATEMENT"

referencing-clause = "REFERENCING"
    ( "OLD TABLE AS" identifier [ "NEW TABLE AS" identifier ]
    / "NEW TABLE AS" identifier [ "OLD TABLE AS" identifier ] )

; Real PostgreSQL has no such clause on CREATE TRIGGER itself — a new
; trigger is always ENABLED, and the other three states are ALTER-only.
; DPG models the state declaratively anyway (same reasoning as RLS's
; enable-dir/force-dir, Section 7.8), so a source file's trigger declaration is
; a complete description of desired state without a separate directive
; block. Omitting this clause means ENABLED — real PostgreSQL's default.
trigger-enable-state = "DISABLED" / "ENABLE REPLICA" / "ENABLE ALWAYS"
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New trigger | `CREATE [CONSTRAINT] TRIGGER name ... ON t ...` | `SAFE` |
   | Trigger changed | `DROP TRIGGER name ON t; CREATE TRIGGER ...` | `CAUTION` |
   | Renamed only (`RENAMED FROM`, nothing else differs) | `ALTER TRIGGER old_name ON t RENAME TO new_name` | `SAFE` |
   | Enable state changed only (`trigger-enable-state`) | `ALTER TABLE t ENABLE/DISABLE TRIGGER name` or `ALTER TABLE t ENABLE REPLICA/ALWAYS TRIGGER name` | `SAFE` |
   | `[NO] DEPENDS ON EXTENSION` changed (`depends-on-extension-dir`, Section 9.1) | `ALTER TRIGGER name ON t [NO] DEPENDS ON EXTENSION ext` | `SAFE` |
   | Trigger removed | `DROP TRIGGER name ON t` | `CAUTION` (stops whatever the trigger enforced) |

   Trigger identity is `(schema, table, trigger_name)`.  `depends-on-
   extension-dir` reuses the same production Function/Procedure already
   define (Section 9.1) — real PostgreSQL's `ALTER TRIGGER ... DEPENDS ON
   EXTENSION` has identical grammar to the function form, just a
   different target object.

   **`RENAMED FROM` on a trigger** (like Constraint/Index/Policy
   `RENAMED FROM`) matches an existing trigger by its old name within
   the same table and emits `ALTER TRIGGER old_name ON t RENAME TO
   new_name` — `SAFE`, metadata-only. PostgreSQL provides no mechanism
   to move a trigger to a different table via rename, so — unlike
   Table/View/Function's cross-schema `RENAMED FROM` extension (Section
   7.6) — there is no schema- or table-qualified form here. When any
   other trigger property changes at the same time (forcing the drop-
   and-recreate row above), the recreated trigger is already created
   under its final (new) name directly, so no separate `RENAME TO` is
   emitted in that case. When only the enable state also changes, the
   two ops are independent statements (`ALTER TABLE` vs. `ALTER
   TRIGGER`) with no conflict. When only `[NO] DEPENDS ON EXTENSION`
   also changes, real PostgreSQL's `ALTER TRIGGER` cannot combine
   `RENAME TO` and `DEPENDS ON EXTENSION` in a single statement — the
   compiler emits `RENAME TO` first (referencing the old name), then
   the `DEPENDS ON EXTENSION` change as a second `ALTER TRIGGER`
   statement referencing the new name.

   **`RULE` has no DPG equivalent** — PostgreSQL's `CREATE RULE` is out
   of scope entirely (Section 23), so the `ENABLE`/`DISABLE RULE` half of real
   PostgreSQL's enable-state model has nothing to attach to and is not
   addressed by this section.

   [DPG] Trigger clauses are passed to PostgreSQL unchanged; DPG does
   not check clause combinations (for example which triggers may use
   `REFERENCING`).  PostgreSQL reports any invalid combination when the
   migration runs.

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

### 7.10. Table-Level Grants and Revocations

   Table-level grants and revocations are declared in `GRANTS { }` and
   `REVOKES { }` blocks inside the table's `{ }` block.

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

   Grants follow the additive model (Section 11.2).

   **`MAINTAIN`** (PostgreSQL 17+): controls `VACUUM`/`ANALYZE`/
   `CLUSTER`/`REINDEX`/`REFRESH MATERIALIZED VIEW` access to the table
   without granting broader privileges — real PostgreSQL's own
   `privilege` production accepts it only for tables/matviews/indexes;
   `USAGE`/`EXECUTE`-only object kinds reusing this same shared
   `privilege` production (Section 11.2) would have it rejected by
   PostgreSQL's own parser, the same passthrough-validation principle
   already applied throughout this document.

   **`GRANTED BY role`** (both `grant-entry` and `revoke-entry`):
   real PostgreSQL accepts this clause but restricts the effective
   grantor to `current_user` in practice — a SQL-standard-compatibility
   clause with limited real-world effect, included here purely to close
   the Tenet-1 expressiveness gap (the RFC's own text sets no "practical
   impact" threshold for what counts as a gap).

### 7.11. Table Lifecycle Directives

```abnf
renamed-from-dir = "RENAMED FROM" WSP qual-name
protected-dir    = "PROTECTED"
deprecated-dir   = "DEPRECATED" WSP SQUOTE text SQUOTE
drop-cascade-dir = "DROP CASCADE"
owner-dir        = "OWNER" WSP identifier
comment-dir      = "COMMENT" WSP SQUOTE text SQUOTE
```

   **`PROTECTED`:** The compiler MUST refuse to emit a `DROP TABLE`
   for a protected table even when the table is absent from the desired
   state.  Removing a protected table requires first removing the
   `PROTECTED` directive.  Safety: any attempt to drop a protected
   table is error DPG-E022.

   **`DEPRECATED 'msg'`:** The compiler emits a `COMMENT ON TABLE`
   prefixed with `[DEPRECATED]` and the message text.  The linter emits
   a warning when any other object references a deprecated table
   (if `warn_on_deprecated = true`).

   **`DROP CASCADE`:** Overrides `default_drop_behavior` for this
   specific object.  The compiler emits `DROP TABLE name CASCADE` when
   removing this table.

   **`RENAMED FROM`:** See Section 7.6, including the cross-schema
   `SET SCHEMA` behaviour a schema-qualified old name triggers.

   **Attaching a downstream naming convention:** a `NAME MAP`/`NAME
   MAPS` directive can also appear in this block, independently of
   `RENAMED FROM` — see Appendix D.2 (Name Maps) for the full feature (ten
   rule keywords, `[namemaps]` `dpg.toml` config, block-layer
   directives, and snapshot representation).

   **`REPLICA IDENTITY`/`CLUSTER ON`** are declared the same way as RLS
   (Section 7.8) — as block directives even though real PostgreSQL has no
   `CREATE TABLE`-native clause for either, only an `ALTER TABLE`
   statement:

```abnf
replica-identity-dir = "REPLICA IDENTITY" WSP
                        ( "DEFAULT" / "FULL" / "NOTHING"
                        / "USING INDEX" WSP index-name )
cluster-dir           = "CLUSTER ON" WSP index-name
```

   **`REPLICA IDENTITY`** controls how much of an updated/deleted row
   PostgreSQL's logical replication can capture — directly gates what a
   Publication (Section 13.1) can actually replicate for `UPDATE`/`DELETE`.
   Omitting the directive means `DEFAULT` (the primary key only),
   PostgreSQL's own default; `USING INDEX` requires a `UNIQUE` index on
   `NOT NULL` columns, left to PostgreSQL's own parser to enforce.
   Changing it emits `ALTER TABLE t REPLICA IDENTITY ...` — `SAFE`
   (metadata-only, no rewrite).

   **`CLUSTER ON`** records which index a future manual `CLUSTER`
   (Section 23 — the `CLUSTER` command itself remains a runtime operation, out
   of scope) would use; declaring it does not itself cluster the table.
   Removing a previously-declared `CLUSTER ON` emits `ALTER TABLE t SET
   WITHOUT CLUSTER` — `SAFE`.  PostgreSQL 17's new `CLUSTER
   (VERBOSE, ...)` parenthesized-options syntax is a variant of the
   `CLUSTER` *command* itself, so it stays out of scope for the same
   reason — no DPG action needed either direction.

```sql
TABLE orders ( ... )
{
    REPLICA IDENTITY FULL;
    CLUSTER ON idx_orders_created_at;
}
```

   **`OF type_name`/`NOT OF` (post-creation):** A table gaining, losing,
   or switching its `OF type_name` association (Section 7.1) after creation is
   an `ALTER`-only transition, distinct from the CREATE-time Form 2
   already covered in Section 7.1: `ALTER TABLE t OF type_name` (gained or
   switched to a different type) / `ALTER TABLE t NOT OF` (removed) —
   `CAUTION` (PostgreSQL validates every column against the type's
   attributes at execution time).

   **`SET ACCESS METHOD` (post-creation):** A change to a table's
   already-declared `USING method` clause (Section 7.1) after creation emits
   `ALTER TABLE t SET ACCESS METHOD method` — `CAUTION` (rewrites the
   table's storage using the new method).

   **`INHERIT`/`NO INHERIT` (post-creation):** A change to a table's
   `INHERITS (...)` list (Section 7.1) after creation — a parent added or
   removed — emits `ALTER TABLE child INHERIT parent` /
   `ALTER TABLE child NO INHERIT parent` per added/removed parent,
   rather than requiring the whole table to be recreated — `SAFE`
   (`INHERIT` briefly validates existing rows against the parent's
   constraints; no rewrite).

### 7.12. Unlogged and Foreign Tables

   **Unlogged tables:**

```sql
UNLOGGED TABLE session_cache (
    key   TEXT NOT NULL PRIMARY KEY,
    value JSONB
);
```

   Emits `CREATE UNLOGGED TABLE`.  Changing a regular table to unlogged
   (or vice versa) emits `ALTER TABLE name SET UNLOGGED` /
   `ALTER TABLE name SET LOGGED` — classified `CAUTION` (rewrites the
   table's storage in place and briefly holds `ACCESS EXCLUSIVE`;
   dependent foreign keys, views, and indexes are kept).

   **Temporary tables** are session-scoped.  DPG MUST NOT manage them.
   A `TEMPORARY TABLE` keyword anywhere in a `.dpg` file is a compiler
   error (DPG-E023).

   **Foreign tables:** `SERVER` and `OPTIONS` are Part 1 clauses
   appearing after `)` per Tenet 5.  They MUST NOT be moved to the
   `{ }` block.

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

   **Diffing semantics:** A foreign table reuses the same `TABLE`
   diffing machinery as a regular table (Section 21) — columns, owner,
   comment, grants/revocations, security labels, rename — with two
   foreign-specific rules:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | `OPTIONS` changed | `ALTER FOREIGN TABLE t OPTIONS (ADD/SET/DROP key 'value', ...)` | `SAFE` |
   | `SERVER` changed | `DROP FOREIGN TABLE t; CREATE FOREIGN TABLE t ... SERVER new_server ...` | `DESTRUCTIVE` |

   `SERVER` cannot be changed via `ALTER FOREIGN TABLE` — real
   PostgreSQL has no such clause — so a `SERVER` change always requires
   the full drop-and-recreate path, unlike `OPTIONS`, which real
   PostgreSQL supports altering in place.  Column add/drop/type-change
   follow Section 21's `TABLE` rules unchanged (a foreign table's columns
   describe the remote shape, not local storage, but the diffing model
   itself does not distinguish the two).


### 7.13. Partitioned Tables

   Partitioning uses the `PARTITION BY` clause in Part 1 (after `)`,
   per Tenet 5).

```abnf
partition-by-clause = "PARTITION BY" WSP partition-strategy
                      WSP "(" partition-col-list ")"

partition-strategy  = "RANGE" / "LIST" / "HASH"

partition-col-list  = partition-col *( "," partition-col )
partition-col       = col-name / "(" expr ")"
```

   Partitions are declared in the `PARTITIONS { }` block inside the
   table's `{ }` block.

```abnf
partition-decl  = [ "FOREIGN" WSP ] partition-name WSP
                  ( "FOR VALUES" WSP bounds-clause / "DEFAULT" )
                  [ WSP "SERVER" WSP identifier
                    [ WSP "OPTIONS" WSP "(" option-list ")" ] ]
                  [ WSP "RENAMED FROM" WSP partition-name ]
                  [ WSP partition-by-clause WSP
                    "{" "PARTITIONS" WSP "{" *partition-decl "}" "}" ]
                  ";"

bounds-clause   = "FROM" WSP "(" literal-list ")"
                  "TO" WSP "(" literal-list ")"        -- RANGE
                / "IN" WSP "(" literal-list ")"        -- LIST
                / "WITH" WSP "(" modulus-remainder ")" -- HASH
```

   **Formalizing sub-partitioning in the grammar:** the recursive
   `[ WSP partition-by-clause WSP "{" "PARTITIONS" WSP "{" *partition-decl
   "}" "}" ]` suffix above makes explicit what the sub-partitioning
   example further below already demonstrated informally — a partition
   entry MAY itself be further partitioned to arbitrary depth, reusing
   `partition-decl` recursively.

   **Foreign table partitions:** `FOREIGN partition-name ... SERVER
   server_name [OPTIONS (...)]` makes one partition a foreign table
   instead of a regular one — real PostgreSQL's own sharding pattern,
   where each partition's data actually lives on a different remote
   server (`postgres_fdw`, etc.).  Column definitions are never
   restated — a partition, foreign or not, always inherits its parent's
   columns.  Emits `CREATE FOREIGN TABLE name PARTITION OF parent FOR
   VALUES ... SERVER server_name [OPTIONS (...)]`.

```sql
TABLE events ( id BIGINT, region TEXT, payload JSONB )
    PARTITION BY LIST (region)
{
    PARTITIONS {
        events_us FOR VALUES IN ('us-east', 'us-west');
        FOREIGN events_archive DEFAULT
            SERVER archive_server OPTIONS (table_name 'events_archive');
    }
}
```

   **Sub-partitioning:** A partition entry MAY have its own
   `PARTITION BY` sub-clause and a nested `{ PARTITIONS { ... } }`
   block:

```sql
TABLE events ( ... ) PARTITION BY RANGE (created_at)
{
    PARTITIONS {
        events_2024 FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')
            PARTITION BY LIST (region) {
                PARTITIONS {
                    events_2024_us FOR VALUES IN ('us-east', 'us-west');
                    events_2024_eu FOR VALUES IN ('eu-west', 'eu-central');
                }
            };
    }
}
```

   **Attaching/detaching an existing standalone table:** Declaring a
   partition from scratch (above) always emits `CREATE TABLE ... PARTITION
   OF ...`. To instead convert an *already-existing standalone table*
   into a partition — or a partition back into a standalone table —
   without dropping and recreating it (and its data), DPG offers a
   symmetric pair of directives mirroring `RENAMED FROM`'s "point at a
   prior identity" shape:

```abnf
; PARTITIONS { } entry form — attaches an existing standalone table
attached-partition-decl = "ATTACHED FROM" WSP table-ref WSP
                           "FOR VALUES" WSP bounds-clause ";"
                         / "ATTACHED FROM" WSP table-ref WSP "DEFAULT" ";"

; Standalone TABLE { } block directive — detaches a former partition
detached-from-dir = "DETACHED FROM" WSP table-ref [ WSP "CONCURRENTLY" ] ";"
```

   -   A `PARTITIONS { }` entry written as `ATTACHED FROM
       existing_table FOR VALUES ...` — where `existing_table` is a
       standalone table already present in the snapshot — emits `ALTER
       TABLE parent ATTACH PARTITION existing_table FOR VALUES ...`
       instead of `CREATE TABLE ... PARTITION OF ...`.  Real PostgreSQL
       validates the existing table's structure and any `CHECK`
       constraints against the target partition bound at attach time;
       DPG performs no additional validation of its own (passthrough
       principle, as elsewhere in this document).  `ATTACH PARTITION`
       has no `CONCURRENTLY` variant in real PostgreSQL — none is
       offered here either.
   -   A standalone `TABLE` declaration carrying `DETACHED FROM
       parent_table [CONCURRENTLY]` in its `{ }` block — where the
       table is currently a partition of `parent_table` per the
       snapshot — emits `ALTER TABLE parent_table DETACH PARTITION
       name [CONCURRENTLY]` instead of the drop this document would
       otherwise prescribe for "declared table disappeared from its
       parent's `PARTITIONS { }` block."  `CONCURRENTLY` runs the
       detach in two non-blocking steps (matching real PostgreSQL);
       omitting it holds a brief `ACCESS EXCLUSIVE` on the parent.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New partition | `CREATE TABLE <name> PARTITION OF <parent> FOR VALUES ...` | `SAFE` |
   | Partition renamed only (`RENAMED FROM`, bound/strategy unchanged) | `ALTER TABLE old_name RENAME TO new_name` | `CAUTION` |
   | Partition attached (`ATTACHED FROM`, existing table) | `ALTER TABLE parent ATTACH PARTITION name FOR VALUES ...` | `CAUTION` |
   | Partition detached (`DETACHED FROM`) | `ALTER TABLE parent DETACH PARTITION name [CONCURRENTLY]` | `CAUTION` (non-transactional if `CONCURRENTLY` written) |
   | Partition removed (absent, not detached) | `DROP TABLE <name>` | `DESTRUCTIVE` |
   | Partition strategy change | Instruction-only; requires `--approve-partition-rebuild` | `DESTRUCTIVE` |
   | Parent constraint dropped, retained locally by a direct partition (PG18+, Section 7.3) | `ALTER TABLE ONLY parent DROP CONSTRAINT name` | `SAFE` |
   | Partition's own local constraint added (Section 7.3) | `ALTER TABLE partition ADD CONSTRAINT name ...` | `CAUTION` |
   | Partition's own local constraint dropped (Section 7.3) | `ALTER TABLE partition DROP CONSTRAINT name` | `DESTRUCTIVE` |

   **`RENAMED FROM` on a partition** matches an existing partition —
   which MUST already be attached as a partition of this same parent
   table per the snapshot; converting an unrelated standalone table
   into a partition is `ATTACHED FROM`'s job, above, not this one's —
   by its old name and emits `ALTER TABLE old_name RENAME TO new_name`,
   the identical mechanism and safety classification as a plain table
   rename (Section 7.6), since a partition is an ordinary table under
   the hood: real PostgreSQL confirms renaming a partition has no
   effect on its partition attachment, constraints, or stored data.
   Because DPG does not model an independent schema for a partition (a
   partition's schema is always its parent table's), the target here is
   a bare `partition-name`, not the `qual-name` cross-schema form
   Table/View/Function use — no `SET SCHEMA` variant applies. Since the
   recursive `PARTITION BY { PARTITIONS { ... } }` suffix reuses this
   same `partition-decl` production at every nesting depth, `RENAMED
   FROM` is available identically on a sub-partition at any depth, with
   no special-casing required. Combining `RENAMED FROM` with `ATTACHED
   FROM`/`DETACHED FROM` on the same entry is out of scope — converting
   a standalone table into a partition under a different name (or vice
   versa) requires two separate migrations.

   **Partition strategy change procedure** (requires
   `--approve-partition-rebuild`):

   1.  Create new partitioned table with target strategy.
   2.  Create all declared partitions.
   3.  `INSERT INTO new_table SELECT * FROM old_table`.
   4.  Verify row counts match.
   5.  `DROP TABLE old_table`.
   6.  `ALTER TABLE new_table RENAME TO old_table`.
   7.  Recreate all indexes, constraints, grants, and policies.

   DPG does not run this sequence: it emits a single `DESTRUCTIVE`
   instruction-only step (Section 17.3) describing it, which `dpg apply`
   refuses to pass until `--approve-partition-rebuild` acknowledges it.

   Indexes declared at the parent partitioned table level are
   automatically inherited by all partitions.  The compiler MUST NOT
   emit duplicate `CREATE INDEX` statements on partitions that already
   inherit a parent-level index. Row Level Security is a notable
   exception to this inheritance: see Section 7.8's confirmed-live note
   — a policy declared on the parent does not protect a partition
   queried directly, and must be declared on each partition separately
   if that protection is needed.

---

---

## 8. Views

### 8.1. Regular Views

   Views use `AS <query>` per PostgreSQL's `CREATE VIEW` syntax.  The
   query text is Part 1.  An optional `{ }` block holds
   grants, a comment, owner, and other externally-attachable concerns.

   **Temporary views** are session-scoped for the same reason temporary
   tables are (Section 7.12) and are excluded on the same terms: DPG MUST NOT
   manage them, and a `TEMPORARY VIEW` (or `TEMP VIEW`) keyword anywhere
   in a `.dpg` file is a compiler error (DPG-E023).

   **PG equivalent:**
   `CREATE [OR REPLACE] VIEW name [(col-list)] [WITH (options)] AS query [WITH CHECK OPTION]`

```abnf
view-decl   = "VIEW" WSP schema-view-name
              [ WSP "(" col-name-list ")" ]
              [ WSP "WITH" WSP "(" view-options ")" ]
              WSP "AS" WSP query ";"
              [ "{" view-block "}" ]

view-block  = *( view-directive ";" )

view-directive = owner-dir / comment-dir / renamed-from-dir
               / deprecated-dir / grants-block / revocations-block
```

   The `WITH CHECK OPTION` (and `WITH LOCAL CHECK OPTION`) clause
   MUST appear at the END of the query text, immediately before the
   terminating `;`, per PostgreSQL syntax.

   Examples:

```sql
SCHEMA public {
    -- Minimal view
    VIEW active_users AS
        SELECT id, email, created_at
        FROM users
        WHERE status = 'active' AND deleted_at IS NULL;

    -- With named column list
    VIEW user_summary (user_id, email, order_count) AS
        SELECT u.id, u.email, COUNT(o.id)
        FROM users u
        LEFT JOIN orders o ON o.user_id = u.id
        GROUP BY u.id, u.email;

    -- With security_barrier option
    VIEW secure_view WITH (security_barrier = true) AS
        SELECT id, email FROM users WHERE tenant_id = current_tenant();

    -- With check option
    VIEW active_orders AS
        SELECT * FROM orders WHERE status != 'cancelled'
        WITH LOCAL CHECK OPTION;

    -- With { } block
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
   | Query changed, same output column list | `CREATE OR REPLACE VIEW ...` | `SAFE` |
   | Output column list changed (any way) | `DROP VIEW CASCADE; CREATE VIEW` | `DESTRUCTIVE` |
   | Owner changed | `ALTER VIEW name OWNER TO role` | `SAFE` |
   | Comment changed | `COMMENT ON VIEW name IS '...'` | `SAFE` |
   | View renamed (`RENAMED FROM`) | `ALTER VIEW old RENAME TO new` | `CAUTION` |
   | View moved to another schema (`RENAMED FROM` schema-qualified, Section 7.6) | `ALTER VIEW old_schema.name SET SCHEMA new_schema` | `SAFE` |
   | View removed | `DROP VIEW name [CASCADE]` | `DESTRUCTIVE` |

   **Output column list comparison:** The compiler compares the columns
   produced by the view query by name and ordinal position.  If
   either the name or the position of any output column differs, the
   change is classified as `DESTRUCTIVE`.  This comparison is
   best-effort: it can name the implicit or explicit output column at
   each target-list position for a plain `SELECT` (or a set operation,
   using its leftmost leaf) whose target list contains no `*`/`t.*`
   star expansion, since a star's true output column count isn't
   knowable without catalog access.  When the query on either side of
   the change doesn't fit that shape, the comparison cannot prove
   whether the output column list changed and the query change is
   instead emitted as `CREATE OR REPLACE VIEW ...` (`SAFE`); if the
   output columns did in fact change, PostgreSQL itself rejects the
   `CREATE OR REPLACE VIEW` at apply time (a real column-name/position
   change is never silently applied) rather than DPG proactively
   detecting it.

### 8.2. Materialized Views

   Materialized views use the same syntax as regular views, prefixed
   with `MATERIALIZED`.

   **PG equivalent:**
   `CREATE MATERIALIZED VIEW [IF NOT EXISTS] name [WITH (options)] [TABLESPACE ts] AS query [WITH NO DATA]`

```abnf
matview-decl = "MATERIALIZED VIEW" WSP schema-view-name
               [ WSP "WITH" WSP "(" storage-params ")" ]
               [ WSP "TABLESPACE" WSP identifier ]
               WSP "AS" WSP query
               [ WSP "WITH NO DATA" ]
               ";"
               [ "{" matview-block "}" ]

matview-block = *( matview-directive ";" )
matview-directive = owner-dir / comment-dir / indices-block
                  / grants-block / revocations-block
                  / renamed-from-dir / deprecated-dir
```

   The `{ }` block of a materialized view MAY contain `INDEXES { }`
   to declare indexes on the materialized view.

   Example:

```sql
SCHEMA analytics {
    MATERIALIZED VIEW daily_revenue AS
        SELECT
            date_trunc('day', created_at) AS day,
            SUM(total_amount)             AS revenue,
            COUNT(*)                      AS order_count
        FROM orders
        WHERE status = 'completed'
        GROUP BY 1;

    MATERIALIZED VIEW product_stats
    WITH (fillfactor = 90)
    TABLESPACE analytics_space AS
        SELECT product_id, COUNT(*) AS purchases, AVG(price) AS avg_price
        FROM order_items
        GROUP BY product_id
    WITH NO DATA {
        INDEXES   { idx_product_stats_id (product_id); }
        GRANTS    { SELECT TO app_readonly; }
    }
}
```

   **Diffing semantics:** Any change to the query text of a
   materialized view requires `DROP MATERIALIZED VIEW` followed by
   `CREATE MATERIALIZED VIEW` — classified as `DESTRUCTIVE`.
   `REFRESH MATERIALIZED VIEW` is a runtime operation and is out of
   scope for DPG (Section 23).  `RENAMED FROM` (Section 7.6) and `OWNER`/
   `COMMENT` changes follow the same rules as regular views: renaming
   emits `ALTER MATERIALIZED VIEW old RENAME TO new` (`CAUTION`); a
   schema-qualified `RENAMED FROM` emits `ALTER MATERIALIZED VIEW
   old_schema.name SET SCHEMA new_schema` (`SAFE`) alongside it when the
   schema also changed.  A `TABLESPACE` change emits `ALTER MATERIALIZED
   VIEW name SET TABLESPACE ts` (`SAFE`); a `WITH (...)` storage-param
   change emits targeted `ALTER MATERIALIZED VIEW name SET (...)`/
   `RESET (...)` clauses (`SAFE`) — the same treatment as a table's
   identical `TABLESPACE`/`WITH (...)` diffing (Section 7.11).

### 8.3. Recursive Views

   Recursive views use the `RECURSIVE` keyword and require a column
   name list.

   **PG equivalent:**
   `CREATE RECURSIVE VIEW name (col1, ...) AS query`

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

   Diffing semantics identical to regular views.

---

## 9. Functions and Procedures

### 9.1. Function Declaration Syntax

   Functions are written in complete, unmodified PostgreSQL SQL syntax
   with only the `CREATE OR REPLACE` verb removed.  The dollar-quoted
   body is Part 1.  The optional `{ }` block is Part 2.

```abnf
function-decl = "FUNCTION" WSP schema-func-name "(" [ arg-list ] ")"
                WSP return-clause
                *( func-attribute )
                func-body
                ";"
                [ "{" func-block "}" ]

; The dollar-quoted form is overwhelmingly the common case (PL/pgSQL,
; SQL, and every other procedural-language extension). LANGUAGE C and
; LANGUAGE internal use a different, string-literal AS form instead —
; DPG does not validate which LANGUAGE pairs with which func-body form,
; left to PostgreSQL's own parser (passthrough principle, as elsewhere
; in this document). BEGIN ATOMIC is PG14+'s standard-SQL-conformant
; alternative to the whole "AS dollar-string" shape, for LANGUAGE SQL
; only — see the BEGIN ATOMIC note below.
func-body     = WSP "AS" WSP dollar-string
              / WSP "AS" WSP string-literal [ WSP "," WSP string-literal ]
              / WSP "BEGIN ATOMIC" WSP sql-stmt-list WSP "END"

; One or more semicolon-terminated SQL statements, real PostgreSQL's
; own BEGIN ATOMIC body — each statement diffed the same as a
; LANGUAGE SQL body (Section 9.5's SQL canonicalisation applies here too).
sql-stmt-list = 1*( <SQL statement> ";" )

return-clause  = "RETURNS" WSP return-type
               / "RETURNS TABLE" WSP "(" col-def-list ")"
               / "RETURNS SETOF" WSP type-ref

func-attribute = "LANGUAGE" WSP lang-name
               / "VOLATILE" / "STABLE" / "IMMUTABLE"
               / "CALLED ON NULL INPUT" / "RETURNS NULL ON NULL INPUT"
                 / "STRICT"
               / "SECURITY DEFINER" / "SECURITY INVOKER"
               / "LEAKPROOF" / "NOT LEAKPROOF"
               / "PARALLEL UNSAFE" / "PARALLEL RESTRICTED" / "PARALLEL SAFE"
               / "COST" WSP number
               / "ROWS" WSP number
               / "SUPPORT" WSP func-ref
               / "WINDOW"
               / "SET" WSP identifier WSP "=" WSP expr
               / "SET" WSP identifier WSP "FROM CURRENT"
               / "TRANSFORM" WSP transform-for-type-list

transform-for-type-list = "FOR TYPE" WSP type-ref
                           *( "," WSP "FOR TYPE" WSP type-ref )

func-block     = *( func-directive ";" )
func-directive = comment-dir / owner-dir / grants-block / revocations-block
               / deprecated-dir / renamed-from-dir / depends-on-extension-dir

; Applies to Function/Procedure only — real PostgreSQL's ALTER AGGREGATE
; has no DEPENDS ON EXTENSION clause, even though Aggregate shares this
; same func-block production. DPG does not reject it on Aggregate itself
; (passthrough principle); PostgreSQL's own parser does.
depends-on-extension-dir = [ "NO" WSP ] "DEPENDS ON EXTENSION" WSP identifier
```

   Function attributes MUST appear in PostgreSQL's own documented
   ordering.  The compiler does not reorder them.

   All attributes listed in `func-attribute` above correspond exactly
   to options accepted by `CREATE FUNCTION` in PostgreSQL 14+.  The
   compiler passes them through verbatim when reconstructing the `CREATE
   OR REPLACE FUNCTION` statement.

   [PG] `AS 'obj_file', 'link_symbol'` (`LANGUAGE C`) and `AS 'name'`
   (`LANGUAGE internal`) are PostgreSQL's string-literal body forms.
   [DPG] Such a body is compared as an ordinary function attribute
   (Section 9.5), not through the body hash.  Base types' support
   functions (Section 5.5) are typically declared this way.

   **`BEGIN ATOMIC ... END`:** PostgreSQL 14+'s standard-SQL-conformant
   alternative to the dollar-quoted `AS` body, `LANGUAGE SQL` only — see
   `func-body` above.  Where the plain dollar-quoted form is the
   PostgreSQL-specific idiom, `BEGIN ATOMIC` is the form the SQL
   standard itself defines, directly relevant to Tenet 3 (Standard SQL
   vs. PostgreSQL-specific): a source file may choose either without
   DPG preferring one.  Diffed identically to a dollar-quoted
   `LANGUAGE SQL` body (Section 9.5's SQL canonicalisation applies verbatim,
   since the body content is ordinary SQL statements either way).

   **`TRANSFORM FOR TYPE type_name [, FOR TYPE type_name ...]`:**
   References a `CREATE TRANSFORM`-registered marshaling pair for the
   named type(s) — e.g. a PL/Python function accepting/returning
   `hstore` via `hstore_plpython`'s transform.  This *references* an
   already-installed transform; it does not declare one — `CREATE
   TRANSFORM` itself remains correctly out of scope (Section 23), the same
   distinction access-method `USING` (Section 7.1) draws against `CREATE
   ACCESS METHOD`.

   **`[NO] DEPENDS ON EXTENSION extension_name`** (Function/Procedure
   only, `func-block`): declares (or removes) an auto-drop dependency
   between the function and an extension, so the function is dropped
   automatically if the extension is — PostgreSQL's mechanism for a
   function that logically belongs to an extension without itself being
   a member object of it. Diffed as a set: an added entry emits `ALTER
   FUNCTION name(...) DEPENDS ON EXTENSION ext` (`SAFE`); a removed one
   emits `ALTER FUNCTION name(...) NO DEPENDS ON EXTENSION ext`
   (`SAFE`).

   **`OWNER`/`RENAMED FROM`** (`func-block`) follow the standard rules
   (Section 7.6): `ALTER FUNCTION name(...) OWNER TO role` (`SAFE`); `ALTER
   FUNCTION old(...) RENAME TO new` (`CAUTION`), plus `SET SCHEMA` when
   schema-qualified (`SAFE`).  Aggregates (Section 9.4) share
   `func-block`; for them these are the only changes that avoid a drop
   and recreate.

   **`GRANTS`/`REVOKES`** (`func-block`) follow Section 11.

### 9.2. Function Attributes

   [PG] Every `CREATE FUNCTION` attribute (`VOLATILE`/`STABLE`/
   `IMMUTABLE`, `STRICT`, `SECURITY DEFINER`, `LEAKPROOF`, `PARALLEL`,
   `COST`, `ROWS`, `SUPPORT`, `SET`, `WINDOW`, `TRANSFORM`) is written and
   behaves exactly as in PostgreSQL.

   [DPG] The linter warns when a `SECURITY DEFINER` function has no
   `SET search_path` (rule `security-definer-search-path`), since such a
   function is open to search-path injection.

   Examples:

```sql
SCHEMA public {
    -- Simple SQL function
    FUNCTION active_user_count() RETURNS BIGINT
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$
        SELECT COUNT(*) FROM users WHERE status = 'active';
    $$;

    -- PL/pgSQL function
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

    -- Named dollar-quote (avoids conflict when body contains $$)
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

   Procedures follow the same model as functions.  They omit the
   `RETURNS` clause.  Procedures MAY issue `COMMIT` mid-execution.

   **PG equivalent:** `CREATE [OR REPLACE] PROCEDURE name (...) LANGUAGE lang AS $$...$$`

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

   Procedure diffing semantics are identical to function diffing
   (Section 9.5).  Procedure identity is `(schema, name, arg_types)`
   where `OUT` and `TABLE` mode parameters are excluded from the type
   key per PostgreSQL's overloading rules.

### 9.4. Aggregate Functions

   Aggregates use two `( )` groups per PostgreSQL's `CREATE AGGREGATE`
   syntax — both are Part 1 per Tenet 5.

   **PG equivalent:**
   `CREATE [OR REPLACE] AGGREGATE name (input_types) (SFUNC = ..., STYPE = ..., ...)`

```abnf
aggregate-decl = "AGGREGATE" WSP schema-func-name
                 WSP "(" agg-input-list ")"
                 WSP "(" agg-options ")"
                 [ "{" func-block "}" ]

agg-input-list = "*"
               / [ mode WSP ] type-ref *( "," [ mode WSP ] type-ref )
               / ordered-set-sig

ordered-set-sig = type-ref *( "," type-ref )
                  WSP "ORDER BY" WSP type-ref *( "," type-ref )

; Every option PostgreSQL's CREATE AGGREGATE accepts (normal-aggregate form).
agg-options = agg-option *( "," WSP agg-option )
agg-option  = "SFUNC" WSP "=" WSP func-ref
            / "STYPE" WSP "=" WSP type-ref
            / "SSPACE" WSP "=" WSP integer
            / "FINALFUNC" WSP "=" WSP func-ref
            / "FINALFUNC_EXTRA"
            / "FINALFUNC_MODIFY" WSP "=" WSP modify-mode
            / "COMBINEFUNC" WSP "=" WSP func-ref
            / "SERIALFUNC" WSP "=" WSP func-ref
            / "DESERIALFUNC" WSP "=" WSP func-ref
            / "INITCOND" WSP "=" WSP string-literal
            / "MSFUNC" WSP "=" WSP func-ref
            / "MINVFUNC" WSP "=" WSP func-ref
            / "MSTYPE" WSP "=" WSP type-ref
            / "MSSPACE" WSP "=" WSP integer
            / "MFINALFUNC" WSP "=" WSP func-ref
            / "MFINALFUNC_EXTRA"
            / "MFINALFUNC_MODIFY" WSP "=" WSP modify-mode
            / "MINITCOND" WSP "=" WSP string-literal
            / "SORTOP" WSP "=" WSP operator-symbol
            / "PARALLEL" WSP "=" WSP ( "SAFE" / "RESTRICTED" / "UNSAFE" )
            / "HYPOTHETICAL"

modify-mode = "READ_ONLY" / "SHAREABLE" / "READ_WRITE"
```

   Example:

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

   **Diffing semantics:** Aggregate identity is `(schema, name, input_types)`.
   A change to any `agg-option` requires `DROP AGGREGATE IF EXISTS`
   followed by `CREATE AGGREGATE` (`DESTRUCTIVE`), because PostgreSQL's
   `ALTER AGGREGATE` cannot change them.  It can only rename, change
   owner, and move schema, which DPG uses for:

   -   Renamed (`RENAMED FROM`): `ALTER AGGREGATE name (input_types)
       RENAME TO new_name` (`CAUTION`); a schema-qualified old name also
       emits `ALTER AGGREGATE ... SET SCHEMA new_schema` (`SAFE`,
       Section 7.6).
   -   Owner changed: `ALTER AGGREGATE name (input_types) OWNER TO role`
       (`SAFE`).

### 9.5. Function Body Diffing Semantics

   The compiler stores a SHA-256 hash of the normalised function body
   in the snapshot (see Section 16.3).  Normalisation depends on the
   function's `LANGUAGE`:

   -   **`LANGUAGE SQL`:** the body is parsed and re-deparsed via the
       same PostgreSQL-grammar parser the compiler uses for every other
       statement, then hashed. This canonicalisation absorbs whitespace,
       quote-style, and clause-formatting differences — the same
       mechanism already relied on for reconstructing the opaque-tier
       object kinds (Tablespace, FDW, Collation, etc. — see Section 25).
       On any parse failure (a body the parser rejects for any reason),
       the compiler falls back to the plain normalisation below rather
       than erroring.  A `BEGIN ATOMIC ... END` body (Section 9.1) is
       canonicalised identically — each statement between `BEGIN ATOMIC`
       and `END` is ordinary SQL, parsed/re-deparsed/hashed the same way
       a dollar-quoted `LANGUAGE SQL` body is; the two forms are
       interchangeable from the differ's perspective, only their Part 1
       spelling differs.
   -   **`LANGUAGE plpgsql`:** the body is parsed with PostgreSQL's
       PL/pgSQL grammar (as part of the full `CREATE FUNCTION`/
       `PROCEDURE` statement, since the body's parameter references
       resolve against the declared arguments).  Source line positions
       are removed from the parse tree, and every embedded SQL
       expression and statement is re-parsed and re-deparsed, before
       hashing.  This absorbs whitespace, blank-line, and comment
       differences both in the control-flow structure and inside
       expressions (`a||b` versus `a || b`).  If parsing fails, the
       affected fragment, or the whole body, falls back to the plain
       normalisation below; canonicalisation never blocks a build.
   -   **Every other language** (`c`, `internal`, and any procedural-
       language extension other than `plpgsql`): plain text
       normalisation only —
       1.  Stripping leading and trailing whitespace from the body text.
       2.  Collapsing all internal runs of whitespace (spaces, tabs,
           newlines) to a single space character.

   Any change to the body that survives normalisation — including,
   for languages with no canonicalisation, changes that are cosmetic but
   not reducible to whitespace alone (comment wording, quote style,
   capitalisation) — changes the hash and causes the compiler to emit:

   ```sql
   CREATE OR REPLACE FUNCTION schema.name(...) RETURNS ... AS $$...$$;
   ```

   No semantic analysis of procedural code is performed for any
   language. For `LANGUAGE SQL`, syntactic reformulations that
   re-deparse identically are absorbed by the canonicalisation above;
   genuinely equivalent but differently-*structured* SQL (e.g. a
   rewritten join order) is still detected as changed, by design — this
   is syntactic canonicalisation, not semantic equivalence. The same
   holds for `LANGUAGE plpgsql`: rewriting `a OR b` as the equivalent
   `NOT (NOT a AND NOT b)` is detected as a change. A fragment shape the
   parser does not recognise keeps its raw text. For every other language, no
   canonicalisation is performed at all: the compiler does not detect
   semantically (or even cosmetically, beyond whitespace) equivalent
   reformulations.

   **Function signature changes** (argument types, return type,
   language, `SECURITY DEFINER`, `STRICT`, any attribute) are handled
   as follows:

   -   Changes to attributes that `CREATE OR REPLACE FUNCTION` can
       update (`LANGUAGE`, `SECURITY DEFINER`, `STRICT`, `VOLATILE`/
       `STABLE`/`IMMUTABLE`, `LEAKPROOF`, `PARALLEL`, `COST`, `ROWS`,
       `SET` options): emit `CREATE OR REPLACE FUNCTION` — Safety
       `SAFE`.  (PostgreSQL requires a superuser to set `LEAKPROOF`.)

   -   Changes to the argument list or return type: PostgreSQL does
       not support `CREATE OR REPLACE` for these.  The compiler emits
       `DROP FUNCTION IF EXISTS` followed by `CREATE FUNCTION` —
       classified as `DESTRUCTIVE`.

---

## 10. Sequences

   Sequences are schema-level objects used for auto-incrementing values
   not backed by `GENERATED AS IDENTITY` or `SERIAL`.

   **PG equivalent:**
   `CREATE [UNLOGGED] SEQUENCE name [AS type] [INCREMENT BY n] [MINVALUE n] [MAXVALUE n] [START WITH n] [CACHE n] [CYCLE|NO CYCLE] [OWNED BY {table.col|NONE}]`

```abnf
sequence-decl  = [ "UNLOGGED" WSP ] "SEQUENCE" WSP schema-name
                 *( sequence-option )
                 ";"
                 [ "{" sequence-block "}" ]

sequence-option = "AS" WSP seq-type
                / "INCREMENT BY" WSP integer
                / "MINVALUE" WSP integer / "NO MINVALUE"
                / "MAXVALUE" WSP integer / "NO MAXVALUE"
                / "START WITH" WSP integer
                / "CACHE" WSP integer
                / "CYCLE" / "NO CYCLE"
                / "OWNED BY" WSP ( table-col-ref / "NONE" )
                / "RESTART" [ WSP "WITH" WSP integer ]

seq-type        = "SMALLINT" / "INTEGER" / "BIGINT"

sequence-block  = *( ( owner-dir / comment-dir / grants-block
                      / revocations-block ) ";" )
```

   **`UNLOGGED`:** Like `UNLOGGED TABLE` (Section 7.12), trades crash-safety
   (the sequence's current value is not WAL-logged) for reduced write
   overhead — a genuinely different tradeoff axis from the temp-table
   session-scoping exclusion (Section 7.12), and available on any PostgreSQL
   version this document targets (Section 1.4).  Toggling it after creation
   follows the same `ALTER SEQUENCE name SET LOGGED/UNLOGGED` path as
   tables, `CAUTION`.

   **`OWNED BY NONE`:** Explicitly detaches the sequence from any
   column, distinct from never having declared `OWNED BY` at all (which
   simply leaves the sequence's existing ownership, if any, untouched).
   Diffed like any other option change — see below.

   **`RESTART [WITH n]`:** Unlike every other `sequence-option`, this
   does not describe persistent, comparable state — real PostgreSQL's
   `RESTART` is an imperative action (reset the sequence's *current*
   value now) that leaves no queryable "current RESTART value" for a
   later `plan` to diff against; `nextval()` calls immediately begin
   moving the value away from `n` again.  The compiler therefore does
   NOT persist `RESTART`'s argument in the snapshot the way it persists
   `START WITH`/`INCREMENT BY`/etc.  Instead, `RESTART`'s mere presence
   in the desired source unconditionally emits `ALTER SEQUENCE name
   RESTART [WITH n]` on every `plan`/`apply` for as long as it remains
   declared — Safety `CAUTION`, transactional, followed by a `SAFE`
   instruction-only note
   recommending the directive be removed from source once the reset has
   been applied, the same one-shot-then-remove usage pattern real
   PostgreSQL's own documentation recommends for `RESTART` itself.

   Sequences accept `GRANTS { }` and `REVOKES { }` blocks, identical in
   shape to a table's (Section 7.10).

   **Rule:** Sequences backing `GENERATED AS IDENTITY` or `SERIAL`
   columns are managed automatically by PostgreSQL and MUST NOT be
   declared in DPG.  The compiler MUST emit a warning (lint rule
   `serial_sequence_declared`) if a declared sequence name matches
   the pattern `<table>_<col>_seq` and a column with that identity
   pattern exists.

   Example:

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

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New sequence | `CREATE SEQUENCE ...` | `SAFE` |
   | Increment/min/max/cache/cycle changed | `ALTER SEQUENCE name ...` | `SAFE` |
   | `OWNED BY` changed (incl. to/from `NONE`) | `ALTER SEQUENCE name OWNED BY {table.col\|NONE}` | `SAFE` |
   | `RESTART [WITH n]` present in source | `ALTER SEQUENCE name RESTART [WITH n]` (re-emitted every plan/apply while present, see above) | `CAUTION` |
   | `UNLOGGED` toggled | `ALTER SEQUENCE name SET LOGGED/UNLOGGED` | `CAUTION` |
   | `AS type` changed | `DROP SEQUENCE; CREATE SEQUENCE` | `DESTRUCTIVE` |
   | Sequence removed | `DROP SEQUENCE name` | `DESTRUCTIVE` |

---

## 11. Access Control

### 11.1. Roles

   Roles are cluster-level objects declared in `.dpg` files inside the
   cluster objects directory.  [PG] Every role attribute is PostgreSQL's
   own `CREATE ROLE` option, written in Part 1.  [DPG] The `{ }` block
   holds the role's comment, rename, per-database settings, and
   membership modifiers.

   **PG equivalent:**
   `CREATE ROLE name [option [...]]`

```abnf
role-decl   = "ROLE" WSP identifier
             *( WSP role-option )
             ";"
             [ "{" role-block "}" ]

role-option = "LOGIN" / "NOLOGIN"
            / "SUPERUSER" / "NOSUPERUSER"
            / "CREATEDB" / "NOCREATEDB"
            / "CREATEROLE" / "NOCREATEROLE"
            / "INHERIT" / "NOINHERIT"
            / "REPLICATION" / "NOREPLICATION"
            / "BYPASSRLS" / "NOBYPASSRLS"
            / "CONNECTION" WSP "LIMIT" WSP integer
            / "PASSWORD" WSP SQUOTE password-literal SQUOTE
            / "VALID" WSP "UNTIL" WSP SQUOTE timestamp SQUOTE
            / "IN" WSP "ROLE" WSP membership-list
            / "ROLE" WSP membership-list
            / "ADMIN" WSP membership-list

; Per-membership modifiers: ADMIN (all versions), INHERIT and SET
; (PostgreSQL 16+).
membership-list = membership-item *( "," WSP membership-item )
membership-item = identifier *( WSP "WITH" WSP membership-opt )
membership-opt  = "ADMIN" WSP ( "OPTION" / boolean )
                 / "INHERIT" WSP boolean
                 / "SET" WSP boolean

role-block  = *( ( comment-dir / renamed-from-dir
                  / role-config-dir ) ";" )

; ALTER ROLE ... [IN DATABASE db] SET param {TO|=} value / SET param
; FROM CURRENT / RESET param / RESET ALL — a real, common need (e.g.
; per-role statement_timeout) with no prior grammar slot at all.
role-config-dir = "SET" WSP identifier WSP ( "TO" / "=" ) WSP config-value
                    [ WSP "IN DATABASE" WSP identifier ]
                / "SET" WSP identifier WSP "FROM CURRENT"
                    [ WSP "IN DATABASE" WSP identifier ]
                / "RESET" WSP ( identifier / "ALL" )
                    [ WSP "IN DATABASE" WSP identifier ]

config-value = string-literal / integer / boolean / identifier
```

   Any option a declaration omits is simply not managed by DPG for that
   role — offline diffing only ever compares options the source explicitly
   sets (the same "declared, so managed" convention already used elsewhere,
   e.g. Sequence's optional params), never PostgreSQL's own defaults for
   whatever was left unstated.

   `password-literal` is an ordinary string, optionally containing one or
   more `{{<secret-uri>}}` placeholders — the exact same mechanism as
   SUBSCRIPTION `CONNECTION` (Section 13.2, Appendix D.1): each placeholder is resolved
   independently at apply time (Appendix D.1) and
   substituted in place (the whole value, or just a fragment); a literal
   with no `{{...}}` at all never touches the resolver. Resolution happens
   once, immediately before the `CREATE ROLE`/`ALTER ROLE ... PASSWORD`
   statement executes — never during `plan`/`diff`.

   **Hardcoded passwords:** The linter MUST emit an error (not a warning)
   when `PASSWORD` is set to a value with no `{{...}}` placeholder at all
   and `forbid_hardcoded_passwords` is enabled (default: `true`).

   **Password drift detection:** the snapshot stores a hash of the
   *declared* `PASSWORD` text (the literal or `{{...}}` reference exactly
   as written, trimmed, hashed — never the resolved value), the same
   pattern SUBSCRIPTION `CONNECTION`'s body-hash diffing uses.  Editing
   the `{{...}}` reference or the literal is therefore detected as a
   change.  Hashing exposes nothing the source file does not already
   contain.

   `PASSWORD` is never introspected: PostgreSQL shows stored passwords
   only to superusers. Every other attribute below introspects normally
   (plain, unrestricted `pg_roles`/`pg_auth_members` columns).

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
    VALID UNTIL '2030-01-01';

ROLE app_admin
    LOGIN
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    INHERIT
    IN ROLE pg_read_all_stats;
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New role | `CREATE ROLE name <options>` | `SAFE` |
   | `LOGIN`/`SUPERUSER`/`CREATEDB`/`CREATEROLE`/`INHERIT`/`REPLICATION`/`BYPASSRLS`/`CONNECTION LIMIT`/`VALID UNTIL` changed | `ALTER ROLE name <changed-options>` | `SAFE` |
   | `PASSWORD` changed | `ALTER ROLE name PASSWORD '<resolved>'` | `CAUTION` (invalidates existing sessions/connections relying on the old password) |
   | `IN ROLE`/`ROLE`/`ADMIN` membership added | `GRANT role TO member [WITH ADMIN OPTION] [WITH INHERIT bool] [WITH SET bool]` | `SAFE` |
   | Membership `WITH` option changed, membership itself unchanged | `GRANT role TO member WITH <changed-option>` (PostgreSQL treats a repeated `GRANT` as updating the option) | `SAFE` |
   | `IN ROLE`/`ROLE`/`ADMIN` membership removed | `REVOKE role FROM member` | `CAUTION` (may remove access something else depends on) |
   | `SET`/`RESET` config parameter changed (`role-config-dir`) | `ALTER ROLE name [IN DATABASE db] SET param {TO\|=} value` / `RESET param` / `RESET ALL` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER ROLE old RENAME TO new` | `CAUTION` |
   | Role removed | `DROP ROLE name` | `DESTRUCTIVE` |

   **`RENAMED FROM` on a role** is schema-agnostic — roles are cluster-
   level, not schema-scoped, so `renamed-from-dir`'s schema-qualification
   extension (Section 7.6) never applies here; only the bare `RENAME TO` form
   is meaningful.  Reason: PostgreSQL refuses to drop a role that owns
   objects, so a role cannot be renamed by dropping and recreating it.

   **`ALTER GROUP ... ADD USER ... [ADMIN OPTION]`** (PostgreSQL 16
   added `ADMIN OPTION` to it) is `ALTER GROUP`'s own legacy spelling of
   role-membership `GRANT`/`REVOKE` — real PostgreSQL treats `ROLE`,
   `USER`, and `GROUP` as fully interchangeable since 8.1, and `ALTER
   GROUP name ADD USER member [WITH ADMIN OPTION]` is documented as
   exactly equivalent to `GRANT name TO member [WITH ADMIN OPTION]`.
   `membership-list`'s `WITH ADMIN`/`INHERIT`/`SET` grammar above
   already covers everything `ALTER GROUP` can express — confirmed not
   a gap, not merely assumed.

   `IN ROLE`/`ROLE`/`ADMIN` are create-time-only PostgreSQL grammar — a
   later membership change has no `ALTER ROLE` equivalent, so it's diffed
   as `GRANT`/`REVOKE` (PostgreSQL's own mechanism for changing membership
   after creation), reusing the same DDL vocabulary as Section 11.2's
   object-level privilege grants rather than inventing new DDL shape
   for this. The *semantics* differ deliberately, though: unlike Section
   11.2's additive model (removing a grant declaration emits nothing),
   removing a role-membership declaration emits an actual `REVOKE` (the
   table above, "membership removed" row) — membership is treated as
   an ordinary, fully-synced property of the role's own declaration,
   the same as `LOGIN`/`SUPERUSER`/`INHERIT`/etc. in the row above it,
   not as a grant on some other object a role might also be
   independently given access to outside DPG's management.

### 11.2. Grants — The Additive Model

   DPG follows PostgreSQL's own additive privilege model:

   **Declaring a grant emits a `GRANT` statement.**

   **Removing a grant declaration emits nothing.** If revocation is
   intended, an explicit `REVOKES { }` entry MUST be added.

   This is a deliberate design choice.  DPG does not attempt to manage
   the full privilege graph.  It only tracks the grants it declared and
   ensures they are present.  Grants applied by other means (e.g., by
   a DBA directly) are not disturbed.

   `dpg verify` reports as drift any DPG-declared grant that is absent
   from the live catalog.  It does NOT report extra grants present in
   the live catalog but absent from DPG source.

   **`WITH GRANT OPTION`:** Including `WITH GRANT OPTION` in a
   `GRANTS { }` entry causes the compiler to emit
   `GRANT ... TO role WITH GRANT OPTION`.  Removing `WITH GRANT OPTION`
   while keeping the grant emits nothing (removing the grant option
   requires an explicit revoke-and-regrant cycle which the operator
   SHOULD perform manually).

   **Privilege list is untyped, by design — an accepted offline
   limitation:** `privilege` (Appendix A) is a single flat union shared
   verbatim across every object kind's `GRANTS { }`/`REVOKES { }`
   block, not PostgreSQL's real per-object-type privilege subsets
   (e.g. `TABLE` accepts `SELECT`/`INSERT`/`UPDATE`/`DELETE`/
   `TRUNCATE`/`REFERENCES`/`TRIGGER`/`MAINTAIN`; `SEQUENCE` accepts only
   `USAGE`/`SELECT`/`UPDATE`; `SCHEMA` only `CREATE`/`USAGE`; `FUNCTION`/
   `PROCEDURE` only `EXECUTE`; and so on).  Every real privilege word
   PostgreSQL has is present in the union, so this is not a Tenet-1
   expressiveness gap — nothing is unexpressable — but DPG offers no
   offline validation contract catching "wrong privilege for this
   object kind" (e.g. `EXECUTE ON TABLE`) at `plan` time; such a
   mistake surfaces only as a PostgreSQL parse/grammar error at `apply`
   time, the same passthrough-to-PostgreSQL's-own-parser principle used
   throughout this document for clause-combination validation generally
   (e.g. Section 7.9's `REFERENCING` note).  Stated here explicitly per
   Tenet 3, rather than left as a silent gap.

### 11.3. Revocations

   Explicit revocations are declared in `REVOKES { }` blocks and
   cause the compiler to emit `REVOKE` statements.

   `REVOKE` on an already-absent privilege is a no-op in PostgreSQL —
   it succeeds without error, unlike a grant declaration's removal
   (Section 11.2), which is the additive model's deliberate no-op.  PostgreSQL's
   `REVOKE` grammar has no `IF EXISTS` clause at all, so the compiler
   emits plain `REVOKE ... FROM role`; there is no guard to add or omit.

   The one revocation failure mode that does error is a target role
   that does not exist — the compiler does not create roles referenced
   only in a `REVOKES { }` block, so a typo'd or since-dropped role
   name surfaces as a normal PostgreSQL error at apply time.

   **However**, the compiler SHOULD check the snapshot: if the
   revocation targets a role that was never granted the privilege by
   DPG, it MAY emit a warning (lint rule: `unnecessary_revocation`) —
   useful as a "did you mean to declare this" signal, independent of
   whether re-running it would error.

### 11.4. Default Privileges

   Default privileges apply to future objects created by a role.

   **PG equivalent:**
   `ALTER DEFAULT PRIVILEGES [FOR ROLE role] [IN SCHEMA schema] GRANT ... / REVOKE ...`

```abnf
default-privileges-decl =
    "DEFAULT PRIVILEGES"
    [ WSP "FOR ROLE" WSP identifier ]
    [ WSP "IN SCHEMA" WSP identifier ]
    "{" dp-block "}"

dp-block = *( ( grants-block / revocations-block ) ";" )
```

   Inside the `GRANTS { }` / `REVOKES { }` sub-blocks, the object
   type is specified with an `ON` clause:

```sql
GRANTS {
    SELECT   ON TABLES    TO app_readonly;
    EXECUTE  ON FUNCTIONS TO app_service;
    USAGE    ON SEQUENCES TO app_service;
}
```

   Example:

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

   Emits:

```sql
ALTER DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public
    GRANT SELECT ON TABLES TO app_readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public
    GRANT EXECUTE ON FUNCTIONS TO app_service;
ALTER DEFAULT PRIVILEGES FOR ROLE app_admin IN SCHEMA public
    GRANT USAGE ON SEQUENCES TO app_service;
```

### 11.5. Owner Impersonation at Object Creation

   PostgreSQL attributes default-privilege eligibility (Section 11.4) to
   whichever role actually *executed* the `CREATE` statement — checked
   against `pg_default_acl` via `current_user`/`current_role` — not to
   an object's final `OWNER`.  A declared `OWNER` (`owner-dir`; see
   Section 7.11 for Table's copy of this directive — every object kind that
   supports `OWNER` defines its own local copy of the same grammar
   rather than sharing one central production) that is applied only
   *after* creation, via a trailing `ALTER ... OWNER TO`, therefore
   never satisfies a matching `DEFAULT PRIVILEGES FOR ROLE` block: the
   role named in `FOR ROLE` never itself ran `CREATE`, so PostgreSQL
   never consults its default-ACL entries for that object.

   To make `OWNER` and `DEFAULT PRIVILEGES FOR ROLE` compose correctly,
   the compiler MUST create every object that declares an `OWNER`
   (`owner-dir`, per object kind — see the note above) as that role,
   not as the connecting role. This is universal — it
   applies to any object with a declared `OWNER`, not only objects
   covered by a matching `DEFAULT PRIVILEGES FOR ROLE` block — because it
   simply matches real PostgreSQL creator semantics: the role that
   creates an object is that object's owner.

   **Execution model.** Each such object's `CREATE` statement (and, for
   an object whose `CREATE` MUST run outside a transaction — e.g.
   `TABLESPACE`, Section 14.7 — the standalone statement) is wrapped as:

```sql
SET ROLE app_admin;
CREATE TABLE ...;
RESET ROLE;
```

   `SET ROLE` is used rather than `SET SESSION AUTHORIZATION`: it only
   requires the connecting role to be a *member* of the target role (not
   superuser), and it does not change `session_user` — the real
   connecting identity is preserved for the audit trail, and only the
   privilege-checking `current_user` changes for the wrapped statement.

   Once an object exists, reassigning its owner continues to use
   `ALTER ... OWNER TO` exactly as before (Section 11.1, and per-object-kind
   diffing sections) — an existing object's ownership change does not
   affect default-privilege attribution for objects already created, so
   it needs no `SET ROLE` wrapping.

   **Pre-flight membership validation.** Before the apply transaction
   opens any DDL, the compiler MUST check, for every distinct role named
   in an `OWNER` directive anywhere in the pending migration, that the
   connecting role is a member of it (`pg_has_role(current_user, owner,
   'MEMBER')`). If any declared `OWNER` fails this check, `apply` MUST
   abort with error **DPG-E036** before executing any statement in the
   migration, naming every role that failed the check in a single error
   — not a bare PostgreSQL "permission denied to set role" surfacing
   mid-transaction on whichever object happens to hit it first.

### 11.6. Parameter Privileges

   PostgreSQL 15+ has a distinct grantable-privilege object type for
   individual GUC configuration parameters, letting an admin delegate
   the ability to change a specific setting without granting broader
   (superuser-adjacent) rights. This is a separate concern from `ALTER
   SYSTEM` the *command* remaining out of scope (Section 23): DPG never emits
   `ALTER SYSTEM` itself, but the *privilege* to run it on a named
   parameter is an ordinary grantable object like any other, and this
   document's own reasoning for excluding `ALTER SYSTEM` (a cluster-
   configuration action, not a schema object) does not extend to who is
   *permitted* to run it — that permission is exactly the kind of
   access-control fact this section already manages for every other
   object kind.

   **PG equivalent:**
   `GRANT {SET | ALTER SYSTEM} ON PARAMETER param_name [, ...] TO role [, ...] [WITH GRANT OPTION]`

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
```

   A top-level (cluster-scoped, not schema-scoped) block — configuration
   parameters have no schema.

```sql
-- production/cluster/parameter_privileges.dpg

PARAMETER PRIVILEGES {
    GRANTS {
        SET ON PARAMETER work_mem, statement_timeout TO app_admin;
    }
}
```

   Emits `GRANT SET ON PARAMETER work_mem, statement_timeout TO
   app_admin`.

   **Diffing semantics:** Same additive model as Table-level grants
   (Section 11.2) — a declared grant emits `GRANT`; removing the declaration
   emits nothing (an explicit `REVOKES { }` entry is required to
   actually `REVOKE`). Safety `SAFE` for both directions — this is
   metadata governing who *may* run a command, not an object with data
   of its own.

---

---

## 12. Full-Text Search Objects

### 12.1. Text Search Configurations

   Text search configurations define how documents are parsed and
   tokenised.  The `MAPPING FOR` sub-block is a declarative way to
   set token mappings; the compiler emits
   `ALTER TEXT SEARCH CONFIGURATION ... ALTER MAPPING FOR ...`.

   **PG equivalent:**
   `CREATE TEXT SEARCH CONFIGURATION name (COPY = source [, PARSER = parser])`

```abnf
tsconfig-decl = "TEXT SEARCH CONFIGURATION" WSP schema-name
                WSP "(" tsconfig-opts ")"
                [ "{" tsconfig-block "}" ]
                ";"

tsconfig-opts  = "COPY" WSP "=" WSP qual-name
               / "PARSER" WSP "=" WSP qual-name

tsconfig-block = *( ( comment-dir / owner-dir / renamed-from-dir / mapping-dir ) ";" )

mapping-dir = "MAPPING FOR" WSP token-type-list
              WSP "WITH" WSP dict-list
            / "MAPPING FOR" WSP token-type-list
              WSP "REPLACE" WSP qual-name WSP "WITH" WSP qual-name
            / "MAPPING" WSP "REPLACE" WSP qual-name WSP "WITH" WSP qual-name
```

   **`REPLACE`** substitutes one dictionary for another within a
   mapping without restating the rest of that mapping's dictionary
   chain — `MAPPING FOR token-types REPLACE old WITH new` scopes the
   substitution to the named token types; bare `MAPPING REPLACE old
   WITH new` (no `FOR`) applies it across every token type's mapping at
   once, matching real PostgreSQL's two `ALTER ... ALTER MAPPING`
   forms exactly. Functionally reachable via the plain `WITH dict-list`
   form too (fully restating the desired dictionary chain already lets
   the differ compute the same end state) — `REPLACE` exists as the
   more targeted native PostgreSQL statement for the common "swap one
   dictionary everywhere" case.

   **`RENAMED FROM`** (`renamed-from-dir`, Section 7.6): a Configuration
   MAY carry the same generic cross-schema `SET SCHEMA` extension as
   every other schema-scoped kind — real PostgreSQL supports `ALTER
   TEXT SEARCH CONFIGURATION ... RENAME TO`/`SET SCHEMA` identically to
   Dictionary, Parser, and Template (Sections 12.2-12.4).

   Example:

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
   | New config | `CREATE TEXT SEARCH CONFIGURATION ...` | `SAFE` |
   | Mapping added/changed | `ALTER TEXT SEARCH CONFIGURATION ... ALTER MAPPING FOR ...` | `SAFE` |
   | Mapping removed | `ALTER TEXT SEARCH CONFIGURATION ... DROP MAPPING FOR ...` | `SAFE` |
   | `REPLACE` form declared | `ALTER TEXT SEARCH CONFIGURATION ... ALTER MAPPING [FOR token-types] REPLACE old WITH new` | `SAFE` |
   | Owner changed | `ALTER TEXT SEARCH CONFIGURATION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER TEXT SEARCH CONFIGURATION old RENAME TO new` | `CAUTION` |
   | Moved to another schema (`RENAMED FROM` schema-qualified, Section 7.6) | `ALTER TEXT SEARCH CONFIGURATION old_schema.name SET SCHEMA new_schema` | `SAFE` |
   | Config removed | `DROP TEXT SEARCH CONFIGURATION name` | `DESTRUCTIVE` |

### 12.2. Text Search Dictionaries

   **PG equivalent:**
   `CREATE TEXT SEARCH DICTIONARY name (TEMPLATE = tmpl, [options])`

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

   Unlike Text Search Parser/Template (Sections 12.3/12.4), a Dictionary MAY
   carry an optional `{ }` block (`owner-dir`/`comment-dir`/
   `renamed-from-dir`, the same generic cross-schema `SET SCHEMA`
   extension as every other kind, Section 7.6) — real PostgreSQL supports
   `ALTER TEXT SEARCH DICTIONARY ... OWNER TO`/`RENAME TO`/`SET SCHEMA`
   for Dictionary but has no `OWNER` concept at all for Parser/Template.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Option (non-`TEMPLATE`) added/changed/removed | `ALTER TEXT SEARCH DICTIONARY name (key [= value], ...)` | `SAFE` |
   | `TEMPLATE` changed | `DROP TEXT SEARCH DICTIONARY` + recreate | `DESTRUCTIVE` |
   | Owner changed | `ALTER TEXT SEARCH DICTIONARY name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER TEXT SEARCH DICTIONARY old RENAME TO new` | `CAUTION` |
   | Dictionary removed | `DROP TEXT SEARCH DICTIONARY name` | `DESTRUCTIVE` (if in use) |

   `TEMPLATE` is fixed at creation, same as a base type's core
   properties (Section 5.5) — real PostgreSQL's `ALTER TEXT SEARCH DICTIONARY`
   can change any other option in place (add, change, or remove —
   `DROP` an option by naming it with no value) without a rebuild; only
   a `TEMPLATE` change forces drop-and-recreate.

### 12.3. Text Search Parsers

   Text search parsers are low-level objects, typically installed via
   an extension.  Explicit declaration is provided for completeness.

   **PG equivalent:**
   `CREATE TEXT SEARCH PARSER name (START = func, GETTOKEN = func, END = func, LEXTYPES = func [, HEADLINE = func])`

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

   Real PostgreSQL has no `OWNER` concept for Parser at all — the `{ }`
   block accepts only `comment-dir`/`renamed-from-dir`.  `SET SCHEMA` is
   unrelated to ownership and still applies via a schema-qualified
   `RENAMED FROM` (Section 7.6) — real PostgreSQL supports moving a Parser
   between schemas despite it having no owner-based ACL surface at all.

   Any change to a parser requires drop + recreate (`DESTRUCTIVE`).
   `COMMENT`/`RENAMED FROM` changes are `SAFE`/`CAUTION` respectively.

### 12.4. Text Search Templates

   **PG equivalent:**
   `CREATE TEXT SEARCH TEMPLATE name ([INIT = func,] LEXIZE = func)`

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

   Same `{ }` block rules as Parser above (`comment-dir`/
   `renamed-from-dir`, no `owner-dir` — real PostgreSQL has no `OWNER`
   concept for Template either).

   Any change to a template requires drop + recreate (`DESTRUCTIVE`).

---

## 13. Logical Replication

### 13.1. Publications

   Publications are database-level objects.  The Part 1 body follows
   PostgreSQL's `CREATE PUBLICATION` syntax exactly.

   **PG equivalent:**
   `CREATE PUBLICATION name [FOR TABLE table[, ...] | FOR ALL TABLES | FOR TABLES IN SCHEMA schema[, ...]] [WITH (options)]`

```abnf
publication-decl = "PUBLICATION" WSP identifier
                   WSP publication-scope
                   [ WSP "WITH" WSP "(" pub-options ")" ]
                   ";"
                   [ "{" pub-block "}" ]

publication-scope = "FOR ALL TABLES"
                  / "FOR TABLE" WSP pub-table-list
                  / "FOR TABLES IN SCHEMA" WSP schema-list

pub-table-list    = pub-table *( "," WSP pub-table )
pub-table         = schema-table-name
                    [ "(" col-list ")" ]
                    [ "WHERE" WSP "(" predicate ")" ]

; pub-options (defined in Appendix A alongside storage-params/sub-options)
; mirrors real CREATE PUBLICATION's WITH (...) clause verbatim — an
; opaque key=value option list handed through as native PG text per
; Tenet 2/5, so e.g. publish, publish_via_partition_root, and any later
; PostgreSQL version's additions such as PG18's
; publish_generated_columns are automatically expressible with zero
; grammar changes here, the same passthrough pattern Subscription's
; WITH options (Section 13.2) already uses.

pub-block = *( ( comment-dir / owner-dir / renamed-from-dir ) ";" )
```

   `owner-dir`/`renamed-from-dir` follow the standard rules (Section 7.6):
   `ALTER PUBLICATION name OWNER TO role` (`SAFE`); `ALTER PUBLICATION
   old RENAME TO new` (`CAUTION`) — Publication is database-level, not
   schema-scoped (like Role and Event Trigger, Sections 11.1/14.1), so the
   cross-schema `SET SCHEMA` half of the generic extension never
   applies here.  A Publication has no `GRANTS`/`REVOKES` directive,
   because PostgreSQL has no privileges on publications.

   Examples:

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

   Subscriptions are database-level objects, declared and diffed as an
   opaque body (byte-for-byte source text, not per-field) — the same tier
   as Tablespace/FDW/Server/Publication/etc. `CONNECTION` is the one part
   of that body DPG treats specially, to support a secret reference
   instead of a literal credential (see below and Appendix D.1); `COMMENT` (in the
   `{ }` block) is diffed and applied like every other Comment-bearing
   object.

   `dump`/`verify`/`plan --live` see every Subscription attribute except
   `CONNECTION` itself: `pg_subscription.subconninfo` has no default grant
   to PUBLIC (PostgreSQL revokes it from a normal caller outright), and
   even a privileged caller who *can* read it has no way to recover
   whatever `{{secret-uri}}` the original `CONNECTION` clause held, if
   any — the same inherent limitation User Mapping `OPTIONS` (Section 14.10, Section 24)
   has on recovering its original reference, though User Mapping's
   redaction works field-by-field rather than by omitting a whole column,
   since `OPTIONS` also carries non-sensitive keys `dump` must still
   reconstruct. An introspected Subscription's `CONNECTION` is rendered
   as a fixed,
   syntactically-valid-but-inert placeholder conninfo instead (with
   `connect = false`, `create_slot = false`, and `enabled = false` always
   forced in its `WITH` clause, since the placeholder can never actually
   be dialed); a `dump`'d Subscription must have its `CONNECTION`
   hand-edited back to a real value before it does anything. Because the
   reconstructed body's `BodyHash` is never stored (same as every other
   reconstructed opaque kind, Section 25), this placeholder never causes a
   spurious `DROP SUBSCRIPTION` + `CREATE SUBSCRIPTION` loop on
   `verify`/`plan --live` — introspecting at all is what makes an
   already-applied Subscription visible as existing, rather than
   `plan --live` proposing a spurious re-`CREATE` for one that's already
   there (which would then error on `apply`).

   **PG equivalent:**
   `CREATE SUBSCRIPTION name CONNECTION 'connstr' PUBLICATION pub [, ...] [WITH (options)]`

```abnf
subscription-decl = "SUBSCRIPTION" WSP identifier
                    WSP "CONNECTION" WSP SQUOTE conn-literal SQUOTE
                    WSP "PUBLICATION" WSP identifier-list
                    [ WSP "WITH" WSP "(" sub-options ")" ]
                    ";"
                    [ "{" subscription-block "}" ]

subscription-block = *( ( comment-dir / owner-dir / renamed-from-dir ) ";" )
```

   `owner-dir`/`renamed-from-dir` follow the standard rules (Section 7.6):
   `ALTER SUBSCRIPTION name OWNER TO role` (`SAFE`); `ALTER SUBSCRIPTION
   old RENAME TO new` (`CAUTION`) — Subscription is database-level, not
   schema-scoped (like Publication, Section 13.1), so the cross-schema
   `SET SCHEMA` half of the generic extension never applies here.

   `conn-literal` is an ordinary libpq conninfo string, used exactly as
   written, optionally with one or more `{{<secret-uri>}}` placeholders
   embedded in it — each resolved independently at apply time and
   substituted in place (e.g. only the password comes from a secret, the
   rest stays literal; or the whole value is one placeholder). `{{...}}` is
   the *only* mechanism that ever triggers resolution: a real conninfo
   string may itself contain a `:` (a `postgresql://user:pass@host/db` URI
   is valid libpq syntax), so nothing else is ever read as a candidate
   reference — a literal with no `{{...}}` at all never touches the
   resolver.

   Resolution happens once, immediately before executing the `CREATE
   SUBSCRIPTION` statement — never during `plan`/`diff` (offline-first is
   unaffected: a `{{...}}` reference is compared as literal text, same as
   any other body change) and never written anywhere after that: the
   snapshot, an archived migration file, and any error message all show the
   placeholder/reference form, not the resolved value. See Appendix D.1 for the
   underlying `SecretResolver`/`ResolveTemplate` mechanism, shared with
   Role `PASSWORD` (Section 11.1) and User Mapping `OPTIONS` (Section 14.10, Section 24).

   Examples:

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

   **Diffing semantics:**

   The Part 1 body (`CONNECTION`, `PUBLICATION`, `WITH` options) is diffed
   as an opaque whole: any change is a full `DROP SUBSCRIPTION` +
   `CREATE SUBSCRIPTION`, not a targeted `ALTER SUBSCRIPTION`, matching
   every other opaque-tier object kind (Section 25). `COMMENT` is diffed
   separately at the field level — a comment-only edit emits a plain
   `COMMENT ON SUBSCRIPTION`, not a drop-and-recreate, so it never
   interrupts an already-syncing subscription.

   PostgreSQL refuses `CREATE SUBSCRIPTION` (with its default
   `create_slot = true`) and `DROP SUBSCRIPTION` inside a transaction
   block, so both are non-transactional (Section 17.3).  A new
   subscription's comment and security labels are non-transactional too,
   so they run after the `CREATE` they depend on.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New subscription | `CREATE SUBSCRIPTION ...` [+ `COMMENT ON SUBSCRIPTION ...` if set] | `SAFE`, non-transactional |
   | Body change (`CONNECTION`, `PUBLICATION`, or `WITH`) | `DROP SUBSCRIPTION ...` + `CREATE SUBSCRIPTION ...` [+ `COMMENT ...`] | `DESTRUCTIVE` then `SAFE`, both non-transactional |
   | Comment-only change | `COMMENT ON SUBSCRIPTION ... IS ...` (or `IS NULL`) | `SAFE` |
   | Owner changed | `ALTER SUBSCRIPTION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER SUBSCRIPTION old RENAME TO new` | `CAUTION` |
   | Subscription removed | `DROP SUBSCRIPTION name` | `DESTRUCTIVE` (non-transactional) |

---

## 14. Advanced PostgreSQL Objects

### 14.1. Event Triggers

   Event triggers fire on DDL events cluster-wide.  They are database-
   level objects (not cluster-level).

   **PG equivalent:**
   `CREATE EVENT TRIGGER name ON event [WHEN TAG IN ('tag', ...)] EXECUTE FUNCTION func()`

```abnf
event-trigger-decl = "EVENT TRIGGER" WSP identifier
                     WSP "ON" WSP event-type
                     [ WSP "WHEN TAG IN" WSP "(" tag-list ")" ]
                     WSP "EXECUTE FUNCTION" WSP func-ref "()"
                     [ WSP trigger-enable-state ]
                     ";"
                     [ "{" event-trigger-block "}" ]

event-type = "login" / "ddl_command_start" / "ddl_command_end" /
             "table_rewrite" / "sql_drop"

; trigger-enable-state (DISABLED/ENABLE REPLICA/ENABLE ALWAYS, Section 7.9) is
; reused verbatim — real PostgreSQL's ALTER EVENT TRIGGER enable-state
; grammar is identical to ALTER TABLE ... ENABLE/DISABLE TRIGGER's,
; just against a different target object (no table name, since event
; triggers are database-level, not per-table).

event-trigger-block = *( ( comment-dir / owner-dir / renamed-from-dir ) ";" )
```

   [PG] **`login`** (PostgreSQL 17+) fires once per authenticated
   connection. `WHEN TAG IN (...)` does not apply to `login` (there is
   no command tag to filter on) — left to PostgreSQL's own parser to
   reject the combination, per the passthrough principle used
   throughout this document.

   [DPG] `WHEN TAG IN (...)` accepts any command tag as a string, so
   tags added by later PostgreSQL releases (such as `'REINDEX'`, 17+)
   need no grammar change.

   Example:

```sql
EVENT TRIGGER prevent_drop_table
    ON sql_drop
    WHEN TAG IN ('DROP TABLE', 'DROP SCHEMA')
    EXECUTE FUNCTION abort_drop();

EVENT TRIGGER audit_ddl
    ON ddl_command_end
    EXECUTE FUNCTION log_ddl_command()
    ENABLE REPLICA
    {
        OWNER audit_admin;
    }
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New event trigger | `CREATE EVENT TRIGGER ...` | `SAFE` |
   | Event/`WHEN TAG IN`/function changed | `DROP EVENT TRIGGER` + `CREATE EVENT TRIGGER` | `SAFE` (no data involved) |
   | Enable state changed only (`trigger-enable-state`) | `ALTER EVENT TRIGGER name ENABLE/DISABLE` (or `ENABLE REPLICA`/`ENABLE ALWAYS`) | `SAFE` |
   | Owner changed | `ALTER EVENT TRIGGER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER EVENT TRIGGER old RENAME TO new` | `CAUTION` |
   | Event trigger dropped | `DROP EVENT TRIGGER name` | `SAFE` |

   Event triggers are database-level, not schema-scoped — `renamed-
   from-dir`'s generic cross-schema `SET SCHEMA` extension (Section 7.6) never
   applies here, since there is no schema to move between; only the
   bare (unqualified) rename form is meaningful, same as Role (Section 11.1).

### 14.2. Collations

   **PG equivalent:**
   `CREATE COLLATION [IF NOT EXISTS] name { (LOCALE = locale | LC_COLLATE = lc, LC_CTYPE = lc | PROVIDER = provider [, DETERMINISTIC = bool] [, RULES = rules]) | FROM existing_collation }`

```sql
SCHEMA public {
    COLLATION case_insensitive (
        PROVIDER      = icu,
        LOCALE        = 'und-u-ks-level2',
        DETERMINISTIC = false
    );

    -- FROM copies an existing collation's definition under a new name
    -- (e.g. a differently-cased alias of a system-provided collation).
    COLLATION case_insensitive_alias FROM case_insensitive;

    -- RULES (PostgreSQL 16+, ICU provider only): custom tailoring rules
    -- layered on top of the base locale.
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

   Since Collation is an `opaque-object-decl` kind (Part 1 passthrough,
   Appendix A), both `RULES = rules` and `FROM existing_collation`
   require no DPG-side grammar of their own — real PostgreSQL `CREATE
   COLLATION` syntax, passed to PostgreSQL's grammar unchanged like every other
   property form above.

   Collation MAY carry an optional `{ }` block (`comment-dir`/
   `owner-dir`/`renamed-from-dir`/`refresh-version-dir`) — previously
   entirely absent, silently sweeping an owner change into the generic
   "any property change → DROP+CREATE" path even though real PostgreSQL
   supports `OWNER TO`/`RENAME TO`/`SET SCHEMA` for Collation without a
   rebuild.

```abnf
; Collation-only: unlike RESTART (Sequence, Section 10), a bare presence
; keyword with no argument — REFRESH VERSION has nothing to parametrise,
; it just re-reads the OS/ICU library's *current* collation version and
; records it, the same non-persistent-target-value shape as RESTART.
refresh-version-dir = "REFRESH VERSION"
```

   **`REFRESH VERSION`** (PostgreSQL 15+): updates the catalog's
   recorded collation version to match the operating system's/ICU's
   *current* version, acknowledging an OS/library upgrade that may have
   silently changed sort order — real PostgreSQL's mitigation for a
   genuine data-corruption risk (an index built under the old collation
   version can silently misbehave after the OS collation library
   changes underneath it). Like `RESTART` (Section 10), this describes an
   imperative action, not comparable target state: its mere presence in
   source unconditionally emits `ALTER COLLATION name REFRESH VERSION`
   on every `plan`/`apply` for as long as it remains declared — Safety
   `CAUTION`, transactional, with the compiler recommending removal from source once
   applied, the same usage pattern as `RESTART`.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Owner changed | `ALTER COLLATION name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER COLLATION old RENAME TO new` | `CAUTION`; schema-qualified additionally emits `ALTER COLLATION old_schema.name SET SCHEMA new_schema` (`SAFE`) |
   | `REFRESH VERSION` present in source | `ALTER COLLATION name REFRESH VERSION` (re-emitted every apply while present, see above) | `CAUTION` |
   | Any other property change (`LOCALE`/`PROVIDER`/`RULES`/`FROM` target/etc.) | `DROP COLLATION` + `CREATE COLLATION` | `DESTRUCTIVE` (dependent objects must be dropped and recreated) |

   The `DESTRUCTIVE` row applies identically to the `FROM
   existing_collation` form: since the body is diffed as opaque text
   (Section 14's `opaque-object-decl` note), changing which collation a `FROM`
   clause points at is itself a property change.

### 14.3. Operators

   **PG equivalent:**
   `CREATE OPERATOR symbol (LEFTARG = t, RIGHTARG = t, FUNCTION = func, [COMMUTATOR = op, NEGATOR = op, RESTRICT = func, JOIN = func, HASHES, MERGES])`

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

   **Operator identity:** `(schema, symbol, leftarg_type, rightarg_type)`.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New operator | `CREATE OPERATOR ...` | `SAFE` |
   | `PROCEDURE`/`FUNCTION` changed | `DROP OPERATOR; CREATE OPERATOR` | `DESTRUCTIVE` |
   | `RESTRICT`/`JOIN` set, cleared, or changed | `ALTER OPERATOR ... SET (...)` | `SAFE` |
   | `COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` newly set (previously unset) | `ALTER OPERATOR ... SET (...)` | `SAFE` |
   | `COMMUTATOR`/`NEGATOR`/`HASHES`/`MERGES` changed once already set | `DROP OPERATOR; CREATE OPERATOR` | `DESTRUCTIVE` |
   | Operator removed | `DROP OPERATOR symbol (leftarg, rightarg)` | `DESTRUCTIVE` |

   [PG] `RESTRICT` and `JOIN` can be changed with `ALTER OPERATOR` in
   either direction.  `COMMUTATOR`, `NEGATOR`, `HASHES`, and `MERGES` can
   only go from unset to set: PostgreSQL rejects changing them once set,
   so DPG changes or clears them by dropping and recreating the
   operator.

### 14.4. Operator Classes and Families

   **PG equivalent (family):**
   `CREATE OPERATOR FAMILY name USING access_method`

   **PG equivalent (class):**
   `CREATE OPERATOR CLASS name [DEFAULT] FOR TYPE type USING access_method [FAMILY family] AS ...`

```sql
SCHEMA public {
    OPERATOR FAMILY my_family USING btree;

    OPERATOR CLASS my_ops FOR TYPE mytype USING btree FAMILY my_family AS
        OPERATOR 1 <,
        OPERATOR 2 <=,
        OPERATOR 3 =,
        OPERATOR 4 >=,
        OPERATOR 5 >,
        FUNCTION 1 mytype_cmp(mytype, mytype);
}
```

   **Diffing (operator class):** Identity is `(schema, name, access_method)`.
   The body (its `AS` member list) is diffed as normalised text
   (passthrough). Any change to the member list requires drop + recreate
   (`DESTRUCTIVE`) — PostgreSQL provides no incremental `ALTER OPERATOR
   CLASS` for its `AS` members at all, so there is no safer alternative.

   **Loose family members.** PostgreSQL separately lets an operator family
   carry members that belong to the family directly rather than to any one
   of its operator classes — most often used for cross-type comparisons
   (e.g. an `int4` family also handling `int4` vs. `int8`), added via
   `ALTER OPERATOR FAMILY name USING access_method ADD OPERATOR
   strategy_num operator_name (left_type, right_type) [FOR SEARCH | FOR
   ORDER BY sort_family_name], FUNCTION support_num [(left_type,
   right_type)] function_name (argument_types), ...`. DPG never exposes
   `ALTER` as source syntax (declarations describe desired state, not
   commands — the same reason table constraints never require `ALTER TABLE
   ADD CONSTRAINT` boilerplate), so these are declared in the family's `{ }`
   block instead, using the exact same member grammar `ALTER OPERATOR
   FAMILY ... ADD` uses, just without repeating the `ALTER`/family header:

```sql
SCHEMA public {
    OPERATOR FAMILY my_family USING btree {
        OPERATOR 1 <(int4, int8),
        OPERATOR 3 =(int4, int8),
        FUNCTION 1 (int4, int8) btint48cmp(int4, int8)
    };
}
```

   `FOR ORDER BY sort_family_name` (used for GiST/SP-GiST "KNN" support —
   `btree` itself rejects it, since it has no notion of ordering operators
   distinct from its own strategies) always names a **btree** family:
   PostgreSQL requires `sort_family_name` to reference one, so it is never
   ambiguous even though the syntax itself doesn't repeat `USING btree`.

   A member's identity is `(kind, strategy/support number, left_type,
   right_type)` — matching PostgreSQL's own `pg_amop`/`pg_amproc` unique
   indexes. Op-types are always compared in their canonical form (e.g.
   `integer`, never `int4`). A `FUNCTION` item's `(left_type, right_type)`
   may be omitted only when the function takes exactly two arguments (then
   defaulting to its own argument types, PostgreSQL's own documented
   default); any other arity requires them explicitly, since the correct
   default (the owning operator class's input type) cannot be derived from
   the function's signature alone.

   **Diffing (loose family members):** unlike an operator class's `AS`
   list, PostgreSQL genuinely supports incremental member changes at the
   family level, so these are diffed per-member rather than forcing the
   whole family to drop and recreate:

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Member added | `ALTER OPERATOR FAMILY f USING am ADD OPERATOR n op(lt,rt) [FOR SEARCH\|FOR ORDER BY sf]` / `... ADD FUNCTION n (lt,rt) fn(args)` | `SAFE` |
   | Member changed in place (same slot, different operator/function/sort target) | `ALTER ... DROP ...;` then `ALTER ... ADD ...` | `DESTRUCTIVE` |
   | Member removed | `ALTER OPERATOR FAMILY f USING am DROP OPERATOR n (lt,rt)` / `DROP FUNCTION n (lt,rt)` | `DESTRUCTIVE` |
   | Family itself dropped and recreated (body or access method changed) | every declared member re-added in full after the `CREATE` | inherits the family's own classification |

   Removal is classified `DESTRUCTIVE` even though it destroys no data:
   dropping a cross-type member silently changes query-plan shape for any
   query that relied on it (an index scan degrading to a sequential scan,
   a merge join becoming a hash join) with no PostgreSQL error anywhere,
   and the common real-world cause of "a declared member is gone" is an
   accidental source omission, not an intentional removal —
   `--allow-destructive` is the right point to require confirmation.

   A member already owned by one of the family's operator classes (i.e.
   already present in some class's own `AS` list) must never be
   re-declared in the family's `{ }` block — PostgreSQL rejects the
   resulting `ALTER ... ADD` as a duplicate. A family that has no
   declaration of its own (relying entirely on PostgreSQL's same-name
   auto-creation from an unqualified `CREATE OPERATOR CLASS`) has nowhere
   to attach loose members; declare the family explicitly first.

### 14.5. Casts

   **PG equivalent:**
   `CREATE CAST (source AS target) (WITH FUNCTION func | WITHOUT FUNCTION | WITH INOUT) [AS IMPLICIT | AS ASSIGNMENT]`

```sql
SCHEMA public {
    CAST (mytype AS TEXT)
        WITH FUNCTION mytype_to_text(mytype)
        AS IMPLICIT;

    CAST (TEXT AS mytype)
        WITH FUNCTION text_to_mytype(TEXT);
}
```

   **Cast identity:** `(source_type, target_type)`.

   **Diffing semantics:** PostgreSQL provides no `ALTER CAST`.  Any
   change to a cast requires `DROP CAST` followed by `CREATE CAST` —
   classified as `DESTRUCTIVE`.  The compiler MUST check for dependent
   objects before emitting `DROP CAST`.

### 14.6. Extended Statistics Objects

   **PG equivalent:**
   `CREATE STATISTICS [IF NOT EXISTS] name [(kinds)] ON { column | (expression) } [, ...] FROM table`

```sql
SCHEMA public {
    STATISTICS orders_stats (dependencies, ndistinct, mcv)
        ON customer_id, created_at
        FROM orders;

    -- Extended statistics on an expression (PostgreSQL 14+), not just
    -- plain columns.
    STATISTICS orders_date_stats (ndistinct)
        ON (date_trunc('month', created_at)), customer_id
        FROM orders;
}
```

   Extended Statistics is an `opaque-object-decl` kind (Appendix A) —
   the whole `ON ... FROM ...` clause, expressions included, is Part 1
   passed to PostgreSQL's grammar unchanged, the same as every other
   property shown above. Confirmed: DPG performs no column-list parsing
   of its own here, so a `(expression)` entry needs no DPG-side grammar
   and works today exactly like the plain-column form.

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New statistics object | `CREATE STATISTICS ...` | `SAFE` |
   | `statistics_target` changed | `ALTER STATISTICS name SET STATISTICS n` | `SAFE` |
   | Column list or kinds changed | `DROP STATISTICS; CREATE STATISTICS` | `DESTRUCTIVE` |
   | Object removed | `DROP STATISTICS name` | `SAFE` |

### 14.7. Tablespaces

   Tablespaces are cluster-level objects.

   **PG equivalent:**
   `CREATE TABLESPACE name [OWNER owner] LOCATION 'path' [WITH (tablespace_option = value, ...)]`

```abnf
tablespace-decl = "TABLESPACE" WSP identifier
                  [ WSP "OWNER" WSP role-spec ]
                  WSP "LOCATION" WSP SQUOTE <text> SQUOTE
                  [ WSP "WITH" WSP "(" storage-params ")" ]
                  ";"
                  [ "{" tablespace-block "}" ]

role-spec = identifier / "CURRENT_ROLE" / "CURRENT_USER" / "SESSION_USER"

tablespace-block = *( ( comment-dir / renamed-from-dir ) ";" )
```

   `OWNER` is inline in Part 1, matching real PostgreSQL's own
   `CREATE TABLESPACE` grammar exactly (Tenet 2/5) rather than a `{ }`
   block directive — unlike Table/View/etc., where `OWNER` has no
   place in the native `CREATE` statement and is therefore a block
   directive instead.  `WITH (tablespace_option = value, ...)` (e.g.
   `seq_page_cost`, `random_page_cost`) follows the same trailing
   position real PostgreSQL uses, after `LOCATION`.  A change to the
   declared inline `OWNER` post-creation emits `ALTER TABLESPACE name
   OWNER TO role`, and `renamed-from-dir` in the `{ }` block emits
   `ALTER TABLESPACE old RENAME TO new` — Tablespace is cluster-level,
   not schema-scoped, so the generic extension's cross-schema `SET
   SCHEMA` half never applies (same as Publication/Server/Role above).
   A `WITH (...)` option change emits a targeted
   `ALTER TABLESPACE name SET (...)`/`RESET (...)` (real PostgreSQL's
   identical two-clause grammar to `ALTER TABLE`'s own `SET`/`RESET`) —
   `CAUTION`, since reloptions like `random_page_cost`/`seq_page_cost`
   alter planning/maintenance behavior, not pure metadata.

```sql
-- production/cluster/tablespaces.dpg

TABLESPACE fast_ssd LOCATION '/mnt/nvme/pg_data'
    WITH (random_page_cost = 1.1, seq_page_cost = 1.0);
TABLESPACE archive  LOCATION '/mnt/hdd/pg_archive';
```

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Owner changed | `ALTER TABLESPACE name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER TABLESPACE old RENAME TO new` | `CAUTION` |
   | `WITH (...)` option changed | `ALTER TABLESPACE name SET (...)`/`RESET (...)` | `CAUTION` |
   | `LOCATION` changed | `DROP TABLESPACE` + `CREATE TABLESPACE` | `DESTRUCTIVE` |
   | Tablespace removed | `DROP TABLESPACE name` | `DESTRUCTIVE` |

   `LOCATION` cannot be changed after creation — any location change
   requires the full drop-and-recreate path.  Dropping a non-empty
   tablespace fails at the PostgreSQL level; the compiler classifies it
   as `DESTRUCTIVE` and additionally emits a warning comment noting
   that it will fail if any objects reside in the tablespace.

### 14.8. Foreign Data Wrappers

   In the common case FDWs are installed via extension.  The explicit
   declaration is reserved for custom C-implemented FDWs and is placed
   in the cluster objects directory.

   **PG equivalent:**
   `CREATE FOREIGN DATA WRAPPER name [HANDLER func] [VALIDATOR func] [OPTIONS (...)]`

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

   `FOREIGN DATA WRAPPER` MAY carry an optional `{ }` block
   (`comment-dir`/`owner-dir`/`renamed-from-dir`/`grants-block`/
   `revocations-block`) — same standard rules as every other kind
   (Section 7.6/Section 11.2) — real PostgreSQL supports `ALTER FOREIGN
   DATA WRAPPER ... OWNER TO`/`RENAME TO` and `GRANT/REVOKE USAGE ON
   FOREIGN DATA WRAPPER` (superuser-only for `CREATE`/`OWNER TO`, same
   restriction as the bare `CREATE FOREIGN DATA WRAPPER` statement
   itself). FDW is database-level, not schema-scoped, so the
   cross-schema `SET SCHEMA` half of the generic extension never
   applies here (same as Publication/Server above).

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New FDW | `CREATE FOREIGN DATA WRAPPER ...` | `SAFE` |
   | `HANDLER`/`VALIDATOR`/`OPTIONS` changed | `DROP FOREIGN DATA WRAPPER` + `CREATE FOREIGN DATA WRAPPER` | `DESTRUCTIVE` |
   | Owner changed | `ALTER FOREIGN DATA WRAPPER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER FOREIGN DATA WRAPPER old RENAME TO new` | `CAUTION` |
   | Grants/Revocations changed | `GRANT`/`REVOKE ... ON FOREIGN DATA WRAPPER name` | `SAFE` |
   | FDW removed | `DROP FOREIGN DATA WRAPPER name` | `DESTRUCTIVE` |

   PostgreSQL provides no `ALTER FOREIGN DATA WRAPPER` for `HANDLER`,
   `VALIDATOR`, or `OPTIONS` — any change to those three forces
   drop-and-recreate; `OWNER TO`, `RENAME TO`, and the grant/revoke
   surface are the only in-place alterations available, same shape as
   Foreign Server below.

### 14.9. Foreign Servers

   Foreign servers are database-level objects.

   **PG equivalent:**
   `CREATE SERVER [IF NOT EXISTS] name [TYPE 'type'] [VERSION 'version'] FOREIGN DATA WRAPPER fdw [OPTIONS (...)]`

```sql
SERVER analytics_warehouse
    FOREIGN DATA WRAPPER postgres_fdw
    OPTIONS (host 'warehouse.internal', dbname 'analytics', port '5432')
{
    OWNER fdw_admin;
    COMMENT 'Read replica warehouse connection';
}
```

   `SERVER` MAY carry an optional `{ }` block (`comment-dir`/
   `owner-dir`/`renamed-from-dir`) — same standard rules as every other
   kind (Section 7.6). Server is database-level, not schema-scoped, so the
   cross-schema `SET SCHEMA` half of the generic extension never applies
   here (same as Publication above).

   **Diffing semantics:**

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New server | `CREATE SERVER ...` | `SAFE` |
   | OPTIONS changed | `ALTER SERVER name OPTIONS (SET key 'value', ...)` | `SAFE` |
   | `VERSION` changed only, `OPTIONS`/FDW unchanged | `ALTER SERVER name VERSION 'new_version'` | `SAFE` |
   | Owner changed | `ALTER SERVER name OWNER TO role` | `SAFE` |
   | Renamed (`RENAMED FROM`) | `ALTER SERVER old RENAME TO new` | `CAUTION` |
   | FDW changed | Drop + recreate | `DESTRUCTIVE` |
   | Server removed | `DROP SERVER name [CASCADE]` | `DESTRUCTIVE` |

### 14.10. User Mappings

   User mappings associate a local PostgreSQL role with credentials for
   a foreign server. Declared and diffed as an opaque body (byte-for-byte
   source text, not per-field) — the same tier as Tablespace/FDW/Server/
   Publication/etc.

   **PG equivalent:**
   `CREATE USER MAPPING [IF NOT EXISTS] FOR user SERVER server [OPTIONS (...)]`

```sql
USER MAPPING FOR app_service
    SERVER analytics_warehouse
    OPTIONS (user 'fdw_user', password '{{vault:secret/fdw/analytics#password}}');
```

   Any `OPTIONS` value MAY hold one or more `{{<secret-uri>}}` placeholders
   (Appendix D.1) — the same mechanism as SUBSCRIPTION `CONNECTION` (Section 13.2) and
   Role `PASSWORD` (Section 11.1), and, like both of those, the only thing that
   ever triggers resolution: a literal option value with no `{{...}}` at
   all never touches the resolver. Unlike `CONNECTION`/`PASSWORD`, DPG
   doesn't isolate one specific option key to resolve — `OPTIONS` keys are
   foreign-data-wrapper-specific, not fixed by DPG, so resolution runs over
   the entire statement text, substituting whichever placeholders it finds
   regardless of which key they're under. Resolution happens once,
   immediately before `CREATE USER MAPPING` executes — never during
   `plan`/`diff`; the snapshot, an archived migration file, and any error
   message all show the placeholder/reference form, not the resolved
   value.

   Hardcoded passwords (a literal value with no `{{...}}` placeholder
   under any password-like `OPTIONS` key — `password`/`passwd`/`pwd`/
   `secret`/`passphrase`) are rejected by the linter when
   `forbid_hardcoded_passwords` is enabled (default `true`), rule
   `hardcoded-fdw-password` (Section 19.1).

   **Diffing semantics:** any change to the mapping is a full
   `DROP USER MAPPING` + `CREATE USER MAPPING`, not a targeted
   `ALTER USER MAPPING`, matching every other opaque-tier object kind
   (Section 25).

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | New mapping | `CREATE USER MAPPING ...` | `SAFE` |
   | OPTIONS changed | `DROP USER MAPPING ...` + `CREATE USER MAPPING ...` | `DESTRUCTIVE` then `SAFE` |
   | Mapping removed | `DROP USER MAPPING FOR user SERVER server` | `DESTRUCTIVE` |

---

### 14.11. Security Labels

   A security label attaches a provider-specific classification tag to
   an object — meaningful only when a label provider is loaded on the
   server (e.g. `sepgsql` for SELinux-integrated deployments; without
   one, PostgreSQL rejects every `SECURITY LABEL` statement). DPG's directive
   mirrors real PostgreSQL's own statement directly, unlike `COMMENT`
   (a single nilable value): a block MAY declare several `SECURITY
   LABEL` entries, one per provider, since PostgreSQL genuinely lets
   multiple independent providers label the same object at once.

   **PG equivalent:**
   `SECURITY LABEL [FOR provider] ON object_type object_name IS { string | NULL }`

```abnf
security-label-decl =
    "SECURITY" WSP "LABEL" [ WSP "FOR" WSP identifier ] WSP string-literal ";"
```

   The DPG form omits `ON object_type object_name` — implicit from the
   enclosing declaration, the same convention `COMMENT`/`GRANTS`/
   `REVOKES` already use — and the provider identifier: omitted,
   PostgreSQL resolves it to the sole loaded provider, erroring if zero
   or more than one is loaded.

```sql
TABLE orders (
    id BIGINT PRIMARY KEY
) {
    SECURITY LABEL FOR sepgsql 'system_u:object_r:sepgsql_table_t:s0';
}

COLUMN ssn {
    SECURITY LABEL FOR sepgsql 'system_u:object_r:sepgsql_secret_table_t:s0';
}
```

   **Supported object kinds:** every kind PostgreSQL's own `SECURITY
   LABEL` grammar supports that DPG models at all — Tables (incl.
   Foreign Tables), Columns, Views, Materialized Views, Functions,
   Procedures, Aggregates (emitted as `ON FUNCTION`, as PostgreSQL's
   grammar requires), Domains
   (`ON DOMAIN`) and every other Type variant (`ON TYPE`), Schemas,
   Sequences, Roles, Tablespaces, Publications, Subscriptions, and
   Event Triggers. `DATABASE` and `LARGE OBJECT` are excluded — DPG
   doesn't model either as an object kind at all (Section 23); `[PROCEDURAL]
   LANGUAGE` is excluded because DPG doesn't support raw `CREATE
   LANGUAGE` either (Section 23) — every real language install goes through
   `CREATE EXTENSION`, which already has its own `COMMENT`/`SECURITY
   LABEL` coverage as an ordinary opaque-tier kind.

   [PG] Labels on roles, tablespaces, and subscriptions are stored
   cluster-wide (`pg_shseclabel`); all others per database
   (`pg_seclabel`).  This affects introspection only, not DPG syntax or
   emitted DDL.

   **Diffing semantics:** keyed by provider, not by (privilege, role)
   the way `GRANTS`/`REVOKES` are — two entries for different
   providers are independent catalog rows, never in conflict. Unlike
   `GRANTS`' additive model, a removed entry emits an explicit `IS
   NULL` (`SECURITY LABEL` has no separate revoke-shaped statement;
   `NULL` is PostgreSQL's own documented way to clear a label). Every
   emitted statement is `SAFE` — `SECURITY LABEL` never touches data,
   only catalog metadata, the same classification `COMMENT ON` already
   gets throughout this document.

   | Change | DDL emitted | Safety |
   |--------|-------------|--------|
   | Label added (new provider) | `SECURITY LABEL [FOR provider] ON ... IS '...'` | `SAFE` |
   | Label changed (same provider) | `SECURITY LABEL [FOR provider] ON ... IS '...'` (re-set) | `SAFE` |
   | Label removed | `SECURITY LABEL [FOR provider] ON ... IS NULL` | `SAFE` |

---

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
   inverted sense relative to a `nullable` field.  `identity` holds the
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
  "name": "user_state",
  "body": "\"active\" | \"suspended\" | \"deleted\"",
  "comment": null
}
```

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

   Introspects the live database catalog and reports drift: any
   divergence between the snapshot and the live state.

```
dpg verify [options] [<cluster>[/<database>]]

Options:
  --cluster <name>
  --database <name>
```

   The drift report is always printed as human-readable text (to stdout
   when a database/cluster has no drift, to stderr with the computed SQL
   otherwise) — there is no `--format` flag.

   **Drift detection model:**

   -   **Reports:** any DPG-declared object property that is absent
       from or differs in the live catalog.

   -   **Reports:** any DPG-declared grant that is absent from the
       live catalog.

   -   **Does NOT report:** extra grants present in the live catalog
       but not declared in DPG source (additive model, Section 11.2).

   -   **Does NOT report:** objects present in the live catalog but
       absent from DPG source (unmanaged objects).

   Exit codes: 0 = no drift; non-zero = drift was detected in at least
   one targeted cluster/database, OR an error occurred (a connection
   failure is reported the same way as drift — both produce a non-zero
   exit; the two are distinguished only by the printed message, not by
   a separate exit code).

### 18.4. dpg dump

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

### 18.5. dpg diff

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

### 18.6. dpg validate

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

### 18.7. dpg fmt

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

   `dpg fmt` deliberately does NOT touch identifier casing — unlike
   keyword casing, identifiers are never rewritten, in either
   direction.  This is intentional, not a gap: DPG's macro
   preprocessor (Section 4.7) spreads identifiers across files verbatim, and
   PostgreSQL identifiers are case-sensitive once quoted, so a
   formatter that normalized casing could silently change which
   objects a macro-expanded reference resolves to.  A case mismatch
   that changes meaning is a compiler error (see the linter/compiler),
   never something the formatter guesses at or silently corrects.

### 18.8. dpg portability

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

### 18.9. dpg init

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

### 18.10. dpg completion

   Generates shell completion scripts.

```
dpg completion <shell>

<shell>: bash | zsh | fish | powershell
```

### 18.11. dpg docs

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

### 18.12. Target Selection

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
   | `pg_roles` (or `pg_authid`) | Roles and role memberships |
   | `pg_tablespace` | Tablespaces |

### 20.2. Drift Detection

   The `dpg verify` command compares the snapshot with the live catalog:

   1.  Introspect the live catalog → produce the live state as IR objects.
   2.  Load the committed snapshot → produce the snapshot state as IR objects.
   3.  Compute the diff between snapshot state (desired) and live state
       (current) — i.e., treat the snapshot as the "desired" input and
       the live catalog as the "snapshot" input to the Differ.
   4.  Any non-empty DiffOps represent drift.

   **Grant drift:** Report as drift any DPG-declared grant absent from
   the live catalog.  Do NOT report extra grants present in the live
   catalog.

---

## 21. Per-Object Diff Algorithms

   This section specifies, for each object type, the precise field
   comparison performed by the Differ and the DDL emitted for each
   type of change.

   **TABLE:**

   | Field | Change detection | DDL | Safety |
   |-------|-----------------|-----|--------|
   | Columns (added) | New col name not in snapshot | `ALTER TABLE t ADD COLUMN c TYPE [DEFAULT ...]` | `SAFE` |
   | Columns (dropped) | Col name in snapshot, absent in desired | `ALTER TABLE t DROP COLUMN c` | `DESTRUCTIVE` |
   | Column type changed | `TypeRef.String()` differs | `ALTER TABLE t ALTER COLUMN c TYPE newtype [USING expr]` | `CAUTION` or `DESTRUCTIVE` |
   | Column NOT NULL added | `nullable` false→true (was true in snap) | `ALTER TABLE t ALTER COLUMN c SET NOT NULL` | `CAUTION` |
   | Column NOT NULL removed | `nullable` true→false | `ALTER TABLE t ALTER COLUMN c DROP NOT NULL` | `SAFE` |
   | Column DEFAULT added | `default` was nil | `ALTER TABLE t ALTER COLUMN c SET DEFAULT expr` | `SAFE` |
   | Column DEFAULT changed | `default` text differs | `ALTER TABLE t ALTER COLUMN c SET DEFAULT expr` | `SAFE` |
   | Column DEFAULT removed | `default` is nil | `ALTER TABLE t ALTER COLUMN c DROP DEFAULT` | `SAFE` |
   | Column statistics | `statistics_target` differs | `ALTER TABLE t ALTER COLUMN c SET STATISTICS n` (or `DEFAULT`, PG17+) | `SAFE` |
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
   | Identity clause added/changed/dropped (Section 7.4) | `identity-opts` differ | `ADD GENERATED AS IDENTITY`/`SET GENERATED`/`SET <option>`/`DROP IDENTITY` (Section 7.4 table) | `SAFE`/`CAUTION` |
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
   - **Declared** — DPG syntax fully specified in this document.
   - **Diffed** — The compiler computes structured per-field changes and emits precise DDL.
   - **Passthrough** — Treated as opaque text; diffed by text equality only.
   - **No SQL** — DPG-native; generates no PostgreSQL DDL.
   - **Out of scope** — Not managed by DPG.
   - **Deferred** — Explicitly out of scope for this version; planned.

   | Feature | Status | Notes |
   |---------|--------|-------|
   | Tables (regular) | Declared, Diffed | Full per-field diff, incl. `LIKE`, typed (`OF type_name`), `USING` access method, `REPLICA IDENTITY`, `CLUSTER ON`, `ATTACH`/`DETACH PARTITION`, cheap constraint rename |
   | Tables (unlogged) | Declared, Diffed | `UNLOGGED` prefix; toggling post-creation uses `SET LOGGED`/`SET UNLOGGED`, not drop+recreate |
   | Tables (temporary) | Out of scope | Session-scoped |
   | Columns — all built-in types | Declared, Diffed | In `()` list |
   | Columns — generated (`ALWAYS AS ... STORED`/`VIRTUAL`) | Declared, Diffed | In `()` list; `VIRTUAL` is PG18+ (Section 7.2) |
   | Columns — identity (`AS IDENTITY`) | Declared, Diffed | In `()` list |
   | Column `COMPRESSION` | Declared, Diffed | `COLUMN c { COMPRESSION m; }` |
   | Column `STORAGE` | Declared, Diffed | `COLUMN c { STORAGE s; }` |
   | Column statistics targets | Declared, Diffed | `COLUMN c { STATISTICS n \| DEFAULT; }` — `DEFAULT` is PG17+ |
   | Column comments | Declared, Diffed | `COLUMN c { COMMENT '...'; }` |
   | Column `DEPRECATED` | Declared, Diffed | `COLUMN c { DEPRECATED '...'; }` |
   | Column `USING` (type change) | Declared, Diffed | `COLUMN c { USING expr; }` |
   | Column renames | Declared, Diffed | `COLUMN new { RENAMED FROM old; }` |
   | Column-level grants | Declared, Diffed | `COLUMN c { GRANTS { ... } }` |
   | Column-level revocations | Declared, Diffed | `COLUMN c { REVOKES { ... } }` |
   | Inline constraints (PK, UNIQUE, CHECK, FK, `NOT NULL`) | Declared, Diffed | Single-column emitted inline; table-level named `NOT NULL` is PG18+ (Section 7.3) |
   | Named constraints in `()` list | Declared, Diffed | Emitted inline for single-column |
   | Constraints in `{}` block | Declared, Diffed | `NOT VALID` required here |
   | `EXCLUSION` constraints | Declared, Diffed | In `()` or `{}` block; on partitioned tables since PG17 (Section 7.3, passthrough) |
   | `NOT VALID` / `VALIDATE CONSTRAINT` | Declared, Diffed | Multi-migration lifecycle; extended to `NOT NULL` constraints in PG18 (Section 7.3) |
   | `ENFORCED`/`NOT ENFORCED` (`CHECK`/`FOREIGN KEY`) | Declared, Diffed | PG18+ (Sections 7.2/7.3) |
   | Temporal keys (`WITHOUT OVERLAPS`/`PERIOD`) | Declared, Diffed | PG18+; references an existing range/multirange column, catalogued as an ordinary `PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY`, not `EXCLUDE` (Section 7.3) |
   | FK `ON DELETE`/`ON UPDATE SET NULL`/`SET DEFAULT (col-list)` | Declared, Diffed | PG15+, column-scoped variant of the existing bare form (Section 7.2) |
   | Indexes — all access methods | Declared, Diffed | btree, hash, gin, gist, brin, spgist, bloom |
   | Indexes — partial | Declared, Diffed | `WHERE` predicate as text |
   | Indexes — expression | Declared, Diffed | Expression as text |
   | Indexes — covering (`INCLUDE`) | Declared, Diffed | Drop + recreate on change |
   | Indexes — concurrent creation | Declared, non-transactional | `CREATE INDEX CONCURRENTLY` |
   | Indexes — `ON ONLY`, opclass parameters, `NULLS [NOT] DISTINCT`, rename | Declared, Diffed | `ONLY` suppresses partition recursion; rename is metadata-only (Section 7.7) |
   | ENUM types | Declared, Diffed | `MIGRATE REMOVE` for value removal; `BEFORE`/`AFTER` positional `ADD VALUE`, `RENAME VALUE`, `OWNER`, `RENAMED FROM`/`SET SCHEMA` |
   | Composite types | Declared, Diffed | Attribute `COLLATE`; `OWNER`, `RENAMED FROM`/`SET SCHEMA` |
   | Range types | Declared, Diffed | Any option change = DESTRUCTIVE; `OWNER`, `RENAMED FROM`/`SET SCHEMA`; multirange companion auto-created, never separately declared |
   | Domain types | Declared, Diffed | `COLLATE`; `NOT VALID`/`VALIDATE CONSTRAINT`/`RENAME CONSTRAINT` lifecycle; `OWNER`, `RENAMED FROM`/`SET SCHEMA` |
   | Base (shell) types | Declared, Passthrough | Bare forward-declaration shell + automatic cycle-breaking for self-referential support functions (Section 5.5); `OWNER`, `RENAMED FROM`/`SET SCHEMA`; support-function/storage property changes use non-destructive `ALTER TYPE ... SET (...)` where real PostgreSQL allows it |
   | Virtual types | Declared, No SQL | DPG-native; snapshot only |
   | Views | Declared, Diffed | Column list change = DESTRUCTIVE; `RENAMED FROM` and cross-schema `SET SCHEMA` supported (Section 7.6, Section 8.1) |
   | Materialized views | Declared, Diffed | Query change = DESTRUCTIVE; `RENAMED FROM`/`SET SCHEMA` supported (Section 8.2) |
   | Recursive views | Declared, Diffed | |
   | Functions — all languages | Declared, Passthrough body | Body hash-diffed; `LEAKPROOF`, `TRANSFORM FOR TYPE`, C/`internal` `AS` forms, PG14+ `BEGIN ATOMIC`, `[NO] DEPENDS ON EXTENSION`, `OWNER`, `REVOKES`, `RENAMED FROM`/`SET SCHEMA` |
   | Procedures | Declared, Passthrough body | Same additions as Functions above (`[NO] DEPENDS ON EXTENSION`, `OWNER`, `REVOKES`, `RENAMED FROM`/`SET SCHEMA`) |
   | Aggregates | Declared, Diffed | Full `agg-options` set (Section 9.4) diffed; any option change = DESTRUCTIVE; `RENAME TO`/`OWNER TO`/`SET SCHEMA` are the only non-destructive ALTER operations, matching real PostgreSQL's `ALTER AGGREGATE` surface |
   | Window functions | Declared, Passthrough body | |
   | Row Level Security | Declared, Diffed | `TO`/`USING`/`WITH CHECK`-only policy changes use non-destructive `ALTER POLICY`, avoiding a zero-active-policy window; policy `RENAMED FROM` also supported (Section 7.8) |
   | Triggers | Declared, Diffed | `RENAMED FROM` supported, metadata-only via `ALTER TRIGGER` (Section 7.9) |
   | Event triggers | Declared, Passthrough | Reconstructed from catalog; hash-diffed. Enable-state (`DISABLED`/`ENABLE REPLICA`/`ENABLE ALWAYS`), `OWNER`, `RENAMED FROM` diffed structurally, not part of the hash. `login` event (PG17+) also supported (Section 14.1) |
   | Sequences | Declared, Diffed | `UNLOGGED`, `OWNED BY NONE`, `REVOKES`, `RESTART [WITH n]` (Section 10) |
   | Schemas | Declared, Diffed | |
   | Extensions | Declared, Diffed | `SCHEMA` change uses non-destructive `ALTER EXTENSION ... SET SCHEMA` for relocatable extensions (Section 6.2) |
   | Roles | Declared, Diffed | Cluster-level; `PASSWORD` (Section 11.1) never live-introspected (superuser-only in PG), diffed offline via a hash of the declared text. `RENAMED FROM`/`RENAME TO` and `SET`/`RESET` session-config-parameter grammar now supported (Section 11.1); membership `WITH ADMIN`/`INHERIT`/`SET` options (PG16+) also supported |
   | Table-level grants | Declared, Diffed | Additive model; `MAINTAIN` privilege (PG17) and `GRANTED BY role` (Section 7.10) also supported |
   | Parameter Privileges (Section 11.6) | Declared, Diffed | Cluster-level; `GRANT {SET\|ALTER SYSTEM} ON PARAMETER`, additive model like Table-level grants |
   | Column-level grants | Declared, Diffed | Additive model |
   | Explicit revocations | Declared, Diffed | |
   | Default Privileges | Declared, Diffed | |
   | Security Labels (Section 14.11) | Declared, Diffed | Keyed by provider; every kind PostgreSQL's own `SECURITY LABEL` grammar supports and DPG models |
   | Tablespaces | Declared, Passthrough | Cluster-level; reconstructed from catalog, hash-diffed. `CREATE`-time `WITH (...)` storage params, `OWNER TO`, `RENAME TO`, and non-destructive `SET`/`RESET` on `WITH (...)` options are all supported (Section 14.7) |
   | Foreign Data Wrappers | Declared, Passthrough | Cluster-level; reconstructed from catalog, hash-diffed |
   | Foreign Servers | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`, `RENAMED FROM`, bare `VERSION`-only change also supported (Section 14.9) |
   | User Mappings | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OPTIONS` may hold a `{{secret-uri}}` reference (Section 14.10, Appendix D.1), resolved only immediately before `CREATE USER MAPPING` executes |
   | Foreign Tables | Declared, Diffed | `SERVER`/`OPTIONS` after `)`; Section 7.12 now documents the ALTER-semantics table directly (`OPTIONS` change is `SAFE` in place, `SERVER` change is `DESTRUCTIVE` drop+recreate, column add/drop follows regular `TABLE` rules) |
   | Partitioned Tables | Declared, Diffed | Partition `RENAMED FROM` supported, same mechanism as a plain table rename (Section 7.13) |
   | Sub-partitioning | Declared, Diffed | |
   | Publications | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`, `RENAMED FROM` also supported (Section 13.1) |
   | Subscriptions | Declared, Passthrough | Reconstructed from the catalog; hash-diffed. `CONNECTION` alone is never introspected (`subconninfo` has no PUBLIC grant, and even a privileged read can't recover the original `{{secret-uri}}`) — reconstructed as a fixed placeholder instead, excluded from the drift comparison like every other reconstructed body (Section 13.2). `CONNECTION` may hold a `{{secret-uri}}` reference in source (Section 13.2, Appendix D.1), resolved only immediately before `CREATE SUBSCRIPTION` executes |
   | Collations | Declared, Passthrough | Reconstructed from catalog, hash-diffed; property changes (`LOCALE`/`PROVIDER`/`RULES`/`FROM` target) = DESTRUCTIVE. `FROM existing_collation` and `RULES` (PG16+) forms, `OWNER`/`RENAMED FROM`/`SET SCHEMA`, and `REFRESH VERSION` (PG15+) also supported (Section 14.2) |
   | Operators | Declared, Passthrough | Reconstructed from catalog, hash-diffed; any change = DESTRUCTIVE |
   | Operator Classes | Declared, Passthrough | Reconstructed from catalog, hash-diffed; any `AS` member-list change = DESTRUCTIVE (PostgreSQL has no incremental `ALTER OPERATOR CLASS`) |
   | Operator Families | Declared, Passthrough + Diffed | Header (name/access method) reconstructed from catalog, hash-diffed; loose members (Section 14.4, `ALTER OPERATOR FAMILY ... ADD`) are structured and diffed incrementally per member, live-path included — not gated on `Reconstructed` the way the bare header hash is |
   | Casts | Declared, Passthrough | Reconstructed from catalog, hash-diffed; any change = DESTRUCTIVE |
   | Extended Statistics Objects | Declared, Passthrough | Reconstructed from catalog; hash-diffed |
   | Text Search Configurations | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`, `RENAMED FROM`/`SET SCHEMA`, `ALTER MAPPING REPLACE` (bulk and per-token-type) also supported (Section 12.1) |
   | Text Search Dictionaries | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `OWNER`/`COMMENT`/`RENAMED FROM` block now supported (Section 12.2) |
   | Text Search Parsers | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `COMMENT`/`RENAMED FROM` block now supported, no `OWNER` (real PostgreSQL has none for this kind, Section 12.3) |
   | Text Search Templates | Declared, Passthrough | Reconstructed from catalog; hash-diffed. `COMMENT`/`RENAMED FROM` block now supported, no `OWNER` (Section 12.4) |
   | Macro preprocessor | Declared, No SQL | Compile-time text expansion |
   | Cross-file macro sharing | Declared, No SQL | Macros defined in any file in the compilation scope are available to all others |
   | Rules (REWRITE) | Out of scope | Legacy |
   | `IMPORT FOREIGN SCHEMA` | Out of scope | Runtime discovery |
   | `REFRESH MATERIALIZED VIEW` | Out of scope | Runtime DML |
   | Temporary tables | Out of scope | Session-scoped |
   | Temporary views | Out of scope | Session-scoped, same reasoning as temporary tables (Section 8.1) |
   | Inline data seeding | Out of scope | DPG is a schema tool; data management is outside its scope |
   | Database management (`CREATE`/`ALTER`/`DROP DATABASE`) | Out of scope | Cluster-provisioning, not schema management; see Section 23 — permanent, not deferred |
   | `REASSIGN OWNED BY` / `DROP OWNED BY` | Out of scope | One-shot maintenance command, not a declarable object; see Section 23 |
   | `ALTER SYSTEM` | Out of scope | Cluster-wide GUC configuration, not schema state; see Section 23 |
   | `CREATE ACCESS METHOD` | Out of scope | Covered by extension install (`CREATE EXTENSION`); see Section 23 |
   | `CREATE CONVERSION` | Out of scope | No realistic use case under UTF-8 dominance; see Section 23 |
   | `CREATE [PROCEDURAL] LANGUAGE` | Out of scope | Covered by extension install (`CREATE EXTENSION`); see Section 23 |
   | `CREATE TRANSFORM` | Out of scope | Always bundled with a specific extension pairing; see Section 23 |
   | `ALTER EXTENSION ADD`/`DROP member_object` | Out of scope | Extension-authoring concern, a tier below application-schema scope; see Section 23 |
   | `CLUSTER` (maintenance command) | Out of scope | One-shot table rewrite, not idempotent as declared state; `CLUSTER ON` (Section 7.11) declares the target index only, doesn't invoke the command; see Section 23 |
   | Large Objects (`lo_create`/`lo_import`/`lo_export` etc.) | Out of scope | No `CREATE`/`ALTER`/`DROP` DDL surface to attach to; see Section 23 |

---

## Appendix A. ABNF Grammar Summary

   The following is a consolidated summary of all ABNF productions
   defined throughout this document.  Individual productions were
   specified inline in their respective sections.  This appendix
   collects them for reference.

```abnf
; Top-level source file
dpg-file = *( WSP / comment / macro-decl / top-level-decl )

; Every declaration kind DPG models, at the granularity a .dpg file
; actually admits at top level (schema-scoped kinds also nest inside a
; schema-decl's { } block via nested-object — both positions are legal,
; see Section 3.6 on directory-inferred schema context for top-level use
; without a wrapping SCHEMA block).
top-level-decl = schema-decl
               / extension-decl
               / role-decl
               / tablespace-decl
               / fdw-decl
               / server-decl
               / user-mapping-decl
               / publication-decl
               / subscription-decl
               / event-trigger-decl
               / default-privileges-decl
               / parameter-privileges-decl
               / opaque-object-decl
               / nested-object

; security-label-decl (Section 14.11) is NOT a top-level declaration — it is a
; { } block directive nested inside another object's own block (Table,
; Column, Function, Publication, etc.), the same position comment-dir/
; grants-block occupy.

; Schema-scoped object kinds — legal directly at top level (relying on
; directory-inferred schema, Section 3.6) or nested inside schema-block.
nested-object = table-decl
              / view-decl
              / matview-decl
              / function-decl
              / procedure-decl
              / aggregate-decl
              / sequence-decl
              / enum-decl
              / composite-decl
              / range-decl
              / domain-decl
              / base-type-decl
              / virtual-type-decl
              / tsconfig-decl

; server-decl / user-mapping-decl have no dedicated ABNF block in their
; own sections (Sections 14.9/14.10) any more than the opaque kinds below do —
; named here distinctly only because they're referenced from other
; productions (server-clause, terminator) that DO need a name to point
; at, unlike the opaque-object-decl kinds, which nothing else references.
server-decl       = "SERVER" WSP identifier <PG CREATE SERVER syntax, Part 1 passthrough>
user-mapping-decl = "USER MAPPING FOR" WSP role-spec <PG CREATE USER MAPPING syntax, Part 1 passthrough>

; base-type-decl is BASE type's own production name (Section 5.5) — named here
; since it's referenced from nested-object above; its option list
; (INPUT, OUTPUT, INTERNALLENGTH, ALIGNMENT, etc.) is the same
; comma-separated key=value shape as storage-params, reused here rather
; than inventing a separate name for an identical shape.
base-type-decl = "TYPE" WSP schema-name WSP "(" storage-params ")" ";"

; opaque-object-decl covers every remaining kind whose Part 1 body is
; passed to PostgreSQL's grammar as literal, unmodified PostgreSQL DDL
; (Tenet 2/5's passthrough pattern — the same kind list Section 16.3.1's
; SnapOpaque table already enumerates for the snapshot side): Procedure
; (body only — its declaration head is function-decl's own grammar,
; Section 9.3), Foreign Data Wrapper, Collation, Operator, Operator Class,
; Operator Family, Cast, Extended Statistics Object, and three of the
; four Text Search object kinds (Dictionary, Parser, Template — Text
; Search Configuration is the exception, with real structured grammar
; of its own: tsconfig-decl, Section 12.1).  None of these has (or needs) a
; DPG-specific ABNF production for its Part 1 body beyond its own
; section's worked "PG equivalent" line — inventing one would violate
; Tenet 2 by giving PostgreSQL's own syntax a second, redundant DPG-
; specific name.  Several of them DO carry an optional trailing { }
; block of ordinary lifecycle directives (owner-dir/comment-dir/
; renamed-from-dir), same as every non-opaque kind — that block is DPG
; grammar for concepts with no place in the native CREATE statement, not
; a second name for the passthrough body, so it doesn't violate the
; same rule (Text Search Dictionary/Parser/Template, Sections 12.2-12.4).
opaque-object-decl = <the object kind's own native CREATE statement,
                       verbatim, per its section's "PG equivalent" line>

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
; qual-name (not bare identifier) since Section 7.6 extends this production to
; cross-schema moves: a schema-qualified old name whose schema differs
; from the object's current one additionally triggers ALTER ... SET
; SCHEMA alongside RENAME TO.
renamed-from-dir = "RENAMED FROM" WSP qual-name
protected-dir    = "PROTECTED"
deprecated-dir   = "DEPRECATED" WSP string-literal
drop-cascade-dir = "DROP CASCADE"

; Dollar-quoted string
dollar-string = dollar-delim *<any byte> dollar-delim
dollar-delim  = "$" *( ALPHA / DIGIT / "_" ) "$"

; Type reference
type-ref  = qual-name [ "(" type-mods ")" ] *( "[]" )
type-mods = integer *( "," integer )

; String literal
string-literal = SQUOTE <text> SQUOTE

; Option lists — the "WITH ( name [= value] [, ...] )" shape shared
; verbatim by Table/Index/Sequence/Materialized-View storage params and
; by Publication/Subscription's own WITH options (real PostgreSQL uses
; the identical grammar in both places). A single shared production
; means a later PostgreSQL version's new option (e.g. PG18's
; publish_generated_columns) is automatically expressible with zero
; grammar changes, the same passthrough reasoning Subscription's fully
; opaque body already relies on.
storage-params = storage-param *( "," WSP storage-param )
storage-param  = identifier [ WSP "=" WSP option-value ]
option-value   = string-literal / integer / boolean / identifier
pub-options    = storage-params
sub-options    = storage-params

; Inline index parameters — real PostgreSQL's index_parameters clause,
; usable both on a standalone CREATE INDEX (index-decl, Section 7.7) and
; inline on a PRIMARY KEY/UNIQUE column or table constraint (col-
; constraint/table-constraint-body, Section 7.2), which is what conflict-clause
; exists to name for the latter, narrower position (no WHERE predicate
; or CONCURRENTLY there — those are CREATE-INDEX-only).
conflict-clause = [ WSP "INCLUDE" WSP "(" col-list ")" ]
                  [ WSP "WITH" WSP "(" storage-params ")" ]
                  [ WSP "USING INDEX TABLESPACE" WSP identifier ]

; EXCLUDE constraint element list (Section 7.2) — mirrors real PostgreSQL's
; exclude_element WITH operator repeating group exactly.
excl-list    = excl-element *( "," WSP excl-element )
excl-element = ( identifier / "(" expr ")" )
               [ WSP identifier [ "(" storage-params ")" ] ]
               [ WSP ( "ASC" / "DESC" ) ]
               [ WSP "NULLS" WSP ( "FIRST" / "LAST" ) ]
               WSP "WITH" WSP operator-symbol
operator-symbol = <a PostgreSQL operator name/symbol, e.g. "=", "&&">

; CONSTRAINT TRIGGER deferrability (Section 7.9) — identical real-PostgreSQL
; shape to the table-constraint deferrable clause defined inline at
; Section 7.2, named here since Section 7.9 references it by name.
deferrable-clause = "NOT DEFERRABLE"
                  / "DEFERRABLE" [ WSP ( "INITIALLY DEFERRED"
                                       / "INITIALLY IMMEDIATE" ) ]

; Bare function reference — a (possibly schema-qualified) function name
; with no argument list or parentheses of its own; each call site
; (Trigger's EXECUTE FUNCTION func-ref "(" arg-list ")", Event Trigger's
; EXECUTE FUNCTION func-ref "()", Function's SUPPORT func-ref) appends
; whatever parenthesization its own context requires.
func-ref = qual-name

; Text Search Configuration MAPPING FOR ... WITH ... (Section 12.1)
token-type-list = identifier *( "," WSP identifier )
dict-list       = qual-name *( "," WSP qual-name )

; Event Trigger WHEN TAG IN (...) (Section 14.1) — command tag string list
tag-list = string-literal *( "," WSP string-literal )

; Table-level trailing TABLESPACE clause (Section 4.5's terminator rule) —
; same shape already used inline for table-clause's own TABLESPACE
; alternative (Section 7.1); named here since Section 4.5 references it by name.
tablespace-clause = "TABLESPACE" WSP identifier

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

   Name map directives appear inside any DPG `{ }` block (object-level
   or column-level).  Two forms are supported.

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
   itself.  `ADD COLUMN` of such a column is `SAFE`, like an identity
   column.

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
   when the row's other part is floor-14.  A non-blank cell documents the
   PostgreSQL version requirement.  Which of these the
   `min-pg-version` rule checks is specified in Section 3.8.

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
   | Section 7.2 | Generated columns (`GENERATED ALWAYS AS ... STORED`) | Standard | — | SQL:2003 generated columns. |
   | Section 7.2 | Generated columns (`GENERATED ALWAYS AS ... VIRTUAL`) | Standard | 18 | SQL:2003 generated columns also standardize `VIRTUAL`; PostgreSQL only implemented the `STORED` variant until PostgreSQL 18 added this one. |
   | Section 7.2 | Identity columns (`GENERATED ... AS IDENTITY`) | Standard | — | SQL:2003 identity columns. |
   | Section 7.2 | `SERIAL`/`BIGSERIAL`/`SMALLSERIAL` | PGSpecific | — | Pre-standard PostgreSQL sequence sugar; `IDENTITY` is the standard-conformant replacement. |
   | Section 7.2 | Column `COMPRESSION`/`STORAGE` | PGSpecific | — | PostgreSQL TOAST-related storage tuning. |
   | Section 7.2 | `EXCLUDE` constraints | PGSpecific | — | No standard exclusion-constraint concept. |
   | Section 7.2 | `NOT VALID`/`VALIDATE CONSTRAINT` | PGSpecific | — | PostgreSQL's incremental constraint-validation lifecycle. |
   | Section 7.2 | `NOT NULL ... NO INHERIT` | PGSpecific | 18 | Extends the pre-existing `CHECK ... NO INHERIT` modifier (floor-14 PGSpecific) to `NOT NULL` constraints for the first time. |
   | Section 7.2 | `ENFORCED`/`NOT ENFORCED` (`CHECK`/`FOREIGN KEY`) | PGSpecific | 18 | PostgreSQL's own catalog-recorded-but-unchecked constraint mode; no SQL-standard citation for this exact clause. |
   | Section 7.2 | FK `ON DELETE`/`ON UPDATE SET NULL`/`SET DEFAULT (col-list)` | PGSpecific | 15 | Column-scoped refinement of the standard bare `SET NULL`/`SET DEFAULT` form (the bare form is covered by the `CREATE TABLE` core row above); the column-list itself has no SQL-standard equivalent. |
   | Section 7.3 | Table-level named `NOT NULL` constraint | Standard | 18 | PostgreSQL 18 gave `NOT NULL` the same named, catalogued constraint treatment `CHECK`/`PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` already had; additive to the pre-existing inline column-level form (Section 7.2 row above), not a replacement. |
   | Section 7.3 | Temporal keys (`WITHOUT OVERLAPS`/`PERIOD`, incl. temporal `FOREIGN KEY`) | Mixed | 18 | SQL:2011 standardizes temporal `PRIMARY KEY`/`UNIQUE`; PostgreSQL enforces it via an exclusion search under the hood while keeping the constraint catalogued as an ordinary `PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` — the concept is standard, the grammar and mechanism are PostgreSQL's own dialect. |
   | Section 7.4 | Column `STATISTICS n` / `STATISTICS DEFAULT` | PGSpecific | 17 (`DEFAULT` keyword only; numeric target is floor-14) | Planner statistics-target tuning; no standard equivalent. |
   | Section 7.7 | `CREATE INDEX` core | Mixed | — | Indexes exist informally across all vendors but are not in ISO/IEC 9075 at all — classified PGSpecific as a whole (see next row), since "index" is not a standard DDL concept, only a near-universal vendor extension. |
   | Section 7.7 | `CREATE INDEX` (all forms: access methods, partial, expression, covering, opclass) | PGSpecific | — | No SQL-standard `CREATE INDEX` statement exists; every vendor's index DDL is proprietary. |
   | Section 7.8 | `ROW LEVEL SECURITY`/`CREATE POLICY` | PGSpecific | — | No standard row-security mechanism. |
   | Section 7.9 | `CREATE TRIGGER` core (`BEFORE`/`AFTER`, events, `FOR EACH ROW`) | Standard | — | SQL:1999+ triggers. |
   | Section 7.9 | `REFERENCING OLD/NEW TABLE` (transition tables) | Standard | — | SQL:1999+ triggers include transition tables. |
   | Section 7.9 | `WHEN (condition)`, `EXECUTE FUNCTION` | PGSpecific | — | PostgreSQL trigger-function-calling model differs from the standard's inline trigger action. |
   | Section 7.10 | `GRANTED BY role` | Standard | — | Real PostgreSQL accepts this SQL-standard-compatibility clause but restricts the effective grantor to `current_user` in practice. |
   | Section 7.11 | `RENAMED FROM`, `PROTECTED`, `DEPRECATED`, `DROP CASCADE` (directives) | N/A | — | DPG-native lifecycle metadata. |
   | Section 7.11 | `OWNER` | PGSpecific | — | PostgreSQL's object-ownership model (`ALTER ... OWNER TO`) has no standard equivalent (the standard ties privileges to the creating authorization identifier with no separate transferable "owner"). |
   | Section 7.12 | `UNLOGGED TABLE` | PGSpecific | — | See Section 7.1 row. |
   | Section 7.12 | `CREATE FOREIGN TABLE` | Standard | — | SQL/MED (ISO/IEC 9075-9) standardizes foreign tables; PostgreSQL's FDW mechanism implements it. |
   | Section 7.13 | `PARTITION BY`/`PARTITION OF` | Standard | — | SQL:2016 adds declarative partitioning as a standard concept, though exact grammar varies by vendor; PostgreSQL's own syntax is a PG-specific dialect of a standardized concept — classified Mixed in spirit, PGSpecific in literal grammar. |
   | Section 7.3 | `DROP CONSTRAINT ... ONLY` on a partitioned table (partition-local constraint declaration) | PGSpecific | 18 | PostgreSQL's own declarative-partitioning constraint-inheritance mechanism; no standard concept of detaching a constraint from a partitioned parent while it stays local to existing partitions. |
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
   | Section 10 | `OWNED BY` | PGSpecific | — | PostgreSQL's sequence-to-column ownership link has no standard equivalent. |
   | Section 11.1 | `CREATE ROLE`/`CREATE USER` core (`LOGIN`, `PASSWORD`) | Standard | — | SQL standardizes authorization identifiers, though PostgreSQL's unified role model (merging users and groups) is its own design. |
   | Section 11.1 | `SUPERUSER`/`CREATEDB`/`CREATEROLE`/`REPLICATION`/`BYPASSRLS`/`CONNECTION LIMIT` | PGSpecific | — | PostgreSQL-specific role attributes. |
   | Section 11.2 | `GRANT`/`REVOKE` core | Standard | — | Core SQL privilege model. |
   | Section 7.10 | `MAINTAIN` privilege | PGSpecific | 17 | PostgreSQL-specific privilege covering VACUUM/ANALYZE/CLUSTER/REINDEX. |
   | Section 11.1 | Role membership (`GRANT role TO role`, `WITH ADMIN OPTION`/`WITH INHERIT`/`WITH SET`) | Mixed | 16 (`WITH INHERIT`/`WITH SET` only) | Standard SQL has roles and role grants; `WITH ADMIN OPTION` is standard and floor-14, `WITH INHERIT`/`WITH SET` are PostgreSQL-specific refinements of role-attribute inheritance. |
   | Section 11.4 | `ALTER DEFAULT PRIVILEGES` | PGSpecific | — | No standard mechanism for privilege templates applied to not-yet-created objects. |
   | Section 11.5 | Owner impersonation (`SET ROLE`/`RESET ROLE`) | Standard | — | SQL standardizes `SET ROLE`; PostgreSQL's specific creator-attribution semantics this section documents are PostgreSQL's own catalog behavior. |
   | Section 11.6 | `PARAMETER PRIVILEGES` (`GRANT {SET\|ALTER SYSTEM} ON PARAMETER`) | PGSpecific | 15 | PostgreSQL's grantable parameter-ACL object type; no standard equivalent. |
   | Section 12.1 | `CREATE TEXT SEARCH CONFIGURATION`/`MAPPING FOR ... WITH ...` | PGSpecific | — | PostgreSQL full-text search is entirely proprietary. |
   | Sections 12.2-12.4 | Text Search Dictionary/Parser/Template | PGSpecific | — | Same as above. |
   | Section 13.1 | `CREATE PUBLICATION` | PGSpecific | — | PostgreSQL's own logical-replication publish/subscribe model. |
   | Section 13.2 | `CREATE SUBSCRIPTION` | PGSpecific | — | Same. |
   | Section 14.1 | `CREATE EVENT TRIGGER` | PGSpecific | — | No standard DDL-event trigger mechanism. |
   | Section 14.1 | Event Trigger `login` event | PGSpecific | 17 | One value of the `ON <event>` clause's event-type enum; every other value (`ddl_command_start`/`ddl_command_end`/`table_rewrite`/`sql_drop`) is floor-14. |
   | Section 14.2 | `CREATE COLLATION` | Standard | — | SQL:2003+ standardizes collations; PostgreSQL's ICU/libc provider mechanism is its own extension of the standard concept. |
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
