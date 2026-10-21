package apply

import "testing"

// TestNormalizeLinterRuleKeysSnakeToKebab guards RFC audit item #17: the
// RFC documents/exemplifies snake_case rule IDs
// ("security_definer_search_path", Section 19.2's own worked example) while the
// actual code emits kebab-case ("security-definer-search-path"), and
// ApplyRuleSeverityOverrides does an exact-string lookup — so an
// unnormalized snake_case config key silently matched nothing.
func TestNormalizeLinterRuleKeysSnakeToKebab(t *testing.T) {
	got := normalizeLinterRuleKeys(map[string]string{
		"security_definer_search_path": "error",
		"already-kebab-case":           "off",
	})
	want := map[string]string{
		"security-definer-search-path": "error",
		"already-kebab-case":           "off",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q (full map: %v)", k, got[k], v, got)
		}
	}
}
