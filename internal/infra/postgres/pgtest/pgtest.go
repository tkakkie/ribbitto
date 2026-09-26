package pgtest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tkakkie/ribbitto/db/migrations"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
)

const templateLock = 0x726962626974746f

// New returns a pool on a fresh clone of the migrated template.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return newDatabase(t, true)
}

// NewEmpty returns a pool on a fresh database without application migrations.
func NewEmpty(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return newDatabase(t, false)
}

func adminConnection(t *testing.T) *pgx.Conn {
	t.Helper()
	url, set := os.LookupEnv("RIBBITTO_TEST_DATABASE_URL")
	if !set && os.Getenv("RIBBITTO_REQUIRE_DB") != "1" {
		t.Skip("RIBBITTO_TEST_DATABASE_URL is unset")
	}
	if url == "" {
		t.Fatal("RIBBITTO_TEST_DATABASE_URL must be set and nonempty")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	require(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require(t, conn.Close(ctx))
	})
	return conn
}

func newDatabase(t *testing.T, migrated bool) *pgxpool.Pool {
	t.Helper()
	admin := adminConnection(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	template := "template0"
	if migrated {
		hash := sha256.New()
		files, err := fs.Glob(migrations.FS, "*.sql")
		require(t, err)
		for _, file := range files {
			data, err := migrations.FS.ReadFile(file)
			require(t, err)
			_, err = fmt.Fprintf(hash, "%s\x00%s\x00", file, data)
			require(t, err)
		}
		template = fmt.Sprintf("ribbitto_tmpl_%x", hash.Sum(nil)[:6])
		require(t, initialize(ctx, admin, template, nil))
	}
	name := "ribbitto_test_" + strings.ToLower(rand.Text())
	require(t, execute(ctx, admin, "CREATE DATABASE "+identifier(name)+" TEMPLATE "+identifier(template)))
	t.Cleanup(func() {
		// t.Context is canceled before cleanup; dropping must still run.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require(t, execute(ctx, admin, "DROP DATABASE "+identifier(name)+" WITH (FORCE)"))
	})
	config, err := pgxpool.ParseConfig(admin.Config().ConnString())
	require(t, err)
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require(t, err)
	// Cleanup is LIFO: close all pool connections before dropping the database.
	t.Cleanup(pool.Close)
	require(t, pool.Ping(ctx))
	return pool
}

// afterRename allows tests to interrupt publication while its transaction is open.
func initialize(ctx context.Context, admin *pgx.Conn, name string, afterRename func() error) (err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err == nil {
			err = execute(cleanup, admin, "SELECT pg_advisory_unlock($1)", templateLock)
		}
		if err != nil {
			_ = admin.Close(cleanup) // Closing also releases a session lock after errors.
		}
	}()
	if err = execute(ctx, admin, "SELECT pg_advisory_lock($1)", templateLock); err != nil {
		return err
	}
	var exists bool
	if err = admin.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return fmt.Errorf("checking template existence: %w", err)
	}
	if exists {
		return nil
	}
	rows, err := admin.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, 'ribbitto_tmpl_') AND strpos(datname, '_building_') > 0")
	if err != nil {
		return fmt.Errorf("finding unfinished templates: %w", err)
	}
	leftovers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("reading unfinished templates: %w", err)
	}
	for _, leftover := range leftovers {
		if err = execute(ctx, admin, "ALTER DATABASE "+identifier(leftover)+" WITH IS_TEMPLATE false"); err != nil {
			return err
		}
		if err = execute(ctx, admin, "DROP DATABASE "+identifier(leftover)+" WITH (FORCE)"); err != nil {
			return err
		}
	}
	buildingName := name + "_building_" + strings.ToLower(rand.Text())
	building := identifier(buildingName)
	if err = execute(ctx, admin, "CREATE DATABASE "+building+" TEMPLATE template0"); err != nil {
		return err
	}
	config := admin.Config().Copy()
	config.Database = buildingName
	db := stdlib.OpenDB(*config)
	err = postgres.Migrate(ctx, db, "up", io.Discard)
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("closing template connections: %w", closeErr)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting template publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "ALTER DATABASE "+building+" RENAME TO "+identifier(name)); err != nil {
		return fmt.Errorf("renaming template: %w", err)
	}
	if afterRename != nil {
		if err = afterRename(); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "ALTER DATABASE "+identifier(name)+" WITH IS_TEMPLATE true ALLOW_CONNECTIONS false"); err != nil {
		return fmt.Errorf("sealing template: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("publishing template: %w", err)
	}
	return nil
}

func identifier(name string) string { return pgx.Identifier{name}.Sanitize() }

func execute(ctx context.Context, conn *pgx.Conn, query string, args ...any) error {
	if _, err := conn.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("executing %s: %w", query, err)
	}
	return nil
}

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
