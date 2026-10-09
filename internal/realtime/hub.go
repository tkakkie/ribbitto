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
	// ErrTooManyConnections refuses a registration over either stream limit.
	ErrTooManyConnections = errors.New("realtime: too many connections")
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
// own cursor against an atomic snapshot before blocking, so a raise between
// the connection's last read and its wait is never lost.
type Hub struct {
	maxStreams int
	mu         sync.Mutex
	// connections is guarded by mu.
	connections int
	orgs        sync.Map // kernel.ID -> *orgSequence; entries are never removed
	byAccount   map[kernel.ID]map[*registration]struct{}
	bySession   map[kernel.ID]map[*registration]struct{}
	byOrg       map[kernel.ID]map[*registration]struct{}
	// shutdown, once CancelAll has run, refuses every registration.
	shutdown bool

	// Test gates, set before use, expose publication and read-to-wait races.
	afterWaitRead       func()
	beforeRaise         func(int64)
	beforeSequenceStore func()
}

// orgSequence is one organisation's latest sequence. changed is closed and
// replaced on every raise, which wakes every waiter at once; a buffered
// per-waiter signal could be dropped.
type orgSequence struct {
	state atomic.Pointer[sequenceState]
	// waiters counts calls of Wait holding changed; see Waiting.
	waiters atomic.Int64
}

// sequenceState pairs the level with the channel a later raise must close.
// Publishing them separately could strand a waiter on the new channel.
type sequenceState struct {
	latest  int64
	changed chan struct{}
}

type registration struct {
	organization     kernel.ID
	account, session kernel.ID
	cancel           context.CancelCauseFunc
}

// DefaultMaxStreams caps open streams per process, leaving headroom below
// the measured active-stream ceiling (docs/architecture/load-testing.md).
const DefaultMaxStreams = 5000

// NewHub returns an empty hub with DefaultMaxStreams as its process cap.
func NewHub() *Hub {
	return NewHubWithMaxStreams(DefaultMaxStreams)
}

// NewHubWithMaxStreams returns an empty hub with the given process cap.
// A cap below 1 refuses every registration.
func NewHubWithMaxStreams(maxStreams int) *Hub {
	return &Hub{
		maxStreams: maxStreams,
		byAccount:  make(map[kernel.ID]map[*registration]struct{}),
		bySession:  make(map[kernel.ID]map[*registration]struct{}),
		byOrg:      make(map[kernel.ID]map[*registration]struct{}),
	}
}

// sequence returns a stable entry, so readers need no registry lock.
func (h *Hub) sequence(org kernel.ID) *orgSequence {
	if s, ok := h.orgs.Load(org); ok {
		return s.(*orgSequence)
	}
	s := &orgSequence{}
	s.state.Store(&sequenceState{changed: make(chan struct{})})
	if h.beforeSequenceStore != nil {
		h.beforeSequenceStore()
	}
	actual, _ := h.orgs.LoadOrStore(org, s)
	return actual.(*orgSequence)
}

// Raise records that the organisation's events up to seq are committed. A
// value at or below the current one changes nothing, so notifications may
// arrive late or out of order.
func (h *Hub) Raise(org kernel.ID, seq int64) {
	s := h.sequence(org)
	for {
		old := s.state.Load()
		if seq <= old.latest {
			return
		}
		if h.beforeRaise != nil {
			h.beforeRaise(seq)
		}
		next := &sequenceState{latest: seq, changed: make(chan struct{})}
		if s.state.CompareAndSwap(old, next) {
			// Only the successful publisher owns this close. A waiter that
			// captured old before publication still wakes after publication.
			close(old.changed)
			return
		}
	}
}

// RaiseIfActive is Raise for an organisation that still has a registered
// connection, checked under the same lock, so a value read while its last
// connection went away does not raise it.
func (h *Hub) RaiseIfActive(org kernel.ID, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.byOrg[org]) > 0 {
		h.Raise(org, seq)
	}
}

// Latest returns the organisation's latest sequence the hub has been told
// about; 0 if none.
func (h *Hub) Latest(org kernel.ID) int64 {
	if s, ok := h.orgs.Load(org); ok {
		return s.(*orgSequence).state.Load().latest
	}
	return 0
}

// Wait blocks until the organisation's latest sequence is greater than
// after and returns that sequence, at once if it already is. If ctx has
// ended or ends first, it returns context.Cause(ctx).
func (h *Hub) Wait(ctx context.Context, org kernel.ID, after int64) (int64, error) {
	if ctx.Err() != nil {
		return 0, context.Cause(ctx)
	}
	s := h.sequence(org)
	for {
		if ctx.Err() != nil {
			return 0, context.Cause(ctx)
		}
		state := s.state.Load()
		if state.latest > after {
			return state.latest, nil
		}
		if h.afterWaitRead != nil {
			h.afterWaitRead()
		}
		// The snapshot already owns the channel the next raise closes,
		// even when that raise precedes accounting or the select below.
		s.waiters.Add(1)
		select {
		case <-state.changed:
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
	if s, ok := h.orgs.Load(org); ok {
		return int(s.(*orgSequence).waiters.Load())
	}
	return 0
}

// Connection names who holds a stream: the organisation from the URL, and
// the account and session that authenticated the request.
type Connection struct {
	Organization, Account, Session kernel.ID
}

// Register adds a connection unless the process already holds maxStreams
// connections or its account holds limit connections, returning
// ErrTooManyConnections and nothing to unregister, or the hub is shutting
// down (ErrShutdown). Both checks and the addition happen under one lock,
// so simultaneous registrations cannot overshoot either limit; an account
// limit below 1 refuses every registration.
//
// The returned context is derived from parent, normally the request's
// context. It ends at the first of: parent ending, CancelAccount or
// CancelSession matching the connection, CancelAll, or unregister running.
// context.Cause reports which (ErrAccountCancelled, ErrSessionEnded,
// ErrUnregistered, ErrShutdown, or parent's cause).
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
	if h.connections >= h.maxStreams || len(h.byAccount[c.Account]) >= limit {
		return nil, nil, ErrTooManyConnections
	}
	ctx, cancel := context.WithCancelCause(parent)
	r := &registration{organization: c.Organization, account: c.Account, session: c.Session, cancel: cancel}
	add(h.byAccount, c.Account, r)
	add(h.bySession, c.Session, r)
	add(h.byOrg, c.Organization, r)
	h.connections++
	var once sync.Once
	unregister = func() {
		once.Do(func() {
			h.mu.Lock()
			remove(h.byAccount, r.account, r)
			remove(h.bySession, r.session, r)
			remove(h.byOrg, r.organization, r)
			h.connections--
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
	return h.connections
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
