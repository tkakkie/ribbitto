package conversationpg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/compose"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func mentionToken(id kernel.ID) string { return "<@" + realtime.FormatPayloadID(id) + ">" }

func mentionFixture(t *testing.T, pool *pgxpool.Pool) conversationtest.OrganizationFixture {
	t.Helper()
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", conversation.DefaultChannelName)
	_, err := pool.Exec(t.Context(), "UPDATE organization SET event_log_boundary_seq = event_seq WHERE id = $1", f.OrganizationID)
	requireNoError(t, err)
	return f
}

func mentionPosting(pool *pgxpool.Pool) *conversation.Posting {
	return conversationpg.NewPagePosting(pool, compose.PostingSequence, compose.PostingEvents, compose.PostReads, compose.LookupMembers, nil)
}

type mentionPostState struct {
	seq, messages, mentions, events int64
	reads                           string
}

func mentionState(t *testing.T, pool *pgxpool.Pool, organizationID kernel.ID) (state mentionPostState) {
	t.Helper()
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT event_seq,
		(SELECT count(*) FROM message WHERE organization_id = $1),
		(SELECT count(*) FROM message_mention WHERE organization_id = $1),
		(SELECT count(*) FROM event_log WHERE organization_id = $1),
		jsonb_build_object(
		 'channels', (SELECT jsonb_agg(c ORDER BY member_id, channel_id) FROM channel_read c WHERE organization_id = $1),
		 'ranges', (SELECT jsonb_agg(r ORDER BY member_id, channel_id, lo) FROM read_range r WHERE organization_id = $1),
		 'floors', (SELECT jsonb_agg(f ORDER BY member_id, channel_id, topic_id) FROM topic_read_floor f WHERE organization_id = $1))::text
		FROM organization WHERE id = $1`, organizationID).Scan(&state.seq, &state.messages, &state.mentions, &state.events, &state.reads))
	return state
}

type beforeMentionPost struct {
	conversation.TxRunner
	before func()
}

func (r beforeMentionPost) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	r.before()
	return r.TxRunner.InTx(ctx, fn)
}

func TestMentionFailuresRollback(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, code, constraint, table string
		deleted, invalid              bool
	}{
		{name: "member deleted after preflight", deleted: true, invalid: true},
		{name: "another foreign key", code: "23503", constraint: "message_mention_message_fkey", table: "message_mention"},
		{name: "unique violation with member constraint name", code: "23505", constraint: "message_mention_member_fkey", table: "message_mention"},
		{name: "cancelled statement", code: "57014", table: "message_mention"},
		{name: "event failure after mentions and reads", code: "P0001", table: "event_log"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := pgtest.New(t)
			f := mentionFixture(t, pool)
			m := membership(f.OrganizationID, f.MemberID)
			bob := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "Bob"), org.RoleMember, "bob", 1)
			_, err := mentionPosting(pool).Post(t.Context(), m, f.Channel.ID, "seed read state")
			requireNoError(t, err)
			before := mentionState(t, pool, f.OrganizationID)
			runner := beforeMentionPost{TxRunner: conversationpg.NewTxRunnerForTest(pool), before: func() {
				if tt.deleted {
					// An adversarial deletion after the real directory lookup exposes
					// the check-to-insert window, without fabricating validation IDs.
					_, err := pool.Exec(t.Context(), "DELETE FROM member WHERE organization_id = $1 AND id = $2", f.OrganizationID, bob)
					requireNoError(t, err)
				}
			}}
			if tt.table != "" {
				_, err := pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION refuse_mention_post() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN RAISE EXCEPTION 'injected failure' USING ERRCODE = '%s', CONSTRAINT = '%s'; END $$;
					CREATE TRIGGER refuse_mention_post AFTER INSERT ON %s FOR EACH ROW EXECUTE FUNCTION refuse_mention_post()`, tt.code, tt.constraint, tt.table))
				requireNoError(t, err)
			}
			service := conversation.NewPagePosting(runner, func(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) },
				compose.PostingSequence, compose.PostingEvents, compose.PostReads, conversationpg.NewSnapshotRunnerForTest(pool), compose.LookupMembers, nil, nil)
			_, err = service.Post(t.Context(), m, f.Channel.ID, mentionToken(f.MemberID)+mentionToken(bob))
			if err == nil || errors.Is(err, conversation.ErrInvalidBody) != tt.invalid || errors.Is(err, conversation.ErrUnknownReference) != tt.invalid {
				t.Fatalf("failure=%v, want validation=%t", err, tt.invalid)
			}
			if !tt.invalid {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != tt.code || pgErr.ConstraintName != tt.constraint {
					t.Fatalf("internal failure lost cause: %v", err)
				}
			}
			if after := mentionState(t, pool, f.OrganizationID); after != before {
				t.Fatalf("rollback changed state: %+v -> %+v", before, after)
			}
			if tt.table != "" {
				_, err := pool.Exec(t.Context(), "DROP TRIGGER refuse_mention_post ON "+tt.table+"; DROP FUNCTION refuse_mention_post()")
				requireNoError(t, err)
			}
			next, err := mentionPosting(pool).Post(t.Context(), m, f.Channel.ID, mentionToken(f.MemberID))
			requireNoError(t, err)
			if next.EventSeq != before.seq+1 {
				t.Fatalf("rollback left a sequence gap: %d after %d", next.EventSeq, before.seq)
			}
			assertMentionMembers(t, pool, f.OrganizationID, next.ID, []kernel.ID{f.MemberID})
			assertEventLog(t, pool, f.OrganizationID, 1, next.EventSeq)
		})
	}
}

func assertMentionMembers(t *testing.T, pool *pgxpool.Pool, organizationID, messageID kernel.ID, members []kernel.ID) {
	t.Helper()
	var total, matched int
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE member_id = ANY($3::uuid[]))
		FROM message_mention WHERE organization_id = $1 AND message_id = $2`, organizationID, messageID, members).Scan(&total, &matched))
	if total != len(members) || matched != len(members) {
		t.Fatalf("mentions=%d matched=%d, want %d", total, matched, len(members))
	}
}
