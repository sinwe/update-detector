//go:build darwin

package companion

import (
	"context"
	"fmt"
	"os/exec"
)

func init() {
	registerApplier(&brewApplier{})
}

// brewApplier is the sole registered Applier on macOS. Every operation
// starts with `brew update` (taps go stale the same way apt lists do),
// mirroring aptApplier's own `apt-get update` prologue -- including
// ErrUpdateFailed when the refresh itself fails, before anything is
// installed.
type brewApplier struct{}

// Packages runs:
//
//	brew update
//	brew upgrade <names...>
//
// Names may mix formulae and cask tokens -- `brew upgrade` accepts both
// in one invocation, matching what the macos checker reports.
func (b *brewApplier) Packages(ctx context.Context, names []string) (string, error) {
	if updateOut, err := runCapped(ctx, brewCommand(ctx, "update")); err != nil {
		return fmt.Sprintf("brew update failed: %v\n%s", err, updateOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	args := append([]string{"upgrade"}, names...)
	out, err := runCapped(ctx, brewCommand(ctx, args...))
	if err != nil {
		return out, err
	}
	return out, nil
}

// Upgrade runs:
//
//	brew update
//	brew upgrade
func (b *brewApplier) Upgrade(ctx context.Context) (string, error) {
	if updateOut, err := runCapped(ctx, brewCommand(ctx, "update")); err != nil {
		return fmt.Sprintf("brew update failed: %v\n%s", err, updateOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	return runCapped(ctx, brewCommand(ctx, "upgrade"))
}

// FullUpgrade runs:
//
//	brew update
//	brew upgrade --greedy
//
// --greedy additionally upgrades casks that declare auto_updates (which
// plain `brew upgrade` deliberately skips). That's the closest brew
// equivalent of apt's upgrade/dist-upgrade split: Upgrade is the safe,
// well-known default, FullUpgrade is the thorough pass that also clears
// the self-updating casks the checker reports as outdated.
func (b *brewApplier) FullUpgrade(ctx context.Context) (string, error) {
	if updateOut, err := runCapped(ctx, brewCommand(ctx, "update")); err != nil {
		return fmt.Sprintf("brew update failed: %v\n%s", err, updateOut), fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	return runCapped(ctx, brewCommand(ctx, "upgrade", "--greedy"))
}

// brewCommand builds a brew exec.Cmd. It is the sole place in the
// companion that knows brew's name, so tests can swap PATH to intercept
// all invocations. Names arrive already validated against the pending
// list by execute.go's own Apply, same trust posture as aptApplier.
func brewCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "brew", args...)
}
