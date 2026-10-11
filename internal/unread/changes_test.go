package unread

import (
	"sync"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestChangesLifetime(t *testing.T) {
	var c Changes
	org, member := kernel.ID{1}, kernel.ID{2}
	for range 100 {
		c.ReadChanged(org, member)
		if len(c.entries) != 0 {
			t.Fatal("inactive publication retained an entry")
		}
		first, second := c.Watch(org, member), c.Watch(org, member)
		first.Release()
		first.Release()
		if len(c.entries) != 1 || second.entry.watches != 1 {
			t.Fatal("double release removed a live subscription")
		}
		second.Release()
		if len(c.entries) != 0 {
			t.Fatal("last release retained an entry")
		}
		reopened := c.Watch(org, member)
		second.Release()
		if generation, _ := reopened.Snapshot(); generation != 0 || reopened.entry == second.entry || reopened.entry.watches != 1 {
			t.Fatal("old handle affected a reopened subscription lifetime")
		}
		reopened.Release()
		if len(c.entries) != 0 {
			t.Fatal("subscription cycle retained an entry")
		}
	}
}

func TestChangesMemberIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c Changes
		org, member := kernel.ID{1}, kernel.ID{2}
		watches := []*ChangeWatch{c.Watch(org, member), c.Watch(org, member), c.Watch(org, kernel.ID{3}), c.Watch(kernel.ID{4}, member)}
		stop := make(chan struct{})
		defer close(stop)
		woke := make([]chan struct{}, len(watches))
		for i, watch := range watches {
			defer watch.Release()
			generation, changed := watch.Snapshot()
			if generation != 0 {
				t.Fatal("new lifetime has a nonzero generation")
			}
			woke[i] = make(chan struct{})
			go func() {
				select {
				case <-changed:
					close(woke[i])
				case <-stop:
				}
			}()
		}
		synctest.Wait() // All waits are blocked before publication.
		c.ReadChanged(org, member)
		synctest.Wait()
		for i, watch := range watches {
			generation, changed := watch.Snapshot()
			want := uint64(0)
			if i < 2 {
				want = 1
			}
			if generation != want || changeClosed(woke[i]) != (i < 2) || changeClosed(changed) {
				t.Fatalf("watch %d: generation=%d, woke=%v; want generation=%d with a fresh open channel", i, generation, changeClosed(woke[i]), want)
			}
		}
	})
}

func TestChangesSubscribePublishOrder(t *testing.T) {
	for _, publishFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "subscribe first", true: "publish first"}[publishFirst], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var c Changes
				org, member := kernel.ID{1}, kernel.ID{2}
				var watch *ChangeWatch
				firstDone := make(chan struct{})
				go func() {
					if publishFirst {
						c.ReadChanged(org, member)
					} else {
						watch = c.Watch(org, member)
					}
					close(firstDone)
				}()
				<-firstDone // Force both serialization orders without timing assumptions.
				if publishFirst {
					watch = c.Watch(org, member)
				} else {
					c.ReadChanged(org, member)
				}
				defer watch.Release()
				generation, changed := watch.Snapshot()
				want := uint64(1)
				if publishFirst {
					want = 0 // The caller's initial persisted snapshot covers this commit.
				}
				if generation != want {
					t.Fatalf("generation=%d, want %d", generation, want)
				}
				c.ReadChanged(org, member) // Publication between capture and wait.
				<-changed
				next, fresh := watch.Snapshot()
				if next != want+1 || fresh == changed || changeClosed(fresh) {
					t.Fatal("publication lost between snapshot and wait")
				}
			})
		})
	}
}

// The contract's order: capture the pair, read persisted state, wait on the
// captured channel. A commit published during the read closes it; a pair
// captured only after the read would look current and miss that commit.
func TestChangesPublicationDuringRead(t *testing.T) {
	var c Changes
	org, member := kernel.ID{1}, kernel.ID{2}
	watch := c.Watch(org, member)
	defer watch.Release()
	covered, changed := watch.Snapshot()
	read := func() { c.ReadChanged(org, member) } // Another tab commits mid-read.
	read()
	if !changeClosed(changed) {
		t.Fatal("publication during the read did not wake the pair captured before it")
	}
	late, lateChanged := watch.Snapshot()
	if late != covered+1 || changeClosed(lateChanged) {
		t.Fatalf("late capture: generation=%d closed=%t, want %d and open (the trap)", late, changeClosed(lateChanged), covered+1)
	}
}

func TestChangesConcurrentSubscribePublish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c Changes
		org, member := kernel.ID{1}, kernel.ID{2}
		first := c.Watch(org, member)
		defer first.Release()
		_, changed := first.Snapshot()
		start := make(chan struct{})
		var done sync.WaitGroup
		var second *ChangeWatch
		var capturedGeneration uint64
		var captured <-chan struct{}
		for i := range 34 {
			done.Go(func() {
				<-start
				switch i {
				case 0:
					second = c.Watch(org, member)
				case 1:
					capturedGeneration, captured = first.Snapshot()
				default:
					c.ReadChanged(org, member)
				}
			})
		}
		synctest.Wait()
		close(start)
		done.Wait()
		defer second.Release()
		generation, fresh := second.Snapshot()
		firstGeneration, firstChannel := first.Snapshot()
		if generation != 32 || firstGeneration != generation || firstChannel != fresh || !changeClosed(changed) || changeClosed(fresh) {
			t.Fatal("concurrent publications or subscription lost a generation/channel pair")
		}
		if capturedGeneration > generation || changeClosed(captured) != (capturedGeneration < generation) || (capturedGeneration == generation && captured != fresh) {
			t.Fatal("snapshot racing publication captured a mismatched generation/channel pair")
		}
	})
}

func TestChangesConcurrentReleasePublish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var c Changes
		org, member := kernel.ID{1}, kernel.ID{2}
		for range 100 {
			watch := c.Watch(org, member)
			start := make(chan struct{})
			var done sync.WaitGroup
			for i := range 4 {
				done.Go(func() {
					<-start
					if i%2 == 0 {
						watch.Release()
					} else {
						c.ReadChanged(org, member)
					}
				})
			}
			synctest.Wait()
			close(start)
			done.Wait()
			if len(c.entries) != 0 || watch.entry.watches != 0 {
				t.Fatal("publication racing final/double release leaked an entry or subscription")
			}
		}
	})
}

func TestChangesFinalReleasePublishOrder(t *testing.T) {
	for _, publishFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "release first", true: "publish first"}[publishFirst], func(t *testing.T) {
			var c Changes
			org, member := kernel.ID{1}, kernel.ID{2}
			watch := c.Watch(org, member)
			_, changed := watch.Snapshot()
			firstDone := make(chan struct{})
			go func() {
				if publishFirst {
					c.ReadChanged(org, member)
				} else {
					watch.Release()
				}
				close(firstDone)
			}()
			<-firstDone
			if publishFirst {
				watch.Release()
			} else {
				c.ReadChanged(org, member)
			}
			if len(c.entries) != 0 || changeClosed(changed) != publishFirst {
				t.Fatal("final release/publication order retained state or lost a live wake")
			}
		})
	}
}

func changeClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
