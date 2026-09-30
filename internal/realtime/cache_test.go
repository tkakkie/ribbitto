package realtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
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

func TestCacheCoalescesConcurrentMisses(t *testing.T) {
	c := NewCache[string, int](8, time.Minute, time.Second, time.Now)
	release := make(chan struct{})
	var loads atomic.Int32
	load := func(context.Context) (int, error) {
		loads.Add(1)
		<-release
		return 42, nil
	}
	const callers = 50
	var wg sync.WaitGroup
	results := make(chan int, callers)
	for range callers {
		wg.Go(func() {
			v, err := c.Get(t.Context(), "k", load)
			if err != nil {
				t.Error(err)
			}
			results <- v
		})
	}
	// Let every caller reach the in-flight load before it finishes.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(results)
	for v := range results {
		if v != 42 {
			t.Fatalf("value %d, want 42", v)
		}
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads for %d concurrent misses, want 1", n, callers)
	}
}

func TestCacheCapacityAndTTL(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	c := NewCache[string, string](2, time.Minute, time.Second, clock.Now)
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

func TestCacheDoesNotStoreErrors(t *testing.T) {
	c := NewCache[string, int](8, time.Minute, time.Second, time.Now)
	failure := errors.New("database unavailable")
	release := make(chan struct{})
	var loads atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
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
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	// The failure was not stored: the next call loads again and succeeds.
	v, err := c.Get(t.Context(), "k", func(context.Context) (int, error) { loads.Add(1); return 7, nil })
	if err != nil || v != 7 || loads.Load() != 2 {
		t.Fatalf("retry = %d, %v after %d loads; want 7, nil, 2", v, err, loads.Load())
	}
}

// A caller that goes away while waiting gets its cause; the load carries on
// for the others and is not cancelled with it.
func TestCacheWaiterCancellationLeavesTheLoad(t *testing.T) {
	c := NewCache[string, int](8, time.Minute, time.Second, time.Now)
	release := make(chan struct{})
	var loadErr error
	load := func(ctx context.Context) (int, error) {
		<-release
		loadErr = ctx.Err()
		return 1, nil
	}
	cause := errors.New("stream closed")
	gone, cancel := context.WithCancelCause(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := c.Get(gone, "k", load)
		first <- err
	}()
	time.Sleep(20 * time.Millisecond)
	second := make(chan int, 1)
	go func() {
		v, _ := c.Get(t.Context(), "k", load)
		second <- v
	}()
	cancel(cause)
	if err := <-first; !errors.Is(err, cause) {
		t.Fatalf("cancelled caller got %v, want %v", err, cause)
	}
	close(release)
	if v := <-second; v != 1 || loadErr != nil {
		t.Fatalf("other caller got %d, load context %v; want 1 and a live load", v, loadErr)
	}
}

func TestCachedEventsKeysReadsByTheHubLevel(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{events: []domain.Event{posted(1, channelA)}}
	events := NewCachedEvents(log, hub, 64, time.Minute)
	read := func(after int64) []domain.Event {
		t.Helper()
		got, err := events.EventsAfter(t.Context(), orgA, after, 10)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	hub.Raise(orgA, 1)
	if got := read(1); len(got) != 0 { // empty, cached at level 1
		t.Fatalf("read after 1 = %v", got)
	}
	// Event 2 commits, but the hub has not been raised yet: a read at the
	// same level may be served from the cache without it...
	log.append(posted(2, channelA))
	read(1)
	calls := log.calls
	// ...and once the hub is raised, the key changes and the log is read
	// again, so the event cannot stay hidden.
	hub.Raise(orgA, 2)
	if got := read(1); len(got) != 1 || got[0].Seq != 2 || log.calls != calls+1 {
		t.Fatalf("read after the raise = %v with %d reads, want event 2 from a fresh read", got, log.calls-calls)
	}
	// A short batch (fewer than the limit) is cached the same way.
	if got := read(0); len(got) != 2 {
		t.Fatalf("read after 0 = %v", got)
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
	done := runAsync(ctx, Stream{Hub: hub, Events: NewCachedEvents(log, hub, 64, time.Minute), Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render), BatchSize: 2}, 0, send)
	send.waitFor(t, 1, 2, 3, 4, 5, 6, 7)
	cancel()
	<-done
}

// Many streams at the same cursor share one read of each new event.
func TestCachedEventsShareReadsBetweenStreams(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{}
	events := NewCachedEvents(log, hub, 64, time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const streams = 50
	senders := make([]*recorder, streams)
	for i := range senders {
		senders[i] = newRecorder()
		runAsync(ctx, Stream{Hub: hub, Events: events, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 0, senders[i])
	}
	time.Sleep(100 * time.Millisecond) // every stream has read once and waits
	log.mu.Lock()
	before := log.calls
	log.mu.Unlock()
	log.append(posted(1, channelA))
	hub.Raise(orgA, 1)
	for _, s := range senders {
		s.waitFor(t, 1)
	}
	log.mu.Lock()
	reads := log.calls - before
	log.mu.Unlock()
	if reads > 2 {
		t.Fatalf("%d log reads for one event and %d streams, want at most 2", reads, streams)
	}
}
