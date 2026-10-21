package apply

import "testing"

func TestObjectDifferences(t *testing.T) {
	got := objectDifferences(`{"default":"'new'","name":"status","type":"text"}`, `{"name":"status","storage":"EXTENDED","type":"varchar"}`)
	if got != `default: "'new'" → (none); type: "text" → "varchar"` {
		t.Errorf("got %q", got)
	}
	if objectDifferences(`20`, `50`) != "" {
		t.Error("scalars are not objects")
	}
}
