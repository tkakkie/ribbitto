package main

import (
	"fmt"
	"sort"
	"strings"
)

type verifyLogEntry struct {
	entry               *aggregatorEntry
	display             string
	ambiguous, withheld bool
}

func verifyLogIndex(r *containerRecord) ([]verifyLogEntry, int) {
	var leaves, packages []verifyLogEntry
	detections := 0
	for _, e := range r.entries {
		withheld := verifyLogScan(e.pkg+"/"+e.name) > 0
		if withheld {
			detections++
		}
		if e.parent || e.allowed {
			continue
		}
		name := e.pkg + "/" + e.name
		if e.kind == "package" {
			if e.state != "fail" {
				continue
			}
			name = e.pkg + ": package failed"
			if e.buildFailed {
				name = e.pkg + " [build failed]"
			}
		} else if e.kind != "test" || e.state != "fail" && e.state != "skip" {
			continue
		}
		display := summaryText(name)
		if !withheld && verifyLogScan(display) > 0 {
			withheld = true
			detections++
		}
		if withheld {
			display = "[name withheld]"
		}
		item := verifyLogEntry{entry: e, display: display, withheld: withheld}
		if e.kind == "test" {
			leaves = append(leaves, item)
		} else {
			packages = append(packages, item)
		}
	}
	// recordSummaryFacts sorts failures by raw pkg+"/"+name. Disallowed skips
	// take their place in that leaf order. Its packageFailures entry walk is
	// r.entries arrival order (after textual package notices), never sorted.
	sort.SliceStable(leaves, func(i, j int) bool {
		a, b := leaves[i].entry, leaves[j].entry
		return a.pkg+"/"+a.name < b.pkg+"/"+b.name
	})
	index := append(leaves, packages...)
	counts := make(map[string]int)
	for _, e := range index {
		counts[e.display]++
	}
	for i := range index {
		index[i].ambiguous = counts[index[i].display] > 1
	}
	return index, detections
}

func (c *verifyLogCapture) indexLines() []string {
	r := c.record
	lines := []string{"Retained failed and disallowed skipped leaves; failed packages."}
	if r.complete && !r.command {
		lines = append(lines, fmt.Sprintf("Packages: %d passed, %d failed, %d skipped", r.packages.passed, r.packages.failed, r.packages.skipped),
			fmt.Sprintf("Leaf tests: %d passed, %d failed, %d skipped", r.leaves.passed, r.leaves.failed, r.leaves.skipped))
	}
	if !r.complete && !r.command {
		lines = append(lines, "Failures (observed, total unknown):")
	}
	for i, item := range c.entries {
		e := item.entry
		line := fmt.Sprintf("#%d %s %s (%s)", i+1, e.kind, item.display, e.state)
		if !r.complete && !r.command {
			line += " observed"
		}
		if item.ambiguous {
			line += " ambiguous: use #N"
		}
		lines = append(lines, line)
	}
	if r.namesNotRetained > 0 {
		if r.complete {
			lines = append(lines, fmt.Sprintf("names not retained for %d failures", r.namesNotRetained))
		} else {
			lines = append(lines, strings.TrimSuffix(fmt.Sprintf(observedFailuresNotRetained, r.namesNotRetained), "\n"))
		}
	}
	if omitted := r.disallowedSkips - len(r.skipped); omitted > 0 {
		lines = append(lines, fmt.Sprintf("%d skip names not retained", omitted))
	}
	return lines
}
