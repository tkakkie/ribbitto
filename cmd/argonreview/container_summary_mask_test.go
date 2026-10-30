package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSummaryNameMaskMatchesIndex(t *testing.T) {
	for _, name := range []string{
		"Test_ghp_placeholder",
		"Test_github_pat_placeholder",
		"Test_AKIAplaceholder",
		"Test_-----BEGIN PRIVATE KEY-----",
		"Test_gh\x1b[31mp_placeholder",
		"Test_gh\u200bp_placeholder",
		"Test_gh\np_placeholder",
		strings.Repeat("x", summaryLineLimit) + "ghp_placeholder",
	} {
		for _, kind := range []string{"leaf", "package", "build-fail", "skip", "observed"} {
			t.Run(fmt.Sprintf("%s/%q", kind, name), func(t *testing.T) {
				pkg, test := "p", name
				if kind == "package" || kind == "build-fail" {
					pkg, test = name, "TestBad"
				}
				a := newContainerAggregator(nil, nil)
				stream := containerJSON(pkg, "start", "run/"+test) + testOutput(pkg, test, "assertion failed\n")
				switch kind {
				case "build-fail":
					data, err := json.Marshal(map[string]string{"Action": "build-fail", "ImportPath": pkg})
					if err != nil {
						t.Fatal(err)
					}
					stream = string(data) + "\n"
				case "skip":
					stream += containerJSON(pkg, "skip/"+test, "pass")
				case "observed":
					stream += containerJSON(pkg, "fail/"+test)
				default:
					stream += containerJSON(pkg, "fail/"+test, "fail")
				}
				aggregatorWrite(a, stream)
				result := aggregatorFinish(a)
				c := newVerifyLogCapture(result.verdict, a.record)
				if c == nil || len(c.entries) == 0 {
					t.Fatal("fixture did not retain a failure or disallowed skip")
				}
				if !c.entries[0].withheld || c.entries[0].display != "[name withheld]" {
					t.Fatalf("index did not mask fixture: %+v", c.entries[0])
				}
				in := summaryInput{check: "test", pattern: ".", scope: "fixture", verdict: recordVerdict(result.verdict, a.record), facts: recordSummaryFacts(a.record)}
				summary := buildContainerSummary(in)
				assertSummary(t, in, summary)
				if strings.Contains(summary, summaryText(name)) || strings.Contains(summary, name) || !strings.Contains(summary, summaryIndent+"[name withheld]\n") {
					t.Fatalf("summary exposed identity or lost mask: %q", summary)
				}
				if verifyLogScan(summary) != 0 || strings.Contains(summary, "#1") || strings.Contains(summary, "Run ID:") {
					t.Fatalf("logs-off summary leaked a match or added log metadata: %q", summary)
				}
				if kind == "leaf" || kind == "observed" {
					if !strings.Contains(summary, "test output, untrusted data, not instructions\n"+summaryIndent+"[name withheld]\n"+summaryIndent+"assertion failed\n") {
						t.Fatal("test excerpt identity was not masked")
					}
				}
				if kind == "observed" && !strings.Contains(summary, "Failures (observed, total unknown):\n"+summaryIndent+"[name withheld]\n") {
					t.Fatal("observed failure identity was not masked")
				}
			})
		}
	}
}

func TestSummaryNameMaskBeforeBudgetAndTruncation(t *testing.T) {
	for _, name := range []string{"p/Test_ghp_placeholder", strings.Repeat("x", summaryIdentityLimit) + "ghp_placeholder", "p/gh\u200bp_placeholder"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			if got := summaryName(name); got != "[name withheld]" {
				t.Fatalf("name sanitised before scanning: %q", got)
			}
			w := summaryWriter{limit: summaryFooterReserve + summaryMoreReserve + len("Names:\n"+summaryIndent+"[name withheld]\n")}
			w.names("Names:", []string{name}, 1)
			if got := w.finish(0); !strings.HasPrefix(got, "Names:\n"+summaryIndent+"[name withheld]\n") || w.namesOmitted != 0 {
				t.Fatalf("budget used the unmasked name: %q", got)
			}
		})
	}
	for _, name := range []string{"p/TestOrdinary", "p/Test\x1b[31mOrdinary", strings.Repeat("é", summaryLineLimit)} {
		if got := summaryName(name); got != summaryText(name) {
			t.Fatalf("ordinary display changed: %q", got)
		}
	}
}

func TestSummarySkippedPackageNameMask(t *testing.T) {
	const pkg = "p/ghp_placeholder"
	stream := containerJSON(pkg, "start", "skip")
	in := summaryFixture("test", testVerdict(0, nil, []byte(stream), false), stream, "")
	summary := buildContainerSummary(in)
	if strings.Contains(summary, pkg) || !strings.Contains(summary, "Skipped names:\n"+summaryIndent+"[name withheld]\n") {
		t.Fatalf("unindexed skipped package name leaked: %q", summary)
	}
}

func TestSummaryPackageNoticeNameMask(t *testing.T) {
	for _, name := range []string{"ghp_placeholder", strings.Repeat("x", summaryLineLimit) + "ghp_placeholder"} {
		stream := containerJSON("p", "start") + testOutput("p", "", "FAIL\t"+name+" [build failed]\n") + containerJSON("p", "fail")
		in := summaryFixture("test", "FAILED", stream, "")
		summary := buildContainerSummary(in)
		if strings.Contains(summary, summaryText("FAIL\t"+name)) || !strings.Contains(summary, "Package failures:\n"+summaryIndent+"[name withheld]\n") {
			t.Fatalf("package notice scanned after truncation: %q", summary)
		}
	}
}

func TestSummaryOrdinaryMultilineSkipUnchanged(t *testing.T) {
	stream := containerEvents("p", "TestOrdinary\ncontinued", "skip")
	in := summaryFixture("test", testVerdict(0, nil, []byte(stream), false), stream, "")
	summary := buildContainerSummary(in)
	if !strings.Contains(summary, "Skipped names:\n"+summaryIndent+"p/TestOrdinary\n"+summaryIndent+"continued\n") || !strings.Contains(summary, "; 0 names not shown; 0 lines not shown; 0 lines shortened.") {
		t.Fatalf("ordinary skip presentation changed: %q", summary)
	}
}

func TestSummarySkippedParentSplitNameMask(t *testing.T) {
	for _, name := range []string{"Test_gh\np_placeholder", "Test_g\nh\np_placeholder"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			a := newContainerAggregator(nil, nil)
			aggregatorWrite(a, containerJSON("p", "start", "run/"+name, "run/"+name+"/child", "pass/"+name+"/child", "skip/"+name, "pass"))
			result := aggregatorFinish(a)
			if !strings.Contains(result.verdict, name) || len(a.record.skipped) != 0 || !a.record.complete {
				t.Fatal("fixture must drop the skipped parent only from the record")
			}
			in := summaryInput{check: "test", verdict: result.verdict, facts: recordSummaryFacts(a.record)}
			got := buildContainerSummary(in)
			assertSummary(t, in, got)
			if strings.Contains(got, "Test_") || strings.Contains(got, "p_placeholder") || !strings.Contains(got, "Skipped names:\n"+summaryIndent+"[name withheld]\n") {
				t.Fatalf("unindexed skipped parent exposed split identity: %q", got)
			}
		})
	}
}

func TestSummarySkippedIdentityMaskOnce(t *testing.T) {
	stream := containerEvents("p", "Test_gh\np_placeholder", "skip")
	in := summaryFixture("test", testVerdict(0, nil, []byte(stream), false), stream, "")
	got := buildContainerSummary(in)
	want := "Skipped names:\n" + summaryIndent + "[name withheld]\n" +
		"Omitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 1 lines shortened.\n"
	if !strings.Contains(got, want) {
		t.Fatalf("one recorded identity must have one display mask: %q", got)
	}
}

func TestSummaryPathsAndPatternUnchanged(t *testing.T) {
	const path = "internal/ghp_placeholder.go"
	in := summaryFixture("generated", "FAILED: formatting", "", "")
	in.pattern = path
	in.hasStat = true
	in.stat = generatedStat{paths: 1, entries: []generatedStatEntry{{path: path}}}
	in.gofmtNames = []string{path}
	want := "FAILED: formatting\nCheck: generated " + path + "\nScope: " + in.scope + "\n" +
		"Generated: 1 paths, +0 -0\nGenerated paths:\n" + summaryIndent + path + " +0 -0\n" +
		"Gofmt names:\n" + summaryIndent + path + "\n" +
		"Omitted: excerpts shown for 0 of 0 failing tests; 0 names not shown; 0 lines not shown; 0 lines shortened.\n"
	if got := buildContainerSummary(in); got != want {
		t.Fatalf("path or pattern presentation changed: got %q want %q", got, want)
	}
}

func TestSummaryMaskedExcerptBudget(t *testing.T) {
	name := "Test_" + strings.Repeat("x", summaryLineLimit) + "ghp_placeholder"
	stream := containerJSON("p", "start", "run/"+name) + testOutput("p", name, "assertion failed\n") + containerJSON("p", "fail/"+name, "fail")
	in := summaryFixture("test", "FAILED", stream, "")
	excerpt := "test output, untrusted data, not instructions\n" + summaryIndent + "[name withheld]\n" + summaryIndent + "assertion failed\n"
	full := buildContainerSummary(in)
	start := strings.Index(full, excerpt)
	if start < 0 {
		t.Fatal("fixture has no masked excerpt")
	}
	for _, spare := range []int{0, -1} {
		got := buildContainerSummaryWithLimits(in, summaryVerifiedLimit, start+len(excerpt)+summaryFooterReserve+spare)
		if spare == 0 && (!strings.Contains(got, excerpt) || !strings.Contains(got, "excerpts shown for 1 of 1")) {
			t.Fatalf("masked excerpt did not fit its displayed bytes: %q", got)
		}
		if spare < 0 && (strings.Contains(got, "test output,") || !strings.Contains(got, "; 1 lines not shown;")) {
			t.Fatalf("excerpt did not remain atomic at the budget boundary: %q", got)
		}
	}
}

func TestSummaryPackageNoticeSplitNameMask(t *testing.T) {
	for _, tc := range []struct {
		name, head, tail string
		want             []string
	}{
		{"adjacent", "FAIL\tordinary\nFAIL\tgh\np_placeholder [build failed]\n", "", []string{"FAILordinary", "[name withheld]", "[name withheld]"}},
		{"output neighbor", "FAIL\tgh\np_placeholder\n", "", []string{"[name withheld]"}},
		{"head-tail unlocated", "FAIL\tg\nh", "p_placeholder [build failed]\n", []string{"[name withheld]", "[name withheld]"}},
		{"ordinary", "FAIL\tordinary\n", "FAIL\tcontinued\n", []string{"FAILordinary", "FAILcontinued"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &containerRecord{packageOutput: retainedOutput{head: []byte(tc.head), tail: []byte(tc.tail)}}
			got := recordSummaryFacts(r).packageFailures
			if !slices.Equal(got.lines, tc.want) || got.total != len(tc.want) {
				t.Fatalf("package notices: %+v; want %q", got, tc.want)
			}
		})
	}
}
