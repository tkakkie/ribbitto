package realtime

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// EphemeralOwner reads current state and the generation that state represents.
// Read runs once at connect, then when the hub generation advances. The owner
// decides connect output, interprets Subscription.PresenceAfter, and returns an
// empty Outgoing.Name when nothing changed for this place, or "reset" when the
// token cannot be served. State must be published before RaiseGeneration.
// Feature owners keep plain state; web adapters render it into the frame.
type EphemeralOwner interface {
	Read(ctx context.Context, sub Subscription, after int64) (EphemeralFrame, error)
}

// EphemeralFrame is one bounded current-state read, including its delivery
// scope. Generation represents the whole organisation's kind, even when no
// output changed for the selected place. Outgoing.ID is ignored.
type EphemeralFrame struct {
	Organization kernel.ID
	Channel      kernel.ID
	Topic        *kernel.ID
	Generation   int64
	Outgoing     Outgoing
}

// ephemeral never retries within a batch: raises during a read or send are
// handled after the next durable batch, bounding both writes and checks.
func (s Stream) ephemeral(ctx context.Context, sub Subscription, seen *streamLevels, connect bool, send Sender, written *time.Time) (bool, error) {
	levels := s.Hub.sequence(sub.Organization)
	for _, entry := range []struct {
		kind   Interest
		seen   *int64
		latest int64
	}{
		{InterestPresence, &seen.presence, levels.presence.Load().latest},
		{InterestTyping, &seen.typing, levels.typing.Load().latest},
	} {
		if !slices.Contains(sub.Interests, entry.kind) {
			continue
		}
		owner := s.Owners[entry.kind]
		if owner == nil {
			*entry.seen = entry.latest
			continue
		}
		if !connect && entry.latest <= *entry.seen {
			continue
		}
		if ctx.Err() != nil {
			return false, context.Cause(ctx)
		}
		frame, err := owner.Read(ctx, sub, *entry.seen)
		if err != nil {
			return false, fmt.Errorf("reading %s state: %w", entry.kind, err)
		}
		if (!connect && frame.Generation <= *entry.seen) || frame.Outgoing.Name == "" || frame.Organization != sub.Organization ||
			(entry.kind == InterestTyping && (frame.Channel != sub.Channel ||
				(sub.Topic != nil && (frame.Topic == nil || *frame.Topic != *sub.Topic)))) {
			*entry.seen = max(*entry.seen, frame.Generation)
			continue
		}
		if ctx.Err() != nil {
			return false, context.Cause(ctx)
		}
		event := Event{OrganizationID: frame.Organization}
		if entry.kind == InterestTyping {
			event.ChannelID = frame.Channel
		}
		allowed, err := s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
		if err != nil {
			return false, fmt.Errorf("authorizing %s frame: %w", entry.kind, err)
		}
		if allowed {
			frame.Outgoing.ID, frame.Outgoing.Ephemeral = 0, true
			if err := send.Send(ctx, frame.Outgoing); err != nil {
				return false, fmt.Errorf("sending %s frame: %w", entry.kind, err)
			}
			*written = time.Now()
		}
		*entry.seen = max(*entry.seen, frame.Generation)
		if allowed && frame.Outgoing.Name == "reset" {
			return true, nil
		}
	}
	return false, nil
}
