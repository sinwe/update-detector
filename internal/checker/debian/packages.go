//go:build !windows

package debian

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"update-detector/internal/aptutil"
	"update-detector/internal/checker"
)

type packageResult struct {
	Total    int
	Security int
	Upgrades []checker.PackageUpgrade
}

// instLineRE matches `apt-get -s dist-upgrade`'s simulated "Inst" lines, e.g.:
//
//	Inst docker-compose-plugin [5.2.0-1~debian.13~trixie] (5.3.0-1~debian.13~trixie Docker CE:trixie [arm64])
//	Inst libpisp1 [1.5.0-1] (1.6.0-1 Raspberry Pi Foundation:stable [arm64]) []
//
// Group 1: name, group 2: current version (absent for new-install lines
// pulled in as dependencies, which this ignores), group 3: candidate
// version, group 4: origin/archive/arch info.
var instLineRE = regexp.MustCompile(`^Inst\s+(\S+)(?:\s+\[([^\]]+)\])?\s+\(([^\s]+)\s+([^)]*)\)`)

func checkUpgradable(ctx context.Context, aptConfigPath string) (packageResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "apt-get", "-s", "dist-upgrade")
	cmd.Env = aptutil.Env(aptConfigPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return packageResult{}, fmt.Errorf("apt-get -s dist-upgrade: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseDistUpgrade(stdout.String()), nil
}

// parseDistUpgrade parses apt-get -s dist-upgrade's "Inst" lines for
// packages that are upgrades to an already-installed version (i.e. have a
// "[current-version]" bracket) — lines without one are new installs pulled
// in as dependencies, not upgrades, and are skipped. Security updates are
// identified by "-security" appearing in the origin/archive field (e.g.
// Debian's "trixie-security"), the same naming convention Ubuntu uses,
// since there's no apt-check equivalent on Debian to classify them for us.
func parseDistUpgrade(raw string) packageResult {
	var result packageResult
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		m := instLineRE.FindStringSubmatch(line)
		if m == nil || m[2] == "" {
			continue
		}
		name, current, candidate, origin := m[1], m[2], m[3], m[4]
		isSecurity := strings.Contains(strings.ToLower(origin), "-security")

		result.Total++
		if isSecurity {
			result.Security++
		}
		result.Upgrades = append(result.Upgrades, checker.PackageUpgrade{
			Name:             name,
			CurrentVersion:   current,
			CandidateVersion: candidate,
			Security:         isSecurity,
		})
	}
	return result
}
