package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func stage2HeadroomSeries(gauge func(int) int, rates func(int) (int64, int64, int64)) []stage2Sample {
	var samples []stage2Sample
	var a, e, wait int64
	for second := 0; second <= 600; second++ {
		raw := json.RawMessage(fmt.Sprintf(`{"database":{},"pool":{"acquire_count":%d,"acquire_duration_ns":0,"empty_acquire_count":%d,"empty_acquire_wait_ns":%d,"canceled_acquire_count":0,"acquired_conns":%d,"idle_conns":0,"total_conns":36,"max_conns":36},"runtime":{},"streams":{},"scheduler":{},"delivery":{}}`, a, e, wait, gauge(second)))
		phase := "traffic"
		if second == 600 {
			phase = "boundary"
		}
		samples = append(samples, stage2Sample{int64(second) * int64(time.Second), phase, raw})
		da, de, dw := rates(second)
		a, e, wait = a+da, e+de, wait+dw
	}
	return samples
}

func stage2CheckHeadroom(t *testing.T, samples []stage2Sample, id, want string) stage2Window {
	t.Helper()
	w := stage2WindowFixture()
	w.Samples = samples
	if err := stage2Evaluate(&w, nil); err != nil {
		t.Fatal(err)
	}
	stage2GateState(t, w, id, want)
	for _, gate := range w.Gates {
		if gate.ID == id && slices.Contains([]string{"empty-acquire", "empty-wait", "waiting-pressure"}, id) {
			if _, ok := gate.Measured["range_0_600s.value"]; !ok {
				t.Fatalf("%s: whole-window ratio omitted", id)
			}
		}
	}
	return w
}

func TestStage2HeadroomBoundaries(t *testing.T) {
	for _, id := range []string{"acquired", "empty-acquire", "empty-wait", "waiting-pressure"} {
		count := 1
		switch id {
		case "acquired":
			count = 3
		case "waiting-pressure":
			count = 11 // Whole window and every planned minute, independently.
		}
		for target := range count {
			for _, delta := range []int64{-1, 0, 1} {
				t.Run(fmt.Sprintf("%s/target=%d/delta=%d", id, target, delta), func(t *testing.T) {
					samples := stage2HeadroomSeries(func(s int) int {
						if s == 600 {
							return 999 // A terminal boundary is not a traffic second.
						}
						band := 0
						if s >= 570 {
							band = 1
						}
						if s >= 594 {
							band = 2
						}
						value := []int{32, 35, 36}[band]
						if id == "acquired" && target == band {
							value += int(delta)
						}
						return value
					}, func(s int) (int64, int64, int64) {
						a, e, wait := int64(2000000), int64(10000), int64(0)
						if target == 0 || s/60 == target-1 {
							e, wait = 20000, 100000000
							if id == "empty-wait" {
								wait = 20000 * int64(5*time.Millisecond)
							}
							if id == "empty-acquire" {
								e += delta
							}
							if id == "empty-wait" || id == "waiting-pressure" {
								wait += delta
							}
						}
						return a, e, wait
					})
					want := "pass"
					if delta > 0 {
						want = "reject"
					}
					w := stage2CheckHeadroom(t, samples, id, want)
					if id == "acquired" && w.Gates[14].Measured["range_0_600s.sampled_max_not_exact_peak"] != 1 {
						t.Fatal("sampled maximum lost its not-exact-peak label")
					}
				})
			}
		}
	}
}

func TestStage2HeadroomMinuteScope(t *testing.T) {
	for _, id := range []string{"empty-acquire", "empty-wait", "waiting-pressure"} {
		for minute := range 10 {
			t.Run(fmt.Sprintf("%s/minute=%d", id, minute), func(t *testing.T) {
				samples := stage2HeadroomSeries(func(int) int { return 0 }, func(s int) (int64, int64, int64) {
					a, e, wait := int64(2000), int64(10), int64(0)
					if s/60 == minute {
						switch id {
						case "empty-acquire":
							e = 24 // 1.2% in this minute, 0.57% over the window.
						case "empty-wait":
							wait = e * int64(6*time.Millisecond)
						case "waiting-pressure":
							wait = int64(200 * time.Millisecond)
						}
					}
					return a, e, wait
				})
				want := "pass"
				if id == "waiting-pressure" {
					want = "reject"
				}
				w := stage2CheckHeadroom(t, samples, id, want)
				for _, gate := range w.Gates {
					if gate.ID == id && id == "waiting-pressure" && gate.Measured["range_0_600s.value"] > .1 {
						t.Fatal("whole-window waiting pressure must pass independently")
					}
				}
			})
		}
	}
}

func TestStage2HeadroomSaturation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds []int
		gap     bool
		want    string
	}{
		{"below", []int{0, 100, 200, 300, 400}, false, "pass"},
		{"one-percent", []int{0, 100, 200, 300, 400, 500}, false, "pass"},
		{"over", []int{0, 100, 200, 300, 400, 500, 599}, false, "reject"},
		{"two", []int{0, 1}, false, "pass"},
		{"three", []int{0, 1, 2}, false, "reject"},
		{"gap-inside-run", []int{0, 1, 2}, true, "inconclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples := stage2HeadroomSeries(func(s int) int {
				if slices.Contains(tc.seconds, s) {
					return 36
				}
				return 0
			}, func(int) (int64, int64, int64) { return 0, 0, 0 })
			if tc.gap {
				samples = slices.Delete(samples, 1, 2)
			}
			stage2CheckHeadroom(t, samples, "saturation", tc.want)
		})
	}
}

func TestStage2HeadroomEvidence(t *testing.T) {
	for _, change := range []string{"zero", "no-empty", "empty", "hole", "boundary", "minute", "reset", "zero-with-wait", "empty-over-acquires", "minute-inconsistent", "duplicate-second"} {
		t.Run(change, func(t *testing.T) {
			samples := stage2HeadroomSeries(func(int) int { return 0 }, func(s int) (int64, int64, int64) {
				switch change {
				case "zero":
					return 0, 0, 0
				case "no-empty":
					return 100, 0, 0
				case "zero-with-wait":
					return 0, 0, 1
				case "empty-over-acquires":
					return 1, 2, 0
				case "minute-inconsistent":
					if s < 60 {
						return 1, 2, 0
					}
					return 100, 0, 0
				}
				return 100, 1, 1
			})
			switch change {
			case "empty":
				samples = nil
			case "hole":
				samples = slices.Delete(samples, 30, 31)
			case "boundary":
				samples = samples[:600]
			case "minute":
				samples = slices.Delete(samples, 60, 61)
			case "reset":
				samples[30].Metrics = samples[0].Metrics
			case "duplicate-second":
				samples = slices.Insert(samples, 1, stage2Sample{1, "traffic", samples[0].Metrics})
			}
			w := stage2CheckHeadroom(t, samples, "stability", "reject")
			for _, id := range []string{"acquired", "saturation", "empty-acquire", "empty-wait", "waiting-pressure"} {
				want := "inconclusive"
				if change == "zero" || change == "no-empty" ||
					(change == "zero-with-wait" || change == "empty-over-acquires") && (id == "acquired" || id == "saturation") ||
					change == "minute-inconsistent" && id != "waiting-pressure" {
					want = "pass"
				}
				stage2GateState(t, w, id, want)
				if (change == "zero" || change == "no-empty") && (id == "empty-acquire" || id == "empty-wait" || id == "waiting-pressure") {
					for _, gate := range w.Gates {
						if gate.ID == id && gate.Measured["range_0_600s.value"] != 0 {
							t.Fatalf("%s: zero evidence yielded %g", id, gate.Measured["range_0_600s.value"])
						}
					}
				}
				if change == "minute" && id == "waiting-pressure" && w.Gates[18].Measured["range_60_120s.boundary_covered"] != 0 {
					t.Fatal("missing minute boundary was hidden")
				}
			}
		})
	}
}

func TestStage2HeadroomWindow(t *testing.T) {
	for _, arm := range fixedStage2().Arms {
		w := stage2WindowFixture()
		w.Config.Arm, w.Effective.Startup = arm, arm
		w.Samples = stage2HeadroomSeries(func(int) int { return 0 }, func(int) (int64, int64, int64) { return 0, 0, 0 })
		if err := stage2Evaluate(&w, nil); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"acquired", "saturation", "empty-acquire", "empty-wait", "waiting-pressure"} {
			want := "pass"
			if arm.Name == "sync" {
				want = "inconclusive"
			}
			stage2GateState(t, w, id, want)
		}
		want := "reject"
		if arm.Name == "sync" {
			want = "inconclusive"
		}
		stage2GateState(t, w, "stability", want)
		raw, _ := json.Marshal(w)
		if err := decodeStage2(raw, &stage2Window{}); err != nil {
			t.Fatal(err)
		}
		for _, change := range []string{"phase", "order", "pool"} {
			invalid := w
			invalid.Samples = slices.Clone(w.Samples)
			switch change {
			case "phase":
				invalid.Samples[0].Phase = "unknown"
			case "order":
				invalid.Samples[1].OffsetNS = 0
			case "pool":
				invalid.Samples[0].Metrics = json.RawMessage(`{"database":{},"pool":{},"runtime":{},"streams":{},"scheduler":{},"delivery":{}`)
			}
			before := invalid
			if err := stage2Evaluate(&invalid, nil); err == nil || !reflect.DeepEqual(before, invalid) {
				t.Fatalf("%s: invalid telemetry accepted or mutated window: %v", change, err)
			}
		}
	}
}
