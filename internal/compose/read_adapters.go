package compose

import (
	"context"

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

// PostReads binds the page and own-message writes to posting's transaction.
func PostReads(tx platform.Tx) conversation.PostReadWriter { return postReadWriter{tx: tx} }

type postReadWriter struct{ tx platform.Tx }

func (w postReadWriter) Read(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, cursor *int64, lo, hi, floor int64) error {
	reads := unread.NewPostReads(func(tx platform.Tx) unread.RangeWriter { return unreadpg.WriterIn(tx) }, topicPrepare,
		func() unread.FeedRead { return NewFeedWriter().Read },
		func(prepare unread.TopicPrepareIn) unread.TopicRead {
			return unread.NewTopicWriter(prepare,
				topicReadCandidates,
				func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) }).Read
		})
	return reads.Read(ctx, w.tx, unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}, m.Member.JoinedEventSeq, topic, cursor, conversation.SequenceRange{Lo: lo, Hi: hi}, floor)
}
