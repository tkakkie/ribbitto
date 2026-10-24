package compose

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// NewFeedWriter binds the feed write to reading's transaction.
func NewFeedWriter() *unread.FeedWriter {
	return unreadpg.NewFeedWriter(
		func(tx platform.Tx) unread.ChannelMessages { return conversationpg.MessageSequencesIn(tx) },
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
}

// NewTopicWriter binds the topic write to reading's transaction.
func NewTopicWriter() *unread.TopicWriter {
	return unread.NewTopicWriter(
		topicPrepare,
		topicReadCandidates,
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
}

// topicReadCandidates binds conversation's store directly to unread's consumer.
func topicReadCandidates(tx platform.Tx) unread.TopicMessages {
	return conversationpg.TopicReadCandidatesIn(tx)
}

func topicPrepare(tx platform.Tx) func(context.Context, unread.TopicScope, int64) (unread.PreparedTopic, error) {
	return func(ctx context.Context, scope unread.TopicScope, joined int64) (unread.PreparedTopic, error) {
		return unreadpg.WriterIn(tx).Prepare(ctx, scope, joined)
	}
}

// PostReads binds the own-message write to posting's transaction.
func PostReads(tx platform.Tx) conversation.PostReadWriter { return postReadWriter{tx: tx} }

type postReadWriter struct{ tx platform.Tx }

func (w postReadWriter) Read(ctx context.Context, m org.Membership, channel kernel.ID, lo, hi int64) error {
	reads := unread.NewPostReads(func(tx platform.Tx) unread.RangeWriter { return unreadpg.WriterIn(tx) })
	if err := reads.Read(ctx, w.tx, unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}, m.Member.JoinedEventSeq, conversation.SequenceRange{Lo: lo, Hi: hi}); err != nil {
		return err
	}
	_, err := pgxbridge.Tx(w.tx).Exec(ctx, `INSERT INTO topic_read_floor (organization_id, channel_id, member_id, topic_id, floor_seq)
SELECT $1::uuid, $2::uuid, $3::uuid, topic_id, $4::bigint FROM message
WHERE organization_id = $1::uuid AND channel_id = $2::uuid AND event_seq = $4::bigint
ON CONFLICT (organization_id, topic_id, member_id) DO UPDATE SET floor_seq = GREATEST(topic_read_floor.floor_seq, EXCLUDED.floor_seq)`,
		pgtype.UUID{Bytes: m.Organization.ID, Valid: true}, pgtype.UUID{Bytes: channel, Valid: true}, pgtype.UUID{Bytes: m.Member.ID, Valid: true}, hi-1)
	return err
}
