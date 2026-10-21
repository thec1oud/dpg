package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/ignore"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
)

// Verification is what Verify compares: one database, or a cluster's
// cluster-level objects.
type Verification struct {
	Cluster, Key string
	// ClusterLevel compares the cluster-level objects rather than the
	// database's.
	ClusterLevel bool
	Introspector pipeline.Introspector
	Differ       pipeline.Differ
	Rules        *ignore.Rules
	Position     LogPosition
	// Source compiles the source.
	Source func() ([]pipeline.IRObject, error)
	// Record records the fingerprints of the objects found as expected in
	// a database that keeps a ledger (see recordSeen).
	Record bool
}

// Verify compares the live objects behind conn with snap, the stored
// snapshot, at the database's position in its migration log. Only
// properties the source declares are compared.
func Verify(ctx context.Context, conn *executor.PgxConn, snap *pipeline.Snapshot, v Verification, p Printer) (*drift.Report, error) {
	snap, pending, err := Positioned(ctx, conn, v.Position, v.Cluster, v.Key, snap)
	if err != nil {
		return nil, err
	}
	if err := v.Rules.FilterSnapshot(snap); err != nil {
		return nil, err
	}
	all, err := v.Introspector.Introspect(ctx, conn)
	if err != nil {
		return nil, ui.WrapDB(fmt.Errorf("introspect: %w", err))
	}
	// Cluster-level objects (roles, tablespaces) are verified against the
	// cluster snapshot, database-scoped ones against the database's,
	// matching dump's routing (IsClusterScoped).
	var live []pipeline.IRObject
	for _, o := range v.Rules.FilterObjects(all) {
		if IsClusterScoped(o) == v.ClusterLevel {
			live = append(live, o)
		}
	}
	source, err := v.Source()
	if err != nil {
		return nil, err
	}
	r, err := DetectAt(v.Differ, live, snap, v.Rules, source, pending)
	if err == nil && v.Record {
		recordSeen(ctx, conn, v.Position, v.Key, snap, r, p)
	}
	return r, err
}

// recordSeen records the fingerprints of the objects verify found as
// expected (no item at all) in a database that keeps a ledger: the
// migrations' guards then know DPG has seen them so (RFC Section 16.10).
// Best effort: a role that can't write gets a note.
func recordSeen(ctx context.Context, conn *executor.PgxConn, lp LogPosition, log string, snap *pipeline.Snapshot, r *drift.Report, p Printer) {
	if lp.Ledger == nil {
		return
	}
	st, err := lp.Ledger.Read(ctx, conn, log)
	if err != nil || !st.Exists {
		return
	}
	if err := RecordFingerprints(ctx, conn, lp.Ledger, log, CleanObjects(ObjectKinds(snap.Objects), r), "verify"); err != nil && p.Err != nil {
		fmt.Fprintf(p.Err, "note: %v\n", err)
	}
}

// DetectAt detects drift against the snapshot at the database's position.
// The source is the next state only of a database at the log's end.
func DetectAt(differ pipeline.Differ, live []pipeline.IRObject, snap *pipeline.Snapshot, rules *ignore.Rules, source []pipeline.IRObject, pending int) (*drift.Report, error) {
	opts := drift.Options{Ignore: rules}
	if pending == 0 {
		opts.Source = rules.FilterObjects(source)
	}
	r, err := drift.Detect(differ, live, snap, opts)
	if r != nil {
		r.Pending = pending
	}
	return r, err
}

// CleanObjects keeps the objects no item of the report is about.
func CleanObjects(kinds map[string]string, r *drift.Report) map[string]string {
	dirty := map[string]bool{}
	for _, it := range r.Items {
		dirty[it.Object] = true
	}
	out := map[string]string{}
	for k, kind := range kinds {
		if !dirty[k] {
			out[k] = kind
		}
	}
	return out
}

// Describe is the one-line description of an item.
func Describe(it drift.Item) string {
	switch it.Change {
	case "missing":
		if it.Property == "" {
			return "missing from the database"
		}
		if it.Snapshot != "" {
			return "missing: " + it.Snapshot
		}
		return "missing from the database"
	case "extra":
		switch {
		case it.Property == "" && it.Group == drift.Warning:
			return "exists in the database but is not declared"
		case it.Property == "":
			return "exists in the database but is not in the snapshot"
		case it.Live != "":
			return "only in the database: " + it.Live
		}
		return "only in the database"
	case "regranted":
		return "revoked in the source, granted in the database: " + it.Live
	}
	if d := objectDifferences(it.Snapshot, it.Live); d != "" {
		return d
	}
	if it.Snapshot != "" || it.Live != "" {
		return fmt.Sprintf("snapshot %s, database %s", orNone(it.Snapshot), orNone(it.Live))
	}
	return "differs from the snapshot"
}

// objectDifferences describes two JSON objects by the keys the snapshot
// side records that differ ("default: 'new' → (none)"); keys only the
// database has are properties the source never declared and are left out.
// It returns "" when the values aren't both objects.
func objectDifferences(snapshotJSON, liveJSON string) string {
	var sm, lm map[string]json.RawMessage
	if json.Unmarshal([]byte(snapshotJSON), &sm) != nil || json.Unmarshal([]byte(liveJSON), &lm) != nil || sm == nil || lm == nil {
		return ""
	}
	var keys []string
	for k := range sm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		sv, lv := string(sm[k]), string(lm[k])
		if sv == lv {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s → %s", k, orNone(sv), orNone(lv)))
	}
	return strings.Join(parts, "; ")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
