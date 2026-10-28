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
	b := r.browser
	// A test-free second package gets the generic no-tests refusal first;
	// retain the more specific browser scope refusal without hiding failures.
	outsideWithoutTests := b != nil && b.run == run && b.namedState == "pass" && b.outsidePackage &&
		result.verdict == "NOT VERIFIED: a targeted package has no tests, or no leaf test passed"
	if !strings.HasPrefix(result.verdict, "verified\n") && !outsideWithoutTests {
		return result.verdict
	}
	if b == nil || b.run != run || b.namedState != "pass" {
		return "NOT VERIFIED: the named test did not run: " + run
	}
	if b.outside || b.outsidePackage {
		return "NOT VERIFIED: a test outside the named test ran"
	}
	if b.leaves > browserLeafLimit {
		return fmt.Sprintf("NOT VERIFIED: too many tests ran (%d, limit %d); name fewer tests", b.leaves, browserLeafLimit)
	}
	return fmt.Sprintf("verified\nnamed test: %s passed (%d leaf tests)", run, b.leaves)
}
