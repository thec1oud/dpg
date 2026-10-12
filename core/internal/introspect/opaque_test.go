package introspect

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// ── parseDictInitOption ──────────────────────────────────────────────────────

// TestParseDictInitOption guards RFC audit item #52: pg_ts_dict.
// dictinitoption's packed format, confirmed live against a real snowball
// dictionary, must parse into ordered key/value pairs with quoting/
// escaping undone.
func TestParseDictInitOption(t *testing.T) {
	got := parseDictInitOption(`language = 'english', stopwords = 'english'`)
	want := []pipeline.StorageParam{{Key: "language", Value: "english"}, {Key: "stopwords", Value: "english"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestParseDictInitOptionEscapedQuote guards a doubled '' escaped single
// quote inside a value unquoting correctly.
func TestParseDictInitOptionEscapedQuote(t *testing.T) {
	got := parseDictInitOption(`dictfile = 'it''s_a_file'`)
	want := []pipeline.StorageParam{{Key: "dictfile", Value: "it's_a_file"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestParseDictInitOptionEmpty guards the empty-string input (a dictionary
// with no options at all) producing a nil/empty slice, not a spurious entry.
func TestParseDictInitOptionEmpty(t *testing.T) {
	if got := parseDictInitOption(""); len(got) != 0 {
		t.Errorf("expected no entries for empty input, got %v", got)
	}
}

// ── formatUserMappingOptions ────────────────────────────────────────────────

func TestFormatUserMappingOptionsRedactsPasswordLikeKeys(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"password", "password"},
		{"passwd", "passwd"},
		{"pwd", "pwd"},
		{"secret", "secret"},
		{"passphrase", "passphrase"},
		{"mixed case", "PassWord"},
		{"substring match", "db_password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatUserMappingOptions([]string{tc.key + "=hunter2"})
			if strings.Contains(got, "hunter2") {
				t.Errorf("formatUserMappingOptions(%q=...) leaked real value into %q", tc.key, got)
			}
			if !strings.Contains(got, UserMappingRedactedPlaceholder) {
				t.Errorf("formatUserMappingOptions(%q=...) = %q, want placeholder %q", tc.key, got, UserMappingRedactedPlaceholder)
			}
		})
	}
}

func TestFormatUserMappingOptionsLeavesNonSensitiveKeysUntouched(t *testing.T) {
	cases := []string{"user", "dbname", "host", "port", "sslmode"}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			got := formatUserMappingOptions([]string{key + "=some_value"})
			if !strings.Contains(got, "some_value") {
				t.Errorf("formatUserMappingOptions(%q=some_value) = %q, want real value preserved", key, got)
			}
			if strings.Contains(got, UserMappingRedactedPlaceholder) {
				t.Errorf("formatUserMappingOptions(%q=some_value) = %q, non-sensitive key should not be redacted", key, got)
			}
		})
	}
}

func TestFormatUserMappingOptionsMixed(t *testing.T) {
	got := formatUserMappingOptions([]string{"user=alice", "password=hunter2", "dbname=mydb"})
	if strings.Contains(got, "hunter2") {
		t.Errorf("formatUserMappingOptions leaked real password into %q", got)
	}
	if !strings.Contains(got, "alice") || !strings.Contains(got, "mydb") {
		t.Errorf("formatUserMappingOptions redacted a non-sensitive key: %q", got)
	}
	if !strings.Contains(got, UserMappingRedactedPlaceholder) {
		t.Errorf("formatUserMappingOptions did not redact password: %q", got)
	}
}

func TestFormatUserMappingOptionsEmpty(t *testing.T) {
	if got := formatUserMappingOptions(nil); got != "" {
		t.Errorf("formatUserMappingOptions(nil) = %q, want empty string", got)
	}
}

func TestUserMappingRedactedPlaceholderHasNoTemplateMarker(t *testing.T) {
	// The linter's hardcoded-fdw-password rule only fires when the literal
	// password value contains no "{{" — the placeholder must never contain
	// one, or a dumped-but-unmodified file would silently pass plan/apply
	// with a fake-but-still-hardcoded value instead of hard-erroring.
	if strings.Contains(UserMappingRedactedPlaceholder, "{{") {
		t.Fatalf("UserMappingRedactedPlaceholder contains %q; the hardcoded-fdw-password lint rule would no longer catch an un-replaced dump", "{{")
	}
}

// TestRecordSkipNoopWithoutSink proves recordSkip is a true no-op — verify
// and plan --live call introspection without ever installing a sink via
// WithSkipWarnings, and must never panic or allocate on their behalf.
func TestRecordSkipNoopWithoutSink(t *testing.T) {
	recordSkip(context.Background(), "collation %s: omitted", "public.x")
}

// TestRecordSkipAppendsToInstalledSink proves dump's WithSkipWarnings/
// recordSkip wiring (RFC Section 18.5) actually surfaces a human-readable
// message for every object introspection silently omits, instead of the
// prior fully-silent omission that gave the operator no way to know their
// dump was incomplete.
func TestRecordSkipAppendsToInstalledSink(t *testing.T) {
	var sink []string
	ctx := WithSkipWarnings(context.Background(), &sink)
	recordSkip(ctx, "collation %s: no reconstructable attributes — omitted, re-declare manually", "public.weird")
	recordSkip(ctx, "operator class %s: no reconstructable members — omitted, re-declare manually", "public.opc")
	if len(sink) != 2 {
		t.Fatalf("expected 2 recorded skips, got %d: %v", len(sink), sink)
	}
	if !strings.Contains(sink[0], "public.weird") || !strings.Contains(sink[1], "public.opc") {
		t.Errorf("recorded skip messages missing expected object names: %v", sink)
	}
}
