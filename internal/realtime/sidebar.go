package realtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
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

// SidebarWatcher optionally extends SidebarSource with member-scoped wakes.
// Wakes invalidate counts; Recount must read fresh persisted state. Publication
// normally follows commit, with the unknown-outcome exception described by
// unread.ReadPublisher and docs/architecture/unread-writes.md.
type SidebarWatcher interface {
	// Watch returns a non-nil watch before the initial recount; Run owns release.
	Watch(organizationID, memberID kernel.ID) SidebarWatch
}

// SidebarWatch tracks changes within one active subscription lifetime.
// Generations are monotonic within that lifetime, not across reopenings.
type SidebarWatch interface {
	// Snapshot atomically captures the generation and channel before a recount.
	// Later publication closes that channel, including before a wait starts.
	Snapshot() (uint64, <-chan struct{})
	// Release ends the watch; repeated calls are harmless. Do not snapshot afterward.
	Release()
}

// SidebarInterval bounds recounts to once per second per connection, allowing
// bursts to coalesce while keeping current counts responsive. Reconnects always
// recount; production query cost was measured in #812
// (docs/architecture/stream-sidebar-results.md).
const SidebarInterval = time.Second

var errSidebarDue = errors.New("realtime: sidebar recount due")

type sidebarDelivery struct {
	dirty             bool
	next              time.Time
	watch             SidebarWatch
	observed, covered uint64
}

// recount runs between batches so a backlog cannot starve it. Denied frames
// also consume the interval; only a successful write changes the heartbeat.
func (s Stream) recount(ctx context.Context, sub Subscription, pending *sidebarDelivery, send Sender, written *time.Time) error {
	if pending.watch != nil {
		pending.observed, _ = pending.watch.Snapshot()
		pending.dirty = pending.dirty || pending.observed > pending.covered
	}
	if !pending.dirty || time.Now().Before(pending.next) {
		return nil
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	covered := pending.observed // Publication during the persisted read remains pending.
	out, err := s.Sidebar.Recount(ctx, sub)
	if err != nil {
		return fmt.Errorf("recounting sidebar: %w", err)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	out.ID, out.Name, out.Ephemeral = 0, "sidebar", true
	allowed, err := s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, Event{OrganizationID: sub.Organization, AudienceMemberID: &sub.Member})
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
	pending.covered = covered
	pending.dirty, pending.next = false, time.Now().Add(SidebarInterval)
	return nil
}
