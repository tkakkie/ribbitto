// Command devdb migrates worktree databases, observes development capacity,
// runs commands with a worktree's database and ports (run) and checks the
// admin connection (health). It keeps admin credentials in the environment
// and suppresses driver errors, which can contain connection strings. The
// Makefile builds it as bin/ai-db; ai_env.py and cmd/capacity also use it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "run" || os.Args[1] == "health") {
		var err error
		if os.Args[1] == "run" {
			err = runWithEnvironment(os.Args[2:])
		} else {
			err = health()
		}
		var shown failure
		switch {
		case errors.As(err, &shown):
			fmt.Fprintln(os.Stderr, shown)
		case err != nil:
			fmt.Fprintln(os.Stderr, generic)
		default:
			return
		}
		os.Exit(1)
	}
	code, err := run()
	if err != nil {
		// The phase, never the driver error, is safe to expose to callers.
		messages := map[int]string{2: "admin URL not set or invalid", 3: "PostgreSQL stopped or unreachable", 4: "PostgreSQL statement failed"}
		fmt.Fprintln(os.Stderr, messages[code])
		os.Exit(code)
	}
}

func run() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if os.Getenv("RIBBITTO_TEST_DATABASE_URL") == "" {
		return 2, fmt.Errorf("admin configuration is required")
	}
	config, err := pgx.ParseConfig(os.Getenv("RIBBITTO_TEST_DATABASE_URL"))
	if err != nil {
		return 2, fmt.Errorf("parsing admin configuration: %w", err)
	}
	if len(os.Args) == 2 && os.Args[1] != "--observe" {
		config.Database = os.Args[1]
		db := stdlib.OpenDB(*config)
		defer func() { _ = db.Close() }()
		if err := db.PingContext(ctx); err != nil {
			return 3, fmt.Errorf("connecting for migration: %w", err)
		}
		return 4, postgres.Migrate(ctx, db, "up", io.Discard)
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return 3, fmt.Errorf("connecting to admin database: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if len(os.Args) == 2 && os.Args[1] == "--observe" {
		return 4, observe(conn)
	}
	query, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 4, fmt.Errorf("reading statement: %w", err)
	}
	rows, err := conn.Query(ctx, string(query))
	if err != nil {
		return 4, fmt.Errorf("executing statement: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return 4, fmt.Errorf("reading statement values: %w", err)
		}
		fmt.Println(values[0])
	}
	return 4, rows.Err()
}

// health reports whether the admin connection works, with the same phase
// messages as the statement mode and never the driver error.
func health() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig(os.Getenv("RIBBITTO_TEST_DATABASE_URL"))
	if os.Getenv("RIBBITTO_TEST_DATABASE_URL") == "" || err != nil {
		return failure("admin URL not set or invalid")
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return failure("PostgreSQL stopped or unreachable")
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
		return failure("PostgreSQL statement failed; check admin permissions")
	}
	fmt.Println("PostgreSQL reachable")
	return nil
}

func observe(conn *pgx.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var limit, peak int
	if err := conn.QueryRow(ctx, "SELECT current_setting('max_connections')::int, count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'").Scan(&limit, &peak); err != nil {
		return fmt.Errorf("reading observer baseline: %w", err)
	}
	// This handshake proves the observer owns a slot before checks begin.
	fmt.Printf("{\"max_connections\":%d,\"baseline\":%d}\n", limit, peak)
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(closed)
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-closed:
			fmt.Println(peak)
			return nil
		case <-ticker.C:
			count, err := clientCount(conn)
			if err != nil {
				return err
			}
			peak = max(peak, count)
		}
	}
}

func clientCount(conn *pgx.Conn) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'").Scan(&count); err != nil {
		return 0, fmt.Errorf("sampling client backends: %w", err)
	}
	return count, nil
}
