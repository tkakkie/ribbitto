package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenPool connects the pgx pool that the sqlc queries use. Migrations keep
// using Open's database/sql handle, which goose needs. A nil counter installs
// no tracer at all, so the pool runs exactly as without metrics.
func OpenPool(ctx context.Context, url string, counter *QueryCounter) (*pgxpool.Pool, error) {
	return OpenPoolWithOptions(ctx, url, counter, PoolOptions{})
}

// PoolOptions holds explicit overrides of the URL's pgx pool configuration.
type PoolOptions struct {
	// MaxConns overrides pool_max_conns in the URL. Zero preserves the URL
	// setting or pgx's default; negative values are rejected.
	MaxConns int32
}

// OpenPoolWithOptions connects like OpenPool, applying options before creation.
func OpenPoolWithOptions(ctx context.Context, url string, counter *QueryCounter, options PoolOptions) (*pgxpool.Pool, error) {
	if options.MaxConns < 0 {
		return nil, fmt.Errorf("pool MaxConns must be non-negative")
	}
	if url == "" {
		return nil, fmt.Errorf("database URL is empty")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parsing database configuration: %w", err)
	}
	if counter != nil {
		config.ConnConfig.Tracer = counter
	}
	if options.MaxConns > 0 {
		config.MaxConns = options.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating database pool: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return pool, nil
}
