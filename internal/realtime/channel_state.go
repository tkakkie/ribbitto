package realtime

import (
	"context"
	"fmt"
	"time"
)

// ChannelStateSource classifies durable invalidations and reads/renders fresh,
// bounded whole state for the subscription's organisation/channel and context's
// language. Trigger runs before message/topic filtering or vanished-target skips;
// it must not read state. Refresh ignores message/topic selection. Adapters own
// kinds, frame names and snapshot/render sharing; no event-sequence cache may
// stand in for current state. ID and Ephemeral are ignored.
type ChannelStateSource interface {
	Trigger(Event) bool
	Refresh(context.Context, Subscription) (Outgoing, error)
}

// ChannelStateInterval bounds refresh frequency while coalescing durable bursts.
const ChannelStateInterval = time.Second

type channelStateDelivery struct {
	dirty bool
	next  time.Time
}

func (s Stream) refreshChannel(ctx context.Context, sub Subscription, pending *channelStateDelivery, send Sender, written *time.Time) error {
	if !pending.dirty || time.Now().Before(pending.next) {
		return nil
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	out, err := s.ChannelState.Refresh(ctx, sub)
	if err != nil {
		return fmt.Errorf("refreshing channel state: %w", err)
	}
	out.ID, out.Ephemeral = 0, true
	if err := s.checkMember(ctx, sub, Event{OrganizationID: sub.Organization, ChannelID: sub.Channel}); err != nil {
		return fmt.Errorf("authorizing channel state: %w", err)
	}
	if err := send.Send(ctx, out); err != nil {
		return fmt.Errorf("sending channel state: %w", err)
	}
	*written = time.Now()
	// Durable triggers committed during Refresh remain above the read cursor;
	// the next batch classifies them again, even when the hub already woke.
	pending.dirty, pending.next = false, time.Now().Add(ChannelStateInterval)
	return nil
}
