//go:build windows

package companion

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// wingetApplier is not independently registered -- windowsApplier (see
// applier_windows.go) is the sole registered Applier for this platform,
// delegating to this type for names it doesn't recognize as a Windows
// Update KB (see kbPattern), the same optional/supplementary role
// winget already has on the detection side (see
// internal/checker/windows/windows.go's own Check).
//
// Every invocation below includes --disable-interactivity, matching the
// detection side's own already-working winget command (see
// internal/checker/windows/packages.go) -- confirmed live, without it
// the companion (always running as a Windows Service, so stdin is never
// a real console) hit `ERROR: Input redirection is not supported,
// exiting the process immediately.` from winget itself.
// --accept-package-agreements/--accept-source-agreements alone aren't
// enough to guarantee winget never falls back to trying to prompt.
type wingetApplier struct{}

// Packages upgrades each named package individually via:
//
//	winget upgrade --id <name> --silent --accept-package-agreements --accept-source-agreements --disable-interactivity
//
// Winget has no batch form, so packages are applied one at a time.
// All output is collected; the first failure aborts the loop.
func (w *wingetApplier) Packages(ctx context.Context, names []string) (string, error) {
	var combined strings.Builder
	for _, name := range names {
		out, err := runCapped(ctx, wingetCommand(ctx,
			"upgrade",
			"--id", name,
			"--silent",
			"--accept-package-agreements",
			"--accept-source-agreements",
			"--disable-interactivity",
		))
		combined.WriteString(out)
		if err != nil {
			return combined.String(), fmt.Errorf("winget upgrade --id %s: %w", name, err)
		}
	}
	return combined.String(), nil
}

// Upgrade runs:
//
//	winget upgrade --all --silent --accept-package-agreements --accept-source-agreements --disable-interactivity
func (w *wingetApplier) Upgrade(ctx context.Context) (string, error) {
	return runCapped(ctx, wingetCommand(ctx,
		"upgrade", "--all",
		"--silent",
		"--accept-package-agreements",
		"--accept-source-agreements",
		"--disable-interactivity",
	))
}

// FullUpgrade is identical to Upgrade on Windows -- winget has no
// dist-upgrade / major-version-step equivalent. ActionFullUpgrade
// therefore behaves identically to ActionUpgrade here; this is a real
// semantic gap, not an oversight, and is called out explicitly rather
// than silently hidden.
func (w *wingetApplier) FullUpgrade(ctx context.Context) (string, error) {
	return w.Upgrade(ctx)
}

// wingetCommand builds a winget exec.Cmd.
func wingetCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "winget", args...)
}
