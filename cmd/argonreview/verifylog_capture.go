package main

import "strings"

type verifyLogCapture struct {
	id                                string
	record                            *containerRecord
	entries                           []verifyLogEntry
	taint                             map[*retainedOutput]int
	packageDetections, nameDetections int
	size                              int
	reads                             [5]int // Protected, with the store's counters, by verifyLogStore.mu.
}

func newVerifyLogCapture(first string, r *containerRecord) *verifyLogCapture {
	if r == nil || !strings.HasPrefix(first, "FAILED") && !strings.HasPrefix(first, "NOT VERIFIED") {
		return nil
	}
	r.mu.Lock()
	sealed := r.sealed
	r.mu.Unlock()
	if !sealed {
		return nil
	}
	entries, nameDetections := verifyLogIndex(r)
	c := &verifyLogCapture{record: r, entries: entries, nameDetections: nameDetections, taint: make(map[*retainedOutput]int)}
	for _, o := range []*retainedOutput{&r.stderr, &r.stdout, &r.compiler, &r.packageOutput} {
		c.taint[o] = verifyLogScanOutput(o)
	}
	for _, e := range r.entries {
		c.taint[&e.output] = verifyLogScanOutput(&e.output)
	}
	c.packageDetections = max(c.taint[&r.packageOutput], c.taint[&r.compiler],
		verifyLogScanSegments(string(r.packageOutput.head), string(r.packageOutput.tail), string(r.compiler.head), string(r.compiler.tail)))
	c.size = c.pinnedBytes()
	return c
}

// pinnedBytes follows grow/keep/budget: retained output capacities, not lengths,
// consume the shared and per-stream limits. The base already reserves all index
// entries and fixed arrays. Static kind/state/cap strings and trusted match text
// add no allocation; finish releases slots, and skipped aliases entry skipName.
func (c *verifyLogCapture) pinnedBytes() int {
	r := c.record
	n := aggregatorBaseCharge + verifyLogIDBytes*2
	for o := range c.taint {
		n += cap(o.head) + cap(o.tail) + aggregatorSlotCharge
	}
	for _, e := range r.entries {
		n += textCharge(len(e.pkg)) + textCharge(len(e.name)) + textCharge(len(e.skipName))
	}
	n += cap(r.allowed) * aggregatorNameSlotCharge
	for _, name := range r.allowed {
		n += textCharge(len(name))
	}
	// A verifyLogEntry has a pointer, string and two flags (32 bytes).
	// SlotCharge conservatively covers each tripwire map key/value and buckets.
	n += cap(c.entries) * 2 * aggregatorNameSlotCharge
	for _, e := range c.entries {
		n += textCharge(len(e.display))
	}
	n += textCharge(len(r.panicLine)) + textCharge(len(r.timeoutLine))
	if b := r.browser; b != nil {
		n += aggregatorBrowserCharge + textCharge(len(b.importPath)) + textCharge(len(b.run))
		for _, e := range b.entries {
			n += textCharge(len(e.name))
		}
	}
	return n
}
