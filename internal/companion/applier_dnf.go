//go:build linux

package companion

import (
	"context"
	"os/exec"
)

func init() {
	if _, err := exec.LookPath("dnf"); err != nil {
		return
	}
	registerApplier(&dnfApplier{})
}

type dnfApplier struct{}

// Packages runs `dnf upgrade -y <names>` (dnf has no separate update step
// required before upgrading named packages; metadata is refreshed inline).
func (a *dnfApplier) Packages(ctx context.Context, names []string) (string, error) {
	args := append([]string{"upgrade", "-y"}, names...)
	out, err := runCapped(ctx, dnfCommand(ctx, args...))
	if err != nil {
		return out, err
	}
	return out, nil
}

// Upgrade runs `dnf upgrade -y`.
func (a *dnfApplier) Upgrade(ctx context.Context) (string, error) {
	out, err := runCapped(ctx, dnfCommand(ctx, "upgrade", "-y"))
	if err != nil {
		return out, err
	}
	return out, nil
}

// FullUpgrade runs `dnf distro-sync -y` (the closest dnf equivalent of
// apt's dist-upgrade).
func (a *dnfApplier) FullUpgrade(ctx context.Context) (string, error) {
	out, err := runCapped(ctx, dnfCommand(ctx, "distro-sync", "-y"))
	if err != nil {
		return out, err
	}
	return out, nil
}

func dnfCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "dnf", args...)
}
