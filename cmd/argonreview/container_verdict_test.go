package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Each shorthand is Action[/Test]; start and package result are explicit so
// fixtures can deliberately omit either end of the protocol.
func containerJSON(pkg string, events ...string) string {
	var out strings.Builder
	for _, s := range events {
		action, test, _ := strings.Cut(s, "/")
		data, _ := json.Marshal(containerTestEvent{Action: action, Package: pkg, Test: test})
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.String()
}

func containerEvents(pkg, test, result string) string {
	return containerJSON(pkg, "start", "run/"+test, result+"/"+test, "pass")
}

func TestContainerTestVerdict(t *testing.T) {
	pass := containerEvents("p", "TestOne", "pass")
	for _, tc := range []struct {
		name, data, want string
		code             int
		err              error
		truncated        bool
	}{
		{name: "leaf pass", data: pass, want: "verified\n1 passing leaf tests"},
		{name: "multiple packages", data: pass + containerEvents("q", "TestTwo", "pass"), want: "verified\n2 passing leaf tests"},
		{name: "parent and passing subtest", data: containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "pass/TestParent/child", "pass/TestParent", "pass"), want: "verified\n1 passing leaf tests"},
		{name: "all subtests skipped", data: containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "skip/TestParent/child", "pass/TestParent", "pass"), want: "NOT VERIFIED: 1 skipped\np/TestParent/child"},
		{name: "nested skipped leaves", data: containerJSON("p", "start", "run/TestParent", "run/TestParent/child", "run/TestParent/child/a", "skip/TestParent/child/a", "run/TestParent/child/b", "skip/TestParent/child/b", "pass/TestParent/child", "pass/TestParent", "pass"), want: "NOT VERIFIED: 2 skipped\np/TestParent/child/a\np/TestParent/child/b"},
		{name: "pass and skip", data: containerJSON("p", "start", "run/TestPass", "pass/TestPass", "run/TestDB", "skip/TestDB", "pass"), want: "NOT VERIFIED: 1 skipped\np/TestDB"},
		{name: "failed test", data: containerJSON("p", "start", "run/TestBad", "fail/TestBad", "fail"), want: "FAILED"},
		{name: "failure wins over skip", data: containerJSON("p", "start", "run/TestDB", "skip/TestDB", "run/TestBad", "fail/TestBad", "fail"), want: "FAILED"},
		{name: "package fails without test failure", data: containerJSON("p", "start", "run/TestOne", "pass/TestOne", "fail"), want: "FAILED"},
		{name: "package pass only", data: containerJSON("p", "start", "pass"), want: "NOT VERIFIED"},
		{name: "package skip", data: containerJSON("p", "start", "skip"), want: "NOT VERIFIED: 1 skipped\np (package)"},
		{name: "no test files", data: containerJSON("p", "start") + `{"Action":"output","Package":"p","Output":"? p [no test files]\n"}` + "\n" + containerJSON("p", "skip") + containerEvents("q", "TestOne", "pass"), want: "NOT VERIFIED"},
		{name: "empty package beside pass", data: pass + containerJSON("q", "start", "pass"), want: "NOT VERIFIED"},
		{name: "missing package result", data: pass + containerJSON("q", "start", "run/TestQ", "pass/TestQ"), want: "FAILED"},
		{name: "missing test result", data: containerJSON("p", "start", "run/TestMissing", "pass"), want: "FAILED"},
		{name: "zero test output", data: "", want: "FAILED"},
		{name: "truncated at line boundary", data: containerJSON("p", "start", "run/TestOne", "pass/TestOne"), want: "FAILED"},
		{name: "truncated final newline", data: strings.TrimSuffix(pass, "\n"), want: "FAILED"},
		{name: "truncated JSON", data: pass + `{"Action":"pass"`, want: "FAILED"},
		{name: "malformed newline terminated", data: pass + "garbage\n", want: "FAILED"},
		{name: "malformed JSON", data: pass + "{\n", want: "FAILED"},
		{name: "nonzero exit", data: pass, code: 1, want: "FAILED"},
		{name: "OOM kill", data: pass, code: 137, want: "FAILED"},
		{name: "abnormal exit", data: pass, code: -1, want: "FAILED"},
		{name: "runner error", data: pass, err: errors.New("lost client"), want: "FAILED"},
		{name: "output cap", data: pass, truncated: true, want: "NOT VERIFIED"},
		{name: "build failure old format", data: containerJSON("p", "start") + `{"Action":"output","Package":"p","Output":"FAIL p [build failed]\n"}` + "\n" + containerJSON("p", "fail"), want: "FAILED"},
		{name: "build failure new format", data: `{"Action":"build-output","ImportPath":"p","Output":"undefined: x\n"}` + "\n" + `{"Action":"build-fail","ImportPath":"p"}` + "\n" + pass, want: "FAILED"},
		{name: "empty Test fail", data: pass + containerJSON("q", "start", "fail"), want: "FAILED"},
		{name: "panic", data: containerJSON("p", "start", "run/TestOne") + `{"Action":"output","Package":"p","Test":"TestOne","Output":"panic: oops\n"}` + "\n", want: "FAILED"},
		{name: "timeout", data: containerJSON("p", "start", "run/TestOne") + `{"Action":"output","Package":"p","Output":"panic: test timed out after 10m\n"}` + "\n", want: "FAILED"},
		{name: "parallel tests", data: containerJSON("p", "start", "run/TestOne", "pause/TestOne", "cont/TestOne", "pass/TestOne", "pass"), want: "verified\n1 passing leaf tests"},
		{name: "unknown action", data: pass + containerJSON("q", "mystery"), want: "FAILED"},
		{name: "duplicate JSON key", data: `{"Action":"fail","Action":"pass","Package":"p"}` + "\n", want: "FAILED"},
		{name: "case duplicate key", data: `{"Action":"fail","ACTION":"pass","Package":"p"}` + "\n", want: "FAILED"},
		{name: "result before start", data: containerJSON("p", "pass"), want: "FAILED"},
		{name: "duplicate package result", data: pass + containerJSON("p", "pass"), want: "FAILED"},
		{name: "duplicate test result", data: containerJSON("p", "start", "run/TestOne", "pass/TestOne", "skip/TestOne", "pass"), want: "FAILED"},
		{name: "duplicate test start", data: containerJSON("p", "start", "run/TestOne", "run/TestOne", "pass/TestOne", "pass"), want: "FAILED"},
		{name: "result without running test", data: containerJSON("p", "start", "pass/TestOne", "pass"), want: "FAILED"},
		{name: "missing package", data: `{"Action":"pass","Test":"TestOne"}` + "\n", want: "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := testVerdict(tc.code, tc.err, []byte(tc.data), tc.truncated)
			if strings.HasPrefix(tc.want, "verified") {
				if got != tc.want {
					t.Fatalf("verdict %q, want %q", got, tc.want)
				}
			} else if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("verdict %q, want %q", got, tc.want)
			}
		})
	}
}

func TestContainerCommandVerdict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      int
		err       error
		truncated bool
		want      string
	}{
		{name: "build or vet success", want: "verified"}, {name: "compiler failure", code: 1, want: "FAILED"},
		{name: "OOM", code: 137, want: "FAILED"}, {name: "abnormal", code: -1, want: "FAILED"},
		{name: "runner error", err: errors.New("docker failed"), want: "FAILED"},
		{name: "capped", truncated: true, want: "NOT VERIFIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandVerdict(tc.code, tc.err, tc.truncated); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("verdict %q, want %q", got, tc.want)
			}
		})
	}
}
