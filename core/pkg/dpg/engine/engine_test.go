package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	e, err := Open(Config{Dir: projectOf(t, nil)})
	if err != nil {
		t.Fatal(err)
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
		{Safe.String(), "SAFE"}, {Caution.String(), "CAUTION"}, {Destructive.String(), "DESTRUCTIVE"},
		{InTransaction.String(), "transaction"}, {BeforeTransaction.String(), "before"}, {AfterTransaction.String(), "after"}, {Instruction.String(), "instruction"}, {Mode(9).String(), "unknown"},
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
