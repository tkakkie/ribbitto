package compose_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func readTracedPool(t *testing.T, pool *pgxpool.Pool, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	c := pool.Config()
	c.ConnConfig.Tracer = tracer
	// Gated tests hold one connection per blocked read while streams keep
	// querying; pgxpool's default (the CPU count, at least 4) is too small
	// on CI's two-CPU runners.
	c.MaxConns = 16
	p, err := pgxpool.NewWithConfig(t.Context(), c)
	feedRequire(t, err)
	t.Cleanup(p.Close)
	return p
}
