package postgres_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestPoolUsableAfterStatementTimeout(t *testing.T) {
	t.Parallel()
	base := pgtest.NewEmpty(t)
	u, err := url.Parse(base.Config().ConnString())
	requireNoError(t, err)
	u.Path = "/" + base.Config().ConnConfig.Database
	q := u.Query()
	q.Set("statement_timeout", "50ms")
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pool, err := postgres.OpenPool(ctx, u.String(), nil)
	requireNoError(t, err)
	t.Cleanup(pool.Close)
	var before, after int
	requireNoError(t, pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&before))
	_, err = pool.Exec(ctx, "SELECT pg_sleep(1)")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
		t.Fatalf("sleep error = %v, want server query cancellation (57014)", err)
	}
	if got := pool.Stat().AcquiredConns(); got != 0 {
		t.Fatalf("acquired connections after cancellation = %d, want 0", got)
	}
	requireNoError(t, pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&after))
	if before != after {
		t.Fatalf("backend changed from %d to %d; want the same usable connection", before, after)
	}
	if stats := pool.Stat(); stats.AcquiredConns() != 0 || stats.IdleConns() != 1 {
		t.Fatalf("pool after reuse: acquired %d, idle %d; want 0, 1", stats.AcquiredConns(), stats.IdleConns())
	}
}

func TestPoolMaximum(t *testing.T) {
	t.Parallel()
	base := pgtest.New(t).Config().ConnString()
	for _, tt := range []struct {
		name   string
		urlMax string
	}{
		{"pgx default", ""},
		{"URL preserved", "7"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(base)
			requireNoError(t, err)
			q := u.Query()
			if tt.urlMax != "" {
				q.Set("pool_max_conns", tt.urlMax)
			}
			u.RawQuery = q.Encode()
			config, err := pgxpool.ParseConfig(u.String())
			requireNoError(t, err)
			want := config.MaxConns
			pool, err := postgres.OpenPool(t.Context(), u.String(), nil)
			requireNoError(t, err)
			t.Cleanup(pool.Close)
			if got := pool.Stat().MaxConns(); got != want {
				t.Fatalf("opened pool maximum = %d, want %d", got, want)
			}
		})
	}
}
