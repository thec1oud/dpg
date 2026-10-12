package resolve

import (
	"fmt"
	"strconv"
	"strings"
)

// ignoreKeys maps a snapshot kind to its [ignore] key (RFC Section 3.9).
var ignoreKeys = map[string]string{
	"role": "roles", "tablespace": "tablespaces", "schema": "schemas", "extension": "extensions",
	"table": "tables", "view": "views", "function": "functions", "procedure": "procedures",
	"aggregate": "aggregates", "type": "types", "sequence": "sequences", "collation": "collations",
	"operator": "operators", "operator_class": "operator_classes", "operator_family": "operator_families",
	"cast": "casts", "statistics": "statistics", "fdw": "foreign_data_wrappers", "server": "servers",
	"user_mapping": "user_mappings", "publication": "publications", "subscription": "subscriptions",
	"event_trigger": "event_triggers", "ts_config": "text_search_configurations",
	"ts_dict": "text_search_dictionaries", "ts_parser": "text_search_parsers",
	"ts_template": "text_search_templates",
}

// IgnorePattern returns the [ignore] key and pattern ignoring an object of
// kind with the given snapshot key: the object's name, schema-qualified
// for a schema-scoped kind, without a routine's argument list (so every
// overload is ignored).
func IgnorePattern(kind, object string) (key, pattern string, ok bool) {
	key, ok = ignoreKeys[kind]
	if !ok {
		return "", "", false
	}
	pattern = object
	if i := strings.IndexAny(pattern, "( "); i > 0 && kind != "cast" && kind != "user_mapping" {
		pattern = pattern[:i]
	}
	return key, pattern, true
}

// addIgnorePattern returns toml with pattern added to key in its [ignore]
// table, creating the key or the table when missing. Comments and layout
// elsewhere are kept. A pattern already present is not added again.
func addIgnorePattern(toml, key, pattern string) (string, error) {
	lit := strconv.Quote(pattern)
	lines := strings.Split(toml, "\n")
	table := -1
	for i, l := range lines {
		if strings.TrimSpace(stripTOMLComment(l)) == "[ignore]" {
			table = i
			break
		}
	}
	if table < 0 {
		out := strings.TrimRight(toml, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + "[ignore]\n" + key + " = [" + lit + "]\n", nil
	}
	// The table runs to the next table header.
	end := len(lines)
	for i := table + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") && !strings.HasPrefix(strings.TrimSpace(lines[i]), "[[") && isHeader(lines[i]) {
			end = i
			break
		}
	}
	for i := table + 1; i < end; i++ {
		code := stripTOMLComment(lines[i])
		k, v, found := strings.Cut(code, "=")
		if !found || strings.TrimSpace(k) != key {
			continue
		}
		// Find the closing ']' of the array, possibly lines later.
		for j := i; j < end; j++ {
			c := stripTOMLComment(lines[j])
			if j == i {
				c = v
			}
			if strings.Contains(c, lit) {
				return toml, nil // already there
			}
			if idx := strings.LastIndex(c, "]"); idx >= 0 {
				if j > i && strings.TrimSpace(c[:idx]) == "" {
					// Multi-line array closed on its own line: add an
					// element line like the previous one.
					prev := j - 1
					for prev > i && strings.TrimSpace(stripTOMLComment(lines[prev])) == "" {
						prev--
					}
					pc := stripTOMLComment(lines[prev])
					if t := strings.TrimRight(pc, " \t"); !strings.HasSuffix(t, ",") && !strings.HasSuffix(t, "[") {
						lines[prev] = t + "," + lines[prev][len(pc):]
					}
					indent := lines[prev][:len(lines[prev])-len(strings.TrimLeft(lines[prev], " \t"))]
					if prev == i {
						indent = "    "
					}
					lines = append(lines[:j], append([]string{indent + lit + ","}, lines[j:]...)...)
					return strings.Join(lines, "\n"), nil
				}
				// Insert before the ']' on line j.
				full := lines[j]
				base := 0
				if j == i {
					base = len(k) + 1
				}
				pos := base + idx
				before := strings.TrimRight(full[:pos], " \t")
				sep := ", "
				if strings.HasSuffix(before, "[") {
					sep = ""
				} else if strings.HasSuffix(before, ",") {
					sep = " "
				}
				lines[j] = before + sep + lit + full[pos:]
				return strings.Join(lines, "\n"), nil
			}
		}
		return "", fmt.Errorf("[ignore] %s: cannot find the end of its array", key)
	}
	// Key missing: add it right after the table header.
	lines = append(lines[:table+1], append([]string{key + " = [" + lit + "]"}, lines[table+1:]...)...)
	return strings.Join(lines, "\n"), nil
}

func isHeader(l string) bool {
	t := strings.TrimSpace(stripTOMLComment(l))
	return strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")
}

// stripTOMLComment removes a '#' comment outside a string.
func stripTOMLComment(l string) string {
	inStr := byte(0)
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case inStr != 0 && c == '\\' && inStr == '"':
			i++
		case inStr != 0 && c == inStr:
			inStr = 0
		case inStr == 0 && (c == '"' || c == '\''):
			inStr = c
		case inStr == 0 && c == '#':
			return l[:i]
		}
	}
	return l
}
