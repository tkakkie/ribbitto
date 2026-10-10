package typing

import (
	"sync"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// Typist is trusted display identity captured when a member starts typing.
type Typist struct {
	Member      kernel.ID
	DisplayName string
	Handle      string
}

// Place selects a channel feed (zero Topic) or a topic within that channel.
type Place struct {
	Channel kernel.ID
	Topic   kernel.ID
}

// GenerationRaiser wakes streams after a visible transition has been published.
type GenerationRaiser interface {
	RaiseGeneration(kernel.ID, realtime.Interest, int64)
}

type organization struct {
	generation int64
	places     map[Place]*summary
}

// State owns memory-only typing activity by organisation, channel and topic.
type State struct {
	mu            sync.Mutex
	hub           GenerationRaiser
	organizations map[kernel.ID]*organization
}

// New constructs memory-only state for the process's hub.
func New(hub GenerationRaiser) *State {
	return &State{hub: hub, organizations: make(map[kernel.ID]*organization)}
}

// Start adds one member/topic activity using server-resolved scope and identity.
// Topic must be nonzero. An existing activity preserves its identity and order.
func (s *State) Start(organizationID kernel.ID, place Place, typist Typist) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if place.Topic == (kernel.ID{}) {
		return
	}
	o := s.organizations[organizationID]
	if o == nil {
		o = &organization{places: make(map[Place]*summary)}
		s.organizations[organizationID] = o
	}
	topic := o.place(place)
	if topic.members[typist.Member] != nil {
		return
	}
	o.generation++
	topic.start(typist, o.generation)
	o.place(Place{Channel: place.Channel}).start(typist, o.generation)
	// Keep the lock through notification so a waking read cannot acknowledge
	// a generation whose state has not yet been published.
	s.hub.RaiseGeneration(organizationID, realtime.InterestTyping, o.generation)
}

// Stop removes only the member's activity in the selected topic, idempotently.
func (s *State) Stop(organizationID kernel.ID, place Place, memberID kernel.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.organizations[organizationID]
	if o == nil || place.Topic == (kernel.ID{}) {
		return
	}
	topic := o.places[place]
	if topic == nil || topic.members[memberID] == nil {
		return
	}
	o.generation++
	topic.stop(memberID, o.generation)
	o.places[Place{Channel: place.Channel}].stop(memberID, o.generation)
	s.hub.RaiseGeneration(organizationID, realtime.InterestTyping, o.generation)
}

func (o *organization) place(place Place) *summary {
	p := o.places[place]
	if p == nil {
		p = &summary{members: make(map[kernel.ID]*member)}
		o.places[place] = p
	}
	return p
}
