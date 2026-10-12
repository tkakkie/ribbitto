package realtime_test

import (
	"slices"
	"testing"
	"time"
)

func TestCostStage1SizingAndOrder(t *testing.T) {
	t.Setenv("RIBBITTO_STREAM_COST_WORKERS", "26")
	for _, tt := range []struct {
		arm              string
		normal, separate int
		want             [3]int
	}{
		{"async", 36, 0, [3]int{36, 0, 26}},
		{"async", 10, 26, [3]int{10, 26, 26}},
		{"sync40", 36, 26, [3]int{40, 0, 40}},
		{"async40", 36, 26, [3]int{40, 0, 40}},
		{"async10+30", 36, 26, [3]int{10, 30, 40}},
		{"", 10, 26, [3]int{10, 0, 0}},
	} {
		recount := ""
		if tt.separate > 0 {
			recount = "26"
		}
		t.Setenv("RIBBITTO_STREAM_COST_RECOUNT_POOL", recount)
		n, r, w := costSizing(t, tt.arm, tt.normal)
		if got := [3]int{n, r, w}; got != tt.want {
			t.Fatalf("arm=%q sizing=%v, want %v", tt.arm, got, tt.want)
		}
	}
	for round, want := range [][]string{{"shared", "partitioned"}, {"partitioned", "shared"}, {"shared", "partitioned"}} {
		if !slices.Equal(costStage1Order(round), want) {
			t.Fatalf("round %d order did not alternate", round+1)
		}
	}
}

func TestCostStage1Aggregation(t *testing.T) {
	good := stepResult{scheduled: 3000, completed: 3000, deliveries: 3000000,
		queue: costEvidenceSeries(func(int) [4]int64 { return [4]int64{} }), postingDuration: 600 * time.Second, queueBound: 1000}
	for count := range 5 {
		passed, eligible := costStage1Verdict(slices.Repeat([]stepResult{good}, count))
		if passed != count || eligible != (count == 3) {
			t.Fatalf("windows=%d passed=%d eligible=%t; require exactly three", count, passed, eligible)
		}
	}
	if passed, eligible := costStage1Verdict([]stepResult{good, good, good, {}}); passed != 3 || eligible {
		t.Fatalf("extra incomplete window: passed=%d eligible=%t, want 3 false", passed, eligible)
	}
	for name, change := range map[string]func(*stepResult){
		"targets":      func(r *stepResult) { r.latency[1] = 251 * time.Millisecond },
		"posts":        func(r *stepResult) { r.completed-- },
		"inconclusive": func(r *stepResult) { r.postingDuration = 599 * time.Second },
		"incomplete":   func(r *stepResult) { r.queue = nil },
		"growth": func(r *stepResult) {
			r.queue = costEvidenceSeries(func(s int) [4]int64 { return [4]int64{int64(s)} })
		},
	} {
		for index := range 3 {
			windows := []stepResult{good, good, good}
			change(&windows[index])
			if passed, eligible := costStage1Verdict(windows); passed != 2 || eligible {
				t.Fatalf("%s window %d: passed=%d eligible=%t, want 2 false", name, index+1, passed, eligible)
			}
		}
	}
}
