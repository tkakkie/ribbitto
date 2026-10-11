package web

import (
	"bytes"
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// RenderSidebar renders a validated snapshot for the resolved membership and
// page selection. members suppresses feed selection on the members panel.
// Counts are member-dependent: this path never uses the shared message cache.
func RenderSidebar(ctx context.Context, m org.Membership, snapshot conversation.SidebarSnapshot, members bool) (realtime.Outgoing, error) {
	page := view.ChannelPage{
		Organization: view.Organization{Slug: m.Organization.Slug, Name: m.Organization.Name},
		Current:      viewChannel(snapshot.Current), Channels: viewChannels(snapshot.Channels),
		Topic: viewTopicPtr(snapshot.Topic), Topics: viewTopics(snapshot.Topics),
		ChannelCounts: snapshot.ChannelCounts, TopicCounts: snapshot.TopicCounts,
	}
	if members {
		page.Members = &view.MemberPage{}
	}
	var html bytes.Buffer
	if err := view.SidebarLists(page).Render(ctx, &html); err != nil {
		return realtime.Outgoing{}, fmt.Errorf("rendering sidebar: %w", err)
	}
	return realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: html.Bytes()}, nil
}
