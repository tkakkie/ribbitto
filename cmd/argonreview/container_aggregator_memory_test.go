package main

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestAggregatorChargesCapacity(t *testing.T) {
	a := newContainerAggregator(nil, nil)
	var buf []byte
	before := a.record.accounted
	if !a.grow(&buf, 1, aggregatorLineLimit) {
		t.Fatal("initial growth refused")
	}
	if cap(buf) <= 1 || a.record.accounted-before != cap(buf) {
		t.Fatalf("charged length instead of capacity: charge=%d capacity=%d", a.record.accounted-before, cap(buf))
	}
	old := cap(buf)
	before = a.record.accounted
	if !a.grow(&buf, old+1, aggregatorLineLimit) || a.record.accounted-before != cap(buf)-old {
		t.Fatal("replacement capacity charge wrong")
	}
	a.record.accounted = aggregatorMemoryLimit - cap(buf) + 1
	before = a.record.accounted
	oldBuffer := buf[:cap(buf)]
	if a.grow(&buf, cap(buf)+1, aggregatorLineLimit) || cap(buf) != len(oldBuffer) || a.record.accounted != before || a.record.capName != "retained memory" {
		t.Fatal("allocation or charge occurred before ceiling check")
	}
}

func TestAggregatorNoAllocationAfterStop(t *testing.T) {
	for _, reason := range []aggregatorStopReason{stopCap, stopMalformed, stopDeadline, stopParentCancellation, stopKill} {
		a := newContainerAggregator(nil, nil)
		a.stop(reason, "")
		out, errout := a.stdoutWriter(), a.stderrWriter()
		input := []byte(containerEvents("p", "TestOne", "pass"))
		before := a.record.accounted
		allocs := testing.AllocsPerRun(100, func() {
			if n, err := out.Write(input); n != len(input) || err != nil {
				panic("writer contract")
			}
			if n, err := errout.Write(input); n != len(input) || err != nil {
				panic("writer contract")
			}
		})
		if allocs != 0 || a.record.accounted != before || a.record.events != 0 {
			t.Fatalf("after %s: allocations=%v bytes=%d", reason, allocs, a.record.accounted-before)
		}
	}
}

func TestAggregatorChargeBeforeAllocate(t *testing.T) {
	a := newContainerAggregator(nil, nil)
	buf := make([]byte, 0, 16)
	allocs := testing.AllocsPerRun(100, func() {
		a.record.stop = stopNone
		a.record.accounted = aggregatorMemoryLimit
		a.grow(&buf, cap(buf)+1, aggregatorLineLimit)
	})
	if allocs != 0 || cap(buf) != 16 {
		t.Fatalf("refused growth allocated: allocations=%v capacity=%d", allocs, cap(buf))
	}
}

func TestAggregatorSealed(t *testing.T) {
	a := aggregatorFixture(containerEvents("p", "TestOne", "pass"))
	before := aggregatorFinish(a)
	input := []byte("not another event\n")
	allocs := testing.AllocsPerRun(100, func() { _, _ = a.stdoutWriter().Write(input); _, _ = a.stderrWriter().Write(input) })
	if allocs != 0 || aggregatorFinish(a).verdict != before.verdict || a.record.events != 4 {
		t.Fatal("sealed record changed or allocated")
	}
}

func TestAggregatorAccountingCoversHeap(t *testing.T) {
	for _, shape := range []string{"tests", "packages", "ancestors", "output", "skips"} {
		t.Run(shape, func(t *testing.T) {
			a := newContainerAggregator(nil, []measurementSkip{{"p", "TestCost", "measure"}})
			aggregatorWrite(a, containerJSON("p", "start"))
			base := a.record.accounted
			// Measure incremental ownership: the pre-existing base envelope must
			// not hide an undercharged test or map entry.
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for i := 0; i < aggregatorTestLimit/16; i++ {
				name := fmt.Sprintf("Test%d", i)
				switch shape {
				case "packages":
					aggregatorWrite(a, containerJSON(fmt.Sprintf("pkg%d", i), "start", "pass"))
				case "ancestors":
					aggregatorWrite(a, containerJSON("p", "run/"+name+"/a/b/c", "pass/"+name+"/a/b/c"))
				case "output":
					if i >= aggregatorOutputLimit/aggregatorTestOutputLimit {
						aggregatorWrite(a, containerJSON("p", "run/"+name, "pass/"+name))
						continue
					}
					aggregatorWrite(a, containerJSON("p", "run/"+name)+testOutput("p", name, strings.Repeat("x", aggregatorTestOutputLimit))+containerJSON("p", "fail/"+name))
				case "skips":
					aggregatorWrite(a, containerJSON("p", "run/"+name, "skip/"+name))
				default:
					aggregatorWrite(a, containerJSON("p", "run/"+name, "pass/"+name))
				}
			}
			aggregatorWrite(a, containerJSON("p", "pass"))
			aggregatorFinish(a)
			runtime.GC()
			runtime.ReadMemStats(&after)
			delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			accounted := a.record.accounted - base
			// Global heap sampling includes allocator/GC bookkeeping noise. Allow
			// one allocator quantum per synthetic test, far below the difference
			// between the required test charge and the undercharging mutant.
			tolerance := aggregatorTestLimit / 16 * aggregatorAllocationQuantum
			if a.record.stop != stopNone || int64(accounted+tolerance) < delta {
				t.Fatalf("%s: accounted excluding base=%d live delta=%d stop=%s/%s", shape, accounted, delta, a.record.stop, a.record.capName)
			}
			runtime.KeepAlive(a)
		})
	}
}

// finishedAggregatorRecord deliberately lets the protocol maps die before the
// caller measures the saved record. An interior pointer into the aggregator
// would otherwise pin all passing identities despite a bounded retrieval index.
func finishedAggregatorRecord(result string) *containerRecord {
	a := aggregatorFixture(containerJSON("p", "start"))
	for i := 0; i < aggregatorTestLimit; i++ {
		name := fmt.Sprintf("Test%d", i)
		aggregatorWrite(a, containerJSON("p", "run/"+name, "pass/"+name))
	}
	aggregatorWrite(a, containerJSON("p", result))
	aggregatorFinish(a)
	return a.record
}

func TestAggregatorRecordReleasesProtocolState(t *testing.T) {
	for _, result := range []string{"pass", "fail", "skip"} {
		t.Run(result, func(t *testing.T) {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			r := finishedAggregatorRecord(result)
			if result != "pass" && (len(r.entries) != 1 || r.entries[0].kind != "package" || r.entries[0].state != result) {
				t.Fatalf("retention fixture lost package result: %s", result)
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			bound := aggregatorOutputLimit + aggregatorStderrLimit + aggregatorNameLimit + aggregatorIndexLimit*aggregatorEntryCharge + aggregatorStdoutLimit + 2*aggregatorLineLimit + aggregatorBaseCharge
			if !r.complete || r.leaves.passed != aggregatorTestLimit || int64(after.HeapAlloc)-int64(before.HeapAlloc) > int64(bound) {
				t.Fatalf("saved record pins protocol state: complete=%v passed=%d live=%d bound=%d", r.complete, r.leaves.passed, int64(after.HeapAlloc)-int64(before.HeapAlloc), bound)
			}
			runtime.KeepAlive(r)
		})
	}
}

func TestAggregatorZeroBudgetAllocations(t *testing.T) {
	a := newContainerAggregator(nil, nil)
	a.outputBytes = aggregatorOutputLimit
	head := make([]byte, aggregatorTestOutputLimit/2)
	tail := make([]byte, aggregatorTestOutputLimit/2)
	before := a.record.accounted
	allocs := testing.AllocsPerRun(100, func() {
		o := retainedOutput{head: head, tail: tail, total: len(head) + len(tail)}
		a.record.accounted = before + cap(head) + cap(tail)
		a.budget(&o)
		if !o.notRetained || o.total != len(head)+len(tail) || len(o.head)+len(o.tail) != 0 || a.record.accounted != before {
			t.Fatal("zero budget changed output accounting")
		}
	})
	if allocs != 0 {
		t.Fatalf("zero budget copied diagnostics: %v allocations", allocs)
	}
}
