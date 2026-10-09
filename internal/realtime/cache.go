package realtime

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Cache is a bounded, expiring in-memory cache shared by an organisation's
// streams. Concurrent misses for one key run a single load and share its
// result (singleflight), so N streams asking for the same event at the
// same moment cause one database read, not N; when the value is not kept,
// that load's joiners share one more private read (see NewCache).
// Errors are never stored: every caller waiting on a
// failed load gets the error, and the next call loads again. It is safe for
// concurrent use; the zero value is not usable, so call NewCache.
//
// It is generic because the event log's reads (here) and the rendered
// messages (in web) need the same bounded, coalescing behaviour.
type Cache[K comparable, V any] struct {
	parent      context.Context
	slots       chan struct{}
	capacity    int
	ttl         time.Duration
	loadTimeout time.Duration
	now         func() time.Time
	keep        func(K, V) bool

	// afterLoad, nil outside tests, pauses after the first result is accepted
	// and before retention or the joiners' second load is decided.
	afterLoad func(context.Context)

	// afterCancellationCheck, nil outside tests, lets cancellation propagate
	// between result acceptance and the joiners' second-load decision. It
	// runs under c.mu, after the cancellation check, so it must not call
	// methods that take the lock (Waiting, Len).
	afterCancellationCheck func(context.Context)

	// beforeSecondLoad, nil outside tests, runs once a joiners' second load
	// is set up and before the starter is released, so tests
	// can make the last joiner leave at that point.
	beforeSecondLoad func(context.Context)

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
	joiners int
	cancel  context.CancelCauseFunc
	// joined is allocated before registration, so joiners wait once, directly
	// for their result, rather than waking with the starter and waiting again.
	joined *cacheCall[V]
	// again, set before done closes, is the load the joiners get instead
	// when value is not kept (see NewCache).
	again *cacheCall[V]
}

// DefaultCacheLoads bounds concurrent loaders in each production cache.
const DefaultCacheLoads = 16

// NewCache returns a cache holding at most capacity entries, each for at
// most ttl, with at most maxLoads loaders running until they actually return.
// Waiting for a slot and loading share loadTimeout. Load contexts retain caller
// values but end with parent or their last waiter, not any individual caller.
// keep, if not nil, decides whether a loaded value is
// stored. A value it rejects may already be out of date, so only the caller
// that started the load gets it; the callers that joined the load while it
// ran share a second load, started once the first has finished and so
// after each of them joined, and open to no one else. Without that, a
// joiner could get an unkept snapshot older than its own call. If parent
// ends before that second load can finish, joiners get its cancellation
// cause instead. Results accepted after parent cancellation are neither
// returned nor stored, even if keep would accept them. Kept values may
// intentionally serve later callers until ttl expires. now is time.Now
// outside tests.
func NewCache[K comparable, V any](parent context.Context, capacity, maxLoads int, ttl, loadTimeout time.Duration, keep func(K, V) bool, now func() time.Time) *Cache[K, V] {
	return &Cache[K, V]{
		parent: parent, slots: make(chan struct{}, max(maxLoads, 1)),
		capacity: max(capacity, 1), ttl: ttl, loadTimeout: loadTimeout, keep: keep, now: now,
		entries: make(map[K]*list.Element), order: list.New(), loading: make(map[K]*cacheCall[V]),
	}
}

// Get returns the value for key, loading it with load on a miss or after
// the entry expired. A caller whose ctx ends while waiting gets
// context.Cause(ctx); the load itself carries on for the others.
func (c *Cache[K, V]) Get(ctx context.Context, key K, load func(context.Context) (V, error)) (V, error) {
	for _, check := range []context.Context{ctx, c.parent} {
		if err := context.Cause(check); err != nil {
			var zero V
			return zero, err
		}
	}
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
		loadCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		call = &cacheCall[V]{done: make(chan struct{}), cancel: cancel,
			joined: &cacheCall[V]{done: make(chan struct{})}}
		c.loading[key] = call
		go c.load(loadCtx, key, call, load)
	} else {
		call.joiners++
	}
	call.waiters++
	c.mu.Unlock()
	defer c.leave(key, call, joined)

	result := call
	if joined {
		result = call.joined
	}
	if err := wait(ctx, result); err != nil {
		var zero V
		return zero, err
	}
	return result.value, result.err
}

func (c *Cache[K, V]) leave(key K, call *cacheCall[V], joined bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	call.waiters--
	if joined {
		call.joiners--
	}
	// Only the joiners need a second load: once the last of them leaves, it
	// is cancelled even while the starter has not collected its result yet.
	if call.waiters == 0 || (call.again != nil && call.joiners == 0) {
		call.cancel(context.Canceled)
	}
	if call.waiters == 0 {
		// A new caller must not join an abandoned load. Its late result must
		// not delete or replace a newer load for this key either.
		if c.loading[key] == call {
			delete(c.loading, key)
		}
	}
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
	stop := context.AfterFunc(c.parent, func() { call.cancel(context.Cause(c.parent)) })
	defer stop()
	defer call.cancel(context.Canceled)
	c.run(ctx, call, load)
	if c.afterLoad != nil {
		c.afterLoad(ctx)
	}

	c.mu.Lock()
	current := c.loading[key] == call
	if current {
		delete(c.loading, key)
	}
	kept := current && call.err == nil && (c.keep == nil || c.keep(key, call.value))
	if c.rejectCancelled(ctx, call) {
		kept = false
	}
	if c.afterCancellationCheck != nil {
		c.afterCancellationCheck(ctx)
	}
	if kept {
		c.entries[key] = c.order.PushFront(&cacheEntry[K, V]{key: key, value: call.value, expires: c.now().Add(c.ttl)})
		for c.order.Len() > c.capacity {
			oldest := c.order.Back()
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*cacheEntry[K, V]).key)
		}
	}
	// Only remaining joiners need a fresh snapshot. They keep their wait
	// registered on the original call until this private second load ends.
	if call.err == nil && !kept && call.joiners > 0 {
		call.again = call.joined
	}
	c.mu.Unlock()
	if call.again != nil && c.beforeSecondLoad != nil {
		c.beforeSecondLoad(ctx)
	}
	close(call.done)
	if call.again != nil {
		c.run(ctx, call.again, load)
	} else {
		call.joined.value, call.joined.err = call.value, call.err
	}
	close(call.joined.done)
}

// run runs load into call.value and call.err. The timeout is enforced
// here, not left to load: a loader that ignores its context must not hold
// every waiter, or the key, beyond loadTimeout. Its late result is dropped;
// only its own goroutine is left running until it returns.
func (c *Cache[K, V]) run(ctx context.Context, call *cacheCall[V], load func(context.Context) (V, error)) {
	ctx, cancel := context.WithTimeout(ctx, c.loadTimeout)
	defer cancel()
	defer c.rejectCancelled(ctx, call)
	if c.rejectCancelled(ctx, call) {
		return
	}
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		call.err = context.Cause(ctx)
		return
	}
	if c.rejectCancelled(ctx, call) {
		<-c.slots
		return
	}
	type result struct {
		value V
		err   error
	}
	done := make(chan result, 1)
	go func() {
		// Timeout/cancellation releases waiters, never a running loader's slot.
		defer func() { <-c.slots }()
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

// rejectCancelled sets call's value to zero and its error to the cancellation
// cause, reporting whether the result was rejected. AfterFunc cancels work
// asynchronously, so check the parent itself and prefer its shutdown cause
// even if the load's own timeout or last waiter ended it too. Retention and
// second-load decisions must use this single check, without re-reading ctx.Err.
func (c *Cache[K, V]) rejectCancelled(ctx context.Context, call *cacheCall[V]) bool {
	for _, check := range []context.Context{c.parent, ctx} {
		if err := context.Cause(check); err != nil {
			var zero V
			call.value, call.err = zero, err
			return true
		}
	}
	return false
}

// Len reports how many entries the cache holds, including expired entries,
// so tests of its users can observe retention after shutdown.
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
