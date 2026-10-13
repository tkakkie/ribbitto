package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"time"
)

type stage2Slot struct {
	Operation, Endpoint string
	LogicalID           uint64
	At, Expires         time.Time
}
type stage2Schedule struct {
	slots []stage2Slot
	next  int
}

func newStage2Schedule(group stage2Group, seed uint64, start time.Time) (*stage2Schedule, error) {
	p := fixedStage2()
	if !slices.Contains(p.Groups[:], group) {
		return nil, fmt.Errorf("unknown stage-2 group")
	}
	s := &stage2Schedule{}
	rates := []int{group.PostsPerSecond, group.ReadsPerSecond, group.PagesPerSecond, group.ReplacementsPerSecond}
	for kind, operation := range []string{"post", "read", "page", "replacement"} {
		rate := rates[kind]
		rng := rand.New(rand.NewPCG(seed, uint64(kind+1)))
		phase := time.Duration(rng.Int64N(int64(time.Second) / int64(rate)))
		first := rng.IntN(2)
		for i := 0; i < rate*p.WindowSeconds; i++ {
			// Compute from the index: truncated periods must not accumulate drift.
			at := start.Add(phase + time.Duration(i)*time.Second/time.Duration(rate))
			expires := start.Add(min(phase+time.Duration(i+1)*time.Second/time.Duration(rate), time.Duration(p.WindowSeconds)*time.Second))
			s.slots = append(s.slots, stage2Slot{operation, []string{"feed", "topic"}[(i+first)%2], uint64(i), at, expires})
		}
	}
	slices.SortStableFunc(s.slots, func(a, b stage2Slot) int { return a.At.Compare(b.At) })
	return s, nil
}

type stage2Observation struct {
	Slot                stage2Slot
	Dispatch, Completed time.Time
	Outcome             string
	Attempts            []stage2Attempt
}

// Poll consumes every due slot. Admission must be nonblocking and launch work
// independently; neither a busy operation nor a stalled generator shifts slots.
func (s *stage2Schedule) poll(now time.Time, admit func(stage2Slot) bool) []stage2Observation {
	var due []stage2Observation
	for s.next < len(s.slots) && !s.slots[s.next].At.After(now) {
		slot := s.slots[s.next]
		s.next++
		o := stage2Observation{Slot: slot, Dispatch: now}
		if !now.Before(slot.Expires) || !admit(slot) {
			o.Dispatch, o.Completed, o.Outcome = time.Time{}, now, "missed"
		}
		due = append(due, o)
	}
	return due
}

// Replacement drivers can retain each attempt here, passing the actual offline
// start. This seam does not reconnect, consume SSE or evaluate recovery gates.
func (o *stage2Observation) finish(start, dispatch, end, offline time.Time, attempt uint64, status int, outcome string) {
	if len(o.Attempts) == 0 {
		o.Dispatch = dispatch
	}
	o.Completed, o.Outcome = end, outcome
	outage := time.Duration(0)
	if o.Slot.Operation == "replacement" && !offline.IsZero() {
		outage = end.Sub(offline)
	}
	o.Attempts = append(o.Attempts, stage2Attempt{o.Slot.Operation, o.Slot.Endpoint, outcome,
		o.Slot.LogicalID, attempt, status, dispatch.Sub(start).Nanoseconds(), outage.Nanoseconds()})
}

// Retain at most 1 MiB for proof, matching main.go's POST drain cap (page
// parsing there allows 16 MiB). Excess bytes are drained, not an outcome failure;
// a verifier that uses response bytes must work with only this prefix.
const stage2ProofBodyLimit = 1 << 20

// Stage-2 feed POSTs and reads use HX-Request: true. Both places share handlers:
// internal/web/channel.go:301-304 returns 200 for feed POSTs, 303 for topic POSTs;
// internal/web/read.go:65-68 returns 204 for reads, with no response-body proof;
// internal/web/channel.go:108-112 returns 200 for page GETs;
// internal/web/stream.go:153-161 returns 200 for SSE connects. Later slices drive
// replacement streams; this HTTP helper does not measure SSE establishment.
func stage2SuccessStatus(slot stage2Slot) int {
	switch slot.Operation {
	case "post":
		if slot.Endpoint == "topic" {
			return http.StatusSeeOther
		}
		return http.StatusOK
	case "read":
		return http.StatusNoContent
	case "page", "replacement":
		return http.StatusOK
	default:
		return 0
	}
}

// Exactly one HTTP exchange; RoundTrip avoids redirect-generated POST retries.
// changed must confirm persisted read advancement using proof supplied by later
// slices (e.g. read state), independently of the empty 204 response body.
func stage2HTTP(ctx context.Context, rt http.RoundTripper, req *http.Request, o stage2Observation, start time.Time, now func() time.Time, changed func([]byte) bool) stage2Observation {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	o.Dispatch = now()
	response, err := rt.RoundTrip(req.WithContext(ctx))
	status, outcome := 0, "failed"
	var body []byte
	var end time.Time
	if err == nil {
		status = response.StatusCode
		body, err = io.ReadAll(io.LimitReader(response.Body, stage2ProofBodyLimit))
		if err == nil && len(body) == stage2ProofBodyLimit {
			_, err = io.Copy(io.Discard, response.Body)
		}
		// Stop at complete-body consumption, before closing or read verification.
		end = now()
		closeErr := response.Body.Close()
		if err == nil {
			err = closeErr
		}
	} else {
		end = now()
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		outcome = "timeout"
	} else if err == nil && status == stage2SuccessStatus(o.Slot) {
		outcome = "succeeded"
		if o.Slot.Operation == "read" {
			outcome = "unverified"
			if changed != nil {
				outcome = "noop"
				if changed(body) {
					outcome = "changed"
				}
			}
		}
	}
	o.finish(start, o.Dispatch, end, time.Time{}, 1, status, outcome)
	return o
}

// Exact nearest-rank percentiles of all supplied samples, including failures.
// Failure and miss counters are required alongside percentiles.
func stage2Percentiles(values []time.Duration) stage2Latency {
	n := len(values)
	if n == 0 {
		return stage2Latency{}
	}
	values = slices.Clone(values)
	slices.Sort(values)
	percentile := func(p int) float64 { return float64(values[(n*p+99)/100-1]) / float64(time.Millisecond) }
	return stage2Latency{uint64(n), percentile(50), percentile(95), percentile(99)}
}

// Summarize only after all scheduled slots have been polled and admitted work
// has settled. Timeouts and no-ops are subsets of Failed; Missed is separate.
// HTTP and dispatch lateness include every executed operation, including failed,
// timed-out, no-op and unverified ones, but exclude misses that never executed.
// Misses retain Scheduled/Missed counts and scheduled-to-detection elapsed time
// in ScheduledToResponse; the completeness gate must reject any miss.
func stage2Summarize(observations []stage2Observation) ([]stage2Operation, []stage2Attempt) {
	ops, attempts := []stage2Operation{}, []stage2Attempt{}
	for _, kind := range []string{"post", "read", "page", "replacement"} {
		for _, endpoint := range []string{"feed", "topic"} {
			op := stage2Operation{Operation: kind, Endpoint: endpoint}
			var httpTimes, totalTimes, lateTimes []time.Duration
			for _, o := range observations {
				if o.Slot.Operation != kind || o.Slot.Endpoint != endpoint {
					continue
				}
				op.Scheduled++
				if o.Outcome == "missed" {
					op.Missed++
				} else {
					op.Executed++
					httpTimes = append(httpTimes, o.Completed.Sub(o.Dispatch))
					lateTimes = append(lateTimes, o.Dispatch.Sub(o.Slot.At))
					switch o.Outcome {
					case "succeeded", "changed":
						op.Succeeded++
						if kind == "read" {
							op.Changed++
						}
					default:
						op.Failed++
						if o.Outcome == "noop" {
							op.Noop++
						}
						if o.Outcome == "timeout" {
							op.Timeouts++
						}
					}
				}
				totalTimes = append(totalTimes, o.Completed.Sub(o.Slot.At))
				attempts = append(attempts, o.Attempts...)
			}
			op.HTTP, op.ScheduledToResponse, op.DispatchLateness = stage2Percentiles(httpTimes), stage2Percentiles(totalTimes), stage2Percentiles(lateTimes)
			ops = append(ops, op)
		}
	}
	return ops, attempts
}
