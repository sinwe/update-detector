//go:build !windows

package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
	"update-detector/internal/checker"
)

// recheckTrackingServer serves the given status at any path except
// /recheck, which it records having been hit and responds 202 to --
// mirrors how the real agent's /status and /recheck are two routes on the
// same server.
func recheckTrackingServer(t *testing.T, status checker.Status) (srv *httptest.Server, recheckCalled *atomic.Bool) {
	t.Helper()
	recheckCalled = &atomic.Bool{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/recheck" {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST /recheck, got %s", r.Method)
			}
			recheckCalled.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}))
	t.Cleanup(srv.Close)
	return srv, recheckCalled
}

// writeFakeAptGet puts a fake "apt-get" script at the front of PATH for the
// duration of the test, so Apply's exec.Command calls hit it instead of a
// real package manager.
func writeFakeAptGet(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "apt-get")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func statusServer(t *testing.T, status checker.Status) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

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

func TestApplyPackagesRejectsWhenNotPending(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeAptGet(t, fmt.Sprintf(`echo "$@" >> %q; exit 0`, callLog))

	srv := statusServer(t, checker.Status{
		Packages: checker.PackageInfo{Upgrades: []checker.PackageUpgrade{{Name: "vim"}}},
	})

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionPackages, Packages: []string{"curl"}}
	result := Apply(context.Background(), srv.URL, "", aggregatorclient.Identity{}, action)

	if result.Success {
		t.Fatalf("expected rejection, got success: %#v", result)
	}
	if !strings.Contains(result.Message, "curl") {
		t.Fatalf("expected message to mention curl, got: %s", result.Message)
	}
	if _, err := os.ReadFile(callLog); err == nil {
		t.Fatal("expected apt-get to never be invoked for a rejected action")
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

func TestApplyRecheckSkipsAptAndTriggersRecheck(t *testing.T) {
	// Deliberately no writeFakeAptGet -- if this incorrectly fell through
	// to running apt-get, it would fail (no such binary on PATH) and
	// result.Success would be false, catching the bug implicitly.
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionRecheck})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
	if !recheckCalled.Load() {
		t.Fatal("expected POST /recheck to have been called")
	}
}

func TestApplySelfUpdateSkipsAptAndLocalValidation(t *testing.T) {
	// No writeFakeAptGet, and the local status server would reject a
	// packages-type action for an empty pending list -- if this
	// incorrectly fell through to the normal apt-get path, it would fail
	// closed on one of those, catching the bug implicitly.
	writeFakeInstallSh(t, filepath.Join(t.TempDir(), "calls.log"), 0)
	srv, _ := recheckTrackingServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{
		ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "companion", TargetVersion: "v0.11.0",
	})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
}

func TestApplyRejectionDoesNotTriggerRecheck(t *testing.T) {
	srv, recheckCalled := recheckTrackingServer(t, checker.Status{
		Packages: checker.PackageInfo{Upgrades: []checker.PackageUpgrade{{Name: "vim"}}},
	})

	result := Apply(context.Background(), srv.URL+"/status", "", aggregatorclient.Identity{}, aggregator.Action{
		ID: "act1", Type: aggregator.ActionPackages, Packages: []string{"curl"},
	})
	if result.Success {
		t.Fatal("expected rejection")
	}
	if recheckCalled.Load() {
		t.Fatal("expected no recheck for a rejected action -- nothing on the host changed")
	}
}

func TestApplyFailsWhenLocalStatusUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	result := Apply(context.Background(), "http://"+addr, "", aggregatorclient.Identity{}, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if result.Success {
		t.Fatal("expected failure when the local agent is unreachable")
	}
}
