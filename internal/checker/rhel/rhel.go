//go:build !windows

// Package rhel implements checker.Checker for RHEL-family Linux hosts
// (RHEL, Rocky, AlmaLinux, CentOS Stream, Fedora) using dnf/yum.
// This is an initial scaffold: package detection uses `dnf check-update`.
package rhel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"update-detector/internal/checker"
	"update-detector/internal/osrelease"
)

func init() {
	checker.Register("rhel", func(f checker.Fields) (checker.Checker, error) {
		return New(Config{
			Hostname:      f["hostname"],
			OSReleaseFile: f["os_release_file"],
		})
	})
}

type Config struct {
	Hostname      string
	OSReleaseFile string
}

type Checker struct {
	cfg Config
}

func New(cfg Config) (*Checker, error) {
	return &Checker{cfg: cfg}, nil
}

func (c *Checker) Platform() string { return "rhel" }

func (c *Checker) Check(ctx context.Context, previous *checker.Status) (checker.Status, error) {
	status := checker.Status{
		Hostname:  c.cfg.Hostname,
		Platform:  c.Platform(),
		CheckedAt: time.Now(),
	}

	// OS release info.
	osPath := c.cfg.OSReleaseFile
	if osPath == "" {
		osPath = "/etc/os-release"
	}
	if raw, err := os.ReadFile(osPath); err == nil {
		parsed := osrelease.Parse(string(raw))
		status.OS = checker.OSInfo{
			CurrentVersion: parsed["VERSION_ID"],
			CurrentCodename: parsed["VERSION_CODENAME"],
		}
	}

	// `dnf check-update` exits 100 when updates are available, 0 when
	// none, and non-100/non-0 on error.
	cmd := exec.CommandContext(ctx, "dnf", "check-update", "-q")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 100 {
			// updates available; output in out
		} else {
			status.Errors = append(status.Errors, fmt.Sprintf("dnf check-update: %v", err))
			status.OK = checker.ComputeOK(status)
			return status, nil
		}
	}

	lines := strings.Split(string(out), "\n")
	// Build a set of security-flagged package names from a separate
	// `dnf check-update --security` run (best-effort; ignored on failure).
	securitySet := map[string]bool{}
	secOut, secErr := exec.CommandContext(ctx, "dnf", "check-update", "-q", "--security").Output()
	if secErr == nil {
		// ok
	} else if ee, ok := secErr.(*exec.ExitError); ok && ee.ExitCode() == 100 {
		// updates available
	} else {
		secOut = nil
	}
	if secOut != nil {
		for _, line := range strings.Split(string(secOut), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 1 {
				continue
			}
			securitySet[stripArch(fields[0])] = true
		}
	}

	var upgrades []checker.PackageUpgrade
	securityCount := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := stripArch(fields[0])
		isSec := securitySet[name]
		if isSec {
			securityCount++
		}
		upgrades = append(upgrades, checker.PackageUpgrade{
			Name:             name,
			CandidateVersion: fields[1],
			Security:         isSec,
		})
	}
	status.Packages = checker.PackageInfo{
		UpgradableTotal:    len(upgrades),
		UpgradableSecurity: securityCount,
		Upgrades:           upgrades,
	}
	status.OK = checker.ComputeOK(status)
	return status, nil
}

var knownArches = map[string]bool{
	"x86_64": true, "aarch64": true, "noarch": true, "i686": true,
	"ppc64le": true, "s390x": true, "armv7hl": true, "i386": true,
}

// stripArch removes a trailing ".<arch>" from a dnf package name.
func stripArch(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		if knownArches[name[i+1:]] {
			return name[:i]
		}
	}
	return name
}
