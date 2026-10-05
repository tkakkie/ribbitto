package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/tkakkie/ribbitto/db/migrations"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/infra/postgres"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime/realtimepg"
)

func assertEventLog(t *testing.T, pool *pgxpool.Pool, org domain.ID, wantSeq int64) {
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
	if seq != wantSeq || count != seq-boundary || valid != count {
		t.Fatalf("event log: seq=%d boundary=%d rows=%d valid=%d; want seq=%d", seq, boundary, count, valid, wantSeq)
	}
}

func TestEventLogMigration(t *testing.T) {
	t.Parallel()
	pool := pgtest.NewEmpty(t)
	ctx := t.Context()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	requireNoError(t, err)
	_, err = provider.UpTo(ctx, 6)
	requireNoError(t, err)
	// Raw SQL writes what the binary of migration 6 wrote: today's stores
	// need tables and columns that do not exist yet.
	old := pgtest.OrganizationFixture{OrganizationID: pgtest.Organization(t, pool, "old", "old", 1)}
	old.AccountID = pgtest.Account(t, pool, "old@example.org", "old")
	old.MemberID = pgtest.Member(t, pool, old.OrganizationID, old.AccountID, org.RoleOwner, "owner", 1)
	requireNoError(t, pool.QueryRow(ctx, "INSERT INTO channel (organization_id, name, is_default) VALUES ($1, 'general', true) RETURNING id", old.OrganizationID).Scan(&old.Channel.ID))
	_, err = pool.Exec(ctx, "INSERT INTO message (organization_id, channel_id, member_id, body, event_seq) VALUES ($1, $2, $3, 'before logging', 2)", old.OrganizationID, old.Channel.ID, old.MemberID)
	requireNoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE organization SET event_seq = 2 WHERE id = $1", old.OrganizationID)
	requireNoError(t, err)
	empty := pgtest.Organization(t, pool, "empty", "Empty", 0)
	_, err = provider.Up(ctx)
	requireNoError(t, err)
	assertEventLog(t, pool, old.OrganizationID, 2)
	assertEventLog(t, pool, empty, 0)
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
	assertEventLog(t, pool, old.OrganizationID, 2)
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
	_, err = postgres.NewPostingStore(pool, eventSequence, appendEvents).Post(ctx, old.OrganizationID, old.Channel.ID, old.MemberID, "after logging")
	requireNoError(t, err)
	assertEventLog(t, pool, old.OrganizationID, 5)
	// Undo every migration after 6, the event log's included.
	_, err = provider.DownTo(ctx, 6)
	requireNoError(t, err)
	var removed bool
	requireNoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.event_log') IS NULL AND
		to_regprocedure('organization_event_seq_logged()') IS NULL AND
		NOT EXISTS (SELECT FROM information_schema.columns WHERE table_name = 'organization' AND column_name = 'event_log_boundary_seq')`).Scan(&removed))
	if !removed {
		t.Fatal("event log migration did not roll back")
	}
}

// completeSetup runs org's setup with conversation's default-channel creator.
// Its conversationpg import is test-only (R1 on #502); 4.12 removes it with
// this file.
func completeSetup(t *testing.T, pool *pgxpool.Pool) (org.SetupResult, error) {
	t.Helper()
	hasher, err := identity.NewHasher()
	requireNoError(t, err)
	return orgpg.NewSetup(pool, hasher, "secret",
		func(tx platform.Tx) org.AccountCreator { return identitypg.AccountCreatorIn(tx) },
		func(tx platform.Tx) org.EventAppender { return realtimepg.AppenderIn(tx) },
		func(tx platform.Tx) org.DefaultChannelCreator { return conversationpg.DefaultChannelCreatorIn(tx) },
	).Complete(t.Context(), "secret", org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "owner@example.org", DisplayName: "Owner", Handle: "owner", Password: "long enough password"})
}

func TestEventLogAudienceAndRollback(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	result, err := completeSetup(t, pool)
	requireNoError(t, err)
	other := pgtest.OrganizationWithOwner(t, pool, "other", "general")
	_, err = pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 2, 'future.private', $2, '{}')`, result.OrganizationID, other.MemberID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "event_log_organization_id_audience_member_id_fkey" {
		t.Fatalf("cross-organisation audience: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO event_log (organization_id, seq, kind, audience_member_id, data)
		VALUES ($1, 2, 'future.private', $2, '{}')`, other.OrganizationID, other.MemberID)
	requireNoError(t, err)
	var channelID, memberID domain.ID
	requireNoError(t, pool.QueryRow(ctx, `SELECT c.id, m.id FROM channel c JOIN member m USING (organization_id)
		WHERE c.organization_id = $1`, result.OrganizationID).Scan(&channelID, &memberID))
	// Fail after the event insert, so the message, sequence and log must all roll back.
	_, err = pool.Exec(ctx, `CREATE FUNCTION refuse_event() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused'; END $$;
		CREATE TRIGGER refuse_event AFTER INSERT ON event_log FOR EACH ROW EXECUTE FUNCTION refuse_event()`)
	requireNoError(t, err)
	if _, err := postgres.NewPostingStore(pool, eventSequence, appendEvents).Post(ctx, result.OrganizationID, channelID, memberID, "rolled back"); err == nil {
		t.Fatal("post succeeded despite event failure")
	}
	assertEventLog(t, pool, result.OrganizationID, 1)
	var messages int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE organization_id = $1", result.OrganizationID).Scan(&messages))
	if messages != 0 {
		t.Fatalf("%d messages survived rollback", messages)
	}
}

// postingEvents binds realtime's appender to conversation's posting transaction.
func postingEvents(tx platform.Tx) conversation.EventAppender { return realtimepg.AppenderIn(tx) }

// postingSequence binds org's sequence to conversation's posting transaction.
func postingSequence(tx platform.Tx) conversation.EventSequence { return orgpg.SequenceIn(tx) }

// appendEvents adapts realtime's appender to the consumer interface the
// event-writing stores declare (decision 26).
func appendEvents(tx platform.Tx) postgres.EventAppender { return realtimepg.AppenderIn(tx) }

// lookupMembers adapts org's directory to the reader's consumer interface.
func lookupMembers(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) }

// lookupAccounts adapts identity's directory to the reader's consumer interface.
func lookupAccounts(s platform.Snapshot) conversation.AccountDirectory {
	return identitypg.AccountsIn(s)
}

// eventSequence adapts org's sequence to the posting and branching stores'
// consumer interface.
func eventSequence(tx platform.Tx) postgres.EventSequence { return orgpg.SequenceIn(tx) }

// eventCursor adapts org's committed event_seq to the reader's consumer
// interface.
func eventCursor(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) }
