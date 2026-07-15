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
	"syscall"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorconfig"
	"update-detector/internal/notifier"
	"update-detector/internal/selfupdate"
	"update-detector/internal/version"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	log.Printf("update-aggregator %s", version.Version)

	cfg, err := aggregatorconfig.Load()
	if err != nil {
		return err
	}

	registry := aggregator.NewRegistry(cfg.RegistryFile)
	if err := registry.Load(); err != nil {
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

	selfUpdateClient := selfupdate.New("", cfg.SelfUpdateIncludePreRelease)
	log.Printf("self-update check enabled (channel=%s, interval=%s)",
		selfUpdateChannelLabel(cfg.SelfUpdateIncludePreRelease), cfg.SelfUpdateCheckInterval)

	hub := aggregator.NewCompanionHub()
	outputHub := aggregator.NewOutputHub()
	srv := aggregator.NewServer(registry, hub, notifyMgr, cfg.AdminApplySharedSecret, selfUpdateClient, outputHub)
	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

func selfUpdateChannelLabel(includePreRelease bool) string {
	if includePreRelease {
		return "prerelease"
	}
	return "stable"
}
