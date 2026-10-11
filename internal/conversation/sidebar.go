package conversation

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// SidebarSnapshot holds fresh sidebar lists and explicit counts, including zero.
// Topic is the validated selection; it never extends the 50-topic Topics list.
type SidebarSnapshot struct {
	Current                    Channel
	Channels                   []Channel
	Topics                     []Topic
	Topic                      *Topic
	ChannelCounts, TopicCounts map[kernel.ID]int64
}

// Sidebar reads lists and counts in an independent read-only repeatable-read
// snapshot, without history, author directories or a stream cursor. Membership
// is resolved by org; the channel and optional topic are checked in the snapshot.
func (s *Reader) Sidebar(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID) (sidebar SidebarSnapshot, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		reads := s.reads(snapshot)
		sidebar.Current, err = reads.GetChannel(ctx, m.Organization.ID, channelID)
		if err != nil {
			return fmt.Errorf("finding channel: %w", err)
		}
		sidebar.Channels, err = reads.ListChannels(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("listing channels: %w", err)
		}
		if topicID != nil {
			selected, err := reads.GetTopic(ctx, m.Organization.ID, channelID, *topicID)
			if err != nil {
				return fmt.Errorf("finding topic: %w", err)
			}
			sidebar.Topic = &selected
		}
		sidebar.Topics, err = reads.ListTopics(ctx, m.Organization.ID, channelID, sidebarTopics)
		if err != nil {
			return fmt.Errorf("listing topics: %w", err)
		}
		counts, err := s.pageCounts(ctx, snapshot, m, channelID, sidebar.Channels, sidebar.Topics, topicID)
		if err != nil {
			return err
		}
		sidebar.ChannelCounts, sidebar.TopicCounts = counts.ChannelCounts, counts.TopicCounts
		return nil
	})
	if err != nil {
		return SidebarSnapshot{}, fmt.Errorf("reading sidebar snapshot: %w", err)
	}
	return sidebar, nil
}
