package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

const browserTestPackage = "github.com/tkakkie/ribbitto/internal/web"
const browserTestRun = "TestBrowser/quote"

type browserVerdictCase struct {
	name, run, data, want string
	code                  int
	err                   error
	truncated             bool
}

func browserVerdictCases() []browserVerdictCase {
	run, pkg := browserTestRun, browserTestPackage
	pass := containerEvents(pkg, run, "pass")
	absent := "NOT VERIFIED: the named test did not run: " + run
	outside := "NOT VERIFIED: a test outside the named test ran"
	rows := []browserVerdictCase{
		{name: "node itself a leaf", data: pass, want: "verified\nnamed test: " + run + " passed (1 leaf tests)"},
		{name: "absent node", data: containerEvents(pkg, "TestOtherBrowser", "pass"), want: absent},
		{name: "no tests", data: containerJSON(pkg, "start", "pass")},
		{name: "target package missing", data: containerEvents("q", run, "pass"), want: absent},
		{name: "ancestor only", data: containerEvents(pkg, "TestBrowser", "pass"), want: absent},
		{name: "group without own test", data: containerEvents(pkg, run+"/child", "pass"), want: absent},
		{name: "sibling", data: containerJSON(pkg, "start", "run/"+run, "pass/"+run, "run/TestBrowser/sibling", "pass/TestBrowser/sibling", "pass"), want: outside},
		{name: "name suffix", data: containerJSON(pkg, "start", "run/"+run, "pass/"+run, "run/"+run+"-selection", "pass/"+run+"-selection", "pass"), want: outside},
		{name: "other top level", data: containerJSON(pkg, "start", "run/"+run, "pass/"+run, "run/TestOtherBrowser", "pass/TestOtherBrowser", "pass"), want: outside},
		{name: "second package", data: pass + containerEvents("q", run, "pass"), want: outside},
		{name: "ancestors allowed", data: containerJSON(pkg, "start", "run/TestBrowser", "run/"+run, "pass/"+run, "pass/TestBrowser", "pass"), want: "verified\nnamed test: " + run + " passed (1 leaf tests)"},
		{name: "descendants only counted", data: containerJSON(pkg, "start", "run/"+run, "run/"+run+"/child", "pass/"+run+"/child", "pass/"+run, "pass"), want: "verified\nnamed test: " + run + " passed (1 leaf tests)"},
		{name: "parallel leaf", data: containerJSON(pkg, "start", "run/"+run, "run/"+run+"/a", "pause/"+run+"/a", "cont/"+run+"/a", "pass/"+run+"/a", "pass/"+run, "pass"), want: "verified\nnamed test: " + run + " passed (1 leaf tests)"},
		{name: "unfinished paused leaf", data: containerJSON(pkg, "start", "run/"+run, "run/"+run+"/a", "pause/"+run+"/a", "pass/"+run, "pass"), want: "FAILED: missing test result"},
		{name: "top level", run: "TestTopicPostBrowser", data: containerEvents(pkg, "TestTopicPostBrowser", "pass"), want: "verified\nnamed test: TestTopicPostBrowser passed (1 leaf tests)"},
		{name: "cut stream", data: strings.TrimSuffix(pass, "\n")},
		{name: "malformed JSON", data: pass + "{\n"},
		{name: "duplicate JSON key", data: `{"Action":"fail","Action":"pass","Package":"p"}` + "\n"},
		{name: "duplicate run", data: containerJSON(pkg, "start", "run/"+run, "run/"+run, "pass/"+run, "pass")},
		{name: "pass without run", data: containerJSON(pkg, "start", "pass/"+run, "pass")},
		{name: "missing package result", data: containerJSON(pkg, "start", "run/"+run, "pass/"+run)},
		{name: "build failure", data: `{"Action":"build-fail","ImportPath":"p"}` + "\n" + pass},
		{name: "panic", data: containerJSON(pkg, "start", "run/"+run) + fmt.Sprintf("{\"Action\":\"output\",\"Package\":%q,\"Output\":\"panic: oops\\n\"}\n", pkg)},
		{name: "output cap", data: pass, truncated: true},
		{name: "runner error", data: pass, err: errors.New("lost client")},
		{name: "exit 1", data: pass, code: 1}, {name: "exit 2", data: pass, code: 2}, {name: "exit 137", data: pass, code: 137},
		{name: "invalid run", run: "TestBrowser", data: pass, want: "FAILED: invalid browser run"},
	}
	for _, node := range []string{run, run + "/child"} {
		for _, result := range []string{"skip", "fail"} {
			events := []string{"start", "run/" + run}
			if node != run {
				events = append(events, "run/"+node, result+"/"+node, "pass/"+run)
			} else {
				events = append(events, result+"/"+run)
			}
			rows = append(rows, browserVerdictCase{name: result + " " + node, data: containerJSON(pkg, append(events, "pass")...)})
		}
	}
	for _, count := range []int{64, 65} {
		events := []string{"start", "run/" + run}
		for i := range count {
			child := fmt.Sprintf("%s/child%d", run, i)
			events = append(events, "run/"+child, "pass/"+child)
		}
		want := "verified\nnamed test: " + run + " passed (64 leaf tests)"
		if count == 65 {
			want = "NOT VERIFIED: too many tests ran (65, limit 64); name fewer tests"
		}
		rows = append(rows, browserVerdictCase{name: fmt.Sprintf("%d leaves", count), data: containerJSON(pkg, append(events, "pass/"+run, "pass")...), want: want})
	}
	for i := range rows {
		if rows[i].run == "" {
			rows[i].run = run
		}
		if rows[i].want == "" {
			rows[i].want = testVerdictWithAllowlist(rows[i].code, rows[i].err, []byte(rows[i].data), rows[i].truncated, nil)
		}
	}
	return rows
}

func TestBrowserVerdict(t *testing.T) {
	for _, tc := range browserVerdictCases() {
		t.Run(tc.name, func(t *testing.T) {
			if got := browserVerdict(browserTestPackage, tc.run, tc.code, tc.err, []byte(tc.data), tc.truncated); got != tc.want {
				t.Fatalf("verdict = %q, want %q", got, tc.want)
			}
		})
	}
}

func browserVerdictNeverWeaker(t *testing.T, run string, code int, runErr error, data []byte, truncated bool) {
	t.Helper()
	base := testVerdictWithAllowlist(code, runErr, data, truncated, nil)
	got := browserVerdict(browserTestPackage, run, code, runErr, data, truncated)
	if !strings.HasPrefix(base, "verified") && got != base {
		t.Fatalf("base = %q; browser = %q", base, got)
	}
	if !strings.HasPrefix(got, "verified") {
		return
	}
	runs, passes := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		var event struct{ Action, Package, Test string }
		if json.Unmarshal([]byte(line), &event) == nil && event.Package == browserTestPackage && event.Test == run {
			if event.Action == "run" {
				runs++
			}
			if event.Action == "pass" {
				passes++
			}
		}
	}
	if !strings.HasPrefix(base, "verified") || runs != 1 || passes != 1 {
		t.Fatalf("browser verified without base success and named node: base=%q, runs=%d, passes=%d", base, runs, passes)
	}
}

func TestBrowserVerdictNeverWeaker(t *testing.T) {
	for _, tc := range browserVerdictCases() {
		if validateBrowserRun(tc.run) == nil {
			t.Run(tc.name, func(t *testing.T) {
				browserVerdictNeverWeaker(t, tc.run, tc.code, tc.err, []byte(tc.data), tc.truncated)
			})
		}
	}
	rng := rand.New(rand.NewPCG(1908, 1))
	for range 300 {
		events := []string{"start"}
		for range rng.IntN(12) {
			action := []string{"run", "pass", "skip", "fail", "output", "pause", "cont"}[rng.IntN(7)]
			name := []string{"", browserTestRun, browserTestRun + "/child", "TestBrowser/quote-selection"}[rng.IntN(4)]
			events = append(events, action+"/"+name)
		}
		events = append(events, "pass")
		browserVerdictNeverWeaker(t, browserTestRun, 0, nil, []byte(containerJSON(browserTestPackage, events...)), false)
	}
}

func FuzzBrowserVerdictNeverWeaker(f *testing.F) {
	for _, tc := range browserVerdictCases() {
		f.Add([]byte(tc.data), uint8(0), tc.code, tc.truncated)
	}
	f.Fuzz(func(t *testing.T, data []byte, selector uint8, code int, truncated bool) {
		runs := []string{browserTestRun, "TestBrowser/drafts-markers/sidebar-sse", "TestTopicPostBrowser"}
		browserVerdictNeverWeaker(t, runs[int(selector)%len(runs)], code, nil, data, truncated)
	})
}
