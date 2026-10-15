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
	SnapshotSeq                int64
	Current                    Channel
	Channels                   []Channel
	Topics                     []Topic
	Topic                      *Topic
	ChannelCounts, TopicCounts map[kernel.ID]int64
}

// Sidebar reads lists and counts in an independent read-only repeatable-read
// snapshot, including its organisation event sequence, without history or
// directories. Membership is resolved by org; the channel and optional topic are checked in the snapshot.
func (s *Reader) Sidebar(ctx context.Context, m org.Membership, channelID kernel.ID, topicID *kernel.ID) (sidebar SidebarSnapshot, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		loaded, err := loadSidebarContext(ctx, s.reads(snapshot), m.Organization.ID, channelID, topicID)
		if err != nil {
			return err
		}
		sidebar.Current, sidebar.Channels = loaded.current, loaded.channels
		sidebar.Topic, sidebar.Topics = loaded.topic, loaded.topics
		sidebar.SnapshotSeq, err = s.cursor(snapshot).EventSeq(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("reading sidebar sequence: %w", err)
		}
		counts, err := s.readCounts(ctx, snapshot, m, channelID, sidebar.Channels, sidebar.Topics, topicID, false)
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

// ChannelListPage is a sidebar snapshot, with the channel's topics nested and
// no topic selected, plus the cursor its stream resumes after.
type ChannelListPage struct {
	SidebarSnapshot
	EventCursor int64
}

// ChannelList reads the channel list page in one snapshot:
// the cursor comes from the snapshot that counted, so the stream neither
// skips nor repeats a change. The list shows no divider, so it reads counts only.
func (s *Reader) ChannelList(ctx context.Context, m org.Membership, channelID kernel.ID) (ChannelListPage, error) {
	sidebar, err := s.Sidebar(ctx, m, channelID, nil)
	if err != nil {
		return ChannelListPage{}, fmt.Errorf("reading channel list snapshot: %w", err)
	}
	return ChannelListPage{SidebarSnapshot: sidebar, EventCursor: sidebar.SnapshotSeq}, nil
}

// sidebarContext excludes counts so callers can read history and
// their cursor before counting, all within the snapshot they already own.
type sidebarContext struct {
	current  Channel
	channels []Channel
	topic    *Topic
	topics   []Topic
}

func loadSidebarContext(ctx context.Context, reads ReadStore, organizationID, channelID kernel.ID, topicID *kernel.ID) (sidebar sidebarContext, err error) {
	sidebar.current, err = reads.GetChannel(ctx, organizationID, channelID)
	if err != nil {
		return sidebarContext{}, fmt.Errorf("finding channel: %w", err)
	}
	sidebar.channels, err = reads.ListChannels(ctx, organizationID)
	if err != nil {
		return sidebarContext{}, fmt.Errorf("listing channels: %w", err)
	}
	if topicID != nil {
		selected, err := reads.GetTopic(ctx, organizationID, channelID, *topicID)
		if err != nil {
			return sidebarContext{}, fmt.Errorf("finding topic: %w", err)
		}
		sidebar.topic = &selected
	}
	sidebar.topics, err = reads.ListTopics(ctx, organizationID, channelID, sidebarTopics)
	if err != nil {
		return sidebarContext{}, fmt.Errorf("listing topics: %w", err)
	}
	return sidebar, nil
}
