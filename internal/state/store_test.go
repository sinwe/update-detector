package state

import (
	"path/filepath"
	"testing"
	"time"

	"update-detector/internal/checker"
)

func TestStoreLoadMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	got, err := s.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil status, got %#v", got)
	}
}

func TestStoreSaveAndLoadRoundTrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nested", "state.json"))
	want := checker.Status{
		Hostname:  "web01",
		Platform:  "ubuntu",
		CheckedAt: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		Packages: checker.PackageInfo{
			UpgradableTotal:    5,
			UpgradableSecurity: 2,
			Upgrades:           []checker.PackageUpgrade{{Name: "curl", CandidateVersion: "7.81.0-1ubuntu1.16"}},
		},
		OK:        false,
	}

	if err := s.Save(want); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil status")
	}
	if got.Hostname != want.Hostname || got.Packages.UpgradableTotal != want.Packages.UpgradableTotal {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if !got.CheckedAt.Equal(want.CheckedAt) {
		t.Fatalf("got CheckedAt %v, want %v", got.CheckedAt, want.CheckedAt)
	}
}
