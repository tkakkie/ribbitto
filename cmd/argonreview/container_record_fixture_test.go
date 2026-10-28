package main

import (
	"fmt"
	"strings"
	"testing"
)

func testVerdict(code int, runErr error, data []byte, cut bool) string {
	return testVerdictWithAllowlist(code, runErr, data, cut, nil)
}

func testVerdictWithAllowlist(code int, runErr error, data []byte, cut bool, allowlist []measurementSkip) string {
	a := newContainerAggregator(nil, allowlist)
	_, _ = a.stdoutWriter().Write(data)
	if cut {
		a.stop(stopCap, "total output")
	}
	return a.finish(code, runErr, aggregatorRunState{completed: runErr == nil}).verdict
}

func recordFactsFixture(check string, stdout, stderr []byte, cut bool) summaryFacts {
	test2json, _ := containerCheckKind(check)
	if !test2json {
		output := containerOutput{over: cut}
		_, _ = output.stdout.Write(stdout)
		_, _ = output.stderr.Write(stderr)
		return recordSummaryFacts(commandRecord(&output))
	}
	var allowlist []measurementSkip
	if check == "test-db" {
		allowlist = measurementSkips()
	}
	a := newContainerAggregator(nil, allowlist)
	_, _ = a.stderrWriter().Write(stderr)
	_, _ = a.stdoutWriter().Write(stdout)
	if cut {
		a.stop(stopCap, "total output")
	}
	a.finish(0, nil, aggregatorRunState{completed: true})
	return recordSummaryFacts(a.record)
}

func browserVerdictFixture(importPath, run string, code int, err error, data []byte, cut bool) string {
	verdict, _ := browserVerdictRecordFixture(importPath, run, code, err, data, cut)
	return verdict
}

func browserVerdictRecordFixture(importPath, run string, code int, err error, data []byte, cut bool) (string, *containerRecord) {
	a := newContainerAggregator(nil, nil)
	a.retainBrowserScope(importPath, run)
	_, _ = a.stdoutWriter().Write(data)
	if cut {
		a.stop(stopCap, "total output")
	}
	result := a.finish(code, err, aggregatorRunState{completed: err == nil})
	return browserVerdict(run, a.record, result), a.record
}

// assertDeadlineRecord moves the old raw-stream report comparison to the
// retained counts, names and stderr. The expected counts use the legacy oracle.
func assertDeadlineRecord(t *testing.T, check, first, stdout, stderr, report string, r *containerRecord) {
	t.Helper()
	if r == nil || !r.sealed {
		t.Fatal("deadline report has no sealed record")
	}
	if !strings.HasPrefix(report, first+"\n"+containerCheckScope(check)+"\n") || strings.Contains(report, `"Action"`) || strings.Contains(report, "--- stdout") {
		t.Fatalf("verdict/scope or raw stream: %s", report)
	}
	if strings.HasPrefix(stdout, "{\"Action\"") && strings.HasSuffix(stdout, "\n") {
		expected, _ := oracleCompleteFacts([]byte(stdout), nil)
		if r.packages != expected.packages || r.leaves != expected.leaves {
			t.Fatalf("record counts %+v/%+v, want %+v/%+v", r.packages, r.leaves, expected.packages, expected.leaves)
		}
	}
	if r.complete {
		count := fmt.Sprintf("Leaf tests: %d passed, %d failed, %d skipped", r.leaves.passed, r.leaves.failed, r.leaves.skipped)
		if !strings.Contains(report, count) {
			t.Fatalf("record counts missing: %s", report)
		}
	}
	if r.stderr.total > 0 && (!strings.Contains(report, "Standard error:\n  | "+stderr+"\n") || string(r.stderr.head) != stderr) {
		t.Fatal("host record lost stderr")
	}
	if r.stderr.total == 0 && r.stop != stopCap && r.stop != stopMalformed && stderr != "" {
		t.Fatal("stderr was neither retained nor refused after stop")
	}
	for _, e := range r.entries {
		if e.state == "fail" && !strings.Contains(report, e.pkg+"/"+e.name) {
			t.Fatal("host lost observed failure identity")
		}
	}
}
