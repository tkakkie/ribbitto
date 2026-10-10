package typing

import (
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type generationFunc func(kernel.ID, realtime.Interest, int64)

func (f generationFunc) RaiseGeneration(id kernel.ID, kind realtime.Interest, generation int64) {
	f(id, kind, generation)
}

func TestTransitions(t *testing.T) {
	org, a, b := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Place{kernel.ID{2}, kernel.ID{5}}
	person := Typist{Member: kernel.ID{4}, DisplayName: "Alice", Handle: "alice"}
	var s *State
	var raises int64
	var selected Place
	var active bool
	s = testState(t, generationFunc(func(id kernel.ID, kind realtime.Interest, generation int64) {
		raises++
		o := s.organizations[id]
		p, feed := o.places[selected], o.places[Place{Channel: a.Channel}]
		if feed == nil || feed.generation != map[int64]int64{1: 1, 2: 1, 3: 1, 4: 4, 5: 5}[generation] || (feed.members[person.Member] != nil) != (generation != 4) {
			t.Fatal("feed notification preceded publication")
		}
		if id != org || kind != realtime.InterestTyping || generation != raises || o.generation != generation || p.generation != generation || (p.members[person.Member] != nil) != active || p.candidates != len(p.members) {
			t.Fatal("notification preceded publication or raised the wrong level")
		}
	}))
	s.Open(org, person.Member, a.Channel)
	for _, tt := range []struct {
		place Place
		start bool
		gen   int64
		feed  int64
		count int
	}{
		{a, true, 1, 1, 1}, {a, true, 1, 1, 1},
		{b, true, 2, 1, 1}, {a, false, 3, 1, 1},
		{a, false, 3, 1, 1}, {b, false, 4, 4, 0}, {a, true, 5, 5, 1},
	} {
		selected, active = tt.place, tt.start
		if tt.start {
			s.Start(org, tt.place, person)
		} else {
			s.Stop(org, tt.place, person.Member)
		}
		got := s.Read(org, Place{Channel: a.Channel}, kernel.ID{})
		if got.Generation != tt.gen || raises != tt.gen || got.PlaceGeneration != tt.feed || len(got.Typists) != tt.count || got.Remaining != 0 {
			t.Fatalf("feed = %+v, raises %d; want %+v", got, raises, tt)
		}
		if self := s.Read(org, tt.place, person.Member); len(self.Typists) != 0 || self.Remaining != 0 {
			t.Fatalf("self indicator = %+v", self)
		}
	}
}

func TestScopes(t *testing.T) {
	for _, dimension := range []string{"organisation", "channel", "topic", "member"} {
		t.Run(dimension, func(t *testing.T) {
			org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
			otherOrg, otherPlace, other := org, place, person
			switch dimension {
			case "organisation":
				otherOrg = kernel.ID{9}
			case "channel":
				otherPlace.Channel = kernel.ID{9}
			case "topic":
				otherPlace.Topic = kernel.ID{9}
			case "member":
				other.Member = kernel.ID{9}
			}
			s := testState(t, realtime.NewHub())
			s.Open(org, person.Member, place.Channel)
			s.Open(otherOrg, other.Member, otherPlace.Channel)
			s.Start(org, place, person)
			s.Stop(otherOrg, otherPlace, other.Member)
			before := s.Read(org, place, kernel.ID{})
			if before.Generation != 1 || !slices.Equal(before.Typists, []Typist{person}) {
				t.Fatalf("foreign stop changed state: %+v", before)
			}
			viewer := kernel.ID{}
			if dimension == "member" {
				viewer = other.Member
			}
			if got := s.Read(otherOrg, otherPlace, viewer); got.Remaining != 0 || (dimension != "member" && len(got.Typists) != 0) || (dimension == "member" && !slices.Equal(got.Typists, []Typist{person})) {
				t.Fatalf("foreign read = %+v", got)
			}
			s.Start(otherOrg, otherPlace, other)
			want := []Typist{person}
			if dimension == "member" {
				want = []Typist{other, person}
			}
			if got := s.Read(otherOrg, otherPlace, kernel.ID{}); !slices.Equal(got.Typists, want) {
				t.Fatalf("independent start = %+v", got)
			}
			s.Stop(otherOrg, otherPlace, other.Member)
			if got := s.Read(org, place, kernel.ID{}); !slices.Equal(got.Typists, []Typist{person}) {
				t.Fatalf("foreign activity erased target: %+v", got)
			}
		})
	}
}

func TestCandidatesAndReadWork(t *testing.T) {
	s := testState(t, realtime.NewHub())
	org, place := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}
	for i := byte(1); i <= 6; i++ {
		s.Open(org, kernel.ID{i}, place.Channel)
		s.Start(org, place, Typist{Member: kernel.ID{i}, DisplayName: string(rune('A' + i))})
	}
	check := func(viewer byte, want []byte, remaining int) Snapshot {
		t.Helper()
		got := s.Read(org, place, kernel.ID{viewer})
		var ids []byte
		for _, person := range got.Typists {
			ids = append(ids, person.Member[0])
		}
		if !slices.Equal(ids, want) || got.Remaining != remaining {
			t.Fatalf("indicator = %+v; want %v + %d", got, want, remaining)
		}
		return got
	}
	check(1, []byte{6, 5, 4}, 2) // Viewer is outside the four latest.
	check(6, []byte{5, 4, 3}, 2)
	check(0, []byte{6, 5, 4}, 3)
	s.Start(org, place, Typist{Member: kernel.ID{1}, DisplayName: "changed"})
	if got := check(1, []byte{6, 5, 4}, 2); got.Generation != 6 {
		t.Fatal("refresh changed generation or order")
	}
	s.Stop(org, place, kernel.ID{6})
	s.Stop(org, place, kernel.ID{5})
	before := check(4, []byte{3, 2, 1}, 0)
	if before.Typists[2].DisplayName == "changed" {
		t.Fatal("refresh replaced captured identity")
	}
	// Poison uncached order and unrelated summaries: any traversal fails
	// deterministically, without timing assertions or extra production hooks.
	p := s.organizations[org].places[place]
	for e := p.starts.Front(); e != nil; e = e.Next() {
		e.Value = nil
	}
	for i := 1; i <= 10000; i++ {
		id := kernel.ID{byte(i), byte(i >> 8), 99}
		s.Open(org, id, id)
		s.Start(org, Place{Channel: id, Topic: id}, Typist{Member: id})
		s.organizations[id] = nil
		s.organizations[org].places[Place{Channel: id}].starts.Front().Value = nil
	}
	if after := check(4, []byte{3, 2, 1}, 0); after.Generation != before.Generation+10000 || after.PlaceGeneration != before.PlaceGeneration {
		t.Fatal("unrelated growth changed selected snapshot")
	}
}

// Summary fixtures leave activities alive; stop timers without new transitions
// against their publication assertions or deliberately poisoned linked lists.
func testState(t *testing.T, hub GenerationRaiser) *State {
	t.Helper()
	s := New(hub)
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, streams := range s.streams {
			for _, timer := range streams.topics {
				timer.Stop()
			}
		}
	})
	return s
}
