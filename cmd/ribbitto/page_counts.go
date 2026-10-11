package main

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

func pageCountsIn(s platform.Snapshot) conversation.PageCounter { return pageCounter{s} }

func (c pageCounter) Read(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	return c.read(ctx, m, channelID, channels, topics, selected, true)
}

func (c pageCounter) ReadCounts(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	return c.read(ctx, m, channelID, channels, topics, selected, false)
}

func (c pageCounter) read(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID, firstUnread bool) (conversation.PageCounts, error) {
	counts, err := newChannelCounts().ReadWithFirstUnread(ctx, c.snapshot, m.Organization.ID, m.Member.ID, m.Member.JoinedEventSeq, channels, firstUnread)
	if err != nil {
		return conversation.PageCounts{}, err
	}
	result := conversation.PageCounts{
		ChannelCounts:   make(map[kernel.ID]int64, len(counts)),
		FeedFirstUnread: counts[channelID].FirstUnread,
	}
	for id, count := range counts {
		result.ChannelCounts[id] = count.Count
	}
	scope := unread.Scope{OrganizationID: m.Organization.ID, ChannelID: channelID, MemberID: m.Member.ID}
	result.TopicCounts, result.TopicFirstUnread, err = newTopicCounts().ReadWithFirstUnread(ctx, c.snapshot, scope, m.Member.JoinedEventSeq, topics, selected, firstUnread)
	return result, err
}

// newChannelCounts binds sidebar counts to the page snapshot.
func newChannelCounts() *unread.ChannelCounts {
	return unread.NewChannelCounts(unreadpg.ChannelRangesIn,
		func(s platform.Snapshot) unread.ChannelCounter { return conversationpg.ChannelUnreadIn(s) })
}

// newTopicCounts binds topic counts to the page snapshot.
func newTopicCounts() *unread.TopicCounts {
	return unread.NewTopicCounts(unreadpg.TopicStateIn,
		func(s platform.Snapshot) unread.TopicCounter { return conversationpg.TopicUnreadIn(s) })
}
