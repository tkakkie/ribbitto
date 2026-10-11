package typing

import (
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestComposerOrdering(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "post", true: "failed submission then retry"}[failed], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(realtime.NewHub())
				org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
				defer s.Open(org, person.Member, place.Channel)()
				generation := s.IssueComposer(org, place, person.Member)
				other := s.IssueComposer(org, place, person.Member)
				if generation == other || !s.Signal(org, place, person, generation, time.Now()) {
					t.Fatal("composers are not unique or issued signal refused")
				}
				handledAt := time.Now()
				if failed {
					// No Posted on failure; the 422 caller carries this exact value back.
					if !s.Signal(org, place, person, generation, handledAt) {
						t.Fatal("failed submission closed composer")
					}
				}
				s.Posted(org, place, person.Member, generation)
				before := s.Read(org, place, kernel.ID{})
				if len(before.Typists) != 0 || s.Signal(org, place, person, generation, handledAt) || !reflect.DeepEqual(before, s.Read(org, place, kernel.ID{})) {
					t.Fatal("pre-post signal revived typing after successful post")
				}
				for _, next := range []string{other, s.IssueComposer(org, place, person.Member)} {
					if !s.Signal(org, place, person, next, time.Now()) || len(s.Read(org, place, kernel.ID{}).Typists) != 1 {
						t.Fatal("other or newly rendered composer cannot type")
					}
					s.Stop(org, place, person.Member)
				}
				time.Sleep(ClosedRetention - time.Nanosecond)
				synctest.Wait()
				if s.Signal(org, place, person, generation, time.Now()) || closedCount(s) != 1 {
					t.Fatal("closed composer lost before retention boundary")
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if closedCount(s) != 0 || s.Signal(org, place, person, generation, handledAt) {
					t.Fatal("owner did not clean up or overdue handling revived typing")
				}
				// A request held outside the server starts handling only after cleanup.
				if !s.Signal(org, place, person, generation, time.Now()) {
					t.Fatal("documented long-open composer trade-off changed")
				}
				time.Sleep(Expiry)
				synctest.Wait()
				if len(s.Read(org, place, kernel.ID{}).Typists) != 0 {
					t.Fatal("post-window signal did not expire")
				}
			})
		})
	}
}

// A double submit posts twice with one generation: the second success restarts
// the retention, and the first post's timer must not end it early.
func TestComposerRepeatedPost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(realtime.NewHub())
		org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
		defer s.Open(org, person.Member, place.Channel)()
		generation := s.IssueComposer(org, place, person.Member)
		s.Posted(org, place, person.Member, generation)
		time.Sleep(ClosedRetention - time.Second)
		s.Posted(org, place, person.Member, generation)
		time.Sleep(2 * time.Second) // Past the first post's retention.
		synctest.Wait()
		if closedCount(s) != 1 || s.Signal(org, place, person, generation, time.Now()) {
			t.Fatal("the first post's timer ended the renewed closure")
		}
		time.Sleep(ClosedRetention - 2*time.Second - time.Nanosecond)
		synctest.Wait()
		if closedCount(s) != 1 || s.Signal(org, place, person, generation, time.Now()) {
			t.Fatal("closure lost before the second post's retention boundary")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if closedCount(s) != 0 {
			t.Fatal("renewed closure not removed at the second post's boundary")
		}
	})
}

// A replaced timer can fire just before the renewing post and then wait for
// the lock; synctest cannot hold that interleaving (mutex waits are not
// durable), so the test runs the stale callback after the renewal directly.
func TestComposerRepostAfterTimerFired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(realtime.NewHub())
		org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
		generation := s.IssueComposer(org, place, person.Member)
		s.Posted(org, place, person.Member, generation)
		s.mu.Lock()
		stale := s.closed[generation]
		s.mu.Unlock()
		s.Posted(org, place, person.Member, generation)
		s.reopen(generation, stale)
		if closedCount(s) != 1 {
			t.Fatal("a fired, replaced timer deleted the renewed closure")
		}
	})
}

func TestComposerRetention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(realtime.NewHub())
		org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
		for range 1000 {
			s.IssueComposer(org, place, person.Member)
		}
		if len(s.organizations) != 0 || len(s.streams) != 0 || closedCount(s) != 0 {
			t.Fatal("issuing composers stored state")
		}
		defer s.Open(org, person.Member, place.Channel)()
		unissued := New(realtime.NewHub()).IssueComposer(org, place, person.Member)
		if s.Signal(org, place, person, unissued, time.Now()) || len(s.organizations) != 0 || len(s.streams[streamKey{org, person.Member, place.Channel}].topics) != 0 {
			t.Fatal("unissued generation changed activity")
		}
		for range 3 {
			for range 100 {
				s.Posted(org, place, person.Member, s.IssueComposer(org, place, person.Member))
			}
			if closedCount(s) != 100 {
				t.Fatal("old posts accumulated")
			}
			time.Sleep(ClosedRetention)
			synctest.Wait()
			if closedCount(s) != 0 {
				t.Fatal("cleanup required a viewer read")
			}
		}
	})
}

func TestComposerScopes(t *testing.T) {
	for _, dimension := range []string{"organisation", "member", "channel", "topic"} {
		t.Run(dimension, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := New(realtime.NewHub())
				org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
				otherOrg, otherPlace, other := org, place, person
				switch dimension {
				case "organisation":
					otherOrg = kernel.ID{9}
				case "member":
					other.Member = kernel.ID{9}
				case "channel":
					otherPlace.Channel = kernel.ID{9}
				case "topic":
					otherPlace.Topic = kernel.ID{9}
				}
				defer s.Open(org, person.Member, place.Channel)()
				defer s.Open(otherOrg, other.Member, otherPlace.Channel)()
				generation := s.IssueComposer(org, place, person.Member)
				if s.Signal(otherOrg, otherPlace, other, generation, time.Now()) || len(s.organizations) != 0 {
					t.Fatal("foreign-scope generation admitted signal")
				}
				otherGeneration := s.IssueComposer(otherOrg, otherPlace, other.Member)
				s.Signal(org, place, person, generation, time.Now())
				s.Signal(otherOrg, otherPlace, other, otherGeneration, time.Now())
				s.Posted(otherOrg, otherPlace, other.Member, otherGeneration)
				got := s.Read(org, place, kernel.ID{})
				if len(got.Typists) != 1 || got.Typists[0].Member != person.Member {
					t.Fatal("post stopped activity in another scope")
				}
				streams := s.streams[streamKey{otherOrg, other.Member, otherPlace.Channel}]
				if streams.topics[otherPlace.Topic] != nil || s.Signal(otherOrg, otherPlace, other, otherGeneration, time.Now()) {
					t.Fatal("post left its scope active or its generation open")
				}
				if !s.Signal(org, place, person, generation, time.Now()) {
					t.Fatal("foreign post closed original composer")
				}
			})
		})
	}
}

func TestSignalDeadlineUnderLock(t *testing.T) {
	for _, elapsed := range []time.Duration{SignalDeadline, SignalDeadline + time.Nanosecond} {
		t.Run(elapsed.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				var s *State
				s = NewWithClock(realtime.NewHub(), func() time.Time {
					if s.mu.TryLock() {
						s.mu.Unlock()
						t.Fatal("acceptance clock read outside state lock")
					}
					return now
				})
				org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
				defer s.Open(org, person.Member, place.Channel)()
				generation := s.IssueComposer(org, place, person.Member)
				s.Signal(org, place, person, generation, now)
				before := s.Read(org, place, kernel.ID{})
				streams := s.streams[streamKey{org, person.Member, place.Channel}]
				timer, handledAt := streams.topics[place.Topic], now
				now = now.Add(elapsed)
				accepted := s.Signal(org, place, person, generation, handledAt)
				if accepted != (elapsed == SignalDeadline) || !reflect.DeepEqual(before, s.Read(org, place, kernel.ID{})) || closedCount(s) != 0 {
					t.Fatal("locked acceptance deadline or unchanged state violated")
				}
				if elapsed > SignalDeadline && streams.topics[place.Topic] != timer {
					t.Fatal("overdue signal refreshed expiry deadline")
				}
			})
		})
	}
}

func TestConcurrentPostAndSignal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(realtime.NewHub())
		org, place, person := kernel.ID{1}, Place{kernel.ID{2}, kernel.ID{3}}, Typist{Member: kernel.ID{4}}
		defer s.Open(org, person.Member, place.Channel)()
		generation, handledAt := s.IssueComposer(org, place, person.Member), time.Now()
		go s.Signal(org, place, person, generation, handledAt)
		go s.Posted(org, place, person.Member, generation)
		synctest.Wait()
		if got := s.Read(org, place, kernel.ID{}); len(got.Typists) != 0 || closedCount(s) != 1 {
			t.Fatal("concurrent signal survived post")
		}
	})
}

func closedCount(s *State) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.closed)
}
