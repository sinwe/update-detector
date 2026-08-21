package aptutil

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"update-detector/internal/checker"
)

// writeFakeAptGet puts a fake "apt-get" script at the front of PATH for
// the duration of the test, so Update's exec.Command call hits it instead
// of a real apt-get -- same technique as
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

func TestUpdateTapsRealOutputToLineSinkWhenPresent(t *testing.T) {
	writeFakeAptGet(t, "echo 'Get:1 http://archive.ubuntu.com noble InRelease'\nexit 0")

	var lines []string
	ctx := checker.WithLineSink(context.Background(), func(line string) { lines = append(lines, line) })

	if err := Update(ctx, "/dev/null"); err != nil {
		t.Fatal(err)
	}

	want := []string{"Get:1 http://archive.ubuntu.com noble InRelease"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got sink lines %v, want %v", lines, want)
	}
}

func TestUpdateUnchangedWithoutLineSink(t *testing.T) {
	writeFakeAptGet(t, "echo 'Get:1 http://archive.ubuntu.com noble InRelease'\nexit 0")

	if err := Update(context.Background(), "/dev/null"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateReturnsStderrOnFailureWithLineSinkAttached(t *testing.T) {
	writeFakeAptGet(t, "echo 'some progress noise'\necho 'E: real failure reason' >&2\nexit 1")

	ctx := checker.WithLineSink(context.Background(), func(string) {})

	err := Update(ctx, "/dev/null")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "real failure reason") {
		t.Fatalf("got error %q, want it to contain the real stderr failure reason", err.Error())
	}
}
