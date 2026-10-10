package typing

import (
	"container/list"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type member struct {
	typist   Typist
	activity int
	position *list.Element
}

type summary struct {
	generation int64
	members    map[kernel.ID]*member
	starts     list.List
	latest     [4]Typist
	candidates int
}

func (p *summary) start(typist Typist, generation int64) {
	if m := p.members[typist.Member]; m != nil {
		m.activity++
		return
	}
	m := &member{typist: typist, activity: 1}
	m.position = p.starts.PushFront(m)
	p.members[typist.Member] = m
	p.changed(generation)
}

func (p *summary) stop(id kernel.ID, generation int64) {
	m := p.members[id]
	m.activity--
	if m.activity != 0 {
		return
	}
	p.starts.Remove(m.position)
	delete(p.members, id)
	p.changed(generation)
}

func (p *summary) changed(generation int64) {
	p.generation, p.latest, p.candidates = generation, [4]Typist{}, 0
	// The full order makes candidate replacement bounded even after stops.
	for e := p.starts.Front(); e != nil && p.candidates < len(p.latest); e = e.Next() {
		p.latest[p.candidates] = e.Value.(*member).typist
		p.candidates++
	}
}

// Snapshot is one consistent organisation generation and viewer-filtered place.
// PlaceGeneration is its last distinct-member transition, including clearing.
// Typists contains at most three names, newest first; Remaining counts the rest.
type Snapshot struct {
	Generation      int64
	PlaceGeneration int64
	Typists         []Typist
	Remaining       int
}

// Read reads only the selected summary; the full set excludes even an old viewer.
// It performs no expiry work and returns copies, safe after releasing the lock.
func (s *State) Read(organizationID kernel.ID, place Place, viewer kernel.ID) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result Snapshot
	o := s.organizations[organizationID]
	if o == nil {
		return result
	}
	result.Generation = o.generation
	p := o.places[place]
	if p == nil {
		return result
	}
	result.PlaceGeneration, result.Remaining = p.generation, len(p.members)
	if _, active := p.members[viewer]; active {
		result.Remaining--
	}
	for _, typist := range p.latest[:p.candidates] {
		if typist.Member != viewer && len(result.Typists) < 3 {
			result.Typists = append(result.Typists, typist)
			result.Remaining--
		}
	}
	return result
}
