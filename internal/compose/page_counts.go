package compose

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/unread/unreadpg"
)

type pageCounter struct{ snapshot platform.Snapshot }

// PageCountsIn binds unread counts to conversation's page and sidebar snapshot.
func PageCountsIn(s platform.Snapshot) conversation.PageCounter { return pageCounter{s} }

func (c pageCounter) Read(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	return c.read(ctx, m, channelID, channels, topics, selected, true)
}

func (c pageCounter) ReadCounts(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	return c.read(ctx, m, channelID, channels, topics, selected, false)
}

func (c pageCounter) read(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID, firstUnread bool) (conversation.PageCounts, error) {
	counts := unread.NewPageCounts(
		func() unread.PageChannelCounts { return NewChannelCounts() },
		func() unread.PageTopicCounts { return NewTopicCounts() })
	result, err := counts.Read(ctx, c.snapshot, unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channelID, MemberID: m.Member.ID}, m.Member.JoinedEventSeq, channels, topics, selected, firstUnread)
	return conversation.PageCounts(result), err
}

// NewChannelCounts binds sidebar counts to the page snapshot.
func NewChannelCounts() *unread.ChannelCounts {
	return unread.NewChannelCounts(unreadpg.ChannelRangesIn,
		func(s platform.Snapshot) unread.ChannelCounter { return conversationpg.ChannelUnreadIn(s) })
}

// NewTopicCounts binds topic counts to the page snapshot.
func NewTopicCounts() *unread.TopicCounts {
	return unread.NewTopicCounts(unreadpg.TopicStateIn,
		func(s platform.Snapshot) unread.TopicCounter { return conversationpg.TopicUnreadIn(s) })
}
