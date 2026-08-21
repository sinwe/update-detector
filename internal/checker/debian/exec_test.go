//go:build !windows

package debian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"update-detector/internal/checker"
)

// writeFakeAptGet puts a fake "apt-get" script at the front of PATH for
// the duration of the test, so checkUpgradable's exec.Command call hits
// it instead of a real apt-get -- same technique as
// internal/companion/execute_test.go's writeFakeAptGet.
func writeFakeAptGet(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "apt-get")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCheckUpgradableTapsRealOutputToLineSinkWhenPresent(t *testing.T) {
	writeFakeAptGet(t, "cat <<'EOF'\n"+sampleDistUpgradeOutput+"EOF")

	var lines []string
	ctx := checker.WithLineSink(context.Background(), func(line string) { lines = append(lines, line) })

	result, err := checkUpgradable(ctx, "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 5 {
		t.Fatalf("got Total=%d, want 5 -- attaching a sink must not change parsed output: %#v", result.Total, result)
	}

	want := strings.Split(strings.TrimSuffix(sampleDistUpgradeOutput, "\n"), "\n")
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got sink lines %v, want %v", lines, want)
	}
}

func TestCheckUpgradableUnchangedWithoutLineSink(t *testing.T) {
	writeFakeAptGet(t, "cat <<'EOF'\n"+sampleDistUpgradeOutput+"EOF")

	result, err := checkUpgradable(context.Background(), "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 5 {
		t.Fatalf("got Total=%d, want 5: %#v", result.Total, result)
	}
}
