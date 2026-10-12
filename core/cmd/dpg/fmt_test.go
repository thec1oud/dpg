package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thec1oud/dpg/internal/format"
)

// TestRunFmtFailsOnUnparseableFile: a file dpg fmt can't parse fails the run,
// with --check and without, and is left unchanged.
func TestRunFmtFailsOnUnparseableFile(t *testing.T) {
	opts := format.Options{IndentSize: 4, KeywordCase: "upper"}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.dpg")
	src := []byte("ROLE a NOLOGIN -- broken {\n    COMMENT 'a';\n}\n")
	if err := os.WriteFile(bad, src, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, check := range []bool{true, false} {
		if err := runFmt([]string{bad}, opts, check, false); err == nil {
			t.Errorf("check=%v: expected an error for an unparseable file", check)
		}
	}
	got, _ := os.ReadFile(bad)
	if string(got) != string(src) {
		t.Errorf("unparseable file was modified:\n%s", got)
	}

	good := filepath.Join(dir, "good.dpg")
	if err := os.WriteFile(good, []byte("ROLE a NOLOGIN;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFmt([]string{good}, opts, true, false); err != nil {
		t.Errorf("formatted file failed --check: %v", err)
	}
}
