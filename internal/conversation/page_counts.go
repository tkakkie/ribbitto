package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// PageCounts holds capped sidebar counts and uncapped first unread sequences;
// First sequences are zero when absent or disabled. Older history uses the same counts.
type PageCounts struct {
	SnapshotSeq                       int64
	Witness                           *SidebarWitness
	ChannelCounts, TopicCounts        map[kernel.ID]int64
	FeedFirstUnread, TopicFirstUnread int64
}

// PageCounter reads both sidebar lists using org's resolved membership,
// including its persisted join sequence, in four statements altogether.
type PageCounter interface {
	Read(context.Context, org.Membership, kernel.ID, []kernel.ID, []kernel.ID, *kernel.ID) (PageCounts, error)
}

// SidebarCounter is the counts-only extension used by independent recounts.
// Selection still controls which topic state is loaded, including an unlisted topic.
type SidebarCounter interface {
	// ReadCounts returns both count maps without computing first unread.
	ReadCounts(context.Context, org.Membership, kernel.ID, []kernel.ID, []kernel.ID, *kernel.ID) (PageCounts, error)
}

// PageCountsIn binds unread's count orchestration to a page or sidebar snapshot.
type PageCountsIn func(platform.Snapshot) PageCounter

func (s *Reader) pageCounts(ctx context.Context, snapshot platform.Snapshot, m org.Membership, channelID kernel.ID, channels []Channel, topics []Topic, selected *kernel.ID) (PageCounts, error) {
	return s.readCounts(ctx, snapshot, m, channelID, channels, topics, selected, true)
}

func (s *Reader) readCounts(ctx context.Context, snapshot platform.Snapshot, m org.Membership, channelID kernel.ID, channels []Channel, topics []Topic, selected *kernel.ID, firstUnread bool) (PageCounts, error) {
	channelIDs, topicIDs := make([]kernel.ID, len(channels)), make([]kernel.ID, len(topics))
	for i, channel := range channels {
		channelIDs[i] = channel.ID
	}
	for i, topic := range topics {
		topicIDs[i] = topic.ID
	}
	counter := s.counts(snapshot)
	read := counter.Read
	if !firstUnread {
		// Legacy injected page counters keep their original contract; production
		// supplies the counts-only extension so recounts skip the SQL probes.
		if sidebar, ok := counter.(SidebarCounter); ok {
			read = sidebar.ReadCounts
		}
	}
	counts, err := read(ctx, m, channelID, channelIDs, topicIDs, selected)
	if err != nil {
		return PageCounts{}, fmt.Errorf("reading page counts: %w", err)
	}
	counts.Witness = s.capture(ctx, snapshot, m, channelID, channelIDs, topicIDs, selected)
	return counts, nil
}
