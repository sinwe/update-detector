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
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := aggregatorconfig.Load()
	if err != nil {
		return err
	}

	registry := aggregator.NewRegistry(cfg.RegistryFile)
	if err := registry.Load(); err != nil {
		return err
	}

	srv := aggregator.NewServer(registry)
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

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
