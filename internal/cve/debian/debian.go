//go:build !windows

// Package debian implements cve.Detector for Debian/Ubuntu-family hosts
// using a multi-scanner fallback: debsecan, then trivy, then osv-scanner.
// The first scanner that is installed and runs successfully wins; if none
// is available the detector returns ErrNoScanner and the caller preserves
// the previous cycle's value rather than reporting "clean".
package debian

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

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
			Platform:       "debian",
		})
	})
	cve.Register("ubuntu", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:          f["suite"],
			DpkgStatusFile: f["dpkg_status_file"],
			OSReleaseFile:  f["os_release_file"],
			Scanners:       f["scanners"],
			Command:        f["cve_command"],
			Platform:       "ubuntu",
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
	// Platform is the OS family ("debian"/"ubuntu"); used to pick the
	// OSV ecosystem. Empty defaults to "debian".
	Platform string
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
		info, err := runScanner(ctx, name, d.cfg.Suite, d.cfg.DpkgStatusFile, d.cfg.Platform)
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

func runScanner(ctx context.Context, name, suite, statusFile, platform string) (checker.VulnerabilityInfo, error) {
	switch name {
	case "debsecan":
		return runDebsecan(ctx, suite, statusFile)
	case "trivy":
		return runTrivy(ctx)
	case "osv-scanner", "osv":
		return runOSV(ctx, statusFile, platform)
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

// --- osv ---

// runOSV queries the OSV API directly for the host's installed dpkg
// packages instead of invoking the osv-scanner binary: the binary's
// source-scan mode doesn't pick up a bare /host/var/lib/dpkg/status
// mount (it expects lockfiles in a project dir), while the API batch
// endpoint answers per-package queries deterministically and carries
// both Debian and Ubuntu data (ecosystem "Debian"/"Ubuntu").
func runOSV(ctx context.Context, statusFile, platform string) (checker.VulnerabilityInfo, error) {
	pkgs, err := parseDpkgStatus(statusFile)
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("osv: %w", err)
	}
	ecosystem := "Debian"
	if platform == "ubuntu" {
		ecosystem = "Ubuntu"
	}
	// Batch of 100 (help text of querybatch; larger batches are allowed
	// but keep request bodies modest and timeouts forgiving).
	const batch = 100
	seen := map[string]bool{}
	info := checker.VulnerabilityInfo{}
	var items []checker.Vulnerability
	for i := 0; i < len(pkgs); i += batch {
		end := i + batch
		if end > len(pkgs) {
			end = len(pkgs)
		}
		vulns, err := osvQueryBatch(ctx, ecosystem, pkgs[i:end])
		if err != nil {
			return checker.VulnerabilityInfo{}, err
		}
		for _, v := range vulns {
			if seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			sev := v.Severity
			if sev == "" {
				sev = cve.SeverityUnknown
			}
			info.Total++
			bumpSeverity(&info, sev)
			items = append(items, checker.Vulnerability{
				ID:       v.ID,
				Severity: sev,
				Package:  v.Package,
				Source:   "osv",
			})
		}
	}
	info.Items = items
	return info, nil
}

type osvBatchRequest struct {
	Queries []osvQuery `json:"queries"`
}
type osvQuery struct {
	Package struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	} `json:"package"`
	Version string `json:"version"`
}
type osvBatchResponse struct {
	Results []struct {
		Vulns []struct {
			ID       string `json:"id"`
			Severity []struct {
				Type  string `json:"type"`
				Score string `json:"score"`
			} `json:"severity"`
		} `json:"vulns"`
		// Package is echoed back per result in richer responses; the
		// batch request order maps results[i] to queries[i], so we
		// track package names by index below.
	} `json:"results"`
}

func osvQueryBatch(ctx context.Context, ecosystem string, pkgs []osvPkg) ([]checker.Vulnerability, error) {
	req := osvBatchRequest{}
	for _, p := range pkgs {
		var q osvQuery
		q.Package.Name = p.Name
		q.Package.Ecosystem = ecosystem
		q.Version = p.Version
		req.Queries = append(req.Queries, q)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("osv: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.osv.dev/v1/querybatch", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("osv: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("osv: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("osv: %s: %s", resp.Status, string(body))
	}
	var out osvBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("osv: bad json: %w", err)
	}
	var res []checker.Vulnerability
	for idx, r := range out.Results {
		pkgName := ""
		if idx < len(pkgs) {
			pkgName = pkgs[idx].Name
		}
		for _, v := range r.Vulns {
			if v.ID == "" {
				continue
			}
			sev := ""
			best := 0.0
			for _, s := range v.Severity {
				if s.Type != "CVSS_V3" && s.Type != "CVSS_V4" {
					continue
				}
				// Score format: "CVSS:3.1/AV:N/..." — the numeric
				// score is in the vector's score field from the API
				// as a decimal string ("9.8") for CVSS_V3 entries.
				f, err := strconv.ParseFloat(s.Score, 64)
				if err == nil && f > best {
					best = f
					sev = severityFromCVSS(f)
				}
			}
			res = append(res, checker.Vulnerability{ID: v.ID, Severity: sev, Package: pkgName})
		}
	}
	return res, nil
}

func severityFromCVSS(score float64) string {
	switch {
	case score >= 9:
		return cve.SeverityCritical
	case score >= 7:
		return cve.SeverityHigh
	case score >= 4:
		return cve.SeverityMedium
	case score > 0:
		return cve.SeverityLow
	}
	return cve.SeverityUnknown
}

type osvPkg struct {
	Name    string
	Version string
}

func parseDpkgStatus(path string) ([]osvPkg, error) {
	if path == "" {
		return nil, fmt.Errorf("no dpkg status file configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pkgs []osvPkg
	var cur osvPkg
	installed := false
	flush := func() {
		if installed && cur.Name != "" && cur.Version != "" {
			pkgs = append(pkgs, cur)
		}
		cur = osvPkg{}
		installed = false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "Package: ") {
			// New paragraph ends previous; dpkg lists Package first.
			flush()
			cur.Name = strings.TrimSpace(strings.TrimPrefix(line, "Package: "))
		} else if strings.HasPrefix(line, "Status: ") {
			installed = strings.TrimSpace(strings.TrimPrefix(line, "Status: ")) == "install ok installed"
		} else if strings.HasPrefix(line, "Version: ") {
			cur.Version = strings.TrimSpace(strings.TrimPrefix(line, "Version: "))
		}
	}
	flush()
	return pkgs, nil
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
