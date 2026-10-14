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
// page selection. The last argument, the stream's panel=members, no longer
// changes the selection: panels open beside the conversation (#874).
// Counts are member-dependent: this path never uses the shared message cache.
func RenderSidebar(ctx context.Context, m org.Membership, snapshot conversation.SidebarSnapshot, _ bool) (realtime.Outgoing, error) {
	page := sidebarView(view.ChannelPage{}, m, snapshot)
	var html bytes.Buffer
	if err := view.SidebarLists(page).Render(ctx, &html); err != nil {
		return realtime.Outgoing{}, fmt.Errorf("rendering sidebar: %w", err)
	}
	return realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: html.Bytes()}, nil
}

// sidebarView keeps the page's form, paging and members state while sharing the
// sidebar conversion between reloads and member-dependent live recounts.
func sidebarView(page view.ChannelPage, m org.Membership, snapshot conversation.SidebarSnapshot) view.ChannelPage {
	page.Organization = view.Organization{Slug: m.Organization.Slug, Name: m.Organization.Name}
	page.Current, page.Channels = viewChannel(snapshot.Current), viewChannels(snapshot.Channels)
	page.Topic, page.Topics = viewTopicPtr(snapshot.Topic), viewTopics(snapshot.Topics)
	page.ChannelCounts, page.TopicCounts = snapshot.ChannelCounts, snapshot.TopicCounts
	page.SnapshotSeq = snapshot.SnapshotSeq
	return page
}
