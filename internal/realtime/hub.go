package realtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/tkakkie/ribbitto/internal/kernel"
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
	// ErrShutdown ends every connection when the server shuts down.
	ErrShutdown = errors.New("realtime: server shutting down")
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
	orgs      map[kernel.ID]*orgSequence
	byAccount map[kernel.ID]map[*registration]struct{}
	bySession map[kernel.ID]map[*registration]struct{}
	byOrg     map[kernel.ID]map[*registration]struct{}
	// shutdown, once CancelAll has run, refuses every registration.
	shutdown bool
}

// orgSequence is one organisation's latest sequence. changed is closed and
// replaced on every raise, which wakes every waiter at once; a buffered
// per-waiter signal could be dropped.
type orgSequence struct {
	latest  int64
	changed chan struct{}
	// waiters counts calls of Wait holding changed; see Waiting.
	waiters atomic.Int64
}

type registration struct {
	organization     kernel.ID
	account, session kernel.ID
	cancel           context.CancelCauseFunc
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{
		orgs:      make(map[kernel.ID]*orgSequence),
		byAccount: make(map[kernel.ID]map[*registration]struct{}),
		bySession: make(map[kernel.ID]map[*registration]struct{}),
		byOrg:     make(map[kernel.ID]map[*registration]struct{}),
	}
}

// sequence returns the organisation's entry, creating it; the caller holds mu.
func (h *Hub) sequence(org kernel.ID) *orgSequence {
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
func (h *Hub) Raise(org kernel.ID, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.raise(org, seq)
}

// RaiseIfActive is Raise for an organisation that still has a registered
// connection, checked under the same lock, so a value read while its last
// connection went away does not raise it.
func (h *Hub) RaiseIfActive(org kernel.ID, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.byOrg[org]) > 0 {
		h.raise(org, seq)
	}
}

// raise is Raise's body; the caller holds mu.
func (h *Hub) raise(org kernel.ID, seq int64) {
	s := h.sequence(org)
	if seq <= s.latest {
		return
	}
	s.latest = seq
	close(s.changed)
	s.changed = make(chan struct{})
}

// Latest returns the organisation's latest sequence the hub has been told
// about; 0 if none.
func (h *Hub) Latest(org kernel.ID) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.orgs[org]; ok {
		return s.latest
	}
	return 0
}

// Wait blocks until the organisation's latest sequence is greater than
// after and returns that sequence, at once if it already is. If ctx has
// ended or ends first, it returns context.Cause(ctx).
func (h *Hub) Wait(ctx context.Context, org kernel.ID, after int64) (int64, error) {
	for {
		if ctx.Err() != nil {
			return 0, context.Cause(ctx)
		}
		h.mu.Lock()
		s := h.sequence(org)
		if s.latest > after {
			latest := s.latest
			h.mu.Unlock()
			return latest, nil
		}
		changed := s.changed
		// Counted under mu with changed in hand: from here, any raise wakes
		// this call. Decremented without mu, to keep wakeups off the lock.
		s.waiters.Add(1)
		h.mu.Unlock()
		select {
		case <-changed:
			s.waiters.Add(-1)
		case <-ctx.Done():
			s.waiters.Add(-1)
			return 0, context.Cause(ctx)
		}
	}
}

// Waiting reports how many calls of Wait for the organisation are counted
// as blocked, so tests can synchronise on it instead of sleeping. A call is
// counted once it holds the channel the next raise closes; after a raise it
// stays counted until it runs its decrement, so read the count only while
// no raise is in flight.
func (h *Hub) Waiting(org kernel.ID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.orgs[org]; ok {
		return int(s.waiters.Load())
	}
	return 0
}

// Connection names who holds a stream: the organisation from the URL, and
// the account and session that authenticated the request.
type Connection struct {
	Organization, Account, Session kernel.ID
}

// Register adds a connection unless its account already holds limit
// connections, in which case it returns ErrTooManyConnections and nothing
// to unregister, or the hub is shutting down (ErrShutdown). The count and the addition happen under one lock, so
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
	if h.shutdown {
		return nil, nil, ErrShutdown
	}
	if len(h.byAccount[c.Account]) >= limit {
		return nil, nil, ErrTooManyConnections
	}
	ctx, cancel := context.WithCancelCause(parent)
	r := &registration{organization: c.Organization, account: c.Account, session: c.Session, cancel: cancel}
	add(h.byAccount, c.Account, r)
	add(h.bySession, c.Session, r)
	add(h.byOrg, c.Organization, r)
	var once sync.Once
	unregister = func() {
		once.Do(func() {
			h.mu.Lock()
			remove(h.byAccount, r.account, r)
			remove(h.bySession, r.session, r)
			remove(h.byOrg, r.organization, r)
			h.mu.Unlock()
			cancel(ErrUnregistered)
		})
	}
	return ctx, unregister, nil
}

// Connections returns how many connections are registered, for the
// development-only metrics listener. It only reads the registry.
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.byAccount {
		n += len(set)
	}
	return n
}

// ActiveOrganizations returns the organisations with at least one
// registered connection, in no particular order. Unlike the sequences,
// which the hub keeps for every organisation it was ever told about, this
// follows the registry, so an organisation drops out when its last
// connection unregisters.
func (h *Hub) ActiveOrganizations() []kernel.ID {
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs := make([]kernel.ID, 0, len(h.byOrg))
	for org := range h.byOrg {
		orgs = append(orgs, org)
	}
	return orgs
}

// CancelAccount ends the contexts of all the account's connections, for
// example when the account signs out everywhere. The connections keep
// their slots until they unregister.
func (h *Hub) CancelAccount(account kernel.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for r := range h.byAccount[account] {
		r.cancel(ErrAccountCancelled)
	}
}

// CancelAll ends the contexts of every registered connection and refuses
// every later registration with ErrShutdown, for a server shutting down: an
// open stream never goes idle, so the server would otherwise wait for it
// until its shutdown deadline, and a request already accepted could still
// register after this ran. The connections keep their slots until they
// unregister.
func (h *Hub) CancelAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdown = true
	for _, set := range h.byAccount {
		for r := range set {
			r.cancel(ErrShutdown)
		}
	}
}

// CancelSession ends the contexts of the session's connections, for example
// when it signs out or is replaced. The connections keep their slots until
// they unregister.
func (h *Hub) CancelSession(session kernel.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for r := range h.bySession[session] {
		r.cancel(ErrSessionEnded)
	}
}

func add(index map[kernel.ID]map[*registration]struct{}, key kernel.ID, r *registration) {
	set, ok := index[key]
	if !ok {
		set = make(map[*registration]struct{})
		index[key] = set
	}
	set[r] = struct{}{}
}

// remove deletes r and drops an empty set, so the maps do not keep an entry
// for every account or session that ever connected.
func remove(index map[kernel.ID]map[*registration]struct{}, key kernel.ID, r *registration) {
	delete(index[key], r)
	if len(index[key]) == 0 {
		delete(index, key)
	}
}
