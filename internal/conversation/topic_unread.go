package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// TopicUnread counts messages using supplied read state, never foreign tables.
type TopicUnread interface {
	// Count returns per-topic counts capped at 100 and the selected topic's
	// uncapped first unread sequence (zero if none). Topics must be unique;
	// IDs and floors must be built together. Prefix is exclusive; readSet holds
	// [lo, hi) ranges above it. No member/author filter applies.
	Count(ctx context.Context, organizationID, channelID kernel.ID, prefix int64, topics []kernel.ID, floors []int64, readSet [][2]int64, selected *kernel.ID) (map[kernel.ID]int64, int64, error)
}
