package realtime_test

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func costEvidenceSeries(value func(int) [4]int64) []costQueueSample {
	series := make([]costQueueSample, 600)
	for second := range series {
		series[second] = costQueueSample{at: time.Duration(second) * time.Second, phase: "posting", values: value(second)}
	}
	return series
}

func TestCostStabilityEvidence(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value func(int) [4]int64
		want  string
	}{
		// A 28.6% low-to-high swing every ten seconds, with a flat mean.
		{"oscillating-plateau", func(s int) [4]int64 {
			n := int64(875 + 250*(s/10%2))
			return [4]int64{n, n * int64(200*time.Microsecond)}
		}, "pass"},
		{"length-only", func(s int) [4]int64 { return [4]int64{int64(s), int64(200 * time.Millisecond)} }, "reject"},
		{"age-only", func(s int) [4]int64 { return [4]int64{100, int64(s) * int64(time.Millisecond)} }, "reject"},
		{"capped-length-growing-age", func(s int) [4]int64 { return [4]int64{5000, int64(s) * int64(time.Millisecond)} }, "reject"},
		{"slow-length", func(s int) [4]int64 { return [4]int64{int64(s / 60)} }, "reject"},
		{"slow-age", func(s int) [4]int64 { return [4]int64{0, int64(s/60) * int64(2*time.Millisecond)} }, "reject"},
		{"late-rise", func(s int) [4]int64 {
			if s >= 540 {
				return [4]int64{100, int64(200 * time.Millisecond)}
			}
			return [4]int64{}
		}, "reject"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			series := costEvidenceSeries(tt.value)
			bound := 1000
			if tt.name == "capped-length-growing-age" {
				bound = 5000
			}
			if !costQueueComplete(series, 600*time.Second) {
				t.Fatal("synthetic evidence is incomplete")
			}
			if strings.HasPrefix(tt.name, "slow-") {
				for start := 60 * time.Second; start <= 360*time.Second; start += 60 * time.Second {
					if costQueueRise(costQueueMean(series, start, start+120*time.Second), costQueueMean(series, start+120*time.Second, start+240*time.Second), bound) {
						t.Fatalf("slow growth rejected by sliding comparison at %s", start)
					}
				}
			}
			if got := costQueueStability(series, 600*time.Second, bound); got != tt.want {
				t.Fatalf("stability = %s, want %s", got, tt.want)
			}
			r := stepResult{arm: "async40", queue: series, postingDuration: 600 * time.Second, queueBound: bound, scheduled: 300, completed: 300, deliveries: 300}
			if !costTargets(r) || costRecommendable(r) != (tt.want == "pass") {
				t.Fatal("stability evidence did not control eligibility independently of latency")
			}
		})
	}
}

func TestCostStabilityEvidenceBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name    string
		metric  int
		earlier int64
		limit   int64
		bound   int
	}{
		{"length-floor-1000", 0, 0, 5, 1000},
		{"length-floor-5000", 0, 0, 25, 5000},
		{"length-percentage", 0, 2000, 100, 5000},
		{"age-floor", 1, 0, int64(10 * time.Millisecond), 5000},
		{"age-percentage", 1, int64(300 * time.Millisecond), int64(15 * time.Millisecond), 5000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, delta := range []int64{-1, 0, 1} {
				series := costEvidenceSeries(func(s int) (v [4]int64) {
					v[tt.metric] = tt.earlier
					if s >= 300 {
						v[tt.metric] += tt.limit + delta
					}
					return v
				})
				want := "pass"
				if delta > 0 {
					want = "reject"
				}
				if got := costQueueStability(series, 600*time.Second, tt.bound); got != want {
					t.Fatalf("delta=%d stability = %s, want %s", delta, got, want)
				}
			}
		})
	}
}

func TestCostStabilityEvidenceCoverage(t *testing.T) {
	series := costEvidenceSeries(func(int) [4]int64 { return [4]int64{100, int64(200 * time.Millisecond)} })
	for _, seconds := range []int{0, 60, 300, 599} {
		if got := costQueueStability(series[:seconds], time.Duration(seconds)*time.Second, 1000); got != "inconclusive" {
			t.Fatalf("duration=%ds stability = %s, want inconclusive", seconds, got)
		}
	}
	for _, tt := range []struct {
		name   string
		series []costQueueSample
		bound  int
	}{
		{"nil", nil, 1000}, {"late-start", series[3:], 1000},
		{"early-stop", series[:597], 1000}, {"invalid-bound", series, 0},
		{"warm-up-gap", append(append([]costQueueSample(nil), series[:10]...), series[13:]...), 1000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := costQueueStability(tt.series, 600*time.Second, tt.bound); got != "reject" {
				t.Fatalf("incomplete stability = %s, want reject", got)
			}
			reportCostQueue(t, tt.series, 600*time.Second, tt.bound)
		})
	}
}

// The archive is opt-in: normal checks need neither Git history nor stored logs.
// Replay preserves every offset and phase; it never extends a short window.
func TestCostStabilityArchiveReplay(t *testing.T) {
	dir := os.Getenv("RIBBITTO_STABILITY_ARCHIVE")
	if dir == "" {
		t.Skip("set RIBBITTO_STABILITY_ARCHIVE to the archived five-minute directory")
	}
	for _, rate := range []int{5, 20} {
		for round := 1; round <= 3; round++ {
			name := fmt.Sprintf("w-%d-%d.log", rate, round)
			t.Run(name, func(t *testing.T) {
				f, err := os.Open(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := f.Close(); err != nil {
						t.Error(err)
					}
				}()
				var series []costQueueSample
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					_, line, found := strings.Cut(scanner.Text(), "queue: at=")
					if !found {
						continue
					}
					var offset, age string
					var s costQueueSample
					if n, err := fmt.Sscanf(line, "%s phase=%s length=%d oldest=%s coalesced=%d cancelled=%d", &offset, &s.phase, &s.values[0], &age, &s.values[2], &s.values[3]); err != nil || n != 6 {
						t.Fatalf("invalid queue record: %q: %v", line, err)
					}
					s.at, err = time.ParseDuration(offset)
					if err != nil {
						t.Fatal(err)
					}
					oldest, err := time.ParseDuration(age)
					if err != nil {
						t.Fatal(err)
					}
					s.values[1] = int64(oldest)
					series = append(series, s)
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if !costQueueComplete(series, 300*time.Second) {
					t.Fatal("archive lacks complete 300s posting coverage")
				}
				if got := costQueueStability(series, 300*time.Second, 5000); got != "inconclusive" {
					t.Fatalf("archived 300s stability = %s, want inconclusive", got)
				}
				r := stepResult{arm: "async40", queue: series, postingDuration: 300 * time.Second, queueBound: 5000, scheduled: 300, completed: 300, deliveries: 300}
				if costRecommendable(r) {
					t.Fatal("archived short window became recommendation-eligible")
				}
				for _, width := range []time.Duration{10 * time.Second, 30 * time.Second} {
					rise, at := costQueueSpike(series, 300*time.Second, width)
					for metric := range rise {
						if math.IsNaN(rise[metric]) || rise[metric] <= 0 || at[metric] < 60*time.Second || at[metric]+2*width > 300*time.Second {
							t.Fatalf("invalid archived spike: width=%s rise=%v offsets=%v", width, rise, at)
						}
					}
					t.Logf("stability=inconclusive width=%s rise[length oldest-ns]=%v earlier-offsets=%v", width, rise, at)
				}
			})
		}
	}
}
