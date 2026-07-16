//go:build windows

package windows

import "golang.org/x/sys/windows/registry"

// checkRebootRequired checks three well-known reboot-pending signals, in
// order, all via world-readable HKLM reads -- no admin privilege or exec
// needed at all, the most reliable part of this whole checker. Unlike
// every other check here, this one has no meaningful failure mode to
// report: a key that doesn't exist just means "not pending," not an
// error, so this returns a plain bool.
//
// RebootRequiredPackages has no Windows analog and always stays empty --
// the same gap internal/checker/debian's own reboot detection already
// has.
func checkRebootRequired() bool {
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) {
		return true
	}
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		return true
	}
	return pendingFileRenameOperationsSet()
}

func keyExists(path string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	return true
}

// pendingFileRenameOperationsSet reports whether Session Manager's
// PendingFileRenameOperations value is set and non-empty -- a non-empty
// REG_MULTI_SZ here means the OS has files staged to be renamed/deleted
// on next boot, the same signal Windows Update and many installers use
// to indicate a pending reboot.
func pendingFileRenameOperationsSet() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	values, _, err := k.GetStringsValue("PendingFileRenameOperations")
	if err != nil {
		return false
	}
	return len(values) > 0
}
