package orgpg

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestTxRunnerAndSetupState(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	runner, state := newTxRunner(pool), newSetupState(pool)
	setup := func(slug string, result error) error {
		return runner.InTx(ctx, func(tx platform.Tx) error {
			writer := registrationWriterIn(tx)
			id, err := writer.CreateOrganization(ctx, slug, slug)
			requireNoError(t, err)
			requireNoError(t, writer.CompleteSetup(ctx, id))
			return result
		})
	}
	failure := errors.New("rolled back")
	if err := setup("rolled-back", failure); err != failure {
		t.Fatalf("an error from fn returns %v, want it as is", err)
	}
	if open, err := state.Open(ctx); err != nil || !open || counts(t, pool) != [4]int64{} {
		t.Fatalf("after a rollback: open = %t, %v, rows %v", open, err, counts(t, pool))
	}
	requireNoError(t, setup("committed", nil))
	if open, err := state.Open(ctx); err != nil || open || counts(t, pool) != [4]int64{1, 0, 1, 0} {
		t.Fatalf("after a commit: open = %t, %v, rows %v", open, err, counts(t, pool))
	}
}

func counts(t *testing.T, pool *pgxpool.Pool) [4]int64 {
	t.Helper()
	var c [4]int64
	if err := pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM organization), (SELECT count(*) FROM member), (SELECT count(*) FROM setup), (SELECT coalesce(sum(event_seq), 0) FROM organization)").Scan(&c[0], &c[1], &c[2], &c[3]); err != nil {
		t.Fatal(err)
	}
	return c
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
