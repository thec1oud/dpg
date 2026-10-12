package drift

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyAcks(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	item := Item{ID: "role:r/connection_limit", Group: Drift, Kind: "role", Object: "r", Property: "connection_limit", Change: "changed", Snapshot: "20", Live: "50"}
	ack := Acknowledge(item, "load test", "dan", now, nil)

	r := &Report{Items: []Item{item}}
	if unused := ApplyAcks(r, []Ack{ack}, now); len(unused) != 0 || r.Items[0].Acknowledged == nil || r.HasDrift() {
		t.Fatalf("acknowledged item must not fail: %+v", r.Items[0])
	}

	changed := item
	changed.Live = "60" // the database changed again
	r = &Report{Items: []Item{changed}}
	ApplyAcks(r, []Ack{ack}, now)
	if r.Items[0].Acknowledged != nil || r.Items[0].Lapsed == nil || !r.HasDrift() {
		t.Errorf("a changed value must lapse the acknowledgement: %+v", r.Items[0])
	}

	sourceChanged := item
	sourceChanged.Source = "30"
	r = &Report{Items: []Item{sourceChanged}}
	ApplyAcks(r, []Ack{ack}, now)
	if r.Items[0].Lapsed == nil {
		t.Error("a changed source value must lapse the acknowledgement")
	}

	past := now.Add(-time.Hour)
	expired := Acknowledge(item, "", "", now.Add(-48*time.Hour), &past)
	r = &Report{Items: []Item{item}}
	ApplyAcks(r, []Ack{expired}, now)
	if r.Items[0].Lapsed == nil || !r.HasDrift() {
		t.Error("an expired acknowledgement must lapse")
	}

	r = &Report{}
	if unused := ApplyAcks(r, []Ack{ack}, now); len(unused) != 1 {
		t.Error("an acknowledgement matching no item is unused")
	}
}

func TestAckFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acknowledged", "prod", "app.json")
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	exp := at.Add(7 * 24 * time.Hour)
	acks := []Ack{
		{ID: "b", Fingerprint: "f2", At: at},
		{ID: "a", Fingerprint: "f1", Reason: "why", By: "dan", At: at, Expires: &exp},
	}
	if err := SaveAcks(path, acks); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAcks(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[0].Expires == nil || !got[0].Expires.Equal(exp) || got[1].ID != "b" {
		t.Errorf("got %+v", got)
	}
	if err := SaveAcks(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("saving no acknowledgements must remove the file")
	}
	if got, err := LoadAcks(path); err != nil || got != nil {
		t.Errorf("missing file: %v %v", got, err)
	}
}
