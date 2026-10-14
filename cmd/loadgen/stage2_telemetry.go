package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

func stage2MetricsSections(raw json.RawMessage) (map[string]json.RawMessage, error) {
	fields := []string{"database", "pool", "runtime", "streams", "scheduler", "delivery"}
	sections, err := requiredObject(raw, fields...)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if _, err := requiredObject(sections[field]); err != nil {
			return nil, fmt.Errorf("metrics section %s: %w", field, err)
		}
	}
	return sections, nil
}

func stage2ReadMetrics(ctx context.Context, t transport) (json.RawMessage, error) {
	if t.origin.Scheme != "http" {
		return nil, fmt.Errorf("stage-2 metrics require loopback HTTP")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.origin.JoinPath("metrics").String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building metrics request: %w", err)
	}
	r, err := t.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("reading metrics: %w", err)
	}
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(r.Body, stage2ProofBodyLimit+1))
	if err != nil || r.StatusCode != http.StatusOK || len(raw) > stage2ProofBodyLimit {
		return raw, fmt.Errorf("metrics status=%d bytes=%d: %w", r.StatusCode, len(raw), errors.Join(err, fmt.Errorf("unusable metrics response")))
	}
	return raw, nil
}

// The terminal boundary supplies counter deltas, never an extra occupancy sample.
// Delayed scrapes leave holes; catching up must not fabricate per-second evidence.
func stage2SampleMetrics(ctx context.Context, start time.Time, now func() time.Time, wait func(context.Context, time.Duration) bool, fetch func(context.Context) (json.RawMessage, error)) ([]stage2Sample, error) {
	samples := []stage2Sample{}
	var problems []error
	for second := 0; second <= fixedStage2().WindowSeconds; second++ {
		at := start.Add(time.Duration(second) * time.Second)
		if !wait(ctx, max(0, at.Sub(now()))) || ctx.Err() != nil {
			return samples, errors.Join(append(problems, fmt.Errorf("sampling stopped: %v", ctx.Err()))...)
		}
		offset := now().Sub(start)
		withinSlot := offset < time.Duration(second+1)*time.Second
		if second == 0 || second == fixedStage2().WindowSeconds {
			withinSlot = offset <= time.Duration(second)*time.Second+2*time.Second
		}
		// A delayed opening scrape may occupy a later second, but cannot be
		// reused as another sample when the scheduler catches up.
		if !withinSlot || (len(samples) > 0 && offset.Nanoseconds() <= samples[len(samples)-1].OffsetNS) {
			problems = append(problems, fmt.Errorf("missed metrics slot %d", second))
			continue
		}
		raw, err := fetch(ctx)
		if err == nil {
			_, err = stage2MetricsSections(raw)
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("metrics slot %d: %w", second, err))
			continue
		}
		phase := "traffic"
		if second == fixedStage2().WindowSeconds {
			phase = "boundary"
		}
		samples = append(samples, stage2Sample{offset.Nanoseconds(), phase, slices.Clone(raw)})
	}
	return samples, errors.Join(problems...)
}

type stage2TelemetryRange struct {
	StartNS, EndNS, ElapsedNS int64
	Samples, Missing          int
	BoundaryCovered           bool
	Delta                     map[string]int64
	ResetOffsetsNS            []int64
	SampledMaxNotExactPeak    map[string]int64
	MeanEmptyWaitNS           float64
	WaitingPressure           float64
}

func stage2PoolEvidence(raw json.RawMessage) (map[string]int64, error) {
	sections, err := stage2MetricsSections(raw)
	if err != nil {
		return nil, err
	}
	pool, err := requiredObject(sections["pool"])
	if err != nil {
		return nil, err
	}
	values := map[string]int64{}
	for _, key := range []string{"acquire_count", "acquire_duration_ns", "empty_acquire_count", "empty_acquire_wait_ns", "canceled_acquire_count", "acquired_conns", "idle_conns", "total_conns", "max_conns"} {
		var value int64
		if string(pool[key]) == "null" || json.Unmarshal(pool[key], &value) != nil || value < 0 {
			return nil, fmt.Errorf("missing or invalid pool.%s", key)
		}
		values[key] = value
	}
	return values, nil
}

// Derive whole-window and all ten planned buckets from retained snapshots.
// Missing boundaries and interior resets remain explicit even if net deltas grow.
func stage2AggregateTelemetry(samples []stage2Sample) ([]stage2TelemetryRange, error) {
	end := int64(fixedStage2().WindowSeconds) * int64(time.Second)
	var retained []stage2Sample
	var pools []map[string]int64
	for _, s := range samples {
		if s.Phase == "drain" {
			continue
		}
		if s.OffsetNS < 0 || s.OffsetNS > end+int64(2*time.Second) ||
			(s.Phase != "traffic" && s.Phase != "boundary") ||
			(s.Phase == "traffic" && s.OffsetNS >= end) ||
			(s.Phase == "boundary" && s.OffsetNS < end) ||
			(len(retained) > 0 && s.OffsetNS <= retained[len(retained)-1].OffsetNS) {
			return nil, fmt.Errorf("invalid metrics offset or phase")
		}
		pool, err := stage2PoolEvidence(s.Metrics)
		if err != nil {
			return nil, fmt.Errorf("metrics at %dns: %w", s.OffsetNS, err)
		}
		retained, pools = append(retained, s), append(pools, pool)
	}
	ranges := []stage2TelemetryRange{{StartNS: 0, EndNS: end}}
	for at := int64(0); at < end; at += int64(time.Minute) {
		ranges = append(ranges, stage2TelemetryRange{StartNS: at, EndNS: at + int64(time.Minute)})
	}
	for n := range ranges {
		r := &ranges[n]
		r.Delta, r.SampledMaxNotExactPeak, r.ResetOffsetsNS = map[string]int64{}, map[string]int64{}, []int64{}
		first, last := -1, -1
		seen := map[int64]bool{}
		for i, s := range retained {
			if s.OffsetNS < r.StartNS {
				continue
			}
			if first < 0 {
				first = i
			}
			if s.OffsetNS >= r.EndNS {
				last = i
				break
			}
			if s.Phase != "traffic" {
				continue
			}
			r.Samples++
			seen[s.OffsetNS/int64(time.Second)] = true
			for _, key := range []string{"acquired_conns", "idle_conns", "total_conns", "max_conns"} {
				r.SampledMaxNotExactPeak[key] = max(r.SampledMaxNotExactPeak[key], pools[i][key])
			}
		}
		r.Missing = int((r.EndNS-r.StartNS)/int64(time.Second)) - len(seen)
		if first >= 0 && last >= 0 {
			startCovered := retained[first].OffsetNS < r.StartNS+int64(time.Second)
			endCovered := retained[last].OffsetNS < r.EndNS+int64(time.Second)
			if r.StartNS == 0 {
				startCovered = retained[first].OffsetNS <= int64(2*time.Second)
			}
			if r.EndNS == end {
				endCovered = retained[last].OffsetNS <= end+int64(2*time.Second)
			}
			r.BoundaryCovered = startCovered && endCovered
		}
		resetEnd := last
		if resetEnd < 0 {
			resetEnd = len(pools) - 1
		}
		for i := first + 1; first >= 0 && i <= resetEnd; i++ {
			for _, key := range []string{"acquire_count", "acquire_duration_ns", "empty_acquire_count", "empty_acquire_wait_ns", "canceled_acquire_count"} {
				if pools[i][key] < pools[i-1][key] {
					r.ResetOffsetsNS = append(r.ResetOffsetsNS, retained[i].OffsetNS)
					break
				}
			}
		}
		if !r.BoundaryCovered {
			continue
		}
		r.ElapsedNS = retained[last].OffsetNS - retained[first].OffsetNS
		for _, key := range []string{"acquire_count", "acquire_duration_ns", "empty_acquire_count", "empty_acquire_wait_ns", "canceled_acquire_count"} {
			r.Delta[key] = pools[last][key] - pools[first][key]
		}
		if len(r.ResetOffsetsNS) == 0 {
			r.WaitingPressure = float64(r.Delta["empty_acquire_wait_ns"]) / float64(r.ElapsedNS)
			if r.Delta["empty_acquire_count"] > 0 {
				r.MeanEmptyWaitNS = float64(r.Delta["empty_acquire_wait_ns"]) / float64(r.Delta["empty_acquire_count"])
			}
		}
	}
	return ranges, nil
}
