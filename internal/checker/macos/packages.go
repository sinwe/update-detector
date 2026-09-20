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

// checkOutdated refreshes Homebrew's own data (`brew update`) and then
// reports what's upgradable (`brew outdated --json=v2`). The refresh is
// the brew equivalent of the apt checkers' `apt-get update` prologue:
// taps go stale the same way apt lists do, and an update failure fails
// this check (falling back to the previous cycle's list) rather than
// silently reporting from stale data. `brew update` only refreshes
// Homebrew itself and its taps -- it never upgrades installed packages,
// so this checker stays read-only like every other checker.
func checkOutdated(ctx context.Context) (packageResult, error) {
	if _, err := exec.LookPath("brew"); err != nil {
		return packageResult{}, fmt.Errorf("brew not found on PATH: %w", err)
	}
	var updateOut, updateErr bytes.Buffer
	updateCmd := exec.CommandContext(ctx, "brew", "update")
	updateCmd.Stdout = &updateOut
	updateCmd.Stderr = &updateErr
	if err := updateCmd.Run(); err != nil {
		return packageResult{}, fmt.Errorf("brew update: %w: %s", err, strings.TrimSpace(updateErr.String()))
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
