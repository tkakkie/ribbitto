package main

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web"
)

type liveSidebar struct {
	reader     *conversation.Reader
	membership org.Membership
	channel    kernel.ID
	topic      *kernel.ID
	members    bool
	changes    *unread.Changes
}

var _ realtime.SidebarWatcher = liveSidebar{}

func sidebarSource(reader *conversation.Reader, changes *unread.Changes) func(org.Membership, kernel.ID, *kernel.ID, bool) realtime.SidebarSource {
	return func(m org.Membership, channel kernel.ID, topic *kernel.ID, members bool) realtime.SidebarSource {
		return liveSidebar{reader, m, channel, topic, members, changes}
	}
}

func (s liveSidebar) Watch(organizationID, memberID kernel.ID) realtime.SidebarWatch {
	return s.changes.Watch(organizationID, memberID)
}

func (s liveSidebar) Trigger(event realtime.Event) bool {
	return event.Kind == conversation.KindPosted || event.Kind == conversation.KindMessagesMoved
}

func (s liveSidebar) Recount(ctx context.Context, _ realtime.Subscription) (realtime.Outgoing, error) {
	// The cursor factory is snapshot-bound, like the counts it accompanies.
	snapshot, err := s.reader.Sidebar(ctx, s.membership, s.channel, s.topic)
	if err != nil {
		return realtime.Outgoing{}, err
	}
	return web.RenderSidebar(ctx, s.membership, snapshot, s.members)
}
