// Package ledger is the table a database records the migrations it has run
// in (RFC Section 16.9): the SQL that keeps it, wrapped around every
// migration so any runner records it, and reading it back.
package ledger

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/migrationlog"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/precondition"
)

// Statuses of a ledger row.
const (
	Started   = "started"   // guard passed; the transaction hasn't committed
	Committed = "committed" // the transaction committed; steps after it haven't finished
	Complete  = "complete"
)

// Migration identifies a migration in a ledger.
type Migration struct {
	Log      string // the database's name, or "_cluster"
	ID       string
	Parent   string
	Checksum string // the migration's steps checksum
	// Objects are what its steps change; their fingerprints are recorded
	// when it has run (RFC Section 16.10).
	Objects []pipeline.Target
}

// Ledger is where a database's ledger lives.
type Ledger struct {
	Schema  string
	Version string // the dpg version recorded with each row
}

func (l Ledger) table() string { return quoteIdent(l.Schema) + ".migrations" }

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteLit(s string) string   { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Bootstrap creates the ledger when it doesn't exist.
func (l Ledger) Bootstrap() []string {
	return []string{
		"CREATE SCHEMA IF NOT EXISTS " + quoteIdent(l.Schema) + ";",
		"CREATE TABLE IF NOT EXISTS " + l.table() + ` (
    log         text NOT NULL,
    id          text NOT NULL,
    parent      text NOT NULL,
    checksum    text NOT NULL,
    dpg_version text NOT NULL,
    status      text NOT NULL CHECK (status IN ('started', 'committed', 'complete')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    PRIMARY KEY (log, id)
);`,
		"CREATE TABLE IF NOT EXISTS " + quoteIdent(l.Schema) + `.objects (
    log         text NOT NULL,
    kind        text NOT NULL,
    key         text NOT NULL,
    fingerprint text NOT NULL,
    pg_major    int NOT NULL,
    migration   text NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (log, kind, key)
);`,
	}
}

// headCheck is PL/pgSQL that raises unless m's parent is the log's last
// complete migration (its head: a complete row no complete row follows).
func (l Ledger) headCheck(m Migration) string {
	t := l.table()
	parent := "the start of the log"
	if m.Parent != "" {
		parent = m.Parent
	}
	return fmt.Sprintf(`  SELECT count(*), min(m.id) INTO n, head FROM %[1]s m
    WHERE m.log = %[2]s AND m.status = 'complete'
      AND NOT EXISTS (SELECT 1 FROM %[1]s c WHERE c.log = m.log AND c.parent = m.id AND c.status = 'complete');
  IF n > 1 THEN
    RAISE EXCEPTION 'DPG-E050: the ledger of %% has %% last migrations', %[2]s, n;
  END IF;
  IF coalesce(head, '') <> %[3]s THEN
    RAISE EXCEPTION 'DPG-E050: migration %% follows %%, but the last migration this database ran is %%', %[4]s, %[5]s, coalesce(head, '(none)');
  END IF;
`, t, quoteLit(m.Log), quoteLit(m.Parent), quoteLit(m.ID), quoteLit(parent))
}

// Start checks the ledger before a migration runs and records it as
// started: it raises when the migration was recorded with other steps, has
// already been applied, failed after its transaction committed, or doesn't
// follow this database's last migration. A started row (a failed earlier
// attempt) is taken over.
func (l Ledger) Start(m Migration) string {
	t := l.table()
	return fmt.Sprintf(`DO $dpg$
DECLARE
  r record;
  n int;
  head text;
BEGIN
  SELECT status, checksum INTO r FROM %[1]s WHERE log = %[2]s AND id = %[3]s;
  IF FOUND THEN
    IF r.checksum <> %[4]s THEN
      RAISE EXCEPTION 'DPG-E049: migration %% was recorded in this database with different statements', %[3]s;
    END IF;
    IF r.status = 'complete' THEN
      RAISE EXCEPTION 'DPG-E049: migration %% has already been applied to this database', %[3]s;
    END IF;
    IF r.status = 'committed' THEN
      RAISE EXCEPTION 'DPG-E049: migration %% failed after its transaction committed; dpg apply --resume finishes it', %[3]s;
    END IF;
  END IF;
%[5]s  INSERT INTO %[1]s (log, id, parent, checksum, dpg_version, status)
    VALUES (%[2]s, %[3]s, %[6]s, %[4]s, %[7]s, 'started')
    ON CONFLICT (log, id) DO UPDATE SET started_at = now();
END
$dpg$;`, t, quoteLit(m.Log), quoteLit(m.ID), quoteLit(m.Checksum), l.headCheck(m), quoteLit(m.Parent), quoteLit(l.Version))
}

// Claim runs first inside the migration's transaction: it locks the
// migration's row, which must still be started (a concurrent run that got
// there first leaves it committed or complete), and checks the head again.
func (l Ledger) Claim(m Migration) string {
	return fmt.Sprintf(`DO $dpg$
DECLARE
  n int;
  head text;
BEGIN
  PERFORM 1 FROM %[1]s WHERE log = %[2]s AND id = %[3]s AND status = 'started' AND checksum = %[4]s FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'DPG-E049: migration %% is not waiting to run in this database (another run applied it)', %[3]s;
  END IF;
%[5]sEND
$dpg$;`, l.table(), quoteLit(m.Log), quoteLit(m.ID), quoteLit(m.Checksum), l.headCheck(m))
}

// Finish records the migration's status at the end of its transaction:
// complete, or committed when steps run after the transaction.
func (l Ledger) Finish(m Migration, status string) string {
	finished := "NULL"
	if status == Complete {
		finished = "now()"
	}
	return fmt.Sprintf("UPDATE %s SET status = %s, finished_at = %s WHERE log = %s AND id = %s;",
		l.table(), quoteLit(status), finished, quoteLit(m.Log), quoteLit(m.ID))
}

// Record records a migration as complete without running it (a database
// that already has its state: the one it was adopted from, or a baseline),
// after the same head check.
func (l Ledger) Record(m Migration) string {
	return fmt.Sprintf(`DO $dpg$
DECLARE
  n int;
  head text;
BEGIN
%[5]s  INSERT INTO %[1]s (log, id, parent, checksum, dpg_version, status, finished_at)
    VALUES (%[2]s, %[3]s, %[6]s, %[4]s, %[7]s, 'complete', now());
END
$dpg$;`, l.table(), quoteLit(m.Log), quoteLit(m.ID), quoteLit(m.Checksum), l.headCheck(m), quoteLit(m.Parent), quoteLit(l.Version))
}

// op is a ledger statement in a migration.
type op struct {
	sql  string
	mode pipeline.ExecMode
}

func (o op) SQL() string             { return o.sql }
func (o op) Safety() pipeline.Safety { return pipeline.Safe }
func (o op) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (o op) Mode() pipeline.ExecMode { return o.mode }
func (o op) Target() pipeline.Target { return pipeline.Target{Kind: "ledger", Key: "ledger"} }

// Wrap surrounds a migration's statements with the ledger's: bootstrap and
// Start before everything, Claim first in the transaction and Finish last,
// and a final Finish after the steps that follow the transaction.
func (l Ledger) Wrap(mg pipeline.Migration, m Migration) pipeline.Migration {
	var pre []pipeline.DiffOp
	for _, s := range l.Bootstrap() {
		pre = append(pre, op{s, pipeline.ModePreTransactional})
	}
	pre = append(pre, op{l.Start(m), pipeline.ModePreTransactional})
	after := 0
	for _, o := range mg.NonTransactional {
		if o.Mode() != pipeline.ModeInstructionOnly {
			after++
		}
	}
	status := Complete
	if after > 0 {
		status = Committed
	}
	out := pipeline.Migration{Meta: mg.Meta}
	out.PreTransactional = append(pre, mg.PreTransactional...)
	out.Transactional = append([]pipeline.DiffOp{op{l.Claim(m), pipeline.ModeTransactional}}, mg.Transactional...)
	for _, s := range l.fingerprints(m) {
		out.Transactional = append(out.Transactional, op{s, pipeline.ModeTransactional})
	}
	out.Transactional = append(out.Transactional, op{l.Finish(m, status), pipeline.ModeTransactional})
	out.NonTransactional = mg.NonTransactional
	if after > 0 {
		out.NonTransactional = append([]pipeline.DiffOp(nil), mg.NonTransactional...)
		for _, s := range l.fingerprints(m) {
			out.NonTransactional = append(out.NonTransactional, op{s, pipeline.ModeNonTransactional})
		}
		out.NonTransactional = append(out.NonTransactional, op{l.Finish(m, Complete), pipeline.ModeNonTransactional})
	}
	return out
}

// fingerprints records the fingerprints of the objects a migration
// changed, and forgets the ones it dropped or renamed away.
func (l Ledger) fingerprints(m Migration) []string {
	fp := precondition.Fingerprints{Schema: l.Schema, Log: m.Log}
	var out []string
	for _, t := range m.Objects {
		if t.From != "" {
			out = append(out, fp.Forget(t.Kind, t.From))
		}
		if t.Effect == pipeline.EffectDrop {
			out = append(out, fp.Forget(t.Kind, t.Key))
			continue
		}
		if s := fp.Record(t.Kind, t.Key, m.ID); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Row is one ledger row.
type Row struct {
	ID, Parent, Checksum, Status string
}

// State is a log's rows in a database's ledger.
type State struct {
	Exists bool // the ledger table exists
	Rows   map[string]Row
}

// Head is the log's last complete migration ("" when none), and an error
// when several complete rows have no complete successor.
func (s *State) Head() (string, error) {
	children := map[string]bool{}
	for _, r := range s.Rows {
		if r.Status == Complete {
			children[r.Parent] = true
		}
	}
	var heads []string
	for _, r := range s.Rows {
		if r.Status == Complete && !children[r.ID] {
			heads = append(heads, r.ID)
		}
	}
	sort.Strings(heads)
	switch len(heads) {
	case 0:
		return "", nil
	case 1:
		return heads[0], nil
	}
	return "", fmt.Errorf("DPG-E050: the ledger has %d last migrations: %s", len(heads), strings.Join(heads, ", "))
}

// Unfinished is the log's row that is started or committed, if any.
func (s *State) Unfinished() *Row {
	var ids []string
	for id, r := range s.Rows {
		if r.Status != Complete {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	r := s.Rows[ids[0]]
	return &r
}

// Read reads a log's rows from a database's ledger.
func (l Ledger) Read(ctx context.Context, q pipeline.Querier, log string) (*State, error) {
	st := &State{Rows: map[string]Row{}}
	rows, err := q.QueryRows(ctx, `SELECT to_regclass($1) IS NOT NULL`, l.table())
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		if err := rows.Scan(&st.Exists); err != nil {
			rows.Close()
			return nil, err
		}
	}
	rows.Close()
	if !st.Exists {
		return st, nil
	}
	rows, err = q.QueryRows(ctx, `SELECT id, parent, checksum, status FROM `+l.table()+` WHERE log = $1`, log)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Parent, &r.Checksum, &r.Status); err != nil {
			return nil, err
		}
		st.Rows[r.ID] = r
	}
	return st, rows.Err()
}

// Of identifies a log entry in a ledger, with the objects its steps change.
func Of(log string, e *migrationlog.Entry) Migration {
	m := Migration{Log: log, ID: e.ID, Parent: e.Parent, Checksum: e.StepsChecksum}
	seen := map[string]bool{}
	for _, s := range e.Steps {
		if s.Target == nil || s.Target.Key == "" || s.Target.Kind == "ledger" {
			continue
		}
		id := s.Target.Kind + "\x00" + s.Target.Key + "\x00" + s.Target.From
		if !seen[id] {
			seen[id] = true
			m.Objects = append(m.Objects, *s.Target)
		}
	}
	return m
}

// Forget removes a migration's row if it has the given status.
func (l Ledger) Forget(m Migration, status string) string {
	return fmt.Sprintf("DELETE FROM %s WHERE log = %s AND id = %s AND status = %s;",
		l.table(), quoteLit(m.Log), quoteLit(m.ID), quoteLit(status))
}

// Finished runs the steps after a committed migration's transaction (dpg
// apply --resume): it checks the row is committed, runs them, and records
// the migration as complete.
func (l Ledger) Finished(mg pipeline.Migration, m Migration) pipeline.Migration {
	check := fmt.Sprintf(`DO $dpg$
BEGIN
  PERFORM 1 FROM %[1]s WHERE log = %[2]s AND id = %[3]s AND status = 'committed' AND checksum = %[4]s;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'DPG-E049: migration %% is not waiting to be finished in this database', %[3]s;
  END IF;
END
$dpg$;`, l.table(), quoteLit(m.Log), quoteLit(m.ID), quoteLit(m.Checksum))
	out := pipeline.Migration{Meta: mg.Meta}
	out.PreTransactional = []pipeline.DiffOp{op{check, pipeline.ModePreTransactional}}
	out.NonTransactional = append([]pipeline.DiffOp(nil), mg.NonTransactional...)
	for _, s := range l.fingerprints(m) {
		out.NonTransactional = append(out.NonTransactional, op{s, pipeline.ModeNonTransactional})
	}
	out.NonTransactional = append(out.NonTransactional, op{l.Finish(m, Complete), pipeline.ModeNonTransactional})
	return out
}
