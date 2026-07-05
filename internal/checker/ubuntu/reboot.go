package ubuntu

import (
	"fmt"
	"os"
	"strings"
)

// checkRebootRequired mirrors how update-notifier decides whether a reboot
// is pending: the mere existence of /var/run/reboot-required means yes, and
// the sibling .pkgs file (if present) lists which packages triggered it.
func checkRebootRequired(rebootRequiredFile string) (required bool, packages []string, err error) {
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
