// Package reboot checks for a pending-reboot marker, shared across checker
// flavors (the mechanism is Ubuntu/update-notifier's convention, but any
// flavor's host may have it present).
package reboot

import (
	"fmt"
	"os"
	"strings"
)

// Check mirrors how update-notifier decides whether a reboot is pending:
// the mere existence of /var/run/reboot-required means yes, and the sibling
// .pkgs file (if present) lists which packages triggered it. Not every
// distro populates this file (e.g. plain Debian/Raspberry Pi OS without
// update-notifier-style tooling installed) — on those, a clean "false" here
// is best-effort, not authoritative.
func Check(rebootRequiredFile string) (required bool, packages []string, err error) {
	_, statErr := os.Stat(rebootRequiredFile)
	switch {
	case statErr == nil:
		required = true
	case os.IsNotExist(statErr):
		return false, nil, nil
	default:
		return false, nil, fmt.Errorf("reboot-required: stat %s: %w", rebootRequiredFile, statErr)
	}

	pkgsFile := rebootRequiredFile + ".pkgs"
	data, readErr := os.ReadFile(pkgsFile)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return required, nil, nil
		}
		return required, nil, fmt.Errorf("reboot-required: reading %s: %w", pkgsFile, readErr)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			packages = append(packages, line)
		}
	}
	return required, packages, nil
}
