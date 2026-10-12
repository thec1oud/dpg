//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/emit"
	"github.com/thec1oud/dpg/internal/executor"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/testpg"
)

// assertNoReplan fails if planning files against the store's snapshot
// produces any ops.
func assertNoReplan(t *testing.T, files []string, dir string, differ pipeline.Differ, store *memStore) {
	t.Helper()
	desired, _, err := compiler.Compile(files, dir, pipeline.Default)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base, _ := store.Load("test", "dpgtest")
	ops, err := differ.Diff(desired, base)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	for _, op := range ops {
		t.Errorf("re-plan not a no-op: [%s] %s", op.Safety(), op.SQL())
	}
}

// TestRoundtripFKDeleteSetColumns guards RFC Section 7.2: ON DELETE SET
// NULL (col-list) keeps its column list, so deleting the referenced row
// nulls only the listed column.
func TestRoundtripFKDeleteSetColumns(t *testing.T) {
	connStr := testpg.Start(t)
	ctx := context.Background()
	differ, emitter, applyExec, store := diff.New(), emit.New(), executor.New(), newMemStore()
	conn, err := executor.Connect(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	dir := t.TempDir()
	f := filepath.Join(dir, "schema.dpg")
	src := `TABLE tenants (id int PRIMARY KEY);
TABLE docs (
    tenant_id int NOT NULL,
    author_id int,
    id        int,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, author_id)
);
TABLE notes (
    tenant_id int NOT NULL,
    doc_id    int,
    CONSTRAINT fk_doc FOREIGN KEY (tenant_id, doc_id) REFERENCES docs (tenant_id, id) ON DELETE SET NULL (doc_id)
);
`
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	applyFixture(t, ctx, conn, []string{f}, dir, differ, emitter, applyExec, store)

	for _, q := range []string{
		`INSERT INTO docs VALUES (1, NULL, 10)`,
		`INSERT INTO notes VALUES (1, 10)`,
		`DELETE FROM docs WHERE id = 10`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rows, err := conn.QueryRows(ctx, `SELECT tenant_id, doc_id IS NULL FROM notes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("notes row missing")
	}
	var tenant int
	var docNull bool
	if err := rows.Scan(&tenant, &docNull); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if tenant != 1 || !docNull {
		t.Errorf("after delete: tenant_id=%d doc_id null=%v, want tenant_id kept and doc_id nulled", tenant, docNull)
	}

	assertNoReplan(t, []string{f}, dir, differ, store)
	assertNoLiveDrift(t, ctx, conn, []string{f}, dir, differ, store)
}
