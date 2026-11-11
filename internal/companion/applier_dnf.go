//go:build linux

package companion

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func init() {
	if _, err := exec.LookPath("dnf"); err != nil {
		return
	}
	registerApplier(&dnfApplier{})
}

type dnfApplier struct{}

// Packages runs:
//
//	dnf makecache
//	dnf upgrade -y <names...>
//	dnf autoremove -y   (best-effort, never gates the result)
func (a *dnfApplier) Packages(ctx context.Context, names []string) (string, error) {
	if cacheOut, err := runCapped(ctx, dnfCommand(ctx, "makecache")); err != nil {
		return fmt.Sprintf("dnf makecache failed: %v\n%s", err, cacheOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	args := append([]string{"upgrade", "-y"}, names...)
	out, err := runCapped(ctx, dnfCommand(ctx, args...))
	if err != nil {
		return out, err
	}
	return a.autoremove(ctx, out), nil
}

// Upgrade runs:
//
//	dnf makecache
//	dnf upgrade -y
//	dnf autoremove -y   (best-effort)
func (a *dnfApplier) Upgrade(ctx context.Context) (string, error) {
	if cacheOut, err := runCapped(ctx, dnfCommand(ctx, "makecache")); err != nil {
		return fmt.Sprintf("dnf makecache failed: %v\n%s", err, cacheOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	out, err := runCapped(ctx, dnfCommand(ctx, "upgrade", "-y"))
	if err != nil {
		return out, err
	}
	return a.autoremove(ctx, out), nil
}

// FullUpgrade runs:
//
//	dnf makecache
//	dnf distro-sync -y
//	dnf autoremove -y   (best-effort)
func (a *dnfApplier) FullUpgrade(ctx context.Context) (string, error) {
	if cacheOut, err := runCapped(ctx, dnfCommand(ctx, "makecache")); err != nil {
		return fmt.Sprintf("dnf makecache failed: %v\n%s", err, cacheOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	out, err := runCapped(ctx, dnfCommand(ctx, "distro-sync", "-y"))
	if err != nil {
		return out, err
	}
	return a.autoremove(ctx, out), nil
}

// autoremove appends a best-effort dnf autoremove to existing output.
// Failure is noted in the returned string but never returned as an error --
// an upgrade that already succeeded should not be reported as failed because
// autoremove hit a problem.
func (a *dnfApplier) autoremove(ctx context.Context, prev string) string {
	out, err := runCapped(ctx, dnfCommand(ctx, "autoremove", "-y"))
	if err != nil {
		return strings.TrimRight(prev, "\n") + fmt.Sprintf("\ndnf autoremove failed: %v\n%s", err, out)
	}
	return strings.TrimRight(prev, "\n") + "\n" + out
}

// dnfCommand builds a dnf exec.Cmd. It is the sole place in the companion
// that knows dnf's name, so tests can swap PATH to intercept all invocations.
func dnfCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "dnf", args...)
}
