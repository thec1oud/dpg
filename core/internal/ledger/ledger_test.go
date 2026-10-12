package ledger

import (
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func TestHead(t *testing.T) {
	st := &State{Rows: map[string]Row{
		"a": {ID: "a", Parent: "", Status: Complete},
		"b": {ID: "b", Parent: "a", Status: Complete},
		"c": {ID: "c", Parent: "b", Status: Started},
	}}
	if h, err := st.Head(); h != "b" || err != nil {
		t.Errorf("head %q %v", h, err)
	}
	if u := st.Unfinished(); u == nil || u.ID != "c" {
		t.Errorf("unfinished %+v", u)
	}
	st.Rows["x"] = Row{ID: "x", Parent: "a", Status: Complete}
	if _, err := st.Head(); err == nil || !strings.Contains(err.Error(), "2 last migrations") {
		t.Errorf("two heads: %v", err)
	}
	if h, err := (&State{Rows: map[string]Row{}}).Head(); h != "" || err != nil {
		t.Errorf("empty: %q %v", h, err)
	}
}

type stmt struct {
	sql  string
	mode pipeline.ExecMode
}

func (s stmt) SQL() string             { return s.sql }
func (s stmt) Safety() pipeline.Safety { return pipeline.Safe }
func (s stmt) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (s stmt) Mode() pipeline.ExecMode { return s.mode }

func TestWrap(t *testing.T) {
	l := Ledger{Schema: "dpg", Version: "v"}
	m := Migration{Log: "app", ID: "id1", Parent: "id0", Checksum: "c"}
	sqls := func(ops []pipeline.DiffOp) string {
		var out []string
		for _, o := range ops {
			out = append(out, strings.SplitN(o.SQL(), "\n", 2)[0])
		}
		return strings.Join(out, " | ")
	}
	w := l.Wrap(pipeline.Migration{
		PreTransactional: []pipeline.DiffOp{stmt{"ALTER TYPE e ADD VALUE IF NOT EXISTS 'x';", pipeline.ModePreTransactional}},
		Transactional:    []pipeline.DiffOp{stmt{"ALTER TABLE t ADD COLUMN c int;", pipeline.ModeTransactional}},
	}, m)
	if got := sqls(w.PreTransactional); got != `CREATE SCHEMA IF NOT EXISTS "dpg"; | CREATE TABLE IF NOT EXISTS "dpg".migrations ( | DO $dpg$ | ALTER TYPE e ADD VALUE IF NOT EXISTS 'x';` {
		t.Errorf("before: %s", got)
	}
	if got := sqls(w.Transactional); !strings.HasPrefix(got, "DO $dpg$ | ALTER TABLE t ADD COLUMN c int; | UPDATE \"dpg\".migrations SET status = 'complete'") {
		t.Errorf("transaction: %s", got)
	}
	if len(w.NonTransactional) != 0 {
		t.Errorf("after: %v", w.NonTransactional)
	}
	// Steps after the transaction: committed in it, complete after them.
	w = l.Wrap(pipeline.Migration{
		NonTransactional: []pipeline.DiffOp{stmt{"CREATE INDEX CONCURRENTLY i ON t (c);", pipeline.ModeNonTransactional}},
	}, m)
	if got := sqls(w.Transactional); !strings.Contains(got, "status = 'committed'") {
		t.Errorf("transaction: %s", got)
	}
	if got := sqls(w.NonTransactional); !strings.HasSuffix(got, "status = 'complete', finished_at = now() WHERE log = 'app' AND id = 'id1';") {
		t.Errorf("after: %s", got)
	}
	// Names are quoted.
	if s := (Ledger{Schema: `we"ird`}).Start(Migration{Log: "o'k", ID: "i"}); !strings.Contains(s, `"we""ird".migrations`) || !strings.Contains(s, `'o''k'`) {
		t.Errorf("quoting: %s", s)
	}
}
