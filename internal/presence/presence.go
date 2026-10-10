package presence

import (
	"container/list"
	"crypto/rand"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// MaxSnapshotMembers bounds a page's presence read.
const MaxSnapshotMembers = 100

// MaxChanges bounds one changed-suffix read.
const MaxChanges = 100

// OfflineRetention bounds how long an offline change remains available.
const OfflineRetention = 5 * time.Minute

// Entry is a member's latest visible presence state.
type Entry struct {
	Member kernel.ID
	Online bool
}

// Changes is one consistent changed suffix and its discard boundary and token.
// Reset means the start cannot be served; Entries is then empty.
type Changes struct {
	Entries  []Entry
	Boundary int64
	Token    Token
	Reset    bool
}

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
	streams    int
	timer      *time.Timer
	entry      Entry
	generation int64
	position   *list.Element
}

type organization struct {
	generation int64
	boundary   int64
	changes    list.List
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
		m = &member{entry: Entry{Member: memberID}}
		o.members[memberID] = m
	}
	if !m.entry.Online {
		s.change(organizationID, o, m, true)
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
			s.change(organizationID, o, m, false)
			var discard *time.Timer
			discard = time.AfterFunc(OfflineRetention, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if m.timer != discard {
					return
				}
				o.boundary = max(o.boundary, m.generation)
				o.changes.Remove(m.position)
				delete(o.members, memberID)
			})
			m.timer = discard
		})
		m.timer = timer
	})
}

func (s *State) change(id kernel.ID, o *organization, m *member, online bool) {
	if m.position != nil {
		o.changes.Remove(m.position)
	}
	o.generation++
	m.entry.Online, m.generation = online, o.generation
	m.position = o.changes.PushBack(m)
	// Holding the state lock through the raise makes every waking read see
	// at least this generation's published state.
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
			if m := o.members[id]; m != nil {
				result.Online[i] = m.entry.Online
			}
		}
	}
	return result, nil
}

// After returns at most MaxChanges entries in last-change order after start.
// Another process, a generation below the discard boundary, or a suffix over
// the limit requires reset. A start exactly at the boundary is valid.
func (s *State) After(organizationID kernel.ID, start Token) Changes {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Changes{Token: Token{Process: s.process}}
	o := s.organizations[organizationID]
	if o != nil {
		result.Token.Generation, result.Boundary = o.generation, o.boundary
	}
	if start.Process != s.process || start.Generation < result.Boundary {
		result.Reset = true
		return result
	}
	if o != nil {
		// Walk backwards so both success and overflow inspect at most 101
		// entries, regardless of the number of unchanged online members.
		for e := o.changes.Back(); e != nil; e = e.Prev() {
			m := e.Value.(*member)
			if m.generation <= start.Generation {
				break
			}
			if len(result.Entries) == MaxChanges {
				result.Entries, result.Reset = nil, true
				return result
			}
			result.Entries = append(result.Entries, m.entry)
		}
	}
	slices.Reverse(result.Entries)
	return result
}
