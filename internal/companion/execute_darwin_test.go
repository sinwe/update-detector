//go:build darwin

package companion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
	"update-detector/internal/checker"
)

// This file mirrors the applier-driving Apply end-to-end tests in
// execute_apt_test.go (Linux) through a fake brew on PATH instead --
// same shared validation/recheck/output-tap behavior, brew argv. The
// platform-independent Apply tests stay in execute_test.go and run
// everywhere.

func TestApplyPackagesSucceedsWhenPending(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeBrew(t, `echo "$@" >> `+callLog+`
exit 0`)

	srv := statusServer(t, checker.Status{
		Packages: checker.PackageInfo{Upgrades: []checker.PackageUpgrade{{Name: "deno"}}},
	})

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionPackages, Packages: []string{"deno"}}
	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, action)

	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
	if result.ActionID != "act1" {
		t.Fatalf("got action id %q, want act1", result.ActionID)
	}

	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("expected brew to have been called: %v", err)
	}
	if !strings.Contains(string(data), "upgrade deno") {
		t.Fatalf("expected brew invocation to upgrade deno, got: %s", data)
	}
}

func TestApplyUpgradeDoesNotRequirePendingList(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	writeFakeBrew(t, `exit 0`)
	srv := statusServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
}

func TestApplyReportsBrewFailure(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	// "update" must succeed so this actually exercises the real upgrade
	// command failing, not the pre-flight refresh.
	writeFakeBrew(t, `if [ "$1" = "update" ]; then exit 0; fi; echo "boom" >&2; exit 1`)
	srv := statusServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionFullUpgrade})
	if result.Success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(result.Message, "boom") {
		t.Fatalf("expected brew stderr in message, got: %s", result.Message)
	}
}

func TestApplyFailsWhenBrewUpdateFails(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeBrew(t, `if [ "$1" = "update" ]; then
  echo "network unreachable" >&2
  exit 1
fi
echo "$@" >> `+callLog+`
exit 0`)
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if result.Success {
		t.Fatal("expected failure when brew update fails")
	}
	if !strings.Contains(result.Message, "network unreachable") {
		t.Fatalf("expected brew update's stderr in message, got: %s", result.Message)
	}
	if _, err := os.ReadFile(callLog); err == nil {
		t.Fatal("expected the real upgrade command to never run after brew update failed")
	}
	if recheckCalled.Load() {
		t.Fatal("expected no recheck when brew update failed -- nothing on the host changed")
	}
}

func TestApplySuccessTriggersRecheck(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	writeFakeBrew(t, `exit 0`)
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
	if !recheckCalled.Load() {
		t.Fatal("expected POST /recheck to have been called after a successful apply")
	}
}

func TestApplyTapsOutputToAttachedSink(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	writeFakeBrew(t, `
if [ "$1" = "update" ]; then exit 0; fi
echo "line one"
echo "line two"
exit 0
`)
	srv := statusServer(t, checker.Status{})

	sink := NewOutputSink(10)
	ctx := WithOutputSink(context.Background(), sink)
	result := Apply(ctx, srv.URL, "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	sink.Close()
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}

	found := map[string]bool{}
	for line := range sink.Lines() {
		found[line] = true
	}
	if !found["line one"] || !found["line two"] {
		t.Fatalf("expected the sink to have tapped the command's output lines, got %v", found)
	}
}
