//go:build !windows

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func init() {
	registerApplier(&aptApplier{})
}

type aptApplier struct{}

// Packages runs:
//
//	apt-get update
//	apt-get install -y --only-upgrade <names...>
//	apt-get autoremove -y   (best-effort, never gates the result)
func (a *aptApplier) Packages(ctx context.Context, names []string) (string, error) {
	if updateOut, err := runCapped(ctx, aptCommand(ctx, "update")); err != nil {
		return fmt.Sprintf("apt-get update failed: %v\n%s", err, updateOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	args := append([]string{"install", "-y", "--only-upgrade"}, names...)
	out, err := runCapped(ctx, aptCommand(ctx, args...))
	if err != nil {
		return out, err
	}
	return a.autoremove(ctx, out), nil
}

// Upgrade runs:
//
//	apt-get update
//	apt-get upgrade -y
//	apt-get autoremove -y   (best-effort)
func (a *aptApplier) Upgrade(ctx context.Context) (string, error) {
	if updateOut, err := runCapped(ctx, aptCommand(ctx, "update")); err != nil {
		return fmt.Sprintf("apt-get update failed: %v\n%s", err, updateOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	out, err := runCapped(ctx, aptCommand(ctx, "upgrade", "-y"))
	if err != nil {
		return out, err
	}
	return a.autoremove(ctx, out), nil
}

// FullUpgrade runs:
//
//	apt-get update
//	apt-get dist-upgrade -y
//	apt-get autoremove -y   (best-effort)
func (a *aptApplier) FullUpgrade(ctx context.Context) (string, error) {
	if updateOut, err := runCapped(ctx, aptCommand(ctx, "update")); err != nil {
		return fmt.Sprintf("apt-get update failed: %v\n%s", err, updateOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	out, err := runCapped(ctx, aptCommand(ctx, "dist-upgrade", "-y"))
	if err != nil {
		return out, err
	}
	return a.autoremove(ctx, out), nil
}

// autoremove appends a best-effort apt-get autoremove to existing output.
// Failure is noted in the returned string but never returned as an error --
// an upgrade that already succeeded should not be reported as failed because
// autoremove hit a problem.
func (a *aptApplier) autoremove(ctx context.Context, prev string) string {
	out, err := runCapped(ctx, aptCommand(ctx, "autoremove", "-y"))
	if err != nil {
		return strings.TrimRight(prev, "\n") + fmt.Sprintf("\napt-get autoremove failed: %v\n%s", err, out)
	}
	return strings.TrimRight(prev, "\n") + "\n" + out
}

// aptCommand builds an apt-get exec.Cmd with DEBIAN_FRONTEND=noninteractive
// injected. It is the sole place in the companion that knows apt-get's name,
// so tests can swap PATH to intercept all invocations.
func aptCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "apt-get", args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	return cmd
}
