// Command ribbitto runs the ribbitto chat server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("command failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// Stop on SIGTERM as well as Ctrl-C: container runtimes send SIGTERM and
	// expect the process to shut down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	args := os.Args[1:]
	command := "serve"
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "serve":
	case len(args) == 2 && args[0] == "migrate" && (args[1] == "up" || args[1] == "down" || args[1] == "status"):
		command = args[1]
	default:
		return fmt.Errorf("usage: ribbitto [serve | migrate up|down|status]")
	}
	db, err := postgres.Open(ctx, os.Getenv("RIBBITTO_DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer func() { _ = db.Close() }()
	if command != "serve" {
		return postgres.Migrate(ctx, db, command, os.Stdout)
	}
	return serve(ctx)
}

func serve(ctx context.Context) error {
	addr := os.Getenv("RIBBITTO_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	handler, err := web.NewHandler(os.Getenv("RIBBITTO_DEV_ASSETS"))
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// Bound header reading so a slow client cannot hold a connection
		// open forever. No WriteTimeout: long-lived SSE responses will
		// manage their own deadlines.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
