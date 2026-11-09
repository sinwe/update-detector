//go:build !windows

// Package debian implements cve.Detector for Debian/Ubuntu-family hosts
// using a multi-scanner fallback: debsecan, then trivy, then osv-scanner.
// The first scanner that is installed and runs successfully wins; if none
// is available the detector returns ErrNoScanner and the caller preserves
// the previous cycle's value rather than reporting "clean".
package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/cve"
)

// ErrNoScanner is returned when no supported scanner binary is available.
var ErrNoScanner = fmt.Errorf("no vulnerability scanner found (tried debsecan, trivy, osv-scanner)")

func init() {
	cve.Register("debian", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:            f["suite"],
			DpkgStatusFile:   f["dpkg_status_file"],
			OSReleaseFile:    f["os_release_file"],
			Scanners:         f["scanners"],
			Command:          f["cve_command"],
			Platform:         "debian",
			CVEToolsCacheDir: f["cve_tools_cache_dir"],
		})
	})
	cve.Register("ubuntu", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			Suite:            f["suite"],
			DpkgStatusFile:   f["dpkg_status_file"],
			OSReleaseFile:     f["os_release_file"],
			Scanners:         f["scanners"],
			Command:          f["cve_command"],
			Platform:         "ubuntu",
			CVEToolsCacheDir: f["cve_tools_cache_dir"],
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
	// CVEToolsCacheDir is where lazily-downloaded scanners are cached.
	CVEToolsCacheDir string
}

type Detector struct {
	cfg      Config
	scanners []string
}

func New(cfg Config) (*Detector, error) {
	if cfg.Command == "disable" {
		cfg.Scanners = ""
		cfg.Command = ""
		scanners := ""
		// Empty list: Detect returns ErrNoScanner, and Check preserves
		// previous value. Disable is honored as "no scanning".
		_ = scanners
		return &Detector{cfg: cfg, scanners: nil}, nil
	}
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
	osRelease := cfg.OSReleaseFile
	if osRelease == "" {
		osRelease = "/host/etc/os-release"
	}
	candidates := []string{osRelease}
	if osRelease == "/host/etc/os-release" {
		candidates = append(candidates, "/etc/os-release")
	}
	if cfg.Suite == "" {
		for _, c := range candidates {
			if raw, err := os.ReadFile(c); err == nil {
				for _, line := range strings.Split(string(raw), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "VERSION_CODENAME=") {
						cfg.Suite = strings.Trim(strings.TrimPrefix(line, "VERSION_CODENAME="), "\"")
					}
				}
				if cfg.Suite != "" {
					break
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
		info, err := runScanner(ctx, name, d.cfg.Suite, d.cfg.DpkgStatusFile, d.cfg.Platform, d.cfg.CVEToolsCacheDir)
		if err == nil {
			info.Scanner = name
			// Enrich unknown severities from OSV (cached, so only new
			// CVEs cost a network round-trip on subsequent runs).
			enrichSeverity(ctx, &info, d.cfg.CVEToolsCacheDir)
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

func runScanner(ctx context.Context, name, suite, statusFile, platform, cacheDir string) (checker.VulnerabilityInfo, error) {
	switch name {
	case "debsecan":
		return runDebsecan(ctx, suite, statusFile)
	case "trivy":
		return runTrivy(ctx, cacheDir)
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

func runTrivy(ctx context.Context, cacheDir string) (checker.VulnerabilityInfo, error) {
	bin, err := ensureTrivy(ctx, cacheDir)
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("trivy: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, "rootfs", "--format", "json", "--quiet", "/")
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
	candidates := []string{path}
	// Native (non-container) hosts keep dpkg status at /var/lib/dpkg/status
	// rather than the container mount point /host/var/lib/dpkg/status.
	if path == "/host/var/lib/dpkg/status" {
		candidates = append(candidates, "/var/lib/dpkg/status")
	}
	if path == "" {
		candidates = []string{"/host/var/lib/dpkg/status", "/var/lib/dpkg/status"}
	}
	var data []byte
	var err error
	for _, c := range candidates {
		data, err = os.ReadFile(c)
		if err == nil {
			break
		}
	}
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


// ensureTrivy returns the path to a trivy binary, downloading it into
// cacheDir on first use so the agent image stays small. The download is
// best-effort; on failure the caller falls through to the next scanner.
func ensureTrivy(ctx context.Context, cacheDir string) (string, error) {
	if cacheDir == "" {
		cacheDir = "/var/lib/update-detector/tools"
	}
	if p, err := exec.LookPath("trivy"); err == nil {
		return p, nil
	}
	bin := filepath.Join(cacheDir, "trivy")
	if info, err := os.Stat(bin); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return bin, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	arch := "amd64"
	switch runtime.GOARCH {
	case "arm64":
		arch = "arm64"
	}
	const ver = "0.69.3"
	url := "https://github.com/aquasecurity/trivy/releases/download/v" + ver + "/trivy_" + ver + "_Linux-" + arch + ".tar.gz"
	tmp := bin + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	resp, err := (&http.Client{Timeout: 300 * time.Second}).Do(req)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("trivy download: %s", resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			gz.Close()
			f.Close()
			os.Remove(tmp)
			return "", err
		}
		if hdr.Name == "trivy" || strings.HasSuffix(hdr.Name, "/trivy") {
			if _, err := io.Copy(f, tr); err != nil {
				gz.Close()
				f.Close()
				os.Remove(tmp)
				return "", err
			}
			found = true
			break
		}
	}
	gz.Close()
	f.Close()
	if !found {
		os.Remove(tmp)
		return "", fmt.Errorf("trivy binary not found in archive")
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, bin); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return bin, nil
}

// severityCache maps CVE ID -> severity string.
type severityCache struct {
	Entries map[string]string `json:"entries"`
}

func loadSeverityCache(cacheDir string) severityCache {
	sc := severityCache{Entries: map[string]string{}}
	if cacheDir == "" {
		cacheDir = "/var/lib/update-detector/tools"
	}
	path := filepath.Join(cacheDir, "cve-severity-cache.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return sc
	}
	_ = json.Unmarshal(data, &sc)
	if sc.Entries == nil {
		sc.Entries = map[string]string{}
	}
	return sc
}

func saveSeverityCache(cacheDir string, sc severityCache) {
	if cacheDir == "" {
		cacheDir = "/var/lib/update-detector/tools"
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	path := filepath.Join(cacheDir, "cve-severity-cache.json")
	data, err := json.Marshal(sc)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// enrichSeverity fills in Severity for CVEs that are currently "unknown"
// by querying the OSV individual vulnerability endpoint and computing a
// CVSS base score from the returned vector. Results are cached so only
// new CVEs cost a network round-trip. Best-effort: failures leave the
// severity as "unknown".
func enrichSeverity(ctx context.Context, info *checker.VulnerabilityInfo, cacheDir string) {
	sc := loadSeverityCache(cacheDir)
	changed := false
	var unknowns []string
	for i := range info.Items {
		if info.Items[i].Severity == cve.SeverityUnknown || info.Items[i].Severity == "" {
			unknowns = append(unknowns, info.Items[i].ID)
		}
	}
	if len(unknowns) == 0 {
		return
	}
	type result struct {
		id  string
		sev string
	}
	sem := make(chan struct{}, 8)
	resCh := make(chan result, len(unknowns))
	var wg sync.WaitGroup
	for _, id := range unknowns {
		if cached, ok := sc.Entries[id]; ok {
			resCh <- result{id: id, sev: cached}
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sev := fetchOSVSeverity(ctx, id)
			sc.Entries[id] = sev
			changed = true
			resCh <- result{id: id, sev: sev}
		}(id)
	}
	go func() { wg.Wait(); close(resCh) }()
	sevByID := map[string]string{}
	for r := range resCh {
		sevByID[r.id] = r.sev
	}
	for i := range info.Items {
		if info.Items[i].Severity == cve.SeverityUnknown || info.Items[i].Severity == "" {
			if sev, ok := sevByID[info.Items[i].ID]; ok && sev != "" {
				info.Items[i].Severity = sev
			}
		}
	}
	info.Critical, info.High, info.Medium, info.Low = 0, 0, 0, 0
	for _, it := range info.Items {
		bumpSeverity(info, it.Severity)
	}
	if changed {
		saveSeverityCache(cacheDir, sc)
	}
}

func fetchOSVSeverity(ctx context.Context, id string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.osv.dev/v1/vulns/"+id, nil)
	if err != nil {
		return cve.SeverityUnknown
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return cve.SeverityUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cve.SeverityUnknown
	}
	var rec struct {
		Severity []struct {
			Type  string `json:"type"`
			Score string `json:"score"`
		} `json:"severity"`
		DatabaseSpecific struct {
			Severity string `json:"severity"`
		} `json:"database_specific"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return cve.SeverityUnknown
	}
	if sev := strings.ToLower(strings.TrimSpace(rec.DatabaseSpecific.Severity)); sev != "" {
		switch sev {
		case "critical":
			return cve.SeverityCritical
		case "high":
			return cve.SeverityHigh
		case "medium", "moderate":
			return cve.SeverityMedium
		case "low":
			return cve.SeverityLow
		}
	}
	for _, s := range rec.Severity {
		if strings.HasPrefix(s.Type, "CVSS_V3") {
			if score, ok := cvssV3BaseScore(s.Score); ok {
				return severityFromCVSS(score)
			}
		}
	}
	return cve.SeverityUnknown
}

// cvssV3BaseScore computes the CVSS v3.0/v3.1 base score from a vector
// string such as "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H".
func cvssV3BaseScore(vector string) (float64, bool) {
	metrics := map[string]string{}
	for _, part := range strings.Split(vector, "/") {
		if kv := strings.SplitN(part, ":", 2); len(kv) == 2 {
			metrics[kv[0]] = kv[1]
		}
	}
	avMap := map[string]float64{"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2}
	acMap := map[string]float64{"L": 0.77, "H": 0.44}
	uiMap := map[string]float64{"N": 0.85, "R": 0.62}
	ciaMap := map[string]float64{"N": 0, "L": 0.22, "H": 0.56}
	av, ok1 := avMap[metrics["AV"]]
	ac, ok2 := acMap[metrics["AC"]]
	ui, ok3 := uiMap[metrics["UI"]]
	c, ok4 := ciaMap[metrics["C"]]
	i, ok5 := ciaMap[metrics["I"]]
	a, ok6 := ciaMap[metrics["A"]]
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return 0, false
	}
	scope := metrics["S"]
	prStr := metrics["PR"]
	var pr float64
	switch scope {
	case "C":
		switch prStr {
		case "N":
			pr = 0.85
		case "L":
			pr = 0.68
		case "H":
			pr = 0.5
		default:
			return 0, false
		}
	default:
		switch prStr {
		case "N":
			pr = 0.85
		case "L":
			pr = 0.62
		case "H":
			pr = 0.27
		default:
			return 0, false
		}
	}
	iss := 1 - (1-c)*(1-i)*(1-a)
	var impact float64
	if scope == "C" {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}
	exploit := 8.22 * av * ac * pr * ui
	var base float64
	if impact <= 0 {
		base = 0
	} else if scope == "C" {
		base = math.Min(1.08*(impact+exploit), 10)
	} else {
		base = math.Min(impact+exploit, 10)
	}
	base = math.Ceil(base*10) / 10
	return base, true
}

