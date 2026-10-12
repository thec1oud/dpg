package main

import (
	"testing"

	"github.com/thec1oud/dpg/internal/compiler"
	"github.com/thec1oud/dpg/internal/diff"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/snapshot"
)

// TestTableLevelPrimaryKeyImpliesNotNull guards RFC Section 7.2: a
// table-level PRIMARY KEY makes its columns NOT NULL, so re-planning a
// table against its own snapshot is a no-op rather than an ALTER COLUMN
// ... DROP NOT NULL that PostgreSQL would reject.
func TestTableLevelPrimaryKeyImpliesNotNull(t *testing.T) {
	file, dir := dpgTempFile(t, `TABLE docs (tenant_id int, id int, note text, PRIMARY KEY (tenant_id, id));`)
	desired, _, err := compiler.CompileWith([]string{file}, dir, pipeline.Default, compiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap := &pipeline.Snapshot{}
	if err := snapshot.Populate(snap, desired); err != nil {
		t.Fatal(err)
	}
	ops, err := diff.New().Diff(desired, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		t.Errorf("re-plan not a no-op: %s", op.SQL())
	}
}
