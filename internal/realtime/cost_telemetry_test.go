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

// Completeness is independent of trend: drain cannot replace posting samples.
func costQueueComplete(series []costQueueSample, duration time.Duration) bool {
	// Allow one lost tick plus scheduling jitter at either posting boundary,
	// but do not let drain samples replace missing posting coverage.
	const tolerance = 2 * time.Second
	var end time.Duration
	var first, previous time.Duration
	posting := 0
	for _, s := range series {
		if s.phase == "posting" {
			if posting == 0 {
				first = s.at
			} else if s.at <= previous || s.at-previous > 2*time.Second {
				return false
			}
			posting++
			previous = s.at
			end = max(end, s.at)
		}
	}
	if posting == 0 || first < 0 || first > tolerance || end < duration-tolerance || end > duration+tolerance {
		return false
	}
	if duration <= 0 {
		return false
	}
	for start := time.Duration(0); start+10*time.Second <= duration; start += 10 * time.Second {
		count := 0
		for _, s := range series {
			if s.phase == "posting" && s.at >= start && s.at < start+10*time.Second {
				count++
			}
		}
		if count < 8 {
			return false
		}
	}
	return true
}

func reportCostQueue(t *testing.T, series []costQueueSample, duration time.Duration, bound int) {
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
	t.Logf("queue summary [length oldest-ns coalesced cancelled]: min=%v max=%v final=%v window-stable=%s", low, high, series[len(series)-1].values, costQueueStability(series, duration, bound))
	for start := 60 * time.Second; start+240*time.Second <= duration; start += 60 * time.Second {
		t.Logf("queue sliding: earlier=[%s,%s) mean=%v later=[%s,%s) mean=%v", start, start+120*time.Second, costQueueMean(series, start, start+120*time.Second), start+120*time.Second, start+240*time.Second, costQueueMean(series, start+120*time.Second, start+240*time.Second))
	}
	if duration >= 600*time.Second {
		t.Logf("queue cumulative: earlier=[1m,4m) mean=%v later=[7m,10m) mean=%v bound=%d", costQueueMean(series, 60*time.Second, 240*time.Second), costQueueMean(series, 420*time.Second, 600*time.Second), bound)
	}
	for _, width := range []time.Duration{10 * time.Second, 30 * time.Second} {
		rise, at := costQueueSpike(series, duration, width)
		t.Logf("queue spike: width=%s rise[length oldest-ns]=%v earlier-offsets=%v", width, rise, at)
	}
}

func TestCostQueueGrowthDisqualifies(t *testing.T) {
	for _, metric := range []int{0, 1} {
		r := stepResult{postingDuration: 600 * time.Second, queueBound: 1000, scheduled: 300, completed: 300, deliveries: 300}
		for second := range 601 {
			s := costQueueSample{at: time.Duration(second) * time.Second, phase: "posting", values: [4]int64{10, 10}}
			s.values[metric] = int64(second + 1)
			if metric == 1 {
				s.values[metric] *= int64(time.Millisecond)
			}
			r.queue = append(r.queue, s)
		}
		if !costTargets(r) {
			t.Fatal("growing queue changed the separate latency verdict")
		}
		if costRecommendable(r) {
			t.Fatal("growing backlog accepted despite passing latency")
		}
		r.queue = append(r.queue, costQueueSample{at: 610 * time.Second, phase: "drain"})
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

func TestCostRecommendationRequiresTelemetry(t *testing.T) {
	sparse := []costQueueSample{
		{at: 0, phase: "posting"}, {at: 10 * time.Second, phase: "posting"},
		{at: 20 * time.Second, phase: "posting"}, {at: 30 * time.Second, phase: "posting"},
	}
	var stable []costQueueSample
	for second := range 600 {
		stable = append(stable, costQueueSample{at: time.Duration(second) * time.Second, phase: "posting"})
	}
	stable = append(stable, costQueueSample{at: 600 * time.Second, phase: "drain"})
	negativeStart := append([]costQueueSample(nil), stable...)
	negativeStart[0].at = -time.Second
	duplicate := append([]costQueueSample{stable[0]}, stable...)
	reordered := append([]costQueueSample(nil), stable...)
	reordered[10], reordered[11] = reordered[11], reordered[10]
	var overrun []costQueueSample
	for second := range 605 {
		overrun = append(overrun, costQueueSample{at: time.Duration(second) * time.Second, phase: "posting"})
	}
	without := func(skip func(int) bool) []costQueueSample {
		var series []costQueueSample
		for i, s := range stable {
			if !skip(i) {
				series = append(series, s)
			}
		}
		return series
	}
	for _, arm := range []string{"sync40", "async40", "async10+30"} {
		for _, tt := range []struct {
			name     string
			series   []costQueueSample
			duration time.Duration
			want     bool
		}{
			{"nil", nil, 600 * time.Second, false}, {"empty", []costQueueSample{}, 600 * time.Second, false},
			{"short", sparse[:3], 30 * time.Second, false},
			{"drain-only", []costQueueSample{{at: 600 * time.Second, phase: "drain"}}, 600 * time.Second, false},
			{"missing-bucket", []costQueueSample{sparse[0], sparse[2], sparse[3]}, 30 * time.Second, false},
			{"four-points", sparse, 30 * time.Second, false},
			{"missing-duration", stable, 0, false},
			{"negative-start", negativeStart, 600 * time.Second, false},
			{"late-start", stable[3:], 600 * time.Second, false},
			{"early-stop", without(func(i int) bool { return i > 400 && i < 600 }), 600 * time.Second, false},
			{"posting-overrun", overrun, 600 * time.Second, false},
			{"duplicate", duplicate, 600 * time.Second, false},
			{"reordered", reordered, 600 * time.Second, false},
			{"missing-stretch", without(func(i int) bool { return i >= 10 && i < 25 }), 600 * time.Second, false},
			// These isolated omissions keep gaps at two seconds but leave only
			// seven samples in the planned bucket [30s,40s).
			{"thin-bucket", without(func(i int) bool { return i == 31 || i == 33 || i == 35 }), 600 * time.Second, false},
			{"stable", stable, 600 * time.Second, true},
			{"tolerated-start", stable[2:], 600 * time.Second, true},
			{"tolerated-end", without(func(i int) bool { return i == 599 }), 600 * time.Second, true},
			{"tolerated-gaps-and-bucket", without(func(i int) bool { return i == 31 || i == 33 }), 600 * time.Second, true},
		} {
			t.Run(arm+"/"+tt.name, func(t *testing.T) {
				r := stepResult{arm: arm, queue: tt.series, postingDuration: tt.duration, queueBound: 1000, scheduled: 300, completed: 300, deliveries: 300}
				if !costTargets(r) {
					t.Fatal("telemetry changed the separate latency verdict")
				}
				if got := costRecommendable(r); got != tt.want {
					t.Fatalf("recommendation eligibility = %t, want %t for %s arm telemetry", got, tt.want, tt.name)
				}
			})
		}
	}
	for _, reference := range []string{"10", "20", "30", "40", "gate10"} {
		t.Run(reference, func(t *testing.T) {
			for _, series := range [][]costQueueSample{nil, {}} {
				r := stepResult{queue: series, scheduled: 300, completed: 300, deliveries: 300}
				if !costRecommendable(r) {
					t.Fatal("synchronous reference without a queue rejected")
				}
			}
		})
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
