package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func stage2MetricFixture(second int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"database":{"queries":%d},"pool":{"acquire_count":%d,"acquire_duration_ns":%d,"empty_acquire_count":%d,"empty_acquire_wait_ns":%d,"canceled_acquire_count":%d,"acquired_conns":%d,"idle_conns":1,"total_conns":36,"max_conns":36},"runtime":{"goroutines":12,"heap_inuse_bytes":4096},"streams":{"open":1000},"scheduler":{"enabled":true,"pending":7,"processing_duration_bucket_counts":[1,2,3,4,5,6]},"delivery":{"enabled":false,"extra":42}}`, second, 1000+second*100, second*2000000, 10+second, 1000000+second*5000000, 3+second, second%37))
}

func TestStage2TelemetrySectionTypes(t *testing.T) {
	for _, section := range []string{"database", "pool", "runtime", "streams", "scheduler", "delivery"} {
		for _, value := range []string{"null", "42", `"text"`, "[]"} {
			t.Run(section+"/"+value, func(t *testing.T) {
				var sections map[string]json.RawMessage
				if err := json.Unmarshal(stage2MetricFixture(0), &sections); err != nil {
					t.Fatal(err)
				}
				sections[section] = json.RawMessage(value)
				raw, err := json.Marshal(sections)
				if err != nil {
					t.Fatal(err)
				}
				t.Run("sampler", func(t *testing.T) {
					start := time.Unix(123, 0)
					clock, calls := start, 0
					samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
						clock = clock.Add(delay)
						return true
					}, func(context.Context) (json.RawMessage, error) {
						calls++
						if calls == 1 {
							return raw, nil
						}
						return stage2MetricFixture(0), nil
					})
					if err == nil || !strings.Contains(err.Error(), "metrics slot 0:") || len(samples) != 600 || calls != 601 {
						t.Fatalf("sampler accepted invalid %s section or lost problem: samples=%d calls=%d err=%v", section, len(samples), calls, err)
					}
					for i, sample := range samples {
						if sample.OffsetNS != int64(i+1)*int64(time.Second) || !reflect.DeepEqual(sample.Metrics, stage2MetricFixture(0)) {
							t.Fatalf("invalid %s section retained or later sample lost at %d: %+v", section, i, sample)
						}
					}
				})
				t.Run("aggregation", func(t *testing.T) {
					if _, err := stage2AggregateTelemetry([]stage2Sample{{0, "traffic", raw}}); err == nil {
						t.Fatalf("aggregation accepted invalid %s section", section)
					}
				})
			})
		}
	}
}

func TestStage2TelemetrySectionContents(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(fmt.Sprintf("extra-fields=%t", extra), func(t *testing.T) {
			raw := stage2MetricFixture(0)
			if extra {
				raw = json.RawMessage(strings.ReplaceAll(string(raw), `:{`, `:{ "future": [null, 1, "opaque", {}], `))
			}
			start := time.Unix(123, 0)
			clock := start
			samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				clock = clock.Add(delay)
				return true
			}, func(context.Context) (json.RawMessage, error) { return raw, nil })
			if err != nil || len(samples) != 601 {
				t.Fatalf("object sections rejected: samples=%d err=%v", len(samples), err)
			}
			if _, err := stage2AggregateTelemetry(samples); err != nil {
				t.Fatalf("object sections rejected by aggregation: %v", err)
			}
			for i, sample := range samples {
				if !reflect.DeepEqual(sample.Metrics, raw) {
					t.Fatalf("section contents changed at %d: got %s want %s", i, sample.Metrics, raw)
				}
			}
		})
	}
}

func TestStage2Telemetry(t *testing.T) {
	start := time.Unix(123, 0)
	clock := start
	wait := func(_ context.Context, delay time.Duration) bool { clock = clock.Add(delay); return true }
	buffer := stage2MetricFixture(0)
	samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, wait, func(context.Context) (json.RawMessage, error) {
		copy(buffer, stage2MetricFixture(0))
		return buffer, nil
	})
	if err != nil || len(samples) != 601 || clock.Sub(start) != 600*time.Second {
		t.Fatalf("per-second schedule: samples=%d elapsed=%s err=%v", len(samples), clock.Sub(start), err)
	}
	buffer[0] = 'x'
	for i, s := range samples {
		phase := "traffic"
		if i == 600 {
			phase = "boundary"
		}
		if s.OffsetNS != int64(i)*int64(time.Second) || s.Phase != phase || !reflect.DeepEqual(s.Metrics, stage2MetricFixture(0)) {
			t.Fatalf("full snapshot or clock lost at %d: %+v", i, s)
		}
		samples[i].Metrics = stage2MetricFixture(i)
	}
	// Neither terminal nor drain gauges belong to occupancy maxima.
	samples[600].Metrics = json.RawMessage(strings.Replace(string(samples[600].Metrics), `"acquired_conns":8`, `"acquired_conns":999`, 1))
	samples = append(samples, stage2Sample{601 * int64(time.Second), "drain", nil})
	ranges, err := stage2AggregateTelemetry(samples)
	if err != nil || len(ranges) != 11 {
		t.Fatalf("planned ranges: %d %v", len(ranges), err)
	}
	for i, r := range ranges {
		seconds, at := 60, (i-1)*60
		if i == 0 {
			seconds, at = 600, 0
		}
		if r.StartNS != int64(at)*int64(time.Second) || r.EndNS != int64(at+seconds)*int64(time.Second) || !r.BoundaryCovered || r.Samples != seconds || r.Missing != 0 || r.ElapsedNS != int64(seconds)*int64(time.Second) || len(r.ResetOffsetsNS) != 0 || r.SampledMaxNotExactPeak["acquired_conns"] != 36 || r.MeanEmptyWaitNS != 5000000 || r.WaitingPressure != .005 {
			t.Fatalf("range evidence lost: %+v", r)
		}
		want := map[string]int64{"acquire_count": int64(seconds * 100), "acquire_duration_ns": int64(seconds * 2000000), "empty_acquire_count": int64(seconds), "empty_acquire_wait_ns": int64(seconds * 5000000), "canceled_acquire_count": int64(seconds)}
		if !reflect.DeepEqual(r.Delta, want) {
			t.Fatalf("counter counts/durations: %v want %v", r.Delta, want)
		}
	}
	for _, change := range []string{"hole", "boundary", "reset", "invalid", "duplicate", "negative", "zero-empty", "jitter", "empty"} {
		t.Run(change, func(t *testing.T) {
			copySamples := append([]stage2Sample(nil), samples[:601]...)
			switch change {
			case "jitter":
				copySamples[600].OffsetNS += int64(time.Second / 2)
			case "empty":
				copySamples = nil
			case "hole":
				copySamples = append(copySamples[:30], copySamples[31:]...)
			case "boundary":
				copySamples = copySamples[:600]
			case "reset":
				copySamples[30].Metrics = stage2MetricFixture(0)
			case "invalid":
				copySamples[30].Metrics = json.RawMessage(strings.Replace(string(copySamples[30].Metrics), `"acquire_count":4000`, `"acquire_count":null`, 1))
			case "duplicate":
				copySamples[30].OffsetNS = copySamples[29].OffsetNS
			case "negative":
				copySamples[0].OffsetNS = -1
			case "zero-empty":
				for i := range copySamples {
					copySamples[i].Metrics = stage2MetricFixture(0)
				}
			}
			got, err := stage2AggregateTelemetry(copySamples)
			if change == "invalid" || change == "duplicate" || change == "negative" {
				if err == nil {
					t.Fatal("invalid telemetry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r := got[0]
			if change == "jitter" && (r.ElapsedNS != int64(600500*time.Millisecond) || r.WaitingPressure != 3e9/600.5e9) || change == "empty" && (len(got) != 11 || r.Missing != 600 || r.BoundaryCovered) {
				t.Fatalf("planned coverage or actual duration lost: %+v", r)
			}
			if change == "hole" && (r.Missing != 1 || got[1].Missing != 1) || change == "boundary" && (r.BoundaryCovered || got[10].BoundaryCovered || len(r.Delta) != 0) || change == "reset" && (!reflect.DeepEqual(r.ResetOffsetsNS, []int64{30 * int64(time.Second)}) || r.Delta["acquire_count"] != 60000 || r.WaitingPressure != 0) || change == "zero-empty" && (r.MeanEmptyWaitNS != 0 || r.WaitingPressure != 0) {
				t.Fatalf("%s evidence hidden: %+v", change, r)
			}
		})
	}
}

func TestStage2TelemetryNeverEarly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slot   int
		offset time.Duration
	}{
		{"normal", -1, 0},
		{"early-wait", 1, 500 * time.Millisecond},
		{"clock-before-start", 0, -time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Unix(123, 0)
			clock, slot := start, -1
			var fetched []int
			samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				slot++
				clock = clock.Add(delay)
				if slot == tc.slot {
					clock = start.Add(tc.offset)
				}
				return true
			}, func(context.Context) (json.RawMessage, error) {
				fetched = append(fetched, slot)
				return stage2MetricFixture(slot), nil
			})
			wantSamples := 601
			if tc.slot >= 0 {
				wantSamples--
				wantProblem := fmt.Sprintf("missed metrics slot %d", tc.slot)
				if err == nil || err.Error() != wantProblem {
					t.Errorf("early dispatch not recorded as a slot problem: got %v want %q", err, wantProblem)
				}
			} else if err != nil {
				t.Errorf("normal schedule failed: %v", err)
			}
			for _, fetchedSlot := range fetched {
				if fetchedSlot == tc.slot {
					t.Errorf("early slot %d fetched metrics", tc.slot)
				}
			}
			for _, sample := range samples {
				if tc.slot >= 0 && sample.OffsetNS == int64(tc.offset) {
					t.Errorf("early slot %d retained sample at %s", tc.slot, tc.offset)
				}
			}
			if len(samples) != wantSamples || len(fetched) != wantSamples {
				t.Fatalf("schedule coverage: samples=%d fetches=%d want=%d", len(samples), len(fetched), wantSamples)
			}
			for i, sample := range samples {
				second := i
				if tc.slot >= 0 && second >= tc.slot {
					second++
				}
				if sample.OffsetNS != int64(second)*int64(time.Second) || !reflect.DeepEqual(sample.Metrics, stage2MetricFixture(second)) {
					t.Fatalf("scheduled sample changed at slot %d: %+v", second, sample)
				}
			}
		})
	}
}

func TestStage2TelemetryElapsedSecondBuckets(t *testing.T) {
	for _, lateOpening := range []bool{false, true} {
		t.Run(fmt.Sprintf("late-opening=%t", lateOpening), func(t *testing.T) {
			start := time.Unix(123, 0)
			clock, slot := start, -1
			if lateOpening {
				clock = start.Add(1500 * time.Millisecond)
			}
			var fetched []int
			samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				slot++
				clock = clock.Add(delay)
				if lateOpening && slot == 1 && clock.Sub(start) != 1600*time.Millisecond {
					t.Fatalf("slot 1 considered at %s, want 1.6s", clock.Sub(start))
				}
				return true
			}, func(context.Context) (json.RawMessage, error) {
				fetched = append(fetched, slot)
				if lateOpening && slot == 0 {
					clock = start.Add(1600 * time.Millisecond)
				}
				return stage2MetricFixture(slot), nil
			})
			wantSlots := make([]int, 0, 601)
			for second := 0; second <= 600; second++ {
				if !lateOpening || second != 1 {
					wantSlots = append(wantSlots, second)
				}
			}
			if lateOpening {
				if err == nil || err.Error() != "missed metrics slot 1" {
					t.Errorf("same-bucket slot not recorded as missed: got %v want missed metrics slot 1", err)
				}
			} else if err != nil {
				t.Errorf("normal schedule failed: %v", err)
			}
			if !reflect.DeepEqual(fetched, wantSlots) {
				t.Errorf("fetch schedule differs: fetches=%d want=%d slot 1 fetched=%t", len(fetched), len(wantSlots), len(fetched) > 1 && fetched[1] == 1)
			}
			if len(samples) != len(wantSlots) {
				t.Fatalf("retained samples=%d want=%d; same-bucket sample must not be retained", len(samples), len(wantSlots))
			}
			for i, second := range wantSlots {
				offset, phase := time.Duration(second)*time.Second, "traffic"
				if lateOpening && second == 0 {
					offset = 1500 * time.Millisecond
				}
				if second == 600 {
					phase = "boundary"
				}
				if samples[i].OffsetNS != int64(offset) || samples[i].Phase != phase || !reflect.DeepEqual(samples[i].Metrics, stage2MetricFixture(second)) {
					t.Fatalf("retained slot %d changed: %+v", second, samples[i])
				}
			}
		})
	}
}

func TestStage2TelemetryTerminalAfterLateTraffic(t *testing.T) {
	for _, terminal := range []time.Duration{600100 * time.Millisecond, 602 * time.Second, 602*time.Second + time.Nanosecond} {
		t.Run(terminal.String(), func(t *testing.T) {
			start := time.Unix(123, 0)
			clock, slot, calls := start, -1, 0
			samples, err := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				slot++
				clock = clock.Add(delay)
				if slot == 599 {
					clock = start.Add(599500 * time.Millisecond)
				}
				return true
			}, func(context.Context) (json.RawMessage, error) {
				calls++
				if slot == 599 {
					clock = start.Add(terminal)
				}
				return stage2MetricFixture(slot), nil
			})
			want := 601
			if terminal > 602*time.Second {
				want--
				if err == nil || err.Error() != "missed metrics slot 600" {
					t.Fatalf("late terminal not recorded as missed: %v", err)
				}
			} else if err != nil {
				t.Fatalf("valid terminal rejected after late traffic: %v", err)
			}
			if len(samples) != want || calls != want {
				t.Fatalf("terminal coverage: samples=%d fetches=%d want=%d", len(samples), calls, want)
			}
			if want == 601 && (samples[600].OffsetNS != int64(terminal) || samples[600].Phase != "boundary") {
				t.Fatalf("terminal dispatch changed: %+v", samples[600])
			}
		})
	}
}

func TestStage2TelemetryFailures(t *testing.T) {
	for _, failure := range []string{"scrape", "slow", "shape", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			start := time.Unix(123, 0)
			clock, calls := start, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			samples, err := stage2SampleMetrics(ctx, start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool { clock = clock.Add(delay); return true }, func(context.Context) (json.RawMessage, error) {
				calls++
				if calls == 1 {
					switch failure {
					case "scrape":
						return nil, fmt.Errorf("scrape failed")
					case "slow":
						clock = clock.Add(2500 * time.Millisecond)
					case "shape":
						return json.RawMessage(`{}`), nil
					case "cancel":
						cancel()
					}
				}
				return stage2MetricFixture(0), nil
			})
			want := 600
			if failure == "cancel" {
				want = 1
			}
			if err == nil || len(samples) != want || calls != want+map[string]int{"scrape": 1, "shape": 1}[failure] || failure == "slow" && samples[1].OffsetNS != int64(2500*time.Millisecond) {
				t.Fatalf("failure lost or backfilled: %s samples=%d calls=%d err=%v", failure, len(samples), calls, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("sampling cancellation identity lost: err=%v", err)
			}
		})
	}
}

func TestStage2TelemetryTerminalFetchCancellation(t *testing.T) {
	for _, cancelDuringFetch := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelDuringFetch), func(t *testing.T) {
			start := time.Unix(123, 0)
			clock, calls := start, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			samples, err := stage2SampleMetrics(ctx, start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				clock = clock.Add(delay)
				return true
			}, func(context.Context) (json.RawMessage, error) {
				second := calls
				calls++
				if second == 600 {
					clock = clock.Add(500 * time.Millisecond)
					if cancelDuringFetch {
						cancel()
					}
				}
				return stage2MetricFixture(second), nil
			})
			if cancelDuringFetch {
				if err == nil || !strings.Contains(err.Error(), "sampling stopped: context canceled") {
					t.Fatalf("terminal-fetch cancellation not reported: err=%v", err)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("terminal-fetch cancellation identity lost: err=%v", err)
				}
			} else if err != nil {
				t.Fatalf("uncancelled sampling failed: %v", err)
			}
			if len(samples) != 601 || calls != 601 {
				t.Fatalf("terminal fetch lost samples: samples=%d calls=%d", len(samples), calls)
			}
			terminal := samples[600]
			if terminal.OffsetNS != int64(600*time.Second) || terminal.Phase != "boundary" || !reflect.DeepEqual(terminal.Metrics, stage2MetricFixture(600)) {
				t.Fatalf("valid terminal snapshot not retained: %+v", terminal)
			}
		})
	}
}

func TestStage2TelemetryPhases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		at    int
		phase string
	}{
		{"traffic-at-end", 600, "traffic"},
		{"boundary-mid-window", 30, "boundary"},
		{"boundary-before-end", 599, "boundary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var samples []stage2Sample
			for second := 0; second <= 600; second++ {
				phase := "traffic"
				if second == 600 {
					phase = "boundary"
				}
				samples = append(samples, stage2Sample{int64(second) * int64(time.Second), phase, stage2MetricFixture(second)})
			}
			samples[tc.at].Phase = tc.phase
			got, err := stage2AggregateTelemetry(samples)
			if err == nil {
				t.Fatalf("wrong phase accepted: BoundaryCovered=%t Missing=%d", got[0].BoundaryCovered, got[0].Missing)
			}
		})
	}
}

func TestStage2TelemetryWindowTolerance(t *testing.T) {
	for _, boundary := range []string{"start", "end"} {
		for _, lateness := range []time.Duration{2*time.Second - time.Nanosecond, 2 * time.Second, 2*time.Second + time.Nanosecond} {
			t.Run(fmt.Sprintf("%s/%s", boundary, lateness), func(t *testing.T) {
				start := time.Unix(123, 0)
				clock := start
				if boundary == "start" {
					clock = clock.Add(lateness)
				}
				waitCalls, firstScrapeSlot := 0, -1
				samples, sampleErr := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
					waitCalls++
					clock = clock.Add(delay)
					if boundary == "end" && clock.Sub(start) == 600*time.Second {
						clock = clock.Add(lateness)
					}
					return true
				}, func(context.Context) (json.RawMessage, error) {
					if firstScrapeSlot < 0 {
						firstScrapeSlot = waitCalls - 1
					}
					return stage2MetricFixture(int(clock.Sub(start) / time.Second)), nil
				})
				inside := lateness <= 2*time.Second
				wantSamples, wantMissing := 601, 0
				if boundary == "start" {
					wantFirstSlot := 0
					if !inside {
						wantFirstSlot = 2
					}
					if firstScrapeSlot != wantFirstSlot {
						t.Fatalf("opening slot tolerance: first scrape slot=%d want=%d", firstScrapeSlot, wantFirstSlot)
					}
					wantMissing = int(lateness / time.Second)
					wantSamples -= wantMissing
					if samples[0].OffsetNS != int64(lateness) {
						t.Fatalf("opening scrape lost: offset=%d want=%d", samples[0].OffsetNS, lateness)
					}
				} else if !inside {
					wantSamples--
				}
				if len(samples) != wantSamples || (sampleErr != nil) != (boundary == "start" || !inside) {
					t.Fatalf("boundary sampling: samples=%d want=%d err=%v", len(samples), wantSamples, sampleErr)
				}
				for i := 1; i < len(samples); i++ {
					if samples[i].OffsetNS <= samples[i-1].OffsetNS {
						t.Fatal("late opening scrape backfilled an expired slot")
					}
				}
				ranges, err := stage2AggregateTelemetry(samples)
				if err != nil {
					t.Fatal(err)
				}
				r := ranges[0]
				edgeBucket := 1
				if boundary == "end" {
					edgeBucket = 10
				}
				if r.BoundaryCovered != inside || ranges[edgeBucket].BoundaryCovered != inside || r.Missing != wantMissing {
					t.Fatalf("window boundary coverage: covered=%t want=%t missing=%d want=%d", r.BoundaryCovered, inside, r.Missing, wantMissing)
				}
				if inside {
					wantElapsed := 600*time.Second + lateness
					if boundary == "start" {
						wantElapsed = 600*time.Second - lateness
					}
					if r.ElapsedNS != int64(wantElapsed) || r.Delta["empty_acquire_wait_ns"] == 0 || r.WaitingPressure != float64(r.Delta["empty_acquire_wait_ns"])/float64(wantElapsed) {
						t.Fatalf("actual boundary duration/ratios lost: %+v", r)
					}
				} else if len(r.Delta) != 0 || r.WaitingPressure != 0 || r.MeanEmptyWaitNS != 0 {
					t.Fatalf("uncovered window has usable ratios: %+v", r)
				}
				if boundary == "end" {
					// Also check retained input independently of the sampler's cutoff.
					samples = append([]stage2Sample(nil), samples[:600]...)
					samples = append(samples, stage2Sample{int64(600*time.Second + lateness), "boundary", stage2MetricFixture(600)})
					got, err := stage2AggregateTelemetry(samples)
					if (err == nil) != inside || inside && (!got[0].BoundaryCovered || !got[10].BoundaryCovered || got[0].Missing != 0) {
						t.Fatalf("retained closing boundary tolerance: lateness=%s err=%v", lateness, err)
					}
				}
			})
		}
	}
}

func TestStage2TelemetryMinuteTolerance(t *testing.T) {
	for _, boundary := range []string{"start", "end"} {
		for _, lateness := range []time.Duration{time.Second - time.Nanosecond, time.Second, time.Second + time.Nanosecond} {
			t.Run(fmt.Sprintf("%s/%s", boundary, lateness), func(t *testing.T) {
				var samples []stage2Sample
				for second := 0; second <= 600; second++ {
					if second == 60 || lateness >= time.Second && second == 61 {
						continue
					}
					phase := "traffic"
					if second == 600 {
						phase = "boundary"
					}
					samples = append(samples, stage2Sample{int64(second) * int64(time.Second), phase, stage2MetricFixture(second)})
					if second == 59 {
						samples = append(samples, stage2Sample{int64(60*time.Second + lateness), "traffic", stage2MetricFixture(60)})
					}
				}
				ranges, err := stage2AggregateTelemetry(samples)
				if err != nil {
					t.Fatal(err)
				}
				index := 1
				if boundary == "start" {
					index = 2
				}
				r := ranges[index]
				inside := lateness < time.Second
				if r.BoundaryCovered != inside || !ranges[0].BoundaryCovered {
					t.Fatalf("minute boundary coverage: covered=%t want=%t", r.BoundaryCovered, inside)
				}
				if inside && (r.ElapsedNS == 0 || len(r.Delta) == 0 || r.WaitingPressure == 0) {
					t.Fatalf("covered minute ratios lost: %+v", r)
				}
				if !inside && (r.ElapsedNS != 0 || len(r.Delta) != 0 || r.WaitingPressure != 0 || r.MeanEmptyWaitNS != 0) {
					t.Fatalf("uncovered minute has usable ratios: %+v", r)
				}
			})
		}
	}
}

func TestStage2TelemetryEndResetAttribution(t *testing.T) {
	for _, lateness := range []time.Duration{0, time.Second - time.Nanosecond, time.Second, time.Second + time.Nanosecond} {
		t.Run(lateness.String(), func(t *testing.T) {
			start := time.Unix(123, 0)
			clock := start
			samples, sampleErr := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				clock = clock.Add(delay)
				if clock.Sub(start) == time.Minute {
					clock = clock.Add(lateness)
				}
				return true
			}, func(context.Context) (json.RawMessage, error) {
				second := int(clock.Sub(start) / time.Second)
				if second >= 60 {
					second -= 60
				}
				return stage2MetricFixture(second), nil
			})
			covered := lateness < time.Second
			if (sampleErr != nil) != !covered {
				t.Fatalf("minute sampling: covered=%t err=%v", covered, sampleErr)
			}
			ranges, err := stage2AggregateTelemetry(samples)
			if err != nil {
				t.Fatal(err)
			}
			r := ranges[1]
			if r.BoundaryCovered != covered {
				t.Fatalf("minute end coverage: got=%t want=%t", r.BoundaryCovered, covered)
			}
			wantResets := []int64{}
			if covered {
				wantResets = append(wantResets, int64(time.Minute+lateness))
			}
			if !reflect.DeepEqual(r.ResetOffsetsNS, wantResets) {
				t.Fatalf("minute end reset attribution: got=%v want=%v covered=%t", r.ResetOffsetsNS, wantResets, covered)
			}
			// A late drop leaves the earlier minute but must still be recorded in the next.
			wantNext := []int64{}
			if !covered {
				wantNext = append(wantNext, int64(time.Minute+lateness))
			}
			if !reflect.DeepEqual(ranges[0].ResetOffsetsNS, []int64{int64(time.Minute + lateness)}) || !reflect.DeepEqual(ranges[2].ResetOffsetsNS, wantNext) {
				t.Fatalf("reset attribution across the minute boundary: window=%v next=%v want next=%v", ranges[0].ResetOffsetsNS, ranges[2].ResetOffsetsNS, wantNext)
			}
		})
	}
}

func TestStage2TelemetryBoundaryResetAttribution(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		opening, minute, closing time.Duration
	}{
		{"on-time", 0, 500 * time.Millisecond, 0},
		{"late", 0, 1200 * time.Millisecond, 0},
		{"exact-minute-cutoff", 0, time.Second, 0},
		{"opening-tolerance", 2 * time.Second, 1200 * time.Millisecond, 0},
		{"opening-uncovered", 2*time.Second + time.Nanosecond, 1200 * time.Millisecond, 0},
		{"terminal-tolerance", 0, 1200 * time.Millisecond, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Unix(123, 0)
			clock := start.Add(tc.opening)
			samples, sampleErr := stage2SampleMetrics(context.Background(), start, func() time.Time { return clock }, func(_ context.Context, delay time.Duration) bool {
				clock = clock.Add(delay)
				switch clock.Sub(start) {
				case 59 * time.Second, 120 * time.Second:
					clock = clock.Add(500 * time.Millisecond)
				case time.Minute:
					clock = clock.Add(tc.minute)
				case 600 * time.Second:
					clock = clock.Add(tc.closing)
				}
				return true
			}, func(context.Context) (json.RawMessage, error) {
				second := int(clock.Sub(start) / time.Second)
				switch {
				case second >= 600:
					second -= 600
				case second >= 120:
					second -= 120
				case second >= 60:
					second -= 60
				}
				return stage2MetricFixture(second), nil
			})
			if (sampleErr != nil) != (tc.opening > 0 || tc.minute >= time.Second) {
				t.Fatalf("boundary sampling: err=%v", sampleErr)
			}
			ranges, err := stage2AggregateTelemetry(samples)
			if err != nil {
				t.Fatal(err)
			}
			firstDrop := int64(time.Minute + tc.minute)
			secondDrop := int64(120*time.Second + 500*time.Millisecond)
			terminalDrop := int64(600*time.Second + tc.closing)
			for i, r := range ranges {
				want := []int64{}
				switch i {
				case 0:
					want = append(want, firstDrop, secondDrop, terminalDrop)
				case 1:
					if tc.minute < time.Second {
						want = append(want, firstDrop)
					}
				case 2:
					if tc.minute >= time.Second {
						want = append(want, firstDrop)
					}
					want = append(want, secondDrop)
				case 10:
					want = append(want, terminalDrop)
				}
				if !reflect.DeepEqual(r.ResetOffsetsNS, want) {
					t.Fatalf("range %d reset attribution: got=%v want=%v", i, r.ResetOffsetsNS, want)
				}
			}
		})
	}
}

func TestStage2ReadMetrics(t *testing.T) {
	for _, mode := range []string{"ok", "503", "large", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/metrics" {
					t.Errorf("metrics path: %s", r.URL.Path)
				}
				if mode == "503" {
					w.WriteHeader(503)
				}
				body := string(stage2MetricFixture(0))
				if mode == "large" {
					body = strings.Repeat("x", stage2ProofBodyLimit+1)
				}
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			transport, err := newTransport(server.URL+"/", "", &counts{})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			raw, err := stage2ReadMetrics(ctx, transport)
			if (err == nil) != (mode == "ok") || mode == "ok" && !reflect.DeepEqual(raw, stage2MetricFixture(0)) {
				t.Fatalf("HTTP metrics evidence: %s %v", mode, err)
			}
		})
	}
}
