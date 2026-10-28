package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// recordSnapshot includes nested entries without copying the record's mutex.
func aggregatorRecordSnapshot(r *containerRecord) string {
	text := fmt.Sprintf("%#v", r)
	for _, e := range r.entries {
		text += fmt.Sprintf("%#v", e)
	}
	return text
}

func TestAggregatorFinishOnce(t *testing.T) {
	complete := aggregatorRunState{completed: true}
	deadline := aggregatorRunState{deadline: true, interrupted: true}
	for _, tc := range []struct {
		name, stream         string
		first, later         aggregatorRunState
		firstCode, laterCode int
	}{
		{"deadline then complete", containerEvents("p", "TestGood", "pass"), deadline, complete, 0, 0},
		{"interrupted then complete", containerEvents("p", "TestGood", "pass"), aggregatorRunState{interrupted: true}, complete, 0, 0},
		{"complete then deadline", containerEvents("p", "TestGood", "pass"), complete, deadline, 0, 137},
		{"failed then complete", containerEvents("p", "TestGood", "pass"), complete, complete, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := aggregatorFixture(tc.stream)
			first := a.finish(tc.firstCode, nil, tc.first)
			record := aggregatorRecordSnapshot(a.record)
			for _, later := range []struct {
				code  int
				err   error
				state aggregatorRunState
			}{
				{tc.laterCode, nil, tc.later},
				{125, context.Canceled, aggregatorRunState{parentCancelled: true}},
				{0, nil, complete},
			} {
				got := a.finish(later.code, later.err, later.state)
				if got != first || aggregatorRecordSnapshot(a.record) != record {
					t.Fatalf("second finish changed sealed verdict/body/record: first=%+v later=%+v", first, got)
				}
			}
		})
	}
}

// aggregatorPark holds a charged allocation alive beside the parsed stream.
// This isolates finish's remaining headroom without a literal limit or a huge
// stream whose unrelated parser cap might hide the finalization regression.
func aggregatorPark(t *testing.T, a *containerAggregator) []byte {
	t.Helper()
	size := aggregatorMemoryLimit - a.record.accounted - aggregatorAllocationQuantum
	if size <= 0 || !a.charge(size) {
		t.Fatal("ceiling fixture could not park below the memory cap")
	}
	return make([]byte, size)
}

func TestAggregatorFinishCeiling(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		code       int
		state      aggregatorRunState
		missing    bool
	}{
		{"nonzero exit", "", 1, aggregatorRunState{completed: true}, false},
		{"kill", "", 137, aggregatorRunState{completed: true}, false},
		{"truncated tail", "{", 0, aggregatorRunState{completed: true}, false},
		{"missing package", "", 0, aggregatorRunState{completed: true}, true},
		{"nonzero cut missing", "{", 1, aggregatorRunState{completed: true}, true},
		{"deadline", "", 0, aggregatorRunState{deadline: true, interrupted: true}, false},
		{"complete skips", "", 0, aggregatorRunState{completed: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := containerJSON("p", "start", "run/TestSkip", "skip/TestSkip", "run/TestGood", "pass/TestGood")
			if !tc.missing {
				stream += containerJSON("p", "pass")
			}
			stream += tc.tail
			var want aggregatorResult
			var reason aggregatorStopReason
			for _, edge := range []bool{false, true} {
				a := aggregatorFixture(stream)
				_, _ = a.stderrWriter().Write([]byte("panic: trailing stderr"))
				var parked []byte
				if edge {
					parked = aggregatorPark(t, a)
				}
				got := a.finish(tc.code, nil, tc.state)
				if !edge {
					want, reason = got, a.record.stop
					if reason != stopNone && (len(a.skipped) != 0 || a.record.panicLine != "") {
						t.Fatal("stop recorded after final diagnostic retention")
					}
				} else {
					if aggregatorFirst(got) != aggregatorFirst(want) || a.record.stop != reason || a.record.capName != "" {
						t.Fatalf("diagnostics changed decision at ceiling: got %q/%s/%s want %q/%s", got.verdict, a.record.stop, a.record.capName, want.verdict, reason)
					}
					if !tc.state.deadline && !strings.Contains(got.body, "skip names not retained") {
						t.Fatalf("omitted skip name is silent: %q", got.body)
					}
				}
				if a.record.accounted > aggregatorMemoryLimit {
					t.Fatal("finish exceeded memory ceiling")
				}
				runtime.KeepAlive(parked)
			}
		})
	}
}

func TestAggregatorFinishReleasesSlots(t *testing.T) {
	for _, placeholder := range []bool{false, true} {
		t.Run(fmt.Sprintf("placeholder=%v", placeholder), func(t *testing.T) {
			e := measurementSkip{"p", "TestReserved", strings.Repeat("m", aggregatorTestOutputLimit)}
			a := newContainerAggregator(nil, []measurementSkip{e})
			aggregatorWrite(a, containerJSON("p", "start"))
			want := "p/TestReserved"
			if placeholder {
				// Even an unstarted output placeholder owns a slot. A skip in
				// another package must see its released capacity before naming.
				aggregatorWrite(a, testOutput("p", e.test, "x")+containerJSON("p", "pass"))
				aggregatorWrite(a, containerEvents("a", "TestSkip", "skip"))
				want = "a/TestSkip"
			} else {
				// An unconfirmed skip needs its own slot released before its
				// name is charged, regardless of either map's iteration order.
				aggregatorWrite(a, containerJSON("p", "run/"+e.test, "skip/"+e.test, "run/TestGood", "pass/TestGood", "pass"))
			}
			parked := aggregatorPark(t, a)
			got := aggregatorFinish(a)
			if a.packages[e.pkg].tests[e.test].slot != nil || a.record.stop != stopNone || strings.Join(a.skipped, ",") != want || strings.Contains(got.body, "skip names not retained") {
				t.Fatalf("skip names charged before all obsolete slots released: %q stop=%s skipped=%v", got.verdict, a.record.stop, a.skipped)
			}
			runtime.KeepAlive(parked)
		})
	}
}

func TestAggregatorFinishOrderIndependent(t *testing.T) {
	for _, gaps := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			var want string
			for i := 0; i < 2*summaryFieldLimit; i++ {
				for _, edge := range []bool{false, true} {
					allowlist := []measurementSkip{{"a", "TestAllowed", "measure"}, {"m", "TestAllowed", "measure"}, {"z", "TestAllowed", "measure"}}
					a := newContainerAggregator(nil, allowlist)
					pkgs := []string{"a", "m", "z"}
					if i%2 != 0 {
						pkgs[0], pkgs[2] = pkgs[2], pkgs[0]
					}
					for _, pkg := range pkgs {
						aggregatorWrite(a, containerJSON(pkg, "start"))
						tests := []string{"TestSkipA", "TestGood", "TestSkipZ", "TestAllowed"}
						for j := range tests {
							name := tests[(j+i)%len(tests)]
							state := "skip"
							if name == "TestGood" {
								state = "pass"
							}
							if failure && name == "TestSkipZ" {
								state = "fail"
							}
							aggregatorWrite(a, containerJSON(pkg, "run/"+name))
							if name == "TestAllowed" {
								aggregatorWrite(a, testOutput(pkg, name, "measure"))
							}
							aggregatorWrite(a, containerJSON(pkg, state+"/"+name))
						}
						if gaps {
							aggregatorWrite(a, containerJSON(pkg, "run/TestMissing"))
						}
						if !gaps || pkg != "z" {
							aggregatorWrite(a, containerJSON(pkg, "pass"))
						}
					}
					var parked []byte
					if edge {
						parked = aggregatorPark(t, a)
					}
					got := aggregatorFinish(a)
					r := a.record
					decision := fmt.Sprintf("%s/%s/%s/%v/%v/%d/%d/%d/%v/%v/%v/%v/%v", aggregatorFirst(got), r.stop, r.capName, r.packages, r.leaves, r.confirmedFailures, r.failedLeaves, r.disallowedSkips, r.complete, got.isError, a.failureSignal, a.noTests, r.allowed)
					if want == "" {
						want = decision
					}
					if decision != want {
						t.Fatalf("arrival/map order changed decision: gaps=%v failure=%v run=%d edge=%v got %q want %q", gaps, failure, i, edge, decision, want)
					}
					runtime.KeepAlive(parked)
				}
			}
		}
	}
}
