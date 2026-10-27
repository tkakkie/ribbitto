package typing

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestSignalNeedsAcceptedStream(t *testing.T) {
	org := kernel.ID{1}
	place := Place{kernel.ID{2}, kernel.ID{3}}
	person := Typist{Member: kernel.ID{4}}
	zero := Place{Channel: place.Channel}
	for _, tt := range []struct {
		name  string
		setup func(*State) func()
		zero  bool
		want  bool
	}{
		{name: "none"},
		{name: "another channel", setup: func(s *State) func() { return s.Open(org, person.Member, kernel.ID{9}) }},
		{name: "another member", setup: func(s *State) func() { return s.Open(org, kernel.ID{9}, place.Channel) }},
		{name: "another organisation", setup: func(s *State) func() { return s.Open(kernel.ID{9}, person.Member, place.Channel) }},
		{name: "closed stream", setup: func(s *State) func() { s.Open(org, person.Member, place.Channel)(); return nil }},
		{name: "zero topic", setup: func(s *State) func() { return s.Open(org, person.Member, place.Channel) }, zero: true},
		{name: "accepted stream", setup: func(s *State) func() { return s.Open(org, person.Member, place.Channel) }, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(realtime.NewHub())
				generation := s.IssueComposer(org, place, person.Member)
				var closeFn func()
				if tt.setup != nil {
					closeFn = tt.setup(s)
				}
				if closeFn != nil {
					defer closeFn()
				}
				target := place
				if tt.zero {
					target = zero
					generation = s.IssueComposer(org, zero, person.Member)
				}
				if got := s.Signal(org, target, person, generation, time.Now()); got != tt.want {
					t.Fatalf("%s: Signal = %t; want %t", tt.name, got, tt.want)
				}
				if !tt.want {
					if got := s.Read(org, place, kernel.ID{}); got.Generation != 0 || len(got.Typists) != 0 {
						t.Fatalf("%s: Read = %+v; want Generation 0 and no typists", tt.name, got)
					}
					if got := len(s.organizations); got != 0 {
						t.Fatalf("%s: len(organizations) = %d; want 0", tt.name, got)
					}
					if got := closedCount(s); got != 0 {
						t.Fatalf("%s: closedCount = %d; want 0", tt.name, got)
					}
					return
				}
				if got := s.Read(org, place, kernel.ID{}); len(got.Typists) != 1 || got.Typists[0].Member != person.Member {
					t.Fatalf("%s: Read = %+v; want one typist %v", tt.name, got, person.Member)
				}
				closeFn()
			})
		})
	}
}

func TestOpenZeroChannel(t *testing.T) {
	for _, tt := range []struct {
		name  string
		place Place
		want  bool
	}{
		{name: "zero channel", place: Place{kernel.ID{}, kernel.ID{3}}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(realtime.NewHub())
				org, person := kernel.ID{1}, Typist{Member: kernel.ID{4}}
				closeFn := s.Open(org, person.Member, tt.place.Channel)
				if closeFn == nil {
					t.Fatalf("%s: Open = nil; want non-nil close function", tt.name)
				}
				defer closeFn()
				generation := s.IssueComposer(org, tt.place, person.Member)
				if got := s.Signal(org, tt.place, person, generation, time.Now()); got != tt.want {
					t.Fatalf("%s: Signal = %t, len(streams) = %d; want %t, 0", tt.name, got, len(s.streams), tt.want)
				}
				if got := len(s.streams); got != 0 {
					t.Fatalf("%s: len(streams) = %d; want 0", tt.name, got)
				}
				if got := s.Read(org, tt.place, kernel.ID{}); got.Generation != 0 || len(got.Typists) != 0 {
					t.Fatalf("%s: Read = %+v; want Generation 0 and no typists", tt.name, got)
				}
				if got := len(s.organizations); got != 0 {
					t.Fatalf("%s: len(organizations) = %d; want 0", tt.name, got)
				}
				if got := closedCount(s); got != 0 {
					t.Fatalf("%s: closedCount = %d; want 0", tt.name, got)
				}
				closeFn()
				closeFn()
			})
		})
	}
}
