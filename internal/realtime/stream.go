package realtime

import (
	"context"
	"fmt"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// EventReader reads an organisation's durable events with a sequence above
// after, in sequence order, at most limit of them. infra/postgres implements
// it without importing this package, so it uses domain types only.
type EventReader interface {
	EventsAfter(ctx context.Context, organizationID domain.ID, after int64, limit int) ([]domain.Event, error)
}

// Authorizer decides, immediately before an event is sent, whether the
// account may still receive it. app implements it without importing this
// package. (false, nil) is an explicit deny; an error means the check itself
// failed and says nothing about access.
type Authorizer interface {
	MayReceive(ctx context.Context, accountID domain.ID, organizationSlug string, event domain.Event) (bool, error)
}

// Renderer turns an event the account may receive into what the stream
// sends. web implements it.
type Renderer interface {
	Render(ctx context.Context, sub Subscription, event domain.Event) (Outgoing, error)
}

// Sender writes one outgoing event to the connection. web implements it.
type Sender interface {
	Send(ctx context.Context, out Outgoing) error
}

// Subscription is what one connection asked for: an organisation's channel,
// on behalf of an account. The organisation and account come from the URL
// and the session, never from the client's request body.
type Subscription struct {
	Organization     domain.ID
	OrganizationSlug string
	Account          domain.ID
	Channel          domain.ID
}

// Outgoing is one event ready to send. ID is the event's sequence, which the
// client returns as Last-Event-ID when it reconnects.
type Outgoing struct {
	ID   int64
	Name string
	Data []byte
}

// DefaultBatchSize bounds how many events one read returns.
const DefaultBatchSize = 100

// Stream runs the per-connection delivery loop. Replay and live delivery
// are the same loop, so they cannot interleave out of order.
type Stream struct {
	Hub        *Hub
	Events     EventReader
	Authorizer Authorizer
	Renderer   Renderer
	// BatchSize bounds each read; zero means DefaultBatchSize.
	BatchSize int
}

// Run delivers the subscription's events with a sequence above cursor until
// ctx ends or delivery fails, and returns the last sequence it delivered or
// skipped, with the reason it stopped.
//
// Cursor rules:
//   - An event of another channel, of a kind this stream does not deliver
//     (including kinds it does not know), or explicitly denied by the
//     Authorizer is skipped, and the cursor moves past it, so a filtered
//     event cannot keep the loop spinning.
//   - An error from the EventReader, from the authorization check itself,
//     from the Renderer or from the Sender stops the loop and returns that
//     error with the cursor still before the event that could not be
//     delivered, so a reconnect resumes there. Treating a failed check as a
//     deny would lose the event for good.
//
// The loop reads until a read returns fewer events than the batch size and
// only then waits on the hub: the hub's value may be older than the log (a
// fresh process knows no sequence yet), and waiting before draining could
// stall replay. Every event is authorized immediately before it is sent,
// including ones read in an earlier batch, since access may have been lost
// in between.
func (s Stream) Run(ctx context.Context, sub Subscription, cursor int64, send Sender) (int64, error) {
	batch := s.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	for {
		events, err := s.Events.EventsAfter(ctx, sub.Organization, cursor, batch)
		if err != nil {
			return cursor, fmt.Errorf("reading events after %d: %w", cursor, err)
		}
		for _, event := range events {
			if event.Kind != domain.EventMessagePosted || event.ChannelID != sub.Channel {
				cursor = event.Seq
				continue
			}
			allowed, err := s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
			if err != nil {
				return cursor, fmt.Errorf("authorizing event %d: %w", event.Seq, err)
			}
			if !allowed {
				cursor = event.Seq
				continue
			}
			out, err := s.Renderer.Render(ctx, sub, event)
			if err != nil {
				return cursor, fmt.Errorf("rendering event %d: %w", event.Seq, err)
			}
			if err := send.Send(ctx, out); err != nil {
				return cursor, fmt.Errorf("sending event %d: %w", event.Seq, err)
			}
			cursor = event.Seq
		}
		if len(events) == batch {
			continue
		}
		if err := s.Hub.Wait(ctx, sub.Organization, cursor); err != nil {
			return cursor, err
		}
	}
}
