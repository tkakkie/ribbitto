package realtime

import (
	"context"
	"sync"
	"time"
)

// RecountObserver optionally aggregates scheduled writer delivery timing.
// Its zero value is ready; it retains no request identities, payloads or history.
type RecountObserver struct {
	mu    sync.Mutex
	stats RecountDeliveryStats
}

// RecountDuration aggregates disjoint buckets with the scheduler's bounds.
type RecountDuration struct {
	// Count is the number of samples.
	Count uint64 `json:"count"`
	// Total sums sample durations in nanoseconds.
	Total time.Duration `json:"total_ns"`
	// Buckets counts <=1ms, <=10ms, <=100ms, <=1s, <=10s, then >10s.
	Buckets [6]uint64 `json:"bucket_counts"`
}

// RecountDeliveryStats contains lifetime counters, independently of scheduler Stats.
type RecountDeliveryStats struct {
	// Submitted counts writer submissions, including refusals.
	Submitted uint64 `json:"submitted"`
	// Received counts completions received by the writer.
	Received uint64 `json:"received"`
	// Delivered counts only successful sends with an uncancelled context.
	Delivered uint64 `json:"delivered"`
	// Denied counts bound membership revocations.
	Denied uint64 `json:"denied"`
	// Failed counts computation, authorization-check, submission and send errors.
	Failed uint64 `json:"failed"`
	// Cancelled counts outstanding attempts abandoned on writer exit or cancellation.
	Cancelled uint64 `json:"cancelled"`
	// Queue measures submission to computation invocation, before watch capture.
	Queue RecountDuration `json:"queue"`
	// Compute measures invocation to return, including watch capture, panics and late returns.
	Compute RecountDuration `json:"compute"`
	// CompletionToSend measures actual return to successful send, including mailbox wait.
	CompletionToSend RecountDuration `json:"completion_to_send"`
	// SubmitToSend measures submission to successful send.
	SubmitToSend RecountDuration `json:"submit_to_send"`
}

// Stats returns a consistent, allocation-free copy of the aggregates.
func (o *RecountObserver) Stats() RecountDeliveryStats {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.stats
}

func durationBucket(d time.Duration) int {
	for i, bound := range [...]time.Duration{time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second} {
		if d <= bound {
			return i
		}
	}
	return 5
}

func (d *RecountDuration) add(sample time.Duration) {
	d.Count++
	d.Total += sample
	d.Buckets[durationBucket(sample)]++
}

type recountObservation struct {
	observer             *RecountObserver
	submitted, completed time.Time
}

func (o *RecountObserver) submit() *recountObservation {
	r := &recountObservation{observer: o, submitted: time.Now()}
	o.mu.Lock()
	o.stats.Submitted++
	o.mu.Unlock()
	return r
}

func (r *recountObservation) wrap(compute func(context.Context) (Outgoing, error)) func(context.Context) (Outgoing, error) {
	return func(ctx context.Context) (Outgoing, error) {
		started := time.Now()
		r.observer.mu.Lock()
		r.observer.stats.Queue.add(started.Sub(r.submitted))
		r.observer.mu.Unlock()
		defer func() {
			r.completed = time.Now() // The mailbox publishes this time to the writer.
			r.observer.mu.Lock()
			r.observer.stats.Compute.add(r.completed.Sub(started))
			r.observer.mu.Unlock()
		}()
		return compute(ctx)
	}
}

func (r *recountObservation) finish(outcome string) {
	if r == nil {
		return
	}
	now := time.Now()
	o := r.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	switch outcome {
	case "delivered":
		o.stats.Delivered++
		o.stats.CompletionToSend.add(now.Sub(r.completed))
		o.stats.SubmitToSend.add(now.Sub(r.submitted))
	case "denied":
		o.stats.Denied++
	case "failed":
		o.stats.Failed++
	case "cancelled":
		o.stats.Cancelled++
	}
}
