package checker

import (
	"context"
	"sync/atomic"
)

// CVEEnabled is the runtime gate for CVE/vulnerability detection. Off by
// default (see config.CVE_ENABLED). The aggregator's per-host toggle
// updates it via SetCVEEnabled so detection can be turned on/off without
// restarting the agent. Stored atomically so the check loop and the
// report path don't race.
var cveEnabled atomic.Bool

// SetCVEEnabled updates the runtime gate.
func SetCVEEnabled(v bool) { cveEnabled.Store(v) }

// CVEEnabled reports whether CVE detection should run this cycle.
func CVEEnabled() bool { return cveEnabled.Load() }

// Checker detects available updates for one OS platform. Implementations
// live in sibling packages (e.g. ubuntu), keyed by their own dependencies;
// this interface is the only thing main.go, httpserver, notifier, and state
// need to know about.
//
// previous is the last known-good Status (nil on the very first check) so an
// implementation can fall back to previously observed values for any
// subsystem it fails to check this cycle, instead of reporting a false zero
// value.
type Checker interface {
	Platform() string
	Check(ctx context.Context, previous *Status) (Status, error)
}
