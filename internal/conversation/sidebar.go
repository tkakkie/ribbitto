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
		loaded, err := loadSidebarContext(ctx, s.reads(snapshot), m.Organization.ID, channelID, topicID)
		if err != nil {
			return err
		}
		sidebar.Current, sidebar.Channels = loaded.current, loaded.channels
		sidebar.Topic, sidebar.Topics = loaded.topic, loaded.topics
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

// ChannelList reads the channel list page in one snapshot, as Members does:
// the cursor comes from the snapshot that counted, so the stream neither
// skips nor repeats a change. The list shows no divider, so it reads counts only.
func (s *Reader) ChannelList(ctx context.Context, m org.Membership, channelID kernel.ID) (page ChannelListPage, err error) {
	err = s.runner.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		loaded, err := loadSidebarContext(ctx, s.reads(snapshot), m.Organization.ID, channelID, nil)
		if err != nil {
			return err
		}
		page.Current, page.Channels, page.Topics = loaded.current, loaded.channels, loaded.topics
		page.EventCursor, err = s.cursor(snapshot).EventSeq(ctx, m.Organization.ID)
		if err != nil {
			return fmt.Errorf("reading channel list cursor: %w", err)
		}
		counts, err := s.readCounts(ctx, snapshot, m, channelID, page.Channels, page.Topics, nil, false)
		if err != nil {
			return err
		}
		page.ChannelCounts, page.TopicCounts = counts.ChannelCounts, counts.TopicCounts
		return nil
	})
	if err != nil {
		return ChannelListPage{}, fmt.Errorf("reading channel list snapshot: %w", err)
	}
	return page, nil
}

// sidebarContext excludes counts so callers can read history or members and
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
