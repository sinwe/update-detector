//go:build windows

package windows

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// checkUpgradable shells out to winget and parses its table output (see
// packages_parse.go, deliberately untagged so that parsing logic is
// testable on any platform). winget itself may be entirely absent on
// locked-down or Server Windows machines -- that shows up as a normal
// exec error here, handled the same fallback-to-previous-value way
// every other subsystem's own exec failure already is.
func checkUpgradable(ctx context.Context) (packageResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "winget", "upgrade",
		"--include-unknown", "--accept-source-agreements", "--disable-interactivity")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return packageResult{}, fmt.Errorf("winget upgrade: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseWingetUpgrade(stdout.String())
}
