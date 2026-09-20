package macos

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"update-detector/internal/checker"
)

// softwareUpdateTimeout bounds `softwareupdate --list`, which phones home
// to Apple's catalog and can stall on a bad network -- a stuck check must
// not wedge the agent's whole detection cycle.
const softwareUpdateTimeout = 90 * time.Second

// readOSInfo combines `sw_vers` (current OS name/version, always local
// and fast) with `softwareupdate --list` (whether Apple offers anything
// newer, network-dependent and best-effort).
func readOSInfo(ctx context.Context) (checker.OSInfo, error) {
	name, version, err := swVers(ctx)
	if err != nil {
		return checker.OSInfo{}, err
	 }
	info := checker.OSInfo{
		CurrentVersion:  version,
		CurrentCodename: name,
		UpdateAvailable: false,
	}
	available, err := softwareUpdateList(ctx)
	if err != nil {
		return checker.OSInfo{}, err
	}
	info.UpdateAvailable = available
	return info, nil
}

// swVers runs `sw_vers` and returns its ProductName and ProductVersion
// (e.g. "macOS", "27.0").
func swVers(ctx context.Context) (name, version string, err error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sw_vers")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("sw_vers: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseSwVers(stdout.String())
}

// parseSwVers parses `sw_vers` output, which is tab-separated
// "Key:\tvalue" lines:
//
//	ProductName:		macOS
//	ProductVersion:		27.0
//	BuildVersion:		26A428
func parseSwVers(raw string) (name, version string, err error) {
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ProductName":
			name = strings.TrimSpace(value)
		case "ProductVersion":
			version = strings.TrimSpace(value)
		}
	}
	if name == "" || version == "" {
		return "", "", fmt.Errorf("sw_vers: could not find ProductName/ProductVersion in %q", raw)
	}
	return name, version, nil
}

// softwareUpdateList runs `softwareupdate --list` and reports whether
// Apple has any OS update on offer for this host.
func softwareUpdateList(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, softwareUpdateTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "softwareupdate", "--list")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("softwareupdate --list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseSoftwareUpdateList(stdout.String()), nil
}

// parseSoftwareUpdateList reports true when `softwareupdate --list`
// offers something. With updates pending it prints a
// "found the following new or updated software" section whose entries
// look like:
//
//	* Label: macOS Tahoe 26.0.1-25A362
//
// With nothing pending it prints "No new software available." instead.
func parseSoftwareUpdateList(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "* Label:") {
			return true
		}
	}
	return false
}
