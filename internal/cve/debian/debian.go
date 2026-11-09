//go:build !windows

// Package debian implements cve.Detector for Debian/Ubuntu-family hosts
// using a multi-scanner fallback: debsecan, then trivy, then osv-scanner.
// The first scanner that is installed and runs successfully wins; if none
// is available the detector returns ErrNoScanner so the caller can offer
// to install one (the companion has sudo and can apt-get install an
// allowlisted scanner) rather than reporting "clean".
package debian

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"update-detector/internal/cve"
	"update-detector/internal/checker"
)

// ErrNoScanner is returned when no supported scanner binary is available.
var ErrNoScanner = fmt.Errorf("no vulnerability scanner found (tried debsecan, trivy, osv-scanner)")

func init() {
	cve.Register("debian", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:          f["suite"],
			DpkgStatusFile: f["dpkg_status_file"],
			OSReleaseFile:  f["os_release_file"],
			Scanners:       f["scanners"],
			Command:        f["cve_command"],
		})
	})
	cve.Register("ubuntu", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:          f["suite"],
			DpkgStatusFile: f["dpkg_status_file"],
			OSReleaseFile:  f["os_release_file"],
			Scanners:       f["scanners"],
			Command:        f["cve_command"],
		})
	})
}

// Config configures the composite detector.
type Config struct {
	// Suite is the Debian/Ubuntu release codename (e.g. "trixie", "noble").
	// Empty auto-detects from OSReleaseFile.
	Suite string
	// DpkgStatusFile is the host's dpkg status file (mounted read-only
	// into the agent container at /host/var/lib/dpkg/status). debsecan
	// scans this instead of the container's own package set.
	DpkgStatusFile string
	// OSReleaseFile is the host's os-release (mounted read-only). Used to
	// auto-detect Suite when Suite is empty.
	OSReleaseFile string
	// Scanners is a comma-separated list of scanner names to try, in order.
	// Defaults to "debsecan,trivy,osv-scanner". Empty disables detection.
	Scanners string
	// Command, if set, restricts detection to a single scanner binary
	// (legacy; overrides Scanners). Empty means use Scanners.
	Command string
}

type Detector struct {
	cfg      Config
	scanners []string
}

func New(cfg Config) (*Detector, error) {
	scanners := cfg.Scanners
	if scanners == "" {
		scanners = "debsecan,trivy,osv-scanner"
	}
	if cfg.Command != "" {
		scanners = cfg.Command
	}
	var list []string
	for _, s := range strings.Split(scanners, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			list = append(list, s)
		}
	}
	// Auto-detect suite from the host's os-release when not pinned.
	if cfg.Suite == "" && cfg.OSReleaseFile != "" {
		if raw, err := os.ReadFile(cfg.OSReleaseFile); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "VERSION_CODENAME=") {
					cfg.Suite = strings.Trim(strings.TrimPrefix(line, "VERSION_CODENAME="), "\"")
				}
			}
		}
	}
	return &Detector{cfg: cfg, scanners: list}, nil
}

func (d *Detector) Platform() string { return "debian" }

// Detect tries each configured scanner in order and returns the first
// successful result. If none is available or all fail, returns ErrNoScanner
// (or the last error).
func (d *Detector) Detect(ctx context.Context, _ *checker.Status) (checker.VulnerabilityInfo, error) {
	if len(d.scanners) == 0 {
		return checker.VulnerabilityInfo{}, ErrNoScanner
	}
	var lastErr error
	for _, name := range d.scanners {
		info, err := runScanner(ctx, name, d.cfg.Suite, d.cfg.DpkgStatusFile)
		if err == nil {
			info.Scanner = name
			info.Items = capItems(info.Items)
			sortItems(info.Items)
			return info, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrNoScanner
	}
	// Normalize "binary not found" into ErrNoScanner so callers can
	// distinguish "no scanner installed" from a real scan failure.
	if isNotFound(lastErr) {
		return checker.VulnerabilityInfo{}, ErrNoScanner
	}
	return checker.VulnerabilityInfo{}, lastErr
}

func runScanner(ctx context.Context, name, suite, statusFile string) (checker.VulnerabilityInfo, error) {
	switch name {
	case "debsecan":
		return runDebsecan(ctx, suite, statusFile)
	case "trivy":
		return runTrivy(ctx)
	case "osv-scanner", "osv":
		return runOSV(ctx, statusFile)
	default:
		// Treat unknown name as a binary to invoke with no args.
		return runGeneric(ctx, name)
	}
}

// --- debsecan ---

var cveIDRE = regexp.MustCompile(`CVE-\d{4}-\d+`)

func runDebsecan(ctx context.Context, suite, statusFile string) (checker.VulnerabilityInfo, error) {
	args := []string{"--format", "report"}
	if suite != "" {
		args = append(args, "--suite", suite)
	}
	if statusFile != "" {
		args = append(args, "--status", statusFile)
	}
	cmd := exec.CommandContext(ctx, "debsecan", args...)
	out, err := cmd.Output()
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("debsecan: %w", err)
	}
	return parseDebsecanReport(string(out)), nil
}

// --- trivy ---

type trivyReport struct {
	Results []struct {
		Vulnerabilities []struct {
			VulnerabilityID string `json:"VulnerabilityID"`
			PkgName         string `json:"PkgName"`
			Severity        string `json:"Severity"`
			FixedVersion    string `json:"FixedVersion"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

func runTrivy(ctx context.Context) (checker.VulnerabilityInfo, error) {
	cmd := exec.CommandContext(ctx, "trivy", "rootfs", "--format", "json", "--quiet", "/")
	out, err := cmd.Output()
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("trivy: %w", err)
	}
	var rep trivyReport
	if err := json.Unmarshal(out, &rep); err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("trivy: bad json: %w", err)
	}
	info := checker.VulnerabilityInfo{}
	seen := map[string]bool{}
	for _, r := range rep.Results {
		for _, v := range r.Vulnerabilities {
			if v.VulnerabilityID == "" || seen[v.VulnerabilityID] {
				continue
			}
			seen[v.VulnerabilityID] = true
			sev := cve.NormalizeSeverity(strings.ToLower(v.Severity))
			info.Total++
			bumpSeverity(&info, sev)
			info.Items = append(info.Items, checker.Vulnerability{
				ID:       v.VulnerabilityID,
				Severity: sev,
				Package:  v.PkgName,
				Source:   "trivy",
				Fixed:    v.FixedVersion != "",
			})
		}
	}
	return info, nil
}

// --- osv-scanner ---

type osvReport struct {
	Results []struct {
		Packages []struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
			Vulnerabilities []struct {
				ID       string `json:"id"`
				Aliases  []string `json:"aliases"`
			} `json:"vulnerabilities"`
		} `json:"packages"`
	} `json:"results"`
}

func runOSV(ctx context.Context, statusFile string) (checker.VulnerabilityInfo, error) {
	target := statusFile
	if target == "" {
		target = "/"
	}
	cmd := exec.CommandContext(ctx, "osv-scanner", "--format", "json", target)
	out, err := cmd.Output()
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("osv-scanner: %w", err)
	}
	var rep osvReport
	if err := json.Unmarshal(out, &rep); err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("osv-scanner: bad json: %w", err)
	}
	info := checker.VulnerabilityInfo{}
	seen := map[string]bool{}
	for _, r := range rep.Results {
		for _, p := range r.Packages {
			for _, v := range p.Vulnerabilities {
				id := v.ID
				if id == "" && len(v.Aliases) > 0 {
					id = v.Aliases[0]
				}
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				// OSV output rarely carries severity; default unknown.
				sev := cve.SeverityUnknown
				info.Total++
				bumpSeverity(&info, sev)
				info.Items = append(info.Items, checker.Vulnerability{
					ID:       id,
					Severity: sev,
					Package:  p.Package.Name,
					Source:   "osv-scanner",
				})
			}
		}
	}
	return info, nil
}

// --- generic fallback ---

func runGeneric(ctx context.Context, name string) (checker.VulnerabilityInfo, error) {
	cmd := exec.CommandContext(ctx, name)
	out, err := cmd.Output()
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("%s: %w", name, err)
	}
	// Best-effort: pull CVE ids out of any output.
	info := checker.VulnerabilityInfo{}
	seen := map[string]bool{}
	for _, id := range findAllCVEs(string(out)) {
		if seen[id] {
			continue
		}
		seen[id] = true
		info.Total++
		info.Items = append(info.Items, checker.Vulnerability{ID: id, Severity: cve.SeverityUnknown, Source: name})
	}
	return info, nil
}

// --- helpers ---

func bumpSeverity(info *checker.VulnerabilityInfo, sev string) {
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
}

func severityFromLine(line string) string {
	l := strings.ToLower(line)
	if strings.Contains(l, "critical") {
		return cve.SeverityCritical
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

func findAllCVEs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for {
		loc := cveIDRE.FindStringIndex(s)
		if loc == nil {
			break
		}
		id := s[loc[0]:loc[1]]
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
		s = s[loc[1]:]
	}
	return out
}

func packageFromLine(line, id string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(line, id))
	if i := strings.Index(rest, "("); i >= 0 {
		rest = strings.TrimSpace(rest[:i])
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "executable file not found") ||
		strings.Contains(s, "no such file") ||
		strings.Contains(s, "not found")
}

const maxItems = 200

func capItems(items []checker.Vulnerability) []checker.Vulnerability {
	if len(items) <= maxItems {
		return items
	}
	return items[:maxItems]
}

func sortItems(items []checker.Vulnerability) {
	sort.Slice(items, func(i, j int) bool {
		ri, rj := sevRank(items[i].Severity), sevRank(items[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return items[i].ID < items[j].ID
	})
}

func sevRank(s string) int {
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


// parseDebsecanReport parses `debsecan --format report` output.
func parseDebsecanReport(out string) checker.VulnerabilityInfo {
	info := checker.VulnerabilityInfo{}
	seen := map[string]bool{}
	lines := strings.Split(out, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		id := cveIDRE.FindString(line)
		if id == "" || !strings.HasPrefix(line, "CVE-") {
			i++
			continue
		}
		if seen[id] {
			i++
			continue
		}
		var pkgs []string
		var sev string
		j := i + 1
		for j < len(lines) {
			l := strings.TrimSpace(lines[j])
			if l == "" || strings.HasPrefix(l, "CVE-") || strings.HasPrefix(l, "***") || strings.HasPrefix(l, "Security report") {
				break
			}
			if strings.HasPrefix(l, "- ") {
				p := strings.TrimPrefix(l, "- ")
				p = strings.TrimSpace(p)
				if idx := strings.Index(p, "("); idx >= 0 {
					urg := strings.TrimSpace(p[idx+1:])
					urg = strings.TrimSuffix(urg, ")")
					urg = strings.TrimSpace(strings.TrimSuffix(urg, "urgency"))
					urg = strings.TrimSpace(urg)
					if sev == "" {
						sev = cve.NormalizeSeverity(urg)
					}
					p = strings.TrimSpace(p[:idx])
				}
				for _, name := range strings.Split(p, ",") {
					name = strings.TrimSpace(name)
					if name != "" {
						pkgs = append(pkgs, name)
					}
				}
			}
			j++
		}
		seen[id] = true
		if sev == "" {
			sev = cve.SeverityUnknown
		}
		info.Total++
		bumpSeverity(&info, sev)
		pkg := ""
		if len(pkgs) > 0 {
			pkg = pkgs[0]
		}
		info.Items = append(info.Items, checker.Vulnerability{
			ID:       id,
			Severity: sev,
			Package:  pkg,
			Source:   "debsecan",
		})
		i = j
	}
	return info
}
