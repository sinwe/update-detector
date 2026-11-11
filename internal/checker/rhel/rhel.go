//go:build !windows

// Package rhel implements checker.Checker for RHEL-family Linux hosts
// (RHEL, Rocky, AlmaLinux, CentOS Stream, Fedora) using dnf/yum.
// This is an initial scaffold: package detection uses `dnf check-update`.
package rhel

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"update-detector/internal/checker"
)

func init() {
	checker.Register("rhel", func(f checker.Fields) (checker.Checker, error) {
		return New(Config{
			Hostname: f["hostname"],
		})
	})
}

type Config struct {
	Hostname string
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
	var upgrades []checker.PackageUpgrade
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// dnf check-update lines: name.arch  version  repo
		upgrades = append(upgrades, checker.PackageUpgrade{
			Name:             fields[0],
			CandidateVersion: fields[1],
		})
	}
	status.Packages = checker.PackageInfo{
		UpgradableTotal: len(upgrades),
		Upgrades:        upgrades,
	}
	status.OK = checker.ComputeOK(status)
	return status, nil
}
