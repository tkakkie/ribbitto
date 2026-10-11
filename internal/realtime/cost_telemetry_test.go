package realtime_test

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// Both pools share one tracer, giving an aggregate acquire/release peak rather
// than adding independently observed peaks that need not happen together.
type costPoolSnapshot struct {
	empty, cancelled int64
	wait             time.Duration
}

func costPoolStats(normal, recount *pgxpool.Pool) costPoolSnapshot {
	var out costPoolSnapshot
	for _, pool := range []*pgxpool.Pool{normal, recount} {
		s := pool.Stat()
		out.empty += s.EmptyAcquireCount()
		out.cancelled += s.CanceledAcquireCount()
		out.wait += s.EmptyAcquireWaitTime()
		if normal == recount {
			break
		}
	}
	return out
}
func (s costPoolSnapshot) EmptyAcquireCount() int64            { return s.empty }
func (s costPoolSnapshot) CanceledAcquireCount() int64         { return s.cancelled }
func (s costPoolSnapshot) EmptyAcquireWaitTime() time.Duration { return s.wait }

type costQueueSample struct {
	at    time.Duration
	phase string
	// Order: pending length, oldest waiting nanoseconds, coalesced, cancelled.
	values [4]int64
}

func costQueuePoint(q *costQueue, start time.Time, phase string) costQueueSample {
	s := q.stats()
	return costQueueSample{time.Since(start), phase, [4]int64{int64(s.queued), int64(s.oldest), int64(s.coalesced), int64(s.cancelled)}}
}

// Joining the sampler before reading its slice keeps telemetry out of the
// stream writer and includes a final drain snapshot before disconnect.
func sampleCostQueue(ctx context.Context, q *costQueue, start time.Time, duration time.Duration) func() []costQueueSample {
	if q == nil {
		return func() []costQueueSample { return nil }
	}
	stop, joined := make(chan struct{}), make(chan struct{})
	series := []costQueueSample{costQueuePoint(q, start, "posting")}
	go func() {
		defer close(joined)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-tick.C:
				phase := "posting"
				if time.Since(start) >= duration {
					phase = "drain"
				}
				series = append(series, costQueuePoint(q, start, phase))
			}
		}
	}()
	return func() []costQueueSample {
		close(stop)
		<-joined
		return append(series, costQueuePoint(q, start, "drain"))
	}
}

// Reject increasing ten-second means in either length or age over the last
// thirty seconds of posting. Age catches a bounded queue stuck at capacity;
// eventual drain cannot erase the overload observed while posting continued.
func costQueueGrowing(series []costQueueSample) bool {
	var end time.Duration
	for _, s := range series {
		if s.phase == "posting" {
			end = max(end, s.at)
		}
	}
	if end < 30*time.Second {
		return len(series) > 0 // Short or missing series cannot qualify an async arm.
	}
	var sums [3][2]float64
	var counts [3]int
	for _, s := range series {
		offset := s.at - (end - 30*time.Second)
		if s.phase != "posting" || offset < 0 || offset >= 30*time.Second {
			continue
		}
		bucket := int(offset / (10 * time.Second))
		counts[bucket]++
		for metric := range 2 {
			sums[bucket][metric] += float64(s.values[metric])
		}
	}
	for _, count := range counts {
		if count == 0 {
			return true // Incomplete telemetry cannot establish stability.
		}
	}
	for metric := range 2 {
		if sums[0][metric]/float64(counts[0]) < sums[1][metric]/float64(counts[1]) &&
			sums[1][metric]/float64(counts[1]) < sums[2][metric]/float64(counts[2]) {
			return true
		}
	}
	return false
}

func reportCostQueue(t *testing.T, series []costQueueSample) {
	if len(series) == 0 {
		return
	}
	low, high := series[0].values, series[0].values
	for _, s := range series {
		for i, value := range s.values {
			low[i], high[i] = min(low[i], value), max(high[i], value)
		}
		t.Logf("queue: at=%s phase=%s length=%d oldest=%s coalesced=%d cancelled=%d", s.at, s.phase, s.values[0], time.Duration(s.values[1]), s.values[2], s.values[3])
	}
	t.Logf("queue summary [length oldest-ns coalesced cancelled]: min=%v max=%v final=%v growing=%t", low, high, series[len(series)-1].values, costQueueGrowing(series))
}

func TestCostQueueGrowthDisqualifies(t *testing.T) {
	for _, metric := range []int{0, 1} {
		r := stepResult{scheduled: 300, completed: 300, deliveries: 300}
		for second := range 61 {
			s := costQueueSample{at: time.Duration(second) * time.Second, phase: "posting", values: [4]int64{10, 10}}
			s.values[metric] = int64(second + 1)
			r.queue = append(r.queue, s)
		}
		if !costTargets(r) {
			t.Fatal("growing queue changed the separate latency verdict")
		}
		if costRecommendable(r) {
			t.Fatal("growing backlog accepted despite passing latency")
		}
		r.queue = append(r.queue, costQueueSample{at: 70 * time.Second, phase: "drain"})
		if costRecommendable(r) {
			t.Fatal("eventual drain erased sustained posting growth")
		}
		for i := range r.queue {
			r.queue[i].values = [4]int64{10, 10}
		}
		if !costRecommendable(r) {
			t.Fatal("stable queue rejected")
		}
	}
}

// The sync comparison uses the identical scheduler but holds its writer until
// completion. Plain synchronous pool sizing remains a separate reference.
type costSynchronousQueue struct {
	realtime.SidebarSource
	queue *costQueue
	work  *costQueueStream
}

func (s costSynchronousQueue) Recount(ctx context.Context, sub realtime.Subscription) (realtime.Outgoing, error) {
	if !s.queue.submit(s.work, costQueueRequest{compute: func(ctx context.Context) (realtime.Outgoing, error) {
		return s.SidebarSource.Recount(ctx, sub)
	}}) {
		return realtime.Outgoing{}, fmt.Errorf("synchronous recount admission refused")
	}
	select {
	case <-ctx.Done():
		return realtime.Outgoing{}, context.Cause(ctx)
	case result := <-s.work.done:
		return result.out, result.err
	}
}

func TestCostQueueTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(1, 2), newCostQueueGate()
		defer q.shutdown()
		active, pending := q.register(), q.register()
		q.submit(active, g.request(1))
		g.want(t, 1)
		q.submit(pending, g.request(2))
		finish := sampleCostQueue(t.Context(), q, time.Now(), time.Second)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		q.submit(pending, g.request(3))
		q.cancel(pending)
		series := finish()
		if len(series) != 4 || series[0].values[0] != 1 || series[1].values[1] != int64(time.Second) || series[1].phase != "drain" || series[3].values != [4]int64{0, 0, 1, 1} {
			t.Fatalf("posting/drain telemetry lost scheduler counters: %+v", series)
		}
	})
}
