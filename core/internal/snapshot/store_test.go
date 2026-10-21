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

// A store with a Warn of its own tells it, not the package's Warn, and
// writes nothing to stderr.
func TestAStoresOwnWarnIsToldInsteadOfThePackages(t *testing.T) {
	old := Warn
	Warn = func(msg string) { t.Errorf("the package's Warn was told %q", msg) }
	defer func() { Warn = old }()

	dir := t.TempDir()
	p := filepath.Join(dir, "c", "db.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"dpg_version": "7.0.0", "objects": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []string
	s := &FileStore{Dir: dir, Warn: func(msg string) { got = append(got, msg) }}
	for range 2 {
		if _, err := s.Load("c", "db"); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || !strings.Contains(got[0], "format 7.0.0") {
		t.Errorf("warnings: %q", got)
	}
}
