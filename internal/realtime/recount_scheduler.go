package realtime

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
)

// RecountScheduler bounds concurrent computations and registered streams.
// Stream.Run optionally uses it; the server composition root leaves it unwired.
type RecountScheduler struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	wake    *sync.Cond
	limit   int
	closed  bool
	streams map[*RecountRegistration]struct{}
	ready   []*RecountRegistration
}

// RecountRegistration owns one running and one replaceable pending computation.
// Call Cancel on every stream exit; all methods are safe for concurrent use.
type RecountRegistration struct {
	q               *RecountScheduler
	ctx             context.Context
	cancel          context.CancelFunc
	closed, running bool
	pending         *recountRequest
	done            chan RecountCompletion
}

type recountRequest struct {
	version uint64
	compute func(context.Context) (Outgoing, error)
}

// RecountCompletion carries computation output, without authorizing or sending it.
type RecountCompletion struct {
	// Version identifies the submitted request, not a scheduler sequence.
	Version uint64
	// Outgoing is computation output for the writer to authorize before sending.
	Outgoing Outgoing
	// Err is the computation error, or a recovered panic with its stack trace.
	// The scheduler never retries it; a panic does not retire the worker.
	Err error
}

// NewRecountScheduler requires positive worker and registration limits.
func NewRecountScheduler(workers, registrations int) *RecountScheduler {
	if workers < 1 || registrations < 1 {
		panic("realtime: recount scheduler requires positive limits")
	}
	q := &RecountScheduler{limit: registrations, streams: make(map[*RecountRegistration]struct{})}
	q.wake = sync.NewCond(&q.mu)
	for range workers {
		q.wg.Go(q.work)
	}
	return q
}

// Register returns nil when capacity is retained or the scheduler is closed.
// Cancellation is explicit, independent of the caller's request context.
func (q *RecountScheduler) Register() *RecountRegistration {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || len(q.streams) >= q.limit {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &RecountRegistration{q: q, ctx: ctx, cancel: cancel, done: make(chan RecountCompletion, 1)}
	q.streams[r] = struct{}{}
	return r
}

// Submit replaces pending work without changing its eligibility position.
// Versions are caller-owned labels; compute must be non-nil and never send.
// It returns false after cancellation or scheduler closure.
func (r *RecountRegistration) Submit(version uint64, compute func(context.Context) (Outgoing, error)) bool {
	q := r.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if r.closed || q.closed || compute == nil {
		return false
	}
	if r.pending == nil && !r.running {
		q.ready = append(q.ready, r)
	}
	r.pending = &recountRequest{version, compute}
	q.wake.Signal()
	return true
}

// Completions is a one-slot mailbox; an unread result is replaced on completion.
// Cancel or scheduler Close drains and closes it, suppressing late results:
// subsequent receives return a zero RecountCompletion and ok == false.
// A result already received remains caller-owned.
func (r *RecountRegistration) Completions() <-chan RecountCompletion { return r.done }

func (q *RecountScheduler) work() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		for len(q.ready) == 0 && !q.closed {
			q.wake.Wait()
		}
		if q.closed {
			return
		}
		r := q.ready[0]
		q.ready = slices.Delete(q.ready, 0, 1)
		request := r.pending
		r.pending, r.running = nil, true
		q.mu.Unlock()
		out, err := request.run(r.ctx)
		q.mu.Lock()
		r.running = false
		if r.closed {
			delete(q.streams, r)
			continue
		}
		// Draining under the producer lock makes the send nonblocking,
		// even when a consumer never receives its completions.
		select {
		case <-r.done:
		default:
		}
		r.done <- RecountCompletion{request.version, out, err}
		if r.pending != nil {
			q.ready = append(q.ready, r)
			q.wake.Signal()
		}
	}
}

func (request *recountRequest) run(ctx context.Context) (out Outgoing, err error) {
	// Recover at the computation boundary so worker bookkeeping still runs.
	defer func() {
		if value := recover(); value != nil {
			out = Outgoing{}
			err = fmt.Errorf("recount computation panicked: %v\n%s", value, debug.Stack())
		}
	}()
	return request.compute(ctx)
}

// Cancel removes eligibility, cancels computation and drops buffered/late results.
// An uncooperative running computation retains its slot until it returns.
func (r *RecountRegistration) Cancel() {
	r.q.mu.Lock()
	defer r.q.mu.Unlock()
	r.cancelLocked()
}

func (r *RecountRegistration) cancelLocked() {
	if r.closed {
		return
	}
	r.closed = true
	r.cancel()
	r.pending = nil
	r.q.ready = slices.DeleteFunc(r.q.ready, func(candidate *RecountRegistration) bool { return candidate == r })
	if !r.running {
		delete(r.q.streams, r)
	}
	select {
	case <-r.done:
	default:
	}
	close(r.done)
}

// Close refuses new work, cancels registrations and waits for actual returns.
// It is idempotent; computations must eventually return for Close to finish.
func (q *RecountScheduler) Close() {
	q.mu.Lock()
	q.closed = true
	for r := range q.streams {
		r.cancelLocked()
	}
	q.wake.Broadcast()
	q.mu.Unlock()
	q.wg.Wait()
}
