package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// delayedCacheParent holds AfterFunc propagation while its own cause is
// already visible. A distinct Done channel prevents the native-parent
// shortcut while Value still forwards the cancellation cause.
type delayedCacheParent struct {
	context.Context
	release <-chan struct{}
	done    <-chan struct{}
}

func (p delayedCacheParent) Done() <-chan struct{} { return p.done }

func delayCacheParent(parent context.Context, release <-chan struct{}) delayedCacheParent {
	done := make(chan struct{})
	context.AfterFunc(parent, func() { close(done) })
	return delayedCacheParent{parent, release, done}
}
func (p delayedCacheParent) AfterFunc(f func()) func() bool {
	return context.AfterFunc(p.Context, func() { <-p.release; f() })
}

func TestCacheParentCancellationAfterLoad(t *testing.T) {
	for _, kept := range []bool{false, true} {
		for _, delayed := range []bool{false, true} {
			t.Run(fmt.Sprintf("kept=%t/delayed=%t", kept, delayed), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					parent, cancel := context.WithCancelCause(t.Context())
					defer cancel(context.Canceled)
					propagation := make(chan struct{})
					propagate := sync.OnceFunc(func() { close(propagation) })
					defer propagate()
					lifetime := context.Context(parent)
					if delayed {
						lifetime = delayCacheParent(parent, propagation)
					}
					c := NewCache[string, int](lifetime, 8, 1, time.Minute, time.Second, func(string, int) bool { return kept }, time.Now)
					gate, decision := make(chan struct{}), make(chan struct{})
					decide := sync.OnceFunc(func() { close(decision) })
					defer decide()
					accepted := make(chan context.Context, 1)
					c.afterLoad = func(ctx context.Context) { accepted <- ctx; <-decision }
					var loads atomic.Int32
					load := func(context.Context) (int, error) {
						loads.Add(1)
						<-gate
						return 42, nil
					}
					type answer struct {
						value int
						err   error
					}
					get := func() <-chan answer {
						out := make(chan answer, 1)
						go func() { v, err := c.Get(t.Context(), "k", load); out <- answer{v, err} }()
						return out
					}
					starter := get()
					synctest.Wait()
					joiner := get()
					synctest.Wait()
					if c.Waiting("k") != 2 {
						t.Fatal("joiner did not join the first load")
					}
					close(gate)
					loadCtx := <-accepted // run accepted a successful first result
					cause := errors.New("cache shutdown")
					cancel(cause)
					synctest.Wait()
					if delayed {
						if loadCtx.Err() != nil {
							t.Fatal("propagation was not delayed")
						}
					} else if !errors.Is(context.Cause(loadCtx), cause) {
						t.Fatal("parent cancellation did not reach the load")
					}
					decide()
					for name, out := range map[string]<-chan answer{"starter": starter, "joiner": joiner} {
						got := <-out
						if got.value != 0 || !errors.Is(got.err, cause) {
							t.Errorf("%s = %d, %v; want zero and parent cause", name, got.value, got.err)
						}
					}
					propagate()
					synctest.Wait()
					if c.Len() != 0 || c.Waiting("k") != 0 || len(c.slots) != 0 || loads.Load() != 1 {
						t.Errorf("entries=%d waiters=%d slots=%d loads=%d; want 0, 0, 0, 1", c.Len(), c.Waiting("k"), len(c.slots), loads.Load())
					}
				})
			})
		}
	}
}

func TestCachedEventsJoinerAtShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		log := &gatedLog{fakeLog: &fakeLog{events: []Event{posted(1, channelA)}}, started: make(chan struct{}), release: make(chan struct{})}
		events := NewCachedEvents(parent, log, NewHub(), 8, time.Minute)
		decision := make(chan struct{})
		decide := sync.OnceFunc(func() { close(decision) })
		defer decide()
		accepted := make(chan context.Context, 1)
		events.cache.afterLoad = func(ctx context.Context) { accepted <- ctx; <-decision }
		type answer struct {
			events []Event
			err    error
		}
		read := func() <-chan answer {
			out := make(chan answer, 1)
			go func() { got, err := events.EventsAfter(t.Context(), orgA, 0, 10); out <- answer{got, err} }()
			return out
		}
		starter := read()
		<-log.started // its short batch has only event 1
		log.append(posted(2, channelA))
		joiner := read()
		synctest.Wait()
		if events.cache.Waiting(eventsKey{organization: orgA, limit: 10}) != 2 {
			t.Fatal("reader did not join")
		}
		close(log.release)
		loadCtx := <-accepted
		cancel(ErrShutdown)
		<-loadCtx.Done()
		decide()
		for name, out := range map[string]<-chan answer{"starter": starter, "joiner": joiner} {
			got := <-out
			if len(got.events) != 0 || !errors.Is(got.err, ErrShutdown) {
				t.Errorf("%s = %v, %v; want no batch and shutdown cause", name, got.events, got.err)
			}
		}
		synctest.Wait()
		if log.calls.Load() != 1 || events.cache.Len() != 0 || len(events.cache.slots) != 0 {
			t.Fatal("shutdown retained a batch or left a loader")
		}
	})
}

func TestCacheSecondResultAtShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		propagation := make(chan struct{})
		defer close(propagation)
		c := NewCache[string, int](delayCacheParent(parent, propagation), 8, 1, time.Minute, time.Second, func(string, int) bool { return false }, time.Now)
		gate := make(chan struct{})
		var loads atomic.Int32
		cause := errors.New("second load shutdown")
		load := func(context.Context) (int, error) {
			if loads.Add(1) == 1 {
				<-gate
				return 42, nil
			}
			cancel(cause)
			return 43, nil
		}
		type answer struct {
			value int
			err   error
		}
		get := func() <-chan answer {
			out := make(chan answer, 1)
			go func() { v, err := c.Get(t.Context(), "k", load); out <- answer{v, err} }()
			return out
		}
		starter := get()
		synctest.Wait()
		joiner := get()
		synctest.Wait()
		if c.Waiting("k") != 2 {
			t.Fatal("joiner did not join")
		}
		close(gate)
		if got := <-starter; got.value != 42 || got.err != nil {
			t.Errorf("starter = %+v; want first result", got)
		}
		if got := <-joiner; got.value != 0 || !errors.Is(got.err, cause) {
			t.Errorf("joiner = %+v; want zero and parent cause", got)
		}
		synctest.Wait()
		if c.Len() != 0 || len(c.slots) != 0 || loads.Load() != 2 {
			t.Fatal("second load retained a result or left a loader")
		}
	})
}

func TestCacheCancellationAfterDecisionCheck(t *testing.T) {
	for _, fullSlots := range []bool{false, true} {
		t.Run(fmt.Sprintf("fullSlots=%t", fullSlots), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				propagation := make(chan struct{})
				propagate := sync.OnceFunc(func() { close(propagation) })
				defer propagate()
				lifetime := context.Context(parent)
				if fullSlots {
					lifetime = delayCacheParent(parent, propagation)
				}
				c := NewCache[string, int](lifetime, 8, 1, time.Minute, time.Second, func(string, int) bool { return false }, time.Now)
				cause := errors.New("shutdown after decision check")
				c.afterCancellationCheck = func(ctx context.Context) {
					if fullSlots {
						// Simulate another loader holding the only slot through shutdown.
						c.slots <- struct{}{}
					}
					cancel(cause)
					if !fullSlots {
						// AfterFunc does not need c.mu, which is held by this hook.
						<-ctx.Done()
					}
				}
				gate := make(chan struct{})
				var loads atomic.Int32
				load := func(context.Context) (int, error) {
					n := loads.Add(1)
					if n == 1 {
						<-gate
					}
					return int(n), nil
				}
				type answer struct {
					value int
					err   error
				}
				get := func() <-chan answer {
					out := make(chan answer, 1)
					go func() { v, err := c.Get(t.Context(), "k", load); out <- answer{v, err} }()
					return out
				}
				starter := get()
				synctest.Wait()
				joiner := get()
				synctest.Wait()
				if c.Waiting("k") != 2 {
					t.Fatal("joiner did not join the first load")
				}
				close(gate)
				synctest.Wait()
				if got := <-starter; got.value != 1 || got.err != nil {
					t.Errorf("starter = %+v; want first result", got)
				}
				select {
				case got := <-joiner:
					if got.value != 0 || !errors.Is(got.err, cause) {
						t.Errorf("joiner = %+v; want zero and parent cause", got)
					}
				default:
					t.Error("cancelled second load waited for a slot or cancellation propagation")
				}
				propagate()
				if fullSlots {
					<-c.slots
				}
				synctest.Wait()
				if c.Len() != 0 || c.Waiting("k") != 0 || len(c.slots) != 0 || loads.Load() != 1 {
					t.Errorf("entries=%d waiters=%d slots=%d loads=%d; want 0, 0, 0, 1", c.Len(), c.Waiting("k"), len(c.slots), loads.Load())
				}
			})
		})
	}
}
