package checker

import "context"

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
