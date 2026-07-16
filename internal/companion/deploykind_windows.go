//go:build windows

package companion

import (
	"golang.org/x/sys/windows/svc/mgr"
)

// nativeUnitPresent checks whether a Windows Service named after the
// component exists on this host. Service names match the systemd unit
// name convention 1:1 (e.g. "update-detector", "update-aggregator",
// "update-detector-companion"), so componentUnitName needs no change.
//
// OpenService succeeds if the service exists regardless of its current
// state (running, stopped, disabled) -- same semantics as the Linux
// path's stat-of-the-unit-file, which also ignores enabled/active state.
func nativeUnitPresent(name string) bool {
	m, err := mgr.Connect()
	if err != nil {
		// Service manager not accessible (unlikely on a real Windows
		// host, but graceful degradation is better than a panic).
		return false
	}
	defer m.Disconnect()

	svc, err := m.OpenService(name)
	if err != nil {
		return false
	}
	svc.Close()
	return true
}
