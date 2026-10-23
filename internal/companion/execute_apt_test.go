//go:build linux

package companion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
	"update-detector/internal/checker"
)

// This file holds the Apply end-to-end tests that drive the real apt
// Applier through a fake apt-get on PATH -- Linux-only, mirroring
// execute_darwin_test.go's brew-driven equivalents. The
// platform-independent Apply tests (pending rejection, recheck routing,
// self-update dispatch, unreachable-agent handling) stay in
// execute_test.go and run everywhere.

func TestApplyPackagesSucceedsWhenPending(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeAptGet(t, fmt.Sprintf(`echo "$@" >> %q; exit 0`, callLog))

	srv := statusServer(t, checker.Status{
		Packages: checker.PackageInfo{Upgrades: []checker.PackageUpgrade{{Name: "curl"}}},
	})

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionPackages, Packages: []string{"curl"}}
	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, action)

	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
	if result.ActionID != "act1" {
		t.Fatalf("got action id %q, want act1", result.ActionID)
	}

	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("expected apt-get to have been called: %v", err)
	}
	if !strings.Contains(string(data), "curl") {
		t.Fatalf("expected apt-get invocation to include curl, got: %s", data)
	}
}

func TestApplyUpgradeDoesNotRequirePendingList(t *testing.T) {
	writeFakeAptGet(t, `exit 0`)
	srv := statusServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
}

func TestApplyReportsAptGetFailure(t *testing.T) {
	// "update" must succeed so this actually exercises the real upgrade
	// command failing, not the pre-flight refresh.
	writeFakeAptGet(t, `if [ "$1" = "update" ]; then exit 0; fi; echo "boom" >&2; exit 1`)
	srv := statusServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionFullUpgrade})
	if result.Success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(result.Message, "boom") {
		t.Fatalf("expected apt-get stderr in message, got: %s", result.Message)
	}
}

func TestApplyFailsWhenAptGetUpdateFails(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeAptGet(t, fmt.Sprintf(`
if [ "$1" = "update" ]; then
  echo "network unreachable" >&2
  exit 1
fi
echo "$@" >> %q
exit 0
`, callLog))
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if result.Success {
		t.Fatal("expected failure when apt-get update fails")
	}
	if !strings.Contains(result.Message, "network unreachable") {
		t.Fatalf("expected apt-get update's stderr in message, got: %s", result.Message)
	}
	if _, err := os.ReadFile(callLog); err == nil {
		t.Fatal("expected the real upgrade command to never run after apt-get update failed")
	}
	if recheckCalled.Load() {
		t.Fatal("expected no recheck when apt-get update failed -- nothing on the host changed")
	}
}

func TestApplySuccessTriggersRecheck(t *testing.T) {
	writeFakeAptGet(t, `exit 0`)
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
	if !recheckCalled.Load() {
		t.Fatal("expected POST /recheck to have been called after a successful apply")
	}
}

func TestApplyFailureStillTriggersRecheck(t *testing.T) {
	// "update" must succeed so this exercises the real upgrade command
	// failing (which can partially apply before erroring, hence still
	// worth rechecking), not the pre-flight refresh failing (which
	// shouldn't trigger a recheck -- nothing on the host changed).
	writeFakeAptGet(t, `if [ "$1" = "update" ]; then exit 0; fi; echo boom >&2; exit 1`)
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionFullUpgrade})
	if result.Success {
		t.Fatal("expected failure")
	}
	if !recheckCalled.Load() {
		t.Fatal("expected POST /recheck to have been called even after a failed apt-get -- it can partially apply before erroring")
	}
}
