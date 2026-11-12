//go:build !windows

// Package rhel implements cve.Detector for RHEL-family hosts using trivy
// (rootfs scan), which supports RPM-based distributions. debsecan is
// Debian/Ubuntu-only and is intentionally not used here.
package rhel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"update-detector/internal/cve"
	"update-detector/internal/cve/debian"

	"update-detector/internal/checker"
)

// ErrNoScanner is returned when trivy is not available.
var ErrNoScanner = fmt.Errorf("no vulnerability scanner found (trivy)")

func init() {
	cve.Register("rhel", func(f cve.Fields) (cve.Detector, error) {
		return New(Config{
			CVEToolsCacheDir: f["cve_tools_cache_dir"],
		})
	})
}

type Config struct {
	CVEToolsCacheDir string
}

type Detector struct {
	cfg Config
}

func New(cfg Config) (*Detector, error) {
	return &Detector{cfg: cfg}, nil
}

func (d *Detector) Platform() string { return "rhel" }

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

func (d *Detector) Detect(ctx context.Context, _ *checker.Status) (checker.VulnerabilityInfo, error) {
	cacheDir := d.cfg.CVEToolsCacheDir
	if cacheDir == "" {
		cacheDir = "/var/lib/update-detector/tools"
	}
	bin, err := debian.EnsureTrivy(ctx, cacheDir)
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("trivy: %w", err)
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	cmd := exec.CommandContext(ctx, bin, "rootfs", "--format", "json", "--quiet", "--exit-code", "0", "--cache-dir", cacheDir, "--skip-dirs", "/proc,/sys,/dev,/mnt", "/")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("trivy: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var rep trivyReport
	if err := json.Unmarshal(out, &rep); err != nil {
		return checker.VulnerabilityInfo{}, fmt.Errorf("trivy: bad json: %w", err)
	}
	info := checker.VulnerabilityInfo{Scanner: "trivy"}
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