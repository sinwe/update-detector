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
	"testing"

	"update-detector/internal/aggregator"
	"update-detector/internal/checker"
)

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
	result := Apply(context.Background(), srv.URL, action)

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
	result := Apply(context.Background(), srv.URL, action)

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

	result := Apply(context.Background(), srv.URL, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
}

func TestApplyReportsAptGetFailure(t *testing.T) {
	writeFakeAptGet(t, `echo "boom" >&2; exit 1`)
	srv := statusServer(t, checker.Status{})

	result := Apply(context.Background(), srv.URL, aggregator.Action{ID: "act1", Type: aggregator.ActionFullUpgrade})
	if result.Success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(result.Message, "boom") {
		t.Fatalf("expected apt-get stderr in message, got: %s", result.Message)
	}
}

func TestApplyFailsWhenLocalStatusUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	result := Apply(context.Background(), "http://"+addr, aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade})
	if result.Success {
		t.Fatal("expected failure when the local agent is unreachable")
	}
}
