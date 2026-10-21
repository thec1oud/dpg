package apply

import (
	"strings"

	"github.com/thec1oud/dpg/internal/config"
	"github.com/thec1oud/dpg/internal/pipeline"
)

// LinterConfig converts a config.LinterConfig (from dpg.toml) to the
// pipeline.LinterConfig used by Linter.Lint.
func LinterConfig(c config.LinterConfig) pipeline.LinterConfig {
	return pipeline.LinterConfig{
		WarnOnDeprecated:         c.WarnOnDeprecated,
		RequireColumnComments:    c.RequireColumnComments,
		ForbidHardcodedPasswords: c.ForbidHardcodedPasswords,
		MaxColumnsPerTable:       c.MaxColumnsPerTable,
		Rules:                    normalizeLinterRuleKeys(c.Rules),
	}
}

// normalizeLinterRuleKeys normalizes [linter.rules] TOML keys from
// snake_case to the actual kebab-case rule IDs (RFC audit item #17):
// Section 19.3 gives kebab-case (e.g. "security-definer-search-path") as
// authoritative, matching every real rule ID internal/linter/linter.go
// emits, but Section 19.2's own worked config example still shows the broken
// snake_case form ("security_definer_search_path = ...") as if it works.
// ApplyRuleSeverityOverrides does an exact-string lookup against the rule
// ID a diagnostic actually carries, so an unnormalized snake_case key
// never matched anything — silently no-oping the RFC's own documented
// example. No rule ID mixes hyphens and underscores (confirmed against
// Section 19.3's complete table), so a blind "_" -> "-" replacement can't
// collide two distinct keys into one.
func normalizeLinterRuleKeys(rules map[string]string) map[string]string {
	if len(rules) == 0 {
		return rules
	}
	out := make(map[string]string, len(rules))
	for k, v := range rules {
		out[strings.ReplaceAll(k, "_", "-")] = v
	}
	return out
}
