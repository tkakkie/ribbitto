package postgres_test

import (
	"context"
	"errors"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	infra "github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/realtime/internal/postgres"
)

var retentionCutoff = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func appendEvents(tx platform.Tx) infra.EventAppender { return postgres.AppenderIn(tx) }

// newCleaner injects org's real lock and boundary, so organization's
// writes stay covered.
func newCleaner(pool *pgxpool.Pool) *postgres.Cleaner {
	return postgres.NewCleaner(pool, orgpg.RetentionBoundaryIn)
}

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

func retentionState(t *testing.T, pool *pgxpool.Pool, id kernel.ID) [2]int64 {
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
	reader := postgres.NewReader(pool, orgpg.BoundsIn, infra.EventKinds())
	requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
		count, err := newCleaner(pool).ExpireBatch(ctx, tx, f.OrganizationID, retentionCutoff)
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

func (failingRaise) RaiseBoundary(context.Context, kernel.ID, int64) error { return errRaise }

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
	boundary := func(tx platform.Tx) realtime.RetentionBoundary { return failingRaise{orgpg.RetentionBoundaryIn(tx)} }
	if err := postgres.NewCleaner(pool, boundary).ExpireEvents(ctx, retentionCutoff); !errors.Is(err, errRaise) {
		t.Fatalf("cleanup = %v, want the raise's error", err)
	}
	if got := retentionState(t, pool, f.OrganizationID); got != before {
		t.Fatalf("(boundary, rows) = %v after a failed raise, want %v", got, before)
	}
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
		_, err := infra.NewPostingStore(pool, appendEvents).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "kept")
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
	err = newCleaner(cleaning).ExpireEvents(ctx, retentionCutoff)
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
	_, err = infra.NewPostingStore(pool, appendEvents).Post(ctx, a.OrganizationID, a.Channel.ID, a.MemberID, "A can still post")
	requireNoError(t, err)
	requireNoError(t, locked.Rollback(ctx))
	requireNoError(t, newCleaner(cleaning).ExpireEvents(ctx, retentionCutoff))
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
			_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, data, created_at)
				SELECT $1, seq, 'future.event', '{}', CASE WHEN seq = 2502 THEN $2::timestamptz
				ELSE '2000-01-01'::timestamptz END FROM generate_series(2502, 1, -1) seq`, org, retentionCutoff)
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
			err = newCleaner(cleaning).ExpireEvents(ctx, retentionCutoff)
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
			requireNoError(t, newCleaner(pool).ExpireEvents(t.Context(), retentionCutoff))
			if got := retentionState(t, pool, org); got != [2]int64{2501, 1} {
				t.Fatalf("final state = %v", got)
			}
		})
	}
}

// Deleting a batch below the existing boundary never lowers it: a lower
// sequence can expire later than a higher one.
func TestEventRetentionBoundaryNeverLowers(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	org := pgtest.Organization(t, pool, "monotonic", "Monotonic", 3)
	_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, data, created_at)
		SELECT $1, seq, 'future.event', '{}', CASE WHEN seq = 1 THEN '2000-01-01'::timestamptz ELSE now() END
		FROM generate_series(1, 3) seq`, org)
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE organization SET event_log_boundary_seq = 2 WHERE id = $1", org)
	requireNoError(t, err)
	requireNoError(t, newCleaner(pool).ExpireEvents(ctx, retentionCutoff))
	if got := retentionState(t, pool, org); got != [2]int64{2, 2} {
		t.Fatalf("(boundary, rows) = %v, want the boundary kept at 2 and seq 1 deleted", got)
	}
}

// A cleaner that waits behind a concurrent post's organisation lock deletes
// with a fresh statement after the lock, so it sees that post's commit.
func TestEventRetentionWaitsForPost(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := pgtest.OrganizationWithOwner(t, pool, "waits", "general")
	_, err := infra.NewPostingStore(pool, appendEvents).Post(ctx, f.OrganizationID, f.Channel.ID, f.MemberID, "old")
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE event_log SET created_at = '2000-01-01' WHERE organization_id = $1", f.OrganizationID)
	requireNoError(t, err)
	// The writer holds the organisation's row lock, as posting does, and
	// commits one more already-expired event while the cleaner waits.
	writer, err := pool.Begin(ctx)
	requireNoError(t, err)
	defer func() { _ = writer.Rollback(t.Context()) }()
	var seq int64
	requireNoError(t, writer.QueryRow(ctx, "UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq", f.OrganizationID).Scan(&seq))
	_, err = writer.Exec(ctx, "INSERT INTO event_log (organization_id, seq, kind, data, created_at) VALUES ($1, $2, 'future.event', '{}', '2000-01-01')", f.OrganizationID, seq)
	requireNoError(t, err)
	// One batch, so a later batch cannot hide a delete that missed the commit.
	type batch struct {
		deleted int64
		err     error
	}
	done := make(chan batch, 1)
	go func() {
		var b batch
		b.err = platform.InTx(ctx, pool, func(tx platform.Tx) error {
			var err error
			b.deleted, err = newCleaner(pool).ExpireBatch(ctx, tx, f.OrganizationID, retentionCutoff)
			return err
		})
		done <- b
	}()
	waitForLockWaiter(t, pool)
	requireNoError(t, writer.Commit(ctx))
	b := <-done
	requireNoError(t, b.err)
	if b.deleted != 2 {
		t.Fatalf("the batch deleted %d events, want 2: the writer's commit too", b.deleted)
	}
	if got := retentionState(t, pool, f.OrganizationID); got != [2]int64{seq, 0} {
		t.Fatalf("(boundary, rows) = %v, want [%d 0]", got, seq)
	}
}

// waitForLockWaiter returns once a session waits on a row lock.
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for range 500 {
		var waiting bool
		requireNoError(t, pool.QueryRow(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database())").Scan(&waiting))
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the cleaner never waited for the organisation lock")
}

// Retention deletes only the expired prefix in sequence order, so the
// boundary never passes a row still in the log: created_at is the writing
// transaction's start, which need not follow seq (#430).
func TestEventRetentionExpiredPrefix(t *testing.T) {
	t.Parallel()
	old, recent := "2000-01-01", "2010-01-01"
	cutoff := time.Date(2005, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		createdAt []string // by seq, from 1
		want      [2]int64 // boundary, rows left
		replay    []int64  // EventsAfter(the boundary)
	}{
		{"out of order", []string{recent, old}, [2]int64{0, 2}, []int64{1, 2}},
		{"expired, recent, expired", []string{old, recent, old}, [2]int64{1, 2}, []int64{2, 3}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := pgtest.New(t)
			ctx := t.Context()
			org := pgtest.Organization(t, pool, "prefix", "Prefix", int64(len(tt.createdAt)))
			for i, at := range tt.createdAt {
				_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, data, created_at)
					VALUES ($1, $2, 'future.event', '{}', $3::timestamptz)`, org, i+1, at)
				requireNoError(t, err)
			}
			requireNoError(t, newCleaner(pool).ExpireEvents(ctx, cutoff))
			if got := retentionState(t, pool, org); got != tt.want {
				t.Fatalf("(boundary, rows) = %v, want %v", got, tt.want)
			}
			events, err := postgres.NewReader(pool, orgpg.BoundsIn, infra.EventKinds()).EventsAfter(ctx, org, tt.want[0], 10)
			requireNoError(t, err)
			var seqs []int64
			for _, e := range events {
				seqs = append(seqs, e.Seq)
			}
			if !reflect.DeepEqual(seqs, tt.replay) {
				t.Fatalf("EventsAfter(%d) = %v, want %v", tt.want[0], seqs, tt.replay)
			}
		})
	}
}
