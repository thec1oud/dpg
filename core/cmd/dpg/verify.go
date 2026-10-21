package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thec1oud/dpg/internal/apply"
	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/project"
	"github.com/thec1oud/dpg/internal/ui"
)

// verifyTarget is one compared cluster or database.
type verifyTarget struct {
	Cluster  string
	Database string // "(cluster)" for cluster-level objects
	Report   *drift.Report
}

func newVerifyCmd() *cobra.Command {
	var (
		clusterName  string
		databaseName string
		format       string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check the live database for drift against the snapshot",
		Long: `Introspects the live database catalog and compares it against the committed
snapshot. Only properties the source declares are compared. Reports drift
(declared objects or properties that differ, including conflicts with source
changes not yet applied), unmanaged objects (live objects DPG doesn't
manage) and warnings (roles and tablespaces DPG doesn't declare, grants it
didn't make). Objects matching an [ignore] pattern in dpg.toml are
not compared.

Items acknowledged with dpg resolve --ack are listed separately and are
not drift until the drift changes or the acknowledgement expires.

Exits 0 when there is no drift (unmanaged objects, warnings and
acknowledged items allowed), 1 when drift is found, 2 on error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return &ui.ExitError{Code: 2, Err: fmt.Errorf("--format must be text or json, got %q", format)}
			}
			targets, err := runVerifyAll(clusterName, databaseName)
			if err != nil {
				return &ui.ExitError{Code: 2, Err: err}
			}
			hasDrift := false
			for _, t := range targets {
				if t.Report.HasDrift() {
					hasDrift = true
				}
			}
			if format == "json" {
				if err := renderVerifyJSON(os.Stdout, targets, hasDrift); err != nil {
					return &ui.ExitError{Code: 2, Err: err}
				}
			} else {
				renderVerifyText(os.Stdout, targets, ui.IsColorEnabled(os.Stdout))
			}
			if hasDrift {
				return &ui.ExitError{Code: 1, Err: ui.ErrSilent}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterName, "cluster", "", "cluster to verify (required when multiple clusters exist)")
	cmd.Flags().StringVar(&databaseName, "database", "", "database to verify (required when multiple databases exist)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")

	return cmd
}

// runVerifyAll verifies every targeted cluster and database.
func runVerifyAll(clusterName, databaseName string) ([]verifyTarget, error) {
	proj, err := discoverProject()
	if err != nil {
		return nil, err
	}
	loadEnv(proj, envFile)

	clusters, err := resolveClusters(proj, clusterName)
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
	differ, err := pipeline.MustResolve[pipeline.Differ](pipeline.Default, pipeline.KeyDiffer)
	if err != nil {
		return nil, err
	}
	secretResolver, err := pipeline.MustResolve[pipeline.SecretResolver](pipeline.Default, pipeline.KeySecretResolver)
	if err != nil {
		return nil, err
	}

	var targets []verifyTarget
	for _, cl := range clusters {
		if len(cl.SourceFiles) > 0 {
			r, err := runVerifyCluster(cl, store, introspector, differ, secretResolver,
				ignore.New(cl.EffectiveIgnore(proj.RootConfig)), logPositionFor(proj))
			if err != nil {
				return nil, fmt.Errorf("%s (cluster): %w", cl.Name(), err)
			}
			if err := applyAckFile(proj, cl.Name(), cl.ClusterSnapshotKey(), r); err != nil {
				return nil, err
			}
			targets = append(targets, verifyTarget{Cluster: cl.Name(), Database: "(cluster)", Report: r})
		}
		databases, err := resolveDatabases(cl, databaseName)
		if err != nil {
			return nil, err
		}
		for _, db := range databases {
			r, err := runVerify(cl, db, store, introspector, differ, secretResolver,
				ignore.New(db.EffectiveIgnore(cl, proj.RootConfig)), logPositionFor(proj))
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", cl.Name(), db.Name(), err)
			}
			if err := applyAckFile(proj, cl.Name(), db.Name(), r); err != nil {
				return nil, err
			}
			targets = append(targets, verifyTarget{Cluster: cl.Name(), Database: db.Name(), Report: r})
		}
	}
	return targets, nil
}

// ackPath is the acknowledgement file of a cluster's database, or of its
// cluster-level objects (database = the cluster snapshot key).
func ackPath(proj *project.Project, cluster, database string) string {
	return filepath.Join(proj.DPGDir(), "acknowledged", cluster, database+".json")
}

// applyAckFile marks the report's acknowledged items (RFC Section 18.3).
func applyAckFile(proj *project.Project, cluster, database string, r *drift.Report) error {
	acks, err := drift.LoadAcks(ackPath(proj, cluster, database))
	if err != nil {
		return err
	}
	drift.ApplyAcks(r, acks, time.Now())
	return nil
}

// runVerify compares one database's objects with its snapshot.
func runVerify(
	cl *project.Cluster,
	db *project.Database,
	store pipeline.SnapshotStore,
	introspector pipeline.Introspector,
	differ pipeline.Differ,
	secretResolver pipeline.SecretResolver,
	rules *ignore.Rules,
	lp apply.LogPosition,
) (*drift.Report, error) {
	ctx := context.Background()

	snap, err := store.Load(cl.Name(), db.Name())
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}
	conn, err := connectDatabase(ctx, cl, db, secretResolver)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	return apply.Verify(ctx, conn, snap, apply.Verification{
		Cluster: cl.Name(), Key: db.Name(),
		Introspector: introspector, Differ: differ, Rules: rules, Position: lp,
		Source: func() ([]pipeline.IRObject, error) {
			source, _, err := compiler.CompileWith(db.SourceFiles, db.Dir, pipeline.Default, apply.CompileOptions(db))
			return source, err
		},
		Record: true,
	}, verifyPrinter())
}

// verifyPrinter is where verify's notes go.
func verifyPrinter() apply.Printer {
	return apply.Printer{Err: os.Stderr, ErrColor: ui.IsColorEnabled(os.Stderr)}
}

// runVerifyCluster compares cluster-level objects (roles, tablespaces) with
// the cluster snapshot.
func runVerifyCluster(
	cl *project.Cluster,
	store pipeline.SnapshotStore,
	introspector pipeline.Introspector,
	differ pipeline.Differ,
	secretResolver pipeline.SecretResolver,
	rules *ignore.Rules,
	lp apply.LogPosition,
) (*drift.Report, error) {
	ctx := context.Background()

	snap, err := store.Load(cl.Name(), cl.ClusterSnapshotKey())
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}

	connStr := cl.ConnectionString()
	if connStr == "" {
		return nil, fmt.Errorf("DPG-E003: cluster %q has no connection configured (set url or link in cluster dpg.toml)", cl.Name())
	}
	if cl.IsLink() {
		connStr, err = secretResolver.Resolve(connStr)
		if err != nil {
			return nil, ui.WrapDB(fmt.Errorf("resolve connection secret: %w", err))
		}
	}
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		return nil, ui.WrapDB(withMissingDatabaseHint(err))
	}
	defer conn.Close(ctx)
	return apply.Verify(ctx, conn, snap, apply.Verification{
		Cluster: cl.Name(), Key: cl.ClusterSnapshotKey(), ClusterLevel: true,
		Introspector: introspector, Differ: differ, Rules: rules, Position: lp,
		Source: func() ([]pipeline.IRObject, error) {
			source, _, err := compiler.Compile(cl.SourceFiles, cl.ObjectsDir, pipeline.Default)
			return source, err
		},
		Record: true,
	}, verifyPrinter())
}

// verifyGroups are the report groups in display order, with headings.
var verifyGroups = []struct {
	group   drift.Group
	heading string
	paint   func(string, bool) string
}{
	{drift.Drift, "drift", ui.Red},
	{drift.Conflict, "conflicts (changed in the source and in the database)", ui.Red},
	{drift.Converged, "converged (the database already has the source's change; the snapshot is out of date)", ui.Yellow},
	{drift.Unmanaged, "unmanaged", ui.Yellow},
	{drift.Warning, "warnings", ui.Yellow},
	{acknowledgedGroup, "acknowledged", ui.Dim},
}

// acknowledgedGroup is the display section for acknowledged items, which
// keep their own group.
const acknowledgedGroup drift.Group = -1

// describeAck describes an acknowledgement.
func describeAck(a drift.Ack, g drift.Group) string {
	s := "acknowledged " + g.String()
	if a.By != "" {
		s += " by " + a.By
	}
	s += " on " + a.At.Format("2006-01-02")
	if a.Expires != nil {
		s += ", until " + a.Expires.Format("2006-01-02")
	}
	if a.Reason != "" {
		s += ": " + a.Reason
	}
	return s
}

// renderVerifyText writes the human-readable report.
func renderVerifyText(w io.Writer, targets []verifyTarget, color bool) {
	counts := map[drift.Group]int{}
	for _, t := range targets {
		label := t.Cluster + "/" + t.Database
		if t.Database == "(cluster)" {
			label = t.Cluster + " (cluster)"
		}
		pending := ""
		if t.Report.Pending > 0 {
			pending = fmt.Sprintf(" (%d migration(s) of the log not run here yet: compared with the log at this database's position; dpg apply runs them)", t.Report.Pending)
		}
		if len(t.Report.Items) == 0 {
			ui.PrintInfo(w, label, "no drift detected"+pending, color)
			continue
		}
		fmt.Fprintf(w, "%s%s\n", ui.Cyan(label, color), ui.Dim(pending, color))
		for _, g := range verifyGroups {
			var items []drift.Item
			for _, it := range t.Report.Items {
				if (g.group == acknowledgedGroup) == (it.Acknowledged != nil) && (it.Acknowledged != nil || it.Group == g.group) {
					items = append(items, it)
				}
			}
			if len(items) == 0 {
				continue
			}
			counts[g.group] += len(items)
			fmt.Fprintf(w, "  %s\n", g.paint(g.heading, color))
			for _, it := range items {
				fmt.Fprintf(w, "    %s  %s\n", it.ID, apply.Describe(it))
				switch {
				case it.Acknowledged != nil:
					fmt.Fprintf(w, "      %s\n", ui.Dim(describeAck(*it.Acknowledged, it.Group), color))
				case it.Lapsed != nil:
					fmt.Fprintf(w, "      %s\n", ui.Yellow("acknowledgement lapsed: the values changed or it expired", color))
				}
				for _, s := range it.SQL {
					fmt.Fprintf(w, "      %s\n", ui.Dim(s, color))
				}
			}
		}
		fmt.Fprintln(w)
	}
	var parts []string
	for _, g := range verifyGroups {
		if n := counts[g.group]; n > 0 {
			name := g.group.String()
			if g.group == acknowledgedGroup {
				name = "acknowledged"
			}
			if n > 1 && (g.group == drift.Conflict || g.group == drift.Warning) {
				name += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
		}
	}
	if len(parts) > 0 {
		fmt.Fprintln(w, strings.Join(parts, ", "))
	}
}

type verifyItemJSON struct {
	ID       string   `json:"id"`
	Group    string   `json:"group"`
	Kind     string   `json:"kind"`
	Object   string   `json:"object"`
	Property string   `json:"property,omitempty"`
	Change   string   `json:"change"`
	Snapshot string   `json:"snapshot,omitempty"`
	Live     string   `json:"live,omitempty"`
	SQL      []string `json:"sql,omitempty"`
	// Acknowledged is the item's acknowledgement; Lapsed marks one that no
	// longer applies.
	Acknowledged *drift.Ack `json:"acknowledged,omitempty"`
	Lapsed       bool       `json:"lapsed,omitempty"`
	Source       string     `json:"source,omitempty"`
}

type verifyTargetJSON struct {
	Cluster  string `json:"cluster"`
	Database string `json:"database"`
	// Pending is how many migrations of the log the database hasn't run.
	Pending int              `json:"pending"`
	Items   []verifyItemJSON `json:"items"`
}

// renderVerifyJSON writes one JSON document for all targets.
func renderVerifyJSON(w io.Writer, targets []verifyTarget, hasDrift bool) error {
	out := struct {
		Drift   bool               `json:"drift"`
		Targets []verifyTargetJSON `json:"targets"`
	}{Drift: hasDrift, Targets: []verifyTargetJSON{}}
	for _, t := range targets {
		tj := verifyTargetJSON{Cluster: t.Cluster, Database: t.Database, Pending: t.Report.Pending, Items: []verifyItemJSON{}}
		for _, it := range t.Report.Items {
			tj.Items = append(tj.Items, verifyItemJSON{
				ID: it.ID, Group: it.Group.String(), Kind: it.Kind, Object: it.Object, Property: it.Property,
				Change: it.Change, Snapshot: it.Snapshot, Live: it.Live, Source: it.Source, SQL: it.SQL,
				Acknowledged: it.Acknowledged, Lapsed: it.Lapsed != nil,
			})
		}
		out.Targets = append(out.Targets, tj)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
