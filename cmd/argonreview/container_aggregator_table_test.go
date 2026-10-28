package main

import (
	"errors"
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
		name, stream, stop, cap, first, body string
		code                                 int
		err                                  error
		state                                aggregatorRunState
	}
	complete := aggregatorRunState{completed: true}
	late := aggregatorRunState{completed: true, deadline: true}
	runningDeadline := aggregatorRunState{deadline: true, interrupted: true}
	rows := []row{
		{name: "A1", stream: fail, stop: "cap", cap: "total output", first: failedCap, body: "observed, total unknown"},
		{name: "A3 crossing", stop: "cap", cap: "total output", first: capFirst},
		{name: "A3 partial", stream: containerJSON("p", "start", "run/TestBad") + strings.TrimSuffix(containerJSON("p", "fail/TestBad"), "\n"), stop: "cap", cap: "total output", first: capFirst, state: complete},
		{name: "A4 package", stream: containerJSON("p", "start", "fail"), stop: "cap", cap: "total output", first: failedCap},
		{name: "A4 build", stream: `{"Action":"build-fail","ImportPath":"p"}` + "\n", stop: "cap", cap: "total output", first: failedCap},
		{name: "A5", stream: containerJSON("p", "start") + testOutput("p", "", "panic: broken"), stop: "cap", cap: "total output", first: capFirst, body: "panic: broken"},
		{name: "B1", stop: "cap", cap: "total output", first: capFirst, body: "Counts unavailable:"},
		{name: "B3", stream: containerJSON("p", "start", "run/TestSkip", "skip/TestSkip"), stop: "cap", cap: "total output", first: capFirst},
		{name: "C1", stop: "cap", cap: "total output", first: capFirst, state: runningDeadline},
		{name: "C2", stream: fail, stop: "cap", cap: "total output", first: capFirst, body: "observed, total unknown", state: runningDeadline},
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
		{name: "E7 exit", stream: pass, code: 125, first: "FAILED", state: complete},
		{name: "F", stream: pass, first: "verified", body: "Leaf tests: 1 passed, 0 failed, 0 skipped", state: complete},
	}
	for _, cap := range []string{"line length", "test count", "package count", "retained memory", "identity length", "event fields"} {
		rows = append(rows, row{name: "A2 " + cap, stream: fail, stop: "cap", cap: cap, first: failedCap, body: "Limit reached: " + cap}, row{name: "B2 " + cap, stop: "cap", cap: cap, first: capFirst, body: "Limit reached: " + cap})
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			a := aggregatorFixture(tc.stream)
			if tc.stop != "" {
				a.stop(tc.stop, tc.cap)
			}
			got := a.finish(tc.code, tc.err, tc.state)
			if aggregatorFirst(got) != tc.first || got.isError != (tc.first != "verified") {
				t.Fatalf("first %q, error=%v; want %q", aggregatorFirst(got), got.isError, tc.first)
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
	for _, stream := range []string{
		containerEvents("p", "TestOne", "pass"),
		containerJSON("p", "start", "run/TestBad", "fail/TestBad", "fail"),
		containerJSON("p", "start", "run/TestA", "pause/TestA", "run/TestB", "pass/TestB", "cont/TestA", "pass/TestA", "pass"),
		containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "skip/TestParent/child", "pass/TestParent", "pass"),
		measurementStream(e, e.message, true), measurementStream(e, "unconfirmed", true),
		containerJSON("p", "start") + testOutput("p", "TestLate", "noise") + containerJSON("p", "run/TestLate", "pass/TestLate", "pass"),
	} {
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
