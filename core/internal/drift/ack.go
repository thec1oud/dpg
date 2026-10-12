package drift

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Ack acknowledges one drift item: the item no longer fails dpg verify
// while its values stay as they were acknowledged and Expires hasn't
// passed.
type Ack struct {
	ID          string     `json:"id"`
	Fingerprint string     `json:"fingerprint"`
	Reason      string     `json:"reason,omitempty"`
	By          string     `json:"by,omitempty"`
	At          time.Time  `json:"at"`
	Expires     *time.Time `json:"expires,omitempty"`
}

// Fingerprint identifies an item's state: its identity and the snapshot,
// live and source values. An acknowledgement lapses when it changes.
func Fingerprint(it Item) string {
	h := sha256.New()
	for _, s := range []string{it.Kind, it.Object, it.Property, it.Change, it.Snapshot, it.Live, it.Source} {
		fmt.Fprintf(h, "%d:%s\n", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Acknowledge returns an acknowledgement of it.
func Acknowledge(it Item, reason, by string, at time.Time, expires *time.Time) Ack {
	return Ack{ID: it.ID, Fingerprint: Fingerprint(it), Reason: reason, By: by, At: at.UTC(), Expires: expires}
}

// ApplyAcks marks the items acks cover as acknowledged, and records on an
// item an acknowledgement that has lapsed (its values changed or it
// expired). It returns the acknowledgements matching no item: the drift
// they covered is gone.
func ApplyAcks(r *Report, acks []Ack, now time.Time) (unused []Ack) {
	byID := map[string]int{}
	for i, it := range r.Items {
		byID[it.ID] = i
	}
	for i := range acks {
		a := acks[i]
		idx, ok := byID[a.ID]
		if !ok {
			unused = append(unused, a)
			continue
		}
		it := &r.Items[idx]
		if a.Fingerprint != Fingerprint(*it) || (a.Expires != nil && !now.Before(*a.Expires)) {
			it.Lapsed = &acks[i]
			continue
		}
		it.Acknowledged = &acks[i]
	}
	return unused
}

// ackFile is the on-disk form: .dpg/acknowledged/<cluster>/<database>.json.
type ackFile struct {
	Version int   `json:"version"`
	Acks    []Ack `json:"acknowledgements"`
}

// LoadAcks reads an acknowledgement file; a missing file has none.
func LoadAcks(path string) ([]Ack, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f ackFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d", path, f.Version)
	}
	return f.Acks, nil
}

// SaveAcks writes an acknowledgement file, sorted by ID; no
// acknowledgements removes it.
func SaveAcks(path string, acks []Ack) error {
	if len(acks) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	sorted := append([]Ack(nil), acks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	data, err := json.MarshalIndent(ackFile{Version: 1, Acks: sorted}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
