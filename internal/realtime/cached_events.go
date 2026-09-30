package realtime

import (
	"context"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// CachedEvents is an EventReader that shares reads between an
// organisation's streams: streams at the same cursor read the log once.
//
// A read is keyed by the hub's level when it starts, as well as by the
// organisation, the cursor and the limit. The hub is raised only after a
// commit, so a read started at level L sees every event up to L; once the
// hub moves past L, the key changes and the log is read again. Without the
// level, a read made between an event's commit and its Raise could be
// served after the Raise without that event, and a stream that has already
// waited past that level would not read it again until the next post.
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
	return &CachedEvents{events: events, hub: hub, cache: NewCache[eventsKey, []domain.Event](capacity, ttl, 10*time.Second, time.Now)}
}

// EventsAfter implements EventReader.
func (c *CachedEvents) EventsAfter(ctx context.Context, organizationID domain.ID, after int64, limit int) ([]domain.Event, error) {
	key := eventsKey{organization: organizationID, after: after, level: c.hub.Latest(organizationID), limit: limit}
	return c.cache.Get(ctx, key, func(ctx context.Context) ([]domain.Event, error) {
		return c.events.EventsAfter(ctx, organizationID, after, limit)
	})
}
