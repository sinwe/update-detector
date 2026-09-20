//go:build darwin

package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeBrew puts a fake "brew" script at the front of PATH for the
// duration of the test, same pattern as execute_test.go's own
// writeFakeAptGet.
func writeFakeBrew(t *testing.T, script string) {
	t.Helper()
	writeFakeExec(t, "brew", script)
}

// writeFakeExec puts a fake executable called name at the front of PATH.
func writeFakeExec(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// All tests below pin BREW_OWNER="" (set-but-empty: run brew directly,
// no sudo) for determinism -- otherwise brewOwner's own stat fallback
// would resolve the real brew owner on the test host and wrap every
// invocation in a real sudo. The sudo-wrapping mode gets its own test
// with a fake sudo on PATH.
func TestBrewPackagesRunsUpdateThenUpgrade(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	callLog := filepath.Join(t.TempDir(), "calls")
	writeFakeBrew(t, `echo "$@" >> `+callLog+`
exit 0`)
	applier, err := applierFor()
	if err != nil {
		t.Fatalf("applierFor: %v", err)
	}
	if _, ok := applier.(*brewApplier); !ok {
		t.Fatalf("got %T, want *brewApplier (darwin must register brew, not apt)", applier)
	}
	if _, err := applier.Packages(context.Background(), []string{"deno", "ffmpeg"}); err != nil {
		t.Fatalf("Packages: %v", err)
	}
	raw, _ := os.ReadFile(callLog)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "update" || lines[1] != "upgrade deno ffmpeg" {
		t.Fatalf("got brew calls %q, want [update] then [upgrade deno ffmpeg]", lines)
	}
}

func TestBrewUpgradeRunsUpdateThenUpgradeAll(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	callLog := filepath.Join(t.TempDir(), "calls")
	writeFakeBrew(t, `echo "$@" >> `+callLog+`
exit 0`)
	applier, err := applierFor()
	if err != nil {
		t.Fatalf("applierFor: %v", err)
	}
	if _, err := applier.Upgrade(context.Background()); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	raw, _ := os.ReadFile(callLog)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "update" || lines[1] != "upgrade" {
		t.Fatalf("got brew calls %q, want [update] then [upgrade]", lines)
	}
}

func TestBrewFullUpgradeAddsGreedy(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	callLog := filepath.Join(t.TempDir(), "calls")
	writeFakeBrew(t, `echo "$@" >> `+callLog+`
exit 0`)
	applier, err := applierFor()
	if err != nil {
		t.Fatalf("applierFor: %v", err)
	}
	if _, err := applier.FullUpgrade(context.Background()); err != nil {
		t.Fatalf("FullUpgrade: %v", err)
	}
	raw, _ := os.ReadFile(callLog)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "update" || lines[1] != "upgrade --greedy" {
		t.Fatalf("got brew calls %q, want [update] then [upgrade --greedy]", lines)
	}
}

func TestBrewUpdateFailureAbortsBeforeUpgrade(t *testing.T) {
	t.Setenv("BREW_OWNER", "")
	callLog := filepath.Join(t.TempDir(), "calls")
	writeFakeBrew(t, `echo "$@" >> `+callLog+`
if [ "$1" = "update" ]; then echo boom >&2; exit 1; fi
exit 0`)
	applier, err := applierFor()
	if err != nil {
		t.Fatalf("applierFor: %v", err)
	}
	if _, err := applier.Packages(context.Background(), []string{"deno"}); !errors.Is(err, ErrUpdateFailed) {
		t.Fatalf("got %v, want ErrUpdateFailed", err)
	}
	raw, _ := os.ReadFile(callLog)
	if lines := strings.Split(strings.TrimSpace(string(raw)), "\n"); len(lines) != 1 || lines[0] != "update" {
		t.Fatalf("got brew calls %q, want only [update] (no upgrade after refresh failure)", lines)
	}
}

func TestBrewWrapsSudoWhenOwnerSet(t *testing.T) {
	t.Setenv("BREW_OWNER", "someowner")
	callLog := filepath.Join(t.TempDir(), "calls")
	writeFakeExec(t, "sudo", `echo "$@" >> `+callLog+`
exit 0`)
	applier, err := applierFor()
	if err != nil {
		t.Fatalf("applierFor: %v", err)
	}
	if _, err := applier.Packages(context.Background(), []string{"deno"}); err != nil {
		t.Fatalf("Packages: %v", err)
	}
	raw, _ := os.ReadFile(callLog)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "-u someowner brew update" || lines[1] != "-u someowner brew upgrade deno" {
		t.Fatalf("got sudo calls %q, want sudo -u wrapping every brew invocation", lines)
	}
}
