package main

import (
	"context"
)

// Debian's wrapper executes the real binary here. Mask it for every other
// check so go-rod cannot bypass browser admission. The integrated preflight
// verifies dpkg -L chromium and that a non-browser check cannot launch Chromium
// on the real image; the mount is harmless on the current image without it.
const containerChromiumDirectory = "/usr/lib/chromium"

const (
	containerBrowserPIDs    = "2048"
	containerRequireBrowser = "RIBBITTO_REQUIRE_BROWSER=1"
	containerBrowserScript  = containerTestDBPrelude + `go test -count=1 -json ` + containerTestDBParallel + ` -run "$2" "$1" || status=$?
` + containerTestDBFinish
)

// Browser calls share admission, budget, execution and cleanup across callers.
func (v containerVerifier) verifyBrowser(ctx context.Context, pattern, run string) (string, error) {
	summary, _, _, err := v.verifyRecorded(ctx, verifyBrowserCheck, pattern, run)
	return summary, err
}

func containerBrowserArguments(copyDir, name, pattern, run string, reservation budgetReservation) ([]string, error) {
	runPattern, err := browserRunPattern(run)
	if err != nil {
		return nil, err
	}
	args := containerBaseArguments(copyDir, name, "test-browser", reservation, true)
	return append(args, containerBrowserScript, "sh", pattern, runPattern), nil
}

func (result *containerExecution) refuse(check, pattern, line string) string {
	result.refused = true
	result.code = 1
	in := summaryInput{check: check, pattern: pattern, scope: containerCheckScope(check), verdict: line, runnerFailed: true, browserRun: result.browserRun}
	result.summary = buildContainerSummary(in)
	return line + "\n" + in.scope + "\n"
}
