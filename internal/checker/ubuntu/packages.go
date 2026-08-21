//go:build !windows

package ubuntu

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"update-detector/internal/aptutil"
	"update-detector/internal/checker"
	"update-detector/internal/linetee"
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

// aptCheckCounts's real content is on stderr (see parseAptCheckCounts) --
// stdout is unused, so only stderr needs a sink tap; a single tapped
// writer value is enough (no cross-stream merge, so no risk of a
// diagnostic line landing where parsing doesn't expect it).
func aptCheckCounts(ctx context.Context, aptConfigPath string) (total int, security int, err error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, aptCheckPath)
	cmd.Env = aptutil.Env(aptConfigPath)
	var errOut io.Writer = &stderr
	if sink := checker.LineSinkFromContext(ctx); sink != nil {
		tee := linetee.New(&stderr, sink)
		defer tee.Flush()
		errOut = tee
	}
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("apt-check: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseAptCheckCounts(stderr.String())
}

// Only stdout (the parsed list itself) is tapped when a sink is present --
// stderr keeps its own independent, un-tapped buffer exactly as before.
// Tapping both would mean sharing one writer value across both streams
// (see internal/aptutil.Update's own comment on why), which here would
// also merge stderr's diagnostic text into the very stdout buffer
// parseUpgradableList parses -- a real risk of breaking parsing during a
// verbose recheck specifically, not just a cosmetic trade-off, so it's
// deliberately avoided.
func aptListUpgradable(ctx context.Context, aptConfigPath string) ([]checker.PackageUpgrade, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "apt", "list", "--upgradable")
	cmd.Env = aptutil.Env(aptConfigPath)
	var out io.Writer = &stdout
	if sink := checker.LineSinkFromContext(ctx); sink != nil {
		tee := linetee.New(&stdout, sink)
		defer tee.Flush()
		out = tee
	}
	cmd.Stdout = out
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

		nameField := fields[0]
		name, archive := nameField, ""
		if idx := strings.Index(nameField, "/"); idx >= 0 {
			name, archive = nameField[:idx], nameField[idx+1:]
		}

		u := checker.PackageUpgrade{
			Name:             name,
			CandidateVersion: fields[1],
			Security:         strings.Contains(strings.ToLower(archive), "-security"),
		}
		const marker = "[upgradable from: "
		if idx := strings.Index(line, marker); idx >= 0 {
			u.CurrentVersion = strings.TrimSuffix(line[idx+len(marker):], "]")
		}
		upgrades = append(upgrades, u)
	}
	return upgrades
}
