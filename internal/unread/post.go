package unread

import (
	"context"
	"errors"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// FeedRead writes a feed page in the caller's transaction.
type FeedRead func(context.Context, platform.Tx, Scope, int64, int64) (bool, error)

// TopicRead writes a topic page in the caller's transaction.
type TopicRead func(context.Context, platform.Tx, TopicScope, int64, int64) (bool, error)

// PostReads combines page reads and the author's own-message read.
type PostReads struct {
	ranges  RangeWriterIn
	prepare TopicPrepareIn
	feed    func() FeedRead
	topic   func(TopicPrepareIn) TopicRead
}

// NewPostReads builds post reads with transaction-bound factories.
func NewPostReads(ranges RangeWriterIn, prepare TopicPrepareIn, feed func() FeedRead, topic func(TopicPrepareIn) TopicRead) *PostReads {
	return &PostReads{ranges: ranges, prepare: prepare, feed: feed, topic: topic}
}

// Read retains posting's organisation-first lock order and never completes tx.
// The caller supplies resolved scope, join sequence and the own-message bounds.
func (w *PostReads) Read(ctx context.Context, tx platform.Tx, scope Scope, joined int64, topic *kernel.ID, cursor *int64, own conversation.SequenceRange, floor int64) error {
	if cursor == nil {
		return w.ranges(tx).Merge(ctx, scope, joined, own)
	}
	if topic == nil {
		if _, err := w.feed()(ctx, tx, scope, joined, *cursor); err != nil {
			return err
		}
		return w.ranges(tx).Merge(ctx, scope, joined, own)
	}
	// Capture preparation only after TopicWriter validates the cursor. Both
	// unions retain the same channel lock in this posting transaction.
	var prepared PreparedTopic
	writer := w.topic(func(tx platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error) {
		return func(ctx context.Context, scope TopicScope, joined int64) (PreparedTopic, error) {
			p, err := w.prepare(tx)(ctx, scope, joined)
			prepared = p
			return p, err
		}
	})
	if _, err := writer(ctx, tx, TopicScope{Scope: scope, TopicID: *topic}, joined, *cursor); err != nil {
		return err
	}
	// Fail rather than panic if a topic writer ever succeeds without preparing.
	if prepared == nil {
		return errors.New("posting topic read: page write returned without preparing")
	}
	return prepared.Add(ctx, []int64{own.Lo}, []int64{own.Hi}, floor)
}
