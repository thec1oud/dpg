package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/pipeline"
)

func testTarget(t *testing.T, items ...drift.Item) *Target {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "dpg.toml")
	if err := os.WriteFile(cfg, []byte("kind = \"cluster\"\n\n[cluster]\nname = \"prod\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range items {
		items[i].ID = drift.ItemID(items[i].Kind, items[i].Object, items[i].Property)
	}
	saved := &pipeline.Snapshot{}
	return &Target{
		Cluster: "prod", Database: "app",
		Report:       &drift.Report{Items: items},
		Snapshot:     &pipeline.Snapshot{},
		SaveSnapshot: func(s *pipeline.Snapshot) error { *saved = *s; return nil },
		ConfigPath:   cfg,
		AckPath:      filepath.Join(dir, ".dpg", "acknowledged", "prod", "app.json"),
	}
}

var now = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func TestPrepareAcknowledgeIgnoreUnacknowledge(t *testing.T) {
	drifted := drift.Item{Group: drift.Drift, Kind: "role", Object: "r", Property: "comment", Change: "changed", Snapshot: `"a"`, Live: `"b"`}
	unmanaged := drift.Item{Group: drift.Unmanaged, Kind: "table", Object: "cron.job", Change: "extra"}
	tg := testTarget(t, drifted, unmanaged)

	plan, err := Prepare([]*Target{tg}, []Decision{
		{Target: tg.Label(), ItemID: tg.Report.Items[0].ID, Action: Acknowledge, Reason: "known", By: "dan"},
		{Target: tg.Label(), ItemID: tg.Report.Items[1].ID, Action: Ignore},
	}, now, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Problems) != 0 || len(plan.Acks) != 1 || len(plan.Files) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	if _, err := os.Stat(tg.AckPath); !os.IsNotExist(err) {
		t.Fatal("Prepare must not write anything")
	}
	if err := Execute(plan, nil); err != nil {
		t.Fatal(err)
	}
	acks, err := drift.LoadAcks(tg.AckPath)
	if err != nil || len(acks) != 1 || acks[0].Reason != "known" || acks[0].By != "dan" {
		t.Fatalf("acks: %+v %v", acks, err)
	}
	cfg, err := config.LoadCluster(tg.ConfigPath)
	if err != nil || len(cfg.Ignore.Tables) != 1 || cfg.Ignore.Tables[0] != "cron.job" {
		t.Fatalf("ignore: %+v %v", cfg.Ignore, err)
	}

	// The acknowledgement now applies; unacknowledging removes it.
	drift.ApplyAcks(tg.Report, acks, now)
	plan, err = Prepare([]*Target{tg}, []Decision{{Target: tg.Label(), ItemID: tg.Report.Items[0].ID, Action: Unacknowledge}}, now, Deps{})
	if err != nil || len(plan.Problems) != 0 {
		t.Fatalf("unack plan: %+v %v", plan, err)
	}
	if err := Execute(plan, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tg.AckPath); !os.IsNotExist(err) {
		t.Error("removing the last acknowledgement must remove the file")
	}
}

func TestPrepareRejectsDisallowedActions(t *testing.T) {
	tg := testTarget(t,
		drift.Item{Group: drift.Unmanaged, Kind: "table", Object: "public.x", Change: "extra"},
		drift.Item{Group: drift.Drift, Kind: "role", Object: "r", Property: "comment", Change: "changed"},
	)
	plan, err := Prepare([]*Target{tg}, []Decision{
		{Target: tg.Label(), ItemID: tg.Report.Items[0].ID, Action: Revert}, // never for unmanaged
		{Target: tg.Label(), ItemID: tg.Report.Items[1].ID, Action: Ignore}, // drift can't be ignored
		{Target: tg.Label(), ItemID: tg.Report.Items[1].ID, Action: Revert}, // no SQL to revert with
		{Target: tg.Label(), ItemID: "role:nope", Action: Adopt},
		{Target: "other/db", ItemID: "x", Action: Adopt},
	}, now, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Problems) != 5 || !plan.Empty() {
		t.Fatalf("expected 5 problems and an empty plan, got %+v", plan)
	}
}

func TestExecuteRollsBackOnFailedCheck(t *testing.T) {
	tg := testTarget(t, drift.Item{Group: drift.Unmanaged, Kind: "role", Object: "rdsadmin", Change: "extra"},
		drift.Item{Group: drift.Drift, Kind: "role", Object: "r", Property: "comment", Change: "changed"})
	before, _ := os.ReadFile(tg.ConfigPath)
	plan, err := Prepare([]*Target{tg}, []Decision{
		{Target: tg.Label(), ItemID: tg.Report.Items[0].ID, Action: Ignore},
		{Target: tg.Label(), ItemID: tg.Report.Items[1].ID, Action: Acknowledge},
	}, now, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(plan, func() error { return errors.New("still drifting") })
	if err == nil || !strings.Contains(err.Error(), "undone") {
		t.Fatalf("expected a rollback error, got %v", err)
	}
	after, _ := os.ReadFile(tg.ConfigPath)
	if string(after) != string(before) {
		t.Errorf("config not restored:\n%s", after)
	}
	if _, err := os.Stat(tg.AckPath); !os.IsNotExist(err) {
		t.Error("acknowledgement file not removed on rollback")
	}
}

func TestActions(t *testing.T) {
	ack := drift.Ack{}
	cases := []struct {
		it   drift.Item
		want string
	}{
		{drift.Item{Group: drift.Drift, SQL: []string{"x"}}, "adopt revert acknowledge"},
		{drift.Item{Group: drift.Drift}, "adopt acknowledge"},
		{drift.Item{Group: drift.Conflict, SQL: []string{"x"}}, "adopt revert acknowledge"},
		{drift.Item{Group: drift.Converged}, "adopt acknowledge"},
		{drift.Item{Group: drift.Unmanaged}, "adopt ignore acknowledge"},
		{drift.Item{Group: drift.Warning, Kind: "role"}, "adopt ignore acknowledge"},
		{drift.Item{Group: drift.Warning, Property: "grant:SELECT:x"}, "adopt acknowledge"},
		{drift.Item{Group: drift.Drift, Acknowledged: &ack}, "unacknowledge"},
	}
	for _, c := range cases {
		var got []string
		for _, a := range Actions(c.it) {
			got = append(got, string(a))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%+v: got %q, want %q", c.it, got, c.want)
		}
	}
}
