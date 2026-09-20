//go:build !windows

package companion

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeFakeInstallSh puts a fake install.sh at installShPath for the
// duration of the test, logging INSTALL_COMPONENTS/INSTALL_VERSION to
// callLog (one line per invocation) and exiting with exitCode. Shared by
// the selfupdate and execute tests (both !windows); the per-platform
// config-discovery tests live in selfupdate_test.go (linux) and
// selfupdate_darwin_test.go (darwin) respectively.
func writeFakeInstallSh(t *testing.T, callLog string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	script := `#!/bin/sh
echo "$INSTALL_COMPONENTS $INSTALL_VERSION" >> "` + callLog + `"
exit ` + strconv.Itoa(exitCode) + `
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := installShPath
	installShPath = path
	t.Cleanup(func() { installShPath = orig })
}
