package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
	infra "github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func appendEvents(tx platform.Tx) infra.EventAppender { return realtimepg.AppenderIn(tx) }

// newCleaner injects infra's real lock and boundary, so organization's
// writes stay covered until org's module moves.
func newCleaner(pool *pgxpool.Pool) *postgres.Cleaner {
	return postgres.NewCleaner(pool, infra.RetentionBoundaryIn)
}

func retentionState(t *testing.T, pool *pgxpool.Pool, id domain.ID) [2]int64 {
	t.Helper()
	var state [2]int64
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT event_log_boundary_seq,
		(SELECT count(*) FROM event_log WHERE organization_id = $1)
		FROM organization WHERE id = $1`, id).Scan(&state[0], &state[1]))
	return state
}

// One batch through the cleaner's step, in a transaction the test holds
// open: until commit a reader still sees the old boundary and every old row.
func TestEventRetentionTransaction(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "retention", "general")
	for range 2 {
		_, err := infra.NewPostingStore(pool, appendEvents).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "kept")
		requireNoError(t, err)
	}
	_, err := pool.Exec(ctx, "UPDATE event_log SET created_at = CASE WHEN seq = 2 THEN '2000-01-01'::timestamptz ELSE '2100-01-01'::timestamptz END WHERE organization_id = $1", f.OrganizationID)
	requireNoError(t, err)
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	reader := realtimepg.NewReader(pool, infra.EventBoundsIn, infra.EventKinds())
	requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
		count, err := newCleaner(pool).ExpireBatch(ctx, tx, f.OrganizationID, cutoff)
		requireNoError(t, err)
		if count != 1 {
			t.Fatalf("deleted %d rows, want 1", count)
		}
		rows, err := reader.EventsAfter(ctx, f.OrganizationID, 1, 10)
		requireNoError(t, err)
		if len(rows) != 2 {
			t.Fatalf("uncommitted cleanup hid rows: %v", rows)
		}
		return nil
	}))
	var remaining int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM event_log WHERE organization_id = $1 AND seq <= 2", f.OrganizationID).Scan(&remaining))
	if remaining != 0 {
		t.Fatalf("expired rows remain: %d", remaining)
	}
	rows, err := reader.EventsAfter(ctx, f.OrganizationID, 2, 10)
	requireNoError(t, err)
	if len(rows) != 1 || rows[0].Seq != 3 {
		t.Fatalf("boundary cursor lost recent event: %v", rows)
	}
	if _, err := reader.EventsAfter(ctx, f.OrganizationID, 1, 10); !errors.Is(err, realtime.ErrCursorExpired) {
		t.Fatalf("below boundary: %v", err)
	}
}

// failingRaise locks for real and fails the boundary update.
type failingRaise struct{ realtime.RetentionBoundary }

var errRaise = errors.New("raise failed")

func (failingRaise) RaiseBoundary(context.Context, domain.ID, int64) error { return errRaise }

// A failed boundary update rolls the batch's deletion back: the boundary
// moves with its deletion or not at all.
func TestEventRetentionFailedRaise(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "raise", "general")
	_, err := infra.NewPostingStore(pool, appendEvents).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "old")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE event_log SET created_at = '2000-01-01' WHERE organization_id = $1", f.OrganizationID)
	requireNoError(t, err)
	before := retentionState(t, pool, f.OrganizationID)
	boundary := func(tx platform.Tx) realtime.RetentionBoundary { return failingRaise{infra.RetentionBoundaryIn(tx)} }
	if err := postgres.NewCleaner(pool, boundary).ExpireEvents(ctx, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, errRaise) {
		t.Fatalf("cleanup = %v, want the raise's error", err)
	}
	if got := retentionState(t, pool, f.OrganizationID); got != before {
		t.Fatalf("(boundary, rows) = %v after a failed raise, want %v", got, before)
	}
}
