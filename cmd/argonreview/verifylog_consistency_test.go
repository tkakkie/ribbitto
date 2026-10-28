package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestVerifyLogRecordConsistency(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			r := verifyLogFixture("first\n")
			r.complete = complete
			r.entries[0].output.tail = []byte("last\n")
			r.entries[0].output.total += len(r.entries[0].output.tail) + len("omitted")
			r.entries[0].output.notRetained = true
			r.namesNotRetained = 2
			r.failedLeaves, r.confirmedFailures = r.namesNotRetained+1, r.namesNotRetained+1
			r.leaves.failed, r.packages.failed = r.failedLeaves, 1
			c := newVerifyLogCapture("FAILED", r)
			facts := recordSummaryFacts(r)
			host := recordHostReport("FAILED", "fixture", r)
			summary := buildContainerSummary(summaryInput{check: "test", pattern: ".", scope: "fixture", verdict: "FAILED", facts: facts})
			index := strings.Join(c.indexLines(), "\n")
			served := strings.Join(verifyLogOutputLines(&r.entries[0].output, false), "\n")
			for _, notice := range []string{fmt.Sprintf("truncated: %d bytes not retained", len("omitted"))} {
				for _, text := range []string{served, host, summary} {
					if strings.Count(text, notice) != 1 {
						t.Fatalf("notice %q missing: %q", notice, text)
					}
				}
			}
			for _, text := range []string{served, host, summary} {
				if len(regexp.MustCompile(`(?m)^(?:  \| )?not retained$`).FindAllString(text, -1)) != 1 {
					t.Fatalf("standalone not-retained notice missing or duplicated: %q", text)
				}
			}
			if complete {
				counts := fmt.Sprintf("Leaf tests: 0 passed, %d failed, 0 skipped", r.failedLeaves)
				for _, text := range []string{index, host, summary} {
					if !strings.Contains(text, counts) || strings.Contains(text, "observed") {
						t.Fatalf("complete counts/labels disagree: %q", text)
					}
				}
				notice := fmt.Sprintf("names not retained for %d failures", r.namesNotRetained)
				if !strings.Contains(index, notice) || !strings.Contains(host, notice) {
					t.Fatal("complete storage omission disagrees")
				}
				// The merged complete summary folds storage loss into its existing
				// display-omission footer; it does not print the host's notice.
				if got := consistencyNumber(t, summary, `; ([0-9]+) names not shown;`); got != r.namesNotRetained {
					t.Fatal("summary omission count disagrees")
				}
			} else {
				for _, text := range []string{index, host, summary} {
					if !strings.Contains(text, "Failures (observed, total unknown):") || strings.Contains(text, "Leaf tests:") {
						t.Fatalf("incomplete record claimed totals: %q", text)
					}
					consistencyNotRetained(t, text, r.namesNotRetained)
				}
			}
			if c.entries[0].display != summaryText(facts.failures[0].name) || len(c.entries)+r.namesNotRetained != r.confirmedFailures {
				t.Fatal("index and summary project different retained failures")
			}
		})
	}
}
