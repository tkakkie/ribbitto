package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// The cache evaluates Done after registering a waiter. This gate avoids
// sleeps and keeps channels outside a synctest bubble for the lock test.
type registeredCacheContext struct {
	context.Context
	registered func()
}

func (c registeredCacheContext) Done() <-chan struct{} {
	c.registered()
	return c.Context.Done()
}

func TestCachedEventsCompletedLeavesIgnoreLock(t *testing.T) {
	for _, limit := range []int{1, 10} {
		log := &gatedLog{fakeLog: &fakeLog{events: []Event{posted(1, channelA)}}, started: make(chan struct{}), release: make(chan struct{})}
		c := NewCachedEvents(t.Context(), log, NewHub(), 8, time.Minute)
		read := func(ctx context.Context) <-chan error {
			out := make(chan error, 1)
			go func() {
				got, err := c.EventsAfter(ctx, orgA, 0, limit)
				if err == nil && (len(got) != 1 || got[0].Seq != 1) {
					t.Error("wrong batch")
				}
				out <- err
			}()
			return out
		}
		starter := read(t.Context())
		receive(t, log.started)
		registered := make(chan struct{})
		joiner := read(registeredCacheContext{t.Context(), sync.OnceFunc(func() { close(registered) })})
		receive(t, registered)
		key := eventsKey{organization: orgA, limit: limit}
		c.cache.mu.Lock()
		call := c.cache.loading[key]
		waiters := call.waiters
		c.cache.mu.Unlock()
		if waiters != 2 {
			t.Fatal("joiner not registered")
		}
		close(log.release)
		if a, b := receive(t, starter), receive(t, joiner); a != nil || b != nil {
			t.Fatalf("read errors: %v, %v", a, b)
		}
		receive(t, call.joined.done)
		if c.cache.Waiting(key) != 0 {
			t.Fatal("completed load still registered")
		}
		// A real completed load must allow cleanup while its mutex is held.
		c.cache.mu.Lock()
		done := make(chan struct{})
		go func() { c.cache.leave(key, call, true); close(done) }()
		func() { defer c.cache.mu.Unlock(); receive(t, done) }()
	}
}

func TestCachedEventsPendingLeavesStillCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := NewCachedEvents(t.Context(), &fakeLog{}, NewHub(), 8, time.Minute)
		gate := make(chan struct{})
		var pending context.Context
		c.cache.beforeSecondLoad = func(ctx context.Context) { pending = ctx; <-gate }
		loadGate := make(chan struct{})
		load := func(ctx context.Context) ([]Event, error) { <-loadGate; return nil, nil }
		key := eventsKey{organization: orgA, limit: 10}
		starter := make(chan error, 1)
		go func() { _, err := c.cache.Get(t.Context(), key, load); starter <- err }()
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		joiner := make(chan error, 1)
		go func() { _, err := c.cache.Get(ctx, key, load); joiner <- err }()
		synctest.Wait()
		call := c.cache.loading[key]
		close(loadGate)
		synctest.Wait() // second read is set up; starter still owns its wait
		cancel()
		if err := <-joiner; !errors.Is(err, context.Canceled) {
			t.Errorf("joiner = %v, want cancellation", err)
		}
		if call.joiners != 0 || call.waiters != 1 || !errors.Is(context.Cause(pending), context.Canceled) {
			t.Error("pending second-load cancellation lost waiter accounting")
		}
		close(gate)
		if err := <-starter; err != nil {
			t.Errorf("starter = %v", err)
		}
		synctest.Wait()
		if c.cache.Len() != 0 || len(c.cache.slots) != 0 {
			t.Error("cancelled second read retained a value or loader")
		}
	})
}
