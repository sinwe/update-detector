// Command update-detector runs a long-lived detection loop: on a timer, it
// checks for available package/security updates, pending-reboot state, and
// OS release upgrades, serves the result over HTTP for Gatus to poll, and
// notifies configured channels (Telegram today) on meaningful changes.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"update-detector/internal/agentstream"
	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
	"update-detector/internal/checker"
	"update-detector/internal/companion"
	"update-detector/internal/companiontoken"
	"update-detector/internal/config"
	"update-detector/internal/hostflavor"
	"update-detector/internal/httpserver"
	"update-detector/internal/notifier"
	"update-detector/internal/state"
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
	log.Printf("update-detector %s", version.Version)

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// The agent runs inside an Ubuntu-based container regardless of the
	// host it's deployed on, so detection must be driven by the *host's*
	// os-release (host-mounted), not the container's own. This is how a
	// Raspberry Pi OS / plain Debian host gets its own checker, rather than
	// Ubuntu-only tooling (apt-check) that doesn't exist there.
	flavor := hostflavor.Detect(cfg.OSReleaseFile)
	log.Printf("detected host OS flavor: %s", flavor)

	// Which platform packages are even available to select here is
	// decided entirely by which of platforms_unix.go/platforms_windows.go
	// got compiled in (their own build tags, blank-importing ubuntu/debian
	// or windows for their init()-time checker.Register calls) -- main.go
	// itself no longer names any platform package directly, so it compiles
	// identically regardless of GOOS.
	chk, err := checker.New(flavor, cfg.CheckerFields())
	if err != nil {
		return err
	}

	var notifiers []notifier.Notifier
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		notifiers = append(notifiers, notifier.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID))
		log.Println("telegram notifications enabled")
	} else {
		log.Println("telegram notifications disabled (TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID not set)")
	}
	notifyMgr := notifier.NewManager(notifiers...)

	var aggClient *aggregatorclient.Client
	var companionSrv *companiontoken.Server
	var identity aggregatorclient.Identity
	if cfg.AggregatorURL != "" {
		var err error
		identity, err = aggregatorclient.LoadOrCreateIdentity(cfg.AgentIdentityFile)
		if err != nil {
			return err
		}
		aggClient = aggregatorclient.New(cfg.AggregatorURL, identity)
		log.Printf("aggregator push enabled (%s), agent id %s", cfg.AggregatorURL, identity.AgentID)

		// Only worth serving once there's an aggregator/identity for a
		// companion to actually use.
		companionSrv, err = companiontoken.Listen(cfg.CompanionSocketPath, identity)
		if err != nil {
			return err
		}
		go func() {
			if err := companionSrv.Serve(); err != nil && err != http.ErrServerClosed {
				log.Printf("companion token socket: %v", err)
			}
		}()
		log.Printf("companion token socket listening on %s", cfg.CompanionSocketPath)
	} else {
		log.Println("aggregator push disabled (AGGREGATOR_URL not set)")
	}

	store := state.NewStore(cfg.StateFile)
	previous, err := store.Load()
	if err != nil {
		log.Printf("state: %v (starting fresh)", err)
		previous = nil
	}

	srv := httpserver.New()
	if previous != nil {
		srv.SetStatus(*previous)
	}

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Handler(),
	}

	go func() {
		log.Printf("listening on %s", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http server: %v", err)
		}
	}()

	if aggClient != nil {
		// Holds the aggregator's stream connection whenever no companion
		// is running (or hasn't connected yet) -- the aggregator's
		// CompanionHub always lets a companion preempt this, since only
		// it can carry out apply-type actions; this only ever receives
		// (and can only ever receive, per that same server-side gate)
		// ActionRecheck. Handled in-process, unlike the companion's own
		// loopback HTTP call, since the agent already is that process.
		onAction := func(action aggregator.Action) {
			switch action.Type {
			case aggregator.ActionRecheck:
				srv.TriggerRecheck()
				resultCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if err := aggClient.ReportActionResult(resultCtx, action.ID, true, "recheck triggered"); err != nil {
					log.Printf("aggregator: reporting recheck result for %s: %v", action.ID, err)
				}
				cancel()
			case aggregator.ActionCompleteCompanionSwap:
				result := companion.CompleteCompanionSwap(ctx, action)
				resultCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if err := aggClient.ReportActionResult(resultCtx, action.ID, result.Success, result.Message); err != nil {
					log.Printf("aggregator: reporting companion swap result for %s: %v", action.ID, err)
				}
				cancel()
			default:
				log.Printf("aggregator: ignoring unexpected action type %q on agent stream", action.Type)
			}
		}
		// aggregatorPresent is meaningless for a plain agent connection
		// (only a companion ever runs the aggregator-colocation check --
		// see CompanionHub.SetAggregatorPresent), so always false here.
		go agentstream.Run(ctx, cfg.AggregatorURL, identity, aggregator.KindAgent, false, false, onAction)
	}

	if aggClient != nil {
		enrollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		aggStatus, err := aggClient.Enroll(enrollCtx, cfg.Hostname)
		cancel()
		if err != nil {
			log.Printf("aggregator: enroll failed (will retry on next report): %v", err)
		} else {
			log.Printf("aggregator: enrollment status: %s", aggStatus)
		}
	}

	runCheck := func(first bool) {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		status, err := chk.Check(checkCtx, previous)
		cancel()
		if err != nil {
			log.Printf("check failed: %v", err)
			return
		}
		if len(status.Errors) > 0 {
			log.Printf("check completed with errors: %v", status.Errors)
		}
		status.AgentVersion = version.Version

		changes := state.Diff(previous, status)
		if len(changes) > 0 || (first && cfg.NotifyOnStartup) {
			notifyChanges := changes
			if first && cfg.NotifyOnStartup {
				notifyChanges = append([]string{"agent started"}, changes...)
			}
			notifyMgr.Send(ctx, notifier.Event{
				Hostname: cfg.Hostname,
				Status:   status,
				Previous: previous,
				Changes:  notifyChanges,
			})
		}

		if err := store.Save(status); err != nil {
			log.Printf("state: saving: %v", err)
		}

		if aggClient != nil {
			reportCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			if err := aggClient.Report(reportCtx, status); err != nil {
				log.Printf("aggregator: report failed: %v", err)
			}
			cancel()
		}

		srv.SetStatus(status)
		previous = &status
	}

	runCheck(true)

	ticker := time.NewTicker(cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			if companionSrv != nil {
				_ = companionSrv.Close()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return httpSrv.Shutdown(shutdownCtx)
		case <-ticker.C:
			runCheck(false)
		case <-srv.Recheck():
			log.Println("out-of-band recheck requested")
			runCheck(false)
			ticker.Reset(cfg.CheckInterval)
		}
	}
}
