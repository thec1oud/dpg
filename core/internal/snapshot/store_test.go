package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWarnsOnFormatMajorMismatch(t *testing.T) {
	var got []string
	old := Warn
	Warn = func(msg string) { got = append(got, msg) }
	defer func() { Warn = old }()

	dir := t.TempDir()
	s := &FileStore{Dir: dir}
	write := func(db, version string) {
		p := filepath.Join(dir, "c", db+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"dpg_version": "`+version+`", "objects": {}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("same", Version)
	write("minor", strings.SplitN(Version, ".", 2)[0]+".99.0")
	write("empty", "")
	write("other", "7.0.0")
	for _, db := range []string{"same", "minor", "empty", "other", "other"} {
		if _, err := s.Load("c", db); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "other.json") || !strings.Contains(got[0], "format 7.0.0") {
		t.Errorf("warnings: %q", got)
	}
}
