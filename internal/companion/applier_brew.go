//go:build darwin

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"syscall"
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
//
// brew itself refuses to run as root, but the companion daemon runs as
// root on macOS (same as on Linux -- needed so self-update can
// re-invoke install.sh) -- so brew always runs via `sudo -u <owner>`,
// where owner is BREW_OWNER when set (see install.sh's own
// install_companion_launchd) or the brew binary's own file owner
// otherwise. Root-to-user sudo never prompts, so this stays
// non-interactive.
func brewCommand(ctx context.Context, args ...string) *exec.Cmd {
	if owner := brewOwner(); owner != "" {
		return exec.CommandContext(ctx, "sudo", append([]string{"-u", owner, "brew"}, args...)...)
	}
	return exec.CommandContext(ctx, "brew", args...)
}

// brewOwner resolves which user brew must run as: BREW_OWNER from the
// companion's own environment when set (explicit, overridable -- empty
// means run brew directly with no sudo), else the owner of the brew
// binary itself (Homebrew requires its prefix to be user-owned, so this
// is always the account brew works for). Empty only when brew isn't
// resolvable at all -- in which case the brew invocation itself fails
// with a clear LookPath-style error either way.
func brewOwner() string {
	if owner, ok := os.LookupEnv("BREW_OWNER"); ok {
		return owner
	}
	path, err := exec.LookPath("brew")
	if err != nil {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	u, err := user.LookupId(fmt.Sprint(st.Uid))
	if err != nil {
		return ""
	}
	return u.Username
}
