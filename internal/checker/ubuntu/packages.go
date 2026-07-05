package ubuntu

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// aptCheckPath is Ubuntu's own upgradable-package counter, shipped by the
// update-notifier-common package. It already knows how to distinguish
// security updates from regular ones via python-apt, which is far more
// robust than re-parsing `apt-get -s dist-upgrade` output ourselves.
const aptCheckPath = "/usr/lib/update-notifier/apt-check"

type packageResult struct {
	Total    int
	Security int
	Names    []string
}

func runAptUpdate(ctx context.Context, aptConfigPath string) error {
	cmd := exec.CommandContext(ctx, "apt-get", "update", "-q", "-o", "Acquire::Retries=2")
	cmd.Env = aptEnv(aptConfigPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apt-get update: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// checkUpgradable calls apt-check twice: -p (--package-names) and the
// default counts mode are mutually exclusive in a single invocation — with
// -p, apt-check writes package names to stderr *instead of* "total;security",
// not in addition to it (confirmed by reading /usr/lib/update-notifier/apt-check).
func checkUpgradable(ctx context.Context, aptConfigPath string) (packageResult, error) {
	total, security, err := aptCheckCounts(ctx, aptConfigPath)
	if err != nil {
		return packageResult{}, err
	}

	names, err := aptCheckPackageNames(ctx, aptConfigPath)
	if err != nil {
		return packageResult{}, err
	}

	return packageResult{Total: total, Security: security, Names: names}, nil
}

func aptCheckCounts(ctx context.Context, aptConfigPath string) (total int, security int, err error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, aptCheckPath)
	cmd.Env = aptEnv(aptConfigPath)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("apt-check: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseAptCheckCounts(stderr.String())
}

func aptCheckPackageNames(ctx context.Context, aptConfigPath string) ([]string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, aptCheckPath, "--package-names")
	cmd.Env = aptEnv(aptConfigPath)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("apt-check --package-names: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parsePackageNames(stderr.String()), nil
}

// parseAptCheckCounts parses apt-check's stderr output, which is a single
// line of the form "total;security".
func parseAptCheckCounts(raw string) (total int, security int, err error) {
	line := strings.TrimSpace(raw)
	parts := strings.SplitN(line, ";", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("apt-check: unexpected output %q", raw)
	}
	total, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("apt-check: parsing total from %q: %w", raw, err)
	}
	security, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("apt-check: parsing security from %q: %w", raw, err)
	}
	return total, security, nil
}

// parsePackageNames parses apt-check --package-names's stdout output: one
// package name per line.
func parsePackageNames(raw string) []string {
	var names []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}

func aptEnv(aptConfigPath string) []string {
	env := append(os.Environ(),
		"APT_CONFIG="+aptConfigPath,
		"DEBIAN_FRONTEND=noninteractive",
	)
	return env
}
