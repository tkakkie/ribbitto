package realtime_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// A production write on the traced pool without counterSQLContext must fail
// as a whole: no panic, an error to the caller, and neither source rows nor
// counters committed. Otherwise a missed wrapper would silently skip counters.
func TestUnreadCountersSQLMissingTracker(t *testing.T) {
	h := newCounterMatrix(t)
	owner, writer := h.members[0], h.join(t, "writer")
	seq := h.post(t, "seed", writer, nil, nil)
	counterSQLTables(t, h.pool)
	_, key := counterSQLContext(t.Context(), owner, h.channel)
	counterSQLMaterialize(t, h.pool, key)
	config := h.pool.Config()
	config.ConnConfig.Tracer = counterSQLAdapter{}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	posting := conversationpg.NewPagePosting(pool, postingSequence, postingEvents, func(tx platform.Tx) conversation.PostReadWriter { return counterSQLPostReads{tx} }, nil)
	reading := unreadpg.NewReading(pool, counterFeedWriter(), counterTopicWriter(nil), nil)
	state := func() string {
		t.Helper()
		var s string
		if err := h.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM message m),
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY member_id,lo) FROM read_range r),
 (SELECT jsonb_agg(to_jsonb(f) ORDER BY member_id,topic_id) FROM topic_read_floor f),
 (SELECT jsonb_agg(to_jsonb(c) ORDER BY channel_id,scope_id) FROM counter_total c),
 (SELECT jsonb_agg(to_jsonb(b) ORDER BY channel_id,member_id) FROM counter_bundle b),
 (SELECT jsonb_agg(to_jsonb(r) ORDER BY channel_id,member_id,topic_id) FROM counter_topic_read r),
 (SELECT event_seq FROM organization WHERE id=$1))::text`, owner.Organization.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	scope := unread.Scope{OrganizationID: owner.Organization.ID, ChannelID: h.channel, MemberID: owner.Member.ID}
	for _, tc := range []struct {
		name  string
		write func(context.Context) error
	}{
		{"older-page-post", func(ctx context.Context) error {
			_, err := posting.PostToTopic(ctx, owner, h.channel, nil, "untracked")
			return err
		}},
		{"page-post", func(ctx context.Context) error {
			_, err := posting.PostFromPage(ctx, owner, h.channel, nil, "untracked", seq)
			return err
		}},
		{"feed-read", func(ctx context.Context) error { return reading.Feed(ctx, scope, owner.Member.JoinedEventSeq, seq) }},
		{"topic-read", func(ctx context.Context) error {
			return reading.Topic(ctx, unread.TopicScope{Scope: scope, TopicID: h.base}, owner.Member.JoinedEventSeq, seq)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := state()
			if err := tc.write(t.Context()); err == nil {
				t.Fatal("untracked write committed without counter maintenance")
			}
			if state() != before {
				t.Fatal("untracked write left source rows or counters committed")
			}
		})
	}
}
