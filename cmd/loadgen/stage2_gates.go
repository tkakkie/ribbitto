package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Attempts belong to globally numbered logical replacements, across endpoints.
// Scheduled counts include misses; retries can never dilute the denominator.
func stage2CountRefusals(ops []stage2Operation, attempts []stage2Attempt) (stage2Refusals, uint64) {
	r := stage2Refusals{}
	for _, op := range ops {
		if op.Operation == "replacement" {
			r.ScheduledReplacements += op.Scheduled
		}
	}
	affected, recovered := map[uint64]bool{}, map[uint64]stage2Attempt{}
	counts := map[uint64]uint64{}
	var normal503 uint64
	for _, a := range attempts {
		if a.Operation != "replacement" {
			if a.Status == 503 {
				normal503++
			}
			continue
		}
		r.Attempts++
		if counts[a.LogicalID] > 0 {
			r.Retries++
		}
		counts[a.LogicalID]++
		if a.Status == 503 {
			r.Refused503++
			affected[a.LogicalID] = true
		}
		if a.Status == 200 && a.Outcome == "succeeded" {
			// Later successes cannot repair the first reconnect's outage evidence.
			if first, ok := recovered[a.LogicalID]; !ok || a.Attempt < first.Attempt {
				recovered[a.LogicalID] = a
			}
		}
	}
	for id := range affected {
		r.AffectedReplacements++
		if first, ok := recovered[id]; ok && first.OutageNS >= 0 {
			r.Recovered++
			r.MaxOutageNS = max(r.MaxOutageNS, first.OutageNS)
		}
	}
	return r, normal503
}

func stage2Successful(op stage2Operation) bool {
	return op.Scheduled > 0 && op.Executed == op.Scheduled && op.Succeeded == op.Scheduled &&
		op.Missed == 0 && op.Failed == 0 && op.Timeouts == 0 && op.Noop == 0 &&
		op.HTTP.Count == op.Scheduled && op.HTTP.validate() == nil &&
		(op.Operation != "read" || op.Changed == op.Scheduled)
}

// Evaluate settled HTTP evidence only. Later slices replace the placeholders;
// schema-valid output must not imply that unevaluated gates passed. Invalid
// operation sets in either input return an error without changing the window.
func stage2Evaluate(w *stage2Window, paired *stage2Window) error {
	if err := stage2ValidateOperations(w.Operations); err != nil {
		return fmt.Errorf("evaluating stage-2 window operations: %w", err)
	}
	if paired != nil {
		if err := stage2ValidateOperations(paired.Operations); err != nil {
			return fmt.Errorf("evaluating stage-2 paired window operations: %w", err)
		}
	}
	headroom, err := stage2Headroom(w.Samples)
	if err != nil {
		return err
	}
	var normal503 uint64
	w.Refusals, normal503 = stage2CountRefusals(w.Operations, w.Attempts)
	w.Gates = make([]stage2Verdict, 0, len(w.Protocol.Gates))
	find := func(ops []stage2Operation, id string) stage2Operation {
		for _, op := range ops {
			if op.Operation+"."+op.Endpoint == id {
				return op
			}
		}
		return stage2Operation{}
	}
	pairedOK := paired != nil && paired.Config.Arm == fixedStage2().Arms[0] &&
		paired.Config.Group == w.Config.Group && paired.Config.Round == w.Config.Round &&
		paired.Config.FixtureSeed == w.Config.FixtureSeed && paired.Config.TrafficSeed == w.Config.TrafficSeed
	for _, gate := range w.Protocol.Gates {
		v := stage2Verdict{gate.ID, "inconclusive", map[string]float64{}}
		pass, evaluated := true, false
		if slices.Contains(gate.Arms, w.Config.Arm.Name) {
			if verdict, ok := headroom[gate.ID]; ok {
				w.Gates = append(w.Gates, verdict)
				continue
			}
			switch gate.ID {
			case "normal-503":
				v.Measured["count"] = float64(normal503)
				pass, evaluated = float64(normal503) <= gate.Limits[0], true
			case "replacement-503":
				r := w.Refusals
				fraction := float64(0)
				if r.ScheduledReplacements > 0 {
					fraction = float64(r.AffectedReplacements) / float64(r.ScheduledReplacements)
				}
				v.Measured = map[string]float64{"fraction": fraction, "recovered": float64(r.Recovered), "affected": float64(r.AffectedReplacements), "max_outage_ms": float64(r.MaxOutageNS) / float64(time.Millisecond)}
				pass = r.ScheduledReplacements > 0 && fraction <= gate.Limits[0] && r.Recovered == r.AffectedReplacements && v.Measured["max_outage_ms"] <= gate.Limits[1]
				evaluated = true
			default:
				id := strings.TrimSuffix(gate.ID, ".paired")
				op := find(w.Operations, id)
				if !strings.Contains(id, ".") {
					break
				}
				limits := gate.Limits
				if id != gate.ID {
					if !pairedOK {
						break
					}
					sync := find(paired.Operations, id)
					if sync.Scheduled == 0 {
						break
					}
					pass = stage2Successful(sync)
					limits = []float64{sync.HTTP.P50MS, sync.HTTP.P95MS, sync.HTTP.P99MS}
					for i, value := range limits {
						limits[i] += max(gate.Limits[0]*value, gate.Limits[1])
					}
				}
				pass, evaluated = pass && stage2Successful(op), true
				for i, value := range []float64{op.HTTP.P50MS, op.HTTP.P95MS, op.HTTP.P99MS} {
					key := []string{"p50_ms", "p95_ms", "p99_ms"}[i]
					v.Measured[key], v.Measured[key+"_limit"] = value, limits[i]
					pass = pass && value <= limits[i]
				}
			}
		}
		if evaluated {
			v.State = "reject"
			if pass {
				v.State = "pass"
			}
		}
		w.Gates = append(w.Gates, v)
	}
	return nil
}
