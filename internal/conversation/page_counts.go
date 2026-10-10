package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PageCounts holds capped sidebar counts and uncapped first unread sequences;
// zero means the scope has no unread message. Older history uses the same counts.
type PageCounts struct {
	ChannelCounts, TopicCounts        map[kernel.ID]int64
	FeedFirstUnread, TopicFirstUnread int64
}

// PageCounter reads both sidebar lists using org's resolved membership,
// including its persisted join sequence, in four statements altogether.
type PageCounter interface {
	Read(context.Context, org.Membership, kernel.ID, []kernel.ID, []kernel.ID, *kernel.ID) (PageCounts, error)
}

// PageCountsIn binds unread's count orchestration to the page's snapshot.
type PageCountsIn func(platform.Snapshot) PageCounter

func (s *Reader) pageCounts(ctx context.Context, snapshot platform.Snapshot, m org.Membership, channelID kernel.ID, channels []Channel, topics []Topic, selected *kernel.ID) (PageCounts, error) {
	channelIDs, topicIDs := make([]kernel.ID, len(channels)), make([]kernel.ID, len(topics))
	for i, channel := range channels {
		channelIDs[i] = channel.ID
	}
	for i, topic := range topics {
		topicIDs[i] = topic.ID
	}
	counts, err := s.counts(snapshot).Read(ctx, m, channelID, channelIDs, topicIDs, selected)
	if err != nil {
		return PageCounts{}, fmt.Errorf("reading page counts: %w", err)
	}
	return counts, nil
}
