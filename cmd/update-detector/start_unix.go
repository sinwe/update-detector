//go:build !windows

package main

// start just runs interactively -- systemd (see install.sh) manages this
// process as a plain foreground command (Type=simple), with no
// in-process protocol required, unlike a Windows Service (see
// start_windows.go).
func start() error {
	return runInteractive()
}
