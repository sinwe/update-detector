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
	"syscall"
	"time"

	"update-detector/internal/agentstream"
	"update-detector/internal/aggregator"
	"update-detector/internal/companion"
	"update-detector/internal/companionconfig"
	"update-detector/internal/version"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	log.Printf("update-detector-companion %s", version.Version)

	cfg := companionconfig.Load()
	if cfg.AggregatorURL == "" {
		return fmt.Errorf("AGGREGATOR_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	identity, err := companion.FetchIdentityWithRetry(ctx, cfg.SocketPath, time.Minute)
	if err != nil {
		return err
	}
	log.Printf("companion: fetched identity for agent %s", identity.AgentID)

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

	agentstream.Run(ctx, cfg.AggregatorURL, identity, aggregator.KindCompanion, func(action aggregator.Action) {
		log.Printf("companion: received action %s (%s)", action.ID, action.Type)

		// Updating the companion itself restarts this very process, via
		// install.sh's own systemctl restart -- report *before* running
		// it, since code after that restart call may never run at all.
		//
		// If Apply below returns having failed, that's only a *real*
		// failure to correct the record with if ctx is still alive --
		// once systemd's restart reaches this process (SIGTERM, via the
		// same ctx everything here is built on), the in-flight
		// install.sh child gets killed too, and Apply surfaces that as
		// an ordinary-looking failure ("signal: terminated") even though
		// the swap+restart it triggered actually succeeded. Confirmed
		// live: without this check, that spurious failure overwrote the
		// correct optimistic success report every time.
		if action.Type == aggregator.ActionSelfUpdate && action.Component == "companion" {
			report(aggregator.ActionResult{
				ActionID: action.ID, Success: true,
				Message: "update installing, restarting shortly", CompletedAt: time.Now(),
			})
			if result := companion.Apply(ctx, cfg.AgentStatusURL, action); !result.Success && ctx.Err() == nil {
				log.Printf("companion: self-update of companion failed before restarting: %s", result.Message)
				report(result)
			}
			return
		}

		result := companion.Apply(ctx, cfg.AgentStatusURL, action)
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
