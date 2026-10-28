package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// aggregatorResult keeps the oracle-compatible verdict (including its skip
// names) separate from the report body. Only a complete record supplies totals.
type aggregatorResult struct {
	verdict, body string
	isError       bool
}

// aggregatorRunState preserves interruption at runner return separately from a
// late deadline: a runner error with a healthy context must keep its own line.
type aggregatorRunState struct {
	deadline, parentCancelled, completed, interrupted bool
}

// finish takes the runner's classification at return, not a later ctx.Err().
// The first call seals the record and result; every later call returns that
// identical result regardless of its code, error or state. The caller owns the
// source-cleanup row, which precedes this decision table.
func (a *containerAggregator) finish(code int, runErr error, state aggregatorRunState) aggregatorResult {
	deadline, parentCancelled, completed := state.deadline, state.parentCancelled, state.completed
	a.record.mu.Lock()
	defer a.record.mu.Unlock()
	r := a.record
	if r.sealed {
		return a.decide(a.sealedCode, a.sealedErr, a.sealedState)
	}
	r.complete = completed && !state.interrupted && runErr == nil && r.stop == stopNone && r.stdoutBytes > 0 && len(a.lines[0]) == 0
	missing, missingPackage := stopNone, ""
	for pkg, p := range a.packages {
		packageMissing := stopNone
		if p.entry.state == "" {
			packageMissing = stopMissingPackage
		}
		if p.runs == 0 {
			a.noTests = true
		}
		summaryCount(&r.packages, p.entry.state)
		passing := 0
		for _, t := range p.tests {
			// No verdict depends on which map entry frees its slot first.
			r.accounted -= cap(t.slot)
			t.slot = nil
			if t.started && !t.parent && t.state == "pass" {
				passing++
			}
		}
		if p.entry.state == "skip" {
			r.disallowedSkips++
		}
		for _, t := range p.tests {
			if !t.started {
				continue
			}
			if t.state == "" && packageMissing == stopNone {
				packageMissing = stopMissingTest
			}
			if !t.parent {
				summaryCount(&r.leaves, t.state)
				if t.state == "fail" {
					r.failedLeaves++
				}
			}
			if t.state == "skip" {
				allowed := t.message && allowedMeasurementSkip(a.allowlist, p.entry.pkg, t.name, t.match, t.parent, passing)
				if allowed {
					t.allowed = true
					r.allowed = append(r.allowed, a.allowedName(p.entry.pkg, t.name))
				} else {
					r.disallowedSkips++
				}
			}
		}
		// Match the oracle's sorted package walk without allocating a key slice
		// after a stop. Within one package its missing result precedes its tests.
		if packageMissing != stopNone && (missing == stopNone || pkg < missingPackage) {
			missing, missingPackage = packageMissing, pkg
		}
	}
	if missing != stopNone {
		r.complete = false
	}
	if r.stop == stopNone {
		switch {
		case deadline && state.interrupted:
			a.stop(stopDeadline, "")
		case parentCancelled && !completed:
			a.stop(stopParentCancellation, "")
		case code == 137:
			a.stop(stopKill, "")
		case r.stdoutBytes == 0 || len(a.lines[0]) > 0:
			a.stop(stopTruncated, "")
		case missing != stopNone:
			a.stop(missing, "")
		case runErr != nil:
			a.stop(stopRunnerError, "")
		case !completed || state.interrupted:
			a.stop(stopIncompleteRun, "")
		}
	}
	if r.stop != stopNone {
		r.complete = false
	}
	// The decision pass above allocates nothing. Diagnostics below may be
	// omitted, but cannot introduce a cap or change any decision evidence.
	if len(a.lines[1]) > 0 {
		a.finishNotice(string(a.lines[1]))
	}
	for _, p := range a.packages {
		if p.entry.state == "skip" {
			p.entry.skipName = a.name(&a.skipped, p.entry.pkg+" (package)")
		}
		for _, t := range p.tests {
			if t.started && t.state == "skip" && !t.allowed {
				t.skipName = a.name(&a.skipped, p.entry.pkg+"/"+t.name)
			}
		}
	}
	// The index uses its precharged storage even after a stop. Selecting here
	// avoids treating a parent whose child arrived later as a failing leaf.
	r.entries = r.indexStorage[:0]
	r.skipped = r.skipStorage[:0]
	for n := a.first; n != nil; {
		next := n.next
		n.next = nil
		if n.kind == "test" && n.parent {
			n = next
			continue
		}
		size := len(n.pkg) + len(n.name) + len(n.skipName)
		if len(r.entries) == aggregatorIndexLimit || size > aggregatorNameLimit-a.indexBytes {
			if n.state == "fail" {
				r.namesNotRetained++
			}
			n = next
			continue
		}
		a.indexBytes += size
		r.entries = append(r.entries, n)
		if n.skipName != "" {
			r.skipped = append(r.skipped, n.skipName)
		}
		n = next
	}
	sort.Strings(r.allowed)
	sort.Strings(r.skipped)
	sort.Strings(a.skipped)
	r.stderrNotServable = code >= 125 && code <= 127 || runErr != nil || deadline || parentCancelled || !completed || state.interrupted
	r.sealed = true
	// Preserve classification rather than rendered text: retaining a rendered
	// report would duplicate the diagnostic budget in the live aggregator.
	a.sealedCode, a.sealedErr, a.sealedState = code, runErr, state
	return a.decide(a.sealedCode, a.sealedErr, a.sealedState)
}

// finishCharge treats final diagnostic retention as optional: all decision
// evidence has already been counted, so a refusal must not introduce a cap.
func (a *containerAggregator) finishCharge(n int) bool {
	if a.record.stop != stopNone || n > aggregatorMemoryLimit-a.record.accounted {
		return false
	}
	return a.charge(n)
}

// finishNotice retains trailing stderr markers without changing the stop.
func (a *containerAggregator) finishNotice(output string) {
	for line := range strings.SplitSeq(output, "\n") {
		for _, marker := range []struct {
			text string
			dest *string
		}{{"panic:", &a.record.panicLine}, {"test timed out", &a.record.timeoutLine}} {
			if *marker.dest == "" && strings.Contains(line, marker.text) && a.finishCharge(textCharge(len(line))) {
				*marker.dest = strings.Clone(line)
			}
		}
	}
}

// name reserves cloned text and any replacement slice together. An omitted
// diagnostic neither changes the stop nor consumes a partial reservation.
func (a *containerAggregator) name(names *[]string, name string) string {
	size, charge := cap(*names), textCharge(len(name))
	if len(*names) == size {
		size = max(1, size*2)
		charge += size * aggregatorNameSlotCharge
	}
	if !a.finishCharge(charge) {
		return ""
	}
	if size != cap(*names) {
		next := make([]string, len(*names), size)
		copy(next, *names)
		a.record.accounted -= cap(*names) * aggregatorNameSlotCharge
		*names = next
	}
	cloned := strings.Clone(name)
	*names = append(*names, cloned)
	return cloned
}

// decide applies N1 in precedence order; no renderer may promote this verdict.
func (a *containerAggregator) decide(code int, runErr error, state aggregatorRunState) aggregatorResult {
	deadline, parentCancelled := state.deadline, state.parentCancelled
	r := a.record
	verdict := "FAILED"
	minimal := false
	switch {
	case r.capName != "":
		verdict = "NOT VERIFIED: output limit exceeded (output cut off)"
		if r.confirmedFailures > 0 && !deadline {
			verdict = "FAILED: output limit exceeded after a confirmed failure (output cut off)"
		}
	case deadline && state.interrupted:
		verdict, minimal = containerTimeLimitVerdict, true
	case r.stop.malformed():
		verdict = "FAILED: " + r.stop.String()
	case code != 0 || runErr != nil || parentCancelled:
		minimal = runErr != nil || parentCancelled || code >= 125 && code <= 127
	case r.stop == stopTruncated:
		verdict = "FAILED: empty or truncated test stream"
	case a.failureSignal:
	case r.stop != stopNone:
		verdict = "FAILED: " + r.stop.String()
	case r.disallowedSkips > 0:
		verdict = fmt.Sprintf("NOT VERIFIED: %d skipped\n%s", r.disallowedSkips, strings.Join(a.skipped, "\n"))
	case a.noTests || r.leaves.passed == 0:
		verdict = "NOT VERIFIED: a targeted package has no tests, or no leaf test passed"
	case r.complete:
		verdict = fmt.Sprintf("verified\n%d passing leaf tests", r.leaves.passed)
		if len(r.allowed) > 0 {
			verdict += "\nallowed measurement skips: " + strings.Join(r.allowed, ", ")
		}
		if deadline {
			verdict, minimal = containerTimeLimitVerdict, true
		}
	}
	body := a.body(minimal)
	return aggregatorResult{verdict: verdict, body: body, isError: !strings.HasPrefix(verdict, "verified\n")}
}

// body preserves the fixed timeout/runner-error body. Other incomplete records
// expose only observed failures, never a misleading confirmed failure total.
func (a *containerAggregator) body(minimal bool) string {
	r := a.record
	if minimal {
		return "Details unavailable: the check did not complete.\nOmitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 0 lines shortened.\n"
	}
	var b strings.Builder
	if r.capName != "" {
		fmt.Fprintf(&b, "Counts unavailable: output limit exceeded; the stream was cut.\nLimit reached: %s\n", r.capName)
	} else if !r.complete {
		b.WriteString("Counts unavailable: the stream was cut.\n")
	}
	if r.complete {
		fmt.Fprintf(&b, "Packages: %d passed, %d failed, %d skipped\nLeaf tests: %d passed, %d failed, %d skipped\n", r.packages.passed, r.packages.failed, r.packages.skipped, r.leaves.passed, r.leaves.failed, r.leaves.skipped)
	} else if r.confirmedFailures > 0 {
		b.WriteString("Failures (observed, total unknown):\n")
	}
	for _, e := range r.entries {
		if !r.complete && e.state != "fail" {
			continue
		}
		fmt.Fprintf(&b, "  | %s/%s (%s, %s)\n", aggregatorText(e.pkg), aggregatorText(e.name), e.kind, e.state)
		writeRetainedOutput(&b, e.output)
	}
	if omitted := r.disallowedSkips - len(a.skipped); omitted > 0 {
		fmt.Fprintf(&b, "%d skip names not retained\n", omitted)
	}
	if r.namesNotRetained > 0 {
		fmt.Fprintf(&b, "names not retained for %d failures\n", r.namesNotRetained)
	}
	writeRetainedOutput(&b, r.packageOutput)
	writeRetainedOutput(&b, r.compiler)
	if r.stdout.total > 0 {
		b.WriteString("Command output:\n")
		writeRetainedOutput(&b, r.stdout)
	}
	if r.panicLine != "" {
		fmt.Fprintf(&b, "Panic:\n  | %s\n", aggregatorText(r.panicLine))
	}
	if r.timeoutLine != "" {
		fmt.Fprintf(&b, "Timeout:\n  | %s\n", aggregatorText(r.timeoutLine))
	}
	if r.stderr.total > 0 {
		b.WriteString("Standard error:\n")
		writeRetainedOutput(&b, r.stderr)
	}
	return b.String()
}

// aggregatorText escapes controls without shortening retained diagnostics. The
// host report must not silently inherit the MCP summary's per-line soft limit.
func aggregatorText(text string) string {
	quoted := strconv.QuoteToASCII(text)
	return quoted[1 : len(quoted)-1]
}

// writeRetainedOutput accounts only storage omissions, not display shortening.
func writeRetainedOutput(b *strings.Builder, o retainedOutput) {
	for _, text := range [][]byte{o.head, o.tail} {
		for line := range strings.SplitSeq(string(text), "\n") {
			if line != "" {
				fmt.Fprintf(b, "  | %s\n", aggregatorText(line))
			}
		}
	}
	if dropped := o.total - len(o.head) - len(o.tail); dropped > 0 {
		fmt.Fprintf(b, "truncated: %d bytes not retained\n", dropped)
	}
	if o.notRetained {
		b.WriteString("not retained\n")
	}
}
