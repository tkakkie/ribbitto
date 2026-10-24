package unread

import (
	"context"
	"fmt"
	"math"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PreparedTopic holds read state and the channel lock until the caller completes its transaction.
type PreparedTopic interface {
	// ReadState returns the prefix, floor and read ranges above the prefix.
	ReadState() (prefix, floor int64, ranges []conversation.SequenceRange)
	// Add unions candidate bounds and raises the floor, including an empty batch.
	Add(context.Context, []int64, []int64, int64) error
}

// TopicPrepareIn binds locked preparation to the caller's transaction.
type TopicPrepareIn func(platform.Tx) func(context.Context, TopicScope, int64) (PreparedTopic, error)

// TopicMessages supplies unread message bounds through a validated cursor.
type TopicMessages interface {
	// Ranges returns [previous + 1, next) for this topic's unread messages
	// through cursor, using whole-channel neighbours (0 or message + 1 if absent).
	// Prefix is exclusive; readSet contains ranges above it. Floor skips older
	// messages unless moved in after it. Validate cursor against committed events
	// in the same transaction before calling.
	Ranges(context.Context, kernel.ID, kernel.ID, kernel.ID, int64, int64, int64, []conversation.SequenceRange) ([]conversation.SequenceRange, error)
	// ShownRanges applies the same rules within [from, through], bounded by cursor.
	ShownRanges(context.Context, kernel.ID, kernel.ID, kernel.ID, int64, int64, int64, int64, int64, []conversation.SequenceRange) ([]conversation.SequenceRange, error)
	// RunFloor returns an exclusive safe floor bound, capped at runEnd (through + 1).
	// Prefix, floor and readSet come from the same locked preparation as candidates.
	RunFloor(context.Context, kernel.ID, kernel.ID, kernel.ID, int64, int64, int64, int64, []conversation.SequenceRange) (int64, error)
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
// Changed is true only when the locked candidate query found unread messages.
func (w *TopicWriter) Read(ctx context.Context, tx platform.Tx, scope TopicScope, joinedEventSeq, cursor int64) (bool, error) {
	if cursor < 0 || cursor == math.MaxInt64 {
		return false, ErrInvalidCursor
	}
	committed, err := w.cursor(tx).EventSeq(ctx, scope.OrganizationID)
	if err != nil {
		return false, fmt.Errorf("reading topic cursor limit: %w", err)
	}
	if cursor > committed {
		return false, ErrInvalidCursor
	}
	p, err := w.prepare(tx)(ctx, scope, joinedEventSeq)
	if err != nil {
		return false, fmt.Errorf("preparing topic read: %w", err)
	}
	prefix, floor, read := p.ReadState()
	ranges, err := w.messages(tx).Ranges(ctx, scope.OrganizationID, scope.ChannelID, scope.TopicID, cursor, prefix, floor, read)
	if err != nil {
		return false, fmt.Errorf("finding topic read candidates: %w", err)
	}
	los, his := make([]int64, len(ranges)), make([]int64, len(ranges))
	for i, r := range ranges {
		los[i], his[i] = r.Lo, r.Hi
	}
	if err := p.Add(ctx, los, his, cursor); err != nil {
		return false, fmt.Errorf("reading topic: %w", err)
	}
	return len(ranges) > 0, nil
}

// ReadShown reads only topic messages in [from, through] visible at cursor,
// under #1486 (decision 32, amended). The caller supplies resolved scope and
// the persisted join sequence, and must roll back tx on error.
func (w *TopicWriter) ReadShown(ctx context.Context, tx platform.Tx, scope TopicScope, joinedEventSeq, cursor, from, through int64) (bool, error) {
	if cursor < 0 || cursor == math.MaxInt64 || from < 1 || through < from || cursor < through {
		return false, ErrInvalidCursor
	}
	committed, err := w.cursor(tx).EventSeq(ctx, scope.OrganizationID)
	if err != nil {
		return false, fmt.Errorf("reading shown topic cursor limit: %w", err)
	}
	if cursor > committed {
		return false, ErrInvalidCursor
	}
	p, err := w.prepare(tx)(ctx, scope, joinedEventSeq)
	if err != nil {
		return false, fmt.Errorf("preparing shown topic read: %w", err)
	}
	prefix, floor, read := p.ReadState()
	messages := w.messages(tx)
	ranges, err := messages.ShownRanges(ctx, scope.OrganizationID, scope.ChannelID, scope.TopicID, cursor, from, through, prefix, floor, read)
	if err != nil {
		return false, fmt.Errorf("finding shown topic read candidates: %w", err)
	}
	// Only unread messages below from can block a raise through the run's end;
	// moves after cursor remain exceptions because their move sequence exceeds it.
	blocking, err := messages.RunFloor(ctx, scope.OrganizationID, scope.ChannelID, scope.TopicID, from, through+1, prefix, floor, read)
	if err != nil {
		return false, fmt.Errorf("guarding shown topic floor: %w", err)
	}
	los, his := make([]int64, len(ranges)), make([]int64, len(ranges))
	for i, r := range ranges {
		los[i], his[i] = r.Lo, r.Hi
	}
	if err := p.Add(ctx, los, his, blocking-1); err != nil {
		return false, fmt.Errorf("reading shown topic: %w", err)
	}
	return len(ranges) > 0, nil
}
