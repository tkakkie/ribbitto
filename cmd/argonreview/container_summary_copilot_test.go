package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSummarySkippedOmissionCounts(t *testing.T) {
	for _, count := range []int{3, 100} {
		for _, long := range []bool{false, true} {
			var stream strings.Builder
			stream.WriteString(containerJSON("p", "start"))
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("Test%03d", i)
				if long {
					name += strings.Repeat("x", 190)
				}
				stream.WriteString(containerJSON("p", "run/"+name, "skip/"+name))
			}
			stream.WriteString(containerJSON("p", "pass"))
			verdict := testVerdict(0, nil, []byte(stream.String()), false)
			for _, mode := range []string{"complete", "malformed", "cut", "small budget"} {
				t.Run(fmt.Sprintf("%d/long=%v/%s", count, long, mode), func(t *testing.T) {
					in := summaryFixture("test", verdict, stream.String(), "")
					limit := summaryErrorBudget
					switch mode {
					case "malformed":
						in.facts = recordFactsFixture("test", []byte(stream.String()+"garbage\n"), nil, false)
					case "cut":
						in.facts.cut = true
					case "small budget":
						limit = 1000
					}
					got := buildContainerSummaryWithLimits(in, summaryVerifiedLimit, limit)
					assertSummary(t, in, got)
					shown := strings.Count(got, "  | p/Test")
					omitted := count - shown
					if shown > 30 || !strings.Contains(got, fmt.Sprintf("; %d names not shown;", omitted)) {
						t.Fatalf("shown=%d, total=%d: %s", shown, count, got)
					}
					if omitted > 0 {
						if !strings.Contains(got, fmt.Sprintf("\nand %d more\n", omitted)) {
							t.Fatal(got)
						}
					} else if strings.Contains(got, "\nand ") {
						t.Fatal(got)
					}
					if count == 100 && long && shown >= 30 {
						t.Fatalf("fixture did not exhaust the name budget: %s", got)
					}
				})
			}
		}
	}
}

func TestSummaryCompilerOnlyCounts(t *testing.T) {
	output := `{"Action":"build-output","ImportPath":"p","Output":"undefined: x\n"}` + "\n"
	fail := `{"Action":"build-fail","ImportPath":"p"}` + "\n"
	for _, check := range []string{"test", "test-db"} {
		for _, tc := range []struct {
			name, stream string
			counts       bool
		}{
			{"output only", output, false},
			{"fail only", fail, false},
			{"compiler only", output + fail, false},
			{"completed package", output + fail + containerEvents("q", "TestPass", "pass"), true},
			{"no tests package", containerJSON("q", "start", "pass"), true},
		} {
			t.Run(check+"/"+tc.name, func(t *testing.T) {
				in := summaryFixture(check, "FAILED", tc.stream, "")
				if in.facts.err != nil {
					t.Fatal(in.facts.err)
				}
				got := buildContainerSummary(in)
				assertSummary(t, in, got)
				for _, prefix := range []string{"Packages:", "Leaf tests:"} {
					if strings.Contains(got, prefix) != tc.counts {
						t.Fatalf("counts=%v: %s", tc.counts, got)
					}
				}
				if strings.Contains(tc.stream, "build-output") && !strings.Contains(got, "Compiler output:\n  | undefined: x\n") {
					t.Fatal(got)
				}
			})
		}
	}
}

func TestSummaryNonverifiedAllowedSkips(t *testing.T) {
	for _, entry := range measurementSkips() {
		base := measurementStream(entry, entry.message, true)
		for _, tc := range []struct {
			name, extra, first string
			code               int
		}{
			{"nonzero exit", "", "FAILED", 1},
			{"failed test", containerEvents("q", "TestBad", "fail"), "FAILED", 0},
			{"unallowed skip", containerEvents("q", "TestSkip", "skip"), "NOT VERIFIED: 1 skipped", 0},
			{"failed and unallowed skip", containerJSON("q", "start", "run/TestSkip", "skip/TestSkip", "run/TestBad", "fail/TestBad", "fail"), "FAILED", 0},
			{"empty package", containerJSON("q", "start", "pass"), "NOT VERIFIED: a targeted package has no tests, or no leaf test passed", 0},
		} {
			t.Run(entry.test+"/"+tc.name, func(t *testing.T) {
				stream := base + tc.extra
				verdict := testVerdictWithAllowlist(tc.code, nil, []byte(stream), false, measurementSkips())
				first, _, _ := strings.Cut(verdict, "\n")
				if first != tc.first {
					t.Fatalf("fixture verdict: %s", verdict)
				}
				for _, mode := range []string{"complete", "cut", "malformed", "ordinary test"} {
					t.Run(mode, func(t *testing.T) {
						in := summaryFixture("test-db", verdict, stream, "")
						switch mode {
						case "cut":
							in.facts.cut = true
						case "malformed":
							in.facts = recordFactsFixture("test-db", []byte(stream+"garbage\n"), nil, false)
						case "ordinary test":
							in.check = "test"
							in.verdict = testVerdict(tc.code, nil, []byte(stream), false)
						}
						// A forged fact must never become a trusted column-zero identity.
						in.facts.allowed = append(in.facts.allowed, "forged/TestInjected")
						got := buildContainerSummary(in)
						assertSummary(t, in, got)
						line := "allowed measurement skips: " + entry.pkg + "/" + entry.test + "\n"
						if strings.Contains(got, line) != (mode == "complete") || strings.Contains(got, "forged/TestInjected") {
							t.Fatal(got)
						}
						if mode == "complete" && !strings.Contains(got, fmt.Sprintf("Leaf tests: 1 passed, %d failed, %d skipped\n", in.facts.leaves.failed, in.facts.leaves.skipped)) {
							t.Fatalf("allowed skip counted as a pass: %s", got)
						}
					})
				}
			})
		}
	}
}

func TestSummaryRunnerFailureMinimal(t *testing.T) {
	for _, tc := range []struct {
		first, report       string
		err                 error
		timedOut, cancelled bool
	}{
		{first: "NOT VERIFIED: time limit exceeded", report: "verified\n", err: context.DeadlineExceeded},
		{first: "NOT VERIFIED: time limit exceeded", report: "partial report", timedOut: true},
		{first: "NOT VERIFIED: cancelled", report: "verified\n", err: context.Canceled},
		{first: "NOT VERIFIED: cancelled", report: "partial report", cancelled: true},
		{first: "FAILED: the check returned an error", report: "verified\n", err: errors.New("runner failed")},
		{first: "NOT VERIFIED: the check could not run", report: "", err: errors.New("runner unavailable")},
		{first: "FAILED: source cleanup failed", report: "FAILED: source cleanup failed\nresult unconfirmed"},
	} {
		t.Run(tc.first+"/"+tc.report, func(t *testing.T) {
			verdict, bad := verifyResult(tc.report, tc.err, tc.timedOut, tc.cancelled)
			first, _, _ := strings.Cut(verdict, "\n")
			if !bad || first != tc.first {
				t.Fatalf("fixture verdict: %s", verdict)
			}
			for _, check := range verifyCheckNames() {
				t.Run(check, func(t *testing.T) {
					in := summaryFixture(check, verdict, "", "")
					want := first + "\nCheck: " + check + " ./...\nScope: " + in.scope + "\n" +
						"Details unavailable: the check did not complete.\n" +
						"Omitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 0 lines shortened.\n"
					entry := measurementSkips()[0]
					stream := measurementStream(entry, entry.message, true) +
						containerJSON("q", "start", "run/TestBad") + testOutput("q", "TestBad", "panic: partial stream\n") + containerJSON("q", "fail/TestBad", "fail")
					for _, output := range []string{"", stream, stream + "garbage\n", containerJSON("q", "start", "run/TestRunning")} {
						in.facts = recordFactsFixture(check, []byte(output), []byte("partial stderr\n"), false)
						in.stat = generatedStat{paths: 1, entries: []generatedStatEntry{{path: "partial path"}}}
						in.hasStat = true
						in.gofmtNames = []string{"partial gofmt"}
						in.diff = "partial diff\n"
						got := buildContainerSummary(in)
						assertSummary(t, in, got)
						if got != want {
							t.Fatalf("runner failure depends on stream:\n%s", got)
						}
					}
				})
			}
		})
	}
}
