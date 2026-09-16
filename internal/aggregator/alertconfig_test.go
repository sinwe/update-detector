package aggregator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAlertStoreSeedsFromEnvOnFirstLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	s := NewAlertStore(path, true, 5*time.Minute)
	if err := s.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	got := s.Get()
	if !got.Enabled || got.After != "5m" {
		t.Fatalf("expected seeded {true 5m}, got %#v", got)
	}
	// Seeding persists, so the next start reads the file, not the env.
	s2 := NewAlertStore(path, false, time.Hour)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := s2.Get(); !got.Enabled || got.After != "5m" {
		t.Fatalf("expected file to win over new defaults, got %#v", got)
	}
}

func TestAlertStoreSeedsSaneDefaultsForUnusableEnv(t *testing.T) {
	s := NewAlertStore(filepath.Join(t.TempDir(), "alerts.json"), false, 0)
	if err := s.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := s.Get(); got.Enabled || got.After != "5m" {
		t.Fatalf("expected {false 5m} for env-after 0, got %#v", got)
	}
}

func TestAlertStoreSetRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	s := NewAlertStore(path, true, 5*time.Minute)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(false, "30m"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "bogus", "0s", "30s", "25h"} {
		if err := s.Set(true, bad); err == nil {
			t.Errorf("expected Set(%q) to fail", bad)
		}
	}

	s2 := NewAlertStore(path, true, 5*time.Minute)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := s2.Get(); got.Enabled || got.After != "30m" {
		t.Fatalf("expected {false 30m} to survive reload, got %#v", got)
	}
}

func TestAlertStoreLoadCorruptFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	if err := os.WriteFile(path, []byte("{bogus"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewAlertStore(path, true, 5*time.Minute).Load(); err == nil {
		t.Fatal("expected Load of a corrupt file to fail")
	}
}

func TestAlertSettingsAfterDuration(t *testing.T) {
	for _, tc := range []struct {
		after string
		want  time.Duration
		ok    bool
	}{
		{"5m", 5 * time.Minute, true},
		{"1h", time.Hour, true},
		{"24h", 24 * time.Hour, true},
		{"", 0, false},
		{"bogus", 0, false},
		{"30s", 0, false},
		{"25h", 0, false},
	} {
		got, ok := AlertSettings{Enabled: true, After: tc.after}.AfterDuration()
		if got != tc.want || ok != tc.ok {
			t.Errorf("AfterDuration(%q) = (%v, %v), want (%v, %v)", tc.after, got, ok, tc.want, tc.ok)
		}
	}
}
