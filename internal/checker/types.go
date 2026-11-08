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

	// Vulnerabilities reports CVEs affecting the host's OS/packages/runtime.
	// Populated by the CVE detector plugin (internal/cve) when one is
	// available for this platform; left zero when detection is unavailable
	// or disabled, so a missing detector never masquerades as "clean".
	Vulnerabilities VulnerabilityInfo `json:"vulnerabilities,omitempty"`

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

// Vulnerability is a single CVE affecting the host's OS/packages/runtime.
// Severity is one of "critical", "high", "medium", "low" (or "unknown"
// when the source doesn't classify). Source names the detector that
// produced it (e.g. "debsecan", "osv", "trivy").
type Vulnerability struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Package  string `json:"package,omitempty"`
	Source   string `json:"source,omitempty"`
	Fixed    bool   `json:"fixed,omitempty"`
}

// VulnerabilityInfo aggregates CVE counts by severity for a single check
// cycle. Items holds the (possibly truncated) detail list; the counts are
// the authoritative totals even when Items is capped.
type VulnerabilityInfo struct {
	Critical int             `json:"critical"`
	High     int             `json:"high"`
	Medium   int             `json:"medium"`
	Low      int             `json:"low"`
	Total    int             `json:"total"`
	Items    []Vulnerability `json:"items,omitempty"`
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
	return !s.RebootRequired && s.Packages.UpgradableSecurity == 0 && !s.OS.UpdateAvailable &&
		s.Vulnerabilities.Critical == 0 && s.Vulnerabilities.High == 0
}
