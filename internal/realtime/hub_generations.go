package realtime

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// RaiseGeneration publishes a presence or typing generation for an organisation.
// Values at or below the current generation change nothing. Other interests
// are ignored. Owners will publish their state before raising its generation;
// no owner raises generations in production yet (#736).
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
func (h *Hub) waitFor(ctx context.Context, sub Subscription, after streamLevels) (streamLevels, error) {
	s := h.sequence(sub.Organization)
	for {
		if ctx.Err() != nil {
			return streamLevels{}, context.Cause(ctx)
		}
		durable := s.state.Load()
		presence := interestedState(&s.presence, sub.Interests, InterestPresence)
		typing := interestedState(&s.typing, sub.Interests, InterestTyping)
		latest := streamLevels{durable.latest, presence.latest, typing.latest}
		if latest.durable > after.durable || latest.presence > after.presence || latest.typing > after.typing {
			return latest, nil
		}
		if h.afterWaitRead != nil {
			h.afterWaitRead()
		}
		s.waiters.Add(1)
		select {
		case <-durable.changed:
		case <-presence.changed:
		case <-typing.changed:
		case <-ctx.Done():
		}
		s.waiters.Add(-1)
	}
}
