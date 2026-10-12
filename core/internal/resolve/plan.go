package resolve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/srcindex"
)

// Target is one cluster's cluster-level objects, or one database, being
// resolved.
type Target struct {
	Cluster  string
	Database string // "(cluster)" for cluster-level objects
	Report   *drift.Report
	// Snapshot is the stored snapshot; SaveSnapshot writes a new one.
	Snapshot     *pipeline.Snapshot
	SaveSnapshot func(*pipeline.Snapshot) error
	// Live holds the introspected objects (after [ignore]).
	Live []pipeline.IRObject
	// Source and Index describe the source files: SourceFiles under
	// SourceDir, compiled with CompileOpts.
	Source      []pipeline.IRObject
	Index       *srcindex.Index
	SourceFiles []string
	SourceDir   string
	CompileOpts compiler.Options
	// NewFile returns the file a new declaration of an object of kind with
	// the given snapshot key goes to.
	NewFile func(kind, key string) string
	// ConfigPath is the dpg.toml [ignore] patterns go to.
	ConfigPath string
	// AckPath is the target's acknowledgement file.
	AckPath string
	// Revert runs a migration restoring the declared state (the normal
	// apply path: safety classes, approval, archive).
	Revert func(ops []pipeline.DiffOp) error
}

// Label names the target: "cluster (cluster)" or "cluster/database".
func (t *Target) Label() string {
	if t.Database == "(cluster)" {
		return t.Cluster + " (cluster)"
	}
	return t.Cluster + "/" + t.Database
}

// Decision is what to do with one item.
type Decision struct {
	Target string // Target.Label()
	ItemID string
	Action Action
	// Macro says how to adopt a part a macro declares: MacroEdit changes the
	// macro (every use), MacroInline expands this use and changes it there.
	Macro   MacroMode
	Reason  string     // acknowledgements
	Expires *time.Time // acknowledgements
	By      string     // acknowledgements
}

// FileChange is the new content of one file.
type FileChange struct {
	Path          string
	Before, After []byte
	existed       bool
}

// SnapshotChange is a target's new snapshot.
type SnapshotChange struct {
	Target        *Target
	Before, After *pipeline.Snapshot
}

// MigrationChange is a revert migration for a target.
type MigrationChange struct {
	Target *Target
	Ops    []pipeline.DiffOp
}

// AckChange is a target's new acknowledgements.
type AckChange struct {
	Target        *Target
	Before, After []drift.Ack
}

// Problem is a decision that can't be carried out.
type Problem struct {
	Target, ItemID string
	Reason         string
}

// Required is an adoption added because a chosen one needs it.
type Required struct {
	Target, ItemID string
	By             string // the item that needs it
}

// Plan is everything a set of decisions changes.
type Plan struct {
	Files      []*FileChange
	Snapshots  []*SnapshotChange
	Migrations []*MigrationChange
	Acks       []*AckChange
	Problems   []Problem
	Decisions  []Decision
	Required   []Required
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool {
	return len(p.Files) == 0 && len(p.Snapshots) == 0 && len(p.Migrations) == 0 && len(p.Acks) == 0
}

// planner accumulates a plan.
type planner struct {
	plan  *Plan
	files map[string]*FileChange
	snaps map[*Target]*SnapshotChange
	acks  map[*Target]*AckChange
	migs  map[*Target]*MigrationChange
	now   time.Time
}

// file returns the pending change of path, reading the file the first time.
func (pl *planner) file(path string) (*FileChange, error) {
	if fc, ok := pl.files[path]; ok {
		return fc, nil
	}
	data, err := os.ReadFile(path)
	existed := true
	if errors.Is(err, os.ErrNotExist) {
		data, existed, err = nil, false, nil
	}
	if err != nil {
		return nil, err
	}
	fc := &FileChange{Path: path, Before: data, After: append([]byte(nil), data...), existed: existed}
	pl.files[path] = fc
	pl.plan.Files = append(pl.plan.Files, fc)
	return fc, nil
}

// snapshot returns the target's pending snapshot, a copy of the stored one.
func (pl *planner) snapshot(t *Target) *pipeline.Snapshot {
	if sc, ok := pl.snaps[t]; ok {
		return sc.After
	}
	after := copySnapshot(t.Snapshot)
	sc := &SnapshotChange{Target: t, Before: t.Snapshot, After: after}
	pl.snaps[t] = sc
	pl.plan.Snapshots = append(pl.plan.Snapshots, sc)
	return after
}

func (pl *planner) ackChange(t *Target) (*AckChange, error) {
	if ac, ok := pl.acks[t]; ok {
		return ac, nil
	}
	before, err := drift.LoadAcks(t.AckPath)
	if err != nil {
		return nil, err
	}
	ac := &AckChange{Target: t, Before: before, After: append([]drift.Ack(nil), before...)}
	pl.acks[t] = ac
	pl.plan.Acks = append(pl.plan.Acks, ac)
	return ac, nil
}

func (pl *planner) problem(d Decision, format string, args ...any) {
	pl.plan.Problems = append(pl.plan.Problems, Problem{Target: d.Target, ItemID: d.ItemID, Reason: fmt.Sprintf(format, args...)})
}

// Prepare turns decisions into a plan. Nothing is written.
func Prepare(targets []*Target, decisions []Decision, now time.Time, deps Deps) (*Plan, error) {
	pl := &planner{
		plan:  &Plan{Decisions: decisions},
		files: map[string]*FileChange{},
		snaps: map[*Target]*SnapshotChange{},
		acks:  map[*Target]*AckChange{},
		migs:  map[*Target]*MigrationChange{},
		now:   now,
	}
	byLabel := map[string]*Target{}
	for _, t := range targets {
		byLabel[t.Label()] = t
	}
	// Adoptions are planned last: they edit text whose positions earlier
	// steps don't change, and several may edit the same file.
	var adopts []Decision
	for _, d := range decisions {
		t, ok := byLabel[d.Target]
		if !ok {
			pl.problem(d, "no such target")
			continue
		}
		it, ok := findItem(t.Report, d.ItemID)
		if !ok {
			pl.problem(d, "no such item")
			continue
		}
		if !Allowed(it, d.Action) {
			pl.problem(d, "%s is not possible for a %s item", d.Action, groupName(it))
			continue
		}
		var err error
		switch d.Action {
		case Acknowledge:
			err = pl.acknowledge(t, it, d)
		case Unacknowledge:
			err = pl.unacknowledge(t, it)
		case Ignore:
			err = pl.ignore(t, it, d)
		case Revert:
			err = pl.revert(t, it, d, deps)
		case Adopt:
			adopts = append(adopts, d)
		}
		if err != nil {
			return nil, err
		}
	}
	adopts = withRequired(byLabel, adopts, pl.plan)
	if err := pl.adoptAll(byLabel, adopts, deps); err != nil {
		return nil, err
	}
	// Drop files the plan didn't actually change.
	var files []*FileChange
	for _, fc := range pl.plan.Files {
		if string(fc.Before) != string(fc.After) || !fc.existed && len(fc.After) > 0 {
			files = append(files, fc)
		}
	}
	pl.plan.Files = files
	sort.Slice(pl.plan.Files, func(i, j int) bool { return pl.plan.Files[i].Path < pl.plan.Files[j].Path })
	return pl.plan, nil
}

func groupName(it drift.Item) string {
	if it.Acknowledged != nil {
		return "acknowledged"
	}
	return it.Group.String()
}

func findItem(r *drift.Report, id string) (drift.Item, bool) {
	for _, it := range r.Items {
		if it.ID == id {
			return it, true
		}
	}
	return drift.Item{}, false
}

func (pl *planner) acknowledge(t *Target, it drift.Item, d Decision) error {
	ac, err := pl.ackChange(t)
	if err != nil {
		return err
	}
	ac.After = removeAck(ac.After, it.ID)
	ac.After = append(ac.After, drift.Acknowledge(it, d.Reason, d.By, pl.now, d.Expires))
	return nil
}

func (pl *planner) unacknowledge(t *Target, it drift.Item) error {
	ac, err := pl.ackChange(t)
	if err != nil {
		return err
	}
	ac.After = removeAck(ac.After, it.ID)
	return nil
}

func removeAck(acks []drift.Ack, id string) []drift.Ack {
	var out []drift.Ack
	for _, a := range acks {
		if a.ID != id {
			out = append(out, a)
		}
	}
	return out
}

func (pl *planner) ignore(t *Target, it drift.Item, d Decision) error {
	key, pattern, ok := IgnorePattern(it.Kind, it.Object)
	if !ok {
		pl.problem(d, "a %s can't be ignored", it.Kind)
		return nil
	}
	fc, err := pl.file(t.ConfigPath)
	if err != nil {
		return err
	}
	out, err := addIgnorePattern(string(fc.After), key, pattern)
	if err != nil {
		pl.problem(d, "%v", err)
		return nil
	}
	fc.After = []byte(out)
	return nil
}

func (pl *planner) revert(t *Target, it drift.Item, d Decision, deps Deps) error {
	ops, err := deps.RevertOps(t, it)
	if err != nil {
		pl.problem(d, "%v", err)
		return nil
	}
	mc, ok := pl.migs[t]
	if !ok {
		mc = &MigrationChange{Target: t}
		pl.migs[t] = mc
		pl.plan.Migrations = append(pl.plan.Migrations, mc)
	}
	mc.Ops = append(mc.Ops, ops...)
	return nil
}

// Execute carries out a plan: revert migrations first, then file,
// snapshot and acknowledgement changes. check then runs (the round trip:
// recompile and detect again); when it fails, every file, snapshot and
// acknowledgement change is undone. A revert migration that ran is not
// undone.
func Execute(p *Plan, check func() error) error {
	for _, m := range p.Migrations {
		if len(m.Ops) == 0 {
			continue
		}
		if err := m.Target.Revert(m.Ops); err != nil {
			return fmt.Errorf("%s: revert: %w", m.Target.Label(), err)
		}
	}
	var undo []func() error
	rollback := func(cause error) error {
		var errs []error
		for i := len(undo) - 1; i >= 0; i-- {
			if err := undo[i](); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%w; rolling back also failed: %v", cause, errors.Join(errs...))
		}
		return fmt.Errorf("%w (all file, snapshot and acknowledgement changes were undone)", cause)
	}
	for _, f := range p.Files {
		f := f
		if err := writeFileAtomic(f.Path, f.After); err != nil {
			return rollback(err)
		}
		undo = append(undo, func() error {
			if !f.existed {
				return os.Remove(f.Path)
			}
			return writeFileAtomic(f.Path, f.Before)
		})
	}
	for _, s := range p.Snapshots {
		s := s
		if err := s.Target.SaveSnapshot(s.After); err != nil {
			return rollback(err)
		}
		undo = append(undo, func() error { return s.Target.SaveSnapshot(s.Before) })
	}
	for _, a := range p.Acks {
		a := a
		if err := drift.SaveAcks(a.Target.AckPath, a.After); err != nil {
			return rollback(err)
		}
		undo = append(undo, func() error { return drift.SaveAcks(a.Target.AckPath, a.Before) })
	}
	if check != nil {
		if err := check(); err != nil {
			return rollback(fmt.Errorf("round-trip check failed: %w", err))
		}
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".dpg-resolve.tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copySnapshot(s *pipeline.Snapshot) *pipeline.Snapshot {
	if s == nil {
		return &pipeline.Snapshot{}
	}
	out := *s
	out.Objects = make(map[string]json.RawMessage, len(s.Objects))
	for k, v := range s.Objects {
		out.Objects[k] = append([]byte(nil), v...)
	}
	return &out
}
