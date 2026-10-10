package presence

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// MaxSnapshotMembers bounds a page's presence read.
const MaxSnapshotMembers = 100

// Token identifies the process and organisation generation represented by a read.
type Token struct {
	Process    string
	Generation int64
}

// Snapshot is the requested members' state and its token from one locked read.
type Snapshot struct {
	Online []bool
	Token  Token
}

type member struct {
	streams int
	timer   *time.Timer
}

type organization struct {
	generation int64
	members    map[kernel.ID]*member
}

// GenerationRaiser wakes streams after a visible transition has been published.
type GenerationRaiser interface {
	RaiseGeneration(kernel.ID, realtime.Interest, int64)
}

// State tracks accepted streams by organisation and member.
type State struct {
	mu            sync.Mutex
	process       string
	hub           GenerationRaiser
	organizations map[kernel.ID]*organization
}

// New constructs memory-only state for the process's hub.
func New(hub GenerationRaiser) *State {
	return &State{process: rand.Text(), hub: hub, organizations: make(map[kernel.ID]*organization)}
}

// Open counts an accepted stream and returns its idempotent cleanup.
func (s *State) Open(organizationID, memberID kernel.ID) func() {
	s.mu.Lock()
	o := s.organizations[organizationID]
	if o == nil {
		o = &organization{members: make(map[kernel.ID]*member)}
		s.organizations[organizationID] = o
	}
	m := o.members[memberID]
	if m == nil {
		m = &member{}
		o.members[memberID] = m
		s.raise(organizationID, o)
	}
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.streams++
	s.mu.Unlock()
	return sync.OnceFunc(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		m.streams--
		if m.streams != 0 {
			return
		}
		var timer *time.Timer
		timer = time.AfterFunc(30*time.Second, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			// Stop may lose to an already running callback; its identity also
			// protects a later last-close timer after a reconnect.
			if m.timer != timer {
				return
			}
			delete(o.members, memberID)
			s.raise(organizationID, o)
		})
		m.timer = timer
	})
}

func (s *State) raise(id kernel.ID, o *organization) {
	// Holding the state lock through the raise makes every waking read see
	// at least this generation's published state.
	o.generation++
	s.hub.RaiseGeneration(id, realtime.InterestPresence, o.generation)
}

// Read returns one online flag per requested member, in request order.
func (s *State) Read(organizationID kernel.ID, members []kernel.ID) (Snapshot, error) {
	if len(members) > MaxSnapshotMembers {
		return Snapshot{}, fmt.Errorf("presence snapshot exceeds %d members", MaxSnapshotMembers)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Snapshot{Online: make([]bool, len(members)), Token: Token{Process: s.process}}
	if o := s.organizations[organizationID]; o != nil {
		result.Token.Generation = o.generation
		for i, id := range members {
			result.Online[i] = o.members[id] != nil
		}
	}
	return result, nil
}
