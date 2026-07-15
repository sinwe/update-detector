//go:build !windows

// Package debian implements checker.Checker for plain Debian-based hosts
// (including Raspberry Pi OS) that don't have Ubuntu's update-notifier
// tooling (apt-check) installed or available. Package detection instead
// parses `apt-get -s dist-upgrade` directly. It never mutates host state:
// apt's writable paths are redirected to a container-owned directory via
// internal/aptutil, same as the ubuntu package.
package debian

import (
	"context"
	"fmt"
	"os"
	"time"

	"update-detector/internal/aptutil"
	"update-detector/internal/checker"
	"update-detector/internal/checker/reboot"
	"update-detector/internal/osrelease"
)

// init registers this package with internal/checker's registry under
// "debian", so main.go can select it by name (via internal/config's
// CheckerFields) without importing this package directly -- see
// checker.Fields for why the handoff is a plain string-keyed map rather
// than Config itself. Unlike ubuntu, this Config has no
// ReleaseUpgradesFile field -- that key in f is simply unused here.
func init() {
	checker.Register("debian", func(f checker.Fields) (checker.Checker, error) {
		return New(Config{
			Hostname:           f["hostname"],
			AptSourcesList:     f["apt_sources_list"],
			AptSourcesListD:    f["apt_sources_list_d"],
			DpkgStatusFile:     f["dpkg_status_file"],
			AptListsCacheDir:   f["apt_lists_cache_dir"],
			OSReleaseFile:      f["os_release_file"],
			RebootRequiredFile: f["reboot_required_file"],
		})
	})
}

type Config struct {
	Hostname string

	AptSourcesList   string
	AptSourcesListD  string
	DpkgStatusFile   string
	AptListsCacheDir string

	OSReleaseFile      string
	RebootRequiredFile string
}

type Checker struct {
	cfg           Config
	aptConfigPath string
}

// New generates the apt.conf override once (its paths don't change at
// runtime) and returns a ready-to-use Checker.
func New(cfg Config) (*Checker, error) {
	confPath, err := aptutil.Write(aptutil.Config{
		SourcesList:  cfg.AptSourcesList,
		SourcesListD: cfg.AptSourcesListD,
		DpkgStatus:   cfg.DpkgStatusFile,
		ListsDir:     cfg.AptListsCacheDir,
	})
	if err != nil {
		return nil, fmt.Errorf("debian checker: %w", err)
	}
	return &Checker{cfg: cfg, aptConfigPath: confPath}, nil
}

func (c *Checker) Platform() string { return "debian" }

// Check aggregates the package and reboot checks into one Status. Unlike
// Ubuntu, Debian has no machine-readable "is there a newer release"
// endpoint equivalent to changelogs.ubuntu.com/meta-release, and no
// update-notifier equivalent that reliably populates a reboot-required
// marker, so OS.UpdateAvailable always stays false and RebootRequired is
// best-effort (true only if something else on the host happens to create
// /var/run/reboot-required).
func (c *Checker) Check(ctx context.Context, previous *checker.Status) (checker.Status, error) {
	status := checker.Status{
		Hostname:  c.cfg.Hostname,
		Platform:  c.Platform(),
		CheckedAt: time.Now(),
	}

	var errs []string

	if pkgResult, err := c.checkPackages(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("packages: %v", err))
		if previous != nil {
			status.Packages = previous.Packages
		}
	} else {
		status.Packages = checker.PackageInfo{
			UpgradableTotal:    pkgResult.Total,
			UpgradableSecurity: pkgResult.Security,
			Upgrades:           pkgResult.Upgrades,
		}
	}

	if required, pkgs, err := reboot.Check(c.cfg.RebootRequiredFile); err != nil {
		errs = append(errs, fmt.Sprintf("reboot: %v", err))
		if previous != nil {
			status.RebootRequired = previous.RebootRequired
			status.RebootRequiredPackages = previous.RebootRequiredPackages
		}
	} else {
		status.RebootRequired = required
		status.RebootRequiredPackages = pkgs
	}

	osInfo, err := readOSInfo(c.cfg.OSReleaseFile)
	if err != nil {
		errs = append(errs, fmt.Sprintf("os-release: %v", err))
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

func (c *Checker) checkPackages(ctx context.Context) (packageResult, error) {
	if err := aptutil.Update(ctx, c.aptConfigPath); err != nil {
		return packageResult{}, err
	}
	return checkUpgradable(ctx, c.aptConfigPath)
}

func readOSInfo(osReleaseFile string) (checker.OSInfo, error) {
	raw, err := os.ReadFile(osReleaseFile)
	if err != nil {
		return checker.OSInfo{}, fmt.Errorf("os-release: reading %s: %w", osReleaseFile, err)
	}
	rel := osrelease.Parse(string(raw))
	return checker.OSInfo{
		CurrentVersion:  rel["VERSION_ID"],
		CurrentCodename: rel["VERSION_CODENAME"],
		UpdateAvailable: false,
	}, nil
}
