package realtime

import (
	"context"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// CachedEvents is an EventReader that shares reads between an
// organisation's streams: streams at the same cursor read the log once.
//
// Only full batches are stored. A full batch is the first limit events
// after the cursor, which never changes once committed. A short or empty
// batch only says what the log held when it was read, and a stream takes it
// as "caught up" and waits: served later from the cache, it could hide
// events committed since — including ones no Raise announces (a crash
// between commit and Raise, or a writer that does not notify). Short
// batches are still shared by the streams reading at the same moment,
// which is the steady-state case: every stream wakes on the same Raise.
//
// Reads in flight are also keyed by the hub's level when they start. The
// hub is raised only after a commit, so a read started at level L sees
// every event up to L; a stream that has seen a higher level does not join
// an older read that may have missed its event.
type CachedEvents struct {
	events EventReader
	hub    *Hub
	cache  *Cache[eventsKey, []domain.Event]
}

type eventsKey struct {
	organization domain.ID
	after, level int64
	limit        int
}

// NewCachedEvents wraps events. capacity and ttl bound the cache; events
// never change once committed, so ttl only bounds memory.
func NewCachedEvents(events EventReader, hub *Hub, capacity int, ttl time.Duration) *CachedEvents {
	return newCachedEvents(events, hub, capacity, ttl, 10*time.Second)
}

func newCachedEvents(events EventReader, hub *Hub, capacity int, ttl, loadTimeout time.Duration) *CachedEvents {
	c := &CachedEvents{events: events, hub: hub}
	c.cache = NewCache[eventsKey, []domain.Event](capacity, ttl, loadTimeout, fullBatch, time.Now)
	return c
}

// EventsAfter implements EventReader.
func (c *CachedEvents) EventsAfter(ctx context.Context, organizationID domain.ID, after int64, limit int) ([]domain.Event, error) {
	key := eventsKey{organization: organizationID, after: after, level: c.hub.Latest(organizationID), limit: limit}
	return c.cache.Get(ctx, key, func(ctx context.Context) ([]domain.Event, error) {
		return c.events.EventsAfter(ctx, organizationID, after, limit)
	})
}

// fullBatch keeps only batches that fill their limit (see CachedEvents).
func fullBatch(key eventsKey, events []domain.Event) bool {
	return len(events) == key.limit
}
