//go:build windows

package windows

import (
	"context"
	"testing"
	"time"
)

// TestCheckRebootRequiredRunsCleanly is a real integration smoke test,
// not a fixture -- it hits the actual registry on whatever Windows
// machine runs this (a hosted windows-latest GitHub Actions runner, as
// of Phase 2). checkRebootRequired has no error return (see its own doc
// comment), so there's nothing to assert beyond "it returns instead of
// panicking" -- a CI-hosted Windows image should always report false
// here, but this test deliberately doesn't assert that, since it's
// legitimately true on any freshly-booted real machine too and isn't
// this function's own behavior to verify.
func TestCheckRebootRequiredRunsCleanly(t *testing.T) {
	got := checkRebootRequired()
	t.Logf("checkRebootRequired() = %v", got)
}

// TestReadOSInfoOnRealMachine confirms the registry keys this relies on
// actually exist and are readable on a real Windows install --
// CurrentVersion is fundamental (present since Windows 2000), so unlike
// checkRebootRequired's own keys, this should never legitimately fail.
func TestReadOSInfoOnRealMachine(t *testing.T) {
	info, err := readOSInfo()
	if err != nil {
		t.Fatalf("unexpected error reading a registry key that should exist on every real Windows install: %v", err)
	}
	if info.CurrentVersion == "" && info.CurrentCodename == "" {
		t.Fatal("got completely empty OSInfo -- expected at least one of DisplayVersion/ReleaseId or ProductName to be set")
	}
	if info.UpdateAvailable {
		t.Fatal("UpdateAvailable must always be false in v1 -- no OS-upgrade detection exists for Windows yet")
	}
	t.Logf("readOSInfo() = %#v", info)
}

// TestCheckUpgradableDoesNotHangOrPanic exercises the real winget exec
// path. Deliberately doesn't assert success: winget's presence on a
// hosted CI image isn't guaranteed, and its absence is an expected,
// already-handled condition (see checkUpgradable's own doc comment),
// not a bug -- this only confirms the call returns within a reasonable
// time instead of hanging, and logs whichever outcome actually happened
// so CI logs show whether winget was present on this runner image.
func TestCheckUpgradableDoesNotHangOrPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := checkUpgradable(ctx)
	if err != nil {
		t.Logf("checkUpgradable() returned an error (expected if winget isn't on this runner image): %v", err)
		return
	}
	t.Logf("checkUpgradable() succeeded: %d upgrade(s) found", result.Total)
}
