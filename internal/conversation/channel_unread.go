package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ChannelUnread counts messages in supplied, ordered, disjoint channel gaps.
// Channels must be unique; parallel gap arrays must be built together. Lower
// bounds are inclusive, upper bounds exclusive. No member/author filter applies.
type ChannelUnread interface {
	// Count returns counts capped at 100 and first unread sequences (zero if none).
	Count(ctx context.Context, organizationID kernel.ID, channels, gapChannels []kernel.ID, los, his []int64) (counts, first map[kernel.ID]int64, err error)
}
