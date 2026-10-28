package main

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// event keeps unknown test2json fields, but refuses ambiguous keys before
// fixed-struct decoding. summaryEvent uses the same distinctKeys algorithm
// with a field cap, so temporary decoder storage stays bounded by one line.
func (a *containerAggregator) event(line []byte) {
	if !utf8.Valid(line) || bytes.IndexByte(line, 0) >= 0 {
		a.stop(stopMalformed, "")
		return
	}
	e, err := summaryEvent(line)
	if err != nil {
		if err.Error() == "event fields exceed limit" {
			a.stop(stopCap, "event fields")
		} else {
			a.stop(stopMalformed, "")
		}
		return
	}
	if len(e.Package) > summaryIdentityLimit || len(e.Test) > summaryIdentityLimit || len(e.ImportPath) > summaryIdentityLimit {
		a.stop(stopCap, "identity length")
		return
	}
	switch e.Action {
	case "start", "run", "pause", "cont", "output", "pass", "fail", "skip", "build-output", "build-fail":
	default:
		// Keep the oracle's diagnostic order even though action validation is
		// a per-line check, before any protocol state can be changed.
		reason := stopUnknownAction
		if e.Package == "" {
			reason = stopWithoutPackage
		} else if p := a.packages[e.Package]; p == nil || p.entry.state != "" {
			reason = stopOutsidePackage
		}
		a.stop(reason, "")
		return
	}
	if e.Action == "build-output" || e.Action == "build-fail" {
		a.notice(e.Output)
		a.keep(&a.record.compiler, e.Output, aggregatorOutputLimit, true)
		if a.record.stop != stopNone {
			return
		}
		if e.Action == "build-fail" {
			if !a.charge(aggregatorEntryCharge) {
				return
			}
			n := &aggregatorEntry{kind: "package", pkg: a.clone(e.ImportPath), state: "fail", buildFailed: true}
			if a.record.stop != stopNone {
				return
			}
			a.result(n)
		}
		a.failureSignal = true
		a.record.events++
		return
	}
	if e.Package == "" {
		a.stop(stopWithoutPackage, "")
		return
	}
	p := a.packages[e.Package]
	if e.Action == "start" {
		if p != nil || e.Test != "" {
			a.stop(stopInvalidStart, "")
			return
		}
		if len(a.packages) == aggregatorPackageLimit {
			a.stop(stopCap, "package count")
			return
		}
		if !a.charge(aggregatorPackageCharge + textCharge(len(e.Package))) {
			return
		}
		name := strings.Clone(e.Package)
		a.packages[name] = &aggregatorPackage{entry: &aggregatorEntry{kind: "package", pkg: name}, tests: make(map[string]*aggregatorEntry)}
		a.record.events++
		return
	}
	if p == nil || p.entry.state != "" {
		a.stop(stopOutsidePackage, "")
		return
	}
	t := p.tests[e.Test]
	switch e.Action {
	case "run":
		if e.Test == "" {
			a.stop(stopWithoutTest, "")
			return
		}
		if t != nil && t.started {
			a.stop(stopDuplicateTest, "")
			return
		}
		if a.tests == aggregatorTestLimit {
			a.stop(stopCap, "test count")
			return
		}
		t = a.test(p, e.Test)
		if t == nil {
			return
		}
		for parent := e.Test; ; {
			i := strings.LastIndexByte(parent, '/')
			if i < 0 {
				break
			}
			parent = parent[:i]
			n := a.test(p, parent)
			if n == nil {
				return
			}
			n.parent = true
		}
		t.started = true
		p.runs++
		a.tests++
	case "pause", "cont":
		if t == nil || !t.started || t.state != "" {
			a.stop(stopOutsideTest, "")
			return
		}
	case "output":
		// Markers are evidence even when the corresponding test later passes.
		a.notice(e.Output)
		if a.record.stop != stopNone {
			return
		}
		if strings.Contains(e.Output, "[no test files]") {
			a.noTests = true
		}
		if strings.Contains(e.Output, "[build failed]") || strings.Contains(e.Output, "panic:") || strings.Contains(e.Output, "test timed out") {
			a.failureSignal = true
		}
		if e.Test == "" {
			a.keep(&a.record.packageOutput, e.Output, aggregatorOutputLimit, true)
		} else {
			t = a.test(p, e.Test)
			if t == nil {
				return
			}
			if t.state == "" {
				a.keep(&t.output, e.Output, aggregatorTestOutputLimit, false)
			} else if t.state != "pass" {
				a.keep(&t.output, e.Output, aggregatorTestOutputLimit, true)
			}
			if a.record.stop != stopNone {
				return
			}
			a.skipMessage(t, e.Output)
		}
	case "pass", "fail", "skip":
		n := p.entry
		if e.Test != "" {
			if t == nil || !t.started || t.state != "" {
				a.stop(stopWithoutRun, "")
				return
			}
			n = t
		}
		if e.Action != "pass" && n.kind == "test" {
			a.budget(&n.output)
		}
		if a.record.stop != stopNone {
			return
		}
		// Use static states rather than retaining a decoder's string allocation.
		switch e.Action {
		case "pass":
			n.state = "pass"
		case "fail":
			n.state = "fail"
		case "skip":
			n.state = "skip"
		}
		if n.state == "pass" {
			a.drop(&n.output)
		} else {
			a.result(n)
		}
	}
	if a.record.stop == stopNone {
		a.record.events++
	}
}

// test creates placeholders for output-before-run and implicit slash ancestors,
// both accepted by the oracle. Only explicit run events consume the test cap.
func (a *containerAggregator) test(p *aggregatorPackage, name string) *aggregatorEntry {
	if t := p.tests[name]; t != nil {
		return t
	}
	if !a.charge(aggregatorTestCharge + textCharge(len(name))) {
		return nil
	}
	name = strings.Clone(name)
	t := &aggregatorEntry{kind: "test", pkg: p.entry.pkg, name: name}
	eligible, slotSize := false, 0
	for _, entry := range a.allowlist {
		if entry.pkg == p.entry.pkg && entry.test == name {
			eligible = true
			slotSize = max(slotSize, len(entry.message))
			t.message = t.message || entry.message == ""
		}
	}
	if eligible {
		if !a.charge(aggregatorSlotCharge + textCharge(slotSize)) {
			return nil
		}
		t.slot = make([]byte, 0, slotSize)
	}
	p.tests[name] = t
	return t
}

// skipMessage uses reserved rolling storage, independent of both output budgets.
// It detects a message across arbitrary event boundaries without concatenation.
func (a *containerAggregator) skipMessage(t *aggregatorEntry, output string) {
	if cap(t.slot) == 0 || t.message {
		return
	}
	for i := 0; i < len(output); i++ {
		if len(t.slot) == cap(t.slot) {
			copy(t.slot, t.slot[1:])
			t.slot = t.slot[:len(t.slot)-1]
		}
		t.slot = append(t.slot, output[i])
		for _, entry := range a.allowlist {
			if entry.pkg == t.pkg && entry.test == t.name && len(entry.message) <= len(t.slot) && string(t.slot[len(t.slot)-len(entry.message):]) == entry.message {
				t.match = entry.message
				t.message = true
				return
			}
		}
	}
}

// result links validated final events, never output-marker suspicions.
func (a *containerAggregator) result(n *aggregatorEntry) {
	if a.last == nil {
		a.first = n
	} else {
		a.last.next = n
	}
	a.last = n
	if n.state == "fail" {
		a.record.confirmedFailures++
		a.failureSignal = true
	}
}

// notice runs before dropping passing output so markers cannot disappear.
func (a *containerAggregator) notice(output string) {
	for line := range strings.SplitSeq(output, "\n") {
		if a.record.stop != stopNone {
			return
		}
		if a.record.panicLine == "" && strings.Contains(line, "panic:") {
			a.record.panicLine = a.clone(line)
		}
		if a.record.stop != stopNone {
			return
		}
		if a.record.timeoutLine == "" && strings.Contains(line, "test timed out") {
			a.record.timeoutLine = a.clone(line)
		}
	}
}
