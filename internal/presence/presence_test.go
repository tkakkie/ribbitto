package presence

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestState(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "last close", true: "reconnect"}[reconnect], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var s *State
				var raises atomic.Int64
				s = New(generationFunc(func(id kernel.ID, kind realtime.Interest, generation int64) {
					o := s.organizations[id]
					if kind != realtime.InterestPresence || o.generation != generation || o.members[kernel.ID{2}].entry.Online != (generation == 1) {
						t.Fatal("generation raised before publishing state")
					}
					raises.Add(1)
				}))
				organizationID, memberID := kernel.ID{1}, kernel.ID{2}
				check := func(online bool, generation int64) {
					t.Helper()
					got, err := s.Read(organizationID, []kernel.ID{memberID})
					if err != nil || got.Online[0] != online || got.Token.Generation != generation || got.Token.Process == "" {
						t.Fatalf("snapshot = %+v, %v; want online %t, generation %d", got, err, online, generation)
					}
					if got := raises.Load(); got != generation {
						t.Fatalf("%d raises; want %d", got, generation)
					}
				}
				advance := func(d time.Duration) { <-time.After(d); synctest.Wait() }
				first, last := s.Open(organizationID, memberID), s.Open(organizationID, memberID)
				check(true, 1)
				first()
				first() // Cleanup must not turn another tab offline.
				advance(30 * time.Second)
				check(true, 1)
				last()
				advance(30*time.Second - time.Nanosecond)
				check(true, 1)
				if reconnect {
					last = s.Open(organizationID, memberID)
					advance(time.Nanosecond)
					check(true, 1)
					last()
					advance(30*time.Second - time.Nanosecond)
					check(true, 1)
				}
				advance(time.Nanosecond)
				check(false, 2)
			})
		})
	}
}

func TestSnapshotScopeAndBound(t *testing.T) {
	s := New(realtime.NewHub())
	id := kernel.ID{2}
	s.Open(kernel.ID{1}, id)
	s.Open(kernel.ID{3}, kernel.ID{4})
	for _, tt := range []struct {
		organization kernel.ID
		online       bool
		otherOnline  bool
		generation   int64
	}{
		{kernel.ID{1}, true, false, 1},
		{kernel.ID{3}, false, true, 1}, // Same member: only the organisation differs.
	} {
		got, err := s.Read(tt.organization, []kernel.ID{id, {4}})
		if err != nil || got.Online[0] != tt.online || got.Online[1] != tt.otherOnline || got.Token.Generation != tt.generation || got.Token.Process != s.process {
			t.Fatalf("snapshot = %+v, %v; want %+v", got, err, tt)
		}
	}
	if _, err := s.Read(kernel.ID{1}, make([]kernel.ID, MaxSnapshotMembers)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(kernel.ID{1}, make([]kernel.ID, MaxSnapshotMembers+1)); err == nil {
		t.Fatal("unbounded snapshot accepted")
	}
}

type generationFunc func(kernel.ID, realtime.Interest, int64)

func (f generationFunc) RaiseGeneration(id kernel.ID, kind realtime.Interest, generation int64) {
	f(id, kind, generation)
}

func TestChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(realtime.NewHub())
		organizationID, a, b := kernel.ID{1}, kernel.ID{2}, kernel.ID{3}
		page, _ := s.Read(organizationID, nil)
		closeA := s.Open(organizationID, a)
		s.Open(organizationID, b)
		advance := func(d time.Duration) { <-time.After(d); synctest.Wait() }
		for range 101 { // Generations do not count towards the entry limit.
			closeA()
			advance(30 * time.Second)
			closeA = s.Open(organizationID, a)
		}
		got := s.After(organizationID, page.Token)
		if got.Reset || len(got.Entries) != 2 || got.Entries[0] != (Entry{b, true}) || got.Entries[1] != (Entry{a, true}) {
			t.Fatalf("coalesced last-change order = %+v", got)
		}
		// The same member differs only by organisation, with a valid process token.
		if other := s.After(kernel.ID{9}, page.Token); other.Reset || len(other.Entries) != 0 {
			t.Fatalf("other organisation = %+v", other)
		}
		closeA()
		advance(30 * time.Second)
		offline := s.After(organizationID, got.Token)
		if offline.Reset || len(offline.Entries) != 1 || offline.Entries[0] != (Entry{a, false}) {
			t.Fatalf("offline suffix = %+v", offline)
		}
		advance(OfflineRetention - time.Nanosecond)
		if kept := s.After(organizationID, got.Token); kept.Reset || len(kept.Entries) != 1 {
			t.Fatalf("expired early: %+v", kept)
		}
		advance(time.Nanosecond)
		if expired := s.After(organizationID, got.Token); !expired.Reset || expired.Boundary != offline.Token.Generation || len(expired.Entries) != 0 {
			t.Fatalf("discard boundary = %+v", expired)
		}
		if equal := s.After(organizationID, offline.Token); equal.Reset || len(equal.Entries) != 0 {
			t.Fatalf("boundary equality = %+v", equal)
		}
		s.Open(organizationID, a)()
		advance(30*time.Second + OfflineRetention)
		if later := s.After(organizationID, offline.Token); !later.Reset || later.Boundary <= offline.Token.Generation {
			t.Fatalf("boundary did not advance: %+v", later)
		}
	})
}
