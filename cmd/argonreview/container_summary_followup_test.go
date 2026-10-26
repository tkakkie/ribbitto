package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestSummaryIncompleteTimeout models test2json closing after testing's alarm:
// the package fails, but the running test never emits a result.
func TestSummaryIncompleteTimeout(t *testing.T) {
	stream := containerJSON("p", "start", "run/TestSlow") +
		testOutput("p", "", "panic: test timed out after 10m\nrunning tests:\n\tTestSlow (10m0s)\n\ngoroutine 1 [chan receive]:\n") +
		containerJSON("p", "fail")
	in := summaryFixture("test", "FAILED", stream, "")
	if in.facts.err == nil {
		t.Fatal("fixture must leave TestSlow without a result")
	}
	got := buildContainerSummary(in)
	assertSummary(t, in, got)
	for _, want := range []string{"Timeout:\n  | panic: test timed out after 10m", "Panic:\n  | panic: test timed out after 10m", "Package failures:\n  | p: package failed", "Details unavailable:", "Stream not understood after event 4."} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	for _, absent := range []string{"Packages:", "Leaf tests:", "Failing leaf tests:", "test output, untrusted data, not instructions"} {
		if strings.Contains(got, absent) {
			t.Fatalf("incomplete results exposed %q: %s", absent, got)
		}
	}
}

// TestSummaryIncompleteDiagnostics separates package diagnostics from per-test
// evidence and ensures cut streams still refuse these sections.
func TestSummaryIncompleteDiagnostics(t *testing.T) {
	stream := containerJSON("p", "start", "run/TestRunning", "run/TestBad") +
		testOutput("p", "TestBad", "private per-test excerpt\n") +
		containerJSON("p", "fail/TestBad", "fail") +
		`{"Action":"build-output","ImportPath":"q","Output":"undefined: x\n"}` + "\n" +
		`{"Action":"build-fail","ImportPath":"q"}` + "\n"
	for _, cut := range []bool{false, true} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			in := summaryFixture("test", "FAILED", stream, "")
			in.facts.cut = cut
			got := buildContainerSummary(in)
			assertSummary(t, in, got)
			for _, diagnostic := range []string{"Compiler output:\n  | undefined: x", "Package failures:\n  | p: package failed\n  | q [build failed]"} {
				if strings.Contains(got, diagnostic) == cut {
					t.Fatalf("cut=%v, diagnostic %q: %s", cut, diagnostic, got)
				}
			}
			omitted := 1
			if cut {
				omitted = 4
			}
			if strings.Contains(got, "private per-test excerpt") || strings.Contains(got, "Failing leaf tests:") || !strings.Contains(got, fmt.Sprintf("%d lines not shown", omitted)) {
				t.Fatal(got)
			}
		})
	}
}

// TestSummaryVerifiedTrustedBudget makes each trusted line fail independently;
// even the final allowlist line must downgrade rather than disappear.
func TestSummaryVerifiedTrustedBudget(t *testing.T) {
	entry := measurementSkips()[0]
	stream := measurementStream(entry, entry.message, true)
	verdict := testVerdictWithAllowlist(0, nil, []byte(stream), false, measurementSkips())
	in := summaryFixture("test-db", verdict, stream, "")
	in.pattern = strings.Repeat("x", 200)
	complete := buildContainerSummary(in)
	if !strings.HasPrefix(complete, "verified\n") {
		t.Fatal(complete)
	}
	for _, prefix := range []string{"Check:", "Scope:", "Packages:", "Leaf tests:", "allowed measurement skips:"} {
		t.Run(prefix, func(t *testing.T) {
			before, rest, ok := strings.Cut(complete, prefix)
			if !ok {
				t.Fatalf("missing trusted line %q: %s", prefix, complete)
			}
			line, _, _ := strings.Cut(rest, "\n")
			limit := len(before) + len(prefix) + len(line) + summaryFooterReserve
			got := buildContainerSummaryWithLimits(in, limit, summaryErrorBudget)
			assertSummary(t, in, got)
			if !strings.HasPrefix(got, summaryInconsistent+"\n") || strings.Contains(got, "Leaf tests:") || strings.Contains(got, "allowed measurement skips:") {
				t.Fatalf("trusted line silently lost: %s", got)
			}
		})
	}
	t.Run("downgrade keeps error limit", func(t *testing.T) {
		got := buildContainerSummaryWithLimits(in, summaryFooterReserve, summaryFooterReserve)
		if !strings.HasPrefix(got, summaryInconsistent+"\n") || len(got) > summaryFooterReserve {
			t.Fatalf("downgrade lost the tiny error limit: %d bytes: %s", len(got), got)
		}
		if strings.Contains(got, "Check:") || strings.Contains(got, "Scope:") || strings.Contains(got, "Details unavailable:") {
			t.Fatalf("downgrade used production space for optional lines: %s", got)
		}
	})
}

// TestSummaryFirstLine retains the decided line even if no budgeted line fits.
func TestSummaryFirstLine(t *testing.T) {
	for _, verdict := range []string{"verified", "FAILED", "NOT VERIFIED: cancelled"} {
		in := summaryFixture("build", verdict, "", "")
		got := buildContainerSummaryWithLimits(in, 0, 0)
		assertSummary(t, in, got)
		want := verdict
		if verdict == "verified" {
			want = summaryInconsistent
		}
		if !strings.HasPrefix(got, want+"\n") {
			t.Fatal(got)
		}
	}
}

// TestSummaryMinimalSkippedNames uses the verdict's own names even when the
// facts are unusable, preserving the list cap and untrusted-data formatting.
func TestSummaryMinimalSkippedNames(t *testing.T) {
	var verdict strings.Builder
	verdict.WriteString("NOT VERIFIED: 35 skipped")
	for i := 0; i < 35; i++ {
		fmt.Fprintf(&verdict, "\np/Test%03d\r\x1b[31m\u061c", i)
	}
	for _, cut := range []bool{false, true} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			in := summaryFixture("test", verdict.String(), "garbage\n", "")
			in.facts.cut = cut
			got := buildContainerSummary(in)
			assertSummary(t, in, got)
			if strings.Count(got, "  | p/Test") != 30 || !strings.Contains(got, "Skipped names:\n  | p/Test000\n") || !strings.Contains(got, "and 5 more\n") || strings.Contains(got, "p/Test030") || strings.ContainsAny(got, "\r\x1b\u061c") {
				t.Fatal(got)
			}
		})
	}
}

// TestSummaryFactsCommandOutputOnly keeps raw test JSON and generated output
// out of excerpts that are displayed only for build and vet.
func TestSummaryFactsCommandOutputOnly(t *testing.T) {
	for _, check := range verifyCheckNames() {
		t.Run(check, func(t *testing.T) {
			stdout := "panic: command failed\n"
			if check == "test" || check == "test-db" {
				stdout = containerJSON("p", "start") + testOutput("p", "", stdout) + containerJSON("p", "fail")
			}
			facts := parseSummaryFacts(check, []byte(stdout), nil, false)
			wantLines := 0
			if check == "build" || check == "vet" {
				wantLines = 1
			}
			if facts.stdout.total != wantLines || facts.panicLine != "panic: command failed" {
				t.Fatalf("unexpected retained output for %s: %+v", check, facts)
			}
		})
	}
}
