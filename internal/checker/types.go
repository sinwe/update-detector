// Package checker defines the platform-agnostic detection result and the
// interface each OS-specific implementation (ubuntu, and later macos/windows)
// must satisfy.
package checker

import "time"

// Status is the JSON shape served at GET /status for Gatus to poll, and the
// shape persisted to disk so restarts can diff against the last known state.
type Status struct {
	Hostname  string    `json:"hostname"`
	Platform  string    `json:"platform"`
	CheckedAt time.Time `json:"checked_at"`

	// AgentVersion is this agent binary's own build version (see
	// internal/version) -- not to be confused with OSInfo, which is about
	// the host's OS release. Set by cmd/update-detector, not by any
	// checker implementation.
	AgentVersion string `json:"agent_version,omitempty"`

	RebootRequired         bool     `json:"reboot_required"`
	RebootRequiredPackages []string `json:"reboot_required_packages,omitempty"`

	OS OSInfo `json:"os"`

	Packages PackageInfo `json:"packages"`

	// Errors lists subsystems that failed to report this cycle; their fields
	// above retain the last successfully observed value rather than being
	// reset, so a transient failure (e.g. a network blip) doesn't produce a
	// false "everything is fine" or a false alarm.
	Errors []string `json:"errors,omitempty"`

	// OK is a convenience boolean for simple Gatus conditions: no reboot
	// pending, no security updates, and no OS upgrade available.
	OK bool `json:"ok"`
}

type OSInfo struct {
	CurrentVersion  string `json:"current_version"`
	CurrentCodename string `json:"current_codename,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	LatestVersion   string `json:"latest_version,omitempty"`
}

type PackageInfo struct {
	UpgradableTotal    int              `json:"upgradable_total"`
	UpgradableSecurity int              `json:"upgradable_security"`
	Upgrades           []PackageUpgrade `json:"upgrades,omitempty"`
}

type PackageUpgrade struct {
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version,omitempty"`
	CandidateVersion string `json:"candidate_version"`
	// Security is best-effort, derived from "-security" appearing in the
	// package's origin/pocket (e.g. Ubuntu's "jammy-security", Debian's
	// "trixie-security"). PackageInfo.UpgradableSecurity is the
	// authoritative count on Ubuntu (from apt-check); this per-package flag
	// is for display/filtering, not the source of truth for that count.
	Security bool `json:"security,omitempty"`
}

// ComputeOK derives the OK convenience field from the rest of the Status.
func ComputeOK(s Status) bool {
	return !s.RebootRequired && s.Packages.UpgradableSecurity == 0 && !s.OS.UpdateAvailable
}
