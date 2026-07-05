// Package state persists the last known checker.Status to disk so restarts
// don't lose the diffing baseline — without this, every container restart
// would look like a fresh "everything just changed" and spam notifications.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"update-detector/internal/checker"
)

type Store struct {
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load returns the last persisted Status, or (nil, nil) if none exists yet.
func (s *Store) Load() (*checker.Status, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: reading %s: %w", s.path, err)
	}
	var status checker.Status
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("state: parsing %s: %w", s.path, err)
	}
	return &status, nil
}

// Save persists status atomically, creating the parent directory if needed.
func (s *Store) Save(status checker.Status) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("state: creating directory for %s: %w", s.path, err)
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encoding status: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("state: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("state: renaming %s to %s: %w", tmp, s.path, err)
	}
	return nil
}
