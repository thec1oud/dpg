package apply

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
)

// createIndexConcurrently matches the CREATE INDEX CONCURRENTLY statements
// the differ writes (createIndex), capturing the index's quoted name and
// its table, quoted and maybe schema-qualified.
var createIndexConcurrently = regexp.MustCompile(`^CREATE (?:UNIQUE )?INDEX CONCURRENTLY ("(?:[^"]|"")+") ON (?:ONLY )?((?:"(?:[^"]|"")+"\.)?"(?:[^"]|"")+")`)

// prepareFinish makes the statements after a committed migration's
// transaction safe to run again. A CREATE INDEX CONCURRENTLY has no IF NOT
// EXISTS: one that was cancelled or failed part way leaves its index
// behind, invalid, and one that succeeded before a later statement failed
// leaves it valid, and either way running it again fails. So an invalid
// index of that name is dropped first (DROP INDEX CONCURRENTLY, outside a
// transaction like the build), and a valid one means the build is done.
// Other statements are as they were.
func prepareFinish(ctx context.Context, q pipeline.Querier, ops []pipeline.DiffOp) ([]pipeline.DiffOp, error) {
	var out []pipeline.DiffOp
	for _, op := range ops {
		m := createIndexConcurrently.FindStringSubmatch(strings.TrimSpace(op.SQL()))
		if m == nil {
			out = append(out, op)
			continue
		}
		schema, valid, found, other, err := indexState(ctx, q, unquote(m[1]), m[2])
		if err != nil {
			return nil, err
		}
		switch {
		case other != "":
			return nil, fmt.Errorf("DPG-E049: the migration builds index %s on %s, but an index of that name is on %s; rename or drop it, then apply again", m[1], m[2], other)
		case !found:
			out = append(out, op)
		case !valid:
			drop := "DROP INDEX CONCURRENTLY IF EXISTS " + quote(schema) + "." + m[1] + ";"
			out = append(out, finishOp{sql: drop}, op)
		}
	}
	return out, nil
}

// indexState finds the index named name in table's schema: whether it
// is there, on table, and valid. other is the table it is on instead, when
// it is on another.
func indexState(ctx context.Context, q pipeline.Querier, name, table string) (schema string, valid, found bool, other string, err error) {
	rows, err := q.QueryRows(ctx, `SELECT n.nspname, i.indisvalid, i.indrelid = to_regclass($2), i.indrelid::regclass::text
FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname = $1 AND c.relnamespace = (SELECT relnamespace FROM pg_class WHERE oid = to_regclass($2))`, name, table)
	if err != nil {
		return "", false, false, "", err
	}
	defer rows.Close()
	if rows.Next() {
		var onTable bool
		var on string
		if err := rows.Scan(&schema, &valid, &onTable, &on); err != nil {
			return "", false, false, "", err
		}
		if !onTable {
			return "", false, false, on, rows.Err()
		}
		found = true
	}
	return schema, valid, found, "", rows.Err()
}

// snapshotKey is the snapshot's key of a relation written as the differ
// quotes it ("s"."t" is s.t).
func snapshotKey(rel string) string {
	var parts []string
	for len(rel) > 0 {
		end := 1
		for end < len(rel) {
			if rel[end] == '"' {
				if end+1 < len(rel) && rel[end+1] == '"' {
					end += 2
					continue
				}
				break
			}
			end++
		}
		parts = append(parts, unquote(rel[:end+1]))
		rel = strings.TrimPrefix(rel[min(end+1, len(rel)):], ".")
	}
	return strings.Join(parts, ".")
}

func unquote(ident string) string {
	return strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(ident, `"`), `"`), `""`, `"`)
}

func quote(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

// finishOp is a statement prepareFinish adds, run outside a transaction.
type finishOp struct{ sql string }

func (o finishOp) SQL() string           { return o.sql }
func (finishOp) Safety() pipeline.Safety { return pipeline.Safe }
func (finishOp) Pos() pipeline.SourcePos { return pipeline.SourcePos{} }
func (finishOp) Mode() pipeline.ExecMode { return pipeline.ModeNonTransactional }
