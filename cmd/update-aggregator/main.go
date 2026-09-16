// Command update-aggregator is the central service update-detector agents
// push their status to: it holds new agents as "pending" until approved on
// its /admin page, and exposes /widgets/* JSON for a Homepage dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorconfig"
	"update-detector/internal/notifier"
	"update-detector/internal/selfupdate"
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
	log.Printf("update-aggregator %s", version.Version)

	cfg, err := aggregatorconfig.Load()
	if err != nil {
		return err
	}

	registry := aggregator.NewRegistry(cfg.RegistryFile)
	if err := registry.Load(); err != nil {
		return err
	}

	// Fleet-wide alert switch + grace period, editable from /admin at
	// runtime. Seeded from the environment on first start (so upgrading
	// keeps the old OFFLINE_ALERT_AFTER behavior); the file wins after
	// that. The watcher always runs now — even with OFFLINE_ALERT_AFTER=0
	// — and simply stays silent until the switch is turned on in /admin.
	alertStore := aggregator.NewAlertStore(
		filepath.Join(filepath.Dir(cfg.RegistryFile), "alert-settings.json"),
		cfg.OfflineAlertAfter > 0, cfg.OfflineAlertAfter)
	if err := alertStore.Load(); err != nil {
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

	if cfg.AdminApplySharedSecret == "" {
		log.Println("apply endpoint disabled (ADMIN_APPLY_SHARED_SECRET not set)")
	} else {
		log.Println("apply endpoint enabled")
	}

	selfUpdateClient := selfupdate.New("", cfg.SelfUpdateChannel)
	log.Printf("self-update check enabled (channel=%s, interval=%s)",
		cfg.SelfUpdateChannel, cfg.SelfUpdateCheckInterval)

	hub := aggregator.NewCompanionHub()
	outputHub := aggregator.NewOutputHub()
	if alertStore.Get().Enabled {
		log.Printf("offline alerts enabled (after %s of continuous disconnection)", mustAlertAfter(alertStore))
	} else {
		log.Println("offline alerts disabled (fleet switch off — toggle in /admin)")
	}
	go aggregator.NewPresenceWatcher(registry, hub, notifyMgr, cfg.OfflineAlertAfter, alertStore).Run(ctx)
	srv := aggregator.NewServer(ctx, registry, hub, notifyMgr, cfg.AdminApplySharedSecret, selfUpdateClient, outputHub)
	srv.SetAlertInfo(cfg.OfflineAlertAfter, notifyMgr.Names())
	srv.SetAlertStore(alertStore)
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

	go selfupdate.Run(ctx, selfUpdateClient, cfg.SelfUpdateCheckInterval, func(err error) {
		log.Printf("self-update: checking for a new release: %v", err)
	})

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// mustAlertAfter resolves the seeded grace period for the startup log:
// the store's own value (just loaded, so valid unless hand-edited), else
// the 5m default. Only cosmetic — the watcher re-resolves every round.
func mustAlertAfter(store *aggregator.AlertStore) time.Duration {
	if d, ok := store.Get().AfterDuration(); ok {
		return d
	}
	return 5 * time.Minute
}
