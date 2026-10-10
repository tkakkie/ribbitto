package unread

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// ChannelReadState contains the prefix end and up to 100 unread gaps.
// Gap bounds are inclusive Lo and exclusive Hi; a truncated set has no open gap.
type ChannelReadState struct {
	Prefix int64
	Gaps   []Range
}

// ChannelRanges reads the first ranges of the sidebar's channels in one statement.
type ChannelRanges interface {
	Read(context.Context, kernel.ID, kernel.ID, int64, []kernel.ID) (map[kernel.ID]ChannelReadState, error)
}

// ChannelRangesIn binds range reads to the caller's snapshot.
type ChannelRangesIn func(platform.Snapshot) ChannelRanges

// ChannelCounter counts messages through a snapshot-bound conversation API.
type ChannelCounter interface {
	Count(context.Context, kernel.ID, []kernel.ID, []kernel.ID, []int64, []int64) (map[kernel.ID]int64, map[kernel.ID]int64, error)
}

// ChannelCounterIn binds message counts to the caller's snapshot.
type ChannelCounterIn func(platform.Snapshot) ChannelCounter

// ChannelCount contains a capped unread count and first unread sequence (zero if none).
type ChannelCount struct{ Count, FirstUnread int64 }

// ChannelCounts combines range loading and counting in two snapshot statements.
type ChannelCounts struct {
	ranges   ChannelRangesIn
	messages ChannelCounterIn
}

// NewChannelCounts builds channel counts with snapshot-bound factories.
func NewChannelCounts(ranges ChannelRangesIn, messages ChannelCounterIn) *ChannelCounts {
	return &ChannelCounts{ranges: ranges, messages: messages}
}

// Read counts the sidebar's channels across all topics. The caller supplies
// resolved scope and org's persisted join sequence in the same snapshot.
func (c *ChannelCounts) Read(ctx context.Context, s platform.Snapshot, organizationID, memberID kernel.ID, joined int64, channels []kernel.ID) (map[kernel.ID]ChannelCount, error) {
	ids := make([]kernel.ID, 0, len(channels))
	seen := make(map[kernel.ID]bool, len(channels))
	for _, id := range channels {
		if !seen[id] {
			ids, seen[id] = append(ids, id), true
		}
	}
	states, err := c.ranges(s).Read(ctx, organizationID, memberID, joined, ids)
	if err != nil {
		return nil, fmt.Errorf("loading channel count ranges: %w", err)
	}
	var gapChannels []kernel.ID
	var los, his []int64
	for _, id := range ids {
		for _, gap := range states[id].Gaps {
			gapChannels = append(gapChannels, id)
			los, his = append(los, gap.Lo), append(his, gap.Hi)
		}
	}
	counts, first, err := c.messages(s).Count(ctx, organizationID, ids, gapChannels, los, his)
	if err != nil {
		return nil, fmt.Errorf("reading channel counts: %w", err)
	}
	result := make(map[kernel.ID]ChannelCount, len(ids))
	for _, id := range ids {
		result[id] = ChannelCount{Count: counts[id], FirstUnread: first[id]}
	}
	return result, nil
}
