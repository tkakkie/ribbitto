package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/tkakkie/ribbitto/db/migrations"
)

// Open connects to PostgreSQL without changing the schema.
func Open(ctx context.Context, url string) (*sql.DB, error) {
	if url == "" {
		return nil, fmt.Errorf("database URL is empty")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parsing database configuration: %w", err)
	}
	db := stdlib.OpenDB(*config)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return db, nil
}

// Migrate applies or reports embedded migrations. Down reverts one migration.
func Migrate(ctx context.Context, db *sql.DB, command string, out io.Writer) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("creating migration provider: %w", err)
	}
	switch command {
	case "up":
		_, err = provider.Up(ctx)
	case "down":
		_, err = provider.Down(ctx)
	case "status":
		var statuses []*goose.MigrationStatus
		statuses, err = provider.Status(ctx)
		if err == nil {
			for _, status := range statuses {
				if _, err = fmt.Fprintf(out, "%05d\t%s\t%s\n", status.Source.Version, status.State, status.Source.Path); err != nil {
					break
				}
			}
		}
	default:
		return fmt.Errorf("unknown migration command %q", command)
	}
	if err != nil {
		return fmt.Errorf("migrating %s: %w", command, err)
	}
	return nil
}
