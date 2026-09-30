package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

func TestQueryCounterClassifies(t *testing.T) {
	counter := postgres.NewQueryCounter()
	for _, sql := range []string{
		"begin", "BEGIN ISOLATION LEVEL REPEATABLE READ", "BEGIN\nISOLATION LEVEL READ COMMITTED",
		"  commit", "COMMIT\tAND NO CHAIN", "rollback", "\n\tROLLBACK\n",
		"-- name: GetOrganizationBySlug :one\nSELECT 1", "select 1", "committed_at", "",
	} {
		counter.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sql})
	}
	got := counter.Counts()
	if want := (postgres.QueryCounts{Queries: 4, Begins: 3, Commits: 2, Rollbacks: 2}); got != want {
		t.Fatalf("Counts() = %+v, want %+v", got, want)
	}
}

func TestOpenPoolTracer(t *testing.T) {
	t.Parallel()
	url := pgtest.New(t).Config().ConnString()
	ctx := t.Context()

	plain, err := postgres.OpenPool(ctx, url, nil)
	requireNoError(t, err)
	t.Cleanup(plain.Close)
	if tracer := plain.Config().ConnConfig.Tracer; tracer != nil {
		t.Fatalf("OpenPool without a counter installed tracer %T", tracer)
	}

	counter := postgres.NewQueryCounter()
	traced, err := postgres.OpenPool(ctx, url, counter)
	requireNoError(t, err)
	t.Cleanup(traced.Close)
	before := counter.Counts()
	requireNoError(t, pgx.BeginFunc(ctx, traced, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "SELECT 1")
		return err
	}))
	stop := errors.New("roll back")
	if err := pgx.BeginFunc(ctx, traced, func(pgx.Tx) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	after := counter.Counts()
	if after.Begins-before.Begins != 2 || after.Commits-before.Commits != 1 || after.Rollbacks-before.Rollbacks != 1 || after.Queries-before.Queries != 1 {
		t.Fatalf("counts moved from %+v to %+v", before, after)
	}
}
