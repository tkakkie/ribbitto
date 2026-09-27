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

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/web"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
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
	if command == "serve" {
		return serve(ctx, os.Getenv("RIBBITTO_DATABASE_URL"))
	}
	db, err := postgres.Open(ctx, os.Getenv("RIBBITTO_DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer func() { _ = db.Close() }()
	return postgres.Migrate(ctx, db, command, os.Stdout)
}

func serve(ctx context.Context, databaseURL string) error {
	addr := os.Getenv("RIBBITTO_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	pool, err := postgres.OpenPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	sessions := auth.NewSessions(postgres.NewSessionStore(pool), time.Now)
	// One hasher for the whole process: its slots are the cap on concurrent
	// Argon2id work (DECISIONS.md 10).
	hasher, err := auth.NewHasher()
	if err != nil {
		return err
	}
	// Deferred after pool.Close, so it runs first: the clean-up must stop and
	// return its connection on every exit path, or Close would wait for it.
	stopCleanup := startSessionCleanup(ctx, sessions)
	defer stopCleanup()

	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		return err
	}
	handler, err := web.NewHandler(os.Getenv("RIBBITTO_DEV_ASSETS"), catalogues, web.Services{
		Sessions: sessions,
		SignIn:   auth.NewSignIn(postgres.NewAccountStore(pool), hasher, sessions),
	})
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

// startSessionCleanup deletes expired sessions at once and then hourly. The
// returned function cancels the loop, including a query in progress, and
// waits for it to finish.
func startSessionCleanup(ctx context.Context, sessions *auth.Sessions) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deleteExpiredSessions(ctx, sessions)
	}()
	return func() {
		cancel()
		<-done
	}
}

// deleteExpiredSessions runs until ctx ends. Failures are only logged:
// expired sessions are already rejected, so a missed run just leaves rows
// until the next one.
func deleteExpiredSessions(ctx context.Context, sessions *auth.Sessions) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := sessions.DeleteExpired(ctx); err != nil && ctx.Err() == nil {
			slog.Error("deleting expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
