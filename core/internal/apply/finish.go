package apply

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/ui"
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
// index of that name on the table is dropped first (DROP INDEX
// CONCURRENTLY, outside a transaction like the build), and a valid one is
// the build done when built says it is the one the statement builds. A
// valid one built otherwise (by hand, say), and an index of that name on
// another table, are refused: neither is DPG's to drop. Other statements
// are as they were.
//
// Its refusals are DPG's (DPG-E057, DPG-E058); its database errors are
// ui.DBErrors.
func prepareFinish(ctx context.Context, q pipeline.Querier, ops []pipeline.DiffOp, built builtFunc) ([]pipeline.DiffOp, error) {
	var out []pipeline.DiffOp
	for _, op := range ops {
		m := createIndexConcurrently.FindStringSubmatch(strings.TrimSpace(op.SQL()))
		if m == nil {
			out = append(out, op)
			continue
		}
		name := unquote(m[1])
		st, err := indexState(ctx, q, name, m[2])
		if err != nil {
			return nil, ui.WrapDB(err)
		}
		switch {
		case st.other != "":
			return nil, fmt.Errorf("DPG-E057: the migration builds index %s on %s, but an index of that name is on %s; rename or drop it, then apply again", m[1], m[2], st.other)
		case !st.found:
			out = append(out, op)
		case !st.valid:
			drop := "DROP INDEX CONCURRENTLY IF EXISTS " + quote(st.schema) + "." + m[1] + ";"
			out = append(out, finishOp{sql: drop}, op)
		default:
			same, err := built(ctx, tableKey(op, m[2]), name)
			if err != nil {
				return nil, err
			}
			if !same {
				return nil, fmt.Errorf("DPG-E058: the migration builds index %s on %s, but the table has a valid index of that name defined otherwise; drop or rename it, then apply again", m[1], m[2])
			}
		}
	}
	return out, nil
}

// builtFunc reports whether the index name on the table of snapshot key
// table is as the migration builds it.
type builtFunc func(ctx context.Context, table, name string) (bool, error)

// tableKey is the snapshot key of the table a CREATE INDEX statement
// builds on (targetKey), the op a step of the log.
func tableKey(op pipeline.DiffOp, table string) string {
	var tg *pipeline.Target
	if t, ok := op.(interface{ StepTarget() *pipeline.Target }); ok {
		tg = t.StepTarget()
	}
	return targetKey(tg, table)
}

// targetKey is the snapshot key of the table a CREATE INDEX statement
// builds on: its step's target, or else the table it names, quoted as the
// differ quotes it.
func targetKey(tg *pipeline.Target, table string) string {
	if tg != nil && tg.Key != "" {
		return tg.Key
	}
	return snapshotKey(table)
}

// index is what indexState finds of an index.
type index struct {
	schema       string
	found, valid bool   // on the table, and valid
	other        string // the table it is on instead, when it is on another
}

// indexState finds the index named name in table's schema.
func indexState(ctx context.Context, q pipeline.Querier, name, table string) (index, error) {
	rows, err := q.QueryRows(ctx, `SELECT n.nspname, i.indisvalid, i.indrelid = to_regclass($2), i.indrelid::regclass::text
FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname = $1 AND c.relnamespace = (SELECT relnamespace FROM pg_class WHERE oid = to_regclass($2))`, name, table)
	if err != nil {
		return index{}, err
	}
	defer rows.Close()
	var st index
	if rows.Next() {
		var onTable bool
		var on string
		if err := rows.Scan(&st.schema, &st.valid, &onTable, &on); err != nil {
			return index{}, err
		}
		if !onTable {
			return index{other: on}, rows.Err()
		}
		st.found = true
	}
	return st, rows.Err()
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
