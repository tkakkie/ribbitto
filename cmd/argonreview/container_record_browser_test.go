package main

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestContainerBrowserRecordSkippedNames(t *testing.T) {
	v := browserRunFixture(t)
	execution := containerExecution{browserRun: browserTestRun, admit: v.browserAdmission}
	v.runner = fakeContainerRunner{func(_ context.Context, args, _ []string, out, _ io.Writer) (int, error) {
		if args[0] == "run" {
			_, _ = io.WriteString(out, containerJSON(browserTestPackage, "start", "run/"+browserTestRun))
			for i := 0; i <= aggregatorIndexLimit; i++ {
				name := fmt.Sprintf("%s/child%04d", browserTestRun, i)
				_, _ = io.WriteString(out, containerJSON(browserTestPackage, "run/"+name, "skip/"+name))
			}
			_, _ = io.WriteString(out, containerJSON(browserTestPackage, "pass/"+browserTestRun, "pass"))
		}
		return 0, nil
	}}
	report, err := runContainerExecution(context.Background(), v.worktree, verifyBrowserCheck, "./internal/web", v.runner, v.copyParent, grantContainerExecution(&execution))
	r := execution.record
	if err != nil || r == nil || !r.complete || r.disallowedSkips != aggregatorIndexLimit+1 || len(r.skipped) >= r.disallowedSkips {
		t.Fatalf("browser skip retention fixture: record=%+v err=%v", r, err)
	}
	verdict := fmt.Sprintf("NOT VERIFIED: %d skipped", r.disallowedSkips)
	var want strings.Builder
	for _, name := range r.skipped {
		fmt.Fprintf(&want, "  | %s\n", aggregatorText(name))
	}
	prefix := verdict + "\n" + containerCheckScope(verifyBrowserCheck) + "\n"
	_, detail, _ := strings.Cut(strings.TrimPrefix(report, prefix), "\n")
	list, _, _ := strings.Cut(detail, "Browser scope evidence:")
	if !strings.HasPrefix(report, prefix) || list != want.String() {
		t.Fatal("browser skipped verdict list must equal record-bounded skip names")
	}
	omitted := r.disallowedSkips - len(r.skipped)
	if !strings.Contains(report, fmt.Sprintf("%d skip names not retained\n", omitted)) || strings.Contains(list, "child1000") {
		t.Fatal("browser skip names beyond retention must be reported as not retained")
	}
	if !strings.HasPrefix(execution.summary, verdict+"\n") || !strings.Contains(execution.summary, fmt.Sprintf("Leaf tests: 0 passed, 0 failed, %d skipped", r.disallowedSkips)) {
		t.Fatal("browser summary must preserve skip verdict and counts")
	}
}

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
