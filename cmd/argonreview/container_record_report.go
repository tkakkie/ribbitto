package main

import (
	"fmt"
	"strings"
)

// containerCheckKind is exhaustive: unknown checks must never silently select
// the command writer and bypass test2json interpretation.
func containerCheckKind(check string) (test2json, classified bool) {
	switch check {
	case "test", "test-db", "test-browser":
		return true, true
	case "build", "vet", "generated", "writeback":
		return false, true
	default:
		return false, false
	}
}

// recordVerdict bounds optional skip diagnostics to what survived in the
// shared record; the aggregator's legacy-compatible diagnostic tail is transient.
func recordVerdict(verdict string, r *containerRecord) string {
	first, _, _ := strings.Cut(verdict, "\n")
	if strings.HasPrefix(first, "NOT VERIFIED:") && strings.HasSuffix(first, " skipped") {
		return first + "\n" + strings.Join(r.skipped, "\n")
	}
	return verdict
}

// recordHostReport puts scope immediately after the first line. All identities
// and output in the body are quoted; verdict diagnostics cannot inject lines.
func recordHostReport(verdict, scope string, r *containerRecord) string {
	first, detail, _ := strings.Cut(verdict, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n", first, scope)
	if r.complete {
		fmt.Fprintf(&b, "Record: complete; %d events, %d bytes streamed\n", r.events, r.streamed)
	} else {
		fmt.Fprintf(&b, "Record: incomplete; %s after event %d; %d bytes streamed\n", r.stop, r.afterEvent, r.streamed)
	}
	if r.stderrNotServable {
		b.WriteString("Stderr is not servable to log retrieval.\n")
	}
	for line := range strings.SplitSeq(detail, "\n") {
		if line != "" {
			fmt.Fprintf(&b, "  | %s\n", aggregatorText(line))
		}
	}
	if browser := r.browser; browser != nil {
		fmt.Fprintf(&b, "Browser scope evidence: named result %s; outside scope %t; outside package %t\n", aggregatorText(browser.namedState), browser.outside, browser.outsidePackage)
		if r.complete {
			fmt.Fprintf(&b, "Browser leaves: %d\n", browser.leaves)
		}
		for _, leaf := range browser.entries[:browser.retained] {
			fmt.Fprintf(&b, "  | %s (%s)\n", aggregatorText(leaf.name), aggregatorText(leaf.state))
		}
	}
	// Host output exposes the whole retained record even for a minimal MCP
	// result. In particular, Docker-client stderr is useful here, not servable.
	a := containerAggregator{record: r}
	b.WriteString(a.body(false))
	return b.String()
}

// commandRecord copies bounded diagnostics before the legacy buffers are
// released. Generated still uses its whole stdout for diff/stat interpretation.
func commandRecord(output *containerOutput) *containerRecord {
	a := newContainerAggregator(nil, nil)
	a.record.command = true
	a.retainCommandHead(output.stdout.Bytes())
	a.keep(&a.record.stderr, string(output.stderr.Bytes()), aggregatorStderrLimit, false)
	for _, data := range []string{output.stdout.String(), output.stderr.String()} {
		for line := range strings.SplitSeq(data, "\n") {
			if a.record.panicLine == "" && strings.Contains(line, "panic:") {
				a.record.panicLine = a.clone(summaryText(line))
			}
			if a.record.timeoutLine == "" && strings.Contains(line, "test timed out") {
				a.record.timeoutLine = a.clone(summaryText(line))
			}
		}
	}
	a.record.complete, a.record.sealed = !output.over, true
	if output.over {
		a.record.stop, a.record.capName = stopCap, "total output"
	}

	return a.record
}
