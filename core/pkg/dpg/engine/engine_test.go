package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/migrationlog"
)

// projectOf writes a project of the given files beside a root, cluster and
// database configuration.
func projectOf(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{
		"dpg.toml":       "kind = \"root\"\n",
		"c/dpg.toml":     "kind = \"cluster\"\n\n[cluster]\nname = \"c\"\n",
		"c/app/dpg.toml": "kind = \"database\"\n\n[database]\nname = \"app\"\n",
	}
	for k, v := range files {
		all[k] = v
	}
	for name, content := range all {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestOpenRefusesASourceTheLogDoesntRecord(t *testing.T) {
	root := projectOf(t, map[string]string{"c/app/t.dpg": "TABLE t (id int PRIMARY KEY);\n"})
	_, err := Open(Config{Dir: root})
	if err == nil || !strings.Contains(err.Error(), "DPG-E056") {
		t.Fatalf("open: %v", err)
	}
	if strings.Contains(err.Error(), "CREATE") {
		t.Errorf("the error quotes SQL: %v", err)
	}
}

func TestOpenNeedsTheLedger(t *testing.T) {
	root := projectOf(t, map[string]string{"dpg.toml": "kind = \"root\"\n\n[ledger]\nenabled = false\n"})
	if _, err := Open(Config{Dir: root}); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("open with the ledger off: %v", err)
	}
}

func TestOpenNeedsTheDatabaseNamedWhenThereAreSeveral(t *testing.T) {
	root := projectOf(t, map[string]string{"c/other/dpg.toml": "kind = \"database\"\n\n[database]\nname = \"other\"\n"})
	if _, err := Open(Config{Dir: root}); err == nil {
		t.Fatal("open picked one of two databases")
	}
	if _, err := Open(Config{Dir: root, Database: "missing"}); err == nil {
		t.Fatal("open found a database that doesn't exist")
	}
	e, err := Open(Config{Dir: root, Cluster: "c", Database: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if e.LedgerSchema() != "dpg" {
		t.Errorf("the ledger's schema is %q", e.LedgerSchema())
	}
}

func TestAnEngineWithoutAConnectionOnlyPlansOffline(t *testing.T) {
	root := projectOf(t, nil)
	e, err := Open(Config{Dir: filepath.Join(root, "c", "app")})
	if err != nil {
		t.Fatal(err)
	}
	if e.Root() != root {
		t.Errorf("root %q, want %q", e.Root(), root)
	}
	ctx := context.Background()
	if _, err := e.Status(ctx); !errors.Is(err, ErrNoConnection) {
		t.Errorf("status: %v", err)
	}
	if _, err := e.Plan(ctx); !errors.Is(err, ErrNoConnection) {
		t.Errorf("plan: %v", err)
	}
	if _, err := e.Apply(ctx, ApplyOptions{}); !errors.Is(err, ErrNoConnection) {
		t.Errorf("apply: %v", err)
	}
	if _, err := e.Verify(ctx); !errors.Is(err, ErrNoConnection) {
		t.Errorf("verify: %v", err)
	}
	if p := e.PlanOffline(); len(p.Steps) != 0 || p.MaxSafety() != Safe {
		t.Errorf("the offline plan of an empty log: %+v", p)
	}
	if err := e.Close(ctx); err != nil {
		t.Error(err)
	}
}

func TestNamesOfStatesSafetiesAndModes(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{UpToDate.String(), "up to date"}, {Pending.String(), "pending"}, {Drifted.String(), "drifted"}, {Ahead.String(), "ahead"}, {State(0).String(), "unknown"},
		{Safe.String(), "SAFE"}, {Caution.String(), "CAUTION"}, {Destructive.String(), "DESTRUCTIVE"}, {UnknownSafety.String(), "unknown"}, {Safety(-1).String(), "unknown"},
		{InTransaction.String(), "transaction"}, {BeforeTransaction.String(), "before"}, {AfterTransaction.String(), "after"}, {Instruction.String(), "instruction"}, {UnknownMode.String(), "unknown"}, {Mode(9).String(), "unknown"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
}

func TestARefusalNamesItsStepsNotTheirSQL(t *testing.T) {
	err := &RefusedError{Steps: []Step{{ID: "m/2", Safety: Destructive, SQL: "DROP TABLE t;"}}, Reason: "above the maximum safety, CAUTION"}
	if got := err.Error(); got != "engine: refused m/2 (DESTRUCTIVE): above the maximum safety, CAUTION; nothing was applied" {
		t.Errorf("%q", got)
	}
}

// A safety or stage the log records that the engine doesn't know is never
// taken for Safe or for a statement in the transaction.
func TestStepsOfMarksWhatItDoesntKnow(t *testing.T) {
	en := &migrationlog.Entry{ID: "m", Steps: []migrationlog.Step{
		{SQL: "SELECT 1", Safety: "SAFE", Stage: migrationlog.StageCheck},
		{SQL: "CREATE TABLE a ();", Safety: "SAFE", Stage: migrationlog.StageTransaction},
		{SQL: "ALTER TABLE a ...;", Safety: "CAUTION", Stage: migrationlog.StageBefore},
		{SQL: "DROP TABLE b;", Safety: "DESTRUCTIVE", Stage: migrationlog.StageAfter},
		{SQL: "-- rebuild", Safety: "DESTRUCTIVE", Stage: migrationlog.StageInstruction},
		{SQL: "x", Safety: "", Stage: migrationlog.StageTransaction},
		{SQL: "y", Safety: "safe", Stage: migrationlog.StageTransaction},
		{SQL: "z", Safety: "SAFE", Stage: "later"},
		{SQL: "w", Safety: "SAFE", Stage: ""},
	}}
	want := []Step{
		{ID: "m/2", Migration: "m", Safety: Safe, Mode: InTransaction, SQL: "CREATE TABLE a ();"},
		{ID: "m/3", Migration: "m", Safety: Caution, Mode: BeforeTransaction, SQL: "ALTER TABLE a ...;"},
		{ID: "m/4", Migration: "m", Safety: Destructive, Mode: AfterTransaction, SQL: "DROP TABLE b;"},
		{ID: "m/5", Migration: "m", Safety: Destructive, Mode: Instruction, SQL: "-- rebuild"},
		{ID: "m/6", Migration: "m", Safety: UnknownSafety, Mode: InTransaction, SQL: "x"},
		{ID: "m/7", Migration: "m", Safety: UnknownSafety, Mode: InTransaction, SQL: "y"},
		{ID: "m/8", Migration: "m", Safety: Safe, Mode: UnknownMode, SQL: "z"},
		{ID: "m/9", Migration: "m", Safety: Safe, Mode: UnknownMode, SQL: "w"},
	}
	got := stepsOf(en, false)
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if after := stepsOf(en, true); len(after) != 1 || after[0].ID != "m/4" {
		t.Errorf("the steps after the transaction: %+v", after)
	}
	if max := (Plan{Steps: got}).MaxSafety(); max != UnknownSafety {
		t.Errorf("max safety %s", max)
	}
	if max := (Plan{Steps: []Step{{Safety: Safe}, {Safety: Safe, Mode: UnknownMode}}}).MaxSafety(); max != UnknownSafety {
		t.Errorf("max safety with an unknown stage %s", max)
	}
	if max := (Plan{Steps: []Step{{Safety: Destructive, Mode: Instruction}, {Safety: Caution}}}).MaxSafety(); max != Caution {
		t.Errorf("max safety beside an instruction %s", max)
	}
}
