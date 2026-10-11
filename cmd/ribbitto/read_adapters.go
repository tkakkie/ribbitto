package main

import (
	"context"
	"errors"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

// newFeedWriter binds the feed write to reading's transaction.
func newFeedWriter() *unread.FeedWriter {
	return unreadpg.NewFeedWriter(
		func(tx platform.Tx) unread.ChannelMessages { return conversationpg.MessageSequencesIn(tx) },
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
}

// newTopicWriter binds the topic write to reading's transaction.
func newTopicWriter() *unread.TopicWriter {
	return unread.NewTopicWriter(
		func(tx platform.Tx) func(context.Context, unread.TopicScope, int64) (unread.PreparedTopic, error) {
			return func(ctx context.Context, scope unread.TopicScope, joined int64) (unread.PreparedTopic, error) {
				return unreadpg.WriterIn(tx).Prepare(ctx, scope, joined)
			}
		},
		func(tx platform.Tx) unread.TopicMessages {
			return topicMessages{conversationpg.TopicReadCandidatesIn(tx)}
		},
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
}

type topicMessages struct {
	candidates conversation.TopicReadCandidates
}

func (m topicMessages) Ranges(ctx context.Context, organizationID, channelID, topicID kernel.ID, cursor, prefix, floor int64, read []unread.Range) ([]unread.Range, error) {
	set := make([]conversation.SequenceRange, len(read))
	for i, r := range read {
		set[i] = conversation.SequenceRange{Lo: r.Lo, Hi: r.Hi}
	}
	bounds, err := m.candidates.Ranges(ctx, organizationID, channelID, topicID, cursor, prefix, floor, set)
	if err != nil {
		return nil, err
	}
	ranges := make([]unread.Range, len(bounds))
	for i, r := range bounds {
		ranges[i] = unread.Range{Lo: r.Lo, Hi: r.Hi}
	}
	return ranges, nil
}

// branchReads adapts unread's range writer to conversation's consumer interface.
func branchReads(tx platform.Tx) conversation.ReadRangeWriter {
	return branchReadWriter{ranges: unreadpg.WriterIn(tx)}
}

type branchReadWriter struct{ ranges unread.RangeWriter }

func (w branchReadWriter) Merge(ctx context.Context, organizationID, channelID, memberID kernel.ID, joined, lo, hi int64) error {
	return w.ranges.Merge(ctx, unread.Scope{OrganizationID: organizationID, ChannelID: channelID, MemberID: memberID}, joined, unread.Range{Lo: lo, Hi: hi})
}

// postReads binds the page and own-message writes to posting's transaction.
func postReads(tx platform.Tx) conversation.PostReadWriter { return postReadWriter{tx: tx} }

type postReadWriter struct{ tx platform.Tx }

func (w postReadWriter) Read(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, cursor *int64, lo, hi, floor int64) error {
	scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channel, MemberID: m.Member.ID}
	joined := m.Member.JoinedEventSeq
	if cursor == nil {
		return unreadpg.WriterIn(w.tx).Merge(ctx, scope, joined, unread.Range{Lo: lo, Hi: hi})
	}
	if topic == nil {
		if _, err := newFeedWriter().Read(ctx, w.tx, scope, joined, *cursor); err != nil {
			return err
		}
		return unreadpg.WriterIn(w.tx).Merge(ctx, scope, joined, unread.Range{Lo: lo, Hi: hi})
	}
	ts := unread.TopicScope{Scope: scope, TopicID: *topic}
	// Capture preparation only after TopicWriter validates the cursor. Both
	// unions retain the same channel lock in this posting transaction.
	var prepared unread.PreparedTopic
	writer := unread.NewTopicWriter(
		func(tx platform.Tx) func(context.Context, unread.TopicScope, int64) (unread.PreparedTopic, error) {
			return func(ctx context.Context, scope unread.TopicScope, joined int64) (unread.PreparedTopic, error) {
				p, err := unreadpg.WriterIn(tx).Prepare(ctx, scope, joined)
				prepared = p
				return p, err
			}
		},
		func(tx platform.Tx) unread.TopicMessages {
			return topicMessages{conversationpg.TopicReadCandidatesIn(tx)}
		},
		func(tx platform.Tx) unread.EventCursor { return orgpg.EventCursorInTx(tx) },
	)
	if _, err := writer.Read(ctx, w.tx, ts, joined, *cursor); err != nil {
		return err
	}
	// TopicWriter.Read prepares before every successful return; fail rather
	// than panic if that ever changes.
	if prepared == nil {
		return errors.New("posting topic read: page write returned without preparing")
	}
	return prepared.Add(ctx, []int64{lo}, []int64{hi}, floor)
}
