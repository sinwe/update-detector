//go:build windows

package windows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrWingetNotFound means winget itself isn't runnable from this
// process -- not a genuine error, since winget is an optional,
// supplementary signal for this checker (see windows.go's own Check),
// not its primary one. Most commonly: winget.exe is a per-user App
// Execution Alias, and this process is running as LocalSystem (the
// install.bat default) or some other account winget was never
// registered for -- confirmed live, this is the exact error Go's own
// exec.ErrNotFound produces in that case. Also covers winget being
// genuinely absent (locked-down or Server Windows).
var ErrWingetNotFound = errors.New("winget not found on PATH for this account")

// checkUpgradable shells out to winget and parses its table output (see
// packages_parse.go, deliberately untagged so that parsing logic is
// testable on any platform).
func checkUpgradable(ctx context.Context) (packageResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "winget", "upgrade",
		"--include-unknown", "--accept-source-agreements", "--disable-interactivity")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return packageResult{}, ErrWingetNotFound
		}
		return packageResult{}, fmt.Errorf("winget upgrade: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseWingetUpgrade(stdout.String())
}
