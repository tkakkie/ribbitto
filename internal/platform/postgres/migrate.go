package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
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
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("creating migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
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

// CheckSchema refuses schemas older than the embedded migrations, permitting
// newer schemas so an operator can roll back the binary. It never changes schema.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("creating schema check provider: %w", err)
	}
	sources := provider.ListSources()
	newest := sources[len(sources)-1].Version
	// Provider.GetDBVersion initializes the version table; startup must be read-only.
	store, err := database.NewStore(goose.DialectPostgres, goose.DefaultTablename)
	if err != nil {
		return fmt.Errorf("creating schema version store: %w", err)
	}
	current, err := store.GetLatestVersion(ctx, db)
	var pgErr *pgconn.PgError
	if errors.Is(err, database.ErrVersionNotFound) || errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		current, err = 0, nil // An empty or never-migrated database is behind too.
	}
	if err != nil {
		return fmt.Errorf("checking database schema version: %w", err)
	}
	if current < newest {
		return fmt.Errorf("database schema version %d is behind binary version %d; run ribbitto migrate up before starting the server", current, newest)
	}
	return nil
}
