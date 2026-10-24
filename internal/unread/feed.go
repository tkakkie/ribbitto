package unread

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ErrInvalidCursor means the cursor is negative, unrepresentable or ahead of committed events.
var ErrInvalidCursor = errors.New("invalid read cursor")

// RangeWriter unions read ranges with the member's persisted join prefix.
type RangeWriter interface {
	Merge(context.Context, Scope, int64, conversation.SequenceRange) error
	// MergeCovered returns only newly covered intervals, excluding the join prefix.
	MergeCovered(context.Context, Scope, int64, conversation.SequenceRange) ([]conversation.SequenceRange, error)
}

// RangeWriterIn binds range writes to the caller's transaction.
type RangeWriterIn func(platform.Tx) RangeWriter

// ChannelMessages supplies conversation's message neighbours; zero means none.
type ChannelMessages interface {
	// LastMessageBefore returns the last sequence strictly below cursor, or zero.
	LastMessageBefore(context.Context, kernel.ID, kernel.ID, int64) (int64, error)
	// FirstMessageAfter returns the first sequence strictly above cursor, or zero.
	FirstMessageAfter(context.Context, kernel.ID, kernel.ID, int64) (int64, error)
	// HasMessages probes parallel half-open bounds in one scoped statement.
	HasMessages(context.Context, kernel.ID, kernel.ID, []int64, []int64) (bool, error)
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
// must roll back tx on error; Read never completes the transaction. It reports
// whether an actual message became read under the member/channel lock.
func (f *FeedWriter) Read(ctx context.Context, tx platform.Tx, scope Scope, joinedEventSeq, cursor int64) (bool, error) {
	if cursor < 0 || cursor == math.MaxInt64 {
		return false, ErrInvalidCursor
	}
	committed, err := f.cursor(tx).EventSeq(ctx, scope.OrganizationID)
	if err != nil {
		return false, fmt.Errorf("reading feed cursor limit: %w", err)
	}
	if cursor > committed {
		return false, ErrInvalidCursor
	}
	next, err := f.messages(tx).FirstMessageAfter(ctx, scope.OrganizationID, scope.ChannelID, cursor)
	if err != nil {
		return false, fmt.Errorf("finding next feed message: %w", err)
	}
	if next == 0 {
		next = cursor + 1
	}
	covered, err := f.ranges(tx).MergeCovered(ctx, scope, joinedEventSeq, conversation.SequenceRange{Lo: 0, Hi: next})
	if err != nil {
		return false, fmt.Errorf("reading feed: %w", err)
	}
	if len(covered) == 0 {
		return false, nil
	}
	los, his := make([]int64, len(covered)), make([]int64, len(covered))
	for i, r := range covered {
		los[i], his[i] = r.Lo, r.Hi
	}
	changed, err := f.messages(tx).HasMessages(ctx, scope.OrganizationID, scope.ChannelID, los, his)
	if err != nil {
		return false, fmt.Errorf("checking newly read feed messages: %w", err)
	}
	return changed, nil
}

// ReadShown unions only the channel messages in [from, through], bounded by
// cursor, under #1486 (decision 32, amended). The caller supplies resolved scope
// and the persisted join sequence, and must roll back tx on error.
func (f *FeedWriter) ReadShown(ctx context.Context, tx platform.Tx, scope Scope, joinedEventSeq, cursor, from, through int64) (bool, error) {
	if cursor < 0 || cursor == math.MaxInt64 || from < 1 || through < from || cursor < through {
		return false, ErrInvalidCursor
	}
	committed, err := f.cursor(tx).EventSeq(ctx, scope.OrganizationID)
	if err != nil {
		return false, fmt.Errorf("reading shown feed cursor limit: %w", err)
	}
	if cursor > committed {
		return false, ErrInvalidCursor
	}
	previous, err := f.messages(tx).LastMessageBefore(ctx, scope.OrganizationID, scope.ChannelID, from)
	if err != nil {
		return false, fmt.Errorf("finding previous shown feed message: %w", err)
	}
	lo := int64(0)
	if previous != 0 {
		lo = previous + 1
	}
	hi, err := f.messages(tx).FirstMessageAfter(ctx, scope.OrganizationID, scope.ChannelID, cursor)
	if err != nil {
		return false, fmt.Errorf("finding next shown feed message: %w", err)
	}
	if hi == 0 {
		hi = cursor + 1
	}
	covered, err := f.ranges(tx).MergeCovered(ctx, scope, joinedEventSeq, conversation.SequenceRange{Lo: lo, Hi: hi})
	if err != nil {
		return false, fmt.Errorf("reading shown feed: %w", err)
	}
	if len(covered) == 0 {
		return false, nil
	}
	los, his := make([]int64, len(covered)), make([]int64, len(covered))
	for i, r := range covered {
		los[i], his[i] = r.Lo, r.Hi
	}
	changed, err := f.messages(tx).HasMessages(ctx, scope.OrganizationID, scope.ChannelID, los, his)
	if err != nil {
		return false, fmt.Errorf("checking newly read shown feed messages: %w", err)
	}
	return changed, nil
}
