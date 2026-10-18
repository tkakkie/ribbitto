package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/tkakkie/ribbitto/db/migrations"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func embeddedSources(t *testing.T, db *sql.DB) []*goose.Source {
	t.Helper()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	return provider.ListSources()
}

func TestCheckSchema(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"never migrated", "zero", "empty version table", "behind", "equal", "ahead"} {
		t.Run(state, func(t *testing.T) {
			pool := pgtest.NewEmpty(t)
			db := stdlib.OpenDBFromPool(pool)
			t.Cleanup(func() { _ = db.Close() })
			ctx := t.Context()
			sources := embeddedSources(t, db)
			newest := sources[len(sources)-1].Version
			if state != "never migrated" {
				m := pgtest.NewMigrator(t, pool)
				if state == "zero" || state == "empty version table" {
					if err := postgres.Migrate(ctx, db, "status", io.Discard); err != nil {
						t.Fatal(err)
					}
				} else {
					m.Up(ctx)
				}
				if state == "empty version table" {
					if _, err := pool.Exec(ctx, "DELETE FROM goose_db_version"); err != nil {
						t.Fatal(err)
					}
				}
				if state == "behind" {
					m.Down(ctx)
				}
				if state == "ahead" {
					if _, err := pool.Exec(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)", newest+1); err != nil {
						t.Fatal(err)
					}
				}
			}
			// A read-only connection also catches accidental version-table initialization.
			config := pool.Config()
			config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
			readOnly, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer readOnly.Close()
			err = postgres.CheckSchema(ctx, readOnly)
			if state == "equal" || state == "ahead" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "ribbitto migrate up") || !strings.Contains(err.Error(), fmt.Sprintf("binary version %d", newest)) {
				t.Fatalf("expected actionable schema error, got %v", err)
			}
			if state == "never migrated" {
				var absent bool
				if err := pool.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NULL").Scan(&absent); err != nil || !absent {
					t.Fatalf("startup changed empty schema: absent=%t, err=%v", absent, err)
				}
			}
		})
	}
}

func TestConcurrentMigrations(t *testing.T) {
	t.Parallel()
	pool := pgtest.NewEmpty(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := postgres.Migrate(ctx, db, "status", io.Discard); err != nil {
		t.Fatal(err)
	}
	gate, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	// Permit both runners to read version zero, but hold the first migration's
	// version INSERT. Without the session locker, the second also attempts DDL.
	if _, err := gate.Exec(ctx, "LOCK TABLE goose_db_version IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		config := pool.Config().ConnConfig.Copy()
		config.RuntimeParams["application_name"] = "concurrent-migration"
		runner := stdlib.OpenDB(*config)
		t.Cleanup(func() { _ = runner.Close() })
		go func() { results <- postgres.Migrate(ctx, runner, "up", io.Discard) }()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var arrived int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND application_name = 'concurrent-migration'
			AND (wait_event_type = 'Lock' OR query LIKE '%pg_try_advisory_lock%')`).Scan(&arrived); err != nil {
			t.Fatal(err)
		}
		if arrived == 2 {
			break
		}
		select {
		case err := <-results:
			t.Fatalf("migration returned before gate release: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err := gate.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	// Every embedded version must appear once, including on a second up run.
	for _, source := range embeddedSources(t, db) {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id = $1 AND is_applied", source.Version).Scan(&count); err != nil || count != 1 {
			t.Errorf("version %d applied %d times: %v", source.Version, count, err)
		}
	}
}
