package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/format"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/resolve"
	snapshotpkg "github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/srcindex"
	"github.com/thec1oud/dpg/internal/ui"
)

type resolveFlags struct {
	cluster, database string
	adopt, revert     []string
	ignoreIDs         []string
	ack, unack        []string
	reason            string
	expires           string
	yes, dryRun       bool
	allowDestructive  bool
}

func newResolveCmd() *cobra.Command {
	var f resolveFlags
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve drift: adopt, revert, ignore or acknowledge items",
		Long: `Resolves the items dpg verify reports. Each item is chosen by its ID:

  --adopt ID          bring the database's state into the source and snapshot
  --revert ID         change the database back to the declared state
  --ignore ID         add an [ignore] pattern for an object DPG doesn't manage
  --ack ID            acknowledge the item (with --reason, --expires)
  --unack ID          remove an item's acknowledgement

Every change is previewed before it is made (--dry-run stops there; --yes
skips the question). After writing, the source is compiled and drift is
detected again; if a resolved item is still reported, every file, snapshot
and acknowledgement change is undone.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runResolve(f, os.Stdin, os.Stdout)
			if err != nil && !isExitError(err) {
				return &ui.ExitError{Code: 2, Err: err}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&f.cluster, "cluster", "", "cluster to resolve (required when multiple clusters exist)")
	cmd.Flags().StringVar(&f.database, "database", "", "database to resolve (required when multiple databases exist)")
	cmd.Flags().StringSliceVar(&f.adopt, "adopt", nil, "item IDs to adopt")
	cmd.Flags().StringSliceVar(&f.revert, "revert", nil, "item IDs to revert")
	cmd.Flags().StringSliceVar(&f.ignoreIDs, "ignore", nil, "item IDs to ignore")
	cmd.Flags().StringSliceVar(&f.ack, "ack", nil, "item IDs to acknowledge")
	cmd.Flags().StringSliceVar(&f.unack, "unack", nil, "item IDs to unacknowledge")
	cmd.Flags().StringVar(&f.reason, "reason", "", "why the items are acknowledged")
	cmd.Flags().StringVar(&f.expires, "expires", "", "acknowledgements lapse on this date (YYYY-MM-DD)")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "skip the confirmation question")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "show the changes without making them")
	cmd.Flags().BoolVar(&f.allowDestructive, "allow-destructive", false, "allow a revert to run DESTRUCTIVE statements")
	return cmd
}

func isExitError(err error) bool {
	_, ok := err.(*ui.ExitError)
	return ok
}

func runResolve(f resolveFlags, in io.Reader, out io.Writer) error {
	var expires *time.Time
	if f.expires != "" {
		t, err := time.Parse("2006-01-02", f.expires)
		if err != nil {
			return fmt.Errorf("--expires: %w", err)
		}
		expires = &t
	}
	if len(f.adopt)+len(f.revert)+len(f.ignoreIDs)+len(f.ack)+len(f.unack) == 0 {
		return fmt.Errorf("no items chosen: pass --adopt, --revert, --ignore, --ack or --unack with item IDs (see dpg verify)")
	}

	env, err := loadResolveEnv(f)
	if err != nil {
		return err
	}
	decisions, err := env.decisions(f, expires)
	if err != nil {
		return err
	}
	plan, err := resolve.Prepare(env.targets, decisions, time.Now(), env.deps)
	if err != nil {
		return err
	}
	color := false
	if f, ok := out.(*os.File); ok {
		color = ui.IsColorEnabled(f)
	}
	renderResolvePlan(out, plan, color)
	if len(plan.Problems) > 0 {
		return &ui.ExitError{Code: 2, Err: fmt.Errorf("%d decision(s) can't be carried out", len(plan.Problems))}
	}
	if plan.Empty() {
		ui.PrintInfo(out, "", "Nothing to change.", color)
		return nil
	}
	if !f.allowDestructive {
		for _, m := range plan.Migrations {
			for _, op := range m.Ops {
				if op.Safety() == pipeline.Destructive {
					return fmt.Errorf("DPG-E025: a revert contains destructive operations; re-run with --allow-destructive\n  first: %s", op.SQL())
				}
			}
		}
	}
	if f.dryRun {
		ui.PrintInfo(out, "dry-run", "nothing changed", color)
		return nil
	}
	if !f.yes {
		fmt.Fprintf(out, "\n%s [y/N] ", ui.Bold("Make these changes?", color))
		sc := bufio.NewScanner(in)
		if !sc.Scan() || !strings.EqualFold(strings.TrimSpace(sc.Text()), "y") {
			ui.PrintInfo(out, "", "Aborted.", color)
			return nil
		}
	}
	if err := resolve.Execute(plan, func() error { return env.check(decisions) }); err != nil {
		return err
	}
	ui.PrintSuccess(out, "Resolved", fmt.Sprintf("%d item(s)", len(decisions)), color)
	return nil
}

// resolveEnv is the loaded project and its targets.
type resolveEnv struct {
	proj    *project.Project
	flags   resolveFlags
	targets []*resolve.Target
	deps    resolve.Deps
}

func loadResolveEnv(f resolveFlags) (*resolveEnv, error) {
	proj, err := discoverProject()
	if err != nil {
		return nil, err
	}
	loadEnv(proj, envFile)
	differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if err != nil {
		return nil, err
	}
	fmtOpts := format.Options{IndentSize: proj.RootConfig.Fmt.IndentSize, KeywordCase: proj.RootConfig.Fmt.KeywordCase}
	if fmtOpts.IndentSize <= 0 {
		fmtOpts.IndentSize = 4
	}
	if fmtOpts.KeywordCase == "" {
		fmtOpts.KeywordCase = "upper"
	}
	env := &resolveEnv{proj: proj, flags: f, deps: resolve.Deps{
		Differ: differ,
		Render: func(obj pipeline.IRObject) string {
			var b strings.Builder
			renderObjectDPG(&b, obj, fmtOpts)
			return b.String()
		},
		Compile: func(files []string, dir string, opts compiler.Options) ([]pipeline.IRObject, error) {
			objs, _, err := compiler.CompileWith(files, dir, pipeline.Default, opts)
			return objs, err
		},
	}}
	env.targets, err = env.load()
	return env, err
}

// load builds a resolve target per cluster (its cluster-level objects) and
// database, with a fresh drift report.
func (env *resolveEnv) load() ([]*resolve.Target, error) {
	proj, f := env.proj, env.flags
	clusters, err := resolveClusters(proj, f.cluster)
	if err != nil {
		return nil, err
	}
	store, err := pipeline.MustResolve[pipeline.SnapshotStore](pipeline.Default, pipeline.KeySnapshotStore)
	if err != nil {
		return nil, err
	}
	introspector, err := pipeline.MustResolve[pipeline.Introspector](pipeline.Default, pipeline.KeyIntrospector)
	if err != nil {
		return nil, err
	}
	secretResolver, err := pipeline.MustResolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
	if err != nil {
		return nil, err
	}
	emitter, err := pipeline.MustResolve[pipeline.Emitter](pipeline.Default, pipeline.KeyEmitter)
	if err != nil {
		return nil, err
	}
	var targets []*resolve.Target
	for _, cl := range clusters {
		cl := cl
		if len(cl.SourceFiles) > 0 {
			rules := ignore.New(cl.EffectiveIgnore(proj.RootConfig))
			t, err := env.target(cl, nil, cl.SourceFiles, cl.ObjectsDir, compiler.Options{}, rules, store, introspector, secretResolver, emitter)
			if err != nil {
				return nil, fmt.Errorf("%s (cluster): %w", cl.Name(), err)
			}
			targets = append(targets, t)
		}
		databases, err := resolveDatabases(cl, f.database)
		if err != nil {
			return nil, err
		}
		for _, db := range databases {
			rules := ignore.New(db.EffectiveIgnore(cl, proj.RootConfig))
			t, err := env.target(cl, db, db.SourceFiles, db.Dir, dbCompileOptions(db), rules, store, introspector, secretResolver, emitter)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
			}
			targets = append(targets, t)
		}
	}
	return targets, nil
}

func (env *resolveEnv) target(cl *project.Cluster, db *project.Database, files []string, dir string, opts compiler.Options,
	rules *ignore.Rules, store pipeline.SnapshotStore, introspector pipeline.Introspector,
	secretResolver pipeline.SecretResolver, emitter pipeline.Emitter) (*resolve.Target, error) {
	ctx := context.Background()
	// [ignore] patterns go to the narrowest config: the database's for its
	// objects, the cluster's for cluster-level ones.
	dbName, snapKey, configPath := "(cluster)", cl.ClusterSnapshotKey(), filepath.Join(cl.Dir, "dpg.toml")
	if db != nil {
		dbName, snapKey, configPath = db.Name(), db.Name(), filepath.Join(db.Dir, "dpg.toml")
	}
	snap, err := store.Load(cl.Name(), snapKey)
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}
	if err := rules.FilterSnapshot(snap); err != nil {
		return nil, err
	}

	var live []pipeline.IRObject
	connect := func() (*executor.PgxConn, error) {
		if db != nil {
			return connectDatabase(ctx, cl, db, secretResolver)
		}
		connStr := cl.ConnectionString()
		if connStr == "" {
			return nil, fmt.Errorf("DPG-E003: cluster %q has no connection configured", cl.Name())
		}
		if cl.IsLink() {
			if connStr, err = secretResolver.Resolve(connStr); err != nil {
				return nil, ui.WrapDB(err)
			}
		}
		conn, err := executor.Connect(ctx, connStr)
		return conn, ui.WrapDB(err)
	}
	conn, err := connect()
	if err != nil {
		return nil, err
	}
	all, err := introspector.Introspect(ctx, conn)
	conn.Close(ctx)
	if err != nil {
		return nil, ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}
	for _, o := range rules.FilterObjects(all) {
		if (db == nil) == isClusterScoped(o) {
			live = append(live, o)
		}
	}

	var index *srcindex.Index
	if index, err = srcindex.Build(files, dir, pipeline.Default, opts); err != nil {
		return nil, err
	}
	source, _, err := compiler.CompileWith(files, dir, pipeline.Default, opts)
	if err != nil {
		return nil, err
	}
	source = rules.FilterObjects(source)
	report, err := drift.Detect(env.deps.Differ, live, snap, drift.Options{Ignore: rules, Source: source})
	if err != nil {
		return nil, err
	}
	ackFile := ackPath(env.proj, cl.Name(), snapKey)
	acks, err := drift.LoadAcks(ackFile)
	if err != nil {
		return nil, err
	}
	drift.ApplyAcks(report, acks, time.Now())

	t := &resolve.Target{
		Cluster: cl.Name(), Database: dbName,
		Report: report, Snapshot: snap, Live: live,
		Source: source, Index: index, SourceFiles: files, SourceDir: dir, CompileOpts: opts,
		ConfigPath: configPath,
		AckPath:    ackFile,
		SaveSnapshot: func(s *pipeline.Snapshot) error {
			return store.Save(cl.Name(), snapKey, s)
		},
		NewFile: func(kind, key string) string { return newDeclarationFile(dir, db == nil, kind, key) },
	}
	t.Revert = func(ops []pipeline.DiffOp) error {
		conn, err := connect()
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		if err := executor.ValidateOwnerMembership(ctx, conn, ops); err != nil {
			return err
		}
		meta := pipeline.MigrationMeta{GeneratedAt: time.Now().UTC(), Cluster: cl.Name(), SourceRoot: cl.ProjectRoot, Database: snapKey}
		m, err := emitter.Emit(ops, meta)
		if err != nil {
			return err
		}
		if err := executor.New().Apply(ctx, m, conn); err != nil {
			return ui.WrapDB(err)
		}
		var sql strings.Builder
		if err := emit.Render(&sql, m, emit.DefaultRenderOptions()); err == nil {
			_, _ = snapshotpkg.SaveMigration(env.proj.MigrationsDir(), cl.Name(), snapKey, sql.String())
		}
		return nil
	}
	return t, nil
}

// newDeclarationFile is the file a newly adopted declaration goes to,
// following the project layout: tables in schemas/<s>/tables/<name>.dpg,
// views, functions, types and sequences in schemas/<s>/<kind>.dpg, other
// schema objects in schemas/<s>/schema.dpg, objects outside a schema in
// objects.dpg, and cluster-level objects in roles.dpg / tablespaces.dpg.
func newDeclarationFile(dir string, cluster bool, kind, key string) string {
	if cluster {
		if kind == "tablespace" {
			return filepath.Join(dir, "tablespaces.dpg")
		}
		return filepath.Join(dir, "roles.dpg")
	}
	schema, name, qualified := strings.Cut(key, ".")
	if !qualified || kind == "schema" || kind == "extension" || kind == "fdw" || kind == "server" ||
		kind == "user_mapping" || kind == "publication" || kind == "subscription" || kind == "event_trigger" || kind == "cast" {
		return filepath.Join(dir, "objects.dpg")
	}
	if i := strings.IndexAny(name, "( "); i > 0 {
		name = name[:i]
	}
	sdir := filepath.Join(dir, "schemas", schema)
	switch kind {
	case "table":
		return filepath.Join(sdir, "tables", name+".dpg")
	case "view":
		return filepath.Join(sdir, "views.dpg")
	case "function", "procedure", "aggregate":
		return filepath.Join(sdir, "functions.dpg")
	case "type":
		return filepath.Join(sdir, "types.dpg")
	case "sequence":
		return filepath.Join(sdir, "sequences.dpg")
	}
	return filepath.Join(sdir, "schema.dpg")
}

// decisions maps the flags to decisions; an ID must name exactly one item
// across the targets.
func (env *resolveEnv) decisions(f resolveFlags, expires *time.Time) ([]resolve.Decision, error) {
	by := gitUserName()
	var out []resolve.Decision
	add := func(ids []string, a resolve.Action) error {
		for _, id := range ids {
			var found []*resolve.Target
			for _, t := range env.targets {
				for _, it := range t.Report.Items {
					if it.ID == id {
						found = append(found, t)
						break
					}
				}
			}
			switch len(found) {
			case 0:
				return fmt.Errorf("%s: no such item (see dpg verify)", id)
			case 1:
			default:
				return fmt.Errorf("%s: found in %d clusters/databases; choose one with --cluster/--database", id, len(found))
			}
			out = append(out, resolve.Decision{Target: found[0].Label(), ItemID: id, Action: a, Reason: f.reason, Expires: expires, By: by})
		}
		return nil
	}
	for _, x := range []struct {
		ids []string
		a   resolve.Action
	}{{f.adopt, resolve.Adopt}, {f.revert, resolve.Revert}, {f.ignoreIDs, resolve.Ignore}, {f.ack, resolve.Acknowledge}, {f.unack, resolve.Unacknowledge}} {
		if err := add(x.ids, x.a); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// check is the round trip after writing: the source compiles, every
// resolved item is gone (acknowledged ones are acknowledged, unacknowledged
// ones aren't), and no new failing item appeared.
func (env *resolveEnv) check(decisions []resolve.Decision) error {
	before := map[string]map[string]bool{}
	for _, t := range env.targets {
		before[t.Label()] = map[string]bool{}
		for _, it := range t.Report.Items {
			if it.Failing() {
				before[t.Label()][it.ID] = true
			}
		}
	}
	// An unacknowledged item is expected to fail again.
	for _, d := range decisions {
		if d.Action == resolve.Unacknowledge && before[d.Target] != nil {
			before[d.Target][d.ItemID] = true
		}
	}
	// The project is discovered again: an [ignore] edit changes its config.
	proj, err := discoverProject()
	if err != nil {
		return err
	}
	fresh := &resolveEnv{proj: proj, flags: env.flags, deps: env.deps}
	after, err := fresh.load()
	if err != nil {
		return err
	}
	byLabel := map[string]*resolve.Target{}
	for _, t := range after {
		byLabel[t.Label()] = t
	}
	var problems []string
	for _, d := range decisions {
		t := byLabel[d.Target]
		if t == nil {
			problems = append(problems, d.Target+" is gone")
			continue
		}
		var it *drift.Item
		for i := range t.Report.Items {
			if t.Report.Items[i].ID == d.ItemID {
				it = &t.Report.Items[i]
			}
		}
		switch d.Action {
		case resolve.Acknowledge:
			if it == nil || it.Acknowledged == nil {
				problems = append(problems, d.ItemID+" is not acknowledged")
			}
		case resolve.Unacknowledge:
			if it != nil && it.Acknowledged != nil {
				problems = append(problems, d.ItemID+" is still acknowledged")
			}
		default:
			if it != nil && it.Acknowledged == nil {
				problems = append(problems, fmt.Sprintf("%s is still reported (%s)", d.ItemID, it.Group))
			}
		}
	}
	for _, t := range after {
		for _, it := range t.Report.Items {
			if it.Failing() && !before[t.Label()][it.ID] {
				problems = append(problems, "new drift: "+it.ID)
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// gitUserName names who acknowledges an item: git's user.name, else $USER.
func gitUserName() string {
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return os.Getenv("USER")
}

// renderResolvePlan prints what a plan changes.
func renderResolvePlan(w io.Writer, p *resolve.Plan, color bool) {
	for _, pr := range p.Problems {
		fmt.Fprintf(w, "%s %s  %s: %s\n", ui.Red("error", color), ui.Cyan(pr.Target, color), pr.ItemID, pr.Reason)
	}
	for _, m := range p.Migrations {
		fmt.Fprintf(w, "\n%s  %s\n", ui.Cyan(m.Target.Label(), color), ui.Bold("revert (runs in the database)", color))
		for _, op := range m.Ops {
			fmt.Fprintf(w, "  %s  %s\n", ui.Dim("["+op.Safety().String()+"]", color), op.SQL())
		}
	}
	for _, fc := range p.Files {
		fmt.Fprintln(w)
		diff := strings.TrimSuffix(resolve.UnifiedDiff(fc.Path, string(fc.Before), string(fc.After)), "\n")
		for _, line := range strings.Split(diff, "\n") {
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
				line = ui.Bold(line, color)
			case strings.HasPrefix(line, "@@"):
				line = ui.Cyan(line, color)
			case strings.HasPrefix(line, "+"):
				line = ui.Green(line, color)
			case strings.HasPrefix(line, "-"):
				line = ui.Red(line, color)
			}
			fmt.Fprintln(w, line)
		}
	}
	for _, s := range p.Snapshots {
		var keys []string
		for k, v := range s.After.Objects {
			if old, ok := s.Before.Objects[k]; !ok || string(old) != string(v) {
				keys = append(keys, k)
			}
		}
		for k := range s.Before.Objects {
			if _, ok := s.After.Objects[k]; !ok {
				keys = append(keys, k+" (removed)")
			}
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			fmt.Fprintf(w, "\n%s  snapshot: %s\n", ui.Cyan(s.Target.Label(), color), strings.Join(keys, ", "))
		}
	}
	for _, a := range p.Acks {
		fmt.Fprintf(w, "\n%s  acknowledgements: %d → %d (%s)\n", ui.Cyan(a.Target.Label(), color), len(a.Before), len(a.After), a.Target.AckPath)
	}
}
