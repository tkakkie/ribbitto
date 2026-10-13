package realtime

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"
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
	stats   RecountStats
}

// RecountRegistration owns one running and one replaceable pending computation.
// Call Cancel on every stream exit; all methods are safe for concurrent use.
type RecountRegistration struct {
	q                          *RecountScheduler
	ctx                        context.Context
	cancel                     context.CancelFunc
	closed, running            bool
	consumed                   bool
	pending                    *recountRequest
	pendingSince, runningSince time.Time
	done                       chan RecountCompletion
}

// consume transfers a reservation to one stream, including concurrent callers.
// Refusing reuse must not cancel work belonging to the successful caller.
func (r *RecountRegistration) consume() bool {
	r.q.mu.Lock()
	defer r.q.mu.Unlock()
	if r.closed || r.consumed {
		return false
	}
	r.consumed = true
	return true
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

// RecountStats is a scheduler snapshot; counters accumulate for its lifetime.
// Ages are zero when no matching work exists, and durations measure dispatch to return.
type RecountStats struct {
	// SnapshotAt is sampled under the scheduler mutex.
	SnapshotAt time.Time
	// Closed reports whether admission has stopped.
	Closed bool
	// Pending includes successors waiting behind running computations.
	Pending int
	// Eligible counts pending registrations queued for a worker.
	Eligible int
	// OldestPendingAge preserves continuous pending time across replacement.
	OldestPendingAge time.Duration
	// RunningWorkers includes cancelled computations until actual return.
	RunningWorkers int
	// WaitingWorkers counts workers idle in the scheduler's condition wait.
	WaitingWorkers int
	// OldestRunningAge includes unfinished, cancelled computations.
	OldestRunningAge time.Duration
	// Registrations includes cancelled registrations retained by running work.
	Registrations int
	// WorkerLimit is the configured computation concurrency bound.
	WorkerLimit int
	// RegistrationLimit is the configured registration capacity.
	RegistrationLimit int
	// Completed counts actual returns, including errors, panics and cancelled work.
	Completed uint64
	// ProcessingDuration sums dispatch-to-return durations for Completed.
	ProcessingDuration time.Duration
	// DurationBuckets are disjoint counts: <=1ms, <=10ms, <=100ms, <=1s, <=10s, >10s.
	DurationBuckets [6]uint64
	// Cancelled counts pending work removed and running work first cancelled.
	// Replacement and repeated Cancel/Close do not increment it.
	Cancelled uint64
	// RefusedCapacity counts Register calls refused by the registration limit.
	RefusedCapacity uint64
	// RefusedClosed counts Register calls refused after closure, ahead of capacity.
	RefusedClosed uint64
	// RejectedSubmissions counts Submit calls rejected for nil compute or closure.
	RejectedSubmissions uint64
	// Errors counts returned errors and recovered panics from live registrations.
	// Classification checks registration cancellation under the scheduler mutex
	// when processing the return; cancellation after that point counts as a live error.
	Errors uint64
	// ErrorsAfterCancellation counts returned errors and recovered panics whose
	// registration was already cancelled when the return was processed under the
	// scheduler mutex. It is disjoint from Errors, regardless of the error value.
	ErrorsAfterCancellation uint64
}

// Stats copies counters and scans the bounded registrations under the mutex.
// It allocates no slices or per-request observations and is safe during Close.
func (q *RecountScheduler) Stats() RecountStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.stats
	s.SnapshotAt, s.Closed = time.Now(), q.closed
	s.Registrations, s.Eligible = len(q.streams), len(q.ready)
	for r := range q.streams {
		if r.pending != nil {
			s.Pending++
			s.OldestPendingAge = max(s.OldestPendingAge, s.SnapshotAt.Sub(r.pendingSince))
		}
		if r.running {
			s.OldestRunningAge = max(s.OldestRunningAge, s.SnapshotAt.Sub(r.runningSince))
		}
	}
	return s
}

// NewRecountScheduler requires positive worker and registration limits.
func NewRecountScheduler(workers, registrations int) *RecountScheduler {
	if workers < 1 || registrations < 1 {
		panic("realtime: recount scheduler requires positive limits")
	}
	q := &RecountScheduler{limit: registrations, streams: make(map[*RecountRegistration]struct{})}
	q.stats.WorkerLimit, q.stats.RegistrationLimit = workers, registrations
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
	if q.closed {
		q.stats.RefusedClosed++
		return nil
	}
	if len(q.streams) >= q.limit {
		q.stats.RefusedCapacity++
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
		q.stats.RejectedSubmissions++
		return false
	}
	if r.pending == nil {
		r.pendingSince = time.Now()
		if !r.running {
			q.ready = append(q.ready, r)
		}
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
			q.stats.WaitingWorkers++
			q.wake.Wait()
			q.stats.WaitingWorkers--
		}
		if q.closed {
			return
		}
		r := q.ready[0]
		q.ready = slices.Delete(q.ready, 0, 1)
		request := r.pending
		r.pending, r.running = nil, true
		start := time.Now()
		r.runningSince = start
		q.stats.RunningWorkers++
		q.mu.Unlock()
		out, err := request.run(r.ctx)
		duration := time.Since(start)
		q.mu.Lock()
		q.stats.RunningWorkers--
		q.stats.Completed++
		q.stats.ProcessingDuration += duration
		q.stats.DurationBuckets[durationBucket(duration)]++
		if err != nil {
			if r.closed {
				q.stats.ErrorsAfterCancellation++
			} else {
				q.stats.Errors++
			}
		}
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
	if r.pending != nil {
		r.q.stats.Cancelled++
	}
	if r.running {
		r.q.stats.Cancelled++
	}
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
