package unread

import (
	"sync"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// Changes holds process-local read-change watches for actively subscribed members.
// Its zero value is ready to use; it must not be copied after first use.
type Changes struct {
	mu      sync.Mutex
	entries map[changeKey]*memberChanges
}

type changeKey struct{ organization, member kernel.ID }

type memberChanges struct {
	generation uint64
	changed    chan struct{}
	watches    int
}

// ChangeWatch is one subscription to a member's read changes.
// Use the returned pointer; a watch must not be copied.
type ChangeWatch struct {
	changes  *Changes
	key      changeKey
	entry    *memberChanges
	released bool // Protected by changes.mu, including concurrent releases.
}

// Watch subscribes before the caller's initial snapshot of persisted read state.
// Generations start at zero for each new subscription lifetime, not each watch.
func (c *Changes) Watch(organizationID, memberID kernel.ID) *ChangeWatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := changeKey{organizationID, memberID}
	entry := c.entries[key]
	if entry == nil {
		entry = &memberChanges{changed: make(chan struct{})}
		if c.entries == nil {
			c.entries = make(map[changeKey]*memberChanges)
		}
		c.entries[key] = entry
	}
	entry.watches++
	return &ChangeWatch{changes: c, key: key, entry: entry}
}

// ReadChanged synchronously advances and wakes only this member's active watches.
// Call after a successful commit; without subscribers it retains nothing.
func (c *Changes) ReadChanged(organizationID, memberID kernel.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[changeKey{organizationID, memberID}]
	if entry == nil {
		return
	}
	entry.generation++
	close(entry.changed)
	entry.changed = make(chan struct{})
}

// Snapshot atomically captures a generation and its wake channel.
// A later publication closes that channel even before the caller begins waiting.
// Only use snapshots for delivery until Release; they contain no persisted state.
func (w *ChangeWatch) Snapshot() (uint64, <-chan struct{}) {
	w.changes.mu.Lock()
	defer w.changes.mu.Unlock()
	return w.entry.generation, w.entry.changed
}

// Release ends this subscription, deleting the member entry after its last watch.
// Repeated calls are harmless, including after the same member subscribes again.
func (w *ChangeWatch) Release() {
	w.changes.mu.Lock()
	defer w.changes.mu.Unlock()
	if w.released {
		return
	}
	w.released = true
	w.entry.watches--
	if w.entry.watches == 0 {
		delete(w.changes.entries, w.key)
	}
}
