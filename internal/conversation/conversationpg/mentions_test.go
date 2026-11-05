package conversationpg_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
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

func TestMentionSequencesAcrossSendsAndMove(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := mentionFixture(t, pool)
	m := membership(f.OrganizationID, f.MemberID)
	service := mentionPosting(pool)
	key := conversation.NewIdempotencyKey()
	body := mentionToken(f.MemberID)
	first, err := service.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: body, Key: &key})
	requireNoError(t, err)
	conversationtest.AssertMentions(t, pool, f.OrganizationID, first.Message.ID, first.Message.EventSeq, []kernel.ID{f.MemberID})
	repeated, err := service.Post(t.Context(), m, f.Channel.ID, body+" **"+body+"**")
	requireNoError(t, err)
	conversationtest.AssertMentions(t, pool, f.OrganizationID, repeated.ID, repeated.EventSeq, []kernel.ID{f.MemberID})
	converted, err := service.Post(t.Context(), m, f.Channel.ID, "@owner")
	requireNoError(t, err)
	conversationtest.AssertMentions(t, pool, f.OrganizationID, converted.ID, converted.EventSeq, []kernel.ID{f.MemberID})
	quoted, err := service.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "quote only", Quote: &first.Message.ID})
	requireNoError(t, err)
	conversationtest.AssertMentions(t, pool, f.OrganizationID, quoted.Message.ID, quoted.Message.EventSeq, nil)
	before := mentionState(t, pool, f.OrganizationID)
	for _, tt := range []struct {
		body string
		err  error
	}{{body, nil}, {body + " changed", conversation.ErrPostKeyReused}} {
		again, err := service.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: tt.body, Key: &key})
		if !errors.Is(err, tt.err) || !again.Repeat || again.Message.ID != first.Message.ID {
			t.Fatalf("resend=%+v, %v; want %v", again, err, tt.err)
		}
		if after := mentionState(t, pool, f.OrganizationID); after != before {
			t.Fatalf("resend changed state: %+v -> %+v", before, after)
		}
		conversationtest.AssertMentions(t, pool, f.OrganizationID, first.Message.ID, first.Message.EventSeq, []kernel.ID{f.MemberID})
	}
	moved, err := conversationpg.NewBrancher(pool, compose.PostingSequence, compose.PostingEvents, compose.BranchReads, nil).Branch(t.Context(), m, f.Channel.ID,
		conversation.Branch{Messages: []kernel.ID{first.Message.ID}, From: f.Channel.DefaultTopicID, NewName: "moved"}, func(conversation.Topic) string { return "moved" })
	requireNoError(t, err)
	conversationtest.AssertMentions(t, pool, f.OrganizationID, first.Message.ID, first.Message.EventSeq, []kernel.ID{f.MemberID})
	var topic kernel.ID
	requireNoError(t, pool.QueryRow(t.Context(), `SELECT m.topic_id FROM message_mention r JOIN message m
		ON m.organization_id = r.organization_id AND m.id = r.message_id
		WHERE r.organization_id = $1 AND r.message_id = $2`, f.OrganizationID, first.Message.ID).Scan(&topic))
	if topic != moved.ID {
		t.Fatalf("mention resolves to topic %v, want %v", topic, moved.ID)
	}
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
			conversationtest.AssertMentions(t, pool, f.OrganizationID, next.ID, next.EventSeq, []kernel.ID{f.MemberID})
			assertEventLog(t, pool, f.OrganizationID, 1, next.EventSeq)
		})
	}
}

func TestMentionValidationAndLiteralBodies(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := mentionFixture(t, pool)
	foreign := conversationtest.OrganizationWithOwner(t, pool, "foreign", conversation.DefaultChannelName)
	service, m := mentionPosting(pool), membership(f.OrganizationID, f.MemberID)
	before := mentionState(t, pool, f.OrganizationID)
	for _, id := range []kernel.ID{{0xee}, foreign.MemberID} {
		_, err := service.Post(t.Context(), m, f.Channel.ID, mentionToken(id))
		if !errors.Is(err, conversation.ErrInvalidBody) || !errors.Is(err, conversation.ErrUnknownReference) {
			t.Fatalf("unknown/foreign mention: %v", err)
		}
		if after := mentionState(t, pool, f.OrganizationID); after != before {
			t.Fatalf("invalid mention wrote state: %+v -> %+v", before, after)
		}
	}
	for _, body := range []string{"`" + mentionToken(f.MemberID) + "`", "```\n" + mentionToken(f.MemberID) + "\n```", "\\" + mentionToken(f.MemberID)} {
		posted, err := service.Post(t.Context(), m, f.Channel.ID, body)
		requireNoError(t, err)
		conversationtest.AssertMentions(t, pool, f.OrganizationID, posted.ID, posted.EventSeq, nil)
	}
}

type mentionTrace struct {
	*platform.QueryCounter
	inserts atomic.Int64
}

func (trace *mentionTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "-- name: InsertMessageMentions :exec") {
		trace.inserts.Add(1)
	}
	return trace.QueryCounter.TraceQueryStart(ctx, conn, data)
}

func TestMaximumMentionPostStatementCount(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	f := mentionFixture(t, pool)
	var ids []kernel.ID
	var references strings.Builder
	for i := range 102 {
		id := orgtest.Member(t, pool, f.OrganizationID, identitytest.Account(t, pool, fmt.Sprintf("m%d@example.org", i), "Member"), org.RoleMember, fmt.Sprintf("m%d", i), 1)
		ids = append(ids, id)
		references.WriteString(mentionToken(id))
	}
	// Start every comparison with an existing read range; its first creation
	// uses a savepoint and would otherwise obscure the mention-only cost.
	_, err := mentionPosting(pool).Post(t.Context(), membership(f.OrganizationID, f.MemberID), f.Channel.ID, "seed read state")
	requireNoError(t, err)
	trace := &mentionTrace{QueryCounter: platform.NewQueryCounter()}
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(t.Context(), config)
	requireNoError(t, err)
	t.Cleanup(traced.Close)
	var baseline platform.QueryCounts
	for _, n := range []int{0, 1, 102} {
		body := ""
		if n > 0 {
			body = references.String()[:39*n]
		}
		body += strings.Repeat("界", conversation.MaxMessageBodyLength-utf8.RuneCountInString(body))
		if utf8.RuneCountInString(body) != 4000 {
			t.Fatal("maximum body is not 4000 code points")
		}
		before, inserts := trace.Counts(), trace.inserts.Load()
		posted, err := mentionPosting(traced).Post(t.Context(), membership(f.OrganizationID, f.MemberID), f.Channel.ID, body)
		requireNoError(t, err)
		after := trace.Counts()
		delta := platform.QueryCounts{Queries: after.Queries - before.Queries, Begins: after.Begins - before.Begins, Commits: after.Commits - before.Commits, Rollbacks: after.Rollbacks - before.Rollbacks}
		wantInserts := int64(0)
		if n == 0 {
			baseline = delta
		} else {
			wantInserts = 1
			if delta.Queries != baseline.Queries+2 || delta.Begins != baseline.Begins+1 || delta.Commits != baseline.Commits+1 || delta.Rollbacks != baseline.Rollbacks {
				t.Fatalf("%d mentions: statements=%+v, no-mention=%+v; want one lookup and one bulk insert", n, delta, baseline)
			}
		}
		if got := trace.inserts.Load() - inserts; got != wantInserts {
			t.Fatalf("%d mentions: bulk inserts=%d, want %d", n, got, wantInserts)
		}
		t.Logf("4000 code points, %d distinct mentions: queries=%d begin=%d commit=%d rollback=%d mention inserts=%d", n, delta.Queries, delta.Begins, delta.Commits, delta.Rollbacks, wantInserts)
		conversationtest.AssertMentions(t, pool, f.OrganizationID, posted.ID, posted.EventSeq, ids[:n])
	}
}
