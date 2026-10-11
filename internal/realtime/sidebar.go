package realtime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SidebarSource recognises recount triggers and renders full current state.
// Trigger receives organisation-scoped events regardless of message interests,
// channel or topic. Consumers own event kinds; realtime never names them.
// Trigger must not run counting queries; those belong only in Recount.
// Recount receives the connection's scope and viewer. Its ID, Name and
// Ephemeral fields are ignored; delivery always sends sidebar without an id.
type SidebarSource interface {
	Trigger(Event) bool
	Recount(context.Context, Subscription) (Outgoing, error)
}

// SidebarInterval bounds recounts to once per second per connection, allowing
// bursts to coalesce while keeping current counts responsive. Reconnects always
// recount; production query cost is measured separately in #812.
const SidebarInterval = time.Second

var errSidebarDue = errors.New("realtime: sidebar recount due")

type sidebarDelivery struct {
	dirty bool
	next  time.Time
}

// recount runs between batches so a backlog cannot starve it. Denied frames
// also consume the interval; only a successful write changes the heartbeat.
func (s Stream) recount(ctx context.Context, sub Subscription, pending *sidebarDelivery, send Sender, written *time.Time) error {
	if !pending.dirty || time.Now().Before(pending.next) {
		return nil
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	out, err := s.Sidebar.Recount(ctx, sub)
	if err != nil {
		return fmt.Errorf("recounting sidebar: %w", err)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	out.ID, out.Name, out.Ephemeral = 0, "sidebar", true
	allowed, err := s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, Event{OrganizationID: sub.Organization})
	if err != nil {
		return fmt.Errorf("authorizing sidebar frame: %w", err)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if allowed {
		if err := send.Send(ctx, out); err != nil {
			return fmt.Errorf("sending sidebar frame: %w", err)
		}
		*written = time.Now()
	}
	pending.dirty, pending.next = false, time.Now().Add(SidebarInterval)
	return nil
}
