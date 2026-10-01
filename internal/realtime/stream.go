package realtime

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// Sender writes to the connection: an outgoing event, or a heartbeat that
// carries no event. web implements it.
type Sender interface {
	Send(ctx context.Context, out Outgoing) error
	Heartbeat(ctx context.Context) error
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
	// Heartbeat is how long the loop waits on the hub before it sends a
	// heartbeat and waits again; zero sends none. An idle stream otherwise
	// writes nothing, so a proxy may close it and a client that stopped
	// reading is never found out.
	Heartbeat time.Duration
}

// errHeartbeatDue ends one wait on the hub when a heartbeat is due.
var errHeartbeatDue = errors.New("realtime: heartbeat due")

// Run delivers the subscription's events with a sequence above cursor until
// ctx ends or delivery fails, and returns the last sequence it delivered or
// skipped, with the reason it stopped.
//
// Cursor rules:
//   - An event of another channel, of a kind this stream does not deliver
//     (including kinds it does not know), or explicitly denied by the
//     Authorizer is skipped, and the cursor moves past it, so a filtered
//     event cannot keep the loop spinning.
//   - Cancellation of ctx is checked before every event, so nothing is sent
//     after it, and returns context.Cause(ctx) without advancing the cursor.
//   - An error from the EventReader, from the authorization check itself,
//     from the Renderer or from the Sender stops the loop and returns that
//     error with the cursor still before the event that could not be
//     delivered, so a reconnect resumes there. Treating a failed check as a
//     deny would lose the event for good. A failed heartbeat stops the loop
//     the same way; it never moves the cursor.
//
// The loop reads until a read returns fewer events than the batch size and
// only then waits on the hub: the hub's value may be older than the log (a
// fresh process knows no sequence yet), and waiting before draining could
// stall replay. The hub's value may also be ahead of the log where no row
// exists (a cursor below the replay boundary), so once caught up the loop
// waits for a sequence above both its cursor and the last value the hub
// reported; otherwise it would read the same empty range again and again.
// A negative cursor is refused. Every event is authorized immediately before it is sent,
// including ones read in an earlier batch, since access may have been lost
// in between.
func (s Stream) Run(ctx context.Context, sub Subscription, cursor int64, send Sender) (int64, error) {
	if cursor < 0 {
		return cursor, fmt.Errorf("negative cursor %d", cursor)
	}
	batch := s.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	// seen is the highest hub value this loop has already caught up with.
	var seen int64
	for {
		events, err := s.Events.EventsAfter(ctx, sub.Organization, cursor, batch)
		if err != nil {
			return cursor, fmt.Errorf("reading events after %d: %w", cursor, err)
		}
		for _, event := range events {
			// A cancelled stream (for example a session that ended) sends
			// nothing more, even if the rest of the batch is already read and
			// the interfaces it calls would not notice the cancellation.
			if ctx.Err() != nil {
				return cursor, context.Cause(ctx)
			}
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
		seen, err = s.wait(ctx, sub.Organization, max(cursor, seen), send)
		if err != nil {
			return cursor, err
		}
	}
}

// wait is Hub.Wait, sending a heartbeat each time Heartbeat passes first.
func (s Stream) wait(ctx context.Context, org domain.ID, after int64, send Sender) (int64, error) {
	if s.Heartbeat <= 0 {
		return s.Hub.Wait(ctx, org, after)
	}
	for {
		waitCtx, cancel := context.WithTimeoutCause(ctx, s.Heartbeat, errHeartbeatDue)
		seq, err := s.Hub.Wait(waitCtx, org, after)
		cancel()
		if err == nil || ctx.Err() != nil || !errors.Is(err, errHeartbeatDue) {
			return seq, err
		}
		if err := send.Heartbeat(ctx); err != nil {
			return 0, fmt.Errorf("sending heartbeat: %w", err)
		}
	}
}
