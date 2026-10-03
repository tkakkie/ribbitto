package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

// Observe completed COMMITs through a separate connection, before the next
// batch starts, without sleeps or production-only synchronization hooks.
type retentionCommitTracer struct{ committed func() }
type retentionCommitKey struct{}

func (tr retentionCommitTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, retentionCommitKey{}, data.SQL == "commit")
}

func (tr retentionCommitTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if committed, _ := ctx.Value(retentionCommitKey{}).(bool); committed && data.Err == nil {
		tr.committed()
	}
}

func retentionState(t *testing.T, pool *pgxpool.Pool, id domain.ID) [2]int64 {
	t.Helper()
	var state [2]int64
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT event_log_boundary_seq,
		(SELECT count(*) FROM event_log WHERE organization_id = $1)
		FROM organization WHERE id = $1`, id).Scan(&state[0], &state[1]))
	return state
}

func TestEventRetentionBlockedOrganization(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	a := pgtest.OrganizationWithOwner(t, pool, "a", "general")
	b := pgtest.OrganizationWithOwner(t, pool, "b", "general")
	// UUIDv7 IDs sort by creation time; assert the fixture's processing order.
	var ordered bool
	requireNoError(t, pool.QueryRow(ctx, "SELECT $1::uuid < $2::uuid", a.OrganizationID, b.OrganizationID).Scan(&ordered))
	if !ordered {
		t.Fatal("expected A to precede B")
	}
	for _, f := range []pgtest.OrganizationFixture{a, b} {
		_, err := postgres.NewPostingStore(pool, appendEvents).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "kept")
		requireNoError(t, err)
		_, err = pool.Exec(ctx, "UPDATE event_log SET created_at = '2000-01-01' WHERE organization_id = $1", f.OrganizationID)
		requireNoError(t, err)
	}
	locked, err := pool.Begin(ctx)
	requireNoError(t, err)
	defer func() { _ = locked.Rollback(t.Context()) }()
	_, err = locked.Exec(ctx, "SELECT id FROM organization WHERE id = $1 FOR UPDATE", b.OrganizationID)
	requireNoError(t, err)
	config := pool.Config()
	// A database lock timeout is deterministic: B stays locked for the run.
	config.ConnConfig.RuntimeParams["lock_timeout"] = "100ms"
	cleaning, err := pgxpool.NewWithConfig(ctx, config)
	requireNoError(t, err)
	defer cleaning.Close()
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	err = realtimepg.NewCleaner(cleaning, postgres.RetentionBoundaryIn).ExpireEvents(ctx, cutoff)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("cleanup = %v, want B's lock timeout", err)
	}
	if got := retentionState(t, pool, a.OrganizationID); got != [2]int64{2, 0} {
		t.Fatalf("A lost committed cleanup: %v", got)
	}
	if got := retentionState(t, pool, b.OrganizationID); got != [2]int64{0, 1} {
		t.Fatalf("B changed while locked: %v", got)
	}
	_, err = postgres.NewPostingStore(pool, appendEvents).Post(ctx, a.OrganizationID, a.Channel.ID, a.MemberID, "A can still post")
	requireNoError(t, err)
	requireNoError(t, locked.Rollback(ctx))
	requireNoError(t, realtimepg.NewCleaner(cleaning, postgres.RetentionBoundaryIn).ExpireEvents(ctx, cutoff))
	if got := retentionState(t, pool, b.OrganizationID); got != [2]int64{2, 0} {
		t.Fatalf("retry did not finish B: %v", got)
	}
}

func TestEventRetentionBatches(t *testing.T) {
	t.Parallel()
	for _, interrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "cancel after first commit"}[interrupt], func(t *testing.T) {
			pool := pgtest.New(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			org := pgtest.Organization(t, pool, "batches", "Batches", 2502)
			cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, data, created_at)
				SELECT $1, seq, 'future.event', '{}', CASE WHEN seq = 2502 THEN $2::timestamptz
				ELSE '2000-01-01'::timestamptz END FROM generate_series(2502, 1, -1) seq`, org, cutoff)
			requireNoError(t, err)
			var states [][2]int64
			config := pool.Config()
			config.ConnConfig.Tracer = retentionCommitTracer{committed: func() {
				states = append(states, retentionState(t, pool, org))
				if interrupt {
					cancel()
				}
			}}
			cleaning, err := pgxpool.NewWithConfig(ctx, config)
			requireNoError(t, err)
			defer cleaning.Close()
			err = realtimepg.NewCleaner(cleaning, postgres.RetentionBoundaryIn).ExpireEvents(ctx, cutoff)
			want := [][2]int64{{1000, 1502}, {2000, 502}, {2501, 1}, {2501, 1}}
			if interrupt {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cleanup = %v, want cancellation", err)
				}
				want = want[:1]
			} else {
				requireNoError(t, err)
			}
			if !reflect.DeepEqual(states, want) {
				t.Fatalf("committed (boundary, rows) = %v, want %v", states, want)
			}
			// A new run resumes partial work and never expires the cutoff itself.
			requireNoError(t, realtimepg.NewCleaner(pool, postgres.RetentionBoundaryIn).ExpireEvents(t.Context(), cutoff))
			if got := retentionState(t, pool, org); got != [2]int64{2501, 1} {
				t.Fatalf("final state = %v", got)
			}
		})
	}
}
