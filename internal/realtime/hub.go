package realtime

import (
	"context"
	"errors"
	"sync"

	"github.com/tkakkie/ribbitto/internal/domain"
)

// Causes reported by context.Cause for a registered connection's context.
// When the request ends first, the cause is the request context's own.
var (
	// ErrTooManyConnections refuses a registration over the account's limit.
	ErrTooManyConnections = errors.New("realtime: too many connections for the account")
	// ErrAccountCancelled ends connections cancelled by CancelAccount.
	ErrAccountCancelled = errors.New("realtime: account's connections cancelled")
	// ErrSessionEnded ends connections cancelled by CancelSession.
	ErrSessionEnded = errors.New("realtime: session ended")
	// ErrUnregistered ends a connection whose unregister function ran.
	ErrUnregistered = errors.New("realtime: connection unregistered")
)

// Hub holds, per organisation, the highest committed event sequence it has
// been told about, and the registry of open connections. It is safe for
// concurrent use; the zero value is not usable, so call NewHub.
//
// The sequence is a level, not a signal: a connection compares it with its
// own cursor under the lock before blocking, so a raise that lands between
// the connection's last read and its wait is never lost.
type Hub struct {
	mu        sync.Mutex
	orgs      map[domain.ID]*orgSequence
	byAccount map[domain.ID]map[*registration]struct{}
	bySession map[domain.ID]map[*registration]struct{}
}

// orgSequence is one organisation's latest sequence. changed is closed and
// replaced on every raise, which wakes every waiter at once; a buffered
// per-waiter signal could be dropped.
type orgSequence struct {
	latest  int64
	changed chan struct{}
}

type registration struct {
	account, session domain.ID
	cancel           context.CancelCauseFunc
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{
		orgs:      make(map[domain.ID]*orgSequence),
		byAccount: make(map[domain.ID]map[*registration]struct{}),
		bySession: make(map[domain.ID]map[*registration]struct{}),
	}
}

// sequence returns the organisation's entry, creating it; the caller holds mu.
func (h *Hub) sequence(org domain.ID) *orgSequence {
	s, ok := h.orgs[org]
	if !ok {
		s = &orgSequence{changed: make(chan struct{})}
		h.orgs[org] = s
	}
	return s
}

// Raise records that the organisation's events up to seq are committed. A
// value at or below the current one changes nothing, so notifications may
// arrive late or out of order.
func (h *Hub) Raise(org domain.ID, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sequence(org)
	if seq <= s.latest {
		return
	}
	s.latest = seq
	close(s.changed)
	s.changed = make(chan struct{})
}

// Wait blocks until the organisation's latest sequence is greater than
// after, returning nil at once if it already is. If ctx ends first, it
// returns context.Cause(ctx).
func (h *Hub) Wait(ctx context.Context, org domain.ID, after int64) error {
	for {
		h.mu.Lock()
		s := h.sequence(org)
		if s.latest > after {
			h.mu.Unlock()
			return nil
		}
		changed := s.changed
		h.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// Connection names who holds a stream: the organisation from the URL, and
// the account and session that authenticated the request.
type Connection struct {
	Organization, Account, Session domain.ID
}

// Register adds a connection unless its account already holds limit
// connections, in which case it returns ErrTooManyConnections and nothing
// to unregister. The count and the addition happen under one lock, so
// simultaneous registrations cannot overshoot the limit; a limit below 1
// refuses every registration.
//
// The returned context is derived from parent, normally the request's
// context. It ends at the first of: parent ending, CancelAccount or
// CancelSession matching the connection, or unregister running.
// context.Cause reports which (ErrAccountCancelled, ErrSessionEnded,
// ErrUnregistered, or parent's cause).
//
// The caller defers unregister right after a successful Register. Only
// unregister frees the slot and removes the connection: a context that has
// merely ended still counts towards the limit until then. unregister
// cancels the context, may be called more than once and from any
// goroutine, and once it has returned no Cancel call reaches the
// connection.
func (h *Hub) Register(parent context.Context, c Connection, limit int) (ctx context.Context, unregister func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.byAccount[c.Account]) >= limit {
		return nil, nil, ErrTooManyConnections
	}
	ctx, cancel := context.WithCancelCause(parent)
	r := &registration{account: c.Account, session: c.Session, cancel: cancel}
	add(h.byAccount, c.Account, r)
	add(h.bySession, c.Session, r)
	var once sync.Once
	unregister = func() {
		once.Do(func() {
			h.mu.Lock()
			remove(h.byAccount, r.account, r)
			remove(h.bySession, r.session, r)
			h.mu.Unlock()
			cancel(ErrUnregistered)
		})
	}
	return ctx, unregister, nil
}

// CancelAccount ends the contexts of all the account's connections, for
// example when the account signs out everywhere. The connections keep
// their slots until they unregister.
func (h *Hub) CancelAccount(account domain.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for r := range h.byAccount[account] {
		r.cancel(ErrAccountCancelled)
	}
}

// CancelSession ends the contexts of the session's connections, for example
// when it signs out or is replaced. The connections keep their slots until
// they unregister.
func (h *Hub) CancelSession(session domain.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for r := range h.bySession[session] {
		r.cancel(ErrSessionEnded)
	}
}

func add(index map[domain.ID]map[*registration]struct{}, key domain.ID, r *registration) {
	set, ok := index[key]
	if !ok {
		set = make(map[*registration]struct{})
		index[key] = set
	}
	set[r] = struct{}{}
}

// remove deletes r and drops an empty set, so the maps do not keep an entry
// for every account or session that ever connected.
func remove(index map[domain.ID]map[*registration]struct{}, key domain.ID, r *registration) {
	delete(index[key], r)
	if len(index[key]) == 0 {
		delete(index, key)
	}
}
