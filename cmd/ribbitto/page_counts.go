package main

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/unread"
)

type pageCounter struct{ snapshot platform.Snapshot }

func pageCountsIn(s platform.Snapshot) conversation.PageCounter { return pageCounter{s} }

func (c pageCounter) Read(ctx context.Context, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) (conversation.PageCounts, error) {
	counts, err := newChannelCounts().Read(ctx, c.snapshot, m.Organization.ID, m.Member.ID, m.Member.JoinedEventSeq, channels)
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
	result.TopicCounts, result.TopicFirstUnread, err = newTopicCounts().Read(ctx, c.snapshot, scope, m.Member.JoinedEventSeq, topics, selected)
	return result, err
}
