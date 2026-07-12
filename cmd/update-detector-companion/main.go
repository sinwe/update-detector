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

	"update-detector/internal/aggregator"
	"update-detector/internal/companion"
	"update-detector/internal/companionconfig"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
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

	companion.StreamRun(ctx, cfg.AggregatorURL, identity, func(action aggregator.Action) {
		log.Printf("companion: received action %s (%s)", action.ID, action.Type)

		result := companion.Apply(ctx, cfg.AgentStatusURL, action)
		if result.Success {
			log.Printf("companion: action %s succeeded", action.ID)
		} else {
			log.Printf("companion: action %s failed: %s", action.ID, result.Message)
		}

		// A fresh, short-lived context -- not the (possibly already
		// canceled, if this action landed during shutdown) outer ctx --
		// so the result still has a chance to be reported.
		reportCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := companion.ReportResult(reportCtx, cfg.AggregatorURL, identity, result); err != nil {
			log.Printf("companion: reporting result for %s: %v", action.ID, err)
		}
		cancel()
	})

	log.Println("companion: shutting down")
	return nil
}
