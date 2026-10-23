//go:build !windows

package macos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"update-detector/internal/checker"
)

// writeFakeExec puts a fake executable called name at the front of PATH.
func writeFakeExec(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCheckOutdatedTapsSink proves the verbose-recheck wiring: with a
// line sink attached, the real brew JSON lines reach the sink while the
// parsed result is unaffected. (Without the tee, macOS verbose rechecks
// streamed nothing at all -- the exact gap this covers.)
func TestCheckOutdatedTapsSink(t *testing.T) {
	raw, err := os.ReadFile("testdata/brew_outdated.json")
	if err != nil {
		t.Fatal(err)
	}
	writeFakeExec(t, "brew", `if [ "$1" = "update" ]; then exit 0; fi
cat <<'JSON'
`+string(raw)+`
JSON`)
	var lines []string
	ctx := checker.WithLineSink(context.Background(), func(s string) { lines = append(lines, s) })
	result, err := checkOutdated(ctx)
	if err != nil {
		t.Fatalf("checkOutdated: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("got total %d, want 3", result.Total)
	}
	if len(lines) == 0 {
		t.Fatal("expected brew JSON lines in the sink, got none")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"deno"`) {
		t.Fatalf("expected tapped lines to contain real brew output, got %q", joined)
	}
}

// TestCheckOutdatedWithoutSinkStaysSilent proves the tap is purely
// additive: no sink attached means no behavior change (the periodic
// background cycle always passes nil).
func TestCheckOutdatedWithoutSinkStaysSilent(t *testing.T) {
	raw, err := os.ReadFile("testdata/brew_outdated.json")
	if err != nil {
		t.Fatal(err)
	}
	writeFakeExec(t, "brew", `if [ "$1" = "update" ]; then exit 0; fi
cat <<'JSON'
`+string(raw)+`
JSON`)
	result, err := checkOutdated(context.Background())
	if err != nil {
		t.Fatalf("checkOutdated: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("got total %d, want 3", result.Total)
	}
}

// TestSoftwareUpdateListTapsSink proves the same wiring for the slow
// `softwareupdate --list` call.
func TestSoftwareUpdateListTapsSink(t *testing.T) {
	writeFakeExec(t, "softwareupdate", `echo 'Software Update found the following new or updated software:'
echo '* Label: macOS Tahoe 26.0.1-25A362'`)
	var lines []string
	ctx := checker.WithLineSink(context.Background(), func(s string) { lines = append(lines, s) })
	available, err := softwareUpdateList(ctx)
	if err != nil {
		t.Fatalf("softwareUpdateList: %v", err)
	}
	if !available {
		t.Fatal("expected available=true for a * Label: listing")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "macOS Tahoe") {
		t.Fatalf("expected tapped lines to contain real softwareupdate output, got %q", joined)
	}
}
