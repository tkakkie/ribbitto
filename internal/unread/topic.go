package unread

import (
	"context"
	"fmt"
	"math"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PreparedTopic holds read state and the channel lock until the caller completes its transaction.
type PreparedTopic interface {
	// ReadState returns the prefix, floor and read ranges above the prefix.
	ReadState() (prefix, floor int64, ranges []Range)
	// Add unions candidate bounds and raises the floor, including an empty batch.
	Add(context.Context, []int64, []int64, int64) error
}

// TopicPrepareIn binds locked preparation to the caller's transaction.
type TopicPrepareIn func(platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error)

// TopicMessages supplies unread message bounds through a validated cursor.
type TopicMessages interface {
	// Ranges returns whole-channel neighbour bounds for this topic's unread messages.
	Ranges(context.Context, kernel.ID, kernel.ID, kernel.ID, int64, int64, int64, []Range) ([]Range, error)
}

// TopicMessagesIn binds conversation's candidate query to the caller's transaction.
type TopicMessagesIn func(platform.Tx) TopicMessages

// TopicWriter reads one topic through a cursor in the caller's transaction.
type TopicWriter struct {
	prepare  TopicPrepareIn
	messages TopicMessagesIn
	cursor   EventCursorIn
}

// NewTopicWriter builds the topic write with transaction-bound factories.
func NewTopicWriter(prepare TopicPrepareIn, messages TopicMessagesIn, cursor EventCursorIn) *TopicWriter {
	return &TopicWriter{prepare: prepare, messages: messages, cursor: cursor}
}

// Read validates the cursor, prepares locked read state, queries candidates and
// unions their bounds while raising the floor. The caller supplies resolved
// scope and org's persisted join sequence, and must roll back tx on error.
func (w *TopicWriter) Read(ctx context.Context, tx platform.Tx, scope TopicScope, joinedEventSeq, cursor int64) error {
	if cursor < 0 || cursor == math.MaxInt64 {
		return ErrInvalidCursor
	}
	committed, err := w.cursor(tx).EventSeq(ctx, scope.OrganizationID)
	if err != nil {
		return fmt.Errorf("reading topic cursor limit: %w", err)
	}
	if cursor > committed {
		return ErrInvalidCursor
	}
	p, err := w.prepare(tx)(ctx, scope, joinedEventSeq)
	if err != nil {
		return fmt.Errorf("preparing topic read: %w", err)
	}
	prefix, floor, read := p.ReadState()
	ranges, err := w.messages(tx).Ranges(ctx, scope.OrganizationID, scope.ChannelID, scope.TopicID, cursor, prefix, floor, read)
	if err != nil {
		return fmt.Errorf("finding topic read candidates: %w", err)
	}
	los, his := make([]int64, len(ranges)), make([]int64, len(ranges))
	for i, r := range ranges {
		los[i], his[i] = r.Lo, r.Hi
	}
	if err := p.Add(ctx, los, his, cursor); err != nil {
		return fmt.Errorf("reading topic: %w", err)
	}
	return nil
}
