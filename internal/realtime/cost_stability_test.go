package realtime_test

import (
	"testing"
	"time"
)

// Means use posting samples equally and half-open offsets from planned start.
func costQueueMean(series []costQueueSample, start, end time.Duration) (mean [2]float64) {
	count := 0
	for _, s := range series {
		if s.phase == "posting" && s.at >= start && s.at < end {
			count++
			for metric := range mean {
				mean[metric] += float64(s.values[metric])
			}
		}
	}
	if count > 0 {
		for metric := range mean {
			mean[metric] /= float64(count)
		}
	}
	return mean
}

func costQueueRise(earlier, later [2]float64, bound int) bool {
	return later[0]-earlier[0] > max(.05*earlier[0], .005*float64(bound)) ||
		later[1]-earlier[1] > max(.05*earlier[1], float64(10*time.Millisecond))
}

func costQueueStability(series []costQueueSample, duration time.Duration, bound int) string {
	if duration < 600*time.Second {
		return "inconclusive"
	}
	if bound <= 0 || !costQueueComplete(series, duration) {
		return "reject"
	}
	for start := 60 * time.Second; start+240*time.Second <= duration; start += 60 * time.Second {
		if costQueueRise(costQueueMean(series, start, start+120*time.Second), costQueueMean(series, start+120*time.Second, start+240*time.Second), bound) {
			return "reject"
		}
	}
	if costQueueRise(costQueueMean(series, 60*time.Second, 240*time.Second), costQueueMean(series, 420*time.Second, 600*time.Second), bound) {
		return "reject"
	}
	return "pass"
}

// Spikes compare adjacent means sliding by one second after warm-up. They are
// diagnostics only; each metric retains its own largest positive rise and offset.
func costQueueSpike(series []costQueueSample, duration, width time.Duration) (rise [2]float64, at [2]time.Duration) {
	for start := 60 * time.Second; start+2*width <= duration; start += time.Second {
		earlier, later := costQueueMean(series, start, start+width), costQueueMean(series, start+width, start+2*width)
		for metric := range rise {
			if delta := later[metric] - earlier[metric]; delta > rise[metric] {
				rise[metric], at[metric] = delta, start
			}
		}
	}
	return rise, at
}

func TestCostStabilityRules(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value func(int) [4]int64
		want  string
	}{
		{"warm-up", func(s int) [4]int64 {
			if s < 60 {
				return [4]int64{}
			}
			return [4]int64{100, int64(100 * time.Millisecond)}
		}, "pass"},
		{"sliding", func(s int) [4]int64 {
			if s/60 == 1 || s/60 == 4 || s/60 == 5 {
				return [4]int64{20}
			}
			return [4]int64{}
		}, "reject"},
		// Every sliding rise is under 5, but the cumulative rise is over 5.
		{"cumulative", func(s int) [4]int64 { return [4]int64{int64(s / 60)} }, "reject"},
		{"terminal-spike", func(s int) [4]int64 {
			if s >= 570 {
				return [4]int64{int64((s-570)/10 + 1)}
			}
			return [4]int64{}
		}, "pass"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var series []costQueueSample
			for second := range 600 {
				series = append(series, costQueueSample{at: time.Duration(second) * time.Second, phase: "posting", values: tt.value(second)})
			}
			if got := costQueueStability(series, 600*time.Second, 1000); got != tt.want {
				t.Fatalf("stability = %s, want %s", got, tt.want)
			}
			if got := costQueueStability(series[:599], 599*time.Second, 1000); got != "inconclusive" {
				t.Fatalf("short window = %s, want inconclusive", got)
			}
		})
	}
}

func TestCostStabilityTolerances(t *testing.T) {
	if costQueueRise([2]float64{}, [2]float64{25}, 5000) || !costQueueRise([2]float64{}, [2]float64{26}, 5000) {
		t.Fatal("registration-bound length tolerance changed")
	}
	for _, earlier := range [][2]float64{{0, 0}, {1000, float64(time.Second)}} {
		limits := [2]float64{max(.05*earlier[0], 5), max(.05*earlier[1], float64(10*time.Millisecond))}
		later := [2]float64{earlier[0] + limits[0], earlier[1] + limits[1]}
		if costQueueRise(earlier, later, 1000) {
			t.Fatal("exact tolerance rejected")
		}
		for metric := range 2 {
			over := later
			over[metric]++
			if !costQueueRise(earlier, over, 1000) {
				t.Fatalf("metric %d above tolerance accepted", metric)
			}
		}
	}
}

func TestCostStabilitySpikes(t *testing.T) {
	for _, width := range []time.Duration{10 * time.Second, 30 * time.Second} {
		var series []costQueueSample
		for second := range 600 {
			value := [4]int64{}
			if second >= 120 && second < 120+int(width/time.Second) {
				value = [4]int64{20, int64(40 * time.Millisecond)}
			}
			series = append(series, costQueueSample{at: time.Duration(second) * time.Second, phase: "posting", values: value})
		}
		series = append(series, costQueueSample{at: 120 * time.Second, phase: "drain", values: [4]int64{10000}})
		rise, at := costQueueSpike(series, 600*time.Second, width)
		if rise != [2]float64{20, float64(40 * time.Millisecond)} || at != [2]time.Duration{120*time.Second - width, 120*time.Second - width} {
			t.Fatalf("spike width=%s rise=%v offsets=%v", width, rise, at)
		}
	}
}
