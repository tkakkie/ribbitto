package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// RenderSidebar renders a validated snapshot for the resolved membership and
// page selection. Counts are member-dependent: this path never uses the
// shared message cache.
func RenderSidebar(ctx context.Context, m org.Membership, snapshot conversation.SidebarSnapshot) (realtime.Outgoing, error) {
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
	page.MemberID = m.Member.ID
	return page
}

// SidebarResync asks the resolved member's streams for fresh counts.
type SidebarResync interface {
	RequestResync(organizationID, memberID kernel.ID)
}

func resyncSidebar(resync SidebarResync, limiter *middleware.MemberRateLimiter) func(http.ResponseWriter, *http.Request, org.Membership) {
	return func(w http.ResponseWriter, r *http.Request, m org.Membership) {
		if resync == nil {
			http.NotFound(w, r)
			return
		}
		if !limiter.Allow(m.Organization.ID, m.Member.ID) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		resync.RequestResync(m.Organization.ID, m.Member.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}
