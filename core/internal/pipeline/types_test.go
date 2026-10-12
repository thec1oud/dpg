package pipeline

import (
	"testing"
)

func TestSafetyString(t *testing.T) {
	cases := []struct {
		s    Safety
		want string
	}{
		{Safe, "SAFE"},
		{Caution, "CAUTION"},
		{Destructive, "DESTRUCTIVE"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Safety(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestSourcePosString(t *testing.T) {
	p := SourcePos{File: "schema.dpg", Line: 10, Col: 5}
	if got := p.String(); got != "schema.dpg:10:5" {
		t.Errorf("SourcePos.String() = %q", got)
	}

	empty := SourcePos{}
	if got := empty.String(); got != "<unknown>" {
		t.Errorf("empty SourcePos.String() = %q, want <unknown>", got)
	}
}

func TestSnapshotSetAndGet(t *testing.T) {
	snap := &Snapshot{}
	type item struct{ X int }
	if err := snap.SetObject("public.mytable", item{X: 42}); err != nil {
		t.Fatal(err)
	}

	var got item
	found, err := snap.GetObject("public.mytable", &got)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected GetObject to return true for stored key")
	}
	if got.X != 42 {
		t.Errorf("expected X=42, got %d", got.X)
	}
}

func TestSnapshotGetMissing(t *testing.T) {
	snap := &Snapshot{}
	var got struct{ X int }
	found, err := snap.GetObject("public.missing", &got)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected GetObject to return false for missing key")
	}
}

// TestErrorfNoCode guards Errorf's existing behavior — Code stays "" — so
// the hundreds of pre-existing call sites across the codebase that predate
// ErrorfCode are unaffected by its addition.
func TestErrorfNoCode(t *testing.T) {
	err := Errorf(SourcePos{File: "schema.dpg", Line: 1, Col: 1}, "something went wrong: %s", "reason")
	if err.Code != "" {
		t.Errorf("Code: got %q, want empty", err.Code)
	}
	if err.Message != "something went wrong: reason" {
		t.Errorf("Message: got %q", err.Message)
	}
}

// TestErrorfCode guards ErrorfCode's Code field, threaded through by
// documented call sites (RFC Appendix C) so cmd/dpg validate --format json
// can surface the real DPG-E0xx code instead of the generic DPG-E000
// fallback.
func TestErrorfCode(t *testing.T) {
	err := ErrorfCode(SourcePos{File: "schema.dpg", Line: 1, Col: 1}, "DPG-E021", "stale RENAMED FROM %q", "old_name")
	if err.Code != "DPG-E021" {
		t.Errorf("Code: got %q, want DPG-E021", err.Code)
	}
	if err.Message != `stale RENAMED FROM "old_name"` {
		t.Errorf("Message: got %q", err.Message)
	}
}
