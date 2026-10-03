package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// fakeClock is a settable time for TTL tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// waitForWaiters blocks until n callers have joined key's load.
func waitForWaiters[K comparable, V any](t *testing.T, c *Cache[K, V], key K, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); c.Waiting(key) < n; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d callers joined the load", c.Waiting(key), n)
		}
	}
}

func TestCacheCoalescesConcurrentMisses(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, DefaultCacheLoads, time.Minute, time.Second, nil, time.Now)
	release := make(chan struct{})
	var loads atomic.Int32
	load := func(context.Context) (int, error) {
		loads.Add(1)
		<-release
		return 42, nil
	}
	const callers = 50
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if v, err := c.Get(t.Context(), "k", load); err != nil || v != 42 {
				t.Errorf("Get = %d, %v", v, err)
			}
		})
	}
	waitForWaiters(t, c, "k", callers)
	close(release)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads for %d concurrent misses, want 1", n, callers)
	}
}

func TestCacheCapacityAndTTL(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := NewCache[string, string](t.Context(), 2, DefaultCacheLoads, time.Minute, time.Second, nil, clock.Now)
	loads := map[string]int{}
	get := func(key string) {
		t.Helper()
		if _, err := c.Get(t.Context(), key, func(context.Context) (string, error) { loads[key]++; return key, nil }); err != nil {
			t.Fatal(err)
		}
	}
	get("a")
	get("b")
	get("a") // hit; b is now the least recently used
	get("c") // evicts b
	if c.Len() != 2 || loads["a"] != 1 {
		t.Fatalf("len %d, loads %v", c.Len(), loads)
	}
	get("b")
	if loads["b"] != 2 {
		t.Fatalf("b was not evicted: loads %v", loads)
	}
	clock.advance(time.Minute)
	get("b")
	if loads["b"] != 3 {
		t.Fatalf("an expired entry was served: loads %v", loads)
	}
}

// A failed load reaches every caller that joined it and is not stored: the
// next call loads again.
func TestCacheDoesNotStoreErrors(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, DefaultCacheLoads, time.Minute, time.Second, nil, time.Now)
	failure := errors.New("database unavailable")
	release := make(chan struct{})
	var loads atomic.Int32
	var wg sync.WaitGroup
	const callers = 10
	for range callers {
		wg.Go(func() {
			_, err := c.Get(t.Context(), "k", func(context.Context) (int, error) {
				loads.Add(1)
				<-release
				return 0, failure
			})
			if !errors.Is(err, failure) {
				t.Errorf("Get = %v, want %v", err, failure)
			}
		})
	}
	waitForWaiters(t, c, "k", callers)
	close(release)
	wg.Wait()
	v, err := c.Get(t.Context(), "k", func(context.Context) (int, error) { loads.Add(1); return 7, nil })
	if err != nil || v != 7 || loads.Load() != 2 {
		t.Fatalf("retry = %d, %v after %d loads; want 7, nil, 2", v, err, loads.Load())
	}
}

// A caller that goes away while waiting gets its cause; the load, started by
// that very caller, carries on for the others and is not cancelled with it.
func TestCacheWaiterCancellationLeavesTheLoad(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, DefaultCacheLoads, time.Minute, time.Second, nil, time.Now)
	release := make(chan struct{})
	loadErr := make(chan error, 1)
	load := func(ctx context.Context) (int, error) {
		<-release
		loadErr <- ctx.Err()
		return 1, nil
	}
	cause := errors.New("stream closed")
	gone, cancel := context.WithCancelCause(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := c.Get(gone, "k", load)
		first <- err
	}()
	waitForWaiters(t, c, "k", 1) // the first caller started the load
	second := make(chan int, 1)
	go func() {
		v, _ := c.Get(t.Context(), "k", load)
		second <- v
	}()
	waitForWaiters(t, c, "k", 2)
	cancel(cause)
	if err := <-first; !errors.Is(err, cause) {
		t.Fatalf("cancelled caller got %v, want %v", err, cause)
	}
	close(release)
	if v := <-second; v != 1 {
		t.Fatalf("other caller got %d, want 1", v)
	}
	if err := <-loadErr; err != nil {
		t.Fatalf("the load's context ended with its first caller: %v", err)
	}
}

// A load that outlives its timeout releases every waiter with the timeout,
// even when the loader ignores its context, and the key can be loaded again
// while that loader is still running.
func TestCacheLoadTimeoutReleasesWaiters(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, DefaultCacheLoads, time.Minute, 300*time.Millisecond, nil, time.Now)
	release := make(chan struct{})
	defer close(release)
	var loads atomic.Int32
	ignoresContext := func(context.Context) (int, error) {
		loads.Add(1)
		<-release
		return 1, nil
	}
	const callers = 5
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			if _, err := c.Get(t.Context(), "k", ignoresContext); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Get = %v, want the load's timeout", err)
			}
		})
	}
	waitForWaiters(t, c, "k", callers)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads timed out for %d callers, want 1", n, callers)
	}
	if v, err := c.Get(t.Context(), "k", func(context.Context) (int, error) { return 3, nil }); err != nil || v != 3 {
		t.Fatalf("retry after a timeout = %d, %v", v, err)
	}
}

// A value keep rejects is not stored; a value it accepts is served until
// it expires.
func TestCacheKeepDecidesWhatIsStored(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, DefaultCacheLoads, time.Minute, time.Second, func(_ string, v int) bool { return v > 0 }, time.Now)
	var loads atomic.Int32
	get := func(v int) int {
		got, err := c.Get(t.Context(), "k", func(context.Context) (int, error) { loads.Add(1); return v, nil })
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if get(0) != 0 || get(0) != 0 || loads.Load() != 2 {
		t.Fatalf("a rejected value was stored: %d loads", loads.Load())
	}
	if get(5) != 5 || get(9) != 5 || loads.Load() != 3 {
		t.Fatalf("a kept value was not served: %d loads", loads.Load())
	}
}

// receive returns the next value from ch, failing the test after five
// seconds instead of hanging.
func receive[V any](t *testing.T, ch <-chan V) V {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting")
		panic("unreachable")
	}
}

// When keep rejects a value, the caller that started the load gets it; the
// callers that joined get a second load started after the first finished,
// and a caller arriving during that second load starts its own.
func TestCacheJoinersOfAnUnkeptValueLoadAgain(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, DefaultCacheLoads, time.Minute, time.Second, func(string, int) bool { return false }, time.Now)
	var loads atomic.Int32
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	opened := make([]bool, len(gates))
	open := func(i int) {
		if !opened[i] {
			opened[i] = true
			close(gates[i])
		}
	}
	// A regression fails here instead of hanging: every gate opens at the end.
	t.Cleanup(func() {
		for i := range gates {
			open(i)
		}
	})
	started := make(chan int, len(gates))
	load := func(context.Context) (int, error) {
		n := int(loads.Add(1))
		started <- n
		<-gates[n-1]
		return n, nil
	}
	get := func() chan int {
		out := make(chan int, 1)
		go func() {
			v, err := c.Get(t.Context(), "k", load)
			if err != nil {
				t.Error(err)
			}
			out <- v
		}()
		return out
	}
	starter := get()
	if n := receive(t, started); n != 1 {
		t.Fatalf("load %d started, want 1", n)
	}
	joiner := get()
	waitForWaiters(t, c, "k", 2)
	open(0)
	if v := receive(t, starter); v != 1 {
		t.Fatalf("starter got %d, want load 1", v)
	}
	if n := receive(t, started); n != 2 { // the joiners' second load runs
		t.Fatalf("load %d started, want 2", n)
	}
	late := get() // arrives while load 2 runs: it must not join it
	if n := receive(t, started); n != 3 {
		t.Fatalf("load %d started, want 3 for the late caller", n)
	}
	open(1)
	open(2)
	if v := receive(t, joiner); v != 2 {
		t.Fatalf("joiner got %d, want load 2", v)
	}
	if v := receive(t, late); v != 3 {
		t.Fatalf("late caller got %d, want its own load 3", v)
	}
}

// A short or empty batch says only what the log held when it was read, so it
// is never served to a later read, even while the hub stays at the same
// level (an event committed before its Raise must still be found). A full
// batch never changes and is served from the cache.
func TestCachedEventsStoreOnlyFullBatches(t *testing.T) {
	hub := NewHub()
	hub.Raise(orgA, 1)
	log := &fakeLog{events: []Event{posted(1, channelA)}}
	events := NewCachedEvents(t.Context(), log, hub, 64, time.Minute)
	read := func(after int64, limit int) []Event {
		t.Helper()
		got, err := events.EventsAfter(t.Context(), orgA, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(1, 2); len(got) != 0 {
		t.Fatalf("read = %v", got)
	}
	log.append(posted(2, channelA)) // committed; the hub is not raised
	if got := read(1, 2); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("after an unannounced commit after an empty batch: %v, want event 2", got)
	}
	log.append(posted(3, channelA)) // still no Raise; the last batch was short
	if got := read(1, 2); len(got) != 2 || got[1].Seq != 3 {
		t.Fatalf("after an unannounced commit after a short batch: %v, want events 2 and 3", got)
	}
	calls := log.calls
	if got := read(1, 2); len(got) != 2 || log.calls != calls+1 {
		t.Fatalf("full batch: %v after %d more reads, want cached rows and one boundary check", got, log.calls-calls)
	}
	// A limit of 0 is never full, though its result is as long as the limit.
	calls = log.calls
	read(1, 0)
	read(1, 0)
	if log.calls != calls+2 {
		t.Fatalf("%d reads for two zero-limit reads, want 2: an empty result was stored", log.calls-calls)
	}
	calls = log.calls
	// A new level is a new key, so even a full batch is read again after a
	// Raise; the level is in the key for the batches that are not full.
	hub.Raise(orgA, 3)
	if read(1, 2); log.calls != calls+2 {
		t.Fatalf("%d reads after a Raise, want 2", log.calls-calls)
	}
}

func TestCachedEventsKeepOrganisationsApart(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{events: []Event{
		posted(1, channelA), posted(2, channelA),
		{OrganizationID: orgB, Seq: 1, Kind: kindPosted, ChannelID: channelB},
		{OrganizationID: orgB, Seq: 2, Kind: kindPosted, ChannelID: channelB},
	}}
	events := NewCachedEvents(t.Context(), log, hub, 64, time.Minute)
	a, err := events.EventsAfter(t.Context(), orgA, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, err := events.EventsAfter(t.Context(), orgB, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 || len(b) != 2 || a[0].OrganizationID != orgA || b[0].OrganizationID != orgB || log.calls != 4 {
		t.Fatalf("orgA %v, orgB %v after %d reads; want each from its own read", a, b, log.calls)
	}
}

// blockedLog holds every read until released, then fails it with err.
type blockedLog struct {
	release chan struct{}
	calls   atomic.Int32
	err     error
}

func (b *blockedLog) EventsAfter(context.Context, kernel.ID, int64, int) ([]Event, error) {
	b.calls.Add(1)
	<-b.release
	return nil, b.err
}

func TestCachedEventsShareAFailureThenRetry(t *testing.T) {
	hub := NewHub()
	failure := errors.New("database unavailable")
	log := &blockedLog{release: make(chan struct{}), err: failure}
	events := NewCachedEvents(t.Context(), log, hub, 64, time.Minute)
	var wg sync.WaitGroup
	const callers = 10
	for range callers {
		wg.Go(func() {
			if _, err := events.EventsAfter(t.Context(), orgA, 0, 5); !errors.Is(err, failure) {
				t.Errorf("EventsAfter = %v, want %v", err, failure)
			}
		})
	}
	waitForWaiters(t, events.cache, eventsKey{organization: orgA, limit: 5}, callers)
	close(log.release)
	wg.Wait()
	log.err = nil
	if _, err := events.EventsAfter(t.Context(), orgA, 0, 5); err != nil || log.calls.Load() != 2 {
		t.Fatalf("retry = %v after %d reads, want one fresh read", err, log.calls.Load())
	}
}

// A stream on a cold hub (level 0) must still drain every batch through the
// cache: the cached read at level 0 is the database's state, not an empty
// guess.
func TestCachedEventsLetAColdStreamDrain(t *testing.T) {
	log := &fakeLog{}
	for seq := int64(1); seq <= 7; seq++ {
		log.events = append(log.events, posted(seq, channelA))
	}
	hub := NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	send := newRecorder()
	done := runAsync(ctx, Stream{Hub: hub, Events: NewCachedEvents(t.Context(), log, hub, 64, time.Minute), Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), BatchSize: 2}, 0, send)
	send.waitFor(t, 1, 2, 3, 4, 5, 6, 7)
	cancel()
	<-done
}

// countingLog counts the reads that start after seq 0 and find an event.
type countingLog struct {
	EventReader
	mu        sync.Mutex
	fromStart int
}

func (c *countingLog) EventsAfter(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	events, err := c.EventReader.EventsAfter(ctx, org, after, limit)
	if after == 0 && len(events) > 0 {
		c.mu.Lock()
		c.fromStart++
		c.mu.Unlock()
	}
	return events, err
}

// answeredReads counts the reads the cache has answered, so a test knows
// when every stream has had its first answer and waits on the hub.
type answeredReads struct {
	EventReader
	answered atomic.Int32
}

func (a *answeredReads) EventsAfter(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	events, err := a.EventReader.EventsAfter(ctx, org, after, limit)
	a.answered.Add(1)
	return events, err
}

// Many streams at the same cursor share one read of a full batch. Every
// stream first reads (nothing) and waits on the hub; only then is the event
// committed and raised, so every stream reads it at the new level, where
// the first read finds a full batch, which is stored and shared: exactly one
// read finds the event, for fifty streams.
func TestCachedEventsShareReadsBetweenStreams(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{}
	counted := &countingLog{EventReader: log}
	events := &answeredReads{EventReader: NewCachedEvents(t.Context(), counted, hub, 64, time.Minute)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const streams = 50
	senders := make([]*recorder, streams)
	for i := range senders {
		senders[i] = newRecorder()
		// A batch of one is full as soon as it holds the event.
		runAsync(ctx, Stream{Hub: hub, Events: events, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), BatchSize: 1}, 0, senders[i])
	}
	// After its empty first read a stream waits on the hub and reads nothing
	// more until a Raise.
	for deadline := time.Now().Add(5 * time.Second); events.answered.Load() < streams; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d streams had their first read", events.answered.Load(), streams)
		}
	}
	log.append(posted(1, channelA))
	hub.Raise(orgA, 1)
	for _, s := range senders {
		s.waitFor(t, 1)
	}
	counted.mu.Lock()
	defer counted.mu.Unlock()
	if counted.fromStart != 1 {
		t.Fatalf("%d reads found the event for %d streams, want 1", counted.fromStart, streams)
	}
}

// gatedLog takes its snapshot of the log when a read starts and, for the
// first read only, holds the answer until released, like a query whose
// snapshot predates a commit that lands while it runs.
type gatedLog struct {
	*fakeLog
	started, release chan struct{}
	calls            atomic.Int32
}

func (g *gatedLog) EventsAfter(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	events, err := g.fakeLog.EventsAfter(ctx, org, after, limit)
	if g.calls.Add(1) == 1 {
		close(g.started)
		<-g.release
	}
	return events, err
}

// A read that joins one already in flight must not get that read's older
// snapshot when the result is not stored: an event committed after the
// first read started, with no Raise yet, would be hidden from a read that
// started after the commit.
func TestCachedEventsJoinerSeesCommitsBeforeItJoined(t *testing.T) {
	hub := NewHub()
	hub.Raise(orgA, 1)
	log := &gatedLog{fakeLog: &fakeLog{events: []Event{posted(1, channelA)}}, started: make(chan struct{}), release: make(chan struct{})}
	released := false
	release := func() {
		if !released {
			released = true
			close(log.release)
		}
	}
	t.Cleanup(release) // a regression fails instead of hanging
	events := NewCachedEvents(t.Context(), log, hub, 64, time.Minute)
	read := func() chan []Event {
		out := make(chan []Event, 1)
		go func() {
			got, err := events.EventsAfter(t.Context(), orgA, 1, 10)
			if err != nil {
				t.Error(err)
			}
			out <- got
		}()
		return out
	}
	first := read()
	receive(t, log.started)         // the first read has its snapshot: nothing after 1
	log.append(posted(2, channelA)) // committed; the hub is not raised
	second := read()
	waitForWaiters(t, events.cache, eventsKey{organization: orgA, after: 1, level: 1, limit: 10}, 2)
	release()
	if got := receive(t, first); len(got) != 0 {
		t.Fatalf("first read = %v, want its own snapshot (empty)", got)
	}
	if got := receive(t, second); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("second read = %v, want event 2, committed before it started", got)
	}
}

func TestCacheLoadLifetime(t *testing.T) {
	for _, second := range []bool{false, true} {
		for _, shutdown := range []bool{false, true} {
			t.Run(fmt.Sprintf("second=%t/shutdown=%t", second, shutdown), func(t *testing.T) {
				parent, stop := context.WithCancel(t.Context())
				defer stop()
				caller, leave := context.WithCancel(t.Context())
				defer leave()
				c := NewCache[string, int](parent, 8, 1, time.Minute, time.Second, func(string, int) bool { return false }, time.Now)
				gate := make(chan struct{})
				started := make(chan context.Context, 2)
				returned := make(chan struct{}, 1)
				var calls atomic.Int32
				load := func(ctx context.Context) (int, error) {
					started <- ctx
					if calls.Add(1) == 1 && second {
						select {
						case <-gate:
							return 0, nil
						case <-ctx.Done():
						}
					}
					<-ctx.Done()
					returned <- struct{}{}
					return 0, ctx.Err()
				}
				get := func() chan error {
					done := make(chan error, 1)
					go func() { _, err := c.Get(caller, "k", load); done <- err }()
					return done
				}
				done := get()
				loadCtx := receive(t, started)
				if second {
					joiner := get()
					waitForWaiters(t, c, "k", 2)
					close(gate)
					if err := receive(t, done); err != nil {
						t.Fatal(err)
					}
					done, loadCtx = joiner, receive(t, started)
				}
				if shutdown {
					stop()
				} else {
					leave()
				}
				receive(t, returned)
				if err := receive(t, done); !errors.Is(err, context.Canceled) || loadCtx.Err() == nil {
					t.Fatalf("Get = %v, load context = %v", err, loadCtx.Err())
				}
			})
		}
	}
}

func TestCacheTimeoutsCannotBypassLoadLimit(t *testing.T) {
	for _, second := range []bool{false, true} {
		t.Run(fmt.Sprintf("second=%t", second), func(t *testing.T) {
			c := NewCache[string, int](t.Context(), 8, 1, time.Minute, 300*time.Millisecond, func(string, int) bool { return false }, time.Now)
			gate, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			started := make(chan context.Context, 8)
			var calls atomic.Int32
			load := func(ctx context.Context) (int, error) {
				started <- ctx
				if calls.Add(1) == 1 && second {
					<-gate
					return 0, nil
				}
				<-release // deliberately ignore cancellation
				return 1, nil
			}
			get := func() chan error {
				done := make(chan error, 1)
				go func() { _, err := c.Get(t.Context(), "k", load); done <- err }()
				return done
			}
			done := get()
			loadCtx := receive(t, started)
			want := int32(1)
			if second {
				joiner := get()
				waitForWaiters(t, c, "k", 2)
				close(gate)
				if err := receive(t, done); err != nil {
					t.Fatal(err)
				}
				done, loadCtx, want = joiner, receive(t, started), 2
			}
			for range 3 {
				if err := receive(t, done); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Get = %v, want load timeout", err)
				}
				done = get()
			}
			if err := receive(t, done); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			cause := errors.New("caller left while waiting for a slot")
			caller, cancel := context.WithTimeoutCause(t.Context(), 10*time.Millisecond, cause)
			defer cancel()
			if _, err := c.Get(caller, "other", load); !errors.Is(err, cause) {
				t.Fatalf("waiting caller = %v", err)
			}
			if calls.Load() != want || loadCtx.Err() == nil {
				t.Fatalf("%d loads, want %d; context = %v", calls.Load(), want, loadCtx.Err())
			}
			unblock()
			if v, err := c.Get(t.Context(), "k", func(context.Context) (int, error) { return 7, nil }); err != nil || v != 7 {
				t.Fatalf("retry after loader returned = %d, %v", v, err)
			}
		})
	}
}

// The joiners' second load is cancelled as soon as its last joiner leaves,
// even while the starter is still waiting for the first result: only the
// joiners need it (maintainer review on #316).
func TestCacheSecondLoadEndsWithItsLastJoiner(t *testing.T) {
	c := NewCache[string, int](t.Context(), 8, 1, time.Minute, time.Second, func(string, int) bool { return false }, time.Now)
	gate := make(chan struct{})
	var calls atomic.Int32
	load := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			<-gate
			return 1, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	joinerCtx, leave := context.WithCancel(t.Context())
	defer leave()
	joined := make(chan error, 1)
	// Set before any Get, so the load goroutine reads it after this write.
	secondLoad := make(chan error, 1)
	c.beforeSecondLoad = func(ctx context.Context) {
		// The second load is set up and the starter still waits: the last
		// joiner leaves now.
		leave()
		<-joined
		secondLoad <- ctx.Err()
	}
	started := make(chan error, 1)
	go func() {
		v, err := c.Get(t.Context(), "k", load)
		if err == nil && v != 1 {
			err = fmt.Errorf("starter got %d, want 1", v)
		}
		started <- err
	}()
	waitForWaiters(t, c, "k", 1)
	go func() { _, err := c.Get(joinerCtx, "k", load); joined <- err }()
	waitForWaiters(t, c, "k", 2)
	close(gate)
	if err := receive(t, secondLoad); !errors.Is(err, context.Canceled) {
		t.Fatalf("second load context after the last joiner left: %v, want canceled", err)
	}
	if err := receive(t, started); err != nil {
		t.Fatalf("starter: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d loads ran, want only the first", n)
	}
}
