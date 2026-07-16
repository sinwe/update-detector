//go:build !windows

package companion

import (
	"fmt"
	"os"
)

// systemdUnitDir is where install.sh always writes a native unit -- a
// var, not a const, purely so tests can point it at a temp dir instead
// of the real /etc/systemd/system.
var systemdUnitDir = "/etc/systemd/system"

// nativeUnitPresent mirrors install.sh's own native_unit_present: a
// systemd unit file at systemdUnitDir/<name>.service is the canonical
// signal that the component was installed natively on this host.
func nativeUnitPresent(name string) bool {
	_, err := os.Stat(fmt.Sprintf("%s/%s.service", systemdUnitDir, name))
	return err == nil
}
