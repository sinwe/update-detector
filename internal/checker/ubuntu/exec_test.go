//go:build !windows

package ubuntu

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"update-detector/internal/checker"
)

// writeFakeApt puts a fake "apt" script at the front of PATH for the
// duration of the test, so aptListUpgradable's exec.Command call hits it
// instead of a real apt -- same technique as
// internal/companion/execute_test.go's writeFakeAptGet.
func writeFakeApt(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "apt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fakeAptListOutput = "Listing...\n" +
	"docker-compose-plugin/noble 5.3.0-1~ubuntu.24.04~noble amd64 [upgradable from: 5.2.0-1~ubuntu.24.04~noble]\n"

func TestAptListUpgradableTapsRealOutputToLineSinkWhenPresent(t *testing.T) {
	writeFakeApt(t, "printf '"+fakeAptListOutput+"'")

	var lines []string
	ctx := checker.WithLineSink(context.Background(), func(line string) { lines = append(lines, line) })

	upgrades, err := aptListUpgradable(ctx, "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if len(upgrades) != 1 || upgrades[0].Name != "docker-compose-plugin" {
		t.Fatalf("got %#v, want one docker-compose-plugin upgrade -- attaching a sink must not change parsed output", upgrades)
	}

	want := []string{"Listing...", "docker-compose-plugin/noble 5.3.0-1~ubuntu.24.04~noble amd64 [upgradable from: 5.2.0-1~ubuntu.24.04~noble]"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got sink lines %v, want %v", lines, want)
	}
}

func TestAptListUpgradableUnchangedWithoutLineSink(t *testing.T) {
	writeFakeApt(t, "printf '"+fakeAptListOutput+"'")

	upgrades, err := aptListUpgradable(context.Background(), "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if len(upgrades) != 1 || upgrades[0].Name != "docker-compose-plugin" {
		t.Fatalf("got %#v, want one docker-compose-plugin upgrade", upgrades)
	}
}
