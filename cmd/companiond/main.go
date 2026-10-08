// Command companiond runs the CompAInion backend daemon: SQLite store, HTTP
// API (with SSE event stream) and the subscription dispatcher.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arnaubennassar/compainion/internal/api"
	"github.com/arnaubennassar/compainion/internal/config"
	"github.com/arnaubennassar/compainion/internal/notify"
	"github.com/arnaubennassar/compainion/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Default().Error("companiond: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	log.Info("store opened", "path", cfg.DBPath)

	srv := api.NewWithLogger(db, log)

	// Subscription dispatcher (webhook + command hooks) polls the events
	// table; cancelled together with the HTTP server on shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&notify.Dispatcher{DB: db, Logger: log}).Run(ctx)

	// Interruption expiry enforcement: every 30s sweep lapsed
	// interruptions; expiry escalates urgency only (bump to urgent + notify),
	// never auto-closes a user decision. Runs until shutdown.
	go expireLoop(ctx, db, log)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		log.Info("shutting down", "signal", sig.String())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	cancel() // stop the dispatcher
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}
