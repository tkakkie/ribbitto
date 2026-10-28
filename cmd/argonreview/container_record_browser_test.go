package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestContainerBrowserRecordEvidence(t *testing.T) {
	a := newContainerAggregator(nil, nil)
	a.retainBrowserScope(browserTestPackage, browserTestRun)
	before := a.record.accounted
	stream := containerJSON(browserTestPackage, "start", "run/"+browserTestRun)
	for i := 0; i < browserLeafLimit+1; i++ {
		name := fmt.Sprintf("%s/child%d", browserTestRun, i)
		stream += containerJSON(browserTestPackage, "run/"+name, "pass/"+name)
	}
	stream += containerJSON(browserTestPackage, "pass/"+browserTestRun, "pass")
	aggregatorWrite(a, stream)
	result := aggregatorFinish(a)
	r := a.record
	if r.browser == nil || r.browser.retained != browserLeafLimit+1 || r.browser.leaves != browserLeafLimit+1 || before < aggregatorBaseCharge+aggregatorBrowserCharge {
		t.Fatal("browser evidence unbounded, missing or uncharged")
	}
	for _, entry := range r.browser.entries {
		if entry.name == "" || entry.state != "pass" {
			t.Fatal("leaf name/result missing")
		}
	}
	wanted := fmt.Sprintf("NOT VERIFIED: too many tests ran (%d, limit %d); name fewer tests", browserLeafLimit+1, browserLeafLimit)
	if got := browserVerdict(browserTestRun, r, result); got != wanted {
		t.Fatalf("browser ignores record: %s", got)
	}
	r.browser.outside = true
	if got := browserVerdict(browserTestRun, r, result); got != "NOT VERIFIED: a test outside the named test ran" {
		t.Fatalf("outside evidence ignored: %s", got)
	}
	if !reflect.DeepEqual(recordSummaryFacts(r).leaves, r.leaves) {
		t.Fatal("browser summary facts differ")
	}
}

func TestContainerBrowserRecordDecisionAllocations(t *testing.T) {
	run := browserTestRun + "/" + strings.Repeat("a", len(browserTestRun))
	a := newContainerAggregator(nil, nil)
	a.retainBrowserScope(browserTestPackage, run)
	name := run + "/" + strings.Repeat("x", summaryIdentityLimit-len(run)-1)
	aggregatorWrite(a, containerJSON(browserTestPackage, "start", "run/"+run, "run/"+name, "pass/"+name, "pass/"+run, "pass"))
	a.stop(stopCap, "total output")
	if allocs := testing.AllocsPerRun(100, func() { a.record.browser.leaves, a.record.browser.retained = 0, 0; a.finishBrowser() }); allocs != 0 {
		t.Fatalf("browser decision allocated after stop: %v", allocs)
	}
	if a.record.browser.leaves != 1 || a.record.browser.entries[0].name != name {
		t.Fatal("decision lost leaf evidence")
	}
}
