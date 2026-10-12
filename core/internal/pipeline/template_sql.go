package pipeline

import (
	"fmt"
	"strings"
)

// ResolveTemplateSQL resolves the {{...}} secret references in a SQL
// statement. A reference inside a single-quoted literal is substituted with
// its quotes doubled, so a secret can't end the literal; elsewhere it is
// substituted as it is (as ResolveTemplate does).
func ResolveTemplateSQL(sql string, resolver SecretResolver) (string, error) {
	if !strings.Contains(sql, "{{") {
		return sql, nil
	}
	var b strings.Builder
	inLiteral := false
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			if inLiteral && i+1 < len(sql) && sql[i+1] == '\'' {
				b.WriteString("''")
				i += 2
				continue
			}
			inLiteral = !inLiteral
			b.WriteByte(c)
			i++
			continue
		}
		if c == '{' && strings.HasPrefix(sql[i:], "{{") {
			end := strings.Index(sql[i:], "}}")
			if end > 0 {
				match := sql[i : i+end+2]
				if m := templateRef.FindStringSubmatch(match); m != nil && m[0] == match {
					val, err := resolver.Resolve(m[1])
					if err != nil {
						return "", fmt.Errorf("resolving {{%s}}: %w", m[1], err)
					}
					if inLiteral {
						val = strings.ReplaceAll(val, "'", "''")
					}
					b.WriteString(val)
					i += end + 2
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), nil
}
