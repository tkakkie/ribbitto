package realtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// account may still receive it. org implements it (org.Authorizer). (false, nil) is an
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

// UnreadRenderer supplies a shared post frame when message interests do not
// select it. An empty name means a non-post or legacy payload has no frame.
type UnreadRenderer interface {
	RenderUnread(context.Context, Event) (Outgoing, error)
}

// Sender writes to the connection: an outgoing event, or a heartbeat that
// carries no event. web implements it.
type Sender interface {
	Send(ctx context.Context, out Outgoing) error
	Heartbeat(ctx context.Context) error
}

// Subscription is one connection's explicit interests in an organisation,
// scoped to a channel or topic for messages, on behalf of an account. Interests
// grant no access: every matching event still passes the Authorizer. The
// organisation and account come from the URL and the session.
type Subscription struct {
	Organization     kernel.ID
	OrganizationSlug string
	Account          kernel.ID
	// Member binds the stream to its server-resolved membership.
	Member  kernel.ID
	Channel kernel.ID
	// Interests is the set of requested output; empty means no message delivery.
	Interests []Interest
	// Topic, when set, narrows the channel to one topic (a topic view).
	Topic *kernel.ID
	// PresenceAfter is the opaque page token interpreted by the presence owner.
	PresenceAfter string
}

// Outgoing is one event ready to send. ID is the event's sequence, which the
// client returns as Last-Event-ID when it reconnects; ephemeral frames omit it.
type Outgoing struct {
	// Ephemeral omits the SSE id field, preserving the durable cursor.
	Ephemeral bool
	ID        int64
	Name      string
	Data      []byte
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
	// Owners supplies current frames; nil entries preserve durable-only delivery.
	Owners map[Interest]EphemeralOwner
	// Sidebar supplies coalesced recounts; nil leaves sidebar delivery unwired.
	Sidebar SidebarSource
	// Recounts optionally schedules sidebar computation; nil keeps synchronous delivery.
	Recounts *RecountScheduler
	// RecountObserver optionally aggregates scheduled delivery; nil disables observations.
	RecountObserver *RecountObserver
	// BatchSize bounds each read; zero means DefaultBatchSize.
	BatchSize int
	// Heartbeat is how long the loop waits on the hub before it sends a
	// heartbeat and waits again; zero sends none. An idle stream otherwise
	// writes nothing, so a proxy may close it and a client that stopped
	// reading is never found out.
	Heartbeat time.Duration
}

// ErrMembershipRevoked ends a stream whose bound member is no longer authorized.
var ErrMembershipRevoked = errors.New("realtime: stream membership revoked")

// errHeartbeatDue ends one wait on the hub when a heartbeat is due.
var errHeartbeatDue = errors.New("realtime: heartbeat due")

// Run delivers the subscription's events with a sequence above cursor until
// ctx ends or delivery fails, and returns the last sequence it delivered or
// skipped, with the reason it stopped.
//
// Cursor rules:
//   - An event outside the subscription, of a kind this stream does not deliver
//     (including an unregistered kind, whose envelope the reader keeps with no
//     channel), or denied for its event audience by the
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
// A denied bound membership ends Run with ErrMembershipRevoked, without
// advancing the cursor. Heartbeats re-check that membership even while idle.
// A negative cursor is refused. Every event is authorized after it is
// rendered and immediately before it is sent, including ones read in an
// earlier batch, since access may have been lost in between.
func (s Stream) Run(ctx context.Context, sub Subscription, cursor int64, send Sender) (int64, error) {
	return s.run(ctx, sub, cursor, send, nil)
}

// RunReserved consumes an already-admitted registration without registering again.
// It returns ErrRecountRefused for nil, cancelled or already-consumed reservations,
// or a reservation from a different non-nil Recounts scheduler. A failed second
// consumption does not cancel the first owner's stream. Every consumed reservation
// is cancelled on exit, or immediately when sidebar delivery is unwired/unwanted.
// With Recounts nil, the reservation supplies the scheduler. Delivery follows Run.
func (s Stream) RunReserved(ctx context.Context, sub Subscription, cursor int64, send Sender, registration *RecountRegistration) (int64, error) {
	if registration == nil || !registration.consume() {
		return cursor, ErrRecountRefused
	}
	defer registration.Cancel()
	if s.Recounts != nil && s.Recounts != registration.q {
		return cursor, ErrRecountRefused
	}
	if s.Sidebar == nil || !slices.Contains(sub.Interests, InterestSidebar) {
		registration.Cancel()
		registration = nil
	}
	return s.run(ctx, sub, cursor, send, registration)
}

func (s Stream) run(ctx context.Context, sub Subscription, cursor int64, send Sender, registration *RecountRegistration) (int64, error) {
	if cursor < 0 {
		return cursor, fmt.Errorf("negative cursor %d", cursor)
	}
	batch := s.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	var seen streamLevels
	connected := false
	// written is when the stream last wrote, which the next heartbeat counts
	// from: wakeups that write nothing, such as another channel's events,
	// must not put it off.
	written := time.Now()
	sidebar := sidebarDelivery{dirty: s.Sidebar != nil && slices.Contains(sub.Interests, InterestSidebar)}
	if sidebar.dirty {
		if source, ok := s.Sidebar.(SidebarWatcher); ok {
			sidebar.watch = source.Watch(sub.Organization, sub.Member)
			defer sidebar.watch.Release()
		}
	}
	var scheduled *scheduledSidebar
	if sidebar.dirty && (registration != nil || s.Recounts != nil) {
		if registration == nil {
			registration = s.Recounts.Register()
		}
		if registration == nil {
			return cursor, ErrRecountRefused
		}
		scheduled = &scheduledSidebar{registration: registration, pending: &sidebar, version: 1}
		defer scheduled.cancel()
	}
	var completion *RecountCompletion
	for {
		events, err := s.Events.EventsAfter(ctx, sub.Organization, cursor, batch)
		if errors.Is(err, ErrCursorExpired) || (err == nil && !contiguous(cursor, events)) {
			if ctx.Err() != nil {
				return cursor, context.Cause(ctx)
			}
			if err := s.checkMember(ctx, sub, Event{OrganizationID: sub.Organization}); err != nil {
				return cursor, err
			}
			return cursor, send.Send(ctx, Outgoing{ID: cursor, Name: "reset"})
		}
		if err != nil {
			return cursor, fmt.Errorf("reading events after %d: %w", cursor, err)
		}
		for _, event := range events {
			if s.Sidebar != nil && slices.Contains(sub.Interests, InterestSidebar) && event.OrganizationID == sub.Organization && s.Sidebar.Trigger(event) {
				sidebar.dirty = true
				if scheduled != nil {
					scheduled.version++
				}
			}
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
		reset, err := s.ephemeral(ctx, sub, &seen, !connected, send, &written)
		if err != nil || reset {
			return cursor, err
		}
		connected = true
		if scheduled != nil {
			err = s.scheduledRecount(ctx, sub, scheduled, completion, send, &written)
			completion = nil
		} else {
			err = s.recount(ctx, sub, &sidebar, send, &written)
		}
		if err != nil {
			return cursor, err
		}
		if len(events) == batch {
			// Draining a backlog that is all other channels' events writes
			// nothing either; the heartbeat is still due on time.
			if s.Heartbeat > 0 && time.Since(written) >= s.Heartbeat {
				if err := s.heartbeat(ctx, sub, send, &written); err != nil {
					return cursor, err
				}
			}
			continue
		}
		seen.durable = max(cursor, seen.durable)
		deadline := time.Time{}
		if sidebar.dirty && (scheduled == nil || !scheduled.active) {
			deadline = sidebar.next
		}
		var completions <-chan RecountCompletion
		if scheduled != nil {
			completions = scheduled.registration.Completions()
		}
		levels, received, err := s.wait(ctx, sub, seen, send, &written, deadline, sidebar.watch, sidebar.observed, completions)
		completion = received
		if received != nil {
			scheduled.received()
		}
		// Generations count as seen only after the owner returns their state.
		seen.durable = levels.durable
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
	if event.OrganizationID != sub.Organization {
		return false, nil
	}
	// Render first, then authorize: a render can wait on the database, and
	// access lost meanwhile must still stop this event. Renders are shared,
	// so rendering one that is then denied costs little. Authorization checks
	// a fresh shared access epoch. A render error stops the loop even for an event
	// that would have been denied, so nothing is ever skipped without a
	// decision.
	var out Outgoing
	var err error
	selected := sub.wants(event)
	if selected {
		out, err = s.Renderer.Render(ctx, event)
		selected = sub.wantsRendered(event, out)
	}
	if err == nil && !selected && slices.Contains(sub.Interests, InterestSidebar) {
		if renderer, ok := s.Renderer.(UnreadRenderer); ok {
			out, err = renderer.RenderUnread(ctx, event)
			selected = out.Name != ""
		}
	}
	if err != nil {
		return false, fmt.Errorf("rendering event %d: %w", event.Seq, err)
	}
	if !selected {
		return false, nil
	}
	// Own-audience and organisation-wide events need only the bound check.
	// Check another audience first so the bound check remains last before send.
	allowed := true
	if event.AudienceMemberID != nil && *event.AudienceMemberID != sub.Member {
		allowed, err = s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
		if err != nil {
			return false, fmt.Errorf("authorizing event %d: %w", event.Seq, err)
		}
	}
	if err := s.checkMember(ctx, sub, event); err != nil {
		return false, err
	}
	if !allowed {
		return false, nil
	}
	if err := send.Send(ctx, out); err != nil {
		return false, fmt.Errorf("sending event %d: %w", event.Seq, err)
	}
	return true, nil
}

// checkMember keeps event scope while binding authorization to this stream,
// independently of the event's audience. Rendering must precede this check.
func (s Stream) checkMember(ctx context.Context, sub Subscription, event Event) error {
	event.AudienceMemberID = &sub.Member
	allowed, err := s.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
	if err != nil {
		return fmt.Errorf("authorizing stream membership: %w", err)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if !allowed {
		return ErrMembershipRevoked
	}
	return nil
}

// heartbeat checks idle membership before writing and recording the last write.
func (s Stream) heartbeat(ctx context.Context, sub Subscription, send Sender, written *time.Time) error {
	if err := s.checkMember(ctx, sub, Event{OrganizationID: sub.Organization}); err != nil {
		return err
	}
	if err := send.Heartbeat(ctx); err != nil {
		return fmt.Errorf("sending heartbeat: %w", err)
	}
	*written = time.Now()
	return nil
}

// wait combines durable and generation wakes with a pending recount deadline,
// sending heartbeats counted from *written, even while the recount is pending.
// A non-nil completion belongs to the writer; receiving it changes no deadline.
// It is returned even when the parent context ends, then with that cause.
func (s Stream) wait(ctx context.Context, sub Subscription, after streamLevels, send Sender, written *time.Time, recount time.Time, watch SidebarWatch, observed uint64, completions <-chan RecountCompletion) (streamLevels, *RecountCompletion, error) {
	if s.Heartbeat <= 0 && recount.IsZero() {
		levels, completion, err := s.Hub.waitFor(ctx, sub, after, watch, observed, completions)
		// The hub returns a received completion without an error; report a
		// parent that ended as the deadline path does through heartbeatDue.
		if completion != nil && ctx.Err() != nil {
			err = context.Cause(ctx)
		}
		return levels, completion, err
	}
	for {
		deadline, cause := recount, errSidebarDue
		if s.Heartbeat > 0 && (deadline.IsZero() || written.Add(s.Heartbeat).Before(deadline)) {
			deadline, cause = written.Add(s.Heartbeat), errHeartbeatDue
		}
		waitCtx, cancel := context.WithDeadlineCause(ctx, deadline, cause)
		levels, completion, err := s.Hub.waitFor(waitCtx, sub, after, watch, observed, completions)
		cancel()
		if ctx.Err() == nil && errors.Is(err, errSidebarDue) {
			return after, nil, nil
		}
		if due, err := heartbeatDue(ctx, err); !due {
			return levels, completion, err
		}
		if err := s.heartbeat(ctx, sub, send, written); err != nil {
			return streamLevels{}, nil, err
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
