package typing

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(realtime.NewHub())
		org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
		defer s.Open(org, person.Member, place.Channel)()
		check := func(generation int64, active bool) {
			t.Helper()
			got := s.Read(org, place, kernel.ID{})
			if got.Generation != generation || got.PlaceGeneration != generation || (len(got.Typists) == 1) != active {
				t.Fatalf("snapshot = %+v; want %d/%t", got, generation, active)
			}
		}
		advance := func(d time.Duration) { time.Sleep(d); synctest.Wait() }
		s.Start(org, place, person)
		advance(Expiry - time.Nanosecond)
		check(1, true)
		s.Start(org, place, person)
		advance(time.Nanosecond)
		check(1, true)
		advance(Expiry - time.Nanosecond)
		check(2, false)
		s.Start(org, place, person)
		advance(time.Second)
		s.Stop(org, place, person.Member)
		s.Start(org, place, person)
		advance(Expiry - time.Second)
		check(5, true)
		advance(time.Second)
		check(6, false)
	})
}

func TestRunningExpiryCallback(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh", true: "stop/restart"}[restart], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(realtime.NewHub())
				org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
				defer s.Open(org, person.Member, place.Channel)()
				s.Start(org, place, person)
				streams := s.streams[streamKey{org, person.Member, place.Channel}]
				s.mu.Lock()
				time.Sleep(Expiry)
				// Stop returning false proves the callback started while the lock is held.
				if streams.topics[place.Topic].Stop() {
					s.mu.Unlock()
					t.Fatal("callback has not started")
				}
				if restart {
					s.stop(org, place, person.Member)
				} else {
					s.refresh(org, place, person.Member, streams)
				}
				s.mu.Unlock()
				if restart {
					s.Start(org, place, person)
				}
				synctest.Wait()
				want := int64(1)
				if restart {
					want = 3
				}
				if got := s.Read(org, place, kernel.ID{}); got.Generation != want || len(got.Typists) != 1 {
					t.Fatalf("stale callback cleared activity: %+v", got)
				}
				time.Sleep(Expiry)
				synctest.Wait()
				if got := s.Read(org, place, kernel.ID{}); got.Generation != want+1 || len(got.Typists) != 0 {
					t.Fatalf("new deadline did not expire: %+v", got)
				}
			})
		})
	}
}

func TestChannelStreams(t *testing.T) {
	for _, dimension := range []string{"organisation", "member", "channel"} {
		t.Run(dimension, func(t *testing.T) {
			s := New(realtime.NewHub())
			org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
			otherOrg, otherMember, otherChannel := org, person.Member, place.Channel
			switch dimension {
			case "organisation":
				otherOrg = kernel.ID{9}
			case "member":
				otherMember = kernel.ID{9}
			case "channel":
				otherChannel = kernel.ID{9}
			}
			defer s.Open(otherOrg, otherMember, otherChannel)()
			s.Start(org, place, person)
			if got := s.Read(org, place, kernel.ID{}); got.Generation != 0 {
				t.Fatalf("foreign stream admitted signal: %+v", got)
			}
			viewer := kernel.ID{}
			if dimension == "member" {
				viewer = otherMember
			}
			closes := make([]func(), 16)
			for i := range closes {
				closes[i] = s.Open(org, person.Member, place.Channel)
			}
			s.Start(org, place, person)
			second := Place{place.Channel, kernel.ID{5}}
			s.Start(org, second, person)
			s.Start(otherOrg, Place{otherChannel, place.Topic}, Typist{Member: otherMember})
			for _, close := range closes[:15] {
				close()
				close()
			}
			if got := s.Read(org, Place{Channel: place.Channel}, viewer); len(got.Typists) != 1 {
				t.Fatalf("non-last close cleared typing: %+v", got)
			}
			done := make(chan struct{})
			go func() { s.Start(org, place, person); close(done) }()
			closes[15]()
			<-done
			closes[15]()
			s.Start(org, place, person) // A signal after last close cannot revive it.
			for _, p := range []Place{place, second, {Channel: place.Channel}} {
				if got := s.Read(org, p, viewer); len(got.Typists) != 0 {
					t.Fatalf("last close left activity: %+v", got)
				}
			}
			if got := s.Read(otherOrg, Place{otherChannel, place.Topic}, kernel.ID{}); len(got.Typists) != 1 {
				t.Fatalf("last close cleared foreign activity: %+v", got)
			}
		})
	}
}
