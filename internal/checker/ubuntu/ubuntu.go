//go:build !windows

// Package ubuntu implements checker.Checker for Ubuntu/Debian-based Linux
// hosts using apt, dpkg, and Ubuntu's own update-notifier/release-upgrader
// tooling. It never mutates host state: apt's writable paths are redirected
// to a container-owned directory via internal/aptutil.
package ubuntu

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"update-detector/internal/aptutil"
	"update-detector/internal/checker"
	"update-detector/internal/checker/reboot"
)

// init registers this package with internal/checker's registry under
// "ubuntu", so main.go can select it by name (via internal/config's
// CheckerFields) without importing this package directly -- see
// checker.Fields for why the handoff is a plain string-keyed map rather
// than Config itself.
func init() {
	checker.Register("ubuntu", func(f checker.Fields) (checker.Checker, error) {
		return New(Config{
			Hostname:            f["hostname"],
			AptSourcesList:      f["apt_sources_list"],
			AptSourcesListD:     f["apt_sources_list_d"],
			DpkgStatusFile:      f["dpkg_status_file"],
			AptListsCacheDir:    f["apt_lists_cache_dir"],
			OSReleaseFile:       f["os_release_file"],
			ReleaseUpgradesFile: f["release_upgrades_file"],
			RebootRequiredFile:  f["reboot_required_file"],
		})
	})
}

type Config struct {
	Hostname string

	AptSourcesList   string
	AptSourcesListD  string
	DpkgStatusFile   string
	AptListsCacheDir string

	OSReleaseFile       string
	ReleaseUpgradesFile string
	RebootRequiredFile  string
}

type Checker struct {
	cfg           Config
	aptConfigPath string
	httpClient    *http.Client
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
		return nil, fmt.Errorf("ubuntu checker: %w", err)
	}
	return &Checker{
		cfg:           cfg,
		aptConfigPath: confPath,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *Checker) Platform() string { return "ubuntu" }

// Check aggregates the package, reboot, and OS-release checks into one
// Status. The caller is expected to pass a ctx with a sensible overall
// deadline for the whole cycle (apt-get update and the meta-release fetch
// both hit the network).
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

	osInfo, err := checkOSRelease(ctx, c.httpClient, c.cfg.OSReleaseFile, c.cfg.ReleaseUpgradesFile)
	if err != nil {
		errs = append(errs, fmt.Sprintf("os-release: %v", err))
		status.OS = osInfo
		if previous != nil {
			// Preserve the last known upgrade-availability signal on
			// failure; keep whatever current-version info this cycle did
			// manage to read (e.g. os-release parsed fine but the
			// meta-release fetch failed).
			if status.OS.CurrentVersion == "" {
				status.OS.CurrentVersion = previous.OS.CurrentVersion
				status.OS.CurrentCodename = previous.OS.CurrentCodename
			}
			status.OS.UpdateAvailable = previous.OS.UpdateAvailable
			status.OS.LatestVersion = previous.OS.LatestVersion
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
