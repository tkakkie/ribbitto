package main

import (
	"fmt"
	"sort"
	"strings"
)

// recordSummaryFacts is a bounded presentation projection, never a second
// protocol interpretation. Only the record's completeness can authorise totals.
func recordSummaryFacts(r *containerRecord) summaryFacts {
	return recordSummaryFactsWithOrdinals(r, false)
}

func recordSummaryFactsWithOrdinals(r *containerRecord, ordinals bool) summaryFacts {
	f := summaryFacts{packages: r.packages, leaves: r.leaves, allowed: r.allowed, skipped: r.skipped,
		namesNotRetained: r.namesNotRetained, events: r.events, afterEvent: r.afterEvent, incomplete: !r.complete && !r.command, cut: r.capName != "", capName: r.capName,
		streamCut: r.stop == stopKill || r.stop == stopTruncated,
		panicLine: r.panicLine, timeoutLine: r.timeoutLine, failedTotal: r.failedLeaves, skippedTotal: r.disallowedSkips, noTests: r.noTests, failureSignal: r.failureSignal}
	if !r.complete && !r.command {
		f.err = fmt.Errorf("incomplete container record: %s", r.stop)
	}
	if !r.stderrNotServable {
		f.stderr = retainedSummaryLines(r.stderr)
	}
	f.stdout = retainedSummaryLines(r.stdout)
	f.compiler = retainedSummaryLines(r.compiler)
	var notices []string
	var noticeMasked []bool
	noticeTotal := 0
	// Detection counts describe kinds, not occurrences. Any lost source can
	// hide a second occurrence whose continuation survives display selection.
	omitted := r.packageOutput.total > len(r.packageOutput.head)+len(r.packageOutput.tail) || r.packageOutput.notRetained ||
		r.compiler.total > len(r.compiler.head)+len(r.compiler.tail) || r.compiler.notRetained || f.compiler.total > len(f.compiler.lines)
	for _, shortened := range f.compiler.shortened {
		omitted = omitted || shortened
	}
	for _, part := range [][]byte{r.packageOutput.head, r.packageOutput.tail} {
		for line := range strings.SplitSeq(string(part), "\n") {
			if strings.Contains(line, "FAIL") || strings.Contains(line, "[build failed]") {
				noticeTotal++
				omitted = omitted || len(line) > summaryLineLimit
				if len(notices) < summaryExcerptLimit {
					notices = append(notices, line)
				}
			} else if line != "" {
				omitted = true
			}
		}
	}
	omitted = omitted || noticeTotal > len(notices)
	// Match capture's segment boundaries, but locate masks only in the bounded
	// display. A hidden match cannot justify printing an unlocated fragment.
	if detections := verifyLogScanSegments(string(r.packageOutput.head), string(r.packageOutput.tail), string(r.compiler.head), string(r.compiler.tail)); detections > 0 {
		lines := append(notices, f.compiler.lines...)
		masked := summaryMaskLines(lines)
		// A located shape must not hide another kind lost during selection.
		if omitted || verifyLogScan(strings.Join(lines, "\n")) < detections {
			for i := range masked {
				masked[i] = summaryWithheld
			}
		}
		for i, line := range notices {
			noticeMasked = append(noticeMasked, masked[i] != line)
		}
		notices = masked[:len(notices)]
		compiler := masked[len(notices):]
		for i, line := range f.compiler.lines {
			f.compiler.shortened[i] = f.compiler.shortened[i] || compiler[i] != line
		}
		f.compiler.lines = compiler
	}
	for i, line := range notices {
		f.packageFailures.add(line, summaryExcerptLimit)
		if i < len(noticeMasked) {
			f.packageFailures.shortened[i] = f.packageFailures.shortened[i] || noticeMasked[i]
		}
	}
	f.packageFailures.total = noticeTotal
	// Derive ordinals from exactly the capture's index, using entry identity.
	// Carry numbers with each entry so equal raw names or labels cannot collide.
	var numbers map[*aggregatorEntry]int
	if ordinals {
		numbers = make(map[*aggregatorEntry]int)
		index, _ := verifyLogIndex(r)
		for i, item := range index {
			numbers[item.entry] = i + 1
		}
		f.ordinals = make(map[string][]int)
	}
	var skipped []*aggregatorEntry
	for _, e := range r.entries {
		if ordinals && e.skipName != "" {
			skipped = append(skipped, e)
		}
		if e.state != "fail" {
			continue
		}
		observed := e.pkg + "/" + e.name + " (" + e.kind + ")"
		f.observed = append(f.observed, observed)
		if ordinals {
			f.ordinals["Failures (observed, total unknown):"] = append(f.ordinals["Failures (observed, total unknown):"], numbers[e])
		}
		if e.kind == "test" {
			name := e.pkg + "/" + e.name
			f.failures = append(f.failures, summaryFailure{name: name, output: retainedSummaryLines(e.output), ordinal: numbers[e]})
		} else if e.name == "" {
			name := e.pkg + ": package failed"
			if e.buildFailed {
				name = e.pkg + " [build failed]"
			}
			before := len(f.packageFailures.lines)
			masked := summaryMaskName(name)
			display := masked
			if ordinals {
				display = summaryText(masked)
			}
			if ordinals && display == "" {
				f.packageFailures.addEmpty(summaryExcerptLimit)
			} else {
				f.packageFailures.add(display, summaryExcerptLimit)
			}
			for i := before; i < len(f.packageFailures.lines); i++ {
				f.packageFailures.shortened[i] = f.packageFailures.shortened[i] || masked != name || display != masked
			}
			if ordinals && len(f.packageFailures.lines) > before {
				for len(f.packageFailures.ordinals) < before {
					f.packageFailures.ordinals = append(f.packageFailures.ordinals, 0)
				}
				f.packageFailures.ordinals = append(f.packageFailures.ordinals, numbers[e])
			}
		}
	}
	sort.Slice(f.failures, func(i, j int) bool { return f.failures[i].name < f.failures[j].name })
	if ordinals {
		for _, failure := range f.failures {
			f.ordinals["Failing leaf tests:"] = append(f.ordinals["Failing leaf tests:"], failure.ordinal)
		}
		// Match r.skipped's raw order, carrying index-derived numbers alongside
		// identities. Even identical raw displays retain separate ordinals.
		sort.SliceStable(skipped, func(i, j int) bool { return skipped[i].skipName < skipped[j].skipName })
		for _, e := range skipped {
			f.ordinals["Skipped names:"] = append(f.ordinals["Skipped names:"], numbers[e])
		}
	}
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
