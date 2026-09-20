package macos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"update-detector/internal/checker"
)

type packageResult struct {
	Total    int
	Upgrades []checker.PackageUpgrade
}

// brewOutdatedEntry is the shared JSON shape of `brew outdated --json=v2`
// entries in both the "formulae" and the "casks" arrays: name, the
// installed version(s), and the newer version available.
type brewOutdatedEntry struct {
	Name              string   `json:"name"`
	InstalledVersions []string `json:"installed_versions"`
	CurrentVersion    string   `json:"current_version"`
	Pinned            bool     `json:"pinned"`
}

type brewOutdatedJSON struct {
	Formulae []brewOutdatedEntry `json:"formulae"`
	Casks    []brewOutdatedEntry `json:"casks"`
}

// checkOutdated runs `brew outdated --json=v2` and parses the result.
// Everything listed is by definition outdated (brew only lists what has
// an upgrade available), so no further filtering is needed beyond
// skipping pinned entries, which brew itself would refuse to upgrade.
func checkOutdated(ctx context.Context) (packageResult, error) {
	if _, err := exec.LookPath("brew"); err != nil {
		return packageResult{}, fmt.Errorf("brew not found on PATH: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "brew", "outdated", "--json=v2")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return packageResult{}, fmt.Errorf("brew outdated: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseBrewOutdated(stdout.Bytes())
}

// parseBrewOutdated converts `brew outdated --json=v2` output into
// package upgrades. Security is always false: Homebrew exposes no
// per-update severity signal at all (unlike apt's -security pocket or
// Windows Update's MSRC ratings), so every brew-sourced upgrade reports
// security: false -- the same documented limitation as winget-sourced
// results on Windows.
func parseBrewOutdated(raw []byte) (packageResult, error) {
	var out brewOutdatedJSON
	if err := json.Unmarshal(raw, &out); err != nil {
		return packageResult{}, fmt.Errorf("parsing brew outdated JSON: %w", err)
	}
	var result packageResult
	for _, entry := range append(out.Formulae, out.Casks...) {
		if entry.Pinned {
			continue
		}
		current := ""
		if len(entry.InstalledVersions) > 0 {
			current = entry.InstalledVersions[0]
		}
		result.Total++
		result.Upgrades = append(result.Upgrades, checker.PackageUpgrade{
			Name:             entry.Name,
			CurrentVersion:   current,
			CandidateVersion: entry.CurrentVersion,
			Security:         false,
		})
	}
	return result, nil
}
