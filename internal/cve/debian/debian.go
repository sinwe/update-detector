//go:build !windows

// Package debian implements cve.Detector for Debian/Ubuntu-family hosts
// using debsecan (Debian's CVE scanner) when it is available. debsecan
// needs network access to refresh its CVE cache (security-tracker.debian.org);
// when it is missing or fails, detection is reported as unavailable rather
// than "clean", and the rest of the check cycle is unaffected.
package debian

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"update-detector/internal/cve"
	"update-detector/internal/checker"
)

func init() {
	cve.Register("debian", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:   f["suite"],
			Command: f["cve_command"],
		})
	})
	cve.Register("ubuntu", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:   f["suite"],
			Command: f["cve_command"],
		})
	})
}

// Config configures the debsecan-backed detector.
type Config struct {
	// Suite is the Debian/Ubuntu release codename (e.g. "trixie", "noble").
	// Empty means debsecan auto-detects from /etc/debian_version.
	Suite string
	// Command overrides the debsecan binary path. Defaults to "debsecan".
	Command string
}

type Detector struct {
	cfg Config
}

func New(cfg Config) (*Detector, error) {
	if cfg.Command == "" {
		cfg.Command = "debsecan"
	}
	return &Detector{cfg: cfg}, nil
}

func (d *Detector) Platform() string { return "debian" }

var cveIDRE = regexp.MustCompile(`CVE-\d{4}-\d+`)

// maxItems caps the detail list sent over the wire; counts stay accurate.
const maxItems = 200

func (d *Detector) Detect(ctx context.Context, _ *checker.Status) (checker.VulnerabilityInfo, error) {
	args := []string{}
	if d.cfg.Suite != "" {
		args = append(args, "--suite", d.cfg.Suite)
	}
	// --only-fixed would hide unfixed CVEs; we want everything affecting
	// installed packages, so run with default behaviour (all CVEs).
	cmd := exec.CommandContext(ctx, d.cfg.Command, args...)
	out, err := cmd.Output()
	if err != nil {
		// debsecan missing or failed: report unavailable, not "clean".
		return checker.VulnerabilityInfo{}, fmt.Errorf("debsecan: %w", err)
	}

	info := checker.VulnerabilityInfo{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		id := cveIDRE.FindString(line)
		if id == "" {
			continue
		}
		sev := severityFromLine(line)
		if seen[id] {
			continue
		}
		seen[id] = true
		info.Total++
		switch sev {
		case cve.SeverityCritical:
			info.Critical++
		case cve.SeverityHigh:
			info.High++
		case cve.SeverityMedium:
			info.Medium++
		case cve.SeverityLow:
			info.Low++
		}
		if len(info.Items) < maxItems {
			pkg := packageFromLine(line, id)
			info.Items = append(info.Items, checker.Vulnerability{
				ID:       id,
				Severity: sev,
				Package:  pkg,
				Source:   "debsecan",
			})
		}
	}
	// Stable ordering: critical/high first.
	sort.Slice(info.Items, func(i, j int) bool {
		return severityRank(info.Items[i].Severity) < severityRank(info.Items[j].Severity)
	})
	return info, nil
}

func severityFromLine(line string) string {
	l := strings.ToLower(line)
	// Check most severe first.
	for _, kw := range []string{"critical"} {
		if strings.Contains(l, kw) {
			return cve.SeverityCritical
		}
	}
	if strings.Contains(l, "high") {
		return cve.SeverityHigh
	}
	if strings.Contains(l, "medium") || strings.Contains(l, "moderate") {
		return cve.SeverityMedium
	}
	if strings.Contains(l, "low") {
		return cve.SeverityLow
	}
	return cve.SeverityUnknown
}

func severityRank(s string) int {
	switch s {
	case cve.SeverityCritical:
		return 0
	case cve.SeverityHigh:
		return 1
	case cve.SeverityMedium:
		return 2
	case cve.SeverityLow:
		return 3
	}
	return 4
}

// packageFromLine extracts the package name following the CVE id.
func packageFromLine(line, id string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(line, id))
	// Remove trailing parenthetical severity.
	if i := strings.Index(rest, "("); i >= 0 {
		rest = strings.TrimSpace(rest[:i])
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}