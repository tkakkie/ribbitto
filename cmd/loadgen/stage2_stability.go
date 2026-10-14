package main

import (
	"encoding/json"
	"fmt"
	"time"
)

type stage2QueuePoint struct {
	at     int64
	values [2]float64
}

// Read the configured capacity from effective metrics, never observed occupancy.
func stage2RegistrationBound(raw json.RawMessage) int {
	sections, err := stage2MetricsSections(raw)
	if err != nil {
		return 0
	}
	var scheduler struct {
		RegistrationLimit int `json:"registration_limit"`
	}
	if json.Unmarshal(sections["scheduler"], &scheduler) != nil {
		return 0
	}
	return scheduler.RegistrationLimit
}

// Stability's posting coverage is intentionally independent of pool completeness.
// The boundary scrape supplies counters, not a posting sample (slice 5a).
func stage2QueueStability(samples []stage2Sample, duration int64, bound int) stage2Verdict {
	v := stage2Verdict{"stability", "pass", map[string]float64{"duration_ns": float64(duration), "registration_bound": float64(bound)}}
	m := v.Measured
	second := int64(time.Second)
	var points []stage2QueuePoint
	incomplete := false
	fail := func(key string, at int64) { m[key] = float64(at); incomplete = true }
	for _, s := range samples {
		if s.Phase != "traffic" {
			continue
		}
		sections, err := stage2MetricsSections(s.Metrics)
		var fields map[string]json.RawMessage
		if err == nil {
			fields, err = requiredObject(sections["scheduler"])
		}
		point := stage2QueuePoint{at: s.OffsetNS}
		for i, key := range []string{"pending", "oldest_pending_age_ns"} {
			var value int64
			if err != nil || string(fields[key]) == "null" || json.Unmarshal(fields[key], &value) != nil || value < 0 {
				fail("invalid_scheduler_at_ns", s.OffsetNS)
			}
			point.values[i] = float64(value)
		}
		if len(points) > 0 {
			previous := points[len(points)-1].at
			if s.OffsetNS <= previous {
				fail("nonincreasing_at_ns", s.OffsetNS)
			}
			if s.OffsetNS-previous > 2*second {
				fail("gap_at_ns", s.OffsetNS)
			}
		}
		points = append(points, point)
	}
	m["posting_samples"] = float64(len(points))
	if len(points) == 0 {
		fail("missing_posting", 1)
	} else {
		first, last := points[0].at, points[len(points)-1].at
		m["first_ns"], m["last_ns"] = float64(first), float64(last)
		if first < 0 || first > 2*second {
			fail("start_uncovered_at_ns", first)
		}
		if last < duration-2*second || last > duration+2*second {
			fail("end_uncovered_at_ns", last)
		}
	}
	for start := int64(0); start+10*second <= duration; start += 10 * second {
		count := 0
		for _, p := range points {
			if p.at >= start && p.at < start+10*second {
				count++
			}
		}
		if count < 8 {
			fail(fmt.Sprintf("bucket_%ds_samples", start/second), int64(count))
		}
	}
	if duration <= 0 {
		incomplete = true
	}
	if !incomplete {
		m["complete"] = 1
	} else {
		m["complete"] = 0
	}
	mean := func(start, end int64) (result [2]float64) {
		count := 0
		for _, p := range points {
			if p.at >= start && p.at < end {
				count++
				for i := range result {
					result[i] += p.values[i]
				}
			}
		}
		if count > 0 {
			for i := range result {
				result[i] /= float64(count)
			}
		}
		return result
	}
	compare := func(start, middle, later, end int64) {
		earlier, after := mean(start, middle), mean(later, end)
		for i, metric := range []string{"length", "age_ns"} {
			prefix := fmt.Sprintf("means_%d_%d_%d_%ds.%s.", start/second, middle/second, later/second, end/second, metric)
			floor := .005 * float64(bound)
			if i == 1 {
				floor = float64(10 * time.Millisecond)
			}
			tolerance := max(.05*earlier[i], floor)
			m[prefix+"earlier"], m[prefix+"later"], m[prefix+"tolerance"] = earlier[i], after[i], tolerance
			if after[i]-earlier[i] > tolerance {
				v.State = "reject"
			}
		}
	}
	for start := 60 * second; start+240*second <= duration; start += 60 * second {
		compare(start, start+120*second, start+120*second, start+240*second)
	}
	compare(60*second, 240*second, 420*second, 600*second)
	for _, width := range []int64{10 * second, 30 * second} {
		rise, at := [2]float64{}, [2]int64{}
		for start := 60 * second; start+2*width <= duration; start += second {
			earlier, later := mean(start, start+width), mean(start+width, start+2*width)
			for i := range rise {
				if delta := later[i] - earlier[i]; delta > rise[i] {
					rise[i], at[i] = delta, start
				}
			}
		}
		for i, metric := range []string{"length", "age_ns"} {
			prefix := fmt.Sprintf("spike_%ds.%s.", width/second, metric)
			m[prefix+"rise"], m[prefix+"earlier_offset_ns"] = rise[i], float64(at[i])
		}
	}
	// #867 checks short duration before completeness or the configured bound.
	if duration < 600*second {
		v.State = "inconclusive"
	} else if bound <= 0 || incomplete {
		v.State = "reject"
	}
	return v
}
