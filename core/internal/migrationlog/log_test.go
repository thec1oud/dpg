package migrationlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func objs(kv ...string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = json.RawMessage(kv[i+1])
	}
	return m
}

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// chain writes migrations taking the objects through states, returning the
// log directory.
func chain(t *testing.T, states ...map[string]json.RawMessage) (string, []*Entry) {
	t.Helper()
	dir := t.TempDir()
	l := &Log{Dir: dir}
	before := map[string]json.RawMessage{}
	var out []*Entry
	for i, after := range states {
		sql := "-- step " + string(rune('a'+i)) + "\n"
		steps := []Step{{SQL: "SELECT " + string(rune('a'+i)) + ";", Safety: "SAFE", Stage: StageTransaction}}
		e, err := New(l.Head(), steps, OriginApply, before, after, t0.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Write(e, sql); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		before = after
	}
	return dir, out
}

func TestHashIgnoresLayout(t *testing.T) {
	a, _ := Hash(objs("t", `{"kind":"table","table":{"name":"t","n":9223372036854775807}}`))
	b, _ := Hash(objs("t", "{\n  \"table\": {\"n\": 9223372036854775807, \"name\": \"t\"},\n  \"kind\": \"table\"\n}"))
	c, _ := Hash(objs("t", `{"kind":"table","table":{"name":"t","n":9223372036854775806}}`))
	if a != b || a == c {
		t.Errorf("hashes: %s %s %s", a, b, c)
	}
}

func TestEntryDiff(t *testing.T) {
	before := objs("a", `{"x":1}`, "b", `{"x":2}`, "c", `{"x":3}`)
	after := objs("a", `{ "x" : 1 }`, "b", `{"x":20}`, "d", `{"x":4}`)
	e, err := New("p", []Step{{SQL: "SELECT 1;", Safety: "SAFE", Stage: StageTransaction}}, OriginApply, before, after, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Set) != 2 || string(e.Set["b"]) != `{"x":20}` || string(e.Set["d"]) != `{"x":4}` {
		t.Errorf("set: %v", e.Set)
	}
	if strings.Join(e.Remove, ",") != "c" {
		t.Errorf("remove: %v", e.Remove)
	}
	if !strings.HasPrefix(e.ID, "20261012T120000Z-") || len(e.ID) != len("20261012T120000Z-")+12 {
		t.Errorf("id: %s", e.ID)
	}
	// The same statements after another migration are another migration.
	steps := []Step{{SQL: "SELECT 1;", Safety: "SAFE", Stage: StageTransaction}}
	e2, _ := New("q", steps, OriginApply, before, after, t0)
	if e2.ID == e.ID || e2.StepsChecksum != e.StepsChecksum {
		t.Errorf("same steps, other parent: %s %s", e.ID, e2.ID)
	}
	got := objs("a", `{"x":1}`, "b", `{"x":2}`, "c", `{"x":3}`)
	e.Apply(got)
	if h1, _ := Hash(got); h1 != e.Snapshot {
		t.Error("applying the entry doesn't give its snapshot")
	}
}

func TestLoadReplayCheck(t *testing.T) {
	s1 := objs("public", `{"kind":"schema"}`)
	s2 := objs("public", `{"kind":"schema"}`, "public.t", `{"kind":"table","v":1}`)
	s3 := objs("public.t", `{"kind":"table","v":2}`)
	dir, es := chain(t, s1, s2, s3)
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 3 || l.Head() != es[2].ID || l.Entries[0].Parent != "" {
		t.Fatalf("log: %+v", l.Entries)
	}
	mid, err := l.Replay(es[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := Hash(mid); h != es[1].Snapshot {
		t.Error("replay to the middle")
	}
	if err := l.Check(es[2].ID, s3); err != nil {
		t.Errorf("check: %v", err)
	}
	if err := l.Check(es[1].ID, s3); err == nil {
		t.Error("a snapshot naming another migration must fail")
	}
	if err := l.Check(es[2].ID, s2); err == nil {
		t.Error("a snapshot that doesn't match the replay must fail")
	}
}

func TestLoadRejects(t *testing.T) {
	s1, s2 := objs("a", `1`), objs("a", `2`)
	code := func(err error) string {
		var e *Error
		if errors.As(err, &e) {
			return e.Code
		}
		return ""
	}

	// Two migrations after the same parent (two branches).
	dir, es := chain(t, s1, s2)
	other, _ := New(es[0].ID, []Step{{SQL: "SELECT 3;", Safety: "SAFE", Stage: StageTransaction}}, OriginApply, s1, objs("a", `3`), t0.Add(time.Hour))
	l := &Log{Dir: dir}
	if err := l.Write(other, "-- other branch\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); code(err) != "DPG-E047" || !strings.Contains(err.Error(), es[1].ID) || !strings.Contains(err.Error(), other.ID) {
		t.Errorf("divergent heads: %v", err)
	}

	// A missing parent.
	dir, es = chain(t, s1, s2)
	sqlPath, entryPath := (&Log{Dir: dir}).Paths(es[0].ID)
	os.Remove(sqlPath)
	os.Remove(entryPath)
	if _, err := Load(dir); code(err) != "DPG-E046" || !strings.Contains(err.Error(), "not in") {
		t.Errorf("missing parent: %v", err)
	}

	// Edited SQL.
	dir, es = chain(t, s1, s2)
	sqlPath, _ = (&Log{Dir: dir}).Paths(es[1].ID)
	os.WriteFile(sqlPath, []byte("DROP TABLE everything;\n"), 0o644)
	if _, err := Load(dir); code(err) != "DPG-E046" || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("edited SQL: %v", err)
	}

	// Edited steps.
	dir, es = chain(t, s1)
	_, entryPath = (&Log{Dir: dir}).Paths(es[0].ID)
	data, _ := os.ReadFile(entryPath)
	os.WriteFile(entryPath, []byte(strings.Replace(string(data), "SELECT a;", "DROP TABLE x;", 1)), 0o644)
	if _, err := Load(dir); code(err) != "DPG-E046" || !strings.Contains(err.Error(), "steps") {
		t.Errorf("edited steps: %v", err)
	}

	// A safety or a stage of a newer dpg.
	for _, st := range []Step{
		{SQL: "SELECT 1;", Safety: "BENIGN", Stage: StageTransaction},
		{SQL: "SELECT 1;", Safety: "", Stage: StageTransaction},
		{SQL: "SELECT 1;", Safety: "SAFE", Stage: "eventually"},
	} {
		dir := t.TempDir()
		e, err := New("", []Step{st}, OriginApply, map[string]json.RawMessage{}, s1, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := (&Log{Dir: dir}).Write(e, "-- x\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); code(err) != "DPG-E046" || !strings.Contains(err.Error(), "which this dpg doesn't know") {
			t.Errorf("step %+v: %v", st, err)
		}
	}
	if got := (stepOp{s: Step{Safety: "BENIGN"}}).Safety(); got != pipeline.Destructive {
		t.Errorf("an unknown safety is %s", got)
	}

	// Missing SQL.
	dir, es = chain(t, s1)
	sqlPath, _ = (&Log{Dir: dir}).Paths(es[0].ID)
	os.Remove(sqlPath)
	if _, err := Load(dir); code(err) != "DPG-E046" {
		t.Errorf("missing SQL: %v", err)
	}

	// An edited diff: replay catches it.
	dir, es = chain(t, s1, s2)
	_, entryPath = (&Log{Dir: dir}).Paths(es[0].ID)
	data, _ = os.ReadFile(entryPath)
	os.WriteFile(entryPath, []byte(strings.Replace(string(data), `"a": 1`, `"a": 5`, 1)), 0o644)
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Replay(""); code(err) != "DPG-E046" || !strings.Contains(err.Error(), es[0].ID) {
		t.Errorf("edited diff: %v", err)
	}

	// Archives from before the log (no .json) are ignored.
	dir, _ = chain(t, s1)
	os.WriteFile(filepath.Join(dir, "20250101T000000Z.sql"), []byte("old"), 0o644)
	if l, err := Load(dir); err != nil || len(l.Entries) != 1 {
		t.Errorf("legacy files: %v", err)
	}

	// No log: empty is fine; a snapshot with objects needs a baseline.
	empty := &Log{Dir: t.TempDir()}
	if err := empty.Check("", nil); err != nil {
		t.Errorf("empty: %v", err)
	}
	if err := empty.Check("", s1); !errors.Is(err, ErrNoBaseline) {
		t.Errorf("no baseline: %v", err)
	}
	if l, err := Load(filepath.Join(t.TempDir(), "absent")); err != nil || len(l.Entries) != 0 {
		t.Errorf("absent dir: %v", err)
	}
}

func TestChainAndRemove(t *testing.T) {
	s1, s2 := objs("a", `1`), objs("a", `2`)
	dir, es := chain(t, s1, s2)
	other, _ := New(es[0].ID, []Step{{SQL: "SELECT 9;", Safety: "SAFE", Stage: StageTransaction}}, OriginRecord, s1, objs("a", `9`), t0.Add(time.Hour))
	if err := (&Log{Dir: dir}).Write(other, "-- other\n"); err != nil {
		t.Fatal(err)
	}
	all, err := LoadEntries(dir)
	if err != nil || len(all) != 3 {
		t.Fatalf("entries: %d %v", len(all), err)
	}
	c, err := Chain(all, other.ID)
	if err != nil || len(c) != 2 || c[0].ID != es[0].ID || c[1].ID != other.ID {
		t.Fatalf("chain: %v %v", c, err)
	}
	if _, err := Chain(all, "nope"); err == nil {
		t.Error("an unknown id must fail")
	}
	if err := Remove(dir, es[1].ID); err != nil {
		t.Fatal(err)
	}
	l, err := Load(dir)
	if err != nil || l.Head() != other.ID {
		t.Errorf("after removing the other branch: %v %v", l, err)
	}
	if err := Remove(dir, "absent"); err != nil {
		t.Errorf("removing an absent entry: %v", err)
	}
}

func TestAMigrationsStatementsKnowTheirSteps(t *testing.T) {
	target := &pipeline.Target{Kind: "table", Key: "public.t"}
	e := &Entry{ID: "m", Steps: []Step{
		{SQL: "CHECK", Safety: "SAFE", Stage: StageCheck},
		{SQL: "ALTER", Safety: "SAFE", Stage: StageTransaction, Target: target},
		{SQL: "CREATE INDEX", Safety: "CAUTION", Stage: StageAfter, Target: target},
	}}
	m := e.Migration(pipeline.MigrationMeta{}, false, true)
	got := map[string][]string{}
	for _, op := range allOps(m) {
		id, ok := op.(pipeline.IdentifiedOp)
		if !ok {
			t.Fatalf("%s isn't an IdentifiedOp", op.SQL())
		}
		got[op.SQL()] = append(got[op.SQL()], id.StepID())
	}
	want := map[string][]string{"CHECK": {"m/1", "m/1"}, "ALTER": {"m/2"}, "CREATE INDEX": {"m/3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("step IDs %v, want %v", got, want)
	}
	if tg := m.NonTransactional[0].(interface{ StepTarget() *pipeline.Target }).StepTarget(); tg != target {
		t.Errorf("target %v", tg)
	}
}

func allOps(m pipeline.Migration) []pipeline.DiffOp {
	return append(append(append([]pipeline.DiffOp(nil), m.PreTransactional...), m.Transactional...), m.NonTransactional...)
}
