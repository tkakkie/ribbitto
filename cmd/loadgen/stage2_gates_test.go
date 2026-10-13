package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func stage2GateState(t *testing.T, w stage2Window, id, want string) {
	t.Helper()
	for _, v := range w.Gates {
		if v.ID == id {
			if v.State != want {
				t.Fatalf("%s: got %s, want %s; measured=%v", id, v.State, want, v.Measured)
			}
			return
		}
	}
	t.Fatalf("missing gate %s", id)
}

func stage2FakeOperation(id string, values []float64) stage2Operation {
	parts, start := strings.Split(id, "."), time.Unix(123, 0)
	var observations []stage2Observation
	for i := range 100 {
		value := values[0]
		if i >= 50 {
			value = values[1]
		}
		if i >= 95 {
			value = values[2]
		}
		o := stage2Observation{Slot: stage2Slot{Operation: parts[0], Endpoint: parts[1], LogicalID: uint64(i), At: start}}
		outcome := "succeeded"
		if parts[0] == "read" {
			outcome = "changed"
		}
		o.finish(start, start, start.Add(time.Duration(value*1e6)), time.Time{}, 1, 200, outcome)
		observations = append(observations, o)
	}
	ops, _ := stage2Summarize(observations)
	for _, op := range ops {
		if op.Operation+"."+op.Endpoint == id {
			return op
		}
	}
	panic("unknown fake operation")
}

func TestStage2NormalBoundaries(t *testing.T) {
	for _, gate := range fixedStage2().Gates {
		if !strings.Contains(gate.ID, ".") {
			continue
		}
		paired := strings.HasSuffix(gate.ID, ".paired")
		id := strings.TrimSuffix(gate.ID, ".paired")
		for _, baseline := range []float64{20, 100} {
			for percentile := range 3 {
				for _, over := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%g/p%d/over=%v", gate.ID, baseline, percentile, over), func(t *testing.T) {
						w, sync := stage2WindowFixture(), stage2WindowFixture()
						sync.Config.Arm = fixedStage2().Arms[0]
						sync.Operations = []stage2Operation{stage2FakeOperation(id, []float64{baseline, baseline + 1, baseline + 2})}
						values := append([]float64(nil), gate.Limits...)
						if paired {
							values = []float64{baseline, baseline + 1, baseline + 2}
							for i, value := range values {
								values[i] += max(.2*value, 10)
							}
						}
						if over {
							values[percentile] += .001
						}
						w.Operations = []stage2Operation{stage2FakeOperation(id, values)}
						stage2Evaluate(&w, &sync)
						want := "pass"
						if over {
							want = "reject"
						}
						stage2GateState(t, w, gate.ID, want)
					})
				}
			}
		}
	}
}

func TestStage2NormalFailures(t *testing.T) {
	for _, outcome := range []string{"failed", "missed", "timeout", "noop", "unverified"} {
		t.Run(outcome, func(t *testing.T) {
			w := stage2WindowFixture()
			start := time.Unix(123, 0)
			o := stage2Observation{Slot: stage2Slot{Operation: "read", Endpoint: "feed", At: start}, Dispatch: start, Completed: start, Outcome: outcome}
			w.Operations, w.Attempts = stage2Summarize([]stage2Observation{o})
			stage2Evaluate(&w, nil)
			stage2GateState(t, w, "read.feed", "reject")
			stage2GateState(t, w, "read.feed.paired", "inconclusive")
			stage2GateState(t, w, "post.topic", "reject")
		})
	}
}

func TestStage2PairedEvidence(t *testing.T) {
	for _, mismatch := range []string{"none", "arm", "group", "round", "fixture", "traffic", "missing", "failed"} {
		w, sync := stage2WindowFixture(), stage2WindowFixture()
		w.Operations = []stage2Operation{stage2FakeOperation("post.feed", []float64{1, 2, 3})}
		sync.Operations, sync.Config = append([]stage2Operation(nil), w.Operations...), w.Config
		sync.Config.Arm = fixedStage2().Arms[0]
		switch mismatch {
		case "arm":
			sync.Config.Arm = w.Config.Arm
		case "group":
			sync.Config.Group = fixedStage2().Groups[1]
		case "round":
			sync.Config.Round++
		case "fixture":
			sync.Config.FixtureSeed++
		case "traffic":
			sync.Config.TrafficSeed++
		case "missing":
			sync.Operations = nil
		case "failed":
			sync.Operations[0].Succeeded--
		}
		stage2Evaluate(&w, &sync)
		want := "inconclusive"
		if mismatch == "none" {
			want = "pass"
		}
		if mismatch == "failed" {
			want = "reject"
		}
		stage2GateState(t, w, "post.feed.paired", want)
	}
}

func TestStage2RefusalBoundaries(t *testing.T) {
	for _, scheduled := range []uint64{999, 1000} {
		for _, outage := range []time.Duration{2 * time.Second, 2*time.Second + time.Nanosecond} {
			for _, success := range []bool{false, true} {
				w := stage2WindowFixture()
				w.Operations = []stage2Operation{{Operation: "replacement", Scheduled: scheduled, Missed: 1}}
				w.Attempts = []stage2Attempt{}
				start := time.Unix(123, 0)
				o := stage2Observation{Slot: stage2Slot{Operation: "replacement", LogicalID: 7, At: start}}
				for i := range 20 {
					o.finish(start, start, start.Add(time.Millisecond), start, uint64(i+1), 503, "failed")
				}
				if success {
					o.finish(start, start, start.Add(outage), start, 21, 200, "succeeded")
					// Later evidence cannot extend recovery beyond the first reconnect.
					o.finish(start, start, start.Add(3*time.Second), start, 22, 200, "succeeded")
				}
				w.Attempts = append(w.Attempts, o.Attempts...)
				stage2Evaluate(&w, nil)
				want, recovered := "reject", uint64(0)
				if success {
					recovered = 1
					if scheduled == 1000 && outage == 2*time.Second {
						want = "pass"
					}
				}
				stage2GateState(t, w, "replacement-503", want)
				r := w.Refusals
				if r.ScheduledReplacements != scheduled || r.AffectedReplacements != 1 || r.Recovered != recovered || r.Refused503 != 20 || r.Attempts != uint64(len(o.Attempts)) || r.Retries != r.Attempts-1 || (success && r.MaxOutageNS != int64(outage)) {
					t.Fatalf("logical denominator/recovery changed by retries: %+v", r)
				}
			}
		}
	}
}

func TestStage2MissingOutage(t *testing.T) {
	for _, measured := range []bool{false, true} {
		t.Run(fmt.Sprintf("measured=%v", measured), func(t *testing.T) {
			w := stage2WindowFixture()
			w.Operations = []stage2Operation{{Operation: "replacement", Scheduled: 1000}}
			start := time.Unix(123, 0)
			o := stage2Observation{Slot: stage2Slot{Operation: "replacement", Endpoint: "feed", LogicalID: 7, At: start}}
			o.finish(start, start, start.Add(time.Millisecond), time.Time{}, 1, 503, "failed")
			end, offline := start.Add(time.Second), time.Time{}
			want, recovered, outage := "reject", uint64(0), int64(-1)
			if measured {
				// A measured zero-duration outage is distinct from missing evidence.
				offline, want, recovered, outage = end, "pass", 1, 0
			}
			o.finish(start, start, end, offline, 2, 200, "succeeded")
			_, w.Attempts = stage2Summarize([]stage2Observation{o})
			stage2Evaluate(&w, nil)
			stage2GateState(t, w, "replacement-503", want)
			if w.Refusals.Recovered != recovered || w.Attempts[1].OutageNS != outage {
				t.Fatalf("missing and zero-duration outage evidence conflated: %+v %+v", w.Refusals, w.Attempts)
			}
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			var decoded stage2Window
			if err := decodeStage2(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Attempts[1].OutageNS != outage {
				t.Fatal("outage evidence changed on JSON round trip")
			}
		})
	}
}

func TestStage2FirstReconnect(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, reversed := range []bool{false, true} {
			t.Run(fmt.Sprintf("missing=%v/reversed=%v", missing, reversed), func(t *testing.T) {
				w := stage2WindowFixture()
				w.Operations = []stage2Operation{{Operation: "replacement", Scheduled: 1000}}
				start := time.Unix(123, 0)
				o := stage2Observation{Slot: stage2Slot{Operation: "replacement", Endpoint: "feed", LogicalID: 7, At: start}}
				o.finish(start, start, start.Add(time.Millisecond), start, 1, 503, "failed")
				offline := start
				if missing {
					offline = time.Time{}
				}
				o.finish(start, start, start.Add(2100*time.Millisecond), offline, 2, 200, "succeeded")
				// A later reconnect has a fresh offline start and a shorter outage.
				o.finish(start, start, start.Add(4*time.Second), start.Add(3*time.Second), 3, 200, "succeeded")
				_, w.Attempts = stage2Summarize([]stage2Observation{o})
				if reversed {
					w.Attempts[1], w.Attempts[2] = w.Attempts[2], w.Attempts[1]
				}
				stage2Evaluate(&w, nil)
				stage2GateState(t, w, "replacement-503", "reject")
				want := stage2Refusals{ScheduledReplacements: 1000, AffectedReplacements: 1, Recovered: 1, Attempts: 3, Refused503: 1, Retries: 2, MaxOutageNS: int64(2100 * time.Millisecond)}
				if missing {
					want.Recovered, want.MaxOutageNS = 0, 0
				}
				if w.Refusals != want {
					t.Fatalf("first reconnect evidence replaced: got %+v, want %+v", w.Refusals, want)
				}
			})
		}
	}
}

func TestStage2EvaluatedWindow(t *testing.T) {
	for _, arm := range fixedStage2().Arms {
		for _, refusedKind := range []string{"none", "establishment", "post", "read", "page"} {
			w := stage2WindowFixture()
			w.Config.Arm, w.Effective.Startup = arm, arm
			var observations []stage2Observation
			for _, kind := range []string{"establishment", "post", "read", "page"} {
				status := 200
				if kind == refusedKind {
					status = 503
				}
				now := time.Unix(123, 0)
				req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/fake", nil)
				o := stage2HTTP(context.Background(), stage2FakeHTTP{&now, status, nil, nil}, req, stage2Observation{Slot: stage2Slot{Operation: kind, Endpoint: "feed", At: now}}, now, func() time.Time { return now }, func([]byte) bool { return true })
				observations = append(observations, o)
			}
			w.Operations, w.Attempts = stage2Summarize(observations)
			stage2Evaluate(&w, nil)
			want, count := "pass", float64(0)
			if refusedKind != "none" {
				want, count = "reject", 1
			}
			stage2GateState(t, w, "normal-503", want)
			if w.Gates[12].Measured["count"] != count || w.Refusals.Refused503 != 0 {
				t.Fatal("normal-work refusals must be separate from replacement refusals")
			}
			for _, gate := range w.Protocol.Gates {
				if (arm.Name == "sync" || strings.HasSuffix(gate.ID, ".paired")) && strings.Contains(gate.ID, ".") || !strings.Contains(gate.ID, ".") && gate.ID != "normal-503" && gate.ID != "replacement-503" {
					stage2GateState(t, w, gate.ID, "inconclusive")
				}
			}
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			if err := decodeStage2(raw, &stage2Window{}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
