package main

import (
	"fmt"
	"strings"
)

// browserLeafLimit bounds how much one named test may run inside the 10-minute
// container limit, so a group name cannot stand in for the whole suite.
const browserLeafLimit = 64

// stdout is not authenticated: test code can print these events itself. This
// is Argon's own check, never a security control or a substitute for CI.
func browserVerdict(run string, r *containerRecord, result aggregatorResult) string {
	if validateBrowserRun(run) != nil {
		return "FAILED: invalid browser run"
	}
	if !strings.HasPrefix(result.verdict, "verified\n") {
		return result.verdict
	}
	b := r.browser
	if b == nil || b.run != run || b.namedState != "pass" {
		return "NOT VERIFIED: the named test did not run: " + run
	}
	if b.outside {
		return "NOT VERIFIED: a test outside the named test ran"
	}
	if b.leaves > browserLeafLimit {
		return fmt.Sprintf("NOT VERIFIED: too many tests ran (%d, limit %d); name fewer tests", b.leaves, browserLeafLimit)
	}
	return fmt.Sprintf("verified\nnamed test: %s passed (%d leaf tests)", run, b.leaves)
}
