//go:build windows

// Package winsvc wraps golang.org/x/sys/windows/svc's Service Control
// Protocol boilerplate so each of this repo's three binaries doesn't have
// to duplicate it. A process registered with the Service Control Manager
// (via install.bat's own `sc create`) must call StartServiceCtrlDispatcher
// (which svc.Run does) within a few seconds of starting, or SCM assumes
// it's broken and kills it -- confirmed live, against a real installed
// service, as "Error 1053: The service did not respond to the start or
// control request in a timely fashion." A plain foreground binary (this
// repo's existing shape, correct as-is for systemd on Linux) has no idea
// it needs to speak this protocol at all, which is exactly what produced
// that error before this package existed.
package winsvc

import (
	"context"

	"golang.org/x/sys/windows/svc"
)

// Run registers this process as the Windows Service named name, calling
// runFn(ctx) as the actual application logic. Blocks until runFn returns
// or SCM tells this service to stop.
//
// SCM communicates a stop/shutdown request exclusively through the
// Service Control Protocol here, never via SIGTERM/Ctrl-C the way
// systemd or an interactive console would -- so runFn's own shutdown
// handling must key off ctx being cancelled (already true for all three
// of this repo's binaries, which already build their shutdown around a
// context), not signal.NotifyContext.
func Run(name string, runFn func(ctx context.Context) error) error {
	return svc.Run(name, &handler{runFn: runFn})
}

type handler struct {
	runFn func(ctx context.Context) error
}

// Execute implements svc.Handler.
func (h *handler) Execute(_ []string, changes <-chan svc.ChangeRequest, status chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.runFn(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case <-errCh:
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		case c := <-changes:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				// Wait for runFn to actually finish its own graceful
				// shutdown (e.g. http.Server.Shutdown) before reporting
				// Stopped -- reporting too early risks SCM tearing the
				// process down before that cleanup completes.
				<-errCh
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		}
	}
}
