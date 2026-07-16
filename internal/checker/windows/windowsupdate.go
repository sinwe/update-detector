//go:build windows

package windows

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// windowsUpdateScript queries the Windows Update Agent API (the same COM
// interface the Settings app's own "Check for updates" ultimately goes
// through) for updates that are available but not yet installed, and not
// hidden by the operator. Unlike winget (see packages.go/
// ErrWingetNotFound), this is this checker's *primary* signal -- it's
// the actual Windows Update mechanism, not a separately-managed package
// manager, and it carries a real MSRC severity rating winget's own
// output has no equivalent of at all. It's also expected to work under
// LocalSystem (install.bat's default) without the account workaround
// winget needs: the Windows Update service is a system-level service,
// not tied to a specific user's own package registration the way
// winget.exe's App Execution Alias is -- unconfirmed against a real
// LocalSystem-run service by this session, flagging rather than
// asserting it as fact.
//
// ConvertTo-Json -InputObject $updates, never `$updates | ConvertTo-Json`
// -- piping a collection with exactly 0 or 1 elements to ConvertTo-Json
// silently collapses it to the literal `null` or a bare (non-array)
// object instead of a JSON array, a well-known PowerShell gotcha. Passing
// the array via -InputObject instead keeps its array-ness regardless of
// how many elements it has.
const windowsUpdateScript = `
$ErrorActionPreference = 'Stop'
$session = New-Object -ComObject Microsoft.Update.Session
$searcher = $session.CreateUpdateSearcher()
$result = $searcher.Search('IsInstalled=0 and IsHidden=0')
$updates = @()
foreach ($u in $result.Updates) {
  $updates += [PSCustomObject]@{
    Title        = $u.Title
    KBArticleIDs = @($u.KBArticleIDs)
    IsMandatory  = [bool]$u.IsMandatory
    MsrcSeverity = [string]$u.MsrcSeverity
    UpdateID     = $u.Identity.UpdateID.ToString()
  }
}
ConvertTo-Json -InputObject $updates -Compress
`

// checkWindowsUpdates shells out to powershell to run windowsUpdateScript
// and parses its JSON output (see windowsupdate_parse.go). Errors here
// are real errors, unlike winget's own optional-signal posture -- the
// Windows Update Agent API is expected to always be present and callable
// on any real Windows host (it's how the OS itself checks for updates),
// so a failure here is worth surfacing rather than silently swallowed.
func checkWindowsUpdates(ctx context.Context) (packageResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", windowsUpdateScript)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return packageResult{}, fmt.Errorf("querying Windows Update: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseWindowsUpdateJSON(stdout.Bytes())
}
