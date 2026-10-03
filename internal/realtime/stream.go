package realtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// EventReader reads an organisation's durable events with a sequence above
// after, in sequence order, at most limit of them. The module's store
// implements it (realtimepg.NewReader).
// Each batch checks the replay boundary and committed event_seq in the same
// snapshot as its rows. A cursor below the boundary or above event_seq returns
// ErrCursorExpired, including for a zero-limit read; equality is valid.
type EventReader interface {
	EventsAfter(ctx context.Context, organizationID kernel.ID, after int64, limit int) ([]Event, error)
}

// Authorizer decides, immediately before an event is sent, whether the
// account may still receive it. app/authz implements it. (false, nil) is an
// explicit deny; an error means the check itself failed and says nothing
// about access.
type Authorizer interface {
	MayReceive(ctx context.Context, accountID kernel.ID, organizationSlug string, event Event) (bool, error)
}

// Renderer turns an event the subscription wants into what the stream
// sends; the stream does the subscription's filtering, so a render depends
// only on the event. It runs before the Authorizer's check, so its result
// is discarded when the check denies the event. web implements it.
type Renderer interface {
	Render(ctx context.Context, event Event) (Outgoing, error)
}

// Sender writes to the connection: an outgoing event, or a heartbeat that
// carries no event. web implements it.
type Sender interface {
	Send(ctx context.Context, out Outgoing) error
	Heartbeat(ctx context.Context) error
}

// Subscription is what one connection asked for: an organisation's channel,
// or one topic in it, on behalf of an account. The organisation and account
// come from the URL and the session, never from the client's request body.
type Subscription struct {
	Organization     kernel.ID
	OrganizationSlug string
	Account          kernel.ID
	Channel          kernel.ID
	// Topic, when set, narrows the channel to one topic (a topic view).
	Topic *kernel.ID
}

// Outgoing is one event ready to send. ID is the event's sequence, which the
// client returns as Last-Event-ID when it reconnects.
type Outgoing struct {
	ID   int64
	Name string
	Data []byte
	// Topic is the message's topic as the shared render read it. It is
	// never sent; legacy posting events without a topic use it for filtering.
	Topic kernel.ID
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
//   - An event outside the subscription, of a kind this stream does not deliver
//     (including an unregistered kind, whose envelope the reader keeps with no
//     channel), or explicitly denied by the
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
// stall replay. The hub's value may also be ahead of what a read returns,
// for example when the organisation's row is gone and reads come back empty,
// so once caught up the loop waits for a sequence above both its cursor and
// the last value the hub reported; otherwise it would read the same empty
// range again and again.
// A negative cursor is refused. Every event is authorized after it is
// rendered and immediately before it is sent, including ones read in an
// earlier batch, since access may have been lost in between.
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
	// written is when the stream last wrote, which the next heartbeat counts
	// from: wakeups that write nothing, such as another channel's events,
	// must not put it off.
	written := time.Now()
	for {
		events, err := s.Events.EventsAfter(ctx, sub.Organization, cursor, batch)
		if errors.Is(err, ErrCursorExpired) || (err == nil && !contiguous(cursor, events)) {
			if ctx.Err() != nil {
				return cursor, context.Cause(ctx)
			}
			return cursor, send.Send(ctx, Outgoing{ID: cursor, Name: "reset"})
		}
		if err != nil {
			return cursor, fmt.Errorf("reading events after %d: %w", cursor, err)
		}
		for _, event := range events {
			sent, err := s.deliver(ctx, sub, event, send)
			if err != nil {
				return cursor, err
			}
			// Skipped events move the cursor but not the last write.
			if sent {
				written = time.Now()
			}
			cursor = event.Seq
		}
		if len(events) == batch {
			// Draining a backlog that is all other channels' events writes
			// nothing either; the heartbeat is still due on time.
			if s.Heartbeat > 0 && time.Since(written) >= s.Heartbeat {
				if err := heartbeat(ctx, send, &written); err != nil {
					return cursor, err
				}
			}
			continue
		}
		seen, err = s.wait(ctx, sub.Organization, max(cursor, seen), send, &written)
		if err != nil {
			return cursor, err
		}
	}
}

// deliver sends one event if this connection wants it and may receive it.
// It reports whether the event was sent; false is a skip, which still moves
// the cursor. An error means the event was neither sent nor skipped, so Run
// returns it with the cursor before the event.
func (s Stream) deliver(ctx context.Context, sub Subscription, event Event, send Sender) (bool, error) {
	// A cancelled stream (for example a session that ended) sends nothing
	// more, even if the rest of the batch is already read and the interfaces
	// it calls would not notice the cancellation.
	if ctx.Err() != nil {
		return false, context.Cause(ctx)
	}
	if !sub.wants(event) {
		return false, nil
	}
	// Render first, then authorize: a render can wait on the database, and
	// access lost meanwhile must still stop this event. Renders are shared,
	// so rendering one that is then denied costs little, and the check stays
	// one query per event. A render error stops the loop even for an event
	// that would have been denied, so nothing is ever skipped without a
	// decision.
	out, err := s.Renderer.Render(ctx, event)
	if err != nil {
		return false, fmt.Errorf("rendering event %d: %w", event.Seq, err)
	}
	if !sub.wantsRendered(event, out) {
		return false, nil
	}
	allowed, err := s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
	if err != nil {
		return false, fmt.Errorf("authorizing event %d: %w", event.Seq, err)
	}
	if !allowed {
		return false, nil
	}
	if err := send.Send(ctx, out); err != nil {
		return false, fmt.Errorf("sending event %d: %w", event.Seq, err)
	}
	return true, nil
}

// heartbeat sends a heartbeat and records it as the last write.
func heartbeat(ctx context.Context, send Sender, written *time.Time) error {
	if err := send.Heartbeat(ctx); err != nil {
		return fmt.Errorf("sending heartbeat: %w", err)
	}
	*written = time.Now()
	return nil
}

// wait is Hub.Wait, sending a heartbeat whenever Heartbeat has passed since
// *written first; it moves *written on every heartbeat.
func (s Stream) wait(ctx context.Context, org kernel.ID, after int64, send Sender, written *time.Time) (int64, error) {
	if s.Heartbeat <= 0 {
		return s.Hub.Wait(ctx, org, after)
	}
	for {
		waitCtx, cancel := context.WithDeadlineCause(ctx, written.Add(s.Heartbeat), errHeartbeatDue)
		seq, err := s.Hub.Wait(waitCtx, org, after)
		cancel()
		if due, err := heartbeatDue(ctx, err); !due {
			return seq, err
		}
		if err := heartbeat(ctx, send, written); err != nil {
			return 0, err
		}
	}
}

// heartbeatDue reads how a wait with a heartbeat deadline ended: due when
// only the deadline ended it, otherwise the error to return. The stream's
// own context is checked first: if it ended too, its cause (a session that
// ended, a shutdown) wins over the deadline that may have fired just before.
func heartbeatDue(ctx context.Context, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, context.Cause(ctx)
	}
	if errors.Is(err, errHeartbeatDue) {
		return true, nil
	}
	return false, err
}

// contiguous reports whether events continue the cursor without a gap. The
// log above the boundary has no gaps (#213's trigger), so a gap anywhere in
// a batch means rows the cursor still needed are gone: the whole batch is
// refused before any of it is delivered.
func contiguous(cursor int64, events []Event) bool {
	next := cursor + 1
	for _, event := range events {
		if event.Seq != next {
			return false
		}
		next++
	}
	return true
}
