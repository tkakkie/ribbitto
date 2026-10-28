package main

import (
	"fmt"
	"sort"
	"strings"
)

// recordSummaryFacts is a bounded presentation projection, never a second
// protocol interpretation. Only the record's completeness can authorise totals.
func recordSummaryFacts(r *containerRecord) summaryFacts {
	f := summaryFacts{packages: r.packages, leaves: r.leaves, allowed: r.allowed, skipped: r.skipped,
		events: r.events, afterEvent: r.afterEvent, incomplete: !r.complete && !r.command, cut: r.capName != "", capName: r.capName,
		streamCut: r.stop == stopKill || r.stop == stopTruncated,
		panicLine: r.panicLine, timeoutLine: r.timeoutLine, failedTotal: r.failedLeaves, skippedTotal: r.disallowedSkips, noTests: r.noTests, failureSignal: r.failureSignal}
	if !r.complete && !r.command {
		f.err = fmt.Errorf("incomplete container record: %s", r.stop)
	}
	f.stderr = retainedSummaryLines(r.stderr)
	f.stdout = retainedSummaryLines(r.stdout)
	f.compiler = retainedSummaryLines(r.compiler)
	for _, part := range [][]byte{r.packageOutput.head, r.packageOutput.tail} {
		for line := range strings.SplitSeq(string(part), "\n") {
			if strings.Contains(line, "FAIL") || strings.Contains(line, "[build failed]") {
				f.packageFailures.add(line, summaryExcerptLimit)
			}
		}
	}
	for _, e := range r.entries {
		if e.state != "fail" {
			continue
		}
		f.observed = append(f.observed, e.pkg+"/"+e.name+" ("+e.kind+")")
		if e.kind == "test" {
			f.failures = append(f.failures, summaryFailure{e.pkg + "/" + e.name, retainedSummaryLines(e.output)})
		} else if e.name == "" {
			name := e.pkg + ": package failed"
			if e.buildFailed {
				name = e.pkg + " [build failed]"
			}
			f.packageFailures.add(name, summaryExcerptLimit)
		}
	}
	sort.Slice(f.failures, func(i, j int) bool { return f.failures[i].name < f.failures[j].name })
	return f
}

// retainedSummaryLines joins fragments only within a retained segment. A gap
// between head and tail is explicit, so two unrelated fragments cannot merge.
func retainedSummaryLines(o retainedOutput) summaryLines {
	var lines summaryLines
	lines.fragment(string(o.head), summaryExcerptLimit)
	if dropped := o.total - len(o.head) - len(o.tail); dropped > 0 {
		lines.end(summaryExcerptLimit)
		lines.add(fmt.Sprintf("truncated: %d bytes not retained", dropped), summaryExcerptLimit)
	}
	lines.fragment(string(o.tail), summaryExcerptLimit)
	lines.end(summaryExcerptLimit)
	if o.notRetained {
		lines.add("not retained", summaryExcerptLimit)
	}
	return lines
}
