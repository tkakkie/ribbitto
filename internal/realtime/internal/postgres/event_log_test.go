package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// assertEventLog takes the scenario's boundary: compared with the boundary it
// reads, a boundary raised over a lost row would still balance the count.
func assertEventLog(t *testing.T, pool *pgxpool.Pool, org kernel.ID, wantBoundary, wantSeq int64) {
	t.Helper()
	var seq, boundary, count, valid int64
	requireNoError(t, pool.QueryRow(t.Context(), `
		SELECT o.event_seq, o.event_log_boundary_seq, count(e.seq),
		 count(e.seq) FILTER (WHERE e.seq > o.event_log_boundary_seq AND e.seq <= o.event_seq
		  AND e.audience_member_id IS NULL AND e.created_at IS NOT NULL AND (
		   (e.kind = 'message.posted' AND EXISTS (SELECT FROM message m
		    WHERE m.organization_id = o.id AND m.event_seq = e.seq
		    AND e.data = jsonb_build_object('channel_id', m.channel_id, 'message_id', m.id, 'topic_id', m.topic_id))) OR
		   (e.kind = 'member.joined' AND EXISTS (SELECT FROM member m
		    WHERE m.organization_id = o.id AND m.joined_event_seq = e.seq
		    AND e.data = jsonb_build_object('member_id', m.id)))))
		FROM organization o LEFT JOIN event_log e ON e.organization_id = o.id
		WHERE o.id = $1 GROUP BY o.id`, org).Scan(&seq, &boundary, &count, &valid))
	// The primary key makes count == interval length prove there are no gaps.
	if seq != wantSeq || boundary != wantBoundary || count != seq-boundary || valid != count {
		t.Fatalf("event log: seq=%d boundary=%d rows=%d valid=%d; want seq=%d boundary=%d", seq, boundary, count, valid, wantSeq, wantBoundary)
	}
}

func TestEventLogMigration(t *testing.T) {
	t.Parallel()
	pool := pgtest.NewEmpty(t)
	ctx := t.Context()
	migrator := pgtest.NewMigrator(t, pool)
	migrator.UpTo(ctx, 6)
	// Raw SQL writes what the binary of migration 6 wrote: today's stores
	// need tables and columns that do not exist yet.
	old := conversationtest.OrganizationFixture{OrganizationID: orgtest.Organization(t, pool, "old", "old", 1)}
	old.AccountID = identitytest.Account(t, pool, "old@example.org", "old")
	old.MemberID = orgtest.Member(t, pool, old.OrganizationID, old.AccountID, org.RoleOwner, "owner", 1)
	requireNoError(t, pool.QueryRow(ctx, "INSERT INTO channel (organization_id, name, is_default) VALUES ($1, 'general', true) RETURNING id", old.OrganizationID).Scan(&old.Channel.ID))
	_, err := pool.Exec(ctx, "INSERT INTO message (organization_id, channel_id, member_id, body, event_seq) VALUES ($1, $2, $3, 'before logging', 2)", old.OrganizationID, old.Channel.ID, old.MemberID)
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE organization SET event_seq = 2 WHERE id = $1", old.OrganizationID)
	requireNoError(t, err)
	empty := orgtest.Organization(t, pool, "empty", "Empty", 0)
	migrator.Up(ctx)
	assertEventLog(t, pool, old.OrganizationID, 2, 2)
	assertEventLog(t, pool, empty, 0, 0)
	// A server still running the previous binary takes a sequence and
	// inserts a message without an event row; the commit must fail so the
	// log keeps no gap.
	stale, err := pool.Begin(ctx)
	requireNoError(t, err)
	var seq int64
	requireNoError(t, stale.QueryRow(ctx, "UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq", old.OrganizationID).Scan(&seq))
	_, err = stale.Exec(ctx, "INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq) SELECT organization_id, id, default_topic_id, $2, 'old binary', $3 FROM channel WHERE id = $1", old.Channel.ID, old.MemberID, seq)
	requireNoError(t, err)
	var pgErr *pgconn.PgError
	if err := stale.Commit(ctx); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("commit without an event row: %v, want check_violation", err)
	}
	assertEventLog(t, pool, old.OrganizationID, 2, 2)
	// An update that takes two sequences must log both, not only the last.
	for _, rows := range [][]int64{{4}, {3, 4}} {
		jump, err := pool.Begin(ctx)
		requireNoError(t, err)
		_, err = jump.Exec(ctx, "UPDATE organization SET event_seq = event_seq + 2 WHERE id = $1", old.OrganizationID)
		requireNoError(t, err)
		for _, seq := range rows {
			_, err = jump.Exec(ctx, "INSERT INTO event_log (organization_id, seq, kind, data) VALUES ($1, $2, 'test.jump', '{}')", old.OrganizationID, seq)
			requireNoError(t, err)
		}
		err = jump.Commit(ctx)
		if len(rows) == 1 && (!errors.As(err, &pgErr) || pgErr.Code != "23514") {
			t.Fatalf("commit with sequence 3 unlogged: %v, want check_violation", err)
		}
		if len(rows) == 2 {
			requireNoError(t, err)
		}
	}
	// Drop the test rows as retention would, raising the boundary past them.
	_, err = pool.Exec(ctx, "DELETE FROM event_log WHERE organization_id = $1 AND kind = 'test.jump'", old.OrganizationID)
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE organization SET event_log_boundary_seq = 4 WHERE id = $1", old.OrganizationID)
	requireNoError(t, err)
	_, err = newPosting(pool).Post(ctx, membership(old), old.Channel.ID, "after logging")
	requireNoError(t, err)
	assertEventLog(t, pool, old.OrganizationID, 4, 5)
	// Undo every migration after 6, the event log's included.
	migrator.DownTo(ctx, 6)
	var removed bool
	requireNoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.event_log') IS NULL AND
		to_regprocedure('organization_event_seq_logged()') IS NULL AND
		NOT EXISTS (SELECT FROM information_schema.columns WHERE table_name = 'organization' AND column_name = 'event_log_boundary_seq')`).Scan(&removed))
	if !removed {
		t.Fatal("event log migration did not roll back")
	}
}

// The audience must be a member of the event's own organisation.
func TestEventLogAudience(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	f := conversationtest.OrganizationWithOwner(t, pool, "audience", "general")
	other := conversationtest.OrganizationWithOwner(t, pool, "other", "general")
	_, err := pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 2, 'future.private', $2, '{}')`, f.OrganizationID, other.MemberID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "event_log_organization_id_audience_member_id_fkey" {
		t.Fatalf("cross-organisation audience: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 2, 'future.private', $2, '{}')`, other.OrganizationID, other.MemberID)
	requireNoError(t, err)
}
