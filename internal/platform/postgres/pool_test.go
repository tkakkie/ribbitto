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
		name     string
		urlMax   string
		explicit int32
	}{
		{"pgx default", "", 0},
		{"URL preserved", "7", 0},
		{"explicit wins", "7", 36},
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
			if tt.explicit > 0 {
				want = tt.explicit
			}
			pool, err := postgres.OpenPoolWithOptions(t.Context(), u.String(), nil, postgres.PoolOptions{MaxConns: tt.explicit})
			requireNoError(t, err)
			t.Cleanup(pool.Close)
			if got := pool.Stat().MaxConns(); got != want {
				t.Fatalf("opened pool maximum = %d, want %d", got, want)
			}
		})
	}
	if _, err := postgres.OpenPoolWithOptions(t.Context(), base, nil, postgres.PoolOptions{MaxConns: -1}); err == nil {
		t.Fatal("negative pool maximum accepted")
	}
}
