package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrRecountRefused ends a stream when its configured scheduler refuses work.
var ErrRecountRefused = errors.New("realtime: recount registration refused")

type scheduledSidebar struct {
	registration  *RecountRegistration
	pending       *sidebarDelivery
	version, sent uint64
	active        bool
	// Each capture belongs to its request; receiving that completion publishes it
	// to the writer even if another computation has already captured a generation.
	captures map[uint64]*uint64
	// Guard only the computation boundary, never the persisted read: exit
	// seals access before releasing the watch, even for an uncooperative worker.
	mu      sync.Mutex
	stopped bool
}

func (p *scheduledSidebar) cancel() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.registration.Cancel()
}

func (s Stream) scheduledRecount(ctx context.Context, sub Subscription, p *scheduledSidebar, completion *RecountCompletion, send Sender, written *time.Time) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	pending := p.pending
	if pending.watch != nil {
		generation, _ := pending.watch.Snapshot()
		if generation > pending.observed {
			p.version++
		}
		pending.observed = generation
		pending.dirty = pending.dirty || generation > pending.covered
	}
	// Poll between batches as well as receiving through the combined wait.
	if completion == nil {
		select {
		case result, ok := <-p.registration.Completions():
			if !ok {
				return ErrRecountRefused
			}
			completion = &result
		default:
		}
	}
	var captured *uint64
	if completion != nil {
		p.active, pending.next = false, time.Now().Add(SidebarInterval)
		captured = p.captures[completion.Version]
		delete(p.captures, completion.Version)
	}
	if completion != nil && completion.Version > p.sent && captured != nil {
		if completion.Err != nil {
			return fmt.Errorf("recounting sidebar: %w", completion.Err)
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		out := completion.Outgoing
		out.ID, out.Name, out.Ephemeral = 0, "sidebar", true
		if err := s.checkMember(ctx, sub, Event{OrganizationID: sub.Organization}); err != nil {
			return fmt.Errorf("authorizing sidebar frame: %w", err)
		}
		if err := send.Send(ctx, out); err != nil {
			return fmt.Errorf("sending sidebar frame: %w", err)
		}
		*written = time.Now()
		pending.covered = *captured
		p.sent, pending.dirty = completion.Version, p.version > completion.Version
	}
	if !pending.dirty || p.active || time.Now().Before(pending.next) {
		return nil
	}
	if p.captures == nil {
		p.captures = make(map[uint64]*uint64)
	}
	covered := pending.covered
	p.captures[p.version] = &covered
	if !p.registration.Submit(p.version, func(worker context.Context) (Outgoing, error) {
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return Outgoing{}, context.Canceled
		}
		if pending.watch != nil {
			covered, _ = pending.watch.Snapshot()
		}
		p.mu.Unlock()
		// Preserve request values while Cancel owns cancellation on every exit.
		compute, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(worker, cancel)
		defer stop()
		defer cancel()
		return s.Sidebar.Recount(compute, sub)
	}) {
		return ErrRecountRefused
	}
	p.active = true
	return nil
}
