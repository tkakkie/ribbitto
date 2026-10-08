// Command devdb migrates worktree databases and runs administrative statements.
// It keeps admin credentials in the environment and suppresses driver errors,
// which can contain connection strings. Used by ai_env.py.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
)

func main() {
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
	if len(os.Args) == 2 {
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
