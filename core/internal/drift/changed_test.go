package drift

import (
	"encoding/json"
	"testing"
)

func TestChangedPropertiesAndTouches(t *testing.T) {
	before := json.RawMessage(`{"kind":"table","table":{"name":"t","comment":"a","columns":[{"name":"x","type":"int"},{"name":"y","type":"int"}],"grants":[]}}`)
	after := json.RawMessage(`{"kind":"table","table":{"name":"t","comment":"b","columns":[{"name":"x","type":"int"},{"name":"y","type":"bigint"},{"name":"z","type":"int"}],"grants":[{"roles":["r"]}]}}`)
	got, err := ChangedProperties(before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"comment", "column:y", "column:z", "grants"} {
		if !got[p] {
			t.Errorf("%s not changed: %v", p, got)
		}
	}
	if got["column:x"] || got["name"] {
		t.Errorf("unchanged reported: %v", got)
	}
	cases := []struct {
		prop string
		want bool
	}{
		{"comment", true}, {"column:x", false}, {"column:y/default", true}, {"grant:SELECT:r", true},
		{"column:x/grant:SELECT:r", false}, {"owner", false}, {"", true},
	}
	for _, c := range cases {
		if Touches(Item{Property: c.prop}, got) != c.want {
			t.Errorf("Touches(%q) != %v", c.prop, c.want)
		}
	}
	whole, _ := ChangedProperties(nil, after)
	if !Touches(Item{Property: "owner"}, whole) {
		t.Error("a created object is changed as a whole")
	}
}
