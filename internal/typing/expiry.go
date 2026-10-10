package typing

import (
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Expiry is the activity lifetime after an accepted start or refresh.
const Expiry = 5 * time.Second

func (s *State) refresh(organizationID kernel.ID, place Place, memberID kernel.ID, streams *channelStreams) {
	if previous := streams.topics[place.Topic]; previous != nil {
		previous.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(Expiry, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// Stop can lose to a running callback. Identity protects refresh and
		// stop/restart; last-close removes the topic before releasing the lock.
		if streams.topics[place.Topic] != timer {
			return
		}
		s.stop(organizationID, place, memberID)
	})
	streams.topics[place.Topic] = timer
}
