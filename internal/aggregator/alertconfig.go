package aggregator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// defaultAlertAfter is the grace period a fresh AlertStore seeds when the
// environment doesn't provide a usable one (OFFLINE_ALERT_AFTER unset or
// <= 0) — matches aggregatorconfig's own "5m" default.
const defaultAlertAfter = 5 * time.Minute

// alertAfterMin/Max bound the grace period the admin API accepts: below
// one poll interval is meaningless and flap-prone, beyond a day is "just
// turn it off".
const (
	alertAfterMin = time.Minute
	alertAfterMax = 24 * time.Hour
)

// AlertSettings is the fleet-wide offline-alert configuration: the
// master switch plus the grace period. Unlike OFFLINE_ALERT_AFTER (which
// only seeds these on first start), both are editable from /admin at
// runtime — the PresenceWatcher re-reads them every round, so changes
// apply within a minute with no restart.
type AlertSettings struct {
	// Enabled is the fleet-wide master switch. Default true.
	Enabled bool `json:"enabled"`
	// After is the grace period as a Go duration string ("5m").
	// Default "5m".
	After string `json:"after"`
}

// AfterDuration parses After, reporting false for anything unusable
// (empty, unparsable, out of range) so callers fall back to a default.
func (s AlertSettings) AfterDuration() (time.Duration, bool) {
	d, err := time.ParseDuration(s.After)
	if err != nil || d < alertAfterMin || d > alertAfterMax {
		return 0, false
	}
	return d, true
}

// AlertStore persists AlertSettings to a single JSON file next to the
// registry (same atomic rewrite discipline as Registry: temp file +
// rename under mutex). A missing file seeds from the constructor
// defaults — which main.go derives from the environment — so the first
// start after upgrading keeps the old env-driven behavior, and every
// edit after that comes from the UI.
type AlertStore struct {
	mu       sync.RWMutex
	path     string
	settings AlertSettings
}

func NewAlertStore(path string, enabled bool, after time.Duration) *AlertStore {
	// Canonical short form ("5m", not Duration.String()'s "5m0s") so the
	// stored value matches the admin page's grace select exactly.
	afterStr := formatAlertAfter(defaultAlertAfter)
	if after >= alertAfterMin && after <= alertAfterMax {
		afterStr = formatAlertAfter(after)
	}
	return &AlertStore{path: path, settings: AlertSettings{Enabled: enabled, After: afterStr}}
}

// Load reads the backing file, seeding (and persisting) the constructor
// defaults when it doesn't exist yet. A corrupt file is an error, same
// as Registry.Load — silently resetting alert preferences would be worse
// than refusing to start.
func (s *AlertStore) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.saveLocked()
		}
		return fmt.Errorf("alerts: reading %s: %w", s.path, err)
	}
	var settings AlertSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("alerts: parsing %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = settings
	return nil
}

// saveLocked persists the settings; callers must hold s.mu for writing.
func (s *AlertStore) saveLocked() error {
	data, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return fmt.Errorf("alerts: encoding: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("alerts: creating directory for %s: %w", s.path, err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("alerts: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("alerts: renaming %s to %s: %w", tmp, s.path, err)
	}
	return nil
}

// Get returns a snapshot of the current settings.
func (s *AlertStore) Get() AlertSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// Set replaces both fields atomically. after must be a Go duration
// string in range — callers (the admin handler) surface validation
// failures to the user; the check here is defense in depth for
// programmatic callers. The validated raw string is stored as-is (not
// Duration.String(), which would render "5m" as "5m0s") so the admin
// page's duration select keeps matching the stored value exactly.
func (s *AlertStore) Set(enabled bool, after string) error {
	d, err := time.ParseDuration(after)
	if err != nil || d < alertAfterMin || d > alertAfterMax {
		return fmt.Errorf("alerts: after %q out of range [%s, %s]", after, alertAfterMin, alertAfterMax)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = AlertSettings{Enabled: enabled, After: after}
	return s.saveLocked()
}
