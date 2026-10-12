package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/introspect"
	"github.com/thec1oud/dpg/internal/ir"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/ui"
)

func newDumpCmd() *cobra.Command {
	var (
		clusterName  string
		databaseName string
		outputDir    string
		yes          bool
	)

	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Introspect a live database and produce initial .dpg source files",
		Long: `Connects to the primary node, reads the live catalog, and writes
.dpg source files and an initial snapshot to the output directory.
Use this to bootstrap a DPG project from an existing database.

If any target file already exists, dump refuses to proceed until you
explicitly confirm the overwrite (or pass --yes) — nothing is ever
silently clobbered.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			proj, err := discoverProject()
			if err != nil {
				return err
			}
			loadEnv(proj, envFile)

			clusters, err := resolveClusters(proj, clusterName)
			if err != nil {
				return err
			}

			introspector, err := pipeline.MustResolve[pipeline.Introspector](pipeline.Default, pipeline.KeyIntrospector)
			if err != nil {
				return err
			}
			store, err := pipeline.MustResolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore)
			if err != nil {
				return err
			}
			secretResolver, err := pipeline.MustResolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
			if err != nil {
				return err
			}

			for _, cl := range clusters {
				databases, err := resolveDatabases(cl, databaseName)
				if err != nil {
					return err
				}
				fmtOpts := format.Options{
					IndentSize:  proj.RootConfig.Fmt.IndentSize,
					KeywordCase: proj.RootConfig.Fmt.KeywordCase,
				}
				if fmtOpts.IndentSize <= 0 {
					fmtOpts.IndentSize = 4
				}
				if fmtOpts.KeywordCase == "" {
					fmtOpts.KeywordCase = "upper"
				}

				clusterOut, dumpStore := dumpClusterTargets(cl, store, outputDir)

				for _, db := range databases {
					out := dumpDatabaseOutputDir(outputDir, db)
					if err := runDump(cl, db, out, clusterOut, introspector, dumpStore, secretResolver, fmtOpts, yes); err != nil {
						return fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to dump (required when multiple clusters exist)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to dump (required when multiple databases exist)")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "output directory (default: cluster/database/ within project root); when set, sandboxes ALL output here, including cluster-level roles.dpg and the snapshot, not just per-database source")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the interactive overwrite confirmation (for scripts/CI)")

	return cmd
}

// dumpClusterTargets decides where cluster-scoped output (roles.dpg) and the
// snapshot for a dump of cluster cl should land this invocation. Without -o
// (outputDir == ""), both keep writing to their real, permanent project
// locations exactly as before -o existed: cl.ObjectsDir and the registered
// store. With -o set, sandbox EVERYTHING dump writes there — cluster-scoped
// roles.dpg and the snapshot otherwise always land in the real project
// regardless of -o (confirmed real, not a bug — was flagged, now fixed).
// Namespaced by cluster name under outputDir so a single -o value covering
// multiple clusters in one invocation doesn't mix their roles.dpg together
// (each cluster's own roles stay distinct output, just no longer
// real-project output).
func dumpClusterTargets(cl *project.Cluster, store pipeline.SnapshotStore, outputDir string) (clusterOut string, dumpStore pipeline.SnapshotStore) {
	if outputDir == "" {
		return cl.ObjectsDir, store
	}
	return filepath.Join(outputDir, cl.Name(), "cluster"),
		&snapshot.FileStore{Dir: filepath.Join(outputDir, ".dpg", "snapshots")}
}

// dumpDatabaseOutputDir decides where a database's per-database dump output
// (schema files, objects.dpg) should land this invocation. Without -o
// (outputDir == ""), it must be db.Dir — the database's own already-resolved
// real directory — not a path reconstructed from declared names
// (cl.Name()/db.Name()). db is only ever reachable here via project
// discovery, which requires its dpg.toml to already exist on disk, so
// db.Dir is always a real, correct answer; reconstructing a name-derived
// path instead silently creates a disconnected sibling directory whenever a
// project's directory name and declared `name =` field diverge (a rename, a
// copy-pasted template, or a typo — nothing prevents this). With -o set,
// behavior is unchanged: outputDir is used verbatim, exactly as before.
func dumpDatabaseOutputDir(outputDir string, db *project.Database) string {
	if outputDir == "" {
		return db.Dir
	}
	return outputDir
}

// confirmOverwrite checks paths for any that already exist on disk and, if
// any do, refuses to let dump proceed until the user explicitly confirms —
// twice: a y/N prompt, then typing the literal word "overwrite" (a second,
// distinct confirmation step, not just repeating y/N — matching the
// type-the-word pattern tools like terraform destroy use for an action this
// hard to reverse). skipConfirm (--yes) bypasses both prompts for
// scripts/CI, but the loud warning listing every file still always prints
// first regardless, so the operator sees exactly what happened even in
// non-interactive use. Returns nil immediately, with no output at all, when
// paths is empty or none of them exist yet — dumping into a brand-new
// project or a fresh scratch directory stays completely frictionless.
// w/r are explicit (rather than hardcoding os.Stderr/os.Stdin) so this is
// unit-testable without a real TTY.
func confirmOverwrite(w io.Writer, r io.Reader, paths []string, skipConfirm bool, color bool) error {
	var existing []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, p)
		}
	}
	if len(existing) == 0 {
		return nil
	}
	sort.Strings(existing)

	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Red(ui.Bold("WARNING: dpg dump will OVERWRITE the following existing file(s):", color), color))
	for _, p := range existing {
		fmt.Fprintln(w, "  "+ui.Yellow(p, color))
	}
	fmt.Fprintln(w, ui.Red("Any hand-edits to these files will be permanently lost.", color))

	if skipConfirm {
		return nil
	}

	scanner := bufio.NewScanner(r)

	fmt.Fprintf(w, "\n%s [y/N] ", ui.Bold("Overwrite these files?", color))
	if !scanner.Scan() || !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
		return fmt.Errorf("aborted: dump would overwrite %d existing file(s); rerun with -o to target a different directory, or --yes to confirm non-interactively", len(existing))
	}

	fmt.Fprintf(w, "%s ", ui.Bold(`Type "overwrite" to confirm:`, color))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "overwrite" {
		return fmt.Errorf("aborted: confirmation text did not match")
	}

	return nil
}

func runDump(
	cl *project.Cluster,
	db *project.Database,
	outDir string,
	clusterObjectsDir string,
	introspector pipeline.Introspector,
	store pipeline.SnapshotStore,
	secretResolver pipeline.SecretResolver,
	fmtOpts format.Options,
	skipConfirm bool,
) error {
	ctx := context.Background()
	color := ui.IsColorEnabled(os.Stdout)

	conn, err := connectDatabase(ctx, cl, db, secretResolver)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var skipped []string
	objects, err := introspector.Introspect(introspect.WithSkipWarnings(ctx, &skipped), conn)
	if err != nil {
		return ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}
	objects = append(objects, mergeExistingVirtualTypes(db)...)

	for _, msg := range skipped {
		fmt.Fprintf(os.Stderr, "%s  %s\n", ui.Yellow("warning", color), msg)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Built once, up front, from every introspected role — a ROLE-direction
	// membership entry's WITH INHERIT compaction (see
	// renderObjectDPGWithRoleInheritDefaults) needs the GRANTEE role's own
	// default, which may not have been rendered (or even reached) yet at
	// the point any single Role object is processed in the loop below.
	roleInheritDefaults := map[string]bool{}
	for _, obj := range objects {
		if r, ok := obj.(*ir.Role); ok && r.Inherit != nil {
			roleInheritDefaults[r.Name] = *r.Inherit
		}
	}

	// Route objects into three buckets: cluster-scoped (roles, tablespaces —
	// shared across databases), database-scoped but schemaless (FDWs, servers,
	// user mappings, publications, event triggers, casts), and schema-scoped
	// (everything owned by a schema).
	schemaFiles := map[string]*strings.Builder{}
	var clusterFile, dbLevelFile strings.Builder
	var dbObjects, clusterObjects []pipeline.IRObject
	for _, obj := range objects {
		if um, ok := obj.(*ir.UserMapping); ok && strings.Contains(um.Body, introspect.UserMappingRedactedPlaceholder) {
			fmt.Fprintf(os.Stderr, "%s  user mapping %s: a password-like OPTIONS value was redacted; replace the placeholder with a {{secret-uri}} reference before applying\n",
				ui.Yellow("warning", color), um.QualifiedName())
		}
		if isClusterScoped(obj) {
			renderObjectDPGWithRoleInheritDefaults(&clusterFile, obj, fmtOpts, roleInheritDefaults)
			clusterObjects = append(clusterObjects, obj)
			continue
		}
		schema := objectSchema(obj)
		if schema == "" {
			renderObjectDPGWithRoleInheritDefaults(&dbLevelFile, obj, fmtOpts, roleInheritDefaults)
			dbObjects = append(dbObjects, obj)
			continue
		}
		if _, ok := schemaFiles[schema]; !ok {
			schemaFiles[schema] = &strings.Builder{}
		}
		renderObjectDPGWithRoleInheritDefaults(schemaFiles[schema], obj, fmtOpts, roleInheritDefaults)
		dbObjects = append(dbObjects, obj)
	}

	// Confirm before overwriting anything that already exists on disk — every
	// path any of the write loops below is about to touch, computed up front
	// so the check happens before the FIRST byte is written, not partway
	// through. Previously dump silently clobbered pre-existing .dpg source
	// with zero confirmation of any kind (a real incident during a prior
	// session recovered only via transcript reconstruction — see
	// .dpg-notes/dpg-tracker.md); this closes that gap for good. The
	// snapshot is deliberately excluded — it's a derived/managed artifact
	// dump is always expected to refresh, not hand-authored source.
	var candidatePaths []string
	for schema := range schemaFiles {
		candidatePaths = append(candidatePaths, filepath.Join(outDir, "schemas", schema, "schema.dpg"))
	}
	if dbLevelFile.Len() > 0 {
		candidatePaths = append(candidatePaths, filepath.Join(outDir, "objects.dpg"))
	}
	if clusterFile.Len() > 0 {
		candidatePaths = append(candidatePaths, filepath.Join(clusterObjectsDir, "roles.dpg"))
	}
	if err := confirmOverwrite(os.Stderr, os.Stdin, candidatePaths, skipConfirm, color); err != nil {
		return err
	}

	// Write DB-level schema files.
	var dpgFiles []string
	for schema, content := range schemaFiles {
		schemaDir := filepath.Join(outDir, "schemas", schema)
		if err := os.MkdirAll(schemaDir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(schemaDir, "schema.dpg")
		if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
			return err
		}
		dpgFiles = append(dpgFiles, path)
		ui.PrintInfo(os.Stdout, "wrote", path, color)
	}

	// Write database-scoped schemaless objects to a database-level file.
	if dbLevelFile.Len() > 0 {
		path := filepath.Join(outDir, "objects.dpg")
		if err := os.WriteFile(path, []byte(dbLevelFile.String()), 0o644); err != nil {
			return err
		}
		dpgFiles = append(dpgFiles, path)
		ui.PrintInfo(os.Stdout, "wrote", path, color)
	}

	// Write cluster-level objects (roles, tablespaces) to the cluster objects
	// dir — clusterObjectsDir, NOT necessarily cl.ObjectsDir: when -o is set,
	// the caller passes a sandboxed path under outDir instead, so a scratch
	// dump never touches the real project's cluster/roles.dpg (see newDumpCmd).
	var clusterDPGFiles []string
	if clusterFile.Len() > 0 {
		if err := os.MkdirAll(clusterObjectsDir, 0o755); err != nil {
			return fmt.Errorf("create cluster objects directory: %w", err)
		}
		path := filepath.Join(clusterObjectsDir, "roles.dpg")
		if err := os.WriteFile(path, []byte(clusterFile.String()), 0o644); err != nil {
			return err
		}
		clusterDPGFiles = append(clusterDPGFiles, path)
		ui.PrintInfo(os.Stdout, "wrote", path, color)
	}

	// Build DB snapshot from compiled source (ensures plan produces no diff).
	// If the freshly written .dpg files fail to recompile, fall back to the raw
	// introspected objects but surface the error — a silent fallback would hide a
	// dump that emits non-compiling source (the snapshot would then diverge from
	// what `plan` reads back).
	dbSnapObjects := dbObjects
	if len(dpgFiles) > 0 {
		if compiled, _, compileErr := compiler.CompileWith(dpgFiles, outDir, pipeline.Default, dbCompileOptions(db)); compileErr == nil {
			dbSnapObjects = compiled
		} else {
			fmt.Fprintf(os.Stderr, "%s  dumped source did not recompile for %s/%s; snapshot built from live catalog instead: %v\n",
				ui.Yellow("warning", color), cl.Name(), db.Name(), compileErr)
		}
	}
	dbSnap := &pipeline.Snapshot{}
	if err := snapshot.Populate(dbSnap, dbSnapObjects); err != nil {
		return fmt.Errorf("build snapshot: %w", err)
	}
	if err := store.Save(cl.Name(), db.Name(), dbSnap); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	ui.PrintSuccess(os.Stdout, "DB snapshot written", cl.Name()+"/"+db.Name(), color)

	// Build cluster snapshot (roles, tablespaces). These are cluster-global, so
	// every database dumps identical content — written once per cluster, safe to repeat.
	if len(clusterObjects) > 0 {
		clusterSnapObjects := clusterObjects
		if len(clusterDPGFiles) > 0 {
			if compiled, _, compileErr := compiler.Compile(clusterDPGFiles, clusterObjectsDir, pipeline.Default); compileErr == nil {
				clusterSnapObjects = compiled
			} else {
				fmt.Fprintf(os.Stderr, "%s  dumped cluster source did not recompile for %s; snapshot built from live catalog instead: %v\n",
					ui.Yellow("warning", color), cl.Name(), compileErr)
			}
		}
		clusterSnap := &pipeline.Snapshot{}
		if err := snapshot.Populate(clusterSnap, clusterSnapObjects); err != nil {
			return fmt.Errorf("build cluster snapshot: %w", err)
		}
		if err := store.Save(cl.Name(), cl.ClusterSnapshotKey(), clusterSnap); err != nil {
			return fmt.Errorf("save cluster snapshot: %w", err)
		}
		ui.PrintSuccess(os.Stdout, "Cluster snapshot written", cl.Name(), color)
	}
	return nil
}

// mergeExistingVirtualTypes recovers any VIRTUAL TYPE declarations (RFC
// Section 5.6) from the project's own current source before dump overwrites it.
// Virtual types are DPG-native — no CREATE TYPE is ever emitted for one, so
// nothing about them exists in the live catalog for introspector.Introspect
// to find. Without this, running `dpg dump` against an already-mature
// project silently dropped every declared virtual type from both the
// regenerated source AND the snapshot (confirmed live: a project with 2
// declared virtual types had 0 left in its snapshot immediately after a
// dump run) — genuine, silent data loss of the exact structural type
// information the snapshot exists to preserve for downstream consumers
// (pkg/dpg, RFC Section 5.6). A brand-new project (db.SourceFiles empty, nothing
// to bootstrap from) returns nil, matching dump's inherent inability to
// discover virtual types from a live database in that case.
func mergeExistingVirtualTypes(db *project.Database) []pipeline.IRObject {
	if len(db.SourceFiles) == 0 {
		return nil
	}
	compiled, _, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, dbCompileOptions(db))
	if err != nil {
		// Not fatal to the dump itself — the live catalog's own objects are
		// still valid and worth writing. Surfaced so the loss isn't silent.
		fmt.Fprintf(os.Stderr, "warning  existing source did not recompile; virtual types could not be recovered: %v\n", err)
		return nil
	}
	var vtypes []pipeline.IRObject
	for _, obj := range compiled {
		if vt, ok := obj.(*ir.VirtualType); ok {
			vtypes = append(vtypes, vt)
		}
	}
	return vtypes
}

// isClusterScoped reports whether obj lives at the cluster level (shared across
// every database) rather than inside one database. Roles, tablespaces, and
// PARAMETER PRIVILEGES (RFC Section 11.6 — configuration parameters have no
// schema and no per-database identity; the RFC's own worked example shows it
// living at "production/cluster/parameter_privileges.dpg") qualify; all other
// schemaless objects (FDWs, servers, user mappings, publications, event
// triggers, casts) are database-scoped.
func isClusterScoped(obj pipeline.IRObject) bool {
	switch obj.(type) {
	case *ir.Role, *ir.Tablespace, *ir.ParameterPrivileges:
		return true
	}
	return false
}

// excludeClusterScoped returns the objects that belong to a database (i.e. not
// cluster-scoped), preserving order. Used to build database-level baselines for
// verify and plan --live so roles/tablespaces are handled only at cluster level.
func excludeClusterScoped(objs []pipeline.IRObject) []pipeline.IRObject {
	out := make([]pipeline.IRObject, 0, len(objs))
	for _, obj := range objs {
		if !isClusterScoped(obj) {
			out = append(out, obj)
		}
	}
	return out
}

// objectSchema returns the schema name for schema-scoped objects.
func objectSchema(obj pipeline.IRObject) string {
	switch o := obj.(type) {
	case *ir.Table:
		return o.Schema
	case *ir.View:
		return o.Schema
	case *ir.Function:
		return o.Schema
	case *ir.Procedure:
		return o.Schema
	case *ir.Aggregate:
		return o.Schema
	case *ir.Type:
		return o.Schema
	case *ir.Sequence:
		return o.Schema
	case *ir.Collation:
		return o.Schema
	case *ir.StatisticsObject:
		return o.Schema
	case *ir.Operator:
		return o.Schema
	case *ir.OperatorClass:
		return o.Schema
	case *ir.OperatorFamily:
		return o.Schema
	case *ir.TSConfig:
		return o.Schema
	case *ir.TSDict:
		return o.Schema
	case *ir.TSParser:
		return o.Schema
	case *ir.TSTemplate:
		return o.Schema
	case *ir.VirtualType:
		return o.Schema
	case *ir.DefaultPrivileges:
		if o.InSchema != nil {
			return *o.InSchema
		}
	}
	return ""
}

// renderObjectDPG writes a minimal DPG declaration for obj into b using fmtOpts
// for keyword case and indentation.
// renderObjectDPG renders obj with no cross-role context available — the
// ROLE case's WITH INHERIT compaction for a ROLE-direction membership entry
// (see renderObjectDPGWithRoleInheritDefaults) never fires here, since that
// needs the OTHER role's own Inherit default, which a single object alone
// can't provide. Every caller except dump's own runDump (which has the full
// introspected role list available) uses this.
// writeIdentityOptsDump appends id's identity-opts clause (RFC Section 7.2)
// to b in dump-rendering form, mirroring internal/diff's writeIdentityOpts
// but using kw for keyword casing like the rest of this file's renderers.
func writeIdentityOptsDump(b *strings.Builder, id *ir.Identity, kw func(string) string) {
	var opts strings.Builder
	if id.StartValue != nil {
		fmt.Fprintf(&opts, " %s %d", kw("START WITH"), *id.StartValue)
	}
	if id.IncrementBy != nil {
		fmt.Fprintf(&opts, " %s %d", kw("INCREMENT BY"), *id.IncrementBy)
	}
	if id.NoMinValue {
		fmt.Fprintf(&opts, " %s", kw("NO MINVALUE"))
	} else if id.MinValue != nil {
		fmt.Fprintf(&opts, " %s %d", kw("MINVALUE"), *id.MinValue)
	}
	if id.NoMaxValue {
		fmt.Fprintf(&opts, " %s", kw("NO MAXVALUE"))
	} else if id.MaxValue != nil {
		fmt.Fprintf(&opts, " %s %d", kw("MAXVALUE"), *id.MaxValue)
	}
	if id.Cache != nil {
		fmt.Fprintf(&opts, " %s %d", kw("CACHE"), *id.Cache)
	}
	if id.Cycle != nil {
		if *id.Cycle {
			fmt.Fprintf(&opts, " %s", kw("CYCLE"))
		} else {
			fmt.Fprintf(&opts, " %s", kw("NO CYCLE"))
		}
	}
	if opts.Len() == 0 {
		return
	}
	b.WriteString(" (")
	b.WriteString(strings.TrimSpace(opts.String()))
	b.WriteString(")")
}

func renderObjectDPG(b *strings.Builder, obj pipeline.IRObject, fmtOpts format.Options) {
	renderObjectDPGWithRoleInheritDefaults(b, obj, fmtOpts, nil)
}

// renderObjectDPGWithRoleInheritDefaults is renderObjectDPG plus
// roleInheritDefaults: a role name -> rolinherit map built from every
// introspected role in the current dump run, used only to compact a
// ROLE-direction membership's WITH INHERIT clause when it matches the
// target role's own default (RFC audit item #32's remaining dump-rendering
// gap — the IN_ROLE-direction case below already does this trivially via
// o.Inherit, since o IS the grantee there). nil (or a name absent from it)
// simply means "unknown default," which renders explicitly rather than
// guessing.
func renderObjectDPGWithRoleInheritDefaults(b *strings.Builder, obj pipeline.IRObject, fmtOpts format.Options, roleInheritDefaults map[string]bool) {
	ind := fmtOpts.Indent()
	kw := fmtOpts.Keyword

	switch o := obj.(type) {
	case *ir.Table:
		inlinedByCol := map[string][]string{}
		// notNullInlined tracks columns whose NOT NULL is rendered via an
		// inlined "CONSTRAINT ... NOT NULL [NO INHERIT]"/"NOT NULL [NO
		// INHERIT]" clause (added to inlinedByCol below) rather than the
		// plain Column.NotNull bool — renderColText must suppress its own
		// bare " NOT NULL" for these, or both would render redundantly.
		notNullInlined := map[string]bool{}
		var refCSTs []*ir.Constraint
		var otherCSTs []*ir.Constraint
		// blockCSTs holds constraints carrying a Comment: real PostgreSQL's
		// native CREATE TABLE column/constraint-list grammar (what every
		// other bucket below renders into) has no way to attach a comment to
		// a constraint inline, so these render instead as CONSTRAINTS { }
		// block entries (see writeEntryCommentBlock) — the DPG-block-level
		// declaration path, parsed by the blockparser rather than pg_query,
		// which is the only place this session's new "{ COMMENT '...'; }"
		// grammar is actually valid syntax.
		var blockCSTs []*ir.Constraint
		for _, cst := range o.Constraints {
			if cst.Comment != nil {
				blockCSTs = append(blockCSTs, cst)
			} else if len(cst.Columns) == 1 && isInlineable(cst.Type) && !cst.NotValid && !cst.NotEnforced {
				// !cst.NotValid matches CHECK's existing exclusion from
				// inlining (see isInlineable/inlineConstraintClause):
				// NOT VALID has no inline column-constraint rendering, so
				// it must stay a table-level item, the only place
				// renderCSTText below actually appends it. !cst.NotEnforced
				// is a deliberate simplification, not a limitation (real
				// PostgreSQL's inline grammar does support ENFORCED/NOT
				// ENFORCED, unlike NOT VALID) — routing the rare NOT
				// ENFORCED case through the one place NOT ENFORCED is
				// already rendered (below) avoids a second rendering path
				// for it.
				inlinedByCol[cst.Columns[0]] = append(inlinedByCol[cst.Columns[0]], inlineConstraintClause(cst))
				if cst.Type == "NOT NULL" {
					notNullInlined[cst.Columns[0]] = true
				}
			} else if cst.Type == "FOREIGN KEY" {
				refCSTs = append(refCSTs, cst)
			} else {
				otherCSTs = append(otherCSTs, cst)
			}
		}

		type tableItem struct {
			section string
			text    string
		}
		var items []tableItem

		renderColText := func(col *ir.Column) string {
			var sb strings.Builder
			if col.Serial != nil {
				// Render the SERIAL sugar keyword directly rather than the
				// normalized underlying type — this is what makes a dumped
				// SERIAL column reapply cleanly (the underlying-type form
				// alone would need a manually-declared sequence + DEFAULT
				// nextval(...) that dump never emits, since the owned
				// sequence is deliberately excluded from introspection).
				fmt.Fprintf(&sb, "%s%s %s", ind, quoteIdentIfNeeded(col.Name), kw(*col.Serial))
			} else {
				fmt.Fprintf(&sb, "%s%s %s", ind, quoteIdentIfNeeded(col.Name), col.Type.String())
			}
			if col.NotNull && col.Identity == nil && col.Serial == nil && !notNullInlined[col.Name] {
				fmt.Fprintf(&sb, " %s %s", kw("NOT"), kw("NULL"))
			}
			if col.Default != nil && col.Serial == nil {
				fmt.Fprintf(&sb, " %s %s", kw("DEFAULT"), *col.Default)
			}
			if col.Identity != nil {
				if col.Identity.Always {
					fmt.Fprintf(&sb, " %s %s %s %s", kw("GENERATED"), kw("ALWAYS"), kw("AS"), kw("IDENTITY"))
				} else {
					fmt.Fprintf(&sb, " %s %s %s %s %s", kw("GENERATED"), kw("BY"), kw("DEFAULT"), kw("AS"), kw("IDENTITY"))
				}
				writeIdentityOptsDump(&sb, col.Identity, kw)
			}
			if col.Generated != nil {
				genKw := kw("STORED")
				if !col.Generated.Stored {
					genKw = kw("VIRTUAL")
				}
				fmt.Fprintf(&sb, " %s %s %s (%s) %s", kw("GENERATED"), kw("ALWAYS"), kw("AS"), col.Generated.Expr, genKw)
			}
			for _, clause := range inlinedByCol[col.Name] {
				fmt.Fprintf(&sb, " %s", clause)
			}
			return sb.String()
		}
		for _, col := range o.Columns {
			items = append(items, tableItem{section: classifyColumn(col), text: renderColText(col)})
		}
		renderCSTText := func(cst *ir.Constraint) string {
			// NOT VALID is real PostgreSQL's own CREATE TABLE constraint-attribute
			// grammar (unlike COMMENT, which has no inline slot at all — see
			// blockCSTs above), so it renders safely as a trailing suffix here.
			notValid := ""
			if cst.NotValid {
				notValid = fmt.Sprintf(" %s %s", kw("NOT"), kw("VALID"))
			}
			enforcedClause := ""
			if cst.NotEnforced {
				enforcedClause = fmt.Sprintf(" %s %s", kw("NOT"), kw("ENFORCED"))
			}
			if cst.Name != "" {
				return fmt.Sprintf("%s%s %s %s%s%s", ind, kw("CONSTRAINT"), quoteIdentIfNeeded(cst.Name), cst.Expr, notValid, enforcedClause)
			}
			return ind + cst.Expr + notValid + enforcedClause
		}
		for _, cst := range refCSTs {
			items = append(items, tableItem{section: "references", text: renderCSTText(cst)})
		}
		for _, cst := range otherCSTs {
			items = append(items, tableItem{section: "constraints", text: renderCSTText(cst)})
		}

		sectionOrder := map[string]int{"": 0, "lifecycle": 1, "timestamps": 2, "references": 3, "constraints": 4}
		sort.SliceStable(items, func(i, j int) bool {
			return sectionOrder[items[i].section] < sectionOrder[items[j].section]
		})

		tableKW := kw("TABLE")
		switch {
		case o.Unlogged:
			tableKW = kw("UNLOGGED") + " " + kw("TABLE")
		case o.Foreign:
			tableKW = kw("FOREIGN") + " " + kw("TABLE")
		}
		fmt.Fprintf(b, "\n%s %s", tableKW, quoteIdentIfNeeded(o.Name))
		if o.OfType.Name != "" {
			// RFC Section 7.1's "Form 2" typed table. Real PostgreSQL
			// rejects empty parens outright ("... OF ty ()" is a syntax
			// error), so they're omitted entirely when items (populated
			// only from table-level constraints for a typed table — see
			// ir.Table.OfType's doc comment) is empty.
			fmt.Fprintf(b, " %s %s", kw("OF"), o.OfType.String())
		}
		if o.OfType.Name == "" || len(items) > 0 {
			b.WriteString(" (\n")
			hasContent := false
			prevSection := "__none__"
			for i, item := range items {
				sep := ","
				if i == len(items)-1 {
					sep = ""
				}
				if item.section != prevSection {
					if item.section != "" {
						if hasContent {
							b.WriteString("\n")
						}
						fmt.Fprintf(b, "%s-- %s\n", ind, item.section)
					}
					prevSection = item.section
				}
				fmt.Fprintf(b, "%s%s\n", item.text, sep)
				hasContent = true
			}
			b.WriteString(")")
		}
		// INHERITS, then PARTITION BY, then USING, then WITH (...): real
		// PostgreSQL's own fixed CREATE TABLE clause order (confirmed live
		// via pg_query.Parse) — INHERITS/PARTITION BY must precede USING,
		// which must precede WITH. The reverse ordering of any of these is
		// an outright syntax error whenever a table declares more than one
		// of them together, so a `dpg dump` round-trip of such a table
		// previously produced unparseable DPG source (INHERITS was rendered
		// after Foreign/Tablespace, USING before PARTITION BY).
		if len(o.Inherits) > 0 {
			parents := make([]string, len(o.Inherits))
			for i, p := range o.Inherits {
				parents[i] = quoteQualIdentIfNeeded(p)
			}
			fmt.Fprintf(b, " %s (%s)", kw("INHERITS"), strings.Join(parents, ", "))
		}
		if o.PartitionBy != nil {
			fmt.Fprintf(b, " %s %s %s (%s)", kw("PARTITION"), kw("BY"), kw(o.PartitionBy.Strategy), strings.Join(o.PartitionBy.Columns, ", "))
		}
		if o.AccessMethod != "" {
			fmt.Fprintf(b, " %s %s", kw("USING"), o.AccessMethod)
		}
		if len(o.StorageParams) > 0 {
			parts := make([]string, len(o.StorageParams))
			for i, p := range o.StorageParams {
				parts[i] = fmt.Sprintf("%s=%s", p.Key, p.Value)
			}
			fmt.Fprintf(b, " %s (%s)", kw("WITH"), strings.Join(parts, ", "))
		}
		if o.Foreign {
			if o.ForeignServer != nil {
				fmt.Fprintf(b, " %s %s", kw("SERVER"), quoteIdentIfNeeded(*o.ForeignServer))
			}
			if len(o.ForeignOptions) > 0 {
				parts := make([]string, len(o.ForeignOptions))
				for i, p := range o.ForeignOptions {
					parts[i] = fmt.Sprintf("%s %s", p.Key, sqlStringLit(p.Value))
				}
				fmt.Fprintf(b, " %s (%s)", kw("OPTIONS"), strings.Join(parts, ", "))
			}
		} else if o.Tablespace != nil {
			fmt.Fprintf(b, " %s %s", kw("TABLESPACE"), quoteIdentIfNeeded(*o.Tablespace))
		}
		var colsWithAttrs []*ir.Column
		for _, col := range o.Columns {
			hasStorage := col.Storage != nil && !col.StorageIsTypeDefault
			if col.Comment != nil || hasStorage || col.Compression != nil || col.Statistics != nil ||
				len(col.Grants) > 0 || len(col.Revocations) > 0 || len(col.SecurityLabels) > 0 {
				colsWithAttrs = append(colsWithAttrs, col)
			}
		}
		replicaIdentityDeclared := o.ReplicaIdentity.Mode != "" && o.ReplicaIdentity.Mode != "DEFAULT"
		if o.Owner != nil || o.Comment != nil || o.RLSEnabled || o.RLSForced || replicaIdentityDeclared || o.ClusterOn != nil ||
			len(o.Indexes) > 0 || len(colsWithAttrs) > 0 ||
			len(o.Partitions) > 0 || len(o.Policies) > 0 || len(o.Triggers) > 0 || len(o.Grants) > 0 || len(o.Revocations) > 0 ||
			len(o.SecurityLabels) > 0 || len(blockCSTs) > 0 {
			b.WriteString(" {\n")
			blockHasContent := false
			if o.Owner != nil {
				fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
				blockHasContent = true
			}
			if o.Comment != nil {
				fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
				blockHasContent = true
			}
			if o.RLSEnabled {
				fmt.Fprintf(b, "%s%s %s %s %s;\n", ind, kw("ENABLE"), kw("ROW"), kw("LEVEL"), kw("SECURITY"))
				blockHasContent = true
			}
			if o.RLSForced {
				fmt.Fprintf(b, "%s%s %s %s %s;\n", ind, kw("FORCE"), kw("ROW"), kw("LEVEL"), kw("SECURITY"))
				blockHasContent = true
			}
			// REPLICA IDENTITY DEFAULT is PostgreSQL's own default (and the
			// zero value here) — rendered only when the table actually
			// declares something else, same "don't clutter every table"
			// convention as StorageIsTypeDefault above.
			if replicaIdentityDeclared {
				switch o.ReplicaIdentity.Mode {
				case "INDEX":
					fmt.Fprintf(b, "%s%s %s %s %s %s;\n", ind, kw("REPLICA"), kw("IDENTITY"), kw("USING"), kw("INDEX"), quoteIdentIfNeeded(o.ReplicaIdentity.IndexName))
				default:
					fmt.Fprintf(b, "%s%s %s %s;\n", ind, kw("REPLICA"), kw("IDENTITY"), kw(o.ReplicaIdentity.Mode))
				}
				blockHasContent = true
			}
			if o.ClusterOn != nil {
				fmt.Fprintf(b, "%s%s %s %s;\n", ind, kw("CLUSTER"), kw("ON"), quoteIdentIfNeeded(*o.ClusterOn))
				blockHasContent = true
			}
			if len(colsWithAttrs) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode A (COLUMNS { … }) — each entry omits the COLUMN verb:
				// "name { COMMENT/STORAGE/COMPRESSION/STATISTICS … }". These
				// attributes are genuinely diffed (diffColumns) but were
				// previously never rendered by dump, so a dumped project
				// silently never detected drift on any of them. STORAGE is
				// suppressed when it's just the column's type's own default
				// (col.StorageIsTypeDefault, set by introspection from
				// pg_type.typstorage) — unlike the others, every real column
				// has a concrete storage mode, so rendering it unconditionally
				// would add a STORAGE line to nearly every variable-length
				// column in every dumped table for no reason.
				fmt.Fprintf(b, "%s%s {\n", ind, kw("COLUMNS"))
				colInd := ind + ind
				for _, col := range colsWithAttrs {
					fmt.Fprintf(b, "%s%s {\n", ind, quoteIdentIfNeeded(col.Name))
					if col.Comment != nil {
						fmt.Fprintf(b, "%s%s %s;\n", colInd, kw("COMMENT"), sqlStringLit(*col.Comment))
					}
					if col.Storage != nil && !col.StorageIsTypeDefault {
						fmt.Fprintf(b, "%s%s %s;\n", colInd, kw("STORAGE"), quoteIdentIfNeeded(*col.Storage))
					}
					if col.Compression != nil {
						fmt.Fprintf(b, "%s%s %s;\n", colInd, kw("COMPRESSION"), quoteIdentIfNeeded(*col.Compression))
					}
					if col.Statistics != nil {
						fmt.Fprintf(b, "%s%s %d;\n", colInd, kw("STATISTICS"), *col.Statistics)
					}
					for _, g := range col.Grants {
						priv := "ALL"
						if len(g.Privileges) > 0 {
							priv = strings.Join(g.Privileges, ", ")
						}
						fmt.Fprintf(b, "%s%s %s %s %s", colInd, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
						if g.WithGrant {
							fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
						}
						b.WriteString(";\n")
					}
					for _, r := range col.Revocations {
						priv := "ALL"
						if len(r.Privileges) > 0 {
							priv = strings.Join(r.Privileges, ", ")
						}
						fmt.Fprintf(b, "%s%s %s %s %s", colInd, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
						if r.Cascade {
							fmt.Fprintf(b, " %s", kw("CASCADE"))
						}
						b.WriteString(";\n")
					}
					writeSecurityLabels(b, colInd, fmtOpts, col.SecurityLabels)
					fmt.Fprintf(b, "%s}\n", ind)
				}
				fmt.Fprintf(b, "%s}\n", ind)
				blockHasContent = true
			}
			if len(o.Indexes) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode A (INDEXES { … }) — the form the scanner accepts. Each entry
				// omits the INDEX verb: "name [UNIQUE] (cols) [USING m] [WHERE …];".
				fmt.Fprintf(b, "%s%s {\n", ind, kw("INDEXES"))
				for _, idx := range o.Indexes {
					renderIndex(b, idx, fmtOpts)
				}
				fmt.Fprintf(b, "%s}\n", ind)
				blockHasContent = true
			}
			if len(blockCSTs) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode A (CONSTRAINTS { … }) — the form the scanner accepts.
				// Only constraints carrying a Comment land here; every other
				// constraint still renders inline in the column/constraint
				// list above (see blockCSTs' doc comment).
				fmt.Fprintf(b, "%s%s {\n", ind, kw("CONSTRAINTS"))
				for _, cst := range blockCSTs {
					fmt.Fprintf(b, "%s%s %s", ind+ind, quoteIdentIfNeeded(cst.Name), cst.Expr)
					if cst.NotValid {
						fmt.Fprintf(b, " %s %s", kw("NOT"), kw("VALID"))
					}
					writeEntryCommentBlock(b, ind, fmtOpts, cst.Comment)
				}
				fmt.Fprintf(b, "%s}\n", ind)
				blockHasContent = true
			}
			if len(o.Partitions) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode A (PARTITIONS { … }) — the form the scanner accepts. A
				// partition entry may itself be sub-partitioned (RFC Section 7.13),
				// rendered as a trailing "PARTITION BY strategy (cols) { PARTITIONS
				// {...} }" clause, recursively.
				fmt.Fprintf(b, "%s%s {\n", ind, kw("PARTITIONS"))
				for _, p := range o.Partitions {
					renderPartitionEntry(b, p, ind+ind, fmtOpts)
				}
				fmt.Fprintf(b, "%s}\n", ind)
				blockHasContent = true
			}
			if len(o.Policies) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode A (POLICIES { … }) — the form the scanner accepts. Each
				// entry omits the POLICY verb, rendered in real PG's own
				// CREATE POLICY clause order: [AS PERMISSIVE|RESTRICTIVE]
				// FOR command [TO roles] [USING (...)] [WITH CHECK (...)].
				fmt.Fprintf(b, "%s%s {\n", ind, kw("POLICIES"))
				for _, pol := range o.Policies {
					renderPolicy(b, pol, fmtOpts)
				}
				fmt.Fprintf(b, "%s}\n", ind)
				blockHasContent = true
			}
			if len(o.Triggers) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode A (TRIGGERS { … }) — the form the scanner accepts. Each
				// entry omits the TRIGGER verb.
				fmt.Fprintf(b, "%s%s {\n", ind, kw("TRIGGERS"))
				for _, trg := range o.Triggers {
					renderTrigger(b, trg, fmtOpts)
				}
				fmt.Fprintf(b, "%s}\n", ind)
				blockHasContent = true
			}
			if len(o.Grants) > 0 || len(o.Revocations) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				// Mode B (Section 4.8 Dual Definition Modes) — matches writeViewBlock's
				// existing flat-directive style for object-level grants.
				for _, g := range o.Grants {
					priv := "ALL"
					if len(g.Privileges) > 0 {
						priv = strings.Join(g.Privileges, ", ")
					}
					fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
					if g.WithGrant {
						fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
					}
					b.WriteString(";\n")
				}
				for _, r := range o.Revocations {
					priv := "ALL"
					if len(r.Privileges) > 0 {
						priv = strings.Join(r.Privileges, ", ")
					}
					fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
					if r.Cascade {
						fmt.Fprintf(b, " %s", kw("CASCADE"))
					}
					b.WriteString(";\n")
				}
			}
			if len(o.SecurityLabels) > 0 {
				if blockHasContent {
					b.WriteString("\n")
				}
				writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
			}
			b.WriteString("}")
		}
		b.WriteString(";\n")

	case *ir.View:
		// Previously always rendered the bare "VIEW" keyword regardless of
		// o.Materialized, and dropped Owner/Comment/Grants/Revocations/
		// WithNoData entirely (none of it was ever read here) — found live-
		// testing a demo project: a genuine MATERIALIZED VIEW round-tripped
		// through dump as a plain VIEW, silently losing the object kind.
		//
		// o.Recursive deliberately does NOT get its own "RECURSIVE VIEW"
		// keyword here: that special PG syntax requires an explicit column
		// list DPG doesn't track, and since dump only ever renders
		// INTROSPECTED objects, o.Query already comes back from
		// pg_get_viewdef as a self-contained "WITH RECURSIVE ... " CTE —
		// which is already valid, self-sufficient recursion under a plain
		// VIEW (the same reconstruction pg_dump itself uses).
		viewKW := kw("VIEW")
		if o.Materialized {
			viewKW = kw("MATERIALIZED") + " " + kw("VIEW")
		}
		fmt.Fprintf(b, "\n%s %s", viewKW, quoteIdentIfNeeded(o.Name))
		if len(o.Columns) > 0 {
			b.WriteString(" (")
			for i, c := range o.Columns {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(quoteIdentIfNeeded(c))
			}
			b.WriteString(")")
		}
		if len(o.StorageParams) > 0 {
			fmt.Fprintf(b, " %s (", kw("WITH"))
			for i, p := range o.StorageParams {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%s=%s", p.Key, p.Value)
			}
			b.WriteString(")")
		}
		if o.Materialized && o.Tablespace != nil {
			fmt.Fprintf(b, " %s %s", kw("TABLESPACE"), quoteIdentIfNeeded(*o.Tablespace))
		}
		fmt.Fprintf(b, " %s %s", kw("AS"), o.Query)
		if o.Materialized && o.WithNoData {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("NO"), kw("DATA"))
		}
		writeViewBlock(b, ind, fmtOpts, o)

	case *ir.Function:
		b.WriteString("\n")
		b.WriteString(kw("FUNCTION"))
		b.WriteString(" ")
		b.WriteString(quoteIdentIfNeeded(o.Name))
		b.WriteString("(")
		writeFuncArgs(b, o.Args)
		b.WriteString(") ")
		b.WriteString(kw("RETURNS"))
		b.WriteString(" ")
		if tableCols := ir.FuncTableColumns(o.Args); len(tableCols) > 0 {
			b.WriteString(kw("TABLE"))
			b.WriteString("(")
			b.WriteString(ir.FormatTableColumns(tableCols))
			b.WriteString(")")
		} else {
			if o.ReturnType.SetOf {
				b.WriteString(kw("SETOF"))
				b.WriteString(" ")
			}
			b.WriteString(o.ReturnType.String())
		}
		b.WriteString(" ")
		b.WriteString(kw("LANGUAGE"))
		b.WriteString(" ")
		b.WriteString(o.Attrs.Language)
		writeFuncTransforms(b, fmtOpts, o.Attrs.Transforms)
		if o.Attrs.Volatility != "" && o.Attrs.Volatility != "VOLATILE" {
			b.WriteString(" ")
			b.WriteString(kw(o.Attrs.Volatility))
		}
		if o.Attrs.Leakproof {
			fmt.Fprintf(b, " %s", kw("LEAKPROOF"))
		}
		if o.Attrs.Strict {
			fmt.Fprintf(b, " %s", kw("STRICT"))
		}
		if o.Attrs.SecurityDef {
			fmt.Fprintf(b, " %s %s", kw("SECURITY"), kw("DEFINER"))
		}
		if o.Attrs.Parallel != "" && o.Attrs.Parallel != "UNSAFE" {
			fmt.Fprintf(b, " %s %s", kw("PARALLEL"), kw(o.Attrs.Parallel))
		}
		if o.Attrs.Cost != nil {
			fmt.Fprintf(b, " %s %v", kw("COST"), *o.Attrs.Cost)
		}
		if o.Attrs.Rows != nil {
			fmt.Fprintf(b, " %s %v", kw("ROWS"), *o.Attrs.Rows)
		}
		writeFuncConfig(b, fmtOpts, o.Attrs.Config)
		writeFuncBody(b, fmtOpts, o.Attrs)
		writeFuncBlockWithDepends(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels, o.DependsOnExtensions)

	case *ir.Aggregate:
		// Previously had no case at all in this switch — an AGGREGATE
		// silently vanished from dump output entirely (not even a wrong
		// stub, unlike VIEW's earlier bug). o.Options holds the same
		// SFUNC/STYPE/INITCOND/... list as o.Body, kept structured so this
		// can render the DPG "AGGREGATE name (args) (options)" declaration
		// syntax directly instead of replaying o.Body's full "CREATE
		// AGGREGATE ..." SQL text (which is a different, invalid shape for
		// DPG source — DPG's own compiler adds the CREATE verb back).
		b.WriteString("\n")
		b.WriteString(kw("AGGREGATE"))
		b.WriteString(" ")
		b.WriteString(quoteIdentIfNeeded(o.Name))
		b.WriteString("(")
		writeFuncArgs(b, o.Args)
		b.WriteString(") (")
		for i, p := range o.Options {
			if i > 0 {
				b.WriteString(", ")
			}
			if p.Value == "" {
				// RFC audit item #29: FINALFUNC_EXTRA/MFINALFUNC_EXTRA/
				// HYPOTHETICAL are bare presence flags with no "= value"
				// part — see buildAggregateOptions' identical doc comment.
				b.WriteString(kw(strings.ToUpper(p.Key)))
			} else {
				fmt.Fprintf(b, "%s = %s", kw(strings.ToUpper(p.Key)), p.Value)
			}
		}
		b.WriteString(")")
		writeFuncBlockWithOwnerAndLabels(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels)

	case *ir.Procedure:
		b.WriteString("\n")
		b.WriteString(kw("PROCEDURE"))
		b.WriteString(" ")
		b.WriteString(quoteIdentIfNeeded(o.Name))
		b.WriteString("(")
		writeFuncArgs(b, o.Args)
		b.WriteString(") ")
		b.WriteString(kw("LANGUAGE"))
		b.WriteString(" ")
		b.WriteString(o.Attrs.Language)
		writeFuncTransforms(b, fmtOpts, o.Attrs.Transforms)
		if o.Attrs.SecurityDef {
			fmt.Fprintf(b, " %s %s", kw("SECURITY"), kw("DEFINER"))
		}
		writeFuncConfig(b, fmtOpts, o.Attrs.Config)
		writeFuncBody(b, fmtOpts, o.Attrs)
		writeFuncBlockWithDepends(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels, o.DependsOnExtensions)

	case *ir.Type:
		switch o.Variant {
		case "ENUM":
			// RFC Section 5.1: TYPE <name> AS ENUM ('a', 'b', …)
			fmt.Fprintf(b, "\n%s %s %s %s (", kw("TYPE"), quoteIdentIfNeeded(o.Name), kw("AS"), kw("ENUM"))
			for i, v := range o.EnumValues {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(sqlStringLit(v))
			}
			b.WriteString(")")
			writeTypeOwnerCommentBlock(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels)
		case "DOMAIN":
			// Renders via the structured Domain* fields (RFC Section 5.4's block
			// syntax), not o.Body — previously the entire domain (base type,
			// DEFAULT, NOT NULL, every CHECK) was crammed into one opaque
			// Body string rendered inline after AS, which not only never
			// matched the RFC's own worked example but meant dump's output
			// couldn't be recompiled into anything diffType could later
			// diff property-by-property (an inline blob round-trips back
			// into Body, not into DomainDefault/DomainConstraints/etc.).
			fmt.Fprintf(b, "\n%s %s %s %s", kw("DOMAIN"), quoteIdentIfNeeded(o.Name), kw("AS"), o.DomainBaseType.String())
			if o.DomainCollation != "" {
				fmt.Fprintf(b, " %s %s", kw("COLLATE"), quoteQualIdentIfNeeded(o.DomainCollation))
			}
			if o.DomainDefault != nil || o.DomainNotNull || len(o.DomainConstraints) > 0 || o.Comment != nil || o.Owner != nil ||
				len(o.Grants) > 0 || len(o.Revocations) > 0 || len(o.SecurityLabels) > 0 {
				b.WriteString(" {\n")
				if o.Owner != nil {
					fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
				}
				if o.DomainDefault != nil {
					fmt.Fprintf(b, "%s%s %s;\n", ind, kw("DEFAULT"), *o.DomainDefault)
				}
				if o.DomainNotNull {
					fmt.Fprintf(b, "%s%s %s;\n", ind, kw("NOT"), kw("NULL"))
				}
				for _, cst := range o.DomainConstraints {
					fmt.Fprintf(b, "%s%s %s %s;\n", ind, kw("CONSTRAINT"), quoteIdentIfNeeded(cst.Name), cst.Expr)
				}
				if o.Comment != nil {
					fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
				}
				for _, g := range o.Grants {
					priv := "ALL"
					if len(g.Privileges) > 0 {
						priv = strings.Join(g.Privileges, ", ")
					}
					fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
					if g.WithGrant {
						fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
					}
					b.WriteString(";\n")
				}
				for _, r := range o.Revocations {
					priv := "ALL"
					if len(r.Privileges) > 0 {
						priv = strings.Join(r.Privileges, ", ")
					}
					fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
					if r.Cascade {
						fmt.Fprintf(b, " %s", kw("CASCADE"))
					}
					b.WriteString(";\n")
				}
				writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
				b.WriteString("}\n")
			} else {
				b.WriteString(";\n")
			}
		case "COMPOSITE":
			// Found live-testing a demo project: introspection already fully
			// captured CompositeAttrs (name+type per field, via
			// introspectCompositeAttrs) — this case was simply never
			// written, so every composite type fell through to the
			// "omitted" default below regardless, silently dropping a
			// fully-introspected object from dump's output.
			fmt.Fprintf(b, "\n%s %s %s (", kw("TYPE"), quoteIdentIfNeeded(o.Name), kw("AS"))
			for i, attr := range o.CompositeAttrs {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%s %s", quoteIdentIfNeeded(attr.Name), attr.Type.String())
				if attr.Collation != "" {
					fmt.Fprintf(b, " %s %s", kw("COLLATE"), quoteQualIdentIfNeeded(attr.Collation))
				}
			}
			b.WriteString(")")
			writeTypeBlock(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels, o.CompositeAttrs)
		case "RANGE":
			// o.Body is introspectRangeBodies' reconstructed options text
			// (e.g. "SUBTYPE = numeric") — same trailing-clause-only shape
			// DOMAIN's Body already uses, no "CREATE TYPE ... AS RANGE"
			// prefix baked in.
			fmt.Fprintf(b, "\n%s %s %s %s (%s)", kw("TYPE"), quoteIdentIfNeeded(o.Name), kw("AS"), kw("RANGE"), o.Body)
			writeTypeOwnerCommentBlock(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels)
		case "BASE":
			// No live introspection reconstructs a base type's body today
			// (would need to recover the original CREATE TYPE(...) options
			// list from pg_type's separate typinput/typoutput/typlen/...
			// columns — deliberately out of scope, same class as C-function
			// TS parser/template bodies). This case exists so a future
			// introspection fix doesn't ALSO need to remember dump — same
			// options-only shape as RANGE, consistent with o.Body's doc
			// comment ("raw Part1 for range/domain/base").
			fmt.Fprintf(b, "\n%s %s (%s)", kw("TYPE"), quoteIdentIfNeeded(o.Name), o.Body)
			writeTypeOwnerCommentBlock(b, ind, fmtOpts, o.Owner, o.Comment, o.Grants, o.Revocations, o.SecurityLabels)
		default:
			fmt.Fprintf(b, "\n-- type %s (%s) omitted\n", o.Name, o.Variant)
		}

	case *ir.VirtualType:
		// No case existed here at all — a declared VIRTUAL TYPE silently
		// vanished from dump output entirely. Doubly consequential for this
		// kind specifically: virtual types are DPG-native (RFC Section 5.6, no
		// backing CREATE TYPE), so live-catalog introspection can never
		// rediscover one on its own — runDump's caller merges any
		// already-declared virtual types back in before this ever runs (see
		// runDump's mergeExistingVirtualTypes call), but without this render
		// case that merge would have been silently pointless.
		fmt.Fprintf(b, "\n%s %s %s %s", kw("VIRTUAL"), kw("TYPE"), quoteIdentIfNeeded(o.Name), kw("AS"))
		fmt.Fprintf(b, " %s", renderVtypeBody(o.Body))
		// RFC Section 4.5: a ';' never appears immediately before a
		// '{ }' block.
		if o.Comment != nil || o.JsonFormat != "" {
			b.WriteString(" {\n")
			if o.Comment != nil {
				fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
			}
			if o.JsonFormat != "" {
				fmt.Fprintf(b, "%s%s %s %s %s;\n", ind, kw("PREFERRED"), kw("JSON"), kw("FORMAT"), o.JsonFormat)
			}
			b.WriteString("}\n")
		} else {
			b.WriteString(";\n")
		}

	case *ir.Sequence:
		b.WriteString("\n")
		if o.Unlogged {
			b.WriteString(kw("UNLOGGED"))
			b.WriteString(" ")
		}
		b.WriteString(kw("SEQUENCE"))
		b.WriteString(" ")
		b.WriteString(quoteIdentIfNeeded(o.Name))
		if o.AsType != nil {
			fmt.Fprintf(b, " %s %s", kw("AS"), o.AsType.String())
		}
		if o.IncrementBy != nil {
			fmt.Fprintf(b, " %s %d", kw("INCREMENT BY"), *o.IncrementBy)
		}
		if o.MinValue != nil {
			fmt.Fprintf(b, " %s %d", kw("MINVALUE"), *o.MinValue)
		}
		if o.MaxValue != nil {
			fmt.Fprintf(b, " %s %d", kw("MAXVALUE"), *o.MaxValue)
		}
		if o.StartValue != nil {
			fmt.Fprintf(b, " %s %d", kw("START WITH"), *o.StartValue)
		}
		if o.Cache != nil {
			fmt.Fprintf(b, " %s %d", kw("CACHE"), *o.Cache)
		}
		if o.Cycle != nil && *o.Cycle {
			b.WriteString(" ")
			b.WriteString(kw("CYCLE"))
		}
		if o.OwnedBy != nil {
			if *o.OwnedBy == "NONE" {
				fmt.Fprintf(b, " %s %s", kw("OWNED BY"), kw("NONE"))
			} else {
				fmt.Fprintf(b, " %s %s", kw("OWNED BY"), quoteDottedIdentIfNeeded(*o.OwnedBy))
			}
		}
		if o.Owner != nil || o.Comment != nil || len(o.Grants) > 0 || len(o.Revocations) > 0 || len(o.SecurityLabels) > 0 {
			b.WriteString(" {\n")
			if o.Owner != nil {
				fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
			}
			if o.Comment != nil {
				fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
			}
			for _, g := range o.Grants {
				priv := "ALL"
				if len(g.Privileges) > 0 {
					priv = strings.Join(g.Privileges, ", ")
				}
				fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
				if g.WithGrant {
					fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
				}
				b.WriteString(";\n")
			}
			for _, r := range o.Revocations {
				priv := "ALL"
				if len(r.Privileges) > 0 {
					priv = strings.Join(r.Privileges, ", ")
				}
				fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
				if r.Cascade {
					fmt.Fprintf(b, " %s", kw("CASCADE"))
				}
				b.WriteString(";\n")
			}
			writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
			b.WriteString("}\n")
		} else {
			b.WriteString(";\n")
		}

	case *ir.Role:
		// Every attribute except PASSWORD (never introspected — RFC Section 11.1,
		// confirmed live that pg_authid/pg_roles.rolpassword are
		// superuser-only, no reliable non-superuser proxy exists even for
		// "has a password", so there is nothing to render or manage from a
		// live dump). Rendering the rest is required, not optional: a bare
		// "ROLE name;" would silently drop LOGIN/SUPERUSER/membership/etc.
		// from dumped source, which — thanks to "undeclared means
		// unmanaged" (RFC Section 11.1) — wouldn't cause spurious plan --live
		// drift, but would make dump fail its actual purpose of capturing
		// a live role's real configuration.
		name := quoteIdentIfNeeded(o.Name)
		var opts strings.Builder
		writeRoleBool := func(v *bool, on, off string) {
			if v == nil {
				return
			}
			if *v {
				fmt.Fprintf(&opts, " %s", kw(on))
			} else {
				fmt.Fprintf(&opts, " %s", kw(off))
			}
		}
		writeRoleBool(o.CanLogin, "LOGIN", "NOLOGIN")
		writeRoleBool(o.Superuser, "SUPERUSER", "NOSUPERUSER")
		writeRoleBool(o.CreateDB, "CREATEDB", "NOCREATEDB")
		writeRoleBool(o.CreateRole, "CREATEROLE", "NOCREATEROLE")
		writeRoleBool(o.Inherit, "INHERIT", "NOINHERIT")
		writeRoleBool(o.IsReplication, "REPLICATION", "NOREPLICATION")
		writeRoleBool(o.BypassRLS, "BYPASSRLS", "NOBYPASSRLS")
		if o.ConnectionLimit != nil {
			fmt.Fprintf(&opts, " %s %s %d", kw("CONNECTION"), kw("LIMIT"), *o.ConnectionLimit)
		}
		if o.ValidUntil != nil {
			fmt.Fprintf(&opts, " %s %s %s", kw("VALID"), kw("UNTIL"), sqlStringLit(*o.ValidUntil))
		}
		// RFC audit item #32: entries with no (or no longer meaningful)
		// WITH INHERIT/SET value render inline via the plain IN ROLE/ROLE/
		// ADMIN lists — real CREATE ROLE accepts no WITH modifiers there at
		// all (confirmed live). Anything else needs its own block directive
		// below. introspectRoleMemberships deliberately records the real
		// live Inherit/Set value unconditionally (never suppressed — a
		// first version that suppressed default-matching values there broke
		// diffRoleMembership's live-comparison outright, see its own doc
		// comment), so the compaction happens here instead, purely for
		// rendering, on a copy — it never touches o.Memberships itself and
		// has no effect on diffing. SET's default is a fixed constant
		// (true, confirmed live) — safe to drop in both directions. INHERIT
		// has a safely-known default for an IN_ROLE-direction entry
		// (o.Inherit — this exact role's own attribute, always non-nil once
		// introspected) directly; a ROLE-direction entry points at a
		// DIFFERENT role (m.Role), whose own INHERIT default is only
		// knowable via roleInheritDefaults (built from every introspected
		// role in the current dump run) — absent there (an unmanaged
		// system role, or no cross-role context at all, e.g. a bare
		// renderObjectDPG call), it's left rendered explicitly rather than
		// guessed.
		var inRole, roleMembers, adminRoles []string
		var membershipDirs []ir.RoleMembership
		for _, m := range o.Memberships {
			if m.Set != nil && *m.Set {
				m.Set = nil
			}
			if m.Direction == "IN_ROLE" && m.Inherit != nil && o.Inherit != nil && *m.Inherit == *o.Inherit {
				m.Inherit = nil
			}
			if m.Direction == "ROLE" && m.Inherit != nil {
				if def, ok := roleInheritDefaults[m.Role]; ok && def == *m.Inherit {
					m.Inherit = nil
				}
			}
			if m.Inherit != nil || m.Set != nil {
				membershipDirs = append(membershipDirs, m)
				continue
			}
			switch {
			case m.Direction == "IN_ROLE":
				inRole = append(inRole, m.Role)
			case m.Admin:
				adminRoles = append(adminRoles, m.Role)
			default:
				roleMembers = append(roleMembers, m.Role)
			}
		}
		if len(inRole) > 0 {
			fmt.Fprintf(&opts, " %s %s %s", kw("IN"), kw("ROLE"), joinIdentsIfNeeded(inRole))
		}
		if len(roleMembers) > 0 {
			fmt.Fprintf(&opts, " %s %s", kw("ROLE"), joinIdentsIfNeeded(roleMembers))
		}
		if len(adminRoles) > 0 {
			fmt.Fprintf(&opts, " %s %s", kw("ADMIN"), joinIdentsIfNeeded(adminRoles))
		}
		if o.Comment != nil || len(o.SecurityLabels) > 0 || len(o.Configs) > 0 || len(membershipDirs) > 0 {
			// { joins the same line as Part 1's own end (RFC Section 4.5's
			// canonical brace placement) — this previously put it on its
			// own line, the only render case in this file that did.
			fmt.Fprintf(b, "\n%s %s%s {\n", kw("ROLE"), name, opts.String())
			if o.Comment != nil {
				fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
			}
			// RFC audit item #74: rendered from live state via
			// introspectRoleConfigs, which always returns a bare, resolved
			// value — quoting it here is always valid SQL regardless of
			// how the original declaration (if any) was written. A live-
			// introspected entry is never rendered as FROM CURRENT either:
			// see introspectRoleConfigs' doc comment — PostgreSQL's own
			// catalog can't distinguish it from an ordinary resolved value.
			for _, c := range o.Configs {
				fmt.Fprintf(b, "%s", ind)
				if c.Reset {
					b.WriteString(kw("RESET"))
					b.WriteString(" ")
					if c.ResetAll {
						b.WriteString(kw("ALL"))
					} else {
						b.WriteString(c.Param)
					}
				} else {
					b.WriteString(kw("SET"))
					b.WriteString(" ")
					b.WriteString(c.Param)
					b.WriteString(" = ")
					if c.Value != nil {
						b.WriteString(sqlStringLit(*c.Value))
					} else {
						b.WriteString("''")
					}
				}
				if c.InDatabase != nil {
					fmt.Fprintf(b, " %s %s %s", kw("IN"), kw("DATABASE"), quoteIdentIfNeeded(*c.InDatabase))
				}
				b.WriteString(";\n")
			}
			// RFC audit item #32: entries whose live INHERIT/SET differs
			// from the real PostgreSQL default (introspectRoleMemberships
			// only ever sets these non-nil in that case) render as their
			// own block directive — the only shape that can carry WITH
			// ADMIN/INHERIT/SET at all.
			for _, m := range membershipDirs {
				if m.Direction == "IN_ROLE" {
					fmt.Fprintf(b, "%s%s %s ", ind, kw("IN"), kw("ROLE"))
				} else {
					fmt.Fprintf(b, "%s%s ", ind, kw("ROLE"))
				}
				b.WriteString(quoteIdentIfNeeded(m.Role))
				if m.Admin {
					fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("ADMIN"), kw("OPTION"))
				}
				if m.Inherit != nil {
					fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("INHERIT"), kw(boolWord(*m.Inherit)))
				}
				if m.Set != nil {
					fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("SET"), kw(boolWord(*m.Set)))
				}
				b.WriteString(";\n")
			}
			writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(b, "\n%s %s%s;\n", kw("ROLE"), name, opts.String())
		}

	case *ir.Schema:
		// Owner/comment are rendered when present so plan --live doesn't
		// perpetually miss owner drift or emit a spurious COMMENT ... IS NULL.
		name := quoteIdentIfNeeded(o.Name)
		fmt.Fprintf(b, "\n%s %s {\n", kw("SCHEMA"), name)
		if o.Owner != nil {
			fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
		}
		if o.Comment != nil {
			fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
		}
		for _, g := range o.Grants {
			priv := "ALL"
			if len(g.Privileges) > 0 {
				priv = strings.Join(g.Privileges, ", ")
			}
			fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
			if g.WithGrant {
				fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
			}
			b.WriteString(";\n")
		}
		for _, r := range o.Revocations {
			priv := "ALL"
			if len(r.Privileges) > 0 {
				priv = strings.Join(r.Privileges, ", ")
			}
			fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
			if r.Cascade {
				fmt.Fprintf(b, " %s", kw("CASCADE"))
			}
			b.WriteString(";\n")
		}
		writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
		b.WriteString("}\n")

	case *ir.Extension:
		// Bare form (no VERSION) so plan --live never pins/updates a version:
		// diffExtension only acts when the desired side declares a version.
		fmt.Fprintf(b, "\n%s %s", kw("EXTENSION"), quoteIdentIfNeeded(o.Name))
		writeFuncBlock(b, ind, fmtOpts, o.Comment, nil, nil)

	// Reliable-tier opaque objects carry a canonicalised CREATE statement in
	// Body; renderOpaqueBody strips the CREATE verb to satisfy the no-verb
	// mandate, then terminates with a bare ";" or a "{ COMMENT ...; }" block
	// via writeFuncBlock, exactly like FUNCTION/PROCEDURE. UserMapping and
	// Publication have no Comment field (COMMENT ON doesn't apply to either
	// in PostgreSQL), so they pass nil.
	case *ir.Collation:
		renderCollationBody(b, ind, fmtOpts, o)
	case *ir.StatisticsObject:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, o.Comment)
	case *ir.DefaultPrivileges:
		renderDefaultPrivileges(b, ind, fmtOpts, o)
	case *ir.ParameterPrivileges:
		renderParameterPrivileges(b, ind, fmtOpts, o)
	case *ir.Cast:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, o.Comment)
	case *ir.EventTrigger:
		renderEventTriggerBody(b, ind, fmtOpts, o)
	case *ir.ForeignDataWrapper:
		renderForeignDataWrapperBody(b, ind, fmtOpts, o)
	case *ir.ForeignServer:
		renderForeignServerBody(b, ind, fmtOpts, o)
	case *ir.UserMapping:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, nil)
	case *ir.Publication:
		renderPublicationBody(b, ind, fmtOpts, o)
	case *ir.Subscription:
		renderSubscriptionBody(b, ind, fmtOpts, o)
	case *ir.Tablespace:
		renderOpaqueBodyWithGrantsAndLabels(b, ind, fmtOpts, o.Body, o.Comment, o.Grants, o.Revocations, o.SecurityLabels)
	case *ir.Operator:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, o.Comment)
	case *ir.OperatorClass:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, o.Comment)
	case *ir.OperatorFamily:
		renderOpFamilyBody(b, ind, fmtOpts, o)
	case *ir.TSConfig:
		renderTSConfigBody(b, ind, fmtOpts, o)
	case *ir.TSDict:
		renderTSDictBody(b, ind, fmtOpts, o)
	case *ir.TSParser:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, o.Comment)
	case *ir.TSTemplate:
		renderOpaqueBody(b, ind, fmtOpts, o.Body, o.Comment)
	}
}

// writeFuncArgs renders a FUNCTION/PROCEDURE argument list's interior
// ("mode name type [DEFAULT expr]", comma-joined) — shared between the two,
// since their parameter syntax is identical.
func writeFuncArgs(b *strings.Builder, args []ir.FuncArg) {
	first := true
	for _, a := range args {
		// RETURNS TABLE(...) columns are rendered in a separate clause by the
		// caller, never inline here — "TABLE a integer" is not valid parameter
		// syntax (PostgreSQL rejects it outright).
		if a.Mode == "TABLE" {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		if a.Mode != "" && a.Mode != "IN" {
			b.WriteString(a.Mode)
			b.WriteString(" ")
		}
		if a.Name != "" {
			b.WriteString(a.Name)
			b.WriteString(" ")
		}
		b.WriteString(a.Type.String())
		if a.Default != nil {
			b.WriteString(" DEFAULT ")
			b.WriteString(*a.Default)
		}
	}
}

// writeFuncTransforms renders RFC audit item #26's `TRANSFORM FOR TYPE t [,
// FOR TYPE t ...]` clause, shared between FUNCTION/PROCEDURE — no-op when
// transforms is empty.
func writeFuncTransforms(b *strings.Builder, fmtOpts format.Options, transforms []ir.TypeRef) {
	if len(transforms) == 0 {
		return
	}
	kw := fmtOpts.Keyword
	fmt.Fprintf(b, " %s ", kw("TRANSFORM"))
	for i, t := range transforms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s %s %s", kw("FOR"), kw("TYPE"), t.String())
	}
}

// writeFuncConfig renders Section 9.2's `SET param = value` / `SET param
// FROM CURRENT` clause list, shared between FUNCTION and PROCEDURE —
// mirrors ir.writeFuncConfig's identical rendering (kept separate for the
// same fmtOpts-keyword-casing reason as writeFuncBody above).
func writeFuncConfig(b *strings.Builder, fmtOpts format.Options, config []ir.FuncConfig) {
	kw := fmtOpts.Keyword
	for _, c := range config {
		fmt.Fprintf(b, " %s %s", kw("SET"), c.Param)
		if c.FromCurrent {
			fmt.Fprintf(b, " %s %s", kw("FROM"), kw("CURRENT"))
		} else if c.Value != nil {
			fmt.Fprintf(b, " = %s", *c.Value)
		}
	}
}

// writeFuncBody renders a FUNCTION/PROCEDURE's trailing body clause, shared
// between the two: RFC audit item #27's `AS 'obj_file'[, 'link_symbol']`
// form when ObjFile is set, item #28's `BEGIN ATOMIC ... END` form when
// AtomicBody is set, or the ordinary dollar-quoted `AS $$...$$` body
// otherwise — mirrors ir.RenderCreateFunctionSQL's identical three-way
// choice (kept as a separate implementation since this one needs fmtOpts'
// keyword casing, that one doesn't).
func writeFuncBody(b *strings.Builder, fmtOpts format.Options, attrs ir.FuncAttrs) {
	kw := fmtOpts.Keyword
	if attrs.ObjFile != nil {
		fmt.Fprintf(b, " %s %s", kw("AS"), sqlStringLit(*attrs.ObjFile))
		if attrs.LinkSymbol != nil {
			fmt.Fprintf(b, ", %s", sqlStringLit(*attrs.LinkSymbol))
		}
		return
	}
	if attrs.AtomicBody {
		fmt.Fprintf(b, " %s %s %s %s", kw("BEGIN"), kw("ATOMIC"), attrs.Body, kw("END"))
		return
	}
	fmt.Fprintf(b, " %s $$%s$$", kw("AS"), attrs.Body)
}

// writeViewBlock writes a VIEW/MATERIALIZED VIEW/RECURSIVE VIEW's { } block
// (Owner/Comment/Grants/Revocations/Indices) — see renderObjectDPG's View
// case. Indices is only ever non-empty for a materialized view (see
// ir.View.Indexes).
func writeViewBlock(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.View) {
	kw := fmtOpts.Keyword
	if o.Owner == nil && o.Comment == nil && len(o.Grants) == 0 && len(o.Revocations) == 0 &&
		len(o.Indexes) == 0 && len(o.SecurityLabels) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	for _, g := range o.Grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range o.Revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	if len(o.Indexes) > 0 {
		fmt.Fprintf(b, "%s%s {\n", ind, kw("INDEXES"))
		for _, idx := range o.Indexes {
			renderIndex(b, idx, fmtOpts)
		}
		fmt.Fprintf(b, "%s}\n", ind)
	}
	writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
	b.WriteString("}\n")
}

// writeTypeOwnerCommentBlock terminates an ENUM/COMPOSITE/RANGE/BASE type
// declaration: a bare ";" when there's nothing to declare, or a "{ }" block
// carrying OWNER/COMMENT/GRANT/REVOKE when there is. DOMAIN has its own
// richer block (DEFAULT/NOT NULL/CONSTRAINT alongside OWNER/COMMENT) and
// doesn't use this. Grants/Revocations rendering is RFC audit item #3.
func writeTypeOwnerCommentBlock(b *strings.Builder, ind string, fmtOpts format.Options, owner, comment *string, grants []ir.Grant, revocations []ir.Revocation, securityLabels []pipeline.SecurityLabel) {
	writeTypeBlock(b, ind, fmtOpts, owner, comment, grants, revocations, securityLabels, nil)
}

// writeTypeBlock is writeTypeOwnerCommentBlock plus a composite type's
// per-attribute ATTRIBUTE name { COMMENT '...'; } entries (RFC Section 5.2).
func writeTypeBlock(b *strings.Builder, ind string, fmtOpts format.Options, owner, comment *string, grants []ir.Grant, revocations []ir.Revocation, securityLabels []pipeline.SecurityLabel, attrs []*ir.Column) {
	kw := fmtOpts.Keyword
	hasAttrComment := false
	for _, a := range attrs {
		if a.Comment != nil {
			hasAttrComment = true
			break
		}
	}
	if owner == nil && comment == nil && len(grants) == 0 && len(revocations) == 0 && len(securityLabels) == 0 && !hasAttrComment {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*owner))
	}
	if comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*comment))
	}
	for _, g := range grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	writeSecurityLabels(b, ind, fmtOpts, securityLabels)
	for _, a := range attrs {
		if a.Comment != nil {
			fmt.Fprintf(b, "%s%s %s { %s %s; }\n", ind, kw("ATTRIBUTE"), quoteIdentIfNeeded(a.Name), kw("COMMENT"), sqlStringLit(*a.Comment))
		}
	}
	b.WriteString("}\n")
}

// writeFuncBlock terminates a FUNCTION/PROCEDURE declaration: a bare ";" when
// there's no COMMENT/GRANT to declare, or a "{ }" block carrying them when
// there is. Comment/Grants are genuinely compared by diffFunction/
// diffProcedure but were previously never rendered by dump at all (the
// object type had no case in this switch whatsoever for PROCEDURE, and
// FUNCTION rendered only a placeholder comment) — a dumped project could
// never detect drift on either, or even reconstruct a function's/procedure's
// body to begin with.
func writeFuncBlock(b *strings.Builder, ind string, fmtOpts format.Options, comment *string, grants []ir.Grant, revocations []ir.Revocation) {
	writeFuncBlockWithLabels(b, ind, fmtOpts, comment, grants, revocations, nil)
}

// writeFuncBlockWithLabels is writeFuncBlock plus SecurityLabels (RFC
// Section 14.11) — kept as a separate entry point rather than adding a parameter
// to writeFuncBlock itself so every existing zero-SecurityLabels call site
// (Extension, and any future FUNCTION-shaped kind that never gets one)
// doesn't need a trailing nil.
func writeFuncBlockWithLabels(b *strings.Builder, ind string, fmtOpts format.Options, comment *string, grants []ir.Grant, revocations []ir.Revocation, securityLabels []pipeline.SecurityLabel) {
	kw := fmtOpts.Keyword
	if comment == nil && len(grants) == 0 && len(revocations) == 0 && len(securityLabels) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*comment))
	}
	for _, g := range grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	writeSecurityLabels(b, ind, fmtOpts, securityLabels)
	b.WriteString("}\n")
}

// writeFuncBlockWithOwnerAndLabels is writeFuncBlockWithLabels plus Owner
// (RFC audit item #70) — Aggregate's own entry point, kept separate from
// writeFuncBlockWithDepends since real PostgreSQL has no
// ALTER AGGREGATE ... DEPENDS ON EXTENSION (see that function's doc
// comment), and separate from writeFuncBlockWithLabels itself so every
// other unrelated caller doesn't need a trailing nil.
func writeFuncBlockWithOwnerAndLabels(b *strings.Builder, ind string, fmtOpts format.Options, owner, comment *string, grants []ir.Grant, revocations []ir.Revocation, securityLabels []pipeline.SecurityLabel) {
	kw := fmtOpts.Keyword
	if owner == nil && comment == nil && len(grants) == 0 && len(revocations) == 0 && len(securityLabels) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*owner))
	}
	if comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*comment))
	}
	for _, g := range grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	writeSecurityLabels(b, ind, fmtOpts, securityLabels)
	b.WriteString("}\n")
}

// writeFuncBlockWithDepends is writeFuncBlockWithLabels plus Owner (RFC
// audit item #70) and DEPENDS ON EXTENSION (Section 9.1) — kept as a
// separate entry point rather than adding parameters to
// writeFuncBlockWithLabels itself, same reasoning as that function's own
// doc comment: Aggregate shares writeFuncBlockWithLabels too, but real
// PostgreSQL has no ALTER AGGREGATE ... DEPENDS ON EXTENSION at all
// (passthrough principle — DPG doesn't reject it there, but Aggregate
// should never actually render one), and every other unrelated caller would
// need trailing nils for directives that will never apply to it either.
// Aggregate's own Owner rendering goes through writeFuncBlockWithOwnerAndLabels
// instead, since it needs Owner but not DEPENDS ON EXTENSION.
func writeFuncBlockWithDepends(b *strings.Builder, ind string, fmtOpts format.Options, owner, comment *string, grants []ir.Grant, revocations []ir.Revocation, securityLabels []pipeline.SecurityLabel, dependsOnExtensions []string) {
	kw := fmtOpts.Keyword
	if owner == nil && comment == nil && len(grants) == 0 && len(revocations) == 0 && len(securityLabels) == 0 && len(dependsOnExtensions) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*owner))
	}
	if comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*comment))
	}
	for _, g := range grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	for _, ext := range dependsOnExtensions {
		fmt.Fprintf(b, "%s%s %s %s %s;\n", ind, kw("DEPENDS"), kw("ON"), kw("EXTENSION"), quoteIdentIfNeeded(ext))
	}
	writeSecurityLabels(b, ind, fmtOpts, securityLabels)
	b.WriteString("}\n")
}

// writeSecurityLabels renders each declared SECURITY LABEL entry (RFC
// Section 14.11) as its own "SECURITY LABEL [FOR provider] '...';" directive line
// — the same DPG block syntax the block parser accepts (see
// blockParser.parseSecurityLabel), reused by every kind's block-rendering
// code below, not just writeFuncBlockWithLabels' FUNCTION-shaped callers.
func writeSecurityLabels(b *strings.Builder, ind string, fmtOpts format.Options, labels []pipeline.SecurityLabel) {
	kw := fmtOpts.Keyword
	for _, l := range labels {
		fmt.Fprintf(b, "%s%s %s", ind, kw("SECURITY"), kw("LABEL"))
		if l.Provider != "" {
			fmt.Fprintf(b, " %s %s", kw("FOR"), quoteIdentIfNeeded(l.Provider))
		}
		fmt.Fprintf(b, " %s;\n", sqlStringLit(l.Label))
	}
}

// renderOpaqueBody writes a reconstructed CREATE statement (already
// canonicalised by the introspector) as a DPG declaration. DPG source obeys the
// no-verb mandate — declarations must not begin with CREATE — so the leading
// CREATE verb is stripped. Empty bodies are skipped so a body-less object never
// emits a bare, invalid ";". The declaration is terminated via writeFuncBlock,
// same as FUNCTION/PROCEDURE, so a live comment round-trips through dump
// instead of being silently dropped (found live-testing a demo project: the
// comment was present in pg_description and correctly introspected onto the
// IR object's Comment field, but dump emitted a bare ";" regardless).
// renderDefaultPrivileges writes one "DEFAULT PRIVILEGES [FOR ROLE x]
// [IN SCHEMA y] { GRANTS { ... } REVOKES { ... } }" declaration — never
// rendered by dump at all before this (no case existed for *ir.DefaultPrivileges),
// and introspection never populated the object in the first place (see
// introspectDefaultPrivileges). o.ObjectType applies to every grant/revoke
// entry here: Builder.BuildDefaultPrivileges splits a declaration naming
// multiple object types into one *ir.DefaultPrivileges per type, matching
// pg_default_acl's own one-row-per-(role,schema,objtype) model, so each
// rendered object is intentionally single-type — real PostgreSQL's own
// GRANT ... ON <type> TO ... clause (confirmed live via \h ALTER DEFAULT
// PRIVILEGES) puts the object type on the grant itself, not as a
// declaration-wide wrapper.
func renderDefaultPrivileges(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.DefaultPrivileges) {
	kw := fmtOpts.Keyword
	entryInd := ind + ind
	fmt.Fprintf(b, "\n%s %s", kw("DEFAULT"), kw("PRIVILEGES"))
	if o.ForRole != nil {
		fmt.Fprintf(b, " %s %s %s", kw("FOR"), kw("ROLE"), quoteIdentIfNeeded(*o.ForRole))
	}
	if o.InSchema != nil {
		fmt.Fprintf(b, " %s %s %s", kw("IN"), kw("SCHEMA"), quoteIdentIfNeeded(*o.InSchema))
	}
	b.WriteString(" {\n")
	if len(o.Grants) > 0 {
		fmt.Fprintf(b, "%s%s {\n", ind, kw("GRANTS"))
		for _, g := range o.Grants {
			priv := "ALL"
			if len(g.Privileges) > 0 {
				priv = strings.Join(g.Privileges, ", ")
			}
			fmt.Fprintf(b, "%s%s %s %s %s %s", entryInd, priv, kw("ON"), kw(o.ObjectType), kw("TO"), strings.Join(g.Roles, ", "))
			if g.WithGrant {
				fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
			}
			b.WriteString(";\n")
		}
		fmt.Fprintf(b, "%s}\n", ind)
	}
	if len(o.Revocations) > 0 {
		fmt.Fprintf(b, "%s%s {\n", ind, kw("REVOKES"))
		for _, r := range o.Revocations {
			priv := "ALL"
			if len(r.Privileges) > 0 {
				priv = strings.Join(r.Privileges, ", ")
			}
			fmt.Fprintf(b, "%s%s %s %s %s %s", entryInd, priv, kw("ON"), kw(o.ObjectType), kw("FROM"), strings.Join(r.Roles, ", "))
			if r.Cascade {
				fmt.Fprintf(b, " %s", kw("CASCADE"))
			}
			b.WriteString(";\n")
		}
		fmt.Fprintf(b, "%s}\n", ind)
	}
	b.WriteString("}\n")
}

// renderParameterPrivileges writes one "PARAMETER PRIVILEGES { GRANTS { ... }
// REVOKES { ... } }" declaration (RFC Section 11.6, PG15+). Unlike
// renderDefaultPrivileges there is no FOR ROLE/IN SCHEMA header and no
// per-object-type split: every grant carries its own PARAMETER identifier
// list directly, and introspectParameterPrivileges always produces exactly
// one *ir.ParameterPrivileges.
func renderParameterPrivileges(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.ParameterPrivileges) {
	kw := fmtOpts.Keyword
	entryInd := ind + ind
	fmt.Fprintf(b, "\n%s %s {\n", kw("PARAMETER"), kw("PRIVILEGES"))
	if len(o.Grants) > 0 {
		fmt.Fprintf(b, "%s%s {\n", ind, kw("GRANTS"))
		for _, g := range o.Grants {
			priv := "ALL"
			if len(g.Privileges) > 0 {
				priv = strings.Join(g.Privileges, ", ")
			}
			fmt.Fprintf(b, "%s%s %s %s %s %s %s", entryInd, priv, kw("ON"), kw("PARAMETER"),
				strings.Join(g.Parameters, ", "), kw("TO"), strings.Join(g.Roles, ", "))
			if g.WithGrant {
				fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
			}
			if g.GrantedBy != nil {
				fmt.Fprintf(b, " %s %s %s", kw("GRANTED"), kw("BY"), *g.GrantedBy)
			}
			b.WriteString(";\n")
		}
		fmt.Fprintf(b, "%s}\n", ind)
	}
	if len(o.Revocations) > 0 {
		fmt.Fprintf(b, "%s%s {\n", ind, kw("REVOKES"))
		for _, r := range o.Revocations {
			priv := "ALL"
			if len(r.Privileges) > 0 {
				priv = strings.Join(r.Privileges, ", ")
			}
			fmt.Fprintf(b, "%s%s %s %s %s %s %s", entryInd, priv, kw("ON"), kw("PARAMETER"),
				strings.Join(r.Parameters, ", "), kw("FROM"), strings.Join(r.Roles, ", "))
			if r.GrantedBy != nil {
				fmt.Fprintf(b, " %s %s %s", kw("GRANTED"), kw("BY"), *r.GrantedBy)
			}
			if r.Cascade {
				fmt.Fprintf(b, " %s", kw("CASCADE"))
			}
			b.WriteString(";\n")
		}
		fmt.Fprintf(b, "%s}\n", ind)
	}
	b.WriteString("}\n")
}

// renderForeignDataWrapperBody is like the old renderOpaqueBodyWithGrants
// (Section 14.8) — kept as its own function like renderForeignServerBody,
// rather than adding an Owner parameter to writeFuncBlock and touching
// every other call site.
func renderForeignDataWrapperBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.ForeignDataWrapper) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil && len(o.Grants) == 0 && len(o.Revocations) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	for _, g := range o.Grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range o.Revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	b.WriteString("}\n")
}

// renderForeignServerBody is renderOpaqueBodyWithGrants plus Owner (RFC
// audit item #79) — kept as its own function, like renderEventTriggerBody/
// renderTSDictBody, rather than adding an Owner parameter to writeFuncBlock
// and touching every other call site.
func renderForeignServerBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.ForeignServer) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil && len(o.Grants) == 0 && len(o.Revocations) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	for _, g := range o.Grants {
		priv := "ALL"
		if len(g.Privileges) > 0 {
			priv = strings.Join(g.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("GRANT"), priv, kw("TO"), strings.Join(g.Roles, ", "))
		if g.WithGrant {
			fmt.Fprintf(b, " %s %s %s", kw("WITH"), kw("GRANT"), kw("OPTION"))
		}
		b.WriteString(";\n")
	}
	for _, r := range o.Revocations {
		priv := "ALL"
		if len(r.Privileges) > 0 {
			priv = strings.Join(r.Privileges, ", ")
		}
		fmt.Fprintf(b, "%s%s %s %s %s", ind, kw("REVOKE"), priv, kw("FROM"), strings.Join(r.Roles, ", "))
		if r.Cascade {
			fmt.Fprintf(b, " %s", kw("CASCADE"))
		}
		b.WriteString(";\n")
	}
	b.WriteString("}\n")
}

// renderOpaqueBodyWithGrantsAndLabels is renderOpaqueBodyWithGrants plus
// SecurityLabels — Tablespace only (RFC audit item #4).
func renderOpaqueBodyWithGrantsAndLabels(b *strings.Builder, ind string, fmtOpts format.Options, body string, comment *string, grants []ir.Grant, revocations []ir.Revocation, securityLabels []pipeline.SecurityLabel) {
	body = strings.TrimSpace(body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	writeFuncBlockWithLabels(b, ind, fmtOpts, comment, grants, revocations, securityLabels)
}

func renderOpaqueBody(b *strings.Builder, ind string, fmtOpts format.Options, body string, comment *string) {
	body = strings.TrimSpace(body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	writeFuncBlock(b, ind, fmtOpts, comment, nil, nil)
}

// renderSubscriptionBody is Subscription's dedicated render function (same
// reasoning as renderPublicationBody): OWNER (Section 13.2) plus Comment/
// SecurityLabels.
func renderSubscriptionBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.Subscription) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil && len(o.SecurityLabels) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
	b.WriteString("}\n")
}

// renderPublicationBody is Publication's dedicated render function, like
// renderSubscriptionBody/renderEventTriggerBody below: OWNER can't go
// through the shared writeFuncBlockWithLabels helper without adding a
// parameter every other caller would pass nil for.
func renderPublicationBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.Publication) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil && len(o.SecurityLabels) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
	b.WriteString("}\n")
}

// renderEventTriggerBody is EventTrigger's dedicated render function
// (Section 14.1) — same reasoning as renderPublicationBody.
func renderEventTriggerBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.EventTrigger) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil && o.EnableState == "" && len(o.SecurityLabels) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	if o.EnableState != "" {
		fmt.Fprintf(b, "%s%s;\n", ind, kw(o.EnableState))
	}
	writeSecurityLabels(b, ind, fmtOpts, o.SecurityLabels)
	b.WriteString("}\n")
}

// renderTSDictBody is renderOpaqueBody's TSDict-specific variant: TSDict's
// OWNER (Section 12.2) has no counterpart in renderOpaqueBody's other
// callers (TSParser/TSTemplate, which real PostgreSQL has no OWNER concept
// for at all) — same reasoning as renderPublicationBody/
// renderEventTriggerBody.
// renderCollationBody is renderOpaqueBody's Collation-specific variant,
// adding Owner (RFC audit item #81) — a normal block-only directive, unlike
// Tablespace's inline OWNER (see ir.Tablespace.Owner's doc comment).
// RefreshVersion is deliberately never rendered here, same reasoning as
// Sequence.Restart: it's a one-shot migration directive with no persisted
// live-catalog state for introspection to reconstruct.
func renderCollationBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.Collation) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	b.WriteString("}\n")
}

func renderTSDictBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.TSDict) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	b.WriteString("}\n")
}

// renderTSConfigBody is renderOpaqueBody's TSConfig-specific variant: a text
// search configuration's { } block may also carry MAPPING FOR entries (RFC
// Section 12.1) — previously dropped entirely, since renderOpaqueBody's block only
// ever knows about Comment/Grants. Reuses renderOpaqueBody's CREATE-prefix
// stripping (DPG source never starts a declaration with CREATE) but builds
// its own block instead of delegating to writeFuncBlock.
func renderTSConfigBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.TSConfig) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Owner == nil && o.Comment == nil && len(o.Mappings) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Owner != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("OWNER"), quoteIdentIfNeeded(*o.Owner))
	}
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	for _, m := range o.Mappings {
		dicts := make([]string, len(m.Dictionaries))
		for i, d := range m.Dictionaries {
			dicts[i] = d.String()
		}
		fmt.Fprintf(b, "%s%s %s %s %s %s;\n", ind, kw("MAPPING"), kw("FOR"),
			strings.Join(m.TokenTypes, ", "), kw("WITH"), strings.Join(dicts, ", "))
	}
	b.WriteString("}\n")
}

// renderOpFamilyBody renders an OPERATOR FAMILY declaration, RFC Section 14.4:
// bare CREATE OPERATOR FAMILY (Part 1, untouched real PG SQL) plus a { }
// block for the family's loose members — the same shape renderTSConfigBody
// uses for MAPPING FOR entries. Members render via m.AddClause(), the exact
// same renderer the differ uses to build "ALTER ... ADD" statements, so
// dumped source and generated SQL can never drift apart. Early-returns to
// the bare "...;" form when there's no comment and no members, matching
// today's output byte-for-byte for the common case (a family with only
// class-owned members, e.g. the demo project's text_ci_ops) — zero output
// churn for objects this feature doesn't touch.
func renderOpFamilyBody(b *strings.Builder, ind string, fmtOpts format.Options, o *ir.OperatorFamily) {
	kw := fmtOpts.Keyword
	body := strings.TrimSpace(o.Body)
	const createPrefix = "CREATE "
	if len(body) >= len(createPrefix) && strings.EqualFold(body[:len(createPrefix)], createPrefix) {
		body = strings.TrimSpace(body[len(createPrefix):])
	}
	if body == "" {
		return
	}
	fmt.Fprintf(b, "\n%s", body)
	if o.Comment == nil && len(o.Members) == 0 {
		b.WriteString(";\n")
		return
	}
	b.WriteString(" {\n")
	if o.Comment != nil {
		fmt.Fprintf(b, "%s%s %s;\n", ind, kw("COMMENT"), sqlStringLit(*o.Comment))
	}
	for _, m := range o.Members {
		fmt.Fprintf(b, "%s%s;\n", ind, m.AddClause())
	}
	b.WriteString("}\n")
}

// boolWord renders a Go bool as the bare "TRUE"/"FALSE" keyword text RFC
// audit item #32's WITH INHERIT/SET clauses expect (real PostgreSQL grammar,
// not a quoted string) — kw() applies keyword casing on top of this.
func boolWord(v bool) string {
	if v {
		return "TRUE"
	}
	return "FALSE"
}

// sqlStringLit renders s as a single-quoted SQL string literal, doubling any
// embedded single quotes. DPG string values (COMMENT text, ENUM labels) use SQL
// single-quote literals, not Go %q double quotes.
func sqlStringLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// isSafeBareIdent reports whether s can appear as an unquoted PostgreSQL
// identifier: non-empty, starting with a lowercase letter or underscore, and
// containing only lowercase letters, digits, underscores, or dollar signs. Any
// uppercase forces quoting, since an unquoted identifier folds to lowercase.
func isSafeBareIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case i > 0 && (r >= '0' && r <= '9' || r == '$'):
		default:
			return false
		}
	}
	return true
}

// quoteIdentIfNeeded double-quotes an identifier when it is not a safe bare
// identifier or is a PostgreSQL reserved / type-or-function-name keyword, so that
// dumped DPG source recompiles. Safe lowercase non-keyword names are left bare to
// keep the output idiomatic. Mirrors PostgreSQL's quote_identifier().
func quoteIdentIfNeeded(s string) string {
	if isSafeBareIdent(s) && !dumpReservedKeywords[s] {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteQualIdentIfNeeded quotes-if-needed a possibly schema-qualified name
// ("schema.name", as stored in ir.Table.Inherits) part by part, rather than
// wrapping the whole dotted string in one identifier — which would produce
// a single malformed quoted identifier instead of a schema-qualified one.
func quoteQualIdentIfNeeded(s string) string {
	schema, name, ok := strings.Cut(s, ".")
	if !ok {
		return quoteIdentIfNeeded(s)
	}
	return quoteIdentIfNeeded(schema) + "." + quoteIdentIfNeeded(name)
}

// quoteDottedIdentIfNeeded is quoteQualIdentIfNeeded generalized to any
// number of dot-separated parts — used for Sequence.OwnedBy (RFC audit item
// #14), which can be "table.column" or "schema.table.column".
func quoteDottedIdentIfNeeded(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = quoteIdentIfNeeded(p)
	}
	return strings.Join(parts, ".")
}

// joinIdentsIfNeeded quotes-if-needed and comma-joins a list of identifiers
// (e.g. a Role's IN ROLE/ROLE/ADMIN membership lists).
func joinIdentsIfNeeded(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quoteIdentIfNeeded(n)
	}
	return strings.Join(quoted, ", ")
}

// dumpReservedKeywords is the set of PostgreSQL RESERVED and TYPE_FUNC_NAME
// keywords — the categories that cannot be a bare identifier in the positions
// dump emits (table/column/schema/role/etc. names). Unreserved and col-name
// keywords are valid bare and are omitted. Stable across PG 14–17; a missing
// future keyword degrades only to the pre-existing unquoted behaviour.
var dumpReservedKeywords = func() map[string]bool {
	const words = `all analyse analyze and any array as asc asymmetric both case cast check
collate column constraint create current_catalog current_date current_role
current_time current_timestamp current_user default deferrable desc distinct do
else end except false fetch for foreign from grant group having in initially
intersect into lateral leading limit localtime localtimestamp not null offset on
only or order placing primary references returning select session_user some
symmetric system_user table then to trailing true union unique user using
variadic when where window with
authorization binary collation concurrently cross current_schema freeze full
ilike inner is isnull join left like natural notnull outer overlaps right similar
tablesample verbose`
	m := make(map[string]bool)
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}()

// classifyColumn returns the presentation section for a column.
// Priority: generated > lifecycle > timestamps > "" (regular).
func classifyColumn(col *ir.Column) string {
	name := strings.ToLower(col.Name)
	for _, kw := range []string{"delet", "archiv", "activ", "enabl", "disabl", "publish", "expir", "suspend"} {
		if strings.Contains(name, kw) {
			return "lifecycle"
		}
	}
	if strings.HasSuffix(name, "_at") || strings.HasSuffix(name, "_on") {
		for _, p := range []string{"creat", "updat", "modif", "insert"} {
			if strings.HasPrefix(name, p) {
				return "timestamps"
			}
		}
	}
	return ""
}

// isInlineable reports whether a constraint type can be written as a column-level clause.
func isInlineable(typ string) bool {
	switch typ {
	case "PRIMARY KEY", "UNIQUE", "FOREIGN KEY", "NOT NULL":
		return true
	}
	return false
}

// inlineConstraintClause returns the bare inline column-level clause for a
// single-column constraint: "PRIMARY KEY", "UNIQUE", or "REFERENCES t(c) ...".
// Constraint names are intentionally omitted; PostgreSQL auto-generates them.
func inlineConstraintClause(cst *ir.Constraint) string {
	switch cst.Type {
	case "PRIMARY KEY":
		return "PRIMARY KEY"
	case "UNIQUE":
		return "UNIQUE"
	case "FOREIGN KEY":
		// pg_get_constraintdef: "FOREIGN KEY (col) REFERENCES tbl(col) [actions]"
		// Strip the "FOREIGN KEY (col) " prefix, leaving "REFERENCES ...".
		upper := strings.ToUpper(cst.Expr)
		if idx := strings.Index(upper, " REFERENCES "); idx >= 0 {
			return strings.TrimSpace(cst.Expr[idx+1:])
		}
		return cst.Expr
	case "NOT NULL":
		// cst.Expr is "NOT NULL colname [NO INHERIT]" — strip the column
		// name (already implied by the column this clause is being
		// inlined onto) the same way FOREIGN KEY strips its own leading
		// "FOREIGN KEY (col) " above.
		if cst.NoInherit {
			return "NOT NULL NO INHERIT"
		}
		return "NOT NULL"
	}
	return cst.Expr
}

// renderIndex writes one entry inside a table's INDEXES { } block.
// Format: [UNIQUE] [CONCURRENTLY] name [USING method] (cols) [WHERE pred];
// (two-level indent, no INDEX verb — that is the grammar parseOneIndex
// accepts). UNIQUE and CONCURRENTLY are both prefix keywords before the
// name, and USING precedes the column list — all three mirror real
// PostgreSQL's own CREATE UNIQUE INDEX CONCURRENTLY name ON table USING
// method (columns) order exactly (dropping only the implicit INDEX/ON
// table). CONCURRENTLY is a bare presence keyword (no boolean value, same
// as real PG); it's only ever true here when the source declared it
// explicitly or introspection found a live index that used it — dump never
// invents one from a project's concurrent_indexes default, since that
// default is a compile-time behavior, not a fact about the object itself.
// renderVtypeBody renders a VIRTUAL TYPE's structured body (RFC Section 5.6) back
// into DPG source syntax: a bare type reference, a "(field type, ...)"
// composite, or a "term | term | ..." union of either. Mirrors
// ir.VtypeTypeRef.String() for the leaf case, extended to composite/union.
func renderVtypeBody(body ir.VtypeBody) string {
	switch v := body.(type) {
	case ir.VtypeTypeRef:
		return v.String()
	case ir.VtypeComposite:
		parts := make([]string, len(v.Fields))
		for i, f := range v.Fields {
			parts[i] = quoteIdentIfNeeded(f.Name) + " " + f.Type.String()
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case ir.VtypeUnion:
		parts := make([]string, len(v.Members))
		for i, m := range v.Members {
			parts[i] = renderVtypeBody(m)
		}
		return strings.Join(parts, " | ")
	}
	return ""
}

// renderPartitionEntry writes one "[FOREIGN] name bounds [SERVER ident
// [OPTIONS (...)]] [PARTITION BY strategy (cols) { PARTITIONS {...} }];"
// entry at the given indent, recursing into p's own sub-partitions (RFC
// Section 7.13) when present. A FOREIGN partition never has sub-partitions
// (real PostgreSQL rejects PARTITION BY on a foreign table), so the SERVER/
// OPTIONS clause and the PARTITION BY suffix never both render for the same
// entry in practice.
func renderPartitionEntry(b *strings.Builder, p *ir.Partition, ind string, fmtOpts format.Options) {
	kw := fmtOpts.Keyword
	baseInd := fmtOpts.Indent()
	if p.Foreign {
		fmt.Fprintf(b, "%s%s ", ind, kw("FOREIGN"))
		fmt.Fprintf(b, "%s %s", quoteIdentIfNeeded(p.Name), p.Bounds)
	} else {
		fmt.Fprintf(b, "%s%s %s", ind, quoteIdentIfNeeded(p.Name), p.Bounds)
	}
	if p.Foreign {
		if p.ForeignServer != nil {
			fmt.Fprintf(b, " %s %s", kw("SERVER"), quoteIdentIfNeeded(*p.ForeignServer))
		}
		if len(p.ForeignOptions) > 0 {
			parts := make([]string, len(p.ForeignOptions))
			for i, o := range p.ForeignOptions {
				parts[i] = o.Key + " " + sqlStringLit(o.Value)
			}
			fmt.Fprintf(b, " %s (%s)", kw("OPTIONS"), strings.Join(parts, ", "))
		}
	}
	if p.PartitionBy != nil {
		fmt.Fprintf(b, " %s %s %s (%s)", kw("PARTITION"), kw("BY"), kw(p.PartitionBy.Strategy), strings.Join(p.PartitionBy.Columns, ", "))
	}
	if p.PartitionBy != nil || len(p.Constraints) > 0 {
		b.WriteString(" {\n")
		if p.PartitionBy != nil {
			fmt.Fprintf(b, "%s%s {\n", ind+baseInd, kw("PARTITIONS"))
			for _, sub := range p.Partitions {
				renderPartitionEntry(b, sub, ind+baseInd+baseInd, fmtOpts)
			}
			fmt.Fprintf(b, "%s}\n", ind+baseInd)
		}
		// RFC Section 7.3's "DROP CONSTRAINT ... ONLY" gap (PostgreSQL 18+) —
		// a constraint declared directly on this partition, independent of
		// the parent. Mode B (singular, unwrapped): the only form used here,
		// since this body already coexists with PARTITIONS above rather than
		// needing its own CONSTRAINTS { } wrapper.
		for _, cst := range p.Constraints {
			fmt.Fprintf(b, "%s%s %s %s", ind+baseInd, kw("CONSTRAINT"), quoteIdentIfNeeded(cst.Name), cst.Expr)
			if cst.NotValid {
				fmt.Fprintf(b, " %s %s", kw("NOT"), kw("VALID"))
			}
			if cst.NotEnforced {
				fmt.Fprintf(b, " %s %s", kw("NOT"), kw("ENFORCED"))
			}
			b.WriteString(";\n")
		}
		fmt.Fprintf(b, "%s}", ind)
	}
	b.WriteString(";\n")
}

// renderPolicy writes one Mode A POLICIES { } entry — previously not
// rendered by dump at all, despite being genuinely diffed (diffPolicies)
// and correctly introspected (introspectPolicies): a live table's RLS
// policy silently vanished from a dumped project, found live-testing a
// demo project (orders.owner_only). Clause order mirrors real PostgreSQL's
// own CREATE POLICY: [AS PERMISSIVE|RESTRICTIVE] FOR command [TO roles]
// [USING (...)] [WITH CHECK (...)] — not the RFC's ABNF listing (FOR before
// AS), which contradicts its own worked example and real PG's grammar.
// writeEntryCommentBlock terminates one INDEXES/POLICIES/TRIGGERS/
// CONSTRAINTS entry, written at ind+ind (nested one level inside the
// owning table's { } block already): a bare ";" when there's no comment,
// or a "{ COMMENT '...'; }" block when there is — mirrors
// parseTrailingCommentBlock's grammar exactly (see blockparser).
func writeEntryCommentBlock(b *strings.Builder, ind string, fmtOpts format.Options, comment *string) {
	kw := fmtOpts.Keyword
	if comment == nil {
		b.WriteString(";\n")
		return
	}
	fmt.Fprintf(b, " {\n%s%s%s %s;\n%s%s}\n",
		ind, ind, ind, kw("COMMENT")+" "+sqlStringLit(*comment), ind, ind)
}

func renderPolicy(b *strings.Builder, pol *ir.Policy, fmtOpts format.Options) {
	ind := fmtOpts.Indent()
	kw := fmtOpts.Keyword
	b.WriteString(ind + ind)
	b.WriteString(quoteIdentIfNeeded(pol.Name))
	if !pol.Permissive {
		fmt.Fprintf(b, " %s %s", kw("AS"), kw("RESTRICTIVE"))
	}
	fmt.Fprintf(b, " %s %s", kw("FOR"), kw(pol.Command))
	if len(pol.Roles) > 0 {
		fmt.Fprintf(b, " %s %s", kw("TO"), strings.Join(pol.Roles, ", "))
	}
	if pol.Using != nil {
		fmt.Fprintf(b, " %s (%s)", kw("USING"), *pol.Using)
	}
	if pol.WithCheck != nil {
		fmt.Fprintf(b, " %s %s (%s)", kw("WITH"), kw("CHECK"), *pol.WithCheck)
	}
	if pol.RenamedFrom != nil {
		fmt.Fprintf(b, " %s %s %s", kw("RENAMED"), kw("FROM"), quoteIdentIfNeeded(*pol.RenamedFrom))
	}
	writeEntryCommentBlock(b, ind, fmtOpts, pol.Comment)
}

// renderTrigger writes one Mode A TRIGGERS { } entry — previously not
// rendered by dump at all, despite being genuinely diffed (diffTriggers)
// and correctly introspected (introspectTriggers): a live table's trigger
// silently vanished from a dumped project, found live-testing a demo
// project (orders.set_updated_at). Clause order mirrors real PostgreSQL's
// own CREATE TRIGGER: timing event_list FOR EACH ROW|STATEMENT [WHEN (...)]
// EXECUTE FUNCTION func(args).
func renderTrigger(b *strings.Builder, trg *ir.Trigger, fmtOpts format.Options) {
	ind := fmtOpts.Indent()
	kw := fmtOpts.Keyword
	b.WriteString(ind + ind)
	b.WriteString(quoteIdentIfNeeded(trg.Name))
	fmt.Fprintf(b, " %s", kw(trg.When))
	events := make([]string, len(trg.Events))
	for i, e := range trg.Events {
		events[i] = kw(e)
		// RFC audit item #1: attach "OF col1, col2" to the UPDATE event
		// specifically, matching real PostgreSQL's own CREATE TRIGGER
		// clause placement (see triggerEventClauses' identical reasoning
		// in internal/diff).
		if e == "UPDATE" && len(trg.UpdateOfColumns) > 0 {
			cols := make([]string, len(trg.UpdateOfColumns))
			for j, c := range trg.UpdateOfColumns {
				cols[j] = quoteIdentIfNeeded(c)
			}
			events[i] = kw(e) + " " + kw("OF") + " " + strings.Join(cols, ", ")
		}
	}
	fmt.Fprintf(b, " %s", strings.Join(events, " "+kw("OR")+" "))
	if trg.OldTransitionName != nil || trg.NewTransitionName != nil {
		fmt.Fprintf(b, " %s", kw("REFERENCING"))
		if trg.OldTransitionName != nil {
			fmt.Fprintf(b, " %s %s %s %s", kw("OLD"), kw("TABLE"), kw("AS"), quoteIdentIfNeeded(*trg.OldTransitionName))
		}
		if trg.NewTransitionName != nil {
			fmt.Fprintf(b, " %s %s %s %s", kw("NEW"), kw("TABLE"), kw("AS"), quoteIdentIfNeeded(*trg.NewTransitionName))
		}
	}
	fmt.Fprintf(b, " %s %s %s", kw("FOR"), kw("EACH"), kw(trg.ForEach))
	if trg.Condition != nil {
		fmt.Fprintf(b, " %s (%s)", kw("WHEN"), *trg.Condition)
	}
	fmt.Fprintf(b, " %s %s %s(%s)", kw("EXECUTE"), kw("FUNCTION"), trg.Function, strings.Join(trg.Args, ", "))
	// trigger-enable-state (Section 7.9, audit item #56): "" means
	// ENABLED, PostgreSQL's own default — rendered only when non-default,
	// same convention used throughout dump for every other always-has-a-
	// concrete-value catalog field.
	if trg.EnableState != "" {
		words := strings.Fields(trg.EnableState)
		kwWords := make([]string, len(words))
		for i, w := range words {
			kwWords[i] = kw(w)
		}
		fmt.Fprintf(b, " %s", strings.Join(kwWords, " "))
	}
	// DEPENDS ON EXTENSION (Section 9.1, reused for triggers — Section
	// 7.9, audit item #75): the grammar allows at most one, unlike
	// Function/Procedure's repeatable func-block form.
	for _, ext := range trg.DependsOnExtensions {
		fmt.Fprintf(b, " %s %s %s %s", kw("DEPENDS"), kw("ON"), kw("EXTENSION"), quoteIdentIfNeeded(ext))
	}
	writeEntryCommentBlock(b, ind, fmtOpts, trg.Comment)
}

func renderIndex(b *strings.Builder, idx *ir.Index, fmtOpts format.Options) {
	ind := fmtOpts.Indent()
	kw := fmtOpts.Keyword
	b.WriteString(ind + ind)
	if idx.Unique {
		fmt.Fprintf(b, "%s ", kw("UNIQUE"))
	}
	if idx.Concurrently {
		fmt.Fprintf(b, "%s ", kw("CONCURRENTLY"))
	}
	if idx.Only {
		fmt.Fprintf(b, "%s ", kw("ONLY"))
	}
	b.WriteString(quoteIdentIfNeeded(idx.Name))
	if idx.Method != "" && idx.Method != "btree" {
		fmt.Fprintf(b, " %s %s", kw("USING"), idx.Method)
	}
	b.WriteString(" (")
	for i, col := range idx.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		if col.Expr != nil {
			// RFC Section 7.7's own grammar requires an expression column's
			// extra wrapping parens ("(" expr ")", distinct from the
			// column-list's own) to unambiguously distinguish it from a
			// column name optionally followed by an opclass (also
			// commonly written "name(params)"-shaped, e.g.
			// "doc tsvector_ops(siglen = 32)") — parseIndexColumnEntry
			// only recognizes a leading '(' as the expression marker,
			// matching real PostgreSQL's own identical requirement.
			b.WriteString("(")
			b.WriteString(col.Expr.Text)
			b.WriteString(")")
		} else {
			// Index column names are emitted bare: the blockparser stores the
			// column text verbatim and the differ's createIndex quotes it when
			// generating SQL, so quoting here would double-quote it.
			b.WriteString(quoteIdentIfNeeded(col.Name))
		}
		if col.Collation != nil {
			fmt.Fprintf(b, " %s %s", kw("COLLATE"), quoteIdentIfNeeded(col.Collation.Name))
		}
		if col.OpClass != nil {
			fmt.Fprintf(b, " %s", quoteQualIdentIfNeeded(col.OpClass.Name))
			if len(col.OpClassParams) > 0 {
				parts := make([]string, len(col.OpClassParams))
				for i, p := range col.OpClassParams {
					parts[i] = fmt.Sprintf("%s = %s", p.Key, p.Value)
				}
				fmt.Fprintf(b, "(%s)", strings.Join(parts, ", "))
			}
		}
		if col.SortOrder != "" {
			b.WriteString(" ")
			b.WriteString(col.SortOrder)
		}
		if col.Nulls != "" {
			fmt.Fprintf(b, " %s %s", kw("NULLS"), col.Nulls)
		}
	}
	b.WriteString(")")
	if len(idx.Include) > 0 {
		// Bare column names; createIndex quotes them when generating SQL.
		inc := make([]string, len(idx.Include))
		for i, c := range idx.Include {
			inc[i] = quoteIdentIfNeeded(c)
		}
		fmt.Fprintf(b, " %s (%s)", kw("INCLUDE"), strings.Join(inc, ", "))
	}
	if idx.NullsNotDistinct {
		fmt.Fprintf(b, " %s %s %s", kw("NULLS"), kw("NOT"), kw("DISTINCT"))
	}
	if len(idx.With) > 0 {
		parts := make([]string, len(idx.With))
		for i, p := range idx.With {
			parts[i] = p.Key + "=" + p.Value
		}
		fmt.Fprintf(b, " %s (%s)", kw("WITH"), strings.Join(parts, ", "))
	}
	if idx.Where != nil {
		fmt.Fprintf(b, " %s %s", kw("WHERE"), *idx.Where)
	}
	if idx.Tablespace != nil {
		fmt.Fprintf(b, " %s %s", kw("TABLESPACE"), quoteIdentIfNeeded(*idx.Tablespace))
	}
	writeEntryCommentBlock(b, ind, fmtOpts, idx.Comment)
}
