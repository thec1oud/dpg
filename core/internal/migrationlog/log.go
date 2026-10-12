// Package migrationlog is the ordered log of a database's migrations (RFC
// Section 16.8): each applied migration's SQL and the change it made to the
// snapshot, chained by parent and hashed, so the snapshot at any point of
// the log can be rebuilt without a database and checked against the
// committed one.
package migrationlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// Format is the version of the entry format.
const Format = "1"

// Origin says what produced a migration.
const (
	OriginApply    = "apply"    // dpg apply
	OriginAdopt    = "adopt"    // dpg resolve --adopt: the database it was adopted from already has it
	OriginBaseline = "baseline" // the first entry of a project that had a snapshot before its log
)

// Stages of a step.
const (
	StageBefore      = "before"      // committed one by one before the transaction
	StageTransaction = "transaction" // inside the transaction
	StageAfter       = "after"       // after COMMIT (CREATE INDEX CONCURRENTLY, ...)
	StageInstruction = "instruction" // a step the operator does by hand; never run
)

// Step is one statement of a migration, with {{...}} secret references
// unresolved.
type Step struct {
	SQL    string `json:"sql"`
	Safety string `json:"safety"` // SAFE, CAUTION, DESTRUCTIVE
	Stage  string `json:"stage"`
}

// StepsOf returns a migration's statements in the order they run.
func StepsOf(m pipeline.Migration) []Step {
	var out []Step
	add := func(ops []pipeline.DiffOp, stage string) {
		for _, op := range ops {
			st := stage
			if op.Mode() == pipeline.ModeInstructionOnly {
				st = StageInstruction
			}
			out = append(out, Step{SQL: op.SQL(), Safety: op.Safety().String(), Stage: st})
		}
	}
	add(m.PreTransactional, StageBefore)
	add(m.Transactional, StageTransaction)
	add(m.NonTransactional, StageAfter)
	return out
}

// StepsChecksum identifies a migration's statements.
func StepsChecksum(steps []Step) string {
	h := sha256.New()
	for _, s := range steps {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", s.Stage, s.Safety, s.SQL)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Entry is one migration of the log: <ID>.json beside <ID>.sql.
type Entry struct {
	Format string `json:"format"`
	ID     string `json:"id"`
	// Parent is the ID of the migration this one follows; "" for the first.
	Parent string `json:"parent"`
	// Checksum is the sha256 of the SQL file (set by Write).
	Checksum string `json:"checksum"`
	// Steps are the statements dpg runs; StepsChecksum identifies them
	// (the ID is derived from it).
	Steps         []Step `json:"steps"`
	StepsChecksum string `json:"steps_checksum"`
	// Snapshot is Hash of the snapshot objects after this migration.
	Snapshot string `json:"snapshot"`
	Origin   string `json:"origin"`
	// Set and Remove are the change to the snapshot objects.
	Set    map[string]json.RawMessage `json:"set,omitempty"`
	Remove []string                   `json:"remove,omitempty"`
}

// Hash identifies a set of snapshot objects, independent of JSON layout.
func Hash(objects map[string]json.RawMessage) (string, error) {
	keys := make([]string, 0, len(objects))
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		c, err := Canonical(objects[k])
		if err != nil {
			return "", fmt.Errorf("%s: %w", k, err)
		}
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(c)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Canonical re-encodes a JSON value with sorted keys and no insignificant
// whitespace, keeping numbers exactly as written.
func Canonical(raw json.RawMessage) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v) // map keys are marshalled sorted
}

// Checksum is the checksum of a migration's SQL.
func Checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// New builds the entry taking the snapshot objects from before to after
// with steps, following parent. Its SQL file is given to Write.
func New(parent string, steps []Step, origin string, before, after map[string]json.RawMessage, now time.Time) (*Entry, error) {
	if steps == nil {
		steps = []Step{}
	}
	e := &Entry{Format: Format, Parent: parent, Steps: steps, StepsChecksum: StepsChecksum(steps), Origin: origin, Set: map[string]json.RawMessage{}}
	for k, v := range after {
		cv, err := Canonical(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if old, ok := before[k]; ok {
			co, err := Canonical(old)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			if bytes.Equal(co, cv) {
				continue
			}
		}
		e.Set[k] = cv
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			e.Remove = append(e.Remove, k)
		}
	}
	sort.Strings(e.Remove)
	h, err := Hash(after)
	if err != nil {
		return nil, err
	}
	e.Snapshot = h
	e.ID = now.UTC().Format("20060102T150405Z") + "-" + strings.TrimPrefix(e.StepsChecksum, "sha256:")[:12]
	return e, nil
}

// Apply applies the entry's change to objects (in place).
func (e *Entry) Apply(objects map[string]json.RawMessage) {
	for _, k := range e.Remove {
		delete(objects, k)
	}
	for k, v := range e.Set {
		objects[k] = v
	}
}

// Log is a database's migrations in order.
type Log struct {
	Dir     string // <migrations dir>/<cluster>/<database>
	Entries []*Entry
}

// Head returns the ID of the last migration, or "".
func (l *Log) Head() string {
	if len(l.Entries) == 0 {
		return ""
	}
	return l.Entries[len(l.Entries)-1].ID
}

// Dir is where a database's log lives.
func Dir(migrationsDir, cluster, database string) string {
	return filepath.Join(migrationsDir, cluster, database)
}

// Error is a log that can't be used; Code is its DPG error code.
type Error struct {
	Code, Msg string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func errorf(code, format string, args ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Load reads the log in dir and orders it by parent. Files without a .json
// entry (archives from before the log) are not part of it. It fails when
// the entries don't form one chain: two migrations following the same
// parent (divergent heads), a parent that isn't in the log, or an entry
// without its SQL file or with a different checksum.
func Load(dir string) (*Log, error) {
	l := &Log{Dir: dir}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	byParent := map[string][]*Entry{}
	byID := map[string]*Entry{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, errorf("DPG-E046", "%s: %v", f, err)
		}
		if e.Format != Format {
			return nil, errorf("DPG-E046", "%s: entry format %q; this dpg reads format %q", f, e.Format, Format)
		}
		if e.ID+".json" != filepath.Base(f) {
			return nil, errorf("DPG-E046", "%s: the file name doesn't match its id %q", f, e.ID)
		}
		sql, err := os.ReadFile(filepath.Join(dir, e.ID+".sql"))
		if err != nil {
			return nil, errorf("DPG-E046", "%s: its SQL file is missing", f)
		}
		if Checksum(string(sql)) != e.Checksum {
			return nil, errorf("DPG-E046", "%s.sql was changed after it was written (checksum mismatch)", filepath.Join(dir, e.ID))
		}
		if StepsChecksum(e.Steps) != e.StepsChecksum {
			return nil, errorf("DPG-E046", "%s: its steps were changed after it was written (checksum mismatch)", f)
		}
		ep := e
		byID[e.ID] = &ep
		byParent[e.Parent] = append(byParent[e.Parent], &ep)
	}
	for parent, children := range byParent {
		if len(children) > 1 {
			var ids []string
			for _, c := range children {
				ids = append(ids, c.ID)
			}
			sort.Strings(ids)
			after := parent
			if after == "" {
				after = "the start of the log"
			}
			return nil, errorf("DPG-E047", "%d migrations follow %s: %s. They were made on different branches; keep the target branch's snapshot and migrations, delete your branch's migration files, and plan again", len(children), after, strings.Join(ids, ", "))
		}
		if parent != "" && byID[parent] == nil {
			return nil, errorf("DPG-E046", "migration %s follows %s, which is not in %s", children[0].ID, parent, dir)
		}
	}
	for cur := ""; ; {
		next := byParent[cur]
		if len(next) == 0 {
			break
		}
		l.Entries = append(l.Entries, next[0])
		cur = next[0].ID
	}
	if len(l.Entries) != len(byID) {
		// Only a cycle can leave entries unreached.
		return nil, errorf("DPG-E046", "the migrations in %s form a cycle", dir)
	}
	return l, nil
}

// Replay rebuilds the snapshot objects after the migration upTo ("" for
// the head), checking each step's hash.
func (l *Log) Replay(upTo string) (map[string]json.RawMessage, error) {
	objects := map[string]json.RawMessage{}
	for _, e := range l.Entries {
		e.Apply(objects)
		h, err := Hash(objects)
		if err != nil {
			return nil, err
		}
		if h != e.Snapshot {
			return nil, errorf("DPG-E046", "migration %s doesn't produce the snapshot it records (the log was edited, or a migration is missing)", e.ID)
		}
		if e.ID == upTo {
			return objects, nil
		}
	}
	if upTo != "" {
		return nil, errorf("DPG-E046", "migration %s is not in %s", upTo, l.Dir)
	}
	return objects, nil
}

// Check verifies the log against the committed snapshot: its head is the
// snapshot's migration and replaying it gives the snapshot's objects.
// A snapshot with objects but no log needs a baseline (ErrNoBaseline).
func (l *Log) Check(snapMigration string, objects map[string]json.RawMessage) error {
	if len(l.Entries) == 0 {
		if snapMigration != "" {
			return errorf("DPG-E046", "the snapshot names migration %s, but %s has no migrations", snapMigration, l.Dir)
		}
		if len(objects) > 0 {
			return ErrNoBaseline
		}
		return nil
	}
	if snapMigration != l.Head() {
		return errorf("DPG-E046", "the snapshot names migration %q, but the log ends at %s", snapMigration, l.Head())
	}
	replayed, err := l.Replay("")
	if err != nil {
		return err
	}
	want, err := Hash(objects)
	if err != nil {
		return err
	}
	if got, _ := Hash(replayed); got != want {
		return errorf("DPG-E046", "the snapshot doesn't match its migration log (it was edited by hand, or written without a migration)")
	}
	return nil
}

// ErrNoBaseline: the snapshot has objects but there is no log yet.
var ErrNoBaseline = errors.New("DPG-E048: the snapshot has no migration log yet; run dpg apply --baseline to start it")

// Write writes an entry and its SQL file to the log's directory, setting
// the entry's checksum.
func (l *Log) Write(e *Entry, sql string) error {
	e.Checksum = Checksum(sql)
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(l.Dir, e.ID+".sql"), []byte(sql)); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(l.Dir, e.ID+".json"), append(data, '\n')); err != nil {
		os.Remove(filepath.Join(l.Dir, e.ID+".sql"))
		return err
	}
	l.Entries = append(l.Entries, e)
	return nil
}

// Paths returns an entry's files.
func (l *Log) Paths(id string) (sql, entry string) {
	return filepath.Join(l.Dir, id+".sql"), filepath.Join(l.Dir, id+".json")
}

// Encode returns an entry's JSON file content.
func Encode(e *Entry) ([]byte, error) {
	data, err := json.MarshalIndent(e, "", "  ")
	return append(data, '\n'), err
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// stepOp is a logged step run again (settling a database that hasn't it).
type stepOp struct{ s Step }

func (o stepOp) SQL() string { return o.s.SQL }
func (o stepOp) Safety() pipeline.Safety {
	switch o.s.Safety {
	case "CAUTION":
		return pipeline.Caution
	case "DESTRUCTIVE":
		return pipeline.Destructive
	}
	return pipeline.Safe
}
func (o stepOp) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (o stepOp) Mode() pipeline.ExecMode {
	switch o.s.Stage {
	case StageBefore:
		return pipeline.ModePreTransactional
	case StageAfter:
		return pipeline.ModeNonTransactional
	case StageInstruction:
		return pipeline.ModeInstructionOnly
	}
	return pipeline.ModeTransactional
}

// ExecSQL resolves the step's secret references (pipeline.SecretBearingOp).
func (o stepOp) ExecSQL(r pipeline.SecretResolver) (string, error) {
	return pipeline.ResolveTemplateSQL(o.s.SQL, r)
}

// Migration returns the entry's steps as a migration; instruction steps
// (never run) are left out unless withInstructions.
func (e *Entry) Migration(meta pipeline.MigrationMeta, withInstructions bool) pipeline.Migration {
	m := pipeline.Migration{Meta: meta}
	for _, s := range e.Steps {
		op := stepOp{s}
		switch s.Stage {
		case StageBefore:
			m.PreTransactional = append(m.PreTransactional, op)
		case StageAfter:
			m.NonTransactional = append(m.NonTransactional, op)
		case StageInstruction:
			if withInstructions {
				m.Transactional = append(m.Transactional, op)
			}
		default:
			m.Transactional = append(m.Transactional, op)
		}
	}
	return m
}

// Index returns the position of migration id in the log, or -1.
func (l *Log) Index(id string) int {
	for i, e := range l.Entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// Find returns migration id, or nil.
func (l *Log) Find(id string) *Entry {
	if i := l.Index(id); i >= 0 {
		return l.Entries[i]
	}
	return nil
}
