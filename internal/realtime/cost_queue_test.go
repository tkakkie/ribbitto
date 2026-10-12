package realtime_test

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

// costQueue is a scheduler only: workers compute, but never send SSE frames.
// Registration cancellation is explicit so the future writer owns disconnects.
type costQueue struct {
	mu                   sync.Mutex
	wake                 *sync.Cond
	workers              sync.WaitGroup
	limit                int
	closed               bool
	streams              map[*costQueueStream]bool
	ready                []*costQueueStream
	coalesced, cancelled int
}

type costQueueStream struct {
	ctx             context.Context
	cancel          context.CancelFunc
	closed, running bool
	pending         *costQueueRequest
	waiting         time.Time
	done            chan costQueueResult
}

type costQueueRequest struct {
	version int64
	compute func(context.Context) (realtime.Outgoing, error)
}

type costQueueResult struct {
	version int64
	out     realtime.Outgoing
	err     error
}

type costQueueStats struct {
	queued, registered, coalesced, cancelled int
	oldest                                   time.Duration
}

func newCostQueue(workers, streams int) *costQueue {
	if workers < 1 || streams < 1 {
		panic("cost queue requires positive bounds")
	}
	q := &costQueue{limit: streams, streams: make(map[*costQueueStream]bool)}
	q.wake = sync.NewCond(&q.mu)
	for range workers {
		q.workers.Go(q.work)
	}
	return q
}

func (q *costQueue) register() *costQueueStream {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || len(q.streams) >= q.limit {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &costQueueStream{ctx: ctx, cancel: cancel, done: make(chan costQueueResult, 1)}
	q.streams[s] = true
	return s
}

func (q *costQueue) submit(s *costQueueStream, r costQueueRequest) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.streams[s] || s.closed || q.closed {
		return false
	}
	if s.pending != nil {
		q.coalesced++
	} else {
		s.waiting = time.Now()
		if !s.running {
			q.ready = append(q.ready, s)
		}
	}
	s.pending = &r
	q.wake.Broadcast()
	return true
}

func (q *costQueue) work() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		for len(q.ready) == 0 && !q.closed {
			q.wake.Wait()
		}
		if q.closed {
			return
		}
		s := q.ready[0]
		q.ready = slices.Delete(q.ready, 0, 1)
		r := s.pending
		s.pending, s.running = nil, true
		q.mu.Unlock()
		out, err := r.compute(s.ctx)
		q.mu.Lock()
		s.running = false
		if s.closed {
			delete(q.streams, s)
			continue
		}
		// A stalled consumer cannot pin a worker or grow a completion backlog.
		select {
		case <-s.done:
		default:
		}
		s.done <- costQueueResult{r.version, out, err}
		if s.pending != nil {
			// Only after returning is a busy stream eligible again, at the tail.
			q.ready = append(q.ready, s)
			q.wake.Broadcast()
		}
	}
}

func (q *costQueue) cancel(s *costQueueStream) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelLocked(s)
}

func (q *costQueue) cancelLocked(s *costQueueStream) {
	if !q.streams[s] || s.closed {
		return
	}
	s.closed = true
	s.cancel()
	if s.pending != nil {
		q.cancelled++
		s.pending = nil
		q.ready = slices.DeleteFunc(q.ready, func(candidate *costQueueStream) bool { return candidate == s })
	}
	if s.running {
		q.cancelled++
	} else {
		delete(q.streams, s)
	}
	select {
	case <-s.done:
	default:
	}
}

func (q *costQueue) shutdown() {
	q.mu.Lock()
	q.closed = true
	for s := range q.streams {
		q.cancelLocked(s)
	}
	q.wake.Broadcast()
	q.mu.Unlock()
	q.workers.Wait()
}

func (q *costQueue) stats() costQueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := costQueueStats{registered: len(q.streams), coalesced: q.coalesced, cancelled: q.cancelled}
	for s := range q.streams {
		if s.pending != nil {
			result.queued++
			result.oldest = max(result.oldest, time.Since(s.waiting))
		}
	}
	return result
}
