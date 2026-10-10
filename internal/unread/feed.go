package unread

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrInvalidCursor means the cursor is negative, unrepresentable or ahead of committed events.
var ErrInvalidCursor = errors.New("invalid read cursor")

// RangeWriter unions read ranges with the member's persisted join prefix.
type RangeWriter interface {
	Merge(context.Context, Scope, int64, Range) error
}

// RangeWriterIn binds range writes to the caller's transaction.
type RangeWriterIn func(platform.Tx) RangeWriter

// ChannelMessages supplies conversation's next message sequence; zero means none.
type ChannelMessages interface {
	FirstMessageAfter(context.Context, kernel.ID, kernel.ID, int64) (int64, error)
}

// ChannelMessagesIn binds conversation's query to the caller's transaction.
type ChannelMessagesIn func(platform.Tx) ChannelMessages

// EventCursor reads org's committed event sequence.
type EventCursor interface {
	EventSeq(context.Context, kernel.ID) (int64, error)
}

// EventCursorIn binds org's cursor to the caller's transaction.
type EventCursorIn func(platform.Tx) EventCursor

// FeedWriter reads a channel through a cursor in the caller's transaction.
type FeedWriter struct {
	ranges   RangeWriterIn
	messages ChannelMessagesIn
	cursor   EventCursorIn
}

// NewFeedWriter builds the feed write with transaction-bound factories.
func NewFeedWriter(ranges RangeWriterIn, messages ChannelMessagesIn, cursor EventCursorIn) *FeedWriter {
	return &FeedWriter{ranges: ranges, messages: messages, cursor: cursor}
}

// Read unions the feed prefix through cursor with the member's persisted join
// prefix. The caller supplies resolved scope and joinedEventSeq from org and
// must roll back tx on error; Read never completes the transaction.
func (f *FeedWriter) Read(ctx context.Context, tx platform.Tx, scope Scope, joinedEventSeq, cursor int64) error {
	if cursor < 0 || cursor == math.MaxInt64 {
		return ErrInvalidCursor
	}
	committed, err := f.cursor(tx).EventSeq(ctx, scope.OrganizationID)
	if err != nil {
		return fmt.Errorf("reading feed cursor limit: %w", err)
	}
	if cursor > committed {
		return ErrInvalidCursor
	}
	next, err := f.messages(tx).FirstMessageAfter(ctx, scope.OrganizationID, scope.ChannelID, cursor)
	if err != nil {
		return fmt.Errorf("finding next feed message: %w", err)
	}
	if next == 0 {
		next = cursor + 1
	}
	if err := f.ranges(tx).Merge(ctx, scope, joinedEventSeq, Range{Lo: 0, Hi: next}); err != nil {
		return fmt.Errorf("reading feed: %w", err)
	}
	return nil
}
