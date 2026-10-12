package migrationlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		e, err := New(l.Head(), sql, OriginApply, before, after, t0.Add(time.Duration(i)*time.Second))
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
	e, err := New("p", "SELECT 1;", OriginApply, before, after, t0)
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
	other, _ := New(es[0].ID, "-- other branch\n", OriginApply, s1, objs("a", `3`), t0.Add(time.Hour))
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
	data, _ := os.ReadFile(entryPath)
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
