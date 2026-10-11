package realtime_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A complete workload is necessary before any latency target can pass.
func costTargets(r stepResult) bool {
	return r.completed == r.scheduled && r.scheduled > 0 && r.missed == 0 &&
		!r.timedOut && r.missing == 0 && r.loopErrors == 0 && r.deliveries > 0 &&
		r.latency[1] <= 250*time.Millisecond && r.recountDelay[1] <= 2*time.Second && r.recountDelay[2] <= 5*time.Second
}

// Eligibility still needs the controlled run and a posting-baseline comparison.
func costRecommendable(r stepResult) bool { return costTargets(r) && !costQueueGrowing(r.queue) }

func TestCostTargetsRequireCompleteWorkload(t *testing.T) {
	complete := stepResult{scheduled: 300, completed: 300, deliveries: 1500000}
	if !costTargets(complete) {
		t.Fatal("complete fast workload rejected")
	}
	for _, change := range []func(*stepResult){
		func(r *stepResult) { r.missed = 1 }, func(r *stepResult) { r.completed-- },
		func(r *stepResult) { r.timedOut = true }, func(r *stepResult) { r.missing = 1 },
		func(r *stepResult) { r.loopErrors = 1 }, func(r *stepResult) { r.deliveries = 0 },
		func(r *stepResult) { r.latency[1] = 251 * time.Millisecond },
		func(r *stepResult) { r.recountDelay[1] = 3 * time.Second },
		func(r *stepResult) { r.recountDelay[2] = 6 * time.Second },
	} {
		r := complete
		change(&r)
		if costTargets(r) {
			t.Fatalf("incomplete or slow workload accepted: %+v", r)
		}
	}
}

func costHost(ctx context.Context, t *testing.T) bool {
	t.Helper()
	read := func(name string) string {
		out, err := exec.CommandContext(ctx, "sysctl", "-n", name).CombinedOutput()
		if err != nil {
			t.Fatalf("required host probe %s: %v: %s", name, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	load, cpus := read("vm.loadavg"), read("hw.ncpu")
	t.Logf("host: at=%s vm.loadavg=%s hw.ncpu=%s", time.Now().UTC().Format(time.RFC3339), load, cpus)
	fields := strings.Fields(strings.Trim(load, "{} "))
	n, err := strconv.Atoi(cpus)
	if err != nil || n <= 0 || len(fields) != 3 {
		t.Fatal("invalid required host probe")
	}
	one, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		t.Fatal(err)
	}
	return one <= float64(n)/2
}

type costPoolGroup struct{ rate, streams int }

func costPoolGroups(selected string) ([]costPoolGroup, error) {
	groups := []costPoolGroup{{5, 1000}, {5, 5000}, {20, 1000}, {20, 5000}}
	if selected == "" {
		return groups, nil
	}
	for _, group := range groups {
		if selected == fmt.Sprintf("%d/%d", group.rate, group.streams) {
			return []costPoolGroup{group}, nil
		}
	}
	return nil, fmt.Errorf("invalid RIBBITTO_STREAM_COST_LONG_GROUP=%q; want 5/1000, 5/5000, 20/1000 or 20/5000", selected)
}

// The previous window's own load must decay before starting another. Post-run
// load includes this window's work; only the orchestrator checks interference.
func costLoadWindow(ctx context.Context, probe func() bool, measure func() bool) error {
	for !probe() {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(10 * time.Second):
		}
	}
	ok := measure()
	probe()
	if !ok {
		return fmt.Errorf("measurement failed; remaining ladder not run")
	}
	return nil
}

func TestCostPoolGroups(t *testing.T) {
	want := []costPoolGroup{{5, 1000}, {5, 5000}, {20, 1000}, {20, 5000}}
	if got, err := costPoolGroups(""); err != nil || !slices.Equal(got, want) {
		t.Fatalf("all groups: %v, %v", got, err)
	}
	for _, group := range want {
		selected := fmt.Sprintf("%d/%d", group.rate, group.streams)
		if got, err := costPoolGroups(selected); err != nil || !slices.Equal(got, []costPoolGroup{group}) {
			t.Fatalf("selected %s: %v, %v", selected, got, err)
		}
	}
	for _, selected := range []string{"20", "20/500", "10/5000", "20/5000/extra"} {
		if _, err := costPoolGroups(selected); err == nil {
			t.Fatalf("invalid group %q accepted", selected)
		}
	}
}

func TestCostLoadWindowGatesOnlyBefore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, probes, measurements := time.Now(), 0, 0
		err := costLoadWindow(t.Context(), func() bool {
			probes++
			return probes == 2 // High before, low before, high after.
		}, func() bool {
			measurements++
			if probes != 2 || time.Since(start) != 10*time.Second {
				t.Fatal("window started before pre-run load decayed")
			}
			return true
		})
		if err != nil || probes != 3 || measurements != 1 {
			t.Fatalf("post-run load must be logged without rejection or retry: probes=%d measurements=%d error=%v", probes, measurements, err)
		}
	})
}

// TestStreamCostPoolLadder is deliberately separate from normal checks. Each
// repetition creates identical fresh fixtures; reversing candidate order and
// interleaving pool10 references limits drift. Select one rate/streams group
// with RIBBITTO_STREAM_COST_LONG_GROUP; unset runs all four in each round.
func TestStreamCostPoolLadder(t *testing.T) {
	if os.Getenv("RIBBITTO_STREAM_COST_LONG") != "1" {
		t.Skip("set RIBBITTO_STREAM_COST_LONG=1 for the controlled 60s pool ladder")
	}
	started := time.Now()
	t.Cleanup(func() { t.Logf("ladder invocation wall=%s", time.Since(started)) })
	groups, err := costPoolGroups(os.Getenv("RIBBITTO_STREAM_COST_LONG_GROUP"))
	if err != nil {
		t.Fatal(err)
	}
	// go test runs this package in internal/realtime, two levels below root.
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
	for key, value := range map[string]string{
		"RIBBITTO_STREAM_COST": "1", "RIBBITTO_STREAM_COST_SIDEBAR": "1",
		"RIBBITTO_STREAM_COST_MEMBERS": "distinct", "RIBBITTO_STREAM_COST_CACHE": "1",
		"RIBBITTO_STREAM_COST_DURATION": "60s", "RIBBITTO_STREAM_COST_ATTRIBUTION": "1",
		"RIBBITTO_STREAM_COST_RECOUNTS": "", "RIBBITTO_STREAM_COST_STAGGER": "",
		"RIBBITTO_STREAM_COST_SMOKE": "",
	} {
		t.Setenv(key, value)
	}
	for round := range 3 {
		arms := []string{"10", "20", "10", "30", "10", "40", "sync40", "async40", "async10+30", "async40", "sync40", "10", "gate10"}
		if round%2 == 1 {
			arms = []string{"10", "gate10", "10", "40", "sync40", "async40", "async10+30", "async40", "sync40", "10", "30", "10", "20"}
		}
		for _, group := range groups {
			for run, arm := range arms {
				err := costLoadWindow(t.Context(), func() bool { return costHost(t.Context(), t) }, func() bool {
					return t.Run(fmt.Sprintf("round%d/rate%d/streams%d/run%d/%s", round+1, group.rate, group.streams, run+1, arm), func(t *testing.T) {
						t.Setenv("RIBBITTO_STREAM_COST_RATE", strconv.Itoa(group.rate))
						t.Setenv("RIBBITTO_STREAM_COST_STEPS", strconv.Itoa(group.streams))
						t.Setenv("RIBBITTO_STREAM_COST_ARM", "")
						t.Setenv("RIBBITTO_STREAM_COST_RECOUNTS", "")
						pool := arm
						switch arm {
						case "sync40", "async40", "async10+30":
							t.Setenv("RIBBITTO_STREAM_COST_ARM", arm)
							pool = "40"
						case "gate10":
							t.Setenv("RIBBITTO_STREAM_COST_RECOUNTS", "10")
							pool = "10"
						}
						t.Setenv("RIBBITTO_STREAM_COST_POOL", pool)
						TestStreamCost(t)
					})
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
