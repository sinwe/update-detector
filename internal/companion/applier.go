package companion

import (
	"context"
	"errors"
	"fmt"
)

// ErrUpdateFailed indicates an Applier's own metadata-refresh prologue
// (e.g. apt-get update) failed before any actual install/upgrade command
// ran -- nothing on the host changed, so triggering a recheck afterward
// would be pointless. An Applier without such a prologue step at all
// (e.g. winget, which has no separate "refresh sources" command) simply
// never returns this; execute.go's Apply only special-cases it, it's
// never required.
var ErrUpdateFailed = errors.New("companion: package manager update failed")

// Applier abstracts the platform-specific package-apply operations so
// that execute.go's Apply can dispatch through it without any
// apt-get/winget knowledge of its own. Each platform package registers
// exactly one Applier via registerApplier from its init().
//
// Three explicit methods (rather than a single Apply(action) method)
// are deliberate: they make the winget upgrade/full-upgrade semantic
// gap a visible one-line fact in applier_winget.go (FullUpgrade just
// calls Upgrade), and keep missingFromPending -- which only applies to
// Packages -- in shared code rather than repeated per Applier.
type Applier interface {
	// Packages installs or upgrades exactly the named packages.
	Packages(ctx context.Context, names []string) (output string, err error)
	// Upgrade upgrades all currently-pending packages.
	Upgrade(ctx context.Context) (output string, err error)
	// FullUpgrade is a dist-upgrade / major-version step. On platforms
	// where there is no meaningful distinction (e.g. winget), this
	// behaves identically to Upgrade.
	FullUpgrade(ctx context.Context) (output string, err error)
}

// defaultApplier is set by exactly one platform package's init()
// via registerApplier.
var defaultApplier Applier

// registerApplier sets the process-wide Applier. Panics on a second
// call -- two platform packages registering at once is a build error,
// not a runtime condition, so failing loudly at startup is correct.
func registerApplier(a Applier) {
	if defaultApplier != nil {
		panic("companion: Applier already registered")
	}
	defaultApplier = a
}

// applierFor returns the registered defaultApplier, or an error if
// none has been registered (e.g. a platform build that forgot to
// blank-import its applier package).
func applierFor() (Applier, error) {
	if defaultApplier == nil {
		return nil, fmt.Errorf("companion: no Applier registered for this platform")
	}
	return defaultApplier, nil
}
