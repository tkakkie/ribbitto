package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestAggregatorSoftLimits(t *testing.T) {
	for _, kind := range []string{"test output", "failure output", "package output", "stderr", "index entries", "index name bytes", "stdout head"} {
		t.Run(kind, func(t *testing.T) {
			e := measurementSkip{"p", "TestCost", "set MEASURE=1"}
			a := newContainerAggregator(nil, []measurementSkip{e})
			aggregatorWrite(a, containerJSON("p", "start"))
			failures := 1
			switch kind {
			case "failure output":
				failures = aggregatorOutputLimit/aggregatorTestOutputLimit + 1
			case "index entries":
				failures = aggregatorIndexLimit + 1
			case "index name bytes":
				failures = aggregatorNameLimit/(summaryIdentityLimit/2) + 1
			}
			for i := 0; i < failures; i++ {
				name := fmt.Sprintf("TestBad%d", i)
				if kind == "index name bytes" {
					name += strings.Repeat("x", summaryIdentityLimit/2-len(name))
				}
				aggregatorWrite(a, containerJSON("p", "run/"+name))
				if kind == "test output" || kind == "failure output" {
					aggregatorWrite(a, testOutput("p", name, strings.Repeat("h", aggregatorTestOutputLimit/2)))
					aggregatorWrite(a, testOutput("p", name, strings.Repeat("t", aggregatorTestOutputLimit/2+1)))
				}
				aggregatorWrite(a, containerJSON("p", "fail/"+name))
			}
			if kind == "package output" {
				line := testOutput("p", "", strings.Repeat("x", aggregatorTestOutputLimit))
				for n := 0; n <= aggregatorOutputLimit; n += aggregatorTestOutputLimit {
					aggregatorWrite(a, line)
				}
			}
			if kind == "stderr" {
				chunk := []byte(strings.Repeat("s", aggregatorStderrLimit/8) + "\n")
				for n := 0; n <= aggregatorStderrLimit; n += len(chunk) {
					_, _ = a.stderrWriter().Write(chunk)
				}
			}
			if kind == "stdout head" {
				a.retainCommandHead([]byte(strings.Repeat("x", aggregatorStdoutLimit)))
				a.retainCommandHead([]byte("x"))
			}
			// Confirm late and split, after every diagnostic pool may be full.
			aggregatorWrite(a, containerJSON("p", "run/"+e.test))
			aggregatorWrite(a, testOutput("p", e.test, strings.Repeat("n", aggregatorTestOutputLimit+1)))
			middle := len(e.message) / 2
			aggregatorWrite(a, testOutput("p", e.test, e.message[:middle]))
			aggregatorWrite(a, testOutput("p", e.test, e.message[middle:]))
			aggregatorWrite(a, containerJSON("p", "skip/"+e.test, "run/TestOrdinary", "skip/TestOrdinary", "run/TestGood", "pass/TestGood", "fail"))
			got := aggregatorFinish(a)
			if got.verdict != "FAILED" || a.record.stop != stopNone || !a.record.complete || a.record.failedLeaves != failures || a.record.leaves.failed != failures || strings.Join(a.record.allowed, ",") != "p/TestCost" || strings.Join(a.skipped, ",") != "p/TestOrdinary" {
				t.Fatalf("soft limit changed evidence: %q stop=%s/%s failures=%d allowed=%v skipped=%v", got.verdict, a.record.stop, a.record.capName, a.record.failedLeaves, a.record.allowed, a.record.skipped)
			}
			if kind == "stdout head" && (len(a.record.stdout.head) != aggregatorStdoutLimit || len(a.record.stdout.tail) != 0) {
				t.Fatal("stdout prefix not bounded")
			}
			switch kind {
			case "test output":
				o := a.record.entries[0].output
				if len(o.head) != aggregatorTestOutputLimit/2 || len(o.tail) != aggregatorTestOutputLimit/2 || o.total-len(o.head)-len(o.tail) != 1 || o.head[0] != 'h' || o.tail[len(o.tail)-1] != 't' || !strings.Contains(got.body, "truncated: 1 bytes not retained") {
					t.Fatal("head/tail omission lost")
				}
			case "failure output":
				if a.outputBytes > aggregatorOutputLimit || !a.record.entries[failures-1].output.notRetained || !strings.Contains(got.body, "not retained") {
					t.Fatal("shared omission lost")
				}
			case "package output":
				if a.outputBytes > aggregatorOutputLimit || a.record.packageOutput.total <= len(a.record.packageOutput.head)+len(a.record.packageOutput.tail) {
					t.Fatal("package output not bounded")
				}
			case "stderr":
				if len(a.record.stderr.head) != aggregatorStderrLimit || a.record.stderr.total <= aggregatorStderrLimit || a.record.stderrNotServable {
					t.Fatal("stderr retention changed trust or omission")
				}
			case "index entries", "index name bytes":
				if a.record.namesNotRetained == 0 || len(a.record.entries) > aggregatorIndexLimit || a.indexBytes > aggregatorNameLimit {
					t.Fatal("index not bounded")
				}
			}
		})
	}
}

func TestAggregatorAllowlistReservation(t *testing.T) {
	e := measurementSkip{"p", "TestCost", "set MEASURE=1"}
	for _, confirmed := range []bool{false, true} {
		a := newContainerAggregator(nil, []measurementSkip{e})
		aggregatorWrite(a, containerJSON("p", "start"))
		for n := 0; n <= aggregatorOutputLimit; n += aggregatorTestOutputLimit {
			aggregatorWrite(a, testOutput("p", "", strings.Repeat("x", aggregatorTestOutputLimit)))
		}
		if a.outputBytes != aggregatorOutputLimit {
			t.Fatalf("reservation fixture has not filled the shared budget: %d", a.outputBytes)
		}
		aggregatorWrite(a, containerJSON("p", "run/"+e.test))
		if confirmed {
			aggregatorWrite(a, testOutput("p", e.test, e.message))
		}
		aggregatorWrite(a, containerJSON("p", "skip/"+e.test, "run/TestGood", "pass/TestGood", "pass"))
		got := aggregatorFinish(a)
		want := "NOT VERIFIED: 1 skipped"
		if confirmed {
			want = "verified"
		}
		if aggregatorFirst(got) != want || (len(a.record.allowed) == 1) != confirmed {
			t.Fatalf("confirmed=%v: %q allowed=%v", confirmed, got.verdict, a.record.allowed)
		}
	}
}

func TestAggregatorMarkersBeforeDiscard(t *testing.T) {
	for _, marker := range []string{"[no test files]", "[build failed]", "panic: broken", "test timed out"} {
		a := aggregatorFixture(containerJSON("p", "start", "run/TestOne") + testOutput("p", "TestOne", marker) + containerJSON("p", "pass/TestOne", "pass"))
		got := aggregatorFinish(a)
		if aggregatorFirst(got) == "verified" || a.packages["p"].tests["TestOne"].output.total != 0 {
			t.Fatalf("discard hid marker %q: %q", marker, got.verdict)
		}
	}
	a := aggregatorFixture(`{"Action":"build-output","ImportPath":"p","Output":"panic: compiler diagnostic"}` + "\n")
	if got := aggregatorFinish(a); got.verdict != "FAILED" || a.record.panicLine != "panic: compiler diagnostic" {
		t.Fatal("compiler marker lost")
	}
}

func TestAggregatorIndexOrder(t *testing.T) {
	stream := containerJSON("p", "start", "run/TestParent", "fail/TestParent", "run/TestParent/child", "fail/TestParent/child", "run/TestA", "skip/TestA", "fail")
	a := aggregatorFixture(stream)
	aggregatorFinish(a)
	if len(a.record.entries) != 3 || a.record.entries[0].name != "TestParent/child" || a.record.entries[1].name != "TestA" || a.record.entries[2].kind != "package" || a.record.leaves.failed != 1 {
		t.Fatal("index did not preserve leaf result order")
	}
}

func TestAggregatorConfirmedVersusObserved(t *testing.T) {
	stream := containerJSON("p", "start", "run/TestBad", "fail/TestBad")
	a := aggregatorFixture(stream)
	got := aggregatorFinish(a)
	if a.record.complete || !strings.Contains(got.body, "observed, total unknown") || strings.Contains(got.body, "Leaf tests:") {
		t.Fatal("missing package supplied total")
	}
	a = aggregatorFixture(stream + containerJSON("p", "fail"))
	got = aggregatorFinish(a)
	if !a.record.complete || strings.Contains(got.body, "observed, total unknown") || !strings.Contains(got.body, "Leaf tests: 0 passed, 1 failed, 0 skipped") {
		t.Fatal("complete stream lost confirmed total")
	}
}

func TestAggregatorStderrTrust(t *testing.T) {
	for _, code := range []int{0, 1, 2, 125, 126, 127, 137} {
		a := aggregatorFixture(containerEvents("p", "TestOne", "pass"))
		_, _ = a.stderrWriter().Write([]byte("diagnostic\n"))
		a.finish(code, nil, aggregatorRunState{completed: true})
		if a.record.stderrNotServable != (code >= 125 && code <= 127) {
			t.Fatalf("exit %d trust=%v", code, a.record.stderrNotServable)
		}
	}
}

func TestAggregatorTailAfterSharedLimit(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start", "run/TestBad") + testOutput("p", "TestBad", strings.Repeat("h", aggregatorTestOutputLimit)) + containerJSON("p", "fail/TestBad"))
	for n := a.outputBytes; n < aggregatorOutputLimit; n += aggregatorTestOutputLimit {
		aggregatorWrite(a, testOutput("p", "", strings.Repeat("p", aggregatorTestOutputLimit)))
	}
	aggregatorWrite(a, testOutput("p", "TestBad", "last diagnostic"))
	aggregatorWrite(a, containerJSON("p", "fail"))
	got := aggregatorFinish(a)
	o := a.record.entries[0].output
	if got.verdict != "FAILED" || !strings.HasSuffix(string(o.tail), "last diagnostic") || a.outputBytes > aggregatorOutputLimit {
		t.Fatal("existing tail stopped rolling at shared limit")
	}
}

func TestAggregatorSharedCapacity(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start"))
	chunk := strings.Repeat("x", aggregatorTestOutputLimit/16+1)
	for i := 0; i < aggregatorIndexLimit; i++ {
		name := fmt.Sprintf("Test%d", i)
		aggregatorWrite(a, containerJSON("p", "run/"+name)+testOutput("p", name, chunk)+testOutput("p", name, "x")+containerJSON("p", "fail/"+name))
	}
	aggregatorWrite(a, containerJSON("p", "fail"))
	aggregatorFinish(a)
	capacity := cap(a.record.packageOutput.head) + cap(a.record.packageOutput.tail) + cap(a.record.compiler.head) + cap(a.record.compiler.tail)
	for _, p := range a.packages {
		for _, test := range p.tests {
			capacity += cap(test.output.head) + cap(test.output.tail)
		}
	}
	if a.record.stop != stopNone || capacity > aggregatorOutputLimit || capacity != a.outputBytes {
		t.Fatalf("shared capacity=%d accounted=%d", capacity, a.outputBytes)
	}
}

func TestAggregatorRecordRetention(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start"))
	for i := 0; i < aggregatorIndexLimit+1; i++ {
		name := fmt.Sprintf("Test%d", i)
		aggregatorWrite(a, containerJSON("p", "run/"+name, "skip/"+name))
	}
	aggregatorWrite(a, containerJSON("p", "pass"))
	got := aggregatorFinish(a)
	if a.record.disallowedSkips != aggregatorIndexLimit+1 || len(a.skipped) != aggregatorIndexLimit+1 || len(a.record.skipped) > aggregatorIndexLimit || len(a.record.entries) > aggregatorIndexLimit || !strings.HasPrefix(got.verdict, fmt.Sprintf("NOT VERIFIED: %d skipped", aggregatorIndexLimit+1)) {
		t.Fatal("saved index changed the temporary verdict list")
	}
	for _, entry := range a.record.entries {
		if entry.next != nil {
			t.Fatal("record pins protocol state beyond its index")
		}
	}
	if a.record.stdout.total != 0 {
		t.Fatal("record retained raw passing-test stream")
	}
}

func TestAggregatorRecordNameBudget(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start"))
	count := aggregatorNameLimit/(summaryIdentityLimit/2) + 1
	for i := 0; i < count; i++ {
		prefix := fmt.Sprintf("Test%d", i)
		name := prefix + strings.Repeat("x", summaryIdentityLimit/2-len(prefix))
		aggregatorWrite(a, containerJSON("p", "run/"+name, "skip/"+name))
	}
	aggregatorWrite(a, containerJSON("p", "pass"))
	got := aggregatorFinish(a)
	bytes := 0
	for _, entry := range a.record.entries {
		bytes += len(entry.pkg) + len(entry.name) + len(entry.skipName)
	}
	if bytes > aggregatorNameLimit || len(a.record.skipped) >= count || a.record.disallowedSkips != count || len(a.skipped) != count || !strings.HasPrefix(got.verdict, fmt.Sprintf("NOT VERIFIED: %d skipped", count)) {
		t.Fatalf("saved names=%d skipped=%d verdict=%q", bytes, len(a.record.skipped), aggregatorFirst(got))
	}
}

func TestAggregatorAllowedSkipBeforeStop(t *testing.T) {
	e := measurementSkips()[0]
	a := newContainerAggregator(nil, []measurementSkip{e})
	aggregatorWrite(a, measurementStream(e, e.message, true))
	a.stop(stopCap, "total output")
	got := aggregatorFinish(a)
	if len(a.record.allowed) != 1 || a.record.allowed[0] != e.pkg+"/"+e.test || len(a.record.entries) != 1 || !a.record.entries[0].allowed || aggregatorFirst(got) != "NOT VERIFIED: output limit exceeded (output cut off)" {
		t.Fatal("stop lost an already confirmable allowed skip")
	}
}

func TestAggregatorAlternativeSkipMessages(t *testing.T) {
	first := measurementSkip{"p", "TestCost", "first gate"}
	second := measurementSkip{"p", "TestCost", "another, longer gate"}
	for _, message := range []string{first.message, second.message, ""} {
		entries := []measurementSkip{first, second}
		if message == "" {
			entries = append(entries, measurementSkip{"p", "TestCost", ""})
		}
		stream := measurementStream(first, message, true)
		a := newContainerAggregator(nil, entries)
		aggregatorWrite(a, stream)
		if got := aggregatorFinish(a); got.verdict != oracleVerdict(0, nil, []byte(stream), false, entries) || aggregatorFirst(got) != "verified" {
			t.Fatalf("alternate message %q: %q", message, got.verdict)
		}
	}
}

func TestAggregatorCrossingOutputDoesNotConfirmSkip(t *testing.T) {
	e := measurementSkip{"p", "TestCost", "measure"}
	a := newContainerAggregator(nil, []measurementSkip{e})
	aggregatorWrite(a, containerJSON("p", "start", "run/"+e.test, "skip/"+e.test, "run/TestGood", "pass/TestGood"))
	line := testOutput("p", e.test, e.message)
	if !a.grow(&a.lines[0], len(line), aggregatorLineLimit) {
		t.Fatal("preparing line capacity")
	}
	a.record.accounted = aggregatorMemoryLimit
	aggregatorWrite(a, line)
	got := aggregatorFinish(a)
	if a.record.capName != "retained memory" || a.packages["p"].tests[e.test].message || len(a.record.allowed) != 0 || a.record.events != 5 || aggregatorFirst(got) != "NOT VERIFIED: output limit exceeded (output cut off)" {
		t.Fatal("crossing output line confirmed a skip message")
	}
}
