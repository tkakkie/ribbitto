package realtime_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Range fixtures expand without sorting: duplicate and reordered offsets survive.
// Both evaluators read this same file, without an application import edge.
func TestCostStabilityShared(t *testing.T) {
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
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var series []costQueueSample
			for _, r := range f.Runs {
				for at := r[0]; at < r[1]; at += r[2] {
					phase := "posting"
					if r[5] == 1 {
						phase = "drain"
					}
					series = append(series, costQueueSample{at: time.Duration(at), phase: phase, values: [4]int64{r[3], r[4]}})
				}
			}
			duration := time.Duration(f.DurationNS)
			if got := costQueueComplete(series, duration); got != f.Complete {
				t.Fatalf("complete = %t, want %t", got, f.Complete)
			}
			if got := costQueueStability(series, duration, f.Bound); got != f.Want {
				t.Fatalf("stability = %s, want %s", got, f.Want)
			}
		})
	}
}
