package postgres_test

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

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
