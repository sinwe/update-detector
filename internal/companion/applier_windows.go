//go:build windows

package companion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func init() {
	registerApplier(&windowsApplier{})
}

// windowsApplier is the sole registered Applier on Windows. Windows
// Update is the primary, required target -- the same primary/optional
// relationship internal/checker/windows/windows.go's own Check already
// established for detection -- with winget (delegated to wingetApplier)
// as an optional, best-effort supplement: a winget failure is folded
// into the returned output as a warning, never turned into this
// method's own error, since winget not working (e.g. under LocalSystem,
// see README.md) must never block applying real Windows Update items.
type windowsApplier struct{}

// Packages installs each requested name: names carrying a "(KBnnnnnnn)"
// marker (see splitPackageNames) are installed via the Windows Update
// Agent API's own download-then-install flow (installWindowsUpdatesByKB,
// the same COM interface windowsupdate.go's detection side already
// uses); everything else is delegated to wingetApplier (`winget upgrade
// --id <name>`), since a winget-sourced name never carries that marker.
func (w *windowsApplier) Packages(ctx context.Context, names []string) (string, error) {
	kbs, wingetNames := splitPackageNames(names)

	var combined strings.Builder
	var err error

	if len(kbs) > 0 {
		out, kbErr := installWindowsUpdatesByKB(ctx, kbs)
		combined.WriteString(out)
		err = kbErr
	}

	if len(wingetNames) > 0 {
		out, wingetErr := (&wingetApplier{}).Packages(ctx, wingetNames)
		if combined.Len() > 0 {
			combined.WriteString("\n")
		}
		combined.WriteString(out)
		switch {
		case wingetErr == nil:
			// nothing more to do
		case len(kbs) > 0:
			// Windows Update work was *also* requested alongside these
			// winget-sourced names, and its own outcome is already
			// reflected in err above -- report this failure as a note,
			// not this call's own error, so a real Windows Update
			// success isn't papered over as an overall failure just
			// because an unrelated winget item also failed.
			combined.WriteString(fmt.Sprintf("\n(winget-sourced item(s) also failed, see above: %v)", wingetErr))
		default:
			// Nothing but winget-sourced names were requested -- for
			// *this* request, winget working is the whole story, so its
			// failure genuinely is this call's own error, not something
			// to swallow as "non-fatal" (that label only applies when
			// winget is truly supplementary to other, successful work,
			// which isn't the case here). Confirmed live: a request for
			// a winget-sourced item on a companion that declined the
			// winget account setup (see install.bat/README) fails with
			// exactly this -- explained explicitly rather than left as
			// a bare exec error, since "why is it even trying winget"
			// is exactly what that looks like from the outside.
			if errors.Is(wingetErr, exec.ErrNotFound) {
				err = fmt.Errorf("%w -- this service isn't configured to run as an account winget works for; re-run install.bat and accept the winget account prompt for this service to fix", wingetErr)
			} else {
				err = wingetErr
			}
		}
	}

	return combined.String(), err
}

// Upgrade installs every currently pending Windows Update, then
// best-effort runs `winget upgrade --all` too (output appended, its own
// failure never turned into this call's error -- see this type's own
// doc comment).
func (w *windowsApplier) Upgrade(ctx context.Context) (string, error) {
	out, err := installAllPendingWindowsUpdates(ctx)
	if wingetOut, wingetErr := (&wingetApplier{}).Upgrade(ctx); wingetErr != nil {
		out += fmt.Sprintf("\nwinget (supplementary, non-fatal): %v\n%s", wingetErr, wingetOut)
	} else {
		out += "\n" + wingetOut
	}
	return out, err
}

// FullUpgrade is identical to Upgrade -- neither Windows Update nor
// winget has a dist-upgrade/major-version-step equivalent, the same
// semantic gap wingetApplier's own FullUpgrade already documents.
func (w *windowsApplier) FullUpgrade(ctx context.Context) (string, error) {
	return w.Upgrade(ctx)
}

// installWindowsUpdatesByKB downloads and installs only the updates
// whose own KBArticleIDs intersect kbs.
func installWindowsUpdatesByKB(ctx context.Context, kbs []string) (string, error) {
	quoted := make([]string, len(kbs))
	for i, kb := range kbs {
		quoted[i] = "'" + kb + "'"
	}
	selectTargets := fmt.Sprintf(`
$kbFilter = @(%s)
$targets = New-Object -ComObject Microsoft.Update.UpdateColl
foreach ($u in $result.Updates) {
  foreach ($kb in $u.KBArticleIDs) {
    if ($kbFilter -contains $kb) { $targets.Add($u) | Out-Null; break }
  }
}
`, strings.Join(quoted, ","))
	return runWindowsUpdateInstall(ctx, selectTargets)
}

// installAllPendingWindowsUpdates downloads and installs every update
// currently reported by the same search windowsupdate.go's own
// detection uses (IsInstalled=0 and IsHidden=0) -- no KB filter.
func installAllPendingWindowsUpdates(ctx context.Context) (string, error) {
	return runWindowsUpdateInstall(ctx, `
$targets = New-Object -ComObject Microsoft.Update.UpdateColl
foreach ($u in $result.Updates) { $targets.Add($u) | Out-Null }
`)
}

// runWindowsUpdateInstall runs the shared search-download-install
// sequence, with selectTargets (a PowerShell fragment referencing
// $result.Updates, set by the shared preamble below) filling in
// $targets. Download() and Install() are the synchronous COM calls (not
// BeginDownload/BeginInstall) -- deliberate, since this whole operation
// is itself already run in its own goroutine by execute.go's caller,
// with no fixed timeout on ctx (same as apt-get's own dist-upgrade,
// which can also run for many minutes).
//
// IUpdateDownloadResult/IInstallationResult.ResultCode uses the
// OperationResultCode enum: 2 (orcSucceeded) and 3
// (orcSucceededWithErrors) are both treated as success here -- a
// multi-update batch partially succeeding is still real, useful
// progress, the same "still worth reporting, not a hard failure"
// posture install.sh's own apt-get autoremove step already takes.
// 0/1/4/5 (NotStarted/InProgress/Failed/Aborted) are real failures.
func runWindowsUpdateInstall(ctx context.Context, selectTargets string) (string, error) {
	script := `
$ErrorActionPreference = 'Stop'
$session = New-Object -ComObject Microsoft.Update.Session
$searcher = $session.CreateUpdateSearcher()
$result = $searcher.Search('IsInstalled=0 and IsHidden=0')
` + selectTargets + `
if ($targets.Count -eq 0) {
  Write-Output 'no matching pending updates found (already installed, or not currently offered)'
  exit 0
}
Write-Output ("found {0} update(s), downloading..." -f $targets.Count)
$downloader = $session.CreateUpdateDownloader()
$downloader.Updates = $targets
$downloadResult = $downloader.Download()
Write-Output ("download result code: {0}" -f $downloadResult.ResultCode)
if ($downloadResult.ResultCode -eq 4 -or $downloadResult.ResultCode -eq 5) {
  Write-Error ("download failed, result code {0}" -f $downloadResult.ResultCode)
  exit 1
}
Write-Output 'installing...'
$installer = $session.CreateUpdateInstaller()
$installer.Updates = $targets
$installResult = $installer.Install()
Write-Output ("install result code: {0}, reboot required: {1}" -f $installResult.ResultCode, $installResult.RebootRequired)
if ($installResult.ResultCode -eq 4 -or $installResult.ResultCode -eq 5) {
  Write-Error ("install failed, result code {0}" -f $installResult.ResultCode)
  exit 1
}
`
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("windows update install: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
