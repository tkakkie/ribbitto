package realtime

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Counting Done evaluations exposes re-entering a select without depending on
// scheduler timing. Cause and detached loader contexts retain the native parent.
type countedWaitContext struct {
	context.Context
	waits atomic.Int64
}

func (c *countedWaitContext) Done() <-chan struct{} {
	c.waits.Add(1)
	return c.Context.Done()
}

func TestCacheJoinerWaitsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := NewCache[string, int](t.Context(), 1, 1, 0, time.Minute, func(string, int) bool { return false }, time.Now)
		first, second := make(chan struct{}), make(chan struct{})
		started := make(chan int, 2)
		var loads atomic.Int64
		load := func(context.Context) (int, error) {
			n := int(loads.Add(1))
			started <- n
			if n == 1 {
				<-first
			} else {
				<-second
			}
			return n, nil
		}
		starter := make(chan int, 1)
		go func() { v, _ := c.Get(t.Context(), "k", load); starter <- v }()
		<-started
		ctx := &countedWaitContext{Context: t.Context()}
		joiner := make(chan int, 1)
		go func() { v, _ := c.Get(ctx, "k", load); joiner <- v }()
		synctest.Wait()
		if c.Waiting("k") != 2 {
			t.Fatal("joiner not registered")
		}
		close(first)
		<-started // the second read is fresh, and the joiner still waits
		synctest.Wait()
		if ctx.waits.Load() != 1 {
			t.Errorf("joiner entered %d waits, want one", ctx.waits.Load())
		}
		close(second)
		if a, b := <-starter, <-joiner; a != 1 || b != 2 {
			t.Fatalf("starter=%d joiner=%d, want 1 and 2", a, b)
		}
		synctest.Wait()
		if c.Waiting("k") != 0 || len(c.slots) != 0 || c.Len() != 0 {
			t.Fatal("joiner result left waiters, loader slots or retained values")
		}
	})
}
