//go:build windows

package main

import (
	"golang.org/x/sys/windows/svc"

	"update-detector/internal/winsvc"
)

// serviceName must match install.bat's own `sc create update-detector`
// name exactly -- this is how SCM routes its control requests back to
// this specific process.
const serviceName = "update-detector"

// start dispatches to winsvc.Run only when actually launched by SCM
// (install.bat's `sc start`) -- running the exact same binary directly
// from a console (e.g. to test it) behaves identically to every other
// platform, no Windows Service Control Protocol involved.
func start() error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return runInteractive()
	}
	return winsvc.Run(serviceName, run)
}
