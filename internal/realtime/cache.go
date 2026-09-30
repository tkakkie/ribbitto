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
// database read, not N. Errors are never stored: every caller waiting on a
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
}

// NewCache returns a cache holding at most capacity entries, each for at
// most ttl. A load runs for at most loadTimeout, detached from the caller
// that started it: one stream going away must not fail the others waiting
// on the same load. keep, if not nil, decides whether a loaded value is
// stored; a value it rejects is still shared with the callers that waited
// for it. now is time.Now outside tests.
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
	call, ok := c.loading[key]
	if !ok {
		call = &cacheCall[V]{done: make(chan struct{})}
		c.loading[key] = call
		go c.load(context.WithoutCancel(ctx), key, call, load)
	}
	call.waiters++
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		var zero V
		return zero, context.Cause(ctx)
	}
}

func (c *Cache[K, V]) load(ctx context.Context, key K, call *cacheCall[V], load func(context.Context) (V, error)) {
	ctx, cancel := context.WithTimeout(ctx, c.loadTimeout)
	defer cancel()
	call.value, call.err = load(ctx)

	c.mu.Lock()
	delete(c.loading, key)
	if call.err == nil && (c.keep == nil || c.keep(key, call.value)) {
		c.entries[key] = c.order.PushFront(&cacheEntry[K, V]{key: key, value: call.value, expires: c.now().Add(c.ttl)})
		for c.order.Len() > c.capacity {
			oldest := c.order.Back()
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*cacheEntry[K, V]).key)
		}
	}
	c.mu.Unlock()
	close(call.done)
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
