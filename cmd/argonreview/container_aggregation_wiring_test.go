package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContainerCheckClassification(t *testing.T) {
	for _, check := range append(verifyCheckNames(), "test-browser") {
		test2json, ok := containerCheckKind(check)
		if !ok {
			t.Fatalf("unclassified approved check %q", check)
		}
		want := strings.HasPrefix(check, "test")
		if test2json != want {
			t.Fatalf("%s: test2json=%v, want %v", check, test2json, want)
		}
	}
	if _, ok := containerCheckKind("future-check"); ok {
		t.Fatal("unknown check classified")
	}
}

// Every N1 row runs through the real copy/run/cleanup/finalisation path. Caps
// other than total/line are primed at their boundary; their allocation shapes
// have independent hard-cap tests in PR 1.
func TestContainerAggregationN1(t *testing.T) {
	const capped = "NOT VERIFIED: output limit exceeded (output cut off)"
	const failedCap = "FAILED: output limit exceeded after a confirmed failure (output cut off)"
	pass := containerEvents("p", "TestOne", "pass")
	fail := containerJSON("p", "start", "run/TestBad", "fail/TestBad")
	type row struct {
		name, stream, cap, timing, first string
		code                             int
		runErr                           error
	}
	rows := []row{
		{name: "A1", stream: fail, cap: "total output", first: failedCap},
		{name: "A3 crossing", cap: "crossing", first: capped},
		{name: "A3 partial", stream: containerJSON("p", "start", "run/TestBad"), cap: "partial", first: capped},
		{name: "A4 package", stream: containerJSON("p", "start", "fail"), cap: "total output", first: failedCap},
		{name: "A4 build", stream: `{"Action":"build-fail","ImportPath":"p"}` + "\n", cap: "total output", first: failedCap},
		{name: "A5", stream: containerJSON("p", "start") + testOutput("p", "", "panic: marker\n"), cap: "total output", first: capped},
		{name: "B1", cap: "total output", first: capped},
		{name: "B3", stream: containerJSON("p", "start", "run/TestSkip", "skip/TestSkip"), cap: "total output", first: capped},
		{name: "D1", stream: pass, timing: "late deadline", first: containerTimeLimitVerdict},
		{name: "D2", stream: fail + containerJSON("p", "fail"), timing: "late deadline", first: "FAILED"},
		{name: "D3 skips", stream: containerEvents("p", "TestSkip", "skip"), timing: "late deadline", first: "NOT VERIFIED: 1 skipped"},
		{name: "D3 no tests", stream: containerJSON("p", "start", "pass"), timing: "late deadline", first: "NOT VERIFIED: a targeted package has no tests, or no leaf test passed"},
		{name: "D4", stream: pass, code: 125, timing: "late deadline", first: "FAILED"},
		{name: "D5", stream: pass, timing: "run deadline", first: containerTimeLimitVerdict},
		{name: "D5 failure", stream: fail, timing: "run deadline", first: containerTimeLimitVerdict},
		{name: "E1", stream: fail, code: 137, first: "FAILED"},
		{name: "E2", stream: containerJSON("p", "start", "run/TestOne"), code: 137, first: "FAILED"},
		{name: "E3", stream: fail + containerJSON("p", "fail") + "{", first: "FAILED: empty or truncated test stream"},
		{name: "E4", stream: fail, first: "FAILED"},
		{name: "E5", stream: fail + "garbage\n{", first: "FAILED: malformed test stream"},
		{name: "E6 parent", stream: pass, timing: "parent cancel", first: "FAILED"},
		{name: "E6 own cancel", stream: fail, cap: "total output", first: failedCap},
		{name: "E7 runner", stream: pass, runErr: errors.New("runner failed"), first: "FAILED"},
		{name: "E7 125", stream: pass, code: 125, first: "FAILED"},
		{name: "F pass", stream: pass, first: "verified"},
		{name: "F fail", stream: fail + containerJSON("p", "fail"), first: "FAILED"},
	}
	for _, capName := range []string{"line length", "test count", "package count", "retained memory", "identity length", "event fields"} {
		rows = append(rows, row{name: "A2 " + capName, stream: fail, cap: capName, first: failedCap}, row{name: "B2 " + capName, cap: capName, first: capped})
	}
	for _, timing := range []string{"cap then deadline", "deadline then cap"} {
		rows = append(rows, row{name: "C1 " + timing, cap: "total output", timing: timing, first: capped}, row{name: "C2 " + timing, stream: fail, cap: "total output", timing: timing, first: capped})
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if strings.Contains(row.timing, "deadline") {
				t.Parallel()
			}
			tree, copies := containerFixture(t), t.TempDir()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			execution := containerExecution{timeout: containerTimeout}
			if strings.Contains(row.timing, "deadline") {
				execution.timeout = 2 * time.Second
			}
			if strings.Contains(row.timing, "then") {
				var expire context.CancelFunc
				parent, expire = context.WithTimeout(parent, 2*time.Second)
				defer expire()
				execution.timeout = containerTimeout
			}
			finished, cleaned := 0, false
			execution.finishAggregation = func(a *containerAggregator, code int, err error, state aggregatorRunState) aggregatorResult {
				finished++
				if !cleaned {
					t.Error("finish preceded container cleanup")
				}
				assertNoContainerCopy(t, copies)
				got := a.finish(code, err, state)
				actual, _, _ := strings.Cut(got.verdict, "\n")
				if actual != row.first {
					t.Errorf("finish verdict %q, want %q", actual, row.first)
				}
				return got
			}
			var active context.Context
			runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, stdout, stderr io.Writer) (int, error) {
				if args[0] == "rm" {
					cleaned = true
				}
				if args[0] == "stop" && row.timing == "late deadline" {
					<-active.Done()
				}
				if args[0] != "run" {
					return 0, nil
				}
				active = ctx
				_, _ = io.WriteString(stderr, "host diagnostic\n")
				_, _ = io.WriteString(stdout, row.stream)
				if row.timing == "deadline then cap" {
					<-ctx.Done()
				}
				wireCap(t, execution.aggregation, stdout, row.cap)
				if row.timing == "run deadline" || row.timing == "cap then deadline" {
					if row.timing == "run deadline" {
						<-ctx.Done()
					} else {
						<-parent.Done()
					}
				}
				if row.timing == "parent cancel" {
					cancel()
					<-ctx.Done()
				}
				return row.code, row.runErr
			}}
			report, _ := runContainerExecution(parent, tree, "test", ".", runner, copies, grantContainerExecution(&execution))
			first, rest, _ := strings.Cut(report, "\n")
			if first != row.first || !strings.HasPrefix(rest, containerCheckScope("test")+"\n") {
				t.Fatalf("report %q, want %q then scope", report, row.first)
			}
			if finished != 1 {
				t.Fatalf("finish calls=%d, want exactly one", finished)
			}
			r := execution.record
			if r == nil || !r.sealed || execution.aggregation != nil || execution.output.stdout.Cap() != 0 || execution.output.stderr.Cap() != 0 {
				t.Fatal("run retained raw/protocol state or unsealed record")
			}
			summaryFirst, _, _ := strings.Cut(execution.summary, "\n")
			_, bad := verifyResult(execution.summary, nil, false, false)
			if summaryFirst != row.first || bad != (row.first != "verified") {
				t.Fatalf("summary verdict/isError: %s", execution.summary)
			}
			if !r.complete && (strings.Contains(execution.summary, "Packages:") || strings.Contains(execution.summary, "Leaf tests:") || strings.Contains(execution.summary, " of ") && !strings.Contains(execution.summary, "Details unavailable: the check did not complete.")) {
				t.Fatalf("incomplete stream supplied totals: %s", execution.summary)
			}
			if !r.complete && r.confirmedFailures > 0 && !strings.Contains(row.timing, "run deadline") && !strings.Contains(execution.summary, "observed, total unknown") {
				t.Fatalf("observed failure lost: %s", execution.summary)
			}
			if row.first == containerTimeLimitVerdict && !strings.HasSuffix(execution.summary, "Details unavailable: the check did not complete.\n"+deadlineOmitted) {
				t.Fatal("fixed timeout body changed")
			}
			if row.code >= 125 && row.code <= 127 || row.runErr != nil || strings.Contains(row.timing, "deadline") || row.timing == "parent cancel" {
				if !r.stderrNotServable {
					t.Fatal("runner stderr is servable")
				}
				if !strings.Contains(report, "host diagnostic") {
					t.Fatal("host lost not-servable stderr")
				}
			}
			if row.name == "E6 own cancel" && (r.stderrNotServable || execution.runState.parentCancelled) {
				t.Fatal("aggregator cancel read as parent cancel")
			}

		})
	}
}

func wireCap(t *testing.T, a *containerAggregator, out io.Writer, capName string) {
	t.Helper()
	switch capName {
	case "":
	case "total output":
		a.record.streamed = aggregatorStreamLimit
		_, _ = io.WriteString(out, "x")
	case "line length":
		_, _ = io.WriteString(out, strings.Repeat("x", aggregatorLineLimit+1))
	case "test count":
		a.tests = aggregatorTestLimit
		_, _ = io.WriteString(out, containerJSON("q", "start", "run/TestExtra"))
	case "package count":
		for i := len(a.packages); i < aggregatorPackageLimit; i++ {
			_, _ = io.WriteString(out, containerJSON(fmt.Sprintf("q%d", i), "start"))
		}
		_, _ = io.WriteString(out, containerJSON("extra", "start"))
	case "retained memory":
		a.record.accounted = aggregatorMemoryLimit
		_, _ = io.WriteString(out, containerJSON("q", "start"))
	case "identity length":
		_, _ = io.WriteString(out, containerJSON(strings.Repeat("q", summaryIdentityLimit+1), "start"))
	case "event fields":
		var line strings.Builder
		line.WriteByte('{')
		for i := 0; i < summaryFieldLimit; i++ {
			fmt.Fprintf(&line, "\"field%d\":0,", i)
		}
		line.WriteString(`"Action":"start"}` + "\n")
		_, _ = io.WriteString(out, line.String())
	case "crossing":
		prefix := containerJSON("p", "start", "run/TestBad")
		_, _ = io.WriteString(out, prefix)
		line := containerJSON("p", "fail/TestBad")
		a.record.streamed = aggregatorStreamLimit - len(line) + 1
		_, _ = io.WriteString(out, line)
	case "partial":
		// This fail is partial and cannot add to confirmedFailures.
		_, _ = io.WriteString(out, `{"Action":"fail"`)
		a.record.streamed = aggregatorStreamLimit
		_, _ = io.WriteString(out, "x")
	default:
		t.Fatalf("unknown cap %q", capName)
	}
}

func TestContainerAggregationFlood(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			execution := containerExecution{}
			writes := 0
			block := testOutput("p", "TestNoise", strings.Repeat("x", aggregatorTestOutputLimit))
			runner := fakeContainerRunner{func(ctx context.Context, args, _ []string, out, _ io.Writer) (int, error) {
				if args[0] != "run" {
					return 0, nil
				}
				if failed {
					_, _ = io.WriteString(out, containerEvents("bad", "TestBad", "fail"))
				}
				_, _ = io.WriteString(out, containerJSON("p", "start", "run/TestNoise"))
				// Model a 2 GiB printer without allocating or retaining a 2 GiB fixture.
				for sent := 0; sent < aggregatorStreamLimit*64; sent += len(block) {
					_, _ = io.WriteString(out, block)
					writes++
					if ctx.Err() != nil {
						break
					}
				}
				return 137, nil
			}}
			start := time.Now()
			copies := t.TempDir()
			report, _ := runContainerExecution(context.Background(), containerFixture(t), "test", ".", runner, copies, grantContainerExecution(&execution))
			want := "NOT VERIFIED: output limit exceeded (output cut off)"
			if failed {
				want = "FAILED: output limit exceeded after a confirmed failure (output cut off)"
			}
			if !strings.HasPrefix(report, want+"\n") || execution.record.streamed != aggregatorStreamLimit || writes > aggregatorStreamLimit/len(block)+1 || time.Since(start) > containerCleanupTimeout {
				t.Fatalf("flood did not stop promptly: writes=%d report=%s", writes, report)
			}
			if execution.output.stdout.Cap() != 0 || execution.aggregation != nil || execution.record.stdout.total != 0 {
				t.Fatal("raw flood retained")
			}
			assertNoContainerCopy(t, copies)
		})
	}
}

func TestContainerAggregationLargePass(t *testing.T) {
	for _, check := range []string{"test", "test-db"} {
		t.Run(check, func(t *testing.T) {
			block := testOutput("p", "TestNoise", strings.Repeat("x", aggregatorTestOutputLimit))
			var execution containerExecution
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, _ io.Writer) (int, error) {
				if args[0] != "run" {
					return 0, nil
				}
				_, _ = io.WriteString(out, containerJSON("p", "start", "run/TestNoise"))
				for n := 0; n <= containerOutputLimit; n += len(block) {
					_, _ = io.WriteString(out, block)
				}
				if execution.output.stdout.Cap() != 0 || execution.output.stderr.Cap() != 0 {
					t.Error("raw stream retained during execution")
				}
				_, _ = io.WriteString(out, containerJSON("p", "pass/TestNoise", "pass"))
				return 0, nil
			}}
			report, err := runContainerExecution(context.Background(), containerFixture(t), check, ".", runner, t.TempDir(), grantContainerExecution(&execution))
			if err != nil || !strings.HasPrefix(report, "verified\n") || execution.record.streamed <= containerOutputLimit || !execution.record.complete {
				t.Fatalf("large pass: %s %v", report, err)
			}
			if strings.Contains(report, strings.Repeat("x", summaryLineLimit)) || execution.record.stdout.total != 0 || execution.output.stdout.Cap() != 0 {
				t.Fatal("passing output retained")
			}
			facts := recordSummaryFacts(execution.record)
			if facts.leaves != (summaryCounts{passed: 1}) || !strings.Contains(report, "Leaf tests: 1 passed, 0 failed, 0 skipped") || !strings.Contains(execution.summary, "Leaf tests: 1 passed, 0 failed, 0 skipped") {
				t.Fatal("host/MCP do not read equal record facts")
			}
		})
	}
}

func TestContainerAggregationSourceCleanupPrecedence(t *testing.T) {
	var execution containerExecution
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, _ io.Writer) (int, error) {
		if args[0] == "run" {
			_, _ = io.WriteString(out, strings.Repeat("x", aggregatorLineLimit+1))
			for _, arg := range args {
				if strings.HasSuffix(arg, ",dst=/work,readonly") {
					dir := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/work,readonly")
					if err := os.Chmod(dir, 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = os.RemoveAll(filepath.Dir(dir)) })
				}
			}
		}
		return 0, nil
	}}
	report, err := runContainerExecution(context.Background(), containerFixture(t), "test", ".", runner, t.TempDir(), grantContainerExecution(&execution))
	if err == nil || !strings.HasPrefix(report, "FAILED: source cleanup failed\n") || !strings.HasPrefix(execution.summary, "FAILED: source cleanup failed\n") || execution.record.capName != "line length" {
		t.Fatalf("source cleanup must precede cap: %s %v", report, err)
	}
}

func TestContainerCommandRecordHead(t *testing.T) {
	for _, check := range []string{"build", "vet", "generated"} {
		t.Run(check, func(t *testing.T) {
			output := strings.Repeat("x", aggregatorStdoutLimit+1)
			var execution containerExecution
			_, _ = runContainerExecution(context.Background(), containerFixture(t), check, "./...", summaryRunner(output, "stderr", 1), t.TempDir(), grantContainerExecution(&execution))
			if execution.record == nil || len(execution.record.stdout.head) != aggregatorStdoutLimit || execution.record.stdout.total != len(output) {
				t.Fatal("command record head missing")
			}
			if check != "generated" && execution.output.stdout.Cap() != 0 {
				t.Fatal("old command buffer still retained")
			}
			if check == "generated" && execution.output.stdout.String() != output {
				t.Fatal("generated whole-output behavior changed")
			}
		})
	}
}

func TestContainerAggregationWholeHostRecord(t *testing.T) {
	var execution containerExecution
	runner := fakeContainerRunner{func(_ context.Context, args, _ []string, out, stderr io.Writer) (int, error) {
		if args[0] != "run" {
			return 0, nil
		}
		_, _ = io.WriteString(out, containerJSON("p", "start"))
		// Fill the shared output pool, keeping both per-test and pool omissions.
		for i := 0; i <= aggregatorOutputLimit/aggregatorTestOutputLimit; i++ {
			name := fmt.Sprintf("TestBad%d", i)
			_, _ = io.WriteString(out, containerJSON("p", "run/"+name))
			_, _ = io.WriteString(out, testOutput("p", name, name+"\n"+strings.Repeat("x", aggregatorTestOutputLimit+1)+"\ntail "+name+"\nverified\n"))
			_, _ = io.WriteString(out, containerJSON("p", "fail/"+name))
		}
		for i := 0; i <= aggregatorIndexLimit; i++ {
			name := fmt.Sprintf("TestSkip%d", i)
			_, _ = io.WriteString(out, containerJSON("p", "run/"+name, "skip/"+name))
		}
		_, _ = io.WriteString(out, containerJSON("p", "fail"))
		for n := 0; n <= aggregatorStderrLimit; n += len("stderr\n") {
			_, _ = io.WriteString(stderr, "stderr\n")
		}
		return 1, nil
	}}
	report, err := runContainerExecution(context.Background(), containerFixture(t), "test", ".", runner, t.TempDir(), grantContainerExecution(&execution))
	if err != nil || execution.record == nil || !execution.record.complete {
		t.Fatalf("host record run: %v", err)
	}
	r := execution.record
	if !strings.Contains(report, "not retained\n") || !strings.Contains(report, "skip names not retained\n") || !strings.Contains(report, "truncated: ") || !strings.Contains(report, "Record: complete;") {
		t.Fatal("retention omissions missing")
	}
	for _, e := range r.entries {
		if !strings.Contains(report, aggregatorText(e.pkg)+"/"+aggregatorText(e.name)+" ("+e.kind+", "+e.state+")") {
			t.Fatal("host omitted retained identity")
		}
		for _, part := range [][]byte{e.output.head, e.output.tail} {
			for line := range strings.SplitSeq(string(part), "\n") {
				if line != "" && !strings.Contains(report, "  | "+aggregatorText(line)+"\n") {
					t.Fatal("host shortened retained output")
				}
			}
		}
	}
	for _, part := range [][]byte{r.stderr.head, r.stderr.tail} {
		for line := range strings.SplitSeq(string(part), "\n") {
			if line != "" && !strings.Contains(report, "  | "+aggregatorText(line)+"\n") {
				t.Fatal("host omitted retained stderr")
			}
		}
	}
	if strings.Contains(report, "\nverified\n") || execution.output.stdout.Cap() != 0 {
		t.Fatal("untrusted grammar or raw stream escaped")
	}
	f := recordSummaryFacts(r)
	if f.packages != r.packages || f.leaves != r.leaves || f.failedTotal != r.failedLeaves || f.skippedTotal != r.disallowedSkips {
		t.Fatal("summary differs from shared record facts")
	}
}
