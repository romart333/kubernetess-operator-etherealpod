// SundayApp is a small groceries HTTP API backed by SQLite. It is the demo
// workload for the EtherealPod operator: a single replica whose data must
// survive crashes and pod recreation (the database lives on a PVC).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultAddr   = ":8080"
	defaultDBPath = "/data/sunday.db"

	// shutdownTimeout must fit within the pod's terminationGracePeriodSeconds
	// (5s in the shipped manifest) so a SIGTERM always ends in a clean exit 0.
	shutdownTimeout = 4 * time.Second
)

// envOr lets the environment override a flag default; explicit flags win.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := flag.String("addr", envOr("SUNDAY_ADDR", defaultAddr), "listen address")
	dbPath := flag.String("db", envOr("SUNDAY_DB_PATH", defaultDBPath), "path to the SQLite database file")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *addr, *dbPath); err != nil {
		log.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, dbPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	store, err := NewSQLiteStore(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Error("close store", "error", err)
		}
	}()

	server := &http.Server{
		Addr:    addr,
		Handler: newHandler(store, log, os.Exit),
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "db", dbPath)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("shut down cleanly")
	return nil
}
