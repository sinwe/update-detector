//go:build darwin

package companion

import (
	"fmt"
	"os"
)

// launchdPlistDir is where install.sh writes LaunchDaemon plists -- a
// var, not a const, purely so tests can point it at a temp dir instead
// of the real /Library/LaunchDaemons (mirrors systemdUnitDir's own
// test hook on Linux).
var launchdPlistDir = "/Library/LaunchDaemons"

// nativeUnitPresent mirrors install.sh's own native_unit_present on
// macOS: a LaunchDaemon plist at launchdPlistDir/com.sinwe/<name>.plist
// is the canonical signal that the component was installed natively on
// this host (see install.sh's install_agent_launchd, which uses exactly
// this label scheme).
func nativeUnitPresent(name string) bool {
	_, err := os.Stat(fmt.Sprintf("%s/com.sinwe.%s.plist", launchdPlistDir, name))
	return err == nil
}
