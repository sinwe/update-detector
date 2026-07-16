//go:build windows

package companion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// installPsPath is where a cached copy of install.ps1 lives on Windows,
// refreshed whenever the companion itself updates. Shelling out to it
// reuses its already battle-tested download/atomic-swap/service-restart
// logic instead of duplicating any of it here. A var, not a const, so
// tests can point it at a fake script.
var installPsPath = `C:\Program Files\update-detector\install.ps1`

// installNative re-invokes install.ps1 non-interactively to update
// component to targetVersion. The Windows Service restart for each
// component is bundled into install.ps1's own install step -- the
// companion's restart happens inside the script, so code after this
// call may never run for Component == "companion".
func installNative(ctx context.Context, component, targetVersion string) error {
	cmd := exec.CommandContext(ctx,
		"powershell", "-NonInteractive", "-File", installPsPath,
		"-Component", component,
		"-Version", targetVersion,
	)
	cmd.Env = os.Environ()
	out, err := runCapped(ctx, cmd)
	if err != nil {
		return fmt.Errorf("selfupdate: install.ps1 failed: %w\n%s", err, out)
	}
	return nil
}
