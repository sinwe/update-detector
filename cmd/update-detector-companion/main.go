// Command update-detector-companion is a host-native process (never
// containerized) that receives package-upgrade triggers from a central
// update-aggregator over SSE and applies them via apt-get, after
// independently validating each one against this host's own update-detector
// agent status. Installed via install.sh, one per host, alongside that
// host's update-detector container. Runs as root (systemd) because
// installing packages needs it -- the safety property is the companion's
// local validation and fixed command templates (see internal/companion),
// not its own privilege level.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"update-detector/internal/agentstream"
	"update-detector/internal/aggregator"
	"update-detector/internal/companion"
	"update-detector/internal/companionconfig"
	"update-detector/internal/version"
)

func main() {
	if err := start(); err != nil {
		log.Fatal(err)
	}
}

// runInteractive runs the main loop until an OS interrupt/terminate
// signal arrives -- correct as-is on every platform except when running
// as a genuine Windows Service (see start_windows.go), which never
// receives those signals from SCM at all.
func runInteractive() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}

func run(ctx context.Context) error {
	log.Printf("update-detector-companion %s", version.Version)

	cfg := companionconfig.Load()
	if cfg.AggregatorURL == "" {
		return fmt.Errorf("AGGREGATOR_URL is required")
	}

	identity, err := companion.FetchIdentityWithRetry(ctx, cfg.SocketPath, time.Minute)
	if err != nil {
		return err
	}
	log.Printf("companion: fetched identity for agent %s", identity.AgentID)

	// Checked once at startup, not per reconnect -- this host's deploy
	// shape essentially never changes within one process's lifetime, and
	// re-running it on every reconnect would mean shelling out to
	// systemctl/docker on every backoff retry for no benefit. If an
	// aggregator does get installed on this host later, restarting the
	// companion (which a self-update of anything on this host already
	// tends to do) picks that up.
	aggregatorPresent := companion.AggregatorColocated(ctx)
	log.Printf("companion: aggregator colocated on this host: %v", aggregatorPresent)
	agentPresent := companion.AgentColocated(ctx)
	log.Printf("companion: agent colocated on this host: %v", agentPresent)

	report := func(result aggregator.ActionResult) {
		// A fresh, short-lived context -- not the (possibly already
		// canceled, if this action landed during shutdown) outer ctx --
		// so the result still has a chance to be reported.
		reportCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := companion.ReportResult(reportCtx, cfg.AggregatorURL, identity, result); err != nil {
			log.Printf("companion: reporting result for %s: %v", result.ActionID, err)
		}
	}

	agentstream.Run(ctx, cfg.AggregatorURL, identity, aggregator.KindCompanion, aggregatorPresent, agentPresent, func(action aggregator.Action) {
		log.Printf("companion: received action %s (%s)", action.ID, action.Type)

		// Live output for whatever command Apply is about to run, best-effort
		// and non-blocking all the way down (see OutputSink.push and
		// StreamOutput's own doc comment) -- a slow/unreachable aggregator
		// must never stall the actual command. streamCtx is scoped to this
		// one action, not the whole process, so the streaming POST ends
		// promptly once Apply returns rather than living for cfg's entire
		// lifetime.
		sink := companion.NewOutputSink(1000)
		streamCtx, cancelStream := context.WithCancel(ctx)
		go func() {
			if err := companion.StreamOutput(streamCtx, cfg.AggregatorURL, identity, action.ID, sink); err != nil {
				log.Printf("companion: streaming output for %s: %v", action.ID, err)
			}
		}()
		actionCtx := companion.WithOutputSink(ctx, sink)
		defer func() {
			sink.Close()
			cancelStream()
		}()

		// Companion self-update has fundamentally different behavior
		// on Linux vs Windows:
		//
		// Linux: install.sh restarts this process (systemctl restart).
		// The companion process dies, but install.sh survives (Linux
		// inode semantics let the running process keep its open fd even
		// after the binary is renamed). So we must report optimistically
		// *before* calling Apply, since code after it may never run.
		//
		// Windows: the companion stages the new binary to .exe.new
		// and returns a Staged result (no restart). The agent (a
		// separate Windows Service on the same host) will later be
		// told to stop the companion, swap the binary, and restart it.
		// The result is reported normally.
		//
		// If Apply returns having failed on Linux, that's only a
		// *real* failure to correct the record with if ctx is still
		// alive -- once systemd's restart reaches this process
		// (SIGTERM, via the same ctx), the in-flight install.sh child
		// gets killed too, and Apply surfaces that as an ordinary-
		// looking failure ("signal: terminated") even though the
		// swap+restart actually succeeded. Confirmed live: without
		// this check, that spurious failure overwrote the correct
		// optimistic success report every time.
		if action.Type == aggregator.ActionSelfUpdate && action.Component == "companion" {
			if runtime.GOOS == "windows" {
				result := companion.Apply(actionCtx, cfg.AgentStatusURL, cfg.AggregatorURL, identity, action)
				report(result)
			} else {
				report(aggregator.ActionResult{
					ActionID: action.ID, Success: true,
					Message: "update installing, restarting shortly", CompletedAt: time.Now(),
				})
				if result := companion.Apply(actionCtx, cfg.AgentStatusURL, cfg.AggregatorURL, identity, action); !result.Success && ctx.Err() == nil {
					log.Printf("companion: self-update of companion failed before restarting: %s", result.Message)
					report(result)
				}
			}
			return
		}

		result := companion.Apply(actionCtx, cfg.AgentStatusURL, cfg.AggregatorURL, identity, action)
		if result.Success {
			log.Printf("companion: action %s succeeded", action.ID)
		} else {
			log.Printf("companion: action %s failed: %s", action.ID, result.Message)
		}
		report(result)
	})

	log.Println("companion: shutting down")
	return nil
}
