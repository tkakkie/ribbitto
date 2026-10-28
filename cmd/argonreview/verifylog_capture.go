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
	c.packageDetections = verifyLogScan(string(r.packageOutput.head) + string(r.packageOutput.tail) + string(r.compiler.head) + string(r.compiler.tail))
	c.size = c.pinnedBytes()
	return c
}

// pinnedBytes counts every retained field reachable through the record, even
// fields that are never served. Fixed arrays use the record's base charge;
// entry charges cover structs, and derived text/maps are charged separately.
// This is conservative accounting, not a second output-retention policy.
func (c *verifyLogCapture) pinnedBytes() int {
	r := c.record
	n := aggregatorBaseCharge + len(r.capName) + len(r.panicLine) + len(r.timeoutLine) + verifyLogIDBytes*2
	for o := range c.taint {
		n += len(o.head) + len(o.tail) + aggregatorNameSlotCharge*2
	}
	for _, e := range r.entries {
		n += aggregatorEntryCharge + len(e.kind) + len(e.pkg) + len(e.name) + len(e.state) + len(e.skipName) + len(e.match) + len(e.slot)
	}
	for _, names := range [][]string{r.allowed, r.skipped} {
		for _, name := range names {
			n += aggregatorNameSlotCharge + len(name)
		}
	}
	for _, e := range c.entries {
		n += aggregatorEntryCharge + len(e.display)
	}
	if b := r.browser; b != nil {
		n += aggregatorBrowserCharge + len(b.importPath) + len(b.run) + len(b.namedState)
		for _, e := range b.entries {
			n += len(e.name) + len(e.state)
		}
	}
	return n
}
