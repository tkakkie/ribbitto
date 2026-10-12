package realtime_test

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

// costWriter isolates writer scheduling; it is not a replacement for Stream.Run.
// One writer sends; a joined bridge adapts Hub.Wait to a selectable wake.
type costWriter struct {
	stream realtime.Stream
	queue  *costQueue
	work   *costQueueStream
}

func (w costWriter) run(parent context.Context, sub realtime.Subscription, cursor int64, send realtime.Sender) (int64, error) {
	ctx, cancel := context.WithCancel(parent)
	wakes, joined := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(joined)
		after := cursor
		for {
			latest, err := w.stream.Hub.Wait(ctx, sub.Organization, after)
			if err != nil {
				return
			}
			after = latest
			select {
			case wakes <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); w.queue.cancel(w.work); <-joined }()
	version, sent := int64(1), int64(0)
	dirty, active := w.stream.Sidebar != nil && slices.Contains(sub.Interests, realtime.InterestSidebar), false
	next, written := time.Time{}, time.Now()
	var completed *costQueueResult
	for {
		if ctx.Err() != nil {
			return cursor, context.Cause(ctx)
		}
		events, err := w.stream.Events.EventsAfter(ctx, sub.Organization, cursor, realtime.DefaultBatchSize)
		if err != nil {
			return cursor, fmt.Errorf("reading prototype events: %w", err)
		}
		for _, event := range events {
			if w.stream.Sidebar != nil && slices.Contains(sub.Interests, realtime.InterestSidebar) && w.stream.Sidebar.Trigger(event) {
				version++
				dirty = true
			}
			if event.OrganizationID == sub.Organization && event.ChannelID == sub.Channel && slices.Contains(sub.Interests, realtime.InterestMessages) {
				out, err := w.stream.Renderer.Render(ctx, event)
				if err != nil {
					return cursor, fmt.Errorf("rendering prototype event: %w", err)
				}
				allowed := true
				if event.AudienceMemberID != nil && *event.AudienceMemberID != sub.Member {
					allowed, err = w.stream.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
					if err != nil {
						return cursor, fmt.Errorf("authorizing prototype audience: %w", err)
					}
				}
				if err := w.checkMember(ctx, sub, event); err != nil {
					return cursor, err
				}
				if allowed {
					if err := send.Send(ctx, out); err != nil {
						return cursor, fmt.Errorf("sending prototype event: %w", err)
					}
					written = time.Now()
				}
			}
			cursor = event.Seq
		}
		// Consume between batches too, so a durable backlog cannot starve completions.
		if completed == nil {
			select {
			case result := <-w.work.done:
				completed = &result
			default:
			}
		}
		if completed != nil {
			result := *completed
			completed = nil
			active, next = false, time.Now().Add(realtime.SidebarInterval)
			// Newer-than-sent results make progress even under continuous invalidation;
			// requiring the desired version starved sends in #832's controlled run.
			if result.version > sent {
				if result.err != nil {
					return cursor, fmt.Errorf("recounting prototype sidebar: %w", result.err)
				}
				out := result.out
				out.ID, out.Name, out.Ephemeral = 0, "sidebar", true
				if err := w.checkMember(ctx, sub, realtime.Event{OrganizationID: sub.Organization}); err != nil {
					return cursor, err
				}
				if err := send.Send(ctx, out); err != nil {
					return cursor, fmt.Errorf("sending prototype sidebar: %w", err)
				}
				// Keep later triggers pending without rolling back the last sent frame.
				sent, dirty, written = result.version, version > result.version, time.Now()
			}
		}
		if dirty && !active && !time.Now().Before(next) {
			if !w.queue.submit(w.work, costQueueRequest{version, func(ctx context.Context) (realtime.Outgoing, error) {
				return w.stream.Sidebar.Recount(ctx, sub)
			}}) {
				return cursor, fmt.Errorf("prototype recount admission refused")
			}
			active = true
		}
		if w.stream.Heartbeat > 0 && time.Since(written) >= w.stream.Heartbeat {
			if err := w.checkMember(ctx, sub, realtime.Event{OrganizationID: sub.Organization}); err != nil {
				return cursor, err
			}
			if err := send.Heartbeat(ctx); err != nil {
				return cursor, fmt.Errorf("sending prototype heartbeat: %w", err)
			}
			written = time.Now()
		}
		if len(events) == realtime.DefaultBatchSize {
			continue
		}
		deadline := time.Time{}
		if dirty && !active {
			deadline = next
		}
		if w.stream.Heartbeat > 0 && (deadline.IsZero() || written.Add(w.stream.Heartbeat).Before(deadline)) {
			deadline = written.Add(w.stream.Heartbeat)
		}
		var timer <-chan time.Time
		if !deadline.IsZero() {
			timer = time.After(time.Until(deadline))
		}
		select {
		case <-ctx.Done():
		case <-wakes:
		case <-timer:
		case result := <-w.work.done:
			completed = &result
		}
	}
}

func (w costWriter) checkMember(ctx context.Context, sub realtime.Subscription, event realtime.Event) error {
	event.AudienceMemberID = &sub.Member
	allowed, err := w.stream.Authorizer.MayReceive(ctx, sub.Account, sub.OrganizationSlug, event)
	if err != nil {
		return fmt.Errorf("authorizing prototype membership: %w", err)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if !allowed {
		return realtime.ErrMembershipRevoked
	}
	return nil
}
