//go:build integration

package diff

import (
	"context"
	"testing"

	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/testpg"
)

// TestBuiltinFuncVolatilityMatchesLiveCatalog checks builtin_funcs.go
// against a live server: every pg_catalog function name is known, and
// every name with a VOLATILE overload is in the volatile set. The tables
// cover PostgreSQL 14 through 18, so they may hold names this server
// lacks.
func TestBuiltinFuncVolatilityMatchesLiveCatalog(t *testing.T) {
	ctx := context.Background()
	conn, err := executor.Connect(ctx, testpg.Start(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	rows, err := conn.QueryRows(ctx, `
SELECT proname, bool_or(provolatile = 'v')
FROM pg_proc
WHERE pronamespace = 'pg_catalog'::regnamespace
GROUP BY proname;
`)
	if err != nil {
		t.Fatalf("query pg_proc: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var volatile bool
		if err := rows.Scan(&name, &volatile); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		switch {
		case volatile && !volatileBuiltinFuncs[name]:
			t.Errorf("%s is volatile but not in volatileBuiltinFuncNames", name)
		case !volatile && !volatileBuiltinFuncs[name] && !nonVolatileBuiltinFuncs[name]:
			t.Errorf("%s is missing from nonVolatileBuiltinFuncNames", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if n < 2000 {
		t.Fatalf("only %d pg_catalog functions read", n)
	}
}
