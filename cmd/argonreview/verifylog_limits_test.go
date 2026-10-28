package main

import (
	"strings"
	"testing"
)

// verifyLogFullRecord fills the retained envelopes, not mutually exclusive
// transient limits: 1,000 times 32 KiB cannot coexist in the 1 MiB shared pool.
// The first entries take their full per-test caps; later ones get no allowance.
func verifyLogFullRecord(t *testing.T) *containerRecord {
	t.Helper()
	a := newContainerAggregator(nil, measurementSkips())
	r := a.record
	r.entries, r.skipped = r.indexStorage[:], r.skipStorage[:0]
	names := 0
	for i := range r.entries {
		e := &aggregatorEntry{kind: "test", pkg: "p", name: strings.Repeat("n", summaryLineLimit), state: "fail"}
		if i < len(measurementSkips()) {
			trusted := measurementSkips()[i]
			e.pkg, e.name, e.state, e.allowed = trusted.pkg, trusted.test, "skip", true
			r.allowed = append(r.allowed, a.allowedNames[i])
		} else if i < 2*len(measurementSkips()) {
			e.state, e.skipName = "skip", e.pkg+"/"+e.name
			r.skipped = append(r.skipped, e.skipName)
		}
		r.entries[i] = e
		names += len(e.pkg) + len(e.name) + len(e.skipName)
	}
	// Fill the aggregate name budget while including maximum-length identities.
	for _, e := range r.entries[2*len(measurementSkips()):] {
		add := min(aggregatorNameLimit-names, summaryIdentityLimit-len(e.name))
		e.name += strings.Repeat("n", add)
		names += add
	}
	if names != aggregatorNameLimit || len(r.entries) != aggregatorIndexLimit {
		t.Fatal("fixture did not fill the entry/name envelope")
	}
	a.keep(&r.packageOutput, strings.Repeat("p", aggregatorTestOutputLimit), aggregatorOutputLimit, true)
	a.keep(&r.compiler, strings.Repeat("c", aggregatorTestOutputLimit), aggregatorOutputLimit, true)
	for _, e := range r.entries {
		if !e.allowed {
			a.keep(&e.output, strings.Repeat("e", aggregatorTestOutputLimit), aggregatorTestOutputLimit, true)
		}
	}
	a.keep(&r.stderr, strings.Repeat("s", aggregatorStderrLimit), aggregatorStderrLimit, false)
	a.keep(&r.stdout, strings.Repeat("o", aggregatorStdoutLimit), aggregatorStdoutLimit, false)
	r.panicLine, r.timeoutLine = strings.Repeat("p", aggregatorLineLimit), strings.Repeat("t", aggregatorLineLimit)
	r.browser = &browserRecord{importPath: strings.Repeat("b", summaryIdentityLimit), run: strings.Repeat("r", summaryIdentityLimit), namedState: "fail", retained: browserLeafLimit + 1}
	for i := range r.browser.entries {
		r.browser.entries[i] = browserLeaf{strings.Repeat("b", summaryIdentityLimit), "fail"}
	}
	if a.outputBytes != aggregatorOutputLimit || cap(r.stderr.head) != aggregatorStderrLimit || cap(r.stdout.head) != aggregatorStdoutLimit || a.record.stop != stopNone {
		t.Fatal("fixture did not fill the output envelopes")
	}
	r.sealed = true
	return r
}

func TestVerifyLogFullRecordStores(t *testing.T) {
	s := &verifyLogStore{}
	var first string
	for i := 0; i <= verifyLogCaptures; i++ {
		c := s.capture("FAILED", verifyLogFullRecord(t))
		if c == nil || c.id == "" {
			t.Fatal("full valid failure did not get a capture ID")
		}
		if c.size <= verifyLogRecordMax || c.size > verifyLogCaptureMax {
			t.Fatalf("full capture accounting = %d, want R_max < size <= %d", c.size, verifyLogCaptureMax)
		}
		if i == 0 {
			first = c.id
		}
		if len(s.captures) != min(i+1, verifyLogCaptures) || s.find(c.id) != c || (s.find(first) == nil) != (i == verifyLogCaptures) {
			t.Fatal("four full captures must fit; fifth must evict oldest by count")
		}
		if s.bytes != len(s.captures)*c.size || s.bytes > verifyLogStoreBytes {
			t.Fatal("full captures exceeded store accounting")
		}
	}
}

func TestVerifyLogPinnedCapacities(t *testing.T) {
	r := verifyLogFixture("")
	c := newVerifyLogCapture("FAILED", r)
	base := c.pinnedBytes()
	r.entries[0].output.head = make([]byte, 1, aggregatorTestOutputLimit/2)
	r.entries[0].output.tail = make([]byte, 0, aggregatorTestOutputLimit/2)
	r.stdout.head = make([]byte, 1, aggregatorStdoutLimit)
	r.allowed = make([]string, 0, len(measurementSkips()))
	want := aggregatorTestOutputLimit + aggregatorStdoutLimit + cap(r.allowed)*aggregatorNameSlotCharge
	if got := c.pinnedBytes() - base; got != want {
		t.Fatalf("pinned capacity charge = %d, want %d", got, want)
	}
}
