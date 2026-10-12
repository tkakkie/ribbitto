package realtime

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// RaiseGeneration publishes a presence or typing generation for an organisation.
// Values at or below the current generation change nothing. Other interests
// are ignored. Owners publish their state before raising its generation;
// presence and typing owners supply current state in production.
func (h *Hub) RaiseGeneration(org kernel.ID, kind Interest, generation int64) {
	switch kind {
	case InterestPresence:
		h.raise(&h.sequence(org).presence, generation)
	case InterestTyping:
		h.raise(&h.sequence(org).typing, generation)
	}
}

type streamLevels struct {
	durable, presence, typing int64
}

func interestedState(level *atomic.Pointer[sequenceState], interests []Interest, kind Interest) *sequenceState {
	if !slices.Contains(interests, kind) {
		// A nil channel disables this kind in the combined select.
		return &sequenceState{}
	}
	return level.Load()
}

// waitFor reads every requested level/channel pair before one combined wait.
// A raise closes the captured channel even if it lands before the select.
// A completion is received directly by the writer, never by a bridge goroutine.
// Once received, it returns with a nil error even if ctx ended; the writer
// checks its parent context without discarding the consumed result.
func (h *Hub) waitFor(ctx context.Context, sub Subscription, after streamLevels, watch SidebarWatch, observed uint64, completions <-chan RecountCompletion) (streamLevels, *RecountCompletion, error) {
	s := h.sequence(sub.Organization)
	for {
		if ctx.Err() != nil {
			return streamLevels{}, nil, context.Cause(ctx)
		}
		durable := s.state.Load()
		presence := interestedState(&s.presence, sub.Interests, InterestPresence)
		typing := interestedState(&s.typing, sub.Interests, InterestTyping)
		var generation uint64
		var changed <-chan struct{}
		if watch != nil {
			generation, changed = watch.Snapshot()
		}
		latest := streamLevels{durable.latest, presence.latest, typing.latest}
		if latest.durable > after.durable || latest.presence > after.presence || latest.typing > after.typing || generation > observed {
			return latest, nil, nil
		}
		if h.afterWaitRead != nil {
			h.afterWaitRead()
		}
		s.waiters.Add(1)
		select {
		case <-durable.changed:
		case <-presence.changed:
		case <-typing.changed:
		case <-changed:
		case completion, ok := <-completions:
			s.waiters.Add(-1)
			if ok {
				return latest, &completion, nil
			}
			// Scheduler cancellation closes its mailbox; closure is not work.
			completions = nil
			continue
		case <-ctx.Done():
		}
		s.waiters.Add(-1)
	}
}
