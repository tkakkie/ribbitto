package realtime

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Cache is a bounded, expiring in-memory cache shared by an organisation's
// streams. Concurrent misses for one key run a single load and share its
// result (singleflight), so N streams asking for the same event cause one
// database read, or two when the value is not kept (see NewCache), not N.
// Errors are never stored: every caller waiting on a
// failed load gets the error, and the next call loads again. It is safe for
// concurrent use; the zero value is not usable, so call NewCache.
//
// It is generic because the event log's reads (here) and the rendered
// messages (in web) need the same bounded, coalescing behaviour.
type Cache[K comparable, V any] struct {
	capacity    int
	ttl         time.Duration
	loadTimeout time.Duration
	now         func() time.Time
	keep        func(K, V) bool

	mu      sync.Mutex
	entries map[K]*list.Element // of *cacheEntry[K, V]; front is most recent
	order   *list.List
	loading map[K]*cacheCall[V]
}

type cacheEntry[K comparable, V any] struct {
	key     K
	value   V
	expires time.Time
}

type cacheCall[V any] struct {
	done    chan struct{}
	value   V
	err     error
	waiters int
	// again, set before done closes, is the load the joiners get instead
	// when value is not kept (see NewCache).
	again *cacheCall[V]
}

// NewCache returns a cache holding at most capacity entries, each for at
// most ttl. A load runs for at most loadTimeout, detached from the caller
// that started it: one stream going away must not fail the others waiting
// on the same load. keep, if not nil, decides whether a loaded value is
// stored. A value it rejects may already be out of date, so only the caller
// that started the load gets it; the callers that joined the load while it
// ran share a second load, started once the first has finished and so
// after each of them joined, and open to no one else. Without that, a
// joiner could get a snapshot older than its own call. now is time.Now
// outside tests.
func NewCache[K comparable, V any](capacity int, ttl, loadTimeout time.Duration, keep func(K, V) bool, now func() time.Time) *Cache[K, V] {
	return &Cache[K, V]{
		capacity: max(capacity, 1), ttl: ttl, loadTimeout: loadTimeout, keep: keep, now: now,
		entries: make(map[K]*list.Element), order: list.New(), loading: make(map[K]*cacheCall[V]),
	}
}

// Get returns the value for key, loading it with load on a miss or after
// the entry expired. A caller whose ctx ends while waiting gets
// context.Cause(ctx); the load itself carries on for the others.
func (c *Cache[K, V]) Get(ctx context.Context, key K, load func(context.Context) (V, error)) (V, error) {
	c.mu.Lock()
	if elem, ok := c.entries[key]; ok {
		entry := elem.Value.(*cacheEntry[K, V])
		if c.now().Before(entry.expires) {
			c.order.MoveToFront(elem)
			c.mu.Unlock()
			return entry.value, nil
		}
		c.order.Remove(elem)
		delete(c.entries, key)
	}
	call, joined := c.loading[key]
	if !joined {
		call = &cacheCall[V]{done: make(chan struct{})}
		c.loading[key] = call
		go c.load(context.WithoutCancel(ctx), key, call, load)
	}
	call.waiters++
	c.mu.Unlock()

	if err := wait(ctx, call); err != nil {
		var zero V
		return zero, err
	}
	if joined && call.again != nil {
		call = call.again
		if err := wait(ctx, call); err != nil {
			var zero V
			return zero, err
		}
	}
	return call.value, call.err
}

// wait blocks until call is done or ctx ends, returning context.Cause(ctx)
// in the second case.
func wait[V any](ctx context.Context, call *cacheCall[V]) error {
	select {
	case <-call.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// load runs one load for key, stores its value if kept, and runs the
// joiners' second load when it is not.
func (c *Cache[K, V]) load(ctx context.Context, key K, call *cacheCall[V], load func(context.Context) (V, error)) {
	c.run(ctx, call, load)

	c.mu.Lock()
	delete(c.loading, key)
	kept := call.err == nil && (c.keep == nil || c.keep(key, call.value))
	if kept {
		c.entries[key] = c.order.PushFront(&cacheEntry[K, V]{key: key, value: call.value, expires: c.now().Add(c.ttl)})
		for c.order.Len() > c.capacity {
			oldest := c.order.Back()
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*cacheEntry[K, V]).key)
		}
	}
	// No one can join call any more, so waiters is final. An error is
	// shared as it is: it says nothing about the data.
	if call.err == nil && !kept && call.waiters > 1 {
		call.again = &cacheCall[V]{done: make(chan struct{})}
	}
	c.mu.Unlock()
	close(call.done)
	if call.again != nil {
		c.run(ctx, call.again, load)
		close(call.again.done)
	}
}

// run runs load into call.value and call.err. The timeout is enforced
// here, not left to load: a loader that ignores its context must not hold
// every waiter, or the key, beyond loadTimeout. Its late result is dropped;
// only its own goroutine is left running until it returns.
func (c *Cache[K, V]) run(ctx context.Context, call *cacheCall[V], load func(context.Context) (V, error)) {
	ctx, cancel := context.WithTimeout(ctx, c.loadTimeout)
	defer cancel()
	type result struct {
		value V
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := load(ctx)
		done <- result{value, err}
	}()
	select {
	case r := <-done:
		call.value, call.err = r.value, r.err
	case <-ctx.Done():
		call.err = context.Cause(ctx)
	}
}

// Len reports how many entries the cache holds, for tests.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Waiting reports how many callers have joined the load in flight for key
// (0 if none), so tests can synchronise on it instead of sleeping.
func (c *Cache[K, V]) Waiting(key K) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if call, ok := c.loading[key]; ok {
		return call.waiters
	}
	return 0
}
