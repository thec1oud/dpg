package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/thec1oud/dpg/internal/drift"
)

func TestObjectDifferences(t *testing.T) {
	got := objectDifferences(`{"default":"'new'","name":"status","type":"text"}`, `{"name":"status","storage":"EXTENDED","type":"varchar"}`)
	if got != `default: "'new'" → (none); type: "text" → "varchar"` {
		t.Errorf("got %q", got)
	}
	if objectDifferences(`20`, `50`) != "" {
		t.Error("scalars are not objects")
	}
}

// TestRenderVerifyText: groups appear in order with their items, and the
// summary counts them.
func TestRenderVerifyText(t *testing.T) {
	targets := []verifyTarget{
		{Cluster: "prod", Database: "(cluster)", Report: &drift.Report{}},
		{Cluster: "prod", Database: "app", Report: &drift.Report{Items: []drift.Item{
			{ID: "table:public.t/comment", Group: drift.Drift, Change: "changed", Snapshot: `"a"`, Live: `"b"`, SQL: []string{"COMMENT ON TABLE t IS 'a';"}},
			{ID: "role:x/comment", Group: drift.Conflict, Change: "changed", Snapshot: `"a"`, Live: `"c"`},
			{ID: "table:public.u", Group: drift.Unmanaged, Change: "extra"},
		}}},
	}
	var b bytes.Buffer
	renderVerifyText(&b, targets, false)
	out := b.String()
	for _, want := range []string{
		"prod (cluster)  no drift detected",
		"prod/app\n  drift\n    table:public.t/comment  snapshot \"a\", database \"b\"\n      COMMENT ON TABLE t IS 'a';",
		"  conflicts",
		"  unmanaged\n    table:public.u  exists in the database but is not in the snapshot",
		"1 drift, 1 conflict, 1 unmanaged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "drift\n") > strings.Index(out, "conflicts") {
		t.Error("drift must come before conflicts")
	}
}

func TestRenderVerifyJSON(t *testing.T) {
	var b bytes.Buffer
	err := renderVerifyJSON(&b, []verifyTarget{{Cluster: "c", Database: "d", Report: &drift.Report{Items: []drift.Item{
		{ID: "role:r/comment", Group: drift.Drift, Kind: "role", Object: "r", Property: "comment", Change: "changed"},
	}}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"drift": true`, `"id": "role:r/comment"`, `"group": "drift"`, `"database": "d"`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %s in %s", want, b.String())
		}
	}
}

// TestRenderVerifyAcknowledged: an acknowledged item is listed apart and
// doesn't count as drift; a lapsed acknowledgement is flagged on the item.
func TestRenderVerifyAcknowledged(t *testing.T) {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	ack := drift.Ack{ID: "role:r/comment", By: "dan", At: at, Reason: "migration in progress"}
	lapsed := drift.Ack{ID: "role:r/owner", At: at}
	targets := []verifyTarget{{Cluster: "prod", Database: "(cluster)", Report: &drift.Report{Items: []drift.Item{
		{ID: "role:r/comment", Group: drift.Drift, Change: "changed", Snapshot: `"a"`, Live: `"b"`, Acknowledged: &ack},
		{ID: "role:r/owner", Group: drift.Drift, Change: "changed", Snapshot: `"x"`, Live: `"y"`, Lapsed: &lapsed},
	}}}}
	var b bytes.Buffer
	renderVerifyText(&b, targets, false)
	out := b.String()
	for _, want := range []string{
		"  drift\n    role:r/owner",
		"acknowledgement lapsed",
		"  acknowledged\n    role:r/comment",
		"acknowledged drift by dan on 2026-10-12: migration in progress",
		"1 drift, 1 acknowledged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !targets[0].Report.HasDrift() {
		t.Error("the lapsed item still fails")
	}
}
