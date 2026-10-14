package main

import (
	"fmt"
	"slices"
	"time"
)

// Consume telemetry's planned ranges; unavailable zero defaults are not evidence.
// Measured keys name the gate's affected range without changing the v1 wire shape.
func stage2Headroom(samples []stage2Sample) (map[string]stage2Verdict, error) {
	ranges, err := stage2AggregateTelemetry(samples)
	if err != nil {
		return nil, fmt.Errorf("evaluating headroom telemetry: %w", err)
	}
	p := fixedStage2()
	c := 0
	for _, arm := range p.Arms {
		if arm.Name == "async" {
			c = arm.C
		}
	}
	var acquired []int64
	atC, run, longest, previous := 0, 0, 0, int64(-2)
	for _, s := range samples {
		if s.Phase != "traffic" {
			continue
		}
		pool, err := stage2PoolEvidence(s.Metrics)
		if err != nil {
			return nil, err
		}
		value, second := pool["acquired_conns"], s.OffsetNS/int64(time.Second)
		acquired = append(acquired, value)
		if value == int64(c) {
			atC++
			if second != previous+1 {
				run = 0
			}
			run++
		} else {
			run = 0
		}
		longest, previous = max(longest, run), second
	}
	slices.Sort(acquired)
	verdicts := map[string]stage2Verdict{}
	for _, gate := range p.Gates {
		if !slices.Contains([]string{"acquired", "saturation", "empty-acquire", "empty-wait", "waiting-pressure"}, gate.ID) {
			continue
		}
		v := stage2Verdict{gate.ID, "pass", map[string]float64{}}
		pass, complete := true, true
		needed := ranges
		if gate.ID != "waiting-pressure" {
			needed = ranges[:1]
		}
		for _, r := range needed {
			prefix := fmt.Sprintf("range_%d_%ds.", r.StartNS/int64(time.Second), r.EndNS/int64(time.Second))
			m := func(key string, value float64) { v.Measured[prefix+key] = value }
			covered := 0.
			if r.BoundaryCovered {
				covered = 1
			}
			m("boundary_covered", covered)
			m("missing_seconds", float64(r.Missing))
			m("samples", float64(r.Samples))
			m("resets", float64(len(r.ResetOffsetsNS)))
			m("elapsed_ns", float64(r.ElapsedNS))
			for key, value := range r.Delta {
				m("delta_"+key, float64(value))
			}
			valid := r.BoundaryCovered && r.Missing == 0 && len(r.ResetOffsetsNS) == 0 &&
				r.Samples == int((r.EndNS-r.StartNS)/int64(time.Second)) && r.ElapsedNS > 0
			switch gate.ID {
			case "acquired":
				m("sampled_max_not_exact_peak", 1)
				if len(acquired) > 0 {
					for i, value := range []int64{acquired[(len(acquired)*95+99)/100-1], acquired[(len(acquired)*99+99)/100-1], acquired[len(acquired)-1]} {
						m([]string{"p95", "p99", "max"}[i], float64(value))
						pass = pass && float64(value) <= gate.Limits[i]
					}
				}
			case "saturation":
				m("at_c", float64(atC))
				m("longest_consecutive_planned_seconds", float64(longest))
				if len(acquired) > 0 {
					fraction := float64(atC) / float64(len(acquired))
					m("fraction", fraction)
					pass = pass && fraction <= gate.Limits[0] && float64(longest) < gate.Limits[1]
				}
			default:
				a, e, wait := r.Delta["acquire_count"], r.Delta["empty_acquire_count"], r.Delta["empty_acquire_wait_ns"]
				consistent := a >= 0 && e >= 0 && e <= a && wait >= 0 && (e > 0 || wait == 0)
				valid = valid && consistent
				if !consistent {
					m("inconsistent_counters", 1)
				}
				value := 0.
				switch gate.ID {
				case "empty-acquire":
					if a > 0 {
						value = float64(e) / float64(a)
					}
				case "empty-wait":
					value = r.MeanEmptyWaitNS / float64(time.Millisecond)
				case "waiting-pressure":
					value = r.WaitingPressure
				}
				m("value", value)
				pass = pass && value <= gate.Limits[0]
			}
			if !valid {
				m("inconclusive", 1)
			}
			complete = complete && valid
		}
		if !complete {
			v.State = "inconclusive"
		} else if !pass {
			v.State = "reject"
		}
		verdicts[gate.ID] = v
	}
	return verdicts, nil
}
