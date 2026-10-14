package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestStage2StabilityShared(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/stage2-stability.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name       string
		DurationNS int64
		Bound      int
		Runs       [][6]int64
		Want       string
		Complete   bool
		Measured   map[string]float64
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var samples []stage2Sample
			for _, r := range f.Runs {
				for at := r[0]; at < r[1]; at += r[2] {
					phase := "traffic"
					if r[5] == 1 {
						phase = "drain"
					}
					metrics := json.RawMessage(fmt.Sprintf(`{"database":{},"pool":{},"runtime":{},"streams":{},"delivery":{},"scheduler":{"pending":%d,"oldest_pending_age_ns":%d}}`, r[3], r[4]))
					samples = append(samples, stage2Sample{at, phase, metrics})
				}
			}
			v := stage2QueueStability(samples, f.DurationNS, f.Bound)
			if got := v.Measured["complete"] == 1; got != f.Complete {
				t.Fatalf("complete = %t, want %t", got, f.Complete)
			}
			if v.State != f.Want {
				t.Fatalf("stability = %s, want %s", v.State, f.Want)
			}
			for key, want := range f.Measured {
				if got, ok := v.Measured[key]; !ok || got != want {
					t.Fatalf("%s = %g, want %g (present=%t)", key, got, want, ok)
				}
			}
		})
	}
}

func TestStage2StabilityWindow(t *testing.T) {
	for _, arm := range fixedStage2().Arms {
		for _, scheduler := range []string{`{"registration_limit":1000,"pending":0,"oldest_pending_age_ns":0}`, `{}`, `{"registration_limit":1000}`, `{"registration_limit":1000,"pending":0,"oldest_pending_age_ns":null}`, `{"registration_limit":1000,"pending":0,"oldest_pending_age_ns":"bad"}`, `{"registration_limit":0,"pending":0,"oldest_pending_age_ns":0}`, `{"registration_limit":1000,"pending":-1,"oldest_pending_age_ns":0}`} {
			w := stage2WindowFixture()
			w.Config.Arm, w.Effective.Startup = arm, arm
			w.Samples = stage2HeadroomSeries(func(int) int { return 0 }, func(int) (int64, int64, int64) { return 0, 0, 0 })
			for i := range w.Samples {
				sections, _ := stage2MetricsSections(w.Samples[i].Metrics)
				sections["scheduler"] = json.RawMessage(scheduler)
				w.Samples[i].Metrics, _ = json.Marshal(sections)
			}
			w.Effective.Metrics = w.Samples[0].Metrics
			if err := stage2Evaluate(&w, nil); err != nil {
				t.Fatal(err)
			}
			want := "reject"
			if arm.Name == "sync" {
				want = "inconclusive"
			} else if scheduler == `{"registration_limit":1000,"pending":0,"oldest_pending_age_ns":0}` {
				want = "pass"
			}
			stage2GateState(t, w, "stability", want)
			raw, _ := json.Marshal(w)
			if err := decodeStage2(raw, &stage2Window{}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
