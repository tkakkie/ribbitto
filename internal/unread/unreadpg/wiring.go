package unreadpg

import (
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/internal/postgres"
)

// WriterIn binds the read-range writer to the caller's transaction.
func WriterIn(tx platform.Tx) *postgres.Writer { return postgres.WriterIn(tx) }

// ChannelRangesIn binds sidebar range reads to the caller's snapshot.
func ChannelRangesIn(s platform.Snapshot) unread.ChannelRanges { return postgres.ChannelRangesIn(s) }

// NewFeedWriter builds the feed use case with injected message and cursor factories.
func NewFeedWriter(messages unread.ChannelMessagesIn, cursor unread.EventCursorIn) *unread.FeedWriter {
	return unread.NewFeedWriter(func(tx platform.Tx) unread.RangeWriter { return WriterIn(tx) }, messages, cursor)
}
