package ubuntu

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"update-detector/internal/checker"
)

// aptCheckPath is Ubuntu's own upgradable-package counter, shipped by the
// update-notifier-common package. It already knows how to distinguish
// security updates from regular ones via python-apt, which is far more
// robust than re-parsing `apt-get -s dist-upgrade` output ourselves. It
// can't report version numbers though, so `apt list --upgradable` covers
// that separately.
const aptCheckPath = "/usr/lib/update-notifier/apt-check"

type packageResult struct {
	Total    int
	Security int
	Upgrades []checker.PackageUpgrade
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

// checkUpgradable gets counts from apt-check (python-apt's own
// security-vs-regular classification) and names+versions from
// `apt list --upgradable` (apt-check has no flag that reports versions).
func checkUpgradable(ctx context.Context, aptConfigPath string) (packageResult, error) {
	total, security, err := aptCheckCounts(ctx, aptConfigPath)
	if err != nil {
		return packageResult{}, err
	}

	upgrades, err := aptListUpgradable(ctx, aptConfigPath)
	if err != nil {
		return packageResult{}, err
	}

	return packageResult{Total: total, Security: security, Upgrades: upgrades}, nil
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

func aptListUpgradable(ctx context.Context, aptConfigPath string) ([]checker.PackageUpgrade, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "apt", "list", "--upgradable")
	cmd.Env = aptEnv(aptConfigPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("apt list --upgradable: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseUpgradableList(stdout.String()), nil
}

// parseAptCheckCounts parses apt-check's stderr output. The final line is
// "total;security", but apt-check (a Python script) can print diagnostic
// warnings to stderr before it — e.g. about a missing
// /var/lib/ubuntu-advantage directory — so only the last non-empty line is
// treated as the counts; everything before it is ignored. Confirmed by
// hitting this exact case against a real host: without this, a transient
// warning line made parsing fail and silently fall back to a stale
// previously-cached count.
func parseAptCheckCounts(raw string) (total int, security int, err error) {
	trimmed := strings.TrimSpace(raw)
	lines := strings.Split(trimmed, "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
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

// parseUpgradableList parses `apt list --upgradable` output, e.g.:
//
//	docker-compose-plugin/noble 5.3.0-1~ubuntu.24.04~noble amd64 [upgradable from: 5.2.0-1~ubuntu.24.04~noble]
//
// apt itself warns this format isn't a stable CLI contract, but it's the
// only source of upgrade version numbers available without linking against
// python-apt directly, and the format has been stable for years in practice.
func parseUpgradableList(raw string) []checker.PackageUpgrade {
	var upgrades []checker.PackageUpgrade
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing...") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name := fields[0]
		if idx := strings.Index(name, "/"); idx >= 0 {
			name = name[:idx]
		}

		u := checker.PackageUpgrade{Name: name, CandidateVersion: fields[1]}
		const marker = "[upgradable from: "
		if idx := strings.Index(line, marker); idx >= 0 {
			u.CurrentVersion = strings.TrimSuffix(line[idx+len(marker):], "]")
		}
		upgrades = append(upgrades, u)
	}
	return upgrades
}

func aptEnv(aptConfigPath string) []string {
	env := append(os.Environ(),
		"APT_CONFIG="+aptConfigPath,
		"DEBIAN_FRONTEND=noninteractive",
	)
	return env
}
