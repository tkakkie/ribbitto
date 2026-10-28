package main

import (
	"context"
	"strings"
	"testing"
)

func TestContainerBrowserVerifiedSummaryCounts(t *testing.T) {
	v := browserRunFixture(t)
	stream := containerJSON(browserTestPackage, "start", "run/TestBrowser", "run/"+browserTestRun,
		"run/"+browserTestRun+"/a", "pass/"+browserTestRun+"/a",
		"run/"+browserTestRun+"/b", "pass/"+browserTestRun+"/b",
		"pass/"+browserTestRun, "pass/TestBrowser", "pass")
	v.runner = summaryRunner(stream, "untrusted stderr", 0)
	summary, err := v.verifyBrowser(context.Background(), "./internal/web", browserTestRun)
	if err != nil || !strings.HasPrefix(summary, "verified\n") || len(summary) > summaryVerifiedLimit {
		t.Fatalf("browser summary: %q, error %v", summary, err)
	}
	for _, line := range []string{"Check: test-browser ./internal/web " + browserTestRun,
		"named test: " + browserTestRun + " passed (2 leaf tests)",
		"Packages: 1 passed, 0 failed, 0 skipped", "Leaf tests: 2 passed, 0 failed, 0 skipped"} {
		if strings.Count(summary, line+"\n") != 1 {
			t.Fatalf("missing or repeated browser summary line %q: %s", line, summary)
		}
	}
	if strings.Contains(summary, "untrusted stderr") {
		t.Fatal("verified browser summary leaked stderr")
	}
}
