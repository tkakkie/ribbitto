package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func testOutput(pkg, test, output string) string {
	data, _ := json.Marshal(containerTestEvent{Action: "output", Package: pkg, Test: test, Output: output})
	return string(data) + "\n"
}

func measurementStream(entry measurementSkip, output string, sibling bool) string {
	data := containerJSON(entry.pkg, "start", "run/"+entry.test) + testOutput(entry.pkg, entry.test, output) + containerJSON(entry.pkg, "skip/"+entry.test)
	if sibling {
		data += containerJSON(entry.pkg, "run/TestSibling", "pass/TestSibling")
	}
	return data + containerJSON(entry.pkg, "pass")
}

func TestMeasurementSkipVerdicts(t *testing.T) {
	for _, entry := range measurementSkips() {
		t.Run(entry.pkg+"/"+entry.test, func(t *testing.T) {
			data := []byte(measurementStream(entry, "    file.go:27: "+entry.message+"\n", true))
			want := "verified\n1 passing leaf tests\nallowed measurement skips: " + entry.pkg + "/" + entry.test
			if got := testVerdictWithAllowlist(0, nil, data, false, measurementSkips()); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			if got := testVerdict(0, nil, data, false); !strings.HasPrefix(got, "NOT VERIFIED: 1 skipped\n"+entry.pkg+"/"+entry.test) {
				t.Fatalf("ordinary test accepted measurement skip: %q", got)
			}
		})
	}
}

func TestMeasurementSkipRefusals(t *testing.T) {
	e := measurementSkip{"example/p", "TestCost", "set RIBBITTO_COST=1"}
	allowed := []measurementSkip{e}
	pass := containerJSON(e.pkg, "run/TestSibling", "pass/TestSibling")
	for _, tc := range []struct {
		name, data, want string
		code             int
	}{
		{"different message", measurementStream(e, "set RIBBITTO_OTHER=1", true), "NOT VERIFIED: 1 skipped\nexample/p/TestCost", 0},
		{"no output", measurementStream(e, "", true), "NOT VERIFIED", 0},
		{"package output only", containerJSON(e.pkg, "start", "run/"+e.test) + testOutput(e.pkg, "", e.message) + containerJSON(e.pkg, "skip/"+e.test) + pass + containerJSON(e.pkg, "pass"), "NOT VERIFIED", 0},
		{"sibling output only", containerJSON(e.pkg, "start", "run/TestSibling") + testOutput(e.pkg, "TestSibling", e.message) + containerJSON(e.pkg, "pass/TestSibling", "run/"+e.test, "skip/"+e.test, "pass"), "NOT VERIFIED", 0},
		{"other package", measurementStream(measurementSkip{"example/q", e.test, e.message}, e.message, true), "NOT VERIFIED: 1 skipped\nexample/q/TestCost", 0},
		{"only test", measurementStream(e, e.message, false), "NOT VERIFIED", 0},
		{"pass in another package only", measurementStream(e, e.message, false) + containerEvents("example/q", "TestPass", "pass"), "NOT VERIFIED", 0},
		{"has subtests", containerJSON(e.pkg, "start", "run/"+e.test, "run/"+e.test+"/child", "pass/"+e.test+"/child") + testOutput(e.pkg, e.test, e.message) + containerJSON(e.pkg, "skip/"+e.test) + pass + containerJSON(e.pkg, "pass"), "NOT VERIFIED", 0},
		{"package skip", containerJSON(e.pkg, "start", "skip"), "NOT VERIFIED: 1 skipped\nexample/p (package)", 0},
		{"package skip after allowed test", strings.TrimSuffix(measurementStream(e, e.message, true), containerJSON(e.pkg, "pass")) + containerJSON(e.pkg, "skip"), "NOT VERIFIED: 1 skipped\nexample/p (package)", 0},
		{"failure same package", strings.TrimSuffix(measurementStream(e, e.message, true), containerJSON(e.pkg, "pass")) + containerJSON(e.pkg, "run/TestBad", "fail/TestBad", "fail"), "FAILED", 0},
		{"failure other package", measurementStream(e, e.message, true) + containerJSON("example/q", "start", "run/TestBad", "fail/TestBad", "fail"), "FAILED", 0},
		{"failure and unallowed skip", measurementStream(e, "other message", true) + containerJSON("example/q", "start", "fail"), "FAILED", 0},
		{"unfinished sibling", strings.TrimSuffix(measurementStream(e, e.message, true), containerJSON(e.pkg, "pass")) + containerJSON(e.pkg, "run/TestUnfinished", "pass"), "FAILED", 0},
		{"server failed", "", "FAILED", containerTestDBStartFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := testVerdictWithAllowlist(tc.code, nil, []byte(tc.data), false, allowed)
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	// Inject a matching nested name: a name-key mismatch must not be the only
	// thing preventing the allowlist from accepting subtest skips.
	e.test += "/nested"
	if got := testVerdictWithAllowlist(0, nil, []byte(measurementStream(e, e.message, true)), false, []measurementSkip{e}); !strings.HasPrefix(got, "NOT VERIFIED") {
		t.Fatalf("nested allowlist entry accepted: %q", got)
	}
}

func TestMeasurementSkipBrowserRefusal(t *testing.T) {
	e := measurementSkip{"github.com/tkakkie/ribbitto/cmd/ribbitto", "TestBrowser", "browser unavailable without RIBBITTO_REQUIRE_BROWSER"}
	if got := testVerdictWithAllowlist(0, nil, []byte(measurementStream(e, e.message, true)), false, measurementSkips()); !strings.HasPrefix(got, "NOT VERIFIED: 1 skipped\n"+e.pkg+"/"+e.test) {
		t.Fatalf("browser skip accepted: %q", got)
	}
}

func TestMeasurementSkipsSorted(t *testing.T) {
	a, b := measurementSkip{"p", "TestA", "a"}, measurementSkip{"p", "TestB", "b"}
	data := containerJSON("p", "start", "run/TestB") + testOutput("p", "TestB", "b") + containerJSON("p", "skip/TestB", "run/TestA") + testOutput("p", "TestA", "a") + containerJSON("p", "skip/TestA", "run/TestParent", "run/TestParent/child", "pass/TestParent/child", "pass/TestParent", "pass")
	want := "verified\n1 passing leaf tests\nallowed measurement skips: p/TestA, p/TestB"
	if got := testVerdictWithAllowlist(0, nil, []byte(data), false, []measurementSkip{b, a}); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestContainerTestDBEndToEnd(t *testing.T) {
	e := measurementSkips()[0]
	data := measurementStream(e, e.message, true)
	ordinary := measurementSkip{e.pkg, "TestDB", "no database"}
	for _, tc := range []struct {
		check, output, want string
		status              int
	}{
		{"test-db", data, "verified\n1 passing leaf tests\nallowed measurement skips: " + e.pkg + "/" + e.test + "\n", 0},
		{"test", data, "NOT VERIFIED: 1 skipped\n" + e.pkg + "/" + e.test + "\n", 0},
		{"test-db", measurementStream(ordinary, ordinary.message, true), "NOT VERIFIED: 1 skipped\n" + ordinary.pkg + "/TestDB\n", 0},
		{"test-db", data, "FAILED\n", 1},
		{"test-db", "", "FAILED\n", containerTestDBStartFailed},
	} {
		t.Run(tc.check+"/"+tc.want, func(t *testing.T) {
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
				if args[0] == "run" {
					_, _ = io.WriteString(stdout, tc.output)
					return tc.status, nil
				}
				return 0, nil
			}}
			report, err := runContainerCheck(context.Background(), containerFixture(t), tc.check, "./...", runner, t.TempDir(), grantContainerBudget())
			first, detail, _ := strings.Cut(strings.TrimSuffix(tc.want, "\n"), "\n")
			if err != nil || !strings.HasPrefix(report, first+"\n"+containerCheckScope(tc.check)+"\n") {
				t.Fatalf("report %q, error %v", report, err)
			}
			for line := range strings.SplitSeq(detail, "\n") {
				if line != "" && !strings.Contains(report, "  | "+line+"\n") {
					t.Fatalf("record lost verdict detail %q: %s", line, report)
				}
			}
			if strings.Contains(report, `"Action"`) {
				t.Fatal("raw test stream escaped")
			}
		})
	}
}
