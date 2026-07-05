package state

import (
	"fmt"

	"update-detector/internal/checker"
)

// Diff computes a human-readable list of notification-worthy changes
// between previous and current. previous may be nil (first run since the
// state file didn't exist yet), in which case nothing is reported — we
// don't want a notification storm reporting the entire baseline as "new".
func Diff(previous *checker.Status, current checker.Status) []string {
	if previous == nil {
		return nil
	}

	var changes []string

	if delta := current.Packages.UpgradableTotal - previous.Packages.UpgradableTotal; delta > 0 {
		changes = append(changes, fmt.Sprintf(
			"%d new package update(s) available (total now %d)",
			delta, current.Packages.UpgradableTotal,
		))
	}

	if delta := current.Packages.UpgradableSecurity - previous.Packages.UpgradableSecurity; delta > 0 {
		changes = append(changes, fmt.Sprintf(
			"%d new security update(s) available (total now %d)",
			delta, current.Packages.UpgradableSecurity,
		))
	}

	if current.RebootRequired && !previous.RebootRequired {
		changes = append(changes, "system now requires a reboot")
	}

	if current.OS.UpdateAvailable && !previous.OS.UpdateAvailable {
		changes = append(changes, fmt.Sprintf(
			"OS release upgrade now available: %s -> %s",
			current.OS.CurrentVersion, current.OS.LatestVersion,
		))
	}

	return changes
}
