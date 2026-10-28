package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// These helpers belong to 1a so that the table and oracle compile on their own.
func aggregatorWrite(a *containerAggregator, text string) {
	_, _ = a.stdoutWriter().Write([]byte(text))
}

func aggregatorFixture(text string) *containerAggregator {
	a := newContainerAggregator(nil, nil)
	aggregatorWrite(a, text)
	return a
}

func aggregatorFirst(result aggregatorResult) string {
	first, _, _ := strings.Cut(result.verdict, "\n")
	return first
}

func aggregatorFinish(a *containerAggregator) aggregatorResult {
	return a.finish(0, nil, aggregatorRunState{completed: true})
}

func TestAggregatorVerdictTable(t *testing.T) {
	pass := containerEvents("p", "TestOne", "pass")
	fail := containerJSON("p", "start", "run/TestBad", "fail/TestBad")
	const capFirst = "NOT VERIFIED: output limit exceeded (output cut off)"
	const failedCap = "FAILED: output limit exceeded after a confirmed failure (output cut off)"
	type row struct {
		name, stream, cap, first, body string
		stop                           aggregatorStopReason
		code                           int
		err                            error
		state                          aggregatorRunState
		crossing                       bool
		verdict                        string
	}
	complete := aggregatorRunState{completed: true}
	late := aggregatorRunState{completed: true, deadline: true}
	runningDeadline := aggregatorRunState{deadline: true, interrupted: true}
	rows := []row{
		{name: "A1", stream: fail, stop: stopCap, cap: "total output", first: failedCap, body: "observed, total unknown"},
		{name: "A3 crossing", stream: containerJSON("p", "start", "run/TestBad"), crossing: true, first: capFirst},
		{name: "A3 partial", stream: containerJSON("p", "start", "run/TestBad") + strings.TrimSuffix(containerJSON("p", "fail/TestBad"), "\n"), stop: stopCap, cap: "total output", first: capFirst, state: complete},
		{name: "A4 package", stream: containerJSON("p", "start", "fail"), stop: stopCap, cap: "total output", first: failedCap},
		{name: "A4 build", stream: `{"Action":"build-fail","ImportPath":"p"}` + "\n", stop: stopCap, cap: "total output", first: failedCap},
		{name: "A5", stream: containerJSON("p", "start") + testOutput("p", "", "panic: broken"), stop: stopCap, cap: "total output", first: capFirst, body: "panic: broken"},
		{name: "B1", stop: stopCap, cap: "total output", first: capFirst, body: "Counts unavailable:"},
		{name: "B3", stream: containerJSON("p", "start", "run/TestSkip", "skip/TestSkip"), stop: stopCap, cap: "total output", first: capFirst},
		{name: "C1", stop: stopCap, cap: "total output", first: capFirst, state: runningDeadline},
		{name: "C2", stream: fail, stop: stopCap, cap: "total output", first: capFirst, body: "observed, total unknown", state: runningDeadline},
		{name: "D1", stream: pass, first: containerTimeLimitVerdict, body: "Details unavailable: the check did not complete.", state: late},
		{name: "D2", stream: fail + containerJSON("p", "fail"), first: "FAILED", body: "Leaf tests: 0 passed, 1 failed", state: late},
		{name: "D3 skips", stream: containerEvents("p", "TestSkip", "skip"), first: "NOT VERIFIED: 1 skipped", state: late},
		{name: "D3 no tests", stream: containerJSON("p", "start", "pass"), first: "NOT VERIFIED: a targeted package has no tests, or no leaf test passed", state: late},
		{name: "D4", stream: pass, code: 125, first: "FAILED", body: "Details unavailable: the check did not complete.", state: late},
		{name: "D5", stream: fail, first: containerTimeLimitVerdict, body: "Details unavailable: the check did not complete.", state: runningDeadline},
		{name: "E1", stream: fail, code: 137, first: "FAILED", body: "observed, total unknown"},
		{name: "E2", stream: containerJSON("p", "start"), code: 137, first: "FAILED", body: "Counts unavailable: the stream was cut."},
		{name: "E3", stream: fail + `{"Action":`, first: "FAILED: empty or truncated test stream", body: "observed, total unknown", state: complete},
		{name: "E4", stream: fail, first: "FAILED", body: "observed, total unknown", state: complete},
		{name: "E5", stream: fail + "garbage\n", first: "FAILED: malformed test stream", body: "observed, total unknown", state: complete},
		{name: "E6", stream: fail, first: "FAILED", body: "Details unavailable: the check did not complete.", state: aggregatorRunState{parentCancelled: true, interrupted: true}},
		{name: "E7 runner", stream: pass, first: "FAILED", err: errors.New("runner failed"), body: "Details unavailable: the check did not complete."},
		{name: "E7 late runner", stream: pass, first: "FAILED", err: errors.New("runner failed"), state: aggregatorRunState{deadline: true}, body: "Details unavailable: the check did not complete."},
		{name: "E7 exit", stream: pass, code: 125, first: "FAILED", state: complete, body: "Details unavailable: the check did not complete."},
		{name: "F", stream: pass, first: "verified", body: "Leaf tests: 1 passed, 0 failed, 0 skipped", state: complete},
	}
	for _, cap := range []string{"line length", "test count", "package count", "retained memory", "identity length", "event fields"} {
		rows = append(rows, row{name: "A2 " + cap, stream: fail, stop: stopCap, cap: cap, first: failedCap, body: "Limit reached: " + cap}, row{name: "B2 " + cap, stop: stopCap, cap: cap, first: capFirst, body: "Limit reached: " + cap})
	}
	for _, tc := range aggregatorParentSkips() {
		first, _, _ := strings.Cut(tc.verdict, "\n")
		rows = append(rows, row{name: "F parent " + tc.name, stream: tc.stream, first: first, verdict: tc.verdict, state: complete})
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			a := aggregatorFixture(tc.stream)
			if tc.crossing {
				line := containerJSON("p", "fail/TestBad")
				a.record.streamed = aggregatorStreamLimit - len(line) + 1
				aggregatorWrite(a, line)
				if a.record.capName != "total output" || a.record.confirmedFailures != 0 {
					t.Fatal("A3 crossing line supplied observed failure evidence")
				}
			}
			if tc.stop != stopNone {
				a.stop(tc.stop, tc.cap)
			}
			got := a.finish(tc.code, tc.err, tc.state)
			if strings.HasPrefix(tc.name, "A3") && (a.record.confirmedFailures != 0 || strings.Contains(got.body, "observed, total unknown") || strings.Contains(got.body, "p/TestBad")) {
				t.Fatal("A3 crossing or partial line supplied observed failures")
			}
			if aggregatorFirst(got) != tc.first || got.isError != (tc.first != "verified") {
				t.Fatalf("first %q, error=%v; want %q", aggregatorFirst(got), got.isError, tc.first)
			}
			if tc.verdict != "" && got.verdict != tc.verdict {
				t.Fatalf("verdict %q want %q", got.verdict, tc.verdict)
			}
			if strings.ContainsAny(tc.name[:1], "DEF") {
				oracle, _, _ := strings.Cut(oracleRunVerdict(tc.code, tc.err, []byte(tc.stream), tc.state), "\n")
				if aggregatorFirst(got) != oracle {
					t.Fatalf("first %q, runner oracle %q", aggregatorFirst(got), oracle)
				}
			}
			if !strings.Contains(got.body, tc.body) {
				t.Fatalf("body %q missing %q", got.body, tc.body)
			}
			if !a.record.complete && (strings.Contains(got.body, "Leaf tests:") || strings.Contains(got.body, "Packages:")) {
				t.Fatal("incomplete stream supplied totals")
			}
			if aggregatorFirst(got) == containerTimeLimitVerdict && got.body != "Details unavailable: the check did not complete.\n"+deadlineOmitted {
				t.Fatalf("timeout body changed: %q", got.body)
			}
		})
	}
}

func TestAggregatorOracle(t *testing.T) {
	e := measurementSkips()[0]
	streams := []string{
		containerEvents("p", "TestOne", "pass"),
		containerJSON("p", "start", "run/TestBad", "fail/TestBad", "fail"),
		containerJSON("p", "start", "run/TestA", "pause/TestA", "run/TestB", "pass/TestB", "cont/TestA", "pass/TestA", "pass"),
		containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "skip/TestParent/child", "pass/TestParent", "pass"),
		measurementStream(e, e.message, true), measurementStream(e, "unconfirmed", true),
		containerJSON("p", "start") + testOutput("p", "TestLate", "noise") + containerJSON("p", "run/TestLate", "pass/TestLate", "pass"),
	}
	for _, tc := range aggregatorParentSkips() {
		streams = append(streams, tc.stream)
	}
	for _, stream := range streams {
		a := newContainerAggregator(nil, measurementSkips())
		aggregatorWrite(a, stream)
		got := aggregatorFinish(a)
		want := oracleVerdict(0, nil, []byte(stream), false, measurementSkips())
		if got.verdict != want {
			t.Fatalf("got %q; oracle %q", got.verdict, want)
		}
		f, confirmed := oracleCompleteFacts([]byte(stream), measurementSkips())
		if a.record.confirmedFailures != confirmed || a.record.events != f.events || a.record.packages != f.packages || a.record.leaves != f.leaves || strings.Join(a.record.allowed, "\n") != strings.Join(f.allowed, "\n") || strings.Join(a.skipped, "\n") != strings.Join(f.skipped, "\n") {
			t.Fatal("counts or skip names differ from oracle facts")
		}
	}
}

func aggregatorParentSkips() []struct{ name, stream, verdict string } {
	return []struct{ name, stream, verdict string }{
		{"after child", containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "skip/TestParent/child", "skip/TestParent", "pass"), "NOT VERIFIED: 2 skipped\np/TestParent\np/TestParent/child"},
		{"before child", containerJSON("p", "start", "run/TestParent", "skip/TestParent", "run/TestParent/child", "skip/TestParent/child", "pass"), "NOT VERIFIED: 2 skipped\np/TestParent\np/TestParent/child"},
		{"passing child", containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "pass/TestParent/child", "skip/TestParent", "pass"), "NOT VERIFIED: 1 skipped\np/TestParent"},
	}
}

func TestAggregatorSkippedParent(t *testing.T) {
	for _, tc := range aggregatorParentSkips() {
		t.Run(tc.name, func(t *testing.T) {
			a := aggregatorFixture(tc.stream)
			got := aggregatorFinish(a)
			if got.verdict != tc.verdict || got.verdict != oracleVerdict(0, nil, []byte(tc.stream), false, nil) {
				t.Fatalf("parent skip lost: got %q want %q", got.verdict, tc.verdict)
			}
		})
	}
}

func TestAggregatorRunStateTable(t *testing.T) {
	for _, code := range []int{0, 1, 2, 137} {
		for bits := 0; bits < 1<<5; bits++ {
			state := aggregatorRunState{completed: bits&1 != 0, interrupted: bits&2 != 0, deadline: bits&4 != 0, parentCancelled: bits&8 != 0}
			var runErr error
			if bits&16 != 0 {
				runErr = errors.New("runner failed")
			}
			t.Run(fmt.Sprintf("exit=%d/completed=%v/interrupted=%v/deadline=%v/parent=%v/error=%v", code, state.completed, state.interrupted, state.deadline, state.parentCancelled, runErr != nil), func(t *testing.T) {
				a := aggregatorFixture(containerEvents("p", "TestOne", "pass"))
				got := a.finish(code, runErr, state)
				want := "FAILED"
				switch {
				case state.deadline && state.interrupted:
					want = containerTimeLimitVerdict
				case code != 0 || runErr != nil || state.parentCancelled:
				case !state.completed || state.interrupted:
					want = "FAILED: incomplete run"
				case state.deadline:
					want = containerTimeLimitVerdict
				default:
					want = "verified"
				}
				if aggregatorFirst(got) != want || got.isError != (want != "verified") {
					t.Fatalf("run classification: got %q want %q", got.verdict, want)
				}
				if (!state.completed || state.interrupted || runErr != nil) && (a.record.complete || a.record.stop == stopNone) {
					t.Fatalf("incomplete run lacks reason: complete=%v stop=%s", a.record.complete, a.record.stop)
				}
				if code == 0 && runErr != nil && !state.deadline && !state.parentCancelled && a.record.stop != stopRunnerError {
					t.Fatalf("healthy-context runner error has no own reason: %s", a.record.stop)
				}
			})
		}
	}
}

func TestAggregatorRequiresComplete(t *testing.T) {
	a := newContainerAggregator(nil, nil)
	a.record.leaves.passed = 1
	if got := a.decide(0, nil, aggregatorRunState{completed: true}); aggregatorFirst(got) == "verified" {
		t.Fatal("verified without a complete record")
	}
}

func TestAggregatorMissingOrder(t *testing.T) {
	for _, tc := range []struct{ stream, want string }{
		{containerJSON("a", "start", "run/TestZ", "run/TestA", "pass") + containerJSON("z", "start"), "FAILED: missing test result"},
		{containerJSON("z", "start", "run/TestA", "pass") + containerJSON("a", "start", "run/TestZ"), "FAILED: missing package result"},
	} {
		for i := 0; i < summaryFieldLimit; i++ {
			got := aggregatorFinish(aggregatorFixture(tc.stream))
			if got.verdict != tc.want || oracleVerdict(0, nil, []byte(tc.stream), false, nil) != tc.want {
				t.Fatalf("missing-result order: got %q want %q", got.verdict, tc.want)
			}
		}
	}
}
