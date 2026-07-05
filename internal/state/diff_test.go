package state

import (
	"testing"

	"update-detector/internal/checker"
)

func TestDiffFirstRun(t *testing.T) {
	current := checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 5}}
	if got := Diff(nil, current); got != nil {
		t.Fatalf("expected nil changes on first run, got %#v", got)
	}
}

func TestDiffNoChange(t *testing.T) {
	prev := checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 5, UpgradableSecurity: 1}}
	current := prev
	if got := Diff(&prev, current); len(got) != 0 {
		t.Fatalf("expected no changes, got %#v", got)
	}
}

func TestDiffNewPackageUpdates(t *testing.T) {
	prev := checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 2}}
	current := checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 5}}
	got := Diff(&prev, current)
	if len(got) != 1 {
		t.Fatalf("expected 1 change, got %#v", got)
	}
}

func TestDiffFewerUpdatesIsNotReported(t *testing.T) {
	prev := checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 5}}
	current := checker.Status{Packages: checker.PackageInfo{UpgradableTotal: 2}}
	if got := Diff(&prev, current); len(got) != 0 {
		t.Fatalf("expected no changes when update count drops, got %#v", got)
	}
}

func TestDiffRebootRequiredTransition(t *testing.T) {
	prev := checker.Status{RebootRequired: false}
	current := checker.Status{RebootRequired: true}
	got := Diff(&prev, current)
	if len(got) != 1 {
		t.Fatalf("expected 1 change, got %#v", got)
	}

	// flipping back off should not itself be reported
	prev2 := checker.Status{RebootRequired: true}
	current2 := checker.Status{RebootRequired: false}
	if got := Diff(&prev2, current2); len(got) != 0 {
		t.Fatalf("expected no changes when reboot flips to not-required, got %#v", got)
	}
}

func TestDiffOSUpgradeNewlyAvailable(t *testing.T) {
	prev := checker.Status{OS: checker.OSInfo{CurrentVersion: "22.04", UpdateAvailable: false}}
	current := checker.Status{OS: checker.OSInfo{CurrentVersion: "22.04", UpdateAvailable: true, LatestVersion: "24.04 LTS"}}
	got := Diff(&prev, current)
	if len(got) != 1 {
		t.Fatalf("expected 1 change, got %#v", got)
	}
}

func TestDiffMultipleChanges(t *testing.T) {
	prev := checker.Status{
		Packages:       checker.PackageInfo{UpgradableTotal: 2, UpgradableSecurity: 0},
		RebootRequired: false,
	}
	current := checker.Status{
		Packages:       checker.PackageInfo{UpgradableTotal: 5, UpgradableSecurity: 1},
		RebootRequired: true,
	}
	got := Diff(&prev, current)
	if len(got) != 3 {
		t.Fatalf("expected 3 changes, got %#v", got)
	}
}
