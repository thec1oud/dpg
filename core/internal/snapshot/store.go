// Package snapshot implements pipeline.SnapshotStore. It reads and writes the
// committed JSON snapshot format from RFC Section 4.2 as JSON files in the project's
// .dpg/snapshots/ directory.
package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeySnapshotStore, &FileStore{Dir: ".dpg/snapshots"})
}

// Version is the snapshot format version written to dpg_version (RFC
// Section 16.4). Its major component changes when the format changes
// incompatibly.
const Version = "0.2.0"

// Warn reports a problem that doesn't stop the command. Tests replace it.
var Warn = func(msg string) { fmt.Fprintln(os.Stderr, "warning: "+msg) }

var warned sync.Map // path → true: each snapshot's version is reported once

// versionMismatch describes a snapshot written in another major format
// version, or "".
func versionMismatch(v string) string {
	if v == "" {
		return ""
	}
	major := func(s string) string { m, _, _ := strings.Cut(s, "."); return m }
	if major(v) == major(Version) {
		return ""
	}
	return fmt.Sprintf("it has snapshot format %s; this dpg reads and writes format %s", v, Version)
}

// FileStore implements pipeline.SnapshotStore using JSON files on disk.
// Each snapshot is stored at Dir/<cluster>/<database>.json, mirroring the
// cluster/database hierarchy of the project layout.
type FileStore struct {
	Dir string
	// Warn, when set, is told of the problems a load finds that don't stop
	// it, each time; otherwise the package's Warn is, once per snapshot.
	Warn func(msg string)
}

// Load implements pipeline.SnapshotStore. It reads and JSON-decodes the snapshot
// for the given cluster and database. Returns a zero-valued Snapshot if the file
// does not exist (first run / no prior apply).
func (s *FileStore) Load(cluster, database string) (*pipeline.Snapshot, error) {
	path := s.path(cluster, database)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &pipeline.Snapshot{}, nil
		}
		return nil, fmt.Errorf("snapshot: reading %s: %w", path, err)
	}

	var snap pipeline.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		if strings.Contains(string(data), "\n<<<<<<< ") || strings.HasPrefix(string(data), "<<<<<<< ") {
			return nil, fmt.Errorf("snapshot: %s has merge conflict markers; the snapshot is rebuilt from the migration log by dpg log repair (dpg log rebase when the log has two branches)", path)
		}
		return nil, fmt.Errorf("snapshot: parsing %s: %w", path, err)
	}
	if msg := versionMismatch(snap.DPGVersion); msg != "" {
		if s.Warn != nil {
			s.Warn(path + ": " + msg)
		} else if _, done := warned.LoadOrStore(path, true); !done {
			Warn(path + ": " + msg)
		}
	}
	return &snap, nil
}

// Save implements pipeline.SnapshotStore. It writes the snapshot as pretty-printed
// JSON. The parent directory is created if it does not exist.
func (s *FileStore) Save(cluster, database string, snap *pipeline.Snapshot) error {
	// Stamp version and apply time.
	snap.DPGVersion = Version
	snap.AppliedAt = time.Now().UTC().Format(time.RFC3339)
	snap.Cluster = cluster
	// _cluster is the internal storage key for cluster-level objects; the
	// database field is omitted from the JSON so the snapshot is unambiguous.
	if database != "_cluster" {
		snap.Database = database
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("snapshot: serialising: %w", err)
	}
	data = append(data, '\n')

	path := s.path(cluster, database)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("snapshot: creating directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("snapshot: writing %s: %w", path, err)
	}
	return nil
}

func (s *FileStore) path(cluster, database string) string {
	return filepath.Join(s.Dir, cluster, database+".json")
}

// Ensure FileStore implements pipeline.SnapshotStore.
var _ pipeline.SnapshotStore = (*FileStore)(nil)
