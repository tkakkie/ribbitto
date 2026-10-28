package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAggregatorMalformed(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"utf8", "{\"Action\":\"output\",\"Package\":\"p\",\"Output\":\"\xff\"}\n", "malformed test stream"},
		{"raw NUL", "{\"Action\":\"output\",\"Package\":\"p\",\"Output\":\"\x00\"}\n", "malformed test stream"},
		{"duplicate", `{"Action":"pass","Action":"output","Package":"p"}` + "\n", "malformed test stream"},
		{"case folded", `{"Action":"pass","ACTION":"output","Package":"p"}` + "\n", "malformed test stream"},
		{"unknown action", containerJSON("p", "mystery"), "unknown test action"},
		{"not object", "null\n", "malformed test stream"},
		{"wrong output type", `{"Action":"output","Package":"p","Output":42}` + "\n", "malformed test stream"},
		{"missing package", `{"Action":"output"}` + "\n", "event without package"},
		{"outside package", containerJSON("q", "pass"), "event outside a running package"},
		{"duplicate package", containerJSON("p", "start"), "duplicate or invalid package start"},
		{"invalid start", containerJSON("q", "start/TestOne"), "duplicate or invalid package start"},
		{"nameless run", containerJSON("p", "run"), "test without name"},
		{"duplicate test", containerJSON("p", "run/TestOne", "run/TestOne"), "duplicate test"},
		{"pause without run", containerJSON("p", "pause/TestOne"), "event outside a running test"},
		{"result without run", containerJSON("p", "fail/TestOne"), "result without a running test"},
		{"duplicate result", containerJSON("p", "run/TestOne", "pass/TestOne", "fail/TestOne"), "result without a running test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := aggregatorFixture(containerJSON("p", "start", "run/TestGood", "pass/TestGood") + tc.line + containerJSON("p", "pass"))
			got := aggregatorFinish(a)
			if aggregatorFirst(got) != "FAILED: "+tc.want || !got.isError {
				t.Fatalf("got %q, want FAILED: %s", got.verdict, tc.want)
			}
			if a.record.confirmedFailures != 0 {
				t.Fatal("malformed event confirmed a failure")
			}
		})
	}
}

func TestAggregatorStandardFields(t *testing.T) {
	for _, extra := range []string{`,"Time":"2026-10-28T00:00:00Z","Elapsed":0.001,"FailedBuild":"example/p"`, `,"Output":"escaped \u0000"`} {
		stream := containerJSON("p", "start", "run/TestOne") + `{"Action":"output","Package":"p","Test":"TestOne"` + extra + "}\n" + containerJSON("p", "pass/TestOne", "pass")
		a := aggregatorFixture(stream)
		if got := aggregatorFinish(a); got.verdict != oracleVerdict(0, nil, []byte(stream), false, nil) || aggregatorFirst(got) != "verified" {
			t.Fatalf("real test2json fields refused: %q", got.verdict)
		}
	}
}

func TestAggregatorIdentityLimit(t *testing.T) {
	for _, field := range []string{"test", "package", "import path"} {
		t.Run(field, func(t *testing.T) {
			for _, size := range []int{summaryIdentityLimit, summaryIdentityLimit + 1} {
				identity := strings.Repeat("x", size)
				stream := containerEvents("p", identity, "pass")
				if field == "package" {
					stream = containerEvents(identity, "TestOne", "pass")
				}
				if field == "import path" {
					stream = `{"Action":"build-output","ImportPath":"` + identity + `"}` + "\n" + containerEvents("p", "TestOne", "pass")
				}
				a := aggregatorFixture(stream)
				got := aggregatorFinish(a)
				if size == summaryIdentityLimit && a.record.capName != "" {
					t.Fatalf("boundary refused: %q", got.verdict)
				}
				if size > summaryIdentityLimit && (a.record.capName != "identity length" || aggregatorFirst(got) == "verified") {
					t.Fatalf("oversized identity accepted: %q", got.verdict)
				}
			}
		})
	}
}

func TestAggregatorLineLimit(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		for _, newline := range []bool{false, true} {
			t.Run(fmt.Sprintf("stderr=%v/newline=%v", stderr, newline), func(t *testing.T) {
				a := newContainerAggregator(nil, nil)
				w := a.stdoutWriter()
				if stderr {
					w = a.stderrWriter()
				}
				chunk := bytes.Repeat([]byte("x"), aggregatorLineLimit)
				_, _ = w.Write(chunk)
				if a.record.stop != stopNone {
					t.Fatal("line boundary refused")
				}
				last := []byte("x")
				if newline {
					last = append(last, '\n')
				}
				_, _ = w.Write(last)
				if a.record.capName != "line length" {
					t.Fatalf("stop %q/%q", a.record.stop, a.record.capName)
				}
			})
		}
	}
}

func TestAggregatorCountLimits(t *testing.T) {
	for _, kind := range []string{"test count", "package count"} {
		t.Run(kind, func(t *testing.T) {
			a := newContainerAggregator(nil, nil)
			limit := aggregatorPackageLimit
			if kind == "test count" {
				limit = aggregatorTestLimit
				aggregatorWrite(a, containerJSON("p", "start"))
			}
			for i := 0; i < limit; i++ {
				if kind == "test count" {
					name := fmt.Sprintf("Test%d", i)
					aggregatorWrite(a, containerJSON("p", "run/"+name, "pass/"+name))
				} else {
					aggregatorWrite(a, containerJSON(fmt.Sprintf("p%d", i), "start", "pass"))
				}
			}
			if a.record.stop != stopNone {
				t.Fatalf("boundary refused: %s/%s", a.record.stop, a.record.capName)
			}
			if kind == "test count" {
				aggregatorWrite(a, containerJSON("p", "run/TestExtra"))
			} else {
				aggregatorWrite(a, containerJSON("extra", "start"))
			}
			if a.record.capName != kind || !aggregatorFinish(a).isError {
				t.Fatalf("cap did not refuse: %q/%q", a.record.stop, a.record.capName)
			}
		})
	}
}

func TestAggregatorMemoryLimit(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start"))
	noise := strings.Repeat("x", aggregatorTestOutputLimit)
	for i := 0; i <= aggregatorMemoryLimit/aggregatorTestOutputLimit && a.record.stop == stopNone; i++ {
		name := fmt.Sprintf("Test%d", i)
		aggregatorWrite(a, containerJSON("p", "run/"+name)+testOutput("p", name, noise))
	}
	if a.record.capName != "retained memory" || a.record.accounted > aggregatorMemoryLimit || !aggregatorFinish(a).isError {
		t.Fatalf("memory not bounded: %s/%s, %d", a.record.stop, a.record.capName, a.record.accounted)
	}
}

// Floods are generated from a reusable bounded chunk; the test never holds the
// complete stream. Their size follows the cap, including the beyond-cap tail.
func aggregatorFlood(a *containerAggregator, bytes int, stderr bool) {
	w := a.stdoutWriter()
	if stderr {
		w = a.stderrWriter()
	}
	chunk := []byte(testOutput("p", "TestNoise", strings.Repeat("x", aggregatorTestOutputLimit/2)))
	if stderr {
		chunk = []byte(strings.Repeat("s", aggregatorStderrLimit/16) + "\n")
	}
	for bytes > 0 {
		size := min(bytes, len(chunk))
		_, _ = w.Write(chunk[:size])
		bytes -= size
	}
}

func TestAggregatorStreamLimit(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		t.Run(fmt.Sprintf("stderr=%v", stderr), func(t *testing.T) {
			a := aggregatorFixture(containerJSON("p", "start", "run/TestNoise"))
			aggregatorFlood(a, aggregatorStreamLimit-a.record.streamed, stderr)
			if a.record.stop != stopNone {
				t.Fatalf("stream boundary refused: %s/%s", a.record.stop, a.record.capName)
			}
			_, _ = a.stderrWriter().Write([]byte("x"))
			if a.record.capName != "total output" || aggregatorFirst(aggregatorFinish(a)) != "NOT VERIFIED: output limit exceeded (output cut off)" {
				t.Fatalf("stream cap not enforced: %s/%s", a.record.stop, a.record.capName)
			}
		})
	}
}

func TestAggregatorCrossingLine(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start", "run/TestNoise"))
	// A crossing line is never a confirmed result, even when its newline is
	// delivered in the same Write as the last available bytes.
	a.record.streamed = aggregatorStreamLimit - len(containerJSON("p", "fail/TestNoise")) + 1
	aggregatorWrite(a, containerJSON("p", "fail/TestNoise"))
	if a.record.confirmedFailures != 0 || a.record.capName != "total output" {
		t.Fatal("crossing line confirmed a failure")
	}
}

func TestAggregatorExitAndTimeout(t *testing.T) {
	for _, code := range []int{0, 1, 2, 137} {
		a := aggregatorFixture(containerEvents("p", "TestOne", "pass"))
		got := a.finish(code, nil, aggregatorRunState{completed: true})
		want := "FAILED"
		if code == 0 {
			want = "verified"
		}
		if aggregatorFirst(got) != want {
			t.Fatalf("exit %d: %q", code, got.verdict)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := newContainerAggregator(cancel, nil)
	aggregatorWrite(a, "invalid\n")
	if ctx.Err() != context.Canceled || a.record.stop != stopMalformed {
		t.Fatal("stop failed to cancel")
	}
	a.stop(stopCap, "total output")
	if a.record.stop != stopMalformed || a.record.capName != "" || a.record.afterEvent != 0 {
		t.Fatal("first reason overwritten")
	}
	a = aggregatorFixture(containerEvents("p", "TestOne", "pass"))
	got := a.finish(-1, context.DeadlineExceeded, aggregatorRunState{deadline: true, interrupted: true})
	if aggregatorFirst(got) != containerTimeLimitVerdict || !a.record.stderrNotServable || a.record.stop != stopDeadline {
		t.Fatalf("timeout: %q", got.verdict)
	}
}

func TestAggregatorLargeStreams(t *testing.T) {
	t.Run("passing beyond old cap", func(t *testing.T) {
		a := aggregatorFixture(containerJSON("p", "start", "run/TestNoise"))
		line := testOutput("p", "TestNoise", strings.Repeat("x", aggregatorTestOutputLimit/2))
		for n := 0; n <= containerOutputLimit; n += len(line) {
			aggregatorWrite(a, line)
		}
		aggregatorWrite(a, containerJSON("p", "pass/TestNoise", "pass"))
		if got := aggregatorFinish(a); aggregatorFirst(got) != "verified" || a.packages["p"].tests["TestNoise"].output.total != 0 || a.record.stdout.total != 0 {
			t.Fatalf("noisy pass: %q", got.verdict)
		}
	})
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure before flood=%v", before), func(t *testing.T) {
			a := aggregatorFixture(containerJSON("p", "start", "run/TestNoise"))
			failure := containerJSON("p", "run/TestBad", "fail/TestBad")
			if before {
				line := testOutput("p", "TestNoise", strings.Repeat("x", aggregatorTestOutputLimit/2))
				for n := 0; n < containerOutputLimit; n += len(line) {
					aggregatorWrite(a, line)
				}
				aggregatorWrite(a, failure)
			}
			// At the chosen cap this is fifty MiB of attempted output.
			aggregatorFlood(a, aggregatorStreamLimit+aggregatorStreamLimit/2+aggregatorStreamLimit/16, false)
			if !before {
				aggregatorWrite(a, failure)
			}
			got := aggregatorFinish(a)
			if before && (!strings.HasPrefix(got.verdict, "FAILED: output limit") || !strings.Contains(got.body, "p/TestBad") || !strings.Contains(got.body, "observed, total unknown")) {
				t.Fatal("earlier failure lost")
			}
			if !before && (a.record.confirmedFailures != 0 || !strings.HasPrefix(got.verdict, "NOT VERIFIED:")) {
				t.Fatal("post-cap failure confirmed")
			}
		})
	}
}

func TestAggregatorEventFlood(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start"))
	line := []byte(testOutput("p", "", strings.Repeat("x", aggregatorStdoutLimit/16)))
	chunk := bytes.Repeat(line, aggregatorLineLimit/len(line))
	for i := 0; i <= aggregatorStreamLimit/len(chunk); i++ {
		_, _ = a.stdoutWriter().Write(chunk)
	}
	if a.record.capName != "total output" || !aggregatorFinish(a).isError {
		t.Fatal("event flood escaped cap")
	}
}

func TestAggregatorFieldLimit(t *testing.T) {
	for _, count := range []int{summaryFieldLimit, summaryFieldLimit + 1} {
		var line strings.Builder
		line.WriteString(`{"Action":"output","Package":"p"`)
		for i := 0; i < count-2; i++ {
			fmt.Fprintf(&line, `,"extra%d":0`, i)
		}
		line.WriteString("}\n")
		a := aggregatorFixture(containerJSON("p", "start") + line.String())
		got := aggregatorFinish(a)
		if count == summaryFieldLimit && a.record.capName != "" {
			t.Fatalf("field boundary refused: %s", a.record.capName)
		}
		if count > summaryFieldLimit && (a.record.capName != "event fields" || !got.isError) {
			t.Fatal("wide object bypassed bounded key checking")
		}
	}
}

func TestAggregatorConcurrentStreams(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start", "run/TestA", "pause/TestA", "run/TestB"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < summaryFieldLimit; i++ {
			_, _ = a.stderrWriter().Write([]byte("diagnostic\n"))
		}
	}()
	for _, event := range []string{"pass/TestB", "cont/TestA", "pass/TestA", "pass"} {
		aggregatorWrite(a, containerJSON("p", event))
	}
	<-done
	if got := aggregatorFinish(a); got.verdict != "verified\n2 passing leaf tests" || a.record.stderr.total != summaryFieldLimit*len("diagnostic\n") {
		t.Fatalf("concurrent evidence: %q", got.verdict)
	}
}

func TestAggregatorForgedEvents(t *testing.T) {
	for _, tc := range []struct{ name, stream, first string }{
		{"breaks structure", containerJSON("p", "pass/TestForged"), "FAILED: event outside a running package"},
		{"satisfies structure", containerEvents("p", "TestForged", "pass"), "verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregatorFinish(aggregatorFixture(tc.stream)); aggregatorFirst(got) != tc.first {
				t.Fatalf("%q", got.verdict)
			}
		})
	}
}

func TestAggregatorMalformedBeforeCut(t *testing.T) {
	a := aggregatorFixture("garbage\n{")
	got := aggregatorFinish(a)
	if aggregatorFirst(got) != "FAILED: malformed test stream" || a.record.stop != stopMalformed || oracleVerdict(0, nil, []byte("garbage\n{"), false, nil) != "FAILED: empty or truncated test stream" {
		t.Fatal("N1 malformed-line precedence changed")
	}
}

func TestAggregatorFirstStopEvent(t *testing.T) {
	a := aggregatorFixture(containerJSON("p", "start", "run/TestBad", "fail/TestBad") + "garbage\n")
	aggregatorWrite(a, containerEvents("q", "TestLater", "fail"))
	if a.record.events != 3 || a.record.afterEvent != 3 || a.record.stop != stopMalformed || a.record.confirmedFailures != 1 {
		t.Fatal("first stop boundary changed")
	}
}
