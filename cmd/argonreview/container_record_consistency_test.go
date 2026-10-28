package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// consistencyNumber accepts exactly one numeric notice, so duplicate notices
// cannot mask double accounting between record retention and display omission.
func consistencyNumber(t *testing.T, text, pattern string) int {
	t.Helper()
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		t.Fatalf("expected one notice matching %q, got %d in %q", pattern, len(matches), text)
	}
	n, err := strconv.Atoi(matches[0][1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// consistencyNotRetained requires the same observed omission vocabulary in both
// renderings, independently of the production format constant.
func consistencyNotRetained(t *testing.T, text string, want int) {
	t.Helper()
	const pattern = `(?m)^Observed failures not named \(not retained\): ([0-9]+)$`
	if want == 0 {
		if strings.Contains(text, "Observed failures not named") {
			t.Fatalf("unexpected storage omission: %q", text)
		}
		return
	}
	if got := consistencyNumber(t, text, pattern); got != want {
		t.Fatalf("not-retained count: got %d want %d", got, want)
	}
}

// TestRecordConsistencyHostReportAndSummary checks one sealed record through
// both readers, including storage loss combined with display loss.
func TestRecordConsistencyHostReportAndSummary(t *testing.T) {
	type row struct {
		name, stop                     string
		skips, width, failures, budget int
	}
	var rows []row
	for _, wide := range []bool{false, true} {
		limit, width := aggregatorIndexLimit, 0
		if wide {
			width = summaryIdentityLimit/2 - len("p")
			limit = aggregatorNameLimit / (len("p") + width)
		}
		for _, delta := range []int{-1, 0, 1} {
			for _, stop := range []string{"cap", "cap deadline", "deadline", "kill", "tail", "malformed", "missing"} {
				rows = append(rows, row{fmt.Sprintf("wide=%v/delta=%d/%s", wide, delta, stop), stop, limit + delta, width, 2, summaryErrorBudget})
			}
		}
	}
	rows = append(rows,
		row{"retained display and storage omissions", "cap", aggregatorIndexLimit - summaryNameLimit - 2, 0, summaryNameLimit + 5, summaryErrorBudget},
		row{"budget display and storage omissions", "tail", aggregatorIndexLimit - 3, 0, 5, 726},
		row{"zero display with storage omissions", "tail", aggregatorIndexLimit - 3, 0, 5, 681},
		row{"only reserved footer", "cap", aggregatorIndexLimit, 0, 2, summaryFooterReserve + len("FAILED: output limit exceeded after a confirmed failure (output cut off)\n")},
		row{"complete", "complete", 2, 0, 2, summaryErrorBudget},
		row{"complete storage limit", "complete", aggregatorIndexLimit, 0, 1, summaryErrorBudget},
	)
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			a := visibilityFixture(tc.skips, tc.width, tc.failures)
			result := visibilityStop(a, tc.stop)
			r := a.record
			in := visibilityInput(a, result)
			summary := buildContainerSummaryWithLimits(in, summaryVerifiedLimit, tc.budget)
			host := recordHostReport(result.verdict, in.scope, r)
			if len(summary) > tc.budget {
				t.Fatalf("summary exceeded budget: %d > %d", len(summary), tc.budget)
			}
			retained := 0
			for _, e := range r.entries {
				if e.state == "fail" {
					retained++
				}
			}
			if len(in.facts.observed) != retained || strings.Count(host, "(test, fail)\n")+strings.Count(host, "(package, fail)\n") != retained {
				t.Fatalf("retained record and host/projection counts disagree: retained=%d host=%q", retained, host)
			}
			if retained+r.namesNotRetained != r.confirmedFailures {
				t.Fatalf("record lost failures: retained=%d not retained=%d observed=%d", retained, r.namesNotRetained, r.confirmedFailures)
			}
			if r.complete && r.namesNotRetained > 0 {
				want := fmt.Sprintf("names not retained for %d failures\n", r.namesNotRetained)
				if !strings.Contains(host, want) || strings.Contains(host, "Observed failures not named") {
					t.Fatalf("complete host storage notice changed: %q", host)
				}
			} else {
				consistencyNotRetained(t, host, r.namesNotRetained)
			}
			if strings.Contains(host, "\nand ") || strings.Contains(host, " more not shown") {
				t.Fatalf("host unexpectedly has a display continuation: %q", host)
			}
			if tc.stop == "deadline" {
				if !strings.HasSuffix(summary, "Details unavailable: the check did not complete.\n"+deadlineOmitted) || strings.Contains(summary, "not retained") {
					t.Fatalf("D5 exposed observations: %q", summary)
				}
				return
			}
			if tc.stop == "complete" {
				counts := fmt.Sprintf("Leaf tests: 0 passed, %d failed, %d skipped\n", tc.failures, tc.skips)
				if !r.complete || !strings.Contains(host, counts) || !strings.Contains(summary, counts) || strings.Contains(summary, "observed") || !strings.Contains(summary, fmt.Sprintf("Omitted: excerpts shown for 0 of %d failing tests;", tc.failures)) {
					t.Fatalf("complete counts changed: host=%q summary=%q", host, summary)
				}
				consistencyNotRetained(t, summary, 0)
				return
			}
			consistencyNotRetained(t, summary, r.namesNotRetained)
			if r.complete || strings.Contains(summary, " of ") || strings.Contains(summary, "Leaf tests:") || strings.Contains(host, "Leaf tests:") || !strings.Contains(summary, "observed failing tests; total unknown;") {
				t.Fatalf("incomplete stream supplied final totals: host=%q summary=%q", host, summary)
			}
			shown := strings.Count(summary, " (test)\n")
			if shown > 0 && r.namesNotRetained > 0 {
				if strings.Index(summary, "Observed failures not named") < strings.LastIndex(summary, " (test)\n") || strings.Index(host, "Observed failures not named") < strings.LastIndex(host, "(test, fail)\n") {
					t.Fatalf("storage omission must follow retained failures: host=%q summary=%q", host, summary)
				}
			}
			omitted := consistencyNumber(t, summary, `; ([0-9]+) names not shown;`)
			if shown+omitted != retained || shown+omitted+r.namesNotRetained != r.confirmedFailures {
				t.Fatalf("failure accounting overlaps or loses evidence: shown=%d display omitted=%d not retained=%d observed=%d", shown, omitted, r.namesNotRetained, r.confirmedFailures)
			}
			if tc.name == "retained display and storage omissions" || tc.name == "budget display and storage omissions" {
				if shown == 0 || omitted == 0 || r.namesNotRetained == 0 || !strings.Contains(summary, fmt.Sprintf("\nand %d more\n", omitted)) || !strings.Contains(summary, "Failures (observed, total unknown):\n") {
					t.Fatalf("fixture must combine headed continuation and storage omission: %q", summary)
				}
			}
			if tc.name == "zero display with storage omissions" {
				if shown != 0 || !strings.Contains(summary, "Failures (observed, total unknown): 3 more not shown\n") {
					t.Fatalf("zero-display continuation lost its list: %q", summary)
				}
			}
		})
	}

	for _, capName := range []string{"line length", "test count", "package count", "retained memory", "identity length", "event fields"} {
		for _, kind := range []string{"test", "package", "build-fail"} {
			t.Run(capName+"/"+kind, func(t *testing.T) {
				a := visibilityFixture(aggregatorIndexLimit, 0, 0)
				switch kind {
				case "test":
					aggregatorWrite(a, containerJSON("p", "run/TestBad", "fail/TestBad"))
				case "package":
					aggregatorWrite(a, containerJSON("p", "fail"))
				case "build-fail":
					aggregatorWrite(a, `{"Action":"build-fail","ImportPath":"p"}`+"\n")
				}
				a.stop(stopCap, capName)
				result := aggregatorFinish(a)
				in := visibilityInput(a, result)
				summary := buildContainerSummary(in)
				host := recordHostReport(result.verdict, in.scope, a.record)
				if aggregatorFirst(result) != "FAILED: output limit exceeded after a confirmed failure (output cut off)" || !result.isError || a.record.namesNotRetained != 1 || len(in.facts.observed) != 0 {
					t.Fatalf("A2/A4 verdict or first-come retention changed: %q", result.verdict)
				}
				for _, text := range []string{host, summary} {
					consistencyNotRetained(t, text, 1)
					if !strings.Contains(text, "Limit reached: "+capName+"\n") || !strings.Contains(text, "Failures (observed, total unknown):\n") || strings.Contains(text, "Leaf tests:") {
						t.Fatalf("A2/A4 observed information missing or final: %q", text)
					}
				}
			})
		}
	}
}
