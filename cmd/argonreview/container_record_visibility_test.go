package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// visibilityFixture fills the first-come index without allocating a whole stream.
// Wide identities reach the name-byte limit before the entry limit.
func visibilityFixture(skips, width, failures int) *containerAggregator {
	a := aggregatorFixture(containerJSON("p", "start"))
	for i := 0; i < skips; i++ {
		name := fmt.Sprintf("TestSkip%04d", i)
		name += strings.Repeat("x", max(0, width-len(name)))
		aggregatorWrite(a, containerJSON("p", "run/"+name, "skip/"+name))
	}
	for i := 0; i < failures; i++ {
		name := fmt.Sprintf("TestBad%d", i)
		aggregatorWrite(a, containerJSON("p", "run/"+name, "fail/"+name))
	}
	return a
}

// visibilityStop drives real stop paths; priming streamed avoids a 32 MiB flood
// per row, as in TestAggregatorCrossingLine.
func visibilityStop(a *containerAggregator, stop string) aggregatorResult {
	state := aggregatorRunState{completed: true}
	code := 0
	var err error
	switch stop {
	case "cap", "cap deadline":
		a.record.streamed = aggregatorStreamLimit
		_, _ = a.stderrWriter().Write([]byte("x"))
		state.deadline = stop == "cap deadline"
	case "deadline":
		state = aggregatorRunState{deadline: true, interrupted: true}
		code, err = -1, context.DeadlineExceeded
	case "kill":
		code, state.completed = 137, false
	case "tail":
		aggregatorWrite(a, "{")
	case "malformed":
		aggregatorWrite(a, "garbage\n")
	case "complete":
		aggregatorWrite(a, containerJSON("p", "fail"))
	}
	return a.finish(code, err, state)
}

// visibilityInput renders the exact sealed record used by the host report.
func visibilityInput(a *containerAggregator, result aggregatorResult) summaryInput {
	return summaryInput{check: "test", pattern: ".", scope: "fixture", verdict: result.verdict, facts: recordSummaryFacts(a.record)}
}

// TestRecordFailureVisibility covers first-come retention at both index limits.
// D5 deliberately stays minimal even when the record has an unretained failure.
func TestRecordFailureVisibility(t *testing.T) {
	for _, wide := range []bool{false, true} {
		limit, width := aggregatorIndexLimit, 0
		if wide {
			width = summaryIdentityLimit/2 - len("p")
			limit = aggregatorNameLimit / (len("p") + width)
		}
		for _, delta := range []int{-1, 0, 1} {
			for _, stop := range []string{"cap", "cap deadline", "deadline", "kill", "tail", "malformed", "missing"} {
				t.Run(fmt.Sprintf("wide=%v/delta=%d/%s", wide, delta, stop), func(t *testing.T) {
					a := visibilityFixture(limit+delta, width, 1)
					result := visibilityStop(a, stop)
					got := buildContainerSummary(visibilityInput(a, result))
					retained := 0
					if delta < 0 {
						retained = 1
					}
					if a.record.failedLeaves != 1 || a.record.confirmedFailures != 1 || a.record.namesNotRetained != 1-retained {
						t.Fatalf("first-come failure accounting: retained=%d not retained=%d confirmed=%d", retained, a.record.namesNotRetained, a.record.confirmedFailures)
					}
					wantFirst := "FAILED"
					switch stop {
					case "cap":
						wantFirst = "FAILED: output limit exceeded after a confirmed failure (output cut off)"
					case "cap deadline":
						wantFirst = "NOT VERIFIED: output limit exceeded (output cut off)"
					case "deadline":
						wantFirst = "NOT VERIFIED: time limit exceeded"
					case "tail":
						wantFirst = "FAILED: empty or truncated test stream"
					case "malformed":
						wantFirst = "FAILED: malformed test stream"
					}
					if aggregatorFirst(result) != wantFirst || !result.isError || !strings.HasPrefix(got, wantFirst+"\n") {
						t.Fatalf("verdict changed: %q summary=%q", result.verdict, got)
					}
					if stop == "deadline" {
						want := "NOT VERIFIED: time limit exceeded\nCheck: test .\nScope: fixture\nDetails unavailable: the check did not complete.\n" + deadlineOmitted
						if got != want {
							t.Fatalf("D5 fixed minimal summary changed: %q", got)
						}
						return
					}
					if strings.Contains(got, "p/TestBad0") != (retained == 1) {
						t.Fatalf("retained failure visibility differs: %s", got)
					}
					if !strings.Contains(got, "Failures (observed, total unknown):") || strings.Contains(got, " of ") || strings.Contains(got, "Leaf tests:") || strings.Contains(got, "Packages:") {
						t.Fatalf("incomplete failure must remain observed, total unknown: %s", got)
					}
					notice := "Observed failures not named (not retained): 1\n"
					if strings.Contains(got, notice) != (retained == 0) {
						t.Fatalf("unretained failure notice missing or inaccurate: %s", got)
					}
				})
			}
		}
	}
}

// TestRecordFailureVisibilityUnavailable keeps observations out of all fixed
// Details-unavailable summaries, even when the sealed record has storage loss.
func TestRecordFailureVisibilityUnavailable(t *testing.T) {
	a := visibilityFixture(aggregatorIndexLimit, 0, 1)
	result := visibilityStop(a, "tail")
	for _, verdict := range []string{"NOT VERIFIED: cancelled", "FAILED: source cleanup failed", "FAILED: the check returned an error", "FAILED"} {
		t.Run(verdict, func(t *testing.T) {
			in := visibilityInput(a, result)
			in.verdict, in.runnerFailed = verdict, true
			want := verdict + "\nCheck: test .\nScope: fixture\nDetails unavailable: the check did not complete.\n" + deadlineOmitted
			if got := buildContainerSummary(in); got != want {
				t.Fatalf("Details-unavailable body changed: %q", got)
			}
		})
	}
}
