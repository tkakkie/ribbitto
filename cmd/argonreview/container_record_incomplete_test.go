package main

import (
	"errors"
	"strings"
	"testing"
)

func TestContainerRecordIncompleteReason(t *testing.T) {
	pass := containerEvents("p", "TestOne", "pass")
	complete := aggregatorRunState{completed: true}
	const cut = "Counts unavailable: the stream was cut.\n"
	const incomplete = "Counts unavailable: the stream is incomplete.\n"
	for _, tc := range []struct {
		name, stream, want string
		stop               aggregatorStopReason
		code               int
		err                error
		state              aggregatorRunState
	}{
		{name: "cap", stream: pass, stop: stopCap, state: complete, want: "Counts unavailable: output limit exceeded; the stream was cut.\n"},
		{name: "kill", stream: pass, code: 137, stop: stopKill, state: complete, want: cut},
		{name: "truncated tail", stream: pass + `{"Action":`, stop: stopTruncated, state: complete, want: cut},
		{name: "malformed", stream: pass + "garbage\n", stop: stopMalformed, state: complete, want: incomplete},
		{name: "missing package result", stream: containerJSON("p", "start", "run/TestOne", "pass/TestOne"), stop: stopMissingPackage, state: complete, want: incomplete},
		{name: "missing test result", stream: containerJSON("p", "start", "run/TestOne", "pass"), stop: stopMissingTest, state: complete, want: incomplete},
		{name: "interrupted", stream: pass, stop: stopIncompleteRun, state: aggregatorRunState{interrupted: true}, want: incomplete},
		{name: "runner error", stream: pass, stop: stopRunnerError, err: errors.New("runner failed"), want: incomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := aggregatorFixture(tc.stream)
			if tc.stop == stopCap {
				a.stop(stopCap, "total output")
			}
			result := a.finish(tc.code, tc.err, tc.state)
			if a.record.complete || a.record.stop != tc.stop {
				t.Fatalf("fixture complete=%v, stop=%s; want incomplete, %s", a.record.complete, a.record.stop, tc.stop)
			}
			in := summaryInput{check: "test", verdict: result.verdict, facts: recordSummaryFacts(a.record)}
			for _, output := range []struct{ name, text string }{
				{"host", recordHostReport(result.verdict, containerCheckScope("test"), a.record)},
				{"MCP", buildContainerSummary(in)},
			} {
				t.Run(output.name, func(t *testing.T) {
					if !strings.Contains(output.text, tc.want) || strings.Count(output.text, "Counts unavailable:") != 1 {
						t.Fatalf("incomplete reason: want %q exactly once:\n%s", tc.want, output.text)
					}
					if strings.Contains(output.text, "Packages:") || strings.Contains(output.text, "Leaf tests:") {
						t.Fatalf("incomplete record supplied totals:\n%s", output.text)
					}
				})
			}
			// Runner failures retain their fixed MCP body even though the record
			// projection carries the neutral reason for other readers.
			if tc.err != nil {
				in.runnerFailed = true
				want := "FAILED\nCheck: test \nScope: \nDetails unavailable: the check did not complete.\n" + deadlineOmitted
				if got := buildContainerSummary(in); got != want {
					t.Fatalf("runner-error MCP body changed: %q, want %q", got, want)
				}
			}
		})
	}
}
