package unread

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PageChannelCounts supplies channel counts in a shared snapshot.
type PageChannelCounts interface {
	ReadWithFirstUnread(context.Context, platform.Snapshot, kernel.ID, kernel.ID, int64, []kernel.ID, bool) (map[kernel.ID]ChannelCount, error)
}

// PageTopicCounts supplies topic counts in a shared snapshot.
type PageTopicCounts interface {
	ReadWithFirstUnread(context.Context, platform.Snapshot, Scope, int64, []kernel.ID, *kernel.ID, bool) (map[kernel.ID]int64, int64, error)
}

// PageCountResult contains both count maps and the page's first unread sequences.
type PageCountResult struct {
	ChannelCounts, TopicCounts        map[kernel.ID]int64
	FeedFirstUnread, TopicFirstUnread int64
}

// PageCounts combines channel and topic counts in the caller's snapshot.
type PageCounts struct {
	channels func() PageChannelCounts
	topics   func() PageTopicCounts
}

// NewPageCounts builds page counting with snapshot-bound count factories.
func NewPageCounts(channels func() PageChannelCounts, topics func() PageTopicCounts) *PageCounts {
	return &PageCounts{channels: channels, topics: topics}
}

// Read forwards selection and the first-unread choice to both count readers.
func (c *PageCounts) Read(ctx context.Context, s platform.Snapshot, scope Scope, joined int64, channels, topics []kernel.ID, selected *kernel.ID, firstUnread bool) (PageCountResult, error) {
	counts, err := c.channels().ReadWithFirstUnread(ctx, s, scope.OrganizationID, scope.MemberID, joined, channels, firstUnread)
	if err != nil {
		return PageCountResult{}, err
	}
	result := PageCountResult{ChannelCounts: make(map[kernel.ID]int64, len(counts)), FeedFirstUnread: counts[scope.ChannelID].FirstUnread}
	for id, count := range counts {
		result.ChannelCounts[id] = count.Count
	}
	result.TopicCounts, result.TopicFirstUnread, err = c.topics().ReadWithFirstUnread(ctx, s, scope, joined, topics, selected, firstUnread)
	return result, err
}
