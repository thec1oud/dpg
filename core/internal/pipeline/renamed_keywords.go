package pipeline

import "fmt"

// renamedKeywords maps block keywords that were renamed to match
// PostgreSQL's own spelling to their replacement, so an unknown-directive
// error can name the keyword to use instead.
var renamedKeywords = map[string]string{
	"INDICES":     "INDEXES",
	"REVOCATIONS": "REVOKES",
	"REVOCATION":  "REVOKE",
}

// RenamedKeywordHint returns a "; X was renamed to Y" suffix when word is a
// renamed keyword, or "" otherwise.
func RenamedKeywordHint(word string) string {
	if repl, ok := renamedKeywords[word]; ok {
		return fmt.Sprintf("; %s was renamed to %s", word, repl)
	}
	return ""
}
