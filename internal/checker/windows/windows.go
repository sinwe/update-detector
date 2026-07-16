//go:build windows

package windows

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows/registry"

	"update-detector/internal/checker"
)

// Config is deliberately minimal compared to ubuntu/debian's own: there
// are no host-mounted file paths to redirect here at all (winget and the
// registry are read live, as whatever process this is), since -- unlike
// the Linux agent, which always runs inside a container -- the Windows
// agent runs natively, never containerized (see
// docs/plugin-architecture-plan.md's explicit "Windows containers ...
// categorically out of scope" call-out).
type Config struct {
	Hostname string
}

type Checker struct {
	cfg Config
}

func New(cfg Config) (*Checker, error) {
	return &Checker{cfg: cfg}, nil
}

// init registers this package with internal/checker's registry under
// "windows", so main.go can select it by name without importing this
// package directly -- see checker.Fields for why the handoff is a plain
// string-keyed map rather than Config itself. Every key besides
// "hostname" that internal/config's CheckerFields always populates
// (apt_sources_list, os_release_file, ...) is simply unused here, same
// as debian already leaves release_upgrades_file unused.
func init() {
	checker.Register("windows", func(f checker.Fields) (checker.Checker, error) {
		return New(Config{Hostname: f["hostname"]})
	})
}

func (c *Checker) Platform() string { return "windows" }

// Check aggregates the package, reboot, and OS-version checks into one
// Status. No OS-upgrade detection in v1 (same posture as
// internal/checker/debian): OSInfo.UpdateAvailable always stays false,
// and CurrentVersion/CurrentCodename are populated purely
// informationally from the registry.
func (c *Checker) Check(ctx context.Context, previous *checker.Status) (checker.Status, error) {
	status := checker.Status{
		Hostname:  c.cfg.Hostname,
		Platform:  c.Platform(),
		CheckedAt: time.Now(),
	}

	var errs []string

	if pkgResult, err := checkUpgradable(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("packages: %v", err))
		if previous != nil {
			status.Packages = previous.Packages
		}
	} else {
		status.Packages = checker.PackageInfo{
			UpgradableTotal:    pkgResult.Total,
			UpgradableSecurity: pkgResult.Security, // always 0 -- winget has no security signal at all
			Upgrades:           pkgResult.Upgrades,
		}
	}

	// checkRebootRequired has no failure mode worth reporting (see its
	// own doc comment) -- nothing to fall back to previous for here.
	status.RebootRequired = checkRebootRequired()

	osInfo, err := readOSInfo()
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

// readOSInfo reads the OS's own display name/version straight from the
// registry -- purely informational, no upgrade-availability signal.
// Unlike checkRebootRequired's own keys, CurrentVersion is fundamental
// (present on every real Windows install since Windows 2000), so its
// absence is a genuine anomaly worth reporting as an error rather than
// silently treating as "no info."
func readOSInfo() (checker.OSInfo, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return checker.OSInfo{}, fmt.Errorf("opening CurrentVersion key: %w", err)
	}
	defer k.Close()

	productName, _, _ := k.GetStringValue("ProductName")
	displayVersion, _, _ := k.GetStringValue("DisplayVersion")
	if displayVersion == "" {
		// DisplayVersion was only added around Windows 10 1909+ --
		// older builds only have ReleaseId.
		displayVersion, _, _ = k.GetStringValue("ReleaseId")
	}

	return checker.OSInfo{
		CurrentVersion:  displayVersion,
		CurrentCodename: productName,
		UpdateAvailable: false,
	}, nil
}
