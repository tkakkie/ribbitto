package typing

import (
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type streamKey struct {
	organization, member, channel kernel.ID
}

type channelStreams struct {
	count  int
	topics map[kernel.ID]*time.Timer
}

// Open counts an accepted channel stream and returns its idempotent cleanup.
// Interests and selected topic do not affect the member's channel lifetime.
func (s *State) Open(organizationID, memberID, channelID kernel.ID) func() {
	if channelID == (kernel.ID{}) {
		return func() {}
	}
	key := streamKey{organizationID, memberID, channelID}
	s.mu.Lock()
	streams := s.streams[key]
	if streams == nil {
		streams = &channelStreams{topics: make(map[kernel.ID]*time.Timer)}
		s.streams[key] = streams
	}
	streams.count++
	s.mu.Unlock()
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		streams.count--
		if streams.count != 0 {
			return
		}
		for topic := range streams.topics {
			s.stop(organizationID, Place{channelID, topic}, memberID)
		}
		delete(s.streams, key)
	})
}
