package realtime_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// Legacy arms remain fixed even when configurable async settings are inherited.
func costSizing(t *testing.T, arm string, normal int) (int, int, int) {
	t.Helper()
	switch arm {
	case "sync40", "async40":
		return 40, 0, 40
	case "async10+30":
		return 10, 30, 40
	case "async":
		return normal, envInts(t, "RIBBITTO_STREAM_COST_RECOUNT_POOL", []int{0})[0], envInts(t, "RIBBITTO_STREAM_COST_WORKERS", []int{40})[0]
	default:
		return normal, 0, 0
	}
}

func costStage1Order(round int) []string {
	if round%2 == 1 {
		return []string{"partitioned", "shared"}
	}
	return []string{"shared", "partitioned"}
}

func costStage1Verdict(windows []stepResult) (passed int, eligible bool) {
	for _, r := range windows {
		if costTargets(r) && costQueueStability(r.queue, r.postingDuration, r.queueBound) == "pass" {
			passed++
		}
	}
	return passed, len(windows) == 3 && passed == 3
}

// TestStreamCostStage1 runs only on the quiet host, after all three parts land.
func TestStreamCostStage1(t *testing.T) {
	if os.Getenv("RIBBITTO_STREAM_COST_STAGE1") != "1" {
		t.Skip("set RIBBITTO_STREAM_COST_STAGE1=1 for the controlled 24-window matrix")
	}
	groups, err := costPoolGroups(os.Getenv("RIBBITTO_STREAM_COST_LONG_GROUP"))
	if err != nil {
		t.Fatal(err)
	}
	marker := "../../MEASURING"
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(marker); err != nil {
			t.Error(err)
		}
	})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	results := make(map[string][]stepResult)
	defer func() {
		for _, group := range groups {
			for _, config := range costStage1Order(0) {
				key := fmt.Sprintf("rate%d/streams%d/%s", group.rate, group.streams, config)
				passed, eligible := costStage1Verdict(results[key])
				t.Logf("stage1 group=%s windows=%d/3 passed=%d/3 recommendation-eligible=%t", key, len(results[key]), passed, eligible)
			}
		}
	}()
	for key, value := range map[string]string{
		"RIBBITTO_STREAM_COST": "1", "RIBBITTO_STREAM_COST_SIDEBAR": "1",
		"RIBBITTO_STREAM_COST_MEMBERS": "distinct", "RIBBITTO_STREAM_COST_CACHE": "1",
		"RIBBITTO_STREAM_COST_DURATION": "600s", "RIBBITTO_STREAM_COST_ATTRIBUTION": "1",
		"RIBBITTO_STREAM_COST_ARM": "async", "RIBBITTO_STREAM_COST_WORKERS": "26",
		"RIBBITTO_STREAM_COST_RECOUNTS": "", "RIBBITTO_STREAM_COST_STAGGER": "",
		"RIBBITTO_STREAM_COST_SMOKE": "",
	} {
		t.Setenv(key, value)
	}
	for round := range 3 {
		for _, group := range groups {
			for _, config := range costStage1Order(round) {
				key := fmt.Sprintf("rate%d/streams%d/%s", group.rate, group.streams, config)
				err := costLoadWindow(t.Context(), func() bool { return costHost(t.Context(), t) }, func() bool {
					var window []stepResult
					ok := t.Run(fmt.Sprintf("round%d/%s", round+1, key), func(t *testing.T) {
						t.Setenv("RIBBITTO_STREAM_COST_RATE", strconv.Itoa(group.rate))
						t.Setenv("RIBBITTO_STREAM_COST_STEPS", strconv.Itoa(group.streams))
						normal, recount := "36", ""
						if config == "partitioned" {
							normal, recount = "10", "26"
						}
						t.Setenv("RIBBITTO_STREAM_COST_POOL", normal)
						t.Setenv("RIBBITTO_STREAM_COST_RECOUNT_POOL", recount)
						runStreamCost(t, func(r stepResult) { window = append(window, r) })
					})
					if ok {
						results[key] = append(results[key], window...)
					}
					return ok
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
