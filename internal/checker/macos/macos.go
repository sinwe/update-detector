// Package macos implements checker.Checker for macOS hosts, driven by
// Homebrew (`brew outdated`) for third-party packages plus
// `softwareupdate --list` for Apple OS updates. Like the Windows agent,
// it always runs natively on the host itself, never containerized -- a
// container has no visibility into the host's Homebrew cellar or
// Software Update catalog, so those signals are only meaningful from a
// native process (see docs/plugin-architecture-plan.md).
package macos

import (
	"context"
	"fmt"
	"time"

	"update-detector/internal/checker"
)

// init registers this package with internal/checker's registry under
// "macos", so main.go can select it by name (via internal/config's
// CheckerFields) without importing this package directly -- see
// checker.Fields for why the handoff is a plain string-keyed map rather
// than Config itself. Only cmd/update-detector/platforms_darwin.go
// blank-imports this package, so "macos" resolves on darwin builds only.
func init() {
	checker.Register("macos", func(f checker.Fields) (checker.Checker, error) {
		return New(Config{Hostname: f["hostname"]})
	})
}

// Config is deliberately minimal: brew and sw_vers/softwareupdate are
// read live, as whatever process this is, so there are no host-mounted
// file paths to redirect -- the same posture as the Windows checker's
// own Config.
type Config struct {
	Hostname string
}

type Checker struct {
	cfg Config
}

func New(cfg Config) (*Checker, error) {
	return &Checker{cfg: cfg}, nil
}

func (c *Checker) Platform() string { return "macos" }

// Check aggregates the Homebrew package, Apple OS-update, and OS-version
// checks into one Status. No reboot-pending detection in v1 (macOS has no
// reliable marker file equivalent to Linux's reboot-required or Windows'
// CBS/WindowsUpdate registry keys), so RebootRequired always stays false.
// A Homebrew failure falls back to the previous cycle's package list;
// a sw_vers/softwareupdate failure falls back to the previous OS info --
// same "never report a false zero" posture as the other checkers.
func (c *Checker) Check(ctx context.Context, previous *checker.Status) (checker.Status, error) {
	status := checker.Status{
		Hostname:  c.cfg.Hostname,
		Platform:  c.Platform(),
		CheckedAt: time.Now(),
	}

	var errs []string

	if pkgResult, err := checkOutdated(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("packages: %v", err))
		if previous != nil {
			status.Packages = previous.Packages
		}
	} else {
		status.Packages = checker.PackageInfo{
			UpgradableTotal:    pkgResult.Total,
			UpgradableSecurity: 0,
			Upgrades:           pkgResult.Upgrades,
		}
	}

	osInfo, err := readOSInfo(ctx)
	if err != nil {
		errs = append(errs, fmt.Sprintf("os-info: %v", err))
		if previous != nil {
			status.OS = previous.OS
		}
	} else {
		status.OS = osInfo
	}

	status.Errors = errs
	status.OK = checker.ComputeOK(status)
	return status, nil
}
